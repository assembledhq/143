package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/api/sse"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	previewsvc "github.com/assembledhq/143/internal/services/preview"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type stubSessionPRCredentialStore struct {
	cred *models.DecryptedUserCredential
	err  error
}

type failingSSEWriter struct {
	header       http.Header
	failOnSubstr string
	failAfter    int
	writes       int
}

func (w *failingSSEWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failingSSEWriter) WriteHeader(int) {}

func (w *failingSSEWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.failAfter > 0 && w.writes < w.failAfter {
		return len(p), nil
	}
	if w.failOnSubstr == "" || strings.Contains(string(p), w.failOnSubstr) {
		return 0, errors.New("sse write failed")
	}
	return len(p), nil
}

func (w *failingSSEWriter) Flush() {}

func (s *stubSessionPRCredentialStore) GetForUser(_ context.Context, _, _ uuid.UUID, _ models.ProviderName) (*models.DecryptedUserCredential, error) {
	return s.cred, s.err
}

type stubSessionPRAuthCredentialChecker struct {
	hasValidCredentialFunc func(context.Context, uuid.UUID, uuid.UUID) (bool, error)
}

func (s *stubSessionPRAuthCredentialChecker) HasValidCredential(ctx context.Context, orgID, userID uuid.UUID) (bool, error) {
	if s.hasValidCredentialFunc != nil {
		return s.hasValidCredentialFunc(ctx, orgID, userID)
	}
	return false, nil
}

func (s *stubSessionPRCredentialStore) Upsert(_ context.Context, _, _ uuid.UUID, _ models.ProviderConfig, _ bool) error {
	return nil
}

func (s *stubSessionPRCredentialStore) Disable(_ context.Context, _, _ uuid.UUID, _ models.ProviderName) error {
	return nil
}

type stubSessionPRTitleSyncer struct {
	called    bool
	lastTitle string
	err       error
}

func (s *stubSessionPRTitleSyncer) SyncSessionTitle(_ context.Context, session *models.Session) error {
	s.called = true
	if session.Title != nil {
		s.lastTitle = *session.Title
	}
	return s.err
}

type archiveTestSnapshotStore struct {
	deleted []string
	err     error
}

func (s *archiveTestSnapshotStore) Save(context.Context, string, io.Reader) error {
	return nil
}

func (s *archiveTestSnapshotStore) Load(context.Context, string, io.Writer) error {
	return nil
}

func (s *archiveTestSnapshotStore) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	return s.err
}

func newSessionHandler(t *testing.T, mock pgxmock.PgxPoolIface) *SessionHandler {
	t.Helper()
	h := NewSessionHandler(
		db.NewSessionStore(mock),
		db.NewSessionLogStore(mock),
		db.NewSessionQuestionStore(mock),
		db.NewPullRequestStore(mock),
		db.NewIssueStore(mock),
		db.NewRepositoryStore(mock),
		db.NewOrganizationStore(mock),
		db.NewJobStore(mock),
		db.NewSessionMessageStore(mock),
		db.NewSessionThreadStore(mock),
		nil, // llmClient — not needed in unit tests
		zerolog.Nop(),
	)
	// SendMessage's optional resolve_review_comment_ids path requires the
	// review-comment store. The store presence is just a feature gate; tx-
	// scoped instances are created on demand inside the handler.
	h.SetReviewCommentStore(db.NewSessionReviewCommentStore(mock))
	h.SetReviewLoopStore(db.NewSessionReviewLoopStore(mock))
	h.SetTxStarter(mock)
	return h
}

// sessionColumns is the standard column set for sessions queries.
// Must match sessionSelectColumns in session_store.go. Update all inline
// AddRow calls in this file when adding/removing/reordering columns.
//
// Note on positional fixtures: every new column on `sessions` ripples
// through several helpers — sessionTestRow, padSessionIdentityColumns,
// padLinearFields, padSessionWorkspaceGeneration, and the row literals in
// internal/db/auth_session_store_test.go and
// internal/api/handlers/session_files_test.go. Each helper has to be taught
// where the new column lives relative to landmarks like pending_snapshot_*,
// linear_*, git_identity_*, and deleted_at. If you add a column, search
// repo-wide for sessionColumns and update every fixture so the positional
// indexes stay coherent — or replace this scaffolding with a named-column
// row builder so future additions stop requiring this dance.
var sessionColumns = []string{
	"id", "primary_issue_id", "org_id", "origin", "interaction_mode", "validation_policy", "agent_type", "status", "autonomy_level", "token_mode",
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
	"deleted_at", "git_identity_source", "git_identity_user_id", "created_at",
}

var reviewLoopColumns = []string{
	"id", "org_id", "session_id", "automation_run_id", "thread_id",
	"status", "source", "agent_type", "max_passes", "fix_mode", "completed_passes", "review_required",
	"bypassed_by_user_id", "bypass_reason", "loop_start_checkpoint_key", "latest_checkpoint_key",
	"latest_summary", "started_by_user_id", "started_at", "completed_at",
}

func reviewLoopRowWithLatestCheckpoint(loopID, sessionID uuid.UUID, status, source string, latestCheckpointKey *string) []any {
	now := time.Now()
	return []any{
		loopID, uuid.New(), sessionID, nil, nil,
		status, source, "claude_code", 2, "minimal", 1, false,
		nil, nil, nil, latestCheckpointKey,
		nil, nil, now, &now,
	}
}

func ptr[T any](v T) *T {
	return &v
}

func sessionTestRowWithPolicyDefaults(values []interface{}) []interface{} {
	origin := string(models.SessionOriginManual)
	interactionMode := string(models.SessionInteractionModeInteractive)
	validationPolicy := string(models.SessionValidationPolicyOnTurnComplete)
	if len(values) > 1 {
		if issueID, ok := values[1].(uuid.UUID); ok && issueID != uuid.Nil {
			origin = string(models.SessionOriginIssueTrigger)
			interactionMode = string(models.SessionInteractionModeSingleRun)
			validationPolicy = string(models.SessionValidationPolicyOnSessionEnd)
		}
	}
	row := make([]interface{}, 0, len(values)+3)
	row = append(row, values[:3]...)
	row = append(
		row,
		origin,
		interactionMode,
		validationPolicy,
	)
	row = append(row, values[3:]...)
	return normalizeSessionRowPrimaryIssueID(row)
}

func stripLegacySessionResultConfidence(row []interface{}) []interface{} {
	if len(row) <= 13 {
		return row
	}
	if _, ok := row[13].(bool); ok {
		return row
	}
	if _, ok := row[12].(bool); ok {
		return row
	}
	stripped := make([]interface{}, 0, len(row)-3)
	stripped = append(stripped, row[:11]...)
	stripped = append(stripped, row[14:]...)
	return stripped
}

// normalizeSessionRowPrimaryIssueID converts a legacy uuid.UUID issue_id
// placeholder at position 1 into the *uuid.UUID primary_issue_id value that
// pgx expects for the SessionStore.sessionSelectColumns subquery. A uuid.Nil
// is mapped to nil so NULL primary links round-trip cleanly.
func normalizeSessionRowPrimaryIssueID(row []interface{}) []interface{} {
	if len(row) < 2 {
		return row
	}
	switch v := row[1].(type) {
	case uuid.UUID:
		if v == uuid.Nil {
			row[1] = nil
		} else {
			id := v
			row[1] = &id
		}
	}
	return row
}

const (
	legacySessionColumnsLen         = 58
	legacyRuntimeInsertIndex        = 42
	legacySessionReasoningIndex     = 35
	legacySessionBaseCommitSHAIndex = 44
	legacySessionDiffCollectedIndex = 54
	legacySessionLatestDiffIndex    = 55
)

const (
	sessionWorkerNodeIndex      = 15
	sessionReasoningIndex       = 35
	sessionWorkspaceGenIndex    = 38
	sessionBaseCommitSHAIndex   = 62
	sessionDiffCollectedAtIndex = 72
	sessionLatestDiffIndex      = 73
	// preLinearSessionColumnsLen is the size of sessionColumns *before*
	// migrations 103 (linear_*) and 100 (git_identity_*) added their
	// columns. Test fixtures authored against the pre-migration shape pass
	// rows of this length (or shorter); the dispatch routes them to the
	// right pad helper, then sessionTestRow pads the four trailing linear
	// columns and the two trailing identity nils at the end.
	preLinearSessionColumnsLen                  = 76
	sessionColumnsWithLegacyResultConfidenceLen = 92
)

// TestPreLinearSessionColumnsLenStaysInSync trips when a future migration
// changes the column count without bumping preLinearSessionColumnsLen.
// Without this guard, every length-based dispatch case below would silently
// route to the wrong padding helper.
func TestPreLinearSessionColumnsLenStaysInSync(t *testing.T) {
	t.Parallel()
	const pendingSnapshotFieldsAdded = 2
	const unpushedChangesFieldAdded = 1
	const linearFieldsAdded = 4
	const identityFieldsAdded = 2
	const prPushFieldsAdded = 2
	const branchCreationFieldsAdded = 3
	const workspaceGenerationFieldAdded = 1
	const workspaceRevisionFieldsAdded = 2
	require.Equal(t, preLinearSessionColumnsLen+pendingSnapshotFieldsAdded+unpushedChangesFieldAdded+workspaceRevisionFieldsAdded+linearFieldsAdded+identityFieldsAdded+prPushFieldsAdded+branchCreationFieldsAdded, sessionColumnsWithLegacyResultConfidenceLen,
		"sessionColumns shifted; bump preLinearSessionColumnsLen, pendingSnapshotFieldsAdded, "+
			"unpushedChangesFieldAdded, workspaceRevisionFieldsAdded, linearFieldsAdded, identityFieldsAdded, prPushFieldsAdded, or branchCreationFieldsAdded if a new migration added more session columns")
	require.Equal(t, len(sessionColumns)+3, sessionColumnsWithLegacyResultConfidenceLen+workspaceGenerationFieldAdded, "legacy confidence columns should stay isolated to test fixtures")
}

// linearSessionDefaults returns the placeholder values for the derived
// has_unpushed_changes field plus the four linear_* columns. Test rows that don't pass
// values for these get them auto-padded by sessionTestRow (one of the
// length-difference cases below).
func linearSessionDefaults() []interface{} {
	return []interface{}{
		int64(0),                              // workspace_revision
		time.Time{},                           // workspace_revision_updated_at
		false,                                 // has_unpushed_changes
		false,                                 // linear_private
		false,                                 // linear_state_sync_disabled
		(*string)(nil),                        // linear_identifier_hint
		string(models.LinearPrepareStateNone), // linear_prepare_state
	}
}

// padLinearFields injects has_unpushed_changes plus the linear_* defaults at the position right
// before the trailing deleted_at/created_at columns when the row was built
// without them. Called from sessionTestRow on each row regardless of the
// branch that resolved its prior shape. Runs before padSessionIdentityColumns,
// so the input still has only deleted_at + created_at at the tail.
func padLinearFields(values []interface{}) []interface{} {
	if len(values) >= sessionColumnsWithLegacyResultConfidenceLen-2 {
		return values
	}
	if len(values) < 2 {
		return values
	}
	insertAt := len(values) - 2 // before deleted_at, created_at
	row := make([]interface{}, 0, len(values)+5)
	row = append(row, values[:insertAt]...)
	row = append(row, linearSessionDefaults()...)
	row = append(row, values[insertAt:]...)
	return row
}

func padSessionWorkspaceGeneration(row []interface{}) []interface{} {
	if len(row) <= sessionWorkspaceGenIndex {
		return row
	}
	switch row[sessionWorkspaceGenIndex].(type) {
	case int64, int, int32:
		return row
	default:
		padded := make([]interface{}, 0, len(row)+1)
		padded = append(padded, row[:sessionWorkspaceGenIndex]...)
		padded = append(padded, int64(0))
		padded = append(padded, row[sessionWorkspaceGenIndex:]...)
		return padded
	}
}

var sessionPullRequestColumns = []string{
	"id", "session_id", "org_id", "github_pr_number", "github_pr_url", "github_repo",
	"title", "body", "status", "review_status", "authored_by", "ci_status", "head_sha", "head_ref", "base_sha",
	"merge_state", "has_conflicts", "failing_test_count", "needs_agent_action", "github_state_synced_at",
	"health_version", "merged_at", "created_at", "updated_at",
}

func sessionPullRequestRow(prID uuid.UUID, sessionID *uuid.UUID, orgID uuid.UUID, repo string, now time.Time) []any {
	return []any{
		prID,
		sessionID,
		orgID,
		42,
		"https://github.com/" + repo + "/pull/42",
		repo,
		"Fix bug",
		(*string)(nil),
		"open",
		"pending",
		"app",
		"",
		(*string)(nil),
		(*string)(nil),
		(*string)(nil),
		models.PullRequestMergeStateUnknown,
		false,
		0,
		false,
		(*time.Time)(nil),
		int64(0),
		(*time.Time)(nil),
		now,
		now,
	}
}

func sessionRowNeedsPolicyDefaults(values []interface{}) bool {
	if len(values) < 4 {
		return false
	}
	agentType, ok := values[3].(string)
	if !ok {
		return false
	}
	switch agentType {
	case "claude_code", "claude-code", "gemini_cli", "gemini-cli", "codex", "amp", "pi", "pm_agent":
		return true
	default:
		return false
	}
}

func normalizeSessionAgentType(value interface{}) interface{} {
	agentType, ok := value.(string)
	if !ok {
		return value
	}
	switch agentType {
	case "claude-code":
		return string(models.AgentTypeClaudeCode)
	case "gemini-cli":
		return string(models.AgentTypeGeminiCLI)
	default:
		return value
	}
}

func normalizeSessionRowAgentType(values []interface{}, agentTypeIndex int) []interface{} {
	if len(values) <= agentTypeIndex {
		return values
	}
	row := append([]interface{}(nil), values...)
	row[agentTypeIndex] = normalizeSessionAgentType(row[agentTypeIndex])
	return row
}

func insertSessionValue(values []interface{}, idx int, value interface{}) []interface{} {
	row := make([]interface{}, 0, len(values)+1)
	row = append(row, values[:idx]...)
	row = append(row, value)
	row = append(row, values[idx:]...)
	return row
}

func sessionRowWithCurrentOptionalDefaults(values []interface{}, includeReasoning bool, includeWorkerNode bool, includeDiffMetadata bool) []interface{} {
	row := values
	if includeWorkerNode {
		row = insertSessionValue(row, sessionWorkerNodeIndex, nil)
	}
	if includeReasoning {
		row = insertSessionValue(row, sessionReasoningIndex, nil)
	}
	if includeDiffMetadata {
		row = insertSessionValue(row, sessionBaseCommitSHAIndex, nil)
		row = insertSessionValue(row, sessionDiffCollectedAtIndex, nil)
		row = insertSessionValue(row, sessionLatestDiffIndex, nil)
	}
	return row
}

func sessionRowWithLegacyOptionalDefaults(values []interface{}, includeReasoning bool, includeWorkerNode bool, includeDiffMetadata bool) []interface{} {
	row := values
	if includeWorkerNode {
		row = insertSessionValue(row, sessionWorkerNodeIndex, nil)
	}
	if includeReasoning {
		row = insertSessionValue(row, legacySessionReasoningIndex, nil)
	}
	if includeDiffMetadata {
		row = insertSessionValue(row, legacySessionBaseCommitSHAIndex, nil)
		row = insertSessionValue(row, legacySessionDiffCollectedIndex, nil)
		row = insertSessionValue(row, legacySessionLatestDiffIndex, nil)
	}
	return row
}

func legacyRuntimeSessionDefaults() []interface{} {
	return []interface{}{
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
	}
}

func expandLegacySessionRow(values []interface{}) []interface{} {
	row := make([]interface{}, 0, len(sessionColumns))
	row = append(row, values[:legacyRuntimeInsertIndex]...)
	row = append(row, legacyRuntimeSessionDefaults()...)
	row = append(row, values[legacyRuntimeInsertIndex:]...)
	return row
}

func sessionRowLikelyOmitsWorkerNodeID(values []interface{}) bool {
	if len(values) <= sessionWorkerNodeIndex {
		return false
	}

	_, ok := values[sessionWorkerNodeIndex].(bool)
	return ok
}

func sessionTestRow(values ...interface{}) []interface{} {
	row := normalizeSessionRowPrimaryIssueID(sessionTestRowDispatch(values...))
	// Dispatch returns the pre-Linear legacy shape (no pending_snapshot_*,
	// no linear_*, no git_identity_*). Chain the pads so each fixture stays
	// oblivious to the column-shaping migrations:
	//   - padLinearFields adds the four linear_* defaults at the position
	//     right before deleted_at/created_at (76 → 80).
	//   - padSessionIdentityColumns splices pending_snapshot_* after
	//     snapshot_key and the git_identity_* pair before created_at
	//     (80 → 84).
	if len(row) == preLinearSessionColumnsLen {
		row = padLinearFields(row)
	}
	row = padSessionIdentityColumns(row)
	if len(row) == sessionColumnsWithLegacyResultConfidenceLen || len(row) == len(sessionColumns)+3 {
		row = stripLegacySessionResultConfidence(row)
	}
	row = padSessionWorkspaceGeneration(row)
	return row
}

func sessionTestRowDispatch(values ...interface{}) []interface{} {
	if sessionRowNeedsPolicyDefaults(values) {
		values = normalizeSessionRowAgentType(values, 3)
		switch len(values) {
		case preLinearSessionColumnsLen - 3:
			return sessionTestRowWithPolicyDefaults(values)
		case preLinearSessionColumnsLen - 4:
			if sessionRowLikelyOmitsWorkerNodeID(values) {
				return sessionRowWithCurrentOptionalDefaults(sessionTestRowWithPolicyDefaults(values), false, true, false)
			}
			return sessionRowWithCurrentOptionalDefaults(sessionTestRowWithPolicyDefaults(values), true, false, false)
		case preLinearSessionColumnsLen - 5:
			return sessionRowWithCurrentOptionalDefaults(sessionTestRowWithPolicyDefaults(values), true, true, false)
		case preLinearSessionColumnsLen - 6:
			return sessionRowWithCurrentOptionalDefaults(sessionTestRowWithPolicyDefaults(values), false, false, true)
		case preLinearSessionColumnsLen - 7:
			if sessionRowLikelyOmitsWorkerNodeID(values) {
				return sessionRowWithCurrentOptionalDefaults(sessionTestRowWithPolicyDefaults(values), false, true, true)
			}
			return sessionRowWithCurrentOptionalDefaults(sessionTestRowWithPolicyDefaults(values), true, false, true)
		case preLinearSessionColumnsLen - 8:
			return sessionRowWithCurrentOptionalDefaults(sessionTestRowWithPolicyDefaults(values), true, true, true)
		case legacySessionColumnsLen - 3:
			return expandLegacySessionRow(sessionTestRowWithPolicyDefaults(values))
		case legacySessionColumnsLen - 4:
			if sessionRowLikelyOmitsWorkerNodeID(values) {
				return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(sessionTestRowWithPolicyDefaults(values), false, true, false))
			}
			return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(sessionTestRowWithPolicyDefaults(values), true, false, false))
		case legacySessionColumnsLen - 5:
			return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(sessionTestRowWithPolicyDefaults(values), true, true, false))
		case legacySessionColumnsLen - 6:
			return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(sessionTestRowWithPolicyDefaults(values), false, false, true))
		case legacySessionColumnsLen - 7:
			if sessionRowLikelyOmitsWorkerNodeID(values) {
				return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(sessionTestRowWithPolicyDefaults(values), false, true, true))
			}
			return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(sessionTestRowWithPolicyDefaults(values), true, false, true))
		case legacySessionColumnsLen - 8:
			return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(sessionTestRowWithPolicyDefaults(values), true, true, true))
		}
	}
	values = stripLegacySessionResultConfidence(values)
	values = normalizeSessionRowAgentType(values, 6)

	switch len(values) {
	case preLinearSessionColumnsLen:
		return values
	case legacySessionColumnsLen:
		return expandLegacySessionRow(values)
	case legacySessionColumnsLen - 1:
		if sessionRowLikelyOmitsWorkerNodeID(values) {
			return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(values, false, true, false))
		}
		return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(values, true, false, false))
	case legacySessionColumnsLen - 2:
		return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(values, true, true, false))
	case legacySessionColumnsLen - 4:
		return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(values, false, false, true))
	case legacySessionColumnsLen - 3:
		if sessionRowLikelyOmitsWorkerNodeID(values) {
			return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(values, false, true, true))
		}
		return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(values, true, false, true))
	case legacySessionColumnsLen - 5:
		return expandLegacySessionRow(sessionRowWithLegacyOptionalDefaults(values, true, true, true))
	case preLinearSessionColumnsLen - 1:
		if sessionRowLikelyOmitsWorkerNodeID(values) {
			return sessionRowWithCurrentOptionalDefaults(values, false, true, false)
		}
		return sessionRowWithCurrentOptionalDefaults(values, true, false, false)
	case preLinearSessionColumnsLen - 2:
		return sessionRowWithCurrentOptionalDefaults(values, true, true, false)
	case preLinearSessionColumnsLen - 4:
		return sessionRowWithCurrentOptionalDefaults(values, false, false, true)
	case preLinearSessionColumnsLen - 3:
		if sessionRowLikelyOmitsWorkerNodeID(values) {
			return sessionRowWithCurrentOptionalDefaults(values, false, true, true)
		}
		return sessionRowWithCurrentOptionalDefaults(values, true, false, true)
	case preLinearSessionColumnsLen - 5:
		return sessionRowWithCurrentOptionalDefaults(values, true, true, true)
	default:
		panic(fmt.Sprintf(
			"sessionTestRow received %d values, want %d, %d, %d, %d, %d, %d, %d, %d, %d, %d, %d, or %d (plus policy-less variants)",
			len(values),
			preLinearSessionColumnsLen,
			preLinearSessionColumnsLen-1,
			preLinearSessionColumnsLen-2,
			preLinearSessionColumnsLen-3,
			preLinearSessionColumnsLen-4,
			preLinearSessionColumnsLen-5,
			legacySessionColumnsLen,
			legacySessionColumnsLen-1,
			legacySessionColumnsLen-2,
			legacySessionColumnsLen-3,
			legacySessionColumnsLen-4,
			legacySessionColumnsLen-5,
		))
	}
}

