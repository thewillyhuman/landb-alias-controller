// Package main is the entry point of the landb-alias-controller.
//
// The controller watches Kubernetes Ingress and Node resources, extracts
// CERN DNS aliases from Ingress hosts, and synchronizes them as metadata
// on OpenStack servers so that CERN's LANDB system can update DNS records.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"gitlab.cern.ch/gfacundo/landb-alias-controller/controller"
	_ "gitlab.cern.ch/gfacundo/landb-alias-controller/metrics" // Register metrics on init.
	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider/openstack"
)

const (
	// providerOpenStack is the identifier for the OpenStack LANDB provider.
	providerOpenStack = "openstack"
	// defaultIngressNodeLabel is the Kubernetes label used to identify
	// nodes serving ingress traffic.
	defaultIngressNodeLabel = "node-role.kubernetes.io/ingress"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(networkingv1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

// run contains the application logic, separated from main for testability.
func run() error {
	// --- Flags ---
	var (
		providerName     string
		ingressNodeLabel string
	)
	flag.StringVar(&providerName, "provider", providerOpenStack,
		"DNS provider to use (currently only 'openstack').")
	flag.StringVar(&ingressNodeLabel, "ingress-node-label", defaultIngressNodeLabel,
		"Kubernetes label identifying ingress nodes.")

	// controller-runtime's zap flag set (--zap-log-level, --zap-devel, etc.)
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	// --- Logger ---
	logger := zap.New(zap.UseFlagOptions(&zapOpts))
	ctrl.SetLogger(logger)
	log := ctrl.Log.WithName("setup")

	log.Info("Starting landb-alias-controller",
		"provider", providerName,
		"ingressNodeLabel", ingressNodeLabel,
	)

	// --- Controller Manager ---
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	// --- Provider ---
	dnsProvider, err := initProvider(providerName, ctrl.Log)
	if err != nil {
		return fmt.Errorf("initializing provider: %w", err)
	}

	// --- Controller Setup ---
	if err := setupController(mgr, dnsProvider, ingressNodeLabel); err != nil {
		return fmt.Errorf("setting up controller: %w", err)
	}

	log.Info("Starting manager")
	return mgr.Start(ctrl.SetupSignalHandler())
}

// initProvider creates the configured DNS alias provider.
func initProvider(name string, log logr.Logger) (provider.Provider, error) {
	switch name {
	case providerOpenStack:
		return openstack.NewProvider(log)
	default:
		return nil, errors.New(fmt.Sprintf("unknown provider %q; supported: %s", name, providerOpenStack))
	}
}

// nodeReadyStatus extracts the Ready condition status from a Node object.
// Returns an empty string if the object is not a Node or has no Ready condition.
func nodeReadyStatus(obj client.Object) corev1.ConditionStatus {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return ""
	}
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status
		}
	}
	return ""
}

// setupController registers the reconciler and configures watches for
// Ingress and Node resources.
func setupController(mgr ctrl.Manager, prov provider.Provider, ingressNodeLabel string) error {
	reconciler := &controller.Controller{
		Client:           mgr.GetClient(),
		Provider:         prov,
		IngressNodeLabel: ingressNodeLabel,
	}

	// hasIngressLabel checks whether an object carries the ingress node label.
	hasIngressLabel := func(obj client.Object) bool {
		_, ok := obj.GetLabels()[ingressNodeLabel]
		return ok
	}

	// labelPredicate filters Node events to those that have (or had) the
	// ingress node label. For Update events, it checks both the old and
	// new object so that label removal also triggers reconciliation.
	labelPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return hasIngressLabel(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return hasIngressLabel(e.ObjectOld) || hasIngressLabel(e.ObjectNew)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return hasIngressLabel(e.Object)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return hasIngressLabel(e.Object)
		},
	}

	// statusChangedPredicate triggers reconciliation when an ingress-labeled
	// node's Ready condition changes (e.g., Ready → NotReady or vice versa).
	statusChangedPredicate := predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return false },
		DeleteFunc:  func(e event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if !hasIngressLabel(e.ObjectOld) && !hasIngressLabel(e.ObjectNew) {
				return false
			}
			return nodeReadyStatus(e.ObjectOld) != nodeReadyStatus(e.ObjectNew)
		},
	}

	return builder.ControllerManagedBy(mgr).
		// Reconcile on any Ingress change across all namespaces.
		For(&networkingv1.Ingress{}).
		// Reconcile on changes to ingress-labeled Nodes. All node events
		// map to the same reconcile key to avoid thundering herd when
		// multiple nodes change simultaneously.
		Watches(&corev1.Node{},
			handler.EnqueueRequestsFromMapFunc(
				func(_ context.Context, _ client.Object) []reconcile.Request {
					return []reconcile.Request{{
						NamespacedName: types.NamespacedName{Name: "landb-alias-reconcile"},
					}}
				},
			),
			builder.WithPredicates(predicate.Or(labelPredicate, statusChangedPredicate)),
		).
		Complete(reconciler)
}
