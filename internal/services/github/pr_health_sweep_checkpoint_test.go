package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
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
)

func sweepCheckpointContext(jobID, token uuid.UUID) context.Context {
	return jobctx.WithLockToken(jobctx.WithJobID(context.Background(), jobID), token)
}

func expectSweepCheckpointLoad(t *testing.T, mock pgxmock.PgxPoolIface, orgID, jobID, token uuid.UUID, checkpoint *db.PullRequestHealthSweepCheckpoint, loadErr error) {
	t.Helper()
	query := mock.ExpectQuery(`SELECT jsonb_typeof\(payload\), payload \? 'health_sweep', payload->'health_sweep'`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "job_id": jobID, "lock_token": token})
	if loadErr != nil {
		query.WillReturnError(loadErr)
		return
	}
	var raw []byte
	if checkpoint != nil {
		var err error
		raw, err = json.Marshal(checkpoint)
		require.NoError(t, err, "encode the exact saved selection fixture")
	}
	query.WillReturnRows(pgxmock.NewRows([]string{"jsonb_typeof", "present", "checkpoint"}).AddRow("object", checkpoint != nil, raw))
}

func expectSweepCheckpointSave(t *testing.T, mock pgxmock.PgxPoolIface, orgID, jobID, token uuid.UUID, checkpoint db.PullRequestHealthSweepCheckpoint, saveErr error) {
	t.Helper()
	raw, err := json.Marshal(checkpoint)
	require.NoError(t, err, "encode the exact expected durable selection")
	query := mock.ExpectExec(`UPDATE jobs[\s\S]*SET payload = jsonb_set\(payload, '\{health_sweep\}'`).
		WithArgs(pgx.NamedArgs{"org_id": orgID, "job_id": jobID, "lock_token": token, "checkpoint": raw})
	if saveErr != nil {
		query.WillReturnError(saveErr)
		return
	}
	query.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
}

func expectSweepRecovery(mock pgxmock.PgxPoolIface, orgID uuid.UUID, limit int, recoveryErr error) {
	query := mock.ExpectQuery("SELECT .+ FROM pull_requests[\\s\\S]*merge_when_ready_state = 'queued'[\\s\\S]*merge_when_ready_state = 'merging'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "stale_before": pgxmock.AnyArg(), "limit": limit})
	if recoveryErr != nil {
		query.WillReturnError(recoveryErr)
		return
	}
	query.WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns))
}

// A newer local lifecycle can supersede an otherwise successful provider read.
// This keeps the fixture focused on sweep retries without reproducing the
// independent health writer's snapshot/upsert tests.
func expectSweepProviderCandidate(mock pgxmock.PgxPoolIface, orgID, id uuid.UUID, name string, succeeds bool) {
	row := newPRTestRow(id, nil, orgID, name, time.Now().UTC(), nil)
	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "id": id}).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(row...))
	expectReconciliationRepository(mock, orgID, uuid.New(), name, 10, false)
	expectReserveCheckStateVersion(mock, orgID, id, 1)
	if !succeeds {
		return
	}
	for range 2 {
		mock.ExpectQuery("SELECT .+ FROM pull_request_health_current").
			WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": id}).
			WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns))
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT COALESCE.*FROM pull_requests.*WHERE org_id = @org_id AND id = @pull_request_id FOR UPDATE").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": id}).
		WillReturnRows(pgxmock.NewRows([]string{"head_sha", "base_sha", "status"}).AddRow("newer-head", "base", models.PullRequestStatusClosed))
	mock.ExpectRollback()
}

