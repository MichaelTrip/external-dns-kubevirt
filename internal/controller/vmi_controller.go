package controller

import (
	"context"
	"net"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

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

	// Annotation is present — collect multus-status IPs.
	// If none are available yet, do nothing: neither create nor delete.
	ipv4Addrs, ipv6Addrs := extractMultusIPs(vmi)
	if len(ipv4Addrs) == 0 && len(ipv6Addrs) == 0 {
		logger.Info("hostname annotation present but no multus-status IPs available yet, skipping", "vmi", req.NamespacedName)
		return ctrl.Result{}, nil
	}

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

// extractMultusIPs returns IPv4 and IPv6 addresses from interfaces whose infoSource contains "multus-status".
func extractMultusIPs(vmi *kubevirtv1.VirtualMachineInstance) (ipv4, ipv6 []string) {
	for _, iface := range vmi.Status.Interfaces {
		if !containsMultusSource(iface.InfoSource) {
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

// containsMultusSource returns true if the infoSource field contains "multus-status".
func containsMultusSource(infoSource string) bool {
	for _, part := range strings.Split(infoSource, ",") {
		if strings.TrimSpace(part) == multusInfoSource {
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

// SetupWithManager registers the controller with the manager.
func (r *VirtualMachineInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubevirtv1.VirtualMachineInstance{}).
		Owns(&dnsendpointv1alpha1.DNSEndpoint{}).
		Complete(r)
}
