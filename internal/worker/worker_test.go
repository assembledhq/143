package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type wakeTestStore struct {
	claims atomic.Int32
}

func (s *wakeTestStore) ClaimNextRunnable(context.Context, string, string, uuid.UUID, time.Duration) (*models.Job, error) {
	s.claims.Add(1)
	return nil, nil
}

func (s *wakeTestStore) RenewLease(context.Context, uuid.UUID, uuid.UUID, time.Duration) (*models.Job, bool, error) {
	return nil, false, nil
}

func (s *wakeTestStore) MarkSucceededWithLease(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

func (s *wakeTestStore) MarkFailedWithLease(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	return false, nil
}

func (s *wakeTestStore) RetryWithLease(context.Context, uuid.UUID, uuid.UUID, string, time.Time) (bool, error) {
	return false, nil
}

func (s *wakeTestStore) RetryWithoutConsumingAttemptWithLease(context.Context, uuid.UUID, uuid.UUID, string, time.Time) (bool, error) {
	return false, nil
}

func (s *wakeTestStore) DeadLetterWithLease(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	return false, nil
}

func newTestWorker(t *testing.T) (*Worker, pgxmock.PgxPoolIface) {
	t.Helper()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")

	w := New(mock, zerolog.Nop(), "test-node")
	w.renewInterval = time.Hour
	return w, mock
}

func TestRetryableError(t *testing.T) {
	t.Parallel()

	cause := errors.New("capacity reached")
	retryable := &RetryableError{Err: cause}

	require.Equal(t, "capacity reached", retryable.Error(), "Error should return the wrapped error message")
	require.ErrorIs(t, retryable.Unwrap(), cause, "Unwrap should expose the wrapped error")
}

type nilStringPointerArg struct{}

func (nilStringPointerArg) Match(v interface{}) bool {
	targetNodeID, ok := v.(*string)
	return ok && targetNodeID == nil
}

func TestHandoffError(t *testing.T) {
	t.Parallel()

	cause := errors.New("executor owns job")
	handoff := &HandoffError{Err: cause}

	require.Equal(t, "executor owns job", handoff.Error(), "Error should return the wrapped error message")
	require.ErrorIs(t, handoff.Unwrap(), cause, "Unwrap should expose the wrapped error")
}

func TestWorker_Poll(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface)
	}{
		{
			name: "no pending jobs returns cleanly",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				mock.ExpectQuery("WITH unavailable_target_nodes AS").
					WithArgs(pgxmock.AnyArg(), "test-node", "test-node", pgxmock.AnyArg(), int(defaultLeaseDuration.Seconds())).
					WillReturnError(pgx.ErrNoRows)
			},
		},
		{
			name: "successful job is fenced and marked succeeded",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()

				jobID := uuid.New()
				lockToken := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				payload := json.RawMessage(`{"key":"value"}`)

				w.Register("test_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
					require.Equal(t, "test_job", jobType, "handler should receive the claimed job type")
					require.JSONEq(t, string(payload), string(got), "handler should receive the claimed payload")
					return nil
				})

				expectClaim(mock, jobID, orgID, "test_job", payload, now, lockToken)
				mock.ExpectExec("UPDATE jobs\\s+SET status = 'succeeded'").
					WithArgs(jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "dead target node is exposed to handler context",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()

				jobID := uuid.New()
				lockToken := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				targetNodeID := "dead-node"

				w.Register("recovery_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
					gotTarget, ok := jobctx.DeadTargetNodeFromContext(ctx)
					require.True(t, ok, "handler context should mark claims recovered from a dead target node")
					require.Equal(t, targetNodeID, gotTarget, "handler context should expose the dead target node id")
					return nil
				})

				expectClaimWithTarget(mock, jobID, orgID, "recovery_job", json.RawMessage(`{}`), now, lockToken, &targetNodeID)
				mock.ExpectExec("UPDATE jobs\\s+SET status = 'succeeded'").
					WithArgs(jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "unknown job type is marked failed behind the fencing token",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()

				jobID := uuid.New()
				lockToken := uuid.New()
				orgID := uuid.New()
				now := time.Now()

				expectClaim(mock, jobID, orgID, "unknown_job", json.RawMessage(`{}`), now, lockToken)
				mock.ExpectExec("UPDATE jobs\\s+SET status = 'failed'").
					WithArgs("no handler for job type: unknown_job", jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "plain failure retries with exponential backoff",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()

				jobID := uuid.New()
				lockToken := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				handlerErr := errors.New("temporary failure")

				w.Register("retry_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
					return handlerErr
				})

				expectClaim(mock, jobID, orgID, "retry_job", json.RawMessage(`{}`), now, lockToken)
				mock.ExpectExec("UPDATE jobs\\s+SET status = 'pending'").
					WithArgs(handlerErr.Error(), pgxmock.AnyArg(), jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "retryable failure preserves attempts",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()

				jobID := uuid.New()
				lockToken := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				handlerErr := &RetryableError{Err: errors.New("capacity reached")}

				w.Register("retryable_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
					return handlerErr
				})

				expectClaim(mock, jobID, orgID, "retryable_job", json.RawMessage(`{}`), now, lockToken)
				mock.ExpectExec("attempts = GREATEST\\(attempts - 1, 0\\)").
					WithArgs(handlerErr.Error(), pgxmock.AnyArg(), jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "retryable failure can clear stale target pin",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()

				jobID := uuid.New()
				lockToken := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				handlerErr := &RetryableError{
					Err:               errors.New("capacity reached"),
					ClearTargetNodeID: true,
				}

				w.Register("clear_target_retry_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
					return handlerErr
				})

				expectClaim(mock, jobID, orgID, "clear_target_retry_job", json.RawMessage(`{}`), now, lockToken)
				mock.ExpectExec("attempts = GREATEST\\(attempts - 1, 0\\)[\\s\\S]+target_node_id").
					WithArgs(handlerErr.Error(), pgxmock.AnyArg(), jobID, lockToken, nilStringPointerArg{}).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "retryable failure can consume attempts for exponential backoff",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()

				jobID := uuid.New()
				lockToken := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				handlerErr := &RetryableError{Err: errors.New("mergeability pending"), ConsumeAttempt: true}

				w.Register("attempt_consuming_retryable_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
					return handlerErr
				})

				expectClaim(mock, jobID, orgID, "attempt_consuming_retryable_job", json.RawMessage(`{}`), now, lockToken)
				mock.ExpectExec("run_at = \\$2,\\s+locked_by_node_id = NULL").
					WithArgs(handlerErr.Error(), pgxmock.AnyArg(), jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "retryable max duration bypass retries old job",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()

				jobID := uuid.New()
				lockToken := uuid.New()
				orgID := uuid.New()
				oldCreatedAt := time.Now().Add(-maxRetryableDuration - time.Minute)
				retryAfter := time.Second
				handlerErr := &RetryableError{
					Err:                    errors.New("stale orphan cleared"),
					RetryAfter:             &retryAfter,
					BypassMaxRetryDuration: true,
				}

				w.Register("stale_retry_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
					return handlerErr
				})

				expectClaim(mock, jobID, orgID, "stale_retry_job", json.RawMessage(`{}`), oldCreatedAt, lockToken)
				mock.ExpectExec("attempts = GREATEST\\(attempts - 1, 0\\)").
					WithArgs(handlerErr.Error(), pgxmock.AnyArg(), jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "retryable max duration bypass preserves target pin",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()

				jobID := uuid.New()
				lockToken := uuid.New()
				orgID := uuid.New()
				oldCreatedAt := time.Now().Add(-maxRetryableDuration - time.Minute)
				retryAfter := time.Second
				targetNodeID := "worker-host-c"
				handlerErr := &RetryableError{
					Err:                    errors.New("sandbox on different node"),
					RetryAfter:             &retryAfter,
					BypassMaxRetryDuration: true,
					TargetNodeID:           &targetNodeID,
				}

				w.Register("targeted_retry_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
					return handlerErr
				})

				expectClaim(mock, jobID, orgID, "targeted_retry_job", json.RawMessage(`{}`), oldCreatedAt, lockToken)
				mock.ExpectExec("attempts = GREATEST\\(attempts - 1, 0\\)[\\s\\S]+target_node_id").
					WithArgs(handlerErr.Error(), pgxmock.AnyArg(), jobID, lockToken, &targetNodeID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "fatal failure dead-letters immediately",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()

				jobID := uuid.New()
				lockToken := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				handlerErr := &FatalError{Err: errors.New("sandbox unavailable")}

				w.Register("fatal_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
					return handlerErr
				})

				expectClaim(mock, jobID, orgID, "fatal_job", json.RawMessage(`{}`), now, lockToken)
				mock.ExpectExec("UPDATE jobs\\s+SET status = 'dead_letter'").
					WithArgs(handlerErr.Error(), jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "retries exhausted dead-letter the job",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()

				jobID := uuid.New()
				lockToken := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				handlerErr := errors.New("permanent failure")

				w.Register("dead_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
					return handlerErr
				})

				expectClaimWithAttempts(mock, jobID, orgID, "dead_job", json.RawMessage(`{}`), now, lockToken, 3, 3)
				mock.ExpectExec("UPDATE jobs\\s+SET status = 'dead_letter'").
					WithArgs(handlerErr.Error(), jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "handoff error skips terminal write",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()

				jobID := uuid.New()
				lockToken := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				handlerErr := &HandoffError{Err: errors.New("session executor owns the job")}

				w.Register("run_agent", func(ctx context.Context, jobType string, got json.RawMessage) error {
					return handlerErr
				})

				expectClaim(mock, jobID, orgID, "run_agent", json.RawMessage(`{}`), now, lockToken)
			},
		},
		{
			name: "draining worker skips claiming new jobs",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				w.RequestDrain()
			},
		},
		{
			name: "claimed job without fencing token is ignored",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()

				jobID := uuid.New()
				orgID := uuid.New()
				now := time.Now()
				mock.ExpectQuery("WITH unavailable_target_nodes AS").
					WithArgs(pgxmock.AnyArg(), "test-node", "test-node", pgxmock.AnyArg(), int(defaultLeaseDuration.Seconds())).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "org_id", "queue", "job_type", "payload", "priority", "status",
						"attempts", "max_attempts", "run_at", "locked_by_node_id", "locked_at",
						"lease_expires_at", "lock_token", "run_owner_id", "owner_kind", "last_error",
						"dedupe_key", "target_node_id", "created_at", "updated_at", "completed_at",
					}).AddRow(
						jobID, orgID, "default", "missing_token", json.RawMessage(`{}`), 5, "running",
						1, 3, now, "test-node", now, now.Add(defaultLeaseDuration), nil, "test-node", string(models.JobOwnerKindWorker), nil, nil, nil, now, now, nil,
					))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w, mock := newTestWorker(t)
			defer mock.Close()

			tt.setupMock(t, w, mock)

			require.NotPanics(t, func() {
				w.poll(context.Background())
			}, "poll should not panic")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestWorker_Poll_LongRunningSessionJobContextHasWatchdog(t *testing.T) {
	t.Parallel()

	w, mock := newTestWorker(t)
	defer mock.Close()

	w.renewInterval = time.Hour
	w.maxLongRunningJobDuration = 20 * time.Millisecond

	jobID := uuid.New()
	lockToken := uuid.New()
	orgID := uuid.New()
	handlerCancelled := make(chan struct{})

	w.Register("run_agent", func(ctx context.Context, jobType string, got json.RawMessage) error {
		<-ctx.Done()
		close(handlerCancelled)
		return nil
	})

	expectClaim(mock, jobID, orgID, "run_agent", json.RawMessage(`{}`), time.Now(), lockToken)
	mock.ExpectExec("UPDATE jobs\\s+SET status = 'succeeded'").
		WithArgs(jobID, lockToken).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w.poll(context.Background())

	select {
	case <-handlerCancelled:
	default:
		t.Fatal("long-running session job handler should be cancelled by the worker watchdog")
	}
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestDefaultMaxLongRunningJobDuration_CoversConfiguredRuntimeCeiling(t *testing.T) {
	t.Parallel()

	expectedMinimum := time.Duration(models.MaxAbsoluteRuntimeCeilingSeconds)*time.Second + agent.HandlerCleanupBuffer
	require.GreaterOrEqual(t, defaultMaxLongRunningJobDuration, expectedMinimum, "worker watchdog should not fire before the largest valid handler runtime")
}

func TestWorker_Start_WakeTriggersPoll(t *testing.T) {
	t.Parallel()

	store := &wakeTestStore{}
	w := &Worker{
		jobs:          store,
		logger:        zerolog.Nop(),
		nodeID:        "test-node",
		handlers:      map[string]JobHandler{},
		pollInterval:  time.Hour,
		leaseDuration: defaultLeaseDuration,
		renewInterval: defaultRenewInterval,
		wakeCh:        make(chan struct{}, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Start(ctx)
	}()

	w.Wake()
	require.Eventually(t, func() bool {
		return store.claims.Load() > 0
	}, time.Second, 20*time.Millisecond, "worker wake-ups should trigger an immediate poll")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker should stop promptly after context cancellation")
	}
}

func TestWorker_Wake_DropsDuplicateSignalsWhenBufferFull(t *testing.T) {
	t.Parallel()

	w := &Worker{wakeCh: make(chan struct{}, 1)}
	w.Wake()
	require.NotPanics(t, func() {
		w.Wake()
	}, "Wake should drop duplicate signals instead of blocking when the channel buffer is full")
}

func TestWorker_Poll_RunsDeadLetterHooksOnTerminalPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		handlerErr  func() error
		createdAt   time.Time
		attempts    int
		maxAttempts int
		finalSQL    string
		finalArgs   int
		expectHooks int
	}{
		{
			name:        "fatal error fires dead-letter hook",
			handlerErr:  func() error { return &FatalError{Err: errors.New("docker down")} },
			createdAt:   time.Now(),
			attempts:    1,
			maxAttempts: 3,
			finalSQL:    "UPDATE jobs\\s+SET status = 'dead_letter'",
			finalArgs:   3,
			expectHooks: 1,
		},
		{
			name:        "retryable timeout fires dead-letter hook",
			handlerErr:  func() error { return &RetryableError{Err: errors.New("capacity reached")} },
			createdAt:   time.Now().Add(-10 * time.Minute),
			attempts:    1,
			maxAttempts: 3,
			finalSQL:    "UPDATE jobs\\s+SET status = 'dead_letter'",
			finalArgs:   3,
			expectHooks: 1,
		},
		{
			name:        "plain retry does not fire dead-letter hook",
			handlerErr:  func() error { return errors.New("temporary failure") },
			createdAt:   time.Now(),
			attempts:    1,
			maxAttempts: 3,
			finalSQL:    "UPDATE jobs\\s+SET status = 'pending'",
			finalArgs:   4,
			expectHooks: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w, mock := newTestWorker(t)
			defer mock.Close()

			jobID := uuid.New()
			lockToken := uuid.New()
			orgID := uuid.New()
			var fired int
			var hookCtxErr error

			w.Register("hook_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
				jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, err error) {
					fired++
					hookCtxErr = hookCtx.Err()
				})
				return tt.handlerErr()
			})

			expectClaimWithAttempts(mock, jobID, orgID, "hook_job", json.RawMessage(`{}`), tt.createdAt, lockToken, tt.attempts, tt.maxAttempts)
			switch tt.finalArgs {
			case 3:
				mock.ExpectExec(tt.finalSQL).
					WithArgs(pgxmock.AnyArg(), jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			case 4:
				mock.ExpectExec(tt.finalSQL).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), jobID, lockToken).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			}

			w.poll(context.Background())

			require.Equal(t, tt.expectHooks, fired, "dead-letter hooks should fire only on terminal give-up paths")
			if tt.expectHooks > 0 {
				require.NoError(t, hookCtxErr, "dead-letter hooks should receive a live context for terminal cleanup writes")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestWorker_Poll_DeadLetterHooksRunWithLiveContext(t *testing.T) {
	t.Parallel()

	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	lockToken := uuid.New()
	orgID := uuid.New()
	var hookErr error

	w.Register("hook_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
		jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, err error) {
			hookErr = hookCtx.Err()
		})
		return &FatalError{Err: errors.New("permanent failure")}
	})

	expectClaim(mock, jobID, orgID, "hook_job", json.RawMessage(`{}`), time.Now(), lockToken)
	mock.ExpectExec("UPDATE jobs\\s+SET status = 'dead_letter'").
		WithArgs(pgxmock.AnyArg(), jobID, lockToken).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w.poll(context.Background())

	require.NoError(t, hookErr, "dead-letter hooks should receive a live context for terminal cleanup writes")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorker_Poll_LostLeaseSkipsTerminalWrite(t *testing.T) {
	t.Parallel()

	w, mock := newTestWorker(t)
	defer mock.Close()

	w.renewInterval = 5 * time.Millisecond

	jobID := uuid.New()
	lockToken := uuid.New()
	orgID := uuid.New()
	releaseHandler := make(chan struct{})

	w.Register("slow_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
		<-ctx.Done()
		close(releaseHandler)
		return nil
	})

	expectClaim(mock, jobID, orgID, "slow_job", json.RawMessage(`{}`), time.Now(), lockToken)
	mock.ExpectQuery("UPDATE jobs SET lease_expires_at = now\\(\\) \\+").
		WithArgs(int(defaultLeaseDuration.Seconds()), jobID, lockToken).
		WillReturnError(pgx.ErrNoRows)

	w.poll(context.Background())

	select {
	case <-releaseHandler:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("handler should be cancelled when lease ownership is lost")
	}
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

