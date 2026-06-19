package db

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

var sessionTestColumns = []string{
	"id", "primary_issue_id", "org_id", "origin", "interaction_mode", "validation_policy",
	"agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier",
	"container_id", "worker_node_id", "turn_holding_container", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "reasoning_effort", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at", "sandbox_state", "workspace_generation", "snapshot_key", "pending_snapshot_key", "pending_snapshot_set_at",
	"runtime_soft_deadline_at", "runtime_hard_deadline_at", "runtime_last_progress_at", "runtime_last_progress_type", "runtime_last_progress_strength",
	"runtime_extension_count", "runtime_extension_seconds", "runtime_stop_reason", "runtime_graceful_stop_at",
	"checkpointed_at", "checkpoint_kind", "checkpoint_capability", "checkpoint_size_bytes", "checkpoint_error",
	"recovery_state", "recovery_queued_at", "recovery_started_at", "recovery_attempt_count",
	"target_branch", "working_branch", "base_commit_sha", "repository_id", "diff_stats", "diff_history", "input_manifest", "archived_at", "archived_by_user_id", "automation_run_id", "pr_creation_state", "pr_creation_error", "pr_push_state", "pr_push_error", "branch_creation_state", "branch_creation_error", "branch_url", "diff_collected_at", "latest_diff_snapshot_id", "workspace_revision", "workspace_revision_updated_at",
	"has_unpushed_changes",
	"linear_private", "linear_state_sync_disabled", "linear_identifier_hint", "linear_prepare_state",
	"deleted_at", "capability_snapshot", "git_identity_source", "git_identity_user_id", "created_at",
}

