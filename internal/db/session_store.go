package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// safePassExprRE validates that a pass expression only contains safe SQL tokens
// (column names, casts, functions like COALESCE). This prevents accidental SQL
// injection if a non-constant expression is ever passed to diffHistoryAppendSQL.
var safePassExprRE = regexp.MustCompile(`^[a-zA-Z0-9_():@, +]+$`)

type SessionStore struct {
	db      DBTX
	streams *cache.SessionStreams
	logger  zerolog.Logger
}

func NewSessionStore(db DBTX) *SessionStore {
	return &SessionStore{db: db, logger: zerolog.Nop()}
}

// SetStreams injects the Redis stream helper used for live session status fan-out.
// lint:allow-no-orgid reason="process-wide dependency injection for Redis session status streaming"
func (s *SessionStore) SetStreams(streams *cache.SessionStreams) {
	s.streams = streams
}

// SetLogger injects the structured logger used for best-effort stream publishing.
// lint:allow-no-orgid reason="process-wide dependency injection for store logging"
func (s *SessionStore) SetLogger(logger zerolog.Logger) {
	s.logger = logger
}

// Begin starts a transaction on the underlying session store.
// lint:allow-no-orgid reason="transaction helper only; scoped methods still enforce org_id individually"
func (s *SessionStore) Begin(ctx context.Context) (pgx.Tx, error) {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return nil, fmt.Errorf("session store does not support transactions")
	}
	return txStarter.Begin(ctx)
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
	CursorTime         *time.Time
	CursorID           *uuid.UUID
	AdHocOnly          bool      // When true, only return runs where pm_plan_id IS NULL (not linked to a PM plan).
	RepositoryID       uuid.UUID // When non-zero, filter sessions by repository via issues table.
	TriggeredByUserID  uuid.UUID // When non-zero, filter sessions to those triggered by this user.
	TriggeredByUserIDs []uuid.UUID
	Search             string // When non-empty, filter sessions by title (case-insensitive prefix/substring match).
	IncludeArchived    bool   // When true, include archived sessions in the results.
	OnlyArchived       bool   // When true, return only archived sessions.
	CreatedAfter       *time.Time
	CreatedBefore      *time.Time
}

// SessionCountsFilters scopes CountsByOrg to a subset of sessions.
// Status, archived, and search are not accepted — the counts endpoint
// always returns totals for the all/active/archived buckets.
type SessionCountsFilters struct {
	RepositoryID       uuid.UUID
	TriggeredByUserID  uuid.UUID
	TriggeredByUserIDs []uuid.UUID
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

// sessionPrimaryIssueIDColumn derives primary_issue_id from the canonical
// session_issue_links join table. It is the only source of truth for the
// primary issue on a session — the legacy sessions.issue_id column was dropped
// in migration 000097.
//
// No ORDER BY is needed: the partial unique index
// idx_session_issue_links_primary enforces at most one row per session_id
// where role = 'primary', so the subquery is deterministic by construction.
const sessionPrimaryIssueIDColumn = `(SELECT sil.issue_id
		FROM session_issue_links sil
		WHERE sil.org_id = sessions.org_id AND sil.session_id = sessions.id AND sil.role = 'primary'
		LIMIT 1) AS primary_issue_id`

// hasUnpushedChangesColumn derives whether the session's latest persisted diff
// snapshot still contains content not represented on the open PR branch. A
// snapshot is considered pushable when it either captured a dirty worktree or
// its recorded HEAD differs from the PR head SHA.
const hasUnpushedChangesColumn = `EXISTS (
		SELECT 1
		FROM pull_requests pr
		JOIN session_diff_snapshots sds
		  ON sds.id = sessions.latest_diff_snapshot_id
		 AND sds.org_id = sessions.org_id
		WHERE pr.session_id = sessions.id
		  AND pr.org_id = sessions.org_id
		  AND pr.status = 'open'
		  AND (
		    sds.workspace_dirty
		    OR (sds.head_commit_sha IS NOT NULL AND sds.head_commit_sha IS DISTINCT FROM pr.head_sha)
		  )
	) AS has_unpushed_changes`

// sessionSelectColumns is used for single-session queries where we want all fields.
const sessionSelectColumns = `id,
	` + sessionPrimaryIssueIDColumn + `,
	org_id, origin, interaction_mode, validation_policy, agent_type, status, autonomy_level, token_mode,
	complexity_tier,
	container_id, worker_node_id, turn_holding_container, started_at, completed_at, token_usage,
	failure_explanation, failure_category, failure_next_steps, failure_retry_advised,
	parent_session_id, revision_context, error, result_summary, diff,
	pm_plan_id, title, pm_approach, pm_reasoning, project_task_id,
	model_override, reasoning_effort, triggered_by_user_id, agent_session_id, current_turn, last_activity_at,
	sandbox_state, workspace_generation, snapshot_key, pending_snapshot_key, pending_snapshot_set_at, runtime_soft_deadline_at, runtime_hard_deadline_at,
	runtime_last_progress_at, runtime_last_progress_type, runtime_last_progress_strength,
	runtime_extension_count, runtime_extension_seconds, runtime_stop_reason, runtime_graceful_stop_at,
	checkpointed_at, checkpoint_kind, checkpoint_capability, checkpoint_size_bytes, checkpoint_error,
	recovery_state, recovery_queued_at, recovery_started_at, recovery_attempt_count,
	target_branch, working_branch, base_commit_sha, repository_id, diff_stats, diff_history, input_manifest, archived_at, archived_by_user_id, automation_run_id, pr_creation_state, pr_creation_error, pr_push_state, pr_push_error, branch_creation_state, branch_creation_error, branch_url, diff_collected_at, latest_diff_snapshot_id, workspace_revision, workspace_revision_updated_at,
	` + hasUnpushedChangesColumn + `,
	linear_private, linear_state_sync_disabled, linear_identifier_hint, linear_prepare_state,
	deleted_at, capability_snapshot, git_identity_source, git_identity_user_id, created_at`

const (
	sessionDiffMaxChars        = 2 * 1024 * 1024
	sessionDiffHistoryMaxBytes = 512 * 1024
)

const sessionResumeRuntimeResetAssignments = `runtime_soft_deadline_at = NULL,
		    runtime_hard_deadline_at = NULL,
		    runtime_last_progress_at = NULL,
		    runtime_last_progress_type = '',
		    runtime_last_progress_strength = '',
		    runtime_extension_count = 0,
		    runtime_extension_seconds = 0,
		    runtime_stop_reason = '',
		    runtime_graceful_stop_at = NULL`

// sessionListColumns excludes raw diff payloads and large JSONB blobs
// (diff_history) from list queries to avoid returning multi-megabyte payloads
// when listing many sessions.
const sessionListColumns = `id,
	` + sessionPrimaryIssueIDColumn + `,
	org_id, origin, interaction_mode, validation_policy, agent_type, status, autonomy_level, token_mode,
	complexity_tier,
	container_id, worker_node_id, turn_holding_container, started_at, completed_at, token_usage,
	failure_explanation, failure_category, failure_next_steps, failure_retry_advised,
	parent_session_id, revision_context, error, result_summary, NULL::text AS diff,
	pm_plan_id, title, pm_approach, pm_reasoning, project_task_id,
	model_override, reasoning_effort, triggered_by_user_id, agent_session_id, current_turn, last_activity_at,
	sandbox_state, workspace_generation, snapshot_key, pending_snapshot_key, pending_snapshot_set_at, runtime_soft_deadline_at, runtime_hard_deadline_at,
	runtime_last_progress_at, runtime_last_progress_type, runtime_last_progress_strength,
	runtime_extension_count, runtime_extension_seconds, runtime_stop_reason, runtime_graceful_stop_at,
	checkpointed_at, checkpoint_kind, checkpoint_capability, checkpoint_size_bytes, checkpoint_error,
	recovery_state, recovery_queued_at, recovery_started_at, recovery_attempt_count,
	target_branch, working_branch, base_commit_sha, repository_id, diff_stats, NULL::jsonb AS diff_history, input_manifest, archived_at, archived_by_user_id, automation_run_id, pr_creation_state, pr_creation_error, pr_push_state, pr_push_error, branch_creation_state, branch_creation_error, branch_url, diff_collected_at, latest_diff_snapshot_id, workspace_revision, workspace_revision_updated_at,
	` + hasUnpushedChangesColumn + `,
	linear_private, linear_state_sync_disabled, linear_identifier_hint, linear_prepare_state,
	deleted_at, capability_snapshot, git_identity_source, git_identity_user_id, created_at`

// sessionAPIDetailColumns is used by the session-detail HTTP endpoint. It keeps
// the same metadata as a full session fetch, but leaves the large diff payloads
// for /sessions/{id}/diff so an accidentally huge diff cannot OOM the API while
// opening the session transcript.
const sessionAPIDetailColumns = `id,
	` + sessionPrimaryIssueIDColumn + `,
	org_id, origin, interaction_mode, validation_policy, agent_type, status, autonomy_level, token_mode,
	complexity_tier,
	container_id, worker_node_id, turn_holding_container, started_at, completed_at, token_usage,
	failure_explanation, failure_category, failure_next_steps, failure_retry_advised,
	parent_session_id, revision_context, error, result_summary, NULL::text AS diff,
	pm_plan_id, title, pm_approach, pm_reasoning, project_task_id,
	model_override, reasoning_effort, triggered_by_user_id, agent_session_id, current_turn, last_activity_at,
	sandbox_state, workspace_generation, snapshot_key, pending_snapshot_key, pending_snapshot_set_at, runtime_soft_deadline_at, runtime_hard_deadline_at,
	runtime_last_progress_at, runtime_last_progress_type, runtime_last_progress_strength,
	runtime_extension_count, runtime_extension_seconds, runtime_stop_reason, runtime_graceful_stop_at,
	checkpointed_at, checkpoint_kind, checkpoint_capability, checkpoint_size_bytes, checkpoint_error,
	recovery_state, recovery_queued_at, recovery_started_at, recovery_attempt_count,
	target_branch, working_branch, base_commit_sha, repository_id, diff_stats, NULL::jsonb AS diff_history, input_manifest, archived_at, archived_by_user_id, automation_run_id, pr_creation_state, pr_creation_error, pr_push_state, pr_push_error, branch_creation_state, branch_creation_error, branch_url, diff_collected_at, latest_diff_snapshot_id, workspace_revision, workspace_revision_updated_at,
	` + hasUnpushedChangesColumn + `,
	linear_private, linear_state_sync_disabled, linear_identifier_hint, linear_prepare_state,
	deleted_at, capability_snapshot, git_identity_source, git_identity_user_id, created_at`

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

type diffStatsPayload struct {
	Added        int `json:"added"`
	Removed      int `json:"removed"`
	FilesChanged int `json:"files_changed"`
}

type workspaceRevisionUpdate struct {
	Revision  int64
	UpdatedAt time.Time
}

func parseDiffStatsPayload(raw json.RawMessage) diffStatsPayload {
	if len(raw) == 0 {
		return diffStatsPayload{}
	}
	var payload diffStatsPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return diffStatsPayload{}
	}
	return payload
}

func hydrateSessionPolicy(session *models.Session) {
	if session == nil {
		return
	}
	if session.Origin == "" {
		switch {
		case session.TriggeredByUserID != nil:
			session.Origin = models.SessionOriginManual
		case session.ProjectTaskID != nil:
			session.Origin = models.SessionOriginProject
		case session.AutomationRunID != nil:
			session.Origin = models.SessionOriginAutomation
		case session.ParentSessionID != nil:
			session.Origin = models.SessionOriginRevision
		default:
			session.Origin = models.SessionOriginIssueTrigger
		}
	}
	if session.InteractionMode == "" {
		if session.Origin == models.SessionOriginManual {
			session.InteractionMode = models.SessionInteractionModeInteractive
		} else {
			session.InteractionMode = models.SessionInteractionModeSingleRun
		}
	}
	if session.ValidationPolicy == "" {
		if session.Origin == models.SessionOriginManual {
			session.ValidationPolicy = models.SessionValidationPolicyOnSessionEnd
		} else {
			session.ValidationPolicy = models.SessionValidationPolicyOnTurnComplete
		}
	}
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
	if len(filters.TriggeredByUserIDs) > 0 {
		query += ` AND triggered_by_user_id = ANY(@triggered_by_user_ids)`
		args["triggered_by_user_ids"] = filters.TriggeredByUserIDs
	} else if filters.TriggeredByUserID != uuid.Nil {
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
	if filters.CreatedAfter != nil {
		query += ` AND created_at >= @created_after`
		args["created_after"] = *filters.CreatedAfter
	}
	if filters.CreatedBefore != nil {
		query += ` AND created_at <= @created_before`
		args["created_before"] = *filters.CreatedBefore
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
	sessions, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		hydrateSessionPolicy(&sessions[i])
	}
	return sessions, nil
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
	if len(filters.TriggeredByUserIDs) > 0 {
		scope += " AND triggered_by_user_id = ANY(@triggered_by_user_ids)"
		args["triggered_by_user_ids"] = filters.TriggeredByUserIDs
	} else if filters.TriggeredByUserID != uuid.Nil {
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
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return models.Session{}, err
	}
	hydrateSessionPolicy(&session)
	return session, nil
}

// SessionMessageAppendState is the small slice of a session needed to append
// an inbound message without claiming a new turn. It intentionally avoids the
// full sessionSelectColumns payload because running-session fast paths only
// need status and turn numbering.
type SessionMessageAppendState struct {
	ID          uuid.UUID            `db:"id"`
	OrgID       uuid.UUID            `db:"org_id"`
	Status      models.SessionStatus `db:"status"`
	CurrentTurn int                  `db:"current_turn"`
}

// GetMessageAppendState returns the current status and turn number for a
// session, scoped to org. Used by follow-up-message paths after a claim loses
// a race to an already-running turn: the caller can append the message to the
// running session without enqueueing a duplicate continuation.
func (s *SessionStore) GetMessageAppendState(ctx context.Context, orgID, sessionID uuid.UUID) (SessionMessageAppendState, error) {
	return scanMessageAppendStateFromRow(s.db.QueryRow(ctx, sessionMessageAppendStateQuery(false), pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	}))
}

