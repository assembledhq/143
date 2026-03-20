package db

import (
	"context"
	"fmt"
	"time"

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
	Statuses     []models.SessionStatus // When non-empty, filter to sessions matching any of these statuses.
	Limit        int
	Cursor       string
	AdHocOnly    bool      // When true, only return runs where pm_plan_id IS NULL (not linked to a PM plan).
	RepositoryID uuid.UUID // When non-zero, filter sessions by repository via issues table.
}

const sessionSelectColumns = `id, COALESCE(issue_id, '00000000-0000-0000-0000-000000000000'::uuid) AS issue_id,
	org_id, agent_type, status, autonomy_level, token_mode,
	complexity_tier, confidence_score, confidence_reasoning, risk_factors,
	container_id, started_at, completed_at, token_usage,
	failure_explanation, failure_category, failure_next_steps, failure_retry_advised,
	parent_session_id, revision_context, error, result_summary, diff,
	pm_plan_id, title, pm_approach, pm_reasoning, project_task_id,
	model_override, triggered_by_user_id, agent_session_id, current_turn, last_activity_at,
	sandbox_state, snapshot_key, target_branch, working_branch, repository_id, created_at`

func (s *SessionStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters SessionFilters) ([]models.Session, error) {
	args := pgx.NamedArgs{"org_id": orgID}

	query := `
		SELECT ` + sessionSelectColumns + `
		FROM sessions
		WHERE org_id = @org_id`

	if filters.RepositoryID != uuid.Nil {
		query += ` AND repository_id = @repository_id`
		args["repository_id"] = filters.RepositoryID
	}

	if len(filters.Statuses) == 1 {
		query += ` AND status = @status`
		args["status"] = string(filters.Statuses[0])
	} else if len(filters.Statuses) > 1 {
		statusStrings := make([]string, len(filters.Statuses))
		for i, s := range filters.Statuses {
			statusStrings[i] = string(s)
		}
		query += ` AND status = ANY(@statuses)`
		args["statuses"] = statusStrings
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
			parent_session_id, revision_context, pm_plan_id, title, pm_approach, pm_reasoning, project_task_id,
			model_override, triggered_by_user_id, target_branch, repository_id
		)
		VALUES (
			@issue_id, @org_id, @agent_type, @status, @autonomy_level, @token_mode, @complexity_tier,
			@parent_session_id, @revision_context, @pm_plan_id, @title, @pm_approach, @pm_reasoning, @project_task_id,
			@model_override, @triggered_by_user_id, @target_branch, @repository_id
		)
		RETURNING id, created_at`

	var issueID interface{} = run.IssueID
	if run.IssueID == uuid.Nil {
		issueID = nil
	}

	args := pgx.NamedArgs{
		"issue_id":              issueID,
		"org_id":                run.OrgID,
		"agent_type":            run.AgentType,
		"status":                run.Status,
		"autonomy_level":        run.AutonomyLevel,
		"token_mode":            run.TokenMode,
		"complexity_tier":       run.ComplexityTier,
		"parent_session_id":     run.ParentSessionID,
		"revision_context":      run.RevisionContext,
		"pm_plan_id":            run.PMPlanID,
		"title":                 run.Title,
		"pm_approach":           run.PMApproach,
		"pm_reasoning":          run.PMReasoning,
		"project_task_id":       run.ProjectTaskID,
		"model_override":        run.ModelOverride,
		"triggered_by_user_id":  run.TriggeredByUserID,
		"target_branch":         run.TargetBranch,
		"repository_id":         run.RepositoryID,
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
		SET status = @status,
		    completed_at = CASE
		        WHEN @status IN ('completed', 'failed', 'cancelled', 'needs_human_guidance', 'pr_created', 'skipped')
		            THEN now()
		        ELSE completed_at
		    END,
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

// ClaimIdle atomically transitions an idle session to running and returns the
// claimed session row. Used when a user sends a follow-up message so only one
// continuation can be queued at a time.
func (s *SessionStore) ClaimIdle(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	query := `
		UPDATE sessions
		SET status = 'running'
		WHERE id = @id AND org_id = @org_id AND status = 'idle'
		RETURNING ` + sessionSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	})
	if err != nil {
		return models.Session{}, fmt.Errorf("claim idle session: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
}

// ClaimForResume atomically transitions a terminal session to running so it
// can be resumed with a follow-up message. Used when a user sends a message
// to a completed/failed/cancelled/pr_created session.
func (s *SessionStore) ClaimForResume(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	query := `
		UPDATE sessions
		SET status = 'running', completed_at = NULL
		WHERE id = @id AND org_id = @org_id AND status IN ('completed', 'pr_created', 'failed', 'cancelled')
		RETURNING ` + sessionSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	})
	if err != nil {
		return models.Session{}, fmt.Errorf("claim terminal session for resume: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
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
	query := `UPDATE sessions SET title = @title WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
		"title":  title,
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

// UpdateTurnComplete sets the session to idle, persists the latest turn result,
// and updates multi-turn metadata.
func (s *SessionStore) UpdateTurnComplete(ctx context.Context, orgID, sessionID uuid.UUID, turn int, result *models.SessionResult, agentSessionID, snapshotKey string) error {
	query := `
		UPDATE sessions
		SET status = 'idle', current_turn = @current_turn, last_activity_at = now(),
		    agent_session_id = @agent_session_id, snapshot_key = @snapshot_key,
		    sandbox_state = 'snapshotted',
		    confidence_score = @confidence_score, confidence_reasoning = @confidence_reasoning,
		    risk_factors = @risk_factors, token_usage = @token_usage,
		    result_summary = @result_summary, diff = @diff, error = @error
		WHERE id = @id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":                   sessionID,
		"org_id":               orgID,
		"current_turn":         turn,
		"agent_session_id":     agentSessionID,
		"snapshot_key":         snapshotKey,
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

// UpdateSnapshotInfo persists snapshot metadata without changing the session status.
// Used after the first run to store snapshot data while letting the normal
// completion flow control the status.
func (s *SessionStore) UpdateSnapshotInfo(ctx context.Context, orgID, sessionID uuid.UUID, agentSessionID, snapshotKey string) error {
	query := `
		UPDATE sessions
		SET last_activity_at = now(),
		    agent_session_id = @agent_session_id, snapshot_key = @snapshot_key,
		    sandbox_state = 'snapshotted'
		WHERE id = @id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":               sessionID,
		"org_id":           orgID,
		"agent_session_id": agentSessionID,
		"snapshot_key":     snapshotKey,
	})
	return err
}

