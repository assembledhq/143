package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
)

type JobHandler func(ctx context.Context, jobType string, payload json.RawMessage) error

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

	err = tx.QueryRow(ctx, `
		SELECT id, org_id, job_type, payload, attempts, max_attempts
		FROM jobs
		WHERE status = 'pending' AND run_at <= now()
		ORDER BY priority DESC, created_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`).Scan(&jobID, &orgID, &jobType, &payload, &attempts, &maxAttempts)

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
	w.logger.Info().Str("job_id", jobID.String()).Str("job_type", jobType).Msg("processing job")
	if err := handler(handlerCtx, jobType, payload); err != nil {
		w.logger.Error().Err(err).Str("job_id", jobID.String()).Msg("job failed")
		if attempts+1 >= maxAttempts {
			w.deadLetterJob(ctx, jobID, err.Error())
		} else {
			w.retryJob(ctx, jobID, err.Error(), attempts+1)
		}
		return
	}

	w.succeedJob(ctx, jobID)
}

func (w *Worker) succeedJob(ctx context.Context, jobID uuid.UUID) {
	_, _ = w.db.Exec(ctx, `
		UPDATE jobs SET status = 'succeeded', completed_at = now(), locked_by_node_id = NULL, locked_at = NULL, updated_at = now()
		WHERE id = $1
	`, jobID)
}

func (w *Worker) failJob(ctx context.Context, jobID uuid.UUID, errMsg string) {
	_, _ = w.db.Exec(ctx, `
		UPDATE jobs SET status = 'failed', last_error = $1, locked_by_node_id = NULL, locked_at = NULL, updated_at = now()
		WHERE id = $2
	`, errMsg, jobID)
}

func (w *Worker) retryJob(ctx context.Context, jobID uuid.UUID, errMsg string, attempt int) {
	// Exponential backoff: 2^attempt seconds
	backoff := time.Duration(1<<uint(attempt)) * time.Second
	_, _ = w.db.Exec(ctx, `
		UPDATE jobs SET status = 'pending', last_error = $1, run_at = now() + $2::interval, locked_by_node_id = NULL, locked_at = NULL, updated_at = now()
		WHERE id = $3
	`, errMsg, fmt.Sprintf("%d seconds", int(backoff.Seconds())), jobID)
}

func (w *Worker) deadLetterJob(ctx context.Context, jobID uuid.UUID, errMsg string) {
	_, _ = w.db.Exec(ctx, `
		UPDATE jobs SET status = 'dead_letter', last_error = $1, completed_at = now(), locked_by_node_id = NULL, locked_at = NULL, updated_at = now()
		WHERE id = $2
	`, errMsg, jobID)
}
