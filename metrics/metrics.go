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

	// NodesManaged reports the number of ingress-labeled nodes the
	// controller is managing.
	NodesManaged = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "landb_nodes_managed_total",
			Help: "Number of ingress nodes currently managed.",
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
		OpenStackAPICalls,
		OpenStackAPIDuration,
	)
}
