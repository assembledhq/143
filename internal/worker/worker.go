package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/jobctx"
)

type JobHandler func(ctx context.Context, jobType string, payload json.RawMessage) error

// RetryableError wraps an error to indicate that the job should be retried
// without consuming an attempt. This is useful for transient conditions like
// concurrency limits where the job will succeed once capacity is available.
type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// FatalError wraps an error to indicate that the job should be dead-lettered
// immediately without retrying. Use this for persistent failures where retrying
// would produce the same result (e.g. Docker daemon unreachable, missing config).
type FatalError struct {
	Err error
}

func (e *FatalError) Error() string { return e.Err.Error() }
func (e *FatalError) Unwrap() error { return e.Err }

type jobContextKey string

const jobOrgIDContextKey jobContextKey = "job_org_id"

func withJobOrgID(ctx context.Context, orgID uuid.UUID) context.Context {
	return context.WithValue(ctx, jobOrgIDContextKey, orgID)
}

func jobOrgIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	orgID, ok := ctx.Value(jobOrgIDContextKey).(uuid.UUID)
	return orgID, ok
}

// WorkerDB is the database interface required by the Worker.
// Both *pgxpool.Pool and pgxmock.PgxPoolIface satisfy this interface.
type WorkerDB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// maxRetryableDuration is the maximum wall-clock time a retryable job is
// allowed to keep retrying before being dead-lettered. This prevents jobs
// from retrying indefinitely (e.g. when stuck behind a concurrency limit).
const maxRetryableDuration = 8 * time.Minute

type Worker struct {
	db           WorkerDB
	logger       zerolog.Logger
	nodeID       string
	handlers     map[string]JobHandler
	pollInterval time.Duration
}

func New(db WorkerDB, logger zerolog.Logger, nodeID string) *Worker {
	return &Worker{
		db:           db,
		logger:       logger,
		nodeID:       nodeID,
		handlers:     make(map[string]JobHandler),
		pollInterval: 5 * time.Second,
	}
}

func (w *Worker) Register(jobType string, handler JobHandler) {
	w.handlers[jobType] = handler
}

func (w *Worker) Start(ctx context.Context) {
	w.logger.Info().Str("node_id", w.nodeID).Msg("worker started")
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info().Msg("worker stopping")
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

func (w *Worker) poll(ctx context.Context) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		w.logger.Error().Err(err).Msg("failed to begin transaction")
		return
	}
	defer func() {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && rollbackErr != pgx.ErrTxClosed {
			w.logger.Error().Err(rollbackErr).Msg("failed to rollback transaction")
		}
	}()

	var jobID uuid.UUID
	var orgID uuid.UUID
	var jobType string
	var payload json.RawMessage
	var attempts, maxAttempts int
	var jobCreatedAt time.Time

	err = tx.QueryRow(ctx, `
		SELECT id, org_id, job_type, payload, attempts, max_attempts, created_at
		FROM jobs
		WHERE status = 'pending' AND run_at <= now()
		ORDER BY priority DESC, created_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`).Scan(&jobID, &orgID, &jobType, &payload, &attempts, &maxAttempts, &jobCreatedAt)

	if err != nil {
		if err == pgx.ErrNoRows {
			if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && rollbackErr != pgx.ErrTxClosed {
				w.logger.Error().Err(rollbackErr).Msg("failed to rollback transaction after no rows")
			}
			return
		}
		w.logger.Error().Err(err).Msg("failed to claim job")
		return
	}

	// Mark as running
	_, err = tx.Exec(ctx, `
		UPDATE jobs SET status = 'running', locked_by_node_id = $1, locked_at = now(), attempts = attempts + 1, updated_at = now()
		WHERE id = $2
	`, w.nodeID, jobID)
	if err != nil {
		w.logger.Error().Err(err).Msg("failed to lock job")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		w.logger.Error().Err(err).Msg("failed to commit job claim")
		return
	}

	// Process the job
	handler, ok := w.handlers[jobType]
	if !ok {
		w.logger.Warn().Str("job_type", jobType).Msg("no handler registered")
		w.failJob(ctx, jobID, fmt.Sprintf("no handler for job type: %s", jobType))
		return
	}

	handlerCtx := withJobOrgID(ctx, orgID)
	handlerCtx = jobctx.WithDeadLetterHooks(handlerCtx)
	w.logger.Info().Str("job_id", jobID.String()).Str("job_type", jobType).Msg("processing job")
	if err := handler(handlerCtx, jobType, payload); err != nil {
		// FatalError means the failure is persistent — dead-letter immediately
		// without wasting retries (e.g. Docker daemon unreachable).
		var fatal *FatalError
		if errors.As(err, &fatal) {
			w.logger.Error().Err(err).Str("job_id", jobID.String()).Msg("job failed (fatal, skipping retries)")
			w.deadLetterJob(ctx, jobID, err.Error())
			jobctx.RunDeadLetterHooks(handlerCtx, err)
			return
		}
		// RetryableError means we should retry without consuming an attempt
		// (e.g. concurrency limit reached — the job will succeed once a slot opens).
		// However, cap retryable retries at maxRetryableDuration to prevent
		// jobs from retrying indefinitely.
		var retryable *RetryableError
		if errors.As(err, &retryable) {
			if time.Since(jobCreatedAt) > maxRetryableDuration {
				w.logger.Error().Err(err).
					Str("job_id", jobID.String()).
					Dur("age", time.Since(jobCreatedAt)).
					Msg("retryable job exceeded max duration, dead-lettering")
				timeoutErr := fmt.Errorf("retryable job timed out after %s: %w", maxRetryableDuration, err)
				w.deadLetterJob(ctx, jobID, timeoutErr.Error())
				jobctx.RunDeadLetterHooks(handlerCtx, timeoutErr)
				return
			}
			w.logger.Info().Err(err).Str("job_id", jobID.String()).Msg("job deferred (retryable)")
			w.retryJob(ctx, jobID, err.Error(), attempts) // don't increment attempt count
			return
		}
		w.logger.Error().Err(err).Str("job_id", jobID.String()).Msg("job failed")
		if attempts+1 >= maxAttempts {
			w.deadLetterJob(ctx, jobID, err.Error())
			jobctx.RunDeadLetterHooks(handlerCtx, err)
		} else {
			w.retryJob(ctx, jobID, err.Error(), attempts+1)
		}
		return
	}

	w.succeedJob(ctx, jobID)
}

