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

// SessionReaper periodically cleans up stale sessions and expired snapshots
// in six phases:
//   - Phase 0: Fail sessions stuck in pending for longer than maxPendingAge
//   - Phase 0.5: Fail sessions stuck in running for longer than maxRunningAge
//     (safety net for orphaned sessions when the worker handler timeout path
//     didn't run, e.g. worker crash or DB write failure during failure handling)
//   - Phase 1: Transition idle sessions to completed (keep snapshots)
//   - Phase 2: Delete snapshots that have exceeded the max snapshot age
//   - Phase 3: Close orphaned container usage events for billing accuracy
//   - Phase 4: Roll up hourly usage data and clean up old rollup rows
type SessionReaper struct {
	sessions         StaleSessionLister
	snapshotStore    storage.SnapshotStore
	orphanCloser     OrphanCloser // nil-safe — billing orphan cleanup disabled if nil
	usageRoller      UsageRoller  // nil-safe — usage rollup disabled if nil
	maxIdleAge       time.Duration
	maxPendingAge    time.Duration
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
	ListExpiredSnapshots(ctx context.Context, olderThan time.Time) ([]models.Session, error)
	UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status string) error
	UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status string, result *models.SessionResult) error
	UpdateFailure(ctx context.Context, orgID, runID uuid.UUID, explanation, category string, nextSteps []string, retryAdvised bool) error
	UpdateSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, state string) error
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

// NewSessionReaper creates a reaper that runs every interval, cleaning up
// sessions idle for longer than maxIdleAge and snapshots older than maxSnapshotAge.
// defaultMaxPendingAge is the maximum time a session can stay in "pending"
// before the reaper considers it stuck and marks it as failed.
const defaultMaxPendingAge = 10 * time.Minute

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
		sessions:       sessions,
		snapshotStore:  snapshotStore,
		maxIdleAge:     maxIdleAge,
		maxPendingAge:  defaultMaxPendingAge,
		maxRunningAge:  defaultMaxRunningAge,
		maxSnapshotAge: maxSnapshotAge,
		interval:       interval,
		logger:         logger,
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
		Dur("max_running", r.maxRunningAge).
		Dur("max_idle", r.maxIdleAge).
		Dur("max_snapshot_age", r.maxSnapshotAge).
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

func (r *SessionReaper) reap(ctx context.Context) {
	// Phase 0: Fail sessions stuck in pending with no active job.
	pendingCutoff := time.Now().Add(-r.maxPendingAge)
	stalePending, err := r.sessions.ListStalePendingSessions(ctx, pendingCutoff)
	if err != nil {
		r.logger.Error().Err(err).Msg("reaper: failed to list stale pending sessions")
	} else {
		for _, s := range stalePending {
			errMsg := fmt.Sprintf("session timed out after %s in pending state without starting", r.maxPendingAge)
			result := &models.SessionResult{
				Error: strPtr(errMsg),
			}
			if err := r.sessions.UpdateResult(ctx, s.OrgID, s.ID, string(models.SessionStatusFailed), result); err != nil {
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

	// Phase 0.5: Fail sessions stuck in "running" past the max running age.
	// Safety net for sessions the handler timeout path couldn't mark failed
	// (worker crash, DB outage during failRun, etc.). Logged at Error level
	// so Grafana alerts surface these — a healthy system should hit this
	// phase 0 times.
	runningCutoff := time.Now().Add(-r.maxRunningAge)
	staleRunning, err := r.sessions.ListStaleRunningSessions(ctx, runningCutoff)
	if err != nil {
		r.logger.Error().Err(err).Msg("reaper: failed to list stale running sessions")
	} else {
		for _, s := range staleRunning {
			errMsg := fmt.Sprintf("session stuck in running state for more than %s — no status update from worker", r.maxRunningAge)
			result := &models.SessionResult{
				Error: strPtr(errMsg),
			}
			if err := r.sessions.UpdateResult(ctx, s.OrgID, s.ID, string(models.SessionStatusFailed), result); err != nil {
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
			startedAt := time.Time{}
			if s.StartedAt != nil {
				startedAt = *s.StartedAt
			}
			r.logger.Error().
				Str("session_id", s.ID.String()).
				Str("org_id", s.OrgID.String()).
				Time("started_at", startedAt).
				Dur("elapsed", time.Since(startedAt)).
				Msg("reaper: failed stale running session — worker did not persist terminal status")
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