// LockMessageAppendState is the SELECT ... FOR UPDATE variant of
// GetMessageAppendState. The caller MUST already hold a transaction (passed
// as tx) — the row lock is released on tx.Commit / tx.Rollback. Used by the
// Linear agent prompted handler to read-and-pin status atomically before
// inserting a queued user message under a still-running turn, so the
// finisher's drain doesn't race the insert.
//
// Keeping the SQL co-located with the rest of the session queries (rather
// than inlined in the worker package) means a future rename of `deleted_at`
// or `status` rebuilds in one place.
func (s *SessionStore) LockMessageAppendState(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (SessionMessageAppendState, error) {
	if tx == nil {
		return SessionMessageAppendState{}, fmt.Errorf("LockMessageAppendState requires a non-nil tx")
	}
	return scanMessageAppendStateFromRow(tx.QueryRow(ctx, sessionMessageAppendStateQuery(true), pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	}))
}

func sessionMessageAppendStateQuery(forUpdate bool) string {
	const base = `
		SELECT id, org_id, status, current_turn
		FROM sessions
		WHERE id = @id
		  AND org_id = @org_id
		  AND deleted_at IS NULL`
	if forUpdate {
		return base + "\n\t\tFOR UPDATE"
	}
	return base
}

func scanMessageAppendStateFromRow(row pgx.Row) (SessionMessageAppendState, error) {
	var state SessionMessageAppendState
	if err := row.Scan(&state.ID, &state.OrgID, &state.Status, &state.CurrentTurn); err != nil {
		return SessionMessageAppendState{}, err
	}
	return state, nil
}

func (s *SessionStore) GetAPIDetailByID(ctx context.Context, orgID, runID uuid.UUID) (models.Session, error) {
	query := `
		SELECT ` + sessionAPIDetailColumns + `
		FROM sessions
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     runID,
		"org_id": orgID,
	})
	if err != nil {
		return models.Session{}, fmt.Errorf("query session API detail: %w", err)
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return models.Session{}, err
	}
	hydrateSessionPolicy(&session)
	return session, nil
}

func (s *SessionStore) GetDiffByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.SessionDiff, error) {
	query := `
		SELECT
			CASE
				WHEN diff IS NULL THEN NULL
				WHEN length(diff) > @diff_max_chars THEN left(diff, @diff_max_chars)
				ELSE diff
			END AS diff,
			diff_stats,
			CASE
				WHEN diff_history IS NULL THEN NULL
				WHEN pg_column_size(diff_history) > @diff_history_max_bytes THEN '[]'::jsonb
				ELSE diff_history
			END AS diff_history,
			COALESCE(length(diff), 0) > @diff_max_chars AS diff_truncated,
			COALESCE(pg_column_size(diff_history), 0) > @diff_history_max_bytes AS diff_history_truncated,
			COALESCE(length(diff), 0)::bigint AS diff_chars,
			COALESCE(pg_column_size(diff_history), 0)::bigint AS diff_history_bytes,
			@diff_max_chars::bigint AS diff_max_chars,
			@diff_history_max_bytes::bigint AS diff_history_max_bytes
		FROM sessions
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`

	var payload models.SessionDiff
	payload.SessionID = sessionID
	if err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":                     sessionID,
		"org_id":                 orgID,
		"diff_max_chars":         sessionDiffMaxChars,
		"diff_history_max_bytes": sessionDiffHistoryMaxBytes,
	}).Scan(
		&payload.Diff,
		&payload.DiffStats,
		&payload.DiffHistory,
		&payload.DiffTruncated,
		&payload.DiffHistoryTruncated,
		&payload.DiffChars,
		&payload.DiffHistoryBytes,
		&payload.DiffMaxChars,
		&payload.DiffHistoryMaxBytes,
	); err != nil {
		return models.SessionDiff{}, err
	}
	return payload, nil
}

func (s *SessionStore) GetLatestDiffSnapshot(ctx context.Context, orgID, sessionID uuid.UUID) (models.SessionDiffSnapshot, error) {
	query := `
		SELECT id, session_id, org_id, turn_number, sequence_number, source,
		       base_commit_sha, head_commit_sha, workspace_dirty, working_branch,
		       target_branch, diff, files_changed, lines_added, lines_removed, captured_at,
		       review_artifact_key, review_artifact_version,
		       review_artifact_compressed_bytes, review_artifact_uncompressed_bytes,
		       review_artifact_file_count, review_artifact_skipped_count,
		       review_artifact_truncated
		FROM session_diff_snapshots
		WHERE org_id = @org_id AND session_id = @session_id
		ORDER BY captured_at DESC, sequence_number DESC
		LIMIT 1`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return models.SessionDiffSnapshot{}, fmt.Errorf("query latest session diff snapshot: %w", err)
	}
	snapshot, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionDiffSnapshot])
	if err != nil {
		return models.SessionDiffSnapshot{}, err
	}
	return snapshot, nil
}

func (s *SessionStore) GetLatestReviewArtifactRef(ctx context.Context, orgID, sessionID uuid.UUID) (models.SessionReviewArtifactRef, error) {
	query := `
		SELECT review_artifact_key, review_artifact_version
		FROM session_diff_snapshots
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND review_artifact_key IS NOT NULL
		ORDER BY captured_at DESC, sequence_number DESC
		LIMIT 1`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return models.SessionReviewArtifactRef{}, fmt.Errorf("query latest review artifact ref: %w", err)
	}
	ref, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionReviewArtifactRef])
	if err != nil {
		return models.SessionReviewArtifactRef{}, err
	}
	return ref, nil
}

func (s *SessionStore) Create(ctx context.Context, run *models.Session) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin session create transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := createSessionRows(ctx, tx, run); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit session create transaction: %w", err)
	}
	return nil
}

func (s *SessionStore) CreateInTx(ctx context.Context, tx pgx.Tx, run *models.Session) error {
	return createSessionRows(ctx, tx, run)
}

// CreateForIncident creates a session for an incident-derived issue while
// guarding against duplicate concurrent starts. It holds a per-issue advisory
// lock for the duration of the transaction, re-checks (inside the lock) for a
// session that is either still active or was created within the cooldown
// window, and only inserts when none is found. This makes the PagerDuty session
// starter's previously racy read-then-write dedup atomic: concurrent webhook
// deliveries for the same incident can no longer each spawn a session, and a
// flapping incident cannot restart a session within the cooldown. When a
// blocking session already exists it is returned with created=false.
func (s *SessionStore) CreateForIncident(ctx context.Context, orgID, issueID uuid.UUID, cooldown time.Duration, run *models.Session) (created bool, existing models.Session, err error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return false, models.Session{}, fmt.Errorf("begin incident session transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lockKey := "pagerduty_incident_session:" + orgID.String() + ":" + issueID.String()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(@lock_key, 0))`, pgx.NamedArgs{"lock_key": lockKey}); err != nil {
		return false, models.Session{}, fmt.Errorf("acquire incident session lock: %w", err)
	}

	activeStatuses := make([]string, len(models.ActiveStatuses))
	for i, st := range models.ActiveStatuses {
		activeStatuses[i] = string(st)
	}
	query := `
		SELECT ` + sessionListColumns + `
		FROM sessions
		WHERE org_id = @org_id AND deleted_at IS NULL AND EXISTS (
			SELECT 1 FROM session_issue_links sil
			WHERE sil.org_id = sessions.org_id AND sil.session_id = sessions.id AND sil.issue_id = @issue_id
		) AND (status = ANY(@active_statuses) OR created_at >= @cooldown_cutoff)
		ORDER BY created_at DESC
		LIMIT 1`
	rows, err := tx.Query(ctx, query, pgx.NamedArgs{
		"org_id":          orgID,
		"issue_id":        issueID,
		"active_statuses": activeStatuses,
		"cooldown_cutoff": time.Now().Add(-cooldown),
	})
	if err != nil {
		return false, models.Session{}, fmt.Errorf("check existing incident sessions: %w", err)
	}
	found, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return false, models.Session{}, fmt.Errorf("scan existing incident sessions: %w", err)
	}
	if len(found) > 0 {
		hydrateSessionPolicy(&found[0])
		return false, found[0], nil
	}

	if err := createSessionRows(ctx, tx, run); err != nil {
		return false, models.Session{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, models.Session{}, fmt.Errorf("commit incident session transaction: %w", err)
	}
	return true, models.Session{}, nil
}

func createSessionRows(ctx context.Context, q DBTX, run *models.Session) error {
	if run.Origin == "" {
		run.Origin = models.SessionOriginIssueTrigger
	}
	if run.InteractionMode == "" {
		run.InteractionMode = models.SessionInteractionModeSingleRun
	}
	if run.ValidationPolicy == "" {
		run.ValidationPolicy = models.SessionValidationPolicyOnTurnComplete
	}
	if run.LinearPrepareState == "" {
		run.LinearPrepareState = models.LinearPrepareStateNone
	}
	if run.CapabilitySnapshot == nil {
		run.CapabilitySnapshot = []models.AgentCapabilitySnapshotItem{}
	}
	query := `
		INSERT INTO sessions (
			org_id, agent_type, status, autonomy_level, token_mode, complexity_tier,
			parent_session_id, revision_context, pm_plan_id, title, pm_approach, pm_reasoning, project_task_id,
			model_override, reasoning_effort, triggered_by_user_id, target_branch, base_commit_sha, repository_id, input_manifest, automation_run_id,
			origin, interaction_mode, validation_policy,
			linear_private, linear_state_sync_disabled, linear_identifier_hint, linear_prepare_state, capability_snapshot
		)
		VALUES (
			@org_id, @agent_type, @status, @autonomy_level, @token_mode, @complexity_tier,
			@parent_session_id, @revision_context, @pm_plan_id, @title, @pm_approach, @pm_reasoning, @project_task_id,
			@model_override, @reasoning_effort, @triggered_by_user_id, @target_branch, @base_commit_sha, @repository_id, @input_manifest, @automation_run_id,
			@origin, @interaction_mode, @validation_policy,
			@linear_private, @linear_state_sync_disabled, @linear_identifier_hint, @linear_prepare_state, @capability_snapshot
		)
		RETURNING id, created_at, last_activity_at`

	args := pgx.NamedArgs{
		"org_id":                     run.OrgID,
		"agent_type":                 run.AgentType,
		"status":                     run.Status,
		"autonomy_level":             run.AutonomyLevel,
		"token_mode":                 run.TokenMode,
		"complexity_tier":            run.ComplexityTier,
		"parent_session_id":          run.ParentSessionID,
		"revision_context":           run.RevisionContext,
		"pm_plan_id":                 run.PMPlanID,
		"title":                      run.Title,
		"pm_approach":                run.PMApproach,
		"pm_reasoning":               run.PMReasoning,
		"project_task_id":            run.ProjectTaskID,
		"model_override":             run.ModelOverride,
		"reasoning_effort":           run.ReasoningEffort,
		"triggered_by_user_id":       run.TriggeredByUserID,
		"target_branch":              run.TargetBranch,
		"base_commit_sha":            run.BaseCommitSHA,
		"repository_id":              run.RepositoryID,
		"input_manifest":             run.InputManifest,
		"automation_run_id":          run.AutomationRunID,
		"origin":                     run.Origin,
		"interaction_mode":           run.InteractionMode,
		"validation_policy":          run.ValidationPolicy,
		"linear_private":             run.LinearPrivate,
		"linear_state_sync_disabled": run.LinearStateSyncDisabled,
		"linear_identifier_hint":     run.LinearIdentifierHint,
		"linear_prepare_state":       run.LinearPrepareState,
		"capability_snapshot":        run.CapabilitySnapshot,
	}

	row := q.QueryRow(ctx, query, args)
	if err := row.Scan(&run.ID, &run.CreatedAt, &run.LastActivityAt); err != nil {
		return err
	}

	// Seed a primary thread row so the multi-tab UI (AgentTabStrip) has
	// something to render and the worker thread-attribution path has a
	// destination from turn 1. Done in the same transaction so the invariant
	// "every session row implies at least one thread row" cannot be violated
	// by a partial failure between session insert and thread insert.
	var primaryThreadID uuid.UUID
	if err := q.QueryRow(ctx, `
		INSERT INTO session_threads (
			session_id, org_id, agent_type, model_override, label, status
		)
		VALUES (@session_id, @org_id, @agent_type, @model_override, @label, @status)
		RETURNING id
	`, pgx.NamedArgs{
		"session_id":     run.ID,
		"org_id":         run.OrgID,
		"agent_type":     run.AgentType,
		"model_override": run.ModelOverride,
		"label":          "Main",
		"status":         models.ThreadStatusIdle,
	}).Scan(&primaryThreadID); err != nil {
		return fmt.Errorf("insert primary session thread: %w", err)
	}
	run.PrimaryThreadID = &primaryThreadID

	if run.PrimaryIssueID != nil {
		if _, err := q.Exec(ctx, `
			INSERT INTO session_issue_links (org_id, session_id, issue_id, role, position, added_by_user_id)
			VALUES (@org_id, @session_id, @issue_id, 'primary', 0, @added_by_user_id)
			ON CONFLICT (session_id, issue_id) DO NOTHING
		`, pgx.NamedArgs{
			"org_id":           run.OrgID,
			"session_id":       run.ID,
			"issue_id":         *run.PrimaryIssueID,
			"added_by_user_id": run.TriggeredByUserID,
		}); err != nil {
			return fmt.Errorf("insert session issue link: %w", err)
		}
	}
	return nil
}

