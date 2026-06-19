package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	previewOnce    sync.Once
	previewMetrics *PreviewMetrics
)

type PreviewMetrics struct {
	CreatesTotal                    otelmetric.Int64Counter
	IdempotencyHits                 otelmetric.Int64Counter
	StableLinkOpens                 otelmetric.Int64Counter
	CheckoutDuration                otelmetric.Float64Histogram
	PhaseDuration                   otelmetric.Float64Histogram
	SessionPhaseDuration            otelmetric.Float64Histogram
	PreviewMinutes                  otelmetric.Float64Counter
	Concurrency                     otelmetric.Int64UpDownCounter
	StartupFailures                 otelmetric.Int64Counter
	DependencyCacheRestores         otelmetric.Int64Counter
	DependencyCacheSaves            otelmetric.Int64Counter
	DependencyCacheRestoreDuration  otelmetric.Float64Histogram
	DependencyCacheSaveDuration     otelmetric.Float64Histogram
	PackageManagerCacheRestores     otelmetric.Int64Counter
	PackageManagerCacheSaves        otelmetric.Int64Counter
	PackageManagerRestoreDuration   otelmetric.Float64Histogram
	PackageManagerSaveDuration      otelmetric.Float64Histogram
	BuildCacheRestores              otelmetric.Int64Counter
	BuildCacheSaves                 otelmetric.Int64Counter
	BuildCacheRestoreDuration       otelmetric.Float64Histogram
	BuildCacheSaveDuration          otelmetric.Float64Histogram
	PrewarmRuns                     otelmetric.Int64Counter
	PrewarmRunDuration              otelmetric.Float64Histogram
	SchedulerDecisions              otelmetric.Int64Counter
	IndexListDuration               otelmetric.Float64Histogram
	ResumeTotal                     otelmetric.Int64Counter
	AutoBuildsTotal                 otelmetric.Int64Counter
	AutoPoolSaturation              otelmetric.Int64Counter
	PRLaunchDecisions               otelmetric.Int64Counter
	SessionPrewarmDecisions         otelmetric.Int64Counter
	SessionPrewarmSkipped           otelmetric.Int64Counter
	SessionPrewarmClassifierLatency otelmetric.Float64Histogram
	SessionPrewarmCostSeconds       otelmetric.Float64Histogram
	SessionPrewarmOpenAfterPrewarm  otelmetric.Int64Counter
	SessionPrewarmClickToReady      otelmetric.Float64Histogram
	SessionPrewarmSpeculativeWaste  otelmetric.Int64Counter
	SessionPrewarmLiveMinutes       otelmetric.Float64Counter
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
		prLaunchDecisions, _ := meter.Int64Counter("preview.pr_launch.decisions", otelmetric.WithUnit("{decision}"))
		sessionPrewarmDecisions, _ := meter.Int64Counter("session_preview_prewarm_decisions_total", otelmetric.WithUnit("{decision}"))
		sessionPrewarmSkipped, _ := meter.Int64Counter("session_preview_prewarm_skipped_total", otelmetric.WithUnit("{skip}"))
		sessionPrewarmClassifierLatency, _ := meter.Float64Histogram("session_preview_classifier_latency_seconds", otelmetric.WithUnit("s"))
		sessionPrewarmCostSeconds, _ := meter.Float64Histogram("session_preview_prewarm_cost_seconds", otelmetric.WithUnit("s"))
		sessionPrewarmOpenAfterPrewarm, _ := meter.Int64Counter("session_preview_open_after_prewarm_total", otelmetric.WithUnit("{open}"))
		sessionPrewarmClickToReady, _ := meter.Float64Histogram("session_preview_click_to_ready_seconds", otelmetric.WithUnit("s"))
		sessionPrewarmSpeculativeWaste, _ := meter.Int64Counter("session_preview_speculative_waste_total", otelmetric.WithUnit("{waste}"))
		sessionPrewarmLiveMinutes, _ := meter.Float64Counter("session_preview_live_minutes_total", otelmetric.WithUnit("min"))
		previewMetrics = &PreviewMetrics{
			CreatesTotal:                    creates,
			IdempotencyHits:                 idem,
			StableLinkOpens:                 opens,
			CheckoutDuration:                checkout,
			PhaseDuration:                   phase,
			SessionPhaseDuration:            sessionPhase,
			PreviewMinutes:                  minutes,
			Concurrency:                     concurrency,
			StartupFailures:                 failures,
			DependencyCacheRestores:         depRestores,
			DependencyCacheSaves:            depSaves,
			DependencyCacheRestoreDuration:  depRestoreDuration,
			DependencyCacheSaveDuration:     depSaveDuration,
			PackageManagerCacheRestores:     pmRestores,
			PackageManagerCacheSaves:        pmSaves,
			PackageManagerRestoreDuration:   pmRestoreDuration,
			PackageManagerSaveDuration:      pmSaveDuration,
			BuildCacheRestores:              buildRestores,
			BuildCacheSaves:                 buildSaves,
			BuildCacheRestoreDuration:       buildRestoreDuration,
			BuildCacheSaveDuration:          buildSaveDuration,
			PrewarmRuns:                     prewarmRuns,
			PrewarmRunDuration:              prewarmRunDuration,
			SchedulerDecisions:              schedulerDecisions,
			IndexListDuration:               indexListDuration,
			ResumeTotal:                     resumeTotal,
			AutoBuildsTotal:                 autoBuildsTotal,
			AutoPoolSaturation:              autoPoolSaturation,
			PRLaunchDecisions:               prLaunchDecisions,
			SessionPrewarmDecisions:         sessionPrewarmDecisions,
			SessionPrewarmSkipped:           sessionPrewarmSkipped,
			SessionPrewarmClassifierLatency: sessionPrewarmClassifierLatency,
			SessionPrewarmCostSeconds:       sessionPrewarmCostSeconds,
			SessionPrewarmOpenAfterPrewarm:  sessionPrewarmOpenAfterPrewarm,
			SessionPrewarmClickToReady:      sessionPrewarmClickToReady,
			SessionPrewarmSpeculativeWaste:  sessionPrewarmSpeculativeWaste,
			SessionPrewarmLiveMinutes:       sessionPrewarmLiveMinutes,
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

func RecordSessionPreviewPrewarmDecision(ctx context.Context, orgID, mode, decision, source, reason string) {
	m := getPreviewMetrics()
	if m == nil || m.SessionPrewarmDecisions == nil {
		return
	}
	m.SessionPrewarmDecisions.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("mode", mode),
		attribute.String("decision", decision),
		attribute.String("source", source),
		attribute.String("reason", reason),
	))
}

