package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/storage"
)

// OrphanCloser closes container usage events that were never stopped.
type OrphanCloser interface {
	CloseOrphans(ctx context.Context, startedBefore time.Time) (int64, error)
}

// UsageRoller computes and upserts hourly usage rollups.
type UsageRoller interface {
	RollupAllOrgs(ctx context.Context, hour time.Time) error
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	GetLatestRollupHour(ctx context.Context) (time.Time, error)
}

// PreviewStopper tears down an active preview for a given session, if any.
// The reaper calls this before expiring a snapshot so that a preview still
// holding the sandbox container is torn down cleanly (via the hold-aware
// destroy path in the preview manager) before the snapshot is deleted.
//
// Implemented by *preview.Manager.
type PreviewStopper interface {
	StopActivePreviewForSession(ctx context.Context, orgID, sessionID uuid.UUID) (bool, error)
}

// RuntimeStalledJobTerminator terminalizes active session jobs after the
// runtime-control reaper has made the owning session terminal.
type RuntimeStalledJobTerminator interface {
	DeadLetterSessionJobs(ctx context.Context, orgID, sessionID uuid.UUID, errMsg string) (int64, error)
}

// SessionReaper periodically cleans up stale sessions and expired snapshots
// in seven phases:
//   - Phase 0: Fail sessions stuck in pending for longer than maxPendingAge
//   - Phase 0.4: Fail sessions whose runtime controller missed an already
//     expired soft deadline or left a stop request in-flight past its grace
//     window
//   - Phase 0.5: Fail sessions stuck in running for longer than maxRunningAge
//     (safety net for orphaned sessions when the worker handler timeout path
//     didn't run, e.g. worker crash or DB write failure during failure handling)
//   - Phase 0.5b: Fail threads stuck in running for longer than maxRunningAge
//     (defense-in-depth above the orchestrator/handler thread.status resets,
//     which can miss when ctx is cancelled by worker drain mid-shutdown)
//   - Phase 1: Transition idle sessions to completed (keep snapshots)
//   - Phase 2: Delete snapshots that have exceeded the max snapshot age
//   - Phase 3: Close orphaned container usage events for billing accuracy
//   - Phase 4: Roll up hourly usage data and clean up old rollup rows
type SessionReaper struct {
	sessions         StaleSessionLister
	threads          StuckThreadLister // nil-safe — Phase 0.5b disabled if nil
	snapshotStore    storage.SnapshotStore
	orphanCloser     OrphanCloser   // nil-safe — billing orphan cleanup disabled if nil
	usageRoller      UsageRoller    // nil-safe — usage rollup disabled if nil
	previewStopper   PreviewStopper // nil-safe — if unset, reaper skips preview teardown before snapshot expiry
	jobTerminator    RuntimeStalledJobTerminator
	maxIdleAge       time.Duration
	maxPendingAge    time.Duration
	runtimeStallAge  time.Duration
	maxRunningAge    time.Duration
	maxSnapshotAge   time.Duration
	interval         time.Duration
	logger           zerolog.Logger
	lastRetentionRun time.Time // throttles retention cleanup to once per hour
	lastRollupHour   time.Time // watermark: last hour successfully rolled up; written only after wg.Wait() in the single reaper goroutine
}

// StaleSessionLister is the subset of the session store used by the reaper.
type StaleSessionLister interface {
	ListStaleIdleSessions(ctx context.Context, olderThan time.Time) ([]models.Session, error)
	ListStalePendingSessions(ctx context.Context, createdBefore time.Time) ([]models.Session, error)
	ListStaleRunningSessions(ctx context.Context, startedBefore time.Time) ([]models.Session, error)
	ListRuntimeControlStalledSessions(ctx context.Context, deadlineBefore, stopRequestedBefore time.Time) ([]models.Session, error)
	ListExpiredSnapshots(ctx context.Context, olderThan time.Time) ([]models.Session, error)
	UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status string) error
	UpdateFailure(ctx context.Context, orgID, runID uuid.UUID, explanation, category string, nextSteps []string, retryAdvised bool) error
	UpdateSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, state string) error
}

