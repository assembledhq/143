package db

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestJobStore_Enqueue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		queue       string
		jobType     string
		payload     any
		priority    int
		dedupeKey   *string
		setupMock   func(mock pgxmock.PgxPoolIface, generatedID uuid.UUID)
		expectErr   bool
		expectNilID bool
	}{
		{
			name:      "enqueues job without dedupe key",
			queue:     "default",
			jobType:   "process_issue",
			payload:   map[string]string{"issue_id": "abc-123"},
			priority:  1,
			dedupeKey: nil,
			setupMock: func(mock pgxmock.PgxPoolIface, generatedID uuid.UUID) {
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id"}).
							AddRow(generatedID),
					)
			},
		},
		{
			name:      "enqueues job with dedupe key",
			queue:     "sync",
			jobType:   "sync_repo",
			payload:   map[string]string{"repo_id": "repo-456"},
			priority:  5,
			dedupeKey: jobDedupeKeyPtr("sync-repo-456"),
			setupMock: func(mock pgxmock.PgxPoolIface, generatedID uuid.UUID) {
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id"}).
							AddRow(generatedID),
					)
			},
		},
		{
			name:      "returns error when payload cannot be marshaled",
			queue:     "default",
			jobType:   "bad_job",
			payload:   make(chan int),
			priority:  1,
			dedupeKey: nil,
			setupMock: func(mock pgxmock.PgxPoolIface, generatedID uuid.UUID) {
				// No DB interaction expected since marshaling fails first
			},
			expectErr: true,
		},
		{
			name:      "treats dedupe conflict as a no-op success",
			queue:     "agent",
			jobType:   "open_pr",
			payload:   map[string]string{"session_id": "abc-123"},
			priority:  5,
			dedupeKey: jobDedupeKeyPtr("open_pr:abc-123"),
			setupMock: func(mock pgxmock.PgxPoolIface, _ uuid.UUID) {
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(pgx.ErrNoRows)
			},
			expectNilID: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewJobStore(mock)
			orgID := uuid.New()
			generatedID := uuid.New()
			tt.setupMock(mock, generatedID)

			id, err := store.Enqueue(context.Background(), orgID, tt.queue, tt.jobType, tt.payload, tt.priority, tt.dedupeKey)
			if tt.expectErr {
				require.Error(t, err, "Enqueue should return an error")
				require.Equal(t, uuid.Nil, id, "should return nil UUID on error")
				return
			}
			require.NoError(t, err, "Enqueue should not return an error")
			if tt.expectNilID {
				require.Equal(t, uuid.Nil, id, "dedupe conflict should return nil UUID with no error")
			} else {
				require.Equal(t, generatedID, id, "should return the generated job ID")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestJobStore_WorkerLoadSamples(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	mock.ExpectQuery("WITH worker_nodes AS").
		WillReturnRows(pgxmock.NewRows([]string{
			"worker_node_id",
			"node_status",
			"running_sessions",
			"turn_held_sessions",
			"sandbox_containers",
			"active_previews",
			"preview_held_containers",
			"running_jobs",
			"running_session_jobs",
		}).
			AddRow("worker-1", "active", int64(2), int64(1), int64(3), int64(4), int64(2), int64(5), int64(2)).
			AddRow("unassigned", "", int64(1), int64(0), int64(1), int64(0), int64(0), int64(0), int64(0)))

	samples, err := store.WorkerLoadSamples(context.Background())
	require.NoError(t, err, "WorkerLoadSamples should not return an error")
	require.Equal(t, []WorkerLoadSample{
		{
			WorkerNodeID:          "worker-1",
			NodeStatus:            "active",
			RunningSessions:       2,
			TurnHeldSessions:      1,
			SandboxContainers:     3,
			ActivePreviews:        4,
			PreviewHeldContainers: 2,
			RunningJobs:           5,
			RunningSessionJobs:    2,
		},
		{
			WorkerNodeID:      "unassigned",
			RunningSessions:   1,
			SandboxContainers: 1,
		},
	}, samples, "WorkerLoadSamples should return the expected per-worker load samples")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestJobStore_EnqueueWithOpts_PinsTargetNode verifies that EnqueueWithOpts
// passes TargetNodeID through to the INSERT so node-affinity routing actually
// records the pin. The plain Enqueue wrapper doesn't take a target — its
// callers get NULL, the unpinned-claim case.
func TestJobStore_EnqueueWithOpts_PinsTargetNode(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewJobStore(mock)
	orgID := uuid.New()
	generatedID := uuid.New()
	target := "worker-host-c"

	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(orgID, "agent", "continue_session", pgxmock.AnyArg(), 5, jobDedupeKeyPtr("k"), &target).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(generatedID))

	id, err := store.EnqueueWithOpts(context.Background(), orgID, EnqueueOpts{
		Queue:        "agent",
		JobType:      "continue_session",
		Payload:      map[string]string{"session_id": "abc"},
		Priority:     5,
		DedupeKey:    jobDedupeKeyPtr("k"),
		TargetNodeID: &target,
	})
	require.NoError(t, err)
	require.Equal(t, generatedID, id, "EnqueueWithOpts should return the generated job id when the pin lands cleanly")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestJobStore_RetryWithoutConsumingAttemptWithLeaseAndTarget_PinsRetry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	jobID := uuid.New()
	lockToken := uuid.New()
	runAt := time.Now()
	target := "worker-host-c"

	mock.ExpectExec("UPDATE jobs[\\s\\S]+attempts = GREATEST[\\s\\S]+target_node_id = \\$[0-9]+").
		WithArgs("retry", runAt, jobID, lockToken, &target).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	ok, err := store.RetryWithoutConsumingAttemptWithLeaseAndTarget(context.Background(), jobID, lockToken, "retry", runAt, &target)
	require.NoError(t, err, "targeted retry should not return an error")
	require.True(t, ok, "targeted retry should report that the fenced row was updated")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_GetLatestFailedByType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupMock  func(mock pgxmock.PgxPoolIface)
		wantResult bool
		expectErr  bool
	}{
		{
			name: "returns latest failed job",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				jobID := uuid.New()
				now := time.Now()
				mock.ExpectQuery("SELECT id, last_error, updated_at FROM jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "last_error", "updated_at"}).
							AddRow(jobID, "connection timeout", now),
					)
			},
			wantResult: true,
		},
		{
			name: "returns nil when no failed jobs exist",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT id, last_error, updated_at FROM jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(pgx.ErrNoRows)
			},
			wantResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewJobStore(mock)
			tt.setupMock(mock)

			result, err := store.GetLatestFailedByType(context.Background(), uuid.New(), "sync_repo")
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantResult {
				require.NotNil(t, result)
				require.Equal(t, "connection timeout", result.LastError)
			} else {
				require.Nil(t, result)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestJobStore_DeleteExpiredCompleted(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewJobStore(mock)

	mock.ExpectQuery("SELECT delete_expired_completed_jobs").
		WithArgs(30).
		WillReturnRows(pgxmock.NewRows([]string{"delete_expired_completed_jobs"}).AddRow(int64(42)))

	deleted, err := store.DeleteExpiredCompleted(context.Background(), 30)
	require.NoError(t, err)
	require.Equal(t, int64(42), deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestJobStore_Notify_PublishesWakeUp(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "Redis client should initialize for notifier tests")

	store := NewJobStore(nil)
	store.SetLogger(zerolog.Nop())
	store.SetNotifier(cache.NewJobNotifier(client, zerolog.Nop()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	delivered := make(chan struct{}, 1)
	listener := cache.NewJobNotifier(client, zerolog.Nop())
	listener.Start(ctx, func() {
		select {
		case delivered <- struct{}{}:
		default:
		}
	})

	require.Eventually(t, func() bool {
		store.Notify(context.Background(), uuid.New())
		select {
		case <-delivered:
			return true
		default:
			return false
		}
	}, time.Second, 20*time.Millisecond, "Notify should publish a Redis wake-up when a notifier is configured")

	store.Notify(context.Background(), uuid.Nil)
}

func TestJobStore_Notify_PublishFailureIsBestEffort(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "Redis client should initialize for notifier failure tests")
	store := NewJobStore(nil)
	store.SetLogger(zerolog.Nop())
	store.SetNotifier(cache.NewJobNotifier(client, zerolog.Nop()))
	mr.Close()

	require.NotPanics(t, func() {
		store.Notify(context.Background(), uuid.New())
	}, "Notify should log and continue when Redis publish fails")
}

func TestJobStore_ClaimNextRunnable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, leaseDuration time.Duration, lockToken uuid.UUID)
		expectNil bool
		expectErr bool
	}{
		{
			name: "claims next due job with lease and fencing token",
			setupMock: func(mock pgxmock.PgxPoolIface, leaseDuration time.Duration, lockToken uuid.UUID) {
				jobID := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				mock.ExpectQuery("WITH dead_target_nodes AS[\\s\\S]*next_job AS[\\s\\S]*RETURNING j.id, j.org_id, j.queue, j.job_type").
					WithArgs(pgxmock.AnyArg(), "worker-1", "worker-1", lockToken, int(leaseDuration.Seconds())).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "org_id", "queue", "job_type", "payload", "priority", "status",
						"attempts", "max_attempts", "run_at", "locked_by_node_id", "locked_at",
						"lease_expires_at", "lock_token", "run_owner_id", "owner_kind", "last_error",
						"dedupe_key", "target_node_id", "created_at", "updated_at", "completed_at",
					}).AddRow(
						jobID, orgID, "default", "run_agent", []byte(`{"session_id":"abc"}`), 5, "running",
						1, 3, now, "worker-1", now, now.Add(leaseDuration), lockToken.String(), "worker-1", "worker", nil, nil, nil, now, now, nil,
					))
			},
		},
		{
			name: "hydrates optional persisted fields including target node",
			setupMock: func(mock pgxmock.PgxPoolIface, leaseDuration time.Duration, lockToken uuid.UUID) {
				jobID := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				completedAt := now.Add(time.Minute)
				mock.ExpectQuery("WITH dead_target_nodes AS[\\s\\S]*next_job AS[\\s\\S]*RETURNING j.id, j.org_id, j.queue, j.job_type").
					WithArgs(pgxmock.AnyArg(), "worker-1", "worker-1", lockToken, int(leaseDuration.Seconds())).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "org_id", "queue", "job_type", "payload", "priority", "status",
						"attempts", "max_attempts", "run_at", "locked_by_node_id", "locked_at",
						"lease_expires_at", "lock_token", "run_owner_id", "owner_kind", "last_error",
						"dedupe_key", "target_node_id", "created_at", "updated_at", "completed_at",
					}).AddRow(
						jobID, orgID, "default", "run_agent", []byte(`{"session_id":"abc"}`), 5, "running",
						1, 3, now, "worker-1", now, now.Add(leaseDuration), lockToken.String(), "worker-1", "worker", "boom", "dedupe-1", "worker-1", now, now, completedAt,
					))
			},
		},
		{
			name: "returns nil when no pending job exists",
			setupMock: func(mock pgxmock.PgxPoolIface, leaseDuration time.Duration, lockToken uuid.UUID) {
				mock.ExpectQuery("WITH dead_target_nodes AS[\\s\\S]*next_job AS[\\s\\S]*RETURNING j.id, j.org_id, j.queue, j.job_type").
					WithArgs(pgxmock.AnyArg(), "worker-1", "worker-1", lockToken, int(leaseDuration.Seconds())).
					WillReturnError(pgx.ErrNoRows)
			},
			expectNil: true,
		},
		{
			name: "returns query errors",
			setupMock: func(mock pgxmock.PgxPoolIface, leaseDuration time.Duration, lockToken uuid.UUID) {
				mock.ExpectQuery("WITH dead_target_nodes AS[\\s\\S]*next_job AS[\\s\\S]*RETURNING j.id, j.org_id, j.queue, j.job_type").
					WithArgs(pgxmock.AnyArg(), "worker-1", "worker-1", lockToken, int(leaseDuration.Seconds())).
					WillReturnError(errors.New("db down"))
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

			store := NewJobStore(mock)
			leaseDuration := 60 * time.Second
			lockToken := uuid.New()
			tt.setupMock(mock, leaseDuration, lockToken)

			job, err := store.ClaimNextRunnable(context.Background(), "worker-1", "worker-1", lockToken, leaseDuration)
			if tt.expectErr {
				require.Error(t, err, "ClaimNextRunnable should return an error")
				require.Nil(t, job, "ClaimNextRunnable should not return a job on error")
				return
			}
			require.NoError(t, err, "ClaimNextRunnable should not return an error")
			if tt.expectNil {
				require.Nil(t, job, "ClaimNextRunnable should return nil when no job is due")
			} else {
				require.NotNil(t, job, "ClaimNextRunnable should return the claimed job")
				require.Equal(t, lockToken, *job.LockToken, "ClaimNextRunnable should persist the fencing token")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestJobStore_RenewLease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupMock  func(mock pgxmock.PgxPoolIface, lockToken uuid.UUID, leaseDuration time.Duration)
		wantActive bool
		expectErr  bool
	}{
		{
			name: "renews active lease",
			setupMock: func(mock pgxmock.PgxPoolIface, lockToken uuid.UUID, leaseDuration time.Duration) {
				mock.ExpectQuery("UPDATE jobs SET lease_expires_at = now\\(\\) \\+").
					WithArgs(int(leaseDuration.Seconds()), uuid.MustParse("11111111-1111-1111-1111-111111111111"), lockToken).
					WillReturnRows(pgxmock.NewRows([]string{"lease_expires_at"}).AddRow(time.Now().Add(leaseDuration)))
			},
			wantActive: true,
		},
		{
			name: "returns inactive when ownership was lost",
			setupMock: func(mock pgxmock.PgxPoolIface, lockToken uuid.UUID, leaseDuration time.Duration) {
				mock.ExpectQuery("UPDATE jobs SET lease_expires_at = now\\(\\) \\+").
					WithArgs(int(leaseDuration.Seconds()), uuid.MustParse("11111111-1111-1111-1111-111111111111"), lockToken).
					WillReturnError(pgx.ErrNoRows)
				mock.ExpectQuery("WITH target AS[\\s\\S]*UPDATE session_executors[\\s\\S]*UPDATE jobs[\\s\\S]*owner_kind = 'worker'").
					WithArgs(uuid.MustParse("11111111-1111-1111-1111-111111111111"), lockToken, "referenced session is already terminal; stopping session job lease renewal").
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(0)))
			},
		},
		{
			name: "terminalizes session job when referenced session is already terminal",
			setupMock: func(mock pgxmock.PgxPoolIface, lockToken uuid.UUID, leaseDuration time.Duration) {
				mock.ExpectQuery("UPDATE jobs SET lease_expires_at = now\\(\\) \\+").
					WithArgs(int(leaseDuration.Seconds()), uuid.MustParse("11111111-1111-1111-1111-111111111111"), lockToken).
					WillReturnError(pgx.ErrNoRows)
				mock.ExpectQuery("WITH target AS[\\s\\S]*s.status IN \\('completed', 'failed', 'cancelled', 'skipped'\\)[\\s\\S]*UPDATE session_executors[\\s\\S]*UPDATE jobs").
					WithArgs(uuid.MustParse("11111111-1111-1111-1111-111111111111"), lockToken, "referenced session is already terminal; stopping session job lease renewal").
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(1)))
			},
		},
		{
			name: "returns errors from renewal query",
			setupMock: func(mock pgxmock.PgxPoolIface, lockToken uuid.UUID, leaseDuration time.Duration) {
				mock.ExpectQuery("UPDATE jobs SET lease_expires_at = now\\(\\) \\+").
					WithArgs(int(leaseDuration.Seconds()), uuid.MustParse("11111111-1111-1111-1111-111111111111"), lockToken).
					WillReturnError(errors.New("write failed"))
			},
			expectErr: true,
		},
	}

	jobID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewJobStore(mock)
			lockToken := uuid.New()
			leaseDuration := 45 * time.Second
			tt.setupMock(mock, lockToken, leaseDuration)

			lease, ok, err := store.RenewLease(context.Background(), jobID, lockToken, leaseDuration)
			if tt.expectErr {
				require.Error(t, err, "RenewLease should return an error")
				require.False(t, ok, "RenewLease should report inactive on error")
				require.Nil(t, lease, "RenewLease should not return a lease on error")
				return
			}
			require.NoError(t, err, "RenewLease should not return an error")
			require.Equal(t, tt.wantActive, ok, "RenewLease should report whether the lease is still owned")
			if tt.wantActive {
				require.NotNil(t, lease, "RenewLease should return the updated lease")
			} else {
				require.Nil(t, lease, "RenewLease should return nil when ownership was lost")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestJobStore_RenewLease_GuardsSessionJobsAgainstTerminalSessions(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("jobs.go")
	require.NoError(t, err, "test should read jobs.go")

	sql := string(body)
	require.Contains(t, sql, "job_type NOT IN ('run_agent', 'continue_session')", "RenewLease should only apply session terminal-state checks to session runner jobs")
	require.Contains(t, sql, "payload->>'session_id'", "RenewLease should inspect the durable session reference in session job payloads")
	require.Contains(t, sql, "s.status NOT IN ('completed', 'failed', 'cancelled', 'skipped')", "RenewLease should refuse renewal when the referenced session is terminal")
	require.Contains(t, sql, "s.org_id = jobs.org_id", "RenewLease should scope the session guard to the job org")
}

func TestJobStore_TerminalizeRunningSessionJobs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	reason := "runtime-control watchdog failed terminal session"

	mock.ExpectQuery("UPDATE session_executors[\\s\\S]*UPDATE jobs[\\s\\S]*owner_kind = 'worker'[\\s\\S]*SELECT COUNT").
		WithArgs(orgID, sessionID, reason).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(2)))

	updated, err := store.TerminalizeRunningSessionJobs(context.Background(), orgID, sessionID, reason)
	require.NoError(t, err, "TerminalizeRunningSessionJobs should not return an error")
	require.Equal(t, int64(2), updated, "TerminalizeRunningSessionJobs should return the affected row count")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_TerminalizeRunningSessionJobs_ClosesExecutorOwnership(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("jobs.go")
	require.NoError(t, err, "test should read jobs.go")

	sql := string(body)
	require.Contains(t, sql, "UPDATE session_executors", "TerminalizeRunningSessionJobs should terminalize active executor rows for executor-owned session jobs")
	require.Contains(t, sql, "se.status IN ('starting', 'running', 'draining')", "TerminalizeRunningSessionJobs should only close active executor rows")
	require.Contains(t, sql, "se.lock_token = target.lock_token", "TerminalizeRunningSessionJobs should fence executor terminalization with the job lock token")
	require.Contains(t, sql, "owner_kind = 'worker'", "TerminalizeRunningSessionJobs should reset job ownership after leaving active executor ownership")
}

