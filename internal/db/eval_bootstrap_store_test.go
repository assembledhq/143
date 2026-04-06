package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestEvalBootstrapStore_Create(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	newID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("INSERT INTO eval_bootstrap_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(newID, now))

	store := NewEvalBootstrapStore(mock)
	run := &models.EvalBootstrapRun{
		OrgID:     orgID,
		RepoID:    repoID,
		Status:    models.EvalBootstrapStatusPending,
		CreatedBy: &userID,
	}

	err = store.Create(context.Background(), run)
	require.NoError(t, err)
	require.Equal(t, newID, run.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvalBootstrapStore_GetByID(t *testing.T) {
	t.Parallel()

	t.Run("found", func(t *testing.T) {
		orgID := uuid.New()
		runID := uuid.New()
		repoID := uuid.New()
		now := time.Now()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "org_id", "repo_id", "status", "candidates", "session_id",
				"created_by", "created_at", "completed_at", "error_message",
			}).AddRow(runID, orgID, repoID, "completed", []byte(`[]`), nil, nil, now, nil, nil))

		store := NewEvalBootstrapStore(mock)
		run, err := store.GetByID(context.Background(), orgID, runID)
		require.NoError(t, err)
		require.Equal(t, runID, run.ID)
		require.Equal(t, models.EvalBootstrapStatus("completed"), run.Status)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("not found", func(t *testing.T) {
		orgID := uuid.New()
		runID := uuid.New()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery("SELECT .+ FROM eval_bootstrap_runs WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{
				"id", "org_id", "repo_id", "status", "candidates", "session_id",
				"created_by", "created_at", "completed_at", "error_message",
			}))

		store := NewEvalBootstrapStore(mock)
		_, err = store.GetByID(context.Background(), orgID, runID)
		require.Error(t, err)
	})
}

func TestEvalBootstrapStore_UpdateStatus(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectExec("UPDATE eval_bootstrap_runs SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewEvalBootstrapStore(mock)
	err = store.UpdateStatus(context.Background(), orgID, runID, models.EvalBootstrapStatusRunning, nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEvalBootstrapStore_UpdateResult(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectExec("UPDATE eval_bootstrap_runs").
		WithArgs(anyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	store := NewEvalBootstrapStore(mock)
	err = store.UpdateResult(context.Background(), orgID, runID,
		models.EvalBootstrapStatusCompleted, []byte(`[{"pr_number":1}]`), nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