type renewLeaseStoreStub struct {
	renewLeaseFn func(ctx context.Context, jobID, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, bool, error)
}

func (s *renewLeaseStoreStub) ClaimNextRunnable(ctx context.Context, nodeID, ownerID string, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, error) {
	return nil, nil
}

func (s *renewLeaseStoreStub) RenewLease(ctx context.Context, jobID, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, bool, error) {
	return s.renewLeaseFn(ctx, jobID, lockToken, leaseDuration)
}

func (s *renewLeaseStoreStub) MarkSucceededWithLease(ctx context.Context, jobID, lockToken uuid.UUID) (bool, error) {
	return false, nil
}

func (s *renewLeaseStoreStub) MarkFailedWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) (bool, error) {
	return false, nil
}

func (s *renewLeaseStoreStub) RetryWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error) {
	return false, nil
}

func (s *renewLeaseStoreStub) RetryWithoutConsumingAttemptWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error) {
	return false, nil
}

func (s *renewLeaseStoreStub) DeadLetterWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) (bool, error) {
	return false, nil
}

func TestWorker_RenewLeaseLoop_CancelsAfterLeaseExpiryOnRenewErrors(t *testing.T) {
	t.Parallel()

	var renewCalls atomic.Int32
	store := &renewLeaseStoreStub{
		renewLeaseFn: func(ctx context.Context, jobID, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, bool, error) {
			renewCalls.Add(1)
			return nil, false, errors.New("database unavailable")
		},
	}
	w := &Worker{
		jobs:          store,
		logger:        zerolog.Nop(),
		leaseDuration: 25 * time.Millisecond,
		renewInterval: 5 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobID := uuid.New()
	lockToken := uuid.New()
	var lostOwnership atomic.Bool
	done := make(chan struct{})
	handlerCancelled := make(chan struct{}, 1)
	initialLeaseExpiry := time.Now().Add(20 * time.Millisecond)

	go w.renewLeaseLoop(ctx, jobID, lockToken, initialLeaseExpiry, &lostOwnership, func() {
		handlerCancelled <- struct{}{}
		cancel()
	}, done)

	select {
	case <-handlerCancelled:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("renewLeaseLoop should cancel the handler once lease renewal has failed past expiry")
	}

	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("renewLeaseLoop should stop after declaring ownership lost")
	}

	require.True(t, lostOwnership.Load(), "renewLeaseLoop should mark ownership lost after the lease window expires")
	require.GreaterOrEqual(t, renewCalls.Load(), int32(1), "renewLeaseLoop should attempt at least one renewal before giving up")
}

