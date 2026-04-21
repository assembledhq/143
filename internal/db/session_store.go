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
	Statuses []models.SessionStatus // When non-empty, filter to sessions matching any of these statuses.
	Limit    int
	// Cursor-based pagination on (last_activity_at, id). Because last_activity_at
	// mutates as sessions get bumped, callers should expect the standard MRU
	// pagination drift: the primary failure mode is that a row bumped after
	// page N was fetched will be *skipped on a later page* (it moved ahead of
	// the cursor). The inverse — a duplicate reappearing on an earlier page —
	// only happens if the same page is re-fetched. The frontend dedupes by id,
	// so this manifests as occasional reordering, not data loss.
	CursorTime        *time.Time
	CursorID          *uuid.UUID
	AdHocOnly         bool      // When true, only return runs where pm_plan_id IS NULL (not linked to a PM plan).
	RepositoryID      uuid.UUID // When non-zero, filter sessions by repository via issues table.
	TriggeredByUserID uuid.UUID // When non-zero, filter sessions to those triggered by this user.
	Search            string    // When non-empty, filter sessions by title (case-insensitive prefix/substring match).
	IncludeArchived   bool      // When true, include archived sessions in the results.
	OnlyArchived      bool      // When true, return only archived sessions.
}

// SessionCountsFilters scopes CountsByOrg to a subset of sessions.
// Status, archived, and search are not accepted — the counts endpoint
// always returns totals for the all/active/archived buckets.
type SessionCountsFilters struct {
	RepositoryID      uuid.UUID
	TriggeredByUserID uuid.UUID
}

// sessionCountsCap bounds each count subquery so a single user with millions
// of sessions can't turn the counts endpoint into a table scan. Clients render
// any bucket that hits the cap as e.g. "99+".
//
// LIMIT only short-circuits when an index lets Postgres stop early. The three
// buckets rely on:
//   - all:      idx_sessions_not_archived (archived_at IS NULL, deleted_at IS NULL)
//   - active:   idx_sessions_not_archived, filtered in-line by status
//   - archived: idx_sessions_archived     (archived_at IS NOT NULL, deleted_at IS NULL)
//
// If either partial index is missing, an empty bucket can still force a full
// slice scan of the (org_id, created_at) range — keep both in place.
const sessionCountsCap = 100

