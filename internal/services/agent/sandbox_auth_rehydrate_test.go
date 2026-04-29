package agent

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

// rehydrateLister is a programmable ContainerHoldingSessionLister. The pages
// field is a list-of-lists keyed by call order: pages[0] is the first batch,
// pages[1] is the second, etc. An empty inner slice signals end-of-stream so
// the caller breaks. Anything beyond the last page is also returned empty,
// which keeps degenerate batch-cap tests well-defined.
//
// errAtCall (1-indexed) lets a test simulate a list failure on a specific
// call number — useful for the partial-progress case where the first page
// returns sessions and the second page errors. errAtCall == 0 means "never
// error here"; the unconditional `err` field still applies.
type rehydrateLister struct {
	pages     [][]models.Session
	err       error
	errAtCall int
	calls     int
}

func (s *rehydrateLister) ListContainerHoldingSessions(_ context.Context, _ uuid.UUID) ([]models.Session, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if s.errAtCall > 0 && s.calls == s.errAtCall {
		return nil, errors.New("simulated mid-stream list failure")
	}
	if s.calls-1 >= len(s.pages) {
		return nil, nil
	}
	return s.pages[s.calls-1], nil
}

type rehydrateRepoStore struct {
	repos map[uuid.UUID]models.Repository
	err   error
}

func (s *rehydrateRepoStore) GetByID(_ context.Context, _, repoID uuid.UUID) (models.Repository, error) {
	if s.err != nil {
		return models.Repository{}, s.err
	}
	if r, ok := s.repos[repoID]; ok {
		return r, nil
	}
	return models.Repository{}, errors.New("repo not found")
}

// rehydrateSandboxAuth records every Listen call so tests can assert exactly
// which session IDs were rehydrated and in what order. Listens after listenErr
// is set return that error to exercise the "leave un-rehydrated, keep going"
// branch.
type rehydrateSandboxAuth struct {
	listened  []uuid.UUID
	listenErr error
}

func (a *rehydrateSandboxAuth) Listen(_ context.Context, sessionID uuid.UUID, _ *models.Session, _ *models.Repository, _ models.OrgSettings) (string, error) {
	if a.listenErr != nil {
		return "", a.listenErr
	}
	a.listened = append(a.listened, sessionID)
	return "/tmp/rehydrate.sock", nil
}

func (a *rehydrateSandboxAuth) Close(_ uuid.UUID) {}

// rehydrateProvider is a minimal SandboxProvider that only implements the
// surface area RehydrateSandboxAuthListeners actually exercises (IsAlive).
// Everything else returns sentinel errors so an accidental regression that
// touches Create/Exec/etc inside rehydrate fails fast with an obvious
// message.
type rehydrateProvider struct {
	alive map[string]bool
	err   error
}

func (p *rehydrateProvider) Name() string { return "rehydrate-test" }
func (p *rehydrateProvider) Create(context.Context, SandboxConfig) (*Sandbox, error) {
	return nil, errors.New("rehydrate test provider should not Create")
}
func (p *rehydrateProvider) CloneRepo(context.Context, *Sandbox, string, string, string) error {
	return errors.New("rehydrate test provider should not CloneRepo")
}
func (p *rehydrateProvider) Exec(context.Context, *Sandbox, string, io.Writer, io.Writer) (int, error) {
	return 0, errors.New("rehydrate test provider should not Exec")
}
func (p *rehydrateProvider) ExecStream(context.Context, *Sandbox, string, func([]byte), io.Writer) (int, error) {
	return 0, errors.New("rehydrate test provider should not ExecStream")
}
func (p *rehydrateProvider) ReadFile(context.Context, *Sandbox, string) ([]byte, error) {
	return nil, errors.New("rehydrate test provider should not ReadFile")
}
func (p *rehydrateProvider) WriteFile(context.Context, *Sandbox, string, []byte) error {
	return errors.New("rehydrate test provider should not WriteFile")
}
func (p *rehydrateProvider) Destroy(context.Context, *Sandbox) error {
	return errors.New("rehydrate test provider should not Destroy")
}
func (p *rehydrateProvider) ConnectionInfo(context.Context, *Sandbox) (*SandboxConnectionInfo, error) {
	return nil, errors.New("rehydrate test provider should not ConnectionInfo")
}
func (p *rehydrateProvider) Snapshot(context.Context, *Sandbox) (io.ReadCloser, error) {
	return nil, errors.New("rehydrate test provider should not Snapshot")
}
func (p *rehydrateProvider) Restore(context.Context, *Sandbox, io.Reader) error {
	return errors.New("rehydrate test provider should not Restore")
}
func (p *rehydrateProvider) IsAlive(_ context.Context, sb *Sandbox) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	return p.alive[sb.ID], nil
}

