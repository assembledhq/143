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
	CreatesTotal                   otelmetric.Int64Counter
	IdempotencyHits                otelmetric.Int64Counter
	StableLinkOpens                otelmetric.Int64Counter
	CheckoutDuration               otelmetric.Float64Histogram
	PhaseDuration                  otelmetric.Float64Histogram
	SessionPhaseDuration           otelmetric.Float64Histogram
	PreviewMinutes                 otelmetric.Float64Counter
	Concurrency                    otelmetric.Int64UpDownCounter
	StartupFailures                otelmetric.Int64Counter
	DependencyCacheRestores        otelmetric.Int64Counter
	DependencyCacheSaves           otelmetric.Int64Counter
	DependencyCacheRestoreDuration otelmetric.Float64Histogram
	DependencyCacheSaveDuration    otelmetric.Float64Histogram
	PackageManagerCacheRestores    otelmetric.Int64Counter
	PackageManagerCacheSaves       otelmetric.Int64Counter
	PackageManagerRestoreDuration  otelmetric.Float64Histogram
	PackageManagerSaveDuration     otelmetric.Float64Histogram
	BuildCacheRestores             otelmetric.Int64Counter
	BuildCacheSaves                otelmetric.Int64Counter
	BuildCacheRestoreDuration      otelmetric.Float64Histogram
	BuildCacheSaveDuration         otelmetric.Float64Histogram
	PrewarmRuns                    otelmetric.Int64Counter
	PrewarmRunDuration             otelmetric.Float64Histogram
	SchedulerDecisions             otelmetric.Int64Counter
	IndexListDuration              otelmetric.Float64Histogram
	ResumeTotal                    otelmetric.Int64Counter
	AutoBuildsTotal                otelmetric.Int64Counter
	AutoPoolSaturation             otelmetric.Int64Counter
}