func TestJobStore_MarkSucceededWithLease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupMock  func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID)
		wantActive bool
		expectErr  bool
	}{
		{
			name: "marks job succeeded when lease is current",
			setupMock: func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID) {
				mock.ExpectExec("UPDATE jobs SET status = 'succeeded'").
					WithArgs(jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			wantActive: true,
		},
		{
			name: "reports inactive when fencing token no longer matches",
			setupMock: func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID) {
				mock.ExpectExec("UPDATE jobs SET status = 'succeeded'").
					WithArgs(jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			},
		},
		{
			name: "returns exec errors",
			setupMock: func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID) {
				mock.ExpectExec("UPDATE jobs SET status = 'succeeded'").
					WithArgs(jobID, lockToken).
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

			store := NewJobStore(mock)
			jobID := uuid.New()
			lockToken := uuid.New()
			tt.setupMock(mock, jobID, lockToken)

			ok, err := store.MarkSucceededWithLease(context.Background(), jobID, lockToken)
			if tt.expectErr {
				require.Error(t, err, "MarkSucceededWithLease should return an error")
				require.False(t, ok, "MarkSucceededWithLease should report inactive on error")
				return
			}
			require.NoError(t, err, "MarkSucceededWithLease should not return an error")
			require.Equal(t, tt.wantActive, ok, "MarkSucceededWithLease should report whether the write won the fencing race")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestJobStore_LeaseTerminalHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		invoke    func(store *JobStore, ctx context.Context, jobID, lockToken uuid.UUID) (bool, error)
		setupMock func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID)
		expectOK  bool
		expectErr bool
	}{
		{
			name: "MarkFailedWithLease returns true on success",
			invoke: func(store *JobStore, ctx context.Context, jobID, lockToken uuid.UUID) (bool, error) {
				return store.MarkFailedWithLease(ctx, jobID, lockToken, "boom")
			},
			setupMock: func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID) {
				mock.ExpectExec("UPDATE jobs SET status = 'failed'").
					WithArgs("boom", jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			expectOK: true,
		},
		{
			name: "RetryWithLease reports lost ownership",
			invoke: func(store *JobStore, ctx context.Context, jobID, lockToken uuid.UUID) (bool, error) {
				return store.RetryWithLease(ctx, jobID, lockToken, "retry", time.Now())
			},
			setupMock: func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID) {
				mock.ExpectExec("UPDATE jobs SET status = 'pending'").
					WithArgs("retry", pgxmock.AnyArg(), jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			},
		},
		{
			name: "RetryWithoutConsumingAttemptWithLease wraps errors",
			invoke: func(store *JobStore, ctx context.Context, jobID, lockToken uuid.UUID) (bool, error) {
				return store.RetryWithoutConsumingAttemptWithLease(ctx, jobID, lockToken, "retry", time.Now())
			},
			setupMock: func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID) {
				mock.ExpectExec("attempts = GREATEST\\(attempts - 1, 0\\)").
					WithArgs("retry", pgxmock.AnyArg(), jobID, lockToken).
					WillReturnError(errors.New("write failed"))
			},
			expectErr: true,
		},
		{
			name: "DeadLetterWithLease returns true on success",
			invoke: func(store *JobStore, ctx context.Context, jobID, lockToken uuid.UUID) (bool, error) {
				return store.DeadLetterWithLease(ctx, jobID, lockToken, "boom")
			},
			setupMock: func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID) {
				mock.ExpectExec("UPDATE jobs SET status = 'dead_letter'").
					WithArgs("boom", jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			expectOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewJobStore(mock)
			jobID := uuid.New()
			lockToken := uuid.New()
			tt.setupMock(mock, jobID, lockToken)

			ok, err := tt.invoke(store, context.Background(), jobID, lockToken)
			if tt.expectErr {
				require.Error(t, err, "helper should return errors from the terminal write")
				require.False(t, ok, "helper should report false on error")
			} else {
				require.NoError(t, err, "helper should not return an error")
				require.Equal(t, tt.expectOK, ok, "helper should report whether it won the fencing race")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestJobStore_ReclaimLostRunningJobs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	staleBefore := time.Now().Add(-90 * time.Second)
	mock.ExpectQuery("WITH dead_nodes AS").
		WithArgs(staleBefore, 100).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(3)))

	reclaimed, err := store.ReclaimLostRunningJobs(context.Background(), staleBefore, 100)
	require.NoError(t, err, "ReclaimLostRunningJobs should not return an error")
	require.Equal(t, int64(3), reclaimed, "ReclaimLostRunningJobs should return the number of reclaimed jobs")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_ReclaimLostRunningSessionJobsForSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	staleBefore := time.Now().Add(-90 * time.Second)
	mock.ExpectQuery("WITH dead_nodes AS").
		WithArgs(orgID, sessionID, staleBefore, 100).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(int64(1)))

	reclaimed, err := store.ReclaimLostRunningSessionJobsForSession(context.Background(), orgID, sessionID, staleBefore, 100)
	require.NoError(t, err, "targeted reclaim should not return an error")
	require.Equal(t, int64(1), reclaimed, "targeted reclaim should return the number of reclaimed session jobs")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_ReclaimLostRunningSessionJobsForSession_ScopesToSessionAndRecoveryMetadata(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("jobs.go")
	require.NoError(t, err, "test should read jobs.go")

	sql := string(body)
	require.Contains(t, sql, "func (s *JobStore) ReclaimLostRunningSessionJobsForSession", "targeted recovery helper should exist")
	require.Contains(t, sql, "j.org_id = $1", "targeted recovery must filter by org id")
	require.Contains(t, sql, "j.payload->>'session_id' = $2::text", "targeted recovery must filter by session payload")
	require.Contains(t, sql, "runtime_stop_reason = 'worker_recovery'", "targeted recovery must persist worker-recovery stop reason")
	require.Contains(t, sql, "recovery_state = 'queued'", "targeted recovery must queue session recovery")
	require.Contains(t, sql, "j.lease_expires_at < now()", "targeted recovery must only reclaim stale running leases")
}

func TestJobStore_ReclaimLostRunningJobs_IncludesLegacyNullLeaseRows(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("jobs.go")
	require.NoError(t, err, "test should read jobs.go")

	sql := string(body)
	require.Contains(t, sql, "j.lease_expires_at IS NULL", "recovery query should include legacy running jobs without a lease expiry")
	require.Contains(t, sql, "OR (j.lease_expires_at IS NULL AND d.id IS NOT NULL)", "legacy null-lease recovery should only reclaim jobs owned by dead or stale nodes")
	require.NotContains(t, sql, "j.lease_expires_at IS NULL AND j.locked_at < $1", "legacy null-lease recovery must not reclaim active live-node jobs using the node heartbeat cutoff")
}

func TestJobStore_ReclaimLostRunningJobs_ReturnsWrappedErrors(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	staleBefore := time.Now().Add(-90 * time.Second)
	mock.ExpectQuery("WITH dead_nodes AS").
		WithArgs(staleBefore, 100).
		WillReturnError(errors.New("update failed"))

	reclaimed, err := store.ReclaimLostRunningJobs(context.Background(), staleBefore, 100)
	require.Error(t, err, "ReclaimLostRunningJobs should return wrapped update errors")
	require.Equal(t, int64(0), reclaimed, "ReclaimLostRunningJobs should return zero on error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_OldestPendingSessionJobAge_UsesRunnableTime(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	runnableAt := time.Now().Add(-45 * time.Second)
	mock.ExpectQuery("SELECT run_at\\s+FROM jobs").
		WillReturnRows(pgxmock.NewRows([]string{"run_at"}).AddRow(runnableAt))

	age, ok, err := store.OldestPendingSessionJobAge(context.Background())
	require.NoError(t, err, "OldestPendingSessionJobAge should not return an error")
	require.True(t, ok, "OldestPendingSessionJobAge should report a runnable job when one exists")
	require.InDelta(t, 45*time.Second, age, float64(2*time.Second), "OldestPendingSessionJobAge should measure backlog from run_at rather than job creation time")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_OldestPendingSessionJobAge_NoRows(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	mock.ExpectQuery("SELECT run_at\\s+FROM jobs").
		WillReturnError(pgx.ErrNoRows)

	age, ok, err := store.OldestPendingSessionJobAge(context.Background())
	require.NoError(t, err, "OldestPendingSessionJobAge should not treat no rows as an error")
	require.False(t, ok, "OldestPendingSessionJobAge should report that no runnable job exists when the queue is empty")
	require.Zero(t, age, "OldestPendingSessionJobAge should return a zero age when the queue is empty")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_OldestPendingSessionJobAge_ReturnsWrappedErrors(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	mock.ExpectQuery("SELECT run_at\\s+FROM jobs").
		WillReturnError(errors.New("query failed"))

	age, ok, err := store.OldestPendingSessionJobAge(context.Background())
	require.Error(t, err, "OldestPendingSessionJobAge should wrap query failures")
	require.Contains(t, err.Error(), "oldest pending session job age", "OldestPendingSessionJobAge should preserve the operation context")
	require.False(t, ok, "OldestPendingSessionJobAge should report no backlog measurement on query error")
	require.Zero(t, age, "OldestPendingSessionJobAge should return a zero age on query error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_QueueHealthSamples(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	mock.ExpectQuery("SELECT\\s+queue,\\s+job_type").
		WillReturnRows(pgxmock.NewRows([]string{
			"queue",
			"job_type",
			"pending_runnable",
			"pending_deferred",
			"running",
			"dead_letter",
			"oldest_runnable_age_seconds",
		}).AddRow("agent", "run_agent", int64(3), int64(2), int64(1), int64(0), float64(42)).
			AddRow("default", "open_pr", int64(0), int64(1), int64(0), int64(2), nil))

	samples, err := store.QueueHealthSamples(context.Background())
	require.NoError(t, err, "QueueHealthSamples should not return an error")
	require.Equal(t, []JobQueueHealthSample{
		{
			Queue:                    "agent",
			JobType:                  "run_agent",
			PendingRunnable:          3,
			PendingDeferred:          2,
			Running:                  1,
			DeadLetter:               0,
			OldestRunnableAgeSeconds: 42,
		},
		{
			Queue:                    "default",
			JobType:                  "open_pr",
			PendingRunnable:          0,
			PendingDeferred:          1,
			Running:                  0,
			DeadLetter:               2,
			OldestRunnableAgeSeconds: 0,
		},
	}, samples, "QueueHealthSamples should return grouped queue health rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_QueueHealthSamples_ReturnsWrappedErrors(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	mock.ExpectQuery("SELECT\\s+queue,\\s+job_type").
		WillReturnError(errors.New("query failed"))

	samples, err := store.QueueHealthSamples(context.Background())
	require.Error(t, err, "QueueHealthSamples should return query errors")
	require.Contains(t, err.Error(), "queue health samples", "QueueHealthSamples should wrap the operation context")
	require.Nil(t, samples, "QueueHealthSamples should return nil samples on error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_CountRunningOwnedByNode(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM jobs").
		WithArgs("worker-1").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))

	count, err := store.CountRunningOwnedByNode(context.Background(), "worker-1")
	require.NoError(t, err, "CountRunningOwnedByNode should not return an error")
	require.Equal(t, 2, count, "CountRunningOwnedByNode should count active owned jobs")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_CountRunningOwnedByNode_ReturnsWrappedErrors(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM jobs").
		WithArgs("worker-1").
		WillReturnError(errors.New("query failed"))

	count, err := store.CountRunningOwnedByNode(context.Background(), "worker-1")
	require.Error(t, err, "CountRunningOwnedByNode should return wrapped query errors")
	require.Equal(t, 0, count, "CountRunningOwnedByNode should return zero on error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_SelectWorkerWithSandboxCapacity(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	mock.ExpectQuery("(?s)WITH candidates AS.*live_sandbox_count_error").
		WithArgs(pgxmock.AnyArg(), "worker-full").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("worker-with-space"))

	nodeID, err := store.SelectWorkerWithSandboxCapacity(context.Background(), "worker-full")
	require.NoError(t, err, "SelectWorkerWithSandboxCapacity should not return an error")
	require.NotNil(t, nodeID, "SelectWorkerWithSandboxCapacity should return an available worker")
	require.Equal(t, "worker-with-space", *nodeID, "SelectWorkerWithSandboxCapacity should pick the advertised worker with capacity")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_SelectWorkerWithSandboxCapacity_NoAvailableWorker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	mock.ExpectQuery("WITH candidates AS").
		WithArgs(pgxmock.AnyArg(), "worker-full").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))

	nodeID, err := store.SelectWorkerWithSandboxCapacity(context.Background(), "worker-full")
	require.NoError(t, err, "SelectWorkerWithSandboxCapacity should not treat no rows as an error")
	require.Nil(t, nodeID, "SelectWorkerWithSandboxCapacity should return nil when no worker advertises capacity")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func jobDedupeKeyPtr(s string) *string {
	return &s
}