func addSessionRow(rows *pgxmock.Rows, values ...interface{}) *pgxmock.Rows {
	row := sessionTestRow(values...)
	if len(row) != len(sessionColumns) {
		panic(fmt.Sprintf("addSessionRow produced %d values for %d session columns", len(row), len(sessionColumns)))
	}
	return rows.AddRow(row...)
}

// padSessionIdentityColumns retrofits rows produced by the legacy
// sessionTestRow dispatch with nil values for columns added after the
// fixture conventions were settled: the pending-snapshot pair
// (pending_snapshot_key + pending_snapshot_set_at, between snapshot_key and
// runtime_soft_deadline_at) and the trailing git_identity_source /
// git_identity_user_id pair (immediately before created_at). Callers don't
// have to update their fixtures one-by-one.
func padSessionIdentityColumns(row []interface{}) []interface{} {
	if len(row) == len(sessionColumns) {
		return row
	}
	if len(row) >= sessionColumnsWithLegacyResultConfidenceLen {
		return row
	}
	if len(row) == sessionColumnsWithLegacyResultConfidenceLen-3 {
		const branchCreationStateIndex = 76
		padded := make([]interface{}, 0, sessionColumnsWithLegacyResultConfidenceLen)
		padded = append(padded, row[:branchCreationStateIndex]...)
		padded = append(padded, "idle", (*string)(nil), (*string)(nil))
		padded = append(padded, row[branchCreationStateIndex:]...)
		return padded
	}
	if len(row) != sessionColumnsWithLegacyResultConfidenceLen-9 {
		// Some other length we don't recognize — let the row through
		// unchanged so the AddRow call surfaces the real mismatch.
		return row
	}
	const pendingSnapshotKeyIndex = 42
	withPending := make([]interface{}, 0, len(row)+2)
	withPending = append(withPending, row[:pendingSnapshotKeyIndex]...)
	withPending = append(withPending, nil, nil) // pending_snapshot_key, pending_snapshot_set_at
	withPending = append(withPending, row[pendingSnapshotKeyIndex:]...)

	// Insert the pr_push pair right after pr_creation_error (and before
	// diff_collected_at). In the post-pending row, diff_collected_at sits
	// at index 74 (the +2 shift from the pre-pending layout where it was at
	// index 72). The pr_push pair lands immediately before it. Use "idle"
	// (not nil) for pr_push_state because the model's field is a non-pointer
	// PRPushState — a NULL would fail pgx scanning. The migration mirrors
	// this with NOT NULL DEFAULT 'idle'.
	const prPushStateIndex = 74
	withPRPush := make([]interface{}, 0, len(withPending)+2)
	withPRPush = append(withPRPush, withPending[:prPushStateIndex]...)
	withPRPush = append(withPRPush, "idle", (*string)(nil)) // pr_push_state, pr_push_error
	withPRPush = append(withPRPush, withPending[prPushStateIndex:]...)

	const branchCreationStateIndex = prPushStateIndex + 2
	withBranch := make([]interface{}, 0, len(withPRPush)+3)
	withBranch = append(withBranch, withPRPush[:branchCreationStateIndex]...)
	withBranch = append(withBranch, "idle", (*string)(nil), (*string)(nil))
	withBranch = append(withBranch, withPRPush[branchCreationStateIndex:]...)

	padded := make([]interface{}, 0, len(sessionColumns))
	padded = append(padded, withBranch[:len(withBranch)-1]...)
	padded = append(padded, nil, nil)
	padded = append(padded, withBranch[len(withBranch)-1])
	return padded
}

func sessionAnyArgs(count int) []interface{} {
	args := make([]interface{}, count)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

type capturingJSONArg struct {
	dest *[]byte
}

func (c capturingJSONArg) Match(v interface{}) bool {
	switch b := v.(type) {
	case json.RawMessage:
		*c.dest = append((*c.dest)[:0], b...)
	case []byte:
		*c.dest = append((*c.dest)[:0], b...)
	case string:
		*c.dest = append((*c.dest)[:0], []byte(b)...)
	default:
		return false
	}
	return true
}

var sessionThreadHandlerColumns = []string{
	"id", "session_id", "org_id", "agent_type", "model_override",
	"label", "instructions", "file_scope", "status", "agent_session_id", "current_turn", "last_activity_at",
	"result_summary", "diff", "failure_explanation", "failure_category",
	"started_at", "completed_at", "created_at", "archived_at",
	"base_snapshot_key", "cost_cents", "pending_message_count", "cancel_requested_at",
}

func sessionThreadHandlerRow(threadID, sessionID, orgID uuid.UUID, status models.ThreadStatus, turn int, now time.Time) []interface{} {
	return []interface{}{
		threadID, sessionID, orgID, models.AgentTypeClaudeCode, nil,
		"Main", nil, nil, status, nil, turn, &now,
		nil, nil, nil, nil,
		nil, nil, now, nil,
		nil, float64(0), 0, nil,
	}
}

func retrySessionRow(sessionID, orgID uuid.UUID, status models.SessionStatus, snapshotKey *string, pendingSnapshotKey *string, sandboxState models.SandboxState, diffStats json.RawMessage, now time.Time) []interface{} {
	values := map[string]interface{}{
		"id":                             sessionID,
		"primary_issue_id":               nil,
		"org_id":                         orgID,
		"origin":                         models.SessionOriginManual,
		"interaction_mode":               models.SessionInteractionModeInteractive,
		"validation_policy":              models.SessionValidationPolicyOnTurnComplete,
		"agent_type":                     models.AgentTypeClaudeCode,
		"status":                         status,
		"autonomy_level":                 models.SessionAutonomySemi,
		"token_mode":                     models.SessionTokenModeLow,
		"complexity_tier":                nil,
		"container_id":                   nil,
		"worker_node_id":                 nil,
		"turn_holding_container":         false,
		"started_at":                     nil,
		"completed_at":                   nil,
		"token_usage":                    nil,
		"failure_explanation":            nil,
		"failure_category":               nil,
		"failure_next_steps":             nil,
		"failure_retry_advised":          false,
		"parent_session_id":              nil,
		"revision_context":               nil,
		"error":                          nil,
		"result_summary":                 nil,
		"diff":                           ptr("diff --git a/file b/file"),
		"pm_plan_id":                     nil,
		"title":                          nil,
		"pm_approach":                    nil,
		"pm_reasoning":                   nil,
		"project_task_id":                nil,
		"model_override":                 nil,
		"reasoning_effort":               nil,
		"triggered_by_user_id":           nil,
		"agent_session_id":               nil,
		"current_turn":                   1,
		"last_activity_at":               now,
		"sandbox_state":                  sandboxState,
		"snapshot_key":                   snapshotKey,
		"pending_snapshot_key":           pendingSnapshotKey,
		"pending_snapshot_set_at":        nil,
		"runtime_soft_deadline_at":       nil,
		"runtime_hard_deadline_at":       nil,
		"runtime_last_progress_at":       nil,
		"runtime_last_progress_type":     models.RuntimeProgressType(""),
		"runtime_last_progress_strength": models.RuntimeProgressStrength(""),
		"runtime_extension_count":        0,
		"runtime_extension_seconds":      0,
		"runtime_stop_reason":            models.RuntimeStopReason(""),
		"runtime_graceful_stop_at":       nil,
		"checkpointed_at":                nil,
		"checkpoint_kind":                models.CheckpointKind(""),
		"checkpoint_capability":          models.CheckpointCapability(""),
		"checkpoint_size_bytes":          int64(0),
		"checkpoint_error":               nil,
		"recovery_state":                 models.RecoveryState(""),
		"recovery_queued_at":             nil,
		"recovery_started_at":            nil,
		"recovery_attempt_count":         0,
		"target_branch":                  nil,
		"working_branch":                 nil,
		"base_commit_sha":                nil,
		"repository_id":                  nil,
		"diff_stats":                     diffStats,
		"diff_history":                   json.RawMessage(`[{"files_changed":1}]`),
		"input_manifest":                 nil,
		"archived_at":                    nil,
		"archived_by_user_id":            nil,
		"automation_run_id":              nil,
		"pr_creation_state":              models.PRCreationStateIdle,
		"pr_creation_error":              nil,
		"pr_push_state":                  models.PRPushStateIdle,
		"pr_push_error":                  nil,
		"branch_creation_state":          models.BranchCreationStateIdle,
		"branch_creation_error":          nil,
		"branch_url":                     nil,
		"diff_collected_at":              nil,
		"latest_diff_snapshot_id":        nil,
		"has_unpushed_changes":           false,
		"linear_private":                 false,
		"linear_state_sync_disabled":     false,
		"linear_identifier_hint":         nil,
		"linear_prepare_state":           models.LinearPrepareStateNone,
		"deleted_at":                     nil,
		"git_identity_source":            nil,
		"git_identity_user_id":           nil,
		"created_at":                     now,
	}
	row := make([]interface{}, len(sessionColumns))
	for i, column := range sessionColumns {
		row[i] = values[column]
	}
	return row
}

func expectManualSessionCreate(mock pgxmock.PgxPoolIface, runID uuid.UUID, now time.Time) {
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(sessionAnyArgs(26)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "last_activity_at"}).AddRow(runID, now, now))
	mock.ExpectQuery("INSERT INTO session_threads").
		WithArgs(sessionAnyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectCommit()
}

func expectIssueSessionCreate(mock pgxmock.PgxPoolIface, runID uuid.UUID, now time.Time) {
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(sessionAnyArgs(26)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "last_activity_at"}).AddRow(runID, now, now))
	mock.ExpectQuery("INSERT INTO session_threads").
		WithArgs(sessionAnyArgs(6)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectExec("INSERT INTO session_issue_links").
		WithArgs(sessionAnyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()
}

func TestSessionHandler_List(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedLen  int
	}{
		{
			name: "returns agent runs for org successfully",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				runID := uuid.New()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil,                      // project_task_id
							nil,                      // model_override
							nil,                      // triggered_by_user_id
							nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedLen:  1,
		},
		{
			name: "returns empty list when no runs exist",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.List(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			var resp models.ListResponse[models.Session]
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "response body should be valid JSON")
			require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of runs")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_List_WithRepositoryID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now()
	runID := uuid.New()
	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ repository_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?repository_id="+repoID.String(), nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 when filtering by repository_id")

	var resp models.ListResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return filtered sessions")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_List_InvalidRepositoryID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?repository_id=not-a-uuid", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid repository_id")
	require.Contains(t, w.Body.String(), "INVALID_REPOSITORY_ID", "error code should indicate invalid repository_id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_List_InvalidStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?status=bogus", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid status")
	require.Contains(t, w.Body.String(), "INVALID_STATUS", "error code should indicate invalid status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_List_CommaSeparatedStatuses(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now()
	runID := uuid.New()
	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ AND status = ANY").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "pending", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 0, now, "none", nil,
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?status=pending,running", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 for comma-separated statuses")

	var resp models.ListResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return filtered sessions")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionCursorRoundTrip(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Nanosecond)
	id := uuid.New()

	encoded := encodeSessionCursor(now, id)
	decodedTime, decodedID, err := decodeSessionCursor(encoded)
	require.NoError(t, err)
	require.True(t, now.Equal(decodedTime), "decoded time should match")
	require.Equal(t, id, decodedID, "decoded ID should match")
}

func TestDecodeSessionCursor_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cursor string
	}{
		{name: "not base64", cursor: "!!!invalid!!!"},
		{name: "missing comma", cursor: "bm9jb21tYQ=="},                                                 // "nocomma"
		{name: "bad timestamp", cursor: "YmFkdGltZSwwMTIzNDU2Ny04OWFiLWNkZWYtMDEyMy00NTY3ODlhYmNkZWY="}, // "badtime,..."
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := decodeSessionCursor(tt.cursor)
			require.Error(t, err)
		})
	}
}

func TestSessionHandler_List_WithCursor(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now()
	runID := uuid.New()
	issueID := uuid.New()
	cursorTime := now.Add(-time.Hour)
	cursorID := uuid.New()
	cursor := encodeSessionCursor(cursorTime, cursorID)

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ AND \\(last_activity_at, id\\) <").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil, nil,
				nil, 0, now, "none", nil,
				nil, nil, nil, nil, nil, nil, nil, nil, nil, "idle", (*string)(nil), nil, now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?cursor="+cursor, nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 with cursor")

	var resp models.ListResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return sessions after cursor")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestSessionHandler_List_EmitsCursorWhenFull exercises the nextCursor emission
// path: when the DB returns exactly `limit` rows, the handler must encode the
// last row's last_activity_at (the MRU sort key) into the returned cursor so
// callers can page from the correct position.
func TestSessionHandler_List_EmitsCursorWhenFull(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now().UTC().Truncate(time.Nanosecond)
	runID := uuid.New()
	issueID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil, nil,
				nil, 0, now, "none", nil,
				nil, nil, nil, nil, nil, nil, nil, nil, nil, "idle", (*string)(nil), nil, now,
			),
		)

	// Request exactly one row so len(runs) == limit and the cursor is emitted.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?limit=1", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.SessionListItem]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Equal(t, 1, len(resp.Data))
	require.NotEmpty(t, resp.Meta.NextCursor, "next cursor must be set when page is full")

	// Cursor must encode (last_activity_at, id) so pagination continues in MRU order.
	decodedTime, decodedID, err := decodeSessionCursor(resp.Meta.NextCursor)
	require.NoError(t, err)
	require.True(t, now.Equal(decodedTime), "cursor time must be last_activity_at of last row")
	require.Equal(t, runID, decodedID, "cursor id must be id of last row")
}

func TestSessionHandler_List_DoesNotMarshalRawDiffPayloads(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)
	now := time.Now().UTC()
	runID := uuid.New()
	largeDiffMarker := "diff --git a/huge b/huge\n+raw payload must not reach session list"
	diffHistory := json.RawMessage(`[{"pass":1,"diff":"raw history must not reach session list","diff_stats":{"added":1,"removed":0,"files_changed":1},"created_at":"2026-01-01T00:00:00Z"}]`)

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(runID, uuid.Nil, orgID, now, pushSessionRowOpts{
			diff:        &largeDiffMarker,
			diffHistory: diffHistory,
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusOK, w.Code, "session list should succeed: %s", w.Body.String())
	require.NotContains(t, w.Body.String(), "raw payload must not reach session list", "session list should not marshal raw diff content")
	require.NotContains(t, w.Body.String(), "raw history must not reach session list", "session list should not marshal raw diff history")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_List_InvalidCursor(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions?cursor=invalid", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.List(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid cursor")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_Counts(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("(?s)SELECT.*all_count.*active_count.*archived_count").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"all_count", "active_count", "archived_count"}).
				AddRow(42, 7, 3),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/counts", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Counts(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200")

	var resp models.SingleResponse[models.SessionCounts]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 42, resp.Data.All, "all count should pass through")
	require.Equal(t, 7, resp.Data.Active, "active count should pass through")
	require.Equal(t, 3, resp.Data.Archived, "archived count should pass through")
	require.Greater(t, resp.Data.Cap, 0, "cap should be populated so clients can render 99+ correctly")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_Counts_WithScopeFilters(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	repoID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("(?s)SELECT.*repository_id.*triggered_by_user_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"all_count", "active_count", "archived_count"}).
				AddRow(5, 2, 1),
		)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sessions/counts?repository_id="+repoID.String()+"&triggered_by_user_id="+userID.String(), nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Counts(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 with scope filters")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_Counts_WithTriggeredByUserIDs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	userID1 := uuid.New()
	userID2 := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery(`(?s)SELECT.*triggered_by_user_id = ANY`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"all_count", "active_count", "archived_count"}).
				AddRow(5, 2, 1),
		)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sessions/counts?triggered_by_user_ids="+userID1.String()+","+userID2.String(), nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Counts(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 with triggered_by_user_ids")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_Counts_InvalidRepositoryID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/counts?repository_id=not-a-uuid", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Counts(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should reject invalid repository_id")
	require.Contains(t, w.Body.String(), "INVALID_REPOSITORY_ID")
}

func TestSessionHandler_Counts_InvalidUserID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/counts?triggered_by_user_id=bad", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Counts(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should reject invalid triggered_by_user_id")
	require.Contains(t, w.Body.String(), "INVALID_USER_ID")
}

func TestSessionHandler_Counts_InvalidUserIDs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/counts?triggered_by_user_ids=bad", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Counts(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should reject invalid triggered_by_user_ids")
	require.Contains(t, w.Body.String(), "INVALID_USER_ID")
}

func TestSessionHandler_Counts_BlankUserIDs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/counts?triggered_by_user_ids=,,,", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Counts(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should reject blank triggered_by_user_ids")
	require.Contains(t, w.Body.String(), "INVALID_USER_ID")
}

func TestSessionHandler_Counts_EmptyUserIDs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/counts?triggered_by_user_ids=", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Counts(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should reject empty triggered_by_user_ids")
	require.Contains(t, w.Body.String(), "INVALID_USER_ID")
}

func TestSessionHandler_Counts_WhitespaceUserIDs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/counts?triggered_by_user_ids=%20", nil)
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Counts(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should reject whitespace triggered_by_user_ids")
	require.Contains(t, w.Body.String(), "INVALID_USER_ID")
}

