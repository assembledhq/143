package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestCheckSyncHandlerWaitsForPredecessor(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		active   bool
		queryErr error
		wantSync int
	}{
		{name: "running predecessor defers without attempt", active: true},
		{name: "terminal predecessor permits fresh sync", wantSync: 1},
		{name: "lookup failure cannot permit sync", queryErr: fmt.Errorf("database unavailable")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated predecessor fixture")
			defer mock.Close()
			orgID, prID, predecessorID := uuid.New(), uuid.New(), uuid.New()
			query := mock.ExpectQuery("SELECT status, run_at, created_at FROM jobs.*WHERE org_id = @org_id AND id = @predecessor_id.*payload->>'pull_request_id' = @pull_request_id").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID.String(), "predecessor_id": predecessorID})
			if tt.queryErr != nil {
				query.WillReturnError(tt.queryErr)
			} else {
				rows := pgxmock.NewRows([]string{"status", "run_at", "created_at"})
				if tt.active {
					now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
					rows.AddRow(models.JobStatusRunning, now, now)
				}
				query.WillReturnRows(rows)
			}
			syncCalls := 0
			services := &Services{PR: &stubPRService{syncPullRequestStateFn: func(ctx context.Context, gotOrgID, gotPRID uuid.UUID) error {
				require.Equal(t, orgID, gotOrgID, "successor must retain tenant identity")
				require.Equal(t, prID, gotPRID, "successor must retain pull request identity")
				require.Equal(t, ghservice.PullRequestSyncReasonIncomplete, ghservice.PullRequestSyncReasonFromContext(ctx), "successor must retain check attribution")
				syncCalls++
				return nil
			}}}
			payload, err := json.Marshal(map[string]string{"org_id": orgID.String(), "pull_request_id": prID.String(), "sync_reason": "incomplete_check_set", "after_job_id": predecessorID.String()})
			require.NoError(t, err, "encode successor payload")
			err = newSyncPullRequestStateHandler(&Stores{Jobs: db.NewJobStore(mock)}, services, zerolog.Nop())(context.Background(), "sync_pull_request_state", payload)
			if tt.active {
				var retry *RetryableError
				require.ErrorAs(t, err, &retry, "active predecessor must defer overlapping GitHub reads")
				require.False(t, retry.ConsumeAttempt, "waiting for predecessor must preserve the attempt budget")
				require.True(t, retry.BypassMaxRetryDuration, "internal dependency wait must survive a predecessor cooldown longer than the ordinary retry budget")
				require.Equal(t, new(5*time.Second), retry.RetryAfter, "waiting successor should poll at a bounded interval")
			} else if tt.queryErr != nil {
				require.ErrorIs(t, err, tt.queryErr, "failed predecessor lookup cannot authorize a read")
			} else {
				require.NoError(t, err, "terminal predecessor permits authoritative read")
			}
			require.Equal(t, tt.wantSync, syncCalls, "only terminal predecessor allows successor execution")
			require.NoError(t, mock.ExpectationsWereMet(), "gate must check the exact predecessor within the tenant")
		})
	}
}

func TestCheckSyncHandlerRejectsInvalidPredecessor(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, reason, predecessor string
		stores                    bool
	}{
		{name: "urgent lane never waits", reason: "head_changed", predecessor: uuid.NewString(), stores: true},
		{name: "missing store fails closed", reason: "incomplete_check_set", predecessor: uuid.NewString()},
		{name: "invalid predecessor fails closed", reason: "incomplete_check_set", predecessor: "invalid", stores: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stores *Stores
			if tt.stores {
				stores = &Stores{Jobs: db.NewJobStore(nil)}
			}
			payload, err := json.Marshal(map[string]string{"org_id": uuid.NewString(), "pull_request_id": uuid.NewString(), "sync_reason": tt.reason, "after_job_id": tt.predecessor})
			require.NoError(t, err, "encode invalid dependency fixture")
			err = newSyncPullRequestStateHandler(stores, &Services{}, zerolog.Nop())(context.Background(), "sync_pull_request_state", payload)
			require.Error(t, err, "invalid dependency cannot reach the GitHub reader")
		})
	}
}

