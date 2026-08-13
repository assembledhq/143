package db

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
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

func TestJobStore_EnqueueWithTargetAndWorkloadPersistsClass(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID, jobID := uuid.New(), uuid.New()
	targetNodeID := "worker-2"
	mock.ExpectQuery(`(?s)INSERT INTO jobs \(org_id,.*workload_class.*target_node_id.*\)`).
		WithArgs(
			orgID,
			"agent",
			"continue_session",
			pgxmock.AnyArg(),
			5,
			pgxmock.AnyArg(),
			models.SandboxWorkloadClassCodeReview,
			&targetNodeID,
		).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	got, err := NewJobStore(mock).EnqueueWithTargetAndWorkload(
		context.Background(),
		orgID,
		"agent",
		"continue_session",
		map[string]string{"session_id": uuid.NewString()},
		5,
		nil,
		&targetNodeID,
		models.SandboxWorkloadClassCodeReview,
	)
	require.NoError(t, err, "classified enqueue should persist a valid workload class")
	require.Equal(t, jobID, got, "classified enqueue should return the inserted job id")
	require.NoError(t, mock.ExpectationsWereMet(), "classified enqueue should persist the workload and target arguments")
}

func TestJobStoreQueueChangesetPRCreationIsAtomic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		reserveRows     int64
		existingState   *models.PRCreationState
		enqueueConflict bool
		enqueueErr      error
		wantEnqueue     bool
		wantQueued      bool
		wantErr         bool
	}{
		{name: "commits reservation and job together", reserveRows: 1, wantEnqueue: true, wantQueued: true},
		{name: "does not enqueue when publication already succeeded", existingState: ptrTo(models.PRCreationStateSucceeded)},
		{name: "requeues work after review left the reservation queued", existingState: ptrTo(models.PRCreationStateQueued), wantEnqueue: true, wantQueued: true},
		{name: "joins an active job that still owns the reservation", existingState: ptrTo(models.PRCreationStatePushing), wantEnqueue: true, enqueueConflict: true},
		{name: "rolls back reservation when enqueue fails", reserveRows: 1, wantEnqueue: true, enqueueErr: errors.New("insert failed"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "test should create the database mock")
			defer mock.Close()

			orgID, sessionID, changesetID, jobID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			mock.ExpectBegin()
			mock.ExpectExec(`UPDATE session_changesets SET pr_creation_state = 'queued'.+WHERE org_id = .+ AND session_id = .+ AND id =`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.reserveRows))
			if tt.reserveRows == 0 {
				mock.ExpectQuery(`SELECT pr_creation_state FROM session_changesets`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"pr_creation_state"}).AddRow(*tt.existingState))
			}
			if tt.wantEnqueue {
				query := mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg())
				if tt.enqueueErr != nil {
					query.WillReturnError(tt.enqueueErr)
				} else if tt.enqueueConflict {
					query.WillReturnRows(pgxmock.NewRows([]string{"id"}))
					mock.ExpectCommit()
				} else {
					query.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
					mock.ExpectCommit()
				}
			}
			mock.ExpectRollback()

			gotJobID, queued, err := NewJobStore(mock).QueueChangesetPRCreation(
				context.Background(), orgID, sessionID, changesetID, "default",
				map[string]any{"changeset_id": changesetID.String()}, 5,
			)
			if tt.wantErr {
				require.Error(t, err, "atomic enqueue should report the failed transaction")
			} else {
				require.NoError(t, err, "atomic enqueue should complete without an error")
			}
			require.Equal(t, tt.wantQueued, queued, "result should identify whether this caller created runnable publication work")
			if tt.wantQueued {
				require.Equal(t, jobID, gotJobID, "committed enqueue should return its durable job ID")
			} else {
				require.Equal(t, uuid.Nil, gotJobID, "non-committed enqueue should not return a job ID")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all transaction boundaries and scoped statements should be verified")
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
			"active_usage_containers",
			"active_memory_allocated_mb",
			"active_cpu_allocated",
			"active_disk_allocated_mb",
		}).
			AddRow("worker-1", "active", int64(2), int64(1), int64(3), int64(4), int64(2), int64(5), int64(2), int64(2), int64(6144), float64(4), int64(20480)).
			AddRow("unassigned", "", int64(1), int64(0), int64(1), int64(0), int64(0), int64(0), int64(0), int64(1), int64(3072), float64(2), int64(10240)))

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
			ActiveUsageContainers: 2,
			ActiveMemoryAllocated: 6144,
			ActiveCPUAllocated:    4,
			ActiveDiskAllocated:   20480,
		},
		{
			WorkerNodeID:          "unassigned",
			RunningSessions:       1,
			SandboxContainers:     1,
			ActiveUsageContainers: 1,
			ActiveMemoryAllocated: 3072,
			ActiveCPUAllocated:    2,
			ActiveDiskAllocated:   10240,
		},
	}, samples, "WorkerLoadSamples should return the expected per-worker load samples")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_RunningJobSamples(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	mock.ExpectQuery("SELECT[\\s\\S]+locked_by_node_id[\\s\\S]+job_type[\\s\\S]+COUNT").
		WillReturnRows(pgxmock.NewRows([]string{
			"worker_node_id",
			"job_type",
			"running",
		}).
			AddRow("worker-1", "run_agent", int64(2)).
			AddRow("worker-2", "start_preview", int64(1)).
			AddRow("unassigned", "sync_pull_request_state", int64(1)))

	samples, err := store.RunningJobSamples(context.Background())
	require.NoError(t, err, "RunningJobSamples should not return an error")
	require.Equal(t, []RunningJobSample{
		{WorkerNodeID: "worker-1", JobType: "run_agent", Running: 2},
		{WorkerNodeID: "worker-2", JobType: "start_preview", Running: 1},
		{WorkerNodeID: "unassigned", JobType: "sync_pull_request_state", Running: 1},
	}, samples, "RunningJobSamples should return running jobs grouped by worker and type")
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

func TestJobStore_EnqueueWithOpts_SetsCustomMaxAttempts(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	orgID := uuid.New()
	generatedID := uuid.New()

	mock.ExpectQuery("INSERT INTO jobs[\\s\\S]+max_attempts").
		WithArgs(orgID, "agent", models.JobTypeRunCodeReview, pgxmock.AnyArg(), 5, jobDedupeKeyPtr("review"), 8).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(generatedID))

	id, err := store.EnqueueWithOpts(context.Background(), orgID, EnqueueOpts{
		Queue:       "agent",
		JobType:     models.JobTypeRunCodeReview,
		Payload:     map[string]string{"session_id": "abc"},
		Priority:    5,
		DedupeKey:   jobDedupeKeyPtr("review"),
		MaxAttempts: 8,
	})
	require.NoError(t, err, "custom retry budget enqueue should succeed")
	require.Equal(t, generatedID, id, "custom retry budget enqueue should return the job id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_EnqueueWithOpts_DefersRunAt(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	store := NewJobStore(mock)
	orgID := uuid.New()
	generatedID := uuid.New()
	runAt := time.Now().UTC().Add(5 * time.Second)

	mock.ExpectQuery("INSERT INTO jobs[\\s\\S]+run_at").
		WithArgs(orgID, "default", "deferred", pgxmock.AnyArg(), 4, jobDedupeKeyPtr("deferred:key"), runAt).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(generatedID))

	id, err := store.EnqueueWithOpts(context.Background(), orgID, EnqueueOpts{
		Queue:     "default",
		JobType:   "deferred",
		Payload:   map[string]string{"kind": "debounced"},
		Priority:  4,
		DedupeKey: jobDedupeKeyPtr("deferred:key"),
		RunAt:     &runAt,
	})
	require.NoError(t, err, "deferred enqueue should succeed")
	require.Equal(t, generatedID, id, "deferred enqueue should return the generated job ID")
	require.NoError(t, mock.ExpectationsWereMet(), "deferred enqueue should persist the requested run time")
}