func TestSessionHandler_Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		idParam      string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name:    "returns agent run by ID successfully",
			idParam: "", // will be set to a valid UUID in the subtest
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				runID := uuid.New()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							runID, issueID, orgID, "claude-code", "running", "supervised", "standard",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil,                      // project_task_id
							nil,                      // model_override
							nil,                      // triggered_by_user_id
							nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "running",
		},
		{
			name:         "returns bad request for invalid UUID",
			idParam:      "invalid",
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID)

			idParam := tt.idParam
			if idParam == "" {
				idParam = uuid.New().String()
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+idParam, nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", idParam)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Get(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_Get_AttachesThreadInboxDeliverySummary(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	threadID := uuid.New()
	now := time.Now().UTC()
	lastError := "delivery needs operator review"
	handler := newSessionHandler(t, mock)
	handler.SetThreadInboxStore(db.NewThreadInboxStore(mock))

	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionHandlerDetailRow(runID, orgID, now)...),
		)
	mock.ExpectQuery("(?s)SELECT .* FROM session_threads").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionThreadHandlerTestColumns()).AddRow(
			threadID, runID, orgID, "claude_code", nil,
			"Backend", nil, nil, "running", nil,
			2, &now,
			nil, nil, nil, nil,
			&now, nil, now,
			nil, nil, float64(0), 0, nil,
		))
	mock.ExpectQuery("(?s)SELECT .* FROM thread_inbox_entries").
		WithArgs(orgID, runID).
		WillReturnRows(pgxmock.NewRows(threadInboxSummaryHandlerTestColumns()).AddRow(
			threadID, 1, 0, 2, 1, 4, 0, int64(8), now, now.Add(time.Second), now.Add(2*time.Second), lastError,
		))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Get(w, req)

	require.Equal(t, http.StatusOK, w.Code, "should return the session detail")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	var resp struct {
		Data struct {
			Threads []models.SessionThread `json:"threads"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "response body should decode as session detail")
	require.Len(t, resp.Data.Threads, 1, "session detail should include thread tabs")
	require.NotNil(t, resp.Data.Threads[0].InboxDelivery, "thread tab should include inbox delivery summary")
	require.Equal(t, models.ThreadInboxSummaryStateUnknownDelivery, resp.Data.Threads[0].InboxDelivery.State, "unknown delivery should dominate delivered and acked counts")
	require.Equal(t, 1, resp.Data.Threads[0].InboxDelivery.PendingCount, "summary should include pending entries")
	require.Equal(t, 1, resp.Data.Threads[0].InboxDelivery.UnknownDeliveryCount, "summary should include unknown delivery entries")
	require.Equal(t, int64(8), resp.Data.Threads[0].InboxDelivery.LastSequenceNo, "summary should include the latest sequence")
	require.Equal(t, &lastError, resp.Data.Threads[0].InboxDelivery.LastError, "summary should include the latest delivery error")
}

func sessionThreadHandlerTestColumns() []string {
	return []string{
		"id", "session_id", "org_id", "agent_type", "model_override",
		"label", "instructions", "file_scope", "status", "agent_session_id",
		"current_turn", "last_activity_at",
		"result_summary", "diff", "failure_explanation", "failure_category",
		"started_at", "completed_at", "created_at",
		"archived_at", "base_snapshot_key", "cost_cents", "pending_message_count", "cancel_requested_at",
	}
}

func sessionHandlerDetailRow(sessionID, orgID uuid.UUID, now time.Time) []interface{} {
	row := make([]interface{}, len(sessionColumns))
	for i, column := range sessionColumns {
		switch column {
		case "id":
			row[i] = sessionID
		case "primary_issue_id":
			row[i] = nil
		case "org_id":
			row[i] = orgID
		case "origin":
			row[i] = string(models.SessionOriginManual)
		case "interaction_mode":
			row[i] = string(models.SessionInteractionModeInteractive)
		case "validation_policy":
			row[i] = string(models.SessionValidationPolicyOnTurnComplete)
		case "agent_type":
			row[i] = string(models.AgentTypeClaudeCode)
		case "status":
			row[i] = string(models.SessionStatusRunning)
		case "autonomy_level":
			row[i] = string(models.SessionAutonomySupervised)
		case "token_mode":
			row[i] = string(models.SessionTokenModeLow)
		case "turn_holding_container", "failure_retry_advised", "has_unpushed_changes",
			"linear_private", "linear_state_sync_disabled":
			row[i] = false
		case "started_at":
			row[i] = &now
		case "current_turn", "runtime_extension_count", "runtime_extension_seconds",
			"recovery_attempt_count":
			row[i] = 0
		case "last_activity_at", "created_at":
			row[i] = now
		case "sandbox_state":
			row[i] = string(models.SandboxStateNone)
		case "runtime_last_progress_type", "runtime_last_progress_strength",
			"runtime_stop_reason", "checkpoint_kind", "checkpoint_capability", "recovery_state":
			row[i] = ""
		case "checkpoint_size_bytes":
			row[i] = int64(0)
		case "pr_creation_state":
			row[i] = string(models.PRCreationStateIdle)
		case "pr_push_state":
			row[i] = string(models.PRPushStateIdle)
		case "branch_creation_state":
			row[i] = string(models.BranchCreationStateIdle)
		case "linear_prepare_state":
			row[i] = string(models.LinearPrepareStateNone)
		default:
			row[i] = nil
		}
	}
	return row
}

func threadInboxSummaryHandlerTestColumns() []string {
	return []string{
		"thread_id", "pending_count", "delivering_count", "delivered_count",
		"unknown_delivery_count", "acked_count", "dead_letter_count", "last_sequence_no",
		"last_accepted_at", "last_delivered_at", "last_acked_at", "last_error",
	}
}

// triggerFixIssueMock sets up the common mock for a successful issue lookup,
// agent run creation, and job enqueue for TriggerFix tests.
func triggerFixIssueMock(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
	now := time.Now()
	issueID := uuid.New()
	repoID := uuid.New()

	// Mock issue lookup
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at", "deleted_at",
			}).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, []byte(repoID.String()),
				"Test issue", nil, nil, "open", now, now,
				1, 0, "medium", nil, "fp-1",
				now, now, nil,
			),
		)

	runID := uuid.New()
	expectIssueSessionCreate(mock, runID, now)

	// Mock job enqueue (6 named args)
	jobID := uuid.New()
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
}

func triggerFixIssueAndOrgDefaultMock(mock pgxmock.PgxPoolIface, orgID uuid.UUID, defaultAgentType string) {
	issueID := uuid.New()
	now := time.Now()
	repoID := uuid.New()
	settings := fmt.Sprintf(`{"default_agent_type":"%s"}`, defaultAgentType)

	// Mock issue lookup
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at", "deleted_at",
			}).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, []byte(repoID.String()),
				"Test issue", nil, nil, "open", now, now,
				1, 0, "medium", nil, "fp-1",
				now, now, nil,
			),
		)

	// Mock org lookup for default agent type.
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
				AddRow(orgID, "Acme", []byte(settings), now, now),
		)

	runID := uuid.New()
	expectIssueSessionCreate(mock, runID, now)

	// Mock job enqueue (6 named args)
	jobID := uuid.New()
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
}

func TestSessionHandler_TriggerFix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		idParam      string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name:    "triggers fix with org default agent type when request omits agent_type",
			idParam: "",
			body:    "",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				triggerFixIssueAndOrgDefaultMock(mock, orgID, "gemini_cli")
			},
			expectedCode: http.StatusCreated,
			expectedBody: "gemini_cli",
		},
		{
			name:    "falls back to codex when org default agent type is missing",
			idParam: "",
			body:    "",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				triggerFixIssueAndOrgDefaultMock(mock, orgID, "")
			},
			expectedCode: http.StatusCreated,
			expectedBody: "codex",
		},
		{
			name:         "triggers fix with gemini_cli agent type",
			idParam:      "",
			body:         `{"agent_type":"gemini_cli"}`,
			setupMock:    triggerFixIssueMock,
			expectedCode: http.StatusCreated,
			expectedBody: "gemini_cli",
		},
		{
			name:         "triggers fix with codex agent type",
			idParam:      "",
			body:         `{"agent_type":"codex"}`,
			setupMock:    triggerFixIssueMock,
			expectedCode: http.StatusCreated,
			expectedBody: "codex",
		},
		{
			name:         "triggers fix with high token mode",
			idParam:      "",
			body:         `{"agent_type":"codex","token_mode":"high"}`,
			setupMock:    triggerFixIssueMock,
			expectedCode: http.StatusCreated,
			expectedBody: "high",
		},
		{
			name:    "rejects invalid agent type",
			idParam: "",
			body:    `{"agent_type":"invalid_agent"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{
							"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
							"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
							"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
							"created_at", "updated_at", "deleted_at",
						}).AddRow(
							issueID, orgID, "ISSUE-1", "sentry", nil, nil,
							"Test issue", nil, nil, "open", now, now,
							1, 0, "medium", nil, "fp-1",
							now, now, nil,
						),
					)
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_AGENT_TYPE",
		},
		{
			name:    "rejects invalid token mode",
			idParam: "",
			body:    `{"agent_type":"codex","token_mode":"extreme"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				issueID := uuid.New()
				mock.ExpectQuery("SELECT").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{
							"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
							"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
							"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
							"created_at", "updated_at", "deleted_at",
						}).AddRow(
							issueID, orgID, "ISSUE-1", "sentry", nil, nil,
							"Test issue", nil, nil, "open", now, now,
							1, 0, "medium", nil, "fp-1",
							now, now, nil,
						),
					)
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_TOKEN_MODE",
		},
		{
			name:         "returns bad request for invalid issue ID",
			idParam:      "bad-id",
			body:         "",
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID)

			idParam := tt.idParam
			if idParam == "" {
				idParam = uuid.New().String()
			}

			var bodyReader *strings.Reader
			if tt.body != "" {
				bodyReader = strings.NewReader(tt.body)
			} else {
				bodyReader = strings.NewReader("")
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/issues/"+idParam+"/fix", bodyReader)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", idParam)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.TriggerFix(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_ListQuestions_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	qID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM session_questions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "session_id", "org_id", "question_text", "options", "context",
				"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
			}).AddRow(
				qID, runID, orgID, "Which fix approach?", nil, nil,
				nil, nil, nil, nil, "pending", now,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String()+"/questions", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListQuestions(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK for questions list")

	var resp models.ListResponse[models.SessionQuestion]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 1, len(resp.Data), "should return one question for the run")
	require.Equal(t, "Which fix approach?", resp.Data[0].QuestionText, "should return the question text")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_AnswerQuestion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, runID uuid.UUID, qID uuid.UUID, userID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name: "answers question successfully",
			body: `{"answer": "Option A"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, runID uuid.UUID, qID uuid.UUID, userID uuid.UUID) {
				now := time.Now()

				// Mock answer update
				mock.ExpectExec("UPDATE session_questions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))

				// Mock get by ID after answer
				mock.ExpectQuery("SELECT .+ FROM session_questions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{
							"id", "session_id", "org_id", "question_text", "options", "context",
							"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
						}).AddRow(
							qID, runID, orgID, "Which fix?", nil, nil,
							nil, stringPtr("Option A"), &userID, &now, "answered", now,
						),
					)
			},
			expectedCode: http.StatusOK,
			expectedBody: "answered",
		},
		{
			name:         "returns bad request when answer is empty",
			body:         `{"answer": ""}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID, runID uuid.UUID, qID uuid.UUID, userID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_ANSWER",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			runID := uuid.New()
			qID := uuid.New()
			userID := uuid.New()

			handler := newSessionHandler(t, mock)
			tt.setupMock(mock, orgID, runID, qID, userID)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runID.String()+"/questions/"+qID.String()+"/answer", strings.NewReader(tt.body))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", runID.String())
			rctx.URLParams.Add("qid", qID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.AnswerQuestion(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_GetPullRequest_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	prID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionPullRequestColumns).AddRow(
				sessionPullRequestRow(prID, &runID, orgID, "org/repo", now)...,
			),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String()+"/pull-request", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetPullRequest(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return 200 OK")

	var resp models.SingleResponse[models.PullRequest]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Equal(t, 42, resp.Data.GitHubPRNumber, "should return the PR number")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetPullRequest_NoPR_Returns200Null(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()

	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String()+"/pull-request", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetPullRequest(w, req)
	require.Equal(t, http.StatusOK, w.Code, "empty state should be 200, not 404")
	require.JSONEq(t, `{"data":null}`, w.Body.String(), "body should be data:null")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetPullRequest_DBError_Returns500(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()

	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("db exploded"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+runID.String()+"/pull-request", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetPullRequest(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code, "real DB errors should 500, not 200")
	require.Contains(t, w.Body.String(), "INTERNAL_ERROR", "error code should surface")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetPullRequest_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/bad-id/pull-request", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetPullRequest(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_ListQuestions_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/bad-id/questions", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListQuestions(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_AnswerQuestion_InvalidQID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runID.String()+"/questions/bad-id/answer", strings.NewReader(`{"answer":"yes"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	rctx.URLParams.Add("qid", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.AnswerQuestion(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid question ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_AnswerQuestion_NoUser(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	qID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+runID.String()+"/questions/"+qID.String()+"/answer", strings.NewReader(`{"answer":"yes"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	rctx.URLParams.Add("qid", qID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	// No user set in context
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.AnswerQuestion(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code, "should return 401 when no user in context")
	require.Contains(t, w.Body.String(), "UNAUTHORIZED")
}

func TestSessionHandler_TriggerFix_InvalidAutonomyLevel(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	now := time.Now()
	mock.ExpectQuery("SELECT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
				"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
				"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
				"created_at", "updated_at", "deleted_at",
			}).AddRow(
				issueID, orgID, "ISSUE-1", "sentry", nil, nil,
				"Test issue", nil, nil, "open", now, now,
				1, 0, "medium", nil, "fp-1",
				now, now, nil,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/issues/"+issueID.String()+"/fix", strings.NewReader(`{"agent_type":"codex","autonomy_level":"chaos"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", issueID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.TriggerFix(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid autonomy level")
	require.Contains(t, w.Body.String(), "INVALID_AUTONOMY_LEVEL")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetLogs_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	// Mock session lookup.
	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	// Mock log listing.
	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(1), runID, orgID, nil, now, "info", "Starting agent", nil, nil).
				AddRow(int64(2), runID, orgID, nil, now, "info", "Agent completed", nil, nil),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String()+"/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetLogs(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.SessionLog]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Equal(t, 2, len(resp.Data))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetLogs_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/bad-id/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetLogs(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_GetLogs_EmptyLogs(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	now := time.Now()

	handler := newSessionHandler(t, mock)

	issueID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String()+"/logs", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetLogs(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp models.ListResponse[models.SessionLog]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.Equal(t, 0, len(resp.Data))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_GetTimeline_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(
				pgxmock.NewRows(sessionColumns),
				sessionID, uuid.New(), orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                             // triggered_by_user_id
				nil, 1, now, "snapshotted", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM session_messages WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(messageColumns).
				AddRow(int64(1), sessionID, orgID, nil, nil, 1, "assistant", "Done fixing", nil, nil, nil, nil, now),
		)
	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(10), sessionID, orgID, nil, now.Add(-time.Minute), "output", "Done fixing", nil, 1),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/timeline", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.GetTimeline(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should return expected status code")

	var resp models.ListResponse[models.SessionTimelineEntry]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response body should be valid JSON")
	require.Len(t, resp.Data, 1, "duplicate final output log should be suppressed from fetched timeline")
	require.Equal(t, models.SessionTimelineKindMessage, resp.Data[0].Kind, "assistant transcript should remain visible")
	require.NotNil(t, resp.Data[0].Message, "timeline message entry should include message payload")
	require.Equal(t, "Done fixing", resp.Data[0].Message.Content, "timeline should return the persisted assistant transcript")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_GetTimeline_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		sessionID    string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID)
		expectedCode int
	}{
		{
			name:         "invalid session id",
			sessionID:    "not-a-uuid",
			expectedCode: http.StatusBadRequest,
		},
		{
			name:      "session not found",
			sessionID: uuid.New().String(),
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(pgx.ErrNoRows)
			},
			expectedCode: http.StatusNotFound,
		},
		{
			name:      "message listing failure",
			sessionID: uuid.New().String(),
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(
							pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "completed", "supervised", "standard",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", nil,
							nil, nil, nil, nil, nil, nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("SELECT .+ FROM session_messages WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("boom"))
			},
			expectedCode: http.StatusInternalServerError,
		},
		{
			name:      "log listing failure",
			sessionID: uuid.New().String(),
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(
							pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "completed", "supervised", "standard",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", nil,
							nil, nil, nil, nil, nil, nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("SELECT .+ FROM session_messages WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(messageColumns))
				mock.ExpectQuery("SELECT .+ FROM session_logs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("boom"))
			},
			expectedCode: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			orgID := uuid.New()
			sessionUUID := uuid.New()
			if tt.sessionID != "not-a-uuid" {
				sessionUUID = uuid.MustParse(tt.sessionID)
			}
			handler := newSessionHandler(t, mock)
			if tt.setupMock != nil {
				tt.setupMock(mock, orgID, sessionUUID)
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+tt.sessionID+"/timeline", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", tt.sessionID)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.GetTimeline(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "GetTimeline should return the expected status code")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_StreamLogs_TerminalRun(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	now := time.Now()
	issueID := uuid.New()

	handler := newSessionHandler(t, mock)

	// Mock session lookup — terminal status triggers GetLogs fallback.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	// GetLogs path: second session lookup + log listing.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(1), runID, orgID, nil, now, "info", "Done", nil, nil),
		)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String()+"/logs/stream", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.StreamLogs(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_StreamLogs_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/bad-id/logs/stream", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.StreamLogs(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

// stubSessionMembershipStore implements sessionMembershipStore for unit tests
// without spinning up Postgres. allowed lists (userID, orgID) pairs that
// resolve to a membership; everything else returns pgx.ErrNoRows. errOverride
// short-circuits Get to a non-ErrNoRows failure for the generic-error branch.
type stubSessionMembershipStore struct {
	allowed     map[[2]uuid.UUID]bool
	errOverride error
}

func (s *stubSessionMembershipStore) Get(_ context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error) {
	if s.errOverride != nil {
		return models.OrganizationMembership{}, s.errOverride
	}
	if s.allowed[[2]uuid.UUID{userID, orgID}] {
		return models.OrganizationMembership{UserID: userID, OrgID: orgID, Role: "member"}, nil
	}
	return models.OrganizationMembership{}, pgx.ErrNoRows
}

// Regression: EventSource cannot send X-Active-Org-ID, so multi-org users
// whose context org (resolved from session last_org_id) differs from the org
// they're actively viewing previously got a silent 404. The handler must
// honour the ?org_id= query fallback when the user has membership in that
// org. Mirrors pull_requests.go's prior art.
func TestSessionHandler_StreamLogs_OrgIDQueryFallback(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	contextOrgID := uuid.New()   // resolved from session hint — wrong org
	requestedOrgID := uuid.New() // active org per X-Active-Org-ID, passed via query
	userID := uuid.New()
	runID := uuid.New()
	now := time.Now()
	issueID := uuid.New()

	handler := newSessionHandler(t, mock)
	handler.SetMembershipStore(&stubSessionMembershipStore{
		allowed: map[[2]uuid.UUID]bool{{userID, requestedOrgID}: true},
	})

	// Run lookup uses the *requested* org, not the context org.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, requestedOrgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil, nil,
				nil,
				"idle",
				(*string)(nil),
				nil,
				now,
			),
		)
	// Terminal status triggers writeLogsForOrg path: second session lookup + log listing.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, requestedOrgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil, nil,
				nil,
				"idle",
				(*string)(nil),
				nil,
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(1), runID, requestedOrgID, nil, now, "info", "Done", nil, nil),
		)

	url := "/api/v1/sessions/" + runID.String() + "/logs/stream?org_id=" + requestedOrgID.String()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, contextOrgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.StreamLogs(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_StreamLogs_OrgIDQueryNonMember(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	requestedOrgID := uuid.New()
	userID := uuid.New()

	handler := newSessionHandler(t, mock)
	handler.SetMembershipStore(&stubSessionMembershipStore{
		allowed: map[[2]uuid.UUID]bool{}, // user is not in requestedOrgID
	})

	url := "/api/v1/sessions/" + uuid.New().String() + "/logs/stream?org_id=" + requestedOrgID.String()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", uuid.New().String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.StreamLogs(w, req)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "FORBIDDEN")
}

// When the client passes ?org_id= matching the context-resolved org, the
// helper short-circuits without touching the membership store. Covers the
// equality branch in streamOrgID.
func TestSessionHandler_StreamLogs_OrgIDQueryMatchesContext(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	now := time.Now()
	issueID := uuid.New()

	handler := newSessionHandler(t, mock)
	// Intentionally no SetMembershipStore: the equality short-circuit must
	// return before the membership lookup, so this would 500 if we fell through.

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", nil,
				nil, nil, nil, nil, nil, nil,
				nil, nil,
				nil,
				"idle",
				(*string)(nil),
				nil,
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", nil,
				nil, nil, nil, nil, nil, nil,
				nil, nil,
				nil,
				"idle",
				(*string)(nil),
				nil,
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(1), runID, orgID, nil, now, "info", "Done", nil, nil),
		)

	url := "/api/v1/sessions/" + runID.String() + "/logs/stream?org_id=" + orgID.String()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.StreamLogs(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestSessionHandler_StreamLogs_OrgIDQueryMalformed(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := newSessionHandler(t, mock)

	url := "/api/v1/sessions/" + uuid.New().String() + "/logs/stream?org_id=not-a-uuid"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", uuid.New().String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.StreamLogs(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_ORG")
}

func TestSessionHandler_StreamLogs_OrgIDQueryMissingUser(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	requestedOrgID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetMembershipStore(&stubSessionMembershipStore{allowed: map[[2]uuid.UUID]bool{}})

	url := "/api/v1/sessions/" + uuid.New().String() + "/logs/stream?org_id=" + requestedOrgID.String()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", uuid.New().String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	// No user injected — exercises the errSessionStreamUnauthorized path.
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.StreamLogs(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Contains(t, w.Body.String(), "UNAUTHORIZED")
}

func TestSessionHandler_StreamLogs_OrgIDQueryMembershipNotConfigured(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	requestedOrgID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock) // no SetMembershipStore — programmer error.

	url := "/api/v1/sessions/" + uuid.New().String() + "/logs/stream?org_id=" + requestedOrgID.String()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", uuid.New().String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.StreamLogs(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "INTERNAL")
}

func TestSessionHandler_StreamLogs_OrgIDQueryMembershipStoreError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	requestedOrgID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetMembershipStore(&stubSessionMembershipStore{
		allowed:     map[[2]uuid.UUID]bool{},
		errOverride: errors.New("db unreachable"),
	})

	url := "/api/v1/sessions/" + uuid.New().String() + "/logs/stream?org_id=" + requestedOrgID.String()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", uuid.New().String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.StreamLogs(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "INTERNAL")
}

// TestSessionHandler_StreamLogs_ShutdownSignal verifies that the SSE loop
// returns promptly when shutdownCh is closed, instead of blocking
// Server.Shutdown until its deadline expires.
func TestSessionHandler_StreamLogs_ShutdownSignal(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	now := time.Now()
	issueID := uuid.New()

	handler := newSessionHandler(t, mock)
	shutdownCh := make(chan struct{})
	handler.SetShutdownSignal(shutdownCh)

	// Non-terminal ("running") status triggers the SSE streaming path.
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "running", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,                      // triggered_by_user_id
				nil, 0, now, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	// Empty logs list so the initial write loop is a no-op.
	mock.ExpectQuery("SELECT .+ FROM session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String()+"/logs/stream", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.StreamLogs(w, req)
	}()

	// Close the shutdown channel; whether the handler has reached its select
	// yet or not, the for-select will pick the shutdownCh case on its first
	// iteration and return.
	close(shutdownCh)

	select {
	case <-done:
		// Expected: handler exited within deadline.
	case <-time.After(2 * time.Second):
		t.Fatal("StreamLogs did not return within 2s of shutdownCh close")
	}

	// A heartbeat is written on the shutdown path so the browser sees a
	// flush before EOF; check for its SSE comment marker.
	require.Contains(t, w.Body.String(), ": ping", "expected heartbeat on shutdown")
	require.NoError(t, mock.ExpectationsWereMet())
}

func newSessionTestStreams(t *testing.T) (*cache.SessionStreams, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "Redis client should initialize for session handler tests")
	return cache.NewSessionStreams(client, zerolog.Nop(), nil), mr
}

var sessionHandlerThreadColumns = []string{
	"id", "session_id", "org_id", "agent_type", "model_override",
	"label", "instructions", "file_scope", "status", "agent_session_id",
	"current_turn", "last_activity_at",
	"result_summary", "diff", "failure_explanation", "failure_category",
	"started_at", "completed_at", "created_at",
	"archived_at", "base_snapshot_key", "cost_cents", "pending_message_count", "cancel_requested_at",
}

func sessionHandlerThreadRow(threadID, sessionID, orgID uuid.UUID, label string, status models.ThreadStatus, turn int, now time.Time) []any {
	return []any{
		threadID, sessionID, orgID, "codex", nil,
		label, nil, nil, string(status), nil,
		turn, nil,
		nil, nil, nil, nil,
		nil, nil, now,
		nil, nil, float64(0), 0, nil,
	}
}

func extractSSEData(t *testing.T, body string, eventName string) string {
	t.Helper()

	blocks := strings.Split(body, "\n\n")
	for _, block := range blocks {
		lines := strings.Split(block, "\n")
		foundEvent := false
		var data []string
		for _, line := range lines {
			if line == "event: "+eventName {
				foundEvent = true
				continue
			}
			if strings.HasPrefix(line, "data: ") {
				data = append(data, strings.TrimPrefix(line, "data: "))
			}
		}
		if foundEvent {
			return strings.Join(data, "\n")
		}
	}
	require.Failf(t, "missing SSE event", "expected event %q in body %q", eventName, body)
	return ""
}

func TestSessionHandler_CatchUpLogs_UsesRedisRangeAndFallbacks(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	streams, _ := newSessionTestStreams(t)
	handler.SetStreams(streams)

	sessionID := uuid.New()
	orgID := uuid.New()
	require.NoError(t, streams.PublishLog(context.Background(), &models.SessionLog{ID: 1, SessionID: sessionID, OrgID: orgID, Level: "info", Message: "one", Timestamp: time.Now()}), "first log publish should succeed")
	require.NoError(t, streams.PublishLog(context.Background(), &models.SessionLog{ID: 2, SessionID: sessionID, OrgID: orgID, Level: "info", Message: "two", Timestamp: time.Now()}), "second log publish should succeed")

	logs, err := handler.catchUpLogs(context.Background(), orgID, sessionID, cache.SessionLogStreamID(1))
	require.NoError(t, err, "Redis XRANGE catch-up should succeed")
	require.Len(t, logs, 1, "Redis XRANGE catch-up should only return newer logs")
	require.Equal(t, int64(2), logs[0].ID, "Redis XRANGE catch-up should return the later log")

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	logs, err = handler.catchUpLogs(context.Background(), orgID, sessionID, "bad-id")
	require.NoError(t, err, "invalid Last-Event-ID should fall back to a full Postgres replay")
	require.Empty(t, logs, "fallback full replay should return the mocked empty result set")
}

func TestShouldSkipRedisLog(t *testing.T) {
	t.Parallel()

	seen, skip := shouldSkipRedisLog(context.Background(), cache.SessionLogStreamID(3), cache.SessionLogStreamID(4), uuid.New())
	require.True(t, skip, "older log IDs should be skipped")
	require.Equal(t, cache.SessionLogStreamID(4), seen, "skip helper should preserve the last delivered ID")

	seen, skip = shouldSkipRedisLog(context.Background(), cache.SessionLogStreamID(5), cache.SessionLogStreamID(4), uuid.New())
	require.False(t, skip, "newer log IDs should not be skipped")
	require.Equal(t, "", seen, "non-skipped entries should not override the last delivered ID")

	seen, skip = shouldSkipRedisLog(context.Background(), "bad-stream-id", cache.SessionLogStreamID(4), uuid.New())
	require.False(t, skip, "invalid current stream IDs should not be skipped")
	require.Equal(t, "", seen, "invalid current stream IDs should not preserve the last delivered ID")

	seen, skip = shouldSkipRedisLog(context.Background(), cache.SessionLogStreamID(5), "bad-last-id", uuid.New())
	require.False(t, skip, "invalid last delivered stream IDs should not skip newer entries")
	require.Equal(t, "", seen, "invalid last delivered stream IDs should not be preserved")
}

func TestSessionHandler_StreamLogsViaRedis_StatusPayloadIncludesThreads(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	streams, _ := newSessionTestStreams(t)
	handler.SetStreams(streams)

	orgID := uuid.New()
	runID := uuid.New()
	threadID := uuid.New()
	issueID := uuid.New()
	now := time.Now().UTC()
	run := models.Session{
		ID:             runID,
		OrgID:          orgID,
		PrimaryIssueID: &issueID,
		Status:         models.SessionStatusRunning,
		AgentType:      models.AgentTypeCodex,
		CurrentTurn:    1,
		CreatedAt:      now,
		LastActivityAt: now,
	}

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))
	mock.ExpectQuery("SELECT .+ FROM session_threads WHERE org_id .+ AND session_id .+ archived_at IS NULL").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionHandlerThreadColumns).
				AddRow(sessionHandlerThreadRow(threadID, runID, orgID, "Main", models.ThreadStatusRunning, 2, now)...),
		)

	rec := newLockedRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		done <- handler.streamLogsViaRedis(ctx, sw, orgID, run, "")
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(rec.BodyString(), "event: status")
	}, 2*time.Second, 20*time.Millisecond, "Redis stream helper should emit the initial status event")
	cancel()

	select {
	case ok := <-done:
		require.True(t, ok, "canceled contexts should exit the Redis stream helper cleanly")
	case <-time.After(2 * time.Second):
		t.Fatal("Redis stream helper did not return after context cancellation")
	}

	var payload struct {
		ID      uuid.UUID              `json:"id"`
		Threads []models.SessionThread `json:"threads"`
	}
	require.NoError(t, json.Unmarshal([]byte(extractSSEData(t, rec.BodyString(), "status")), &payload), "status event data should decode as SessionDetail")
	require.Equal(t, runID, payload.ID, "status payload should preserve the session id")
	require.Equal(t, []models.SessionThread{{
		ID:                  threadID,
		SessionID:           runID,
		OrgID:               orgID,
		AgentType:           models.AgentTypeCodex,
		Label:               "Main",
		Status:              models.ThreadStatusRunning,
		CurrentTurn:         2,
		CreatedAt:           now,
		CostCents:           0,
		PendingMessageCount: 0,
	}}, payload.Threads, "status payload should include current session thread state")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaRedis_FallsBackWhenRedisUnavailable(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)

	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, PrimaryIssueID: &issueID, Status: models.SessionStatusRunning}

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")

	require.False(t, handler.streamLogsViaRedis(context.Background(), sw, orgID, run, ""), "Redis stream helper should fall back when Redis subscriptions are unavailable")
	require.Empty(t, rec.Body.String(), "fallback path should not emit partial SSE output")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaRedis_ContextCanceledAfterSetup(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	streams, _ := newSessionTestStreams(t)
	handler.SetStreams(streams)

	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, PrimaryIssueID: &issueID, Status: models.SessionStatusRunning}

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() {
		done <- handler.streamLogsViaRedis(ctx, sw, orgID, run, "")
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case ok := <-done:
		require.True(t, ok, "canceled contexts should exit the Redis stream helper cleanly after setup")
	case <-time.After(2 * time.Second):
		t.Fatal("Redis stream helper did not return after context cancellation")
	}
	require.Contains(t, rec.Body.String(), "event: status", "Redis stream helper should still emit the initial status event before honoring cancellation")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaRedis_ShutdownSignalAfterSetup(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	streams, _ := newSessionTestStreams(t)
	handler.SetStreams(streams)
	shutdownCh := make(chan struct{})
	handler.SetShutdownSignal(shutdownCh)

	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, PrimaryIssueID: &issueID, Status: models.SessionStatusRunning}

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")
	close(shutdownCh)

	require.True(t, handler.streamLogsViaRedis(context.Background(), sw, orgID, run, ""), "shutdown signals should exit the Redis stream helper cleanly")
	require.Contains(t, rec.Body.String(), ": ping", "shutdown handling should emit a heartbeat before returning")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaRedis_ReplayAndStatusWriteFailures(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	streams, _ := newSessionTestStreams(t)
	handler.SetStreams(streams)

	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, PrimaryIssueID: &issueID, Status: models.SessionStatusRunning}
	require.NoError(t, streams.PublishLog(context.Background(), &models.SessionLog{ID: 11, SessionID: runID, OrgID: orgID, Level: "info", Message: "hello", Timestamp: time.Now()}), "test should seed Redis replay logs")

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(11), runID, orgID, nil, time.Now(), "info", "hello", nil, nil),
		)

	logFailWriter := &failingSSEWriter{}
	logFailSW := sse.NewWriter(logFailWriter)
	require.NotNil(t, logFailSW, "SSE writer should initialize")
	require.False(t, handler.streamLogsViaRedis(context.Background(), logFailSW, orgID, run, ""), "replay write failures should abort the Redis stream helper")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	statusFailSW := sse.NewWriter(&failingSSEWriter{failOnSubstr: "event: status"})
	require.NotNil(t, statusFailSW, "SSE writer should initialize")
	require.False(t, handler.streamLogsViaRedis(context.Background(), statusFailSW, orgID, run, ""), "initial status write failures should abort the Redis stream helper")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaRedis_SubscriptionClosureWritesRetryEvent(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	streams, mr := newSessionTestStreams(t)
	handler.SetStreams(streams)

	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, PrimaryIssueID: &issueID, Status: models.SessionStatusRunning}

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")

	done := make(chan bool, 1)
	go func() {
		done <- handler.streamLogsViaRedis(context.Background(), sw, orgID, run, "")
	}()

	time.Sleep(20 * time.Millisecond)
	mr.Close()

	select {
	case ok := <-done:
		require.True(t, ok, "subscription closures should end the Redis stream helper cleanly")
	case <-time.After(2 * time.Second):
		t.Fatal("Redis stream helper did not finish after Redis subscription closure")
	}

	body := rec.Body.String()
	require.Contains(t, body, "event: error", "subscription closures should tell the client to reconnect")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaPolling_ReplaysAndFinishes(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	// Pin the polling interval so the test isn't sensitive to the jittered
	// production range (2s–3.5s). Production uses jittered per-connection
	// intervals to avoid a synchronized N-client dogpile when Redis recovers.
	handler.SetPollIntervalForTest(20 * time.Millisecond)
	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	now := time.Now()
	run := models.Session{ID: runID, OrgID: orgID, PrimaryIssueID: &issueID, Status: models.SessionStatusRunning}

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", nil,
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id .+ sl.id >").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(9), runID, orgID, nil, now, "info", "done", nil, nil),
		)

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.streamLogsViaPolling(context.Background(), sw, orgID, run, "")
	}()

	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("polling stream helper did not finish after terminal status")
	}

	body := rec.Body.String()
	require.Contains(t, body, `event: done`, "polling stream should emit a done event for terminal statuses")
	require.Contains(t, body, `id: 9-0`, "polling stream should emit incremental log events")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaPolling_InvalidLastEventIDFallsBackAndWriteFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, PrimaryIssueID: &issueID, Status: models.SessionStatusRunning}
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}).
				AddRow(int64(3), runID, orgID, nil, now, "info", "hello", nil, nil),
		)

	sw := sse.NewWriter(&failingSSEWriter{})
	require.NotNil(t, sw, "SSE writer should initialize")

	handler.streamLogsViaPolling(context.Background(), sw, orgID, run, "bad-id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaPolling_InitialLoadFailureReturns(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, PrimaryIssueID: &issueID, Status: models.SessionStatusRunning}

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id .+ sl.id >").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), int64(2)).
		WillReturnError(context.DeadlineExceeded)

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")

	handler.streamLogsViaPolling(context.Background(), sw, orgID, run, cache.SessionLogStreamID(2))
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_StreamLogsViaPolling_ReloadFailureReturns(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	handler.SetPollIntervalForTest(20 * time.Millisecond)
	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	run := models.Session{ID: runID, OrgID: orgID, PrimaryIssueID: &issueID, Status: models.SessionStatusRunning}

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.DeadlineExceeded)

	rec := httptest.NewRecorder()
	sw := sse.NewWriter(rec)
	require.NotNil(t, sw, "SSE writer should initialize")

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.streamLogsViaPolling(context.Background(), sw, orgID, run, "")
	}()

	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("polling stream helper did not return after reload failure")
	}
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestHumanInputSSEEventType(t *testing.T) {
	t.Parallel()

	updatedMetadata, err := json.Marshal(map[string]string{"event": string(sse.EventHumanInputUpdated)})
	require.NoError(t, err, "updated metadata should marshal")

	tests := []struct {
		name       string
		log        models.SessionLog
		expected   sse.EventType
		expectedOK bool
	}{
		{
			name:       "human input without metadata is created",
			log:        models.SessionLog{Level: "human_input"},
			expected:   sse.EventHumanInputCreated,
			expectedOK: true,
		},
		{
			name:       "human input updated metadata is updated",
			log:        models.SessionLog{Level: "human_input", Metadata: updatedMetadata},
			expected:   sse.EventHumanInputUpdated,
			expectedOK: true,
		},
		{
			name:       "ordinary log has no named human input event",
			log:        models.SessionLog{Level: "output"},
			expectedOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			eventType, ok := humanInputSSEEventType(tt.log)
			require.Equal(t, tt.expectedOK, ok, "event type detection should match expectation")
			require.Equal(t, tt.expected, eventType, "event type detection should return expected event")
		})
	}
}

