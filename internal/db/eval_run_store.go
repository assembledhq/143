package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type EvalRunStore struct {
	db DBTX
}

func NewEvalRunStore(db DBTX) *EvalRunStore {
	return &EvalRunStore{db: db}
}

const evalRunColumns = `id, task_id, org_id, batch_id,
	session_id, thread_id,
	input_manifest, model, server_deploy_sha, pm_document_set_pin_id,
	config_ref, context_overrides,
	agent_diff, agent_trace, token_usage,
	criterion_results, final_score, passed,
	status, duration_seconds, sandbox_id,
	started_at, completed_at, error_message, created_at`

func scanEvalRun(row pgx.Row) (models.EvalRun, error) {
	var r models.EvalRun
	err := row.Scan(
		&r.ID, &r.TaskID, &r.OrgID, &r.BatchID,
		&r.SessionID, &r.ThreadID,
		&r.InputManifest, &r.Model, &r.ServerDeploySHA, &r.PMDocumentSetPinID,
		&r.ConfigRef, &r.ContextOverrides,
		&r.AgentDiff, &r.AgentTrace, &r.TokenUsage,
		&r.CriterionResults, &r.FinalScore, &r.Passed,
		&r.Status, &r.DurationSeconds, &r.SandboxID,
		&r.StartedAt, &r.CompletedAt, &r.ErrorMessage, &r.CreatedAt,
	)
	return r, err
}

func scanEvalRuns(rows pgx.Rows) ([]models.EvalRun, error) {
	var runs []models.EvalRun
	for rows.Next() {
		var r models.EvalRun
		err := rows.Scan(
			&r.ID, &r.TaskID, &r.OrgID, &r.BatchID,
			&r.SessionID, &r.ThreadID,
			&r.InputManifest, &r.Model, &r.ServerDeploySHA, &r.PMDocumentSetPinID,
			&r.ConfigRef, &r.ContextOverrides,
			&r.AgentDiff, &r.AgentTrace, &r.TokenUsage,
			&r.CriterionResults, &r.FinalScore, &r.Passed,
			&r.Status, &r.DurationSeconds, &r.SandboxID,
			&r.StartedAt, &r.CompletedAt, &r.ErrorMessage, &r.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan eval run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (s *EvalRunStore) Create(ctx context.Context, run *models.EvalRun) error {
	query := fmt.Sprintf(`INSERT INTO eval_runs (
		task_id, org_id, batch_id,
		input_manifest, model, server_deploy_sha, pm_document_set_pin_id,
		config_ref, context_overrides
	) VALUES (
		@task_id, @org_id, @batch_id,
		@input_manifest, @model, @server_deploy_sha, @pm_document_set_pin_id,
		@config_ref, @context_overrides
	) RETURNING %s`, evalRunColumns)
	if run.SessionID != nil || run.ThreadID != nil {
		query = fmt.Sprintf(`INSERT INTO eval_runs (
			task_id, org_id, batch_id, session_id, thread_id,
			input_manifest, model, server_deploy_sha, pm_document_set_pin_id,
			config_ref, context_overrides
		) VALUES (
			@task_id, @org_id, @batch_id, @session_id, @thread_id,
			@input_manifest, @model, @server_deploy_sha, @pm_document_set_pin_id,
			@config_ref, @context_overrides
		) RETURNING %s`, evalRunColumns)
	}
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"task_id":                run.TaskID,
		"org_id":                 run.OrgID,
		"batch_id":               run.BatchID,
		"session_id":             run.SessionID,
		"thread_id":              run.ThreadID,
		"input_manifest":         run.InputManifest,
		"model":                  run.Model,
		"server_deploy_sha":      run.ServerDeploySHA,
		"pm_document_set_pin_id": run.PMDocumentSetPinID,
		"config_ref":             run.ConfigRef,
		"context_overrides":      run.ContextOverrides,
	})

	scanned, err := scanEvalRun(row)
	if err != nil {
		return fmt.Errorf("create eval run: %w", err)
	}
	*run = scanned
	return nil
}

func (s *EvalRunStore) GetByID(ctx context.Context, orgID, runID uuid.UUID) (models.EvalRun, error) {
	query := fmt.Sprintf(`SELECT %s FROM eval_runs WHERE id = @id AND org_id = @org_id`, evalRunColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     runID,
		"org_id": orgID,
	})
	return scanEvalRun(row)
}

