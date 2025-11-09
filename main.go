package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"gitlab.cern.ch/gfacundo/landb-alias-controller/controller"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/internal"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider/openstack"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlzap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	// scheme is the Kubernetes scheme.
	scheme = runtime.NewScheme()
	// setupLog is the logger for the setup.
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(networkingv1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
}

// main is the main entry point of the application.
func main() {
	if err := Run(); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// Run is the main logic of the application. It is responsible for parsing flags,
// initializing the manager, and setting up the controller.
func Run() error {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var providerName string
	var ingressNodeLabel string
	var logLevel string

	// --- General Flags ---
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&providerName, "provider", internal.ProviderOpenStack, "The DNS provider to use (e.g., 'openstack').")
	flag.StringVar(&ingressNodeLabel, "ingress-node-label", internal.IngressNodeLabelDefault, "The label to use for selecting ingress nodes.")
	flag.StringVar(&logLevel, "log-level", "info", "The log level to use (e.g., 'debug', 'info', 'warn', 'error').")

	flag.Parse()
	configureLogging(logLevel)

	leaderElectionID, err := randomHex(8)
	if err != nil {
		return err
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                server.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       leaderElectionID,
	})
	if err != nil {
		return err
	}

	if err := addHealthChecks(mgr); err != nil {
		return err
	}

	// --- Initialize Provider ---
	// The provider implementation is expected to read its own configuration
	// from environment variables (e.g., OS_AUTH_URL).
	dnsProvider, err := initProvider(providerName, mgr)
	if err != nil {
		return fmt.Errorf("failed to initialize provider: %w", err)
	}

	// Defer password zeroing if the provider supports it.
	// This is interface-based, so main doesn't need to know *what*
	// provider it is, only that it *might* have this method.
	if z, ok := dnsProvider.(interface{ ZeroPassword() }); ok {
		setupLog.Info("registering provider password zeroing on exit")
		defer z.ZeroPassword()
	}

	// --- Setup Controller ---
	if err := setupController(mgr, dnsProvider, ingressNodeLabel); err != nil {
		return err
	}

	setupLog.Info("starting manager")
	return mgr.Start(ctrl.SetupSignalHandler())
}

// initProvider acts as a factory for creating the specified DNS provider.
func initProvider(providerName string, mgr ctrl.Manager) (provider.Provider, error) {
	switch providerName {
	case internal.ProviderOpenStack:
		setupLog.V(1).Info("using openstack provider")
		// The openstack.NewProvider function will read its configuration
		// directly from environment variables (OS_AUTH_URL, OS_PASSWORD, etc.)
		return openstack.NewProvider(mgr.GetClient())

	// --- Future Providers ---
	// case "route53":
	//    return route53.NewProvider(...)
	// case "cloudflare":
	//    return cloudflare.NewProvider(...)

	default:
		return nil, fmt.Errorf("unsupported provider %q", providerName)
	}
}

// configureLogging configures the logging for the application.
func configureLogging(logLevel string) {
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(logLevel)); err != nil {
		setupLog.Error(err, "invalid log level, defaulting to info", "logLevel", logLevel)
		level = zapcore.InfoLevel
	}

	atomicLevel := zap.NewAtomicLevel()
	atomicLevel.SetLevel(level)

	opts := ctrlzap.Options{
		Development: true,
		Level:       &atomicLevel,
	}
	opts.BindFlags(flag.CommandLine)

	ctrl.SetLogger(ctrlzap.New(ctrlzap.UseFlagOptions(&opts)))
}

// addHealthChecks adds health checks to the manager.
func addHealthChecks(mgr ctrl.Manager) error {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return nil
}

// setupController sets up the controller with the manager.
func setupController(mgr ctrl.Manager, dnsProvider provider.Provider, ingressNodeLabel string) error {
	// A change to *any* Ingress node (e.g., IP change, label added/removed)
	// requires a full reconciliation, as it affects the IP list for *all* aliases.
	nodeHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, node client.Object) []reconcile.Request {
			// Check if it's an ingress node
			labels := node.GetLabels()
			if _, ok := labels[ingressNodeLabel]; !ok {
				// We also check for the "true" value, just in case.
				// The selector in the controller is for label existence.
				if val, ok := labels[ingressNodeLabel]; !ok || val != internal.TrueString {
					return nil // Not an ingress node, ignore.
				}
			}

			// Ingress node changed. We must trigger a full resync.
			// The controller's Reconcile() ignores the request details, so we
			// can just find the *first* Ingress and enqueue a request for it
			// to "poke" the controller.
			c := mgr.GetClient()
			ingressList := &networkingv1.IngressList{}
			if err := c.List(ctx, ingressList, client.Limit(1)); err != nil {
				setupLog.Error(err, "failed to list ingresses for node watch handler")
				return nil
			}

			if len(ingressList.Items) == 0 {
				setupLog.V(1).Info("Node changed, but no ingresses found to trigger reconcile.")
				return nil // No ingresses to trigger
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      ingressList.Items[0].Name,
					Namespace: ingressList.Items[0].Namespace,
				},
			}
			setupLog.V(1).Info("Ingress node changed, enqueuing dummy request for ingress to trigger full reconcile", "node", node.GetName(), "ingress", req.NamespacedName)
			return []reconcile.Request{req}
		},
	)

	return builder.ControllerManagedBy(mgr).
		// Watch for changes to Ingress resources
		For(&networkingv1.Ingress{}).
		// Also watch for changes to Nodes, and trigger Ingress reconciles
		Watches(&corev1.Node{}, nodeHandler).
		Complete(&controller.Controller{
			Client:           mgr.GetClient(),
			DnsProvider:      dnsProvider,
			IngressNodeLabel: ingressNodeLabel,
		})
}

// randomHex generates a random hex string of the given length.
func randomHex(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
