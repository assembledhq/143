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
	PhaseDuration    otelmetric.Float64Histogram
	PreviewMinutes   otelmetric.Float64Counter
	Concurrency      otelmetric.Int64UpDownCounter
	StartupFailures  otelmetric.Int64Counter
}

func getPreviewMetrics() *PreviewMetrics {
	previewOnce.Do(func() {
		meter := otel.Meter("github.com/assembledhq/143/preview")
		creates, _ := meter.Int64Counter("preview.branch.creates", otelmetric.WithUnit("{preview}"))
		idem, _ := meter.Int64Counter("preview.branch.idempotency_hits", otelmetric.WithUnit("{hit}"))
		opens, _ := meter.Int64Counter("preview.stable_link.opens", otelmetric.WithUnit("{open}"))
		checkout, _ := meter.Float64Histogram("preview.branch.checkout_duration", otelmetric.WithUnit("s"))
		phase, _ := meter.Float64Histogram("preview.branch.phase_duration", otelmetric.WithUnit("s"))
		minutes, _ := meter.Float64Counter("preview.branch.minutes", otelmetric.WithUnit("min"))
		concurrency, _ := meter.Int64UpDownCounter("preview.branch.concurrency", otelmetric.WithUnit("{preview}"))
		failures, _ := meter.Int64Counter("preview.branch.startup_failures", otelmetric.WithUnit("{failure}"))
		previewMetrics = &PreviewMetrics{
			CreatesTotal:     creates,
			IdempotencyHits:  idem,
			StableLinkOpens:  opens,
			CheckoutDuration: checkout,
			PhaseDuration:    phase,
			PreviewMinutes:   minutes,
			Concurrency:      concurrency,
			StartupFailures:  failures,
		}
	})
	return previewMetrics
}

func RecordBranchPreviewCreate(ctx context.Context, orgID, source, repo string) {
	m := getPreviewMetrics()
	if m == nil || m.CreatesTotal == nil {
		return
	}
	m.CreatesTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("preview.source", source),
		attribute.String("repository.full_name", repo),
	))
}

func RecordBranchPreviewIdempotencyHit(ctx context.Context, orgID, source string) {
	m := getPreviewMetrics()
	if m == nil || m.IdempotencyHits == nil {
		return
	}
	m.IdempotencyHits.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("preview.idempotency_source", source),
	))
}

func RecordStablePreviewLinkOpen(ctx context.Context, orgID, repo, linkType string, expired bool) {
	m := getPreviewMetrics()
	if m == nil || m.StableLinkOpens == nil {
		return
	}
	m.StableLinkOpens.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("repository.full_name", repo),
		attribute.String("preview.link_type", linkType),
		attribute.Bool("preview.expired", expired),
	))
}

func RecordBranchPreviewCheckout(ctx context.Context, orgID, source, repo string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.CheckoutDuration == nil {
		return
	}
	m.CheckoutDuration.Record(ctx, duration.Seconds(), otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("preview.source", source),
		attribute.String("repository.full_name", repo),
	))
}

func RecordBranchPreviewPhaseDuration(ctx context.Context, orgID, source, repo, phase string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.PhaseDuration == nil {
		return
	}
	m.PhaseDuration.Record(ctx, duration.Seconds(), otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("preview.source", source),
		attribute.String("repository.full_name", repo),
		attribute.String("preview.phase", phase),
	))
}

func AddBranchPreviewConcurrency(ctx context.Context, orgID, source, repo string, delta int64) {
	m := getPreviewMetrics()
	if m == nil || m.Concurrency == nil {
		return
	}
	m.Concurrency.Add(ctx, delta, otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("preview.source", source),
		attribute.String("repository.full_name", repo),
	))
}

func RecordBranchPreviewMinutes(ctx context.Context, orgID, source, repo string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.PreviewMinutes == nil || duration <= 0 {
		return
	}
	m.PreviewMinutes.Add(ctx, duration.Minutes(), otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("preview.source", source),
		attribute.String("repository.full_name", repo),
	))
}

func RecordBranchPreviewStartupFailure(ctx context.Context, orgID, source, repo, failureClass string) {
	m := getPreviewMetrics()
	if m == nil || m.StartupFailures == nil {
		return
	}
	m.StartupFailures.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("preview.source", source),
		attribute.String("repository.full_name", repo),
		attribute.String("preview.failure_class", failureClass),
	))
}
