package sandboxauth

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
)

type brokerSessionStoreStub struct {
	mu      sync.Mutex
	session models.Session
	err     error
	calls   int
}

func (s *brokerSessionStoreStub) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return models.Session{}, s.err
	}
	return s.session, nil
}

func (s *brokerSessionStoreStub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type brokerRepositoryStoreStub struct {
	repo models.Repository
	err  error
}

func (s *brokerRepositoryStoreStub) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error) {
	if s.err != nil {
		return models.Repository{}, s.err
	}
	return s.repo, nil
}

type brokerOrganizationStoreStub struct {
	org models.Organization
	err error
}

func (s *brokerOrganizationStoreStub) GetByID(context.Context, uuid.UUID) (models.Organization, error) {
	if s.err != nil {
		return models.Organization{}, s.err
	}
	return s.org, nil
}

type lockedResolver struct {
	mu         sync.Mutex
	resolution *identity.Resolution
	calls      int
}

func (r *lockedResolver) ResolveSandbox(context.Context, *models.Session, *models.Repository, string) (*identity.Resolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.resolution, nil
}

func newBrokerTestDeps(t *testing.T) (*Broker, uuid.UUID, uuid.UUID, uuid.UUID, *brokerSessionStoreStub) {
	t.Helper()

	orgID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	sessionStore := &brokerSessionStoreStub{
		session: models.Session{
			ID:           sessionID,
			OrgID:        orgID,
			RepositoryID: &repoID,
		},
	}
	repoStore := &brokerRepositoryStoreStub{
		repo: models.Repository{
			ID:             repoID,
			OrgID:          orgID,
			FullName:       "owner/repo",
			InstallationID: 123,
		},
	}
	settings, err := json.Marshal(models.OrgSettings{PRAuthorship: models.PRAuthorshipAppOnly})
	require.NoError(t, err, "test org settings should marshal")
	orgStore := &brokerOrganizationStoreStub{
		org: models.Organization{ID: orgID, Settings: settings},
	}
	resolver := &lockedResolver{resolution: &identity.Resolution{Token: "ghs_test", Source: identity.SourceApp}}
	server := NewServer(resolver, shortSocketDir(t), zerolog.Nop())
	t.Cleanup(server.Shutdown)
	return NewBroker(server, sessionStore, repoStore, orgStore, zerolog.Nop()), orgID, sessionID, repoID, sessionStore
}

func TestBroker_RefCountsHoldersAndClosesOnlyAfterLastRelease(t *testing.T) {
	t.Parallel()

	broker, orgID, sessionID, _, sessions := newBrokerTestDeps(t)
	holderA := uuid.New()
	holderB := uuid.New()

	socketPath, err := broker.Acquire(context.Background(), orgID, sessionID, holderA)
	require.NoError(t, err, "first acquire should open a sandbox auth listener")
	secondPath, err := broker.Acquire(context.Background(), orgID, sessionID, holderB)
	require.NoError(t, err, "second acquire should attach to the existing listener")
	require.Equal(t, socketPath, secondPath, "concurrent holders for one session should share the deterministic socket")
	require.Equal(t, 1, sessions.callCount(), "active broker entries should be reused without reloading session state")

	resp, err := NewClient(socketPath).Get(context.Background(), ActionPush)
	require.NoError(t, err, "socket should serve credentials while both holders are active")
	require.Equal(t, "ghs_test", resp.Token, "socket should return resolver token")

	require.NoError(t, broker.Release(context.Background(), orgID, sessionID, holderA), "releasing one holder should succeed")
	resp, err = NewClient(socketPath).Get(context.Background(), ActionPush)
	require.NoError(t, err, "socket should remain live until the final holder releases")
	require.Equal(t, "ghs_test", resp.Token, "socket should still return credentials after partial release")

	require.NoError(t, broker.Release(context.Background(), orgID, sessionID, holderB), "releasing the final holder should close the socket")
	_, statErr := os.Stat(socketPath)
	require.True(t, errors.Is(statErr, os.ErrNotExist), "socket file should be removed after the final holder releases")
}

func TestBroker_ReleaseUnknownHolderDoesNotCloseActiveSocket(t *testing.T) {
	t.Parallel()

	broker, orgID, sessionID, _, _ := newBrokerTestDeps(t)
	holder := uuid.New()
	socketPath, err := broker.Acquire(context.Background(), orgID, sessionID, holder)
	require.NoError(t, err, "acquire should open a sandbox auth listener")

	require.NoError(t, broker.Release(context.Background(), orgID, sessionID, uuid.New()), "unknown holder release should be idempotent")
	resp, err := NewClient(socketPath).Get(context.Background(), ActionAPI)
	require.NoError(t, err, "unknown holder release should not close the live listener")
	require.Equal(t, "ghs_test", resp.Token, "socket should continue serving credentials after unknown release")

	require.NoError(t, broker.Release(context.Background(), orgID, sessionID, holder), "known holder release should close the socket")
}