func TestReconcilePullRequestHealthSweepRetriesOnlyFrozenRemainder(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create isolated sweep retry fixture")
	t.Cleanup(mock.Close)
	mock.MatchExpectationsInOrder(false)
	orgID, jobID, token := uuid.New(), uuid.New(), uuid.New()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	names := []string{"example/first", "example/transient", "example/third"}
	checkpoint := db.PullRequestHealthSweepCheckpoint{Version: 1, RemainingPullRequestIDs: ids, Population: 3, PopulationAvailable: true, CandidateLimit: 50, SelectedCount: 3}
	expectSweepCheckpointLoad(t, mock, orgID, jobID, token, nil, nil)
	expectHealthPopulation(mock, orgID, 3)
	rows := pgxmock.NewRows(prTestPullRequestColumns)
	for i, id := range ids {
		rows.AddRow(newPRTestRow(id, nil, orgID, names[i], time.Now().UTC(), nil)...)
		expectSweepProviderCandidate(mock, orgID, id, names[i], i != 1)
	}
	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "before": pgxmock.AnyArg(), "limit": 50}).WillReturnRows(rows)
	expectSweepCheckpointSave(t, mock, orgID, jobID, token, checkpoint, nil)
	remainder := checkpoint
	remainder.RemainingPullRequestIDs = []uuid.UUID{ids[1]}
	expectSweepCheckpointSave(t, mock, orgID, jobID, token, remainder, nil)
	recoveryErr := errors.New("independent recovery is temporarily unavailable")
	expectSweepRecovery(mock, orgID, 50, recoveryErr)

	var mu sync.Mutex
	calls := make(map[string]int)
	service := &PRService{jobs: db.NewJobStore(mock), pullRequests: db.NewPullRequestStore(mock), repos: db.NewRepositoryStore(mock), logger: zerolog.Nop(),
		cachedResolver: identity.NewResolver(reconciliationTokenSource{}, zerolog.Nop()), baseURL: "https://api.github.com",
		httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			calls[r.URL.Path]++
			firstTransient := r.URL.Path == "/repos/example/transient/pulls/42" && calls[r.URL.Path] == 1
			mu.Unlock()
			code, body := http.StatusOK, `{"number":42,"state":"open","mergeable":true,"mergeable_state":"clean","head":{"sha":"head"},"base":{"ref":"main","sha":"base"}}`
			switch {
			case firstTransient:
				code, body = http.StatusServiceUnavailable, `{"message":"temporarily unavailable"}`
			case strings.HasSuffix(r.URL.Path, "/check-runs"):
				body = `{"check_runs":[{"id":1,"name":"tests","status":"completed","conclusion":"success"}]}`
			case strings.HasSuffix(r.URL.Path, "/status"):
				body = `{"statuses":[]}`
			}
			return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}}
	ctx := sweepCheckpointContext(jobID, token)
	err = service.ReconcilePullRequestState(ctx, orgID, 50)
	require.True(t, ClassifyRetry(err, time.Now()).Retryable, "one transient provider failure should retry the same periodic job")
	require.ErrorIs(t, err, recoveryErr, "independent recovery should still run after the health phase")
	require.NoError(t, mock.ExpectationsWereMet(), "first attempt should persist only the failed PR after its initial bounded selection")

	// The next stale batch could contain completely different PRs. The retry
	// must not query it, count population again, or create another job.
	expectSweepCheckpointLoad(t, mock, orgID, jobID, token, &remainder, nil)
	expectSweepProviderCandidate(mock, orgID, ids[1], names[1], true)
	complete := checkpoint
	complete.RemainingPullRequestIDs = []uuid.UUID{}
	expectSweepCheckpointSave(t, mock, orgID, jobID, token, complete, nil)
	expectSweepRecovery(mock, orgID, 50, recoveryErr)
	err = service.ReconcilePullRequestState(ctx, orgID, 50)
	require.ErrorIs(t, err, recoveryErr, "a completed targeted retry should still expose independent recovery failures")
	require.False(t, ClassifyRetry(err, time.Now()).Retryable, "the recovered health phase should contribute no provider retry error")
	require.NoError(t, mock.ExpectationsWereMet(), "retry should read only its frozen one-PR remainder and persist explicit completion")

	expectSweepCheckpointLoad(t, mock, orgID, jobID, token, &complete, nil)
	expectSweepRecovery(mock, orgID, 50, nil)
	require.NoError(t, service.ReconcilePullRequestState(ctx, orgID, 50), "a later recovery retry should leave completed health work untouched")
	require.NoError(t, mock.ExpectationsWereMet(), "completed health checkpoint should skip selection, provider reads and additional queue work")
	mu.Lock()
	actual := make(map[string]int, len(calls))
	for path, count := range calls {
		actual[path] = count
	}
	mu.Unlock()
	require.Equal(t, map[string]int{
		"/repos/example/first/pulls/42": 1, "/repos/example/first/commits/head/check-runs": 1, "/repos/example/first/commits/head/status": 1,
		"/repos/example/transient/pulls/42": 2, "/repos/example/transient/commits/head/check-runs": 1, "/repos/example/transient/commits/head/status": 1,
		"/repos/example/third/pulls/42": 1, "/repos/example/third/commits/head/check-runs": 1, "/repos/example/third/commits/head/status": 1,
	}, actual, "successful candidates must never be fetched again by retries of this periodic admission")
}