func (s *EvalRunStore) GetBySessionID(ctx context.Context, orgID, sessionID uuid.UUID) (models.EvalRun, error) {
	query := fmt.Sprintf(`SELECT %s FROM eval_runs WHERE org_id = @org_id AND session_id = @session_id ORDER BY created_at DESC LIMIT 1`, evalRunColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	return scanEvalRun(row)
}

func (s *EvalRunStore) AttachSession(ctx context.Context, orgID, runID, sessionID uuid.UUID, threadID *uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE eval_runs
		 SET session_id = @session_id, thread_id = @thread_id
		 WHERE id = @id AND org_id = @org_id AND session_id IS NULL`,
		pgx.NamedArgs{
			"id":         runID,
			"org_id":     orgID,
			"session_id": sessionID,
			"thread_id":  threadID,
		},
	)
	if err != nil {
		return fmt.Errorf("attach eval run session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *EvalRunStore) ListByTask(ctx context.Context, orgID, taskID uuid.UUID, limit int) ([]models.EvalRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query := fmt.Sprintf(`SELECT %s FROM eval_runs
		WHERE org_id = @org_id AND task_id = @task_id
		ORDER BY created_at DESC LIMIT @limit`, evalRunColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":  orgID,
		"task_id": taskID,
		"limit":   limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list eval runs by task: %w", err)
	}
	defer rows.Close()
	return scanEvalRuns(rows)
}

func (s *EvalRunStore) ListByBatch(ctx context.Context, orgID, batchID uuid.UUID) ([]models.EvalRun, error) {
	query := fmt.Sprintf(`SELECT %s FROM eval_runs
		WHERE org_id = @org_id AND batch_id = @batch_id
		ORDER BY task_id, created_at DESC`, evalRunColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"batch_id": batchID,
	})
	if err != nil {
		return nil, fmt.Errorf("list eval runs by batch: %w", err)
	}
	defer rows.Close()
	return scanEvalRuns(rows)
}

// UpdateStatus transitions an eval run to a new status.
func (s *EvalRunStore) UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status models.EvalRunStatus) error {
	query := `UPDATE eval_runs SET status = @status, started_at = CASE WHEN @status = 'running' AND started_at IS NULL THEN now() ELSE started_at END WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     runID,
		"org_id": orgID,
		"status": status,
	})
	if err != nil {
		return fmt.Errorf("update eval run status: %w", err)
	}
	return nil
}

// UpdatePostSessionArtifacts persists the agent session output and moves the
// run into grading without marking it completed.
func (s *EvalRunStore) UpdatePostSessionArtifacts(ctx context.Context, orgID, runID uuid.UUID, agentDiff *string, agentTrace json.RawMessage, inputManifest json.RawMessage) error {
	query := `UPDATE eval_runs SET
		status = @status,
		agent_diff = @agent_diff,
		agent_trace = @agent_trace,
		input_manifest = @input_manifest
		WHERE id = @id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":             runID,
		"org_id":         orgID,
		"status":         models.EvalRunStatusGrading,
		"agent_diff":     agentDiff,
		"agent_trace":    agentTrace,
		"input_manifest": inputManifest,
	})
	if err != nil {
		return fmt.Errorf("update eval run post-session artifacts: %w", err)
	}
	return nil
}

// UpdateResult stores the completed eval run results (scoring, diff, etc.).
func (s *EvalRunStore) UpdateResult(ctx context.Context, orgID, runID uuid.UUID, result *models.EvalRunResult) error {
	query := `UPDATE eval_runs SET
		status = @status,
		agent_diff = @agent_diff,
		agent_trace = @agent_trace,
		token_usage = @token_usage,
		criterion_results = @criterion_results,
		final_score = @final_score,
		passed = @passed,
		duration_seconds = @duration_seconds,
		sandbox_id = @sandbox_id,
		error_message = @error_message,
		input_manifest = @input_manifest,
		completed_at = now()
		WHERE id = @id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":                runID,
		"org_id":            orgID,
		"status":            result.Status,
		"agent_diff":        result.AgentDiff,
		"agent_trace":       result.AgentTrace,
		"token_usage":       result.TokenUsage,
		"criterion_results": result.CriterionResults,
		"final_score":       result.FinalScore,
		"passed":            result.Passed,
		"duration_seconds":  result.DurationSeconds,
		"sandbox_id":        result.SandboxID,
		"error_message":     result.ErrorMessage,
		"input_manifest":    result.InputManifest,
	})
	if err != nil {
		return fmt.Errorf("update eval run result: %w", err)
	}
	return nil
}

