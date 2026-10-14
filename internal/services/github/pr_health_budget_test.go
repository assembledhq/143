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
	return newPRHealthQuotaControllerForInstallation(t, 42, 10)
}

func newPRHealthQuotaControllerForInstallation(t *testing.T, installationID, remaining int64) *ratelimit.Controller {
	t.Helper()
	controller, _ := newPRHealthQuotaFixture(t, installationID, remaining, true)
	return controller
}

func newPRHealthQuotaFixture(t *testing.T, installationID, remaining int64, occupySlot bool) (*ratelimit.Controller, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + server.Addr()}, zerolog.Nop(), nil)
	t.Cleanup(func() { require.NoError(t, client.Close(), "isolated quota cache should close") })
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	controller, err := ratelimit.NewController(ratelimit.Config{
		Mode: ratelimit.ModeEnforce, AppID: 1, InstallationAllowlist: []int64{installationID}, Cache: client,
		Now: func() time.Time { return now }, BackgroundQuotaReserve: 20, BackgroundQuotaInterval: time.Minute, Logger: zerolog.Nop(),
	})
	require.NoError(t, err, "enabled isolated quota controller should initialize")
	controller.Observe(context.Background(), ratelimit.Permit{Scope: ratelimit.Scope{InstallationID: installationID}}, ratelimit.Observation{StatusCode: http.StatusOK, Header: http.Header{
		"X-Ratelimit-Limit": {"100"}, "X-Ratelimit-Remaining": {strconv.FormatInt(remaining, 10)}, "X-Ratelimit-Resource": {"core"},
		"X-Ratelimit-Reset": {strconv.FormatInt(now.Add(time.Hour).Unix(), 10)},
	}})
	if occupySlot {
		require.True(t, controller.AdmitBackground(context.Background(), installationID, "another-tenant:another-pr"), "another background candidate should occupy the current interval")
	}
	return controller, server
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

func TestPRHealthBackgroundBudgetAdmittedSweepSharesOrdinaryFlights(t *testing.T) {
	t.Parallel()
	orgID, prID := uuid.New(), uuid.New()
	service, gate := newDriftSyncService(t, orgID, prID)
	service.backgroundQuotaController = newPRHealthQuotaControllerForInstallation(t, 123, 90)
	ctx := WithPullRequestSyncReason(withReconciliationScope(context.Background()), PullRequestSyncReasonStaleReconcile)
	leaderErr := make(chan error, 1)
	go func() { leaderErr <- service.SyncPullRequestState(ctx, orgID, prID) }()
	gate.awaitInFlight()
	var followerRan atomic.Bool
	follower := service.prHealthSyncGroup.DoChan(orgID.String()+":"+prID.String(), func() (any, error) {
		followerRan.Store(true)
		return nil, nil
	})
	gate.releaseNow()
	result := <-follower
	require.True(t, result.Shared, "an admitted sweep should share remote reads with ordinary stale work")
	require.False(t, followerRan.Load(), "ordinary stale work should reuse an admitted sweep instead of duplicating its remote reads")
	require.NoError(t, <-leaderErr, "healthy admitted sweep should complete normally")
	require.Equal(t, int32(1), gate.fetches.Load(), "admitted sweep and ordinary flight should make only one remote read")
}

