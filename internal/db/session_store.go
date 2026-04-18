package db

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// safePassExprRE validates that a pass expression only contains safe SQL tokens
// (column names, casts, functions like COALESCE). This prevents accidental SQL
// injection if a non-constant expression is ever passed to diffHistoryAppendSQL.
var safePassExprRE = regexp.MustCompile(`^[a-zA-Z0-9_():@, +]+$`)

type SessionStore struct {
	db DBTX
}

func NewSessionStore(db DBTX) *SessionStore {
	return &SessionStore{db: db}
}

type SessionFilters struct {
	Statuses           []models.SessionStatus // When non-empty, filter to sessions matching any of these statuses.
	Limit              int
	CursorTime         *time.Time // Cursor-based pagination: created_at of the last item.
	CursorID           *uuid.UUID // Cursor-based pagination: id of the last item.
	AdHocOnly          bool       // When true, only return runs where pm_plan_id IS NULL (not linked to a PM plan).
	RepositoryID       uuid.UUID  // When non-zero, filter sessions by repository via issues table.
	TriggeredByUserID  uuid.UUID  // When non-zero, filter sessions to those triggered by this user.
	Search             string     // When non-empty, filter sessions by title (case-insensitive prefix/substring match).
	IncludeArchived    bool       // When true, include archived sessions in the results.
	OnlyArchived       bool       // When true, return only archived sessions.
}

// sessionSelectColumns is used for single-session queries where we want all fields.
const sessionSelectColumns = `id, COALESCE(issue_id, '00000000-0000-0000-0000-000000000000'::uuid) AS issue_id,
	org_id, agent_type, status, autonomy_level, token_mode,
	complexity_tier, confidence_score, confidence_reasoning, risk_factors,
	container_id, started_at, completed_at, token_usage,
	failure_explanation, failure_category, failure_next_steps, failure_retry_advised,
	parent_session_id, revision_context, error, result_summary, diff,
	pm_plan_id, title, pm_approach, pm_reasoning, project_task_id,
	model_override, triggered_by_user_id, agent_session_id, current_turn, last_activity_at,
	sandbox_state, snapshot_key, target_branch, working_branch, repository_id, diff_stats, diff_history, input_manifest, archived_at, archived_by_user_id, automation_run_id, deleted_at, created_at`

// sessionListColumns excludes large JSONB blobs (diff_history) from list queries
// to avoid returning multi-megabyte payloads when listing many sessions.
const sessionListColumns = `id, COALESCE(issue_id, '00000000-0000-0000-0000-000000000000'::uuid) AS issue_id,
	org_id, agent_type, status, autonomy_level, token_mode,
	complexity_tier, confidence_score, confidence_reasoning, risk_factors,
	container_id, started_at, completed_at, token_usage,
	failure_explanation, failure_category, failure_next_steps, failure_retry_advised,
	parent_session_id, revision_context, error, result_summary, diff,
	pm_plan_id, title, pm_approach, pm_reasoning, project_task_id,
	model_override, triggered_by_user_id, agent_session_id, current_turn, last_activity_at,
	sandbox_state, snapshot_key, target_branch, working_branch, repository_id, diff_stats, NULL::jsonb AS diff_history, input_manifest, archived_at, archived_by_user_id, automation_run_id, deleted_at, created_at`

// maxDiffHistoryEntries caps the number of entries kept in diff_history.
// Older entries beyond this limit are pruned when a new entry is appended.
const maxDiffHistoryEntries = 20