func TestSessionHandler_StreamLogsViaPolling_StatusAndDoneWriteFailures(t *testing.T) {
	t.Parallel()

	t.Run("initial status write failure returns immediately", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool without error")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		runID := uuid.New()
		issueID := uuid.New()
		run := models.Session{ID: runID, OrgID: orgID, PrimaryIssueID: &issueID, Status: models.SessionStatusRunning}

		mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

		sw := sse.NewWriter(&failingSSEWriter{failOnSubstr: "event: status"})
		require.NotNil(t, sw, "SSE writer should initialize")

		handler.streamLogsViaPolling(context.Background(), sw, orgID, run, "")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("shutdown heartbeat failure still returns", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool without error")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		shutdownCh := make(chan struct{})
		handler.SetShutdownSignal(shutdownCh)

		orgID := uuid.New()
		runID := uuid.New()
		issueID := uuid.New()
		run := models.Session{ID: runID, OrgID: orgID, PrimaryIssueID: &issueID, Status: models.SessionStatusRunning}

		mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

		close(shutdownCh)
		sw := sse.NewWriter(&failingSSEWriter{failOnSubstr: ": ping", failAfter: 2})
		require.NotNil(t, sw, "SSE writer should initialize")

		handler.streamLogsViaPolling(context.Background(), sw, orgID, run, "")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("status and done write failures during reload return", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name         string
			status       string
			failOnSubstr string
			failAfter    int
		}{
			{name: "status event failure", status: string(models.SessionStatusCompleted), failOnSubstr: "event: status", failAfter: 2},
			{name: "done event failure", status: string(models.SessionStatusCompleted), failOnSubstr: "event: done", failAfter: 3},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "should create mock pool without error")
				defer mock.Close()

				handler := newSessionHandler(t, mock)
				handler.SetPollIntervalForTest(20 * time.Millisecond)
				orgID := uuid.New()
				runID := uuid.New()
				issueID := uuid.New()
				now := time.Now()
				run := models.Session{ID: runID, OrgID: orgID, PrimaryIssueID: &issueID, Status: models.SessionStatusRunning}

				mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							runID, issueID, orgID, "claude-code", tt.status, "supervised", "standard",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 0, now, "none", nil,
							nil, nil, nil, nil, nil, nil, nil, nil, nil,
							"idle", (*string)(nil), nil, now,
						),
					)
				mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id .+ sl.id >").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

				sw := sse.NewWriter(&failingSSEWriter{failOnSubstr: tt.failOnSubstr, failAfter: tt.failAfter})
				require.NotNil(t, sw, "SSE writer should initialize")

				done := make(chan struct{})
				go func() {
					defer close(done)
					handler.streamLogsViaPolling(context.Background(), sw, orgID, run, "")
				}()

				select {
				case <-done:
				case <-time.After(1500 * time.Millisecond):
					t.Fatal("polling stream helper did not return after a status/done write failure")
				}

				require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
			})
		}
	})
}

func TestSessionHandler_StreamLogs_RedisFallbackToPolling(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	now := time.Now()
	handler := newSessionHandler(t, mock)
	handler.SetPollIntervalForTest(20 * time.Millisecond)
	streams, _ := newSessionTestStreams(t)
	handler.SetStreams(streams)
	handler.SetShutdownSignal(make(chan struct{}))

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				runID, issueID, orgID, "claude-code", "running", "supervised", "standard",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", nil,
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.DeadlineExceeded)
	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+runID.String()+"/logs/stream", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", runID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.StreamLogs(w, req)
	}()

	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("StreamLogs did not finish after falling back to polling")
	}
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_SSEFallbackJitterRange(t *testing.T) {
	t.Parallel()

	// Locks in the dogpile-mitigation contract: every sampled interval must
	// fall inside the documented bracket so a future change can't silently
	// regress the worst-case Postgres query rate during a Redis outage.
	// The test override path is also verified — if SetPollIntervalForTest is
	// ever broken, every other test in this file would still pass on luck
	// because the production sampler returns timing in the seconds range.
	h := &SessionHandler{}

	for i := 0; i < 200; i++ {
		got := h.sseFallbackPollInterval()
		require.GreaterOrEqual(t, got, sseFallbackPollMin, "production poll interval must respect the lower bound")
		require.LessOrEqual(t, got, sseFallbackPollMax, "production poll interval must respect the upper bound")
		hb := h.sseFallbackHeartbeatInterval()
		require.GreaterOrEqual(t, hb, sseFallbackHeartbeatMin, "production heartbeat interval must respect the lower bound")
		require.LessOrEqual(t, hb, sseFallbackHeartbeatMax, "production heartbeat interval must respect the upper bound")
	}

	h.SetPollIntervalForTest(75 * time.Millisecond)
	require.Equal(t, 75*time.Millisecond, h.sseFallbackPollInterval(), "test override should pin the poll interval to the requested value")
	require.Equal(t, 5*75*time.Millisecond, h.sseFallbackHeartbeatInterval(), "test override should derive the heartbeat from the same scale")

	h.SetPollIntervalForTest(0)
	got := h.sseFallbackPollInterval()
	require.GreaterOrEqual(t, got, sseFallbackPollMin, "passing 0 to the override should restore the default randomized sampler")
	require.LessOrEqual(t, got, sseFallbackPollMax, "passing 0 to the override should restore the default randomized sampler")
}

func TestSessionHandler_CreateManual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name: "creates manual session successfully",
			body: `{"message":"Fix the login bug","agent_type":"claude_code","references":[{"kind":"file","token":"@internal/api/handlers/sessions.go","path":"internal/api/handlers/sessions.go","display":"internal/api/handlers/sessions.go"}]}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				runID := uuid.New()
				messageID := int64(1)
				jobID := uuid.New()

				// Mock org settings lookup
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))

				expectManualSessionCreate(mock, runID, now)

				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))

				// Mock concurrency check
				mock.ExpectQuery("SELECT count").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

				// Mock job enqueue (6 named args)
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "claude_code",
		},
		{
			name:         "returns bad request for invalid reference kind",
			body:         `{"message":"Fix bug","references":[{"kind":"unknown","display":"Unknown"}]}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_REFERENCES",
		},
		{
			name:         "returns bad request for malformed command",
			body:         `{"message":"/review","commands":[{"kind":"command","agent_type":"claude_code","name":"review","display":"/review"}]}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_COMMANDS",
		},
		{
			name: "rejects commands targeting a different agent than the session",
			body: `{"message":"/diff","agent_type":"claude_code","commands":[{"kind":"command","agent_type":"codex","name":"diff","token":"/diff","display":"/diff"}]}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "COMMAND_AGENT_MISMATCH",
		},
		{
			name: "uses org default agent type when not specified",
			body: `{"message":"Fix the login bug"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				runID := uuid.New()
				messageID := int64(1)
				jobID := uuid.New()

				// Mock org lookup for default agent type.
				mock.ExpectQuery("SELECT .+ FROM organizations").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
							AddRow(orgID, "Acme", []byte(`{"default_agent_type":"gemini_cli"}`), now, now),
					)

				expectManualSessionCreate(mock, runID, now)

				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))

				// Mock concurrency check
				mock.ExpectQuery("SELECT count").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

				// Mock job enqueue
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "gemini_cli",
		},
		{
			name:         "returns bad request for empty message",
			body:         `{"message":"  ","agent_type":"claude_code"}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_MESSAGE",
		},
		{
			name: "creates manual session successfully with only images",
			body: `{"message":"  ","images":["https://example.com/mobile-shot.png"],"agent_type":"claude_code"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				runID := uuid.New()
				messageID := int64(1)
				jobID := uuid.New()

				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))

				expectManualSessionCreate(mock, runID, now)

				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))

				mock.ExpectQuery("SELECT count").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "claude_code",
		},
		{
			name:         "returns bad request for invalid body",
			body:         `{invalid`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_BODY",
		},
		{
			name: "returns bad request for invalid agent type",
			body: `{"message":"Fix bug","agent_type":"invalid"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_AGENT_TYPE",
		},
		{
			name: "returns bad request for invalid autonomy level",
			body: `{"message":"Fix bug","agent_type":"claude_code","autonomy_level":"chaos"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_AUTONOMY_LEVEL",
		},
		{
			name: "returns bad request for invalid token mode",
			body: `{"message":"Fix bug","agent_type":"claude_code","token_mode":"extreme"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_TOKEN_MODE",
		},
		{
			name: "accepts xhigh reasoning effort for supported agent type",
			body: `{"message":"Fix bug","agent_type":"codex","reasoning_effort":"xhigh"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				runID := uuid.New()
				messageID := int64(1)
				jobID := uuid.New()

				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))

				expectManualSessionCreate(mock, runID, now)

				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))

				mock.ExpectQuery("SELECT count").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(),
					).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "codex",
		},
		{
			name: "accepts max reasoning effort for Claude Code",
			body: `{"message":"Fix bug","agent_type":"claude_code","reasoning_effort":"max"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				runID := uuid.New()
				messageID := int64(1)
				jobID := uuid.New()

				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))

				expectManualSessionCreate(mock, runID, now)

				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(messageID, now))

				mock.ExpectQuery("SELECT count").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(),
					).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "claude_code",
		},
		{
			name: "returns bad request for invalid reasoning effort",
			body: `{"message":"Fix bug","agent_type":"claude_code","reasoning_effort":"turbo"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_REASONING_EFFORT",
		},
		{
			name: "returns bad request when reasoning effort level is unsupported for agent type",
			body: `{"message":"Fix bug","agent_type":"codex","reasoning_effort":"max"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_REASONING_EFFORT",
		},
		{
			name: "returns bad request when reasoning effort is unsupported for agent type",
			body: `{"message":"Fix bug","agent_type":"gemini_cli","reasoning_effort":"high"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
						AddRow(orgID, "test-org", nil, now, now))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_REASONING_EFFORT",
		},
		{
			name:         "returns bad request for invalid branch characters",
			body:         `{"message":"Fix bug","agent_type":"claude_code","branch":"main..exploit"}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_BRANCH",
		},
		{
			name:         "returns bad request for invalid repository_id format",
			body:         `{"message":"Fix bug","agent_type":"claude_code","repository_id":"not-a-uuid"}`,
			setupMock:    func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_REPOSITORY_ID",
		},
		{
			name: "returns not found for non-existent repository",
			body: `{"message":"Fix bug","agent_type":"claude_code","repository_id":"` + uuid.New().String() + `"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				mock.ExpectQuery("SELECT").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "org_id", "platform", "platform_id", "full_name",
						"default_branch", "installed_at", "created_at", "updated_at",
					}))
			},
			expectedCode: http.StatusNotFound,
			expectedBody: "REPOSITORY_NOT_FOUND",
		},
		{
			name: "rejects creation against a disconnected repository",
			body: `{"message":"Fix bug","agent_type":"claude_code","repository_id":"` + uuid.New().String() + `"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID uuid.UUID) {
				now := time.Now()
				cols := []string{
					"id", "org_id", "integration_id", "github_id", "full_name", "default_branch",
					"private", "language", "description", "clone_url", "installation_id", "status",
					"last_synced_at", "context_quality", "settings", "created_at", "updated_at",
				}
				mock.ExpectQuery("SELECT .+ FROM repositories WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(cols).AddRow(
						uuid.New(), orgID, uuid.New(), int64(1), "org/repo", "main",
						false, nil, nil, "https://github.com/org/repo.git", int64(1),
						"disconnected", nil, nil, json.RawMessage(`{}`), now, now,
					))
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "REPO_DISCONNECTED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual", strings.NewReader(tt.body))
			ctx := middleware.WithOrgID(req.Context(), orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.CreateManual(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
			require.Contains(t, w.Body.String(), tt.expectedBody)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestSessionHandler_EndSession_EnqueuesOpenPR(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "idle", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("UPDATE sessions SET status = @status, completed_at = now\\(\\), last_activity_at = now\\(\\) WHERE id = @id AND org_id = @org_id .+ RETURNING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 1, now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{
			snapshotKey:     "snapshots/test.tar",
			prCreationState: "queued",
		}))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/end", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.EndSession(w, req)

	require.Equal(t, http.StatusOK, w.Code, "ending an idle non-manual session should enqueue PR creation")
	require.Contains(t, w.Body.String(), `"status":"completed"`, "response should return the completed session")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_EndSession_ManualEnqueuesOpenPR(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "idle", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID, // triggered_by_user_id
				nil, 1, now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("UPDATE sessions SET status = @status, completed_at = now\\(\\), last_activity_at = now\\(\\) WHERE id = @id AND org_id = @org_id .+ RETURNING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 1, now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	// Expect open_pr job instead of validate.
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{
			snapshotKey:     "snapshots/test.tar",
			prCreationState: "queued",
		}))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/end", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.EndSession(w, req)

	require.Equal(t, http.StatusOK, w.Code, "ending a manual session should enqueue open_pr")
	require.Contains(t, w.Body.String(), `"status":"completed"`, "response should return the completed session")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestBuildManualSessionDescription(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		message  string
		images   []string
		expected string
	}{
		{
			name:     "message only",
			message:  "Fix the bug",
			images:   nil,
			expected: "Fix the bug",
		},
		{
			name:     "message with images",
			message:  "Fix the bug",
			images:   []string{"https://example.com/img1.png", "https://example.com/img2.png"},
			expected: "Fix the bug\n\n### Attached images\n- https://example.com/img1.png\n- https://example.com/img2.png",
		},
		{
			name:     "empty images slice",
			message:  "Fix the bug",
			images:   []string{},
			expected: "Fix the bug",
		},
		{
			name:     "blank image URLs filtered",
			message:  "Fix the bug",
			images:   []string{"  ", "https://example.com/img.png"},
			expected: "Fix the bug\n\n### Attached images\n- https://example.com/img.png",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := buildManualSessionDescription(tt.message, tt.images)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestManualSessionTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		message  string
		expected string
	}{
		{
			name:     "short message",
			message:  "Fix the login bug",
			expected: "Fix the login bug",
		},
		{
			name:     "empty message",
			message:  "",
			expected: "Manual Session",
		},
		{
			name:     "whitespace only",
			message:  "   ",
			expected: "Manual Session",
		},
		{
			name:     "multiline uses first line",
			message:  "Fix the login bug\nMore details here",
			expected: "Fix the login bug",
		},
		{
			name:     "long message truncated",
			message:  strings.Repeat("a", 200),
			expected: strings.Repeat("a", 120) + "...",
		},
		{
			name:     "long message truncates at utf8 boundary",
			message:  strings.Repeat("a", 119) + "…" + strings.Repeat("b", 20),
			expected: strings.Repeat("a", 119) + "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := manualSessionTitle(tt.message)
			require.True(t, utf8.ValidString(result), "manualSessionTitle should always return valid UTF-8")
			require.Equal(t, tt.expected, result, "manualSessionTitle should derive the expected display title")
		})
	}
}

func TestIsValidGitRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ref   string
		valid bool
	}{
		{"main", true},
		{"feature/add-auth", true},
		{"fix-123", true},
		{"refs/heads/main", true},
		{"", false},
		{"main..develop", false},
		{"branch~1", false},
		{"branch^2", false},
		{"branch:file", false},
		{"branch name", false},
		{"branch\\path", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.valid, isValidGitRef(tt.ref))
		})
	}
}

