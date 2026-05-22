package metrics

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	previewOnce    sync.Once
	previewMetrics *PreviewMetrics
)

type PreviewMetrics struct {
	CreatesTotal     otelmetric.Int64Counter
	IdempotencyHits  otelmetric.Int64Counter
	StableLinkOpens  otelmetric.Int64Counter
	CheckoutDuration otelmetric.Float64Histogram
	StartupFailures  otelmetric.Int64Counter
}

func getPreviewMetrics() *PreviewMetrics {
	previewOnce.Do(func() {
		meter := otel.Meter("github.com/assembledhq/143/preview")
		creates, _ := meter.Int64Counter("preview.branch.creates", otelmetric.WithUnit("{preview}"))
		idem, _ := meter.Int64Counter("preview.branch.idempotency_hits", otelmetric.WithUnit("{hit}"))
		opens, _ := meter.Int64Counter("preview.stable_link.opens", otelmetric.WithUnit("{open}"))
		checkout, _ := meter.Float64Histogram("preview.branch.checkout_duration", otelmetric.WithUnit("s"))
		failures, _ := meter.Int64Counter("preview.branch.startup_failures", otelmetric.WithUnit("{failure}"))
		previewMetrics = &PreviewMetrics{
			CreatesTotal:     creates,
			IdempotencyHits:  idem,
			StableLinkOpens:  opens,
			CheckoutDuration: checkout,
			StartupFailures:  failures,
		}
	})
	return previewMetrics
}

func RecordBranchPreviewCreate(ctx context.Context, source, repo string) {
	m := getPreviewMetrics()
	if m == nil || m.CreatesTotal == nil {
		return
	}
	m.CreatesTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("preview.source", source),
		attribute.String("repository.full_name", repo),
	))
}

func RecordBranchPreviewIdempotencyHit(ctx context.Context, source string) {
	m := getPreviewMetrics()
	if m == nil || m.IdempotencyHits == nil {
		return
	}
	m.IdempotencyHits.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("preview.idempotency_source", source)))
}

func RecordStablePreviewLinkOpen(ctx context.Context, linkType string, expired bool) {
	m := getPreviewMetrics()
	if m == nil || m.StableLinkOpens == nil {
		return
	}
	m.StableLinkOpens.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("preview.link_type", linkType),
		attribute.Bool("preview.expired", expired),
	))
}

func RecordBranchPreviewCheckout(ctx context.Context, source, repo string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.CheckoutDuration == nil {
		return
	}
	m.CheckoutDuration.Record(ctx, duration.Seconds(), otelmetric.WithAttributes(
		attribute.String("preview.source", source),
		attribute.String("repository.full_name", repo),
	))
}

func RecordBranchPreviewStartupFailure(ctx context.Context, source, repo, failureClass string) {
	m := getPreviewMetrics()
	if m == nil || m.StartupFailures == nil {
		return
	}
	m.StartupFailures.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("preview.source", source),
		attribute.String("repository.full_name", repo),
		attribute.String("preview.failure_class", failureClass),
	))
}
