// Package controller implements the Kubernetes reconciliation loop that
// watches Ingress and Node resources and drives DNS alias synchronization
// via a provider.Provider.
//
// The controller is provider-agnostic: it builds a DesiredState describing
// what aliases should exist and which nodes should serve them, then
// delegates all infrastructure interaction to the provider.
package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"gitlab.cern.ch/kubernetes/networking/landb-controller/landb-alias-controller/metrics"
	"gitlab.cern.ch/kubernetes/networking/landb-controller/landb-alias-controller/provider"
)

const (
	// cernChSuffix is the DNS suffix used to identify CERN-managed domains.
	cernChSuffix = ".cern.ch"

	// landbSetAnnotation is the node annotation used to declare the desired
	// OpenStack landb-set metadata value.
	landbSetAnnotation = "landb.cern.ch/set"

	// landbSetLabel is accepted for compatibility with clusters that already
	// model a single landb-set value as a node label.
	landbSetLabel = "landb.cern.ch/set"

	// requeueDelay is the delay before retrying after a failed reconciliation.
	// OpenStack errors can be transient, so we retry instead of giving up.
	requeueDelay = 30 * time.Second
)

// LabelSelector represents a label key with an optional value.
// If Value is nil, only the key's presence is required (e.g., "node-role.kubernetes.io/ingress").
// If Value is non-nil, both key and value must match (e.g., "role=ingress").
type LabelSelector struct {
	Key   string
	Value *string
}

// Matches returns true if the given labels satisfy this selector.
func (ls LabelSelector) Matches(labels map[string]string) bool {
	v, ok := labels[ls.Key]
	if !ok {
		return false
	}
	if ls.Value != nil {
		return v == *ls.Value
	}
	return true
}

// String returns the selector as "key" or "key=value".
func (ls LabelSelector) String() string {
	if ls.Value != nil {
		return ls.Key + "=" + *ls.Value
	}
	return ls.Key
}

// ParseLabelSelector parses a string like "key" or "key=value" into a LabelSelector.
func ParseLabelSelector(s string) LabelSelector {
	key, value, hasValue := strings.Cut(s, "=")
	if hasValue {
		return LabelSelector{Key: key, Value: &value}
	}
	return LabelSelector{Key: key}
}

// Controller reconciles Kubernetes Ingress and Node resources into DNS
// alias configuration on the infrastructure provider.
//
// It is registered with the controller-runtime manager and triggered
// whenever an Ingress or ingress-labeled Node changes.
type Controller struct {
	// Client is the Kubernetes API client provided by controller-runtime.
	client.Client

	// Provider is the DNS alias backend (e.g., OpenStack LANDB).
	Provider provider.Provider

	// IngressNodeLabels are the Kubernetes label selectors used to identify
	// nodes that serve ingress traffic. A node matching any selector is selected.
	// Each entry is a LabelSelector with a key and optional value.
	IngressNodeLabels []LabelSelector
}

// Reconcile is the entry point called by controller-runtime on each event.
// It builds the desired alias state from the cluster and delegates
// synchronization to the provider.
func (c *Controller) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Reconciliation triggered")

	start := time.Now()
	err := c.runOnce(ctx)
	duration := time.Since(start).Seconds()

	metrics.ReconciliationDuration.Observe(duration)

	if err != nil {
		metrics.ReconciliationsTotal.WithLabelValues("error").Inc()
		log.Error(err, "Reconciliation failed, will retry", "retryAfter", requeueDelay)
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	metrics.ReconciliationsTotal.WithLabelValues("success").Inc()
	log.Info("Reconciliation completed successfully", "duration", fmt.Sprintf("%.2fs", duration))
	return ctrl.Result{}, nil
}

