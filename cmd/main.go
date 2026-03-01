package main

import (
	"flag"
	"fmt"
	"os"

	kubevirtv1 "kubevirt.io/api/core/v1"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/michaeltrip/external-dns-kubevirt/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kubevirtv1.AddToScheme(scheme))
	utilruntime.Must(controller.AddDNSEndpointToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var leaderElect bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election for controller manager.")

	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	restConfig := ctrl.GetConfigOrDie()

	if err := checkRequiredCRDs(restConfig); err != nil {
		setupLog.Error(err, "required CRDs not found — install KubeVirt and External-DNS before starting")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "external-dns-kubevirt-leader",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controller.VirtualMachineInstanceReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "VirtualMachineInstance")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// crdRequirement describes a CRD that must be present before the controller starts.
type crdRequirement struct {
	group    string
	version  string
	resource string
}

// requiredCRDs lists the API resources that must exist in the cluster.
var requiredCRDs = []crdRequirement{
	{group: "kubevirt.io", version: "v1", resource: "virtualmachineinstances"},
	{group: "externaldns.k8s.io", version: "v1alpha1", resource: "dnsendpoints"},
}

// checkRequiredCRDs uses the discovery API to verify that all required CRDs are
// registered in the cluster. It returns an error listing any missing resources.
func checkRequiredCRDs(cfg *rest.Config) error {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create discovery client: %w", err)
	}

	var missing []string
	for _, req := range requiredCRDs {
		groupVersion := req.group + "/" + req.version
		resourceList, err := dc.ServerResourcesForGroupVersion(groupVersion)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s/%s (%s)", groupVersion, req.resource, err))
			continue
		}
		found := false
		for _, r := range resourceList.APIResources {
			if r.Name == req.resource {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, fmt.Sprintf("%s/%s", groupVersion, req.resource))
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required CRDs: %v", missing)
	}
	return nil
}