type countingBrokerServer struct {
	mu          sync.Mutex
	listenCalls int
	closeCalls  int
	socketPath  string
}

func (s *countingBrokerServer) Listen(context.Context, uuid.UUID, *models.Session, *models.Repository, models.OrgSettings) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listenCalls++
	time.Sleep(5 * time.Millisecond)
	return s.socketPath, nil
}

func (s *countingBrokerServer) Close(uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
}

func (s *countingBrokerServer) Shutdown() {}

func (s *countingBrokerServer) SocketPath(sessionID uuid.UUID) string {
	return "/tmp/143-auth-test/" + sessionID.String() + "/sock"
}

func (s *countingBrokerServer) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listenCalls, s.closeCalls
}

func TestBroker_ConcurrentAcquireCreatesOneListener(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	sessionStore := &brokerSessionStoreStub{session: models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}}
	repoStore := &brokerRepositoryStoreStub{repo: models.Repository{ID: repoID, OrgID: orgID, FullName: "owner/repo", InstallationID: 123}}
	orgStore := &brokerOrganizationStoreStub{org: models.Organization{ID: orgID}}
	server := &countingBrokerServer{socketPath: "/tmp/143-auth-test/sock"}
	broker := NewBroker(server, sessionStore, repoStore, orgStore, zerolog.Nop())

	const holders = 16
	var wg sync.WaitGroup
	errCh := make(chan error, holders)
	pathCh := make(chan string, holders)
	holderIDs := make([]uuid.UUID, holders)
	for i := 0; i < holders; i++ {
		holderIDs[i] = uuid.New()
		holderID := holderIDs[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			socketPath, err := broker.Acquire(context.Background(), orgID, sessionID, holderID)
			if err != nil {
				errCh <- err
				return
			}
			pathCh <- socketPath
			errCh <- nil
		}()
	}
	wg.Wait()
	close(errCh)
	close(pathCh)

	for err := range errCh {
		require.NoError(t, err, "concurrent acquire/release should not fail")
	}
	for socketPath := range pathCh {
		require.Equal(t, server.socketPath, socketPath, "all holders should receive the same socket path")
	}
	listenCalls, closeCalls := server.counts()
	require.Equal(t, 1, listenCalls, "broker should serialize concurrent acquires into one listener")
	require.Equal(t, 0, closeCalls, "broker should not close before holders release")
	for _, holderID := range holderIDs {
		require.NoError(t, broker.Release(context.Background(), orgID, sessionID, holderID), "releasing acquired holders should succeed")
	}
	listenCalls, closeCalls = server.counts()
	require.Equal(t, 1, listenCalls, "broker should not reopen during release")
	require.Equal(t, 1, closeCalls, "broker should close once after the final concurrent holder releases")
}

func TestLeaseClient_ReleasesOneGeneratedHolderPerClose(t *testing.T) {
	t.Parallel()

	broker, orgID, sessionID, repoID, _ := newBrokerTestDeps(t)
	repo := &models.Repository{ID: repoID, OrgID: orgID, FullName: "owner/repo", InstallationID: 123}
	run := &models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}
	client := NewLeaseClient(broker, "test", zerolog.Nop())

	socketPath, err := client.Listen(context.Background(), sessionID, run, repo, models.OrgSettings{})
	require.NoError(t, err, "first local lease should open a listener")
	secondPath, err := client.Listen(context.Background(), sessionID, run, repo, models.OrgSettings{})
	require.NoError(t, err, "second local lease should attach to the same listener")
	require.Equal(t, socketPath, secondPath, "local lease client should reuse the active socket path")

	client.Close(sessionID)
	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	require.NoError(t, err, "socket should still accept connections after one local close")
	require.NoError(t, conn.Close(), "test connection should close cleanly")

	client.Close(sessionID)
	_, statErr := os.Stat(socketPath)
	require.True(t, errors.Is(statErr, os.ErrNotExist), "socket file should be removed after all local leases close")
}