func TestWorker_Start_StopsOnContextCancel(t *testing.T) {
	t.Parallel()

	w, mock := newTestWorker(t)
	defer mock.Close()

	w.pollInterval = 10 * time.Millisecond
	mock.MatchExpectationsInOrder(false)
	for range 5 {
		mock.ExpectQuery("WITH unavailable_target_nodes AS").
			WithArgs(pgxmock.AnyArg(), "test-node", "test-node", pgxmock.AnyArg(), int(defaultLeaseDuration.Seconds())).
			WillReturnError(pgx.ErrNoRows)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker should stop when context is cancelled")
	}
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

type terminalLeaseStoreStub struct {
	markSucceededFn  func(ctx context.Context, jobID, lockToken uuid.UUID) (bool, error)
	markFailedFn     func(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) (bool, error)
	retryFn          func(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error)
	retryNoAttemptFn func(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error)
	deadLetterFn     func(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) (bool, error)
}

func (s *terminalLeaseStoreStub) ClaimNextRunnable(ctx context.Context, nodeID, ownerID string, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, error) {
	return nil, nil
}

func (s *terminalLeaseStoreStub) RenewLease(ctx context.Context, jobID, lockToken uuid.UUID, leaseDuration time.Duration) (*models.Job, bool, error) {
	return nil, false, nil
}

func (s *terminalLeaseStoreStub) MarkSucceededWithLease(ctx context.Context, jobID, lockToken uuid.UUID) (bool, error) {
	return s.markSucceededFn(ctx, jobID, lockToken)
}

func (s *terminalLeaseStoreStub) MarkFailedWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) (bool, error) {
	return s.markFailedFn(ctx, jobID, lockToken, errMsg)
}