func TestReconcilePullRequestHealthSweepRequiresDurableSelection(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                string
		load, freeze, empty bool
	}{
		{name: "checkpoint read fails", load: true},
		{name: "initial checkpoint write fails", freeze: true},
		{name: "empty selection is durably complete", empty: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated persistence failure fixture")
			t.Cleanup(mock.Close)
			orgID, jobID, token, id := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			persistenceErr := errors.New("checkpoint storage temporarily unavailable")
			var loadErr error
			if tt.load {
				loadErr = persistenceErr
			}
			expectSweepCheckpointLoad(t, mock, orgID, jobID, token, nil, loadErr)
			if !tt.load {
				expectHealthPopulation(mock, orgID, 1)
				rows := pgxmock.NewRows(prTestPullRequestColumns)
				ids := []uuid.UUID{}
				if !tt.empty {
					rows.AddRow(newPRTestRow(id, nil, orgID, "example/repository", time.Now().UTC(), nil)...)
					ids = append(ids, id)
				}
				mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE org_id").WithArgs(pgx.NamedArgs{"org_id": orgID, "before": pgxmock.AnyArg(), "limit": 50}).WillReturnRows(rows)
				var saveErr error
				if tt.freeze {
					saveErr = persistenceErr
				}
				expectSweepCheckpointSave(t, mock, orgID, jobID, token, db.PullRequestHealthSweepCheckpoint{Version: 1, RemainingPullRequestIDs: ids, Population: 1, PopulationAvailable: true, CandidateLimit: 50, SelectedCount: len(ids)}, saveErr)
			}
			expectSweepRecovery(mock, orgID, 50, nil)
			var providerCalls atomic.Int32
			service := &PRService{jobs: db.NewJobStore(mock), pullRequests: db.NewPullRequestStore(mock), logger: zerolog.Nop(), httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				providerCalls.Add(1)
				return nil, errors.New("provider must not be called before durable selection")
			})}}
			err = service.ReconcilePullRequestState(sweepCheckpointContext(jobID, token), orgID, 50)
			if tt.empty {
				require.NoError(t, err, "a persisted empty selection should complete without provider requests")
			} else {
				require.ErrorIs(t, err, persistenceErr, "checkpoint failure must be returned rather than advancing the selection")
			}
			require.Equal(t, int32(0), providerCalls.Load(), "checkpoint failures and empty selections must make no provider requests")
			require.NoError(t, mock.ExpectationsWereMet(), "checkpoint failure must stop all health reads while preserving independent recovery")
		})
	}
}

func TestReconcilePullRequestHealthSweepRejectsMissingOwnership(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name         string
		jobID, token bool
		store, lost  bool
	}{
		{name: "job without ownership token", jobID: true, store: true},
		{name: "token without job", token: true, store: true},
		{name: "worker without checkpoint store", jobID: true, token: true},
		{name: "current ownership was replaced", jobID: true, token: true, store: true, lost: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated ownership fixture")
			t.Cleanup(mock.Close)
			orgID, jobID, token := uuid.New(), uuid.New(), uuid.New()
			ctx := context.Background()
			if tt.jobID {
				ctx = jobctx.WithJobID(ctx, jobID)
			}
			if tt.token {
				ctx = jobctx.WithLockToken(ctx, token)
			}
			service := &PRService{pullRequests: db.NewPullRequestStore(mock), logger: zerolog.Nop()}
			if tt.store {
				service.jobs = db.NewJobStore(mock)
			}
			if tt.lost {
				expectSweepCheckpointLoad(t, mock, orgID, jobID, token, nil, pgx.ErrNoRows)
			}
			err = service.ReconcilePullRequestState(ctx, orgID, 50)
			require.Error(t, err, "worker health reads require a current durable ownership fence")
			if tt.lost {
				require.ErrorIs(t, err, db.ErrPullRequestHealthSweepLeaseLost, "a replaced owner must retain the precise fencing failure")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "invalid worker ownership must stop before health or recovery work")
		})
	}
}