func (w *Worker) succeedJob(ctx context.Context, jobID uuid.UUID) {
	if _, err := w.db.Exec(ctx, `
		UPDATE jobs SET status = 'succeeded', completed_at = now(), locked_by_node_id = NULL, locked_at = NULL, updated_at = now()
		WHERE id = $1
	`, jobID); err != nil {
		w.logger.Warn().Err(err).Str("job_id", jobID.String()).Msg("failed to mark job as succeeded")
	}
}

func (w *Worker) failJob(ctx context.Context, jobID uuid.UUID, errMsg string) {
	if _, err := w.db.Exec(ctx, `
		UPDATE jobs SET status = 'failed', last_error = $1, locked_by_node_id = NULL, locked_at = NULL, updated_at = now()
		WHERE id = $2
	`, errMsg, jobID); err != nil {
		w.logger.Warn().Err(err).Str("job_id", jobID.String()).Msg("failed to mark job as failed")
	}
}

func (w *Worker) retryJob(ctx context.Context, jobID uuid.UUID, errMsg string, attempt int) {
	// Exponential backoff: 2^attempt seconds, capped at ~17 minutes.
	exp := attempt
	if exp > 10 {
		exp = 10
	}
	backoff := time.Duration(1<<exp) * time.Second // exp is capped at 10 above
	if _, err := w.db.Exec(ctx, `
		UPDATE jobs SET status = 'pending', last_error = $1, run_at = now() + $2::interval, locked_by_node_id = NULL, locked_at = NULL, updated_at = now()
		WHERE id = $3
	`, errMsg, fmt.Sprintf("%d seconds", int(backoff.Seconds())), jobID); err != nil {
		w.logger.Warn().Err(err).Str("job_id", jobID.String()).Msg("failed to schedule job retry")
	}
}

func (w *Worker) deadLetterJob(ctx context.Context, jobID uuid.UUID, errMsg string) {
	if _, err := w.db.Exec(ctx, `
		UPDATE jobs SET status = 'dead_letter', last_error = $1, completed_at = now(), locked_by_node_id = NULL, locked_at = NULL, updated_at = now()
		WHERE id = $2
	`, errMsg, jobID); err != nil {
		w.logger.Warn().Err(err).Str("job_id", jobID.String()).Msg("failed to dead-letter job")
	}
}