// SetLinearPrepareState transitions the session's pre-start preparation
// state. Used by the Linear linker to gate turn 1 against primary issue
// resolution: the run_agent worker reads this and refuses to start until it
// is "ready" or "none".
//
// Intentionally does *not* bump last_activity_at — the prepare-state
// transition is internal infra, not user-visible activity. Bumping it here
// would surface preparing sessions in the MRU sort while they are blocked
// on a worker, confusing the sidebar.
func (s *SessionStore) SetLinearPrepareState(ctx context.Context, orgID, sessionID uuid.UUID, state models.LinearPrepareState) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE sessions
		SET linear_prepare_state = @state
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`,
		pgx.NamedArgs{
			"id":     sessionID,
			"org_id": orgID,
			"state":  state,
		})
	if err != nil {
		return fmt.Errorf("update linear prepare state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session not found")
	}
	return nil
}

// SetLinearPrepareStateIfNotReady writes state unless the row is already in
// "ready" - the terminal-positive state. Used by the prepare-worker failure
// paths so two distinct-hash workers racing on the same session can't have
// one mark "failed" on top of the other's "ready" success. "ready" is
// sticky once observed; "failed" -> "ready" is still allowed because a later
// successful prepare should win over an earlier failure.
//
// Returns nil with zero rows affected when the row was already "ready" - the
// no-op is intentional and not an error.
func (s *SessionStore) SetLinearPrepareStateIfNotReady(ctx context.Context, orgID, sessionID uuid.UUID, state models.LinearPrepareState) error {
	_, err := s.db.Exec(ctx, `
		UPDATE sessions
		SET linear_prepare_state = @state
		WHERE id = @id
		  AND org_id = @org_id
		  AND deleted_at IS NULL
		  AND linear_prepare_state <> @ready`,
		pgx.NamedArgs{
			"id":     sessionID,
			"org_id": orgID,
			"state":  state,
			"ready":  models.LinearPrepareStateReady,
		})
	if err != nil {
		return fmt.Errorf("update linear prepare state: %w", err)
	}
	return nil
}

// SetLinearIdentifierHint records the primary Linear identifier for a
// session. The agent's branch-naming logic reads this so the working branch
// includes the identifier — Linear's GitHub integration matches branch
// prefixes independently of the PR title.
func (s *SessionStore) SetLinearIdentifierHint(ctx context.Context, orgID, sessionID uuid.UUID, identifier string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE sessions
		SET linear_identifier_hint = NULLIF(@identifier, '')
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`,
		pgx.NamedArgs{
			"id":         sessionID,
			"org_id":     orgID,
			"identifier": identifier,
		})
	if err != nil {
		return fmt.Errorf("update linear identifier hint: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session not found")
	}
	return nil
}

func (s *SessionStore) BeginRuntime(ctx context.Context, orgID, sessionID uuid.UUID, capability models.CheckpointCapability, softDeadline, hardDeadline, observedAt time.Time) error {
	query := `
		UPDATE sessions
		SET status = @status,
		    started_at = @runtime_started_at,
		    completed_at = NULL,
		    error = NULL,
		    failure_explanation = NULL,
		    failure_category = NULL,
		    failure_next_steps = NULL,
		    failure_retry_advised = false,
		    runtime_soft_deadline_at = @runtime_soft_deadline_at,
		    runtime_hard_deadline_at = @runtime_hard_deadline_at,
		    runtime_last_progress_at = @runtime_last_progress_at,
		    runtime_last_progress_type = @runtime_last_progress_type,
		    runtime_last_progress_strength = @runtime_last_progress_strength,
		    runtime_extension_count = 0,
		    runtime_extension_seconds = 0,
		    runtime_stop_reason = '',
		    runtime_graceful_stop_at = NULL,
		    checkpoint_capability = @checkpoint_capability,
		    checkpoint_error = NULL,
		    recovery_state = CASE
		        WHEN recovery_state = 'recovering' THEN recovery_state
		        ELSE ''
		    END,
		    recovery_queued_at = NULL,
		    recovery_started_at = CASE
		        WHEN recovery_state = 'recovering' THEN recovery_started_at
		        ELSE NULL
		    END,
		    last_activity_at = now()
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		RETURNING ` + sessionSelectColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":                             sessionID,
		"org_id":                         orgID,
		"status":                         string(models.SessionStatusRunning),
		"runtime_started_at":             observedAt.UTC(),
		"runtime_soft_deadline_at":       softDeadline.UTC(),
		"runtime_hard_deadline_at":       hardDeadline.UTC(),
		"runtime_last_progress_at":       observedAt.UTC(),
		"runtime_last_progress_type":     string(models.RuntimeProgressTypeAssistantOutput),
		"runtime_last_progress_strength": string(models.RuntimeProgressStrengthWeak),
		"checkpoint_capability":          string(capability),
	})
	if err != nil {
		return err
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return err
	}
	hydrateSessionPolicy(&session)
	s.publishStatus(ctx, &session)
	return nil
}

func (s *SessionStore) RequestCancel(ctx context.Context, orgID, sessionID uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `
		INSERT INTO session_cancel_requests (org_id, session_id, requested_at, delivered_at)
		VALUES (@org_id, @session_id, now(), NULL)
		ON CONFLICT (org_id, session_id)
		DO UPDATE SET requested_at = now(), delivered_at = NULL`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID},
	)
	if err != nil {
		return fmt.Errorf("request session cancel: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session cancel request not recorded")
	}
	return nil
}

