package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

const previewVerificationRunColumns = `id, org_id, session_id, preview_instance_id,
	workspace_revision, config_digest, trigger, status, attempt, max_attempts,
	plan, steps, captures, console_error_count, summary, failure_reason, skip_reason,
	started_at, completed_at, created_at, updated_at`

type PreviewVerificationRunStore struct{ db DBTX }

func NewPreviewVerificationRunStore(db DBTX) *PreviewVerificationRunStore {
	return &PreviewVerificationRunStore{db: db}
}

func (s *PreviewVerificationRunStore) Create(ctx context.Context, orgID uuid.UUID, run models.PreviewVerificationRun) (models.PreviewVerificationRun, error) {
	if err := run.Status.Validate(); err != nil {
		return models.PreviewVerificationRun{}, fmt.Errorf("create preview verification run: %w", err)
	}
	if err := run.Trigger.Validate(); err != nil {
		return models.PreviewVerificationRun{}, fmt.Errorf("create preview verification run: %w", err)
	}
	query := `INSERT INTO preview_verification_runs (
		org_id, session_id, preview_instance_id, workspace_revision, config_digest,
		trigger, status, attempt, max_attempts, plan, steps, captures,
		console_error_count, summary, failure_reason, skip_reason, completed_at
	) VALUES (@org_id, @session_id, @preview_instance_id, @workspace_revision, @config_digest,
		@trigger, @status, @attempt, @max_attempts, @plan, @steps, @captures,
		@console_error_count, @summary, @failure_reason, @skip_reason, @completed_at)
	RETURNING ` + previewVerificationRunColumns
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID, "session_id": run.SessionID, "preview_instance_id": run.PreviewInstanceID,
		"workspace_revision": run.WorkspaceRevision, "config_digest": run.ConfigDigest,
		"trigger": run.Trigger, "status": run.Status, "attempt": run.Attempt,
		"max_attempts": run.MaxAttempts, "plan": normalizedJSON(run.Plan), "steps": normalizedJSON(run.Steps),
		"captures": normalizedJSON(run.Captures), "console_error_count": run.ConsoleErrorCount,
		"summary": run.Summary, "failure_reason": run.FailureReason, "skip_reason": run.SkipReason,
		"completed_at": run.CompletedAt,
	})
	if err != nil {
		return models.PreviewVerificationRun{}, fmt.Errorf("create preview verification run: %w", err)
	}
	created, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[models.PreviewVerificationRun])
	if err != nil {
		return models.PreviewVerificationRun{}, fmt.Errorf("collect preview verification run: %w", err)
	}
	created.Steps = normalizePreviewVerificationStepCompatibility(created.Steps)
	return created, nil
}

func (s *PreviewVerificationRunStore) Complete(ctx context.Context, orgID, runID uuid.UUID, status models.PreviewVerificationStatus, attempt int, steps, captures json.RawMessage, consoleErrors int, summary, failureReason string) (models.PreviewVerificationRun, error) {
	if err := status.Validate(); err != nil {
		return models.PreviewVerificationRun{}, fmt.Errorf("complete preview verification run: %w", err)
	}
	if status == models.PreviewVerificationStatusRunning {
		return models.PreviewVerificationRun{}, fmt.Errorf("complete preview verification run: running is not terminal")
	}
	query := `UPDATE preview_verification_runs SET
		status = @status, attempt = @attempt, steps = @steps, captures = @captures,
		console_error_count = @console_error_count, summary = @summary,
		failure_reason = @failure_reason, completed_at = @completed_at, updated_at = @completed_at
	WHERE org_id = @org_id AND id = @id AND status = 'running'
	RETURNING ` + previewVerificationRunColumns
	now := time.Now().UTC()
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID, "id": runID, "status": status, "attempt": attempt,
		"steps": normalizedJSON(steps), "captures": normalizedJSON(captures),
		"console_error_count": consoleErrors, "summary": summary,
		"failure_reason": failureReason, "completed_at": now,
	})
	if err != nil {
		return models.PreviewVerificationRun{}, fmt.Errorf("complete preview verification run: %w", err)
	}
	completed, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[models.PreviewVerificationRun])
	if err != nil {
		return models.PreviewVerificationRun{}, fmt.Errorf("collect completed preview verification run: %w", err)
	}
	completed.Steps = normalizePreviewVerificationStepCompatibility(completed.Steps)
	return completed, nil
}

func (s *PreviewVerificationRunStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID, limit int) ([]models.PreviewVerificationRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	query := `SELECT ` + previewVerificationRunColumns + ` FROM preview_verification_runs
		WHERE org_id = @org_id AND session_id = @session_id
		ORDER BY created_at DESC, id DESC LIMIT @limit`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "limit": limit})
	if err != nil {
		return nil, fmt.Errorf("list preview verification runs: %w", err)
	}
	runs, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.PreviewVerificationRun])
	if err != nil {
		return nil, fmt.Errorf("collect preview verification runs: %w", err)
	}
	for i := range runs {
		runs[i].Steps = normalizePreviewVerificationStepCompatibility(runs[i].Steps)
	}
	return runs, nil
}

func normalizePreviewVerificationStepCompatibility(value json.RawMessage) json.RawMessage {
	var steps []map[string]json.RawMessage
	if err := json.Unmarshal(value, &steps); err != nil {
		return value
	}
	changed := false
	for _, step := range steps {
		capture, hasCapture := step["capture"]
		legacyCapture, hasLegacyCapture := step["artifact"]
		if hasCapture && !hasLegacyCapture {
			step["artifact"] = capture
			changed = true
		} else if hasLegacyCapture && !hasCapture {
			step["capture"] = legacyCapture
			changed = true
		}
	}
	if !changed {
		return value
	}
	normalized, err := json.Marshal(steps)
	if err != nil {
		return value
	}
	return normalized
}

// normalizedJSON guarantees a jsonb array for the plan/steps/captures columns.
// A nil Go slice marshals to JSON null, which passes Go typing but violates the
// jsonb_typeof(...) = 'array' CHECK constraints, so treat null (and empty) as [].
func normalizedJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return json.RawMessage(`[]`)
	}
	return value
}
