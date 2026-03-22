// Package provider defines the interface that DNS alias providers must
// implement, along with the data types used to describe the desired state.
//
// The controller builds an AliasSet from Kubernetes Ingress and Node
// resources and passes it to the provider's Sync method. The provider
// is responsible for reading the current infrastructure state, computing
// the diff, and applying the necessary changes.
package provider

import "context"

// NodeInfo holds the identifying information for a single Kubernetes node
// that participates in ingress traffic.
type NodeInfo struct {
	// Name is the Kubernetes node name, which must match the OpenStack
	// server name so the provider can locate the corresponding instance.
	Name string

	// IP is the node's routable IP address (ExternalIP preferred,
	// InternalIP as fallback).
	IP string
}

// AliasSet represents the complete desired state of DNS aliases.
//
// Aliases are the base names (e.g., "myapp") extracted from Ingress hosts
// after stripping the ".cern.ch" suffix. Nodes are the ingress-labeled
// Kubernetes nodes, sorted alphabetically by name. The node's position
// in the slice determines its load-balancer suffix (--load-N-).
type AliasSet struct {
	// Aliases is a sorted, deduplicated list of alias names.
	Aliases []string

	// Nodes is the ordered list of ingress nodes. The index in this
	// slice determines the --load-N- suffix assigned to each node.
	Nodes []NodeInfo

	// StaleNodes are cluster nodes without the ingress label that may
	// still carry landb-alias metadata from a previous configuration.
	// The provider should remove any landb-alias* metadata from these.
	StaleNodes []NodeInfo
}

// Provider defines the contract that any DNS alias backend must satisfy.
//
// Implementations are expected to be safe for sequential use within a
// single reconciliation loop. Concurrent use is not required.
type Provider interface {
	// Sync reconciles the infrastructure to match the desired AliasSet.
	//
	// The implementation should:
	//  1. For each node, compute the desired metadata from aliases + node index.
	//  2. Read the current metadata from the infrastructure.
	//  3. Diff and apply only the necessary changes.
	//  4. Return a non-nil error if any operation fails.
	//
	// Partial failures should be aggregated (e.g., via errors.Join) so that
	// all nodes are attempted even if some fail.
	Sync(ctx context.Context, desired AliasSet) error
}
