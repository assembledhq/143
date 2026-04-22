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
)

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
				mock.ExpectQuery("WITH next_job AS").
					WithArgs("test-node", "test-node", pgxmock.AnyArg(), int(defaultLeaseDuration.Seconds())).
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
			name: "draining worker skips claiming new jobs",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				w.RequestDrain()
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

			w.Register("hook_job", func(ctx context.Context, jobType string, got json.RawMessage) error {
				jobctx.RegisterDeadLetterHook(ctx, func(_ context.Context, err error) {
					fired++
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
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
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
		mock.ExpectQuery("WITH next_job AS").
			WithArgs("test-node", "test-node", pgxmock.AnyArg(), int(defaultLeaseDuration.Seconds())).
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
	mock.ExpectQuery("WITH next_job AS").
		WithArgs("test-node", "test-node", pgxmock.AnyArg(), int(defaultLeaseDuration.Seconds())).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "queue", "job_type", "payload", "priority", "status",
			"attempts", "max_attempts", "run_at", "locked_by_node_id", "locked_at",
			"lease_expires_at", "lock_token", "run_owner_id", "last_error",
			"dedupe_key", "created_at", "updated_at", "completed_at",
		}).AddRow(
			jobID, orgID, "default", jobType, payload, 5, "running",
			attempts, maxAttempts, createdAt, "test-node", createdAt, createdAt.Add(defaultLeaseDuration),
			lockToken.String(), "test-node", nil, nil, createdAt, createdAt, nil,
		))
}
