package agent

import (
	"context"
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
}

// SessionReaper periodically cleans up stale sessions and expired snapshots
// in four phases:
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
	maxSnapshotAge   time.Duration
	interval         time.Duration
	logger           zerolog.Logger
	lastRetentionRun time.Time // throttles retention cleanup to once per hour
	lastRollupHour   time.Time // watermark: last hour successfully rolled up; written only after wg.Wait() in the single reaper goroutine
}

// StaleSessionLister is the subset of the session store used by the reaper.
type StaleSessionLister interface {
	ListStaleIdleSessions(ctx context.Context, olderThan time.Time) ([]models.Session, error)
	ListExpiredSnapshots(ctx context.Context, olderThan time.Time) ([]models.Session, error)
	UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status string) error
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
func NewSessionReaper(sessions StaleSessionLister, snapshotStore storage.SnapshotStore, maxIdleAge, maxSnapshotAge, interval time.Duration, logger zerolog.Logger, opts ...SessionReaperOption) *SessionReaper {
	r := &SessionReaper{
		sessions:       sessions,
		snapshotStore:  snapshotStore,
		maxIdleAge:     maxIdleAge,
		maxSnapshotAge: maxSnapshotAge,
		interval:       interval,
		logger:         logger,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Run starts the reaper loop. It blocks until ctx is cancelled.
func (r *SessionReaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.logger.Info().
		Dur("interval", r.interval).
		Dur("max_idle", r.maxIdleAge).
		Dur("max_snapshot_age", r.maxSnapshotAge).
		Msg("snapshot reaper started")

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

func (r *SessionReaper) reap(ctx context.Context) {
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

	// Only roll up fully completed hours. The current hour is still in
	// progress — rolling it up now would permanently undercount because the
	// watermark advances past it, preventing a re-roll once more events land.
	lastCompletedHour := now.UTC().Truncate(time.Hour).Add(-time.Hour)
	startHour := lastCompletedHour

	if r.lastRollupHour.IsZero() {
		startHour = lastCompletedHour.Add(-startupLookback)
	} else if r.lastRollupHour.Before(lastCompletedHour) {
		startHour = r.lastRollupHour.Add(time.Hour)
	}

	// Collect hours to process.
	var hours []time.Time
	for h := startHour; !h.After(lastCompletedHour); h = h.Add(time.Hour) {
		hours = append(hours, h)
	}

	if len(hours) == 0 {
		return
	}

	// For a single hour (the common steady-state case), skip goroutine overhead.
	if len(hours) == 1 {
		if err := r.usageRoller.RollupAllOrgs(ctx, hours[0]); err != nil {
			r.logger.Error().Err(err).Time("hour", hours[0]).Msg("reaper: failed to roll up hourly usage")
			return
		}
		r.lastRollupHour = hours[0]
		return
	}

	// Multiple hours: run concurrently with bounded parallelism.
	sem := make(chan struct{}, maxConcurrentRollups)
	var mu sync.Mutex
	var firstErr error
	var errHour time.Time

	var wg sync.WaitGroup
	for _, h := range hours {
		wg.Add(1)
		go func(hour time.Time) {
			defer wg.Done()

			// Check for prior error or context cancellation.
			mu.Lock()
			failed := firstErr != nil
			mu.Unlock()
			if failed {
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}

			sem <- struct{}{}
			err := r.usageRoller.RollupAllOrgs(ctx, hour)
			<-sem

			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					errHour = hour
				}
				mu.Unlock()
			}
		}(h)
	}
	wg.Wait()

	if firstErr != nil {
		r.logger.Error().Err(firstErr).Time("hour", errHour).Msg("reaper: failed to roll up hourly usage")
		// Don't advance watermark past what we can guarantee — but hours are
		// independent (idempotent upserts), so re-rolling on next tick is safe.
		return
	}
	r.lastRollupHour = lastCompletedHour
}
