package agent

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type fakeContainerLeaser struct {
	mu        sync.Mutex
	ensured   []uuid.UUID
	released  []uuid.UUID
	ensureErr map[uuid.UUID]error
	entries   map[uuid.UUID]bool // sessions for which we hold a local listener
}

func newFakeContainerLeaser() *fakeContainerLeaser {
	return &fakeContainerLeaser{ensureErr: map[uuid.UUID]error{}, entries: map[uuid.UUID]bool{}}
}

func (f *fakeContainerLeaser) EnsureContainerLease(_ context.Context, sessionID uuid.UUID, _ *models.Session, _ *models.Repository, _ models.OrgSettings) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureErr[sessionID]; err != nil {
		return "", err
	}
	f.ensured = append(f.ensured, sessionID)
	f.entries[sessionID] = true
	return socketPathFor(sessionID), nil
}

func (f *fakeContainerLeaser) ReleaseContainerLease(_ uuid.UUID, sessionID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, sessionID)
	delete(f.entries, sessionID)
	return nil
}

func (f *fakeContainerLeaser) ContainerSocketState(sessionID uuid.UUID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return socketPathFor(sessionID), f.entries[sessionID]
}

func socketPathFor(sessionID uuid.UUID) string {
	return "/run/143-auth/" + sessionID.String() + "/sock"
}

func (f *fakeContainerLeaser) ensuredSorted() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]uuid.UUID(nil), f.ensured...)
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func (f *fakeContainerLeaser) releasedSlice() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uuid.UUID(nil), f.released...)
}

type fakeManagedLister struct {
	mu         sync.Mutex
	containers []ManagedSandboxContainer
	err        error
}

func (f *fakeManagedLister) set(containers []ManagedSandboxContainer, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers = containers
	f.err = err
}

func (f *fakeManagedLister) ListManagedSandboxes(context.Context) ([]ManagedSandboxContainer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return append([]ManagedSandboxContainer(nil), f.containers...), nil
}

type fakeSessionLoader struct {
	sessions map[uuid.UUID]models.Session
	errs     map[uuid.UUID]error
}

func (f *fakeSessionLoader) GetByID(_ context.Context, _ uuid.UUID, sessionID uuid.UUID) (models.Session, error) {
	if err := f.errs[sessionID]; err != nil {
		return models.Session{}, err
	}
	if s, ok := f.sessions[sessionID]; ok {
		return s, nil
	}
	return models.Session{}, errors.New("session not found")
}

type fakeRepoLoader struct {
	repos map[uuid.UUID]models.Repository
}

func (f *fakeRepoLoader) GetByID(_ context.Context, _ uuid.UUID, repoID uuid.UUID) (models.Repository, error) {
	if r, ok := f.repos[repoID]; ok {
		return r, nil
	}
	return models.Repository{}, errors.New("repo not found")
}

// reconcilerFixture wires a reconciler over fakes with one well-formed,
// mappable live session by default.
type reconcilerFixture struct {
	reconciler *SandboxAuthSocketReconciler
	leaser     *fakeContainerLeaser
	lister     *fakeManagedLister
	orgID      uuid.UUID
	sessionID  uuid.UUID
	repoID     uuid.UUID

	mu        sync.Mutex
	livePaths map[string]bool // paths a (simulated foreign) process is serving
}

func (f *reconcilerFixture) setLive(path string, live bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.livePaths[path] = live
}

func newReconcilerFixture(t *testing.T) *reconcilerFixture {
	t.Helper()
	orgID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	leaser := newFakeContainerLeaser()
	lister := &fakeManagedLister{}
	sessions := &fakeSessionLoader{
		sessions: map[uuid.UUID]models.Session{
			sessionID: {ID: sessionID, OrgID: orgID, RepositoryID: &repoID},
		},
		errs: map[uuid.UUID]error{},
	}
	repos := &fakeRepoLoader{repos: map[uuid.UUID]models.Repository{
		repoID: {ID: repoID, OrgID: orgID, FullName: "owner/repo"},
	}}
	orgSettings := func(context.Context, uuid.UUID) (models.OrgSettings, error) {
		return models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}, nil
	}
	r := NewSandboxAuthSocketReconciler(leaser, lister, sessions, repos, orgSettings, 0, zerolog.Nop())
	f := &reconcilerFixture{
		reconciler: r, leaser: leaser, lister: lister,
		orgID: orgID, sessionID: sessionID, repoID: repoID,
		livePaths: map[string]bool{},
	}
	// Liveness is driven by the fixture rather than a real dial, so cross-gen
	// tests can simulate a sibling generation still serving a socket.
	r.socketLive = func(path string) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.livePaths[path]
	}
	return f
}

func managed(orgID, sessionID uuid.UUID) ManagedSandboxContainer {
	return ManagedSandboxContainer{
		ID:        "container-" + sessionID.String(),
		SessionID: sessionID.String(),
		OrgID:     orgID.String(),
		Purpose:   "agent_run",
	}
}

func TestSandboxAuthSocketReconciler_PinsLiveContainers(t *testing.T) {
	t.Parallel()
	f := newReconcilerFixture(t)
	f.lister.set([]ManagedSandboxContainer{managed(f.orgID, f.sessionID)}, nil)

	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))
	require.Equal(t, []uuid.UUID{f.sessionID}, f.leaser.ensuredSorted(), "the live container's socket should be pinned")
	require.Empty(t, f.leaser.releasedSlice(), "nothing should be released while the container is alive")
}

func TestSandboxAuthSocketReconciler_IsIdempotentAcrossTicks(t *testing.T) {
	t.Parallel()
	f := newReconcilerFixture(t)
	f.lister.set([]ManagedSandboxContainer{managed(f.orgID, f.sessionID)}, nil)

	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))
	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))
	require.Equal(t, []uuid.UUID{f.sessionID}, f.leaser.ensuredSorted(), "an already-pinned container must not be re-pinned on the next tick")
}

