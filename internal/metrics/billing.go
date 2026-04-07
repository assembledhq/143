package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Billing-related Prometheus metrics for container usage observability.
//
// NOTE: org_id is used as a label dimension. This is acceptable for deployments
// with up to ~100 orgs. For higher cardinality, remove org_id from labels and
// rely on the container_usage_events DB table for per-org queries.
var (
	// ContainerStartsTotal counts sandbox container creation events.
	ContainerStartsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "container_starts_total",
			Help: "Total number of sandbox containers started",
		},
		[]string{"org_id", "provider", "image"},
	)

	// ContainerStopsTotal counts sandbox container destruction events.
	ContainerStopsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "container_stops_total",
			Help: "Total number of sandbox containers stopped",
		},
		[]string{"org_id", "exit_reason"},
	)

	// ContainersActive tracks the number of currently running sandbox containers.
	ContainersActive = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "containers_active",
			Help: "Number of sandbox containers currently running",
		},
		[]string{"org_id"},
	)

	// ContainerDurationSeconds records the wall-clock duration of container runs.
	ContainerDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "container_duration_seconds",
			Help: "Wall-clock duration of sandbox container runs in seconds",
			Buckets: []float64{
				10, 30, 60, 120, 300, 600, 900, 1200, 1800, 3600,
			},
		},
		[]string{"org_id", "exit_reason"},
	)

	// ContainerCPUAllocated records the CPU cores allocated to containers.
	ContainerCPUAllocated = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "container_cpu_allocated",
			Help: "CPU cores allocated to sandbox containers",
			Buckets: []float64{0.5, 1, 2, 4, 8},
		},
		[]string{"org_id"},
	)

	// ContainerMemoryAllocatedMB records the memory allocated to containers.
	ContainerMemoryAllocatedMB = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "container_memory_allocated_mb",
			Help: "Memory (MB) allocated to sandbox containers",
			Buckets: []float64{512, 1024, 2048, 4096, 8192, 16384},
		},
		[]string{"org_id"},
	)

	// ContainerMinutesTotal is a counter of total billable container-minutes.
	// Increment by the actual minutes each container ran.
	ContainerMinutesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "container_minutes_total",
			Help: "Total billable container-minutes consumed",
		},
		[]string{"org_id"},
	)
)