// sessionSelectColumns is used for single-session queries where we want all fields.
const sessionSelectColumns = `id, COALESCE(issue_id, '00000000-0000-0000-0000-000000000000'::uuid) AS issue_id,
	org_id, agent_type, status, autonomy_level, token_mode,
	complexity_tier, confidence_score, confidence_reasoning, risk_factors,
	container_id, turn_holding_container, started_at, completed_at, token_usage,
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
	container_id, turn_holding_container, started_at, completed_at, token_usage,
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
		query += ` AND (last_activity_at, id) < (@cursor_time, @cursor_id)`
		args["cursor_time"] = *filters.CursorTime
		args["cursor_id"] = *filters.CursorID
	}

	// Uses partial index idx_sessions_last_activity
	// (org_id, last_activity_at DESC, id DESC) WHERE deleted_at IS NULL for
	// efficient filtering and MRU sort.
	query += ` ORDER BY last_activity_at DESC, id DESC`

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

// CountsByOrg returns non-archived, active, and archived session counts for
// the org, optionally narrowed by repository and user. Each bucket is counted
// via a LIMIT-bounded subquery; worst-case cost is O(cap) per bucket as long
// as an index with the bucket's predicate exists (see sessionCountsCap).
func (s *SessionStore) CountsByOrg(ctx context.Context, orgID uuid.UUID, filters SessionCountsFilters) (models.SessionCounts, error) {
	args := pgx.NamedArgs{
		"org_id": orgID,
		"cap":    sessionCountsCap,
	}

	// Shared scope predicates applied to every bucket. We avoid an extra SQL
	// branch by only building the fragment when a filter is set.
	var scope string
	if filters.RepositoryID != uuid.Nil {
		scope += " AND repository_id = @repository_id"
		args["repository_id"] = filters.RepositoryID
	}
	if filters.TriggeredByUserID != uuid.Nil {
		scope += " AND triggered_by_user_id = @triggered_by_user_id"
		args["triggered_by_user_id"] = filters.TriggeredByUserID
	}

	activeStrings := make([]string, len(models.ActiveStatuses))
	for i, status := range models.ActiveStatuses {
		activeStrings[i] = string(status)
	}
	args["active_statuses"] = activeStrings

	query := fmt.Sprintf(`
		SELECT
			(SELECT count(*) FROM (
				SELECT 1 FROM sessions
				WHERE org_id = @org_id AND deleted_at IS NULL AND archived_at IS NULL%s
				LIMIT @cap
			) t_all) AS all_count,
			(SELECT count(*) FROM (
				SELECT 1 FROM sessions
				WHERE org_id = @org_id AND deleted_at IS NULL AND archived_at IS NULL AND status = ANY(@active_statuses)%s
				LIMIT @cap
			) t_active) AS active_count,
			(SELECT count(*) FROM (
				SELECT 1 FROM sessions
				WHERE org_id = @org_id AND deleted_at IS NULL AND archived_at IS NOT NULL%s
				LIMIT @cap
			) t_archived) AS archived_count`, scope, scope, scope)

	var out models.SessionCounts
	if err := s.db.QueryRow(ctx, query, args).Scan(&out.All, &out.Active, &out.Archived); err != nil {
		return models.SessionCounts{}, fmt.Errorf("query session counts: %w", err)
	}
	out.Cap = sessionCountsCap
	return out, nil
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
		RETURNING id, created_at, last_activity_at`

	var issueID interface{} = run.IssueID
	if run.IssueID == uuid.Nil {
		issueID = nil
	}

	args := pgx.NamedArgs{
		"issue_id":             issueID,
		"org_id":               run.OrgID,
		"agent_type":           run.AgentType,
		"status":               run.Status,
		"autonomy_level":       run.AutonomyLevel,
		"token_mode":           run.TokenMode,
		"complexity_tier":      run.ComplexityTier,
		"parent_session_id":    run.ParentSessionID,
		"revision_context":     run.RevisionContext,
		"pm_plan_id":           run.PMPlanID,
		"title":                run.Title,
		"pm_approach":          run.PMApproach,
		"pm_reasoning":         run.PMReasoning,
		"project_task_id":      run.ProjectTaskID,
		"model_override":       run.ModelOverride,
		"triggered_by_user_id": run.TriggeredByUserID,
		"target_branch":        run.TargetBranch,
		"repository_id":        run.RepositoryID,
		"automation_run_id":    run.AutomationRunID,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&run.ID, &run.CreatedAt, &run.LastActivityAt)
}

func (s *SessionStore) UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status string) error {
	query := `UPDATE sessions SET status = @status, last_activity_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	if status == "running" {
		// Clear completed_at so a resumed session doesn't display as "completed"
		// while actively running. Duration is computed from started_at, so that is
		// also refreshed to reflect the current run.
		query = `UPDATE sessions SET status = @status, started_at = now(), completed_at = NULL, last_activity_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	} else if status == "completed" || status == "failed" || status == "cancelled" {
		query = `UPDATE sessions SET status = @status, completed_at = now(), last_activity_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	}
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     runID,
		"org_id": orgID,
		"status": status,
	})
	return err
}

// UpdatePMPlanID links a session to a PM plan. Bumps last_activity_at so the
// method is self-contained — callers do not have to remember to pair it with
// a separate activity bump. Today's sole caller already calls UpdateResult
// microseconds earlier, so this is a redundant write on the hot path, but the
// cost (one UPDATE per plan creation) is negligible versus the coupling risk
// of a future caller silently skipping the MRU bump.
func (s *SessionStore) UpdatePMPlanID(ctx context.Context, orgID, runID, planID uuid.UUID) error {
	query := `UPDATE sessions SET pm_plan_id = @pm_plan_id, last_activity_at = now() WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":         runID,
		"org_id":     orgID,
		"pm_plan_id": planID,
	})
	return err
}