// messageColumns is the standard column set for session_messages queries.
var messageColumns = []string{
	"id", "session_id", "org_id", "thread_id", "user_id", "turn_number", "role", "content", "attachments", "references", "commands", "token_usage", "created_at",
}

func TestSessionHandler_ListMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID)
		expectedCode int
		expectedLen  int
	}{
		{
			name: "returns messages for session",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				// Session lookup.
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, now, "snapshotted", nil,
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				// Messages query.
				userID := uuid.New()
				mock.ExpectQuery("SELECT .+ FROM session_messages WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(messageColumns).
							AddRow(int64(1), sessionID, orgID, nil, &userID, 1, "user", "Hello", nil, nil, nil, nil, now).
							AddRow(int64(2), sessionID, orgID, nil, nil, 1, "assistant", "Hi there", nil, nil, nil, nil, now),
					)
			},
			expectedCode: http.StatusOK,
			expectedLen:  2,
		},
		{
			name: "returns empty list for session with no messages",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 0, now, "none", nil,
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				mock.ExpectQuery("SELECT .+ FROM session_messages WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(messageColumns))
			},
			expectedCode: http.StatusOK,
			expectedLen:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			sessionID := uuid.New()
			handler := newSessionHandler(t, mock)

			tt.setupMock(mock, orgID, sessionID)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String()+"/messages", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", sessionID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.ListMessages(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")

			var resp models.ListResponse[models.SessionMessage]
			err = json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err, "response body should be valid JSON")
			require.Equal(t, tt.expectedLen, len(resp.Data), "should return expected number of messages")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_SendMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		setupMock    func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID)
		expectedCode int
		expectedBody string
	}{
		{
			name: "sends message and enqueues continue_session job",
			body: `{"message":"Please add tests"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				// GetByID — session is idle.
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				mock.ExpectBegin()
				// ClaimIdle succeeds.
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				// Create message.
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				// Enqueue job.
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Please add tests",
		},
		{
			name: "sends message to running session without enqueuing job",
			body: `{"message":"Quick note while you work"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				// GetByID — session is running.
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 2, now, "running", nil,
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				// Running session, no review-comment resolve requested:
				// short-circuits to a single non-tx INSERT (no Begin, no
				// Commit, no ClaimIdle, no ClaimForResume, no Enqueue).
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Quick note while you work",
		},
		{
			name: "rejects empty message",
			body: `{"message":""}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_MESSAGE",
		},
		{
			name: "persists references and commands on follow-up to running session",
			body: `{"message":"/review","references":[{"kind":"file","token":"@cmd/server/main.go","path":"cmd/server/main.go","display":"main.go"}],"commands":[{"kind":"command","agent_type":"claude_code","name":"review","token":"/review","display":"/review"}]}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "running", nil,
							nil,
							nil,
							nil,
							nil,
							nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				// Running session, no review-comment resolve requested:
				// message gets persisted in-place via the non-tx fast path.
				// Exact JSON shape is covered by the model unit tests; here
				// we only confirm the handler routes both fields to the store.
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
						pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "/review",
		},
		{
			name: "rejects command targeting another agent",
			body: `{"message":"/diff","commands":[{"kind":"command","agent_type":"codex","name":"diff","token":"/diff","display":"/diff"}]}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "running", nil,
							nil, nil, nil, nil, nil, nil, nil, nil, nil, "idle", (*string)(nil), nil, now,
						),
					)
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "COMMAND_AGENT_MISMATCH",
		},
		{
			name: "rejects malformed command in send message",
			body: `{"message":"/review","commands":[{"kind":"command","agent_type":"claude_code","name":"review","display":"/review"}]}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_COMMANDS",
		},
		{
			name: "rejects when session is not idle or resumable",
			body: `{"message":"More work"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				// GetByID — session is pending (not running, not idle, not terminal).
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "pending", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 0, now, "none", nil,
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				mock.ExpectBegin()
				// ClaimIdle fails (no row returned).
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				// ClaimForResume also fails (no row returned). Carries an
				// extra @statuses arg now that the resumable-status set is
				// bound at runtime.
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectRollback()
			},
			expectedCode: http.StatusConflict,
			expectedBody: "NOT_RESUMABLE",
		},
		{
			name: "returns error when transaction begin fails",
			body: `{"message":"Please continue"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin().WillReturnError(fmt.Errorf("cannot begin tx"))
			},
			expectedCode: http.StatusInternalServerError,
			expectedBody: "TX_BEGIN_FAILED",
		},
		{
			name: "logs rollback error when transaction rollback fails",
			body: `{"message":"More work"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "pending", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 0, now, "none", nil,
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectRollback().WillReturnError(fmt.Errorf("rollback failed"))
			},
			expectedCode: http.StatusConflict,
			expectedBody: "NOT_RESUMABLE",
		},
		{
			name: "rejects message to completed session with destroyed sandbox snapshot",
			body: `{"message":"Continue please"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 3, now, "destroyed", nil,
							nil, nil, nil, nil, nil,
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
			},
			expectedCode: http.StatusGone,
			expectedBody: "SNAPSHOT_EXPIRED",
		},
		{
			name: "rejects message to idle session with destroyed sandbox snapshot",
			body: `{"message":"Continue please"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 2, now, "destroyed", nil,
							nil, nil, nil, nil, nil,
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
			},
			expectedCode: http.StatusGone,
			expectedBody: "SNAPSHOT_EXPIRED",
		},
		{
			name: "sends message to completed session via ClaimForResume",
			body: `{"message":"Continue working on this"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				// GetByID — session is completed.
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				mock.ExpectBegin()
				// ClaimIdle fails (no row returned).
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				// ClaimForResume succeeds. Carries an extra @statuses arg
				// now that the resumable-status set is bound at runtime.
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil, // triggered_by_user_id
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil,      // target_branch
							nil,      // working_branch
							nil,      // repository_id
							nil,      // diff_stats
							nil,      // diff_history
							nil,      // input_manifest
							nil, nil, // archived_at, archived_by_user_id
							nil,            // automation_run_id
							"idle",         // pr_creation_state
							(*string)(nil), // pr_creation_error
							nil,            // deleted_at
							now,
						),
					)
				// Create message.
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				// Enqueue job.
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Continue working on this",
		},
		{
			name: "returns error when message creation fails in transaction",
			body: `{"message":"Please continue"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("insert failed"))
				mock.ExpectRollback()
			},
			expectedCode: http.StatusInternalServerError,
			expectedBody: "CREATE_FAILED",
		},
		{
			name: "sends message to awaiting input session via ClaimForResume",
			body: `{"message":"Here is the clarification"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				answer := "Here is the clarification"
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "awaiting_input", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectQuery(`UPDATE sessions\s+SET status = 'running', completed_at = NULL,\s+last_activity_at = now\(\)\s+WHERE id = @id AND org_id = @org_id AND status = ANY\(@statuses\)\s+AND sandbox_state != 'destroyed'\s+RETURNING`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				mock.ExpectQuery("UPDATE session_questions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows([]string{
							"id", "session_id", "org_id", "question_text", "options", "context",
							"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
						}).AddRow(
							uuid.New(), sessionID, orgID, "Which fix?", nil, nil,
							stringPtr("implementation"), &answer, &userID, &now, "answered", now,
						),
					)
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()
				mock.ExpectQuery("INSERT INTO audit_logs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Here is the clarification",
		},
		{
			name: "returns error when awaiting input answer update fails",
			body: `{"message":"Here is the clarification"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "awaiting_input", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectQuery(`UPDATE sessions\s+SET status = 'running', completed_at = NULL,\s+last_activity_at = now\(\)\s+WHERE id = @id AND org_id = @org_id AND status = ANY\(@statuses\)\s+AND sandbox_state != 'destroyed'\s+RETURNING`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				mock.ExpectQuery("UPDATE session_questions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("update failed"))
				mock.ExpectRollback()
			},
			expectedCode: http.StatusInternalServerError,
			expectedBody: "ANSWER_FAILED",
		},
		{
			name: "sends message to needs human guidance session via ClaimForResume",
			body: `{"message":"Please refine the fix"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "needs_human_guidance", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectQuery(`UPDATE sessions\s+SET status = 'running', completed_at = NULL,\s+last_activity_at = now\(\)\s+WHERE id = @id AND org_id = @org_id AND status = ANY\(@statuses\)\s+AND sandbox_state != 'destroyed'\s+RETURNING`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Please refine the fix",
		},
		{
			name: "sends message to awaiting input session without pending question",
			body: `{"message":"Continuing without a stored question"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "awaiting_input", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
				mock.ExpectQuery(`UPDATE sessions\s+SET status = 'running', completed_at = NULL,\s+last_activity_at = now\(\)\s+WHERE id = @id AND org_id = @org_id AND status = ANY\(@statuses\)\s+AND sandbox_state != 'destroyed'\s+RETURNING`).
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				mock.ExpectQuery("UPDATE session_questions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "session_id", "org_id", "question_text", "options", "context",
						"blocks_phase", "answer_text", "answered_by", "answered_at", "status", "created_at",
					}))
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit()
			},
			expectedCode: http.StatusCreated,
			expectedBody: "Continuing without a stored question",
		},
		{
			name: "rejects awaiting input follow-up without text answer",
			body: `{"images":["https://example.com/image.png"]}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "awaiting_input", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 2, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
			},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_ANSWER",
		},
		{
			name: "rolls back message creation when enqueue fails",
			body: `{"message":"Please continue"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("queue unavailable"))
				mock.ExpectRollback()
			},
			expectedCode: http.StatusInternalServerError,
			expectedBody: "ENQUEUE_FAILED",
		},
		{
			name: "returns error when commit fails",
			body: `{"message":"Please continue"}`,
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID, userID uuid.UUID) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "idle", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions SET status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, uuid.New(), orgID, "claude-code", "running", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, nil, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO session_messages").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
				mock.ExpectCommit().WillReturnError(fmt.Errorf("commit failed"))
				mock.ExpectRollback()
			},
			expectedCode: http.StatusInternalServerError,
			expectedBody: "TX_COMMIT_FAILED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			orgID := uuid.New()
			sessionID := uuid.New()
			userID := uuid.New()
			handler := newSessionHandler(t, mock)
			handler.audit = db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop())

			tt.setupMock(mock, orgID, sessionID, userID)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/messages", strings.NewReader(tt.body))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", sessionID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.SendMessage(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected content")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_ListMessages_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/bad-id/messages", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ListMessages(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

func TestSessionHandler_SendMessage_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/bad-id/messages", strings.NewReader(`{"message":"test"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "bad-id")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.SendMessage(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code, "should return 400 for invalid ID")
	require.Contains(t, w.Body.String(), "INVALID_ID")
}

// TestSessionHandler_SendMessage_ResolvesReviewComments verifies that
// resolve_review_comment_ids is honored end-to-end: malformed UUIDs and IDs
// that don't belong to the session are rejected with descriptive 400s, and
// valid IDs are looked up + flipped to resolved inside the same transaction
// that creates the message — so a partial state ("message saved, comment
// still open") is impossible.
func TestSessionHandler_SendMessage_ResolvesReviewComments(t *testing.T) {
	t.Parallel()

	now := time.Now()

	idleSessionRow := func(rows *pgxmock.Rows, sessionID, orgID uuid.UUID, status string) *pgxmock.Rows {
		return addSessionRow(rows,
			sessionID, uuid.New(), orgID, "claude-code", status, "semi", "low",
			nil, nil, nil, nil,
			nil, false, &now, nil, nil,
			nil, nil, nil, false,
			nil, nil, nil, nil, nil,
			nil, nil, nil, nil,
			nil, nil,
			nil, // triggered_by_user_id
			nil, 1, now, "snapshotted", stringPtr("snapshots/test"),
			nil, nil, nil, nil, nil, nil,
			nil, nil,
			nil,
			"idle",
			(*string)(nil),
			nil,
			now,
		)
	}

	t.Run("rejects malformed comment ID", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		handler := newSessionHandler(t, mock)
		// No DB calls expected — validation rejects before any query.

		req := newSendMessageRequest(sessionID, orgID, userID, `{"message":"hello","resolve_review_comment_ids":["not-a-uuid"]}`)
		w := httptest.NewRecorder()
		handler.SendMessage(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_REVIEW_COMMENT_ID")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rejects resolve IDs when review comment store is not configured", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should initialize")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentID := uuid.New()
		handler := newSessionHandler(t, mock)
		handler.SetReviewCommentStore(nil)

		req := newSendMessageRequest(sessionID, orgID, userID, fmt.Sprintf(`{"message":"hello","resolve_review_comment_ids":[%q]}`, commentID.String()))
		w := httptest.NewRecorder()
		handler.SendMessage(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code, "handler should reject resolve IDs when review comments are disabled")
		require.Contains(t, w.Body.String(), "REVIEW_COMMENTS_NOT_CONFIGURED", "response should explain the missing review-comment store")
		require.NoError(t, mock.ExpectationsWereMet(), "request should fail before DB access")
	})

	t.Run("rejects ID that does not belong to the session", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentID := uuid.New()
		handler := newSessionHandler(t, mock)
		handler.audit = db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop())

		// Session is idle, claim succeeds, message is created, then the lookup
		// for the foreign comment ID returns zero rows → handler aborts the tx.
		mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(idleSessionRow(pgxmock.NewRows(sessionColumns), sessionID, orgID, "idle"))
		mock.ExpectBegin()
		mock.ExpectQuery("UPDATE sessions SET status").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(idleSessionRow(pgxmock.NewRows(sessionColumns), sessionID, orgID, "running"))
		mock.ExpectQuery("INSERT INTO session_messages").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id = ANY").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(reviewCommentColumns)) // no rows match → invalid
		mock.ExpectRollback()

		req := newSendMessageRequest(sessionID, orgID, userID, fmt.Sprintf(`{"message":"hello","resolve_review_comment_ids":[%q]}`, commentID.String()))
		w := httptest.NewRecorder()
		handler.SendMessage(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "INVALID_REVIEW_COMMENT_IDS")
		require.Contains(t, w.Body.String(), commentID.String(),
			"error should name the offending IDs so client can debug without leaking other tenants' data")
		require.NoError(t, mock.ExpectationsWereMet(),
			"the message insert and comment lookup must roll back when validation fails")
	})

	t.Run("resolves comments inside the same tx for an idle session", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentUserID := uuid.New()
		commentID := uuid.New()
		handler := newSessionHandler(t, mock)
		handler.audit = db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop())

		mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(idleSessionRow(pgxmock.NewRows(sessionColumns), sessionID, orgID, "idle"))
		mock.ExpectBegin()
		mock.ExpectQuery("UPDATE sessions SET status").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(idleSessionRow(pgxmock.NewRows(sessionColumns), sessionID, orgID, "running"))
		mock.ExpectQuery("INSERT INTO session_messages").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id = ANY").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(commentID, sessionID, orgID, commentUserID, "main.go",
						42, "right", "fix this", false, (*time.Time)(nil), (*int)(nil),
						1, now, now),
			)
		mock.ExpectQuery("UPDATE session_review_comments SET resolved = true").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(commentID, sessionID, orgID, commentUserID, "main.go",
						42, "right", "fix this", true, &now, srcIntPtr(2),
						1, now, now),
			)
		mock.ExpectQuery("INSERT INTO jobs").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
		mock.ExpectCommit()
		// audit emitter writes one row for the resolved comment.
		mock.ExpectQuery("INSERT INTO audit_logs").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))

		req := newSendMessageRequest(sessionID, orgID, userID, fmt.Sprintf(`{"message":"address review","resolve_review_comment_ids":[%q]}`, commentID.String()))
		w := httptest.NewRecorder()
		handler.SendMessage(w, req)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("resolves comments inline for a running session without enqueuing a job", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentUserID := uuid.New()
		commentID := uuid.New()
		handler := newSessionHandler(t, mock)
		handler.audit = db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop())

		mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(idleSessionRow(pgxmock.NewRows(sessionColumns), sessionID, orgID, "running"))
		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO session_messages").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(2), now))
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id = ANY").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(commentID, sessionID, orgID, commentUserID, "main.go",
						5, "right", "consider X", false, (*time.Time)(nil), (*int)(nil),
						1, now, now),
			)
		mock.ExpectQuery("UPDATE session_review_comments SET resolved = true").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(commentID, sessionID, orgID, commentUserID, "main.go",
						5, "right", "consider X", true, &now, srcIntPtr(2),
						1, now, now),
			)
		mock.ExpectCommit()
		mock.ExpectQuery("INSERT INTO audit_logs").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))

		req := newSendMessageRequest(sessionID, orgID, userID, fmt.Sprintf(`{"message":"thanks for the catch","resolve_review_comment_ids":[%q]}`, commentID.String()))
		w := httptest.NewRecorder()
		handler.SendMessage(w, req)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("deduplicates repeated IDs before lookup and resolve", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should initialize")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentUserID := uuid.New()
		commentID := uuid.New()
		handler := newSessionHandler(t, mock)
		handler.audit = db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop())

		mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(idleSessionRow(pgxmock.NewRows(sessionColumns), sessionID, orgID, "running"))
		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO session_messages").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(4), now))
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id = ANY").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(commentID, sessionID, orgID, commentUserID, "main.go",
						9, "right", "dedupe me", false, (*time.Time)(nil), (*int)(nil),
						1, now, now),
			)
		mock.ExpectQuery("UPDATE session_review_comments SET resolved = true").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(commentID, sessionID, orgID, commentUserID, "main.go",
						9, "right", "dedupe me", true, &now, srcIntPtr(1),
						1, now, now),
			)
		mock.ExpectCommit()
		mock.ExpectQuery("INSERT INTO audit_logs").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))

		req := newSendMessageRequest(sessionID, orgID, userID, fmt.Sprintf(`{"message":"dedupe","resolve_review_comment_ids":[%q,%q]}`, commentID.String(), commentID.String()))
		w := httptest.NewRecorder()
		handler.SendMessage(w, req)
		require.Equal(t, http.StatusCreated, w.Code, "duplicate IDs should be resolved once without failing validation")
		require.NoError(t, mock.ExpectationsWereMet(), "deduplicated request should issue one lookup and one resolve")
	})

	t.Run("rejects request that exceeds the per-message resolve cap", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		handler := newSessionHandler(t, mock)
		// Build a list one over the cap. Validation should reject before any
		// DB query — keeps audit + resolve work bounded under client misuse.
		ids := make([]string, 0, maxReviewCommentResolveIDsPerMessage+1)
		for i := 0; i <= maxReviewCommentResolveIDsPerMessage; i++ {
			ids = append(ids, uuid.New().String())
		}
		idsJSON, jerr := json.Marshal(ids)
		require.NoError(t, jerr)
		body := fmt.Sprintf(`{"message":"hello","resolve_review_comment_ids":%s}`, string(idsJSON))

		req := newSendMessageRequest(sessionID, orgID, userID, body)
		w := httptest.NewRecorder()
		handler.SendMessage(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code)
		require.Contains(t, w.Body.String(), "TOO_MANY_REVIEW_COMMENT_IDS")
		require.NoError(t, mock.ExpectationsWereMet(), "no DB queries should run when validation rejects upfront")
	})

	t.Run("batches audit emission into a single INSERT for many resolved comments", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentUserID := uuid.New()
		const n = 4
		commentIDs := make([]uuid.UUID, n)
		for i := range commentIDs {
			commentIDs[i] = uuid.New()
		}
		handler := newSessionHandler(t, mock)
		handler.audit = db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop())

		// Build the SELECT response: every requested ID exists, all unresolved.
		selectRows := pgxmock.NewRows(reviewCommentColumns)
		for i, id := range commentIDs {
			selectRows.AddRow(id, sessionID, orgID, commentUserID, "main.go",
				10+i, "right", "comment body", false, (*time.Time)(nil), (*int)(nil),
				1, now, now)
		}
		// Build the UPDATE RETURNING response: all flip to resolved.
		updateRows := pgxmock.NewRows(reviewCommentColumns)
		for i, id := range commentIDs {
			updateRows.AddRow(id, sessionID, orgID, commentUserID, "main.go",
				10+i, "right", "comment body", true, &now, srcIntPtr(1),
				1, now, now)
		}

		mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(idleSessionRow(pgxmock.NewRows(sessionColumns), sessionID, orgID, "running"))
		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO session_messages").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(7), now))
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id = ANY").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(selectRows)
		mock.ExpectQuery("UPDATE session_review_comments SET resolved = true").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(updateRows)
		mock.ExpectCommit()
		// CRITICAL: a single batched audit INSERT (Exec, not Query — the
		// batch path skips RETURNING). If this asserts N times instead of
		// once, the N+1 has regressed.
		auditArgs := make([]any, 0, n*13)
		for i := 0; i < n*13; i++ {
			auditArgs = append(auditArgs, pgxmock.AnyArg())
		}
		mock.ExpectExec("INSERT INTO audit_logs").
			WithArgs(auditArgs...).
			WillReturnResult(pgxmock.NewResult("INSERT", n))

		idsJSON := make([]string, n)
		for i, id := range commentIDs {
			idsJSON[i] = `"` + id.String() + `"`
		}
		body := fmt.Sprintf(`{"message":"addressing all of these","resolve_review_comment_ids":[%s]}`, strings.Join(idsJSON, ","))
		req := newSendMessageRequest(sessionID, orgID, userID, body)
		w := httptest.NewRecorder()
		handler.SendMessage(w, req)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("idempotent: already-resolved IDs commit cleanly with no audit emission", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		commentUserID := uuid.New()
		commentID := uuid.New()
		handler := newSessionHandler(t, mock)
		handler.audit = db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop())

		// Validation finds the comment (it exists), the resolve UPDATE
		// returns zero rows because the comment is already resolved, and
		// the request still succeeds — no audit emission, idempotent.
		mock.ExpectQuery("SELECT .+ FROM sessions WHERE").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(idleSessionRow(pgxmock.NewRows(sessionColumns), sessionID, orgID, "running"))
		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO session_messages").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(3), now))
		mock.ExpectQuery("SELECT .+ FROM session_review_comments WHERE id = ANY").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(reviewCommentColumns).
					AddRow(commentID, sessionID, orgID, commentUserID, "main.go",
						12, "right", "already addressed", true, &now, srcIntPtr(1),
						1, now, now),
			)
		mock.ExpectQuery("UPDATE session_review_comments SET resolved = true").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(reviewCommentColumns)) // already resolved → no rows changed
		mock.ExpectCommit()
		// No ExpectQuery on audit_logs — idempotent path emits zero audit rows.

		req := newSendMessageRequest(sessionID, orgID, userID, fmt.Sprintf(`{"message":"thanks","resolve_review_comment_ids":[%q]}`, commentID.String()))
		w := httptest.NewRecorder()
		handler.SendMessage(w, req)
		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestCurrentResolutionPass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		session  *models.Session
		expected int
	}{
		{name: "nil session defaults to first pass", session: nil, expected: 1},
		{name: "zero current turn defaults to first pass", session: &models.Session{}, expected: 1},
		{name: "nonzero current turn is preserved", session: &models.Session{CurrentTurn: 3}, expected: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := currentResolutionPass(tt.session)
			require.Equal(t, tt.expected, got, "currentResolutionPass should return the expected pass number")
		})
	}
}

