package controller

import (
	"context"
	"fmt"
	log "github.com/sirupsen/logrus"
	"gitlab.cern.ch/gfacundo/landb-alias-controller/plan"
	openstack2 "gitlab.cern.ch/gfacundo/landb-alias-controller/provider/openstack"
	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sort"
	"strings"
	"sync"
	"time"
)

// Controller is the main controller for the application.
// It is responsible for reconciling the state of the OpenStack instances with the state of the Kubernetes ingresses.
type Controller struct {
	// Client is the Kubernetes client.
	client.Client
	// runAtMutex is a mutex to protect the lastRunAt field.
	runAtMutex sync.Mutex
	// lastRunAt is the time of the last reconciliation.
	lastRunAt time.Time
	// Interval is the reconciliation interval.
	Interval time.Duration
	// OpenstackClient is the OpenStack client.
	OpenstackClient *openstack2.Client
}

// Reconcile is the main reconciliation loop.
// It is called every time an ingress is created, updated, or deleted.
func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	err := c.RunOnce(ctx)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	return ctrl.Result{Requeue: false, RequeueAfter: 0}, nil
}

// RunOnce is the main logic of the controller.
// It is called by the Reconcile function.
func (c *Controller) RunOnce(ctx context.Context) error {
	log.Info("reconcile cycle initiated")

	// Update the last run time for metrics.
	c.runAtMutex.Lock()
	c.lastRunAt = time.Now()
	c.runAtMutex.Unlock()

	clusterLanDBAliases, err := c.listIngressLanDBAliases(ctx)
	if err != nil {
		log.Errorf("error compiling landb aliases from cluster ingresses: %v", err)
		return err // Exit the execution of the reconciliation as an empty list of aliases is dangerous.
	}

	clusterIngressNodes, err := c.listIngressNodes(ctx)
	if err != nil {
		log.Errorf("error compiling ingress nodes from api server: %v", err)
		return err
	}

	for nodeIndex, node := range clusterIngressNodes {
		log.Infof("reconciling ingress node %v", nodeIndex)

		// Get node openstack landb aliases.
		currentNodeAliases, err := c.OpenstackClient.GetInstanceLanDBAliases(ctx, node)
		if err != nil {
			log.Errorf("error retrieving node landb aliases from openstack: %v", err)
		}
		log.Infof("current landb-aliases: %+v", currentNodeAliases.AsMap())

		// Compute node aliases to be.
		desiredNodeAliases := buildNodeAliasMap(clusterLanDBAliases, nodeIndex)
		log.Infof("desired landb-aliases: %+v", desiredNodeAliases.AsMap())

		plan := plan.Plan{
			Current: currentNodeAliases,
			Desired: desiredNodeAliases,
		}

		plan = *plan.Calculate()
		log.Infof("computed plan for node %s: %s", node, plan.ToString())
		updates := plan.Changes.Update
		deletes := plan.Changes.Delete

		// Perform the updates
		err = c.OpenstackClient.SetInstanceProperties(ctx, node, updates)
		if err != nil {
			log.Errorf("error updating instance %s properties: %v", node, err)
			return err
		}

		// Perform the deletes
		for _, key := range deletes.Keys() {
			err = c.OpenstackClient.DeleteInstanceProperty(ctx, node, key)
			if err != nil {
				log.Errorf("error deleting instance %s property %s: %v", node, key, err)
				return err
			}
		}

		log.Infof("reconciling finished for ingress node %s", node)
	}

	log.Info("reconcile cycle finished")
	return nil
}

// BuildNodeAliasMap generates a map of Landb alias properties for a node.
// - clusterAliases: list of base aliases (from cluster Ingress hosts)
// - nodeIndex: the index of the node to use in the suffix
// Returns a map like: {"landb-alias": "...", "landb-alias2": "...", ...}
// Each property will not exceed maxLen (254 characters).
func buildNodeAliasMap(clusterAliases []string, nodeIndex int) *openstack2.PropertySet {
	const maxLen = 200
	suffix := fmt.Sprintf("load-%d-", nodeIndex)

	// Build full alias strings with suffix
	fullAliases := make([]string, 0, len(clusterAliases))
	for _, alias := range clusterAliases {
		fullAliases = append(fullAliases, fmt.Sprintf("%s--%s", alias, suffix))
	}

	// Split into multiple properties if the length exceeds maxLen
	props := make(map[string]string)
	current := ""
	propIndex := 1

	for _, a := range fullAliases {
		if len(current)+len(a)+1 > maxLen { // +1 for comma
			propName := "landb-alias"
			if propIndex > 1 {
				propName = fmt.Sprintf("landb-alias%d", propIndex)
			}
			props[propName] = current
			current = a
			propIndex++
		} else {
			if current != "" {
				current += ","
			}
			current += a
		}
	}

	if current != "" {
		propName := "landb-alias"
		if propIndex > 1 {
			propName = fmt.Sprintf("landb-alias%d", propIndex)
		}
		props[propName] = current
	}

	return openstack2.NewPropertySet(props)
}

// listIngressLanDBAliases returns a sorted, deduplicated list of LANDB aliases
// derived from Ingress hosts ending with "cern.ch". The ".cern.ch" suffix is removed.
func (c *Controller) listIngressLanDBAliases(ctx context.Context) ([]string, error) {
	log.Info("obtaining list of desired landb aliases from cluster ingresses")
	ingresses := &networkingv1.IngressList{}
	if err := c.List(ctx, ingresses); err != nil {
		return nil, fmt.Errorf("unable to list cluster ingresses: %w", err)
	}

	aliasSet := make(map[string]struct{})
	for _, ingress := range ingresses.Items {
		for _, rule := range ingress.Spec.Rules {
			if strings.HasSuffix(rule.Host, "cern.ch") {
				alias := strings.TrimSuffix(rule.Host, ".cern.ch")
				aliasSet[alias] = struct{}{}
			} else {
				log.Debugf("ingress defined host %s is not cern managed, skipping", rule.Host)
			}
		}
	}

	// Convert set to sorted slice
	aliases := make([]string, 0, len(aliasSet))
	for alias := range aliasSet {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	log.Infof("desired list of desired landb aliases from cluster ingresses: %v", aliases)

	return aliases, nil
}

// listIngressNodes returns the names of all nodes labeled as ingress nodes in the cluster.
// Nodes are sorted alphabetically for deterministic output.
func (c *Controller) listIngressNodes(ctx context.Context) ([]string, error) {
	log.Info("obtaining list nodes labeled as ingress")
	nodes := &v1.NodeList{}
	if err := c.List(ctx, nodes); err != nil {
		return nil, fmt.Errorf("unable to list nodes: %w", err)
	}

	var ingressNodes []string
	for _, node := range nodes.Items {
		if node.Labels["node-role.kubernetes.io/ingress"] == "true" {
			ingressNodes = append(ingressNodes, node.Name)
		} else {
			log.Debugf("node %s not labeled as ingress, skipping", node.Name)
		}
	}

	sort.Strings(ingressNodes) // deterministic order
	log.Infof("list of nodes labeled as ingress: %v", ingressNodes)
	return ingressNodes, nil
}
