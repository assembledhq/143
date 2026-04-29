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
	require.Equal(t, map[uuid.UUID]struct{}{missing.ID: {}, good.ID: {}}, keep, "live sessions must be preserved from sweep even when their listener rehydrate fails")
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
	require.Equal(t, map[uuid.UUID]struct{}{first.ID: {}}, keep, "a live session whose Listen failed must still be preserved from sweep")
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

// TestRehydrate_SkipsRowsWithEmptyContainerID covers the defensive guard
// against rows where container_id is unset or empty. The query already
// filters those out (WHERE container_id IS NOT NULL), but a future schema
// change or a race that nulls the column mid-page must not deref a nil
// pointer or call IsAlive with an empty ID.
func TestRehydrate_SkipsRowsWithEmptyContainerID(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	repoID := uuid.New()

	withNilID := models.Session{ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID}
	emptyStr := ""
	withEmptyID := models.Session{ID: uuid.New(), OrgID: orgID, ContainerID: &emptyStr, RepositoryID: &repoID}

	auth := &rehydrateSandboxAuth{}
	prov := &rehydrateProvider{} // no entries — IsAlive should never be called

	keep, err := RehydrateSandboxAuthListeners(
		context.Background(),
		&rehydrateLister{pages: [][]models.Session{{withNilID, withEmptyID}}},
		&rehydrateRepoStore{},
		nil,
		prov,
		auth,
		zerolog.Nop(),
	)
	require.NoError(t, err)
	require.NotNil(t, keep)
	require.Empty(t, keep, "rows with no container_id must be skipped silently — no IsAlive probe, no Listen, no errored-counter bump")
	require.Empty(t, auth.listened, "Listen must not be called for rows with missing container_id")
}

// TestRehydrate_IsAliveErrorIsCounted covers the per-row branch where
// IsAlive's retries all fail. The row is skipped (no Listen) and counted as
// errored, but it stays in the keep set because an unknown-liveness preview
// hold is not safe to sweep.
func TestRehydrate_IsAliveErrorIsCounted(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	repoID := uuid.New()
	flaky := newSession(orgID, repoID, "container-flaky")

	prov := &rehydrateProvider{err: errors.New("docker daemon unavailable")}
	auth := &rehydrateSandboxAuth{}

	SetIsAliveBackoffForTesting(0)
	t.Cleanup(func() { SetIsAliveBackoffForTesting(500 * time.Millisecond) })

	keep, err := RehydrateSandboxAuthListeners(
		context.Background(),
		&rehydrateLister{pages: [][]models.Session{{flaky}}},
		&rehydrateRepoStore{repos: map[uuid.UUID]models.Repository{repoID: {InstallationID: 1, FullName: "owner/repo"}}},
		nil,
		prov,
		auth,
		zerolog.Nop(),
	)
	require.NoError(t, err, "transient IsAlive failures are per-row; the loop must keep going")
	require.NotNil(t, keep)
	require.Equal(t, map[uuid.UUID]struct{}{flaky.ID: {}}, keep, "a row whose IsAlive probe failed must be preserved because it may still be live")
	require.Empty(t, auth.listened, "Listen must not be called when IsAlive itself failed — the next turn boundary will retry from scratch")
}

// TestRehydrate_SkipsRowsWithNilRepositoryID covers the defensive branch
// for sessions whose repository_id is unset. The Listen call needs a repo
// to capture in the resolver closure; without one, we skip the row rather
// than fabricate a default that would later resolve to the wrong identity.
func TestRehydrate_SkipsRowsWithNilRepositoryID(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	cid := "container-no-repo"
	noRepo := models.Session{ID: uuid.New(), OrgID: orgID, ContainerID: &cid}

	prov := &rehydrateProvider{alive: map[string]bool{"container-no-repo": true}}
	auth := &rehydrateSandboxAuth{}

	SetIsAliveBackoffForTesting(0)
	t.Cleanup(func() { SetIsAliveBackoffForTesting(500 * time.Millisecond) })

	keep, err := RehydrateSandboxAuthListeners(
		context.Background(),
		&rehydrateLister{pages: [][]models.Session{{noRepo}}},
		&rehydrateRepoStore{},
		nil,
		prov,
		auth,
		zerolog.Nop(),
	)
	require.NoError(t, err)
	require.NotNil(t, keep)
	require.Equal(t, map[uuid.UUID]struct{}{noRepo.ID: {}}, keep, "live rows without a repository_id must be skipped for Listen but preserved from sweep")
	require.Empty(t, auth.listened, "Listen must not fire when the repo lookup is impossible (nil repository_id)")
}