func newSession(orgID, repoID uuid.UUID, containerID string) models.Session {
	cid := containerID
	return models.Session{
		ID:           uuid.New(),
		OrgID:        orgID,
		ContainerID:  &cid,
		RepositoryID: &repoID,
	}
}

func TestRehydrate_SkipsWhenSandboxAuthNil(t *testing.T) {
	t.Parallel()
	keep, err := RehydrateSandboxAuthListeners(
		context.Background(),
		&rehydrateLister{},
		&rehydrateRepoStore{},
		nil,
		&rehydrateProvider{},
		nil, // sandboxAuth is the nil-check we're testing
		zerolog.Nop(),
	)
	require.NoError(t, err)
	require.Nil(t, keep, "bail-out paths must return a nil keep so callers can distinguish 'didn't run' from 'ran with no sessions' and skip the sweep")
}

func TestRehydrate_SkipsWhenProviderNil(t *testing.T) {
	t.Parallel()
	keep, err := RehydrateSandboxAuthListeners(
		context.Background(),
		&rehydrateLister{},
		&rehydrateRepoStore{},
		nil,
		nil,
		&rehydrateSandboxAuth{},
		zerolog.Nop(),
	)
	require.NoError(t, err)
	require.Nil(t, keep, "bail-out paths must return a nil keep so callers skip the sweep")
}

// TestRehydrate_NoSessionsReturnsNonNilEmptyMap is the negative-space partner
// to the bail-out tests: when rehydrate actually ran (deps non-nil) but the
// list returned no rows, the keep map must be non-nil so the caller knows it
// can safely sweep — every UUID-named subdir on disk really is stale.
func TestRehydrate_NoSessionsReturnsNonNilEmptyMap(t *testing.T) {
	t.Parallel()
	keep, err := RehydrateSandboxAuthListeners(
		context.Background(),
		&rehydrateLister{},
		&rehydrateRepoStore{},
		nil,
		&rehydrateProvider{},
		&rehydrateSandboxAuth{},
		zerolog.Nop(),
	)
	require.NoError(t, err)
	require.NotNil(t, keep, "successful run with no sessions must return a non-nil empty map so the caller knows sweep is safe")
	require.Empty(t, keep)
}

func TestRehydrate_RebindsLiveContainerOnly(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	repoID := uuid.New()
	live := newSession(orgID, repoID, "container-live")
	dead := newSession(orgID, repoID, "container-dead")

	prov := &rehydrateProvider{
		alive: map[string]bool{
			"container-live": true,
			"container-dead": false,
		},
	}
	auth := &rehydrateSandboxAuth{}

	// Drop reconciler retry backoff to zero so this test doesn't pay 500ms
	// per IsAlive miss if the provider ever returns a transient error.
	SetIsAliveBackoffForTesting(0)
	t.Cleanup(func() { SetIsAliveBackoffForTesting(500 * time.Millisecond) })

	keep, err := RehydrateSandboxAuthListeners(
		context.Background(),
		&rehydrateLister{pages: [][]models.Session{{live, dead}}},
		&rehydrateRepoStore{repos: map[uuid.UUID]models.Repository{repoID: {InstallationID: 1, FullName: "owner/repo"}}},
		nil,
		prov,
		auth,
		zerolog.Nop(),
	)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{live.ID}, auth.listened, "only the alive container's session should be Listen'd")
	require.Contains(t, keep, live.ID)
	require.NotContains(t, keep, dead.ID)
}

func TestRehydrate_PerSessionFailureDoesNotStopLoop(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	goodRepo := uuid.New()
	missingRepo := uuid.New()
	good := newSession(orgID, goodRepo, "container-good")
	missing := newSession(orgID, missingRepo, "container-missing-repo")

	prov := &rehydrateProvider{
		alive: map[string]bool{
			"container-good":         true,
			"container-missing-repo": true,
		},
	}
	auth := &rehydrateSandboxAuth{}

	// Repo store knows about goodRepo but not missingRepo; rehydrate must
	// still process `good` even though `missing`'s repo lookup fails.
	repos := &rehydrateRepoStore{repos: map[uuid.UUID]models.Repository{
		goodRepo: {InstallationID: 1, FullName: "owner/good"},
	}}

	SetIsAliveBackoffForTesting(0)
	t.Cleanup(func() { SetIsAliveBackoffForTesting(500 * time.Millisecond) })

	keep, err := RehydrateSandboxAuthListeners(
		context.Background(),
		&rehydrateLister{pages: [][]models.Session{{missing, good}}},
		repos,
		nil,
		prov,
		auth,
		zerolog.Nop(),
	)
	require.NoError(t, err, "list-page failures aside, per-row errors must not bubble up")
	require.Equal(t, []uuid.UUID{good.ID}, auth.listened, "good session should still be rehydrated despite the prior repo lookup failure")
	require.Equal(t, map[uuid.UUID]struct{}{good.ID: {}}, keep)
}

