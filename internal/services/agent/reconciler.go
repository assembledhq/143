package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// reconcileIsAliveAttempts bounds how many times the reconciler re-probes a
// single container's liveness before giving up on it for this startup pass.
// A transient docker-daemon hiccup shouldn't consign a row to wait for the
// next server restart; two extra attempts with a short backoff let the probe
// ride through a 1–2s stall (e.g. dockerd still coming up, api rate limit)
// without materially slowing the startup pass.
const reconcileIsAliveAttempts = 3

// reconcileIsAliveBackoff is the delay between IsAlive retries inside the
// reconciler. Kept short because the reconciler blocks startup — we're
// racing to get the server ready, not waiting minutes for a flaky daemon.
//
// Stored as an atomic so parallel tests calling SetIsAliveBackoffForTesting
// don't race the production read at probeAliveWithRetry. Production code
// never reassigns this.
var reconcileIsAliveBackoff atomic.Int64

func init() {
	reconcileIsAliveBackoff.Store(int64(500 * time.Millisecond))
}

// SetIsAliveBackoffForTesting replaces the retry backoff used by the
// reconciler's IsAlive probe. Test-only — the production setting is 500ms;
// tests drop it to zero so exhaustion tests don't wait over a second.
func SetIsAliveBackoffForTesting(d time.Duration) {
	reconcileIsAliveBackoff.Store(int64(d))
}

// reconcileMaxBatches caps how many pages of 100 orphan rows the reconciler
// will work through in a single startup pass. It's a safety valve against
// a pathological state (every clear losing to a concurrent holder, or a
// runaway orphan accumulation) blocking startup indefinitely. Hitting this
// cap is logged at Warn so ops notices — a healthy system clears all
// orphans in 1–2 batches.
const reconcileMaxBatches = 20

