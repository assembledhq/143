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
		nil, "claude-sonnet-4-6", nil, nil,
		nil, json.RawMessage(`{}`),
		nil, nil, nil,
		nil, nil, nil,
		"pending", nil, nil,
		nil, nil, nil, now,
	}
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
		WithArgs(anyArgs(12)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewEvalRunStore(mock)
	score := 0.85
	passed := true
	duration := 120
	result := &models.EvalRunResult{
		Status:     models.EvalRunStatusCompleted,
		FinalScore: &score,
		Passed:     &passed,
		DurationSeconds: &duration,
		CriterionResults: json.RawMessage(`[{"name":"tests_pass","score":1.0,"pass":true}]`),
	}

	err = store.UpdateResult(context.Background(), orgID, runID, result)
	require.NoError(t, err, "UpdateResult should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
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
