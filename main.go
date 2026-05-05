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
	"strings"

	"github.com/go-logr/logr"
	"github.com/gophercloud/gophercloud/v2"
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

	"gitlab.cern.ch/kubernetes/networking/landb-controller/landb-alias-controller/controller"
	_ "gitlab.cern.ch/kubernetes/networking/landb-controller/landb-alias-controller/metrics" // Register metrics on init.
	"gitlab.cern.ch/kubernetes/networking/landb-controller/landb-alias-controller/provider"
	"gitlab.cern.ch/kubernetes/networking/landb-controller/landb-alias-controller/provider/openstack"
)

const (
	// providerOpenStack is the identifier for the OpenStack LANDB provider.
	providerOpenStack = "openstack"
	// defaultIngressNodeLabels are the Kubernetes labels used to identify
	// nodes serving ingress traffic (comma-separated).
	defaultIngressNodeLabels = "node-role.kubernetes.io/ingress,role=ingress"
	// landbMetadataPrefix identifies Kubernetes labels and annotations that
	// should trigger LANDB metadata reconciliation.
	landbMetadataPrefix = "landb.cern.ch/"
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
		providerName      string
		ingressNodeLabels string
		cloudConfigSecret string
	)
	flag.StringVar(&providerName, "provider", providerOpenStack,
		"DNS provider to use (currently only 'openstack').")
	flag.StringVar(&ingressNodeLabels, "ingress-node-labels", defaultIngressNodeLabels,
		"Comma-separated Kubernetes labels identifying ingress nodes (OR logic).")
	flag.StringVar(&cloudConfigSecret, "cloud-config-secret", "",
		"Read OpenStack credentials from a Kubernetes secret (format: namespace/name, e.g. kube-system/cloud-config).")

	// controller-runtime's zap flag set (--zap-log-level, --zap-devel, etc.)
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	// --- Logger ---
	logger := zap.New(zap.UseFlagOptions(&zapOpts))
	ctrl.SetLogger(logger)
	log := ctrl.Log.WithName("setup")

	labels := parseLabels(ingressNodeLabels)

	log.Info(
		"Starting landb-alias-controller",
		"provider", providerName,
		"ingressNodeLabels", labels,
		"cloudConfigSecret", cloudConfigSecret,
	)

	// --- Controller Manager ---
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		return fmt.Errorf("creating manager: %w", err)
	}

	// --- Provider ---
	dnsProvider, err := initProvider(providerName, ctrl.Log, cloudConfigSecret, mgr.GetAPIReader())
	if err != nil {
		return fmt.Errorf("initializing provider: %w", err)
	}

	// --- Controller Setup ---
	if err := setupController(mgr, dnsProvider, labels); err != nil {
		return fmt.Errorf("setting up controller: %w", err)
	}

	log.Info("Starting manager")
	return mgr.Start(ctrl.SetupSignalHandler())
}

// initProvider creates the configured DNS alias provider.
func initProvider(name string, log logr.Logger, cloudConfigSecret string, reader client.Reader) (provider.Provider, error) {
	switch name {
	case providerOpenStack:
		authOpts, err := buildAuthOptions(cloudConfigSecret, reader)
		if err != nil {
			return nil, err
		}
		return openstack.NewProvider(log, authOpts)
	default:
		return nil, errors.New(fmt.Sprintf("unknown provider %q; supported: %s", name, providerOpenStack))
	}
}

