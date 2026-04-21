package agent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

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
// whose container_id is set but neither holder (turn or preview) is true —
// i.e. a container that leaked because the server crashed mid-turn or
// mid-Stop. For each such session it claims the row via a CAS clear, and
// only if the claim succeeds destroys the container — so a new turn or
// preview that acquired a hold in the gap between list and clear is never
// ambushed by a reconciler destroy.
//
// It loops ListOrphanedContainers until it returns an empty slice, with a
// safety cap so a degenerate state (clear perpetually losing to new holders)
// can't spin forever at startup.
//
// Safe to run concurrently with the orchestrator: ClearContainerID's CAS
// predicate rejects rows where a new holder has come back, so newly-active
// sessions are never disturbed.
func ReconcileOrphanedContainers(ctx context.Context, store OrphanedContainerLister, provider SandboxProvider, logger zerolog.Logger) error {
	if provider == nil {
		logger.Debug().Msg("reconciler: no sandbox provider configured; skipping orphan cleanup")
		return nil
	}

	const maxBatches = 20 // 20 * 100 = 2000 orphans, far more than we'd ever see
	var totalDestroyed, totalCleared, totalSkipped, totalRaced int
	var cursor uuid.UUID // keyset cursor; advances past rows we couldn't clear

	for batch := 0; batch < maxBatches; batch++ {
		sessions, err := store.ListOrphanedContainers(ctx, cursor)
		if err != nil {
			return fmt.Errorf("list orphaned containers: %w", err)
		}
		if len(sessions) == 0 {
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
			expectedID := *s.ContainerID
			sb := &Sandbox{ID: expectedID, Provider: "docker"}

			// Probe liveness to pick telemetry bucket (destroyed vs already_gone)
			// and to skip destroy calls that would log noise on a pruned
			// container. A transient inspect error means docker is being
			// flaky right now — leave this row for the next startup rather
			// than racing a CAS-clear against a half-working daemon.
			alive, aliveErr := provider.IsAlive(ctx, sb)
			if aliveErr != nil {
				logger.Warn().Err(aliveErr).
					Str("session_id", s.ID.String()).
					Str("container_id", expectedID).
					Msg("reconciler: liveness check failed; leaving orphan row for next startup")
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
