package metrics

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// PreviewInfrastructureCleanupMetrics reports local pending teardown and
// reconciliation outcomes without resource identities or high-cardinality
// tenant, preview, container, or volume labels.
type PreviewInfrastructureCleanupMetrics struct {
	pending otelmetric.Int64Gauge
	passes  otelmetric.Int64Counter
}

func NewPreviewInfrastructureCleanupMetrics() (*PreviewInfrastructureCleanupMetrics, error) {
	return newPreviewInfrastructureCleanupMetrics(otel.Meter("github.com/assembledhq/143/preview"))
}

func newPreviewInfrastructureCleanupMetrics(meter otelmetric.Meter) (*PreviewInfrastructureCleanupMetrics, error) {
	pending, err := meter.Int64Gauge("preview.infrastructure.cleanup.pending", otelmetric.WithUnit("{container}"))
	if err != nil {
		return nil, fmt.Errorf("create preview infrastructure pending-cleanup gauge: %w", err)
	}
	passes, err := meter.Int64Counter("preview.infrastructure.cleanup.passes", otelmetric.WithUnit("{pass}"))
	if err != nil {
		return nil, fmt.Errorf("create preview infrastructure cleanup counter: %w", err)
	}
	return &PreviewInfrastructureCleanupMetrics{pending: pending, passes: passes}, nil
}

func (m *PreviewInfrastructureCleanupMetrics) Record(ctx context.Context, pending int64, failed bool) {
	if m == nil {
		return
	}
	m.pending.Record(ctx, pending)
	result := "success"
	if failed {
		result = "error"
	}
	m.passes.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("result", result)))
}