func TestReconcilePullRequestHealthSweepRetainsAllSharedThrottleCandidates(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		saveErr error
		lost    bool
	}{
		{name: "one shared throttle keeps every pending PR"},
		{name: "failed remainder write preserves throttle", saveErr: errors.New("checkpoint storage unavailable")},
		{name: "lost ownership preserves throttle and stops recovery", saveErr: db.ErrPullRequestHealthSweepLeaseLost, lost: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated shared-throttle checkpoint fixture")
			t.Cleanup(mock.Close)
			mock.MatchExpectationsInOrder(false)
			orgID, jobID, token := uuid.New(), uuid.New(), uuid.New()
			ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
			checkpoint := db.PullRequestHealthSweepCheckpoint{Version: 1, RemainingPullRequestIDs: ids, Population: 3, PopulationAvailable: true, CandidateLimit: 50, SelectedCount: 3}
			expectSweepCheckpointLoad(t, mock, orgID, jobID, token, &checkpoint, nil)
			throttle := &ratelimit.Deferral{InstallationID: 42, Kind: ratelimit.KindSecondary, RetryAt: time.Now().Add(time.Minute), Generation: 3}
			for _, id := range ids {
				mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").WithArgs(pgx.NamedArgs{"org_id": orgID, "id": id}).WillReturnError(throttle)
			}
			expectSweepCheckpointSave(t, mock, orgID, jobID, token, checkpoint, tt.saveErr)
			if !tt.lost {
				expectSweepRecovery(mock, orgID, 50, nil)
			}
			var logs bytes.Buffer
			service := &PRService{jobs: db.NewJobStore(mock), pullRequests: db.NewPullRequestStore(mock), logger: zerolog.New(zerolog.SyncWriter(&logs))}
			err = service.ReconcilePullRequestState(sweepCheckpointContext(jobID, token), orgID, 50)
			var actual *ratelimit.Deferral
			require.ErrorAs(t, err, &actual, "shared throttle must remain available to the worker's typed retry scheduler")
			require.Same(t, throttle, actual, "checkpointing must preserve the original throttle identity and retry deadline")
			if tt.saveErr != nil {
				require.ErrorIs(t, err, tt.saveErr, "checkpoint failure must be joined without losing the provider throttle")
			}
			require.Equal(t, 1, strings.Count(err.Error(), "reconcile pull request "), "shared throttle evidence should stay deduplicated while all affected IDs remain durable")
			var summary map[string]any
			decoder := json.NewDecoder(&logs)
			for decoder.More() {
				var record map[string]any
				require.NoError(t, decoder.Decode(&record), "sweep logs should remain structured JSON")
				if record["message"] == "completed periodic pull request health sweep" {
					summary = record
				}
			}
			require.Equal(t, true, summary["health_checkpoint_available"], "a resumed checkpoint should remain explicitly durable")
			require.Equal(t, tt.saveErr == nil, summary["health_checkpoint_progress_saved"], "desired retry progress must not be reported as saved after a failed write")
			if tt.saveErr == nil {
				require.Equal(t, float64(3), summary["health_checkpoint_remaining"], "telemetry should expose the acknowledged persisted remainder")
			} else {
				_, present := summary["health_checkpoint_remaining"]
				require.False(t, present, "a failed or ambiguous write must leave the persisted remainder unknown in telemetry")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "no candidate may disappear when one throttle object is reused")
		})
	}
}

func TestPullRequestHealthSweepSelectionResumesOriginalMetadata(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create isolated resumed metadata fixture")
	t.Cleanup(mock.Close)
	orgID, jobID, token, id := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	checkpoint := db.PullRequestHealthSweepCheckpoint{Version: 1, RemainingPullRequestIDs: []uuid.UUID{id}, Population: 681, PopulationAvailable: false, CandidateLimit: 137, SelectedCount: 137}
	expectSweepCheckpointLoad(t, mock, orgID, jobID, token, &checkpoint, nil)
	service := &PRService{jobs: db.NewJobStore(mock)}
	selection, err := service.pullRequestHealthSweepSelection(sweepCheckpointContext(jobID, token), orgID, 50)
	require.NoError(t, err, "resuming a saved candidate remainder should not require fresh population or stale queries")
	require.Equal(t, []models.PullRequest{{ID: id}}, selection.candidates, "resumed candidates must come exclusively from the saved PR identities")
	require.True(t, selection.resumed, "summary must identify a retry's frozen metadata")
	require.Equal(t, 681, selection.population, "saved population must remain the original selection's measurement")
	require.False(t, selection.populationAvailable, "retry must not relabel a saved unavailable population as a fresh count")
	require.Equal(t, 137, selection.initialSelected, "original selection size must stay distinct from this attempt's one remaining candidate")
	require.NoError(t, mock.ExpectationsWereMet(), "resumption must only load its fenced checkpoint")
}

func TestReconcilePullRequestHealthSweepCancellationPreservesCheckpoint(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create isolated canceled-attempt fixture")
	t.Cleanup(mock.Close)
	orgID, jobID, token := uuid.New(), uuid.New(), uuid.New()
	checkpoint := db.PullRequestHealthSweepCheckpoint{
		Version: 1, RemainingPullRequestIDs: []uuid.UUID{uuid.New(), uuid.New()},
		Population: 2, PopulationAvailable: true, CandidateLimit: 50, SelectedCount: 2,
	}
	expectSweepCheckpointLoad(t, mock, orgID, jobID, token, &checkpoint, nil)
	expectSweepRecovery(mock, orgID, 50, context.Canceled)
	ctx, cancel := context.WithCancel(sweepCheckpointContext(jobID, token))
	cancel()
	service := &PRService{jobs: db.NewJobStore(mock), pullRequests: db.NewPullRequestStore(mock), logger: zerolog.Nop()}
	err = service.ReconcilePullRequestState(ctx, orgID, 50)
	require.ErrorIs(t, err, context.Canceled, "caller cancellation must remain visible to the worker")
	require.NoError(t, mock.ExpectationsWereMet(), "canceled unstarted work must not clear or advance its saved checkpoint")
}