// OrphanedContainerLister is the narrow subset of the session store needed
// by the reconciler. ListOrphanedContainers is keyset-paginated by session id
// (pass uuid.Nil as the cursor for the first page). ClearContainerID is a
// CAS: it only clears the row when the expected container_id still matches
// and no holder has slipped in since the list.
type OrphanedContainerLister interface {
	ListOrphanedContainers(ctx context.Context, afterID uuid.UUID) ([]models.Session, error)
	ClearContainerID(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (cleared bool, err error)
}

// ReconcileOrphanedContainers is a one-shot startup pass that finds sessions
// whose container_id is set but no preview hold is in place — i.e. a
// container that leaked because the server crashed mid-turn or mid-Stop.
// For each such session it probes IsAlive; only when the container is
// actually gone does it CAS-clear the row (resetting the stuck
// turn_holding_container flag at the same time) and no-op the destroy.
// Containers that still report alive are left untouched so we never ambush
// a live turn running on the shared host.
//
// It loops ListOrphanedContainers until it returns an empty slice, with a
// safety cap so a degenerate state (clear perpetually losing to new holders)
// can't spin forever at startup.
//
// Safe to run concurrently with the orchestrator: ClearContainerID's CAS
// predicate rejects rows where the container_id has been rewritten or a
// preview hold has been claimed since the list, and the IsAlive gate means
// we never touch a row whose container is genuinely still running.
func ReconcileOrphanedContainers(ctx context.Context, store OrphanedContainerLister, provider SandboxProvider, logger zerolog.Logger) error {
	if provider == nil {
		logger.Debug().Msg("reconciler: no sandbox provider configured; skipping orphan cleanup")
		return nil
	}

	var totalDestroyed, totalCleared, totalSkipped, totalRaced int
	var cursor uuid.UUID // keyset cursor; advances past rows we couldn't clear
	hitCap := true       // stays true only if every batch returned a full page

	for batch := 0; batch < reconcileMaxBatches; batch++ {
		sessions, err := store.ListOrphanedContainers(ctx, cursor)
		if err != nil {
			return fmt.Errorf("list orphaned containers: %w", err)
		}
		if len(sessions) == 0 {
			hitCap = false
			break
		}
		// Advance the cursor to the last row we saw before processing, so
		// the next batch skips past this page regardless of whether we
		// successfully cleared each row. Without this, a persistently
		// transient destroy failure or a row that keeps getting re-acquired
		// would cause us to re-read the same page every iteration.
		cursor = sessions[len(sessions)-1].ID

		for _, s := range sessions {
			if s.ContainerID == nil || *s.ContainerID == "" {
				continue
			}
			if startupContainerDestroyProtected(s) {
				totalSkipped++
				logger.Info().
					Str("session_id", s.ID.String()).
					Str("status", s.Status).
					Str("recovery_state", string(s.RecoveryState)).
					Msg("reconciler: preserving container for active or recovering session")
				continue
			}
			expectedID := *s.ContainerID
			sb := &Sandbox{ID: expectedID, Provider: "docker"}

			// Probe liveness to pick telemetry bucket (destroyed vs already_gone)
			// and to skip destroy calls that would log noise on a pruned
			// container. Retry a small number of times on transient errors so
			// a docker-daemon hiccup at startup doesn't strand this row until
			// the next server restart — only give up (leave the row alone)
			// once retries are exhausted.
			alive, aliveErr := probeAliveWithRetry(ctx, provider, sb, logger, s.ID, expectedID)
			if aliveErr != nil {
				continue
			}

			// CAS-clear the row FIRST. Only if this wins do we have the
			// right to destroy the container — the WHERE clause rejects
			// rows where a new turn/preview has acquired a hold in the gap
			// since ListOrphanedContainers, so we can't accidentally
			// destroy a container that's back in use.
			cleared, clearErr := store.ClearContainerID(ctx, s.OrgID, s.ID, expectedID)
			if clearErr != nil {
				logger.Warn().Err(clearErr).
					Str("session_id", s.ID.String()).
					Msg("reconciler: failed to clear container_id; row will be re-picked on next startup")
				continue
			}
			if !cleared {
				// Either a new holder acquired the row, or container_id
				// was already rewritten by a concurrent hydrate. Either
				// way, the container is someone else's problem now.
				totalRaced++
				logger.Debug().
					Str("session_id", s.ID.String()).
					Str("container_id", expectedID).
					Msg("reconciler: row no longer orphaned; leaving container alive")
				continue
			}

			totalCleared++
			if !alive {
				totalSkipped++
				continue
			}
			if err := provider.Destroy(ctx, sb); err != nil {
				// We've already cleared the DB row, so the container is
				// now untracked. Log loudly — ops may need a manual
				// docker prune if this recurs.
				logger.Warn().Err(err).
					Str("session_id", s.ID.String()).
					Str("container_id", expectedID).
					Msg("reconciler: destroy failed after CAS-clear; container may be orphaned on the host")
				continue
			}
			totalDestroyed++
		}
	}

	if hitCap {
		// We exhausted reconcileMaxBatches without emptying the page stream.
		// Either there are more than reconcileMaxBatches*100 orphans (very
		// unusual) or clears are persistently losing to concurrent holders.
		// Either way, surface it — the untouched tail will be reclaimed on
		// the next server restart, but if this recurs, ops may want to raise
		// the cap or investigate the holder-race pattern.
		logger.Warn().
			Int("batches", reconcileMaxBatches).
			Int("rows_per_batch", 100).
			Msg("reconciler: reached batch cap before draining orphaned containers; remaining rows will be retried on next startup")
	}

	if totalCleared > 0 || totalDestroyed > 0 || totalSkipped > 0 || totalRaced > 0 {
		logger.Info().
			Int("destroyed", totalDestroyed).
			Int("already_gone", totalSkipped).
			Int("cleared", totalCleared).
			Int("raced", totalRaced).
			Msg("reconciler: orphan cleanup complete")
	} else {
		logger.Debug().Msg("reconciler: no orphaned containers found")
	}
	return nil
}

func startupContainerDestroyProtected(s models.Session) bool {
	if s.Status == string(models.SessionStatusRunning) {
		return true
	}
	switch s.RecoveryState {
	case models.RecoveryStateQueued, models.RecoveryStateRecovering:
		return true
	default:
		return false
	}
}

// probeAliveWithRetry calls provider.IsAlive with bounded retries so a
// transient docker-daemon hiccup at startup doesn't skip an orphan row
// until the next server restart. On the final failure it logs a warning
// and returns the last error, letting the caller skip the row; on success
// it returns the liveness result.
func probeAliveWithRetry(
	ctx context.Context,
	provider SandboxProvider,
	sb *Sandbox,
	logger zerolog.Logger,
	sessionID uuid.UUID,
	containerID string,
) (bool, error) {
	var lastErr error
	for attempt := 1; attempt <= reconcileIsAliveAttempts; attempt++ {
		alive, err := provider.IsAlive(ctx, sb)
		if err == nil {
			return alive, nil
		}
		lastErr = err
		// Abort early on context cancellation — no point sleeping and
		// retrying against a dead context.
		if ctx.Err() != nil {
			break
		}
		if attempt < reconcileIsAliveAttempts {
			logger.Debug().Err(err).
				Str("session_id", sessionID.String()).
				Str("container_id", containerID).
				Int("attempt", attempt).
				Msg("reconciler: liveness check failed; retrying")
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(time.Duration(reconcileIsAliveBackoff.Load())):
			}
		}
	}
	logger.Warn().Err(lastErr).
		Str("session_id", sessionID.String()).
		Str("container_id", containerID).
		Int("attempts", reconcileIsAliveAttempts).
		Msg("reconciler: liveness check failed after retries; leaving orphan row for next startup")
	return false, lastErr
}