func anyDBArgs(count int) []interface{} {
	args := make([]interface{}, count)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func setSessionTestColumnValue(row []interface{}, column string, value interface{}) {
	for i, col := range sessionTestColumns {
		if col == column {
			row[i] = value
			return
		}
	}
	panic("unknown session test column: " + column)
}

func sqlFragmentPattern(fragment string) string {
	fields := strings.Fields(fragment)
	for i := range fields {
		fields[i] = regexp.QuoteMeta(fields[i])
	}
	return strings.Join(fields, `\s+`)
}

func claimForResumeQueryPattern() string {
	return `UPDATE sessions\s+SET status = 'running', started_at = now\(\), completed_at = NULL,\s+` +
		sqlFragmentPattern(sessionResumeRuntimeResetAssignments) +
		`,\s+last_activity_at = now\(\)\s+WHERE id = @id AND org_id = @org_id AND status = ANY\(@statuses\)\s+AND sandbox_state != 'destroyed'\s+RETURNING`
}

// newAgentSessionRow returns a completed-session row for mock queries. The
// three timestamp columns are distinct so that tests which assert on
// MRU ordering or pagination drift against last_activity_at don't accidentally
// also satisfy a regression that ordered by started_at / created_at.
func newAgentSessionRow(sessionID, issueID, orgID uuid.UUID, now time.Time) []interface{} {
	createdAt := now.Add(-2 * time.Hour) // oldest
	startedAt := now.Add(-time.Hour)     // middle
	lastActivityAt := now                // newest
	completedAt := now.Add(-5 * time.Minute)
	var primaryIssueID any
	if issueID != uuid.Nil {
		issueIDCopy := issueID
		primaryIssueID = &issueIDCopy
	}
	return []interface{}{
		sessionID, primaryIssueID, orgID, "issue_trigger", "single_run", "on_turn_complete",
		"claude-code", "completed", "supervised", "low",
		nil,
		nil, nil, false, &startedAt, &completedAt, nil,
		nil, nil, nil, false,
		nil, nil, nil, nil, nil,
		nil, nil, nil, nil,
		nil, nil, nil, nil,
		nil, 0, lastActivityAt, "none", int64(0), nil, nil, nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, workspace_generation, snapshot_key, pending_snapshot_key, pending_snapshot_set_at
		nil,      // runtime_soft_deadline_at
		nil,      // runtime_hard_deadline_at
		nil,      // runtime_last_progress_at
		"",       // runtime_last_progress_type
		"",       // runtime_last_progress_strength
		0,        // runtime_extension_count
		0,        // runtime_extension_seconds
		"",       // runtime_stop_reason
		nil,      // runtime_graceful_stop_at
		nil,      // checkpointed_at
		"",       // checkpoint_kind
		"",       // checkpoint_capability
		int64(0), // checkpoint_size_bytes
		nil,      // checkpoint_error
		"",       // recovery_state
		nil,      // recovery_queued_at
		nil,      // recovery_started_at
		0,        // recovery_attempt_count
		nil,      // target_branch
		nil,      // working_branch
		nil,      // base_commit_sha
		nil,      // repository_id
		nil,      // diff_stats
		nil,      // diff_history
		nil,      // input_manifest
		nil, nil, // archived_at, archived_by_user_id
		nil,            // automation_run_id
		"idle",         // pr_creation_state
		(*string)(nil), // pr_creation_error
		"idle",         // pr_push_state
		(*string)(nil), // pr_push_error
		"idle",         // branch_creation_state
		(*string)(nil), // branch_creation_error
		(*string)(nil), // branch_url
		nil,            // diff_collected_at
		nil,            // latest_diff_snapshot_id
		int64(0),       // workspace_revision
		now,            // workspace_revision_updated_at
		false,          // has_unpushed_changes
		false,          // linear_private
		false,          // linear_state_sync_disabled
		(*string)(nil), // linear_identifier_hint
		"none",         // linear_prepare_state
		nil,            // deleted_at
		nil,            // capability_snapshot
		nil,            // git_identity_source
		nil,            // git_identity_user_id
		createdAt,
	}
}

func TestSessionStore_ListByOrg_WithRepositoryID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	repoID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ repository_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).
				AddRow(newAgentSessionRow(sessionID, issueID, orgID, now)...),
		)

	sessions, err := store.ListByOrg(context.Background(), orgID, SessionFilters{
		RepositoryID: repoID,
	})
	require.NoError(t, err, "ListByOrg with RepositoryID should not return an error")
	require.Len(t, sessions, 1, "should return one session")
	require.Equal(t, sessionID, sessions[0].ID, "should return the correct session ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_ListByOrg_WithoutRepositoryID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).
				AddRow(newAgentSessionRow(sessionID, issueID, orgID, now)...),
		)

	sessions, err := store.ListByOrg(context.Background(), orgID, SessionFilters{})
	require.NoError(t, err, "ListByOrg without RepositoryID should not return an error")
	require.Len(t, sessions, 1, "should return one session")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_ListByOrg_OmitsRawDiffPayloads(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()

	mock.ExpectQuery(`(?s)SELECT .+NULL::text AS diff.+NULL::jsonb AS diff_history.+FROM sessions WHERE org_id`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns))

	sessions, err := store.ListByOrg(context.Background(), orgID, SessionFilters{})
	require.NoError(t, err, "ListByOrg should not return an error when selecting lightweight session rows")
	require.Empty(t, sessions, "ListByOrg should return no rows from the empty result set")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_GetAPIDetailByID_OmitsRawDiffPayloads(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery(`(?s)SELECT .+NULL::text AS diff.+NULL::jsonb AS diff_history.+FROM sessions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).
				AddRow(newAgentSessionRow(sessionID, uuid.Nil, orgID, now)...),
		)

	session, err := store.GetAPIDetailByID(context.Background(), orgID, sessionID)
	require.NoError(t, err, "GetAPIDetailByID should not return an error")
	require.Equal(t, sessionID, session.ID, "GetAPIDetailByID should return the requested session")
	require.Nil(t, session.Diff, "API detail fetch should not hydrate the raw diff")
	require.Nil(t, session.DiffHistory, "API detail fetch should not hydrate diff history")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_GetDiffByID_ReturnsDiffPayload(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	diff := "diff --git a/main.go b/main.go\n"
	diffStats := json.RawMessage(`{"added":1,"removed":0,"files_changed":1}`)
	diffHistory := json.RawMessage(`[{"pass":1,"diff":"diff --git a/main.go b/main.go\n","diff_stats":{"added":1,"removed":0,"files_changed":1},"created_at":"2026-01-01T00:00:00Z"}]`)

	mock.ExpectQuery(`(?s)SELECT .+left\(diff, @diff_max_chars\).+pg_column_size\(diff_history\).+FROM sessions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"diff", "diff_stats", "diff_history", "diff_truncated", "diff_history_truncated",
				"diff_chars", "diff_history_bytes", "diff_max_chars", "diff_history_max_bytes",
			}).
				AddRow(&diff, diffStats, diffHistory, false, false, int64(len(diff)), int64(len(diffHistory)), int64(sessionDiffMaxChars), int64(sessionDiffHistoryMaxBytes)),
		)

	payload, err := store.GetDiffByID(context.Background(), orgID, sessionID)
	require.NoError(t, err, "GetDiffByID should not return an error")
	require.Equal(t, sessionID, payload.SessionID, "GetDiffByID should preserve the requested session ID")
	require.Equal(t, &diff, payload.Diff, "GetDiffByID should return the raw diff")
	require.Equal(t, diffStats, payload.DiffStats, "GetDiffByID should return persisted diff stats")
	require.Equal(t, diffHistory, payload.DiffHistory, "GetDiffByID should return diff history")
	require.False(t, payload.DiffTruncated, "GetDiffByID should report an untruncated diff when it fits")
	require.False(t, payload.DiffHistoryTruncated, "GetDiffByID should report untruncated history when it fits")
	require.Equal(t, int64(len(diff)), payload.DiffChars, "GetDiffByID should return the original diff size")
	require.Equal(t, int64(sessionDiffMaxChars), payload.DiffMaxChars, "GetDiffByID should return the configured diff cap")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_GetDiffByID_ReturnsTruncationMetadata(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	truncatedDiff := strings.Repeat("+", 128)
	diffStats := json.RawMessage(`{"added":99999,"removed":1,"files_changed":3000}`)

	mock.ExpectQuery(`(?s)left\(diff, @diff_max_chars\).+CASE WHEN diff_history IS NULL THEN NULL.+pg_column_size\(diff_history\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"diff", "diff_stats", "diff_history", "diff_truncated", "diff_history_truncated",
				"diff_chars", "diff_history_bytes", "diff_max_chars", "diff_history_max_bytes",
			}).
				AddRow(&truncatedDiff, diffStats, json.RawMessage(`[]`), true, true, int64(sessionDiffMaxChars+4096), int64(sessionDiffHistoryMaxBytes+4096), int64(sessionDiffMaxChars), int64(sessionDiffHistoryMaxBytes)),
		)

	payload, err := store.GetDiffByID(context.Background(), orgID, sessionID)
	require.NoError(t, err, "GetDiffByID should not fail for an oversized diff")
	require.Equal(t, &truncatedDiff, payload.Diff, "GetDiffByID should return the bounded diff prefix")
	require.True(t, payload.DiffTruncated, "GetDiffByID should report when the raw diff was capped")
	require.True(t, payload.DiffHistoryTruncated, "GetDiffByID should report when diff history was omitted")
	require.Equal(t, int64(sessionDiffMaxChars+4096), payload.DiffChars, "GetDiffByID should preserve original diff size metadata")
	require.Equal(t, int64(sessionDiffHistoryMaxBytes), payload.DiffHistoryMaxBytes, "GetDiffByID should return the configured history cap")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_QueryColumnsStayInSyncWithSessionModel(t *testing.T) {
	t.Parallel()

	requiredColumns := []string{
		"base_commit_sha",
		"diff_collected_at",
		"latest_diff_snapshot_id",
		"has_unpushed_changes",
		"origin",
		"interaction_mode",
		"validation_policy",
		// Migration 102 — Linear session linking. Locked here so a future
		// migration that drops a column from the SELECT lists (or this
		// test fixture) trips immediately rather than silently corrupting
		// pgx.RowToStructByName[models.Session].
		"linear_private",
		"linear_state_sync_disabled",
		"linear_identifier_hint",
		"linear_prepare_state",
	}

	for _, tt := range []struct {
		name    string
		columns string
	}{
		{name: "select columns", columns: sessionSelectColumns},
		{name: "list columns", columns: sessionListColumns},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for _, column := range requiredColumns {
				require.True(
					t,
					strings.Contains(tt.columns, column),
					"query column list should include %s so RowToStructByName[models.Session] remains aligned",
					column,
				)
			}
		})
	}
}

func TestSessionStore_ResumeRuntimeResetCoversAllRuntimeColumns(t *testing.T) {
	t.Parallel()

	for _, column := range sessionTestColumns {
		if !strings.HasPrefix(column, "runtime_") {
			continue
		}
		require.Contains(
			t,
			sessionResumeRuntimeResetAssignments,
			column+" =",
			"ClaimForResume should reset %s so stale runtime-control fields cannot survive a resume claim",
			column,
		)
	}
}

// TestSessionStore_InsertColumnsExcludePrimaryIssueID guards against a phantom-
// column regression: models.Session has `db:"primary_issue_id"` because the
// SELECT subquery aliases the value, but no such column exists on the table.
// If a future refactor lists primary_issue_id (or the legacy issue_id) in the
// INSERT column list, postgres will fail at runtime. This test fails earlier,
// at unit-test time, so the breakage is obvious in CI.
func TestSessionStore_InsertColumnsExcludePrimaryIssueID(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("session_store.go")
	require.NoError(t, err, "should be able to read session_store.go")

	src := string(source)

	// Locate every `INSERT INTO sessions (` block (there is one today, but be
	// defensive in case future writes add more) and assert neither column
	// appears in the column list of any of them. We bound the scan to the
	// block's closing paren so we don't accidentally match later code.
	const marker = "INSERT INTO sessions ("
	for idx := 0; ; {
		hit := strings.Index(src[idx:], marker)
		if hit < 0 {
			break
		}
		start := idx + hit
		end := strings.Index(src[start:], ")")
		require.Greater(t, end, 0, "INSERT INTO sessions block at offset %d must have a closing paren", start)
		columnList := src[start : start+end]

		require.NotContains(t, columnList, "primary_issue_id", "INSERT INTO sessions must not list primary_issue_id — it is a SELECT alias only, not a real column")
		require.NotContains(t, columnList, "issue_id", "INSERT INTO sessions must not list issue_id — the column was dropped in migration 000097")

		idx = start + end
	}
}

func TestSessionStore_UpdatePRCreationState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		state     models.PRCreationState
		errMsg    string
		expectErr bool
	}{
		{
			name:   "failed state stores error message",
			state:  models.PRCreationStateFailed,
			errMsg: "push failed",
		},
		{
			name:  "queued state clears error message",
			state: models.PRCreationStateQueued,
		},
		{
			name:      "invalid state returns validation error",
			state:     models.PRCreationState("bogus"),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			store.SetLogger(zerolog.Nop())
			store.SetStreams(nil)
			orgID := uuid.New()
			sessionID := uuid.New()
			now := time.Now()

			if !tt.expectErr {
				mock.ExpectQuery("UPDATE sessions[\\s\\S]*pr_creation_state[\\s\\S]*RETURNING").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(newAgentSessionRow(sessionID, uuid.New(), orgID, now)...))
			}

			err = store.UpdatePRCreationState(context.Background(), orgID, sessionID, tt.state, tt.errMsg)
			if tt.expectErr {
				require.Error(t, err, "UpdatePRCreationState should reject invalid enum values")
				require.Contains(t, err.Error(), "invalid PRCreationState", "UpdatePRCreationState should surface enum validation failures")
				return
			}

			require.NoError(t, err, "UpdatePRCreationState should persist valid state transitions")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_UpdatePRCreationState_PublishesSessionStatus(t *testing.T) {
	t.Parallel()

	// The frontend session-detail page now relies on the session status SSE
	// (Redis stream `143:stream:{ses:ID}:status`) to observe pr_creation_state
	// transitions in lieu of a 2s poll on /sessions/{id}/pr. This test locks
	// in the publishStatus call by reading the miniredis stream after the
	// transition and asserting the entry exists.
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	store.SetLogger(zerolog.Nop())

	mr := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "Redis client should initialize")
	defer client.Close()
	streams := cache.NewSessionStreams(client, zerolog.Nop(), nil)
	require.NotNil(t, streams, "session streams helper should initialize")
	store.SetStreams(streams)

	now := time.Now()
	sessionID := uuid.New()
	issueID := uuid.New()
	orgID := uuid.New()

	mock.ExpectQuery("UPDATE sessions[\\s\\S]*pr_creation_state[\\s\\S]*RETURNING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(newAgentSessionRow(sessionID, issueID, orgID, now)...))

	require.NoError(t, store.UpdatePRCreationState(context.Background(), orgID, sessionID, models.PRCreationStateSucceeded, ""), "UpdatePRCreationState should succeed")

	streamKey := "143:stream:{ses:" + sessionID.String() + "}:status"
	entries, err := mr.Stream(streamKey)
	require.NoError(t, err, "miniredis should know about the status stream key after publish")
	require.Len(t, entries, 1, "status stream should contain exactly one entry from the published transition")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdatePRCreationState_NoMatchingRowIsNoOp(t *testing.T) {
	t.Parallel()

	// Pre-existing semantics: the "AND pr_creation_state <> 'succeeded'" guard
	// makes the terminal state sticky. When the WHERE clause filters the row
	// out (already-succeeded session), the call must succeed without error and
	// without publishing a stale status — preserving the prior Exec-based
	// no-op behavior even after switching to RETURNING.
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	store.SetLogger(zerolog.Nop())
	store.SetStreams(nil)
	orgID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectQuery("UPDATE sessions[\\s\\S]*pr_creation_state[\\s\\S]*RETURNING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns))

	err = store.UpdatePRCreationState(context.Background(), orgID, sessionID, models.PRCreationStateQueued, "")
	require.NoError(t, err, "no-rows-affected should not surface as an error so callers stay idempotent")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_TryMarkPRPushQueued(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		returnRow  bool
		wantQueued bool
	}{
		{
			name:       "row returned signals successful CAS",
			returnRow:  true,
			wantQueued: true,
		},
		{
			name:       "no row returned signals concurrent winner",
			returnRow:  false,
			wantQueued: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			store.SetLogger(zerolog.Nop())
			store.SetStreams(nil)
			orgID := uuid.New()
			sessionID := uuid.New()
			now := time.Now()

			// CAS update should pass exactly two args (id, org_id) and
			// guard with a NOT IN ('queued','pushing') predicate.
			expect := mock.ExpectQuery("UPDATE sessions[\\s\\S]*pr_push_state = 'queued'[\\s\\S]*pr_push_state NOT IN[\\s\\S]*RETURNING").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg())
			if tt.returnRow {
				expect.WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(newAgentSessionRow(sessionID, uuid.New(), orgID, now)...))
			} else {
				expect.WillReturnRows(pgxmock.NewRows(sessionTestColumns))
			}

			queued, err := store.TryMarkPRPushQueued(context.Background(), orgID, sessionID)
			require.NoError(t, err, "TryMarkPRPushQueued should not error on a successful CAS attempt")
			require.Equal(t, tt.wantQueued, queued, "TryMarkPRPushQueued should report whether the row transitioned")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_TryMarkPRCreationQueued(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		returnRow  bool
		wantQueued bool
	}{
		{
			name:       "row returned signals successful CAS",
			returnRow:  true,
			wantQueued: true,
		},
		{
			name:       "no row returned signals concurrent winner or terminal state",
			returnRow:  false,
			wantQueued: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			store.SetLogger(zerolog.Nop())
			store.SetStreams(nil)
			orgID := uuid.New()
			sessionID := uuid.New()
			now := time.Now()

			expect := mock.ExpectQuery("UPDATE sessions[\\s\\S]*pr_creation_state = 'queued'[\\s\\S]*pr_creation_state NOT IN[\\s\\S]*RETURNING").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg())
			if tt.returnRow {
				expect.WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(newAgentSessionRow(sessionID, uuid.New(), orgID, now)...))
			} else {
				expect.WillReturnRows(pgxmock.NewRows(sessionTestColumns))
			}

			queued, err := store.TryMarkPRCreationQueued(context.Background(), orgID, sessionID)
			require.NoError(t, err, "TryMarkPRCreationQueued should not error on a successful CAS attempt")
			require.Equal(t, tt.wantQueued, queued, "TryMarkPRCreationQueued should report whether the row transitioned")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_ClearSnapshotKey(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.ClearSnapshotKey(context.Background(), orgID, sessionID)
	require.NoError(t, err, "ClearSnapshotKey should update the row without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_SetPendingSnapshotKey(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()

	// Set must stamp pending_snapshot_set_at = NOW() in the same UPDATE so
	// the stranded-pending reaper has a timestamp to match against. Pin the
	// regex so a regression that drops the set_at write is caught.
	mock.ExpectExec("UPDATE sessions[\\s\\S]*pending_snapshot_key = @key[\\s\\S]*pending_snapshot_set_at = NOW\\(\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.SetPendingSnapshotKey(context.Background(), orgID, sessionID, "snapshots/org/session/post-pr.tar.zst")
	require.NoError(t, err, "SetPendingSnapshotKey should not error on a clean update")
	require.NoError(t, mock.ExpectationsWereMet(), "expectations should be met")
}

func TestSessionStore_PromotePendingSnapshot(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	expected := "snapshots/org/session/post-pr.tar.zst"

	// Promote should match on pending_snapshot_key = @expected_key — that's
	// the optimistic-concurrency guard against a stale upload clobbering a
	// newer one. Verify the named arg is in the SQL.
	// Promote must clear both pending_snapshot_key AND pending_snapshot_set_at
	// in the same statement — otherwise the reaper would see a phantom
	// timestamp on a row whose pending key has already been promoted.
	mock.ExpectQuery("UPDATE sessions[\\s\\S]*snapshot_key = pending_snapshot_key[\\s\\S]*pending_snapshot_key = NULL[\\s\\S]*pending_snapshot_set_at = NULL[\\s\\S]*workspace_revision = workspace_revision \\+ 1[\\s\\S]*workspace_revision_updated_at = NOW\\(\\)[\\s\\S]*pending_snapshot_key = @expected_key[\\s\\S]*RETURNING workspace_revision, workspace_revision_updated_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), expected).
		WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow(int64(4), time.Now()))

	err = store.PromotePendingSnapshot(context.Background(), orgID, sessionID, expected)
	require.NoError(t, err, "PromotePendingSnapshot should not error on a clean update")
	require.NoError(t, mock.ExpectationsWereMet(), "expectations should be met")
}

func TestSessionStore_PublishCheckpoint_BumpsWorkspaceRevisionForSnapshot(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	checkpointedAt := time.Now().UTC()

	mock.ExpectQuery("UPDATE sessions[\\s\\S]*snapshot_key = CASE[\\s\\S]*workspace_revision = CASE[\\s\\S]*@snapshot_key = '' THEN workspace_revision[\\s\\S]*ELSE workspace_revision \\+ 1[\\s\\S]*workspace_revision_updated_at = CASE[\\s\\S]*@snapshot_key = '' THEN workspace_revision_updated_at[\\s\\S]*ELSE @checkpointed_at[\\s\\S]*RETURNING workspace_revision, workspace_revision_updated_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow(int64(3), checkpointedAt))

	published, err := store.PublishCheckpoint(
		context.Background(),
		orgID,
		sessionID,
		uuid.Nil,
		"agent-session-1",
		"snapshots/org/session/checkpoint.tar.zst",
		models.CheckpointKindTurnComplete,
		models.CheckpointCapabilityFullResume,
		1024,
		checkpointedAt,
		nil,
		models.RuntimeStopReasonNone,
	)
	require.NoError(t, err, "PublishCheckpoint should update checkpoint metadata")
	require.True(t, published, "PublishCheckpoint should report that the checkpoint row was updated")
	require.NoError(t, mock.ExpectationsWereMet(), "expectations should be met")
}

func TestSessionStore_BumpWorkspaceRevision(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	updatedAt := time.Now().UTC()

	mock.ExpectQuery("UPDATE sessions[\\s\\S]*workspace_revision = workspace_revision \\+ 1[\\s\\S]*workspace_revision_updated_at = @updated_at[\\s\\S]*RETURNING workspace_revision, workspace_revision_updated_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).
				AddRow(int64(9), updatedAt),
		)

	revision, gotUpdatedAt, err := store.BumpWorkspaceRevision(context.Background(), orgID, sessionID, "test")
	require.NoError(t, err, "BumpWorkspaceRevision should update and return the new revision")
	require.Equal(t, int64(9), revision, "BumpWorkspaceRevision should return the incremented revision")
	require.Equal(t, updatedAt, gotUpdatedAt, "BumpWorkspaceRevision should return the revision timestamp")
	require.NoError(t, mock.ExpectationsWereMet(), "expectations should be met")
}

func TestSessionStore_ClearPendingSnapshot(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()

	// Clear must NULL both pending_snapshot_key AND pending_snapshot_set_at
	// in lockstep, same reasoning as Promote.
	mock.ExpectExec("UPDATE sessions[\\s\\S]*pending_snapshot_key = NULL[\\s\\S]*pending_snapshot_set_at = NULL").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.ClearPendingSnapshot(context.Background(), orgID, sessionID)
	require.NoError(t, err, "ClearPendingSnapshot should not error")
	require.NoError(t, mock.ExpectationsWereMet(), "expectations should be met")
}

func TestSessionStore_ReapStrandedPendingSnapshots(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	threshold := time.Now().Add(-15 * time.Minute)

	// Reaper must scope on both columns being non-NULL AND on the timestamp
	// guard; without the guard, a fresh upload that just stamped set_at would
	// be reaped out from under itself.
	mock.ExpectExec("UPDATE sessions[\\s\\S]*pending_snapshot_key = NULL[\\s\\S]*pending_snapshot_set_at = NULL[\\s\\S]*pending_snapshot_key IS NOT NULL[\\s\\S]*pending_snapshot_set_at IS NOT NULL[\\s\\S]*pending_snapshot_set_at <= @older_than").
		WithArgs(threshold).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	cleared, err := store.ReapStrandedPendingSnapshots(context.Background(), threshold)
	require.NoError(t, err, "ReapStrandedPendingSnapshots should not error on a clean update")
	require.Equal(t, int64(3), cleared, "ReapStrandedPendingSnapshots should return the rows-affected count")
	require.NoError(t, mock.ExpectationsWereMet(), "expectations should be met")
}

// TestSessionStore_ListByOrg_MRUOrdering verifies the list query orders by
// last_activity_at (Most-Recently-Updated), not created_at. Regressions here
// would flip the Sessions page back to creation-time ordering, which is the
// wrong default for a team product with long-lived async agents.
func TestSessionStore_ListByOrg_MRUOrdering(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	// Match ORDER BY last_activity_at DESC, id DESC explicitly so a regression
	// to ORDER BY created_at would fail the expectation.
	mock.ExpectQuery(`ORDER BY last_activity_at DESC, id DESC`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).
				AddRow(newAgentSessionRow(sessionID, issueID, orgID, now)...),
		)

	sessions, err := store.ListByOrg(context.Background(), orgID, SessionFilters{})
	require.NoError(t, err, "ListByOrg should not return an error")
	require.Len(t, sessions, 1, "should return one session")
	require.NoError(t, mock.ExpectationsWereMet(), "ORDER BY last_activity_at must be present in the list query")
}

// TestSessionStore_ListByOrg_CursorUsesLastActivity verifies the cursor predicate
// compares (last_activity_at, id) — matching the MRU ordering. If the predicate
// drifts back to created_at the cursor will skip / repeat rows in pagination.
func TestSessionStore_ListByOrg_CursorUsesLastActivity(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	cursorTime := time.Now().Add(-time.Hour)
	cursorID := uuid.New()

	mock.ExpectQuery(`\(last_activity_at, id\) < \(@cursor_time, @cursor_id\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns))

	_, err = store.ListByOrg(context.Background(), orgID, SessionFilters{
		CursorTime: &cursorTime,
		CursorID:   &cursorID,
	})
	require.NoError(t, err, "ListByOrg with cursor should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "cursor predicate must reference last_activity_at")
}

func TestSessionStore_ListByOrg_WithSearch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ title ILIKE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).
				AddRow(newAgentSessionRow(sessionID, issueID, orgID, now)...),
		)

	sessions, err := store.ListByOrg(context.Background(), orgID, SessionFilters{
		Search: "fix bug",
	})
	require.NoError(t, err, "ListByOrg with Search should not return an error")
	require.Len(t, sessions, 1, "should return one session")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_CountsByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()

	// Args: org_id, cap, active_statuses.
	mock.ExpectQuery("(?s)SELECT.*all_count.*active_count.*archived_count").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"all_count", "active_count", "archived_count"}).
				AddRow(17, 5, 2),
		)

	counts, err := store.CountsByOrg(context.Background(), orgID, SessionCountsFilters{})
	require.NoError(t, err, "CountsByOrg should not return an error")
	require.Equal(t, 17, counts.All, "all count should pass through")
	require.Equal(t, 5, counts.Active, "active count should pass through")
	require.Equal(t, 2, counts.Archived, "archived count should pass through")
	require.Greater(t, counts.Cap, 0, "cap should be set on the response")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_CountsByOrg_WithScopeFilters(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()

	// Args: org_id, cap, active_statuses, repository_id, triggered_by_user_id.
	mock.ExpectQuery("(?s)SELECT.*repository_id.*triggered_by_user_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"all_count", "active_count", "archived_count"}).
				AddRow(3, 1, 0),
		)

	counts, err := store.CountsByOrg(context.Background(), orgID, SessionCountsFilters{
		RepositoryID:      repoID,
		TriggeredByUserID: userID,
	})
	require.NoError(t, err, "CountsByOrg with scope filters should not return an error")
	require.Equal(t, 3, counts.All)
	require.Equal(t, 1, counts.Active)
	require.Equal(t, 0, counts.Archived)
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_CountsByOrg_WithTriggeredByUserIDs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	userID1 := uuid.New()
	userID2 := uuid.New()

	mock.ExpectQuery(`(?s)SELECT.*triggered_by_user_id = ANY`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"all_count", "active_count", "archived_count"}).
				AddRow(4, 2, 1),
		)

	counts, err := store.CountsByOrg(context.Background(), orgID, SessionCountsFilters{
		TriggeredByUserIDs: []uuid.UUID{userID1, userID2},
	})
	require.NoError(t, err, "CountsByOrg with TriggeredByUserIDs should not return an error")
	require.Equal(t, 4, counts.All, "all count should reflect the scoped users")
	require.Equal(t, 2, counts.Active, "active count should reflect the scoped users")
	require.Equal(t, 1, counts.Archived, "archived count should reflect the scoped users")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateTitle(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectExec("UPDATE sessions SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateTitle(context.Background(), orgID, sessionID, "Fix auth flow")
	require.NoError(t, err, "UpdateTitle should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_CountRunningByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()

	mock.ExpectQuery("SELECT count\\(\\*\\) FROM sessions WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))

	count, err := store.CountRunningByOrg(context.Background(), orgID)
	require.NoError(t, err)
	require.Equal(t, 3, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_ListByIDs_Empty(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()

	sessions, err := store.ListByIDs(context.Background(), orgID, []uuid.UUID{})
	require.NoError(t, err)
	require.Nil(t, sessions)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_ListByIDs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ id = ANY").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).
				AddRow(newAgentSessionRow(sessionID, issueID, orgID, now)...),
		)

	sessions, err := store.ListByIDs(context.Background(), orgID, []uuid.UUID{sessionID})
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, sessionID, sessions[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_UpdateResult(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()

	result := &models.SessionResult{
		TokenUsage:    json.RawMessage(`{"input": 100}`),
		ResultSummary: stringPtr("Fixed the bug"),
	}

	now := time.Now()
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(anyDBArgs(11)...).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).AddRow(
				newAgentSessionRow(sessionID, uuid.New(), orgID, now)...,
			),
		)

	err = store.UpdateResult(context.Background(), orgID, sessionID, models.SessionStatusCompleted, result)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_UpdateResult_PersistsModelUsed(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	modelUsed := "gpt-5.4"
	now := time.Now()

	mock.ExpectQuery(`UPDATE sessions[\s\S]+model_used = COALESCE\(@model_used, model_used\)`).
		WithArgs(anyDBArgs(11)...).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).AddRow(
				newAgentSessionRow(sessionID, uuid.New(), orgID, now)...,
			),
		)

	err = store.UpdateResult(context.Background(), orgID, sessionID, models.SessionStatusCompleted, &models.SessionResult{
		ModelUsed: &modelUsed,
	})
	require.NoError(t, err, "UpdateResult should persist model_used when provided")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateResultClearsStaleFailureDetails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery(`UPDATE sessions[\s\S]+failure_explanation = NULL[\s\S]+failure_category = NULL[\s\S]+failure_next_steps = NULL[\s\S]+failure_retry_advised = false`).
		WithArgs(anyDBArgs(11)...).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).AddRow(
				newAgentSessionRow(sessionID, uuid.New(), orgID, now)...,
			),
		)

	err = store.UpdateResult(context.Background(), orgID, sessionID, models.SessionStatusCompleted, &models.SessionResult{
		ResultSummary: stringPtr("new successful result"),
	})
	require.NoError(t, err, "UpdateResult should clear stale failure details while persisting a new result")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateStatus_PublishesAndQueriesTerminalCleanup(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	store.SetLogger(zerolog.Nop())
	store.SetStreams(nil)

	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("UPDATE sessions SET status = @status, completed_at = now\\(\\), error = NULL, failure_explanation = NULL, failure_category = NULL, failure_next_steps = NULL, failure_retry_advised = false, last_activity_at = now\\(\\) WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL RETURNING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(newAgentSessionRow(sessionID, issueID, orgID, now)...))

	require.NoError(t, store.UpdateStatus(context.Background(), orgID, sessionID, models.SessionStatusCompleted), "UpdateStatus should succeed for terminal transitions")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), 10).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(newAgentSessionRow(sessionID, issueID, orgID, now)...))

	rows, err := store.ListTerminalEndedBefore(context.Background(), now, 10)
	require.NoError(t, err, "ListTerminalEndedBefore should succeed")
	require.Len(t, rows, 1, "ListTerminalEndedBefore should return the matching session")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateStatusTerminalClearsStaleFailureDetails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status models.SessionStatus
	}{
		{name: "completed clears stale failure details", status: models.SessionStatusCompleted},
		{name: "pr created clears stale failure details", status: models.SessionStatusPRCreated},
		{name: "skipped clears stale failure details", status: models.SessionStatusSkipped},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			store.SetLogger(zerolog.Nop())
			store.SetStreams(nil)

			orgID := uuid.New()
			sessionID := uuid.New()
			issueID := uuid.New()
			now := time.Now()

			mock.ExpectQuery(`UPDATE sessions SET status = @status, completed_at = now\(\), error = NULL, failure_explanation = NULL, failure_category = NULL, failure_next_steps = NULL, failure_retry_advised = false, last_activity_at = now\(\) WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL RETURNING`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(newAgentSessionRow(sessionID, issueID, orgID, now)...))

			err = store.UpdateStatus(context.Background(), orgID, sessionID, tt.status)
			require.NoError(t, err, "UpdateStatus should clear stale failure details for successful terminal status")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_SetRepositoryContextScopesToOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	store.SetLogger(zerolog.Nop())
	store.SetStreams(nil)

	orgID := uuid.New()
	sessionID := uuid.New()
	repoID := uuid.New()
	now := time.Now()
	branch := "main"
	row := newAgentSessionRow(sessionID, uuid.Nil, orgID, now)
	setSessionTestColumnValue(row, "repository_id", &repoID)
	setSessionTestColumnValue(row, "target_branch", &branch)

	mock.ExpectQuery(`UPDATE sessions[\s\S]+WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL[\s\S]+RETURNING`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(row...))

	got, err := store.SetRepositoryContext(context.Background(), orgID, sessionID, repoID, &branch)
	require.NoError(t, err, "SetRepositoryContext should update the session repository inside the org")
	require.NotNil(t, got.RepositoryID, "SetRepositoryContext should return the selected repository")
	require.Equal(t, repoID, *got.RepositoryID, "SetRepositoryContext should persist the selected repository")
	require.NotNil(t, got.TargetBranch, "SetRepositoryContext should return the selected branch")
	require.Equal(t, branch, *got.TargetBranch, "SetRepositoryContext should persist the selected branch")
	require.NoError(t, mock.ExpectationsWereMet(), "repository context update should be scoped by org_id")
}

