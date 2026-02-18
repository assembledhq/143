package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type AgentRunStore struct {
	db DBTX
}

func NewAgentRunStore(db DBTX) *AgentRunStore {
	return &AgentRunStore{db: db}
}

type AgentRunFilters struct {
	Status string
	Limit  int
	Cursor string
}

func (s *AgentRunStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters AgentRunFilters) ([]models.AgentRun, error) {
	query := `
		SELECT id, issue_id, org_id, agent_type, status, autonomy_level, token_mode,
		       complexity_tier, confidence_score, confidence_reasoning, risk_factors,
		       container_id, started_at, completed_at, token_usage,
		       failure_explanation, failure_category, failure_next_steps, failure_retry_advised,
		       parent_run_id, revision_context, error, result_summary, diff, created_at
		FROM agent_runs
		WHERE org_id = @org_id`

	args := pgx.NamedArgs{"org_id": orgID}

	if filters.Status != "" {
		query += ` AND status = @status`
		args["status"] = filters.Status
	}
	if filters.Cursor != "" {
		cursorID, err := uuid.Parse(filters.Cursor)
		if err == nil {
			query += ` AND id < @cursor_id`
			args["cursor_id"] = cursorID
		}
	}

	query += ` ORDER BY created_at DESC`

	limit := filters.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("query agent runs: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.AgentRun])
}

func (s *AgentRunStore) GetByID(ctx context.Context, orgID, runID uuid.UUID) (models.AgentRun, error) {
	query := `
		SELECT id, issue_id, org_id, agent_type, status, autonomy_level, token_mode,
		       complexity_tier, confidence_score, confidence_reasoning, risk_factors,
		       container_id, started_at, completed_at, token_usage,
		       failure_explanation, failure_category, failure_next_steps, failure_retry_advised,
		       parent_run_id, revision_context, error, result_summary, diff, created_at
		FROM agent_runs
		WHERE id = @id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     runID,
		"org_id": orgID,
	})
	if err != nil {
		return models.AgentRun{}, fmt.Errorf("query agent run: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.AgentRun])
}

func (s *AgentRunStore) Create(ctx context.Context, run *models.AgentRun) error {
	query := `
		INSERT INTO agent_runs (issue_id, org_id, agent_type, status, autonomy_level, token_mode, complexity_tier, parent_run_id, revision_context)
		VALUES (@issue_id, @org_id, @agent_type, @status, @autonomy_level, @token_mode, @complexity_tier, @parent_run_id, @revision_context)
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"issue_id":         run.IssueID,
		"org_id":           run.OrgID,
		"agent_type":       run.AgentType,
		"status":           run.Status,
		"autonomy_level":   run.AutonomyLevel,
		"token_mode":       run.TokenMode,
		"complexity_tier":  run.ComplexityTier,
		"parent_run_id":    run.ParentRunID,
		"revision_context": run.RevisionContext,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&run.ID, &run.CreatedAt)
}

func (s *AgentRunStore) UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status string) error {
	query := `UPDATE agent_runs SET status = @status WHERE id = @id AND org_id = @org_id`
	if status == "running" {
		query = `UPDATE agent_runs SET status = @status, started_at = now() WHERE id = @id AND org_id = @org_id`
	} else if status == "completed" || status == "failed" || status == "cancelled" {
		query = `UPDATE agent_runs SET status = @status, completed_at = now() WHERE id = @id AND org_id = @org_id`
	}
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     runID,
		"org_id": orgID,
		"status": status,
	})
	return err
}

func (s *AgentRunStore) UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status string, result *models.AgentRunResult) error {
	query := `
		UPDATE agent_runs
		SET status = @status, completed_at = now(),
		    confidence_score = @confidence_score, confidence_reasoning = @confidence_reasoning,
		    risk_factors = @risk_factors, token_usage = @token_usage,
		    result_summary = @result_summary, diff = @diff, error = @error
		WHERE id = @id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":                   runID,
		"org_id":               orgID,
		"status":               status,
		"confidence_score":     result.ConfidenceScore,
		"confidence_reasoning": result.ConfidenceReasoning,
		"risk_factors":         result.RiskFactors,
		"token_usage":          result.TokenUsage,
		"result_summary":       result.ResultSummary,
		"diff":                 result.Diff,
		"error":                result.Error,
	})
	return err
}

func (s *AgentRunStore) UpdateFailure(ctx context.Context, orgID, runID uuid.UUID, explanation, category string, nextSteps []string, retryAdvised bool) error {
	query := `
		UPDATE agent_runs
		SET failure_explanation = @failure_explanation,
		    failure_category = @failure_category,
		    failure_next_steps = @failure_next_steps,
		    failure_retry_advised = @failure_retry_advised
		WHERE id = @id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":                    runID,
		"org_id":                orgID,
		"failure_explanation":   explanation,
		"failure_category":      category,
		"failure_next_steps":    nextSteps,
		"failure_retry_advised": retryAdvised,
	})
	return err
}

func (s *AgentRunStore) CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM agent_runs WHERE org_id = @org_id AND status = 'running'`, pgx.NamedArgs{"org_id": orgID}).Scan(&count)
	return count, err
}

func (s *AgentRunStore) ListByIssue(ctx context.Context, orgID, issueID uuid.UUID) ([]models.AgentRun, error) {
	query := `
		SELECT id, issue_id, org_id, agent_type, status, autonomy_level, token_mode,
		       complexity_tier, confidence_score, confidence_reasoning, risk_factors,
		       container_id, started_at, completed_at, token_usage,
		       failure_explanation, failure_category, failure_next_steps, failure_retry_advised,
		       parent_run_id, revision_context, error, result_summary, diff, created_at
		FROM agent_runs
		WHERE org_id = @org_id AND issue_id = @issue_id
		ORDER BY created_at DESC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"issue_id": issueID,
	})
	if err != nil {
		return nil, fmt.Errorf("query agent runs by issue: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.AgentRun])
}