func TestPRHealthBackgroundBudgetFlightKeysRespectRolloutScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		mode         ratelimit.Mode
		allowlist    []int64
		reserve      int64
		source       identity.Source
		installation int64
		expected     string
	}{
		{name: "disabled", mode: ratelimit.ModeEnforce, allowlist: []int64{42}, source: identity.SourceApp, installation: 42, expected: "ordinary"},
		{name: "observe empty allowlist", mode: ratelimit.ModeObserve, reserve: 20, source: identity.SourceApp, installation: 42, expected: "ordinary"},
		{name: "observe selected", mode: ratelimit.ModeObserve, allowlist: []int64{42}, reserve: 20, source: identity.SourceApp, installation: 42, expected: "ordinary"},
		{name: "enforce empty allowlist", mode: ratelimit.ModeEnforce, reserve: 20, source: identity.SourceApp, installation: 42, expected: "ordinary"},
		{name: "enforce selected", mode: ratelimit.ModeEnforce, allowlist: []int64{42}, reserve: 20, source: identity.SourceApp, installation: 42, expected: "ordinary:quota-sweep"},
		{name: "enforce unselected", mode: ratelimit.ModeEnforce, allowlist: []int64{43}, reserve: 20, source: identity.SourceApp, installation: 42, expected: "ordinary"},
		{name: "user token", mode: ratelimit.ModeEnforce, allowlist: []int64{42}, reserve: 20, source: identity.SourceUser, installation: 42, expected: "ordinary"},
		{name: "unresolved installation", mode: ratelimit.ModeEnforce, allowlist: []int64{42}, reserve: 20, source: identity.SourceApp, expected: "ordinary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			controller, err := ratelimit.NewController(ratelimit.Config{Mode: tt.mode, AppID: 1, InstallationAllowlist: tt.allowlist, BackgroundQuotaReserve: tt.reserve})
			require.NoError(t, err, "rollout settings should construct an isolated controller")
			service := &PRService{backgroundQuotaController: controller}
			ctx := WithPullRequestSyncReason(withReconciliationScope(context.Background()), PullRequestSyncReasonStaleReconcile)
			actual := service.backgroundQuotaFlightKey(ctx, "ordinary", &identity.Resolution{Source: tt.source, InstallationID: tt.installation})
			require.Equal(t, tt.expected, actual, "non-enforcing sweeps must retain ordinary queued-flight deduplication")
		})
	}
}

func TestPRHealthBackgroundBudgetBypassRetainsOrdinaryFlight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mode      ratelimit.Mode
		allowlist []int64
		reserve   int64
	}{
		{name: "disabled reserve", mode: ratelimit.ModeEnforce, allowlist: []int64{123}},
		{name: "observe selected", mode: ratelimit.ModeObserve, allowlist: []int64{123}, reserve: 20},
		{name: "enforce unselected", mode: ratelimit.ModeEnforce, allowlist: []int64{42}, reserve: 20},
		{name: "enforce empty allowlist", mode: ratelimit.ModeEnforce, reserve: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, prID := uuid.New(), uuid.New()
			service, gate := newDriftSyncService(t, orgID, prID)
			controller, err := ratelimit.NewController(ratelimit.Config{Mode: tt.mode, AppID: 1, InstallationAllowlist: tt.allowlist, BackgroundQuotaReserve: tt.reserve})
			require.NoError(t, err, "bypassed quota controller should initialize")
			service.backgroundQuotaController = controller
			ctx := WithPullRequestSyncReason(withReconciliationScope(context.Background()), PullRequestSyncReasonStaleReconcile)
			leaderErr := make(chan error, 1)
			go func() { leaderErr <- service.SyncPullRequestState(ctx, orgID, prID) }()
			gate.awaitInFlight()
			var followerRan atomic.Bool
			follower := service.prHealthSyncGroup.DoChan(orgID.String()+":"+prID.String(), func() (any, error) {
				followerRan.Store(true)
				return nil, nil
			})
			gate.releaseNow()
			result := <-follower
			require.NoError(t, result.Err, "ordinary follower should share the healthy sweep result")
			require.True(t, result.Shared, "a bypassed sweep must stay on the ordinary flight")
			require.False(t, followerRan.Load(), "bypassed quota must not spend a duplicate remote flight")
			require.NoError(t, <-leaderErr, "bypassed sweep should complete normally")
		})
	}
}

type quotaResolutionFailingTokenSource struct {
	failure error
	calls   []int64
}

func (s *quotaResolutionFailingTokenSource) GetInstallationToken(_ context.Context, installationID int64) (string, error) {
	s.calls = append(s.calls, installationID)
	if installationID == 7 {
		return "", &GitHubAPIError{StatusCode: http.StatusNotFound}
	}
	return "", s.failure
}

