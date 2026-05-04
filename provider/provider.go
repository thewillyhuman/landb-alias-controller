// Package provider defines the interface that DNS alias providers must
// implement, along with the data types used to describe the desired state.
//
// The controller builds a DesiredState from Kubernetes Ingress and Node
// resources and passes it to the provider's Sync method. The provider
// is responsible for reading the current infrastructure state, computing
// the diff, and applying the necessary changes.
package provider

import "context"

// NodeInfo holds the identifying information for a single Kubernetes node
// that participates in metadata reconciliation.
type NodeInfo struct {
	// Name is the Kubernetes node name, which must match the OpenStack
	// server name so the provider can locate the corresponding instance.
	Name string

	// Ready indicates whether Kubernetes currently reports the node as Ready.
	// Controllers use this to remove metadata from unhealthy nodes.
	Ready bool

	// LandbSet is the desired LANDB set metadata value for this node. Multiple
	// sets are encoded as a comma-separated string. An empty value means the
	// controller should remove the metadata key.
	LandbSet string
}

// DesiredState represents the complete desired infrastructure state.
//
// Aliases are the base names (e.g., "myapp") extracted from Ingress hosts
// after stripping the ".cern.ch" suffix. IngressNodes are the ingress-labeled
// Kubernetes nodes, sorted alphabetically by name. The node's position in the
// slice determines its load-balancer suffix (--load-N-).
type DesiredState struct {
	// Aliases is a sorted, deduplicated list of alias names.
	Aliases []string

	// IngressNodes is the ordered list of ingress nodes. The index in this
	// slice determines the --load-N- suffix assigned to each node.
	IngressNodes []NodeInfo

	// StaleAliasNodes are cluster nodes that should not serve aliases, either
	// because they lack the ingress label or because they are NotReady.
	StaleAliasNodes []NodeInfo

	// LandbSetNodes are Ready nodes with one or more desired landb-set values.
	LandbSetNodes []NodeInfo

	// StaleLandbSetNodes are nodes without a desired landb-set value, or nodes
	// that declare a value but are not Ready. The provider should remove any
	// stale landb-set metadata from these.
	StaleLandbSetNodes []NodeInfo
}

// Provider defines the contract that any DNS alias backend must satisfy.
//
// Implementations are expected to be safe for sequential use within a
// single reconciliation loop. Concurrent use is not required.
type Provider interface {
	// Sync reconciles the infrastructure to match the desired state.
	//
	// The implementation should:
	//  1. Reconcile landb-alias metadata for ingress nodes.
	//  2. Remove stale landb-alias metadata from non-ingress or NotReady nodes.
	//  3. Reconcile landb-set metadata for Ready nodes that declare one or more values.
	//  4. Remove stale landb-set metadata from nodes that no longer declare values or are NotReady.
	//  5. Return a non-nil error if any operation fails.
	//
	// Partial failures should be aggregated (e.g., via errors.Join) so that
	// all nodes are attempted even if some fail.
	Sync(ctx context.Context, desired DesiredState) error
}