// diffHistoryAppendSQL returns the SQL fragment for appending to diff_history
// with a cap of maxDiffHistoryEntries. The caller must supply @diff, @diff_stats,
// and a pass-number expression (e.g. "@current_turn::int" or "COALESCE(current_turn, 0) + 1").
//
// IMPORTANT: passExpr is interpolated directly into SQL via fmt.Sprintf.
// It MUST be a trusted constant expression — never pass user-controlled input.
// The function panics if passExpr contains characters outside [a-zA-Z0-9_():@, +].
func diffHistoryAppendSQL(passExpr string) string {
	if !safePassExprRE.MatchString(passExpr) {
		panic(fmt.Sprintf("diffHistoryAppendSQL: unsafe passExpr: %q", passExpr))
	}
	return fmt.Sprintf(`CASE WHEN @diff::text IS NOT NULL THEN
	  (SELECT jsonb_agg(elem) FROM (
	    SELECT elem FROM jsonb_array_elements(
	      COALESCE(diff_history, '[]'::jsonb) || jsonb_build_array(jsonb_build_object(
	        'pass', %s,
	        'diff', @diff::text,
	        'diff_stats', COALESCE(@diff_stats::jsonb, '{}'::jsonb),
	        'created_at', now()
	      ))
	    ) WITH ORDINALITY AS t(elem, ord)
	    ORDER BY ord DESC
	    LIMIT %d
	  ) AS trimmed)
	ELSE diff_history END`, passExpr, maxDiffHistoryEntries)
}

// computeDiffStatsForResult computes diff stats from a SessionResult's diff.
func computeDiffStatsForResult(result *models.SessionResult) json.RawMessage {
	if result.Diff == nil {
		return nil
	}
	return models.ComputeDiffStats(*result.Diff)
}

func (s *SessionStore) ListByOrg(ctx context.Context, orgID uuid.UUID, filters SessionFilters) ([]models.Session, error) {
	args := pgx.NamedArgs{"org_id": orgID}

	query := `
		SELECT ` + sessionListColumns + `
		FROM sessions
		WHERE org_id = @org_id AND deleted_at IS NULL`

	if filters.OnlyArchived {
		query += ` AND archived_at IS NOT NULL`
	} else if !filters.IncludeArchived {
		query += ` AND archived_at IS NULL`
	}

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
	if filters.TriggeredByUserID != uuid.Nil {
		query += ` AND triggered_by_user_id = @triggered_by_user_id`
		args["triggered_by_user_id"] = filters.TriggeredByUserID
	}
	if filters.Search != "" {
		query += ` AND title ILIKE @search`
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(filters.Search)
		args["search"] = "%" + escaped + "%"
	}
	if filters.AdHocOnly {
		query += ` AND pm_plan_id IS NULL`
	}
	if filters.CursorTime != nil && filters.CursorID != nil {
		query += ` AND (created_at, id) < (@cursor_time, @cursor_id)`
		args["cursor_time"] = *filters.CursorTime
		args["cursor_id"] = *filters.CursorID
	}

	// Uses partial index idx_sessions_deleted (org_id, created_at DESC, id DESC)
	// WHERE deleted_at IS NULL for efficient filtering and sort.
	query += ` ORDER BY created_at DESC, id DESC`

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
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`

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
			model_override, triggered_by_user_id, target_branch, repository_id, automation_run_id
		)
		VALUES (
			@issue_id, @org_id, @agent_type, @status, @autonomy_level, @token_mode, @complexity_tier,
			@parent_session_id, @revision_context, @pm_plan_id, @title, @pm_approach, @pm_reasoning, @project_task_id,
			@model_override, @triggered_by_user_id, @target_branch, @repository_id, @automation_run_id
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
		"automation_run_id":     run.AutomationRunID,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&run.ID, &run.CreatedAt)
}

func (s *SessionStore) UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status string) error {
	query := `UPDATE sessions SET status = @status WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	if status == "running" {
		query = `UPDATE sessions SET status = @status, started_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	} else if status == "completed" || status == "failed" || status == "cancelled" {
		query = `UPDATE sessions SET status = @status, completed_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	}
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     runID,
		"org_id": orgID,
		"status": status,
	})
	return err
}