// TestParseAndDedupeReviewCommentIDs covers the shared helper used by both
// session-level SendMessage and thread-level SendThreadMessage. The
// behaviors under test (cap, dedupe, malformed UUID rejection) are uniform
// across both surfaces; the deeper validate-and-resolve tests live in the db
// package against ValidateAndResolveByIDs.
func TestParseAndDedupeReviewCommentIDs(t *testing.T) {
	t.Parallel()

	t.Run("returns nil for empty input", func(t *testing.T) {
		t.Parallel()
		out, perr := parseAndDedupeReviewCommentIDs(nil)
		require.Nil(t, perr)
		require.Nil(t, out)
	})

	t.Run("rejects malformed UUID before any work", func(t *testing.T) {
		t.Parallel()
		out, perr := parseAndDedupeReviewCommentIDs([]string{"not-a-uuid"})
		require.NotNil(t, perr)
		require.Equal(t, "INVALID_REVIEW_COMMENT_ID", perr.code)
		require.Equal(t, http.StatusBadRequest, perr.status)
		require.Empty(t, out)
	})

	t.Run("rejects too many IDs", func(t *testing.T) {
		t.Parallel()
		raw := make([]string, maxReviewCommentResolveIDsPerMessage+1)
		for i := range raw {
			raw[i] = uuid.New().String()
		}
		out, perr := parseAndDedupeReviewCommentIDs(raw)
		require.NotNil(t, perr)
		require.Equal(t, "TOO_MANY_REVIEW_COMMENT_IDS", perr.code)
		require.Equal(t, http.StatusBadRequest, perr.status)
		require.Empty(t, out)
	})

	t.Run("dedupes preserving first occurrence", func(t *testing.T) {
		t.Parallel()
		a := uuid.New()
		b := uuid.New()
		out, perr := parseAndDedupeReviewCommentIDs([]string{a.String(), b.String(), a.String()})
		require.Nil(t, perr)
		require.Equal(t, []uuid.UUID{a, b}, out)
	})
}

func TestRenderReviewCommentResolveError(t *testing.T) {
	t.Parallel()

	t.Run("returns false for nil error", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		require.False(t, renderReviewCommentResolveError(w, req, nil))
		require.Equal(t, http.StatusOK, w.Code, "no body should be written")
	})

	t.Run("returns false for unrelated errors", func(t *testing.T) {
		t.Parallel()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		require.False(t, renderReviewCommentResolveError(w, req, errors.New("connection refused")))
		require.Equal(t, http.StatusOK, w.Code, "unrelated errors should not be written by this helper")
	})

	t.Run("renders ErrReviewCommentsNotInSession with capped IDs", func(t *testing.T) {
		t.Parallel()
		ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		require.True(t, renderReviewCommentResolveError(w, req, &db.ErrReviewCommentsNotInSession{Missing: ids}))
		require.Equal(t, http.StatusBadRequest, w.Code)
		body := w.Body.String()
		require.Contains(t, body, "INVALID_REVIEW_COMMENT_IDS")
		require.Contains(t, body, ids[0].String(), "first missing ID should appear")
		require.Contains(t, body, ids[4].String(), "fifth missing ID should appear")
		require.NotContains(t, body, ids[5].String(), "missing ID details should be capped at 5")
		require.NotContains(t, body, ids[6].String(), "missing ID details should be capped at 5")
	})
}

// newSendMessageRequest builds a *http.Request configured the way the chi
// router does at runtime: URL params populated, org and user contexts set.
func newSendMessageRequest(sessionID, orgID, userID uuid.UUID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/messages", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "member"})
	return req.WithContext(ctx)
}

// srcIntPtr is mirrored from the db package's test helper since handlers_test
// can't import it. Both produce a *int suitable for resolved_by_pass.
func srcIntPtr(i int) *int { return &i }

// mockLLMClient is a test double for llm.Client.
// The WaitGroup lets the test verify that the handler waits for the LLM call
// to finish before returning a response (i.e. the call is synchronous).
type mockLLMClient struct {
	response string
	err      error
	wg       sync.WaitGroup
}

func (m *mockLLMClient) Complete(_ context.Context, _, _ string) (string, error) {
	defer m.wg.Done()
	return m.response, m.err
}

func newMockLLMClient(response string, err error) *mockLLMClient {
	m := &mockLLMClient{response: response, err: err}
	m.wg.Add(1)
	return m
}

