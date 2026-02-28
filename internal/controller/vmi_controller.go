package controller

import (
	"context"
	"net"
	"reflect"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kubevirtv1 "kubevirt.io/api/core/v1"

	dnsendpointv1alpha1 "sigs.k8s.io/external-dns/endpoint"
)

const (
	// annotationHostname is the External-DNS annotation for hostnames (comma-separated).
	annotationHostname = "external-dns.alpha.kubernetes.io/hostname"
	// annotationTTL is the External-DNS annotation for record TTL in seconds.
	annotationTTL = "external-dns.alpha.kubernetes.io/ttl"
	// defaultTTL is used when the TTL annotation is absent or invalid.
	defaultTTL = dnsendpointv1alpha1.TTL(300)
	// multusInfoSource is the infoSource value that indicates multus-status IPs.
	multusInfoSource = "multus-status"
	// guestAgentInfoSource is the infoSource value set by the QEMU guest agent.
	// It provides a richer IP list (iface.IPs) including IPv6 global unicast addresses.
	guestAgentInfoSource = "guest-agent"
)

// AddDNSEndpointToScheme registers the DNSEndpoint CRD types with the given scheme.
func AddDNSEndpointToScheme(s *runtime.Scheme) error {
	s.AddKnownTypes(
		schema.GroupVersion{Group: "externaldns.k8s.io", Version: "v1alpha1"},
		&dnsendpointv1alpha1.DNSEndpoint{},
		&dnsendpointv1alpha1.DNSEndpointList{},
	)
	metav1.AddToGroupVersion(s, schema.GroupVersion{Group: "externaldns.k8s.io", Version: "v1alpha1"})
	return nil
}