func TestBroker_ContainerLeasePinSurvivesTurnHolderRelease(t *testing.T) {
	t.Parallel()

	broker, orgID, sessionID, repoID, _ := newBrokerTestDeps(t)
	run := &models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}
	repo := &models.Repository{ID: repoID, OrgID: orgID, FullName: "owner/repo", InstallationID: 123}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipAppOnly}

	// The reconciler pins a container lease for a live sandbox.
	socketPath, err := broker.EnsureContainerLease(context.Background(), sessionID, run, repo, settings)
	require.NoError(t, err, "pinning a container lease should open the listener")

	// A turn acquires then releases its short-lived holder lease.
	holder := uuid.New()
	acquired, err := broker.Acquire(context.Background(), orgID, sessionID, holder)
	require.NoError(t, err, "turn holder should acquire against the pinned session")
	require.Equal(t, socketPath, acquired, "turn holder should attach to the pinned listener")
	require.NoError(t, broker.Release(context.Background(), orgID, sessionID, holder), "turn holder should release cleanly")

	// The socket must remain live across the turn boundary because the
	// container lease still pins it — the entire point of the fix.
	resp, err := NewClient(socketPath).Get(context.Background(), ActionPush)
	require.NoError(t, err, "socket must stay open after the turn holder releases while the container is pinned")
	require.Equal(t, "ghs_test", resp.Token, "pinned socket should keep serving credentials between turns")

	// Releasing the container lease (container reaped) finally closes it.
	require.NoError(t, broker.ReleaseContainerLease(orgID, sessionID), "releasing the container lease should succeed")
	_, statErr := os.Stat(socketPath)
	require.True(t, errors.Is(statErr, os.ErrNotExist), "socket should be removed once the container lease is released and no holders remain")
}

func TestBroker_ContainerLeaseReleaseKeepsSocketWhileHolderActive(t *testing.T) {
	t.Parallel()

	broker, orgID, sessionID, repoID, _ := newBrokerTestDeps(t)
	run := &models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}
	repo := &models.Repository{ID: repoID, OrgID: orgID, FullName: "owner/repo", InstallationID: 123}

	socketPath, err := broker.EnsureContainerLease(context.Background(), sessionID, run, repo, models.OrgSettings{})
	require.NoError(t, err, "pinning a container lease should open the listener")

	holder := uuid.New()
	_, err = broker.Acquire(context.Background(), orgID, sessionID, holder)
	require.NoError(t, err, "turn holder should acquire against the pinned session")

	// Container reaped mid-turn: the reconciler unpins, but the in-flight turn
	// holder must keep the socket open so the running push doesn't break.
	require.NoError(t, broker.ReleaseContainerLease(orgID, sessionID), "releasing the container lease mid-turn should succeed")
	resp, err := NewClient(socketPath).Get(context.Background(), ActionPush)
	require.NoError(t, err, "socket should remain open while a turn holder is active even after the container pin is dropped")
	require.Equal(t, "ghs_test", resp.Token, "socket should keep serving while a holder remains")

	// The final holder release now closes it, since the pin is already gone.
	require.NoError(t, broker.Release(context.Background(), orgID, sessionID, holder), "final holder release should close the unpinned socket")
	_, statErr := os.Stat(socketPath)
	require.True(t, errors.Is(statErr, os.ErrNotExist), "socket should close once both the pin and the last holder are gone")
}

func TestBroker_EnsureContainerLeaseIsIdempotent(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	run := &models.Session{ID: sessionID, OrgID: orgID, RepositoryID: &repoID}
	repo := &models.Repository{ID: repoID, OrgID: orgID, FullName: "owner/repo", InstallationID: 123}
	sessionStore := &brokerSessionStoreStub{session: *run}
	repoStore := &brokerRepositoryStoreStub{repo: *repo}
	orgStore := &brokerOrganizationStoreStub{org: models.Organization{ID: orgID}}
	server := &countingBrokerServer{socketPath: "/tmp/143-auth-test/sock"}
	broker := NewBroker(server, sessionStore, repoStore, orgStore, zerolog.Nop())

	for i := 0; i < 3; i++ {
		_, err := broker.EnsureContainerLease(context.Background(), sessionID, run, repo, models.OrgSettings{})
		require.NoError(t, err, "repeated container-lease pins should succeed")
	}
	listenCalls, closeCalls := server.counts()
	require.Equal(t, 1, listenCalls, "repeated EnsureContainerLease should open the listener exactly once")
	require.Equal(t, 0, closeCalls, "pinning should never close the listener")

	require.NoError(t, broker.ReleaseContainerLease(orgID, sessionID), "releasing the idempotently-pinned lease should succeed")
	listenCalls, closeCalls = server.counts()
	require.Equal(t, 1, listenCalls, "release should not reopen the listener")
	require.Equal(t, 1, closeCalls, "a single ReleaseContainerLease should close the idempotently-pinned listener once")
}

func TestBroker_ReleaseContainerLeaseUnknownSessionIsNoop(t *testing.T) {
	t.Parallel()

	broker, orgID, _, _, _ := newBrokerTestDeps(t)
	require.NoError(t, broker.ReleaseContainerLease(orgID, uuid.New()), "releasing a container lease for an unknown session should be a no-op")
	require.NoError(t, broker.ReleaseContainerLease(uuid.Nil, uuid.New()), "releasing with a nil org should also be a no-op for unknown sessions")
}
