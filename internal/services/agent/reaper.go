package agent

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/storage"
)

// SessionReaper periodically cleans up stale sessions and expired snapshots
// in two phases:
//   - Phase 1: Transition idle sessions to completed (keep snapshots)
//   - Phase 2: Delete snapshots that have exceeded the max snapshot age
type SessionReaper struct {
	sessions       StaleSessionLister
	snapshotStore  storage.SnapshotStore
	maxIdleAge     time.Duration
	maxSnapshotAge time.Duration
	interval       time.Duration
	logger         zerolog.Logger
}

// StaleSessionLister is the subset of the session store used by the reaper.
type StaleSessionLister interface {
	ListStaleIdleSessions(ctx context.Context, olderThan time.Time) ([]models.Session, error)
	ListExpiredSnapshots(ctx context.Context, olderThan time.Time) ([]models.Session, error)
	UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status string) error
	UpdateSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, state string) error
}

// NewSessionReaper creates a reaper that runs every interval, cleaning up
// sessions idle for longer than maxIdleAge and snapshots older than maxSnapshotAge.
func NewSessionReaper(sessions StaleSessionLister, snapshotStore storage.SnapshotStore, maxIdleAge, maxSnapshotAge, interval time.Duration, logger zerolog.Logger) *SessionReaper {
	return &SessionReaper{
		sessions:       sessions,
		snapshotStore:  snapshotStore,
		maxIdleAge:     maxIdleAge,
		maxSnapshotAge: maxSnapshotAge,
		interval:       interval,
		logger:         logger,
	}
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
}
