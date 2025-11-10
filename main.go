package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/internal/log"
	"os"

	"gitlab.cern.ch/gfacundo/landb-alias-controller/controller"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/internal"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider/openstack"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var (
	// scheme is the Kubernetes scheme.
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme)) // Pods, Services, Deployments, ConfigMaps, Secrets, etc..
	utilruntime.Must(networkingv1.AddToScheme(scheme))   // Ingresses and NetworkPolicies.
	utilruntime.Must(corev1.AddToScheme(scheme))
}

// main is the main entry point of the application.
func main() {
	log.GlobalLogger = log.NewLogger(log.DefaultLogLevel)
	log.GlobalLogger.Info("starting landb alias controller")
	if err := Run(); err != nil {
		log.GlobalLogger.Error("startup failed")
		os.Exit(1)
	}
}

// Run is the main logic of the application. It is responsible for parsing flags,
// initializing the manager, and setting up the controller.
func Run() error {
	var providerName string
	var ingressNodeLabel string
	var logLevel string

	// --- General Flags ---
	flag.StringVar(&providerName, "provider", internal.ProviderOpenStack, "The DNS provider to use (e.g., 'openstack').")
	flag.StringVar(&ingressNodeLabel, "ingress-node-label", internal.IngressNodeLabelDefault, "The label to use for selecting ingress nodes.")
	flag.StringVar(&logLevel, "log-level", "info", "The log level to use (e.g., 'debug', 'info', 'warn', 'error').")
	flag.Parse()

	// --- Set the logger ---
	level, exists := log.LevelFromString(logLevel)
	if !exists {
		log.GlobalLogger.Warn("invalid log level [%s]. Continuing with default log level [%s]", logLevel, log.LevelNames[log.DefaultLogLevel])
		log.GlobalLogger = log.NewLogger(log.DefaultLogLevel)
	} else {
		log.GlobalLogger.Info("setting log level to [%s]", logLevel)
		log.GlobalLogger = log.NewLogger(level)
	}

	// -- Init kubernetes runtime control manager ---
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		log.GlobalLogger.Debug("error %v", err)
		log.GlobalLogger.Error("unable to start kubernetes runtime control manager")
		return err
	}

	// --- Initialize Provider ---
	// The provider implementation is expected to read its own configuration
	// from environment variables (e.g., OS_AUTH_URL).
	dnsProvider, err := initProvider(providerName, mgr)
	if err != nil {
		log.GlobalLogger.Debug("error: %v", err)
		log.GlobalLogger.Error("failed to initialize dns provider [%s]", providerName)
		return errors.New("failed to initialize dns provider")
	}

	// --- Setup Controller ---
	if err := setupController(mgr, dnsProvider, ingressNodeLabel); err != nil {
		log.GlobalLogger.Debug("error: %v", err)
		log.GlobalLogger.Error("failed to setup controller")
		return err
	}

	log.GlobalLogger.Info("starting resources watcher")
	return mgr.Start(ctrl.SetupSignalHandler())
}

// initProvider acts as a factory for creating the specified DNS provider.
func initProvider(providerName string, mgr ctrl.Manager) (provider.Provider, error) {
	switch providerName {
	case internal.ProviderOpenStack:
		log.GlobalLogger.Info("using dns provider: openstack")
		// The openstack.NewProvider function will read its configuration
		// directly from environment variables (OS_AUTH_URL, OS_PASSWORD, etc.)
		return openstack.NewProvider(mgr.GetClient())

	// --- Future Providers ---
	// case "route53":
	//    return route53.NewProvider(...)
	// case "cloudflare":
	//    return cloudflare.NewProvider(...)

	default:
		return nil, errors.New(fmt.Sprintf("provider [%s] did not match any of registered dns providers", providerName))
	}
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
				log.GlobalLogger.Error("failed to list ingresses for node watch handler")
				return nil
			}

			if len(ingressList.Items) == 0 {
				log.GlobalLogger.Info("Node changed, but no ingresses found to trigger reconcile")
				return nil // No ingresses to trigger
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      ingressList.Items[0].Name,
					Namespace: ingressList.Items[0].Namespace,
				},
			}
			log.GlobalLogger.Info("Ingress node changed, enqueuing dummy request for ingress to trigger full reconcile", "node", node.GetName(), "ingress", req.NamespacedName)
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