func (s *SessionStore) UpdateSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, state string) error {
	query := `UPDATE sessions SET sandbox_state = @sandbox_state WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":            sessionID,
		"org_id":        orgID,
		"sandbox_state": state,
	})
	return err
}

// UpdateWorkingBranch sets the working branch name for a session.
func (s *SessionStore) UpdateWorkingBranch(ctx context.Context, orgID, sessionID uuid.UUID, branch string) error {
	query := `UPDATE sessions SET working_branch = @working_branch WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":              sessionID,
		"org_id":          orgID,
		"working_branch":  branch,
	})
	return err
}

// ListStaleIdleSessions returns idle sessions that have been inactive longer
// than the idle timeout. These sessions should be transitioned to completed
// but their snapshots are preserved for later resumption.
func (s *SessionStore) ListStaleIdleSessions(ctx context.Context, olderThan time.Time) ([]models.Session, error) {
	query := `
		SELECT ` + sessionSelectColumns + `
		FROM sessions
		WHERE status = 'idle'
		  AND last_activity_at < @older_than
		ORDER BY last_activity_at ASC NULLS FIRST
		LIMIT 100`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"older_than": olderThan,
	})
	if err != nil {
		return nil, fmt.Errorf("query stale idle sessions: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
}

// ListExpiredSnapshots returns non-active sessions whose snapshots have
// exceeded the maximum snapshot age and should be cleaned up from storage.
func (s *SessionStore) ListExpiredSnapshots(ctx context.Context, olderThan time.Time) ([]models.Session, error) {
	query := `
		SELECT ` + sessionSelectColumns + `
		FROM sessions
		WHERE sandbox_state = 'snapshotted'
		  AND last_activity_at < @older_than
		  AND status NOT IN ('running', 'idle')
		ORDER BY last_activity_at ASC NULLS FIRST
		LIMIT 100`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"older_than": olderThan,
	})
	if err != nil {
		return nil, fmt.Errorf("query expired snapshots: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
}
