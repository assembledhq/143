package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type SessionStore struct {
	db DBTX
}

func NewSessionStore(db DBTX) *SessionStore {
	return &SessionStore{db: db}
}

type SessionFilters struct {
	Status    models.SessionStatus
	Limit     int
	Cursor    string
	AdHocOnly bool // When true, only return runs where pm_plan_id IS NULL (not linked to a PM plan).
}

const sessionSelectColumns = `id, COALESCE(issue_id, '00000000-0000-0000-0000-000000000000'::uuid) AS issue_id,
	org_id, agent_type, status, autonomy_level, token_mode,
	complexity_tier, confidence_score, confidence_reasoning, risk_factors,
	container_id, started_at, completed_at, token_usage,
	failure_explanation, failure_category, failure_next_steps, failure_retry_advised,
	parent_session_id, revision_context, error, result_summary, diff,
	pm_plan_id, pm_approach, pm_reasoning, project_task_id,
	model_override, created_at`

func (s *SessionStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters SessionFilters) ([]models.Session, error) {
	query := `
		SELECT ` + sessionSelectColumns + `
		FROM sessions
		WHERE org_id = @org_id`

	args := pgx.NamedArgs{"org_id": orgID}

	if filters.Status != "" {
		query += ` AND status = @status`
		args["status"] = string(filters.Status)
	}
	if filters.AdHocOnly {
		query += ` AND pm_plan_id IS NULL`
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
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
}

func (s *SessionStore) GetByID(ctx context.Context, orgID, runID uuid.UUID) (models.Session, error) {
	query := `
		SELECT ` + sessionSelectColumns + `
		FROM sessions
		WHERE id = @id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     runID,
		"org_id": orgID,
	})
	if err != nil {
		return models.Session{}, fmt.Errorf("query session: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
}

func (s *SessionStore) Create(ctx context.Context, run *models.Session) error {
	query := `
		INSERT INTO sessions (
			issue_id, org_id, agent_type, status, autonomy_level, token_mode, complexity_tier,
			parent_session_id, revision_context, pm_plan_id, pm_approach, pm_reasoning, project_task_id,
			model_override
		)
		VALUES (
			@issue_id, @org_id, @agent_type, @status, @autonomy_level, @token_mode, @complexity_tier,
			@parent_session_id, @revision_context, @pm_plan_id, @pm_approach, @pm_reasoning, @project_task_id,
			@model_override
		)
		RETURNING id, created_at`

	var issueID interface{} = run.IssueID
	if run.IssueID == uuid.Nil {
		issueID = nil
	}

	args := pgx.NamedArgs{
		"issue_id":         issueID,
		"org_id":           run.OrgID,
		"agent_type":       run.AgentType,
		"status":           run.Status,
		"autonomy_level":   run.AutonomyLevel,
		"token_mode":       run.TokenMode,
		"complexity_tier":  run.ComplexityTier,
		"parent_session_id":    run.ParentSessionID,
		"revision_context": run.RevisionContext,
		"pm_plan_id":       run.PMPlanID,
		"pm_approach":      run.PMApproach,
		"pm_reasoning":     run.PMReasoning,
		"project_task_id":  run.ProjectTaskID,
		"model_override":   run.ModelOverride,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&run.ID, &run.CreatedAt)
}

func (s *SessionStore) UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status string) error {
	query := `UPDATE sessions SET status = @status WHERE id = @id AND org_id = @org_id`
	if status == "running" {
		query = `UPDATE sessions SET status = @status, started_at = now() WHERE id = @id AND org_id = @org_id`
	} else if status == "completed" || status == "failed" || status == "cancelled" {
		query = `UPDATE sessions SET status = @status, completed_at = now() WHERE id = @id AND org_id = @org_id`
	}
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     runID,
		"org_id": orgID,
		"status": status,
	})
	return err
}

func (s *SessionStore) UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status string, result *models.SessionResult) error {
	query := `
		UPDATE sessions
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

func (s *SessionStore) UpdateFailure(ctx context.Context, orgID, runID uuid.UUID, explanation, category string, nextSteps []string, retryAdvised bool) error {
	query := `
		UPDATE sessions
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

func (s *SessionStore) UpdateTitle(ctx context.Context, orgID, sessionID uuid.UUID, title string) error {
	query := `UPDATE sessions SET pm_approach = @pm_approach WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":          sessionID,
		"org_id":      orgID,
		"pm_approach": title,
	})
	return err
}

func (s *SessionStore) CountRunningByOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE org_id = @org_id AND status = 'running'`, pgx.NamedArgs{"org_id": orgID}).Scan(&count)
	return count, err
}

func (s *SessionStore) ListByIssue(ctx context.Context, orgID, issueID uuid.UUID) ([]models.Session, error) {
	query := `
		SELECT ` + sessionSelectColumns + `
		FROM sessions
		WHERE org_id = @org_id AND issue_id = @issue_id
		ORDER BY created_at DESC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"issue_id": issueID,
	})
	if err != nil {
		return nil, fmt.Errorf("query sessions by issue: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
}

func (s *SessionStore) ListRecentByOrg(ctx context.Context, orgID uuid.UUID, statuses []string, limit int) ([]models.Session, error) {
	query := `
		SELECT ` + sessionSelectColumns + `
		FROM sessions
		WHERE org_id = @org_id AND status = ANY(@statuses)
		ORDER BY created_at DESC`

	if limit <= 0 || limit > 200 {
		limit = 20
	}
	query += fmt.Sprintf(` LIMIT %d`, limit)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"statuses": statuses,
	})
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
}

func (s *SessionStore) ListByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]models.Session, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	query := `
		SELECT ` + sessionSelectColumns + `
		FROM sessions
		WHERE org_id = @org_id AND id = ANY(@ids)`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID,
		"ids":    ids,
	})
	if err != nil {
		return nil, fmt.Errorf("query sessions by ids: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
}