// buildAuthOptions constructs OpenStack auth options from either a
// cloud-config Kubernetes secret or environment variables.
func buildAuthOptions(cloudConfigSecret string, reader client.Reader) (gophercloud.AuthOptions, error) {
	if cloudConfigSecret != "" {
		namespace, name, ok := strings.Cut(cloudConfigSecret, "/")
		if !ok {
			return gophercloud.AuthOptions{}, fmt.Errorf(
				"invalid --cloud-config-secret format %q; expected namespace/name", cloudConfigSecret,
			)
		}
		return openstack.ReadCloudConfigSecret(context.Background(), reader, namespace, name)
	}

	return gophercloud.AuthOptions{
		IdentityEndpoint: os.Getenv("OS_AUTH_URL"),
		Username:         os.Getenv("OS_USERNAME"),
		Password:         os.Getenv("OS_PASSWORD"),
		TenantName:       os.Getenv("OS_PROJECT_NAME"),
		DomainName:       os.Getenv("OS_USER_DOMAIN_NAME"),
	}, nil
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

// parseLabels splits a comma-separated label string and parses each entry
// into a LabelSelector. Entries can be "key" (presence-only) or "key=value".
func parseLabels(s string) []controller.LabelSelector {
	var selectors []controller.LabelSelector
	for _, l := range strings.Split(s, ",") {
		l = strings.TrimSpace(l)
		if l != "" {
			selectors = append(selectors, controller.ParseLabelSelector(l))
		}
	}
	return selectors
}

// setupController registers the reconciler and configures watches for
// Ingress and Node resources.
func setupController(mgr ctrl.Manager, prov provider.Provider, ingressNodeLabels []controller.LabelSelector) error {
	reconciler := &controller.Controller{
		Client:            mgr.GetClient(),
		Provider:          prov,
		IngressNodeLabels: ingressNodeLabels,
	}

	// hasIngressLabel checks whether an object matches any of the ingress node label selectors.
	hasIngressLabel := func(obj client.Object) bool {
		objLabels := obj.GetLabels()
		for _, sel := range ingressNodeLabels {
			if sel.Matches(objLabels) {
				return true
			}
		}
		return false
	}

	// ingressPredicate filters Node events to those that have (or had) the
	// ingress node label. For Update events, it checks both the old and
	// new object so that label removal also triggers reconciliation.
	ingressPredicate := predicate.Funcs{
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

	// landbMetadataPredicate triggers reconciliation when any LANDB-owned node
	// label or annotation is added, removed, or changed. This covers nodes that
	// are not ingress nodes, because landb-set metadata is independent from
	// alias membership.
	landbMetadataPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return hasLandbMetadata(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return landbMetadataChanged(e.ObjectOld, e.ObjectNew)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return hasLandbMetadata(e.Object)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return hasLandbMetadata(e.Object)
		},
	}

	return builder.ControllerManagedBy(mgr).
		// Reconcile on any Ingress change across all namespaces.
		For(&networkingv1.Ingress{}).
		// Reconcile on relevant Node changes. All node events map to the same
		// reconcile key to avoid thundering herd when multiple nodes change
		// simultaneously.
		Watches(
			&corev1.Node{},
			handler.EnqueueRequestsFromMapFunc(
				func(_ context.Context, _ client.Object) []reconcile.Request {
					return []reconcile.Request{{
						NamespacedName: types.NamespacedName{Name: "landb-alias-reconcile"},
					}}
				},
			),
			builder.WithPredicates(predicate.Or(ingressPredicate, statusChangedPredicate, landbMetadataPredicate)),
		).
		Complete(reconciler)
}

func hasLandbMetadata(obj client.Object) bool {
	return hasPrefixedKey(obj.GetLabels(), landbMetadataPrefix) ||
		hasPrefixedKey(obj.GetAnnotations(), landbMetadataPrefix)
}

func landbMetadataChanged(oldObj, newObj client.Object) bool {
	return prefixedMapChanged(oldObj.GetLabels(), newObj.GetLabels(), landbMetadataPrefix) ||
		prefixedMapChanged(oldObj.GetAnnotations(), newObj.GetAnnotations(), landbMetadataPrefix)
}

func hasPrefixedKey(values map[string]string, prefix string) bool {
	for key := range values {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func prefixedMapChanged(oldValues, newValues map[string]string, prefix string) bool {
	keys := make(map[string]struct{})
	for key := range oldValues {
		if strings.HasPrefix(key, prefix) {
			keys[key] = struct{}{}
		}
	}
	for key := range newValues {
		if strings.HasPrefix(key, prefix) {
			keys[key] = struct{}{}
		}
	}

	for key := range keys {
		oldValue, hadOldValue := oldValues[key]
		newValue, hasNewValue := newValues[key]
		if hadOldValue != hasNewValue || oldValue != newValue {
			return true
		}
	}
	return false
}