func (s *terminalLeaseStoreStub) RetryWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error) {
	return s.retryFn(ctx, jobID, lockToken, errMsg, runAt)
}

func (s *terminalLeaseStoreStub) RetryWithoutConsumingAttemptWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error) {
	return s.retryNoAttemptFn(ctx, jobID, lockToken, errMsg, runAt)
}

func (s *terminalLeaseStoreStub) DeadLetterWithLease(ctx context.Context, jobID, lockToken uuid.UUID, errMsg string) (bool, error) {
	return s.deadLetterFn(ctx, jobID, lockToken, errMsg)
}

func TestWorker_StateAccessors(t *testing.T) {
	t.Parallel()

	w := &Worker{}
	require.False(t, w.IsDraining(), "IsDraining should default to false")
	require.Equal(t, 0, w.ActiveJobCount(), "ActiveJobCount should default to zero")
	require.Equal(t, 0, w.ActiveRunAgentCount(), "ActiveRunAgentCount should default to zero")

	w.RequestDrain()
	w.activeJobs.Add(2)
	w.activeRunAgentJobs.Add(1)

	require.True(t, w.IsDraining(), "RequestDrain should flip the drain bit")
	require.Equal(t, 2, w.ActiveJobCount(), "ActiveJobCount should expose the current counter")
	require.Equal(t, 1, w.ActiveRunAgentCount(), "ActiveRunAgentCount should expose the current run-agent counter")
}

