package github

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
	"github.com/assembledhq/143/internal/services/github/ratelimit"
)

func newPRHealthQuotaController(t *testing.T) *ratelimit.Controller {
	t.Helper()
	server := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + server.Addr()}, zerolog.Nop(), nil)
	t.Cleanup(func() { require.NoError(t, client.Close(), "isolated quota cache should close") })
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	controller, err := ratelimit.NewController(ratelimit.Config{
		Mode: ratelimit.ModeEnforce, AppID: 1, InstallationAllowlist: []int64{42}, Cache: client,
		Now: func() time.Time { return now }, BackgroundQuotaReserve: 20, BackgroundQuotaInterval: time.Minute, Logger: zerolog.Nop(),
	})
	require.NoError(t, err, "enabled isolated quota controller should initialize")
	controller.Observe(context.Background(), ratelimit.Permit{Scope: ratelimit.Scope{InstallationID: 42}}, ratelimit.Observation{StatusCode: http.StatusOK, Header: http.Header{
		"X-Ratelimit-Limit": {"100"}, "X-Ratelimit-Remaining": {"10"}, "X-Ratelimit-Resource": {"core"},
		"X-Ratelimit-Reset": {strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
	}})
	require.True(t, controller.AdmitBackground(context.Background(), 42, "another-tenant:another-pr"), "another background candidate should occupy the current interval")
	return controller
}

func TestPRHealthBackgroundBudgetOnlyScopesSweeps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		reason       PullRequestSyncReason
		sweep        bool
		source       identity.Source
		installation int64
		admitted     bool
	}{
		{name: "scoped stale reconciliation", reason: PullRequestSyncReasonStaleReconcile, sweep: true, source: identity.SourceApp, installation: 42},
		{name: "ordinary async stale job", reason: PullRequestSyncReasonStaleReconcile, source: identity.SourceApp, installation: 42, admitted: true},
		{name: "active code review", reason: PullRequestSyncReasonCodeReview, sweep: true, source: identity.SourceApp, installation: 42, admitted: true},
		{name: "manual refresh", reason: PullRequestSyncReasonManual, sweep: true, source: identity.SourceApp, installation: 42, admitted: true},
		{name: "final merge check", reason: PullRequestSyncReasonMergeSafety, sweep: true, source: identity.SourceApp, installation: 42, admitted: true},
		{name: "repair refresh", reason: PullRequestSyncReasonRepair, sweep: true, source: identity.SourceApp, installation: 42, admitted: true},
		{name: "user principal", reason: PullRequestSyncReasonStaleReconcile, sweep: true, source: identity.SourceUser, installation: 42, admitted: true},
		{name: "unresolved principal", reason: PullRequestSyncReasonStaleReconcile, sweep: true, source: identity.SourceApp, admitted: true},
		{name: "healthy installation", reason: PullRequestSyncReasonStaleReconcile, sweep: true, source: identity.SourceApp, installation: 99, admitted: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service := &PRService{backgroundQuotaController: newPRHealthQuotaController(t)}
			ctx := context.Background()
			if tt.sweep {
				ctx = withReconciliationScope(ctx)
			}
			ctx = WithPullRequestSyncReason(ctx, tt.reason)
			admitted := service.admitBackgroundPullRequestSync(ctx, uuid.New(), uuid.New(), &identity.Resolution{Source: tt.source, InstallationID: tt.installation})
			require.Equal(t, tt.admitted, admitted, "only explicitly scoped stale sweep work may consume the optional budget")
		})
	}
}

func TestPRHealthBackgroundBudgetSkipsBeforeFreshnessMutation(t *testing.T) {
	t.Parallel()
	controller := newPRHealthQuotaController(t)
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "isolated sync database should initialize")
	t.Cleanup(mock.Close)
	orgID, prID, repoID := uuid.New(), uuid.New(), uuid.New()
	row := newPRTestRow(prID, nil, orgID, "example/budgeted", time.Now().UTC(), nil)
	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(row...))
	expectReconciliationRepository(mock, orgID, repoID, "example/budgeted", 42, false)
	requests := 0
	service := &PRService{
		backgroundQuotaController: controller, cachedResolver: identity.NewResolver(reconciliationTokenSource{}, zerolog.Nop()),
		pullRequests: db.NewPullRequestStore(mock), repos: db.NewRepositoryStore(mock), logger: zerolog.Nop(),
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { requests++; return nil, nil })},
	}
	ctx := WithPullRequestSyncReason(withReconciliationScope(context.Background()), PullRequestSyncReasonStaleReconcile)
	err = service.SyncPullRequestState(ctx, orgID, prID)
	require.ErrorIs(t, err, errPullRequestBackgroundQuotaDeferred, "budget skip should reach only the scoped sweep's private sentinel")
	require.Equal(t, 0, requests, "budget skip should perform no GitHub requests")
	require.Equal(t, RetryClassification{}, ClassifyRetry(err, time.Now()), "optimization skips must not become provider cooldown or retry state")
	require.False(t, reconciliationScopeFromContext(ctx).hasBlockedInstallation(), "budget skips must not block independent publication work")
	require.NoError(t, mock.ExpectationsWereMet(), "budget skip must not reserve or mark the PR snapshot freshly synchronized")
}

