package db

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var sessionColumns = []string{
	"id", "primary_issue_id", "org_id", "origin", "interaction_mode", "validation_policy",
	"agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "worker_node_id", "turn_holding_container", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "reasoning_effort", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at",
	"sandbox_state", "snapshot_key", "pending_snapshot_key", "pending_snapshot_set_at",
	"runtime_soft_deadline_at", "runtime_hard_deadline_at", "runtime_last_progress_at", "runtime_last_progress_type", "runtime_last_progress_strength",
	"runtime_extension_count", "runtime_extension_seconds", "runtime_stop_reason", "runtime_graceful_stop_at",
	"checkpointed_at", "checkpoint_kind", "checkpoint_capability", "checkpoint_size_bytes", "checkpoint_error",
	"recovery_state", "recovery_queued_at", "recovery_started_at", "recovery_attempt_count",
	"target_branch", "working_branch",
	"base_commit_sha", "repository_id", "diff_stats", "diff_history", "input_manifest",
	"archived_at", "archived_by_user_id", "automation_run_id",
	"pr_creation_state", "pr_creation_error", "pr_push_state", "pr_push_error", "diff_collected_at", "latest_diff_snapshot_id",
	"has_unpushed_changes",
	"linear_private", "linear_state_sync_disabled", "linear_identifier_hint", "linear_prepare_state",
	"deleted_at", "git_identity_source", "git_identity_user_id", "created_at",
}

func newSessionRow(id, issueID, orgID uuid.UUID, now time.Time) []interface{} {
	var primaryIssueID any
	if issueID != uuid.Nil {
		issueIDCopy := issueID
		primaryIssueID = &issueIDCopy
	}
	return []interface{}{
		id, primaryIssueID, orgID, "issue_trigger", "single_run", "on_turn_complete",
		"claude_code", "pending", "supervised", "low",
		nil, nil, nil, []string{},
		nil, nil, false, nil, nil, json.RawMessage(`{}`),
		nil, nil, []string{}, false,
		nil, json.RawMessage(`{}`), nil, nil, nil,
		nil, nil, nil, nil,
		nil,      // project_task_id
		nil,      // model_override
		nil,      // reasoning_effort
		nil,      // triggered_by_user_id
		nil,      // agent_session_id
		0,        // current_turn
		now,      // last_activity_at
		"none",   // sandbox_state
		nil,      // snapshot_key
		nil,      // pending_snapshot_key
		nil,      // pending_snapshot_set_at
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
		nil,            // diff_collected_at
		nil,            // latest_diff_snapshot_id
		false,          // has_unpushed_changes
		false,          // linear_private
		false,          // linear_state_sync_disabled
		(*string)(nil), // linear_identifier_hint
		"none",         // linear_prepare_state
		nil,            // deleted_at
		nil,            // git_identity_source
		nil,            // git_identity_user_id
		now,            // created_at
	}
}

