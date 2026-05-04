// Package metrics defines and registers Prometheus metrics for the
// landb-alias-controller. All metrics are registered with the
// controller-runtime metrics registry so they are automatically
// exposed on the /metrics HTTP endpoint.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// ReconciliationsTotal counts completed reconciliation cycles,
	// partitioned by result ("success" or "error").
	ReconciliationsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "landb_reconciliations_total",
			Help: "Total number of reconciliation cycles executed.",
		},
		[]string{"result"},
	)

	// ReconciliationDuration tracks the wall-clock time of each
	// reconciliation cycle in seconds.
	ReconciliationDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "landb_reconciliation_duration_seconds",
			Help:    "Duration of reconciliation cycles in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)

	// AliasesDesired reports the number of unique aliases the controller
	// wants to configure. Updated after each successful state build.
	AliasesDesired = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "landb_aliases_desired_total",
			Help: "Number of unique desired aliases derived from Ingress resources.",
		},
	)

	// NodesManaged reports the total number of Kubernetes nodes considered
	// during reconciliation.
	NodesManaged = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "landb_nodes_managed_total",
			Help: "Number of Kubernetes nodes considered for alias or landb-set reconciliation.",
		},
	)

	// IngressNodesManaged reports the number of Ready ingress nodes used for
	// landb-alias metadata.
	IngressNodesManaged = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "landb_ingress_nodes_managed_total",
			Help: "Number of Ready ingress nodes currently managed for landb-alias metadata.",
		},
	)

	// AliasCleanupNodes reports the number of nodes that should not serve
	// aliases and are checked for stale landb-alias metadata.
	AliasCleanupNodes = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "landb_alias_cleanup_nodes_total",
			Help: "Number of nodes checked for stale landb-alias metadata.",
		},
	)

	// LandbSetNodesManaged reports the number of Ready nodes declaring one or
	// more landb-set values through annotation or label.
	LandbSetNodesManaged = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "landb_set_nodes_managed_total",
			Help: "Number of Ready nodes currently declaring one or more landb-set metadata values.",
		},
	)

	// LandbSetCleanupNodes reports the number of nodes without an active
	// desired landb-set value list that are checked for stale landb-set metadata.
	LandbSetCleanupNodes = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "landb_set_cleanup_nodes_total",
			Help: "Number of nodes checked for stale landb-set metadata, including NotReady nodes.",
		},
	)

	// OpenStackAPICalls counts OpenStack API calls, partitioned by
	// operation (e.g., "get_server_id", "get_metadata", "update_metadata",
	// "delete_metadatum") and result ("success" or "error").
	OpenStackAPICalls = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "landb_openstack_api_calls_total",
			Help: "Total number of OpenStack API calls.",
		},
		[]string{"operation", "result"},
	)

	// OpenStackAPIDuration tracks the latency of individual OpenStack
	// API calls in seconds, partitioned by operation.
	OpenStackAPIDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "landb_openstack_api_duration_seconds",
			Help:    "Duration of OpenStack API calls in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"operation"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		ReconciliationsTotal,
		ReconciliationDuration,
		AliasesDesired,
		NodesManaged,
		IngressNodesManaged,
		AliasCleanupNodes,
		LandbSetNodesManaged,
		LandbSetCleanupNodes,
		OpenStackAPICalls,
		OpenStackAPIDuration,
	)
}