// StuckThreadLister is the subset of the session-thread store used by the
// reaper's Phase 0.5b. Wiring is optional via WithStuckThreadLister; when
// unset, the reaper logs a startup notice and skips the phase.
type StuckThreadLister interface {
	ListStuckRunningThreads(ctx context.Context, startedBefore time.Time) ([]models.SessionThread, error)
	UpdateResult(ctx context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus, result *models.SessionResult) error
}

type runtimeStalledThreadFailer interface {
	FailRunningBySession(ctx context.Context, orgID, sessionID uuid.UUID, result *models.SessionResult) (int64, error)
}

// SessionReaperOption configures optional SessionReaper dependencies.
type SessionReaperOption func(*SessionReaper)

// WithOrphanCloser enables billing orphan cleanup in the reaper.
func WithOrphanCloser(oc OrphanCloser) SessionReaperOption {
	return func(r *SessionReaper) { r.orphanCloser = oc }
}

// WithUsageRoller enables hourly usage rollup in the reaper.
func WithUsageRoller(ur UsageRoller) SessionReaperOption {
	return func(r *SessionReaper) { r.usageRoller = ur }
}

// WithPreviewStopper wires in the preview manager so the reaper can tear
// down previews before expiring their sessions' snapshots. Without this,
// a snapshot expiry on a session whose preview is still running would
// leave behind an orphan container.
func WithPreviewStopper(ps PreviewStopper) SessionReaperOption {
	return func(r *SessionReaper) { r.previewStopper = ps }
}

// WithStuckThreadLister enables Phase 0.5b — failing threads stuck in
// status='running' past maxRunningAge. Defense-in-depth above the
// orchestrator/handler thread.status resets, which can miss when ctx is
// cancelled by worker drain mid-shutdown. Without this option, the phase
// is skipped (the session-level Phase 0.5 still runs).
func WithStuckThreadLister(t StuckThreadLister) SessionReaperOption {
	return func(r *SessionReaper) { r.threads = t }
}

// WithRuntimeStalledJobTerminator lets Phase 0.4 close active run_agent and
// continue_session jobs for sessions it terminalizes.
func WithRuntimeStalledJobTerminator(j RuntimeStalledJobTerminator) SessionReaperOption {
	return func(r *SessionReaper) { r.jobTerminator = j }
}

// NewSessionReaper creates a reaper that runs every interval, cleaning up
// sessions idle for longer than maxIdleAge and snapshots older than maxSnapshotAge.
// defaultMaxPendingAge is the maximum time a session can stay in "pending"
// before the reaper considers it stuck and marks it as failed.
const defaultMaxPendingAge = 10 * time.Minute

const defaultRuntimeStallAge = 2 * time.Minute

// reaperPreviewStopTimeout is the deadline the reaper gives
// StopActivePreviewForSession before moving on to delete the snapshot blob.
// Must be generous enough for the preview's hold-aware destroy path
// (ReleasePreviewHold + FinalizeContainerDestroy + provider.Destroy) to
// finish on a shared docker daemon that may be briefly busy with other
// lifecycle calls, but short enough that a single wedged preview doesn't
// stall Phase 2 and block every other snapshot expiry in the tick.
const reaperPreviewStopTimeout = 60 * time.Second

// defaultMaxRunningAge is the safety-net cutoff for sessions stuck in
// "running". It must be at or above minRunningAgeFloor — otherwise an
// admin who legitimately raises their org's session timeout to the
// allowed maximum would have sessions killed by the reaper before the
// orchestrator's own timeout fires. Set a comfortable margin above the
// floor so raising MaxMaxSessionDurationSeconds in models doesn't
// silently trip the constructor's warning.
const defaultMaxRunningAge = 150 * time.Minute // 2h30m; floor is ~2h17m