func RecordSessionPreviewPrewarmSkipped(ctx context.Context, orgID, reason string) {
	m := getPreviewMetrics()
	if m == nil || m.SessionPrewarmSkipped == nil {
		return
	}
	m.SessionPrewarmSkipped.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("reason", reason),
	))
}

func RecordSessionPreviewClassifierLatency(ctx context.Context, orgID, phase string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.SessionPrewarmClassifierLatency == nil || duration <= 0 {
		return
	}
	m.SessionPrewarmClassifierLatency.Record(ctx, duration.Seconds(), otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("phase", phase),
	))
}

func RecordSessionPreviewPrewarmResume(ctx context.Context, orgID, repositoryID uuid.UUID) {
	m := getPreviewMetrics()
	if m == nil || m.ResumeTotal == nil {
		return
	}
	m.ResumeTotal.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("org.id", orgID.String()),
		attribute.String("repository.id", repositoryID.String()),
		attribute.String("preview.source", "session_prewarm"),
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

func RecordPRPreviewLaunchDecision(ctx context.Context, orgID, repo, intent, action, reason string, autoOpen bool) {
	m := getPreviewMetrics()
	if m == nil || m.PRLaunchDecisions == nil {
		return
	}
	m.PRLaunchDecisions.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("repository.full_name", repo),
		attribute.String("preview.intent", intent),
		attribute.String("preview.launch_action", action),
		attribute.String("preview.launch_reason", reason),
		attribute.Bool("preview.auto_open", autoOpen),
	))
}

// RecordSessionPrewarmCost records how long a speculative prewarm job took.
// phase is "session_start" or "post_turn"; decision is "cache" or "warm_build".
func RecordSessionPrewarmCost(ctx context.Context, orgID, decision, phase string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.SessionPrewarmCostSeconds == nil || duration <= 0 {
		return
	}
	m.SessionPrewarmCostSeconds.Record(ctx, duration.Seconds(), otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("decision", decision),
		attribute.String("phase", phase),
	))
}

// RecordSessionPrewarmOpenAfterPrewarm records that a user opened a preview
// that had been prewarmed speculatively.
func RecordSessionPrewarmOpenAfterPrewarm(ctx context.Context, orgID, decision string) {
	m := getPreviewMetrics()
	if m == nil || m.SessionPrewarmOpenAfterPrewarm == nil {
		return
	}
	m.SessionPrewarmOpenAfterPrewarm.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("decision", decision),
	))
}

// RecordSessionPrewarmClickToReady records the elapsed time from the user
// clicking Preview to the preview being ready. path distinguishes the startup
// path: "live_reuse", "warm_resume", "prewarm_cache", "prewarm_warm", or
// "cold_start".
func RecordSessionPrewarmClickToReady(ctx context.Context, orgID, path string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.SessionPrewarmClickToReady == nil || duration <= 0 {
		return
	}
	m.SessionPrewarmClickToReady.Record(ctx, duration.Seconds(), otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("path", path),
	))
}

// RecordSessionPrewarmSpeculativeWaste records a warm build that was never
// opened by the user (stale or superseded). reason is the cause.
func RecordSessionPrewarmSpeculativeWaste(ctx context.Context, orgID, reason string) {
	m := getPreviewMetrics()
	if m == nil || m.SessionPrewarmSpeculativeWaste == nil {
		return
	}
	m.SessionPrewarmSpeculativeWaste.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("reason", reason),
	))
}

// RecordSessionPrewarmLiveMinutes records sandbox runtime minutes consumed by
// a speculative warm build. This is always attributed to initiator=speculative.
func RecordSessionPrewarmLiveMinutes(ctx context.Context, orgID string, duration time.Duration) {
	m := getPreviewMetrics()
	if m == nil || m.SessionPrewarmLiveMinutes == nil || duration <= 0 {
		return
	}
	m.SessionPrewarmLiveMinutes.Add(ctx, duration.Minutes(), otelmetric.WithAttributes(
		attribute.String("org.id", orgID),
		attribute.String("initiator", "speculative"),
	))
}
