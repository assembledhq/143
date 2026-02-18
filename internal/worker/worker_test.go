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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestWorker(t *testing.T) (*Worker, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	w := New(mock, zerolog.Nop(), "test-node")
	return w, mock
}

// ---------------------------------------------------------------------------
// Direct tests for job lifecycle methods
// ---------------------------------------------------------------------------

func TestWorker_SucceedJob(t *testing.T) {
	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()

	mock.ExpectExec("UPDATE jobs SET status = 'succeeded'").
		WithArgs(jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w.succeedJob(context.Background(), jobID)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWorker_FailJob(t *testing.T) {
	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	errMsg := "something went wrong"

	mock.ExpectExec("UPDATE jobs SET status = 'failed'").
		WithArgs(errMsg, jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w.failJob(context.Background(), jobID, errMsg)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWorker_RetryJob(t *testing.T) {
	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	errMsg := "transient error"
	attempt := 2
	// backoff = 2^attempt seconds = 4 seconds for attempt=2
	backoff := time.Duration(1<<uint(attempt)) * time.Second
	intervalStr := fmt.Sprintf("%d seconds", int(backoff.Seconds()))

	mock.ExpectExec("UPDATE jobs SET status = 'pending'").
		WithArgs(errMsg, intervalStr, jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w.retryJob(context.Background(), jobID, errMsg, attempt)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWorker_RetryJob_BackoffCalculation(t *testing.T) {
	// Verify exponential backoff at various attempt levels.
	tests := []struct {
		attempt     int
		expectedSec int
	}{
		{0, 1},  // 2^0 = 1
		{1, 2},  // 2^1 = 2
		{2, 4},  // 2^2 = 4
		{3, 8},  // 2^3 = 8
		{5, 32}, // 2^5 = 32
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("attempt_%d", tc.attempt), func(t *testing.T) {
			w, mock := newTestWorker(t)
			defer mock.Close()

			jobID := uuid.New()
			intervalStr := fmt.Sprintf("%d seconds", tc.expectedSec)

			mock.ExpectExec("UPDATE jobs SET status = 'pending'").
				WithArgs("err", intervalStr, jobID).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			w.retryJob(context.Background(), jobID, "err", tc.attempt)

			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestWorker_DeadLetterJob(t *testing.T) {
	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	errMsg := "max attempts exceeded"

	mock.ExpectExec("UPDATE jobs SET status = 'dead_letter'").
		WithArgs(errMsg, jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w.deadLetterJob(context.Background(), jobID, errMsg)

	assert.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Poll tests
// ---------------------------------------------------------------------------

func TestWorker_Poll_BeginError(t *testing.T) {
	w, mock := newTestWorker(t)
	defer mock.Close()

	mock.ExpectBegin().WillReturnError(errors.New("connection refused"))

	w.poll(context.Background())

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWorker_Poll_NoJobs(t *testing.T) {
	w, mock := newTestWorker(t)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM jobs").
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()
	// The deferred Rollback after the explicit one is also called; pgxmock
	// is tolerant of extra rollbacks after a rollback/commit.

	w.poll(context.Background())

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWorker_Poll_SuccessfulJob(t *testing.T) {
	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	payload := json.RawMessage(`{"key":"value"}`)

	// Register a handler that succeeds
	handlerCalled := false
	w.Register("test_job", func(ctx context.Context, jobType string, p json.RawMessage) error {
		handlerCalled = true
		assert.Equal(t, "test_job", jobType)
		assert.JSONEq(t, `{"key":"value"}`, string(p))
		return nil
	})

	// Transaction: begin
	mock.ExpectBegin()

	// Transaction: SELECT for the pending job (no args)
	rows := pgxmock.NewRows([]string{"id", "job_type", "payload", "attempts", "max_attempts"}).
		AddRow(jobID, "test_job", payload, 0, 3)
	mock.ExpectQuery("SELECT .+ FROM jobs").
		WillReturnRows(rows)

	// Transaction: UPDATE to mark running (2 args: nodeID, jobID)
	mock.ExpectExec("UPDATE jobs SET status = 'running'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Transaction: commit
	mock.ExpectCommit()

	// After handler succeeds: succeedJob exec (1 arg: jobID)
	mock.ExpectExec("UPDATE jobs SET status = 'succeeded'").
		WithArgs(jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w.poll(context.Background())

	assert.True(t, handlerCalled, "handler should have been called")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWorker_Poll_FailedJob_Retry(t *testing.T) {
	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	payload := json.RawMessage(`{}`)
	handlerErr := errors.New("temporary failure")

	// attempts=0, maxAttempts=3 => after increment: attempts+1=1 < 3 => retry
	w.Register("retry_job", func(ctx context.Context, jobType string, p json.RawMessage) error {
		return handlerErr
	})

	mock.ExpectBegin()
	rows := pgxmock.NewRows([]string{"id", "job_type", "payload", "attempts", "max_attempts"}).
		AddRow(jobID, "retry_job", payload, 0, 3)
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

	w.poll(context.Background())

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWorker_Poll_FailedJob_DeadLetter(t *testing.T) {
	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	payload := json.RawMessage(`{}`)
	handlerErr := errors.New("permanent failure")

	// attempts=2, maxAttempts=3 => after increment: attempts+1=3 >= 3 => dead letter
	w.Register("dead_job", func(ctx context.Context, jobType string, p json.RawMessage) error {
		return handlerErr
	})

	mock.ExpectBegin()
	rows := pgxmock.NewRows([]string{"id", "job_type", "payload", "attempts", "max_attempts"}).
		AddRow(jobID, "dead_job", payload, 2, 3)
	mock.ExpectQuery("SELECT .+ FROM jobs").
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE jobs SET status = 'running'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	// deadLetterJob (2 args: errMsg, jobID)
	mock.ExpectExec("UPDATE jobs SET status = 'dead_letter'").
		WithArgs(handlerErr.Error(), jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w.poll(context.Background())

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWorker_Poll_NoHandler(t *testing.T) {
	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	payload := json.RawMessage(`{}`)

	// Do NOT register any handler for "unknown_job"

	mock.ExpectBegin()
	rows := pgxmock.NewRows([]string{"id", "job_type", "payload", "attempts", "max_attempts"}).
		AddRow(jobID, "unknown_job", payload, 0, 3)
	mock.ExpectQuery("SELECT .+ FROM jobs").
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE jobs SET status = 'running'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	// failJob because no handler registered (2 args: errMsg, jobID)
	mock.ExpectExec("UPDATE jobs SET status = 'failed'").
		WithArgs("no handler for job type: unknown_job", jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w.poll(context.Background())

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWorker_Poll_QueryError(t *testing.T) {
	w, mock := newTestWorker(t)
	defer mock.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM jobs").
		WillReturnError(errors.New("database error"))
	mock.ExpectRollback()

	w.poll(context.Background())

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWorker_Poll_LockExecError(t *testing.T) {
	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	payload := json.RawMessage(`{}`)

	mock.ExpectBegin()
	rows := pgxmock.NewRows([]string{"id", "job_type", "payload", "attempts", "max_attempts"}).
		AddRow(jobID, "some_job", payload, 0, 3)
	mock.ExpectQuery("SELECT .+ FROM jobs").
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE jobs SET status = 'running'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("lock failed"))
	mock.ExpectRollback()

	w.poll(context.Background())

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWorker_Poll_CommitError(t *testing.T) {
	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	payload := json.RawMessage(`{}`)

	mock.ExpectBegin()
	rows := pgxmock.NewRows([]string{"id", "job_type", "payload", "attempts", "max_attempts"}).
		AddRow(jobID, "some_job", payload, 0, 3)
	mock.ExpectQuery("SELECT .+ FROM jobs").
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE jobs SET status = 'running'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit().WillReturnError(errors.New("commit failed"))
	mock.ExpectRollback()

	w.poll(context.Background())

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWorker_Poll_FailedJob_DeadLetter_ExactBoundary(t *testing.T) {
	// Test the exact boundary: attempts=1, maxAttempts=2 => attempts+1=2 >= 2 => dead letter
	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	payload := json.RawMessage(`{}`)
	handlerErr := errors.New("boundary failure")

	w.Register("boundary_job", func(ctx context.Context, jobType string, p json.RawMessage) error {
		return handlerErr
	})

	mock.ExpectBegin()
	rows := pgxmock.NewRows([]string{"id", "job_type", "payload", "attempts", "max_attempts"}).
		AddRow(jobID, "boundary_job", payload, 1, 2)
	mock.ExpectQuery("SELECT .+ FROM jobs").
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE jobs SET status = 'running'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	mock.ExpectExec("UPDATE jobs SET status = 'dead_letter'").
		WithArgs(handlerErr.Error(), jobID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	w.poll(context.Background())

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestWorker_Poll_FailedJob_Retry_JustBelowBoundary(t *testing.T) {
	// Test just below the boundary: attempts=0, maxAttempts=2 => attempts+1=1 < 2 => retry
	w, mock := newTestWorker(t)
	defer mock.Close()

	jobID := uuid.New()
	payload := json.RawMessage(`{}`)
	handlerErr := errors.New("retryable failure")

	w.Register("retry_boundary_job", func(ctx context.Context, jobType string, p json.RawMessage) error {
		return handlerErr
	})

	mock.ExpectBegin()
	rows := pgxmock.NewRows([]string{"id", "job_type", "payload", "attempts", "max_attempts"}).
		AddRow(jobID, "retry_boundary_job", payload, 0, 2)
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

	w.poll(context.Background())

	assert.NoError(t, mock.ExpectationsWereMet())
}