// minRunningAgeFloor is the hard floor for the reaper's running-session
// cutoff. It's derived from the maximum per-org session timeout
// (OrgSettings.MaxMaxSessionDurationSeconds) plus HandlerCleanupBuffer plus
// a 15-minute safety margin for orchestrator bookkeeping. If an operator
// configures SESSION_MAX_RUNNING_AGE below this, NewSessionReaper logs a
// warning and raises it — otherwise an admin who legitimately raises their
// org's timeout above SESSION_MAX_RUNNING_AGE would have sessions killed
// by the reaper before their own configured timeout fires.
var minRunningAgeFloor = time.Duration(models.MaxMaxSessionDurationSeconds)*time.Second + HandlerCleanupBuffer + 15*time.Minute

func NewSessionReaper(sessions StaleSessionLister, snapshotStore storage.SnapshotStore, maxIdleAge, maxSnapshotAge, interval time.Duration, logger zerolog.Logger, opts ...SessionReaperOption) *SessionReaper {
	r := &SessionReaper{
		sessions:        sessions,
		snapshotStore:   snapshotStore,
		maxIdleAge:      maxIdleAge,
		maxPendingAge:   defaultMaxPendingAge,
		runtimeStallAge: defaultRuntimeStallAge,
		maxRunningAge:   defaultMaxRunningAge,
		maxSnapshotAge:  maxSnapshotAge,
		interval:        interval,
		logger:          logger,
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.maxRunningAge < minRunningAgeFloor {
		logger.Warn().
			Dur("configured", r.maxRunningAge).
			Dur("floor", minRunningAgeFloor).
			Msg("reaper: max_running_age is below the safe floor; raising to protect long-running sessions from premature reaping")
		r.maxRunningAge = minRunningAgeFloor
	}
	return r
}

// WithMaxRunningAge overrides the max running-state age before the reaper
// considers a session stuck and fails it.
func WithMaxRunningAge(d time.Duration) SessionReaperOption {
	return func(r *SessionReaper) {
		if d > 0 {
			r.maxRunningAge = d
		}
	}
}

// Run starts the reaper loop. It blocks until ctx is cancelled.
func (r *SessionReaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.logger.Info().
		Dur("interval", r.interval).
		Dur("max_pending", r.maxPendingAge).
		Dur("runtime_stall", r.runtimeStallAge).
		Dur("max_running", r.maxRunningAge).
		Dur("max_idle", r.maxIdleAge).
		Dur("max_snapshot_age", r.maxSnapshotAge).
		Bool("stuck_thread_phase_enabled", r.threads != nil).
		Msg("session reaper started")

	for {
		select {
		case <-ctx.Done():
			r.logger.Info().Msg("snapshot reaper stopped")
			return
		case <-ticker.C:
			r.reap(ctx)
		}
	}
}

// FailureCategoryStuckPending is the failure category for sessions that timed
// out in the pending state without ever starting.
const FailureCategoryStuckPending = "stuck_pending"

// FailureCategoryStuckRunning is the failure category for sessions that
// started but never reached a terminal status — typically because the worker
// handler context timeout path didn't execute (crashed worker, DB write
// failure during failure bookkeeping, or a new silent-failure code path).
const FailureCategoryStuckRunning = "stuck_running"

// FailureCategoryRuntimeControlStalled is for sessions whose per-run runtime
// controller missed or could not complete its own stop policy. This is narrower
// than stuck_running and should page earlier because it means an active worker
// may still be renewing the job lease while no useful agent work is happening.
const FailureCategoryRuntimeControlStalled = "runtime_control_stalled"

// FailureCategoryStuckThread is the failure category for threads stuck in
// status='running' past maxRunningAge. Distinguished from StuckRunning so
// alerts on the two phases can be tuned independently — a stuck thread is
// usually traceable to a continue_session dead-letter that the orchestrator
// or handler couldn't unwind, while a stuck session is usually a worker
// crash mid-RunAgent.
const FailureCategoryStuckThread = "stuck_thread"

func (r *SessionReaper) reap(ctx context.Context) {
	// Phase 0: Fail sessions stuck in pending with no active job.
	pendingCutoff := time.Now().Add(-r.maxPendingAge)
	stalePending, err := r.sessions.ListStalePendingSessions(ctx, pendingCutoff)
	if err != nil {
		r.logger.Error().Err(err).Msg("reaper: failed to list stale pending sessions")
	} else {
		for _, s := range stalePending {
			if err := r.sessions.UpdateStatus(ctx, s.OrgID, s.ID, string(models.SessionStatusFailed)); err != nil {
				r.logger.Error().Err(err).Str("session_id", s.ID.String()).Msg("reaper: failed to mark stale pending session as failed")
				continue
			}
			explanation := "This session was unable to start within the expected time. This can happen when the system is under heavy load or if there was an internal error processing the request."
			nextSteps := []string{
				"Try running the session again",
				"Check if you have other sessions currently running that may be consuming capacity",
			}
			if err := r.sessions.UpdateFailure(ctx, s.OrgID, s.ID, explanation, FailureCategoryStuckPending, nextSteps, true); err != nil {
				r.logger.Error().Err(err).Str("session_id", s.ID.String()).Msg("reaper: failed to update failure details for stale pending session")
			}
			r.logger.Warn().
				Str("session_id", s.ID.String()).
				Str("org_id", s.OrgID.String()).
				Time("created_at", s.CreatedAt).
				Msg("reaper: failed stale pending session")
		}
	}

	// Phase 0.4: Fail sessions whose runtime controller has already missed
	// an expired deadline or left a stop request past its persisted stop-after
	// deadline. Unlike the broad maxRunningAge safety net, this is keyed off
	// the row's own runtime budget fields, so it can act quickly without
	// cutting off healthy long-running sessions.
	now := time.Now()
	runtimeDeadlineCutoff := now.Add(-r.runtimeStallAge)
	runtimeStalled, err := r.sessions.ListRuntimeControlStalledSessions(ctx, runtimeDeadlineCutoff, now)
	if err != nil {
		r.logger.Error().Err(err).Msg("reaper: failed to list runtime-control stalled sessions")
	} else {
		for _, s := range runtimeStalled {
			if err := r.sessions.UpdateStatus(ctx, s.OrgID, s.ID, string(models.SessionStatusFailed)); err != nil {
				r.logger.Error().Err(err).Str("session_id", s.ID.String()).Msg("reaper: failed to mark runtime-control stalled session as failed")
				continue
			}
			explanation := "This session crossed its runtime stop deadline, but the worker kept the job alive without completing shutdown. The platform stopped it so it does not consume capacity indefinitely."
			nextSteps := []string{
				"Retry the session; a fresh worker should start from the latest durable state when available",
				"If this repeats, inspect worker logs for interactive command cancellation and Docker exec shutdown failures",
			}
			if err := r.sessions.UpdateFailure(ctx, s.OrgID, s.ID, explanation, FailureCategoryRuntimeControlStalled, nextSteps, true); err != nil {
				r.logger.Error().Err(err).Str("session_id", s.ID.String()).Msg("reaper: failed to update failure details for runtime-control stalled session")
			}
			if r.jobTerminator != nil {
				errMsg := fmt.Sprintf("%s: session %s crossed runtime stop deadline", FailureCategoryRuntimeControlStalled, s.ID.String())
				affected, err := r.jobTerminator.DeadLetterSessionJobs(ctx, s.OrgID, s.ID, errMsg)
				if err != nil {
					r.logger.Error().Err(err).Str("session_id", s.ID.String()).Msg("reaper: failed to dead-letter runtime-control stalled session jobs")
				} else if affected > 0 {
					r.logger.Warn().
						Str("session_id", s.ID.String()).
						Str("org_id", s.OrgID.String()).
						Int64("jobs_dead_lettered", affected).
						Msg("reaper: dead-lettered runtime-control stalled session jobs")
				}
			}
			if threadFailer, ok := r.threads.(runtimeStalledThreadFailer); ok {
				errMsg := "Session stopped after crossing its runtime stop deadline."
				category := FailureCategoryRuntimeControlStalled
				result := &models.SessionResult{
					Error:           &errMsg,
					FailureCategory: &category,
				}
				affected, err := threadFailer.FailRunningBySession(ctx, s.OrgID, s.ID, result)
				if err != nil {
					r.logger.Error().Err(err).Str("session_id", s.ID.String()).Msg("reaper: failed to fail runtime-control stalled session threads")
				} else if affected > 0 {
					r.logger.Warn().
						Str("session_id", s.ID.String()).
						Str("org_id", s.OrgID.String()).
						Int64("threads_failed", affected).
						Msg("reaper: failed runtime-control stalled session threads")
				}
			}
			event := r.logger.Error().
				Str("session_id", s.ID.String()).
				Str("org_id", s.OrgID.String()).
				Str("runtime_stop_reason", string(s.RuntimeStopReason))
			if s.RuntimeLastProgressAt != nil {
				event = event.Time("runtime_last_progress_at", *s.RuntimeLastProgressAt)
			}
			if s.RuntimeSoftDeadlineAt != nil {
				event = event.Time("runtime_soft_deadline_at", *s.RuntimeSoftDeadlineAt)
			}
			if s.RuntimeGracefulStopAt != nil {
				event = event.Time("runtime_graceful_stop_at", *s.RuntimeGracefulStopAt)
			}
			event.Msg("reaper: failed runtime-control stalled session")
		}
	}

	// Phase 0.5: Fail sessions stuck in "running" past the max running age.
	// Safety net for sessions the handler timeout path couldn't mark failed
	// (worker crash, DB outage during failRun, etc.). Logged at Error level
	// so Grafana alerts surface these — a healthy system should hit this
	// phase 0 times.
	//
	// Dedup with the orchestrator's own timeout path is structural: the
	// query below filters `status='running'`, so any session the orchestrator
	// already transitioned to `failed` is invisible here. A session the
	// orchestrator is *mid-failing* could race, but failRunWithCategory uses
	// a 30s cleanup context and this phase only fires after maxRunningAge
	// (≥2h17m), which is far longer than that window.
	runningCutoff := time.Now().Add(-r.maxRunningAge)
	staleRunning, err := r.sessions.ListStaleRunningSessions(ctx, runningCutoff)
	if err != nil {
		r.logger.Error().Err(err).Msg("reaper: failed to list stale running sessions")
	} else {
		for _, s := range staleRunning {
			if err := r.sessions.UpdateStatus(ctx, s.OrgID, s.ID, string(models.SessionStatusFailed)); err != nil {
				r.logger.Error().Err(err).Str("session_id", s.ID.String()).Msg("reaper: failed to mark stale running session as failed")
				continue
			}
			explanation := "This session started but never reported a result. This usually means the worker processing it crashed or lost connectivity to the database. The work may or may not have completed inside the sandbox; any produced diff has been lost."
			nextSteps := []string{
				"Retry the session — a fresh worker should pick it up",
				"If this keeps happening across multiple sessions, check worker health and database connectivity",
			}
			if err := r.sessions.UpdateFailure(ctx, s.OrgID, s.ID, explanation, FailureCategoryStuckRunning, nextSteps, true); err != nil {
				r.logger.Error().Err(err).Str("session_id", s.ID.String()).Msg("reaper: failed to update failure details for stale running session")
			}
			// Omit started_at/elapsed when nil rather than logging a
			// year-zero timestamp with a multi-millennium duration, which
			// would poison Grafana aggregations on this alert.
			event := r.logger.Error().
				Str("session_id", s.ID.String()).
				Str("org_id", s.OrgID.String())
			if s.StartedAt != nil {
				event = event.Time("started_at", *s.StartedAt).
					Dur("elapsed", time.Since(*s.StartedAt))
			}
			event.Msg("reaper: failed stale running session — worker did not persist terminal status")
		}
	}

	// Phase 0.5b: Fail threads stuck in status='running' past maxRunningAge.
	// Defense-in-depth above the orchestrator/handler thread.status resets:
	// when a continue_session job dead-letters during a rolling deploy, the
	// orchestrator's revert and the handler's revert can both miss if their
	// ctx was cancelled before the DB write landed. Without this phase, the
	// thread row stays 'running' forever and the UI shows "Agent is working"
	// next to "Session is not active" — exactly the orphan we hit in prod.
	//
	// Reuses maxRunningAge (~2h30m floor) because the cutoff is the same
	// idea: any thread alive longer than the maximum legitimate turn cap
	// has been orphaned. Logged at Warn (not Error) because in steady state
	// after the orchestrator/handler patches we expect this phase to fire
	// rarely; promote to Error in alerts if the rate gets noisy.
	if r.threads != nil {
		stuckThreads, err := r.threads.ListStuckRunningThreads(ctx, runningCutoff)
		if err != nil {
			r.logger.Error().Err(err).Msg("reaper: failed to list stuck running threads")
		} else {
			for _, t := range stuckThreads {
				errMsg := fmt.Sprintf("thread stuck in running state for more than %s — no terminal status from worker", r.maxRunningAge)
				result := &models.SessionResult{
					Error:           strPtr(errMsg),
					FailureCategory: strPtr(FailureCategoryStuckThread),
				}
				if err := r.threads.UpdateResult(ctx, t.OrgID, t.ID, models.ThreadStatusFailed, result); err != nil {
					r.logger.Error().Err(err).
						Str("thread_id", t.ID.String()).
						Str("session_id", t.SessionID.String()).
						Msg("reaper: failed to mark stuck running thread as failed")
					continue
				}
				event := r.logger.Warn().
					Str("thread_id", t.ID.String()).
					Str("session_id", t.SessionID.String()).
					Str("org_id", t.OrgID.String())
				if t.StartedAt != nil {
					event = event.Time("started_at", *t.StartedAt).
						Dur("elapsed", time.Since(*t.StartedAt))
				}
				event.Msg("reaper: failed stuck running thread — orchestrator/handler did not reset thread status")
			}
		}
	}

	// Phase 1: Transition stale idle sessions to completed (keep snapshots).
	idleCutoff := time.Now().Add(-r.maxIdleAge)
	staleSessions, err := r.sessions.ListStaleIdleSessions(ctx, idleCutoff)
	if err != nil {
		r.logger.Error().Err(err).Msg("reaper: failed to list stale idle sessions")
	} else {
		for _, s := range staleSessions {
			if err := r.sessions.UpdateStatus(ctx, s.OrgID, s.ID, string(models.SessionStatusCompleted)); err != nil {
				r.logger.Error().Err(err).Str("session_id", s.ID.String()).Msg("reaper: failed to mark session completed")
				continue
			}
			r.logger.Info().
				Str("session_id", s.ID.String()).
				Msg("reaper: transitioned idle session to completed")
		}
	}

	// Phase 2: Delete snapshots that have exceeded the max snapshot age.
	snapshotCutoff := time.Now().Add(-r.maxSnapshotAge)
	expiredSnapshots, err := r.sessions.ListExpiredSnapshots(ctx, snapshotCutoff)
	if err != nil {
		r.logger.Error().Err(err).Msg("reaper: failed to list expired snapshots")
		return
	}

	if len(expiredSnapshots) > 0 {
		r.logger.Info().Int("count", len(expiredSnapshots)).Msg("reaper: cleaning up expired snapshots")
	}

	for _, s := range expiredSnapshots {
		// If a preview is still holding this session's sandbox, tear it
		// down cleanly first. StopPreview runs the hold-aware destroy
		// path, so the container and container_id are cleared before we
		// delete the snapshot blob. Skipping this step would either
		// leak a container on the worker or cause a later StopPreview
		// to misbehave against a session whose snapshot is already gone.
		//
		// Detach from the reaper's ctx for this call: if the worker is
		// shutting down mid-reap, StopPreview still needs to finish its
		// hold-release + destroy sequence so we don't leak the container
		// and leave preview_holding_container=TRUE stuck on the row.
		if r.previewStopper != nil {
			stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), reaperPreviewStopTimeout)
			if stopped, stopErr := r.previewStopper.StopActivePreviewForSession(stopCtx, s.OrgID, s.ID); stopErr != nil {
				r.logger.Warn().Err(stopErr).
					Str("session_id", s.ID.String()).
					Msg("reaper: failed to stop preview holding expired-snapshot session; proceeding with snapshot cleanup")
			} else if stopped {
				r.logger.Info().
					Str("session_id", s.ID.String()).
					Msg("reaper: stopped preview before expiring session snapshot")
			}
			stopCancel()
		}

		if s.SnapshotKey != nil && *s.SnapshotKey != "" {
			if err := r.snapshotStore.Delete(ctx, *s.SnapshotKey); err != nil {
				r.logger.Error().Err(err).
					Str("session_id", s.ID.String()).
					Str("snapshot_key", *s.SnapshotKey).
					Msg("reaper: failed to delete snapshot")
				continue
			}
		}

		if err := r.sessions.UpdateSandboxState(ctx, s.OrgID, s.ID, string(models.SandboxStateDestroyed)); err != nil {
			r.logger.Error().Err(err).Str("session_id", s.ID.String()).Msg("reaper: failed to update sandbox state")
			continue
		}

		r.logger.Info().
			Str("session_id", s.ID.String()).
			Str("status", s.Status).
			Msg("reaper: cleaned up expired snapshot")
	}

	// Phase 3: Close orphaned container usage events.
	// Any container_usage_event with stopped_at IS NULL that started before the
	// idle cutoff is assumed to be from a crashed process. Close it so billing
	// records are accurate.
	if r.orphanCloser != nil {
		closed, err := r.orphanCloser.CloseOrphans(ctx, idleCutoff)
		if err != nil {
			r.logger.Error().Err(err).Msg("reaper: failed to close orphaned container usage events")
		} else if closed > 0 {
			r.logger.Info().Int64("count", closed).Msg("reaper: closed orphaned container usage events")
		}
	}

	// Phase 4: Roll up hourly usage data for the billing dashboard.
	if r.usageRoller != nil {
		now := time.Now().UTC()
		r.reapUsageRollups(ctx, now)

		// Clean up rollup rows older than 90 days — throttled to once per hour
		// to avoid running a DELETE scan on every reaper tick.
		if now.Sub(r.lastRetentionRun) >= time.Hour {
			cutoff := now.AddDate(0, 0, -90)
			deleted, err := r.usageRoller.DeleteOlderThan(ctx, cutoff)
			if err != nil {
				r.logger.Error().Err(err).Msg("reaper: failed to clean up old usage rollup rows")
			} else {
				if deleted > 0 {
					r.logger.Info().Int64("count", deleted).Msg("reaper: cleaned up old usage rollup rows")
				}
				r.lastRetentionRun = now
			}
		}
	}
}

