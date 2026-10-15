package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
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
			orgID := uuid.New()
			count := mock.ExpectQuery(`SELECT COUNT\(\*\) FROM`).WithArgs(pgx.NamedArgs{"org_id": orgID, "population_cap": 1000})
			if tt.countErr != nil {
				count.WillReturnError(tt.countErr)
			} else {
				count.WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(tt.population))
			}
			mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").WithArgs(pgx.NamedArgs{"org_id": orgID, "before": pgxmock.AnyArg(), "limit": tt.wantLimit}).WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))
			mock.ExpectQuery("SELECT s.id[\\s\\S]*FROM session_publish_state").WithArgs(pgx.NamedArgs{"org_id": orgID, "stuck_before": pgxmock.AnyArg(), "limit": 50}).WillReturnRows(pgxmock.NewRows([]string{"id"}))
			mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*merge_when_ready_state = 'queued'[\\s\\S]*merge_when_ready_state = 'merging'").WithArgs(pgx.NamedArgs{"org_id": orgID, "stale_before": pgxmock.AnyArg(), "limit": 50}).WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))
			mock.ExpectQuery("SELECT .+ FROM session_publications").WithArgs(pgx.NamedArgs{"org_id": orgID, "updated_before": pgxmock.AnyArg(), "limit": 50}).WillReturnRows(pgxmock.NewRows(publicationHealthTestColumns))
			var logs bytes.Buffer
			service := &PRService{
				pullRequests: db.NewPullRequestStore(mock), sessions: db.NewSessionStore(mock), repos: db.NewRepositoryStore(mock),
				publications: db.NewSessionPublicationStore(mock), changesets: db.NewSessionChangesetStore(mock), logger: zerolog.New(&logs),
			}
			require.NoError(t, service.ReconcilePullRequestState(context.Background(), orgID, 50), "a count failure alone should not retry completed health and recovery work")
			require.NoError(t, mock.ExpectationsWereMet(), "every independent recovery should retain its original limit")
			var records []map[string]any
			decoder := json.NewDecoder(&logs)
			for decoder.More() {
				var record map[string]any
				require.NoError(t, decoder.Decode(&record), "sweep telemetry should remain structured JSON")
				records = append(records, record)
			}
			last := records[len(records)-1]
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
