package sandboxauth

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

type brokerSocketServer interface {
	Listen(ctx context.Context, sessionID uuid.UUID, run *models.Session, repo *models.Repository, orgSettings models.OrgSettings) (socketPath string, err error)
	Close(sessionID uuid.UUID)
	Shutdown()
	// SocketPath returns the deterministic on-host socket path for a session,
	// independent of whether a listener is currently bound there.
	SocketPath(sessionID uuid.UUID) string
}

type BrokerSessionStore interface {
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
}

type BrokerRepositoryStore interface {
	GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error)
}

type BrokerOrganizationStore interface {
	GetByID(ctx context.Context, orgID uuid.UUID) (models.Organization, error)
}

// Broker is the worker-owned lease manager for sandbox GitHub auth sockets.
// It keeps socket listener ownership in the long-lived worker process while
// session executors and direct worker callers hold explicit leases. A session
// socket closes only after the final holder releases, so one runtime ending
// cannot unlink the socket out from under a sibling tab or PR push.
type Broker struct {
	server       brokerSocketServer
	sessions     BrokerSessionStore
	repositories BrokerRepositoryStore
	orgs         BrokerOrganizationStore
	logger       zerolog.Logger

	mu     sync.Mutex
	active map[uuid.UUID]*brokerEntry
}

type brokerEntry struct {
	orgID      uuid.UUID
	socketPath string
	holders    map[uuid.UUID]int

	// containerPinned marks a listener that must stay open for as long as the
	// session's sandbox container is alive on this host, independent of the
	// per-turn holder leases above. Set by EnsureContainerLease (driven by the
	// worker's container reconciler) and cleared by ReleaseContainerLease. The
	// socket closes only when BOTH the holder count is zero AND the container
	// pin is cleared — so a turn ending no longer tears the socket down out
	// from under a held-alive sandbox waiting for its next push.
	containerPinned bool
}

func NewBroker(
	server brokerSocketServer,
	sessions BrokerSessionStore,
	repositories BrokerRepositoryStore,
	orgs BrokerOrganizationStore,
	logger zerolog.Logger,
) *Broker {
	return &Broker{
		server:       server,
		sessions:     sessions,
		repositories: repositories,
		orgs:         orgs,
		logger:       logger,
		active:       make(map[uuid.UUID]*brokerEntry),
	}
}

func (b *Broker) Acquire(ctx context.Context, orgID, sessionID, holderID uuid.UUID) (string, error) {
	if b == nil || b.server == nil {
		return "", fmt.Errorf("sandboxauth broker is not configured")
	}
	if orgID == uuid.Nil {
		return "", fmt.Errorf("sandboxauth broker acquire: org id is required")
	}
	if sessionID == uuid.Nil {
		return "", fmt.Errorf("sandboxauth broker acquire: session id is required")
	}
	if holderID == uuid.Nil {
		return "", fmt.Errorf("sandboxauth broker acquire: holder id is required")
	}
	if socketPath, ok, err := b.attachExisting(orgID, sessionID, holderID); ok || err != nil {
		return socketPath, err
	}
	if b.sessions == nil {
		return "", fmt.Errorf("sandboxauth broker acquire: session store is not configured")
	}
	if b.repositories == nil {
		return "", fmt.Errorf("sandboxauth broker acquire: repository store is not configured")
	}
	if b.orgs == nil {
		return "", fmt.Errorf("sandboxauth broker acquire: organization store is not configured")
	}

	run, err := b.sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		return "", fmt.Errorf("sandboxauth broker acquire: load session: %w", err)
	}
	if run.ID != sessionID {
		return "", fmt.Errorf("sandboxauth broker acquire: loaded session id %s does not match requested session %s", run.ID, sessionID)
	}
	if run.OrgID != orgID {
		return "", fmt.Errorf("sandboxauth broker acquire: loaded session org %s does not match requested org %s", run.OrgID, orgID)
	}
	if run.RepositoryID == nil || *run.RepositoryID == uuid.Nil {
		return "", fmt.Errorf("sandboxauth broker acquire: session has no repository")
	}
	repo, err := b.repositories.GetByID(ctx, orgID, *run.RepositoryID)
	if err != nil {
		return "", fmt.Errorf("sandboxauth broker acquire: load repository: %w", err)
	}
	if repo.OrgID != orgID {
		return "", fmt.Errorf("sandboxauth broker acquire: loaded repository org %s does not match requested org %s", repo.OrgID, orgID)
	}
	org, err := b.orgs.GetByID(ctx, orgID)
	if err != nil {
		return "", fmt.Errorf("sandboxauth broker acquire: load organization: %w", err)
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return "", fmt.Errorf("sandboxauth broker acquire: parse org settings: %w", err)
	}
	return b.AcquirePrepared(ctx, sessionID, holderID, &run, &repo, settings)
}

