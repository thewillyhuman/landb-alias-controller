package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"gitlab.cern.ch/gfacundo/landb-alias-controller/dns"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/internal"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/internal/utils"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/plan"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/provider" // Added
	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Controller is the main controller for the application.
// It is responsible for reconciling the state of the DNS provider with the
// state of the Kubernetes ingresses.
//
// The controller works by:
// 1. Watching for changes to Ingress and Node resources.
// 2. Building a desired state of DNS records from the cluster.
// 3. Getting the current state of DNS records from the provider.
// 4. Calculating the difference between the desired and current state.
// 5. Applying the changes to the provider.
type Controller struct {
	// Client is the Kubernetes client.
	client.Client
	// runAtMutex is a mutex to protect the lastRunAt field.
	runAtMutex sync.Mutex
	// lastRunAt is the time of the last reconciliation.
	lastRunAt time.Time
	// Interval is the reconciliation interval.
	Interval time.Duration
	// DnsProvider is the generic DNS provider (e.g., OpenStack, Route53).
	DnsProvider provider.Provider
	// IngressNodeLabel is the label used to select ingress nodes.
	IngressNodeLabel string
}

// Reconcile is the main reconciliation loop. It is called every time an
// ingress is created, updated, or deleted. It is also called when a node
// with the ingress label is created, updated, or deleted.
//
// The reconciliation loop is idempotent. It can be called multiple times
// without changing the result.
func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	err := c.RunOnce(ctx)
	if err != nil {
		// Log the error but return TerminalError to prevent requeueing on
		// what is likely a persistent configuration or provider issue.
		log.Error(err, "Reconciliation failed")
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	// Successful reconciliation, no requeue needed.
	// The controller will be triggered again by Ingress/Node changes.
	return ctrl.Result{}, nil
}

// RunOnce is the main logic of the controller.
// It performs a full reconciliation cycle.
func (c *Controller) RunOnce(ctx context.Context) error {
	log := log.FromContext(ctx)
	log.Info("Starting reconciliation cycle")

	// Update the last run time for metrics.
	c.runAtMutex.Lock()
	c.lastRunAt = time.Now()
	c.runAtMutex.Unlock()

	// --- Step 1: Build Desired State ---
	// Get the full list of desired DNS records from Kubernetes (Ingresses + Nodes)
	desiredRecords, err := c.buildDesiredRecords(ctx)
	if err != nil {
		log.Error(err, "Failed to build desired DNS records")
		return err // Exit if we can't determine the desired state.
	}

	// --- Step 2: Get Current State ---
	// Get the current list of records from the DNS provider
	currentRecords, err := c.DnsProvider.Records()
	if err != nil {
		log.Error(err, "Failed to fetch current records from provider")
		return err
	}

	// --- Step 3: Calculate Plan ---
	// Compute the diff (create, update, delete)
	p := &plan.Plan{
		Current: currentRecords,
		Desired: desiredRecords,
		Changes: &plan.Changes{}, // Init to empty to avoid nils.
	}
	calculatedPlan := p.Calculate()

	log.Info("Reconciliation plan calculated", "create", len(calculatedPlan.Changes.Create), "update", len(calculatedPlan.Changes.Update), "delete", len(calculatedPlan.Changes.Delete))
	log.V(1).Info("Full reconciliation plan", "plan", calculatedPlan.ToString())

	// If there are no changes, we're done.
	if len(calculatedPlan.Changes.Create) == 0 &&
		len(calculatedPlan.Changes.Update) == 0 &&
		len(calculatedPlan.Changes.Delete) == 0 {
		log.Info("Skipping reconciliation: no changes required")
		return nil
	}

	// --- Step 4: Apply Plan ---
	// Send the set of changes to the provider to execute.
	if err := c.DnsProvider.Reconcile(calculatedPlan.Changes); err != nil {
		log.Error(err, "Failed to apply reconciliation plan")
		return err
	}

	log.Info("Successfully applied reconciliation plan")
	return nil
}

// buildDesiredRecords constructs the full list of desired dns.Record objects
// based on the current cluster state (Ingresses and Nodes).
func (c *Controller) buildDesiredRecords(ctx context.Context) ([]*dns.Record, error) {
	log := log.FromContext(ctx)
	// Get base aliases from Ingresses, e.g., ["app1", "app2"]
	baseAliases, err := c.listIngressLanDBAliases(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list ingress aliases: %w", err)
	}

	// Get all ingress node IPs, e.g., ["1.1.1.1", "2.2.2.2"]
	nodeIPs, err := c.listIngressNodeIPs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list ingress node IPs: %w", err)
	}

	if len(nodeIPs) == 0 {
		log.V(1).Info("No ingress node IPs found. Desired record list will be empty.")
		return []*dns.Record{}, nil // Return empty slice, not nil
	}

	var desiredRecords = make([]*dns.Record, 0)
	for _, alias := range baseAliases {
		rec := &dns.Record{
			Name:   alias,
			Type:   internal.ARecord,
			TTL:    300,     // Default TTL. Could be made configurable.
			Values: nodeIPs, // All aliases point to all ingress IPs
		}
		desiredRecords = append(desiredRecords, rec)
	}

	return desiredRecords, nil
}

// listIngressLanDBAliases returns a sorted, deduplicated list of LANDB aliases
// derived from Ingress hosts ending with "cern.ch". The ".cern.ch" suffix is removed.
func (c *Controller) listIngressLanDBAliases(ctx context.Context) ([]string, error) {
	log := log.FromContext(ctx)
	log.V(1).Info("Listing desired aliases from ingresses")
	ingresses := &networkingv1.IngressList{}
	if err := c.List(ctx, ingresses); err != nil {
		return nil, fmt.Errorf("unable to list cluster ingresses: %w", err)
	}

	aliasSet := make(map[string]struct{})
	for _, ingress := range ingresses.Items {
		for _, rule := range ingress.Spec.Rules {
			if strings.HasSuffix(rule.Host, internal.CernChSuffix) {
				alias := strings.TrimSuffix(rule.Host, internal.CernChDomain)
				aliasSet[alias] = struct{}{}
			} else {
				log.V(1).Info("Ingress host is not a CERN managed domain, skipping", "host", rule.Host)
			}
		}
	}

	// Convert set to sorted slice
	aliases := make([]string, 0, len(aliasSet))
	for alias := range aliasSet {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	log.V(1).Info("Desired aliases from ingresses", "aliases", aliases)

	return aliases, nil
}

// listIngressNodeIPs returns the IP addresses of all nodes labeled as ingress nodes.
// IPs are sorted alphabetically for deterministic output.
func (c *Controller) listIngressNodeIPs(ctx context.Context) ([]string, error) {
	log := log.FromContext(ctx)
	log.V(1).Info("Listing ingress node IPs")
	nodes := &v1.NodeList{}
	// Use a label selector to find nodes where the ingress role label *exists*
	selector := client.MatchingLabels{c.IngressNodeLabel: internal.TrueString}
	if err := c.List(ctx, nodes, selector); err != nil {
		return nil, fmt.Errorf("unable to list nodes: %w", err)
	}

	var nodeIPs []string
	for i := range nodes.Items {
		node := &nodes.Items[i]
		ip, err := utils.GetNodeIP(node)
		if err != nil {
			log.V(1).Info("Skipping node", "node", node.Name, "error", err)
			continue
		}
		nodeIPs = append(nodeIPs, ip)
	}

	sort.Strings(nodeIPs) // deterministic order
	log.V(1).Info("Ingress node IPs", "ips", nodeIPs)
	return nodeIPs, nil
}
