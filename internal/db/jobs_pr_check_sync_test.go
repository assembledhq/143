package db

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestEnqueuePullRequestCheckSync(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		status           models.JobStatus
		awaiting         bool
		pendingSuccessor bool
		wantInsert       bool
	}{
		{name: "first request establishes deadline", wantInsert: true},
		{name: "pending job retains first deadline", status: models.JobStatusPending},
		{name: "running job receives one successor", status: models.JobStatusRunning, wantInsert: true},
		{name: "pending successor absorbs reopened base", status: models.JobStatusRunning, pendingSuccessor: true},
		{name: "claimed successor still waiting absorbs event", status: models.JobStatusRunning, awaiting: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated enqueue fixture")
			defer mock.Close()
			orgID, prID, activeID, newID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			key := "sync_pull_request_state:" + prID.String() + ":checks"
			runAt := time.Date(2026, 1, 2, 10, 0, 15, 0, time.UTC)
			payload := map[string]string{"org_id": orgID.String(), "pull_request_id": prID.String(), "sync_reason": "incomplete_check_set"}
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT id FROM pull_requests.*WHERE org_id = @org_id AND id = @pull_request_id FOR UPDATE").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
				WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(prID))
			rows := pgxmock.NewRows([]string{"id", "status", "awaiting_predecessor"})
			if tt.pendingSuccessor {
				rows.AddRow(uuid.New(), models.JobStatusPending, true)
			}
			if tt.status != "" {
				rows.AddRow(activeID, tt.status, tt.awaiting)
			}
			mock.ExpectQuery("SELECT j.id, j.status,.*predecessor.org_id = @org_id.*j.org_id = @org_id.*FOR UPDATE OF j").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "queue": "default", "job_type": "sync_pull_request_state", "dedupe_key": key, "successor_prefix": key + ":after:"}).
				WillReturnRows(rows)
			wantID := uuid.Nil
			if tt.wantInsert {
				wantID = newID
				insertKey := key
				insertPayload := map[string]string{"org_id": orgID.String(), "pull_request_id": prID.String(), "sync_reason": "incomplete_check_set"}
				if tt.status == models.JobStatusRunning {
					insertKey += ":after:" + activeID.String()
					insertPayload["after_job_id"] = activeID.String()
				}
				payloadJSON, err := json.Marshal(insertPayload)
				require.NoError(t, err, "encode full expected successor payload")
				mock.ExpectQuery("INSERT INTO jobs").WithArgs(pgx.NamedArgs{
					"org_id": orgID, "queue": "default", "job_type": "sync_pull_request_state", "payload": payloadJSON,
					"priority": 6, "dedupe_key": &insertKey, "run_at": runAt,
				}).WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(newID))
			}
			mock.ExpectCommit()
			got, err := NewJobStore(mock).EnqueuePullRequestCheckSync(context.Background(), orgID, prID, EnqueueOpts{
				Queue: "default", JobType: "sync_pull_request_state", Payload: payload, Priority: 6, DedupeKey: &key, RunAt: &runAt,
			})
			require.NoError(t, err, "serialize check requests under the pull request lock")
			require.Equal(t, wantID, got, "only uncovered events should create durable work")
			require.Equal(t, map[string]string{"org_id": orgID.String(), "pull_request_id": prID.String(), "sync_reason": "incomplete_check_set"}, payload, "successor fields must not mutate the caller payload")
			require.NoError(t, mock.ExpectationsWereMet(), "scope all queue decisions by tenant and retain existing deadlines")
		})
	}
}

func TestGetPullRequestCheckSyncPredecessor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name        string
		predecessor *PullRequestCheckSyncPredecessor
		queryErr    error
	}{
		{name: "running predecessor", predecessor: &PullRequestCheckSyncPredecessor{Status: models.JobStatusRunning, RunAt: now, CreatedAt: now.Add(-time.Minute)}},
		{name: "pending cooldown", predecessor: &PullRequestCheckSyncPredecessor{Status: models.JobStatusPending, RunAt: now.Add(time.Hour), CreatedAt: now.Add(-time.Hour)}},
		{name: "terminal predecessor"},
		{name: "database failure", queryErr: fmt.Errorf("unavailable")},
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
				if tt.predecessor != nil {
					rows.AddRow(tt.predecessor.Status, tt.predecessor.RunAt, tt.predecessor.CreatedAt)
				}
				query.WillReturnRows(rows)
			}
			predecessor, err := NewJobStore(mock).GetPullRequestCheckSyncPredecessor(context.Background(), orgID, prID, predecessorID)
			if tt.queryErr != nil {
				require.ErrorIs(t, err, tt.queryErr, "database failures cannot permit an overlapping read")
			} else {
				require.NoError(t, err, "read predecessor scoped to tenant and pull request")
				require.Equal(t, tt.predecessor, predecessor, "return durable predecessor status and scheduling timestamps")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "predecessor lookup must include tenant and pull request identity")
		})
	}
}

func TestEnqueuePullRequestCheckSyncRequiresOwnedPullRequest(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create isolated ownership fixture")
	defer mock.Close()
	orgID, prID := uuid.New(), uuid.New()
	key := "sync_pull_request_state:" + prID.String() + ":checks"
	runAt := time.Date(2026, 1, 2, 10, 0, 15, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM pull_requests.*WHERE org_id = @org_id AND id = @pull_request_id FOR UPDATE").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
		WillReturnRows(pgxmock.NewRows([]string{"id"}))
	mock.ExpectRollback()
	_, err = NewJobStore(mock).EnqueuePullRequestCheckSync(context.Background(), orgID, prID, EnqueueOpts{
		Queue: "default", JobType: "sync_pull_request_state", Payload: map[string]string{"pull_request_id": prID.String()}, DedupeKey: &key, RunAt: &runAt,
	})
	require.ErrorIs(t, err, pgx.ErrNoRows, "a foreign or missing pull request cannot schedule tenant work")
	require.NoError(t, mock.ExpectationsWereMet(), "ownership failure must stop before reading or inserting jobs")
}