func TestWorker_TerminalWriteHelpers(t *testing.T) {
	t.Parallel()

	jobID := uuid.New()
	lockToken := uuid.New()

	tests := []struct {
		name string
		run  func(w *Worker)
	}{
		{
			name: "succeedJob tolerates errors",
			run: func(w *Worker) {
				w.succeedJob(context.Background(), jobID, lockToken)
			},
		},
		{
			name: "failJob tolerates lost ownership",
			run: func(w *Worker) {
				w.failJob(context.Background(), jobID, lockToken, "boom")
			},
		},
		{
			name: "retryJob handles schedule errors",
			run: func(w *Worker) {
				w.retryJob(context.Background(), jobID, lockToken, "boom", 1, false)
			},
		},
		{
			name: "retryJob handles lost ownership for preserved attempts",
			run: func(w *Worker) {
				w.retryJob(context.Background(), jobID, lockToken, "boom", 1, true)
			},
		},
		{
			name: "deadLetterJob tolerates lost ownership",
			run: func(w *Worker) {
				w.deadLetterJob(context.Background(), jobID, lockToken, "boom")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &terminalLeaseStoreStub{
				markSucceededFn: func(ctx context.Context, gotJobID, gotLockToken uuid.UUID) (bool, error) {
					require.Equal(t, jobID, gotJobID, "succeedJob should pass the claimed job ID")
					require.Equal(t, lockToken, gotLockToken, "succeedJob should pass the claimed fencing token")
					return false, errors.New("write failed")
				},
				markFailedFn: func(ctx context.Context, gotJobID, gotLockToken uuid.UUID, errMsg string) (bool, error) {
					require.Equal(t, "boom", errMsg, "failJob should pass the failure message through")
					return false, nil
				},
				retryFn: func(ctx context.Context, gotJobID, gotLockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error) {
					require.Equal(t, "boom", errMsg, "retryJob should pass the retry error through")
					return false, errors.New("schedule failed")
				},
				retryNoAttemptFn: func(ctx context.Context, gotJobID, gotLockToken uuid.UUID, errMsg string, runAt time.Time) (bool, error) {
					require.Equal(t, "boom", errMsg, "retryJob should pass the retry error through")
					return false, nil
				},
				deadLetterFn: func(ctx context.Context, gotJobID, gotLockToken uuid.UUID, errMsg string) (bool, error) {
					require.Equal(t, "boom", errMsg, "deadLetterJob should pass the dead-letter message through")
					return false, nil
				},
			}
			w := &Worker{jobs: store, logger: zerolog.Nop()}
			tt.run(w)
		})
	}
}