func TestSandboxAuthSocketReconciler_ReleasesWhenContainerGone(t *testing.T) {
	t.Parallel()
	f := newReconcilerFixture(t)
	f.lister.set([]ManagedSandboxContainer{managed(f.orgID, f.sessionID)}, nil)
	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))

	// Container reaped: it disappears from the host enumeration.
	f.lister.set(nil, nil)
	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))
	require.Equal(t, []uuid.UUID{f.sessionID}, f.leaser.releasedSlice(), "a vanished container's pin should be released")

	// And it must not release twice on a subsequent tick.
	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))
	require.Len(t, f.leaser.releasedSlice(), 1, "a released pin must not be released again")
}

func TestSandboxAuthSocketReconciler_DoesNotReleaseOnListError(t *testing.T) {
	t.Parallel()
	f := newReconcilerFixture(t)
	f.lister.set([]ManagedSandboxContainer{managed(f.orgID, f.sessionID)}, nil)
	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))

	// A transient Docker error must NOT be read as "all containers vanished".
	f.lister.set(nil, errors.New("docker daemon unavailable"))
	err := f.reconciler.ReconcileOnce(context.Background())
	require.Error(t, err, "a list failure should surface as an error")
	require.Empty(t, f.leaser.releasedSlice(), "pins must be preserved across a transient enumeration failure")
}

func TestSandboxAuthSocketReconciler_SkipsUnmappableContainers(t *testing.T) {
	t.Parallel()
	f := newReconcilerFixture(t)
	f.lister.set([]ManagedSandboxContainer{
		{ID: "legacy-1", SessionID: "", OrgID: ""},            // unlabeled
		{ID: "bad-1", SessionID: "not-a-uuid", OrgID: "nope"}, // unparseable
		managed(f.orgID, f.sessionID),                         // valid
	}, nil)

	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))
	require.Equal(t, []uuid.UUID{f.sessionID}, f.leaser.ensuredSorted(), "only the mappable container should be pinned")
}

func TestSandboxAuthSocketReconciler_RetriesAfterPinFailure(t *testing.T) {
	t.Parallel()
	f := newReconcilerFixture(t)
	// First pass: the lease pin fails (e.g. transient repo lookup blip).
	f.leaser.ensureErr[f.sessionID] = errors.New("ensure lease failed")
	f.lister.set([]ManagedSandboxContainer{managed(f.orgID, f.sessionID)}, nil)
	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))
	require.Empty(t, f.leaser.ensuredSorted(), "a failed pin should not be recorded as pinned")

	// Next pass: the failure clears and the container is pinned (proving the
	// failed session was not marked pinned and is retried).
	f.leaser.mu.Lock()
	f.leaser.ensureErr = map[uuid.UUID]error{}
	f.leaser.mu.Unlock()
	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))
	require.Equal(t, []uuid.UUID{f.sessionID}, f.leaser.ensuredSorted(), "a previously-failed pin should be retried on the next tick")
}

func TestSandboxAuthSocketReconciler_SkipsSessionWithoutRepository(t *testing.T) {
	t.Parallel()
	f := newReconcilerFixture(t)
	// A session row with no repository can't resolve credentials; it must be
	// skipped rather than erroring the whole pass.
	noRepoSession := uuid.New()
	f.reconciler.sessions.(*fakeSessionLoader).sessions[noRepoSession] = models.Session{ID: noRepoSession, OrgID: f.orgID}
	f.lister.set([]ManagedSandboxContainer{
		managed(f.orgID, noRepoSession),
		managed(f.orgID, f.sessionID),
	}, nil)

	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))
	require.Equal(t, []uuid.UUID{f.sessionID}, f.leaser.ensuredSorted(), "only the session with a repository should be pinned")
}

func TestSandboxAuthSocketReconciler_DoesNotStealLiveForeignSocket(t *testing.T) {
	t.Parallel()
	f := newReconcilerFixture(t)
	f.lister.set([]ManagedSandboxContainer{managed(f.orgID, f.sessionID)}, nil)

	// A sibling worker generation (draining during a rolling deploy) already
	// serves this session's socket: we have no local entry, but the path is
	// live. The reconciler must NOT pin/steal it.
	f.setLive(socketPathFor(f.sessionID), true)
	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))
	require.Empty(t, f.leaser.ensuredSorted(), "a socket served by a sibling generation must not be stolen")

	// Once the sibling's listener is gone, the next tick takes over and binds.
	f.setLive(socketPathFor(f.sessionID), false)
	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))
	require.Equal(t, []uuid.UUID{f.sessionID}, f.leaser.ensuredSorted(), "the reconciler should take over once the foreign listener is gone")
}

func TestSandboxAuthSocketReconciler_PinsOwnSocketEvenWhenLive(t *testing.T) {
	t.Parallel()
	f := newReconcilerFixture(t)
	f.lister.set([]ManagedSandboxContainer{managed(f.orgID, f.sessionID)}, nil)

	// Simulate a turn that already acquired a local listener (e.g. the executor
	// acquired a per-turn lease): we own a local entry AND the socket is live.
	// The reconciler must still pin it (so it survives the turn boundary) rather
	// than treating it as a foreign socket to skip.
	f.leaser.mu.Lock()
	f.leaser.entries[f.sessionID] = true
	f.leaser.mu.Unlock()
	f.setLive(socketPathFor(f.sessionID), true)

	require.NoError(t, f.reconciler.ReconcileOnce(context.Background()))
	require.Equal(t, []uuid.UUID{f.sessionID}, f.leaser.ensuredSorted(), "a session we already own a listener for must be pinned even while live")
}