func TestJobStore_HasActiveByDedupeKeyFiltersByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	mock.ExpectQuery("SELECT EXISTS[\\s\\S]+org_id[\\s\\S]+dedupe_key[\\s\\S]+status IN").
		WithArgs(orgID, "agent", "code_review:key").
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	active, err := NewJobStore(mock).HasActiveByDedupeKey(context.Background(), orgID, "agent", "code_review:key")
	require.NoError(t, err, "active dedupe lookup should succeed")
	require.True(t, active, "active dedupe lookup should return the stored state")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestJobStore_RetryWithLeaseAndTargetMaintainsReservationConsistency(t *testing.T) {
	t.Parallel()

	target := "worker-host-c"
	tests := []struct {
		name             string
		preserveAttempts bool
		targetNodeID     *string
		queryPattern     string
	}{
		{
			name:             "preserves attempts and reserved target",
			preserveAttempts: true,
			targetNodeID:     &target,
			queryPattern:     `(?s)UPDATE jobs.*attempts = GREATEST.*target_node_id = \$5.*sandbox_slot_reserved_until = CASE.*WHEN \$5 IS NULL THEN NULL`,
		},
		{
			name:             "preserves attempts and clears stale reservation",
			preserveAttempts: true,
			queryPattern:     `(?s)UPDATE jobs.*attempts = GREATEST.*target_node_id = \$5.*sandbox_slot_reserved_until = CASE.*WHEN \$5 IS NULL THEN NULL`,
		},
		{
			name:         "consumes attempt and preserves reserved target",
			targetNodeID: &target,
			queryPattern: `(?s)UPDATE jobs.*run_at = \$2.*target_node_id = \$5.*sandbox_slot_reserved_until = CASE.*WHEN \$5 IS NULL THEN NULL`,
		},
		{
			name:         "consumes attempt and clears stale reservation",
			queryPattern: `(?s)UPDATE jobs.*run_at = \$2.*target_node_id = \$5.*sandbox_slot_reserved_until = CASE.*WHEN \$5 IS NULL THEN NULL`,
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
			runAt := time.Now()
			mock.ExpectExec(tt.queryPattern).
				WithArgs("retry", runAt, jobID, lockToken, tt.targetNodeID).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			var ok bool
			if tt.preserveAttempts {
				ok, err = store.RetryWithoutConsumingAttemptWithLeaseAndTarget(context.Background(), jobID, lockToken, "retry", runAt, tt.targetNodeID)
			} else {
				ok, err = store.RetryWithLeaseAndTarget(context.Background(), jobID, lockToken, "retry", runAt, tt.targetNodeID)
			}
			require.NoError(t, err, "targeted retry should not return an error")
			require.True(t, ok, "targeted retry should report that the fenced row was updated")
			require.NoError(t, mock.ExpectationsWereMet(), "targeted retry should keep the reservation only when a target remains pinned")
		})
	}
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

	persistedRetryStart := time.Date(2026, time.July, 21, 19, 0, 0, 0, time.UTC)
	tests := []struct {
		name                     string
		setupMock                func(mock pgxmock.PgxPoolIface, leaseDuration time.Duration, lockToken uuid.UUID)
		expectNil                bool
		expectErr                bool
		expectedRetryWindowStart *time.Time
	}{
		{
			name: "claims next due job with lease and fencing token",
			setupMock: func(mock pgxmock.PgxPoolIface, leaseDuration time.Duration, lockToken uuid.UUID) {
				jobID := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				mock.ExpectBegin()
				mock.ExpectQuery("WITH unavailable_target_nodes AS[\\s\\S]*SELECT j.id, j.org_id, j.job_type").
					WithArgs(pgxmock.AnyArg(), "worker-1", []uuid.UUID{}).
					WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
						AddRow(jobID, orgID, "run_agent", nil, models.SandboxWorkloadClassInteractive, models.JobStatusPending, nil, now))
				expectSandboxClaimCandidateSavepoint(mock)
				mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations.*FOR NO KEY UPDATE`).
					WithArgs(orgID).
					WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"max_concurrent_runs":3}`)))
				mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*id <>.*job_type IN`).
					WithArgs(orgID, jobID).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
				mock.ExpectQuery("UPDATE jobs j[\\s\\S]*RETURNING j.id, j.org_id, j.queue, j.job_type").
					WithArgs("worker-1", "worker-1", lockToken, int(leaseDuration.Seconds()), jobID).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "org_id", "queue", "job_type", "payload", "priority", "status",
						"attempts", "max_attempts", "run_at", "locked_by_node_id", "locked_at",
						"lease_expires_at", "lock_token", "run_owner_id", "owner_kind", "last_error",
						"dedupe_key", "workload_class", "target_node_id", "sandbox_slot_reserved_until", "retry_window_started_at", "created_at", "updated_at", "completed_at",
					}).AddRow(
						jobID, orgID, "default", "run_agent", []byte(`{"session_id":"abc"}`), 5, "running",
						1, 3, now, "worker-1", now, now.Add(leaseDuration), lockToken.String(), "worker-1", "worker", nil, nil, models.SandboxWorkloadClassInteractive, nil, nil, nil, now, now, nil,
					))
				mock.ExpectCommit()
				mock.ExpectRollback()
			},
		},
		{
			name: "hydrates optional persisted fields including target node",
			setupMock: func(mock pgxmock.PgxPoolIface, leaseDuration time.Duration, lockToken uuid.UUID) {
				jobID := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				completedAt := now.Add(time.Minute)
				mock.ExpectBegin()
				mock.ExpectQuery("WITH unavailable_target_nodes AS[\\s\\S]*SELECT j.id, j.org_id, j.job_type").
					WithArgs(pgxmock.AnyArg(), "worker-1", []uuid.UUID{}).
					WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
						AddRow(jobID, orgID, "run_agent", nil, models.SandboxWorkloadClassInteractive, models.JobStatusPending, nil, now))
				expectSandboxClaimCandidateSavepoint(mock)
				mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations.*FOR NO KEY UPDATE`).
					WithArgs(orgID).
					WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"max_concurrent_runs":3}`)))
				mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*id <>.*job_type IN`).
					WithArgs(orgID, jobID).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
				mock.ExpectQuery("UPDATE jobs j[\\s\\S]*RETURNING j.id, j.org_id, j.queue, j.job_type").
					WithArgs("worker-1", "worker-1", lockToken, int(leaseDuration.Seconds()), jobID).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "org_id", "queue", "job_type", "payload", "priority", "status",
						"attempts", "max_attempts", "run_at", "locked_by_node_id", "locked_at",
						"lease_expires_at", "lock_token", "run_owner_id", "owner_kind", "last_error",
						"dedupe_key", "workload_class", "target_node_id", "sandbox_slot_reserved_until", "retry_window_started_at", "created_at", "updated_at", "completed_at",
					}).AddRow(
						jobID, orgID, "default", "run_agent", []byte(`{"session_id":"abc"}`), 5, "running",
						1, 3, now, "worker-1", now, now.Add(leaseDuration), lockToken.String(), "worker-1", "worker", "boom", "dedupe-1", models.SandboxWorkloadClassCodeReview, "worker-1", nil, persistedRetryStart, now, now, completedAt,
					))
				mock.ExpectCommit()
				mock.ExpectRollback()
			},
			expectedRetryWindowStart: &persistedRetryStart,
		},
		{
			name: "claims an expired initial run for its terminal capacity handler",
			setupMock: func(mock pgxmock.PgxPoolIface, leaseDuration time.Duration, lockToken uuid.UUID) {
				jobID := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				mock.ExpectBegin()
				mock.ExpectQuery("WITH unavailable_target_nodes AS[\\s\\S]*SELECT j.id, j.org_id, j.job_type").
					WithArgs(pgxmock.AnyArg(), "worker-1", []uuid.UUID{}).
					WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
						AddRow(jobID, orgID, "run_agent", nil, models.SandboxWorkloadClassInteractive, models.JobStatusPending, persistedRetryStart, persistedRetryStart))
				expectSandboxClaimCandidateSavepoint(mock)
				mock.ExpectQuery("UPDATE jobs j[\\s\\S]*RETURNING j.id, j.org_id, j.queue, j.job_type").
					WithArgs("worker-1", "worker-1", lockToken, int(leaseDuration.Seconds()), jobID).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "org_id", "queue", "job_type", "payload", "priority", "status",
						"attempts", "max_attempts", "run_at", "locked_by_node_id", "locked_at",
						"lease_expires_at", "lock_token", "run_owner_id", "owner_kind", "last_error",
						"dedupe_key", "workload_class", "target_node_id", "sandbox_slot_reserved_until", "retry_window_started_at", "created_at", "updated_at", "completed_at",
					}).AddRow(
						jobID, orgID, "default", "run_agent", []byte(`{"session_id":"abc"}`), 5, "running",
						1, 3, now, "worker-1", now, now.Add(leaseDuration), lockToken.String(), "worker-1", "worker", nil, nil, models.SandboxWorkloadClassInteractive, "worker-1", nil, persistedRetryStart, persistedRetryStart, now, nil,
					))
				mock.ExpectCommit()
				mock.ExpectRollback()
			},
			expectedRetryWindowStart: &persistedRetryStart,
		},
		{
			name: "returns nil when no pending job exists",
			setupMock: func(mock pgxmock.PgxPoolIface, leaseDuration time.Duration, lockToken uuid.UUID) {
				mock.ExpectBegin()
				mock.ExpectQuery("WITH unavailable_target_nodes AS[\\s\\S]*SELECT j.id, j.org_id, j.job_type").
					WithArgs(pgxmock.AnyArg(), "worker-1", []uuid.UUID{}).
					WillReturnError(pgx.ErrNoRows)
				mock.ExpectRollback()
			},
			expectNil: true,
		},
		{
			name: "returns query errors",
			setupMock: func(mock pgxmock.PgxPoolIface, leaseDuration time.Duration, lockToken uuid.UUID) {
				mock.ExpectBegin()
				mock.ExpectQuery("WITH unavailable_target_nodes AS[\\s\\S]*SELECT j.id, j.org_id, j.job_type").
					WithArgs(pgxmock.AnyArg(), "worker-1", []uuid.UUID{}).
					WillReturnError(errors.New("db down"))
				mock.ExpectRollback()
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
				require.NotEmpty(t, job.WorkloadClass, "ClaimNextRunnable should hydrate workload admission policy")
				require.Equal(t, tt.expectedRetryWindowStart, job.RetryWindowStartedAt, "ClaimNextRunnable should hydrate the durable retry window start")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestJobStore_ClaimNextRunnableRequiresSandboxRouting(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	leaseDuration := 60 * time.Second
	lockToken := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)WITH unavailable_target_nodes AS.*j.job_type NOT IN \('run_agent', 'continue_session'\) OR j.target_node_id IS NOT NULL.*d.id IS NOT NULL AND j.sandbox_slot_reserved_until IS NULL.*ORDER BY.*j.priority DESC,.*CASE.*j.run_at ASC,.*j.created_at ASC`).
		WithArgs(pgxmock.AnyArg(), "worker-1", []uuid.UUID{}).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	job, err := NewJobStore(mock).ClaimNextRunnable(context.Background(), "worker-1", "worker-1", lockToken, leaseDuration)
	require.NoError(t, err, "claim should treat an unreserved sandbox job as not runnable")
	require.Nil(t, job, "unbound sandbox jobs should wait for capacity-aware routing before claim")
	require.NoError(t, mock.ExpectationsWereMet(), "claim SQL should require routing while preserving real dead-worker affinity recovery")
}

func TestJobStore_ClaimNextRunnableAtomicallyDefersAffinitySandboxTurnAtSharedOrgLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		workloadClass models.SandboxWorkloadClass
	}{
		{name: "interactive", workloadClass: models.SandboxWorkloadClassInteractive},
		{name: "code review", workloadClass: models.SandboxWorkloadClassCodeReview},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			jobID, orgID := uuid.New(), uuid.New()
			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)WITH unavailable_target_nodes AS.*SELECT j.id, j.org_id, j.job_type`).
				WithArgs(pgxmock.AnyArg(), "worker-1", []uuid.UUID{}).
				WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
					AddRow(jobID, orgID, "continue_session", nil, tt.workloadClass, models.JobStatusPending, nil, time.Now().Add(-sandboxRoutingTerminalProbeAge-time.Minute)))
			expectSandboxClaimCandidateSavepoint(mock)
			mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations.*FOR NO KEY UPDATE`).
				WithArgs(orgID).
				WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"max_concurrent_runs":2}`)))
			mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*id <>.*job_type IN`).
				WithArgs(orgID, jobID).
				WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
			mock.ExpectExec(`(?s)UPDATE jobs.*jsonb_set\(payload, '\{capacity_waited\}'.*retry_window_started_at = CASE.*WHEN job_type = 'continue_session' THEN NULL.*target_node_id = CASE.*sandbox_slot_reserved_until IS NULL THEN target_node_id.*sandbox_slot_reserved_until = NULL`).
				WithArgs(tt.workloadClass, int(SandboxOrgLimitRetryDelay.Seconds()), jobID).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			mock.ExpectCommit()
			mock.ExpectRollback()
			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)WITH unavailable_target_nodes AS.*SELECT j.id, j.org_id, j.job_type`).
				WithArgs(pgxmock.AnyArg(), "worker-1", []uuid.UUID{orgID}).
				WillReturnError(pgx.ErrNoRows)
			mock.ExpectRollback()

			job, err := NewJobStore(mock).ClaimNextRunnable(context.Background(), "worker-1", "worker-1", uuid.New(), time.Minute)
			require.NoError(t, err, "affinity-bound sandbox admission should defer cleanly at the serialized shared org limit")
			require.Nil(t, job, "org-limited sandbox work should not be claimed")
			require.NoError(t, mock.ExpectationsWereMet(), "claim should lock the org before counting all active turns and preserve ordinary affinity")
		})
	}
}

