package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
	"github.com/assembledhq/143/internal/services/github/ratelimit"
	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
)

type reconciliationTokenSource struct{}

func (reconciliationTokenSource) GetInstallationToken(_ context.Context, id int64) (string, error) {
	if id == 7 {
		return "", &GitHubAPIError{StatusCode: http.StatusNotFound}
	}
	return fmt.Sprintf("installation-%d", id), nil
}

type reconciliationIntegrationSource struct{}

func (reconciliationIntegrationSource) GetByID(context.Context, uuid.UUID) (models.Integration, error) {
	return models.Integration{Config: []byte(`{"installation_id":10}`)}, nil
}

func expectReconciliationRepository(mock pgxmock.PgxPoolIface, orgID, repositoryID uuid.UUID, name string, installationID int64, byID bool) {
	query := "SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'"
	args := pgx.NamedArgs{"org_id": orgID, "full_name": name}
	if byID {
		query = "SELECT .+ FROM repositories.+WHERE id = @id AND org_id = @org_id"
		args = pgx.NamedArgs{"org_id": orgID, "id": repositoryID}
	}
	now := time.Now().UTC()
	mock.ExpectQuery(query).WithArgs(args).WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
		repositoryID, orgID, uuid.New(), int64(1), name, "main", false, nil, nil,
		"https://github.com/"+name+".git", installationID, "active", nil, nil, []byte(`{}`), now, now,
	))
}

func TestReconciliationScopeUsesAuthenticatedInstallation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		resolution    *identity.Resolution
		throttle      error
		wantBlockedID int64
	}{
		{name: "resolved fallback installation", resolution: &identity.Resolution{Source: identity.SourceApp, InstallationID: 99}, throttle: &GitHubAPIError{StatusCode: 429}, wantBlockedID: 99},
		{name: "typed deferral overrides recorded identity", resolution: &identity.Resolution{Source: identity.SourceApp, InstallationID: 42}, throttle: &ratelimit.Deferral{InstallationID: 99, Kind: ratelimit.KindSecondary}, wantBlockedID: 99},
		{name: "unknown identity remains ungrouped", resolution: &identity.Resolution{Source: identity.SourceApp}, throttle: &GitHubAPIError{StatusCode: 429}},
		{name: "user identity is not an installation", resolution: &identity.Resolution{Source: identity.SourceUser, InstallationID: 99}, throttle: &GitHubAPIError{StatusCode: 429}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scope := reconciliationScopeFromContext(withReconciliationScope(context.Background()))
			scope.observe(tt.resolution, tt.throttle)
			require.Equal(t, tt.wantBlockedID > 0, scope.hasBlockedInstallation(), "only resolved installation credentials should create a sweep suppression bucket")
			require.NoError(t, scope.deferral(&identity.Resolution{Source: identity.SourceApp, InstallationID: 42}), "a stale repository installation must not be suppressed for another credential's throttle")
			require.NoError(t, scope.deferral(&identity.Resolution{Source: identity.SourceApp}), "unknown identities must not share an installation-zero bucket")
			if tt.wantBlockedID > 0 {
				require.ErrorIs(t, scope.deferral(&identity.Resolution{Source: identity.SourceApp, InstallationID: tt.wantBlockedID}), tt.throttle, "the affected installation should retain its original retry error")
			}
		})
	}
}