func (s *SessionStore) UpdatePMPlanID(ctx context.Context, orgID, runID, planID uuid.UUID) error {
	query := `UPDATE sessions SET pm_plan_id = @pm_plan_id WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":         runID,
		"org_id":     orgID,
		"pm_plan_id": planID,
	})
	return err
}

func (s *SessionStore) UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status string, result *models.SessionResult) error {
	diffStats := computeDiffStatsForResult(result)

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
		    result_summary = @result_summary, diff = @diff, error = @error,
		    diff_stats = @diff_stats,
		    diff_history = ` + diffHistoryAppendSQL("COALESCE(current_turn, 0) + 1") + `
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`

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
		"diff_stats":           diffStats,
	})
	return err
}

// ClaimIdle atomically transitions an idle session to running and returns the
// claimed session row. Used when a user sends a follow-up message so only one
// continuation can be queued at a time.
// Sessions whose sandbox snapshot has been destroyed cannot be claimed.
func (s *SessionStore) ClaimIdle(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	query := `
		UPDATE sessions
		SET status = 'running'
		WHERE id = @id AND org_id = @org_id AND status = 'idle'
		  AND sandbox_state != 'destroyed'
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
// Sessions whose sandbox snapshot has been destroyed cannot be resumed.
func (s *SessionStore) ClaimForResume(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	query := `
		UPDATE sessions
		SET status = 'running', completed_at = NULL
		WHERE id = @id AND org_id = @org_id AND status IN ('completed', 'pr_created', 'failed', 'cancelled')
		  AND sandbox_state != 'destroyed'
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
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`

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

var (
	// ErrSessionNotFound is returned when the session does not exist.
	ErrSessionNotFound = fmt.Errorf("session not found")
	// ErrSessionNotFailed is returned when trying to retry a session that is not in failed status.
	ErrSessionNotFailed = fmt.Errorf("session is not in failed status")
	// ErrSessionAlreadyArchived is returned when trying to archive an already-archived session.
	ErrSessionAlreadyArchived = fmt.Errorf("session not found or already archived")
	// ErrSessionNotArchived is returned when trying to unarchive a session that is not archived.
	ErrSessionNotArchived = fmt.Errorf("session not found or not archived")
)

// ResetForRetry resets a failed session back to pending so it can be re-run.
// It clears failure fields, result fields, timestamps, and error state.
func (s *SessionStore) ResetForRetry(ctx context.Context, orgID, sessionID uuid.UUID) error {
	// First check if the session exists and its current status.
	var currentStatus string
	err := s.db.QueryRow(ctx,
		`SELECT status FROM sessions WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`,
		pgx.NamedArgs{"id": sessionID, "org_id": orgID},
	).Scan(&currentStatus)
	if err != nil {
		return ErrSessionNotFound
	}
	if currentStatus != "failed" {
		return ErrSessionNotFailed
	}

	query := `
		UPDATE sessions
		SET status = 'pending',
		    started_at = NULL,
		    completed_at = NULL,
		    error = NULL,
		    failure_explanation = NULL,
		    failure_category = NULL,
		    failure_next_steps = NULL,
		    failure_retry_advised = false,
		    result_summary = NULL,
		    confidence_score = NULL,
		    confidence_reasoning = NULL,
		    risk_factors = NULL,
		    token_usage = NULL,
		    diff = NULL,
		    diff_stats = NULL
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`

	_, err = s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	})
	return err
}

// UndoResetForRetry reverts a session back to failed status after a retry enqueue failure.
func (s *SessionStore) UndoResetForRetry(ctx context.Context, orgID, sessionID uuid.UUID, explanation, category string) error {
	query := `
		UPDATE sessions
		SET status = 'failed',
		    completed_at = now(),
		    failure_explanation = @failure_explanation,
		    failure_category = @failure_category,
		    failure_retry_advised = true
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":                  sessionID,
		"org_id":              orgID,
		"failure_explanation": explanation,
		"failure_category":    category,
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
		SELECT ` + sessionListColumns + `
		FROM sessions
		WHERE org_id = @org_id AND issue_id = @issue_id AND deleted_at IS NULL
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
		SELECT ` + sessionListColumns + `
		FROM sessions
		WHERE org_id = @org_id AND status = ANY(@statuses) AND deleted_at IS NULL
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
		SELECT ` + sessionListColumns + `
		FROM sessions
		WHERE org_id = @org_id AND id = ANY(@ids) AND deleted_at IS NULL`

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
// and updates multi-turn metadata. It also computes diff_stats and appends
// a snapshot to diff_history for diff-between-passes review.
func (s *SessionStore) UpdateTurnComplete(ctx context.Context, orgID, sessionID uuid.UUID, turn int, result *models.SessionResult, agentSessionID, snapshotKey string) error {
	diffStats := computeDiffStatsForResult(result)

	query := `
		UPDATE sessions
		SET status = 'idle', current_turn = @current_turn, last_activity_at = now(),
		    agent_session_id = @agent_session_id, snapshot_key = @snapshot_key,
		    sandbox_state = 'snapshotted',
		    confidence_score = @confidence_score, confidence_reasoning = @confidence_reasoning,
		    risk_factors = @risk_factors, token_usage = @token_usage,
		    result_summary = @result_summary, diff = @diff, error = @error,
		    diff_stats = @diff_stats,
		    diff_history = ` + diffHistoryAppendSQL("@current_turn::int") + `
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
		"diff_stats":           diffStats,
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

// ListStalePendingSessions returns pending sessions created before the given
// cutoff. These sessions have been stuck in pending for too long and should be
// failed with an explanatory error.
func (s *SessionStore) ListStalePendingSessions(ctx context.Context, createdBefore time.Time) ([]models.Session, error) {
	query := `
		SELECT ` + sessionListColumns + `
		FROM sessions s
		WHERE s.status = 'pending'
		  AND s.deleted_at IS NULL
		  AND s.created_at < @created_before
		ORDER BY s.created_at ASC
		LIMIT 100`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"created_before": createdBefore,
	})
	if err != nil {
		return nil, fmt.Errorf("query stale pending sessions: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
}

// ListStaleIdleSessions returns idle sessions that have been inactive longer
// than the idle timeout. These sessions should be transitioned to completed
// but their snapshots are preserved for later resumption.
func (s *SessionStore) ListStaleIdleSessions(ctx context.Context, olderThan time.Time) ([]models.Session, error) {
	query := `
		SELECT ` + sessionListColumns + `
		FROM sessions
		WHERE status = 'idle'
		  AND deleted_at IS NULL
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
// Note: intentionally does NOT filter by deleted_at IS NULL — we want to
// clean up snapshots even for soft-deleted sessions to free storage.
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

// Archive marks a session as archived, hiding it from default list views.
func (s *SessionStore) Archive(ctx context.Context, orgID, sessionID, userID uuid.UUID) error {
	query := `UPDATE sessions SET archived_at = now(), archived_by_user_id = @user_id, updated_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NULL`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":      sessionID,
		"org_id":  orgID,
		"user_id": userID,
	})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionAlreadyArchived
	}
	return nil
}

// Unarchive removes the archived flag from a session, restoring it to default views.
func (s *SessionStore) Unarchive(ctx context.Context, orgID, sessionID uuid.UUID) error {
	query := `UPDATE sessions SET archived_at = NULL, archived_by_user_id = NULL, updated_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NOT NULL`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrSessionNotArchived
	}
	return nil
}

// SoftDelete marks a session as deleted without removing the row.
// Child rows (logs, messages, threads, etc.) remain intact for audit purposes.
func (s *SessionStore) SoftDelete(ctx context.Context, orgID, sessionID uuid.UUID) error {
	query := `UPDATE sessions SET deleted_at = now(), updated_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	})
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session not found or already deleted")
	}
	return nil
}