func TestJobStore_ClaimNextRunnableIsolatesInvalidSandboxCandidate(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, orgID := uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)WITH unavailable_target_nodes AS.*SELECT j.id, j.org_id, j.job_type`).
		WithArgs(pgxmock.AnyArg(), "worker-1", []uuid.UUID{}).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
			AddRow(jobID, orgID, "run_agent", nil, models.SandboxWorkloadClassInteractive, models.JobStatusPending, nil, time.Now()))
	expectSandboxClaimCandidateSavepoint(mock)
	mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations.*FOR NO KEY UPDATE`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"session_automation":{"automatic_follow_through":{"pr_feedback_mode":"invalid"}}}`)))
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT sandbox_claim_candidate`).WillReturnResult(pgxmock.NewResult("ROLLBACK", 0))
	mock.ExpectExec(`(?s)UPDATE jobs.*last_error = @last_error.*run_at = @run_at.*target_node_id = CASE.*sandbox_slot_reserved_until IS NULL THEN target_node_id.*sandbox_slot_reserved_until = NULL.*org_id = @org_id.*status = 'pending'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), jobID, orgID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`RELEASE SAVEPOINT sandbox_claim_candidate`).WillReturnResult(pgxmock.NewResult("RELEASE", 0))
	mock.ExpectCommit()
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)WITH unavailable_target_nodes AS.*SELECT j.id, j.org_id, j.job_type`).
		WithArgs(pgxmock.AnyArg(), "worker-1", []uuid.UUID{}).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	job, err := NewJobStore(mock).ClaimNextRunnable(context.Background(), "worker-1", "worker-1", uuid.New(), time.Minute)
	require.NoError(t, err, "one invalid sandbox candidate should not fail the worker claim pass")
	require.Nil(t, job, "invalid candidate should be deferred instead of claimed")
	require.NoError(t, mock.ExpectationsWereMet(), "claim should isolate the failed candidate and continue its bounded scan")
}

func TestJobStore_ClaimNextRunnablePreservesInitialRunTerminalDeadlineAtOrgLimit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, orgID := uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)WITH unavailable_target_nodes AS.*SELECT j.id, j.org_id, j.job_type`).
		WithArgs(pgxmock.AnyArg(), "worker-1", []uuid.UUID{}).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
			AddRow(jobID, orgID, "run_agent", nil, models.SandboxWorkloadClassInteractive, models.JobStatusPending, nil, time.Now()))
	expectSandboxClaimCandidateSavepoint(mock)
	mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations.*FOR NO KEY UPDATE`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"max_concurrent_runs":1}`)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*id <>.*job_type IN`).
		WithArgs(orgID, jobID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectExec(`(?s)UPDATE jobs.*retry_window_started_at = CASE.*WHEN job_type = 'run_agent' AND attempts = 0 THEN created_at.*target_node_id = CASE`).
		WithArgs(models.SandboxWorkloadClassInteractive, int(SandboxOrgLimitRetryDelay.Seconds()), jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)WITH unavailable_target_nodes AS.*SELECT j.id, j.org_id, j.job_type`).
		WithArgs(pgxmock.AnyArg(), "worker-1", []uuid.UUID{orgID}).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	job, err := NewJobStore(mock).ClaimNextRunnable(context.Background(), "worker-1", "worker-1", uuid.New(), time.Minute)
	require.NoError(t, err, "metadata-fallback initial run should defer cleanly at the org limit")
	require.Nil(t, job, "org-limited initial run should remain pending until its terminal deadline")
	require.NoError(t, mock.ExpectationsWereMet(), "first claim deferral should anchor the terminal deadline to job creation")
}

func TestJobStore_ClaimNextRunnableBypassesOrgLimitForCancelledContinuation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, orgID, sessionID := uuid.New(), uuid.New(), uuid.New()
	lockToken := uuid.New()
	now := time.Now()
	leaseDuration := time.Minute
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)WITH unavailable_target_nodes AS.*SELECT j.id, j.org_id, j.job_type`).
		WithArgs(pgxmock.AnyArg(), "worker-1", []uuid.UUID{}).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
			AddRow(jobID, orgID, "continue_session", sessionID.String(), models.SandboxWorkloadClassCodeReview, models.JobStatusPending, nil, now))
	expectSandboxClaimCandidateSavepoint(mock)
	mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations.*FOR NO KEY UPDATE`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"max_concurrent_runs":1}`)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*id <>.*job_type IN`).
		WithArgs(orgID, jobID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT s.status.*session_cancel_requests.*session_threads.*JOIN jobs payload_job.*s.deleted_at IS NULL`).
		WithArgs(orgID, sessionID, jobID).
		WillReturnRows(pgxmock.NewRows([]string{"status", "pending_cancel", "stopped_thread", "capacity_cleanup"}).
			AddRow(models.SessionStatusRunning, true, false, false))
	mock.ExpectExec(`(?s)UPDATE jobs.*jsonb_set\(payload, '\{capacity_waited\}'.*sandbox_slot_reserved_until = NULL.*job_type = 'continue_session'`).
		WithArgs(jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery(`(?s)UPDATE jobs j.*status = 'running'.*RETURNING j.id, j.org_id`).
		WithArgs("worker-1", "worker-1", lockToken, int(leaseDuration.Seconds()), jobID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "queue", "job_type", "payload", "priority", "status",
			"attempts", "max_attempts", "run_at", "locked_by_node_id", "locked_at",
			"lease_expires_at", "lock_token", "run_owner_id", "owner_kind", "last_error",
			"dedupe_key", "workload_class", "target_node_id", "sandbox_slot_reserved_until", "retry_window_started_at", "created_at", "updated_at", "completed_at",
		}).AddRow(
			jobID, orgID, "agent", "continue_session", []byte(`{"session_id":"`+sessionID.String()+`","capacity_waited":true,"capacity_cleanup":true}`), 1, models.JobStatusRunning,
			1, 3, now, "worker-1", now, now.Add(leaseDuration), lockToken.String(), "worker-1", "worker", nil,
			nil, models.SandboxWorkloadClassCodeReview, "worker-1", nil, nil, now, now, nil,
		))
	mock.ExpectCommit()
	mock.ExpectRollback()

	job, err := NewJobStore(mock).ClaimNextRunnable(context.Background(), "worker-1", "worker-1", lockToken, leaseDuration)
	require.NoError(t, err, "cancelled continuation should bypass shared org admission for lightweight cleanup")
	require.NotNil(t, job, "cancelled continuation should be claimed instead of deferred")
	require.Contains(t, string(job.Payload), `"capacity_waited":true`, "claimed cleanup job should force the handler durable-state check")
	require.Contains(t, string(job.Payload), `"capacity_cleanup":true`, "claimed cleanup job should remain terminal if a live turn consumes cancellation first")
	require.NoError(t, mock.ExpectationsWereMet(), "claim should probe tenant-scoped cancellation before org-limit deferral")
}