func TestRetryBackoff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		attempt     int
		expectedSec int
	}{
		{name: "attempt zero waits one second", attempt: 0, expectedSec: 1},
		{name: "attempt one waits two seconds", attempt: 1, expectedSec: 2},
		{name: "attempt two waits four seconds", attempt: 2, expectedSec: 4},
		{name: "attempt ten is capped at 1024 seconds", attempt: 10, expectedSec: 1024},
		{name: "attempt above ten stays capped", attempt: 20, expectedSec: 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, time.Duration(tt.expectedSec)*time.Second, retryBackoff(tt.attempt), "retryBackoff should follow the capped exponential schedule")
		})
	}
}

func expectClaim(mock pgxmock.PgxPoolIface, jobID, orgID uuid.UUID, jobType string, payload json.RawMessage, createdAt time.Time, lockToken uuid.UUID) {
	expectClaimWithAttempts(mock, jobID, orgID, jobType, payload, createdAt, lockToken, 1, 3)
}

func expectClaimWithAttempts(mock pgxmock.PgxPoolIface, jobID, orgID uuid.UUID, jobType string, payload json.RawMessage, createdAt time.Time, lockToken uuid.UUID, attempts, maxAttempts int) {
	expectClaimWithAttemptsAndTarget(mock, jobID, orgID, jobType, payload, createdAt, lockToken, attempts, maxAttempts, nil)
}