// EvalBatchStore

type EvalBatchStore struct {
	db DBTX
}

func NewEvalBatchStore(db DBTX) *EvalBatchStore {
	return &EvalBatchStore{db: db}
}

const evalBatchColumns = `id, org_id, name, status, task_count, run_count, created_by, created_at, completed_at`

func scanEvalBatch(row pgx.Row) (models.EvalBatch, error) {
	var b models.EvalBatch
	err := row.Scan(
		&b.ID, &b.OrgID, &b.Name, &b.Status, &b.TaskCount, &b.RunCount,
		&b.CreatedBy, &b.CreatedAt, &b.CompletedAt,
	)
	return b, err
}

func (s *EvalBatchStore) Create(ctx context.Context, batch *models.EvalBatch) error {
	query := fmt.Sprintf(`INSERT INTO eval_batches (
		org_id, name, status, task_count, run_count, created_by
	) VALUES (
		@org_id, @name, @status, @task_count, @run_count, @created_by
	) RETURNING %s`, evalBatchColumns)

	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":     batch.OrgID,
		"name":       batch.Name,
		"status":     batch.Status,
		"task_count": batch.TaskCount,
		"run_count":  batch.RunCount,
		"created_by": batch.CreatedBy,
	})

	scanned, err := scanEvalBatch(row)
	if err != nil {
		return fmt.Errorf("create eval batch: %w", err)
	}
	*batch = scanned
	return nil
}

func (s *EvalBatchStore) GetByID(ctx context.Context, orgID, batchID uuid.UUID) (models.EvalBatch, error) {
	query := fmt.Sprintf(`SELECT %s FROM eval_batches WHERE id = @id AND org_id = @org_id`, evalBatchColumns)
	row := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     batchID,
		"org_id": orgID,
	})
	return scanEvalBatch(row)
}

func (s *EvalBatchStore) ListByOrg(ctx context.Context, orgID uuid.UUID, limit int) ([]models.EvalBatch, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	query := fmt.Sprintf(`SELECT %s FROM eval_batches
		WHERE org_id = @org_id
		ORDER BY created_at DESC LIMIT @limit`, evalBatchColumns)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID,
		"limit":  limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list eval batches: %w", err)
	}
	defer rows.Close()

	var batches []models.EvalBatch
	for rows.Next() {
		var b models.EvalBatch
		err := rows.Scan(
			&b.ID, &b.OrgID, &b.Name, &b.Status, &b.TaskCount, &b.RunCount,
			&b.CreatedBy, &b.CreatedAt, &b.CompletedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan eval batch: %w", err)
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}

func (s *EvalBatchStore) UpdateStatus(ctx context.Context, orgID, batchID uuid.UUID, status models.EvalBatchStatus) error {
	query := `UPDATE eval_batches SET status = @status,
		completed_at = CASE WHEN @status IN ('completed', 'failed') THEN now() ELSE completed_at END
		WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     batchID,
		"org_id": orgID,
		"status": status,
	})
	if err != nil {
		return fmt.Errorf("update eval batch status: %w", err)
	}
	return nil
}

// CompleteBatchIfDone atomically marks a batch as completed if all its runs are
// finished (not pending, running, or grading). This avoids the race condition
// where multiple concurrent workers each check and try to complete the same
// batch.
func (s *EvalBatchStore) CompleteBatchIfDone(ctx context.Context, orgID, batchID uuid.UUID) error {
	query := `UPDATE eval_batches SET status = 'completed', completed_at = now()
		WHERE id = @id AND org_id = @org_id AND status != 'completed'
		AND NOT EXISTS (
			SELECT 1 FROM eval_runs
			WHERE batch_id = @id AND org_id = @org_id AND status IN ('pending', 'running', 'grading')
		)`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     batchID,
		"org_id": orgID,
	})
	if err != nil {
		return fmt.Errorf("complete batch if done: %w", err)
	}
	return nil
}