func TestJobStore_EnsureRetryWindowStartedAtWithLease(t *testing.T) {
	t.Parallel()

	requestedStart := time.Date(2026, time.July, 21, 19, 0, 0, 0, time.UTC)
	existingStart := requestedStart.Add(-time.Hour)
	tests := []struct {
		name          string
		setupMock     func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID)
		expectedStart time.Time
		expectedOK    bool
		expectErr     bool
	}{
		{
			name: "records first retry window",
			setupMock: func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID) {
				mock.ExpectQuery("UPDATE jobs[\\s\\S]+retry_window_started_at = COALESCE\\(retry_window_started_at, \\$1\\)").
					WithArgs(requestedStart, jobID, lockToken).
					WillReturnRows(pgxmock.NewRows([]string{"retry_window_started_at"}).AddRow(requestedStart))
			},
			expectedStart: requestedStart,
			expectedOK:    true,
		},
		{
			name: "preserves existing retry window",
			setupMock: func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID) {
				mock.ExpectQuery("UPDATE jobs[\\s\\S]+retry_window_started_at = COALESCE\\(retry_window_started_at, \\$1\\)").
					WithArgs(requestedStart, jobID, lockToken).
					WillReturnRows(pgxmock.NewRows([]string{"retry_window_started_at"}).AddRow(existingStart))
			},
			expectedStart: existingStart,
			expectedOK:    true,
		},
		{
			name: "reports lost ownership",
			setupMock: func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID) {
				mock.ExpectQuery("UPDATE jobs[\\s\\S]+retry_window_started_at").
					WithArgs(requestedStart, jobID, lockToken).
					WillReturnError(pgx.ErrNoRows)
			},
		},
		{
			name: "wraps database failure",
			setupMock: func(mock pgxmock.PgxPoolIface, jobID, lockToken uuid.UUID) {
				mock.ExpectQuery("UPDATE jobs[\\s\\S]+retry_window_started_at").
					WithArgs(requestedStart, jobID, lockToken).
					WillReturnError(errors.New("db unavailable"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should initialize")
			defer mock.Close()
			jobID := uuid.New()
			lockToken := uuid.New()
			tt.setupMock(mock, jobID, lockToken)

			actual, ok, err := NewJobStore(mock).EnsureRetryWindowStartedAtWithLease(context.Background(), jobID, lockToken, requestedStart)
			if tt.expectErr {
				require.Error(t, err, "EnsureRetryWindowStartedAtWithLease should return database failures")
			} else {
				require.NoError(t, err, "EnsureRetryWindowStartedAtWithLease should complete without error")
			}
			require.Equal(t, tt.expectedOK, ok, "EnsureRetryWindowStartedAtWithLease should report fenced ownership")
			require.Equal(t, tt.expectedStart, actual, "EnsureRetryWindowStartedAtWithLease should return the durable first retry time")
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
				mock.ExpectQuery("UPDATE jobs SET lease_expires_at = now\\(\\) \\+[\\s\\S]*sandbox_slot_reserved_until = CASE[\\s\\S]*sandbox_session.container_id IS NOT NULL[\\s\\S]*ELSE now\\(\\) \\+").
					WithArgs(int(leaseDuration.Seconds()), uuid.MustParse("11111111-1111-1111-1111-111111111111"), lockToken).
					WillReturnRows(pgxmock.NewRows([]string{"lease_expires_at"}).AddRow(time.Now().Add(leaseDuration)))
			},
			wantActive: true,
		},
		{
			name: "returns inactive when ownership was lost",
			setupMock: func(mock pgxmock.PgxPoolIface, lockToken uuid.UUID, leaseDuration time.Duration) {
				mock.ExpectQuery("UPDATE jobs SET lease_expires_at = now\\(\\) \\+[\\s\\S]*sandbox_slot_reserved_until = CASE[\\s\\S]*sandbox_session.container_id IS NOT NULL[\\s\\S]*ELSE now\\(\\) \\+").
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
				mock.ExpectQuery("UPDATE jobs SET lease_expires_at = now\\(\\) \\+[\\s\\S]*sandbox_slot_reserved_until = CASE[\\s\\S]*sandbox_session.container_id IS NOT NULL[\\s\\S]*ELSE now\\(\\) \\+").
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
				mock.ExpectQuery("UPDATE jobs SET lease_expires_at = now\\(\\) \\+[\\s\\S]*sandbox_slot_reserved_until = CASE[\\s\\S]*sandbox_session.container_id IS NOT NULL[\\s\\S]*ELSE now\\(\\) \\+").
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
	require.NotContains(t, sql, "sandbox_session.id::text = payload->>'session_id'", "reservation renewal should not cast away the sessions primary-key index")
	require.NotContains(t, sql, "s.id::text = payload->>'session_id'", "terminal-session renewal checks should not cast away the sessions primary-key index")
	require.Contains(t, sql, "THEN (payload->>'session_id')::uuid", "renewal should guard and cast the payload value for indexed UUID lookup")
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

func TestJobStore_ReclaimLostRunningJobs_RecoveryMetadataSessionJobsOnly(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("jobs.go")
	require.NoError(t, err, "test should read jobs.go")

	sql := string(body)
	require.Contains(t, sql, "RETURNING j.org_id, NULLIF(j.payload->>'session_id', '') AS session_id, j.job_type", "global recovery should keep job type available when applying session recovery metadata")
	require.Contains(t, sql, "AND uj.job_type IN ('run_agent', 'continue_session')", "global recovery should only mark sessions recovering for agent runtime jobs")
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

func TestJobStore_SandboxCapacitySummary(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("WITH fresh_workers").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"fresh_workers", "workers_with_slots", "live_sandboxes", "reserved_sandboxes", "max_sandboxes"}).
			AddRow(3, 2, 5, 1, 12))

	got, err := NewJobStore(mock).SandboxCapacitySummary(context.Background())

	require.NoError(t, err, "SandboxCapacitySummary should scan aggregate node metadata")
	require.Equal(t, SandboxCapacitySummary{
		FreshWorkers:      3,
		WorkersWithSlots:  2,
		LiveSandboxes:     5,
		ReservedSandboxes: 1,
		MaxSandboxes:      12,
	}, got, "SandboxCapacitySummary should return worker headroom")
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

func expectSandboxRoutingCandidateSavepoint(mock pgxmock.PgxPoolIface) {
	mock.ExpectExec(`SAVEPOINT sandbox_route_candidate`).
		WillReturnResult(pgxmock.NewResult("SAVEPOINT", 0))
}

func expectSandboxClaimCandidateSavepoint(mock pgxmock.PgxPoolIface) {
	mock.ExpectExec(`SAVEPOINT sandbox_claim_candidate`).
		WillReturnResult(pgxmock.NewResult("SAVEPOINT", 0))
}

func TestJobStore_RouteNextSandboxJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		jobType          string
		workloadClass    models.SandboxWorkloadClass
		orgSettings      string
		activeTurns      int
		jobAge           time.Duration
		candidateNodeID  string
		expectedReason   SandboxRoutingReason
		expectedDeferred bool
	}{
		{
			name:            "reserves an available worker for interactive work",
			jobType:         "run_agent",
			workloadClass:   models.SandboxWorkloadClassInteractive,
			candidateNodeID: "worker-with-space",
			expectedReason:  SandboxRoutingReasonReserved,
		},
		{
			name:             "waits ten seconds only when the fleet is full",
			jobType:          "continue_session",
			workloadClass:    models.SandboxWorkloadClassInteractive,
			expectedReason:   SandboxRoutingReasonFleetCapacity,
			expectedDeferred: true,
		},
		{
			name:             "defers code review at the shared org turn limit",
			jobType:          "continue_session",
			workloadClass:    models.SandboxWorkloadClassCodeReview,
			orgSettings:      `{"max_concurrent_runs":2}`,
			activeTurns:      2,
			jobAge:           sandboxRoutingTerminalProbeAge + time.Minute,
			expectedReason:   SandboxRoutingReasonOrgLimit,
			expectedDeferred: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			jobID, orgID := uuid.New(), uuid.New()
			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)SELECT j.id, j.org_id,.*effective_workload_class.*j.status,.*j.created_at.*FROM jobs j.*ORDER BY.*j.priority DESC,.*CASE WHEN j.workload_class = 'interactive' THEN 0 ELSE 1 END,.*j.run_at ASC,.*j.created_at ASC`).
				WithArgs(pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
					AddRow(jobID, orgID, tt.jobType, nil, tt.workloadClass, models.JobStatusPending, nil, time.Now().Add(-tt.jobAge)))
			expectSandboxRoutingCandidateSavepoint(mock)
			orgSettings := tt.orgSettings
			if orgSettings == "" {
				orgSettings = `{"max_concurrent_runs":3}`
			}
			mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations`).
				WithArgs(orgID).
				WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(orgSettings)))
			mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*id <>.*job_type IN`).
				WithArgs(orgID, jobID).
				WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(tt.activeTurns))
			if tt.expectedReason != SandboxRoutingReasonOrgLimit {
				candidateQuery := mock.ExpectQuery(`(?s)WITH excluded_capacity_nodes AS.*candidate_nodes AS.*raw_load AS.*reserved_job.status = 'pending'.*reserved_job.id <> @job_id.*reserved_job.status = 'running'.*reserved_job.id <> @job_id.*candidate_load AS.*SELECT id.*FROM candidate_load`).
					WithArgs(jobID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg(), tt.workloadClass)
				if tt.candidateNodeID == "" {
					candidateQuery.WillReturnRows(pgxmock.NewRows([]string{"id"}))
					if tt.expectedReason == SandboxRoutingReasonFleetCapacity {
						mock.ExpectQuery(`(?s)SELECT EXISTS.*max_active_sandboxes`).
							WithArgs(pgxmock.AnyArg()).
							WillReturnRows(pgxmock.NewRows([]string{"available"}).AddRow(true))
					}
				} else {
					candidateQuery.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(tt.candidateNodeID))
					mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock`).
						WithArgs(tt.candidateNodeID).
						WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
					mock.ExpectQuery(`(?s)SELECT.*FROM nodes n.*WHERE n.id =`).
						WithArgs(tt.candidateNodeID, pgxmock.AnyArg(), jobID).
						WillReturnRows(pgxmock.NewRows([]string{"live", "local_reserved", "sandbox_turn_local_reserved", "max_active", "interactive_reserved", "pending_durable_reserved", "running_durable_reserved", "shared_turn_reserved", "shared_non_turn_reserved"}).AddRow(1, 0, 0, 4, 1, 0, 0, 0, 0))
					mock.ExpectExec(`(?s)UPDATE jobs.*sandbox_slot_reserved_until =`).
						WithArgs(tt.workloadClass, tt.candidateNodeID, pgxmock.AnyArg(), jobID, models.JobStatusPending).
						WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				}
			}
			if tt.expectedDeferred {
				clearRetryWindow := tt.jobType == "continue_session" && tt.expectedReason == SandboxRoutingReasonOrgLimit
				mock.ExpectExec(`(?s)UPDATE jobs.*jsonb_set\(payload, '\{capacity_waited\}'.*retry_window_started_at = CASE.*WHEN.*clear_retry_window.*THEN NULL.*COALESCE\(retry_window_started_at, now\(\)\).*run_at =`).
					WithArgs(tt.workloadClass, clearRetryWindow, pgxmock.AnyArg(), jobID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}
			mock.ExpectCommit()
			mock.ExpectRollback()

			result, err := NewJobStore(mock).RouteNextSandboxJob(context.Background())
			require.NoError(t, err, "routing should make an atomic reservation or bounded deferral decision")
			require.NotNil(t, result, "routing should report the selected sandbox job")
			require.Equal(t, jobID, result.JobID, "routing result should identify the locked job")
			require.Equal(t, tt.workloadClass, result.WorkloadClass, "routing should preserve the job workload class")
			require.Equal(t, tt.expectedReason, result.Reason, "routing should report why the job was reserved or deferred")
			require.Equal(t, tt.expectedDeferred, result.Deferred, "routing should only defer for genuine fleet or org capacity")
			if tt.candidateNodeID == "" {
				require.Nil(t, result.TargetNodeID, "routing without capacity should not leave a stale worker target")
			} else {
				require.NotNil(t, result.TargetNodeID, "routing with capacity should return the reserved worker")
				require.Equal(t, tt.candidateNodeID, *result.TargetNodeID, "routing should return the worker whose slot was reserved")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "routing should keep the job, org decision, and worker reservation in one transaction")
		})
	}
}