// TestRehydrate_OrgSettingsLoaderErrorIsPerRow covers the branch where the
// loader fails for one session: that row is skipped and counted as
// errored, but other sessions in the same page must still be processed.
func TestRehydrate_OrgSettingsLoaderErrorIsPerRow(t *testing.T) {
	t.Parallel()
	failingOrg := uuid.New()
	healthyOrg := uuid.New()
	repoID := uuid.New()
	failing := newSession(failingOrg, repoID, "container-failing-org")
	healthy := newSession(healthyOrg, repoID, "container-healthy-org")

	prov := &rehydrateProvider{alive: map[string]bool{
		"container-failing-org": true,
		"container-healthy-org": true,
	}}
	auth := &rehydrateSandboxAuth{}

	SetIsAliveBackoffForTesting(0)
	t.Cleanup(func() { SetIsAliveBackoffForTesting(500 * time.Millisecond) })

	loader := func(_ context.Context, gotOrgID uuid.UUID) (models.OrgSettings, error) {
		if gotOrgID == failingOrg {
			return models.OrgSettings{}, errors.New("settings parse error")
		}
		return models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}, nil
	}

	keep, err := RehydrateSandboxAuthListeners(
		context.Background(),
		&rehydrateLister{pages: [][]models.Session{{failing, healthy}}},
		&rehydrateRepoStore{repos: map[uuid.UUID]models.Repository{repoID: {InstallationID: 1, FullName: "owner/repo"}}},
		loader,
		prov,
		auth,
		zerolog.Nop(),
	)
	require.NoError(t, err, "per-row loader failures must not bubble up — the loop must continue")
	require.NotNil(t, keep)
	require.Contains(t, keep, failing.ID, "the failing-org row must be preserved from sweep even though listener rehydrate failed")
	require.Contains(t, keep, healthy.ID, "the healthy row must still be rehydrated despite the prior failure")
}

// TestRehydrate_HitsBatchCap covers the safety-valve branch where the
// pagination cursor never empties. A pathological query (or a runaway
// preview accumulation) would otherwise spin forever; rehydrateMaxBatches
// caps the work and emits a warn log so ops can spot it.
func TestRehydrate_HitsBatchCap(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()

	// Build rehydrateMaxBatches+1 non-empty pages, each with one session
	// whose container reads as dead so per-row work stays cheap. The +1
	// ensures the loop exits via the cap, not via empty-page break — which
	// is the branch we're testing.
	const overCap = rehydrateMaxBatches + 1
	pages := make([][]models.Session, overCap)
	for i := range pages {
		pages[i] = []models.Session{
			newSession(orgID, uuid.New(), "container-dead"),
		}
	}

	prov := &rehydrateProvider{} // alive map empty → all containers dead → fast skip
	auth := &rehydrateSandboxAuth{}

	SetIsAliveBackoffForTesting(0)
	t.Cleanup(func() { SetIsAliveBackoffForTesting(500 * time.Millisecond) })

	lister := &rehydrateLister{pages: pages}
	keep, err := RehydrateSandboxAuthListeners(
		context.Background(),
		lister,
		&rehydrateRepoStore{},
		nil,
		prov,
		auth,
		zerolog.Nop(),
	)
	require.NoError(t, err, "hitting the batch cap is non-fatal — the rest of the rows are deferred to the next turn boundary")
	require.Nil(t, keep, "hitting the batch cap means pagination coverage is incomplete, so callers must skip sweep")
	require.Equal(t, rehydrateMaxBatches, lister.calls, "the cap must stop pagination at exactly rehydrateMaxBatches calls")
}
