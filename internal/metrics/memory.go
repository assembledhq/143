package metrics

import (
	"context"

	"go.opentelemetry.io/otel"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// MemoryMetrics holds OTel instruments for memory service observability.
type MemoryMetrics struct {
	InjectionsTotal  otelmetric.Int64Counter
	InjectedCount    otelmetric.Float64Histogram
	ReinforcedTotal  otelmetric.Int64Counter
	ReinforcedCount  otelmetric.Float64Histogram
}

// NewMemoryMetrics creates and registers memory OTel instruments.
func NewMemoryMetrics() (*MemoryMetrics, error) {
	meter := otel.Meter("github.com/assembledhq/143/memory")

	injectionsTotal, err := meter.Int64Counter("memory.context_injections",
		otelmetric.WithDescription("Total number of times memories were injected into agent context"),
		otelmetric.WithUnit("{injection}"),
	)
	if err != nil {
		return nil, err
	}

	injectedCount, err := meter.Float64Histogram("memory.injected_count",
		otelmetric.WithDescription("Number of memories selected per context injection"),
		otelmetric.WithUnit("{memory}"),
		otelmetric.WithExplicitBucketBoundaries(0, 1, 2, 5, 10, 20, 50),
	)
	if err != nil {
		return nil, err
	}

	reinforcedTotal, err := meter.Int64Counter("memory.reinforcements",
		otelmetric.WithDescription("Total number of memory reinforcement operations"),
		otelmetric.WithUnit("{reinforcement}"),
	)
	if err != nil {
		return nil, err
	}

	reinforcedCount, err := meter.Float64Histogram("memory.reinforced_count",
		otelmetric.WithDescription("Number of memories reinforced per PR approval"),
		otelmetric.WithUnit("{memory}"),
		otelmetric.WithExplicitBucketBoundaries(0, 1, 2, 5, 10, 20, 50),
	)
	if err != nil {
		return nil, err
	}

	return &MemoryMetrics{
		InjectionsTotal: injectionsTotal,
		InjectedCount:   injectedCount,
		ReinforcedTotal: reinforcedTotal,
		ReinforcedCount: reinforcedCount,
	}, nil
}

// RecordInjection records a memory context injection event.
func (m *MemoryMetrics) RecordInjection(ctx context.Context, count int) {
	m.InjectionsTotal.Add(ctx, 1)
	m.InjectedCount.Record(ctx, float64(count))
}

// RecordReinforcement records a memory reinforcement event.
func (m *MemoryMetrics) RecordReinforcement(ctx context.Context, count int) {
	m.ReinforcedTotal.Add(ctx, 1)
	m.ReinforcedCount.Record(ctx, float64(count))
}