func TestCheckSyncPredecessorPollDelay(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name     string
		status   models.JobStatus
		due, age time.Duration
		want     time.Duration
	}{
		{name: "scheduled cooldown", status: models.JobStatusPending, due: 70 * time.Second, age: time.Hour, want: 70 * time.Second},
		{name: "long cooldown bounded", status: models.JobStatusPending, due: 2 * time.Hour, age: time.Hour, want: 2 * time.Minute},
		{name: "near deadline avoids hot polling", status: models.JobStatusPending, due: time.Second, want: 5 * time.Second},
		{name: "new running read", status: models.JobStatusRunning, want: 5 * time.Second},
		{name: "running read stays responsive", status: models.JobStatusRunning, age: 15 * time.Second, want: 5 * time.Second},
		{name: "old running read stays responsive", status: models.JobStatusRunning, age: 2 * time.Hour, want: 5 * time.Second},
		{name: "overdue pending job backs off", status: models.JobStatusPending, due: -time.Minute, age: time.Minute, want: 40 * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			predecessor := db.PullRequestCheckSyncPredecessor{Status: tt.status, RunAt: now.Add(tt.due), CreatedAt: now.Add(-tt.age)}
			require.Equal(t, tt.want, checkSyncPredecessorPollDelay(predecessor, now), "dependency polling follows durable scheduling while staying bounded")
		})
	}
}

func TestCheckSyncDependencyWaitPreservesGitHubRetryWindow(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create isolated dependency and provider fixture")
	defer mock.Close()
	orgID, prID, predecessorID, lockToken := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	queryArgs := pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID.String(), "predecessor_id": predecessorID}
	mock.ExpectQuery("SELECT status, run_at, created_at FROM jobs").WithArgs(queryArgs).
		WillReturnRows(pgxmock.NewRows([]string{"status", "run_at", "created_at"}).AddRow(models.JobStatusPending, now.Add(time.Hour), now.Add(-time.Hour)))
	mock.ExpectQuery("SELECT status, run_at, created_at FROM jobs").WithArgs(queryArgs).
		WillReturnRows(pgxmock.NewRows([]string{"status", "run_at", "created_at"}))
	services := &Services{PR: &stubPRService{syncPullRequestStateFn: func(context.Context, uuid.UUID, uuid.UUID) error {
		return &ghservice.GitHubAPIError{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"120"}}}
	}}}
	payload, err := json.Marshal(map[string]string{"org_id": orgID.String(), "pull_request_id": prID.String(), "sync_reason": "incomplete_check_set", "after_job_id": predecessorID.String()})
	require.NoError(t, err, "encode dependency fixture")
	handler := newSyncPullRequestStateHandler(&Stores{Jobs: db.NewJobStore(mock)}, services, zerolog.Nop())
	job := &models.Job{ID: uuid.New(), LockToken: &lockToken, CreatedAt: now.Add(-3 * time.Hour)}
	windowStore := &retryWindowLeaseStoreStub{startedAt: now, ok: true}
	var dependencyRetry *RetryableError
	require.ErrorAs(t, handler(context.Background(), "sync_pull_request_state", payload), &dependencyRetry, "active dependency must defer execution")
	_, owned, err := ensureRetryWindowStartedAt(context.Background(), windowStore, job, dependencyRetry, now.Add(-time.Hour))
	require.NoError(t, err, "dependency wait must remain independently retryable")
	require.True(t, owned, "dependency wait retains its worker lease")
	require.Nil(t, job.RetryWindowStartedAt, "dependency waiting cannot consume the later provider recovery window")
	require.Equal(t, 0, windowStore.calls, "dependency wait must not establish a provider retry episode")
	var providerRetry *RetryableError
	require.ErrorAs(t, handler(context.Background(), "sync_pull_request_state", payload), &providerRetry, "provider throttle must use the GitHub retry policy")
	require.False(t, providerRetry.BypassMaxRetryDuration, "actual provider requests retain a bounded terminal recovery window")
	require.False(t, providerRetry.ConsumeAttempt, "provider cooldown preserves attempts")
	require.Equal(t, new(githubRateLimitMaxRetryDuration), providerRetry.MaxRetryDuration, "provider recovery starts with its full normal budget")
	startedAt, owned, err := ensureRetryWindowStartedAt(context.Background(), windowStore, job, providerRetry, now)
	require.NoError(t, err, "first provider failure must start its own durable retry window")
	require.True(t, owned, "provider retry retains its worker lease")
	require.Equal(t, now, startedAt, "provider window starts after the dependency completed")
	require.Equal(t, new(now), job.RetryWindowStartedAt, "provider episode cannot inherit job age or dependency waiting time")
	require.Equal(t, 1, windowStore.calls, "provider recovery initializes its durable window exactly once")
	require.NoError(t, mock.ExpectationsWereMet(), "handler must inspect only the expected dependency states")
}