// VirtualMachineInstanceReconciler reconciles VirtualMachineInstance objects.
type VirtualMachineInstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=get;list;watch
// +kubebuilder:rbac:groups=externaldns.k8s.io,resources=dnsendpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile reads the state of the VirtualMachineInstance and creates/updates/deletes a DNSEndpoint accordingly.
func (r *VirtualMachineInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	vmi := &kubevirtv1.VirtualMachineInstance{}
	if err := r.Get(ctx, req.NamespacedName, vmi); err != nil {
		if apierrors.IsNotFound(err) {
			// VMI was deleted; DNSEndpoint is cleaned up via OwnerReference GC.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// If the hostname annotation is absent, clean up any existing DNSEndpoint.
	hostname, hasAnnotation := vmi.Annotations[annotationHostname]
	hostname = strings.TrimSpace(hostname)
	if !hasAnnotation || hostname == "" {
		logger.Info("hostname annotation absent, ensuring DNSEndpoint is deleted", "vmi", req.NamespacedName)
		return ctrl.Result{}, r.deleteEndpointIfExists(ctx, vmi)
	}

	// Annotation is present — collect the best available IPs.
	// guest-agent IPs are preferred (richer data); multus-status is the fallback.
	// If neither source yields IPs yet, do nothing: neither create nor delete.
	ipv4Addrs, ipv6Addrs, ipSource := extractBestIPs(vmi)
	if len(ipv4Addrs) == 0 && len(ipv6Addrs) == 0 {
		logger.Info("hostname annotation present but no IPs available yet, skipping", "vmi", req.NamespacedName)
		return ctrl.Result{}, nil
	}
	logger.Info("resolved IPs", "vmi", req.NamespacedName, "source", ipSource, "ipv4", ipv4Addrs, "ipv6", ipv6Addrs)

	ttl := parseTTL(vmi.Annotations[annotationTTL])
	hostnames := parseHostnames(hostname)
	endpoints := buildEndpoints(hostnames, ipv4Addrs, ipv6Addrs, ttl)

	desired := &dnsendpointv1alpha1.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmi.Name,
			Namespace: vmi.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, desired, func() error {
		desired.Spec = dnsendpointv1alpha1.DNSEndpointSpec{
			Endpoints: endpoints,
		}
		// Set VMI as the owner so the DNSEndpoint is garbage-collected when the VMI is deleted.
		return controllerutil.SetControllerReference(vmi, desired, r.Scheme)
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("reconciled DNSEndpoint", "vmi", req.NamespacedName, "operation", op)
	return ctrl.Result{}, nil
}

// deleteEndpointIfExists deletes the DNSEndpoint with the same name/namespace as the VMI, if it exists.
func (r *VirtualMachineInstanceReconciler) deleteEndpointIfExists(ctx context.Context, vmi *kubevirtv1.VirtualMachineInstance) error {
	endpoint := &dnsendpointv1alpha1.DNSEndpoint{}
	err := r.Get(ctx, client.ObjectKey{Name: vmi.Name, Namespace: vmi.Namespace}, endpoint)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.Delete(ctx, endpoint)
}

// extractBestIPs returns IPv4 and IPv6 addresses for the VMI using the best
// available infoSource. The guest-agent source is preferred because it exposes
// the full iface.IPs list (including global IPv6 unicast). multus-status is
// used as a fallback, reading only the single iface.IP field.
//
// The returned source string indicates which source was used ("guest-agent" or
// "multus-status").
func extractBestIPs(vmi *kubevirtv1.VirtualMachineInstance) (ipv4, ipv6 []string, source string) {
	gaV4, gaV6 := extractGuestAgentIPs(vmi)
	if len(gaV4) > 0 || len(gaV6) > 0 {
		return gaV4, gaV6, guestAgentInfoSource
	}
	mV4, mV6 := extractMultusIPs(vmi)
	if len(mV4) > 0 || len(mV6) > 0 {
		return mV4, mV6, multusInfoSource
	}
	return nil, nil, ""
}

// extractGuestAgentIPs returns IPv4 and IPv6 addresses from interfaces whose
// infoSource contains "guest-agent", using the full iface.IPs list.
// Link-local IPv6 addresses (fe80::/10) are skipped.
func extractGuestAgentIPs(vmi *kubevirtv1.VirtualMachineInstance) (ipv4, ipv6 []string) {
	for _, iface := range vmi.Status.Interfaces {
		if !containsInfoSource(iface.InfoSource, guestAgentInfoSource) {
			continue
		}
		for _, addr := range iface.IPs {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			ip := net.ParseIP(addr)
			if ip == nil {
				continue
			}
			if ip.To4() != nil {
				ipv4 = append(ipv4, addr)
			} else if ip.To16() != nil && !ip.IsLinkLocalUnicast() {
				ipv6 = append(ipv6, addr)
			}
		}
	}
	return
}

// extractMultusIPs returns IPv4 and IPv6 addresses from interfaces whose
// infoSource contains "multus-status", using the single iface.IP field.
func extractMultusIPs(vmi *kubevirtv1.VirtualMachineInstance) (ipv4, ipv6 []string) {
	for _, iface := range vmi.Status.Interfaces {
		if !containsInfoSource(iface.InfoSource, multusInfoSource) {
			continue
		}
		addr := strings.TrimSpace(iface.IP)
		if addr == "" {
			continue
		}
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			ipv4 = append(ipv4, addr)
		} else if ip.To16() != nil {
			ipv6 = append(ipv6, addr)
		}
	}
	return
}

// containsInfoSource returns true if the comma-separated infoSource field
// contains the given source token (exact match after trimming spaces).
func containsInfoSource(infoSource, source string) bool {
	for _, part := range strings.Split(infoSource, ",") {
		if strings.TrimSpace(part) == source {
			return true
		}
	}
	return false
}

// parseHostnames splits a comma-separated list of hostnames.
func parseHostnames(raw string) []string {
	var result []string
	for _, h := range strings.Split(raw, ",") {
		h = strings.TrimSpace(h)
		if h != "" {
			result = append(result, h)
		}
	}
	return result
}

// parseTTL converts the TTL annotation string to a dnsendpointv1alpha1.TTL value.
// Falls back to defaultTTL if the value is absent or not a valid integer.
func parseTTL(raw string) dnsendpointv1alpha1.TTL {
	if raw == "" {
		return defaultTTL
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || v <= 0 {
		return defaultTTL
	}
	return dnsendpointv1alpha1.TTL(v)
}

// buildEndpoints creates Endpoint entries for each record type that has targets.
func buildEndpoints(hostnames, ipv4, ipv6 []string, ttl dnsendpointv1alpha1.TTL) []*dnsendpointv1alpha1.Endpoint {
	var endpoints []*dnsendpointv1alpha1.Endpoint
	for _, hostname := range hostnames {
		if len(ipv4) > 0 {
			endpoints = append(endpoints, &dnsendpointv1alpha1.Endpoint{
				DNSName:    hostname,
				RecordType: "A",
				Targets:    dnsendpointv1alpha1.Targets(ipv4),
				RecordTTL:  ttl,
			})
		}
		if len(ipv6) > 0 {
			endpoints = append(endpoints, &dnsendpointv1alpha1.Endpoint{
				DNSName:    hostname,
				RecordType: "AAAA",
				Targets:    dnsendpointv1alpha1.Targets(ipv6),
				RecordTTL:  ttl,
			})
		}
	}
	return endpoints
}

// vmiChangedPredicate filters VMI update events to those where either the
// hostname annotation or the status.interfaces list has actually changed.
// The full Interfaces slice comparison covers both iface.IP (multus-status)
// and iface.IPs (guest-agent) fields. Create and delete events always pass through.
var vmiChangedPredicate = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldVMI, ok1 := e.ObjectOld.(*kubevirtv1.VirtualMachineInstance)
		newVMI, ok2 := e.ObjectNew.(*kubevirtv1.VirtualMachineInstance)
		if !ok1 || !ok2 {
			return true
		}
		annotationChanged := oldVMI.Annotations[annotationHostname] != newVMI.Annotations[annotationHostname]
		interfacesChanged := !reflect.DeepEqual(oldVMI.Status.Interfaces, newVMI.Status.Interfaces)
		return annotationChanged || interfacesChanged
	},
	CreateFunc:  func(e event.CreateEvent) bool { return true },
	DeleteFunc:  func(e event.DeleteEvent) bool { return true },
	GenericFunc: func(e event.GenericEvent) bool { return true },
}

// SetupWithManager registers the controller with the manager.
func (r *VirtualMachineInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubevirtv1.VirtualMachineInstance{}, builder.WithPredicates(vmiChangedPredicate)).
		Owns(&dnsendpointv1alpha1.DNSEndpoint{}).
		Complete(r)
}
