package agent

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// defaultSandboxAuthReconcileInterval is how often the worker reconciles
// per-session credential sockets against the sandbox containers actually alive
// on this host. Short enough that a held-alive sandbox created mid-turn gets a
// durable socket pin well before its turn ends (turns run minutes), long enough
// that the steady-state cost is one cheap local Docker list per tick.
const defaultSandboxAuthReconcileInterval = 30 * time.Second

// OrgSettingsLoader resolves an org's parsed settings for the credential
// resolver capture. Defined as a function type so callers can pass a thin
// closure over the org store (or a test stub) without dragging the full store
// interface into the reconciler.
type OrgSettingsLoader func(ctx context.Context, orgID uuid.UUID) (models.OrgSettings, error)

// SandboxAuthContainerLeaser is the broker surface the reconciler drives: pin a
// session's credential socket open while its container is alive, drop the pin
// when the container is gone, and report whether this process already owns a
// local listener for a session. *sandboxauth.Broker implements it.
type SandboxAuthContainerLeaser interface {
	EnsureContainerLease(ctx context.Context, sessionID uuid.UUID, run *models.Session, repo *models.Repository, orgSettings models.OrgSettings) (string, error)
	ReleaseContainerLease(orgID, sessionID uuid.UUID) error
	// ContainerSocketState returns the deterministic socket path for a session
	// and whether this process already owns a local listener entry for it.
	ContainerSocketState(sessionID uuid.UUID) (string, bool)
}

// ManagedSandboxLister enumerates the sandbox containers physically present on
// this host. Grounding the reconcile in local Docker — rather than the DB's
// worker_node_id bookkeeping — is what makes the credential socket survive
// rolling deploys: a new worker generation sees the same host containers no
// matter which (now-stale) node id the session row still records.
type ManagedSandboxLister interface {
	ListManagedSandboxes(ctx context.Context) ([]ManagedSandboxContainer, error)
}

// SandboxAuthSessionLoader loads a single session row by id. Narrow subset of
// the session store so the reconciler stays unit-testable.
type SandboxAuthSessionLoader interface {
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
}

// SandboxAuthSocketReconciler keeps the set of open per-session GitHub
// credential sockets in lockstep with the sandbox containers alive on this
// host. It is the worker-side owner of socket lifetime under the
// worker-owns-the-socket model (see
// docs/design/future/109-sandbox-auth-socket-ownership.md):
//
//   - The per-turn session executor still acquires/releases a short-lived
//     holder lease over the remote broker (which keeps the socket open during a
//     turn and hands the executor the socket path to bind-mount).
//   - This reconciler additionally pins a *container* lease for every live
//     sandbox, so the socket stays bound across turn boundaries and is
//     re-bound after a worker restart — closing the window where a held-alive
//     sandbox's `git push` dialed a dead socket.
//
// It is self-healing: pins are derived from live Docker state every tick, so a
// reaped container's socket is released without needing every destroy path to
// notify us, and a worker restart re-pins everything on its first pass.
type SandboxAuthSocketReconciler struct {
	leaser      SandboxAuthContainerLeaser
	provider    ManagedSandboxLister
	sessions    SandboxAuthSessionLoader
	repos       RepositoryStore
	orgSettings OrgSettingsLoader
	interval    time.Duration
	logger      zerolog.Logger

	// socketLive probes whether some process is already serving the socket at a
	// path. Overridable in tests; defaults to a short Unix-socket dial.
	socketLive func(path string) bool

	// pinned tracks the sessions we currently hold a container lease for so a
	// later tick can release the lease once the container disappears. It is
	// reconciler-owned and only ever touched from ReconcileOnce, which Run
	// drives serially — no locking needed. Resetting it on process start is
	// correct: the broker it pins against is also empty on process start, and
	// the first tick re-pins every live container.
	pinned map[uuid.UUID]uuid.UUID // sessionID -> orgID
}

// NewSandboxAuthSocketReconciler builds a reconciler. A non-positive interval
// falls back to defaultSandboxAuthReconcileInterval.
func NewSandboxAuthSocketReconciler(
	leaser SandboxAuthContainerLeaser,
	provider ManagedSandboxLister,
	sessions SandboxAuthSessionLoader,
	repos RepositoryStore,
	orgSettings OrgSettingsLoader,
	interval time.Duration,
	logger zerolog.Logger,
) *SandboxAuthSocketReconciler {
	if interval <= 0 {
		interval = defaultSandboxAuthReconcileInterval
	}
	return &SandboxAuthSocketReconciler{
		leaser:      leaser,
		provider:    provider,
		sessions:    sessions,
		repos:       repos,
		orgSettings: orgSettings,
		interval:    interval,
		logger:      logger,
		socketLive:  sandboxAuthSocketLive,
		pinned:      make(map[uuid.UUID]uuid.UUID),
	}
}