func TestJobStore_RouteNextSandboxJobIsolatesInvalidOrgSettings(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, orgID := uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT j.id, j.org_id,.*FROM jobs j.*FOR UPDATE OF j SKIP LOCKED`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
			AddRow(jobID, orgID, "run_agent", nil, models.SandboxWorkloadClassInteractive, models.JobStatusPending, nil, time.Now()))
	expectSandboxRoutingCandidateSavepoint(mock)
	mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations.*FOR NO KEY UPDATE`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"session_automation":{"automatic_follow_through":{"pr_feedback_mode":"invalid"}}}`)))
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT sandbox_route_candidate`).WillReturnResult(pgxmock.NewResult("ROLLBACK", 0))
	mock.ExpectExec(`(?s)UPDATE jobs.*target_node_id = NULL.*sandbox_slot_reserved_until = NULL.*last_error = @last_error.*run_at = @run_at.*org_id = @org_id.*status = 'pending'`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), jobID, orgID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`RELEASE SAVEPOINT sandbox_route_candidate`).WillReturnResult(pgxmock.NewResult("RELEASE", 0))
	mock.ExpectCommit()
	mock.ExpectRollback()

	result, err := NewJobStore(mock).RouteNextSandboxJob(context.Background())
	require.NoError(t, err, "invalid settings on one job should not fail the fleet routing pass")
	require.NotNil(t, result, "isolated routing failure should return a durable deferral result")
	require.Equal(t, jobID, result.JobID, "routing failure should identify only the affected job")
	require.Equal(t, SandboxRoutingReasonJobError, result.Reason, "routing failure should expose its operational reason")
	require.True(t, result.Deferred, "routing failure should move the affected job behind other due work")
	require.Contains(t, result.RoutingError, "invalid PR feedback human mode", "routing result should preserve the actionable configuration error")
	require.NoError(t, mock.ExpectationsWereMet(), "invalid job configuration should roll back routing before deferring only that job")
}

func TestJobStore_RouteNextSandboxJobFallsBackWhenFleetCapacityMetadataIsMissing(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, orgID := uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT j.id, j.org_id,.*effective_workload_class.*j.status,.*j.created_at.*FROM jobs j`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
			AddRow(jobID, orgID, "run_agent", nil, models.SandboxWorkloadClassInteractive, models.JobStatusPending, nil, time.Now()))
	expectSandboxRoutingCandidateSavepoint(mock)
	mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"max_concurrent_runs":3}`)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*id <>.*job_type IN`).
		WithArgs(orgID, jobID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`(?s)WITH excluded_capacity_nodes AS.*candidate_nodes AS.*raw_load AS.*candidate_load AS.*SELECT id.*FROM candidate_load`).
		WithArgs(jobID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg(), models.SandboxWorkloadClassInteractive).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)SELECT EXISTS.*max_active_sandboxes`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"available"}).AddRow(false))
	mock.ExpectQuery(`(?s)WITH excluded_capacity_nodes AS.*candidate_nodes AS.*SELECT n.id.*FROM candidate_nodes n.*active_job_count`).
		WithArgs(jobID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("worker-without-capacity-metadata"))
	mock.ExpectExec(`(?s)UPDATE jobs.*target_node_id =.*sandbox_slot_reserved_until =.*status =`).
		WithArgs(models.SandboxWorkloadClassInteractive, "worker-without-capacity-metadata", (*time.Time)(nil), jobID, models.JobStatusPending).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	result, err := NewJobStore(mock).RouteNextSandboxJob(context.Background())
	require.NoError(t, err, "missing fleet metadata should use the compatibility route instead of waiting eight minutes")
	require.NotNil(t, result, "metadata fallback should report its placement")
	require.Equal(t, SandboxRoutingReasonMetadataFallback, result.Reason, "routing should expose missing fleet metadata as an operational signal")
	require.False(t, result.Deferred, "missing metadata should not masquerade as genuine fleet saturation")
	require.NotNil(t, result.TargetNodeID, "metadata fallback should select a fresh worker")
	require.Equal(t, "worker-without-capacity-metadata", *result.TargetNodeID, "metadata fallback should persist the selected compatibility worker")
	require.NoError(t, mock.ExpectationsWereMet(), "metadata fallback placement should commit atomically")
}

func TestJobStore_RouteNextSandboxJobUsesTerminalProbeAfterBoundedDeferral(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, orgID := uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT j.id, j.org_id,.*effective_workload_class.*j.status,.*j.created_at.*FROM jobs j`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
			AddRow(jobID, orgID, "run_agent", nil, models.SandboxWorkloadClassInteractive, models.JobStatusPending, time.Now().Add(-time.Minute), time.Now().Add(-sandboxRoutingTerminalProbeAge-time.Minute)))
	expectSandboxRoutingCandidateSavepoint(mock)
	mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"max_concurrent_runs":3}`)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*id <>.*job_type IN`).
		WithArgs(orgID, jobID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`(?s)WITH excluded_capacity_nodes AS.*candidate_nodes AS.*raw_load AS.*candidate_load AS.*SELECT id.*FROM candidate_load`).
		WithArgs(jobID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg(), models.SandboxWorkloadClassInteractive).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)WITH excluded_capacity_nodes AS.*candidate_nodes AS.*SELECT n.id.*FROM candidate_nodes n.*active_job_count`).
		WithArgs(jobID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("worker-terminal-probe"))
	mock.ExpectExec(`(?s)UPDATE jobs.*target_node_id =.*sandbox_slot_reserved_until = NULL.*retry_window_started_at =.*status =`).
		WithArgs(models.SandboxWorkloadClassInteractive, "worker-terminal-probe", pgxmock.AnyArg(), jobID, models.JobStatusPending).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	result, err := NewJobStore(mock).RouteNextSandboxJob(context.Background())
	require.NoError(t, err, "an over-age unrouted turn should be sent through the established claimed-job terminal path")
	require.NotNil(t, result, "routing should report the terminal probe placement")
	require.Equal(t, SandboxRoutingReasonTerminalProbe, result.Reason, "routing should distinguish a terminal capacity probe from a reserved slot")
	require.False(t, result.Deferred, "terminal probes should become claimable instead of waiting unboundedly")
	require.NotNil(t, result.TargetNodeID, "terminal probes should target a live worker")
	require.Equal(t, "worker-terminal-probe", *result.TargetNodeID, "terminal probes should use the selected live worker")
	require.NoError(t, mock.ExpectationsWereMet(), "terminal probe placement should explicitly clear stale reservation metadata")
}

