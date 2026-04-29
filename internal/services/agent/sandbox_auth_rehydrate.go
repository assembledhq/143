package agent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// rehydrateMaxBatches caps how many pages of 100 container-holding sessions
// the rehydrate pass will work through at startup. Mirrors the reconciler's
// safety valve: a healthy worker rarely has more than a handful of sessions
// with live preview-held containers, so hitting the cap means something is
// wrong (degenerate query, runaway preview accumulation) and we want to keep
// startup moving rather than spin forever.
const rehydrateMaxBatches = 20

// ContainerHoldingSessionLister is the narrow subset of the session store
// needed by the rehydrate pass. ListContainerHoldingSessions is keyset-
// paginated by session id (pass uuid.Nil as the cursor for the first page)
// and returns sessions where container_id is set and a preview hold is in
// place — i.e. the ones whose containers survive worker restarts and whose
// in-sandbox tooling will dial a dead socket until we Listen again.
type ContainerHoldingSessionLister interface {
	ListContainerHoldingSessions(ctx context.Context, afterID uuid.UUID) ([]models.Session, error)
}

// OrgSettingsLoader resolves an org's PR-authorship policy for the sandbox
// auth resolver. Defined as a function type so callers can pass either the
// orchestrator's existing helper (which surfaces parse errors) or a test
// stub without dragging the full OrgStore into rehydrate. The captured
// settings are pinned to the listener for its lifetime (see
// sandboxauth.Server.Listen), so getting them right at rehydrate time
// matters — a wrong value persists until the next turn boundary.
type OrgSettingsLoader func(ctx context.Context, orgID uuid.UUID) (models.OrgSettings, error)