func (s *SessionStore) ConsumeCancelRequest(ctx context.Context, orgID, sessionID uuid.UUID) (bool, error) {
	tag, err := s.db.Exec(ctx, `
		UPDATE session_cancel_requests
		SET delivered_at = now()
		WHERE org_id = @org_id
		  AND session_id = @session_id
		  AND delivered_at IS NULL`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID},
	)
	if err != nil {
		return false, fmt.Errorf("consume session cancel request: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *SessionStore) RecordRuntimeProgress(ctx context.Context, orgID, sessionID uuid.UUID, progressType models.RuntimeProgressType, strength models.RuntimeProgressStrength, observedAt time.Time) error {
	query := `
		UPDATE sessions
		SET runtime_last_progress_at = @runtime_last_progress_at,
		    runtime_last_progress_type = @runtime_last_progress_type,
		    runtime_last_progress_strength = @runtime_last_progress_strength
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":                             sessionID,
		"org_id":                         orgID,
		"runtime_last_progress_at":       observedAt.UTC(),
		"runtime_last_progress_type":     string(progressType),
		"runtime_last_progress_strength": string(strength),
	})
	return err
}

func (s *SessionStore) MarkRuntimeStopRequested(ctx context.Context, orgID, sessionID uuid.UUID, reason models.RuntimeStopReason, stopAfter time.Time) error {
	query := `
		UPDATE sessions
		SET runtime_stop_reason = CASE
		        WHEN runtime_stop_reason = '' OR @runtime_stop_reason = 'user_cancel' THEN @runtime_stop_reason
		        ELSE runtime_stop_reason
		    END,
		    runtime_graceful_stop_at = CASE
		        WHEN runtime_graceful_stop_at IS NULL OR @runtime_stop_reason = 'user_cancel' THEN @runtime_stop_after
		        ELSE runtime_graceful_stop_at
		    END
		WHERE id = @id
		  AND org_id = @org_id
		  AND deleted_at IS NULL
		  AND status = 'running'`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":                  sessionID,
		"org_id":              orgID,
		"runtime_stop_reason": string(reason),
		"runtime_stop_after":  stopAfter.UTC(),
	})
	if err != nil {
		return fmt.Errorf("mark runtime stop requested: %w", err)
	}
	return nil
}

// ListRuntimeControlStalledSessions returns running sessions whose runtime
// controller should already have stopped or requested stop handling. This is a
// narrower watchdog than ListStaleRunningSessions: it only targets rows whose
// own runtime budget has already expired or whose persisted stop-after deadline
// has passed.
// lint:allow-no-orgid reason="cross-org reaper scan for stalled runtime control"
func (s *SessionStore) ListRuntimeControlStalledSessions(ctx context.Context, deadlineBefore, stopAfterBefore time.Time) ([]models.Session, error) {
	query := `
		SELECT ` + sessionListColumns + `
		FROM sessions
		WHERE status = 'running'
		  AND deleted_at IS NULL
		  AND (
		    (
		      runtime_stop_reason <> ''
		      AND runtime_graceful_stop_at IS NOT NULL
		      AND (started_at IS NULL OR runtime_graceful_stop_at >= started_at)
		      AND runtime_graceful_stop_at < @stop_after_before
		    )
		    OR (
		      runtime_stop_reason = ''
		      AND runtime_soft_deadline_at IS NOT NULL
		      AND (started_at IS NULL OR runtime_soft_deadline_at >= started_at)
		      AND runtime_soft_deadline_at < @deadline_before
		    )
		  )
		ORDER BY COALESCE(runtime_graceful_stop_at, runtime_soft_deadline_at) ASC
		LIMIT 100`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"deadline_before":   deadlineBefore,
		"stop_after_before": stopAfterBefore,
	})
	if err != nil {
		return nil, fmt.Errorf("query runtime-control stalled sessions: %w", err)
	}
	sessions, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return nil, fmt.Errorf("collect runtime-control stalled sessions: %w", err)
	}
	return sessions, nil
}

func (s *SessionStore) GrantRuntimeExtension(ctx context.Context, orgID, sessionID uuid.UUID, lockToken uuid.UUID, expectedSoftDeadline, newSoftDeadline, hardDeadline time.Time, extensionSeconds int) (bool, error) {
	args := pgx.NamedArgs{
		"id":                     sessionID,
		"org_id":                 orgID,
		"expected_soft_deadline": expectedSoftDeadline.UTC(),
		"new_soft_deadline":      newSoftDeadline.UTC(),
		"runtime_hard_deadline":  hardDeadline.UTC(),
		"extension_seconds":      extensionSeconds,
	}

	query := `
		UPDATE sessions s
		SET runtime_soft_deadline_at = @new_soft_deadline,
		    runtime_hard_deadline_at = @runtime_hard_deadline,
		    runtime_extension_count = runtime_extension_count + 1,
		    runtime_extension_seconds = runtime_extension_seconds + @extension_seconds
		WHERE s.id = @id
		  AND s.org_id = @org_id
		  AND s.deleted_at IS NULL
		  AND s.status = 'running'
		  AND s.runtime_soft_deadline_at = @expected_soft_deadline`
	if lockToken != uuid.Nil {
		query += `
		  AND EXISTS (
			SELECT 1
			FROM jobs j
			WHERE j.status = 'running'
			  AND j.org_id = @org_id
			  AND j.lock_token = @lock_token
			  AND NULLIF(j.payload->>'session_id', '')::uuid = s.id
		  )`
		args["lock_token"] = lockToken
	}

	tag, err := s.db.Exec(ctx, query, args)
	if err != nil {
		return false, fmt.Errorf("grant runtime extension: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *SessionStore) BumpWorkspaceRevision(ctx context.Context, orgID, sessionID uuid.UUID, reason string) (int64, time.Time, error) {
	updatedAt := time.Now().UTC()
	rows, err := s.db.Query(ctx, `
		UPDATE sessions
		SET workspace_revision = workspace_revision + 1,
		    workspace_revision_updated_at = @updated_at
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		RETURNING workspace_revision, workspace_revision_updated_at`,
		pgx.NamedArgs{
			"id":         sessionID,
			"org_id":     orgID,
			"updated_at": updatedAt,
		},
	)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("bump workspace revision (%s): %w", reason, err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[struct {
		Revision  int64
		UpdatedAt time.Time
	}])
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("collect workspace revision (%s): %w", reason, err)
	}
	s.publishWorkspaceGenerationChanged(ctx, orgID, sessionID, row.Revision, row.UpdatedAt, reason)
	return row.Revision, row.UpdatedAt, nil
}

func (s *SessionStore) PublishCheckpoint(ctx context.Context, orgID, sessionID uuid.UUID, lockToken uuid.UUID, agentSessionID, snapshotKey string, kind models.CheckpointKind, capability models.CheckpointCapability, sizeBytes int64, checkpointedAt time.Time, checkpointErr *string, stopReason models.RuntimeStopReason) (bool, error) {
	args := pgx.NamedArgs{
		"id":                    sessionID,
		"org_id":                orgID,
		"agent_session_id":      agentSessionID,
		"snapshot_key":          snapshotKey,
		"checkpoint_kind":       string(kind),
		"checkpoint_capability": string(capability),
		"checkpoint_size_bytes": sizeBytes,
		"checkpointed_at":       checkpointedAt.UTC(),
		"checkpoint_error":      checkpointErr,
		"runtime_stop_reason":   string(stopReason),
	}

	query := `
		UPDATE sessions s
		SET agent_session_id = CASE
		        WHEN @agent_session_id = '' THEN agent_session_id
		        ELSE @agent_session_id
		    END,
		    snapshot_key = CASE
		        WHEN @snapshot_key = '' THEN snapshot_key
		        ELSE @snapshot_key
		    END,
		    sandbox_state = CASE
		        WHEN @snapshot_key = '' THEN sandbox_state
		        ELSE 'snapshotted'
		    END,
		    checkpoint_kind = @checkpoint_kind,
		    checkpoint_capability = @checkpoint_capability,
		    checkpoint_size_bytes = @checkpoint_size_bytes,
		    checkpointed_at = @checkpointed_at,
		    checkpoint_error = @checkpoint_error,
		    runtime_stop_reason = @runtime_stop_reason,
		    runtime_graceful_stop_at = CASE
		        WHEN @runtime_stop_reason = '' THEN runtime_graceful_stop_at
		        ELSE @checkpointed_at
		    END,
		    workspace_revision = CASE
		        WHEN @snapshot_key = '' THEN workspace_revision
		        ELSE workspace_revision + 1
		    END,
		    workspace_revision_updated_at = CASE
		        WHEN @snapshot_key = '' THEN workspace_revision_updated_at
		        ELSE @checkpointed_at
		    END
		WHERE s.id = @id
		  AND s.org_id = @org_id
		  AND s.deleted_at IS NULL`
	if lockToken != uuid.Nil {
		query += `
		  AND EXISTS (
			SELECT 1
			FROM jobs j
			WHERE j.status = 'running'
			  AND j.org_id = @org_id
			  AND j.lock_token = @lock_token
			  AND NULLIF(j.payload->>'session_id', '')::uuid = s.id
		  )`
		args["lock_token"] = lockToken
	}

	query += `
		RETURNING workspace_revision, workspace_revision_updated_at`

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return false, fmt.Errorf("publish checkpoint: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[struct {
		Revision  int64
		UpdatedAt time.Time
	}])
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("collect published checkpoint: %w", err)
	}
	if snapshotKey != "" {
		s.publishWorkspaceGenerationChanged(ctx, orgID, sessionID, row.Revision, row.UpdatedAt, "checkpoint")
	}
	return true, nil
}

func (s *SessionStore) UpdateRecoveryState(ctx context.Context, orgID, sessionID uuid.UUID, state models.RecoveryState, queuedAt, startedAt *time.Time, incrementAttempt bool) error {
	query := `
		UPDATE sessions
		SET recovery_state = @recovery_state,
		    recovery_queued_at = @recovery_queued_at,
		    recovery_started_at = @recovery_started_at,
		    recovery_attempt_count = CASE
		        WHEN @increment_attempt THEN recovery_attempt_count + 1
		        ELSE recovery_attempt_count
		    END
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`

	args := pgx.NamedArgs{
		"id":                sessionID,
		"org_id":            orgID,
		"recovery_state":    string(state),
		"increment_attempt": incrementAttempt,
	}
	if queuedAt != nil {
		args["recovery_queued_at"] = queuedAt.UTC()
	} else {
		args["recovery_queued_at"] = nil
	}
	if startedAt != nil {
		args["recovery_started_at"] = startedAt.UTC()
	} else {
		args["recovery_started_at"] = nil
	}

	_, err := s.db.Exec(ctx, query, args)
	return err
}

func (s *SessionStore) UpdateStatus(ctx context.Context, orgID, runID uuid.UUID, status models.SessionStatus) error {
	query := `UPDATE sessions SET status = @status, last_activity_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	if status == models.SessionStatusRunning {
		// Clear completed_at so a resumed session doesn't display as "completed"
		// while actively running. Duration is computed from started_at, so that is
		// also refreshed to reflect the current run.
		query = `UPDATE sessions SET status = @status, started_at = now(), completed_at = NULL, error = NULL, failure_explanation = NULL, failure_category = NULL, failure_next_steps = NULL, failure_retry_advised = false, runtime_soft_deadline_at = NULL, runtime_hard_deadline_at = NULL, runtime_last_progress_at = NULL, runtime_last_progress_type = '', runtime_last_progress_strength = '', runtime_stop_reason = '', runtime_graceful_stop_at = NULL, last_activity_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	} else if status == models.SessionStatusCompleted || status == models.SessionStatusPRCreated || status == models.SessionStatusSkipped {
		query = `UPDATE sessions SET status = @status, completed_at = now(), error = NULL, failure_explanation = NULL, failure_category = NULL, failure_next_steps = NULL, failure_retry_advised = false, last_activity_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	} else if status == models.SessionStatusFailed || status == models.SessionStatusCancelled {
		query = `UPDATE sessions SET status = @status, completed_at = now(), last_activity_at = now() WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	}
	rows, err := s.db.Query(ctx, query+` RETURNING `+sessionSelectColumns, pgx.NamedArgs{
		"id":     runID,
		"org_id": orgID,
		"status": string(status),
	})
	if err != nil {
		return err
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return err
	}
	hydrateSessionPolicy(&session)
	s.publishStatus(ctx, &session)
	return nil
}

func (s *SessionStore) SetRepositoryContext(ctx context.Context, orgID, sessionID, repositoryID uuid.UUID, targetBranch *string) (models.Session, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE sessions
		SET repository_id = @repository_id,
			target_branch = NULLIF(@target_branch::text, ''),
			last_activity_at = now()
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		RETURNING `+sessionSelectColumns,
		pgx.NamedArgs{
			"id":            sessionID,
			"org_id":        orgID,
			"repository_id": repositoryID,
			"target_branch": targetBranch,
		})
	if err != nil {
		return models.Session{}, fmt.Errorf("set session repository context: %w", err)
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return models.Session{}, fmt.Errorf("collect session repository context: %w", err)
	}
	hydrateSessionPolicy(&session)
	s.publishStatus(ctx, &session)
	return session, nil
}

func (s *SessionStore) UpdateInputManifest(ctx context.Context, orgID, sessionID uuid.UUID, inputManifest json.RawMessage) (models.Session, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE sessions
		SET input_manifest = @input_manifest,
			last_activity_at = now()
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		RETURNING `+sessionSelectColumns,
		pgx.NamedArgs{
			"id":             sessionID,
			"org_id":         orgID,
			"input_manifest": inputManifest,
		})
	if err != nil {
		return models.Session{}, fmt.Errorf("update session input manifest: %w", err)
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return models.Session{}, fmt.Errorf("collect session input manifest update: %w", err)
	}
	hydrateSessionPolicy(&session)
	s.publishStatus(ctx, &session)
	return session, nil
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
func (s *SessionStore) UpdateResult(ctx context.Context, orgID, runID uuid.UUID, status models.SessionStatus, result *models.SessionResult) error {
	diffStats := computeDiffStatsForResult(result)
	if !shouldPersistDiffSnapshot(result) {
		return s.updateResultRow(ctx, s.db, orgID, runID, status, result, diffStats)
	}

	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.updateResultRow(ctx, tx, orgID, runID, status, result, diffStats); err != nil {
		return err
	}
	updated, err := s.writeDiffSnapshot(ctx, tx, orgID, runID, 0, result, diffStats)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.publishWorkspaceGenerationChanged(ctx, orgID, runID, updated.Revision, updated.UpdatedAt, "diff_snapshot")
	return nil
}

func (s *SessionStore) updateResultRow(ctx context.Context, db DBTX, orgID, runID uuid.UUID, status models.SessionStatus, result *models.SessionResult, diffStats json.RawMessage) error {

	// COALESCE on diff / diff_stats / diff_collected_at preserves the
	// previously persisted authoritative diff when the current turn did not
	// produce one (collection skipped or failed — sessiondiff.Collect returned
	// ErrNoBaseCommitSHA, which the adapter logs and leaves result.Diff empty,
	// which strPtr converts to nil here). Without this guard, an empty/NULL
	// diff would overwrite the prior diff and blank out the Changes tab.
	// diff_history's append SQL already no-ops when @diff IS NULL.
	query := `
		UPDATE sessions
		SET status = @status,
		    last_activity_at = now(),
		    completed_at = CASE
		        WHEN @status IN ('completed', 'failed', 'cancelled', 'needs_human_guidance', 'pr_created', 'skipped')
		            THEN now()
		        ELSE completed_at
		    END,
		    token_usage = @token_usage,
		    model_used = COALESCE(@model_used, model_used),
		    result_summary = @result_summary,
		    diff = COALESCE(@diff, diff),
		    error = @error,
		    failure_explanation = NULL,
		    failure_category = NULL,
		    failure_next_steps = NULL,
		    failure_retry_advised = false,
		    base_commit_sha = COALESCE(@base_commit_sha, base_commit_sha),
		    diff_collected_at = COALESCE(@diff_collected_at, diff_collected_at),
		    diff_stats = COALESCE(@diff_stats, diff_stats),
		    diff_history = ` + diffHistoryAppendSQL("COALESCE(current_turn, 0) + 1") + `
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`

	rows, err := db.Query(ctx, query+` RETURNING `+sessionSelectColumns, pgx.NamedArgs{
		"id":                runID,
		"org_id":            orgID,
		"status":            string(status),
		"token_usage":       result.TokenUsage,
		"model_used":        result.ModelUsed,
		"result_summary":    result.ResultSummary,
		"diff":              result.Diff,
		"error":             result.Error,
		"base_commit_sha":   result.DiffBaseCommitSHA,
		"diff_collected_at": result.DiffCollectedAt,
		"diff_stats":        diffStats,
	})
	if err != nil {
		return err
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return err
	}
	hydrateSessionPolicy(&session)
	s.publishStatus(ctx, &session)
	return nil
}

func (s *SessionStore) publishStatus(ctx context.Context, session *models.Session) {
	if s.streams == nil || session == nil {
		return
	}
	if err := s.streams.PublishStatus(ctx, session); err != nil {
		s.logger.Warn().Err(err).Str("session_id", session.ID.String()).Msg("failed to publish session status to Redis")
	}
}

func (s *SessionStore) publishWorkspaceGenerationChanged(ctx context.Context, orgID, sessionID uuid.UUID, revision int64, updatedAt time.Time, reason string) {
	if s.streams == nil {
		return
	}
	event := models.SessionWorkspaceGenerationChangedEvent{
		SessionID:                  sessionID,
		OrgID:                      orgID,
		WorkspaceRevision:          revision,
		WorkspaceRevisionUpdatedAt: updatedAt.UTC(),
		Reason:                     reason,
	}
	if err := s.streams.PublishEvent(ctx, models.SessionStreamEvent{
		Type:      models.SessionStreamEventWorkspaceGenerationChanged,
		SessionID: sessionID,
		OrgID:     orgID,
		Data:      event,
	}); err != nil {
		s.logger.Warn().Err(err).Str("session_id", sessionID.String()).Str("reason", reason).Msg("failed to publish workspace generation event to Redis")
	}
}

// ListTerminalEndedBefore returns terminal sessions whose completed_at is older than before.
// lint:allow-no-orgid reason="cross-org Redis cleanup scans terminal sessions across all orgs"
func (s *SessionStore) ListTerminalEndedBefore(ctx context.Context, before time.Time, limit int) ([]models.Session, error) {
	query := `
		SELECT ` + sessionSelectColumns + `
		FROM sessions
		WHERE status IN ('completed', 'failed', 'cancelled', 'pr_created', 'skipped')
		  AND completed_at IS NOT NULL
		  AND completed_at < @before
		ORDER BY completed_at ASC
		LIMIT @limit`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"before": before,
		"limit":  limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list terminal sessions before: %w", err)
	}
	sessions, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		hydrateSessionPolicy(&sessions[i])
	}
	return sessions, nil
}

// ClaimIdle atomically transitions an idle session to running and returns the
// claimed session row. Used when a user sends a follow-up message so only one
// continuation can be queued at a time.
// Sessions whose sandbox snapshot has been destroyed cannot be claimed.
func (s *SessionStore) ClaimIdle(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	query := `
		UPDATE sessions
		SET status = 'running', started_at = now(), completed_at = NULL,
		    error = NULL,
		    failure_explanation = NULL,
		    failure_category = NULL,
		    failure_next_steps = NULL,
		    failure_retry_advised = false,
		    last_activity_at = now()
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
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return models.Session{}, err
	}
	hydrateSessionPolicy(&session)
	return session, nil
}