func TestJobStore_RouteNextSandboxJobUsesTerminalProbeForFleetBlockedContinuation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, orgID := uuid.New(), uuid.New()
	mock.ExpectBegin()
	fleetWaitStartedAt := time.Now().Add(-sandboxRoutingTerminalProbeAge - time.Minute)
	mock.ExpectQuery(`(?s)SELECT j.id, j.org_id,.*effective_workload_class.*j.status,.*j.created_at.*FROM jobs j`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
			AddRow(jobID, orgID, "continue_session", nil, models.SandboxWorkloadClassInteractive, models.JobStatusPending, fleetWaitStartedAt, time.Now().Add(-2*sandboxRoutingTerminalProbeAge)))
	expectSandboxRoutingCandidateSavepoint(mock)
	mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"max_concurrent_runs":3}`)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*id <>.*job_type IN`).
		WithArgs(orgID, jobID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`(?s)WITH excluded_capacity_nodes AS.*candidate_nodes AS.*raw_load AS.*candidate_load AS.*SELECT id.*FROM candidate_load`).
		WithArgs(jobID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg(), models.SandboxWorkloadClassInteractive).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)WITH excluded_capacity_nodes AS.*candidate_nodes AS.*SELECT n.id.*FROM candidate_nodes n.*active_job_count`).
		WithArgs(jobID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("worker-continuation-terminal-probe"))
	mock.ExpectExec(`(?s)UPDATE jobs.*target_node_id =.*sandbox_slot_reserved_until = NULL.*retry_window_started_at =.*status =`).
		WithArgs(models.SandboxWorkloadClassInteractive, "worker-continuation-terminal-probe", fleetWaitStartedAt, jobID, models.JobStatusPending).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	result, err := NewJobStore(mock).RouteNextSandboxJob(context.Background())
	require.NoError(t, err, "a continuation blocked by genuine fleet saturation should reach its bounded handler path")
	require.NotNil(t, result, "routing should report the continuation terminal probe placement")
	require.Equal(t, SandboxRoutingReasonTerminalProbe, result.Reason, "fleet-blocked continuations should stop deferring after the bounded wait")
	require.False(t, result.Deferred, "terminal continuation probes should become claimable")
	require.NotNil(t, result.TargetNodeID, "terminal continuation probes should target a live worker")
	require.Equal(t, "worker-continuation-terminal-probe", *result.TargetNodeID, "terminal continuation probes should use the selected fallback worker")
	require.NoError(t, mock.ExpectationsWereMet(), "continuation terminal probing should persist the existing bounded retry marker")
}

func TestJobStore_RouteNextSandboxJobStartsFleetWindowAfterOrgWait(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, orgID := uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT j.id, j.org_id,.*effective_workload_class.*j.status,.*j.created_at.*FROM jobs j`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
			AddRow(jobID, orgID, "continue_session", nil, models.SandboxWorkloadClassInteractive, models.JobStatusPending, nil, time.Now().Add(-2*sandboxRoutingTerminalProbeAge)))
	expectSandboxRoutingCandidateSavepoint(mock)
	mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations.*FOR NO KEY UPDATE`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"max_concurrent_runs":3}`)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*id <>.*job_type IN`).
		WithArgs(orgID, jobID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`(?s)WITH excluded_capacity_nodes AS.*candidate_nodes AS.*raw_load AS.*candidate_load AS.*SELECT id.*FROM candidate_load`).
		WithArgs(jobID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg(), models.SandboxWorkloadClassInteractive).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)SELECT EXISTS.*max_active_sandboxes`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"available"}).AddRow(true))
	mock.ExpectExec(`(?s)UPDATE jobs.*jsonb_set\(payload, '\{capacity_waited\}'.*retry_window_started_at = CASE.*COALESCE\(retry_window_started_at, now\(\)\).*run_at =`).
		WithArgs(models.SandboxWorkloadClassInteractive, false, pgxmock.AnyArg(), jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	result, err := NewJobStore(mock).RouteNextSandboxJob(context.Background())
	require.NoError(t, err, "an aged continuation admitted after an organization wait should begin a fresh bounded fleet wait")
	require.NotNil(t, result, "routing should report the fleet-capacity deferral")
	require.Equal(t, SandboxRoutingReasonFleetCapacity, result.Reason, "routing should not treat old job age as fleet-wait age")
	require.True(t, result.Deferred, "the first fleet miss after an organization wait should defer normally")
	require.Nil(t, result.TargetNodeID, "a normal fleet deferral should not force a terminal worker placement")
	require.NoError(t, mock.ExpectationsWereMet(), "routing should start the fleet window only after organization admission succeeds")
}

func TestJobStore_RouteNextSandboxJobUsesTerminalProbeAtOrgLimitForInitialRun(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, orgID := uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT j.id, j.org_id,.*effective_workload_class.*j.status,.*j.created_at.*FROM jobs j`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
			AddRow(jobID, orgID, "run_agent", nil, models.SandboxWorkloadClassInteractive, models.JobStatusPending, time.Now().Add(-time.Minute), time.Now().Add(-sandboxRoutingTerminalProbeAge-time.Minute)))
	expectSandboxRoutingCandidateSavepoint(mock)
	mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"max_concurrent_runs":2}`)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*id <>.*job_type IN`).
		WithArgs(orgID, jobID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`(?s)WITH excluded_capacity_nodes AS.*candidate_nodes AS.*SELECT n.id.*FROM candidate_nodes n.*active_job_count`).
		WithArgs(jobID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("worker-org-terminal-probe"))
	mock.ExpectExec(`(?s)UPDATE jobs.*target_node_id =.*sandbox_slot_reserved_until = NULL.*retry_window_started_at =.*status =`).
		WithArgs(models.SandboxWorkloadClassInteractive, "worker-org-terminal-probe", pgxmock.AnyArg(), jobID, models.JobStatusPending).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	result, err := NewJobStore(mock).RouteNextSandboxJob(context.Background())
	require.NoError(t, err, "an initial run waiting beyond the org-limit window should reach its existing terminal handler")
	require.NotNil(t, result, "routing should report the org-limit terminal probe placement")
	require.Equal(t, SandboxRoutingReasonTerminalProbe, result.Reason, "over-age initial runs should stop deferring at the org limit")
	require.False(t, result.Deferred, "terminal probes should become claimable")
	require.NotNil(t, result.TargetNodeID, "terminal probes should target a live worker")
	require.Equal(t, "worker-org-terminal-probe", *result.TargetNodeID, "terminal probes should use the selected live worker")
	require.NoError(t, mock.ExpectationsWereMet(), "org-limit terminal routing should replace any ordinary retry timestamp with its durable terminal marker")
}

func TestJobStore_RouteNextSandboxJobBypassesOrgLimitForCancelledContinuation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, orgID, sessionID := uuid.New(), uuid.New(), uuid.New()
	createdAt := time.Now().Add(-time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT j.id, j.org_id,.*effective_workload_class.*j.status,.*j.created_at.*FROM jobs j`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
			AddRow(jobID, orgID, "continue_session", sessionID.String(), models.SandboxWorkloadClassCodeReview, models.JobStatusPending, nil, createdAt))
	expectSandboxRoutingCandidateSavepoint(mock)
	mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations.*FOR NO KEY UPDATE`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"max_concurrent_runs":1}`)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*id <>.*job_type IN`).
		WithArgs(orgID, jobID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT s.status.*session_cancel_requests.*session_threads.*JOIN jobs payload_job.*s.deleted_at IS NULL`).
		WithArgs(orgID, sessionID, jobID).
		WillReturnRows(pgxmock.NewRows([]string{"status", "pending_cancel", "stopped_thread", "capacity_cleanup"}).
			AddRow(models.SessionStatusRunning, true, false, false))
	mock.ExpectQuery(`(?s)WITH excluded_capacity_nodes AS.*candidate_nodes AS.*SELECT n.id.*FROM candidate_nodes n.*active_job_count`).
		WithArgs(jobID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("worker-cleanup"))
	mock.ExpectExec(`(?s)UPDATE jobs.*capacity_waited.*capacity_cleanup.*org_id = @org_id.*job_type = 'continue_session'`).
		WithArgs(jobID, orgID, models.JobStatusPending).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`(?s)UPDATE jobs.*jsonb_set\(payload, '\{capacity_waited\}'.*target_node_id =.*sandbox_slot_reserved_until = NULL.*retry_window_started_at =`).
		WithArgs(models.SandboxWorkloadClassCodeReview, "worker-cleanup", createdAt, jobID, models.JobStatusPending).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	result, err := NewJobStore(mock).RouteNextSandboxJob(context.Background())
	require.NoError(t, err, "cancelled continuation should route to lightweight cleanup despite the shared org limit")
	require.NotNil(t, result, "routing should report the cancellation cleanup placement")
	require.Equal(t, SandboxRoutingReasonTerminalProbe, result.Reason, "durable cancellation should use the terminal-probe path")
	require.False(t, result.Deferred, "cancelled continuation should not remain in the org-limit queue")
	require.NotNil(t, result.TargetNodeID, "cancellation cleanup should target a live worker")
	require.Equal(t, "worker-cleanup", *result.TargetNodeID, "cancellation cleanup should use the selected live worker")
	require.NoError(t, mock.ExpectationsWereMet(), "routing should inspect tenant-scoped cancellation before deferring")
}