// RehydrateSandboxAuthListeners is a one-shot startup pass that re-opens the
// per-session GitHub credential socket listener for sessions whose containers
// are still alive across a worker restart (preview holds keep them running).
//
// Why this exists: the host-side sandboxauth.Server holds its listeners in
// process memory, so a worker restart leaves their sockets on disk with no
// listener attached. The container's directory bind-mount survives the
// restart, so the in-sandbox 143-tools helper keeps dialing the same path —
// and gets ECONNREFUSED until the next turn boundary calls Listen again.
// That gap can stretch indefinitely if the user is iterating in a preview
// without sending a new turn (the common case for "I just want to push from
// the held-alive sandbox"). Re-opening listeners proactively at startup
// closes the gap to a single boot delay.
//
// Per session we:
//  1. Probe IsAlive (cheap docker inspect). Containers that report dead are
//     skipped — the orphan reconciler clears those rows separately.
//  2. Load the repo + org settings the resolver needs.
//  3. Call sandboxAuth.Listen, which removes any stale socket file at the
//     deterministic path and binds a fresh one. The bind-mount inside the
//     container resolves the new file at lookup time, so the next push
//     succeeds without any container-side reconfiguration.
//
// Best-effort by design: per-session failures are logged and the loop
// continues so a single bad row can't strand the startup pass. If sandboxAuth
// or provider is nil (legacy GITHUB_TOKEN path or no docker), we bail early
// — there's nothing to rehydrate.
func RehydrateSandboxAuthListeners(
	ctx context.Context,
	sessions ContainerHoldingSessionLister,
	repos RepositoryStore,
	orgSettings OrgSettingsLoader,
	provider SandboxProvider,
	sandboxAuth SandboxAuthServer,
	logger zerolog.Logger,
) (map[uuid.UUID]struct{}, error) {
	rehydrated := make(map[uuid.UUID]struct{})
	if sandboxAuth == nil {
		logger.Debug().Msg("rehydrate: sandbox auth server not configured; skipping")
		return rehydrated, nil
	}
	if provider == nil {
		logger.Debug().Msg("rehydrate: no sandbox provider configured; skipping")
		return rehydrated, nil
	}

	var totalRehydrated, totalDead, totalErrored int
	var cursor uuid.UUID
	hitCap := true

	for batch := 0; batch < rehydrateMaxBatches; batch++ {
		page, err := sessions.ListContainerHoldingSessions(ctx, cursor)
		if err != nil {
			return rehydrated, fmt.Errorf("list container-holding sessions: %w", err)
		}
		if len(page) == 0 {
			hitCap = false
			break
		}
		// Advance the cursor before processing so a transient per-row failure
		// can't cause us to re-read the same page on the next iteration.
		cursor = page[len(page)-1].ID

		for i := range page {
			run := &page[i]
			rowLog := logger.With().
				Str("session_id", run.ID.String()).
				Str("org_id", run.OrgID.String()).
				Logger()

			if run.ContainerID == nil || *run.ContainerID == "" {
				continue
			}
			containerID := *run.ContainerID
			sb := &Sandbox{ID: containerID, Provider: "docker"}

			alive, aliveErr := probeAliveWithRetry(ctx, provider, sb, rowLog, run.ID, containerID)
			if aliveErr != nil {
				totalErrored++
				continue
			}
			if !alive {
				// Reconciler will clear this row in its own pass; nothing to
				// rehydrate against a dead container.
				totalDead++
				continue
			}

			if run.RepositoryID == nil {
				rowLog.Debug().Msg("rehydrate: skipping container-holding session with no repository linked")
				continue
			}
			repo, err := repos.GetByID(ctx, run.OrgID, *run.RepositoryID)
			if err != nil {
				rowLog.Warn().Err(err).Msg("rehydrate: failed to load repository; leaving listener un-rehydrated for this session")
				totalErrored++
				continue
			}

			settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
			if orgSettings != nil {
				loaded, err := orgSettings(ctx, run.OrgID)
				if err != nil {
					rowLog.Warn().Err(err).Msg("rehydrate: failed to load org settings; leaving listener un-rehydrated for this session")
					totalErrored++
					continue
				}
				settings = loaded
			}

			if _, err := sandboxAuth.Listen(ctx, run.ID, run, &repo, settings); err != nil {
				rowLog.Warn().Err(err).
					Str("container_id", containerID).
					Msg("rehydrate: failed to re-open sandbox auth socket; next turn boundary will retry")
				totalErrored++
				continue
			}
			rehydrated[run.ID] = struct{}{}
			totalRehydrated++
		}
	}

	if hitCap {
		logger.Warn().
			Int("batches", rehydrateMaxBatches).
			Int("rows_per_batch", 100).
			Msg("rehydrate: reached batch cap before draining container-holding sessions; remaining rows will retry on next turn boundary")
	}

	if totalRehydrated > 0 || totalDead > 0 || totalErrored > 0 {
		logger.Info().
			Int("rehydrated", totalRehydrated).
			Int("dead_skipped", totalDead).
			Int("errored", totalErrored).
			Msg("rehydrate: sandbox auth listener rehydration complete")
	} else {
		logger.Debug().Msg("rehydrate: no container-holding sessions found")
	}
	return rehydrated, nil
}

// RehydrateSandboxAuthListeners runs the freestanding RehydrateSandboxAuthListeners
// helper using the orchestrator's already-wired dependencies. Convenience for
// callers (cmd/server/main.go) that already have an Orchestrator and would
// otherwise have to rewire sessions/repos/orgs/provider/sandboxAuth into the
// freestanding form.
//
// Returns nil and logs at debug if the orchestrator wasn't configured with a
// sandbox auth server (legacy GITHUB_TOKEN env path, or local-dev with no
// SANDBOX_AUTH_SOCKET_DIR). The sessions store must implement
// ContainerHoldingSessionLister; if it doesn't (e.g., a stub in tests that
// only implements the orchestrator-time methods), this returns nil with a
// warn-level log so the boot path isn't fatal.
func (o *Orchestrator) RehydrateSandboxAuthListeners(ctx context.Context) (map[uuid.UUID]struct{}, error) {
	if o.sandboxAuth == nil {
		o.logger.Debug().Msg("rehydrate: orchestrator has no sandbox auth server; skipping")
		return nil, nil
	}
	lister, ok := o.sessions.(ContainerHoldingSessionLister)
	if !ok {
		o.logger.Warn().Msg("rehydrate: session store does not implement ContainerHoldingSessionLister; skipping")
		return nil, nil
	}
	return RehydrateSandboxAuthListeners(
		ctx,
		lister,
		o.repositories,
		o.sandboxAuthOrgSettings,
		o.provider,
		o.sandboxAuth,
		o.logger,
	)
}