func TestReconcilePullRequestStateContinuesHealthyInstallations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		firstInstallation int64
	}{
		{name: "direct installation", firstInstallation: 10},
		{name: "stale repository uses integration fallback", firstInstallation: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "database mock should initialize")
			t.Cleanup(mock.Close)
			orgID := uuid.New()
			now := time.Now().UTC()
			names := []string{"example/throttled", "example/same-installation", "example/healthy"}
			ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
			rows := make([][]any, 3)
			staleRows := pgxmock.NewRows(prTestPullRequestColumns)
			for i := range ids {
				rows[i] = newPRTestRow(ids[i], nil, orgID, names[i], now, nil)
				staleRows.AddRow(rows[i]...)
			}
			mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").WithArgs(pgx.NamedArgs{"org_id": orgID, "before": pgxmock.AnyArg(), "limit": 10}).WillReturnRows(staleRows)
			for i := range ids {
				mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(rows[i]...))
				installationID := int64(10)
				if i == 0 {
					installationID = tt.firstInstallation
				}
				if i == 2 {
					installationID = 20
				}
				expectReconciliationRepository(mock, orgID, uuid.New(), names[i], installationID, false)
				if i != 1 {
					expectReserveCheckStateVersion(mock, orgID, ids[i], 0)
				}
			}
			mock.ExpectQuery("SELECT s.id[\\s\\S]*FROM session_publish_state").WithArgs(pgx.NamedArgs{"org_id": orgID, "stuck_before": pgxmock.AnyArg(), "limit": 10}).WillReturnRows(pgxmock.NewRows([]string{"id"}))
			mergeRows := pgxmock.NewRows(prTestPullRequestColumns)
			for _, i := range []int{1, 2} {
				row := append([]any(nil), rows[i]...)
				row[21] = models.PullRequestMergeWhenReadyStateQueued
				mergeRows.AddRow(row...)
			}
			mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*merge_when_ready_state = 'queued'[\\s\\S]*merge_when_ready_state = 'merging'").WithArgs(pgx.NamedArgs{"org_id": orgID, "stale_before": pgxmock.AnyArg(), "limit": 10}).WillReturnRows(mergeRows)
			expectReconciliationRepository(mock, orgID, uuid.New(), names[1], 10, false)
			expectReconciliationRepository(mock, orgID, uuid.New(), names[2], 20, false)
			mock.ExpectQuery("INSERT INTO jobs").WithArgs(pgx.NamedArgs{"org_id": orgID, "queue": "default", "job_type": "merge_pull_request_when_ready", "payload": pgxmock.AnyArg(), "priority": 7, "dedupe_key": pgxmock.AnyArg()}).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

			resolver := identity.NewResolver(reconciliationTokenSource{}, zerolog.Nop())
			resolver.SetIntegrations(reconciliationIntegrationSource{})
			var mu sync.Mutex
			var paths []string
			service := &PRService{cachedResolver: resolver, pullRequests: db.NewPullRequestStore(mock), repos: db.NewRepositoryStore(mock), sessions: db.NewSessionStore(mock), jobs: db.NewJobStore(mock), logger: zerolog.Nop(), baseURL: "https://api.github.com", httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				code := http.StatusServiceUnavailable
				if strings.Contains(r.URL.Path, "/throttled/") {
					code = http.StatusTooManyRequests
				}
				return &http.Response{StatusCode: code, Header: http.Header{"Retry-After": []string{"60"}}, Body: io.NopCloser(strings.NewReader(`{"message":"upstream unavailable"}`))}, nil
			})}}
			err = service.ReconcilePullRequestState(context.Background(), orgID, 10)
			require.Error(t, err, "throttle and transient failure should remain available for durable retry")
			require.True(t, ClassifyRetry(err, now).RateLimited, "joined result should retain the installation throttle")
			mu.Lock()
			actual := append([]string(nil), paths...)
			mu.Unlock()
			require.Equal(t, []string{"/repos/example/throttled/pulls/42", "/repos/example/healthy/pulls/42"}, actual, "sweep should suppress the same installation and still attempt a healthy installation")
			require.NoError(t, mock.ExpectationsWereMet(), "bounded sweep should run local recovery and enqueue only the healthy installation's merge")
		})
	}
}

func TestReconcileSessionPublicationsContinuesHealthyInstallations(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "database mock should initialize")
	t.Cleanup(mock.Close)
	orgID := uuid.New()
	now := time.Now().UTC()
	candidates := make([]models.SessionPublication, 3)
	rows := pgxmock.NewRows(publicationHealthTestColumns)
	for i := range candidates {
		candidates[i] = models.SessionPublication{ID: uuid.New(), OrgID: orgID, SessionID: uuid.New(), ChangesetID: uuid.New(), RepositoryID: uuid.New(), State: models.SessionPublicationStateBranchPublished, Source: models.SessionPublicationSourceAutomation, ReviewGateState: models.SessionPublicationReviewGatePassed, GitHubPRNumber: ptrInt(42), BaseBranch: "main", HeadBranch: "143/session", RequestedAt: now, CreatedAt: now, UpdatedAt: now}
		rows.AddRow(publicationHealthTestRow(candidates[i])...)
	}
	mock.ExpectQuery(`SELECT[\s\S]+FROM session_publications[\s\S]+ORDER BY updated_at`).WithArgs(pgx.NamedArgs{"org_id": orgID, "updated_before": pgxmock.AnyArg(), "limit": 10}).WillReturnRows(rows)
	names := []string{"example/throttled", "example/same-installation", "example/healthy"}
	for i, pub := range candidates {
		mock.ExpectQuery(`SELECT[\s\S]+FROM pull_requests[\s\S]+changeset_id = @changeset_id`).WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": pub.SessionID, "changeset_id": pub.ChangesetID}).WillReturnError(pgx.ErrNoRows)
		installationID := int64(10)
		if i == 2 {
			installationID = 20
		}
		expectReconciliationRepository(mock, orgID, pub.RepositoryID, names[i], installationID, true)
		if i == 2 {
			mock.ExpectExec("UPDATE session_publications").WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": pub.SessionID, "changeset_id": pub.ChangesetID, "state": models.SessionPublicationStateRetryableFailed, "error_code": pgxmock.AnyArg(), "error_message": pgxmock.AnyArg(), "completed": false}).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		}
	}
	var paths []string
	service := &PRService{cachedResolver: identity.NewResolver(reconciliationTokenSource{}, zerolog.Nop()), pullRequests: db.NewPullRequestStore(mock), publications: db.NewSessionPublicationStore(mock), repos: db.NewRepositoryStore(mock), changesets: db.NewSessionChangesetStore(mock), sessions: db.NewSessionStore(mock), logger: zerolog.Nop(), baseURL: "https://api.github.com", httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		code := http.StatusServiceUnavailable
		if strings.Contains(r.URL.Path, "/throttled/") {
			code = http.StatusTooManyRequests
		}
		return &http.Response{StatusCode: code, Header: http.Header{"Retry-After": []string{"60"}}, Body: io.NopCloser(strings.NewReader(`{"message":"upstream unavailable"}`))}, nil
	})}}
	err = service.reconcileSessionPublications(context.Background(), orgID, 10)
	require.Error(t, err, "publication sweep should retain errors for durable retry")
	require.True(t, ClassifyRetry(err, now).RateLimited, "publication sweep should retain the throttle")
	require.Equal(t, []string{"/repos/example/throttled/pulls/42", "/repos/example/healthy/pulls/42"}, paths, "publication throttle must not stop healthy installations or repeat same-installation GitHub calls")
	require.NoError(t, mock.ExpectationsWereMet(), "only ordinary failures should rotate publication state")
}

