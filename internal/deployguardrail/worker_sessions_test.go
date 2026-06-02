package deployguardrail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLoadWorkerSessionCounts(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("WITH active_session_jobs AS").
		WillReturnRows(pgxmock.NewRows([]string{
			"worker_owned_active_long_jobs",
			"executor_owned_active_sessions",
			"worker_owned_no_checkpoint",
			"draining_session_executor_count",
		}).AddRow(int64(3), int64(2), int64(1), int64(4)))

	counts, err := LoadWorkerSessionCounts(context.Background(), mock)
	require.NoError(t, err, "LoadWorkerSessionCounts should load the deploy guardrail snapshot")
	require.Equal(t, WorkerSessionCounts{
		WorkerOwnedActiveLongJobs:    3,
		ExecutorOwnedActiveSessions:  2,
		WorkerOwnedNoCheckpoint:      1,
		DrainingSessionExecutorCount: 4,
	}, counts, "LoadWorkerSessionCounts should return exact active session counts")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestLoadWorkerSessionCounts_SchemaUnavailable(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("WITH active_session_jobs AS").
		WillReturnError(&pgconn.PgError{Code: "42703", Message: "column owner_kind does not exist"})

	_, err = LoadWorkerSessionCounts(context.Background(), mock)
	require.ErrorIs(t, err, ErrGuardrailSchemaUnavailable, "LoadWorkerSessionCounts should expose a typed error before migrations are applied")
	require.True(t, errors.Is(err, ErrGuardrailSchemaUnavailable), "schema error should be detectable through errors.Is")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIsDatabaseConnectionSaturated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		saturated bool
	}{
		{
			name:      "detects wrapped too many clients pg error",
			err:       fmt.Errorf("ping database: %w", &pgconn.PgError{Code: "53300", Message: "sorry, too many clients already"}),
			saturated: true,
		},
		{
			name: "ignores unrelated pg errors",
			err:  &pgconn.PgError{Code: "08006", Message: "connection failure"},
		},
		{
			name: "ignores nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.saturated, IsDatabaseConnectionSaturated(tt.err), "IsDatabaseConnectionSaturated should only match Postgres resource exhaustion")
		})
	}
}

func TestWorkerSessionCountsQueryGuardsMalformedSessionIDs(t *testing.T) {
	t.Parallel()

	require.Contains(t, workerSessionCountsQuery, "~*", "guardrail query should validate session_id before uuid casting")
	require.Contains(t, strings.ToLower(workerSessionCountsQuery), "else null", "guardrail query should avoid casting malformed session_id payloads")
}

func TestEvaluateWorkerSessionDeploy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		counts  WorkerSessionCounts
		force   bool
		blocked bool
	}{
		{
			name:    "blocks active inline no-checkpoint sessions by default",
			counts:  WorkerSessionCounts{WorkerOwnedNoCheckpoint: 1},
			blocked: true,
		},
		{
			name:   "force permits active inline no-checkpoint sessions",
			counts: WorkerSessionCounts{WorkerOwnedNoCheckpoint: 1},
			force:  true,
		},
		{
			name:   "executor owned sessions do not block deploy",
			counts: WorkerSessionCounts{ExecutorOwnedActiveSessions: 10},
		},
		{
			name:   "worker-owned checkpointed sessions warn but do not block",
			counts: WorkerSessionCounts{WorkerOwnedActiveLongJobs: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decision := EvaluateWorkerSessionDeploy(tt.counts, tt.force)
			require.Equal(t, tt.blocked, decision.Blocked, "EvaluateWorkerSessionDeploy should enforce only no-checkpoint inline sessions")
		})
	}
}
