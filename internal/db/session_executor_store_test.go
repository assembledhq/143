package db

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestSessionExecutorStore_CreateStarting(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionExecutorStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	executorID := uuid.New()
	deadline := time.Now().Add(90 * time.Minute).UTC()

	mock.ExpectQuery("INSERT INTO session_executors").
		WithArgs(orgID, sessionID, (*uuid.UUID)(nil), jobID, "run_agent", "worker-1", "worker-1", lockToken, "143:sha", "build-sha", &deadline).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(executorID))

	id, err := store.CreateStarting(context.Background(), orgID, models.CreateSessionExecutorParams{
		SessionID:         sessionID,
		JobID:             jobID,
		JobType:           "run_agent",
		HostNodeID:        "worker-1",
		OwnerID:           "worker-1",
		LockToken:         lockToken,
		Image:             "143:sha",
		BuildSHA:          "build-sha",
		RuntimeDeadlineAt: &deadline,
	})
	require.NoError(t, err, "CreateStarting should insert an org-scoped executor row")
	require.Equal(t, executorID, id, "CreateStarting should return the inserted executor id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionExecutorStore_RecordContainerIDWithLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionExecutorStore(mock)
	orgID := uuid.New()
	executorID := uuid.New()
	lockToken := uuid.New()

	mock.ExpectExec("UPDATE session_executors").
		WithArgs(orgID, executorID, lockToken, "container-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	ok, err := store.RecordContainerIDWithLease(context.Background(), orgID, executorID, lockToken, "container-1")

	require.NoError(t, err, "RecordContainerIDWithLease should persist the Docker container id")
	require.True(t, ok, "RecordContainerIDWithLease should report a successful fenced update")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionExecutorStore_ClearPreHandoffReservation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionExecutorStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()

	mock.ExpectExec("UPDATE session_executors se").
		WithArgs(orgID, sessionID, jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	cleared, err := store.ClearPreHandoffReservation(context.Background(), orgID, sessionID, jobID)
	require.NoError(t, err, "ClearPreHandoffReservation should clear worker-owned pre-handoff executor rows")
	require.Equal(t, int64(1), cleared, "ClearPreHandoffReservation should report cleared executor rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionExecutorStore_ClearPreHandoffReservationOnlyTargetsStartingRows(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionExecutorStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()

	mock.ExpectExec("UPDATE session_executors se[\\s\\S]+AND se.status = 'starting'[\\s\\S]+AND j.owner_kind = 'worker'").
		WithArgs(orgID, sessionID, jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	cleared, err := store.ClearPreHandoffReservation(context.Background(), orgID, sessionID, jobID)
	require.NoError(t, err, "ClearPreHandoffReservation should clear only pre-handoff starting rows")
	require.Equal(t, int64(1), cleared, "ClearPreHandoffReservation should report cleared starting rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_HandoffToSessionExecutorWithLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	jobID := uuid.New()
	orgID := uuid.New()
	lockToken := uuid.New()
	executorID := uuid.New()

	mock.ExpectExec("UPDATE jobs\\s+SET owner_kind = 'session_executor'").
		WithArgs(executorID.String(), orgID, jobID, lockToken).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	ok, err := store.HandoffToSessionExecutorWithLease(context.Background(), orgID, jobID, lockToken, executorID)
	require.NoError(t, err, "HandoffToSessionExecutorWithLease should update the job owner")
	require.True(t, ok, "HandoffToSessionExecutorWithLease should report that the fenced update landed")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_RenewLeaseForSessionExecutorFencesOwner(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	jobID := uuid.New()
	orgID := uuid.New()
	lockToken := uuid.New()
	executorID := uuid.New()
	leaseExpiresAt := time.Now().Add(time.Minute)

	mock.ExpectQuery("UPDATE jobs\\s+SET lease_expires_at").
		WithArgs(pgx.NamedArgs{
			"lease_seconds": int(time.Minute.Seconds()),
			"org_id":        orgID,
			"job_id":        jobID,
			"lock_token":    lockToken,
			"executor_id":   executorID.String(),
		}).
		WillReturnRows(pgxmock.NewRows([]string{"lease_expires_at"}).AddRow(leaseExpiresAt))

	job, ok, err := store.RenewLeaseForSessionExecutor(context.Background(), orgID, jobID, lockToken, executorID, time.Minute)
	require.NoError(t, err, "RenewLeaseForSessionExecutor should not return an error")
	require.True(t, ok, "RenewLeaseForSessionExecutor should report that the fenced renewal landed")
	require.Equal(t, leaseExpiresAt, *job.LeaseExpiresAt, "RenewLeaseForSessionExecutor should return the new lease expiry")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_RenewLeaseForSessionExecutor_GuardsTerminalSessionJobs(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("jobs.go")
	require.NoError(t, err, "test should read jobs.go")

	sql := string(body)
	require.Contains(t, sql, "func (s *JobStore) RenewLeaseForSessionExecutor", "test should inspect the executor-owned renew path")
	require.Contains(t, sql, "s.status NOT IN ('completed', 'failed', 'cancelled', 'skipped')", "RenewLeaseForSessionExecutor should refuse renewal when the referenced session is terminal")
	require.Contains(t, sql, "s.org_id = jobs.org_id", "RenewLeaseForSessionExecutor should scope the terminal-session guard to the job org")
	require.Contains(t, sql, "terminalizeIfReferencedSessionTerminal", "RenewLeaseForSessionExecutor should terminalize terminal-session jobs after fenced lease loss")
}

func TestJobStore_RenewLeaseForSessionExecutor_TerminalizesTerminalSessionJob(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	orgID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	executorID := uuid.New()

	mock.ExpectQuery("UPDATE jobs\\s+SET lease_expires_at").
		WithArgs(pgx.NamedArgs{
			"lease_seconds": int(time.Minute.Seconds()),
			"org_id":        orgID,
			"job_id":        jobID,
			"lock_token":    lockToken,
			"executor_id":   executorID.String(),
		}).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery("WITH target AS[\\s\\S]*UPDATE session_executors[\\s\\S]*UPDATE jobs[\\s\\S]*owner_kind = 'worker'").
		WithArgs(jobID, lockToken, "referenced session is already terminal; stopping session job lease renewal").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(1)))

	job, ok, err := store.RenewLeaseForSessionExecutor(context.Background(), orgID, jobID, lockToken, executorID, time.Minute)
	require.NoError(t, err, "RenewLeaseForSessionExecutor should not return an error when terminalizing a terminal-session job")
	require.False(t, ok, "RenewLeaseForSessionExecutor should report lost ownership after terminal-session cleanup")
	require.Nil(t, job, "RenewLeaseForSessionExecutor should not return a job after terminal-session cleanup")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionExecutorStore_HeartbeatWithLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionExecutorStore(mock)
	orgID := uuid.New()
	executorID := uuid.New()
	lockToken := uuid.New()

	mock.ExpectQuery("UPDATE session_executors[\\s\\S]+RETURNING drain_intent").
		WithArgs(orgID, executorID, lockToken, int((2 * time.Minute).Seconds())).
		WillReturnRows(pgxmock.NewRows([]string{"drain_intent"}).AddRow("none"))

	ok, intent, err := store.HeartbeatWithLease(context.Background(), orgID, executorID, lockToken, 2*time.Minute)
	require.NoError(t, err, "HeartbeatWithLease should persist the executor heartbeat")
	require.True(t, ok, "HeartbeatWithLease should report that the fenced update landed")
	require.Equal(t, models.DrainIntentNone, intent, "HeartbeatWithLease should return the current executor drain intent")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionExecutorStore_GetByID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionExecutorStore(mock)
	executorID := uuid.New()
	orgID := uuid.New()
	sessionID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .* FROM session_executors").
		WithArgs(executorID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "thread_id", "job_id", "job_type",
			"host_node_id", "owner_id", "lock_token", "status", "container_id", "image", "build_sha",
			"heartbeat_at", "lease_expires_at", "runtime_deadline_at", "drain_intent",
			"drain_requested_at", "drain_deadline_at", "started_at", "completed_at",
			"exit_code", "last_error", "created_at", "updated_at",
		}).AddRow(
			executorID, orgID, sessionID, nil, jobID, "run_agent",
			"worker-1", "worker-1", lockToken, string(models.SessionExecutorStatusStarting), "container-1", "143:sha", "build-sha",
			now, now.Add(time.Minute), now.Add(90*time.Minute), "none", nil, nil, now, nil, nil, nil, now, now,
		))

	executor, err := store.GetByID(context.Background(), executorID)
	require.NoError(t, err, "GetByID should load the executor by globally unique id")
	require.Equal(t, executorID, executor.ID, "GetByID should return the matching executor")
	require.Equal(t, orgID, executor.OrgID, "GetByID should preserve org scope on the model")
	require.Equal(t, lockToken, executor.LockToken, "GetByID should return the fencing token")
	require.NotNil(t, executor.ContainerID, "GetByID should return the recorded Docker container id")
	require.Equal(t, "container-1", *executor.ContainerID, "GetByID should preserve the Docker container id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionExecutorStore_StateTransitionsWithLease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		invoke    func(store *SessionExecutorStore, ctx context.Context, orgID, executorID, lockToken uuid.UUID) (bool, error)
		setupMock func(mock pgxmock.PgxPoolIface, orgID, executorID, lockToken uuid.UUID)
		expectOK  bool
		expectErr bool
	}{
		{
			name: "MarkRunningWithLease returns true",
			invoke: func(store *SessionExecutorStore, ctx context.Context, orgID, executorID, lockToken uuid.UUID) (bool, error) {
				return store.MarkRunningWithLease(ctx, orgID, executorID, lockToken, time.Minute)
			},
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, executorID, lockToken uuid.UUID) {
				mock.ExpectExec("UPDATE session_executors\\s+SET status = 'running'").
					WithArgs(orgID, executorID, lockToken, int(time.Minute.Seconds())).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			expectOK: true,
		},
		{
			name: "MarkDrainingWithLease reports lost ownership",
			invoke: func(store *SessionExecutorStore, ctx context.Context, orgID, executorID, lockToken uuid.UUID) (bool, error) {
				return store.MarkDrainingWithLease(ctx, orgID, executorID, lockToken)
			},
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, executorID, lockToken uuid.UUID) {
				mock.ExpectExec("UPDATE session_executors\\s+SET status = 'draining'").
					WithArgs(orgID, executorID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			},
		},
		{
			name: "MarkTerminalWithLease wraps errors",
			invoke: func(store *SessionExecutorStore, ctx context.Context, orgID, executorID, lockToken uuid.UUID) (bool, error) {
				exitCode := 1
				return store.MarkTerminalWithLease(ctx, orgID, executorID, lockToken, models.SessionExecutorStatusFailed, &exitCode, "boom")
			},
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, executorID, lockToken uuid.UUID) {
				mock.ExpectExec("UPDATE session_executors\\s+SET status = \\$4").
					WithArgs(orgID, executorID, lockToken, models.SessionExecutorStatusFailed, 1, "boom").
					WillReturnError(errors.New("write failed"))
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

			store := NewSessionExecutorStore(mock)
			orgID := uuid.New()
			executorID := uuid.New()
			lockToken := uuid.New()
			tt.setupMock(mock, orgID, executorID, lockToken)

			ok, err := tt.invoke(store, context.Background(), orgID, executorID, lockToken)
			if tt.expectErr {
				require.Error(t, err, "state transition should return write errors")
				require.False(t, ok, "state transition should report false on error")
				return
			}
			require.NoError(t, err, "state transition should not return an error")
			require.Equal(t, tt.expectOK, ok, "state transition should report whether the fenced write landed")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionExecutorStore_MarkDeployBudgetExpiredByNode(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionExecutorStore(mock)
	now := time.Date(2026, 5, 28, 17, 0, 0, 0, time.UTC)

	mock.ExpectExec("UPDATE session_executors[\\s\\S]+drain_intent <> 'deploy_budget_expired'").
		WithArgs(now, 45, "worker-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))

	updated, err := store.MarkDeployBudgetExpiredByNode(context.Background(), "worker-1", now, 45*time.Second)
	require.NoError(t, err, "MarkDeployBudgetExpiredByNode should update over-budget active executors")
	require.Equal(t, int64(2), updated, "MarkDeployBudgetExpiredByNode should return updated executor count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionExecutorStore_MarkDeployBudgetExpiredByNodeSkipsAlreadyMarkedExecutors(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionExecutorStore(mock)
	now := time.Date(2026, 5, 28, 17, 0, 0, 0, time.UTC)

	mock.ExpectExec("UPDATE session_executors[\\s\\S]+drain_intent <> 'deploy_budget_expired'").
		WithArgs(now, 45, "worker-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	updated, err := store.MarkDeployBudgetExpiredByNode(context.Background(), "worker-1", now, 45*time.Second)
	require.NoError(t, err, "MarkDeployBudgetExpiredByNode should not error when every expired executor was already marked")
	require.Equal(t, int64(0), updated, "MarkDeployBudgetExpiredByNode should return zero once budget expiry has already been recorded")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionExecutorStore_MarkHumanInputCheckpointByJobUsesValidThreadLookup(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionExecutorStore(mock)
	orgID := uuid.New()
	jobID := uuid.New()
	lockToken := uuid.New()

	mock.ExpectExec("UPDATE session_executors se[\\s\\S]+EXISTS \\([\\s\\S]+FROM session_threads th[\\s\\S]+th.org_id = se.org_id[\\s\\S]+th.id = se.thread_id[\\s\\S]+th.status = 'awaiting_input'[\\s\\S]+\\)").
		WithArgs(orgID, jobID, lockToken).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	marked, err := store.MarkHumanInputCheckpointByJob(context.Background(), orgID, jobID, lockToken)
	require.NoError(t, err, "MarkHumanInputCheckpointByJob should use SQL that PostgreSQL can parse")
	require.True(t, marked, "MarkHumanInputCheckpointByJob should report marked executors")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionExecutorStore_ReclaimLost(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionExecutorStore(mock)
	staleBefore := time.Now().Add(-2 * time.Minute)
	mock.ExpectQuery("WITH stale_active AS").
		WithArgs(staleBefore, 100).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(2)))

	reclaimed, err := store.ReclaimLost(context.Background(), staleBefore, 100)
	require.NoError(t, err, "ReclaimLost should mark stale executors lost and requeue their jobs")
	require.Equal(t, int64(2), reclaimed, "ReclaimLost should return the number of lost executors")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionExecutorStore_ReclaimLostForSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionExecutorStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	staleBefore := time.Now().Add(-2 * time.Minute)
	mock.ExpectQuery("WITH stale_active AS").
		WithArgs(orgID, sessionID, staleBefore, 100).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(1)))

	reclaimed, err := store.ReclaimLostForSession(context.Background(), orgID, sessionID, staleBefore, 100)
	require.NoError(t, err, "targeted ReclaimLost should mark stale executors lost and requeue their jobs")
	require.Equal(t, int64(1), reclaimed, "targeted ReclaimLost should return the number of reclaimed rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionExecutorStore_ReclaimLostForSession_ScopesToSessionAndRecoveryMetadata(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("session_executor_store.go")
	require.NoError(t, err, "test should read session executor store source")

	body := string(src)
	require.Contains(t, body, "func (s *SessionExecutorStore) ReclaimLostForSession", "targeted executor recovery helper should exist")
	require.Contains(t, body, "se.org_id = $1", "targeted executor recovery must filter by org id")
	require.Contains(t, body, "se.session_id = $2", "targeted executor recovery must filter by session id")
	require.Contains(t, body, "runtime_stop_reason = 'worker_recovery'", "targeted executor recovery must persist worker-recovery stop reason")
	require.Contains(t, body, "recovery_state = 'queued'", "targeted executor recovery must queue session recovery")
	require.Contains(t, body, "j.lease_expires_at < now()", "targeted executor recovery must only reclaim stale running leases")
}

func TestSessionExecutorStore_ReclaimLostClearsPreHandoffOrphans(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionExecutorStore(mock)
	staleBefore := time.Now().Add(-2 * time.Minute)
	mock.ExpectQuery("WITH stale_active AS").
		WithArgs(staleBefore, 100).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(1)))

	reclaimed, err := store.ReclaimLost(context.Background(), staleBefore, 100)
	require.NoError(t, err, "ReclaimLost should clear stale pre-handoff executors")
	require.Equal(t, int64(1), reclaimed, "ReclaimLost should count pre-handoff orphan cleanup")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionExecutorStore_ReclaimLostClearsTerminalJobOrphans(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("session_executor_store.go")
	require.NoError(t, err, "test should read session executor store source")

	body := string(src)
	require.Contains(t, body, "j.lock_token IS NULL", "ReclaimLost should consider stale executors whose job lock was already cleared")
	require.Contains(t, body, "stale.job_status IN ('succeeded', 'failed', 'cancelled', 'skipped', 'dead_letter')", "ReclaimLost should clear active executor rows for terminal jobs")
}

func TestSessionExecutorStore_ReclaimLostDefersWhileThreadRuntimeLeaseActive(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("session_executor_store.go")
	require.NoError(t, err, "test should read session executor store source")

	body := string(src)
	require.Contains(t, body, "NOT EXISTS (\n\t\t\t\tSELECT 1\n\t\t\t\tFROM thread_runtimes tr", "ReclaimLost should consult active thread runtimes before requeueing a session job")
	require.Contains(t, body, "tr.thread_id = se.thread_id", "ReclaimLost should gate recovery on the same thread runtime, not unrelated session work")
	require.Contains(t, body, "tr.lease_expires_at > now()", "ReclaimLost should defer recovery while the thread runtime lease is still valid")
	require.NotContains(t, body, "tr.lease_expires_at IS NULL OR tr.lease_expires_at > now()", "ReclaimLost should not treat NULL runtime leases as active because runtime reapers consider NULL leases expired")
}

func TestJobStore_GetRunningForSessionExecutor(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	jobID := uuid.New()
	orgID := uuid.New()
	lockToken := uuid.New()
	executorID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT j.id, j.org_id, j.queue").
		WithArgs(orgID, jobID, lockToken, executorID.String()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "queue", "job_type", "payload", "priority", "status",
			"attempts", "max_attempts", "run_at", "locked_by_node_id", "locked_at",
			"lease_expires_at", "lock_token", "run_owner_id", "owner_kind", "last_error",
			"dedupe_key", "target_node_id", "created_at", "updated_at", "completed_at", "channel",
		}).AddRow(
			jobID, orgID, "default", "run_agent", []byte(`{"session_id":"abc"}`), 5, "running",
			1, 3, now, "worker-1", now, now.Add(time.Minute), lockToken.String(), executorID.String(), "session_executor", nil,
			nil, nil, now, now, nil, "stable",
		))

	job, ok, err := store.GetRunningForSessionExecutor(context.Background(), orgID, jobID, lockToken, executorID)
	require.NoError(t, err, "GetRunningForSessionExecutor should not return an error")
	require.True(t, ok, "GetRunningForSessionExecutor should report an active fenced job")
	require.NotNil(t, job, "GetRunningForSessionExecutor should return the job")
	require.Equal(t, models.JobOwnerKindSessionExecutor, job.OwnerKind, "GetRunningForSessionExecutor should hydrate executor ownership")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