func TestPRHealthBackgroundBudgetPreResolutionRetainsTrustedFailureScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		reserve int64
		failure func() error
		blocked bool
	}{
		{name: "preflight typed fallback deferral", reserve: 20, blocked: true, failure: func() error {
			return &ratelimit.Deferral{InstallationID: 10, Kind: ratelimit.KindSecondary, RetryAt: time.Now().Add(time.Minute)}
		}},
		{name: "inside flight typed fallback deferral", blocked: true, failure: func() error {
			return &ratelimit.Deferral{InstallationID: 10, Kind: ratelimit.KindSecondary, RetryAt: time.Now().Add(time.Minute)}
		}},
		{name: "preflight controlled fallback error", reserve: 20, blocked: true, failure: func() error {
			return &ratelimit.ControlledError{Err: &GitHubAPIError{StatusCode: http.StatusTooManyRequests}, Deferral: ratelimit.Deferral{InstallationID: 10, Kind: ratelimit.KindSecondary, RetryAt: time.Now().Add(time.Minute)}}
		}},
		{name: "preflight raw throttle remains ungrouped", reserve: 20, failure: func() error {
			return &GitHubAPIError{StatusCode: http.StatusTooManyRequests}
		}},
		{name: "preflight unknown typed identity remains ungrouped", reserve: 20, failure: func() error {
			return &ratelimit.Deferral{Kind: ratelimit.KindSecondary, RetryAt: time.Now().Add(time.Minute)}
		}},
		{name: "preflight ordinary provider failure remains ungrouped", reserve: 20, failure: func() error {
			return &GitHubAPIError{StatusCode: http.StatusServiceUnavailable}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "isolated resolution database should initialize")
			t.Cleanup(mock.Close)
			orgID, prID := uuid.New(), uuid.New()
			row := newPRTestRow(prID, nil, orgID, "example/quota-scope", time.Now().UTC(), nil)
			mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(row...))
			expectReconciliationRepository(mock, orgID, uuid.New(), "example/quota-scope", 7, false)
			failure := tt.failure()
			source := &quotaResolutionFailingTokenSource{failure: failure}
			resolver := identity.NewResolver(source, zerolog.Nop())
			resolver.SetIntegrations(reconciliationIntegrationSource{})
			controller, err := ratelimit.NewController(ratelimit.Config{Mode: ratelimit.ModeEnforce, AppID: 1, InstallationAllowlist: []int64{10}, BackgroundQuotaReserve: tt.reserve})
			require.NoError(t, err, "enforcement canary should initialize with the selected fallback installation")
			service := &PRService{backgroundQuotaController: controller, cachedResolver: resolver, pullRequests: db.NewPullRequestStore(mock), repos: db.NewRepositoryStore(mock), logger: zerolog.Nop()}
			ctx := WithPullRequestSyncReason(withReconciliationScope(context.Background()), PullRequestSyncReasonStaleReconcile)
			err = service.SyncPullRequestState(ctx, orgID, prID)
			require.ErrorIs(t, err, failure, "resolution failures must preserve the original typed retry error")
			require.Equal(t, []int64{7, 10}, source.calls, "scope propagation must perform only the existing primary and fallback token attempts")
			scope := reconciliationScopeFromContext(ctx)
			require.Equal(t, tt.blocked, scope.hasBlockedInstallation(), "only a trusted typed failure may establish an installation suppression bucket")
			if tt.blocked {
				require.ErrorIs(t, scope.deferral(&identity.Resolution{Source: identity.SourceApp, InstallationID: 10}), err, "preflight and in-flight failures must preserve the same authenticated fallback scope")
			} else {
				require.NoError(t, scope.deferral(&identity.Resolution{Source: identity.SourceApp, InstallationID: 10}), "unresolved failures must not guess a provider principal")
			}
			require.NoError(t, scope.deferral(&identity.Resolution{Source: identity.SourceApp, InstallationID: 7}), "the stale recorded installation must not inherit another installation's failure")
			require.NoError(t, scope.deferral(&identity.Resolution{Source: identity.SourceApp, InstallationID: 99}), "unrelated installations must remain available")
			require.NoError(t, mock.ExpectationsWereMet(), "token-resolution failures must stop before freshness reservation or GitHub health reads")
		})
	}
}