func (b *Broker) AcquirePrepared(
	ctx context.Context,
	sessionID uuid.UUID,
	holderID uuid.UUID,
	run *models.Session,
	repo *models.Repository,
	orgSettings models.OrgSettings,
) (string, error) {
	if holderID == uuid.Nil {
		return "", fmt.Errorf("sandboxauth broker acquire: holder id is required")
	}
	if err := validatePreparedListen(sessionID, run, repo); err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry := b.active[sessionID]; entry != nil {
		if entry.orgID != run.OrgID {
			return "", fmt.Errorf("sandboxauth broker acquire: active session org %s does not match requested org %s", entry.orgID, run.OrgID)
		}
		entry.holders[holderID]++
		return entry.socketPath, nil
	}
	socketPath, err := b.server.Listen(ctx, sessionID, run, repo, orgSettings)
	if err != nil {
		return "", err
	}
	b.active[sessionID] = &brokerEntry{
		orgID:      run.OrgID,
		socketPath: socketPath,
		holders:    map[uuid.UUID]int{holderID: 1},
	}
	return socketPath, nil
}

// EnsureContainerLease pins the session's credential socket open for the
// lifetime of its sandbox container, independent of per-turn holder leases.
// It is the worker-side counterpart to a live container: the container
// reconciler calls it for every sandbox alive on this host so the socket
// survives turn boundaries and worker restarts, and calls ReleaseContainerLease
// when the container is gone.
//
// Idempotent: pinning an already-pinned (or holder-leased) session just marks
// the existing listener pinned and returns its socket path without re-binding.
// If no listener exists yet it opens one — so EnsureContainerLease both
// rehydrates a socket whose owning process restarted and keeps it open across
// the gaps between turn leases.
func (b *Broker) EnsureContainerLease(
	ctx context.Context,
	sessionID uuid.UUID,
	run *models.Session,
	repo *models.Repository,
	orgSettings models.OrgSettings,
) (string, error) {
	if err := validatePreparedListen(sessionID, run, repo); err != nil {
		return "", err
	}
	if b == nil || b.server == nil {
		return "", fmt.Errorf("sandboxauth broker is not configured")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry := b.active[sessionID]; entry != nil {
		if entry.orgID != run.OrgID {
			return "", fmt.Errorf("sandboxauth broker container lease: active session org %s does not match requested org %s", entry.orgID, run.OrgID)
		}
		entry.containerPinned = true
		return entry.socketPath, nil
	}
	socketPath, err := b.server.Listen(ctx, sessionID, run, repo, orgSettings)
	if err != nil {
		return "", err
	}
	b.active[sessionID] = &brokerEntry{
		orgID:           run.OrgID,
		socketPath:      socketPath,
		holders:         make(map[uuid.UUID]int),
		containerPinned: true,
	}
	return socketPath, nil
}

// ReleaseContainerLease drops the container pin for a session — called when the
// reconciler observes the container is no longer alive on this host. The socket
// is closed immediately if no per-turn holder lease remains; if a turn is still
// in flight the socket stays open until that last holder releases (which will
// then close it, since the pin is gone). Idempotent and safe to call for an
// unknown session.
func (b *Broker) ReleaseContainerLease(orgID, sessionID uuid.UUID) error {
	if b == nil || b.server == nil {
		return nil
	}
	if sessionID == uuid.Nil {
		return fmt.Errorf("sandboxauth broker container release: session id is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.active[sessionID]
	if entry == nil {
		return nil
	}
	if orgID != uuid.Nil && entry.orgID != orgID {
		return fmt.Errorf("sandboxauth broker container release: active session org %s does not match requested org %s", entry.orgID, orgID)
	}
	entry.containerPinned = false
	if len(entry.holders) == 0 {
		delete(b.active, sessionID)
		b.server.Close(sessionID)
	}
	return nil
}

// ContainerSocketState reports the deterministic socket path for a session and
// whether this broker currently owns a local listener entry for it. The
// container reconciler uses it to avoid stealing a socket that a sibling worker
// generation is still serving during a rolling deploy: when there is no local
// entry but the path is live, the reconciler adopts (leaves it alone) and takes
// over only once that foreign listener is gone.
func (b *Broker) ContainerSocketState(sessionID uuid.UUID) (string, bool) {
	if b == nil || b.server == nil {
		return "", false
	}
	b.mu.Lock()
	_, has := b.active[sessionID]
	b.mu.Unlock()
	return b.server.SocketPath(sessionID), has
}

func (b *Broker) Release(ctx context.Context, orgID, sessionID, holderID uuid.UUID) error {
	_ = ctx
	if b == nil || b.server == nil {
		return nil
	}
	if orgID == uuid.Nil {
		return fmt.Errorf("sandboxauth broker release: org id is required")
	}
	if sessionID == uuid.Nil {
		return fmt.Errorf("sandboxauth broker release: session id is required")
	}
	if holderID == uuid.Nil {
		return fmt.Errorf("sandboxauth broker release: holder id is required")
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.active[sessionID]
	if entry == nil {
		return nil
	}
	if entry.orgID != orgID {
		return fmt.Errorf("sandboxauth broker release: active session org %s does not match requested org %s", entry.orgID, orgID)
	}
	b.releaseLocked(sessionID, holderID)
	return nil
}

func (b *Broker) ReleaseHolder(sessionID, holderID uuid.UUID) error {
	if b == nil || b.server == nil {
		return nil
	}
	if sessionID == uuid.Nil {
		return fmt.Errorf("sandboxauth broker release: session id is required")
	}
	if holderID == uuid.Nil {
		return fmt.Errorf("sandboxauth broker release: holder id is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.releaseLocked(sessionID, holderID)
	return nil
}

func (b *Broker) Shutdown() {
	if b == nil || b.server == nil {
		return
	}
	b.mu.Lock()
	for sessionID := range b.active {
		b.server.Close(sessionID)
	}
	b.active = make(map[uuid.UUID]*brokerEntry)
	b.mu.Unlock()
	b.server.Shutdown()
}

func (b *Broker) attachExisting(orgID, sessionID, holderID uuid.UUID) (string, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.active[sessionID]
	if entry == nil {
		return "", false, nil
	}
	if entry.orgID != orgID {
		return "", true, fmt.Errorf("sandboxauth broker acquire: active session org %s does not match requested org %s", entry.orgID, orgID)
	}
	entry.holders[holderID]++
	return entry.socketPath, true, nil
}

func (b *Broker) releaseLocked(sessionID, holderID uuid.UUID) {
	entry := b.active[sessionID]
	if entry == nil {
		return
	}
	count := entry.holders[holderID]
	if count <= 0 {
		return
	}
	if count > 1 {
		entry.holders[holderID] = count - 1
		return
	}
	delete(entry.holders, holderID)
	if len(entry.holders) > 0 {
		return
	}
	// The last per-turn holder is gone, but keep the socket open if a container
	// lease still pins it — the sandbox is held alive across this turn boundary
	// and its next push must still reach a live listener.
	if entry.containerPinned {
		return
	}
	delete(b.active, sessionID)
	b.server.Close(sessionID)
}

func validatePreparedListen(sessionID uuid.UUID, run *models.Session, repo *models.Repository) error {
	if sessionID == uuid.Nil {
		return fmt.Errorf("sandboxauth broker listen: session id is required")
	}
	if run == nil {
		return fmt.Errorf("sandboxauth broker listen: session is required")
	}
	if repo == nil {
		return fmt.Errorf("sandboxauth broker listen: repository is required")
	}
	if run.ID != sessionID {
		return fmt.Errorf("sandboxauth broker listen: run id %s does not match session id %s", run.ID, sessionID)
	}
	if run.OrgID == uuid.Nil {
		return fmt.Errorf("sandboxauth broker listen: run org id is required")
	}
	if repo.OrgID != uuid.Nil && repo.OrgID != run.OrgID {
		return fmt.Errorf("sandboxauth broker listen: repo org %s does not match run org %s", repo.OrgID, run.OrgID)
	}
	if run.RepositoryID != nil && *run.RepositoryID != uuid.Nil && repo.ID != uuid.Nil && *run.RepositoryID != repo.ID {
		return fmt.Errorf("sandboxauth broker listen: repo id %s does not match run repository id %s", repo.ID, *run.RepositoryID)
	}
	return nil
}