func TestSessionStore_UpdateInputManifestScopesToOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	store.SetLogger(zerolog.Nop())
	store.SetStreams(nil)

	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()
	inputManifest := json.RawMessage(`{"slack":{"routing_mode":"answer_only"}}`)
	row := newAgentSessionRow(sessionID, uuid.Nil, orgID, now)
	setSessionTestColumnValue(row, "input_manifest", inputManifest)

	mock.ExpectQuery(`UPDATE sessions[\s\S]+SET input_manifest = @input_manifest[\s\S]+WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL[\s\S]+RETURNING`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(row...))

	got, err := store.UpdateInputManifest(context.Background(), orgID, sessionID, inputManifest)
	require.NoError(t, err, "UpdateInputManifest should update the session input manifest inside the org")
	require.JSONEq(t, string(inputManifest), string(got.InputManifest), "UpdateInputManifest should return the persisted manifest")
	require.NoError(t, mock.ExpectationsWereMet(), "input manifest update should be scoped by org_id")
}

func TestSessionStore_SettersAndUpdateStatusError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	store.SetLogger(zerolog.Nop())
	store.SetStreams(nil)

	mock.ExpectQuery(`UPDATE sessions SET status = @status, started_at = now\(\), completed_at = NULL, error = NULL, failure_explanation = NULL, failure_category = NULL, failure_next_steps = NULL, failure_retry_advised = false, runtime_soft_deadline_at = NULL, runtime_hard_deadline_at = NULL, runtime_last_progress_at = NULL, runtime_last_progress_type = '', runtime_last_progress_strength = '', runtime_stop_reason = '', runtime_graceful_stop_at = NULL, last_activity_at = now\(\) WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL RETURNING`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.DeadlineExceeded)

	err = store.UpdateStatus(context.Background(), uuid.New(), uuid.New(), models.SessionStatusRunning)
	require.Error(t, err, "UpdateStatus should surface query failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateStatusRunningClearsRuntimeControlFields(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	store.SetLogger(zerolog.Nop())
	store.SetStreams(nil)

	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock.ExpectQuery(`runtime_soft_deadline_at = NULL, runtime_hard_deadline_at = NULL, runtime_last_progress_at = NULL, runtime_last_progress_type = '', runtime_last_progress_strength = '', runtime_stop_reason = '', runtime_graceful_stop_at = NULL`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(newAgentSessionRow(sessionID, issueID, orgID, now)...))

	err = store.UpdateStatus(context.Background(), orgID, sessionID, models.SessionStatusRunning)

	require.NoError(t, err, "UpdateStatus should transition the session to running")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_MarkRunningWithSandboxStateClearsRuntimeControlFields(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	store.SetLogger(zerolog.Nop())
	store.SetStreams(nil)

	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock.ExpectQuery(`runtime_soft_deadline_at = NULL[\s\S]+runtime_hard_deadline_at = NULL[\s\S]+runtime_last_progress_at = NULL[\s\S]+runtime_last_progress_type = ''[\s\S]+runtime_last_progress_strength = ''[\s\S]+runtime_stop_reason = ''[\s\S]+runtime_graceful_stop_at = NULL`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(newAgentSessionRow(sessionID, issueID, orgID, now)...))

	err = store.MarkRunningWithSandboxState(context.Background(), orgID, sessionID, models.SandboxStateRunning)

	require.NoError(t, err, "MarkRunningWithSandboxState should clear stale runtime control fields")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateStatus_CollectError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	store.SetLogger(zerolog.Nop())
	store.SetStreams(nil)

	mock.ExpectQuery("UPDATE sessions SET status = @status, completed_at = now\\(\\), error = NULL, failure_explanation = NULL, failure_category = NULL, failure_next_steps = NULL, failure_retry_advised = false, last_activity_at = now\\(\\) WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL RETURNING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	err = store.UpdateStatus(context.Background(), uuid.New(), uuid.New(), models.SessionStatusCompleted)
	require.Error(t, err, "UpdateStatus should surface row collection failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateResult_ErrorBranches(t *testing.T) {
	t.Parallel()

	t.Run("query error", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectQuery("UPDATE sessions").
			WithArgs(anyDBArgs(11)...).
			WillReturnError(context.DeadlineExceeded)

		err = store.UpdateResult(context.Background(), uuid.New(), uuid.New(), models.SessionStatusCompleted, &models.SessionResult{})
		require.Error(t, err, "UpdateResult should surface query failures")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("collect error and best-effort publish failure", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewSessionStore(mock)
		store.SetLogger(zerolog.Nop())

		mr := miniredis.RunT(t)
		client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
		require.NotNil(t, client, "Redis client should initialize")
		store.SetStreams(cache.NewSessionStreams(client, zerolog.Nop(), nil))
		mr.Close()

		now := time.Now()
		sessionID := uuid.New()
		issueID := uuid.New()
		orgID := uuid.New()

		mock.ExpectQuery("UPDATE sessions SET status = @status, completed_at = now\\(\\), error = NULL, failure_explanation = NULL, failure_category = NULL, failure_next_steps = NULL, failure_retry_advised = false, last_activity_at = now\\(\\) WHERE id = @id AND org_id = @org_id AND deleted_at IS NULL RETURNING").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(sessionTestColumns).AddRow(newAgentSessionRow(sessionID, issueID, orgID, now)...),
			)

		require.NoError(t, store.UpdateStatus(context.Background(), orgID, sessionID, models.SessionStatusCompleted), "UpdateStatus should tolerate best-effort Redis publish failures")

		mock.ExpectQuery("UPDATE sessions").
			WithArgs(anyDBArgs(11)...).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(sessionID))

		err = store.UpdateResult(context.Background(), orgID, sessionID, models.SessionStatusCompleted, &models.SessionResult{})
		require.Error(t, err, "UpdateResult should surface row collection failures")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func TestSessionStore_ListTerminalEndedBefore_Error(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), 10).
		WillReturnError(context.DeadlineExceeded)

	rows, err := store.ListTerminalEndedBefore(context.Background(), time.Now(), 10)
	require.Error(t, err, "ListTerminalEndedBefore should surface query failures")
	require.Nil(t, rows, "query failures should not return rows")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_ClaimIdle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		wantErr   bool
	}{
		{
			name: "claims idle session successfully",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionTestColumns).AddRow(
							newAgentSessionRow(uuid.New(), uuid.New(), uuid.New(), now)...,
						),
					)
			},
			wantErr: false,
		},
		{
			name: "returns error when no matching row",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionTestColumns))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewSessionStore(mock)
			tt.setupMock(mock)

			_, err = store.ClaimIdle(context.Background(), uuid.New(), uuid.New())
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSessionStore_ClaimForResume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		wantErr   bool
	}{
		{
			name: "resumes completed session successfully",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				mock.ExpectQuery(claimForResumeQueryPattern()).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionTestColumns).AddRow(
							newAgentSessionRow(uuid.New(), uuid.New(), uuid.New(), now)...,
						),
					)
			},
			wantErr: false,
		},
		{
			name: "returns error when no matching row",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery(claimForResumeQueryPattern()).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionTestColumns))
			},
			wantErr: true,
		},
		{
			name: "resumes awaiting input session successfully",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				row := newAgentSessionRow(uuid.New(), uuid.New(), uuid.New(), now)
				row[4] = models.SessionStatusRunning
				mock.ExpectQuery(claimForResumeQueryPattern()).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionTestColumns).AddRow(row...),
					)
			},
			wantErr: false,
		},
		{
			name: "resumes needs human guidance session successfully",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				row := newAgentSessionRow(uuid.New(), uuid.New(), uuid.New(), now)
				row[4] = models.SessionStatusRunning
				mock.ExpectQuery(claimForResumeQueryPattern()).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionTestColumns).AddRow(row...),
					)
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgx mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			tt.setupMock(mock)

			_, err = store.ClaimForResume(context.Background(), uuid.New(), uuid.New())
			if tt.wantErr {
				require.Error(t, err, "ClaimForResume should return an error when no session can be resumed")
			} else {
				require.NoError(t, err, "ClaimForResume should resume the session")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all session resume expectations should be met")
		})
	}
}