func TestRehydrate_ListenFailureCountsAsErrorButContinues(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	repoID := uuid.New()
	first := newSession(orgID, repoID, "container-first")

	prov := &rehydrateProvider{alive: map[string]bool{"container-first": true}}
	auth := &rehydrateSandboxAuth{listenErr: errors.New("address in use")}

	SetIsAliveBackoffForTesting(0)
	t.Cleanup(func() { SetIsAliveBackoffForTesting(500 * time.Millisecond) })

	keep, err := RehydrateSandboxAuthListeners(
		context.Background(),
		&rehydrateLister{pages: [][]models.Session{{first}}},
		&rehydrateRepoStore{repos: map[uuid.UUID]models.Repository{repoID: {InstallationID: 1, FullName: "owner/repo"}}},
		nil,
		prov,
		auth,
		zerolog.Nop(),
	)
	require.NoError(t, err, "Listen errors are per-row; the loop must keep going")
	require.Empty(t, keep, "a session whose Listen failed must not be reported as rehydrated (otherwise the sweep would think it's live)")
}

func TestRehydrate_ListErrorBubbles(t *testing.T) {
	t.Parallel()
	listErr := errors.New("db down")
	keep, err := RehydrateSandboxAuthListeners(
		context.Background(),
		&rehydrateLister{err: listErr},
		&rehydrateRepoStore{},
		nil,
		&rehydrateProvider{},
		&rehydrateSandboxAuth{},
		zerolog.Nop(),
	)
	require.Error(t, err)
	require.ErrorIs(t, err, listErr, "a list-page failure must surface so ops can investigate; per-row failures are swallowed but page failures aren't")
	require.Nil(t, keep, "list-page errors must return nil keep — a partial map would let the caller sweep based on incomplete coverage and clobber unvisited live sockets")
}

// TestRehydrate_PartialListErrorReturnsNilKeep verifies the contract that
// a list-page failure mid-stream still yields a nil keep, not the partial
// map of sessions we'd already processed. A partial keep would let the
// caller sweep based on incomplete coverage and clobber sockets for the
// (still-live) sessions we never visited.
func TestRehydrate_PartialListErrorReturnsNilKeep(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	repoID := uuid.New()
	first := newSession(orgID, repoID, "container-first")

	prov := &rehydrateProvider{alive: map[string]bool{"container-first": true}}
	auth := &rehydrateSandboxAuth{}

	SetIsAliveBackoffForTesting(0)
	t.Cleanup(func() { SetIsAliveBackoffForTesting(500 * time.Millisecond) })

	// First call returns one session (which gets Listen'd), second call
	// errors. The function must return nil keep despite the partial
	// progress.
	keep, err := RehydrateSandboxAuthListeners(
		context.Background(),
		&rehydrateLister{
			pages:     [][]models.Session{{first}},
			errAtCall: 2,
		},
		&rehydrateRepoStore{repos: map[uuid.UUID]models.Repository{repoID: {InstallationID: 1, FullName: "owner/repo"}}},
		nil,
		prov,
		auth,
		zerolog.Nop(),
	)
	require.Error(t, err)
	require.Nil(t, keep, "partial-progress + list error must return nil keep so the caller skips the sweep entirely")
	require.Equal(t, []uuid.UUID{first.ID}, auth.listened, "the first-page Listen must have happened (proving 'partial progress' is real); we just don't trust the partial keep for sweep purposes")
}

func TestRehydrate_OrgSettingsLoaderCalledPerSession(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	repoID := uuid.New()
	run := newSession(orgID, repoID, "container-x")

	prov := &rehydrateProvider{alive: map[string]bool{"container-x": true}}
	auth := &rehydrateSandboxAuth{}

	SetIsAliveBackoffForTesting(0)
	t.Cleanup(func() { SetIsAliveBackoffForTesting(500 * time.Millisecond) })

	var loaderCalls int
	loader := func(_ context.Context, gotOrgID uuid.UUID) (models.OrgSettings, error) {
		loaderCalls++
		require.Equal(t, orgID, gotOrgID, "loader must receive the session's org_id")
		return models.OrgSettings{PRAuthorship: models.PRAuthorshipAppOnly}, nil
	}

	_, err := RehydrateSandboxAuthListeners(
		context.Background(),
		&rehydrateLister{pages: [][]models.Session{{run}}},
		&rehydrateRepoStore{repos: map[uuid.UUID]models.Repository{repoID: {InstallationID: 1, FullName: "owner/repo"}}},
		loader,
		prov,
		auth,
		zerolog.Nop(),
	)
	require.NoError(t, err)
	require.Equal(t, 1, loaderCalls, "loader must be consulted exactly once per rehydrated session so the captured policy reflects org config at restart time")
}