// UpdateResult persists a turn result and status transition. Always bumps
// last_activity_at because every call represents user-driven activity (the
// agent finished processing a user turn). Do NOT call this from reaper /
// sweeper code paths — use a status-only update instead, otherwise dormant
// sessions will resurface at the top of the MRU-ordered list.
func (s *SessionStore) UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status string, result *models.SessionResult) error {
	diffStats := computeDiffStatsForResult(result)

	query := `
		UPDATE sessions
		SET status = @status,
		    last_activity_at = now(),
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
		SET status = 'running', started_at = now(), completed_at = NULL, last_activity_at = now()
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
		SET status = 'running', completed_at = NULL, last_activity_at = now()
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
		    failure_retry_advised = @failure_retry_advised,
		    last_activity_at = now()
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
		    diff_stats = NULL,
		    last_activity_at = now()
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
		    failure_retry_advised = true,
		    last_activity_at = now()
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
	query := `UPDATE sessions SET title = @title, last_activity_at = now() WHERE id = @id AND org_id = @org_id`
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
//
// Deliberately does NOT touch last_activity_at: the orchestrator calls
// UpdateResult (which bumps last_activity_at) immediately before this, so an
// additional bump here is redundant and would double-write the column on
// every snapshot.
func (s *SessionStore) UpdateSnapshotInfo(ctx context.Context, orgID, sessionID uuid.UUID, agentSessionID, snapshotKey string) error {
	query := `
		UPDATE sessions
		SET agent_session_id = @agent_session_id, snapshot_key = @snapshot_key,
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

// UpdateSandboxState changes only the sandbox lifecycle column. It deliberately
// does NOT touch last_activity_at because the reaper calls this to mark
// long-completed sessions as 'destroyed' during snapshot cleanup — bumping the
// MRU timestamp there would resurface dormant sessions at the top of the list.
// Caller-driven activity (turn results, status transitions) bumps last_activity_at
// through its own update path.
func (s *SessionStore) UpdateSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, state string) error {
	query := `UPDATE sessions SET sandbox_state = @sandbox_state WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":            sessionID,
		"org_id":        orgID,
		"sandbox_state": state,
	})
	return err
}

// AcquireTurnHold marks the agent turn as a holder of the sandbox container.
// It uses COALESCE so a concurrently-published container_id (e.g. from a
// preview that hydrated first) is preserved rather than overwritten. The
// returned actualContainerID is the ID that is now stored on the row:
//   - Equal to proposedContainerID → we won the race; the caller's sandbox is
//     the authoritative one.
//   - Different from proposedContainerID → another caller published first; the
//     caller should destroy their just-created sandbox and attach to
//     actualContainerID instead.
//
// Paired with ReleaseTurnHold, it forms half of the refcount that governs
// container destruction (the other half is preview_holding_container on the
// preview_instances row).
//
// Deliberately does NOT bump last_activity_at — the caller (orchestrator)
// already writes status='running' via UpdateStatus on the same code path, and
// double-bumping would waste writes.
func (s *SessionStore) AcquireTurnHold(ctx context.Context, orgID, sessionID uuid.UUID, proposedContainerID string) (actualContainerID string, err error) {
	query := `UPDATE sessions
		SET container_id = COALESCE(container_id, @container_id),
		    turn_holding_container = TRUE
		WHERE id = @id AND org_id = @org_id
		RETURNING COALESCE(container_id, '')`
	if err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":           sessionID,
		"org_id":       orgID,
		"container_id": proposedContainerID,
	}).Scan(&actualContainerID); err != nil {
		return "", fmt.Errorf("acquire turn hold: %w", err)
	}
	return actualContainerID, nil
}

// ReleaseTurnHold flips turn_holding_container to false and returns the
// sibling holder state so the caller can decide whether to destroy the
// container. The RETURNING clause reads both the container_id and the active
// preview hold in one round-trip, eliminating the TOCTOU gap between release
// and destroy decision.
//
// destroyNow is true when, at the time of the release, no other holder was
// active. Callers MUST NOT act on this signal directly: pass containerID into
// FinalizeContainerDestroy, which atomically re-checks holders and clears
// container_id only if still safe. destroyNow is false when the preview still
// holds the container — the caller must leave both container_id and the
// container itself alive.
//
// containerID is the ID that was recorded on the row (empty if the session
// had no live container — a no-op release).
func (s *SessionStore) ReleaseTurnHold(ctx context.Context, orgID, sessionID uuid.UUID) (destroyNow bool, containerID string, err error) {
	query := `WITH released AS (
			UPDATE sessions
			SET turn_holding_container = FALSE
			WHERE id = @id AND org_id = @org_id
			RETURNING id, container_id
		)
		SELECT
			COALESCE(released.container_id, '') AS container_id,
			COALESCE((
				SELECT TRUE
				FROM preview_instances
				WHERE session_id = released.id
				  AND org_id = @org_id
				  AND preview_holding_container = TRUE
				LIMIT 1
			), FALSE) AS preview_holds
		FROM released`

	var cid string
	var previewHolds bool
	err = s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	}).Scan(&cid, &previewHolds)
	if err != nil {
		return false, "", fmt.Errorf("release turn hold: %w", err)
	}
	return cid != "" && !previewHolds, cid, nil
}

// PublishHydratedContainerID is the preview-hydrate CAS: a preview has just
// created a container from the session's snapshot and wants to publish its
// ID so a concurrent ContinueSession can attach to the same container.
//
// The UPDATE only writes container_id when the row's current container_id IS
// NULL, so a concurrent orchestrator that already published one wins the race
// and the preview becomes the loser. The returned actualContainerID is the ID
// now stored on the row:
//   - Equal to proposedContainerID → we won; caller's sandbox is authoritative.
//   - Different → the caller must destroy its just-created sandbox and attach
//     to actualContainerID instead.
//
// Unlike AcquireTurnHold, this does NOT flip turn_holding_container — the
// orchestrator owns that flag and the preview must not claim it. It also
// marks sandbox_state=running so the reaper and the reuse path see the live
// container.
func (s *SessionStore) PublishHydratedContainerID(ctx context.Context, orgID, sessionID uuid.UUID, proposedContainerID string) (actualContainerID string, err error) {
	query := `UPDATE sessions
		SET container_id = COALESCE(container_id, @container_id),
		    sandbox_state = CASE WHEN container_id IS NULL THEN 'running' ELSE sandbox_state END
		WHERE id = @id AND org_id = @org_id
		RETURNING COALESCE(container_id, '')`
	if err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":           sessionID,
		"org_id":       orgID,
		"container_id": proposedContainerID,
	}).Scan(&actualContainerID); err != nil {
		return "", fmt.Errorf("publish hydrated container id: %w", err)
	}
	return actualContainerID, nil
}

// ClearContainerID sets container_id back to NULL unconditionally. Used by
// the startup reconciler for orphaned containers discovered out-of-band.
// Lifecycle code paths (orchestrator / preview manager) should call
// FinalizeContainerDestroy instead, which does an atomic CAS that closes the
// race between release and destroy.
func (s *SessionStore) ClearContainerID(ctx context.Context, orgID, sessionID uuid.UUID) error {
	query := `UPDATE sessions SET container_id = NULL WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	})
	if err != nil {
		return fmt.Errorf("clear container id: %w", err)
	}
	return nil
}

