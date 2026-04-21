package agent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// OrphanedContainerLister is the narrow subset of the session store needed
// by the reconciler.
type OrphanedContainerLister interface {
	ListOrphanedContainers(ctx context.Context) ([]models.Session, error)
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

	for batch := 0; batch < maxBatches; batch++ {
		sessions, err := store.ListOrphanedContainers(ctx)
		if err != nil {
			return fmt.Errorf("list orphaned containers: %w", err)
		}
		if len(sessions) == 0 {
			break
		}

		for _, s := range sessions {
			if s.ContainerID == nil || *s.ContainerID == "" {
				continue
			}
			sb := &Sandbox{ID: *s.ContainerID, Provider: "docker"}
			if err := provider.Destroy(ctx, sb); err != nil {
				// Container may already be gone (common case: Docker pruned on
				// host restart). Log at info, not error — this is expected.
				logger.Info().Err(err).
					Str("session_id", s.ID.String()).
					Str("container_id", *s.ContainerID).
					Msg("reconciler: destroy failed; clearing container_id anyway")
				totalSkipped++
			} else {
				totalDestroyed++
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