// runOnce performs a single full reconciliation cycle:
//  1. List all CERN aliases from Ingress resources.
//  2. List all ingress-labeled nodes.
//  3. Build the DesiredState and pass it to the provider.
func (c *Controller) runOnce(ctx context.Context) error {
	log := log.FromContext(ctx)

	// Step 1: Extract desired aliases from Ingress hosts.
	aliases, err := c.listAliases(ctx)
	if err != nil {
		return fmt.Errorf("listing aliases: %w", err)
	}
	log.V(1).Info("Desired aliases", "count", len(aliases), "aliases", aliases)
	metrics.AliasesDesired.Set(float64(len(aliases)))

	// Step 2: Collect Ready ingress nodes for alias metadata.
	ingressNodes, err := c.listIngressNodes(ctx)
	if err != nil {
		return fmt.Errorf("listing ingress nodes: %w", err)
	}
	log.Info("Ingress nodes", "count", len(ingressNodes))
	for _, n := range ingressNodes {
		log.V(1).Info("  Ingress node", "name", n.Name)
	}
	metrics.IngressNodesManaged.Set(float64(len(ingressNodes)))

	if len(ingressNodes) == 0 {
		log.Info("No ingress nodes found; desired state is empty")
	}
	if len(aliases) == 0 {
		log.Info("No CERN aliases found in Ingress resources")
	}

	// Step 3: Collect all nodes so alias cleanup and landb-set metadata can
	// be reconciled independently from ingress membership.
	allNodes, err := c.listAllNodes(ctx)
	if err != nil {
		return fmt.Errorf("listing all nodes: %w", err)
	}
	metrics.NodesManaged.Set(float64(len(allNodes)))

	staleAliasNodes := buildStaleAliasNodes(allNodes, ingressNodes)
	log.Info("Stale alias nodes", "count", len(staleAliasNodes))
	for _, n := range staleAliasNodes {
		log.V(1).Info("  Stale alias node", "name", n.Name)
	}
	metrics.AliasCleanupNodes.Set(float64(len(staleAliasNodes)))

	landbSetNodes, staleLandbSetNodes := splitLandbSetNodes(allNodes)
	log.Info("Nodes with landb-set", "count", len(landbSetNodes))
	for _, n := range landbSetNodes {
		log.V(1).Info("  Landb set node", "name", n.Name, "landbSet", n.LandbSet)
	}
	log.Info("Stale landb-set nodes", "count", len(staleLandbSetNodes))
	for _, n := range staleLandbSetNodes {
		if !n.Ready && n.LandbSet != "" {
			log.Info("Skipping NotReady landb-set node", "node", n.Name)
		}
		log.V(1).Info("  Stale landb-set node", "name", n.Name)
	}
	metrics.LandbSetNodesManaged.Set(float64(len(landbSetNodes)))
	metrics.LandbSetCleanupNodes.Set(float64(len(staleLandbSetNodes)))

	// Step 4: Sync with the provider.
	desired := provider.DesiredState{
		Aliases:            aliases,
		IngressNodes:       ingressNodes,
		StaleAliasNodes:    staleAliasNodes,
		LandbSetNodes:      landbSetNodes,
		StaleLandbSetNodes: staleLandbSetNodes,
	}
	if err := c.Provider.Sync(ctx, desired); err != nil {
		return fmt.Errorf("provider sync: %w", err)
	}

	return nil
}

// listAliases returns a sorted, deduplicated list of LANDB alias names
// derived from all Ingress hosts ending with ".cern.ch". The suffix is
// stripped, leaving just the alias (e.g., "app.cern.ch" → "app").
func (c *Controller) listAliases(ctx context.Context) ([]string, error) {
	log := log.FromContext(ctx)
	log.V(1).Info("Listing aliases from Ingress resources")

	var ingresses networkingv1.IngressList
	if err := c.List(ctx, &ingresses); err != nil {
		return nil, fmt.Errorf("listing ingresses: %w", err)
	}

	seen := make(map[string]struct{})
	for _, ingress := range ingresses.Items {
		for _, rule := range ingress.Spec.Rules {
			if !strings.HasSuffix(rule.Host, cernChSuffix) {
				log.V(1).Info("Skipping non-CERN host", "host", rule.Host,
					"ingress", ingress.Namespace+"/"+ingress.Name)
				continue
			}
			alias := strings.TrimSuffix(rule.Host, cernChSuffix)
			if alias == "" {
				continue
			}
			seen[alias] = struct{}{}
			log.V(1).Info("Found alias", "alias", alias, "host", rule.Host,
				"ingress", ingress.Namespace+"/"+ingress.Name)
		}
	}

	aliases := make([]string, 0, len(seen))
	for alias := range seen {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	return aliases, nil
}

// listIngressNodes returns ingress-labeled nodes sorted alphabetically by name.
// A node is selected if it carries any of the configured ingress labels.
// The sorted order is critical because the node's index determines its
// --load-N- suffix.
func (c *Controller) listIngressNodes(ctx context.Context) ([]provider.NodeInfo, error) {
	log := log.FromContext(ctx)
	log.V(1).Info("Listing ingress nodes", "labels", c.IngressNodeLabels)

	// Query once per label selector and deduplicate by node name.
	seen := make(map[string]struct{})
	var nodes []provider.NodeInfo

	for _, sel := range c.IngressNodeLabels {
		var nodeList v1.NodeList
		var listOpt client.ListOption
		if sel.Value != nil {
			listOpt = client.MatchingLabels{sel.Key: *sel.Value}
		} else {
			listOpt = client.HasLabels{sel.Key}
		}
		if err := c.List(ctx, &nodeList, listOpt); err != nil {
			return nil, fmt.Errorf("listing nodes with label %q: %w", sel, err)
		}

		for i := range nodeList.Items {
			node := &nodeList.Items[i]
			if _, exists := seen[node.Name]; exists {
				continue
			}
			seen[node.Name] = struct{}{}

			if !isNodeReady(node) {
				log.Info("Skipping NotReady ingress node", "node", node.Name)
				continue
			}
			nodeInfo := nodeInfoFromNode(node)
			nodes = append(nodes, nodeInfo)
			log.V(1).Info("Ingress node found", "node", node.Name,
				"landbSet", nodeInfo.LandbSet, "matchedLabel", sel.String())
		}
	}

	// Sort by name for deterministic --load-N- suffix assignment.
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})

	return nodes, nil
}