// ClaimForResume atomically transitions a resumable paused session to running
// so it can continue from a follow-up message. Used when a user sends a
// message to a completed/failed/cancelled/pr_created session, or to a paused
// session waiting on guidance/input. The set of resumable statuses lives in
// models.ResumableSessionStatuses so the session and thread paths share a
// single source of truth.
// Sessions whose sandbox snapshot has been destroyed cannot be resumed.
func (s *SessionStore) ClaimForResume(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	query := fmt.Sprintf(`
		UPDATE sessions
		SET status = 'running', started_at = now(), completed_at = NULL,
		    %s,
		    last_activity_at = now()
		WHERE id = @id AND org_id = @org_id AND status = ANY(@statuses)
		  AND sandbox_state != 'destroyed'
		RETURNING `+sessionSelectColumns, sessionResumeRuntimeResetAssignments)

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":       sessionID,
		"org_id":   orgID,
		"statuses": sessionStatusStrings(models.ResumableSessionStatuses),
	})
	if err != nil {
		return models.Session{}, fmt.Errorf("claim terminal session for resume: %w", err)
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return models.Session{}, err
	}
	hydrateSessionPolicy(&session)
	return session, nil
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

func (s *SessionStore) UpdateRevisionContext(ctx context.Context, orgID, sessionID uuid.UUID, revisionContext []byte) error {
	_, err := s.db.Exec(ctx, `
		UPDATE sessions
		SET revision_context = @revision_context,
			last_activity_at = now()
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`, pgx.NamedArgs{
		"id":               sessionID,
		"org_id":           orgID,
		"revision_context": revisionContext,
	})
	return err
}

// sessionStatusStrings converts a slice of typed SessionStatus values into the
// raw []string form pgx needs to bind a postgres text[] parameter. Used by
// queries that match against models.ResumableSessionStatuses so the typed
// constant remains the single source of truth.
func sessionStatusStrings(statuses []models.SessionStatus) []string {
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = string(s)
	}
	return out
}

// threadStatusStrings is the thread-status counterpart to sessionStatusStrings.
// Used by SessionThreadStore queries that match against
// models.ResumableThreadStatuses.
func threadStatusStrings(statuses []models.ThreadStatus) []string {
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = string(s)
	}
	return out
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
		WHERE org_id = @org_id AND deleted_at IS NULL AND EXISTS (
			SELECT 1
			FROM session_issue_links sil
			WHERE sil.org_id = sessions.org_id AND sil.session_id = sessions.id AND sil.issue_id = @issue_id
		)
		ORDER BY created_at DESC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":   orgID,
		"issue_id": issueID,
	})
	if err != nil {
		return nil, fmt.Errorf("query sessions by issue: %w", err)
	}
	sessions, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		hydrateSessionPolicy(&sessions[i])
	}
	return sessions, nil
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
	sessions, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		hydrateSessionPolicy(&sessions[i])
	}
	return sessions, nil
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
	sessions, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		hydrateSessionPolicy(&sessions[i])
	}
	return sessions, nil
}

// UpdateTurnComplete sets the session to idle, persists the latest turn result,
// and updates multi-turn metadata. It also computes diff_stats and appends
// a snapshot to diff_history for diff-between-passes review.
func (s *SessionStore) UpdateTurnComplete(ctx context.Context, orgID, sessionID uuid.UUID, turn int, result *models.SessionResult, agentSessionID, snapshotKey string) error {
	diffStats := computeDiffStatsForResult(result)
	if !shouldPersistDiffSnapshot(result) {
		return s.updateTurnCompleteRow(ctx, s.db, orgID, sessionID, turn, result, agentSessionID, snapshotKey, diffStats)
	}

	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.updateTurnCompleteRow(ctx, tx, orgID, sessionID, turn, result, agentSessionID, snapshotKey, diffStats); err != nil {
		return err
	}
	updated, err := s.writeDiffSnapshot(ctx, tx, orgID, sessionID, turn, result, diffStats)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.publishWorkspaceGenerationChanged(ctx, orgID, sessionID, updated.Revision, updated.UpdatedAt, "diff_snapshot")
	return nil
}

func (s *SessionStore) updateTurnCompleteRow(ctx context.Context, db DBTX, orgID, sessionID uuid.UUID, turn int, result *models.SessionResult, agentSessionID, snapshotKey string, diffStats json.RawMessage) error {

	// COALESCE on diff / diff_stats / diff_collected_at: see updateResultRow
	// for the full rationale. Briefly: when sessiondiff.Collect returns
	// ErrNoBaseCommitSHA (or the agent produces no changes against base), the
	// adapter leaves result.Diff empty → strPtr returns nil → @diff is NULL
	// here. Falling back to the previously persisted diff is strictly better
	// than clobbering the Changes tab with a blank value.
	query := `
		UPDATE sessions
		SET status = 'idle',
		    -- Multiple sibling tabs can complete from the same in-memory
		    -- session.CurrentTurn. Advance relative to the row so concurrent
		    -- completions are counted instead of last-writer-wins collapsing
		    -- them to the same value.
		    current_turn = GREATEST(current_turn + 1, @current_turn),
		    last_activity_at = now(),
		    agent_session_id = @agent_session_id, snapshot_key = @snapshot_key,
		    sandbox_state = 'snapshotted',
		    workspace_generation = workspace_generation + 1,
		    pr_creation_state = 'idle', pr_creation_error = NULL,
		    -- Only reset pr_push_state when no push is currently in flight.
		    -- A concurrent turn-complete from the orchestrator must never
		    -- silently overwrite an active push (the handler's in-flight 409
		    -- guard relies on this column being authoritative).
		    pr_push_state = CASE WHEN pr_push_state IN ('queued', 'pushing') THEN pr_push_state ELSE 'idle' END,
		    pr_push_error = CASE WHEN pr_push_state IN ('queued', 'pushing') THEN pr_push_error ELSE NULL END,
		    branch_creation_state = CASE WHEN branch_creation_state IN ('queued', 'pushing') THEN branch_creation_state ELSE 'idle' END,
		    branch_creation_error = CASE WHEN branch_creation_state IN ('queued', 'pushing') THEN branch_creation_error ELSE NULL END,
		    token_usage = @token_usage,
		    model_used = COALESCE(@model_used, model_used),
		    result_summary = @result_summary,
		    diff = COALESCE(@diff, diff),
		    error = @error,
		    failure_explanation = NULL,
		    failure_category = NULL,
		    failure_next_steps = NULL,
		    failure_retry_advised = false,
		    base_commit_sha = COALESCE(@base_commit_sha, base_commit_sha),
		    diff_collected_at = COALESCE(@diff_collected_at, diff_collected_at),
		    diff_stats = COALESCE(@diff_stats, diff_stats),
		    diff_history = ` + diffHistoryAppendSQL("GREATEST(current_turn + 1, @current_turn)::int") + `
		WHERE id = @id AND org_id = @org_id`

	_, err := db.Exec(ctx, query, pgx.NamedArgs{
		"id":                sessionID,
		"org_id":            orgID,
		"current_turn":      turn,
		"agent_session_id":  agentSessionID,
		"snapshot_key":      snapshotKey,
		"token_usage":       result.TokenUsage,
		"model_used":        result.ModelUsed,
		"result_summary":    result.ResultSummary,
		"diff":              result.Diff,
		"error":             result.Error,
		"base_commit_sha":   result.DiffBaseCommitSHA,
		"diff_collected_at": result.DiffCollectedAt,
		"diff_stats":        diffStats,
	})
	return err
}

func shouldPersistDiffSnapshot(result *models.SessionResult) bool {
	return result != nil && result.Diff != nil && result.DiffBaseCommitSHA != nil && *result.DiffBaseCommitSHA != ""
}

func (s *SessionStore) writeDiffSnapshot(ctx context.Context, db DBTX, orgID, sessionID uuid.UUID, turn int, result *models.SessionResult, diffStats json.RawMessage) (workspaceRevisionUpdate, error) {
	stats := parseDiffStatsPayload(diffStats)
	source := result.DiffSource
	if source == "" {
		source = "turn_complete"
	}
	capturedAt := time.Now().UTC()
	if result.DiffCollectedAt != nil {
		capturedAt = result.DiffCollectedAt.UTC()
	}

	var snapshotID uuid.UUID
	insertQuery := `
		INSERT INTO session_diff_snapshots (
			session_id, org_id, turn_number, sequence_number, source,
			base_commit_sha, head_commit_sha, workspace_dirty, working_branch, target_branch, diff,
			files_changed, lines_added, lines_removed, captured_at,
			review_artifact_key, review_artifact_version, review_artifact_compressed_bytes,
			review_artifact_uncompressed_bytes, review_artifact_file_count,
			review_artifact_skipped_count, review_artifact_truncated
		)
		SELECT
			@session_id, @org_id, @turn_number, 1, @source,
			@base_commit_sha, @head_commit_sha, @workspace_dirty, working_branch, target_branch, @diff,
			@files_changed, @lines_added, @lines_removed, @captured_at,
			@review_artifact_key, @review_artifact_version, @review_artifact_compressed_bytes,
			@review_artifact_uncompressed_bytes, @review_artifact_file_count,
			@review_artifact_skipped_count, @review_artifact_truncated
		FROM sessions
		WHERE id = @session_id AND org_id = @org_id
		RETURNING id`

	if err := db.QueryRow(ctx, insertQuery, pgx.NamedArgs{
		"session_id":                         sessionID,
		"org_id":                             orgID,
		"turn_number":                        turn,
		"source":                             source,
		"base_commit_sha":                    *result.DiffBaseCommitSHA,
		"head_commit_sha":                    result.DiffHeadCommitSHA,
		"workspace_dirty":                    result.DiffWorkspaceDirty,
		"diff":                               *result.Diff,
		"files_changed":                      stats.FilesChanged,
		"lines_added":                        stats.Added,
		"lines_removed":                      stats.Removed,
		"captured_at":                        capturedAt,
		"review_artifact_key":                result.ReviewArtifactKey,
		"review_artifact_version":            result.ReviewArtifactVersion,
		"review_artifact_compressed_bytes":   result.ReviewArtifactCompressedBytes,
		"review_artifact_uncompressed_bytes": result.ReviewArtifactUncompressedBytes,
		"review_artifact_file_count":         result.ReviewArtifactFileCount,
		"review_artifact_skipped_count":      result.ReviewArtifactSkippedCount,
		"review_artifact_truncated":          result.ReviewArtifactTruncated,
	}).Scan(&snapshotID); err != nil {
		return workspaceRevisionUpdate{}, fmt.Errorf("insert session diff snapshot: %w", err)
	}

	rows, err := db.Query(ctx, `
		UPDATE sessions
		SET latest_diff_snapshot_id = @snapshot_id,
		    diff_collected_at = @captured_at,
		    workspace_revision = workspace_revision + 1,
		    workspace_revision_updated_at = @captured_at
		WHERE id = @session_id AND org_id = @org_id
		RETURNING workspace_revision, workspace_revision_updated_at`,
		pgx.NamedArgs{
			"snapshot_id": snapshotID,
			"captured_at": capturedAt,
			"session_id":  sessionID,
			"org_id":      orgID,
		},
	)
	if err != nil {
		return workspaceRevisionUpdate{}, fmt.Errorf("update session latest diff snapshot: %w", err)
	}
	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[struct {
		Revision  int64
		UpdatedAt time.Time
	}])
	if err != nil {
		return workspaceRevisionUpdate{}, fmt.Errorf("collect session workspace revision: %w", err)
	}
	return workspaceRevisionUpdate{Revision: updated.Revision, UpdatedAt: updated.UpdatedAt}, nil
}