func getPreviewMetrics() *PreviewMetrics {
	previewOnce.Do(func() {
		meter := otel.Meter("github.com/assembledhq/143/preview")
		creates, _ := meter.Int64Counter("preview.branch.creates", otelmetric.WithUnit("{preview}"))
		idem, _ := meter.Int64Counter("preview.branch.idempotency_hits", otelmetric.WithUnit("{hit}"))
		opens, _ := meter.Int64Counter("preview.stable_link.opens", otelmetric.WithUnit("{open}"))
		checkout, _ := meter.Float64Histogram("preview.branch.checkout_duration", otelmetric.WithUnit("s"))
		phase, _ := meter.Float64Histogram("preview.branch.phase_duration", otelmetric.WithUnit("s"))
		sessionPhase, _ := meter.Float64Histogram("preview.session.phase_duration", otelmetric.WithUnit("s"))
		minutes, _ := meter.Float64Counter("preview.branch.minutes", otelmetric.WithUnit("min"))
		concurrency, _ := meter.Int64UpDownCounter("preview.branch.concurrency", otelmetric.WithUnit("{preview}"))
		failures, _ := meter.Int64Counter("preview.branch.startup_failures", otelmetric.WithUnit("{failure}"))
		depRestores, _ := meter.Int64Counter("preview.session.dependency_cache.restores", otelmetric.WithUnit("{restore}"))
		depSaves, _ := meter.Int64Counter("preview.session.dependency_cache.saves", otelmetric.WithUnit("{save}"))
		depRestoreDuration, _ := meter.Float64Histogram("preview.session.dependency_cache.restore_duration", otelmetric.WithUnit("s"))
		depSaveDuration, _ := meter.Float64Histogram("preview.session.dependency_cache.save_duration", otelmetric.WithUnit("s"))
		pmRestores, _ := meter.Int64Counter("preview.session.package_manager_cache.restores", otelmetric.WithUnit("{restore}"))
		pmSaves, _ := meter.Int64Counter("preview.session.package_manager_cache.saves", otelmetric.WithUnit("{save}"))
		pmRestoreDuration, _ := meter.Float64Histogram("preview.session.package_manager_cache.restore_duration", otelmetric.WithUnit("s"))
		pmSaveDuration, _ := meter.Float64Histogram("preview.session.package_manager_cache.save_duration", otelmetric.WithUnit("s"))
		buildRestores, _ := meter.Int64Counter("preview.session.build_cache.restores", otelmetric.WithUnit("{restore}"))
		buildSaves, _ := meter.Int64Counter("preview.session.build_cache.saves", otelmetric.WithUnit("{save}"))
		buildRestoreDuration, _ := meter.Float64Histogram("preview.session.build_cache.restore_duration", otelmetric.WithUnit("s"))
		buildSaveDuration, _ := meter.Float64Histogram("preview.session.build_cache.save_duration", otelmetric.WithUnit("s"))
		prewarmRuns, _ := meter.Int64Counter("preview.cache_prewarm.runs", otelmetric.WithUnit("{run}"))
		prewarmRunDuration, _ := meter.Float64Histogram("preview.cache_prewarm.run_duration", otelmetric.WithUnit("s"))
		schedulerDecisions, _ := meter.Int64Counter("preview.session.dependency_cache.scheduler_decisions", otelmetric.WithUnit("{decision}"))
		indexListDuration, _ := meter.Float64Histogram("preview.index.list_duration", otelmetric.WithUnit("s"))
		resumeTotal, _ := meter.Int64Counter("preview.resume.total", otelmetric.WithUnit("{resume}"))
		autoBuildsTotal, _ := meter.Int64Counter("preview.auto.builds_total", otelmetric.WithUnit("{build}"))
		autoPoolSaturation, _ := meter.Int64Counter("preview.auto.pool_saturation", otelmetric.WithUnit("{event}"))
		previewMetrics = &PreviewMetrics{
			CreatesTotal:                   creates,
			IdempotencyHits:                idem,
			StableLinkOpens:                opens,
			CheckoutDuration:               checkout,
			PhaseDuration:                  phase,
			SessionPhaseDuration:           sessionPhase,
			PreviewMinutes:                 minutes,
			Concurrency:                    concurrency,
			StartupFailures:                failures,
			DependencyCacheRestores:        depRestores,
			DependencyCacheSaves:           depSaves,
			DependencyCacheRestoreDuration: depRestoreDuration,
			DependencyCacheSaveDuration:    depSaveDuration,
			PackageManagerCacheRestores:    pmRestores,
			PackageManagerCacheSaves:       pmSaves,
			PackageManagerRestoreDuration:  pmRestoreDuration,
			PackageManagerSaveDuration:     pmSaveDuration,
			BuildCacheRestores:             buildRestores,
			BuildCacheSaves:                buildSaves,
			BuildCacheRestoreDuration:      buildRestoreDuration,
			BuildCacheSaveDuration:         buildSaveDuration,
			PrewarmRuns:                    prewarmRuns,
			PrewarmRunDuration:             prewarmRunDuration,
			SchedulerDecisions:             schedulerDecisions,
			IndexListDuration:              indexListDuration,
			ResumeTotal:                    resumeTotal,
			AutoBuildsTotal:                autoBuildsTotal,
			AutoPoolSaturation:             autoPoolSaturation,
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

func RecordSessionPreviewPhaseDuration(ctx context.Context, orgID, phase string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.SessionPhaseDuration == nil || duration <= 0 {
		return
	}
	m.SessionPhaseDuration.Record(ctx, duration.Seconds(), otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
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

func RecordSessionDependencyCacheRestore(ctx context.Context, orgID, result string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.DependencyCacheRestores == nil {
		return
	}
	attrs := otelmetric.WithAttributes(attribute.String("org.id", orgID), attribute.String("result", result))
	m.DependencyCacheRestores.Add(ctx, 1, attrs)
	if m.DependencyCacheRestoreDuration != nil && duration > 0 {
		m.DependencyCacheRestoreDuration.Record(ctx, duration.Seconds(), attrs)
	}
}

func RecordSessionDependencyCacheSave(ctx context.Context, orgID, result string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.DependencyCacheSaves == nil {
		return
	}
	attrs := otelmetric.WithAttributes(attribute.String("org.id", orgID), attribute.String("result", result))
	m.DependencyCacheSaves.Add(ctx, 1, attrs)
	if m.DependencyCacheSaveDuration != nil && duration > 0 {
		m.DependencyCacheSaveDuration.Record(ctx, duration.Seconds(), attrs)
	}
}

func RecordSessionPackageManagerCacheRestore(ctx context.Context, orgID, result string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.PackageManagerCacheRestores == nil {
		return
	}
	attrs := otelmetric.WithAttributes(attribute.String("org.id", orgID), attribute.String("result", result))
	m.PackageManagerCacheRestores.Add(ctx, 1, attrs)
	if m.PackageManagerRestoreDuration != nil && duration > 0 {
		m.PackageManagerRestoreDuration.Record(ctx, duration.Seconds(), attrs)
	}
}

func RecordSessionPackageManagerCacheSave(ctx context.Context, orgID, result string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.PackageManagerCacheSaves == nil {
		return
	}
	attrs := otelmetric.WithAttributes(attribute.String("org.id", orgID), attribute.String("result", result))
	m.PackageManagerCacheSaves.Add(ctx, 1, attrs)
	if m.PackageManagerSaveDuration != nil && duration > 0 {
		m.PackageManagerSaveDuration.Record(ctx, duration.Seconds(), attrs)
	}
}

func RecordSessionBuildCacheRestore(ctx context.Context, orgID, result string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.BuildCacheRestores == nil {
		return
	}
	attrs := otelmetric.WithAttributes(attribute.String("org.id", orgID), attribute.String("result", result))
	m.BuildCacheRestores.Add(ctx, 1, attrs)
	if m.BuildCacheRestoreDuration != nil && duration > 0 {
		m.BuildCacheRestoreDuration.Record(ctx, duration.Seconds(), attrs)
	}
}

func RecordSessionBuildCacheSave(ctx context.Context, orgID, result string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.BuildCacheSaves == nil {
		return
	}
	attrs := otelmetric.WithAttributes(attribute.String("org.id", orgID), attribute.String("result", result))
	m.BuildCacheSaves.Add(ctx, 1, attrs)
	if m.BuildCacheSaveDuration != nil && duration > 0 {
		m.BuildCacheSaveDuration.Record(ctx, duration.Seconds(), attrs)
	}
}

func RecordPreviewCachePrewarmRun(ctx context.Context, orgID, source, status string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.PrewarmRuns == nil {
		return
	}
	attrs := otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("preview.source", source),
		attribute.String("status", status),
	)
	m.PrewarmRuns.Add(ctx, 1, attrs)
	if m.PrewarmRunDuration != nil && duration > 0 {
		m.PrewarmRunDuration.Record(ctx, duration.Seconds(), attrs)
	}
}

func RecordSessionDependencyCacheSchedulerDecision(ctx context.Context, orgID, decision string) {
	m := getPreviewMetrics()
	if m == nil || m.SchedulerDecisions == nil {
		return
	}
	m.SchedulerDecisions.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("decision", decision),
	))
}

func RecordPreviewIndexListDuration(ctx context.Context, orgID, scope string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.IndexListDuration == nil || duration <= 0 {
		return
	}
	m.IndexListDuration.Record(ctx, duration.Seconds(), otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("preview.scope", scope),
	))
}

func RecordPreviewResume(ctx context.Context, orgID, path string) {
	m := getPreviewMetrics()
	if m == nil || m.ResumeTotal == nil {
		return
	}
	m.ResumeTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("path", path),
	))
}

func RecordPreviewAutoBuild(ctx context.Context, orgID, mode, result string) {
	m := getPreviewMetrics()
	if m == nil || m.AutoBuildsTotal == nil {
		return
	}
	m.AutoBuildsTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("mode", mode),
		attribute.String("result", result),
	))
}

func RecordPreviewAutoPoolSaturation(ctx context.Context, orgID string) {
	m := getPreviewMetrics()
	if m == nil || m.AutoPoolSaturation == nil {
		return
	}
	m.AutoPoolSaturation.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("org.id", orgID)))
}
