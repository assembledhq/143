package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newTestWorker(t *testing.T) (*Worker, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	w := New(mock, zerolog.Nop(), "test-node")
	return w, mock
}

func TestRetryableError(t *testing.T) {
	t.Parallel()

	cause := errors.New("capacity reached")
	retryable := &RetryableError{Err: cause}

	require.Equal(t, "capacity reached", retryable.Error(), "Error should return the wrapped error message")
	require.ErrorIs(t, retryable.Unwrap(), cause, "Unwrap should expose the wrapped error")
}

// ---------------------------------------------------------------------------
// Direct tests for job lifecycle methods
// ---------------------------------------------------------------------------

func TestWorker_SucceedJob(t *testing.T) {
	t.Parallel()

	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()

	mock.ExpectExec("UPDATE jobs SET status = 'succeeded'").
		WithArgs(jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w.succeedJob(context.Background(), jobID)

	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorker_FailJob(t *testing.T) {
	t.Parallel()

	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	errMsg := "something went wrong"

	mock.ExpectExec("UPDATE jobs SET status = 'failed'").
		WithArgs(errMsg, jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w.failJob(context.Background(), jobID, errMsg)

	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorker_RetryJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		attempt     int
		expectedSec int
	}{
		{name: "attempt 0 backs off 1 second", attempt: 0, expectedSec: 1},
		{name: "attempt 1 backs off 2 seconds", attempt: 1, expectedSec: 2},
		{name: "attempt 2 backs off 4 seconds", attempt: 2, expectedSec: 4},
		{name: "attempt 3 backs off 8 seconds", attempt: 3, expectedSec: 8},
		{name: "attempt 5 backs off 32 seconds", attempt: 5, expectedSec: 32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w, mock := newTestWorker(t)
			defer mock.Close()

			jobID := uuid.New()
			errMsg := "transient error"
			intervalStr := fmt.Sprintf("%d seconds", tt.expectedSec)

			mock.ExpectExec("UPDATE jobs SET status = 'pending'").
				WithArgs(errMsg, intervalStr, jobID).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			w.retryJob(context.Background(), jobID, errMsg, tt.attempt)

			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestWorker_DeadLetterJob(t *testing.T) {
	t.Parallel()

	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	errMsg := "max attempts exceeded"

	mock.ExpectExec("UPDATE jobs SET status = 'dead_letter'").
		WithArgs(errMsg, jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w.deadLetterJob(context.Background(), jobID, errMsg)

	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorker_LifecycleMethodsLogWarningOnExecFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		expectSQL     string
		invoke        func(w *Worker, ctx context.Context, jobID uuid.UUID)
		expectedArg1  string
	}{
		{
			name:      "succeedJob logs warning when update fails",
			expectSQL: "UPDATE jobs SET status = 'succeeded'",
			invoke: func(w *Worker, ctx context.Context, jobID uuid.UUID) {
				w.succeedJob(ctx, jobID)
			},
		},
		{
			name:      "failJob logs warning when update fails",
			expectSQL: "UPDATE jobs SET status = 'failed'",
			invoke: func(w *Worker, ctx context.Context, jobID uuid.UUID) {
				w.failJob(ctx, jobID, "boom")
			},
			expectedArg1: "boom",
		},
		{
			name:      "deadLetterJob logs warning when update fails",
			expectSQL: "UPDATE jobs SET status = 'dead_letter'",
			invoke: func(w *Worker, ctx context.Context, jobID uuid.UUID) {
				w.deadLetterJob(ctx, jobID, "boom")
			},
			expectedArg1: "boom",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w, mock := newTestWorker(t)
			defer mock.Close()

			jobID := uuid.New()
			if tt.expectedArg1 == "" {
				mock.ExpectExec(tt.expectSQL).
					WithArgs(jobID).
					WillReturnError(errors.New("write failed"))
			} else {
				mock.ExpectExec(tt.expectSQL).
					WithArgs(tt.expectedArg1, jobID).
					WillReturnError(errors.New("write failed"))
			}

			require.NotPanics(t, func() {
				tt.invoke(w, context.Background(), jobID)
			}, "lifecycle helpers should swallow best-effort update failures after logging")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

// ---------------------------------------------------------------------------
// Poll tests — consolidated into a single table-driven test
// ---------------------------------------------------------------------------

func TestWorker_Poll(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface)
	}{
		{
			name: "begin error does not panic",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				mock.ExpectBegin().WillReturnError(errors.New("connection refused"))
			},
		},
		{
			name: "no pending jobs rolls back gracefully",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT .+ FROM jobs").
					WillReturnError(pgx.ErrNoRows)
				mock.ExpectRollback()
			},
		},
		{
			name: "successful job is marked succeeded",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				jobID := uuid.New()
				orgID := uuid.New()
				payload := json.RawMessage(`{"key":"value"}`)

				w.Register("test_job", func(ctx context.Context, jobType string, p json.RawMessage) error {
					require.Equal(t, "test_job", jobType, "handler should receive correct job type")
					require.JSONEq(t, `{"key":"value"}`, string(p), "handler should receive correct payload")
					return nil
				})

				mock.ExpectBegin()
				rows := pgxmock.NewRows([]string{"id", "org_id", "job_type", "payload", "attempts", "max_attempts"}).
					AddRow(jobID, orgID, "test_job", payload, 0, 3)
				mock.ExpectQuery("SELECT .+ FROM jobs").
					WillReturnRows(rows)
				mock.ExpectExec("UPDATE jobs SET status = 'running'").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
				mock.ExpectExec("UPDATE jobs SET status = 'succeeded'").
					WithArgs(jobID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "failed job with remaining attempts is retried",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				jobID := uuid.New()
				orgID := uuid.New()
				payload := json.RawMessage(`{}`)
				handlerErr := errors.New("temporary failure")

				w.Register("retry_job", func(ctx context.Context, jobType string, p json.RawMessage) error {
					return handlerErr
				})

				mock.ExpectBegin()
				rows := pgxmock.NewRows([]string{"id", "org_id", "job_type", "payload", "attempts", "max_attempts"}).
					AddRow(jobID, orgID, "retry_job", payload, 0, 3)
				mock.ExpectQuery("SELECT .+ FROM jobs").
					WillReturnRows(rows)
				mock.ExpectExec("UPDATE jobs SET status = 'running'").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
				// retryJob with attempt=1 => backoff = 2^1 = 2 seconds
				mock.ExpectExec("UPDATE jobs SET status = 'pending'").
					WithArgs(handlerErr.Error(), "2 seconds", jobID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "failed job at max attempts is dead lettered",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				jobID := uuid.New()
				orgID := uuid.New()
				payload := json.RawMessage(`{}`)
				handlerErr := errors.New("permanent failure")

				w.Register("dead_job", func(ctx context.Context, jobType string, p json.RawMessage) error {
					return handlerErr
				})

				mock.ExpectBegin()
				rows := pgxmock.NewRows([]string{"id", "org_id", "job_type", "payload", "attempts", "max_attempts"}).
					AddRow(jobID, orgID, "dead_job", payload, 2, 3)
				mock.ExpectQuery("SELECT .+ FROM jobs").
					WillReturnRows(rows)
				mock.ExpectExec("UPDATE jobs SET status = 'running'").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
				mock.ExpectExec("UPDATE jobs SET status = 'dead_letter'").
					WithArgs(handlerErr.Error(), jobID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "no handler registered fails the job",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				jobID := uuid.New()
				orgID := uuid.New()
				payload := json.RawMessage(`{}`)

				mock.ExpectBegin()
				rows := pgxmock.NewRows([]string{"id", "org_id", "job_type", "payload", "attempts", "max_attempts"}).
					AddRow(jobID, orgID, "unknown_job", payload, 0, 3)
				mock.ExpectQuery("SELECT .+ FROM jobs").
					WillReturnRows(rows)
				mock.ExpectExec("UPDATE jobs SET status = 'running'").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
				mock.ExpectExec("UPDATE jobs SET status = 'failed'").
					WithArgs("no handler for job type: unknown_job", jobID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "query error rolls back gracefully",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				mock.ExpectBegin()
				mock.ExpectQuery("SELECT .+ FROM jobs").
					WillReturnError(errors.New("database error"))
				mock.ExpectRollback()
			},
		},
		{
			name: "lock exec error rolls back gracefully",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				jobID := uuid.New()
				orgID := uuid.New()
				payload := json.RawMessage(`{}`)

				mock.ExpectBegin()
				rows := pgxmock.NewRows([]string{"id", "org_id", "job_type", "payload", "attempts", "max_attempts"}).
					AddRow(jobID, orgID, "some_job", payload, 0, 3)
				mock.ExpectQuery("SELECT .+ FROM jobs").
					WillReturnRows(rows)
				mock.ExpectExec("UPDATE jobs SET status = 'running'").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("lock failed"))
				mock.ExpectRollback()
			},
		},
		{
			name: "commit error rolls back gracefully",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				jobID := uuid.New()
				orgID := uuid.New()
				payload := json.RawMessage(`{}`)

				mock.ExpectBegin()
				rows := pgxmock.NewRows([]string{"id", "org_id", "job_type", "payload", "attempts", "max_attempts"}).
					AddRow(jobID, orgID, "some_job", payload, 0, 3)
				mock.ExpectQuery("SELECT .+ FROM jobs").
					WillReturnRows(rows)
				mock.ExpectExec("UPDATE jobs SET status = 'running'").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
				mock.ExpectRollback()
			},
		},
		{
			name: "dead letter at exact boundary (attempts+1 == maxAttempts)",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				jobID := uuid.New()
				orgID := uuid.New()
				payload := json.RawMessage(`{}`)
				handlerErr := errors.New("boundary failure")

				w.Register("boundary_job", func(ctx context.Context, jobType string, p json.RawMessage) error {
					return handlerErr
				})

				mock.ExpectBegin()
				rows := pgxmock.NewRows([]string{"id", "org_id", "job_type", "payload", "attempts", "max_attempts"}).
					AddRow(jobID, orgID, "boundary_job", payload, 1, 2)
				mock.ExpectQuery("SELECT .+ FROM jobs").
					WillReturnRows(rows)
				mock.ExpectExec("UPDATE jobs SET status = 'running'").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
				mock.ExpectExec("UPDATE jobs SET status = 'dead_letter'").
					WithArgs(handlerErr.Error(), jobID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "retry just below boundary (attempts+1 < maxAttempts)",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				jobID := uuid.New()
				orgID := uuid.New()
				payload := json.RawMessage(`{}`)
				handlerErr := errors.New("retryable failure")

				w.Register("retry_boundary_job", func(ctx context.Context, jobType string, p json.RawMessage) error {
					return handlerErr
				})

				mock.ExpectBegin()
				rows := pgxmock.NewRows([]string{"id", "org_id", "job_type", "payload", "attempts", "max_attempts"}).
					AddRow(jobID, orgID, "retry_boundary_job", payload, 0, 2)
				mock.ExpectQuery("SELECT .+ FROM jobs").
					WillReturnRows(rows)
				mock.ExpectExec("UPDATE jobs SET status = 'running'").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
				// retryJob with attempt=1 => backoff = 2^1 = 2 seconds
				mock.ExpectExec("UPDATE jobs SET status = 'pending'").
					WithArgs(handlerErr.Error(), "2 seconds", jobID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "fatal error dead-letters immediately without retrying",
			setupMock: func(t *testing.T, w *Worker, mock pgxmock.PgxPoolIface) {
				t.Helper()
				jobID := uuid.New()
				orgID := uuid.New()
				payload := json.RawMessage(`{}`)
				handlerErr := &FatalError{Err: errors.New("docker daemon unreachable")}

				w.Register("fatal_job", func(ctx context.Context, jobType string, p json.RawMessage) error {
					return handlerErr
				})

				mock.ExpectBegin()
				rows := pgxmock.NewRows([]string{"id", "org_id", "job_type", "payload", "attempts", "max_attempts"}).
					AddRow(jobID, orgID, "fatal_job", payload, 0, 3) // attempt 0 of 3 — should still dead-letter
				mock.ExpectQuery("SELECT .+ FROM jobs").
					WillReturnRows(rows)
				mock.ExpectExec("UPDATE jobs SET status = 'running'").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
				mock.ExpectExec("UPDATE jobs SET status = 'dead_letter'").
					WithArgs(handlerErr.Error(), jobID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w, mock := newTestWorker(t)
			defer mock.Close()

			tt.setupMock(t, w, mock)

			// poll should not panic regardless of scenario
			require.NotPanics(t, func() {
				w.poll(context.Background())
			}, "poll should not panic")

			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestWorker_Start_StopsOnContextCancel(t *testing.T) {
	t.Parallel()

	w, mock := newTestWorker(t)
	defer mock.Close()

	// Set a very short poll interval so the test doesn't hang.
	w.pollInterval = 10 * time.Millisecond

	// The poll will try to begin a transaction. We'll let it run a few times,
	// each returning no rows, then cancel.
	mock.MatchExpectationsInOrder(false)
	for range 5 {
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT .+ FROM jobs").WillReturnError(pgx.ErrNoRows)
		mock.ExpectRollback()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Worker stopped as expected.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Worker.Start did not stop after context cancellation")
	}
}

// ---------------------------------------------------------------------------
// Backoff verification (separate since it tests retryJob directly)
// ---------------------------------------------------------------------------

func TestWorker_RetryJob_BackoffCalculation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		attempt     int
		expectedSec int
	}{
		{name: "2^0 = 1 second", attempt: 0, expectedSec: 1},
		{name: "2^1 = 2 seconds", attempt: 1, expectedSec: 2},
		{name: "2^2 = 4 seconds", attempt: 2, expectedSec: 4},
		{name: "2^3 = 8 seconds", attempt: 3, expectedSec: 8},
		{name: "2^5 = 32 seconds", attempt: 5, expectedSec: 32},
		{name: "2^10 = 1024 seconds (cap)", attempt: 10, expectedSec: 1024},
		{name: "attempt 11 capped at 2^10 = 1024 seconds", attempt: 11, expectedSec: 1024},
		{name: "attempt 20 capped at 2^10 = 1024 seconds", attempt: 20, expectedSec: 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w, mock := newTestWorker(t)
			defer mock.Close()

			jobID := uuid.New()
			exp := tt.attempt
			if exp > 10 {
				exp = 10
			}
			expectedBackoff := time.Duration(1<<uint(exp)) * time.Second
			require.Equal(t, tt.expectedSec, int(expectedBackoff.Seconds()), "backoff formula should produce expected seconds")

			intervalStr := fmt.Sprintf("%d seconds", tt.expectedSec)

			mock.ExpectExec("UPDATE jobs SET status = 'pending'").
				WithArgs("err", intervalStr, jobID).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			w.retryJob(context.Background(), jobID, "err", tt.attempt)

			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