// MarkLatestDiffSnapshotPushed normalizes the latest persisted diff snapshot
// after a successful PR create/push so the read-model no longer treats that
// snapshot as pending local work. This updates both the recorded head commit
// and the dirty-worktree bit because the push flow stages/commits any
// uncommitted changes before pushing.
func (s *SessionStore) MarkLatestDiffSnapshotPushed(ctx context.Context, orgID, sessionID uuid.UUID, pushedHeadSHA string) error {
	_, err := s.db.Exec(ctx, `
		UPDATE session_diff_snapshots
		SET head_commit_sha = @head_commit_sha,
		    workspace_dirty = FALSE
		WHERE id = (
			SELECT latest_diff_snapshot_id
			FROM sessions
			WHERE id = @id AND org_id = @org_id
		)
		  AND org_id = @org_id
	`, pgx.NamedArgs{
		"id":              sessionID,
		"org_id":          orgID,
		"head_commit_sha": pushedHeadSHA,
	})
	return err
}

func (s *SessionStore) UpdateBaseCommitSHA(ctx context.Context, orgID, sessionID uuid.UUID, baseCommitSHA string) error {
	query := `UPDATE sessions SET base_commit_sha = @base_commit_sha WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":              sessionID,
		"org_id":          orgID,
		"base_commit_sha": baseCommitSHA,
	})
	return err
}

// SetGitIdentity records which GitHub identity (user vs app) the orchestrator
// resolved for this session's git pushes. Stamped at session-start; nil
// userID is fine for the app-token path (the session was triggered without
// a user OAuth token, so attribution lives in the Co-authored-by trailer
// on each commit instead). Idempotent: re-running with the same values is
// a no-op write.
func (s *SessionStore) SetGitIdentity(ctx context.Context, orgID, sessionID uuid.UUID, source string, userID *uuid.UUID) error {
	query := `
		UPDATE sessions
		SET git_identity_source = @source,
		    git_identity_user_id = @user_id
		WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":      sessionID,
		"org_id":  orgID,
		"source":  source,
		"user_id": userID,
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

// UpdateWorkspaceSnapshot persists a refreshed snapshot key plus diff metadata
// for workspace-mutating actions that are not agent turns, such as "revert this
// tab". Unlike UpdateTurnComplete, this intentionally leaves status, current
// turn and summary untouched.
func (s *SessionStore) UpdateWorkspaceSnapshot(ctx context.Context, orgID, sessionID uuid.UUID, snapshotKey string, result *models.SessionResult) error {
	query := `
		UPDATE sessions
		SET snapshot_key = @snapshot_key,
		    sandbox_state = 'snapshotted',
		    last_activity_at = now(),
		    diff = COALESCE(@diff, diff),
		    base_commit_sha = COALESCE(@base_commit_sha, base_commit_sha),
		    diff_collected_at = COALESCE(@diff_collected_at, diff_collected_at)
		WHERE id = @id AND org_id = @org_id`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":                sessionID,
		"org_id":            orgID,
		"snapshot_key":      snapshotKey,
		"diff":              result.Diff,
		"base_commit_sha":   result.DiffBaseCommitSHA,
		"diff_collected_at": result.DiffCollectedAt,
	})
	return err
}

// UpdateSandboxState changes only the sandbox lifecycle column. It deliberately
// does NOT touch last_activity_at because the reaper calls this to mark
// long-completed sessions as 'destroyed' during snapshot cleanup — bumping the
// MRU timestamp there would resurface dormant sessions at the top of the list.
// Caller-driven activity (turn results, status transitions) bumps last_activity_at
// through its own update path.
func (s *SessionStore) UpdateSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, state models.SandboxState) error {
	query := `UPDATE sessions SET sandbox_state = @sandbox_state WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":            sessionID,
		"org_id":        orgID,
		"sandbox_state": string(state),
	})
	return err
}

// MarkRunningWithSandboxState flips status and sandbox_state in a single
// UPDATE so a caller can keep the session "running" while sibling thread
// runtimes mutate the shared sandbox without ever leaving the row in a
// half-updated state. Mirrors UpdateStatus's "clear failure fields and
// refresh started_at when entering running" semantics so a session resumed
// through the sibling-runtime keepalive path looks indistinguishable from
// one resumed via UpdateStatus(SessionStatusRunning) followed by
// UpdateSandboxState.
func (s *SessionStore) MarkRunningWithSandboxState(ctx context.Context, orgID, sessionID uuid.UUID, sandboxState models.SandboxState) error {
	rows, err := s.db.Query(ctx, `
		UPDATE sessions
		SET status = @status,
			sandbox_state = @sandbox_state,
			started_at = now(),
			completed_at = NULL,
			error = NULL,
			failure_explanation = NULL,
			failure_category = NULL,
			failure_next_steps = NULL,
			failure_retry_advised = false,
			runtime_soft_deadline_at = NULL,
			runtime_hard_deadline_at = NULL,
			runtime_last_progress_at = NULL,
			runtime_last_progress_type = '',
			runtime_last_progress_strength = '',
			runtime_stop_reason = '',
			runtime_graceful_stop_at = NULL,
			last_activity_at = now()
		WHERE id = @id
		  AND org_id = @org_id
		  AND deleted_at IS NULL
		RETURNING `+sessionSelectColumns, pgx.NamedArgs{
		"id":            sessionID,
		"org_id":        orgID,
		"status":        string(models.SessionStatusRunning),
		"sandbox_state": string(sandboxState),
	})
	if err != nil {
		return err
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return err
	}
	hydrateSessionPolicy(&session)
	s.publishStatus(ctx, &session)
	return nil
}

// UpdatePRCreationState transitions pr_creation_state and sets/clears
// pr_creation_error atomically. The error is only preserved in the `failed`
// state — any other transition clears it so a prior failure doesn't leak into
// the next attempt's UI. Pass errMsg == "" in all non-failed transitions.
//
// `succeeded` is terminal: this function refuses to transition out of it so
// a late worker retry can't flip a real PR back to `failed` or `pushing`.
// Transitioning `idle -> queued` from a `succeeded` state (e.g. after the PR
// was deleted upstream) is intentionally not supported here — the session
// row should be treated as done.
func (s *SessionStore) UpdatePRCreationState(ctx context.Context, orgID, sessionID uuid.UUID, state models.PRCreationState, errMsg string) error {
	if err := state.Validate(); err != nil {
		return err
	}
	var errArg any
	if state == models.PRCreationStateFailed && errMsg != "" {
		errArg = errMsg
	} else {
		errArg = nil
	}
	// RETURNING + publishStatus replaces a prior frontend 2s poll on
	// /sessions/{id}/pr that existed solely to observe this column's
	// transitions. Detail page now relies on the session status SSE.
	query := `UPDATE sessions
		SET pr_creation_state = @state, pr_creation_error = @err
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		  AND pr_creation_state <> 'succeeded'
		RETURNING ` + sessionSelectColumns
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
		"state":  string(state),
		"err":    errArg,
	})
	if err != nil {
		return err
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		// Pre-existing semantics: matching zero rows (e.g. the row is
		// already in the terminal `succeeded` state) is a no-op, not an
		// error. ErrNoRows here means the WHERE clause filtered the row
		// out — surface no error and skip publishing since nothing changed.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	hydrateSessionPolicy(&session)
	s.publishStatus(ctx, &session)
	return nil
}

// TryMarkPRCreationQueued atomically starts PR creation from any non-in-flight,
// non-terminal state. It rejects concurrent submitters and sessions that have
// already created a PR.
func (s *SessionStore) TryMarkPRCreationQueued(ctx context.Context, orgID, sessionID uuid.UUID) (bool, error) {
	query := `UPDATE sessions
		SET pr_creation_state = 'queued', pr_creation_error = NULL
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		  AND pr_creation_state NOT IN ('queued', 'pushing', 'succeeded')
		RETURNING ` + sessionSelectColumns
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	})
	if err != nil {
		return false, err
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	hydrateSessionPolicy(&session)
	s.publishStatus(ctx, &session)
	return true, nil
}

// TryMarkPRPushQueued atomically transitions pr_push_state from any non-in-
// flight state ('idle', 'succeeded', 'failed') to 'queued', clearing any
// previous error. Returns (true, nil) when the row was updated, (false, nil)
// when a concurrent request already moved the column to 'queued' or 'pushing'.
//
// The push handler uses this instead of UpdatePRPushState to start a push so
// two concurrent API requests that both pass the in-memory precheck cannot
// both transition the column to 'queued'. The handler's job-enqueue dedupe
// key collapses the worker side onto a single job; this CAS gives the API
// side the matching guarantee that exactly one of the racing requests
// returns 202 and the other returns 409.
func (s *SessionStore) TryMarkPRPushQueued(ctx context.Context, orgID, sessionID uuid.UUID) (bool, error) {
	// RETURNING + publishStatus so the API call that flips this column
	// surfaces immediately on the session status SSE — the detail-page
	// Push button used to wait for the worker's first state transition
	// (or a 1s polling fallback) to reflect the user's click.
	query := `UPDATE sessions
		SET pr_push_state = 'queued', pr_push_error = NULL
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		  AND pr_push_state NOT IN ('queued', 'pushing')
		RETURNING ` + sessionSelectColumns
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	})
	if err != nil {
		return false, err
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	hydrateSessionPolicy(&session)
	s.publishStatus(ctx, &session)
	return true, nil
}

// UpdatePRPushState transitions pr_push_state and sets/clears pr_push_error
// atomically. Mirrors UpdatePRCreationState but does not treat `succeeded` as
// terminal — a session can have its changes pushed multiple times across
// follow-up turns, so the column must be free to cycle through the state
// machine. Each new turn complete resets the column to `idle` separately.
//
// To start a new push (idle → queued) prefer TryMarkPRPushQueued, which
// rejects races between concurrent submitters; this method is for downstream
// transitions (queued → pushing → succeeded/failed) where the worker is the
// sole writer.
func (s *SessionStore) UpdatePRPushState(ctx context.Context, orgID, sessionID uuid.UUID, state models.PRPushState, errMsg string) error {
	if err := state.Validate(); err != nil {
		return err
	}
	var errArg any
	if state == models.PRPushStateFailed && errMsg != "" {
		errArg = errMsg
	} else {
		errArg = nil
	}
	// RETURNING + publishStatus so the detail-page Push button reflects
	// transitions on the existing session status SSE without a separate poll.
	query := `UPDATE sessions
		SET pr_push_state = @state, pr_push_error = @err
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		RETURNING ` + sessionSelectColumns
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
		"state":  string(state),
		"err":    errArg,
	})
	if err != nil {
		return err
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	hydrateSessionPolicy(&session)
	s.publishStatus(ctx, &session)
	return nil
}

// TryMarkBranchCreationQueued atomically starts a branch-only publish.
func (s *SessionStore) TryMarkBranchCreationQueued(ctx context.Context, orgID, sessionID uuid.UUID) (bool, error) {
	query := `UPDATE sessions
		SET branch_creation_state = 'queued', branch_creation_error = NULL
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		  AND branch_creation_state NOT IN ('queued', 'pushing')
		RETURNING ` + sessionSelectColumns
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	})
	if err != nil {
		return false, err
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	hydrateSessionPolicy(&session)
	s.publishStatus(ctx, &session)
	return true, nil
}

// UpdateBranchCreationState transitions branch_creation_state and stores the
// branch URL only on success.
func (s *SessionStore) UpdateBranchCreationState(ctx context.Context, orgID, sessionID uuid.UUID, state models.BranchCreationState, errMsg string, branchURL *string) error {
	if err := state.Validate(); err != nil {
		return err
	}
	var errArg any
	if state == models.BranchCreationStateFailed && errMsg != "" {
		errArg = errMsg
	}
	query := `UPDATE sessions
		SET branch_creation_state = @state,
		    branch_creation_error = @err,
		    branch_url = CASE WHEN @branch_url::text IS NULL THEN branch_url ELSE @branch_url::text END
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		RETURNING ` + sessionSelectColumns
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":         sessionID,
		"org_id":     orgID,
		"state":      string(state),
		"err":        errArg,
		"branch_url": branchURL,
	})
	if err != nil {
		return err
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	hydrateSessionPolicy(&session)
	s.publishStatus(ctx, &session)
	return nil
}

// publishActionJobTypes are the worker job types that drive the three
// PR-level publish columns (pr_creation_state / pr_push_state /
// branch_creation_state) through queued -> pushing -> succeeded/failed.
const publishActionJobTypes = `'open_pr', 'push_pr_changes', 'create_branch'`

// ListStuckPublishActionSessions returns sessions whose PR-level action column
// is wedged in an in-flight state ('queued' or 'pushing') with no backing job
// that can still make progress.
//
// The handlers write a terminal state inline on every error, so a clean
// failure already lands as 'failed'. The gap this closes is a job that dies
// *without* its handler returning — a worker OOM/crash/lease-loss mid-push.
// ReclaimLostRunningJobs requeues such a job and ClaimNextRunnable increments
// `attempts` on every re-dispatch, but a job that kills its worker on each
// attempt is never dead-lettered (the handler never reaches the dead-letter
// path), so `attempts` climbs past `max_attempts` while the column stays
// 'pushing' forever.
//
// A backing job counts as "still working" — and so blocks reconciliation — when
// either it has retry budget left (pending/running AND attempts < max_attempts)
// or a worker is provably on it right now (running with an unexpired lease). The
// live-lease clause matters because `attempts` is incremented at claim time, so
// the final attempt runs with attempts == max_attempts; without it a slow but
// healthy last attempt (a push can take minutes) would be force-failed mid-run.
// The OOM loop is still caught: it spends ~all its time either pending without
// budget or running with an expired lease, so a sweep lands on a moment where
// neither clause holds.
//
// The second EXISTS clause requires a backing job older than stuckBefore so we
// never race a brand-new action whose job is mid-enqueue, and bounds the scan
// to genuinely-aged rows.
func (s *SessionStore) ListStuckPublishActionSessions(ctx context.Context, orgID uuid.UUID, stuckBefore time.Time, limit int) ([]uuid.UUID, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT s.id
		FROM sessions s
		WHERE s.org_id = @org_id
		  AND s.deleted_at IS NULL
		  AND (
		    s.pr_creation_state IN ('queued', 'pushing')
		    OR s.pr_push_state IN ('queued', 'pushing')
		    OR s.branch_creation_state IN ('queued', 'pushing')
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM jobs j
		    WHERE j.org_id = s.org_id
		      AND j.job_type IN (` + publishActionJobTypes + `)
		      AND NULLIF(j.payload->>'session_id', '') = s.id::text
		      AND (
		        -- Still has retry budget: will run (or run again) and terminalize.
		        (j.status IN ('pending', 'running') AND j.attempts < j.max_attempts)
		        -- Or a worker is provably on it right now. attempts is bumped at
		        -- claim time, so the final attempt runs with attempts ==
		        -- max_attempts; the live lease keeps us from force-failing a slow
		        -- but healthy last attempt mid-run.
		        OR (j.status = 'running' AND j.lease_expires_at IS NOT NULL AND j.lease_expires_at > now())
		      )
		  )
		  AND EXISTS (
		    SELECT 1 FROM jobs j2
		    WHERE j2.org_id = s.org_id
		      AND j2.job_type IN (` + publishActionJobTypes + `)
		      AND NULLIF(j2.payload->>'session_id', '') = s.id::text
		      AND j2.created_at < @stuck_before
		  )
		ORDER BY s.id
		LIMIT @limit`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":       orgID,
		"stuck_before": stuckBefore,
		"limit":        limit,
	})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// FailInFlightPublishActions force-transitions any of the three PR-level
