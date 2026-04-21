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
// (pass uuid.Nil as the cursor for the first page).
type OrphanedContainerLister interface {
	ListOrphanedContainers(ctx context.Context, afterID uuid.UUID) ([]models.Session, error)
	ClearContainerID(ctx context.Context, orgID, sessionID uuid.UUID) error
}

// ReconcileOrphanedContainers is a one-shot startup pass that finds sessions
// whose container_id is set but neither holder (turn or preview) is true —
// i.e. a container that leaked because the server crashed mid-turn or
// mid-Stop. For each such session, the reconciler destroys the container
// (best-effort) and then clears container_id so the row is consistent.
//
// It loops ListOrphanedContainers until it returns an empty slice, with a
// safety cap so a degenerate state (destroy perpetually failing) can't spin
// forever at startup.
//
// Safe to run concurrently with the orchestrator: ClearContainerID is a
// targeted UPDATE that doesn't affect sessions which acquired a new hold
// between the list and the clear.
func ReconcileOrphanedContainers(ctx context.Context, store OrphanedContainerLister, provider SandboxProvider, logger zerolog.Logger) error {
	if provider == nil {
		logger.Debug().Msg("reconciler: no sandbox provider configured; skipping orphan cleanup")
		return nil
	}

	const maxBatches = 20 // 20 * 100 = 2000 orphans, far more than we'd ever see
	var totalDestroyed, totalCleared, totalSkipped int
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
		// transient inspect or destroy failure on the same row would cause
		// us to re-read the same page every iteration.
		cursor = sessions[len(sessions)-1].ID

		for _, s := range sessions {
			if s.ContainerID == nil || *s.ContainerID == "" {
				continue
			}
			sb := &Sandbox{ID: *s.ContainerID, Provider: "docker"}
			// Probe first so we can distinguish "container is gone; safe to
			// clear the row" from "lookup failed transiently; leave it for
			// the next startup". Without this probe a transient error from
			// Destroy would cause us to clear container_id and permanently
			// forget a container that may still be alive.
			alive, aliveErr := provider.IsAlive(ctx, sb)
			if aliveErr != nil {
				logger.Warn().Err(aliveErr).
					Str("session_id", s.ID.String()).
					Str("container_id", *s.ContainerID).
					Msg("reconciler: liveness check failed; leaving orphan row for next startup")
				continue
			}
			if alive {
				if err := provider.Destroy(ctx, sb); err != nil {
					logger.Warn().Err(err).
						Str("session_id", s.ID.String()).
						Str("container_id", *s.ContainerID).
						Msg("reconciler: destroy failed on live container; leaving orphan row for next startup")
					continue
				}
				totalDestroyed++
			} else {
				totalSkipped++
			}

			if err := store.ClearContainerID(ctx, s.OrgID, s.ID); err != nil {
				logger.Warn().Err(err).
					Str("session_id", s.ID.String()).
					Msg("reconciler: failed to clear container_id; row will be re-picked on next startup")
				continue
			}
			totalCleared++
		}
	}

	if totalCleared > 0 || totalDestroyed > 0 || totalSkipped > 0 {
		logger.Info().
			Int("destroyed", totalDestroyed).
			Int("already_gone", totalSkipped).
			Int("cleared", totalCleared).
			Msg("reconciler: orphan cleanup complete")
	} else {
		logger.Debug().Msg("reconciler: no orphaned containers found")
	}
	return nil
}