// Done is first consulted by SyncPullRequestState after registering its
// singleflight waiter, giving the test a deterministic join barrier.
type reconciliationWaiterContext struct {
	context.Context
	joined chan struct{}
	once   sync.Once
}

func (c *reconciliationWaiterContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.joined) })
	return c.Context.Done()
}

func TestReconciliationWaiterRetainsOutsideFlightInstallation(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "database mock should initialize")
	t.Cleanup(mock.Close)
	orgID := uuid.New()
	firstID, secondID := uuid.New(), uuid.New()
	now := time.Now().UTC()
	for i, id := range []uuid.UUID{firstID, secondID} {
		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(newPRTestRow(id, nil, orgID, "example/repository", now, nil)...))
		expectReconciliationRepository(mock, orgID, uuid.New(), "example/repository", 10, false)
		if i == 0 {
			expectReserveCheckStateVersion(mock, orgID, id, 0)
		}
	}
	controller, err := ratelimit.NewController(ratelimit.Config{Mode: ratelimit.ModeObserve, Environment: "test", AppID: 1, Logger: zerolog.Nop()})
	require.NoError(t, err, "observe-mode controller should initialize")
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var mu sync.Mutex
	calls := 0
	client := githubtelemetry.NewControlledHTTPClient(5*time.Second, zerolog.Nop(), controller, "pr_health", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			close(started)
			<-release
		}
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"60"}}, Body: io.NopCloser(strings.NewReader(`{"message":"API rate limit exceeded"}`))}, nil
	}))
	service := &PRService{cachedResolver: identity.NewResolver(reconciliationTokenSource{}, zerolog.Nop()), pullRequests: db.NewPullRequestStore(mock), repos: db.NewRepositoryStore(mock), logger: zerolog.Nop(), baseURL: "https://api.github.com", httpClient: client}
	leaderErr := make(chan error, 1)
	go func() { leaderErr <- service.SyncPullRequestState(context.Background(), orgID, firstID) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("outside flight should reach its GitHub request")
	}
	waiterCtx := &reconciliationWaiterContext{Context: context.Background(), joined: make(chan struct{})}
	sweepCtx := withReconciliationScope(waiterCtx)
	waiterErr := make(chan error, 1)
	go func() { waiterErr <- service.SyncPullRequestState(sweepCtx, orgID, firstID) }()
	select {
	case <-waiterCtx.joined:
	case <-time.After(5 * time.Second):
		t.Fatal("reconciliation waiter should join the existing flight")
	}
	unblock()
	var flightErr error
	select {
	case flightErr = <-waiterErr:
	case <-time.After(5 * time.Second):
		t.Fatal("shared throttle should return to the reconciliation waiter")
	}
	select {
	case err = <-leaderErr:
	case <-time.After(5 * time.Second):
		t.Fatal("shared throttle should return to the outside leader")
	}
	var apiErr *GitHubAPIError
	require.True(t, errors.As(flightErr, &apiErr), "observe-mode flight should preserve the original GitHub response error")
	_, controlled := ratelimit.AsDeferral(flightErr)
	require.False(t, controlled, "observe-mode regression should exercise a raw throttle without controller deferral identity")
	require.ErrorIs(t, reconciliationScopeFromContext(sweepCtx).deferral(&identity.Resolution{Source: identity.SourceApp, InstallationID: 10}), flightErr, "every waiter should receive the outside leader's authenticated installation")
	require.Error(t, err, "outside leader should retain its original throttle")
	err = service.SyncPullRequestState(sweepCtx, orgID, secondID)
	require.True(t, ClassifyRetry(err, now).RateLimited, "next PR in the sweep should defer for the same installation")
	mu.Lock()
	actualCalls := calls
	mu.Unlock()
	require.Equal(t, 1, actualCalls, "observe-mode shared-flight throttle must suppress the next same-installation request")
	require.NoError(t, mock.ExpectationsWereMet(), "second PR should stop before reserving or replacing authoritative health")
}