// publish columns that are still in flight ('queued'/'pushing') to 'failed'
// with errMsg, leaving already-terminal columns untouched. It is the durable
// backstop for ListStuckPublishActionSessions: a guarded CAS that cannot
// clobber a state that legitimately advanced between the scan and this write,
// and a no-op (returns false) when nothing is in flight. On a real transition
// it publishes the session status SSE so connected clients converge.
func (s *SessionStore) FailInFlightPublishActions(ctx context.Context, orgID, sessionID uuid.UUID, errMsg string) (bool, error) {
	query := `UPDATE sessions
		SET pr_creation_state = CASE WHEN pr_creation_state IN ('queued', 'pushing') THEN 'failed' ELSE pr_creation_state END,
		    pr_creation_error = CASE WHEN pr_creation_state IN ('queued', 'pushing') THEN @err ELSE pr_creation_error END,
		    pr_push_state = CASE WHEN pr_push_state IN ('queued', 'pushing') THEN 'failed' ELSE pr_push_state END,
		    pr_push_error = CASE WHEN pr_push_state IN ('queued', 'pushing') THEN @err ELSE pr_push_error END,
		    branch_creation_state = CASE WHEN branch_creation_state IN ('queued', 'pushing') THEN 'failed' ELSE branch_creation_state END,
		    branch_creation_error = CASE WHEN branch_creation_state IN ('queued', 'pushing') THEN @err ELSE branch_creation_error END
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL
		  AND (
		    pr_creation_state IN ('queued', 'pushing')
		    OR pr_push_state IN ('queued', 'pushing')
		    OR branch_creation_state IN ('queued', 'pushing')
		  )
		RETURNING ` + sessionSelectColumns
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
		"err":    errMsg,
	})
	if err != nil {
		return false, err
	}
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	hydrateSessionPolicy(&session)
	s.publishStatus(ctx, &session)
	return true, nil
}

// ClearSnapshotKey NULLs the snapshot_key column and transitions sandbox_state
// to 'destroyed'. Called after the snapshot file has been removed from storage
// — on PR merge, session archive, or reaper expiry. Idempotent: calling it on
// a row that already has snapshot_key NULL is a no-op.
func (s *SessionStore) ClearSnapshotKey(ctx context.Context, orgID, sessionID uuid.UUID) error {
	query := `UPDATE sessions
		SET snapshot_key = NULL, sandbox_state = 'destroyed'
		WHERE id = @id AND org_id = @org_id AND snapshot_key IS NOT NULL`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	})
	return err
}

// SetPendingSnapshotKey records that an async upload is in progress for the
// given key. Called immediately after CreatePR captures the post-push tar but
// before the upload to object storage begins. Hydration paths must check this
// column and wait until it is NULL before resuming a session — see
// PromotePendingSnapshot.
//
// Also stamps pending_snapshot_set_at = NOW() so the stranded-pending reaper
// can identify rows whose owning upload goroutine died.
func (s *SessionStore) SetPendingSnapshotKey(ctx context.Context, orgID, sessionID uuid.UUID, key string) error {
	query := `UPDATE sessions
		SET pending_snapshot_key = @key,
		    pending_snapshot_set_at = NOW()
		WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
		"key":    key,
	})
	return err
}

// PromotePendingSnapshot atomically advances snapshot_key to the value of
// pending_snapshot_key and clears pending_snapshot_key. Called by the upload
// goroutine once snapshots.Save returns success.
//
// The expectedKey guard ensures that a stale upload that lost a race with a
// newer one does not clobber the newer key — if pending_snapshot_key has
// already been changed (or cleared), this is a no-op. sandbox_state is also
// set to 'snapshotted' to mirror the invariant maintained by
// UpdateSnapshotInfo. pending_snapshot_set_at is cleared in lockstep so the
// stranded-pending reaper does not see a phantom timestamp.
func (s *SessionStore) PromotePendingSnapshot(ctx context.Context, orgID, sessionID uuid.UUID, expectedKey string) error {
	query := `UPDATE sessions
		SET snapshot_key = pending_snapshot_key,
		    pending_snapshot_key = NULL,
		    pending_snapshot_set_at = NULL,
		    sandbox_state = 'snapshotted',
		    workspace_revision = workspace_revision + 1,
		    workspace_revision_updated_at = NOW()
		WHERE id = @id AND org_id = @org_id AND pending_snapshot_key = @expected_key
		RETURNING workspace_revision, workspace_revision_updated_at`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":           sessionID,
		"org_id":       orgID,
		"expected_key": expectedKey,
	})
	if err != nil {
		return err
	}
	updated, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[struct {
		Revision  int64
		UpdatedAt time.Time
	}])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s.publishWorkspaceGenerationChanged(ctx, orgID, sessionID, updated.Revision, updated.UpdatedAt, "promote_pending_snapshot")
	return nil
}

// ClearPendingSnapshot NULLs pending_snapshot_key (and pending_snapshot_set_at
// in lockstep) without touching snapshot_key. Called when an in-flight upload
// fails — the session falls back to its previous (pre-PR) snapshot, which is
// degraded but recoverable (the user can retry the action; the agent itself
// can re-fetch state).
func (s *SessionStore) ClearPendingSnapshot(ctx context.Context, orgID, sessionID uuid.UUID) error {
	query := `UPDATE sessions
		SET pending_snapshot_key = NULL,
		    pending_snapshot_set_at = NULL
		WHERE id = @id AND org_id = @org_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	})
	return err
}

// ReapStrandedPendingSnapshots clears pending_snapshot_key (and
// pending_snapshot_set_at) for any session whose pending upload was set
// before olderThan. A row is considered stranded when its owning upload
// goroutine cannot finish — the worker hard-crashed, the graceful drain
// timed out, or some bug left the row inconsistent. Callers should pass an
// olderThan that comfortably exceeds postPRSnapshotUploadTimeout so a
// legitimately slow upload is never reaped out from under itself.
//
// Returns the number of rows cleared so the caller can log/meter.
//
// The WHERE clause uses pending_snapshot_set_at <= @older_than rather than
// "<", so a clock with second-level resolution can still reap a row whose
// timestamp is exactly equal to the threshold instant.
//
// lint:allow-no-orgid reason="cross-org sweep run by the leader-elected cluster.Scheduler — per-org fanout adds no security since the reaper takes no external input, and would force enumerating every org each tick"
func (s *SessionStore) ReapStrandedPendingSnapshots(ctx context.Context, olderThan time.Time) (int64, error) {
	query := `UPDATE sessions
		SET pending_snapshot_key = NULL,
		    pending_snapshot_set_at = NULL
		WHERE pending_snapshot_key IS NOT NULL
		  AND pending_snapshot_set_at IS NOT NULL
		  AND pending_snapshot_set_at <= @older_than`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{"older_than": olderThan})
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
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
// turn_holding_container only flips to TRUE when the caller also wins the
// container_id write (row was NULL or already matches). A caller that loses
// the COALESCE race leaves the flag unchanged, so the winning holder's state
// isn't clobbered — and so the loser's subsequent ReleaseTurnHold can only
// ever drop its own flag, never someone else's.
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
		    turn_holding_container = CASE
		        WHEN container_id IS NULL OR container_id = @container_id THEN TRUE
		        ELSE turn_holding_container
		    END
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
			), FALSE) AS preview_holds,
			COALESCE((
				SELECT TRUE
				FROM session_sandbox_holders h
				WHERE h.session_id = released.id
				  AND h.org_id = @org_id
				  AND h.status IN ('active', 'draining')
				  AND h.expires_at > now()
				LIMIT 1
			), FALSE) AS sandbox_holds
		FROM released`

	var cid string
	var previewHolds bool
	var sandboxHolds bool
	err = s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	}).Scan(&cid, &previewHolds, &sandboxHolds)
	if err != nil {
		return false, "", fmt.Errorf("release turn hold: %w", err)
	}
	return cid != "" && !previewHolds && !sandboxHolds, cid, nil
}

// PeekContainerID returns the session's current container_id (empty when
// NULL) using a single-column lookup. It exists for the preview hydrate
// race-detection peek, which only needs to know whether a peer has
// published a container_id since the caller last read the row — fetching
// the full Session struct via GetByID would pull ~30 columns and hydrate
// the policy JSON for no benefit. Returns the empty string for both
// "no row" and "NULL container_id"; the caller cannot distinguish those
// cases, but neither outcome should change the hydrate path's behavior.
func (s *SessionStore) PeekContainerID(ctx context.Context, orgID, sessionID uuid.UUID) (string, error) {
	query := `SELECT COALESCE(container_id, '')
		FROM sessions
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	var containerID string
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	}).Scan(&containerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("peek container id: %w", err)
	}
	return containerID, nil
}

