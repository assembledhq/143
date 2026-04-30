package metrics

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// ActiveContainerCounter is a callback that returns the current number of
// active (running) containers. Used by the async observable gauge so the
// metric always reflects the true DB state rather than drifting on crash.
type ActiveContainerCounter func(ctx context.Context) (int64, error)

const meterName = "github.com/assembledhq/143/billing"

// BillingMetrics holds OTel instruments for container usage observability.
//
// NOTE: org_id is used as an attribute dimension. This is acceptable for
// deployments with up to ~100 orgs. For higher cardinality, remove org_id
// from attributes and rely on the container_usage_events DB table for
// per-org queries.
type BillingMetrics struct {
	ContainerStartsTotal  otelmetric.Int64Counter
	ContainerStopsTotal   otelmetric.Int64Counter
	ContainerDurationSec  otelmetric.Float64Histogram
	ContainerCPUAllocated otelmetric.Float64Histogram
	ContainerMemAllocated otelmetric.Float64Histogram
	ContainerMinutesTotal otelmetric.Float64Counter
	// containersActiveReg holds the registration so it can be cleaned up.
	containersActiveReg otelmetric.Registration
}

// NewBillingMetrics creates and registers all billing OTel instruments against
// the global MeterProvider. Call after telemetry.InitMeterProvider.
//
// If activeCounter is non-nil, an async observable gauge is registered that
// queries the true active container count (e.g. from the DB). This avoids
// gauge drift if the server crashes between start and stop events.
// Pass nil to skip the active gauge (e.g. in tests).
func NewBillingMetrics(activeCounter ActiveContainerCounter) (*BillingMetrics, error) {
	meter := otel.Meter(meterName)

	starts, err := meter.Int64Counter("container.starts",
		otelmetric.WithDescription("Total number of sandbox containers started"),
		otelmetric.WithUnit("{container}"),
	)
	if err != nil {
		return nil, err
	}

	stops, err := meter.Int64Counter("container.stops",
		otelmetric.WithDescription("Total number of sandbox containers stopped"),
		otelmetric.WithUnit("{container}"),
	)
	if err != nil {
		return nil, err
	}

	var activeReg otelmetric.Registration
	if activeCounter != nil {
		gauge, gErr := meter.Int64ObservableGauge("container.active",
			otelmetric.WithDescription("Number of sandbox containers currently running"),
			otelmetric.WithUnit("{container}"),
		)
		if gErr != nil {
			return nil, gErr
		}
		activeReg, err = meter.RegisterCallback(func(ctx context.Context, o otelmetric.Observer) error {
			count, cbErr := activeCounter(ctx)
			if cbErr != nil {
				return cbErr
			}
			o.ObserveInt64(gauge, count)
			return nil
		}, gauge)
		if err != nil {
			return nil, err
		}
	}

	duration, err := meter.Float64Histogram("container.duration",
		otelmetric.WithDescription("Wall-clock duration of sandbox container runs"),
		otelmetric.WithUnit("s"),
		otelmetric.WithExplicitBucketBoundaries(10, 30, 60, 120, 300, 600, 900, 1200, 1800, 3600),
	)
	if err != nil {
		return nil, err
	}

	cpu, err := meter.Float64Histogram("container.cpu.allocated",
		otelmetric.WithDescription("CPU cores allocated to sandbox containers"),
		otelmetric.WithUnit("{cores}"),
		otelmetric.WithExplicitBucketBoundaries(0.5, 1, 2, 4, 8),
	)
	if err != nil {
		return nil, err
	}

	mem, err := meter.Float64Histogram("container.memory.allocated",
		otelmetric.WithDescription("Memory allocated to sandbox containers"),
		otelmetric.WithUnit("MiBy"),
		otelmetric.WithExplicitBucketBoundaries(512, 1024, 2048, 4096, 8192, 16384),
	)
	if err != nil {
		return nil, err
	}

	minutes, err := meter.Float64Counter("container.minutes",
		otelmetric.WithDescription("Total billable container-minutes consumed"),
		otelmetric.WithUnit("min"),
	)
	if err != nil {
		return nil, err
	}

	return &BillingMetrics{
		ContainerStartsTotal:  starts,
		ContainerStopsTotal:   stops,
		ContainerDurationSec:  duration,
		ContainerCPUAllocated: cpu,
		ContainerMemAllocated: mem,
		ContainerMinutesTotal: minutes,
		containersActiveReg:   activeReg,
	}, nil
}

// Billing attribute keys.
var (
	AttrOrgID      = attribute.Key("org.id")
	AttrProvider   = attribute.Key("container.provider")
	AttrExitReason = attribute.Key("container.exit_reason")
)

// RecordStart records metrics when a container starts.
// NOTE: container.image is intentionally excluded from metric attributes to
// avoid unbounded cardinality (tags/SHAs). Image data lives in the DB.
func (m *BillingMetrics) RecordStart(ctx context.Context, orgID, provider string, cpuLimit float64, memoryMB int) {
	attrs := otelmetric.WithAttributes(
		AttrOrgID.String(orgID),
		AttrProvider.String(provider),
	)
	m.ContainerStartsTotal.Add(ctx, 1, attrs)
	m.ContainerCPUAllocated.Record(ctx, cpuLimit, otelmetric.WithAttributes(AttrOrgID.String(orgID)))
	m.ContainerMemAllocated.Record(ctx, float64(memoryMB), otelmetric.WithAttributes(AttrOrgID.String(orgID)))
}

// RecordStop records metrics when a container stops.
func (m *BillingMetrics) RecordStop(ctx context.Context, orgID, exitReason string, durationSec, durationMin float64) {
	orgAttr := otelmetric.WithAttributes(AttrOrgID.String(orgID))
	reasonAttrs := otelmetric.WithAttributes(AttrOrgID.String(orgID), AttrExitReason.String(exitReason))

	m.ContainerStopsTotal.Add(ctx, 1, reasonAttrs)
	m.ContainerDurationSec.Record(ctx, durationSec, reasonAttrs)
	m.ContainerMinutesTotal.Add(ctx, durationMin, orgAttr)
}
