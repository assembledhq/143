package db

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var evalRunTestColumns = []string{
	"id", "task_id", "org_id", "batch_id",
	"session_id", "thread_id",
	"input_manifest", "model", "server_deploy_sha", "pm_document_set_pin_id",
	"config_ref", "context_overrides",
	"agent_diff", "agent_trace", "token_usage",
	"criterion_results", "final_score", "passed",
	"status", "duration_seconds", "sandbox_id",
	"started_at", "completed_at", "error_message", "created_at",
}

func newEvalRunRow(runID, taskID, orgID uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		runID, taskID, orgID, nil,
		nil, nil,
		nil, "claude-sonnet-4-6", nil, nil,
		nil, json.RawMessage(`{}`),
		nil, nil, nil,
		nil, nil, nil,
		"pending", nil, nil,
		nil, nil, nil, now,
	}
}

func newSessionBackedEvalRunRow(runID, taskID, orgID, sessionID, threadID uuid.UUID, now time.Time) []interface{} {
	row := newEvalRunRow(runID, taskID, orgID, now)
	row[4] = &sessionID
	row[5] = &threadID
	return row
}

func TestEvalRunStore_Create(t *testing.T) {
	t.Parallel()

	runID := uuid.New()
	taskID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("INSERT INTO eval_runs").
		WithArgs(anyArgs(9)...).
		WillReturnRows(pgxmock.NewRows(evalRunTestColumns).AddRow(newEvalRunRow(runID, taskID, orgID, now)...))

	store := NewEvalRunStore(mock)
	run := &models.EvalRun{
		TaskID:           taskID,
		OrgID:            orgID,
		Model:            "claude-sonnet-4-6",
		ContextOverrides: json.RawMessage(`{}`),
	}

	err = store.Create(context.Background(), run)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, runID, run.ID, "Create should set the run ID")
	require.Equal(t, models.EvalRunStatusPending, run.Status, "new run should be pending")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestEvalRunStore_CreateWithSessionThread(t *testing.T) {
	t.Parallel()

	runID := uuid.New()
	taskID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("INSERT INTO eval_runs").
		WithArgs(anyArgs(11)...).
		WillReturnRows(pgxmock.NewRows(evalRunTestColumns).AddRow(newSessionBackedEvalRunRow(runID, taskID, orgID, sessionID, threadID, now)...))

	store := NewEvalRunStore(mock)
	run := &models.EvalRun{
		TaskID:           taskID,
		OrgID:            orgID,
		SessionID:        &sessionID,
		ThreadID:         &threadID,
		Model:            "codex",
		ContextOverrides: json.RawMessage(`{}`),
	}

	err = store.Create(context.Background(), run)
	require.NoError(t, err, "Create should insert a session-backed eval run")
	require.Equal(t, sessionID, *run.SessionID, "Create should preserve the linked session ID")
	require.Equal(t, threadID, *run.ThreadID, "Create should preserve the linked thread ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestEvalRunStore_GetBySessionID(t *testing.T) {
	t.Parallel()

	runID := uuid.New()
	taskID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM eval_runs WHERE org_id = @org_id AND session_id = @session_id").
		WithArgs(anyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(evalRunTestColumns).AddRow(newSessionBackedEvalRunRow(runID, taskID, orgID, sessionID, uuid.New(), now)...))

	store := NewEvalRunStore(mock)
	run, err := store.GetBySessionID(context.Background(), orgID, sessionID)
	require.NoError(t, err, "GetBySessionID should return the eval run linked to the session")
	require.Equal(t, runID, run.ID, "GetBySessionID should scan the expected eval run")
	require.Equal(t, sessionID, *run.SessionID, "GetBySessionID should preserve the lookup session ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestEvalRunStore_GetByID(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()
	taskID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns run when found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM eval_runs WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(evalRunTestColumns).AddRow(newEvalRunRow(runID, taskID, orgID, now)...))
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM eval_runs WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(evalRunTestColumns))
			},
			expectErr: true,
		},
		{
			name: "returns error on db failure",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM eval_runs WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection closed"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			tt.setupMock(mock)

			store := NewEvalRunStore(mock)
			run, err := store.GetByID(context.Background(), orgID, runID)

			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error")
			} else {
				require.NoError(t, err, "GetByID should not return an error")
				require.Equal(t, runID, run.ID, "should return the correct run")
				require.Equal(t, orgID, run.OrgID, "should belong to the correct org")
			}

			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestEvalRunStore_ListByTask(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	taskID := uuid.New()
	runID1 := uuid.New()
	runID2 := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM eval_runs").
		WithArgs(anyArgs(3)...).
		WillReturnRows(pgxmock.NewRows(evalRunTestColumns).
			AddRow(newEvalRunRow(runID1, taskID, orgID, now)...).
			AddRow(newEvalRunRow(runID2, taskID, orgID, now)...))

	store := NewEvalRunStore(mock)
	runs, err := store.ListByTask(context.Background(), orgID, taskID, 50)
	require.NoError(t, err, "ListByTask should not return an error")
	require.Len(t, runs, 2, "should return both runs for task")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestEvalRunStore_UpdateResult(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectExec("UPDATE eval_runs SET").
		WithArgs(anyArgs(13)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewEvalRunStore(mock)
	score := 0.85
	passed := true
	duration := 120
	result := &models.EvalRunResult{
		Status:           models.EvalRunStatusCompleted,
		FinalScore:       &score,
		Passed:           &passed,
		DurationSeconds:  &duration,
		CriterionResults: json.RawMessage(`[{"name":"tests_pass","score":1.0,"pass":true}]`),
	}

	err = store.UpdateResult(context.Background(), orgID, runID, result)
	require.NoError(t, err, "UpdateResult should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestEvalRunStore_UpdatePostSessionArtifacts(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()
	diff := "diff --git a/app.go b/app.go"
	trace := json.RawMessage(`{"session_id":"s1"}`)
	manifest := json.RawMessage(`{"base_commit_sha":"abc123"}`)

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectExec("UPDATE eval_runs SET").
		WithArgs(models.EvalRunStatusGrading, &diff, trace, manifest, runID, orgID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewEvalRunStore(mock)
	err = store.UpdatePostSessionArtifacts(context.Background(), orgID, runID, &diff, trace, manifest)
	require.NoError(t, err, "UpdatePostSessionArtifacts should move the run into grading with session artifacts")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestEvalRunStore_ListByBatch(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	batchID := uuid.New()
	runID := uuid.New()
	taskID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM eval_runs WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(evalRunTestColumns).AddRow(newEvalRunRow(runID, taskID, orgID, now)...))

	store := NewEvalRunStore(mock)
	runs, err := store.ListByBatch(context.Background(), orgID, batchID)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, runID, runs[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvalRunStore_UpdateStatus(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectExec("UPDATE eval_runs SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewEvalRunStore(mock)
	err = store.UpdateStatus(context.Background(), orgID, runID, models.EvalRunStatusRunning)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvalBatchStore_Create(t *testing.T) {
	t.Parallel()

	batchID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	batchColumns := []string{"id", "org_id", "name", "status", "task_count", "run_count", "created_by", "created_at", "completed_at"}
	mock.ExpectQuery("INSERT INTO eval_batches").
		WithArgs(anyArgs(6)...).
		WillReturnRows(pgxmock.NewRows(batchColumns).AddRow(
			batchID, orgID, "Compare models", "pending", 5, 10, nil, now, nil,
		))

	store := NewEvalBatchStore(mock)
	batch := &models.EvalBatch{
		OrgID:     orgID,
		Name:      "Compare models",
		Status:    models.EvalBatchStatusPending,
		TaskCount: 5,
		RunCount:  10,
	}

	err = store.Create(context.Background(), batch)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, batchID, batch.ID, "Create should set the batch ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestEvalBatchStore_GetByID(t *testing.T) {
	t.Parallel()

	batchID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	batchColumns := []string{"id", "org_id", "name", "status", "task_count", "run_count", "created_by", "created_at", "completed_at"}
	mock.ExpectQuery("SELECT .+ FROM eval_batches WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(batchColumns).AddRow(
			batchID, orgID, "Test Batch", "pending", 2, 4, nil, now, nil,
		))

	store := NewEvalBatchStore(mock)
	batch, err := store.GetByID(context.Background(), orgID, batchID)
	require.NoError(t, err)
	require.Equal(t, batchID, batch.ID)
	require.Equal(t, "Test Batch", batch.Name)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvalBatchStore_ListByOrg(t *testing.T) {
	t.Parallel()

	batchID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	batchColumns := []string{"id", "org_id", "name", "status", "task_count", "run_count", "created_by", "created_at", "completed_at"}
	mock.ExpectQuery("SELECT .+ FROM eval_batches WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(batchColumns).AddRow(
			batchID, orgID, "Batch 1", "completed", 3, 6, nil, now, &now,
		))

	store := NewEvalBatchStore(mock)
	batches, err := store.ListByOrg(context.Background(), orgID, 20)
	require.NoError(t, err)
	require.Len(t, batches, 1)
	require.Equal(t, batchID, batches[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvalBatchStore_CompleteBatchIfDone(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	batchID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectExec("status IN \\('pending', 'running', 'grading'\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewEvalBatchStore(mock)
	err = store.CompleteBatchIfDone(context.Background(), orgID, batchID)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvalBatchStore_UpdateStatus(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	batchID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectExec("UPDATE eval_batches SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewEvalBatchStore(mock)
	err = store.UpdateStatus(context.Background(), orgID, batchID, models.EvalBatchStatusRunning)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