// listAllNodes returns all cluster nodes sorted alphabetically by name.
// This is used to identify stale nodes that may need metadata cleanup.
func (c *Controller) listAllNodes(ctx context.Context) ([]provider.NodeInfo, error) {
	log := log.FromContext(ctx)
	log.V(1).Info("Listing all cluster nodes")

	var nodeList v1.NodeList
	if err := c.List(ctx, &nodeList); err != nil {
		return nil, fmt.Errorf("listing all nodes: %w", err)
	}

	var nodes []provider.NodeInfo
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		nodes = append(nodes, nodeInfoFromNode(node))
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})

	return nodes, nil
}

func buildStaleAliasNodes(allNodes, ingressNodes []provider.NodeInfo) []provider.NodeInfo {
	ingressSet := make(map[string]struct{}, len(ingressNodes))
	for _, n := range ingressNodes {
		ingressSet[n.Name] = struct{}{}
	}

	var staleAliasNodes []provider.NodeInfo
	for _, n := range allNodes {
		if _, isIngress := ingressSet[n.Name]; !isIngress {
			staleAliasNodes = append(staleAliasNodes, n)
		}
	}
	return staleAliasNodes
}

func splitLandbSetNodes(allNodes []provider.NodeInfo) ([]provider.NodeInfo, []provider.NodeInfo) {
	var landbSetNodes []provider.NodeInfo
	var staleLandbSetNodes []provider.NodeInfo
	for _, n := range allNodes {
		if n.Ready && n.LandbSet != "" {
			landbSetNodes = append(landbSetNodes, n)
			continue
		}
		staleLandbSetNodes = append(staleLandbSetNodes, n)
	}
	return landbSetNodes, staleLandbSetNodes
}

func nodeInfoFromNode(node *v1.Node) provider.NodeInfo {
	return provider.NodeInfo{
		Name:     node.Name,
		Ready:    isNodeReady(node),
		LandbSet: nodeLandbSet(node),
	}
}

func nodeLandbSet(node *v1.Node) string {
	if _, ok := node.Annotations[landbSetAnnotation]; ok {
		return normalizeLandbSet(node.Annotations[landbSetAnnotation])
	}
	return normalizeLandbSet(node.Labels[landbSetLabel])
}

func normalizeLandbSet(value string) string {
	parts := strings.Split(value, ",")
	seen := make(map[string]struct{}, len(parts))
	sets := make([]string, 0, len(parts))

	for _, part := range parts {
		set := strings.TrimSpace(part)
		if set == "" {
			continue
		}
		if _, exists := seen[set]; exists {
			continue
		}
		seen[set] = struct{}{}
		sets = append(sets, set)
	}

	return strings.Join(sets, ",")
}

// isNodeReady returns true if the node has a Ready condition with status True.
// Nodes without a Ready condition are treated as not ready.
func isNodeReady(node *v1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == v1.NodeReady {
			return cond.Status == v1.ConditionTrue
		}
	}
	return false
}