type noTxDB struct{}

func (noTxDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (noTxDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unused")
}
func (noTxDB) QueryRow(context.Context, string, ...any) pgx.Row       { return nil }
func (noTxDB) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }

func TestSessionStore_Begin(t *testing.T) {
	t.Parallel()

	t.Run("starts transaction when supported", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectBegin()
		mock.ExpectRollback()
		tx, err := NewSessionStore(mock).Begin(context.Background())
		require.NoError(t, err)
		require.NotNil(t, tx)
		require.NoError(t, tx.Rollback(context.Background()))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error when transactions unsupported", func(t *testing.T) {
		t.Parallel()
		_, err := NewSessionStore(noTxDB{}).Begin(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not support transactions")
	})
}

func TestSessionStore_UpdatePMPlanID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	// Pinned (intentional tripwire — exact-match regex): UpdatePMPlanID must
	// bump last_activity_at so the method is self-contained. Today's sole
	// caller also calls UpdateResult immediately before this, so the bump is
	// technically redundant on the hot path, but removing it couples
	// correctness to an unwritten caller contract.
	mock.ExpectExec(`^UPDATE sessions SET pm_plan_id = @pm_plan_id, last_activity_at = now\(\) WHERE id = @id AND org_id = @org_id$`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdatePMPlanID(context.Background(), uuid.New(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet(), "UpdatePMPlanID must bump last_activity_at")
}

func TestSessionStore_ResetForRetry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectQuery(`SELECT status FROM sessions WHERE id = @id AND org_id = @org_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("failed"))

	// Pinned: ResetForRetry must bump last_activity_at so the retry surfaces
	// to the top of the MRU-ordered sessions list.
	mock.ExpectExec(`(?s)UPDATE sessions.+SET status = 'pending'.+last_activity_at = now\(\).+WHERE id = @id AND org_id = @org_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.ResetForRetry(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet(), "ResetForRetry must bump last_activity_at")
}

func TestSessionStore_ResetForRetry_NotFailed(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectQuery(`SELECT status FROM sessions WHERE id = @id AND org_id = @org_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("running"))

	err = store.ResetForRetry(context.Background(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, ErrSessionNotFailed)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_UndoResetForRetry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	// Pinned: UndoResetForRetry must bump last_activity_at so the session
	// reverts to visible-as-just-updated after a retry enqueue failure.
	mock.ExpectExec(`(?s)UPDATE sessions.+SET status = 'failed'.+last_activity_at = now\(\).+WHERE id = @id AND org_id = @org_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UndoResetForRetry(context.Background(), uuid.New(), uuid.New(), "retry failed", "transient")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet(), "UndoResetForRetry must bump last_activity_at")
}

func TestSessionStore_SoftDelete(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectExec("UPDATE sessions SET deleted_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("WITH target_runs AS").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = store.SoftDelete(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err, "SoftDelete should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_SoftDelete_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectExec("UPDATE sessions SET deleted_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = store.SoftDelete(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "SoftDelete should return an error when session not found")
	require.Contains(t, err.Error(), "not found or already deleted")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func stringPtr(s string) *string { return &s }

// =============================================================================
// Additional session store tests for coverage
// =============================================================================

func TestSessionStore_UpdateFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectExec("UPDATE sessions.+SET failure_explanation").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateFailure(context.Background(), uuid.New(), uuid.New(), "test failure", "runtime", []string{"retry"}, true)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_UpdateRevisionContext(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectExec("UPDATE sessions.+SET revision_context").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateRevisionContext(context.Background(), uuid.New(), uuid.New(), []byte(`{"repair_action":"fix_tests"}`))
	require.NoError(t, err, "UpdateRevisionContext should persist the revision context")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateSnapshotInfo(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	// Pinned: UpdateSnapshotInfo must NOT write last_activity_at. The
	// orchestrator calls UpdateResult (which bumps it) immediately before
	// this; a second bump here would be a redundant write on every snapshot.
	mock.ExpectExec(`UPDATE sessions\s+SET agent_session_id = @agent_session_id, snapshot_key = @snapshot_key,\s+sandbox_state = 'snapshotted'\s+WHERE id = @id AND org_id = @org_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateSnapshotInfo(context.Background(), uuid.New(), uuid.New(), "agent-123", "snap-key")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet(), "UpdateSnapshotInfo must not write last_activity_at")
}

func TestSessionStore_SetGitIdentity(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	userID := uuid.New()

	mock.ExpectExec(`UPDATE sessions\s+SET git_identity_source = @source,\s+git_identity_user_id = @user_id\s+WHERE id = @id AND org_id = @org_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.SetGitIdentity(context.Background(), uuid.New(), uuid.New(), "user", &userID)
	require.NoError(t, err, "SetGitIdentity should persist the resolved git identity metadata")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateSandboxState(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	// Pinned: UpdateSandboxState must NOT touch last_activity_at. The reaper
	// uses this to mark long-completed sessions as 'destroyed' during snapshot
	// cleanup; bumping the MRU timestamp there would resurface dormant
	// sessions at the top of the Sessions page.
	mock.ExpectExec(`^UPDATE sessions SET sandbox_state = @sandbox_state WHERE id = @id AND org_id = @org_id$`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateSandboxState(context.Background(), uuid.New(), uuid.New(), "snapshotted")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet(), "UpdateSandboxState must not write last_activity_at")
}

func TestSessionStore_UpdateWorkingBranch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectExec("UPDATE sessions SET working_branch").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateWorkingBranch(context.Background(), uuid.New(), uuid.New(), "feature/my-branch")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_UpdateTurnComplete(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	result := &models.SessionResult{
		TokenUsage:    json.RawMessage(`{"input":100,"output":200}`),
		ResultSummary: stringPtr("task done"),
		Diff:          stringPtr("diff content"),
	}

	mock.ExpectExec("UPDATE sessions.+SET status = 'idle'.+pr_creation_state = 'idle', pr_creation_error = NULL").
		WithArgs(anyDBArgs(13)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateTurnComplete(context.Background(), uuid.New(), uuid.New(), 2, result, "agent-123", "snap-key")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_UpdateTurnCompleteAtomicallyAdvancesConcurrentTurns(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectExec(`UPDATE sessions[\s\S]+current_turn = GREATEST\(current_turn \+ 1, @current_turn\)`).
		WithArgs(anyDBArgs(13)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateTurnComplete(context.Background(), uuid.New(), uuid.New(), 2, &models.SessionResult{}, "agent-123", "snap-key")
	require.NoError(t, err, "UpdateTurnComplete should atomically advance the shared session turn counter")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateTurnCompleteClearsStaleFailureDetails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectExec(`UPDATE sessions[\s\S]+SET status = 'idle'[\s\S]+failure_explanation = NULL[\s\S]+failure_category = NULL[\s\S]+failure_next_steps = NULL[\s\S]+failure_retry_advised = false`).
		WithArgs(anyDBArgs(13)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateTurnComplete(context.Background(), uuid.New(), uuid.New(), 3, &models.SessionResult{
		ResultSummary: stringPtr("turn recovered"),
	}, "agent-456", "snap-key")
	require.NoError(t, err, "UpdateTurnComplete should clear stale failure details when a turn succeeds")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_ListStaleIdleSessions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectQuery("SELECT .+ FROM sessions.+WHERE status = 'idle'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns))

	sessions, err := store.ListStaleIdleSessions(context.Background(), time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, sessions, 0)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_ListStaleRunningSessions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	// The query must NOT alias `sessions` — sessionPrimaryIssueIDColumn
	// hardcodes `sessions.org_id` / `sessions.id`, which Postgres rejects
	// (42P01) when the outer FROM uses an alias.
	mock.ExpectQuery("FROM sessions\\s+WHERE status = 'running'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns))

	sessions, err := store.ListStaleRunningSessions(context.Background(), time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	require.Len(t, sessions, 0)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_ListStaleRunningSessions_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectQuery("FROM sessions\\s+WHERE status = 'running'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("db down"))

	_, err = store.ListStaleRunningSessions(context.Background(), time.Now().Add(-1*time.Hour))
	require.Error(t, err)
	require.Contains(t, err.Error(), "query stale running sessions")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_ListStalePendingSessions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)

	// See TestSessionStore_ListStaleRunningSessions: the query must not
	// alias `sessions`.
	mock.ExpectQuery("FROM sessions\\s+WHERE status = 'pending'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns))

	sessions, err := store.ListStalePendingSessions(context.Background(), time.Now().Add(-1*time.Hour))
	require.NoError(t, err, "ListStalePendingSessions should not return an error")
	require.Len(t, sessions, 0, "ListStalePendingSessions should return no rows from an empty result")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_ListStalePendingSessions_UsesLastActivityCutoff(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectQuery("FROM sessions\\s+WHERE status = 'pending'[\\s\\S]+last_activity_at < @activity_before").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns))

	sessions, err := store.ListStalePendingSessions(context.Background(), time.Now().Add(-1*time.Hour))
	require.NoError(t, err, "ListStalePendingSessions should not return an error")
	require.Len(t, sessions, 0, "ListStalePendingSessions should return no rows from an empty result")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_ListExpiredSnapshots(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectQuery("SELECT .+ FROM sessions.+sandbox_state = 'snapshotted'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns))

	sessions, err := store.ListExpiredSnapshots(context.Background(), time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.Len(t, sessions, 0)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_Archive(t *testing.T) {
	t.Parallel()

	t.Run("archives session successfully", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)
		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()

		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		err = store.Archive(context.Background(), orgID, sessionID, userID)
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns error when session not found or already archived", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)

		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		err = store.Archive(context.Background(), uuid.New(), uuid.New(), uuid.New())
		require.ErrorIs(t, err, ErrSessionAlreadyArchived)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestSessionStore_ArchiveSystem(t *testing.T) {
	t.Parallel()

	t.Run("archives session without a user actor", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)

		mock.ExpectExec("UPDATE sessions SET archived_at = now\\(\\), archived_by_user_id = NULL").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		archived, err := store.ArchiveSystem(context.Background(), uuid.New(), uuid.New())
		require.NoError(t, err, "ArchiveSystem should not return an error when archiving succeeds")
		require.True(t, archived, "ArchiveSystem should report that it archived the session")
		require.NoError(t, mock.ExpectationsWereMet(), "ArchiveSystem should execute the archive update")
	})

	t.Run("is a no-op when the session is already archived", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)

		mock.ExpectExec("UPDATE sessions SET archived_at = now\\(\\), archived_by_user_id = NULL").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		archived, err := store.ArchiveSystem(context.Background(), uuid.New(), uuid.New())
		require.NoError(t, err, "already-archived sessions should not produce an error")
		require.False(t, archived, "ArchiveSystem should report that no archive transition happened")
		require.NoError(t, mock.ExpectationsWereMet(), "ArchiveSystem should still issue the idempotent archive update")
	})

	t.Run("returns database errors", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewSessionStore(mock)

		mock.ExpectExec("UPDATE sessions SET archived_at = now\\(\\), archived_by_user_id = NULL").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("write failed"))

		archived, err := store.ArchiveSystem(context.Background(), uuid.New(), uuid.New())
		require.Error(t, err, "ArchiveSystem should return database errors")
		require.False(t, archived, "ArchiveSystem should not report an archive transition when the update fails")
		require.Contains(t, err.Error(), "write failed", "ArchiveSystem should preserve the underlying database error")
		require.NoError(t, mock.ExpectationsWereMet(), "ArchiveSystem should execute the archive update even on failure")
	})
}

func TestSessionStore_Unarchive(t *testing.T) {
	t.Parallel()

	t.Run("unarchives session successfully", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)

		// Pinned: Unarchive must bump last_activity_at so the restored session
		// surfaces at the top of the MRU list, not pages deep at its old slot.
		mock.ExpectExec(`UPDATE sessions SET archived_at = NULL, archived_by_user_id = NULL, last_activity_at = now\(\)`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		err = store.Unarchive(context.Background(), uuid.New(), uuid.New())
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet(), "Unarchive must bump last_activity_at")
	})

	t.Run("returns error when session not found or not archived", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)

		mock.ExpectExec(`UPDATE sessions SET archived_at = NULL, archived_by_user_id = NULL, last_activity_at = now\(\)`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		err = store.Unarchive(context.Background(), uuid.New(), uuid.New())
		require.ErrorIs(t, err, ErrSessionNotArchived)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestSessionStore_AcquireTurnHold(t *testing.T) {
	t.Parallel()

	t.Run("publishes proposed ID when container_id was null", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectQuery(`UPDATE sessions\s+SET container_id = COALESCE\(container_id, @container_id\),\s+turn_holding_container = CASE`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"coalesce"}).AddRow("container-xyz"))

		got, err := store.AcquireTurnHold(context.Background(), uuid.New(), uuid.New(), "container-xyz")
		require.NoError(t, err)
		require.Equal(t, "container-xyz", got)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns existing ID when another holder published first", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectQuery(`UPDATE sessions`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"coalesce"}).AddRow("winner"))

		got, err := store.AcquireTurnHold(context.Background(), uuid.New(), uuid.New(), "loser")
		require.NoError(t, err)
		require.Equal(t, "winner", got)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestSessionStore_AcquireTurnHold_DBError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	mock.ExpectQuery(`UPDATE sessions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))

	_, err = store.AcquireTurnHold(context.Background(), uuid.New(), uuid.New(), "c1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "acquire turn hold")
}

func TestSessionStore_ReleaseTurnHold(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		containerID     string
		previewHolds    bool
		sandboxHolds    bool
		wantDestroyNow  bool
		wantContainerID string
	}{
		{"destroys when no holders remain", "container-1", false, false, true, "container-1"},
		{"keeps alive when preview still holds", "container-1", true, false, false, "container-1"},
		{"keeps alive when sandbox holder remains", "container-1", false, true, false, "container-1"},
		{"no-op when container was already empty", "", false, false, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewSessionStore(mock)
			mock.ExpectQuery(`WITH released AS \(\s*UPDATE sessions\s+SET turn_holding_container = FALSE`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows([]string{"container_id", "preview_holds", "sandbox_holds"}).
						AddRow(tt.containerID, tt.previewHolds, tt.sandboxHolds),
				)

			destroyNow, cid, err := store.ReleaseTurnHold(context.Background(), uuid.New(), uuid.New())
			require.NoError(t, err)
			require.Equal(t, tt.wantDestroyNow, destroyNow)
			require.Equal(t, tt.wantContainerID, cid)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSessionStore_ReleaseTurnHold_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	mock.ExpectQuery(`WITH released AS`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("db gone"))

	_, _, err = store.ReleaseTurnHold(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "release turn hold")
}

func TestSessionStore_PublishHydratedContainerID(t *testing.T) {
	t.Parallel()

	t.Run("publishes when container_id was null", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectQuery(`UPDATE sessions\s+SET container_id = COALESCE\(container_id, @container_id\)`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"coalesce"}).AddRow("container-abc"))

		got, err := store.PublishHydratedContainerID(context.Background(), uuid.New(), uuid.New(), "container-abc")
		require.NoError(t, err)
		require.Equal(t, "container-abc", got)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns existing ID when orchestrator published first", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectQuery(`UPDATE sessions`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"coalesce"}).AddRow("orch-winner"))

		got, err := store.PublishHydratedContainerID(context.Background(), uuid.New(), uuid.New(), "preview-losing")
		require.NoError(t, err)
		require.Equal(t, "orch-winner", got)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestSessionStore_PublishHydratedContainerID_DBError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	mock.ExpectQuery(`UPDATE sessions`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))

	_, err = store.PublishHydratedContainerID(context.Background(), uuid.New(), uuid.New(), "c1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "publish hydrated container id")
}

func TestSessionStore_ContainerHoldState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		turnHolds    bool
		previewHolds bool
		expectedTurn bool
		expectedPrev bool
	}{
		{
			name:         "turn holder owns container",
			turnHolds:    true,
			previewHolds: false,
			expectedTurn: true,
			expectedPrev: false,
		},
		{
			name:         "preview holder owns container",
			turnHolds:    false,
			previewHolds: true,
			expectedTurn: false,
			expectedPrev: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			store := NewSessionStore(mock)
			mock.ExpectQuery(`SELECT\s+COALESCE\(s\.turn_holding_container, FALSE\) AS turn_holds`).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"turn_holds", "preview_holds"}).AddRow(tt.turnHolds, tt.previewHolds))

			gotTurn, gotPreview, err := store.ContainerHoldState(context.Background(), uuid.New(), uuid.New(), "container-1")
			require.NoError(t, err, "ContainerHoldState should read holder state")
			require.Equal(t, tt.expectedTurn, gotTurn, "ContainerHoldState should return the exact turn holder flag")
			require.Equal(t, tt.expectedPrev, gotPreview, "ContainerHoldState should return the exact preview holder flag")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_ContainerHoldState_DBError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := NewSessionStore(mock)
	mock.ExpectQuery(`SELECT\s+COALESCE\(s\.turn_holding_container, FALSE\) AS turn_holds`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))

	_, _, err = store.ContainerHoldState(context.Background(), uuid.New(), uuid.New(), "container-1")
	require.Error(t, err, "ContainerHoldState should return database errors")
	require.Contains(t, err.Error(), "container hold state", "ContainerHoldState should wrap the database error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_FinalizeContainerDestroy(t *testing.T) {
	t.Parallel()

	t.Run("clears row when no holder has returned", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectExec(`UPDATE sessions\s+SET container_id = NULL,\s+worker_node_id = NULL,\s+sandbox_state = CASE`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		cleared, err := store.FinalizeContainerDestroy(context.Background(), uuid.New(), uuid.New(), "c-1")
		require.NoError(t, err)
		require.True(t, cleared)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("marks sandbox as none when no snapshot exists", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectExec(`UPDATE sessions\s+SET container_id = NULL,\s+worker_node_id = NULL,\s+sandbox_state = CASE`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		cleared, err := store.FinalizeContainerDestroy(context.Background(), uuid.New(), uuid.New(), "c-1")
		require.NoError(t, err)
		require.True(t, cleared)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns false when CAS matches zero rows", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectExec(`UPDATE sessions`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		cleared, err := store.FinalizeContainerDestroy(context.Background(), uuid.New(), uuid.New(), "c-1")
		require.NoError(t, err)
		require.False(t, cleared)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("checks active sandbox holders before clearing", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock")
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectExec(`NOT EXISTS \(\s*SELECT 1 FROM session_sandbox_holders h[\s\S]+h\.status IN \('active', 'draining'\)[\s\S]+h\.expires_at > now\(\)`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		cleared, err := store.FinalizeContainerDestroy(context.Background(), uuid.New(), uuid.New(), "c-1")
		require.NoError(t, err, "FinalizeContainerDestroy should not fail when an active sandbox holder prevents cleanup")
		require.False(t, cleared, "FinalizeContainerDestroy should leave the container alive when any active sandbox holder remains")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("wraps db error", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectExec(`UPDATE sessions`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("boom"))

		_, err = store.FinalizeContainerDestroy(context.Background(), uuid.New(), uuid.New(), "c-1")
		require.Error(t, err)
		require.Contains(t, err.Error(), "finalize container destroy")
	})
}

func TestSessionStore_SetWorkerNodeIDForContainer(t *testing.T) {
	t.Parallel()

	t.Run("records worker ownership for matching container", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectExec(`UPDATE sessions\s+SET worker_node_id = @worker_node_id`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		err = store.SetWorkerNodeIDForContainer(context.Background(), uuid.New(), uuid.New(), "container-1", "worker-a")
		require.NoError(t, err, "SetWorkerNodeIDForContainer should update ownership when the container still matches")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns an error when ownership can no longer be recorded", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectExec(`UPDATE sessions\s+SET worker_node_id = @worker_node_id`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		err = store.SetWorkerNodeIDForContainer(context.Background(), uuid.New(), uuid.New(), "container-1", "worker-a")
		require.Error(t, err, "SetWorkerNodeIDForContainer should fail when the row no longer matches the expected container")
		require.Contains(t, err.Error(), "worker ownership could be recorded", "the error should explain the ownership race")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func TestSessionStore_ClearContainerID_CAS_Clears(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	// WHERE clause must pin container_id = @expected AND no preview hold,
	// and the SET must reset turn_holding_container=FALSE so a crashed-turn
	// flag doesn't get stranded AND null worker_node_id so the next turn's
	// SetWorkerNodeIDForContainer CAS isn't rejected by a stale owner.
	mock.ExpectExec(`UPDATE sessions\s+SET container_id = NULL,\s+worker_node_id = NULL,\s+turn_holding_container = FALSE\s+WHERE id = @id\s+AND org_id = @org_id\s+AND container_id = @expected\s+AND NOT EXISTS`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	cleared, err := store.ClearContainerID(context.Background(), uuid.New(), uuid.New(), "abc")
	require.NoError(t, err)
	require.True(t, cleared)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_ClearContainerID_CAS_NoMatchReturnsFalse(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	mock.ExpectExec(`UPDATE sessions\s+SET container_id = NULL`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	cleared, err := store.ClearContainerID(context.Background(), uuid.New(), uuid.New(), "abc")
	require.NoError(t, err)
	require.False(t, cleared, "CAS must report cleared=false when no row matched")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_ClearContainerID_DBError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	mock.ExpectExec(`UPDATE sessions\s+SET container_id = NULL`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))

	_, err = store.ClearContainerID(context.Background(), uuid.New(), uuid.New(), "abc")
	require.Error(t, err)
	require.Contains(t, err.Error(), "clear container id")
}

// TestSessionStore_ClearContainerID_UnsticksStaleWorkerOwnership locks in the
// invariant fixed by 000114_session_worker_node_id_unstuck: ClearContainerID
// MUST null worker_node_id along with container_id, so the next turn (on a
// possibly different worker) can stamp ownership without being rejected by a
// stale value.
//
// The bug: pre-fix, ClearContainerID nulled container_id but left worker_node_id
// pointing at the dead worker. The next ContinueSession's SetWorkerNodeIDForContainer
// CAS — which requires worker_node_id IS NULL/empty OR equal to ours — failed
// with "session container ownership changed before worker ownership could be
// recorded", and the user-facing turn failed.
//
// If a future cleanup drops the worker_node_id null in ClearContainerID, this
// test fails because the second ExpectExec's regex no longer matches the SQL.
func TestSessionStore_ClearContainerID_UnsticksStaleWorkerOwnership(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()

	// Step 1: orphan reconciler clears the row left behind by a crashed worker.
	// The SQL must null both container_id and worker_node_id.
	mock.ExpectExec(`UPDATE sessions\s+SET container_id = NULL,\s+worker_node_id = NULL,\s+turn_holding_container = FALSE`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	cleared, err := store.ClearContainerID(context.Background(), orgID, sessionID, "container-from-dead-worker")
	require.NoError(t, err)
	require.True(t, cleared)

	// Step 2: a new turn on a *different* worker stamps fresh ownership.
	// CAS predicate is `worker_node_id IS NULL/'' OR equal to ours` — the
	// preceding clear set it to NULL, so this matches the row.
	mock.ExpectExec(`UPDATE sessions\s+SET worker_node_id = @worker_node_id`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	err = store.SetWorkerNodeIDForContainer(context.Background(), orgID, sessionID, "container-on-new-worker", "worker-new")
	require.NoError(t, err, "SetWorkerNodeIDForContainer must succeed for a new worker after ClearContainerID")

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_ListOrphanedContainers(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	now := time.Now()
	// turn_holding_container is intentionally NOT part of the WHERE —
	// crashed-turn rows (stuck with the flag TRUE) must be reachable so
	// the reconciler can clear them via its IsAlive + CAS pipeline.
	mock.ExpectQuery(`FROM sessions\s+WHERE container_id IS NOT NULL\s+AND id > @after_id\s+AND status <> 'running'\s+AND COALESCE\(recovery_state, ''\) NOT IN \('queued', 'recovering'\)\s+AND NOT EXISTS`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).
				AddRow(newAgentSessionRow(uuid.New(), uuid.New(), uuid.New(), now)...),
		)

	sessions, err := store.ListOrphanedContainers(context.Background(), uuid.Nil)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_ListOrphanedContainers_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	mock.ExpectQuery(`FROM sessions\s+WHERE container_id IS NOT NULL`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))

	_, err = store.ListOrphanedContainers(context.Background(), uuid.Nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "list orphaned containers")
}

func TestSessionStore_ListReferencedContainerIDs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	mock.ExpectQuery(`SELECT container_id\s+FROM sessions\s+WHERE container_id IS NOT NULL`).
		WillReturnRows(
			pgxmock.NewRows([]string{"container_id"}).
				AddRow("container-a").
				AddRow("container-b"),
		)

	ids, err := store.ListReferencedContainerIDs(context.Background())
	require.NoError(t, err, "ListReferencedContainerIDs should not return an error")
	require.Equal(t, []string{"container-a", "container-b"}, ids, "ListReferencedContainerIDs should return every non-null session container id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_ListReferencedContainerIDs_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	mock.ExpectQuery(`SELECT container_id\s+FROM sessions\s+WHERE container_id IS NOT NULL`).
		WillReturnError(errors.New("boom"))

	_, err = store.ListReferencedContainerIDs(context.Background())
	require.Error(t, err, "ListReferencedContainerIDs should surface query failures")
	require.Contains(t, err.Error(), "list referenced container ids", "ListReferencedContainerIDs should wrap query failures with context")
}

// TestSessionStore_ListContainerHoldingSessions is the rehydrate-side
// counterpart to ListOrphanedContainers: same paging, opposite predicate
// (EXISTS preview hold instead of NOT EXISTS). The query must filter by
// preview_holding_container so we don't try to rehydrate listeners for
// containers that aren't actually being kept alive across a worker restart.
func TestSessionStore_ListContainerHoldingSessions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	now := time.Now()
	mock.ExpectQuery(`FROM sessions\s+WHERE container_id IS NOT NULL\s+AND id > @after_id\s+AND EXISTS[\s\S]+p\.worker_node_id = @worker_node_id[\s\S]+LIMIT @limit`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).
				AddRow(newAgentSessionRow(uuid.New(), uuid.New(), uuid.New(), now)...),
		)

	sessions, err := store.ListContainerHoldingSessions(context.Background(), "worker-a", uuid.Nil, 500)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_ListContainerHoldingSessions_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	mock.ExpectQuery(`FROM sessions\s+WHERE container_id IS NOT NULL`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))

	_, err = store.ListContainerHoldingSessions(context.Background(), "worker-a", uuid.Nil, 500)
	require.Error(t, err)
	require.Contains(t, err.Error(), "list container-holding sessions")
}

// TestSessionStore_ListContainerHoldingSessions_ScanError covers the path
// where pgx.CollectRows fails mid-scan (a corrupt row, a column type
// mismatch, or a transient driver fault). We surface the error verbatim
// — the rehydrate caller then aborts the pass without a partial keep set.
func TestSessionStore_ListContainerHoldingSessions_ScanError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	now := time.Now()
	scanErr := errors.New("simulated mid-row scan failure")
	mock.ExpectQuery(`FROM sessions\s+WHERE container_id IS NOT NULL\s+AND id > @after_id\s+AND EXISTS[\s\S]+p\.worker_node_id = @worker_node_id[\s\S]+LIMIT @limit`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionTestColumns).
				AddRow(newAgentSessionRow(uuid.New(), uuid.New(), uuid.New(), now)...).
				RowError(0, scanErr),
		)

	_, err = store.ListContainerHoldingSessions(context.Background(), "worker-a", uuid.Nil, 500)
	require.Error(t, err)
	require.ErrorIs(t, err, scanErr, "scan errors must propagate to the caller so partial result sets aren't returned")
}

func TestSessionStore_BeginRuntime_PreservesRecoveringState(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery(`UPDATE sessions[\s\S]+SET status = @status[\s\S]+started_at = @runtime_started_at[\s\S]+completed_at = NULL[\s\S]+runtime_soft_deadline_at = @runtime_soft_deadline_at[\s\S]+recovery_state = CASE\s+WHEN recovery_state = 'recovering' THEN recovery_state ELSE '' END[\s\S]+RETURNING`).
		WithArgs(
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows(sessionTestColumns).AddRow(newAgentSessionRow(sessionID, issueID, orgID, now)...))

	err = store.BeginRuntime(
		context.Background(),
		orgID,
		sessionID,
		models.CheckpointCapabilityFullResume,
		now.Add(10*time.Minute),
		now.Add(20*time.Minute),
		now,
	)
	require.NoError(t, err, "BeginRuntime should not clear recovering state when resuming from a checkpoint")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_LinearSessionFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		call      func(store *SessionStore, orgID, sessionID uuid.UUID) error
		sql       string
		rows      int64
		errText   string
		expectErr bool
	}{
		{
			name: "sets prepare state",
			call: func(store *SessionStore, orgID, sessionID uuid.UUID) error {
				return store.SetLinearPrepareState(context.Background(), orgID, sessionID, models.LinearPrepareStateReady)
			},
			sql:  "UPDATE sessions[\\s\\S]+SET linear_prepare_state",
			rows: 1,
		},
		{
			name: "prepare state missing session",
			call: func(store *SessionStore, orgID, sessionID uuid.UUID) error {
				return store.SetLinearPrepareState(context.Background(), orgID, sessionID, models.LinearPrepareStateReady)
			},
			sql:       "UPDATE sessions[\\s\\S]+SET linear_prepare_state",
			rows:      0,
			errText:   "session not found",
			expectErr: true,
		},
		{
			name: "sets identifier hint",
			call: func(store *SessionStore, orgID, sessionID uuid.UUID) error {
				return store.SetLinearIdentifierHint(context.Background(), orgID, sessionID, "ACS-123")
			},
			sql:  "UPDATE sessions[\\s\\S]+SET linear_identifier_hint",
			rows: 1,
		},
		{
			name: "identifier hint missing session",
			call: func(store *SessionStore, orgID, sessionID uuid.UUID) error {
				return store.SetLinearIdentifierHint(context.Background(), orgID, sessionID, "ACS-123")
			},
			sql:       "UPDATE sessions[\\s\\S]+SET linear_identifier_hint",
			rows:      0,
			errText:   "session not found",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			mock.ExpectExec(tt.sql).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))

			err = tt.call(NewSessionStore(mock), uuid.New(), uuid.New())
			if tt.expectErr {
				require.Error(t, err, "linear session field update should return expected error")
				require.Contains(t, err.Error(), tt.errText, "linear session field update should include expected context")
			} else {
				require.NoError(t, err, "linear session field update should succeed")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

// TestSessionStore_SetLinearPrepareStateIfNotReady locks the guarded-write
// contract that protects "ready" from being clobbered by a concurrent
// prepare-worker failure path. The unguarded SetLinearPrepareState would let
// a sibling worker mark "failed" on top of an earlier worker's "ready",
// dead-lettering a usable session for no reason.
func TestSessionStore_SetLinearPrepareStateIfNotReady(t *testing.T) {
	t.Parallel()

	t.Run("writes when not ready", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		mock.ExpectExec("UPDATE sessions[\\s\\S]+SET linear_prepare_state[\\s\\S]+linear_prepare_state <>").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		err = NewSessionStore(mock).SetLinearPrepareStateIfNotReady(context.Background(), uuid.New(), uuid.New(), models.LinearPrepareStateFailed)
		require.NoError(t, err, "guarded write should succeed when current state is not ready")
		require.NoError(t, mock.ExpectationsWereMet(), "guarded write should issue the conditional UPDATE")
	})

	t.Run("zero rows is not an error", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		mock.ExpectExec("UPDATE sessions[\\s\\S]+SET linear_prepare_state[\\s\\S]+linear_prepare_state <>").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		err = NewSessionStore(mock).SetLinearPrepareStateIfNotReady(context.Background(), uuid.New(), uuid.New(), models.LinearPrepareStateFailed)
		require.NoError(t, err, "guarded write should treat already-ready rows as an intentional no-op")
		require.NoError(t, mock.ExpectationsWereMet(), "guarded write should issue the conditional UPDATE even when no rows match")
	})

	t.Run("wraps db errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		mock.ExpectExec("UPDATE sessions[\\s\\S]+SET linear_prepare_state[\\s\\S]+linear_prepare_state <>").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("db unavailable"))

		err = NewSessionStore(mock).SetLinearPrepareStateIfNotReady(context.Background(), uuid.New(), uuid.New(), models.LinearPrepareStateFailed)
		require.Error(t, err, "guarded write should surface database errors")
		require.Contains(t, err.Error(), "update linear prepare state", "guarded write should wrap database errors with context")
	})
}