// FinalizeContainerDestroy atomically clears container_id and marks
// sandbox_state='snapshotted', but only when the caller's view of the world
// still holds: no holder is active and the container_id still matches
// expectedContainerID. Returns true when the row was updated (the caller owns
// the destroy), false when another holder has come back (the caller must
// leave the container alone — someone else is using it).
//
// This is the TOCTOU-safe companion to ReleaseTurnHold / ReleasePreviewHold.
// A release reports "destroyNow=true" based on a snapshot of state inside its
// own SQL, but in the window between release and the Go-side destroy a new
// holder can acquire. FinalizeContainerDestroy re-checks atomically: if the
// new holder has set turn_holding_container=TRUE, or a preview has set
// preview_holding_container=TRUE, the UPDATE matches zero rows and destroy
// is skipped. Clearing container_id and destroying the container is the
// ordering that prevents new reuse-path readers from attaching to a dying ID.
func (s *SessionStore) FinalizeContainerDestroy(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (cleared bool, err error) {
	query := `UPDATE sessions
		SET container_id = NULL, sandbox_state = 'snapshotted'
		WHERE id = @id
		  AND org_id = @org_id
		  AND container_id = @expected
		  AND turn_holding_container = FALSE
		  AND NOT EXISTS (
		    SELECT 1 FROM preview_instances p
		    WHERE p.session_id = sessions.id
		      AND p.org_id = sessions.org_id
		      AND p.preview_holding_container = TRUE
		  )`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":       sessionID,
		"org_id":   orgID,
		"expected": expectedContainerID,
	})
	if err != nil {
		return false, fmt.Errorf("finalize container destroy: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListOrphanedContainers returns sessions whose container_id is set but no
// holder (turn or preview) is marked true. Called on startup to clean up
// containers that leaked from a crashed server — the reconciler destroys the
// container (best-effort) and then calls ClearContainerID.
//
// Returns at most 100 rows per call; the reconciler loops until it gets an
// empty slice so a backlog after a long outage doesn't block startup.
// lint:allow-no-orgid reason="startup reconciler scans across all orgs by design"
func (s *SessionStore) ListOrphanedContainers(ctx context.Context) ([]models.Session, error) {
	query := `
		SELECT ` + sessionSelectColumns + `
		FROM sessions
		WHERE container_id IS NOT NULL
		  AND turn_holding_container = FALSE
		  AND NOT EXISTS (
		    SELECT 1 FROM preview_instances p
		    WHERE p.session_id = sessions.id
		      AND p.org_id = sessions.org_id
		      AND p.preview_holding_container = TRUE
		  )
		LIMIT 100`

	rows, err := s.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list orphaned containers: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
}

// UpdateWorkingBranch sets the working branch name for a session.
func (s *SessionStore) UpdateWorkingBranch(ctx context.Context, orgID, sessionID uuid.UUID, branch string) error {
	query := `UPDATE sessions SET working_branch = @working_branch, last_activity_at = now() WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":             sessionID,
		"org_id":         orgID,
		"working_branch": branch,
	})
	return err
}

// ListStalePendingSessions returns pending sessions created before the given
// cutoff. These sessions have been stuck in pending for too long and should be
// failed with an explanatory error.
// lint:allow-no-orgid reason="cross-org reaper scan for stuck pending sessions"
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

// ListStaleRunningSessions returns running sessions whose started_at is
// older than the given cutoff. These sessions exceeded their wall-clock
// budget without the orchestrator persisting a terminal status — typically
// because the worker crashed mid-execution or a DB write failed during
// failure handling. The reaper fails them so the UI stops showing them as
// active and concurrency slots are freed.
//
// Rows with status='running' AND started_at IS NULL are excluded: the
// orchestrator always writes started_at in the same UpdateStatus call that
// sets status='running' (see UpdateStatus in this package), so such rows
// should be structurally impossible. If one ever appears, it indicates a
// corrupted write path and needs investigation rather than reaping.
// lint:allow-no-orgid reason="cross-org reaper scan for stuck running sessions"
func (s *SessionStore) ListStaleRunningSessions(ctx context.Context, startedBefore time.Time) ([]models.Session, error) {
	query := `
		SELECT ` + sessionListColumns + `
		FROM sessions s
		WHERE s.status = 'running'
		  AND s.deleted_at IS NULL
		  AND s.started_at IS NOT NULL
		  AND s.started_at < @started_before
		ORDER BY s.started_at ASC
		LIMIT 100`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"started_before": startedBefore,
	})
	if err != nil {
		return nil, fmt.Errorf("query stale running sessions: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
}

// ListStaleIdleSessions returns idle sessions that have been inactive longer
// than the idle timeout. These sessions should be transitioned to completed
// but their snapshots are preserved for later resumption.
// lint:allow-no-orgid reason="cross-org reaper scan for idle sessions"
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
// lint:allow-no-orgid reason="cross-org reaper scan for expired session snapshots"
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
	query := `UPDATE sessions SET archived_at = now(), archived_by_user_id = @user_id WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NULL`
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

// ArchiveSystem archives a session without a user actor (e.g. webhook-driven auto-archive).
// archived_by_user_id is left NULL, and an already-archived session is a no-op rather than an error.
func (s *SessionStore) ArchiveSystem(ctx context.Context, orgID, sessionID uuid.UUID) error {
	query := `UPDATE sessions SET archived_at = now(), archived_by_user_id = NULL WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NULL`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	})
	return err
}

// Unarchive removes the archived flag from a session, restoring it to default views.
// Bumps last_activity_at so the restored session surfaces at the top of the
// MRU-ordered list rather than reappearing pages deep at its old position.
func (s *SessionStore) Unarchive(ctx context.Context, orgID, sessionID uuid.UUID) error {
	query := `UPDATE sessions SET archived_at = NULL, archived_by_user_id = NULL, last_activity_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NOT NULL`
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
	query := `UPDATE sessions SET deleted_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
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