func TestPRHealthBackgroundBudgetCoalescesAdmissionAndOrdinaryReads(t *testing.T) {
	t.Parallel()
	orgID, prID := uuid.New(), uuid.New()
	service, gate := newDriftSyncService(t, orgID, prID)
	controller, redis := newPRHealthQuotaFixture(t, 123, 10, false)
	service.backgroundQuotaController = controller
	sweepCtx := WithPullRequestSyncReason(withReconciliationScope(context.Background()), PullRequestSyncReasonStaleReconcile)
	sweepErr := make(chan error, 1)
	go func() { sweepErr <- service.SyncPullRequestState(sweepCtx, orgID, prID) }()
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("one low-quota admission should begin the shared remote read without spending a second slot")
	}
	readKey := orgID.String() + ":" + prID.String()
	resolved := &resolvedPullRequestHealthSync{resolution: &identity.Resolution{Source: identity.SourceApp, InstallationID: 123}}
	var duplicateAdmissionRan atomic.Bool
	duplicate := service.prHealthSyncGroup.DoChan(service.backgroundQuotaFlightKey(sweepCtx, readKey, resolved.resolution), func() (any, error) {
		duplicateAdmissionRan.Store(true)
		return nil, service.syncBudgetedPullRequestState(sweepCtx, readKey, orgID, prID, resolved)
	})
	waiterCtx := &reconciliationWaiterContext{Context: WithPullRequestSyncReason(context.Background(), PullRequestSyncReasonStaleReconcile), joined: make(chan struct{})}
	ordinaryErr := make(chan error, 1)
	go func() { ordinaryErr <- service.SyncPullRequestState(waiterCtx, orgID, prID) }()
	select {
	case <-waiterCtx.joined:
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary stale work should register on the admitted sweep's read flight")
	}
	gate.releaseNow()
	require.NoError(t, <-sweepErr, "one admitted sweep should complete without double-spending quota")
	require.NoError(t, <-ordinaryErr, "ordinary callers sharing an admitted read must not receive a quota sentinel")
	result := <-duplicate
	require.NoError(t, result.Err, "duplicate sweep should reuse the successful admitted sweep")
	require.True(t, result.Shared, "duplicate sweeps should share their outer admission lane")
	require.False(t, duplicateAdmissionRan.Load(), "duplicate sweeps must not consume another admission")
	require.Equal(t, int32(1), gate.fetches.Load(), "admitted sweep, duplicate sweep, and ordinary caller should make one remote read")
	require.Equal(t, "1", redis.HGet("{ghrl:v1:unknown:app:1:inst:123}:quota:core", "sequence"), "the admitted read should consume exactly one distributed quota slot")
}

func TestPRHealthBackgroundBudgetAdmittedSweepJoinsExistingOrdinaryRead(t *testing.T) {
	t.Parallel()
	orgID, prID := uuid.New(), uuid.New()
	service, gate := newDriftSyncService(t, orgID, prID)
	controller, redis := newPRHealthQuotaFixture(t, 123, 10, false)
	service.backgroundQuotaController = controller
	ordinaryErr := make(chan error, 1)
	go func() {
		ordinaryErr <- service.SyncPullRequestState(WithPullRequestSyncReason(context.Background(), PullRequestSyncReasonStaleReconcile), orgID, prID)
	}()
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary read should enter its remote request before sweep admission")
	}
	readKey := orgID.String() + ":" + prID.String()
	resolved := &resolvedPullRequestHealthSync{resolution: &identity.Resolution{Source: identity.SourceApp, InstallationID: 123}}
	sweepCtx := WithPullRequestSyncReason(withReconciliationScope(context.Background()), PullRequestSyncReasonStaleReconcile)
	require.True(t, service.admitBackgroundPullRequestSync(sweepCtx, orgID, prID, resolved.resolution), "sweep should obtain its quota slot before entering the existing ordinary flight")
	waiterCtx := &reconciliationWaiterContext{Context: sweepCtx, joined: make(chan struct{})}
	sweepErr := make(chan error, 1)
	go func() { sweepErr <- service.syncAdmittedPullRequestState(waiterCtx, readKey, orgID, prID, resolved) }()
	select {
	case <-waiterCtx.joined:
	case <-time.After(5 * time.Second):
		t.Fatal("admitted sweep should register on the existing ordinary flight")
	}
	gate.releaseNow()
	require.NoError(t, <-ordinaryErr, "ordinary leader should complete normally")
	require.NoError(t, <-sweepErr, "admitted sweep should receive the existing ordinary result")
	require.Equal(t, int32(1), gate.fetches.Load(), "admitted sweep must not duplicate an already-running ordinary read")
	require.Equal(t, "1", redis.HGet("{ghrl:v1:unknown:app:1:inst:123}:quota:core", "sequence"), "joining an existing read must not consume a second quota admission")
}