func TestJobStore_RouteNextSandboxJobBypassesFleetCapacityForCancelledContinuation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, orgID, sessionID := uuid.New(), uuid.New(), uuid.New()
	createdAt := time.Now().Add(-time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT j.id, j.org_id,.*effective_workload_class.*j.status,.*j.created_at.*FROM jobs j`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "job_type", "session_id", "workload_class", "status", "retry_window_started_at", "created_at"}).
			AddRow(jobID, orgID, "continue_session", sessionID.String(), models.SandboxWorkloadClassInteractive, models.JobStatusPending, nil, createdAt))
	expectSandboxRoutingCandidateSavepoint(mock)
	mock.ExpectQuery(`(?s)SELECT origin.*FROM sessions.*org_id = @org_id.*id = @session_id`).
		WithArgs(orgID, sessionID).
		WillReturnRows(pgxmock.NewRows([]string{"origin"}).AddRow(models.SessionOriginManual))
	mock.ExpectQuery(`(?s)SELECT settings.*FROM organizations.*FOR NO KEY UPDATE`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"settings"}).AddRow([]byte(`{"max_concurrent_runs":3}`)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*id <>.*job_type IN`).
		WithArgs(orgID, jobID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`(?s)WITH excluded_capacity_nodes AS.*candidate_nodes AS.*raw_load AS.*candidate_load AS.*SELECT id.*FROM candidate_load`).
		WithArgs(jobID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg(), models.SandboxWorkloadClassInteractive).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)SELECT s.status.*session_cancel_requests.*session_threads.*JOIN jobs payload_job.*capacity_cleanup.*s.deleted_at IS NULL`).
		WithArgs(orgID, sessionID, jobID).
		WillReturnRows(pgxmock.NewRows([]string{"status", "pending_cancel", "stopped_thread", "capacity_cleanup"}).
			AddRow(models.SessionStatusRunning, true, false, false))
	mock.ExpectQuery(`(?s)WITH excluded_capacity_nodes AS.*candidate_nodes AS.*SELECT n.id.*FROM candidate_nodes n.*active_job_count`).
		WithArgs(jobID, orgID, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("worker-fleet-cleanup"))
	mock.ExpectExec(`(?s)UPDATE jobs.*capacity_waited.*capacity_cleanup.*org_id = @org_id.*job_type = 'continue_session'`).
		WithArgs(jobID, orgID, models.JobStatusPending).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec(`(?s)UPDATE jobs.*jsonb_set\(payload, '\{capacity_waited\}'.*target_node_id =.*sandbox_slot_reserved_until = NULL.*retry_window_started_at =`).
		WithArgs(models.SandboxWorkloadClassInteractive, "worker-fleet-cleanup", createdAt, jobID, models.JobStatusPending).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	result, err := NewJobStore(mock).RouteNextSandboxJob(context.Background())
	require.NoError(t, err, "cancelled continuation should bypass a full fleet for lightweight cleanup")
	require.NotNil(t, result, "routing should report the fleet cancellation cleanup placement")
	require.Equal(t, SandboxRoutingReasonTerminalProbe, result.Reason, "fleet cancellation should use the terminal-probe path")
	require.False(t, result.Deferred, "cancelled continuation should not wait for fleet capacity")
	require.NotNil(t, result.TargetNodeID, "fleet cancellation cleanup should target a live worker")
	require.Equal(t, "worker-fleet-cleanup", *result.TargetNodeID, "fleet cancellation cleanup should use the selected fallback worker")
	require.NoError(t, mock.ExpectationsWereMet(), "fleet routing should inspect durable cancellation before deferring")
}

func TestContinueSessionNeedsTerminalProbe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		status               models.SessionStatus
		pendingSessionCancel bool
		stoppedThread        bool
		capacityCleanup      bool
		expected             bool
	}{
		{name: "pending session cancellation", status: models.SessionStatusRunning, pendingSessionCancel: true, expected: true},
		{name: "non-resumable terminal session", status: models.SessionStatusSkipped, expected: true},
		{name: "cancelled thread", status: models.SessionStatusRunning, stoppedThread: true, expected: true},
		{name: "persisted capacity cleanup", status: models.SessionStatusRunning, capacityCleanup: true, expected: true},
		{name: "resumable session", status: models.SessionStatusFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			jobID, orgID, sessionID := uuid.New(), uuid.New(), uuid.New()
			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)SELECT s.status.*session_cancel_requests.*session_threads.*JOIN jobs payload_job.*s.deleted_at IS NULL`).
				WithArgs(orgID, sessionID, jobID).
				WillReturnRows(pgxmock.NewRows([]string{"status", "pending_cancel", "stopped_thread", "capacity_cleanup"}).
					AddRow(tt.status, tt.pendingSessionCancel, tt.stoppedThread, tt.capacityCleanup))
			mock.ExpectRollback()

			tx, err := mock.Begin(context.Background())
			require.NoError(t, err, "should begin durable-state probe transaction")
			actual, err := continueSessionNeedsTerminalProbe(context.Background(), tx, sandboxRoutingJob{
				ID:        jobID,
				OrgID:     orgID,
				JobType:   "continue_session",
				SessionID: &sessionID,
			})
			require.NoError(t, err, "durable-state probe should complete")
			require.Equal(t, tt.expected, actual, "probe should bypass admission only for terminal or cancelled continuation state")
			require.NoError(t, tx.Rollback(context.Background()), "test transaction should roll back cleanly")
			require.NoError(t, mock.ExpectationsWereMet(), "durable-state probe should stay scoped to job organization and session")
		})
	}
}

func TestResolveSandboxRoutingWorkloadClassUsesTypedSessionLookup(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, orgID, sessionID := uuid.New(), uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT origin.*FROM sessions.*org_id = @org_id.*id = @session_id`).
		WithArgs(orgID, sessionID).
		WillReturnRows(pgxmock.NewRows([]string{"origin"}).AddRow(models.SessionOriginCodeReview))
	mock.ExpectExec(`(?s)UPDATE jobs.*workload_class = @workload_class.*id = @job_id.*status = @status`).
		WithArgs(models.SandboxWorkloadClassCodeReview, jobID, models.JobStatusPending).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectRollback()

	tx, err := mock.Begin(context.Background())
	require.NoError(t, err, "should begin workload classification transaction")
	job := sandboxRoutingJob{
		ID:            jobID,
		OrgID:         orgID,
		SessionID:     &sessionID,
		WorkloadClass: models.SandboxWorkloadClassInteractive,
		Status:        models.JobStatusPending,
	}
	err = resolveSandboxRoutingWorkloadClass(context.Background(), tx, &job)
	require.NoError(t, err, "legacy review classification should use the typed session key")
	require.Equal(t, models.SandboxWorkloadClassCodeReview, job.WorkloadClass, "review sessions should repair the persisted workload class")
	require.NoError(t, tx.Rollback(context.Background()), "test transaction should roll back cleanly")
	require.NoError(t, mock.ExpectationsWereMet(), "classification should use the indexed org and UUID session lookup")
}

func TestSandboxRoutingCapacityLoadDoesNotDoubleCountRunningTurns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                     string
		live                     int
		localReserved            int
		sandboxTurnLocalReserved int
		pendingDurable           int
		runningDurable           int
		sharedTurnReserved       int
		sharedNonTurnReserved    int
		expected                 int
	}{
		{name: "running turn overlaps local reservation", localReserved: 1, sandboxTurnLocalReserved: 1, runningDurable: 1, expected: 1},
		{name: "pending reservation remains additive", localReserved: 1, sandboxTurnLocalReserved: 1, runningDurable: 1, pendingDurable: 1, expected: 2},
		{name: "preview reservation remains independent", localReserved: 2, sandboxTurnLocalReserved: 1, runningDurable: 1, expected: 2},
		{name: "shared preview reservation remains additive", runningDurable: 1, sharedNonTurnReserved: 1, expected: 2},
		{name: "heartbeat and shared preview reservation overlap", localReserved: 1, sharedNonTurnReserved: 1, expected: 1},
		{name: "heartbeat and shared turn reservation overlap", sandboxTurnLocalReserved: 1, sharedTurnReserved: 1, expected: 1},
		{name: "shared executor turns exceed main-process heartbeat", sandboxTurnLocalReserved: 1, sharedTurnReserved: 2, expected: 2},
		{name: "durable and shared turns are disjoint", runningDurable: 1, sharedTurnReserved: 1, expected: 2},
		{name: "heartbeat overlaps combined durable and shared turns", sandboxTurnLocalReserved: 2, runningDurable: 1, sharedTurnReserved: 1, expected: 2},
		{name: "durable reservation covers pre-admission heartbeat lag", runningDurable: 1, expected: 1},
		{name: "live sandboxes remain additive", live: 2, localReserved: 1, sandboxTurnLocalReserved: 1, runningDurable: 1, expected: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := sandboxRoutingCapacityLoad(tt.live, tt.localReserved, tt.sandboxTurnLocalReserved, tt.pendingDurable, tt.runningDurable, tt.sharedTurnReserved, tt.sharedNonTurnReserved)
			require.Equal(t, tt.expected, actual, "capacity load should count each distinct sandbox or reservation exactly once")
		})
	}
}

func TestJobStore_ReleaseSandboxSlotReservationWithLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, lockToken := uuid.New(), uuid.New()
	mock.ExpectExec(`(?s)UPDATE jobs.*sandbox_slot_reserved_until = NULL.*lock_token = \$2`).
		WithArgs(jobID, lockToken).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	released, err := NewJobStore(mock).ReleaseSandboxSlotReservationWithLease(context.Background(), jobID, lockToken)
	require.NoError(t, err, "fenced reservation release should succeed")
	require.True(t, released, "matching running job lease should release its durable worker slot")
	require.NoError(t, mock.ExpectationsWereMet(), "reservation release should require the current fencing token")
}

func TestJobStore_ReleaseSandboxRoutingPlacementWithLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, lockToken := uuid.New(), uuid.New()
	mock.ExpectExec(`(?s)UPDATE jobs.*target_node_id = NULL.*sandbox_slot_reserved_until = NULL.*lock_token = \$2.*sandbox_slot_reserved_until IS NOT NULL`).
		WithArgs(jobID, lockToken).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	released, err := NewJobStore(mock).ReleaseSandboxRoutingPlacementWithLease(context.Background(), jobID, lockToken)
	require.NoError(t, err, "fenced placement release should succeed")
	require.True(t, released, "matching running job lease should release its rejected pre-claim placement")
	require.NoError(t, mock.ExpectationsWereMet(), "placement release should clear the target only for a durable reservation owned by the current lease")
}

func TestJobStore_ClearSandboxRoutingPlacementWithLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, lockToken := uuid.New(), uuid.New()
	mock.ExpectExec(`(?s)UPDATE jobs.*target_node_id = NULL.*sandbox_slot_reserved_until = NULL.*lock_token = \$2`).
		WithArgs(jobID, lockToken).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	cleared, err := NewJobStore(mock).ClearSandboxRoutingPlacementWithLease(context.Background(), jobID, lockToken)
	require.NoError(t, err, "fenced fresh-sandbox failure cleanup should succeed")
	require.True(t, cleared, "matching running job lease should clear worker affinity even without a durable slot")
	require.NoError(t, mock.ExpectationsWereMet(), "fresh-sandbox failure cleanup should require the current fencing token")
}

func TestJobStore_ReserveSandboxSlotForRetryRequiresCurrentLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	jobID, staleLockToken := uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT j.id, j.org_id,.*effective_workload_class.*j.status,.*j.created_at.*lock_token = @lock_token`).
		WithArgs(jobID, staleLockToken).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	result, err := NewJobStore(mock).ReserveSandboxSlotForRetry(context.Background(), jobID, staleLockToken, "worker-old")
	require.Error(t, err, "a stale executor should not retarget a recovered job attempt")
	require.Nil(t, result, "stale lease routing should not return a placement")
	require.ErrorIs(t, err, pgx.ErrNoRows, "lease mismatch should be reported as lost ownership")
	require.NoError(t, mock.ExpectationsWereMet(), "retry routing should fence its locked select with the current lock token")
}