// reapUsageRollups rolls up all hours from the watermark through the last
// completed hour. On a fresh process with no watermark, it backfills a bounded
// startup window (24h) so ordinary downtime does not leave permanent holes in
// the rollup. For longer outages, use the backfill-usage CLI:
//
//	DATABASE_URL=... go run cmd/backfill-usage/main.go --days <N>
//
// When catching up multiple hours (e.g. startup), rollups run concurrently
// with bounded parallelism to avoid blocking the reaper tick for minutes.
func (r *SessionReaper) reapUsageRollups(ctx context.Context, now time.Time) {
	const startupLookback = 24 * time.Hour
	const maxConcurrentRollups = 4

	// Roll up fully completed hours first, then do a best-effort roll of the
	// current in-progress hour so the dashboard stays near-real-time. The
	// watermark only advances past completed hours; the current hour is
	// re-rolled every tick until it completes.
	currentHour := now.UTC().Truncate(time.Hour)
	lastCompletedHour := currentHour.Add(-time.Hour)
	startHour := lastCompletedHour

	if r.lastRollupHour.IsZero() {
		// Seed watermark from the database so we don't redundantly re-roll
		// hours that were already materialized before this process started.
		if latest, err := r.usageRoller.GetLatestRollupHour(ctx); err != nil {
			r.logger.Warn().Err(err).Msg("reaper: failed to seed rollup watermark from DB, falling back to lookback")
		} else if !latest.IsZero() {
			r.lastRollupHour = latest
			r.logger.Info().Time("watermark", latest).Msg("reaper: seeded rollup watermark from DB")
		}
		// After seeding, re-evaluate: if we have a watermark, advance from it;
		// otherwise fall back to the startup lookback window.
		if r.lastRollupHour.IsZero() {
			startHour = lastCompletedHour.Add(-startupLookback)
		} else {
			startHour = r.lastRollupHour.Add(time.Hour)
		}
	} else if r.lastRollupHour.Before(lastCompletedHour) {
		startHour = r.lastRollupHour.Add(time.Hour)
	}

	// Collect completed hours to process.
	var hours []time.Time
	for h := startHour; !h.After(lastCompletedHour); h = h.Add(time.Hour) {
		hours = append(hours, h)
	}

	if len(hours) == 0 {
		// No completed hours to catch up on — just roll the current hour.
		if err := r.usageRoller.RollupAllOrgs(ctx, currentHour); err != nil {
			r.logger.Warn().Err(err).Time("hour", currentHour).Msg("reaper: failed to roll up current hour (best-effort)")
		}
		return
	}

	// For a single hour (the common steady-state case), skip goroutine overhead.
	if len(hours) == 1 {
		if err := r.usageRoller.RollupAllOrgs(ctx, hours[0]); err != nil {
			r.logger.Error().Err(err).Time("hour", hours[0]).Msg("reaper: failed to roll up hourly usage")
			return
		}
		r.lastRollupHour = hours[0]
	} else {
		// Multiple hours: run concurrently with bounded parallelism.
		// Track per-hour success so we can advance the watermark to the
		// latest contiguous successful hour even if one hour fails.
		sem := make(chan struct{}, maxConcurrentRollups)
		var mu sync.Mutex
		succeeded := make(map[time.Time]bool, len(hours))
		var firstErr error
		var errHour time.Time

		var wg sync.WaitGroup
		for _, h := range hours {
			wg.Add(1)
			go func(hour time.Time) {
				defer wg.Done()

				select {
				case <-ctx.Done():
					return
				default:
				}

				sem <- struct{}{}
				err := r.usageRoller.RollupAllOrgs(ctx, hour)
				<-sem

				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
						errHour = hour
					}
				} else {
					succeeded[hour] = true
				}
				mu.Unlock()
			}(h)
		}
		wg.Wait()

		if firstErr != nil {
			r.logger.Error().Err(firstErr).Time("hour", errHour).Msg("reaper: failed to roll up hourly usage")
		}

		// Advance watermark to the latest contiguous successful hour so a
		// single persistently failing hour doesn't block all progress.
		for _, h := range hours {
			if !succeeded[h] {
				break
			}
			r.lastRollupHour = h
		}
	}

	// Best-effort roll of the current in-progress hour. This keeps the
	// dashboard near-real-time. The watermark is NOT advanced past this
	// hour, so it will be re-rolled on the next tick and again as a
	// completed hour once the hour boundary passes.
	if err := r.usageRoller.RollupAllOrgs(ctx, currentHour); err != nil {
		r.logger.Warn().Err(err).Time("hour", currentHour).Msg("reaper: failed to roll up current hour (best-effort)")
	}
}