// PeekContainerOwnership returns the current container_id and worker_node_id
// using a narrow lookup for sandbox race handling. Empty strings represent
// NULL values or a missing row.
func (s *SessionStore) PeekContainerOwnership(ctx context.Context, orgID, sessionID uuid.UUID) (containerID string, workerNodeID string, err error) {
	query := `SELECT COALESCE(container_id, ''), COALESCE(worker_node_id, '')
		FROM sessions
		WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL`
	err = s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	}).Scan(&containerID, &workerNodeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", nil
		}
		return "", "", fmt.Errorf("peek container ownership: %w", err)
	}
	return containerID, workerNodeID, nil
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

// ContainerHoldState returns whether the expected container is currently held
// by an agent turn, by a preview, or both. It is intentionally pinned to
// container_id = @expected so callers diagnosing a race do not accidentally
// read holder state for a newer container published after their liveness probe.
func (s *SessionStore) ContainerHoldState(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (turnHolds bool, previewHolds bool, err error) {
	query := `SELECT
			COALESCE(s.turn_holding_container, FALSE) AS turn_holds,
			EXISTS (
				SELECT 1
				FROM preview_instances p
				WHERE p.session_id = s.id
				  AND p.org_id = s.org_id
				  AND p.preview_holding_container = TRUE
			) AS preview_holds
		FROM sessions s
		WHERE s.id = @id
		  AND s.org_id = @org_id
		  AND s.container_id = @expected`
	if err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"id":       sessionID,
		"org_id":   orgID,
		"expected": expectedContainerID,
	}).Scan(&turnHolds, &previewHolds); err != nil {
		return false, false, fmt.Errorf("container hold state: %w", err)
	}
	return turnHolds, previewHolds, nil
}

// SetWorkerNodeIDForContainer records the worker node currently owning the
// session's live container. The update is CAS-scoped to the expected
// container_id so callers do not accidentally stamp ownership onto a newer
// container after a race.
func (s *SessionStore) SetWorkerNodeIDForContainer(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID, workerNodeID string) error {
	query := `UPDATE sessions
		SET worker_node_id = @worker_node_id
		WHERE id = @id
		  AND org_id = @org_id
		  AND container_id = @expected
		  AND COALESCE(worker_node_id, '') IN ('', @worker_node_id)`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":             sessionID,
		"org_id":         orgID,
		"expected":       expectedContainerID,
		"worker_node_id": workerNodeID,
	})
	if err != nil {
		return fmt.Errorf("set worker node id for container: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Two CAS conditions can produce this: container_id no longer matches
		// expectedContainerID (a concurrent hydrate/destroy raced us), or
		// worker_node_id is already held by a different worker. The IDs are
		// not in this string because callers surface it as a user-facing chat
		// message; callers log the structured IDs separately for ops.
		return fmt.Errorf("session container ownership changed before worker ownership could be recorded")
	}
	return nil
}

// ClearContainerID is the startup reconciler's CAS-safe orphan cleanup: it
// clears container_id only when the expected ID still matches AND no preview
// hold has appeared between the reconciler's scan and this call. Returns
// cleared=true when the row was updated — the caller now owns the right to
// destroy the container. cleared=false means the row was already retaken
// (concurrent hydrate published a new container_id) or a preview claimed the
// hold in the gap; the reconciler must leave the container alone.
//
// It deliberately clears turn_holding_container=FALSE as well. A crashed-turn
// row can carry turn_holding_container=TRUE (the orchestrator never got to
// release), and leaving that flag stuck would (a) permanently pin the session
// out of ListIdlePreviews and (b) prevent any subsequent orphan pass from
// picking the row up. The reconciler only calls this after IsAlive confirmed
// the container is gone from the host, so the flag is definitively stale —
// resetting it is both safe and necessary.
//
// It intentionally does not touch sandbox_state: the reconciler doesn't know
// whether a valid snapshot exists, so deciding between 'snapshotted' and
// 'destroyed' is the reaper's job (Phase 2 / Phase 4 in SessionReaper.reap).
// Lifecycle code paths (orchestrator / preview manager) call
// FinalizeContainerDestroy instead, which additionally flips sandbox_state
// to 'snapshotted'.
func (s *SessionStore) ClearContainerID(ctx context.Context, orgID, sessionID uuid.UUID, expectedContainerID string) (cleared bool, err error) {
	// worker_node_id is paired with container_id ownership: once the container
	// is gone, the recorded owner is by definition stale. Leaving it set would
	// trip up the next turn's SetWorkerNodeIDForContainer CAS (which rejects
	// a different worker stamping over a stale value) and silently fail the
	// turn — symmetrical to FinalizeContainerDestroy, which clears both.
	query := `UPDATE sessions
		SET container_id = NULL,
		    worker_node_id = NULL,
		    turn_holding_container = FALSE
		WHERE id = @id
		  AND org_id = @org_id
		  AND container_id = @expected
		  AND NOT EXISTS (
		    SELECT 1 FROM preview_instances p
		    WHERE p.session_id = sessions.id
		      AND p.org_id = sessions.org_id
		      AND p.preview_holding_container = TRUE
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM session_sandbox_holders h
		    WHERE h.session_id = sessions.id
		      AND h.org_id = sessions.org_id
		      AND h.status IN ('active', 'draining')
		      AND h.expires_at > now()
		  )`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":       sessionID,
		"org_id":   orgID,
		"expected": expectedContainerID,
	})
	if err != nil {
		return false, fmt.Errorf("clear container id: %w", err)
	}
	return tag.RowsAffected() > 0, nil
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
		SET container_id = NULL,
		    worker_node_id = NULL,
		    sandbox_state = CASE
		        WHEN snapshot_key IS NULL OR snapshot_key = '' THEN 'none'
		        ELSE 'snapshotted'
		    END
		WHERE id = @id
		  AND org_id = @org_id
		  AND container_id = @expected
		  AND turn_holding_container = FALSE
		  AND NOT EXISTS (
		    SELECT 1 FROM preview_instances p
		    WHERE p.session_id = sessions.id
		      AND p.org_id = sessions.org_id
		      AND p.preview_holding_container = TRUE
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM session_sandbox_holders h
		    WHERE h.session_id = sessions.id
		      AND h.org_id = sessions.org_id
		      AND h.status IN ('active', 'draining')
		      AND h.expires_at > now()
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

// ListOrphanedContainers returns sessions whose container_id is set and no
// preview currently holds the sandbox. Called on startup to clean up
// containers that leaked from a crashed server — the reconciler probes each
// row's container via IsAlive, then calls ClearContainerID (CAS-safe) and,
// if that claimed the row, destroys the container.
//
// turn_holding_container is deliberately NOT filtered here: a worker crash
// mid-turn leaves the flag stuck TRUE, and if we skipped those rows the
// reconciler could never reclaim them. IsAlive (run by the caller) is the
// ground-truth gate — live turns on the shared host show as alive and are
// left alone; truly orphaned turn-held rows come back dead and get cleared.
// Preview holds are still filtered out because a live preview owns its own
// teardown path and the reaper's Phase-2 preview-stopper handles stuck
// preview holds separately.
//
// Returns at most 100 rows per call, keyset-paginated by session id > afterID
// and ordered by id ASC. The reconciler passes the last seen id as a cursor
// so that rows it can't clear (e.g. transient destroy/inspect failures)
// don't cause it to re-read the same 100 rows forever — it simply moves past
// them and they'll be picked up again on the next startup.
// lint:allow-no-orgid reason="startup reconciler scans across all orgs by design"
func (s *SessionStore) ListOrphanedContainers(ctx context.Context, afterID uuid.UUID) ([]models.Session, error) {
	query := `
		SELECT ` + sessionSelectColumns + `
		FROM sessions
		WHERE container_id IS NOT NULL
		  AND id > @after_id
		  AND status <> 'running'
		  AND COALESCE(recovery_state, '') NOT IN ('queued', 'recovering')
		  AND NOT EXISTS (
		    SELECT 1 FROM preview_instances p
		    WHERE p.session_id = sessions.id
		      AND p.org_id = sessions.org_id
		      AND p.preview_holding_container = TRUE
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM session_sandbox_holders h
		    WHERE h.session_id = sessions.id
		      AND h.org_id = sessions.org_id
		      AND h.status IN ('active', 'draining')
		      AND h.expires_at > now()
		  )
		ORDER BY id ASC
		LIMIT 100`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"after_id": afterID})
	if err != nil {
		return nil, fmt.Errorf("list orphaned containers: %w", err)
	}
	sessions, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		hydrateSessionPolicy(&sessions[i])
	}
	return sessions, nil
}

// ListReferencedContainerIDs returns every live container_id currently
// referenced by a session row. It is used by worker-local Docker GC to avoid
// deleting a container that any DB row still owns.
// lint:allow-no-orgid reason="worker-local Docker GC reconciles host containers against all session container references"
func (s *SessionStore) ListReferencedContainerIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT container_id
		FROM sessions
		WHERE container_id IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("list referenced container ids: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan referenced container id: %w", err)
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate referenced container ids: %w", err)
	}
	return ids, nil
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

// ListStalePendingSessions returns pending sessions whose latest pending-state
// activity is before the given cutoff. These sessions have been stuck in
// pending for too long and should be failed with an explanatory error.
// lint:allow-no-orgid reason="cross-org reaper scan for stuck pending sessions"
func (s *SessionStore) ListStalePendingSessions(ctx context.Context, activityBefore time.Time) ([]models.Session, error) {
	// No alias on `sessions`: sessionPrimaryIssueIDColumn references
	// sessions.org_id / sessions.id literally, and a table alias would shadow
	// the original name (Postgres 42P01).
	query := `
		SELECT ` + sessionListColumns + `
		FROM sessions
		WHERE status = 'pending'
		  AND deleted_at IS NULL
		  AND last_activity_at < @activity_before
		ORDER BY last_activity_at ASC
		LIMIT 100`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"activity_before": activityBefore,
	})
	if err != nil {
		return nil, fmt.Errorf("query stale pending sessions: %w", err)
	}
	sessions, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		hydrateSessionPolicy(&sessions[i])
	}
	return sessions, nil
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
	// No alias on `sessions`: sessionPrimaryIssueIDColumn references
	// sessions.org_id / sessions.id literally, and a table alias would shadow
	// the original name (Postgres 42P01).
	query := `
		SELECT ` + sessionListColumns + `
		FROM sessions
		WHERE status = 'running'
		  AND deleted_at IS NULL
		  AND started_at IS NOT NULL
		  AND started_at < @started_before
		ORDER BY started_at ASC
		LIMIT 100`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"started_before": startedBefore,
	})
	if err != nil {
		return nil, fmt.Errorf("query stale running sessions: %w", err)
	}
	sessions, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		hydrateSessionPolicy(&sessions[i])
	}
	return sessions, nil
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
	sessions, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		hydrateSessionPolicy(&sessions[i])
	}
	return sessions, nil
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
	sessions, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.Session])
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		hydrateSessionPolicy(&sessions[i])
	}
	return sessions, nil
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

// ArchiveSystem archives a session without a user actor (e.g. webhook-driven
// auto-archive). archived_by_user_id is left NULL. The bool return reports
// whether this call transitioned the row into the archived state; already-
// archived sessions are treated as a no-op rather than an error.
func (s *SessionStore) ArchiveSystem(ctx context.Context, orgID, sessionID uuid.UUID) (bool, error) {
	query := `UPDATE sessions SET archived_at = now(), archived_by_user_id = NULL WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL AND archived_at IS NULL`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":     sessionID,
		"org_id": orgID,
	})
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
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
	if _, err := s.db.Exec(ctx, `
		WITH target_runs AS (
			UPDATE session_preview_prewarm_runs
			SET status = 'failed',
				error = 'session deleted',
				completed_at = now(),
				updated_at = now()
			WHERE org_id = @org_id
			  AND session_id = @session_id
			  AND status IN ('queued', 'running')
			RETURNING job_id
		),
		cancelled_jobs AS (
			UPDATE jobs
			SET status = 'cancelled',
				last_error = 'session deleted',
				completed_at = now(),
				locked_by_node_id = NULL,
				run_owner_id = NULL,
				owner_kind = 'worker',
				lock_token = NULL,
				locked_at = NULL,
				lease_expires_at = NULL,
				updated_at = now()
			WHERE org_id = @org_id
			  AND id IN (SELECT job_id FROM target_runs WHERE job_id IS NOT NULL)
			  AND status IN ('pending', 'running')
			RETURNING id
		)
		UPDATE preview_groups
		SET current_status = 'expired',
			last_activity_at = now()
		WHERE org_id = @org_id
		  AND group_kind = 'session'
		  AND source_id = @session_id::text`,
		pgx.NamedArgs{"org_id": orgID, "session_id": sessionID},
	); err != nil {
		return fmt.Errorf("cleanup session preview prewarm state: %w", err)
	}
	return nil
}