func TestSessionStore_ListByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID1 := uuid.New()
	runID2 := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		filters   SessionFilters
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  int
		expectErr bool
	}{
		{
			name:    "returns agent runs for org",
			filters: SessionFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).
							AddRow(newSessionRow(runID1, issueID, orgID, now)...).
							AddRow(newSessionRow(runID2, issueID, orgID, now)...),
					)
			},
			expected: 2,
		},
		{
			name:    "returns filtered agent runs by status",
			filters: SessionFilters{Statuses: []models.SessionStatus{models.SessionStatusRunning}},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ AND status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).
							AddRow(newSessionRow(runID1, issueID, orgID, now)...),
					)
			},
			expected: 1,
		},
		{
			name:    "returns filtered agent runs by multiple statuses",
			filters: SessionFilters{Statuses: []models.SessionStatus{models.SessionStatusPending, models.SessionStatusRunning}},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ AND status = ANY").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).
							AddRow(newSessionRow(runID1, issueID, orgID, now)...).
							AddRow(newSessionRow(runID2, issueID, orgID, now)...),
					)
			},
			expected: 2,
		},
		{
			name:    "returns only ad-hoc runs when AdHocOnly is true",
			filters: SessionFilters{AdHocOnly: true},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ AND pm_plan_id IS NULL").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).
							AddRow(newSessionRow(runID1, issueID, orgID, now)...),
					)
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			tt.setupMock(mock)

			runs, err := store.ListByOrg(context.Background(), orgID, tt.filters)
			if tt.expectErr {
				require.Error(t, err, "ListByOrg should return an error")
				return
			}
			require.NoError(t, err, "ListByOrg should not return an error")
			require.Len(t, runs, tt.expected, "should return expected number of agent runs")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_GetByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, runID, issueID uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns agent run when found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, runID, issueID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).
							AddRow(newSessionRow(runID, issueID, orgID, now)...),
					)
			},
		},
		{
			name: "returns error when agent run not found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, runID, issueID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
			},
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
			orgID := uuid.New()
			runID := uuid.New()
			issueID := uuid.New()
			now := time.Now()
			tt.setupMock(mock, orgID, runID, issueID, now)

			run, err := store.GetByID(context.Background(), orgID, runID)
			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error when agent run is not found")
				return
			}
			require.NoError(t, err, "GetByID should not return an error")
			require.Equal(t, runID, run.ID, "should return the correct agent run ID")
			require.NotNil(t, run.PrimaryIssueID, "should populate the primary issue ID")
			require.Equal(t, issueID, *run.PrimaryIssueID, "should return the correct issue ID")
			require.Equal(t, models.AgentType("claude_code"), run.AgentType, "should return the correct agent type")
			require.False(t, run.HasUnpushedChanges, "GetByID should default has_unpushed_changes to false when the derived column is false")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_GetByID_WithUnpushedChanges(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	now := time.Now()
	row := newSessionRow(runID, issueID, orgID, now)
	row[78] = true // has_unpushed_changes

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).
				AddRow(row...),
		)

	run, err := store.GetByID(context.Background(), orgID, runID)
	require.NoError(t, err, "GetByID should not return an error when the session exists")
	require.True(t, run.HasUnpushedChanges, "GetByID should surface the derived has_unpushed_changes flag")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_ListRecentByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).
				AddRow(newSessionRow(runID, issueID, orgID, now)...),
		)

	store := NewSessionStore(mock)
	runs, err := store.ListRecentByOrg(context.Background(), orgID, []string{"completed", "failed"}, 20)
	require.NoError(t, err, "ListRecentByOrg should succeed")
	require.Len(t, runs, 1, "should return expected runs")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	threadID := uuid.New()

	issueID := uuid.New()
	orgID := uuid.New()
	modelOverride := "opus-4-7"
	run := &models.Session{
		PrimaryIssueID: &issueID,
		OrgID:          orgID,
		AgentType:      "claude_code",
		Status:         "pending",
		AutonomyLevel:  "supervised",
		TokenMode:      "low",
		ModelOverride:  &modelOverride,
	}

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "last_activity_at"}).
				AddRow(generatedID, now, now),
		)
	// Pin the seeded primary thread's args so a regression that swaps fields
	// (e.g. status defaulted to 'pending', or label hard-coded to the agent
	// name) is caught by this test instead of slipping through under
	// AnyArg() matchers. Order mirrors the named-args block in
	// SessionStore.Create.
	mock.ExpectQuery("INSERT INTO session_threads").
		WithArgs(generatedID, orgID, models.AgentType("claude_code"), &modelOverride, "Main", models.ThreadStatusIdle).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(threadID))
	mock.ExpectExec("INSERT INTO session_issue_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	err = store.Create(context.Background(), run)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, generatedID, run.ID, "should set the generated ID on the agent run")
	require.Equal(t, now, run.CreatedAt, "should set the created_at timestamp on the agent run")
	require.Equal(t, now, run.LastActivityAt, "should set the last_activity_at timestamp on the agent run")
	require.NotNil(t, run.PrimaryThreadID, "Create should expose the seeded primary thread ID")
	require.Equal(t, threadID, *run.PrimaryThreadID, "Create should expose the seeded primary thread ID")
	require.NotNil(t, run.PrimaryIssueID, "Create should preserve the primary issue ID for issue-backed sessions")
	require.Equal(t, issueID, *run.PrimaryIssueID, "Create should preserve the primary issue ID on issue-backed sessions")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_Create_AllowsNilIssueID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	orgID := uuid.New()

	run := &models.Session{
		OrgID:         orgID,
		AgentType:     "claude_code",
		Status:        "pending",
		AutonomyLevel: "supervised",
		TokenMode:     "low",
	}

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "last_activity_at"}).
				AddRow(generatedID, now, now),
		)
	// Pin the seeded primary thread args (label "Main", idle status, agent
	// mirrored from the session, no model override) so a regression that
	// changes any of these defaults is caught here as well as in the
	// happy-path Create test.
	mock.ExpectQuery("INSERT INTO session_threads").
		WithArgs(generatedID, orgID, models.AgentType("claude_code"), (*string)(nil), "Main", models.ThreadStatusIdle).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectCommit()

	err = store.Create(context.Background(), run)
	require.NoError(t, err, "Create should not return an error for zero-issue sessions")
	require.Equal(t, generatedID, run.ID, "should set the generated ID on the agent run")
	require.Equal(t, now, run.CreatedAt, "should set the created_at timestamp on the agent run")
	require.Nil(t, run.PrimaryIssueID, "Create should keep the primary issue unset for issue-less sessions")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_Create_RollsBackWhenPrimaryLinkInsertFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	issueID := uuid.New()
	orgID := uuid.New()

	run := &models.Session{
		PrimaryIssueID: &issueID,
		OrgID:          orgID,
		AgentType:      "claude_code",
		Status:         "pending",
		AutonomyLevel:  "supervised",
		TokenMode:      "low",
	}

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "last_activity_at"}).
				AddRow(generatedID, now, now),
		)
	// Pin the seeded primary thread args so a rollback regression that
	// also corrupts the thread INSERT's defaults (label, status, mirrored
	// agent_type) is caught here.
	mock.ExpectQuery("INSERT INTO session_threads").
		WithArgs(generatedID, orgID, models.AgentType("claude_code"), (*string)(nil), "Main", models.ThreadStatusIdle).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
	mock.ExpectExec("INSERT INTO session_issue_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.DeadlineExceeded)
	mock.ExpectRollback()

	err = store.Create(context.Background(), run)
	require.Error(t, err, "Create should fail when inserting the primary issue link fails")
	require.Contains(t, err.Error(), "insert session issue link", "Create should wrap primary issue link failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_GetByID_PreservesPersistedPolicy(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	row := newSessionRow(runID, issueID, orgID, now)
	row[3] = models.SessionOriginIssueTrigger
	row[4] = models.SessionInteractionModeSingleRun
	row[5] = models.SessionValidationPolicyOnTurnComplete
	row[36] = &userID

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).
				AddRow(row...),
		)

	run, err := store.GetByID(context.Background(), orgID, runID)
	require.NoError(t, err, "GetByID should not return an error")
	require.Equal(t, models.SessionOriginIssueTrigger, run.Origin, "GetByID should preserve the persisted origin")
	require.Equal(t, models.SessionInteractionModeSingleRun, run.InteractionMode, "GetByID should preserve the persisted interaction mode")
	require.Equal(t, models.SessionValidationPolicyOnTurnComplete, run.ValidationPolicy, "GetByID should preserve the persisted validation policy")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_UpdateStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  string
		queryRE string
	}{
		{
			name:    "sets started_at when transitioning to running",
			status:  "running",
			queryRE: "UPDATE sessions SET status .+ started_at",
		},
		{
			name:    "sets completed_at when transitioning to completed",
			status:  "completed",
			queryRE: "UPDATE sessions SET status .+ completed_at",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			orgID := uuid.New()
			runID := uuid.New()

			now := time.Now()
			mock.ExpectQuery(tt.queryRE).
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(sessionTestColumns).AddRow(
						newAgentSessionRow(runID, uuid.New(), orgID, now)...,
					),
				)

			err = store.UpdateStatus(context.Background(), orgID, runID, tt.status)
			require.NoError(t, err, "UpdateStatus should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_ListByIssue(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	orgID := uuid.New()
	issueID := uuid.New()
	runID := uuid.New()
	now := time.Now()

	mock.ExpectQuery(`(?s)SELECT .+ FROM sessions
		WHERE org_id = @org_id AND deleted_at IS NULL AND EXISTS \(
			SELECT 1
			FROM session_issue_links sil
			WHERE sil.org_id = sessions.org_id AND sil.session_id = sessions.id AND sil.issue_id = @issue_id
		\)
		ORDER BY created_at DESC`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).
				AddRow(newSessionRow(runID, issueID, orgID, now)...),
		)

	runs, err := store.ListByIssue(context.Background(), orgID, issueID)
	require.NoError(t, err, "ListByIssue should not return an error")
	require.Len(t, runs, 1, "should return the agent run for the issue")
	require.Equal(t, runID, runs[0].ID, "should return the correct agent run ID")
	require.NotNil(t, runs[0].PrimaryIssueID, "should populate the primary issue ID")
	require.Equal(t, issueID, *runs[0].PrimaryIssueID, "should return the correct issue ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// TestSessionSelectColumns_MatchesModel is a regression test ensuring that
// sessionSelectColumns includes every column expected by the Session model struct.
// Without this, adding a new field to models.Session but forgetting to add it to
// sessionSelectColumns causes GetByID/List queries to fail at runtime (the bug
// that caused "failed to load session details" for manual sessions).
func TestSessionSelectColumns_MatchesModel(t *testing.T) {
	t.Parallel()

	// Extract column names from the Session struct's db tags.
	typ := reflect.TypeOf(models.Session{})
	structCols := make(map[string]bool)
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("db")
		if tag != "" && tag != "-" {
			structCols[tag] = true
		}
	}

	// Parse column names out of sessionSelectColumns.
	// Handles plain columns ("org_id") and aliased expressions ("COALESCE(...) AS issue_id").
	selectCols := make(map[string]bool)
	for _, part := range strings.Split(sessionSelectColumns, ",") {
		col := strings.TrimSpace(part)
		if col == "" {
			continue
		}
		// If there's an "AS alias", use the alias.
		if idx := strings.LastIndex(strings.ToUpper(col), " AS "); idx != -1 {
			col = strings.TrimSpace(col[idx+4:])
		} else if strings.Contains(col, "(") {
			// Expression without an alias (e.g. COALESCE(...)) — skip, the alias form is required.
			continue
		}
		selectCols[col] = true
	}

	for col := range structCols {
		require.Contains(t, selectCols, col,
			"sessionSelectColumns is missing column %q from models.Session — add it to the SELECT constant", col)
	}
}