func TestJobStore_RerouteSandboxAfterStartupFailureRequiresCurrentLease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create startup-failure routing mock")
	defer mock.Close()

	jobID, staleLockToken := uuid.New(), uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT j.id, j.org_id,.*effective_workload_class.*j.status,.*j.created_at.*lock_token = @lock_token`).
		WithArgs(jobID, staleLockToken).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	result, err := NewJobStore(mock).RerouteSandboxAfterStartupFailure(context.Background(), jobID, staleLockToken, "worker-old")
	require.Error(t, err, "a stale startup attempt should not persist a failed-host exclusion")
	require.Nil(t, result, "stale startup rerouting should not return a placement")
	require.ErrorIs(t, err, pgx.ErrNoRows, "lease mismatch should be reported as lost ownership")
	require.NoError(t, mock.ExpectationsWereMet(), "failed-host persistence should be fenced before any payload mutation")
}

func TestJobStore_CountActiveSandboxTurnsByOrgUsesSharedPool(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*org_id = @org_id.*job_type IN.*status = 'running'`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(4))

	count, err := NewJobStore(mock).CountActiveSandboxTurnsByOrg(context.Background(), orgID)
	require.NoError(t, err, "shared turn count should succeed")
	require.Equal(t, 4, count, "shared turn count should include running turns across both workload classes")
	require.NoError(t, mock.ExpectationsWereMet(), "shared turn count should remain scoped to the organization")
}

func TestJobStore_CountAdmittedSandboxTurnsByOrgIncludesPendingReservations(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jobs.*org_id = @org_id.*job_type IN.*status = 'running'.*status = 'pending'.*sandbox_slot_reserved_until > now\(\)`).
		WithArgs(orgID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(5))

	count, err := NewJobStore(mock).CountAdmittedSandboxTurnsByOrg(context.Background(), orgID)
	require.NoError(t, err, "admitted turn count should succeed")
	require.Equal(t, 5, count, "runtime capacity should include running turns and pending durable reservations")
	require.NoError(t, mock.ExpectationsWereMet(), "admitted turn count should remain scoped to the organization")
}

func TestJobStore_IsSessionWaitingForSandboxCapacity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		waiting  bool
		expected bool
	}{
		{name: "reports durable capacity wait", waiting: true, expected: true},
		{name: "does not classify admitted reservation as waiting", expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			orgID, sessionID := uuid.New(), uuid.New()
			mock.ExpectQuery(`(?s)SELECT EXISTS.*FROM jobs.*org_id = @org_id.*payload->>'session_id' = @session_id.*sandbox_slot_reserved_until IS NULL.*sandbox_slot_reserved_until <= now\(\).*capacity_cleanup.*retry_window_started_at IS NOT DISTINCT FROM waiting_job.created_at.*terminal_probe_seconds.*COUNT\(\*\).*>= @max_concurrent_runs`).
				WithArgs(orgID, sessionID.String(), int(sandboxRoutingTerminalProbeAge.Seconds()), 2).
				WillReturnRows(pgxmock.NewRows([]string{"waiting"}).AddRow(tt.waiting))

			waiting, err := NewJobStore(mock).IsSessionWaitingForSandboxCapacity(context.Background(), orgID, sessionID, 2)
			require.NoError(t, err, "session capacity wait lookup should succeed")
			require.Equal(t, tt.expected, waiting, "session wait state should distinguish blocked work from its own reservation")
			require.NoError(t, mock.ExpectationsWereMet(), "session wait lookup should remain tenant scoped")
		})
	}
}

func TestJobStore_HasActiveCodeReviewSandboxTurn(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	mock.ExpectQuery(`(?s)SELECT EXISTS.*FROM jobs.*org_id = @org_id.*job_type IN.*workload_class = @workload_class.*status = 'running'.*sandbox_slot_reserved_until > now\(\)`).
		WithArgs(orgID, models.SandboxWorkloadClassCodeReview).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	exists, err := NewJobStore(mock).HasActiveCodeReviewSandboxTurn(context.Background(), orgID)
	require.NoError(t, err, "code review turn check should succeed")
	require.True(t, exists, "should report an admitted code-review turn")
	require.NoError(t, mock.ExpectationsWereMet(), "code review turn check should stay org-scoped and class-filtered")
}

func jobDedupeKeyPtr(s string) *string {
	return &s
}

func TestRunAgentEnqueueOptsPreservesSessionWorkloadClass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		origin   models.SessionOrigin
		expected models.SandboxWorkloadClass
	}{
		{name: "interactive uses rolling-deploy schema default", origin: models.SessionOriginManual},
		{name: "code review is persisted explicitly", origin: models.SessionOriginCodeReview, expected: models.SandboxWorkloadClassCodeReview},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := &models.Session{ID: uuid.New(), OrgID: uuid.New(), Origin: tt.origin}
			dedupeKey := RunAgentDedupeKey(session.ID)
			opts := RunAgentEnqueueOpts(session, 5, &dedupeKey)

			require.Equal(t, "run_agent", opts.JobType, "canonical enqueue policy should always schedule run_agent")
			require.Equal(t, tt.expected, opts.WorkloadClass, "canonical enqueue policy should preserve review classification while using the interactive schema default")
			require.Equal(t, &dedupeKey, opts.DedupeKey, "canonical enqueue policy should preserve caller deduplication")
		})
	}
}

func TestContinueSessionEnqueueOptsCentralizesSchedulingPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		session        *models.Session
		expectedClass  models.SandboxWorkloadClass
		expectedTarget *string
	}{
		{name: "missing session uses unpinned interactive fallback", expectedClass: models.SandboxWorkloadClassInteractive},
		{name: "interactive session retains its live worker", session: continuationSchedulingSession(models.SessionOriginManual, "worker-a"), expectedTarget: jobStringPtr("worker-a")},
		{name: "code review persists its workload class", session: continuationSchedulingSession(models.SessionOriginCodeReview, "worker-review"), expectedClass: models.SandboxWorkloadClassCodeReview, expectedTarget: jobStringPtr("worker-review")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			payload := map[string]string{"session_id": uuid.NewString()}
			dedupeKey := ContinueSessionDedupeKey(uuid.New())
			opts := ContinueSessionEnqueueOpts(tt.session, payload, &dedupeKey)

			require.Equal(t, "agent", opts.Queue, "canonical continuation policy should always use the agent queue")
			require.Equal(t, "continue_session", opts.JobType, "canonical continuation policy should always schedule a continuation")
			require.Equal(t, payload, opts.Payload, "canonical continuation policy should preserve the caller payload")
			require.Equal(t, 5, opts.Priority, "canonical continuation policy should preserve continuation priority")
			require.Equal(t, &dedupeKey, opts.DedupeKey, "canonical continuation policy should preserve caller deduplication")
			require.Equal(t, tt.expectedTarget, opts.TargetNodeID, "canonical continuation policy should derive affinity from the session runtime")
			require.Equal(t, tt.expectedClass, opts.WorkloadClass, "canonical continuation policy should persist code-review classification")
		})
	}
}

func continuationSchedulingSession(origin models.SessionOrigin, workerNodeID string) *models.Session {
	containerID := "sandbox-1"
	return &models.Session{Origin: origin, ContainerID: &containerID, WorkerNodeID: &workerNodeID}
}

func jobStringPtr(value string) *string {
	return &value
}

func TestOpenPRDedupeKeyUsesChangesetScope(t *testing.T) {
	t.Parallel()
	changesetID := uuid.New()
	require.Equal(t, "open_pr:"+changesetID.String(), OpenPRDedupeKey(changesetID), "open PR jobs should deduplicate by changeset PR slot")
}