func TestSessionHandler_CreateManual_WithLLMTitle(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()

	llmClient := newMockLLMClient("Fix authentication login flow", nil)
	handler := NewSessionHandler(
		db.NewSessionStore(mock),
		db.NewSessionLogStore(mock),
		db.NewSessionQuestionStore(mock),
		db.NewPullRequestStore(mock),
		db.NewIssueStore(mock),
		db.NewRepositoryStore(mock),
		db.NewOrganizationStore(mock),
		db.NewJobStore(mock),
		db.NewSessionMessageStore(mock),
		db.NewSessionThreadStore(mock),
		llmClient,
		zerolog.Nop(),
	)

	now := time.Now()
	runID := uuid.New()
	jobID := uuid.New()

	// Mock org settings lookup
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test-org", nil, now, now))

	expectManualSessionCreate(mock, runID, now)

	// Mock concurrency check
	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	// Mock job enqueue
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	// Mock UpdateTitle call
	mock.ExpectExec("UPDATE sessions SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual",
		strings.NewReader(`{"message":"The login page throws a 500 error when users try to authenticate with SSO","agent_type":"claude_code"}`))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	// WaitGroup confirms the LLM was called synchronously before the response.
	llmClient.wg.Wait()

	require.Equal(t, http.StatusCreated, w.Code)

	var resp models.SingleResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	require.NotNil(t, resp.Data.Title)
	require.Equal(t, "Fix authentication login flow", *resp.Data.Title)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_CreateManual_LLMError_Returns500(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()

	llmClient := newMockLLMClient("", fmt.Errorf("rate limited"))
	handler := NewSessionHandler(
		db.NewSessionStore(mock),
		db.NewSessionLogStore(mock),
		db.NewSessionQuestionStore(mock),
		db.NewPullRequestStore(mock),
		db.NewIssueStore(mock),
		db.NewRepositoryStore(mock),
		db.NewOrganizationStore(mock),
		db.NewJobStore(mock),
		db.NewSessionMessageStore(mock),
		db.NewSessionThreadStore(mock),
		llmClient,
		zerolog.Nop(),
	)

	now := time.Now()
	runID := uuid.New()
	jobID := uuid.New()

	// Mock org settings lookup
	mock.ExpectQuery("SELECT .+ FROM organizations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "test-org", nil, now, now))

	expectManualSessionCreate(mock, runID, now)

	// Mock concurrency check
	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	// Mock job enqueue
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))

	// No UpdateTitle mock — the LLM error means it should never be called.

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/manual",
		strings.NewReader(`{"message":"Fix the login bug","agent_type":"claude_code"}`))
	ctx := middleware.WithOrgID(req.Context(), orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateManual(w, req)

	// WaitGroup confirms the LLM was called synchronously.
	llmClient.wg.Wait()

	// LLM failure should propagate as a 500 error.
	require.Equal(t, http.StatusInternalServerError, w.Code, "LLM title generation failure should return 500")

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_CreatePR_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_Success"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)

	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"

	// Mock session lookup — session has a diff.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,                               // triggered_by_user_id
				nil, 0, now, "none", &snapshotKey, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	// Mock PR lookup — no existing PR (returns empty result).
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,                               // triggered_by_user_id
				nil, 0, now, "none", &snapshotKey, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
				nil,      // target_branch
				nil,      // working_branch
				nil,      // repository_id
				nil,      // diff_stats
				nil,      // diff_history
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"queued",       // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "should return 202 Accepted: %s", w.Body.String())
	require.Contains(t, w.Body.String(), `"status":"queued"`, "response should indicate job was queued")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_RejectsActiveThreadRuntimeHolders(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-active-thread-runtime"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetSessionSandboxHolderStore(db.NewSessionSandboxHolderStore(mock))

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "snapshotted", &snapshotKey,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil, nil,
				nil,
				"idle",
				(*string)(nil),
				nil,
				now,
			),
		)
	mock.ExpectQuery("SELECT count").
		WithArgs(orgID, sessionID).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "CreatePR should reject snapshot publication while a thread runtime is mutating the shared workspace")
	require.Contains(t, w.Body.String(), "SNAPSHOT_NOT_QUIESCENT", "response should expose the quiescence guard")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_BuilderRequiresCleanReviewLoop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		settings       json.RawMessage
		reviewRows     *pgxmock.Rows
		expectedStatus int
		expectedBody   string
		expectEnqueue  bool
	}{
		{
			name:           "blocks builder when no review loop exists",
			settings:       json.RawMessage(`{}`),
			reviewRows:     pgxmock.NewRows(reviewLoopColumns),
			expectedStatus: http.StatusConflict,
			expectedBody:   "REVIEW_REQUIRED_BEFORE_PR",
		},
		{
			name:     "allows builder after clean review loop",
			settings: json.RawMessage(`{}`),
			reviewRows: pgxmock.NewRows(reviewLoopColumns).AddRow(
				reviewLoopRowWithLatestCheckpoint(uuid.New(), uuid.New(), "clean", "manual", ptr("snap-allows-builder-after-clean-review-loop"))...,
			),
			expectedStatus: http.StatusAccepted,
			expectedBody:   `"status":"queued"`,
			expectEnqueue:  true,
		},
		{
			name:     "blocks builder when clean review loop is for older snapshot",
			settings: json.RawMessage(`{}`),
			reviewRows: pgxmock.NewRows(reviewLoopColumns).AddRow(
				reviewLoopRowWithLatestCheckpoint(uuid.New(), uuid.New(), "clean", "manual", ptr("snap-older-review-loop"))...,
			),
			expectedStatus: http.StatusConflict,
			expectedBody:   "REVIEW_REQUIRED_BEFORE_PR",
		},
		{
			name:           "allows builder when org disables requirement",
			settings:       json.RawMessage(`{"builder_permissions":{"require_review_before_pr":false}}`),
			expectedStatus: http.StatusAccepted,
			expectedBody:   `"status":"queued"`,
			expectEnqueue:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			now := time.Now()
			snapshotKey := "snap-" + strings.ReplaceAll(tt.name, " ", "-")
			orgID := uuid.New()
			sessionID := uuid.New()
			issueID := uuid.New()
			jobID := uuid.New()
			handler := newSessionHandler(t, mock)

			mock.ExpectQuery("SELECT .+ FROM sessions").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					addSessionRow(pgxmock.NewRows(sessionColumns),
						sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
						nil, nil, nil, nil,
						nil, false, &now, &now, nil,
						nil, nil, nil, false,
						nil, nil, nil, nil, nil,
						nil, nil, nil, nil,
						nil, nil,
						nil,
						nil, 0, now, "none", &snapshotKey,
						nil, nil, nil, nil, nil,
						nil,
						nil, nil,
						nil,
						"idle", (*string)(nil),
						nil,
						now,
					),
				)
			mock.ExpectQuery("SELECT .+ FROM pull_requests").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))
			mock.ExpectQuery("SELECT .+ FROM organizations").
				WithArgs(pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
					AddRow(orgID, "Acme", tt.settings, now, now))
			if tt.reviewRows != nil {
				mock.ExpectQuery("SELECT .+ FROM session_review_loops").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(tt.reviewRows)
			}
			if tt.expectEnqueue {
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, nil, nil, nil,
							nil, nil,
							nil,
							nil, 0, now, "none", &snapshotKey,
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"queued", (*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
				mock.ExpectCommit()
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", sessionID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			ctx = middleware.WithActiveRole(ctx, string(models.RoleBuilder))
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.CreatePR(w, req)

			require.Equal(t, tt.expectedStatus, w.Code, "CreatePR should return the expected status for builder review policy")
			require.Contains(t, w.Body.String(), tt.expectedBody, "CreatePR should return the expected builder review policy response")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_CreatePR_DedupeConflict(t *testing.T) {
	t.Parallel()

	// Regression: an in-flight open_pr job for the same session must not cause
	// a 500 ENQUEUE_FAILED response. The dedupe conflict means a PR job is
	// already queued, so the request is effectively a success.
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_DedupeConflict"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			),
		)

	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))

	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"queued", (*string)(nil),
				nil,
				now,
			),
		)
	// ON CONFLICT DO NOTHING fires because the dedupe_key already matches a
	// pending job. EnqueueInTx returns uuid.Nil without an error, and the
	// queued state repairs any legacy request that inserted the job before
	// marking the action.
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "dedupe conflict should be a success — the existing job satisfies the request")
	require.Contains(t, w.Body.String(), `"status":"queued"`, "response should still indicate queued status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_ReturnsAuthInterceptWhenUserCredentialMissing(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_ReturnsAuthInterceptWhenUserCredentialMissing"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRCredentialStore(&stubSessionPRCredentialStore{err: pgx.ErrNoRows})
	handler.SetPRAuthCredentialChecker(&stubSessionPRAuthCredentialChecker{
		hasValidCredentialFunc: func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
			return false, nil
		},
	})
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_preferred"}`), now, now))

	body := strings.NewReader(`{"author_mode":"auto"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "missing user credential should trigger PR authorship auth intercept")
	var resp models.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "response should decode")
	require.Equal(t, "GITHUB_PR_AUTHORSHIP_REQUIRED", resp.Error.Code, "response should carry auth-required code")
	details, ok := resp.Error.Details.(map[string]any)
	require.True(t, ok, "error details should be present")
	require.Equal(t, sessionID.String(), details["session_id"], "details should name the session being resumed")
	require.NotEmpty(t, details["resume_token"], "details should include a resume token")
	require.Equal(t, true, details["can_fallback_to_app"], "user_preferred should allow app fallback")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_InvalidAuthorMode(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_InvalidAuthorMode"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", strings.NewReader(`{"author_mode":"bogus"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "CreatePR should reject invalid author modes")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_PRAuthCheckerError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_PRAuthCheckerError"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRAuthCredentialChecker(&stubSessionPRAuthCredentialChecker{
		hasValidCredentialFunc: func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
			return false, context.DeadlineExceeded
		},
	})
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_preferred"}`), now, now))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", strings.NewReader(`{"author_mode":"auto"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "CreatePR should surface PR auth checker failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_ResumeTokenRequiresSigningKey(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_ResumeTokenRequiresSigningKey"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_preferred"}`), now, now))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", strings.NewReader(`{"author_mode":"auto","resume_token":"resume"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "CreatePR should reject resume tokens when signing is not configured")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_AppAuthorModeBypassesAuthIntercept(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_AppAuthorModeBypassesAuthIntercept"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRCredentialStore(&stubSessionPRCredentialStore{err: pgx.ErrNoRows})
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{
			snapshotKey:     snapshotKey,
			prCreationState: "queued",
		}))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectCommit()

	body := strings.NewReader(`{"author_mode":"app"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "explicit app author mode should enqueue PR creation without auth intercept")
	require.Contains(t, w.Body.String(), `"status":"queued"`, "response should indicate job was queued")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_InvalidStoredCredentialTriggersAuthIntercept(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_InvalidStoredCredentialTriggersAuthIntercept"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRCredentialStore(&stubSessionPRCredentialStore{
		cred: &models.DecryptedUserCredential{
			Config: models.GitHubAppUserConfig{
				AccessToken:           "ghu_stale",
				TokenType:             "bearer",
				ExpiresAt:             now.Add(-time.Minute),
				RefreshToken:          "ghr_test",
				RefreshTokenExpiresAt: now.Add(30 * 24 * time.Hour),
			},
		},
	})
	handler.SetPRAuthCredentialChecker(&stubSessionPRAuthCredentialChecker{
		hasValidCredentialFunc: func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
			return false, nil
		},
	})
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_preferred"}`), now, now))

	body := strings.NewReader(`{"author_mode":"auto"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "stale user credential should trigger reconnect instead of enqueueing PR creation")
	var resp models.ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "response should decode")
	require.Equal(t, "GITHUB_PR_AUTHORSHIP_REQUIRED", resp.Error.Code, "response should request GitHub reauthorization")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_UserPreferredWithoutGitHubAppUserAuthFallsBackToApp(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_UserPreferredWithoutGitHubAppUserAuthFallsBackToApp"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_preferred"}`), now, now))
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{
			snapshotKey:     snapshotKey,
			prCreationState: "queued",
		}))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", strings.NewReader(`{"author_mode":"auto"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "user_preferred should fall back to app auth when GitHub App user auth is unavailable")
	require.Contains(t, w.Body.String(), `"status":"queued"`, "response should enqueue PR creation")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_UserRequiredWithoutGitHubAppUserAuthFailsFast(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_UserRequiredWithoutGitHubAppUserAuthFailsFast"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_required"}`), now, now))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", strings.NewReader(`{"author_mode":"auto"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, "user_required should fail fast when GitHub App user auth is unavailable")
	require.Contains(t, w.Body.String(), "GITHUB_APP_USER_AUTH_NOT_CONFIGURED", "response should explain the missing GitHub App user auth configuration")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_UserRequiredWithoutCheckerIgnoresStoredGitHubAppUserCredential(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_UserRequiredWithoutCheckerIgnoresStoredGitHubAppUserCredential"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	handler := newSessionHandler(t, mock)
	handler.SetPRCredentialStore(&stubSessionPRCredentialStore{
		cred: &models.DecryptedUserCredential{
			Config: models.GitHubAppUserConfig{
				AccessToken:           "ghu_present_but_unusable",
				TokenType:             "bearer",
				ExpiresAt:             now.Add(time.Hour),
				RefreshToken:          "ghr_test",
				RefreshTokenExpiresAt: now.Add(30 * 24 * time.Hour),
			},
		},
	})
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length", "https://app.143.dev")

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).AddRow(sessionTestRow(
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				&userID,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle", (*string)(nil),
				nil,
				now,
			)...),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{"pr_authorship":"user_required"}`), now, now))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", strings.NewReader(`{"author_mode":"auto"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, "user_required should fail fast even if a stored github_app_user row exists when the resolver is unwired")
	require.Contains(t, w.Body.String(), "GITHUB_APP_USER_AUTH_NOT_CONFIGURED", "response should explain the missing GitHub App user auth configuration")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_SnapshotExpired(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	// Mock session lookup — sandbox_state=destroyed simulates true retention
	// expiry after the saved snapshot was reaped.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "destroyed", nil,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusGone, w.Code, "should return 410 when snapshot retention has expired")
	require.Contains(t, w.Body.String(), "SNAPSHOT_EXPIRED", "error code should indicate snapshot expiry")
	require.Contains(t, w.Body.String(), "This session snapshot expired before a PR could be created.", "response should explain that the reusable checkpoint aged out")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_SnapshotNotCaptured(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	// Mock session lookup — terminal session finished without a reusable
	// checkpoint ever being persisted, so the UX should explain that this is
	// a missing save, not an age-based expiry.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", nil,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "should return 409 when the session never saved a reusable checkpoint")
	require.Contains(t, w.Body.String(), "SNAPSHOT_NOT_CAPTURED", "error code should distinguish a missing checkpoint from retention expiry")
	require.Contains(t, w.Body.String(), "This session finished without saving a reusable checkpoint for PR creation.", "response should explain that the checkpoint was never saved")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_InFlightRejectsDuplicateSubmit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state string
	}{
		{name: "queued state rejects duplicate", state: "queued"},
		{name: "pushing state rejects duplicate", state: "pushing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			now := time.Now()
			snapshotKey := "snap-" + tt.state
			orgID := uuid.New()
			sessionID := uuid.New()
			issueID := uuid.New()
			handler := newSessionHandler(t, mock)

			mock.ExpectQuery("SELECT .+ FROM sessions").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					addSessionRow(pgxmock.NewRows(sessionColumns),
						sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
						nil, nil, nil, nil,
						nil, false, &now, &now, nil,
						nil, nil, nil, false,
						nil, nil, nil, nil, nil,
						nil, nil, nil, nil,
						nil, nil,
						nil,
						nil, 0, now, "none", &snapshotKey,
						nil, nil, nil, nil, nil,
						nil,      // input_manifest
						nil, nil, // archived_at, archived_by_user_id
						nil,            // automation_run_id
						tt.state,       // pr_creation_state
						(*string)(nil), // pr_creation_error
						nil,            // deleted_at
						now,
					),
				)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", sessionID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.CreatePR(w, req)

			require.Equal(t, http.StatusConflict, w.Code, "in-flight PR creation should reject duplicate submits")
			require.Contains(t, w.Body.String(), "PR_IN_FLIGHT", "error code should indicate an in-flight PR creation")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_CreatePR_UpdateStateErrorRollsBackAndReturns500(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_UpdateStateErrorStillAccepted"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("write failed"))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "CreatePR should fail when the queued state cannot be committed")
	require.Contains(t, w.Body.String(), "INTERNAL_ERROR", "response should identify state transition failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_AlreadyExists(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_AlreadyExists"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	prID := uuid.New()
	handler := newSessionHandler(t, mock)

	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"

	// Mock session lookup — session has a diff.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	// Mock PR lookup - PR already exists.
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionPullRequestColumns).AddRow(
				sessionPullRequestRow(prID, &sessionID, orgID, "org/repo", now)...,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "should return 409 when PR already exists")
	require.Contains(t, w.Body.String(), "PR_EXISTS", "error code should indicate PR already exists")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_SucceededWithoutStoredPRRejectsRetry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_SucceededWithoutStoredPRRejectsRetry"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"succeeded",    // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "should reject retries after PR creation already succeeded without re-enqueueing")
	require.Contains(t, w.Body.String(), "PR_ALREADY_CREATED", "error code should indicate the terminal PR state")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_SessionNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	handler := newSessionHandler(t, mock)

	// Mock session lookup — not found.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusNotFound, w.Code, "should return 404 when session not found")
	require.Contains(t, w.Body.String(), "NOT_FOUND", "error code should indicate session not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_CreatePR_PRLookupDBError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	snapshotKey := "snap-TestSessionHandler_CreatePR_PRLookupDBError"
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	diff := "--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new"

	// Mock session lookup — session has a diff.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, &diff,
				nil, nil, nil, nil,
				nil, nil,
				nil,
				nil, 0, now, "none", &snapshotKey,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	// Mock PR lookup — returns a database error.
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("connection refused"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreatePR(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "should return 500 on PR lookup DB error")
	require.Contains(t, w.Body.String(), "INTERNAL_ERROR", "error code should indicate internal error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// pushSessionRowOpts customizes pushSessionRow's defaults. The zero value
// produces a completed, snapshot-backed session whose pr_push_state is "idle"
// — i.e., a session that's ready to accept a "Push changes" request.
type pushSessionRowOpts struct {
	snapshotKey        string // explicit key; empty means generate, unless nilSnapshot
	pendingSnapshotKey string // explicit pending upload key; empty means NULL
	nilSnapshot        bool   // if true, snapshot_key is NULL
	sandboxState       string // defaults to "none"
	prCreationState    string // defaults to "idle"
	pushState          string // defaults to "idle"
	branchState        string // defaults to "idle"
	status             string // defaults to "completed"
	diff               *string
	diffHistory        any
}

// pushSessionRow builds a fully-padded session row (matching sessionColumns
// in length and order) for push-changes handler tests. Built directly rather
// than via addSessionRow because those test variants need to override
// pr_push_state, which the auto-pad in padSessionIdentityColumns otherwise
// forces to "idle" — and the dispatch by length doesn't have a slot for the
// "explicit pr_push values" shape.
func pushSessionRow(sessionID, issueID, orgID uuid.UUID, now time.Time, opts pushSessionRowOpts) *pgxmock.Rows {
	snapshotKey := opts.snapshotKey
	if snapshotKey == "" && !opts.nilSnapshot {
		snapshotKey = "snap-push-" + sessionID.String()
	}
	var snapshotKeyArg any
	if snapshotKey != "" {
		snapshotKeyArg = &snapshotKey
	} else {
		snapshotKeyArg = nil
	}
	var pendingSnapshotKeyArg any
	if opts.pendingSnapshotKey != "" {
		pendingSnapshotKeyArg = &opts.pendingSnapshotKey
	}
	sandboxState := opts.sandboxState
	if sandboxState == "" {
		sandboxState = "none"
	}
	pushState := opts.pushState
	if pushState == "" {
		pushState = "idle"
	}
	prCreationState := opts.prCreationState
	if prCreationState == "" {
		prCreationState = "idle"
	}
	branchState := opts.branchState
	if branchState == "" {
		branchState = "idle"
	}
	status := opts.status
	if status == "" {
		status = "completed"
	}
	primaryIssueID := &issueID
	values := map[string]any{
		"id":                             sessionID,
		"primary_issue_id":               primaryIssueID,
		"org_id":                         orgID,
		"origin":                         string(models.SessionOriginIssueTrigger),
		"interaction_mode":               string(models.SessionInteractionModeSingleRun),
		"validation_policy":              string(models.SessionValidationPolicyOnSessionEnd),
		"agent_type":                     string(models.AgentTypeClaudeCode),
		"status":                         status,
		"autonomy_level":                 "semi",
		"token_mode":                     "low",
		"complexity_tier":                nil,
		"container_id":                   nil,
		"worker_node_id":                 nil,
		"turn_holding_container":         false,
		"started_at":                     &now,
		"completed_at":                   &now,
		"token_usage":                    nil,
		"failure_explanation":            nil,
		"failure_category":               nil,
		"failure_next_steps":             nil,
		"failure_retry_advised":          false,
		"parent_session_id":              nil,
		"revision_context":               nil,
		"error":                          nil,
		"result_summary":                 nil,
		"diff":                           opts.diff,
		"pm_plan_id":                     nil,
		"title":                          nil,
		"pm_approach":                    nil,
		"pm_reasoning":                   nil,
		"project_task_id":                nil,
		"model_override":                 nil,
		"reasoning_effort":               nil,
		"triggered_by_user_id":           nil,
		"agent_session_id":               nil,
		"current_turn":                   0,
		"last_activity_at":               now,
		"sandbox_state":                  sandboxState,
		"snapshot_key":                   snapshotKeyArg,
		"pending_snapshot_key":           pendingSnapshotKeyArg,
		"pending_snapshot_set_at":        nil,
		"runtime_soft_deadline_at":       nil,
		"runtime_hard_deadline_at":       nil,
		"runtime_last_progress_at":       nil,
		"runtime_last_progress_type":     "",
		"runtime_last_progress_strength": "",
		"runtime_extension_count":        0,
		"runtime_extension_seconds":      0,
		"runtime_stop_reason":            "",
		"runtime_graceful_stop_at":       nil,
		"checkpointed_at":                nil,
		"checkpoint_kind":                "",
		"checkpoint_capability":          "",
		"checkpoint_size_bytes":          int64(0),
		"checkpoint_error":               nil,
		"recovery_state":                 "",
		"recovery_queued_at":             nil,
		"recovery_started_at":            nil,
		"recovery_attempt_count":         0,
		"target_branch":                  nil,
		"working_branch":                 nil,
		"base_commit_sha":                nil,
		"repository_id":                  nil,
		"diff_stats":                     nil,
		"diff_history":                   opts.diffHistory,
		"input_manifest":                 nil,
		"archived_at":                    nil,
		"archived_by_user_id":            nil,
		"automation_run_id":              nil,
		"pr_creation_state":              prCreationState,
		"pr_creation_error":              (*string)(nil),
		"pr_push_state":                  pushState,
		"pr_push_error":                  (*string)(nil),
		"branch_creation_state":          branchState,
		"branch_creation_error":          (*string)(nil),
		"branch_url":                     (*string)(nil),
		"diff_collected_at":              nil,
		"latest_diff_snapshot_id":        nil,
		"has_unpushed_changes":           false,
		"linear_private":                 false,
		"linear_state_sync_disabled":     false,
		"linear_identifier_hint":         (*string)(nil),
		"linear_prepare_state":           string(models.LinearPrepareStateNone),
		"deleted_at":                     nil,
		"git_identity_source":            nil,
		"git_identity_user_id":           nil,
		"created_at":                     now,
	}
	row := make([]any, len(sessionColumns))
	for i, col := range sessionColumns {
		row[i] = values[col]
	}
	return pgxmock.NewRows(sessionColumns).AddRow(row...)
}

func TestSessionHandler_PushChangesToPR_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	prID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{}))

	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionPullRequestColumns).
				AddRow(sessionPullRequestRow(prID, &sessionID, orgID, "owner/repo", now)...),
		)

	mock.ExpectBegin()
	// CAS update from TryMarkPRPushQueued — 2 args (id, org_id). RETURNING +
	// publishStatus replaced bare Exec so the SSE detail page sees the
	// transition immediately; the test returns the post-CAS row (pr_push_state
	// flipped to "queued") to mirror what Postgres would actually return.
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{pushState: "queued"}))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr/push", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.PushChangesToPR(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "should return 202 Accepted")
	require.Contains(t, w.Body.String(), `"status":"queued"`, "response should indicate job was queued")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_PushChangesToPR_BuilderRequiresCleanReviewLoopForCurrentSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		settings       json.RawMessage
		reviewRows     *pgxmock.Rows
		expectedStatus int
		expectedBody   string
		expectEnqueue  bool
	}{
		{
			name:     "allows builder after clean review loop for current snapshot",
			settings: json.RawMessage(`{}`),
			reviewRows: pgxmock.NewRows(reviewLoopColumns).AddRow(
				reviewLoopRowWithLatestCheckpoint(uuid.New(), uuid.New(), "clean", "manual", ptr("snap-push-current"))...,
			),
			expectedStatus: http.StatusAccepted,
			expectedBody:   `"status":"queued"`,
			expectEnqueue:  true,
		},
		{
			name:     "blocks builder when clean review loop is for older snapshot",
			settings: json.RawMessage(`{}`),
			reviewRows: pgxmock.NewRows(reviewLoopColumns).AddRow(
				reviewLoopRowWithLatestCheckpoint(uuid.New(), uuid.New(), "clean", "manual", ptr("snap-push-older"))...,
			),
			expectedStatus: http.StatusConflict,
			expectedBody:   "REVIEW_REQUIRED_BEFORE_PR",
		},
		{
			name:           "allows builder when org disables requirement",
			settings:       json.RawMessage(`{"builder_permissions":{"require_review_before_pr":false}}`),
			expectedStatus: http.StatusAccepted,
			expectedBody:   `"status":"queued"`,
			expectEnqueue:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			now := time.Now()
			orgID := uuid.New()
			sessionID := uuid.New()
			issueID := uuid.New()
			prID := uuid.New()
			jobID := uuid.New()
			handler := newSessionHandler(t, mock)

			mock.ExpectQuery("SELECT .+ FROM sessions").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{snapshotKey: "snap-push-current"}))

			mock.ExpectQuery("SELECT .+ FROM pull_requests").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(sessionPullRequestColumns).
						AddRow(sessionPullRequestRow(prID, &sessionID, orgID, "owner/repo", now)...),
				)

			mock.ExpectQuery("SELECT .+ FROM organizations").
				WithArgs(pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
					AddRow(orgID, "Acme", tt.settings, now, now))
			if tt.reviewRows != nil {
				mock.ExpectQuery("SELECT .+ FROM session_review_loops").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(tt.reviewRows)
			}
			if tt.expectEnqueue {
				mock.ExpectBegin()
				mock.ExpectQuery("UPDATE sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{
						snapshotKey: "snap-push-current",
						pushState:   "queued",
					}))
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
				mock.ExpectCommit()
			}

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr/push", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", sessionID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			ctx = middleware.WithActiveRole(ctx, string(models.RoleBuilder))
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.PushChangesToPR(w, req)

			require.Equal(t, tt.expectedStatus, w.Code, "PushChangesToPR should return the expected status for builder review policy")
			require.Contains(t, w.Body.String(), tt.expectedBody, "PushChangesToPR should return the expected builder review policy response")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_PushChangesToPR_CASLosesRaceReturnsConflict(t *testing.T) {
	t.Parallel()

	// Two concurrent submitters that both see pr_push_state='idle' will both
	// pass the in-memory precheck. The CAS-on-mark-queued is the tiebreaker:
	// the loser sees rows-affected=0 from TryMarkPRPushQueued and must
	// return 409 PR_PUSH_IN_FLIGHT instead of 202. Simulate that by feeding
	// the handler an idle row but mocking the UPDATE to affect zero rows.
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	prID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{}))

	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionPullRequestColumns).
				AddRow(sessionPullRequestRow(prID, &sessionID, orgID, "owner/repo", now)...),
		)

	mock.ExpectBegin()
	// CAS UPDATE matches no rows because a concurrent winner already moved
	// pr_push_state to 'queued'. RETURNING with empty rows triggers
	// pgx.ErrNoRows in TryMarkPRPushQueued, which surfaces as (false, nil).
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr/push", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.PushChangesToPR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "CAS losing the race should produce a 409")
	require.Contains(t, w.Body.String(), "PR_PUSH_IN_FLIGHT", "loser of the CAS race should see the in-flight code")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_PushChangesToPR_EnqueueFailureRollsBackQueuedState(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	prID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{}))
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionPullRequestColumns).
				AddRow(sessionPullRequestRow(prID, &sessionID, orgID, "owner/repo", now)...),
		)
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{pushState: "queued"}))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr/push", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.PushChangesToPR(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "enqueue failure should not return success")
	require.Contains(t, w.Body.String(), "ENQUEUE_FAILED", "response should identify enqueue failure")
	require.NoError(t, mock.ExpectationsWereMet(), "failed enqueue should roll back the queued action state")
}

func TestSessionHandler_CreateBranch_QueuesActionAndJobInOneTransaction(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	jobID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{}))
	mock.ExpectQuery("SELECT .+ FROM organizations").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "name", "settings", "created_at", "updated_at"}).
			AddRow(orgID, "Acme", json.RawMessage(`{}`), now, now))
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{branchState: "queued"}))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(jobID))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/branch", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CreateBranch(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "branch creation should return 202 Accepted")
	require.Contains(t, w.Body.String(), `"status":"queued"`, "response should indicate branch job was queued")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_PushChangesToPR_PendingSnapshotRejectsWithoutEnqueue(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{
			pendingSnapshotKey: "snapshots/pending/post-pr.tar.zst",
		}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr/push", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.PushChangesToPR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "pending snapshot upload should block push enqueue")
	require.Contains(t, w.Body.String(), `"code":"SNAPSHOT_PENDING"`, "response should expose a retryable pending-snapshot code")
	require.NoError(t, mock.ExpectationsWereMet(), "handler should not query PRs or enqueue jobs while snapshot upload is pending")
}

func TestSessionHandler_PushChangesToPR_NoPR(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{}))

	// PR lookup returns no rows — session has never had a PR opened.
	mock.ExpectQuery("SELECT .+ FROM pull_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr/push", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.PushChangesToPR(w, req)

	require.Equal(t, http.StatusNotFound, w.Code, "should return 404 when no PR exists for the session")
	require.Contains(t, w.Body.String(), "NO_PR", "error code should distinguish missing-PR from session-not-found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_PushChangesToPR_PRClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		prStatus  string
		wantSlice string
	}{
		{name: "merged PR", prStatus: "merged", wantSlice: "PR_CLOSED"},
		{name: "closed PR", prStatus: "closed", wantSlice: "PR_CLOSED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			now := time.Now()
			orgID := uuid.New()
			sessionID := uuid.New()
			issueID := uuid.New()
			prID := uuid.New()
			handler := newSessionHandler(t, mock)

			mock.ExpectQuery("SELECT .+ FROM sessions").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{}))

			row := sessionPullRequestRow(prID, &sessionID, orgID, "owner/repo", now)
			row[8] = tt.prStatus // status column
			mock.ExpectQuery("SELECT .+ FROM pull_requests").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pgxmock.NewRows(sessionPullRequestColumns).AddRow(row...))

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr/push", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", sessionID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.PushChangesToPR(w, req)

			require.Equal(t, http.StatusConflict, w.Code, "should return 409 when PR is no longer open")
			require.Contains(t, w.Body.String(), tt.wantSlice, "error code should indicate PR is closed/merged")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_PushChangesToPR_InFlightRejectsDuplicateSubmit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state string
	}{
		{name: "queued state rejects duplicate", state: "queued"},
		{name: "pushing state rejects duplicate", state: "pushing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			now := time.Now()
			orgID := uuid.New()
			sessionID := uuid.New()
			issueID := uuid.New()
			handler := newSessionHandler(t, mock)

			mock.ExpectQuery("SELECT .+ FROM sessions").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{pushState: tt.state}))

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr/push", nil)
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", sessionID.String())
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.PushChangesToPR(w, req)

			require.Equal(t, http.StatusConflict, w.Code, "in-flight push should reject duplicate submits")
			require.Contains(t, w.Body.String(), "PR_PUSH_IN_FLIGHT", "error code should indicate an in-flight push")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_PushChangesToPR_SnapshotExpired(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	// Sandbox destroyed simulates true retention expiry. The PR row may still
	// exist but pushing requires a hydratable sandbox.
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{
			sandboxState: "destroyed",
			nilSnapshot:  true,
		}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr/push", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.PushChangesToPR(w, req)

	require.Equal(t, http.StatusGone, w.Code, "should return 410 when snapshot retention has expired")
	require.Contains(t, w.Body.String(), "SNAPSHOT_EXPIRED", "error code should indicate snapshot expiry")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_PushChangesToPR_SnapshotNotCaptured(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{nilSnapshot: true}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr/push", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.PushChangesToPR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "should return 409 when no snapshot was captured")
	require.Contains(t, w.Body.String(), "SNAPSHOT_NOT_CAPTURED", "error code should indicate missing checkpoint")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_RetrySession_DefaultsToCheckpoint(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now().UTC()
	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	snapshotKey := "snapshots/session.tar"
	diffStats := json.RawMessage(`{"files_changed":7}`)
	handler := newSessionHandler(t, mock)
	handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop()))

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(retrySessionRow(sessionID, orgID, models.SessionStatusFailed, &snapshotKey, nil, models.SandboxStateSnapshotted, diffStats, now)...))
	mock.ExpectQuery("session_messages").
		WithArgs(sessionAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(sessionThreadHandlerColumns).AddRow(sessionThreadHandlerRow(threadID, sessionID, orgID, models.ThreadStatusFailed, 2, now)...))
	mock.ExpectQuery("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(retrySessionRow(sessionID, orgID, models.SessionStatusRunning, &snapshotKey, nil, models.SandboxStateSnapshotted, diffStats, now)...))
	mock.ExpectQuery("UPDATE session_threads").
		WithArgs(sessionAnyArgs(5)...).
		WillReturnRows(pgxmock.NewRows(sessionThreadHandlerColumns).AddRow(sessionThreadHandlerRow(threadID, sessionID, orgID, models.ThreadStatusRunning, 2, now)...))
	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(sessionAnyArgs(11)...).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(42), now))

	var jobPayload []byte
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), capturingArg(&jobPayload), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	var auditDetails []byte
	mock.ExpectQuery("INSERT INTO audit_logs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), capturingJSONArg{dest: &auditDetails},
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), now))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/retry", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.RetrySession(w, req)

	require.Equal(t, http.StatusOK, w.Code, "checkpoint retry should return the claimed running session: %s", w.Body.String())

	var resp models.SingleResponse[models.Session]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "response should decode as a session response")
	require.Equal(t, models.SessionStatusRunning, resp.Data.Status, "checkpoint retry should mark the session running")
	require.JSONEq(t, string(diffStats), string(resp.Data.DiffStats), "checkpoint retry should preserve existing diff stats")

	var payload map[string]string
	require.NoError(t, json.Unmarshal(jobPayload, &payload), "job payload should decode as string map")
	require.Equal(t, sessionID.String(), payload["session_id"], "continue_session payload should include the session id")
	require.Equal(t, threadID.String(), payload["thread_id"], "continue_session payload should include the retry thread id")

	var details map[string]any
	require.NoError(t, json.Unmarshal(auditDetails, &details), "audit details should decode as an object")
	require.Equal(t, string(models.SessionRetryModeCheckpoint), details["retry_mode"], "audit details should record checkpoint retry mode")
	require.Equal(t, "continue_session", details["job_type"], "audit details should record continue_session job type")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_RetrySession_CheckpointRejectsMissingSnapshot(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now().UTC()
	orgID := uuid.New()
	sessionID := uuid.New()
	diffStats := json.RawMessage(`{"files_changed":7}`)
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(retrySessionRow(sessionID, orgID, models.SessionStatusFailed, nil, nil, models.SandboxStateNone, diffStats, now)...))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/retry", strings.NewReader(`{"mode":"checkpoint"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.RetrySession(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "checkpoint retry should reject sessions without a saved checkpoint")
	require.Contains(t, w.Body.String(), "CHECKPOINT_UNAVAILABLE", "error response should explain the missing checkpoint")
	require.NoError(t, mock.ExpectationsWereMet(), "checkpoint retry should not issue any diff-clearing update")
}