func expectClaimWithTarget(mock pgxmock.PgxPoolIface, jobID, orgID uuid.UUID, jobType string, payload json.RawMessage, createdAt time.Time, lockToken uuid.UUID, targetNodeID *string) {
	expectClaimWithAttemptsAndTarget(mock, jobID, orgID, jobType, payload, createdAt, lockToken, 1, 3, targetNodeID)
}

func expectClaimWithAttemptsAndTarget(mock pgxmock.PgxPoolIface, jobID, orgID uuid.UUID, jobType string, payload json.RawMessage, createdAt time.Time, lockToken uuid.UUID, attempts, maxAttempts int, targetNodeID *string) {
	var target any
	if targetNodeID != nil {
		target = *targetNodeID
	}
	mock.ExpectQuery("WITH unavailable_target_nodes AS").
		WithArgs(pgxmock.AnyArg(), "test-node", "test-node", pgxmock.AnyArg(), int(defaultLeaseDuration.Seconds())).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "queue", "job_type", "payload", "priority", "status",
			"attempts", "max_attempts", "run_at", "locked_by_node_id", "locked_at",
			"lease_expires_at", "lock_token", "run_owner_id", "owner_kind", "last_error",
			"dedupe_key", "target_node_id", "created_at", "updated_at", "completed_at",
		}).AddRow(
			jobID, orgID, "default", jobType, payload, 5, "running",
			attempts, maxAttempts, createdAt, "test-node", createdAt, createdAt.Add(defaultLeaseDuration),
			lockToken.String(), "test-node", string(models.JobOwnerKindWorker), nil, nil, target, createdAt, createdAt, nil,
		))
}
