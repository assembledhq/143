package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/github/identity"
	"github.com/assembledhq/143/internal/services/github/ratelimit"
	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
)

func expectHealthPopulation(mock pgxmock.PgxPoolIface, orgID uuid.UUID, population int) {
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM`).WithArgs(pgx.NamedArgs{"org_id": orgID, "population_cap": 1000}).WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(population))
}

func TestPullRequestHealthCandidateLimit(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                    string
		limit, population, want int
	}{
		{name: "empty population", limit: 50, want: 50},
		{name: "small explicit bound", limit: 10, population: 20, want: 10},
		{name: "floor boundary", limit: 50, population: 250, want: 50},
		{name: "rounds upward", limit: 50, population: 251, want: 51},
		{name: "larger population", limit: 50, population: 681, want: 137},
		{name: "requested larger bound", limit: 180, population: 681, want: 180},
		{name: "zero defaults", population: 251, want: 51},
		{name: "negative defaults", limit: -1, population: 250, want: 50},
		{name: "oversized defaults", limit: 201, population: 250, want: 50},
		{name: "cap reached", limit: 50, population: 1000, want: 200},
		{name: "population beyond cap", limit: 50, population: 1000000, want: 200},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, pullRequestHealthCandidateLimit(tt.limit, tt.population), "health capacity should cover five passes without changing the requested floor or store cap")
		})
	}
}

func TestReconcilePullRequestHealthCapacityPreservesRecoveryLimits(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		population int
		countErr   error
		wantLimit  int
	}{
		{name: "adaptive health only", population: 681, wantLimit: 137},
		{name: "failed count falls back", countErr: errors.New("count unavailable"), wantLimit: 50},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "capacity mock should initialize")
			t.Cleanup(mock.Close)
			orgID, jobID, token := uuid.New(), uuid.New(), uuid.New()
			expectSweepCheckpointLoad(t, mock, orgID, jobID, token, nil, nil)
			count := mock.ExpectQuery(`SELECT COUNT\(\*\) FROM`).WithArgs(pgx.NamedArgs{"org_id": orgID, "population_cap": 1000})
			if tt.countErr != nil {
				count.WillReturnError(tt.countErr)
			} else {
				count.WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(tt.population))
			}
			mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").WithArgs(pgx.NamedArgs{"org_id": orgID, "before": pgxmock.AnyArg(), "limit": tt.wantLimit}).WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))
			expectSweepCheckpointSave(t, mock, orgID, jobID, token, db.PullRequestHealthSweepCheckpoint{
				Version: 1, RemainingPullRequestIDs: []uuid.UUID{}, Population: tt.population, PopulationAvailable: tt.countErr == nil,
				CandidateLimit: tt.wantLimit, SelectedCount: 0,
			}, nil)
			mock.ExpectQuery("SELECT s.id[\\s\\S]*FROM session_publish_state").WithArgs(pgx.NamedArgs{"org_id": orgID, "stuck_before": pgxmock.AnyArg(), "limit": 50}).WillReturnRows(pgxmock.NewRows([]string{"id"}))
			mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*merge_when_ready_state = 'queued'[\\s\\S]*merge_when_ready_state = 'merging'").WithArgs(pgx.NamedArgs{"org_id": orgID, "stale_before": pgxmock.AnyArg(), "limit": 50}).WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))
			mock.ExpectQuery("SELECT .+ FROM session_publications").WithArgs(pgx.NamedArgs{"org_id": orgID, "updated_before": pgxmock.AnyArg(), "limit": 50}).WillReturnRows(pgxmock.NewRows(publicationHealthTestColumns))
			var logs bytes.Buffer
			service := &PRService{
				pullRequests: db.NewPullRequestStore(mock), sessions: db.NewSessionStore(mock), repos: db.NewRepositoryStore(mock),
				publications: db.NewSessionPublicationStore(mock), changesets: db.NewSessionChangesetStore(mock), jobs: db.NewJobStore(mock), logger: zerolog.New(&logs),
			}
			ctx := jobctx.WithLockToken(jobctx.WithJobID(context.Background(), jobID), token)
			require.NoError(t, service.ReconcilePullRequestState(ctx, orgID, 50), "a count failure alone should not retry completed health and recovery work")
			require.NoError(t, mock.ExpectationsWereMet(), "every independent recovery should retain its original limit")
			var records []map[string]any
			decoder := json.NewDecoder(&logs)
			for decoder.More() {
				var record map[string]any
				require.NoError(t, decoder.Decode(&record), "sweep telemetry should remain structured JSON")
				records = append(records, record)
			}
			last := records[len(records)-1]
			require.Equal(t, jobID.String(), last["job_id"], "sweep outcomes should join the same current job as its native request logs")
			require.Equal(t, float64(tt.wantLimit), last["health_candidate_limit"], "telemetry should expose actual health capacity")
			require.Equal(t, tt.countErr == nil, last["health_population_available"], "telemetry should distinguish fallback from an empty population")
			require.Equal(t, float64(0), last["health_started"], "empty health candidates should not become attempted requests")
		})
	}
}

func TestRunPullRequestHealthCandidatesBoundsConcurrencyAndPreservesOrder(t *testing.T) {
	t.Parallel()
	candidates := []models.PullRequest{{ID: uuid.New()}, {ID: uuid.New()}, {ID: uuid.New()}, {ID: uuid.New()}}
	started := make(chan uuid.UUID, len(candidates))
	release := make(chan struct{})
	done := make(chan []pullRequestHealthOutcome, 1)
	var active, maximum atomic.Int32
	firstErr := errors.New("first candidate retry")
	go func() {
		done <- runPullRequestHealthCandidates(context.Background(), candidates, time.Now().Add(time.Minute), time.Now, func(_ context.Context, id uuid.UUID) error {
			current := active.Add(1)
			for old := maximum.Load(); current > old && !maximum.CompareAndSwap(old, current); old = maximum.Load() {
			}
			started <- id
			<-release
			active.Add(-1)
			if id == candidates[0].ID {
				return firstErr
			}
			return nil
		})
	}()
	for range prHealthSweepConcurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("two health candidates should start concurrently")
		}
	}
	select {
	case <-done:
		t.Fatal("health dispatch must wait for its in-flight candidates")
	default:
	}
	require.Equal(t, int32(prHealthSweepConcurrency), active.Load(), "only the bounded health workers should run before either finishes")
	close(release)
	select {
	case outcomes := <-done:
		require.Equal(t, []pullRequestHealthOutcome{{started: true, err: firstErr}, {started: true}, {started: true}, {started: true}}, outcomes, "completion order should not change candidate-order error aggregation")
	case <-time.After(time.Second):
		t.Fatal("health workers should settle after their in-flight candidates finish")
	}
	require.Equal(t, int32(prHealthSweepConcurrency), maximum.Load(), "health parallelism should never exceed its fixed bound")
}

func TestRunPullRequestHealthCandidatesSkipsExpiredOrCancelledWork(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name               string
		cancelled, expired bool
	}{
		{name: "expired start budget", expired: true},
		{name: "cancelled parent", cancelled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if tt.cancelled {
				cancel()
			}
			deadline := time.Now().Add(time.Minute)
			if tt.expired {
				deadline = time.Now().Add(-time.Second)
			}
			var calls atomic.Int32
			outcomes := runPullRequestHealthCandidates(ctx, []models.PullRequest{{ID: uuid.New()}, {ID: uuid.New()}, {ID: uuid.New()}}, deadline, time.Now, func(context.Context, uuid.UUID) error {
				calls.Add(1)
				return nil
			})
			require.Equal(t, int32(0), calls.Load(), "expired or cancelled work should never start a provider sync")
			want := pullRequestHealthOutcome{budgetExpired: tt.expired}
			require.Equal(t, []pullRequestHealthOutcome{want, want, want}, outcomes, "skipped candidates should report the specific start gate")
		})
	}
}

func TestRunPullRequestHealthCandidatesStopsStartingAfterInFlightBudgetExpiry(t *testing.T) {
	t.Parallel()
	candidates := []models.PullRequest{{ID: uuid.New()}, {ID: uuid.New()}, {ID: uuid.New()}, {ID: uuid.New()}}
	started := make(chan struct{}, len(candidates))
	release := make(chan struct{})
	done := make(chan []pullRequestHealthOutcome, 1)
	deadline := time.Now().Add(time.Minute)
	var expired atomic.Bool
	now := func() time.Time {
		if expired.Load() {
			return deadline
		}
		return deadline.Add(-time.Second)
	}
	go func() {
		done <- runPullRequestHealthCandidates(context.Background(), candidates, deadline, now, func(context.Context, uuid.UUID) error {
			started <- struct{}{}
			<-release
			return nil
		})
	}()
	for range prHealthSweepConcurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("the bounded first pair should start before the dispatch budget expires")
		}
	}
	expired.Store(true)
	select {
	case <-done:
		t.Fatal("expiry must not cancel an already in-flight shared sync")
	default:
	}
	close(release)
	select {
	case outcomes := <-done:
		require.Equal(t, []pullRequestHealthOutcome{{started: true}, {started: true}, {budgetExpired: true}, {budgetExpired: true}}, outcomes, "after settling the first pair no later candidate should start past the deadline")
	case <-time.After(time.Second):
		t.Fatal("finished health candidates should release the recovery phase")
	}
}

func TestRunPullRequestHealthCandidatesCancellationStopsFurtherStarts(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	started := make(chan struct{}, 4)
	done := make(chan []pullRequestHealthOutcome, 1)
	go func() {
		done <- runPullRequestHealthCandidates(ctx, []models.PullRequest{{ID: uuid.New()}, {ID: uuid.New()}, {ID: uuid.New()}, {ID: uuid.New()}}, time.Now().Add(time.Minute), time.Now, func(ctx context.Context, _ uuid.UUID) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	for range prHealthSweepConcurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("the bounded first pair should start before cancellation")
		}
	}
	cancel()
	select {
	case outcomes := <-done:
		require.Equal(t, []pullRequestHealthOutcome{{started: true, err: context.Canceled}, {started: true, err: context.Canceled}, {}, {}}, outcomes, "parent cancellation should preserve started errors and skip new syncs")
	case <-time.After(time.Second):
		t.Fatal("cancelled health dispatch should settle")
	}
}

func TestWithPullRequestHealthMetadataPreservesAttribution(t *testing.T) {
	t.Parallel()
	orgID, prID, assessmentID := uuid.New(), uuid.New(), uuid.New()
	original := githubtelemetry.RequestMetadata{Caller: "pr_health", SyncReason: "stale_reconcile", HealthOrigin: "periodic", SessionID: uuid.NewString(), AssessmentID: assessmentID, PullRequestID: uuid.New(), OrgID: uuid.NewString()}
	ctx := githubtelemetry.WithRequestMetadata(context.Background(), original)
	actual, present := githubtelemetry.RequestMetadataFromContext(withPullRequestHealthMetadata(ctx, orgID, prID))
	require.True(t, present, "health sync should carry request attribution")
	want := original
	want.OrgID, want.PullRequestID = orgID.String(), prID
	require.Equal(t, want, actual, "validated health identity should replace only organization and PR while preserving caller, origin, session, and assessment")
}

func TestReconcilePullRequestHealthRetainedThrottleFanout(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "throttle fanout database mock should initialize")
	t.Cleanup(mock.Close)
	mock.MatchExpectationsInOrder(false)
	orgID := uuid.New()
	now := time.Now().UTC()
	retained := &reconciliationInstallationError{installationID: 42, err: &GitHubAPIError{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"60"}}}}
	ctx := withReconciliationScope(context.Background())
	reconciliationScopeFromContext(ctx).observe(nil, retained)
	expectHealthPopulation(mock, orgID, 1000)
	staleRows := pgxmock.NewRows(prTestPullRequestColumns)
	ids := make([]uuid.UUID, prHealthCandidateLimit)
	for i := range ids {
		ids[i] = uuid.New()
		row := newPRTestRow(ids[i], nil, orgID, "example/deferred", now, nil)
		staleRows.AddRow(row...)
		mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").WithArgs(pgx.NamedArgs{"org_id": orgID, "id": ids[i]}).WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(row...))
		expectReconciliationRepository(mock, orgID, uuid.New(), "example/deferred", 42, false)
	}
	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").WithArgs(pgx.NamedArgs{"org_id": orgID, "before": pgxmock.AnyArg(), "limit": prHealthCandidateLimit}).WillReturnRows(staleRows)
	mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*merge_when_ready_state = 'queued'[\\s\\S]*merge_when_ready_state = 'merging'").WithArgs(pgx.NamedArgs{"org_id": orgID, "stale_before": pgxmock.AnyArg(), "limit": 50}).WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))
	var logs bytes.Buffer
	var outbound atomic.Int32
	service := &PRService{
		pullRequests: db.NewPullRequestStore(mock), repos: db.NewRepositoryStore(mock),
		cachedResolver: identity.NewResolver(reconciliationTokenSource{}, zerolog.Nop()), logger: zerolog.New(zerolog.SyncWriter(&logs)),
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			outbound.Add(1)
			return nil, errors.New("suppressed requests must not reach GitHub")
		})},
	}
	err = service.ReconcilePullRequestState(ctx, orgID, 50)
	require.Error(t, err, "the original throttle should remain available for the durable retry")
	joined, ok := err.(interface{ Unwrap() []error })
	require.True(t, ok, "reconciliation should preserve its joined-error contract")
	var actualErrors []string
	for _, cause := range joined.Unwrap() {
		actualErrors = append(actualErrors, cause.Error())
	}
	require.Equal(t, []string{fmt.Sprintf("reconcile pull request %s: %s", ids[0], retained.Error())}, actualErrors, "two hundred identical retained throttles should keep only the first joined entry")
	require.Equal(t, ClassifyRetry(retained, now), ClassifyRetry(err, now), "deduplication must preserve throttle kind and retry timing")
	require.ErrorIs(t, err, retained, "the retained installation error should remain in the error chain")
	require.Equal(t, int32(0), outbound.Load(), "all candidates should stay suppressed by the installation cooldown")
	decoder := json.NewDecoder(&logs)
	warnings := 0
	var summary map[string]any
	for {
		var record map[string]any
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err, "fanout telemetry should remain structured JSON")
		if record["level"] == "warn" {
			warnings++
		}
		if record["message"] == "completed periodic pull request health sweep" {
			summary = record
		}
	}
	require.Equal(t, 1, warnings, "the retained throttle should produce one warning per sweep")
	require.Equal(t, float64(prHealthCandidateLimit), summary["health_deferred"], "every suppressed candidate must still contribute to the deferred count")
	require.Equal(t, float64(prHealthCandidateLimit), summary["health_started"], "deduplication must not hide candidate processing")
	require.Equal(t, float64(0), summary["health_failed"], "retained cooldowns should not become ordinary failures")
	require.NoError(t, mock.ExpectationsWereMet(), "the sweep should preserve all candidate reads and the original recovery bound")
}

func TestRetainReconciliationThrottlePreservesDistinctErrors(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		build func() ([]error, []bool)
	}{
		{name: "same scoped error through separate wrappers", build: func() ([]error, []bool) {
			retained := &reconciliationInstallationError{installationID: 42, err: &GitHubAPIError{StatusCode: 429}}
			return []error{fmt.Errorf("first: %w", retained), fmt.Errorf("second: %w", retained)}, []bool{true, false}
		}},
		{name: "different installations sharing an underlying error", build: func() ([]error, []bool) {
			cause := &GitHubAPIError{StatusCode: 429}
			return []error{&reconciliationInstallationError{installationID: 42, err: cause}, &reconciliationInstallationError{installationID: 43, err: cause}}, []bool{true, true}
		}},
		{name: "distinct controller generations and deadlines", build: func() ([]error, []bool) {
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			first := &ratelimit.Deferral{InstallationID: 42, Kind: ratelimit.KindSecondary, Generation: 1, RetryAt: now.Add(time.Minute)}
			generation := &ratelimit.Deferral{InstallationID: 42, Kind: ratelimit.KindSecondary, Generation: 2, RetryAt: first.RetryAt}
			deadline := &ratelimit.Deferral{InstallationID: 42, Kind: ratelimit.KindSecondary, Generation: 2, RetryAt: now.Add(2 * time.Minute)}
			return []error{first, first, generation, deadline}, []bool{true, false, true, true}
		}},
		{name: "retained controlled outbound error", build: func() ([]error, []bool) {
			retained := &ratelimit.ControlledError{Err: &GitHubAPIError{StatusCode: 429}, Deferral: ratelimit.Deferral{InstallationID: 42, Generation: 1}}
			return []error{retained, fmt.Errorf("shared: %w", retained)}, []bool{true, false}
		}},
		{name: "unknown or user principal is never globally collapsed", build: func() ([]error, []bool) {
			unknown := &GitHubAPIError{StatusCode: 429}
			unresolved := &ratelimit.Deferral{Kind: ratelimit.KindSecondary}
			return []error{unknown, unknown, unresolved, unresolved}, []bool{true, true, true, true}
		}},
		{name: "joined sibling metadata is retained", build: func() ([]error, []bool) {
			retained := &reconciliationInstallationError{installationID: 42, err: &GitHubAPIError{StatusCode: 429}}
			joined := errors.Join(retained, &ratelimit.Deferral{InstallationID: 43, Kind: ratelimit.KindSecondary, Generation: 2})
			return []error{retained, joined, joined}, []bool{true, true, true}
		}},
		{name: "outer response retry metadata is retained", build: func() ([]error, []bool) {
			retained := &reconciliationInstallationError{installationID: 42, err: &GitHubAPIError{StatusCode: 429}}
			first := &GitHubResponseReadError{StatusCode: 429, Header: http.Header{"Retry-After": []string{"60"}}, Err: retained}
			second := &GitHubResponseReadError{StatusCode: 429, Header: http.Header{"Retry-After": []string{"10"}}, Err: retained}
			return []error{first, second}, []bool{true, true}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			original, want := tt.build()
			seen := make(map[error]struct{})
			var actual []bool
			var retained []error
			for _, err := range original {
				keep := retainReconciliationThrottle(seen, err)
				actual = append(actual, keep)
				if keep {
					retained = append(retained, err)
				}
			}
			require.Equal(t, want, actual, "only the same retained installation-scoped error should be suppressed")
			now := time.Date(2025, 12, 31, 23, 59, 0, 0, time.UTC)
			require.Equal(t, ClassifyRetry(errors.Join(original...), now), ClassifyRetry(errors.Join(retained...), now), "retained errors should preserve the earliest retry deadline and classification")
		})
	}
}