func TestSessionHandler_RetrySession_StartOverUsesRunAgent(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now().UTC()
	orgID := uuid.New()
	sessionID := uuid.New()
	handler := newSessionHandler(t, mock)
	diffStats := json.RawMessage(`{"files_changed":7}`)

	mock.ExpectQuery("SELECT status FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow("failed"))
	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	var jobPayload []byte
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), capturingArg(&jobPayload), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(retrySessionRow(sessionID, orgID, models.SessionStatusPending, nil, nil, models.SandboxStateNone, nil, now)...))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/retry", strings.NewReader(`{"mode":"start_over"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.RetrySession(w, req)

	require.Equal(t, http.StatusOK, w.Code, "start-over retry should succeed for a failed session")

	var payload map[string]string
	require.NoError(t, json.Unmarshal(jobPayload, &payload), "job payload should decode as string map")
	require.Equal(t, sessionID.String(), payload["session_id"], "run_agent payload should include the session id")
	require.Empty(t, payload["thread_id"], "start-over retry should not enqueue a thread-scoped continuation")

	var resp models.SingleResponse[models.Session]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "response should decode as a session response")
	require.Nil(t, resp.Data.DiffStats, "start-over retry should return cleared diff stats")
	require.NotEqual(t, string(diffStats), string(resp.Data.DiffStats), "start-over retry should not preserve prior diff stats")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_RetrySession_InvalidMode(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	handler := newSessionHandler(t, mock)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/retry", strings.NewReader(`{"mode":"fresh"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.RetrySession(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "invalid retry mode should return 400")
	require.Contains(t, w.Body.String(), "INVALID_RETRY_MODE", "error response should identify retry mode validation failures")
	require.NoError(t, mock.ExpectationsWereMet(), "invalid mode should not hit the database")
}

func TestSessionHandler_PushChangesToPR_SessionNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr/push", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.PushChangesToPR(w, req)

	require.Equal(t, http.StatusNotFound, w.Code, "should return 404 when session does not exist")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// Defense-in-depth: the frontend gates the push button on isRunning, but a
// stale tab or a racing client could still POST while the session is mid-turn.
// The handler must reject it server-side so the worker doesn't hydrate a
// snapshot the active turn is about to invalidate.
func TestSessionHandler_PushChangesToPR_RejectsRunningSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pushSessionRow(sessionID, issueID, orgID, now, pushSessionRowOpts{
			status: string(models.SessionStatusRunning),
		}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/pr/push", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.PushChangesToPR(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "should return 409 when session is currently running")
	require.Contains(t, w.Body.String(), "SESSION_RUNNING", "error code should distinguish running-session from other in-flight states")
	require.NoError(t, mock.ExpectationsWereMet(), "handler should not query PRs or enqueue jobs while session is running")
}

// mockCanceller implements SessionCanceller for testing.
type mockCanceller struct {
	called    bool
	sessionID uuid.UUID
	result    bool
}

func (m *mockCanceller) CancelSession(sessionID uuid.UUID) bool {
	m.called = true
	m.sessionID = sessionID
	return m.result
}

type fakeSessionWorkerSelector struct {
	worker previewsvc.WorkerNode
	err    error
	calls  []string
}

func (f *fakeSessionWorkerSelector) ResolveNode(_ context.Context, nodeID string) (previewsvc.WorkerNode, error) {
	f.calls = append(f.calls, nodeID)
	return f.worker, f.err
}

type fakeSessionWorkerCancelClient struct {
	resp   *previewsvc.RemoteCancelSessionResponse
	err    error
	calls  []previewsvc.RemoteCancelSessionRequest
	worker previewsvc.WorkerNode
}

func (f *fakeSessionWorkerCancelClient) CancelSession(_ context.Context, worker previewsvc.WorkerNode, req previewsvc.RemoteCancelSessionRequest) (*previewsvc.RemoteCancelSessionResponse, error) {
	f.calls = append(f.calls, req)
	f.worker = worker
	return f.resp, f.err
}

func TestSessionHandler_CancelSession_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)
	canceller := &mockCanceller{result: true}
	handler.SetCanceller(canceller)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "running", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, now, "running", nil,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectExec("INSERT INTO session_cancel_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE session_cancel_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "cancel should return 202 Accepted")
	require.Contains(t, w.Body.String(), `"status":"running"`, "response should still show running status")
	require.True(t, canceller.called, "canceller should have been called")
	require.Equal(t, sessionID, canceller.sessionID, "canceller should receive correct session ID")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_CancelSession_RoutesDirectWorkerCancelWhenLocalRegistryMisses(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	containerID := "sandbox-1"
	workerNodeID := "worker-a"
	handler := newSessionHandler(t, mock)
	canceller := &mockCanceller{result: false}
	selector := &fakeSessionWorkerSelector{
		worker: previewsvc.WorkerNode{ID: workerNodeID, BaseURL: "http://worker-a"},
	}
	client := &fakeSessionWorkerCancelClient{resp: &previewsvc.RemoteCancelSessionResponse{Accepted: true}}
	handler.SetCanceller(canceller)
	handler.SetWorkerRuntime(selector, client, "api-node")
	row := sessionTestRow(
		sessionID, issueID, orgID, "claude_code", "running", "semi", "low",
		nil, nil, nil, nil,
		nil, false, &now, nil, nil,
		nil, nil, nil, false,
		nil, nil, nil, nil, nil,
		nil, nil, nil, nil,
		nil, nil,
		nil, // triggered_by_user_id
		nil, 1, now, "running", nil,
		nil, nil, nil, nil, nil,
		nil,      // input_manifest
		nil, nil, // archived_at, archived_by_user_id
		nil,            // automation_run_id
		"idle",         // pr_creation_state
		(*string)(nil), // pr_creation_error
		nil,            // deleted_at
		now,
	)
	row[11] = &containerID
	row[12] = &workerNodeID

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(row...))
	mock.ExpectExec("INSERT INTO session_cancel_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "cancel should be accepted after direct worker routing: %s", w.Body.String())
	require.True(t, canceller.called, "handler should try the local registry before remote routing")
	require.Equal(t, []string{workerNodeID}, selector.calls, "handler should resolve the session's worker node")
	require.Equal(t, previewsvc.WorkerNode{ID: workerNodeID, BaseURL: "http://worker-a"}, client.worker, "handler should send cancel to the resolved worker")
	require.Equal(t, []previewsvc.RemoteCancelSessionRequest{{OrgID: orgID, SessionID: sessionID}}, client.calls, "handler should send the cancel payload to the worker")
	require.NoError(t, mock.ExpectationsWereMet(), "direct worker cancel should not enqueue a normal worker job")
}

func TestSessionHandler_CancelSession_EnqueuesTargetedWorkerCancelWhenLocalRegistryMisses(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	containerID := "sandbox-1"
	workerNodeID := "worker-a"
	handler := newSessionHandler(t, mock)
	canceller := &mockCanceller{result: false}
	handler.SetCanceller(canceller)
	row := sessionTestRow(
		sessionID, issueID, orgID, "claude_code", "running", "semi", "low",
		nil, nil, nil, nil,
		nil, false, &now, nil, nil,
		nil, nil, nil, false,
		nil, nil, nil, nil, nil,
		nil, nil, nil, nil,
		nil, nil,
		nil, // triggered_by_user_id
		nil, 1, now, "running", nil,
		nil, nil, nil, nil, nil,
		nil,      // input_manifest
		nil, nil, // archived_at, archived_by_user_id
		nil,            // automation_run_id
		"idle",         // pr_creation_state
		(*string)(nil), // pr_creation_error
		nil,            // deleted_at
		now,
	)
	row[11] = &containerID
	row[12] = &workerNodeID

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns).AddRow(row...))
	mock.ExpectExec("INSERT INTO session_cancel_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("INSERT INTO jobs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "cancel should still be accepted when routed through a worker job: %s", w.Body.String())
	require.True(t, canceller.called, "handler should try the local registry before enqueuing")
	require.Equal(t, sessionID, canceller.sessionID, "local registry lookup should use the requested session")
	require.NoError(t, mock.ExpectationsWereMet(), "worker cancel job should be enqueued for the owning node")
}

func TestSessionHandler_CancelSession_RecordsPendingCancelWhenWorkerTargetMissing(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)
	canceller := &mockCanceller{result: false}
	handler.SetCanceller(canceller)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "running", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, now, "running", nil,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)
	mock.ExpectExec("INSERT INTO session_cancel_requests").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, "cancel should be accepted while durable intent waits for worker registration: %s", w.Body.String())
	require.True(t, canceller.called, "handler should try the local registry before relying on durable intent")
	require.NoError(t, mock.ExpectationsWereMet(), "handler should not enqueue a worker job without a known worker target")
}

func TestSessionHandler_UpdateTitle(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	existingTitle := "Original title"
	handler := newSessionHandler(t, mock)
	titleSyncer := &stubSessionPRTitleSyncer{}
	handler.SetPRTitleSyncer(titleSyncer)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, &existingTitle, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, now, "none", nil,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	mock.ExpectExec("UPDATE sessions SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/sessions/"+sessionID.String(), strings.NewReader(`{"title":"Updated session title"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Update(w, req)

	require.Equal(t, http.StatusOK, w.Code, "update should return 200 OK")

	var resp models.SingleResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response should decode")
	require.NotNil(t, resp.Data.Title, "updated session should include title")
	require.Equal(t, "Updated session title", *resp.Data.Title, "response should include the updated title")
	require.True(t, titleSyncer.called, "PR title syncer should be invoked")
	require.Equal(t, "Updated session title", titleSyncer.lastTitle, "PR title syncer should receive the updated title")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_UpdateTitle_SyncFailureStillSucceeds(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	existingTitle := "Original title"
	handler := newSessionHandler(t, mock)
	titleSyncer := &stubSessionPRTitleSyncer{err: errors.New("github unavailable")}
	handler.SetPRTitleSyncer(titleSyncer)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, &now, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, &existingTitle, nil, nil,
				nil, nil,
				nil,
				nil, 1, now, "none", nil,
				nil, nil, nil, nil, nil,
				nil,
				nil, nil,
				nil,
				"idle",
				(*string)(nil),
				nil,
				now,
			),
		)

	mock.ExpectExec("UPDATE sessions SET title").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/sessions/"+sessionID.String(), strings.NewReader(`{"title":"Updated session title"}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.Update(w, req)

	require.Equal(t, http.StatusOK, w.Code, "update should still return 200 when PR sync fails")

	var resp models.SingleResponse[models.Session]
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err, "response should decode")
	require.NotNil(t, resp.Data.Title, "updated session should include title")
	require.Equal(t, "Updated session title", *resp.Data.Title, "response should include the updated title")
	require.True(t, titleSyncer.called, "PR title syncer should still be invoked")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionHandler_UpdateTitle_ErrorPaths(t *testing.T) {
	t.Parallel()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	existingTitle := "Original title"

	tests := []struct {
		name           string
		sessionParam   string
		body           string
		setupMock      func(mock pgxmock.PgxPoolIface)
		expectedStatus int
		expectedCode   string
		expectSync     bool
		expectedTitle  *string
	}{
		{
			name:           "returns bad request for invalid session id",
			sessionParam:   "not-a-uuid",
			body:           `{"title":"Updated session title"}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_ID",
		},
		{
			name:           "returns bad request for invalid json body",
			sessionParam:   sessionID.String(),
			body:           `{"title":`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_BODY",
		},
		{
			name:           "returns bad request when title is missing",
			sessionParam:   sessionID.String(),
			body:           `{}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_BODY",
		},
		{
			name:           "returns bad request for invalid title",
			sessionParam:   sessionID.String(),
			body:           `{"title":"   "}`,
			expectedStatus: http.StatusBadRequest,
			expectedCode:   "INVALID_TITLE",
		},
		{
			name:         "returns not found when session does not exist",
			sessionParam: sessionID.String(),
			body:         `{"title":"Updated session title"}`,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
			},
			expectedStatus: http.StatusNotFound,
			expectedCode:   "NOT_FOUND",
		},
		{
			name:         "returns existing session when title is unchanged",
			sessionParam: sessionID.String(),
			body:         `{"title":"Original title"}`,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, &existingTitle, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "none", nil,
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
			},
			expectedStatus: http.StatusOK,
			expectedTitle:  &existingTitle,
		},
		{
			name:         "returns internal error when update fails",
			sessionParam: sessionID.String(),
			body:         `{"title":"Updated session title"}`,
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						addSessionRow(pgxmock.NewRows(sessionColumns),
							sessionID, issueID, orgID, "claude_code", "completed", "semi", "low",
							nil, nil, nil, nil,
							nil, false, &now, &now, nil,
							nil, nil, nil, false,
							nil, nil, nil, nil, nil,
							nil, &existingTitle, nil, nil,
							nil, nil,
							nil,
							nil, 1, now, "none", nil,
							nil, nil, nil, nil, nil,
							nil,
							nil, nil,
							nil,
							"idle",
							(*string)(nil),
							nil,
							now,
						),
					)
				mock.ExpectExec("UPDATE sessions SET title").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("write failed"))
			},
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   "UPDATE_FAILED",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool without error")
			defer mock.Close()

			handler := newSessionHandler(t, mock)
			titleSyncer := &stubSessionPRTitleSyncer{}
			handler.SetPRTitleSyncer(titleSyncer)

			if tt.setupMock != nil {
				tt.setupMock(mock)
			}

			req := httptest.NewRequest(http.MethodPatch, "/api/v1/sessions/"+tt.sessionParam, strings.NewReader(tt.body))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("id", tt.sessionParam)
			ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			handler.Update(w, req)

			require.Equal(t, tt.expectedStatus, w.Code, "update should return the expected status code")

			if tt.expectedTitle != nil {
				var resp models.SingleResponse[models.Session]
				err = json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "response should decode")
				require.NotNil(t, resp.Data.Title, "response should include the current title")
				require.Equal(t, *tt.expectedTitle, *resp.Data.Title, "response should preserve the existing title")
			} else if tt.expectedCode != "" {
				var resp models.ErrorResponse
				err = json.Unmarshal(w.Body.Bytes(), &resp)
				require.NoError(t, err, "error response should decode")
				require.Equal(t, tt.expectedCode, resp.Error.Code, "error response should include the expected code")
			}

			require.Equal(t, tt.expectSync, titleSyncer.called, "PR title syncer should only run for successful updates")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionHandler_CancelSession_NotRunning(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)
	canceller := &mockCanceller{result: true}
	handler.SetCanceller(canceller)

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "idle", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, now, "snapshotted", stringPtr("snapshots/test.tar"),
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusConflict, w.Code, "cancelling non-running session should return 409")
	require.Contains(t, w.Body.String(), "NOT_RUNNING")
	require.False(t, canceller.called, "canceller should not be called for non-running sessions")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_CancelSession_InvalidID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	handler := newSessionHandler(t, mock)
	canceller := &mockCanceller{result: true}
	handler.SetCanceller(canceller)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/not-a-uuid/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "not-a-uuid")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_ID")
	require.False(t, canceller.called)
}

func TestSessionHandler_CancelSession_NoCanceller(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	handler := newSessionHandler(t, mock)
	// Don't set canceller — leave it nil.

	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			addSessionRow(pgxmock.NewRows(sessionColumns),
				sessionID, issueID, orgID, "claude_code", "running", "semi", "low",
				nil, nil, nil, nil,
				nil, false, &now, nil, nil,
				nil, nil, nil, false,
				nil, nil, nil, nil, nil,
				nil, nil, nil, nil,
				nil, nil,
				nil, // triggered_by_user_id
				nil, 1, now, "running", nil,
				nil, nil, nil, nil, nil,
				nil,      // input_manifest
				nil, nil, // archived_at, archived_by_user_id
				nil,            // automation_run_id
				"idle",         // pr_creation_state
				(*string)(nil), // pr_creation_error
				nil,            // deleted_at
				now,
			),
		)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/cancel", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", sessionID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.CancelSession(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "CANCEL_UNAVAILABLE")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionHandler_ArchiveSession(t *testing.T) {
	t.Parallel()

	t.Run("archives session successfully", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()

		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusOK, w.Code, "archive should return 200")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns 401 when user is not authenticated", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		sessionID := uuid.New()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, uuid.New())
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("returns 404 when session not found", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()

		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("cleans up snapshot when archiving session with snapshot key", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock pool")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		snapshotStore := &archiveTestSnapshotStore{}
		handler.SetSnapshotStore(snapshotStore)

		orgID := uuid.New()
		sessionID := uuid.New()
		issueID := uuid.New()
		userID := uuid.New()
		now := time.Now()
		snapshotKey := "snapshots/session.tar.zst"

		mock.ExpectQuery("SELECT .+ FROM sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				addSessionRow(pgxmock.NewRows(sessionColumns),
					sessionID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
					nil, nil, nil, nil,
					nil, false, &now, &now, nil,
					nil, nil, nil, false,
					nil, nil, nil, nil, nil,
					nil, nil, nil, nil,
					nil, nil, nil,
					nil, 0, now, "saved", &snapshotKey,
					nil, nil, nil, nil, nil, nil,
					nil, nil,
					nil,
					"idle",
					(*string)(nil),
					nil,
					now,
				),
			)
		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectExec("UPDATE sessions\\s+SET snapshot_key = NULL, sandbox_state = 'destroyed'").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusOK, w.Code, "archive should return 200 after snapshot cleanup")
		require.Equal(t, []string{snapshotKey}, snapshotStore.deleted, "archive should delete the stored snapshot exactly once")
		require.NoError(t, mock.ExpectationsWereMet(), "archive should satisfy all database expectations")
	})

	t.Run("still archives when preload lookup fails for audit", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock pool")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop()))

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()

		mock.ExpectQuery("SELECT .+ FROM sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("db down"))
		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectQuery("INSERT INTO audit_logs").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusOK, w.Code, "archive should still succeed when the preload lookup fails")
		require.NoError(t, mock.ExpectationsWereMet(), "archive should satisfy all database expectations")
	})

	t.Run("ignores snapshot cleanup failure after archive succeeds", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgx mock pool")
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		snapshotStore := &archiveTestSnapshotStore{err: errors.New("delete failed")}
		handler.SetSnapshotStore(snapshotStore)

		orgID := uuid.New()
		sessionID := uuid.New()
		issueID := uuid.New()
		userID := uuid.New()
		now := time.Now()
		snapshotKey := "snapshots/session.tar.zst"

		mock.ExpectQuery("SELECT .+ FROM sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				addSessionRow(pgxmock.NewRows(sessionColumns),
					sessionID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
					nil, nil, nil, nil,
					nil, false, &now, &now, nil,
					nil, nil, nil, false,
					nil, nil, nil, nil, nil,
					nil, nil, nil, nil,
					nil, nil, nil,
					nil, 0, now, "saved", &snapshotKey,
					nil, nil, nil, nil, nil, nil,
					nil, nil,
					nil,
					"idle",
					(*string)(nil),
					nil,
					now,
				),
			)
		mock.ExpectExec("UPDATE sessions SET archived_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/archive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID})
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.ArchiveSession(w, req)

		require.Equal(t, http.StatusOK, w.Code, "archive should still succeed when snapshot cleanup fails")
		require.Equal(t, []string{snapshotKey}, snapshotStore.deleted, "archive should still attempt snapshot cleanup")
		require.NoError(t, mock.ExpectationsWereMet(), "archive should satisfy all database expectations")
	})
}

func TestSessionHandler_UnarchiveSession(t *testing.T) {
	t.Parallel()

	t.Run("unarchives session successfully", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		sessionID := uuid.New()

		mock.ExpectExec("UPDATE sessions SET archived_at = NULL").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/unarchive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.UnarchiveSession(w, req)

		require.Equal(t, http.StatusOK, w.Code, "unarchive should return 200")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns 404 when session not found or not archived", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)
		orgID := uuid.New()
		sessionID := uuid.New()

		mock.ExpectExec("UPDATE sessions SET archived_at = NULL").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID.String()+"/unarchive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", sessionID.String())
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, orgID)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.UnarchiveSession(w, req)

		require.Equal(t, http.StatusNotFound, w.Code)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("returns 400 for invalid session ID", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := newSessionHandler(t, mock)

		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/not-a-uuid/unarchive", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "not-a-uuid")
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		ctx = middleware.WithOrgID(ctx, uuid.New())
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.UnarchiveSession(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func stringPtr(s string) *string {
	return &s
}