// sandboxAuthSocketLive reports whether a Unix-domain socket at path has a live
// listener. A successful connect (immediately closed — the server treats a
// no-request connection as a quiet probe) means someone is serving it;
// ECONNREFUSED / ENOENT mean no one is.
func sandboxAuthSocketLive(path string) bool {
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Run reconciles on every interval tick until ctx is done. Callers that need
// an immediate pass (e.g. to re-bind sockets at worker startup before jobs are
// accepted) should call ReconcileOnce synchronously first, then start Run for
// the steady-state loop.
func (r *SandboxAuthSocketReconciler) Run(ctx context.Context) {
	if r == nil {
		return
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.ReconcileOnce(ctx); err != nil {
				r.logger.Warn().Err(err).Msg("sandboxauth reconcile: tick failed; will retry on next tick")
			}
		}
	}
}

type managedSandboxRef struct {
	container ManagedSandboxContainer
	orgID     uuid.UUID
}

// ReconcileOnce makes the set of container-pinned credential sockets match the
// sandbox containers alive on this host: it pins newly-observed containers and
// releases pins for containers that are gone.
//
// Crucially, it only releases pins when the Docker enumeration SUCCEEDS — a
// transient Docker error returns early without dropping any pins, so a daemon
// hiccup can never be misread as "every container vanished" and tear down live
// sockets.
func (r *SandboxAuthSocketReconciler) ReconcileOnce(ctx context.Context) error {
	if r == nil || r.leaser == nil || r.provider == nil {
		return nil
	}

	containers, err := r.provider.ListManagedSandboxes(ctx)
	if err != nil {
		return fmt.Errorf("list managed sandboxes: %w", err)
	}

	live := make(map[uuid.UUID]managedSandboxRef, len(containers))
	for _, c := range containers {
		orgID, sessionID, perr := parseManagedSandboxIDs(c)
		if perr != nil || sessionID == uuid.Nil {
			// Unlabeled / legacy container we can't map to a session. The GC
			// handles those by other means; there's no socket to pin.
			continue
		}
		live[sessionID] = managedSandboxRef{container: c, orgID: orgID}
	}

	var pinnedCount, releasedCount, failedCount int

	// Pin containers we're not already holding a lease for.
	var adoptedCount int
	for sessionID, ref := range live {
		if _, ok := r.pinned[sessionID]; ok {
			continue
		}
		// Cross-generation safety: if we don't already own a local listener for
		// this session but its socket is already live, another worker
		// generation (draining on this same host during a rolling deploy) is
		// serving it. Don't steal it — leave it alone and re-check next tick;
		// we take over only once that listener is gone. Without this guard the
		// reconciler would unlink a sibling's live socket on every deploy.
		path, hasLocal := r.leaser.ContainerSocketState(sessionID)
		if !hasLocal && r.socketLive(path) {
			adoptedCount++
			continue
		}
		orgID, err := r.pinContainer(ctx, sessionID, ref.orgID)
		if err != nil {
			failedCount++
			r.logger.Debug().Err(err).
				Str("session_id", sessionID.String()).
				Str("container_id", ref.container.ID).
				Msg("sandboxauth reconcile: failed to pin container socket; will retry next tick")
			continue
		}
		r.pinned[sessionID] = orgID
		pinnedCount++
	}

	// Release pins for containers that are no longer alive on this host.
	for sessionID, orgID := range r.pinned {
		if _, ok := live[sessionID]; ok {
			continue
		}
		if err := r.leaser.ReleaseContainerLease(orgID, sessionID); err != nil {
			r.logger.Warn().Err(err).
				Str("session_id", sessionID.String()).
				Msg("sandboxauth reconcile: failed to release container socket lease; dropping pin anyway")
		}
		// Drop the pin from our map regardless: the container is gone. Keeping a
		// stale entry would block a re-pin if the session's container ever
		// reappears under the same id.
		delete(r.pinned, sessionID)
		releasedCount++
	}

	if pinnedCount > 0 || releasedCount > 0 || failedCount > 0 {
		r.logger.Info().
			Int("pinned", pinnedCount).
			Int("released", releasedCount).
			Int("adopted", adoptedCount).
			Int("failed", failedCount).
			Int("active", len(r.pinned)).
			Msg("sandboxauth reconcile: container socket leases reconciled")
	}
	return nil
}

// pinContainer loads the session/repo/org-settings the resolver needs and pins
// the credential socket open for the container. Returns the authoritative org
// id from the loaded session (used as the release key) on success.
func (r *SandboxAuthSocketReconciler) pinContainer(ctx context.Context, sessionID, labelOrgID uuid.UUID) (uuid.UUID, error) {
	run, err := r.sessions.GetByID(ctx, labelOrgID, sessionID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("load session: %w", err)
	}
	if run.RepositoryID == nil || *run.RepositoryID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("session has no repository linked")
	}
	repo, err := r.repos.GetByID(ctx, run.OrgID, *run.RepositoryID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("load repository: %w", err)
	}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	if r.orgSettings != nil {
		loaded, err := r.orgSettings(ctx, run.OrgID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("load org settings: %w", err)
		}
		settings = loaded
	}
	if _, err := r.leaser.EnsureContainerLease(ctx, sessionID, &run, &repo, settings); err != nil {
		return uuid.Nil, fmt.Errorf("ensure container lease: %w", err)
	}
	return run.OrgID, nil
}
