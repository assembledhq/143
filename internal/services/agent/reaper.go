package agent

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/storage"
)

// SessionReaper periodically cleans up stale session snapshots from object
// storage. It handles two cases:
//   - Idle sessions that have been inactive for longer than maxIdleAge
//   - Completed/failed/cancelled sessions that still have snapshots
type SessionReaper struct {
	sessions      StaleSessionLister
	snapshotStore storage.SnapshotStore
	maxIdleAge    time.Duration
	interval      time.Duration
	logger        zerolog.Logger
}

// StaleSessionLister is the subset of the session store used by the reaper.
type StaleSessionLister interface {
	ListStaleSessions(ctx context.Context, olderThan time.Time) ([]models.Session, error)
	UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status string) error
	UpdateSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, state string) error
}

// NewSessionReaper creates a reaper that runs every interval, cleaning up
// sessions idle for longer than maxIdleAge.
func NewSessionReaper(sessions StaleSessionLister, snapshotStore storage.SnapshotStore, maxIdleAge, interval time.Duration, logger zerolog.Logger) *SessionReaper {
	return &SessionReaper{
		sessions:      sessions,
		snapshotStore: snapshotStore,
		maxIdleAge:    maxIdleAge,
		interval:      interval,
		logger:        logger,
	}
}

// Run starts the reaper loop. It blocks until ctx is cancelled.
func (r *SessionReaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.logger.Info().
		Dur("interval", r.interval).
		Dur("max_idle", r.maxIdleAge).
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
	cutoff := time.Now().Add(-r.maxIdleAge)
	sessions, err := r.sessions.ListStaleSessions(ctx, cutoff)
	if err != nil {
		r.logger.Error().Err(err).Msg("reaper: failed to list stale sessions")
		return
	}

	if len(sessions) == 0 {
		return
	}

	r.logger.Info().Int("count", len(sessions)).Msg("reaper: cleaning up stale snapshots")

	for _, s := range sessions {
		if s.SnapshotKey != nil && *s.SnapshotKey != "" {
			if err := r.snapshotStore.Delete(ctx, *s.SnapshotKey); err != nil {
				r.logger.Error().Err(err).
					Str("session_id", s.ID.String()).
					Str("snapshot_key", *s.SnapshotKey).
					Msg("reaper: failed to delete snapshot")
				continue
			}
		}

		// Only set status to completed for idle sessions; completed/failed
		// sessions should keep their existing status.
		if s.Status == string(models.SessionStatusIdle) {
			if err := r.sessions.UpdateStatus(ctx, s.OrgID, s.ID, string(models.SessionStatusCompleted)); err != nil {
				r.logger.Error().Err(err).Str("session_id", s.ID.String()).Msg("reaper: failed to mark session completed")
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
			Msg("reaper: cleaned up stale session")
	}
}
