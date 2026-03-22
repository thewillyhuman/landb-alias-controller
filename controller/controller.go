// Package controller implements the Kubernetes reconciliation loop that
// watches Ingress and Node resources and drives DNS alias synchronization
// via a provider.Provider.
//
// The controller is provider-agnostic: it builds an AliasSet describing
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

	"gitlab.cern.ch/gfacundo/landb-alias-controller/metrics"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider"
)

const (
	// cernChSuffix is the DNS suffix used to identify CERN-managed domains.
	cernChSuffix = ".cern.ch"

	// requeueDelay is the delay before retrying after a failed reconciliation.
	// OpenStack errors can be transient, so we retry instead of giving up.
	requeueDelay = 30 * time.Second
)

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

	// IngressNodeLabel is the Kubernetes label key used to identify
	// nodes that serve ingress traffic (e.g., "node-role.kubernetes.io/ingress").
	IngressNodeLabel string
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
//  2. List all ingress-labeled nodes with their IPs.
//  3. Build the AliasSet and pass it to the provider.
func (c *Controller) runOnce(ctx context.Context) error {
	log := log.FromContext(ctx)

	// Step 1: Extract desired aliases from Ingress hosts.
	aliases, err := c.listAliases(ctx)
	if err != nil {
		return fmt.Errorf("listing aliases: %w", err)
	}
	log.V(1).Info("Desired aliases", "count", len(aliases), "aliases", aliases)
	metrics.AliasesDesired.Set(float64(len(aliases)))

	// Step 2: Collect ingress nodes with their IPs.
	nodes, err := c.listNodes(ctx)
	if err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}
	log.V(1).Info("Ingress nodes", "count", len(nodes))
	for _, n := range nodes {
		log.V(1).Info("  Node", "name", n.Name, "ip", n.IP)
	}
	metrics.NodesManaged.Set(float64(len(nodes)))

	if len(nodes) == 0 {
		log.Info("No ingress nodes found; desired state is empty")
	}
	if len(aliases) == 0 {
		log.Info("No CERN aliases found in Ingress resources")
	}

	// Step 3: Identify stale nodes (non-ingress nodes that may carry
	// leftover landb-alias metadata).
	allNodes, err := c.listAllNodes(ctx)
	if err != nil {
		return fmt.Errorf("listing all nodes: %w", err)
	}

	ingressSet := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		ingressSet[n.Name] = struct{}{}
	}
	var staleNodes []provider.NodeInfo
	for _, n := range allNodes {
		if _, isIngress := ingressSet[n.Name]; !isIngress {
			staleNodes = append(staleNodes, n)
		}
	}
	log.V(1).Info("Stale nodes", "count", len(staleNodes))

	// Step 4: Sync with the provider.
	desired := provider.AliasSet{
		Aliases:    aliases,
		Nodes:      nodes,
		StaleNodes: staleNodes,
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

// listNodes returns the ingress-labeled nodes sorted alphabetically by
// name. The sorted order is critical because the node's index determines
// its --load-N- suffix.
func (c *Controller) listNodes(ctx context.Context) ([]provider.NodeInfo, error) {
	log := log.FromContext(ctx)
	log.V(1).Info("Listing ingress nodes", "label", c.IngressNodeLabel)

	var nodeList v1.NodeList
	if err := c.List(ctx, &nodeList, client.HasLabels{c.IngressNodeLabel}); err != nil {
		return nil, fmt.Errorf("listing nodes with label %q: %w", c.IngressNodeLabel, err)
	}

	var nodes []provider.NodeInfo
	for i := range nodeList.Items {
		node := &nodeList.Items[i]
		ip, err := getNodeIP(node)
		if err != nil {
			log.Info("Skipping node without usable IP", "node", node.Name, "error", err)
			continue
		}
		nodes = append(nodes, provider.NodeInfo{Name: node.Name, IP: ip})
		log.V(1).Info("Ingress node found", "node", node.Name, "ip", ip)
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
		ip, err := getNodeIP(node)
		if err != nil {
			log.V(1).Info("Skipping node without usable IP", "node", node.Name, "error", err)
			continue
		}
		nodes = append(nodes, provider.NodeInfo{Name: node.Name, IP: ip})
	}

	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Name < nodes[j].Name
	})

	return nodes, nil
}

// getNodeIP extracts the best available IP address from a Kubernetes node,
// preferring ExternalIP over InternalIP.
func getNodeIP(node *v1.Node) (string, error) {
	for _, addr := range node.Status.Addresses {
		if addr.Type == v1.NodeExternalIP && addr.Address != "" {
			return addr.Address, nil
		}
	}
	for _, addr := range node.Status.Addresses {
		if addr.Type == v1.NodeInternalIP && addr.Address != "" {
			return addr.Address, nil
		}
	}
	return "", fmt.Errorf("no ExternalIP or InternalIP found for node %s", node.Name)
}