func TestPRHealthBackgroundBudgetPreservesIndependentRecovery(t *testing.T) {
	t.Parallel()
	controller := newPRHealthQuotaController(t)
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "isolated sweep database should initialize")
	t.Cleanup(mock.Close)
	orgID, prID := uuid.New(), uuid.New()
	row := newPRTestRow(prID, nil, orgID, "example/budgeted", time.Now().UTC(), nil)
	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").WithArgs(pgx.NamedArgs{"org_id": orgID, "before": pgxmock.AnyArg(), "limit": 10}).WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(row...))
	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(row...))
	expectReconciliationRepository(mock, orgID, uuid.New(), "example/budgeted", 42, false)
	mock.ExpectQuery("SELECT s.id[\\s\\S]*FROM session_publish_state").WithArgs(pgx.NamedArgs{"org_id": orgID, "stuck_before": pgxmock.AnyArg(), "limit": 10}).WillReturnRows(pgxmock.NewRows([]string{"id"}))
	queued := append([]any(nil), row...)
	queued[21] = models.PullRequestMergeWhenReadyStateQueued
	mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*merge_when_ready_state = 'queued'[\\s\\S]*merge_when_ready_state = 'merging'").WithArgs(pgx.NamedArgs{"org_id": orgID, "stale_before": pgxmock.AnyArg(), "limit": 10}).WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(queued...))
	mock.ExpectQuery("INSERT INTO jobs").WithArgs(pgx.NamedArgs{"org_id": orgID, "queue": "default", "job_type": "merge_pull_request_when_ready", "payload": pgxmock.AnyArg(), "priority": 7, "dedupe_key": pgxmock.AnyArg()}).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	requests := 0
	service := &PRService{
		backgroundQuotaController: controller, cachedResolver: identity.NewResolver(reconciliationTokenSource{}, zerolog.Nop()),
		pullRequests: db.NewPullRequestStore(mock), repos: db.NewRepositoryStore(mock), sessions: db.NewSessionStore(mock), jobs: db.NewJobStore(mock), logger: zerolog.Nop(),
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"message":"unexpected"}`))}, nil
		})},
	}
	require.NoError(t, service.ReconcilePullRequestState(context.Background(), orgID, 10), "sweep should consume optional budget skips successfully")
	require.Equal(t, 0, requests, "budgeted sweep should save remote health readbacks")
	require.NoError(t, mock.ExpectationsWereMet(), "local recovery and merge processing should remain independent of the optional budget")
}

func TestPRHealthBackgroundBudgetSweepDoesNotAbsorbOrdinaryFlights(t *testing.T) {
	t.Parallel()
	orgID, prID := uuid.New(), uuid.New()
	service, gate := newDriftSyncService(t, orgID, prID)
	service.backgroundQuotaController = newPRHealthQuotaController(t)
	ctx := WithPullRequestSyncReason(withReconciliationScope(context.Background()), PullRequestSyncReasonStaleReconcile)
	leaderErr := make(chan error, 1)
	go func() { leaderErr <- service.SyncPullRequestState(ctx, orgID, prID) }()
	gate.awaitInFlight()
	var followerRan atomic.Bool
	follower := service.prHealthSyncGroup.DoChan(orgID.String()+":"+prID.String(), func() (any, error) {
		followerRan.Store(true)
		return nil, nil
	})
	select {
	case result := <-follower:
		require.False(t, result.Shared, "a budgeted sweep must not absorb an ordinary or urgent flight")
		require.True(t, followerRan.Load(), "ordinary work should obtain its own flight immediately")
	case <-time.After(5 * time.Second):
		t.Fatal("budgeted sweep absorbed work outside its scoped flight")
	}
	gate.releaseNow()
	require.NoError(t, <-leaderErr, "healthy scoped sweep should complete independently")
}