func TestPRHealthBackgroundBudgetSkippedSweepDoesNotJoinOrdinaryRead(t *testing.T) {
	t.Parallel()
	orgID, prID := uuid.New(), uuid.New()
	service, gate := newDriftSyncService(t, orgID, prID)
	service.backgroundQuotaController = newPRHealthQuotaControllerForInstallation(t, 123, 10)
	ordinaryErr := make(chan error, 1)
	go func() {
		ordinaryErr <- service.SyncPullRequestState(WithPullRequestSyncReason(context.Background(), PullRequestSyncReasonStaleReconcile), orgID, prID)
	}()
	select {
	case <-gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary work should bypass the occupied background slot")
	}
	readKey := orgID.String() + ":" + prID.String()
	resolved := &resolvedPullRequestHealthSync{resolution: &identity.Resolution{Source: identity.SourceApp, InstallationID: 123}}
	sweepCtx := WithPullRequestSyncReason(withReconciliationScope(context.Background()), PullRequestSyncReasonStaleReconcile)
	err := service.syncBudgetedPullRequestState(sweepCtx, readKey, orgID, prID, resolved)
	require.ErrorIs(t, err, errPullRequestBackgroundQuotaDeferred, "a skipped sweep must return before waiting on any ordinary read")
	waiterCtx := &reconciliationWaiterContext{Context: WithPullRequestSyncReason(context.Background(), PullRequestSyncReasonStaleReconcile), joined: make(chan struct{})}
	followerErr := make(chan error, 1)
	go func() { followerErr <- service.SyncPullRequestState(waiterCtx, orgID, prID) }()
	select {
	case <-waiterCtx.joined:
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary follower should still join its independently running read")
	}
	gate.releaseNow()
	require.NoError(t, <-ordinaryErr, "ordinary leader must not inherit the skipped sweep's sentinel")
	require.NoError(t, <-followerErr, "ordinary follower must not inherit the skipped sweep's sentinel")
	require.Equal(t, int32(1), gate.fetches.Load(), "skipped sweep should issue no additional remote reads")
}

func TestPRHealthBackgroundBudgetJoinedReadRetainsFailureIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		failure func() error
		blocked bool
	}{
		{name: "authenticated ordinary leader", blocked: true, failure: func() error {
			return &reconciliationInstallationError{err: &GitHubAPIError{StatusCode: http.StatusTooManyRequests}, installationID: 77}
		}},
		{name: "typed controller deferral", blocked: true, failure: func() error {
			return &ratelimit.Deferral{InstallationID: 77, Kind: ratelimit.KindSecondary, RetryAt: time.Now().Add(time.Minute)}
		}},
		{name: "unknown leader identity", failure: func() error { return &GitHubAPIError{StatusCode: http.StatusTooManyRequests} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, prID := uuid.New(), uuid.New()
			readKey := orgID.String() + ":" + prID.String()
			failure := tt.failure()
			service := &PRService{}
			release := make(chan struct{})
			leader := service.prHealthSyncGroup.DoChan(readKey, func() (any, error) { <-release; return nil, failure })
			sweepCtx := WithPullRequestSyncReason(withReconciliationScope(context.Background()), PullRequestSyncReasonStaleReconcile)
			waiterCtx := &reconciliationWaiterContext{Context: sweepCtx, joined: make(chan struct{})}
			resolved := &resolvedPullRequestHealthSync{resolution: &identity.Resolution{Source: identity.SourceApp, InstallationID: 123}}
			sweepErr := make(chan error, 1)
			go func() { sweepErr <- service.syncAdmittedPullRequestState(waiterCtx, readKey, orgID, prID, resolved) }()
			select {
			case <-waiterCtx.joined:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("admitted sweep should register on the existing leader")
			}
			close(release)
			require.ErrorIs(t, <-sweepErr, failure, "joined sweep should preserve the leader's original failure")
			require.ErrorIs(t, (<-leader).Err, failure, "outside leader should retain its original retry contract")
			scope := reconciliationScopeFromContext(sweepCtx)
			require.Equal(t, tt.blocked, scope.hasBlockedInstallation(), "joined reads must propagate only trusted failure identity")
			if tt.blocked {
				require.ErrorIs(t, scope.deferral(&identity.Resolution{Source: identity.SourceApp, InstallationID: 77}), failure, "scope suppression must follow the ordinary leader's actual installation")
			}
			require.NoError(t, scope.deferral(resolved.resolution), "the admitted waiter's installation must not relabel another leader's error")
		})
	}
}
