package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var sessionTestColumns = []string{
	"id", "issue_id", "org_id", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "turn_holding_container", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at", "sandbox_state", "snapshot_key",
	"target_branch", "working_branch", "repository_id", "diff_stats", "diff_history", "input_manifest", "archived_at", "archived_by_user_id", "automation_run_id", "deleted_at", "created_at",
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
	return []interface{}{
		sessionID, issueID, orgID, "claude-code", "completed", "supervised", "low",
		nil, nil, nil, nil,
		nil, false, &startedAt, &completedAt, nil,
		nil, nil, nil, false,
		nil, nil, nil, nil, nil,
		nil, nil, nil, nil,
		nil, nil, nil,
		nil, 0, lastActivityAt, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
		nil,      // target_branch
		nil,      // working_branch
		nil,      // repository_id
		nil,      // diff_stats
		nil,      // diff_history
		nil,      // input_manifest
		nil, nil, // archived_at, archived_by_user_id
		nil, // automation_run_id
		nil, // deleted_at
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

	mock.ExpectExec("UPDATE sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateResult(context.Background(), orgID, sessionID, "completed", result)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
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
				mock.ExpectQuery("UPDATE sessions\\s+SET status = 'running', completed_at = NULL, last_activity_at = now\\(\\)\\s+WHERE id = @id AND org_id = @org_id AND status IN \\('completed', 'pr_created', 'failed', 'cancelled', 'awaiting_input', 'needs_human_guidance'\\)\\s+AND sandbox_state != 'destroyed'\\s+RETURNING").
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
				mock.ExpectQuery("UPDATE sessions\\s+SET status = 'running', completed_at = NULL, last_activity_at = now\\(\\)\\s+WHERE id = @id AND org_id = @org_id AND status IN \\('completed', 'pr_created', 'failed', 'cancelled', 'awaiting_input', 'needs_human_guidance'\\)\\s+AND sandbox_state != 'destroyed'\\s+RETURNING").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionTestColumns))
			},
			wantErr: true,
		},
		{
			name: "resumes awaiting input session successfully",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				row := newAgentSessionRow(uuid.New(), uuid.New(), uuid.New(), now)
				row[4] = string(models.SessionStatusRunning)
				mock.ExpectQuery("UPDATE sessions\\s+SET status = 'running', completed_at = NULL, last_activity_at = now\\(\\)\\s+WHERE id = @id AND org_id = @org_id AND status IN \\('completed', 'pr_created', 'failed', 'cancelled', 'awaiting_input', 'needs_human_guidance'\\)\\s+AND sandbox_state != 'destroyed'\\s+RETURNING").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
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
				row[4] = string(models.SessionStatusRunning)
				mock.ExpectQuery("UPDATE sessions\\s+SET status = 'running', completed_at = NULL, last_activity_at = now\\(\\)\\s+WHERE id = @id AND org_id = @org_id AND status IN \\('completed', 'pr_created', 'failed', 'cancelled', 'awaiting_input', 'needs_human_guidance'\\)\\s+AND sandbox_state != 'destroyed'\\s+RETURNING").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
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
			require.NoError(t, err)
			defer mock.Close()

			store := NewSessionStore(mock)
			tt.setupMock(mock)

			_, err = store.ClaimForResume(context.Background(), uuid.New(), uuid.New())
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
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

func stringPtr(s string) *string    { return &s }
func float64Ptr(f float64) *float64 { return &f }

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
		ConfidenceScore:     float64Ptr(0.85),
		ConfidenceReasoning: stringPtr("good progress"),
		RiskFactors:         []string{"none"},
		TokenUsage:          json.RawMessage(`{"input":100,"output":200}`),
		ResultSummary:       stringPtr("task done"),
		Diff:                stringPtr("diff content"),
	}

	mock.ExpectExec("UPDATE sessions.+SET status = 'idle'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateTurnComplete(context.Background(), uuid.New(), uuid.New(), 2, result, "agent-123", "snap-key")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
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

	mock.ExpectQuery("SELECT .+ FROM sessions.+WHERE s.status = 'running'").
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

	mock.ExpectQuery("SELECT .+ FROM sessions.+WHERE s.status = 'running'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("db down"))

	_, err = store.ListStaleRunningSessions(context.Background(), time.Now().Add(-1*time.Hour))
	require.Error(t, err)
	require.Contains(t, err.Error(), "query stale running sessions")
	require.NoError(t, mock.ExpectationsWereMet())
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

		err = store.ArchiveSystem(context.Background(), uuid.New(), uuid.New())
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
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

		err = store.ArchiveSystem(context.Background(), uuid.New(), uuid.New())
		require.NoError(t, err, "already-archived sessions should not produce an error")
		require.NoError(t, mock.ExpectationsWereMet())
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
		wantDestroyNow  bool
		wantContainerID string
	}{
		{"destroys when no preview hold", "container-1", false, true, "container-1"},
		{"keeps alive when preview still holds", "container-1", true, false, "container-1"},
		{"no-op when container was already empty", "", false, false, ""},
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
					pgxmock.NewRows([]string{"container_id", "preview_holds"}).
						AddRow(tt.containerID, tt.previewHolds),
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

func TestSessionStore_FinalizeContainerDestroy(t *testing.T) {
	t.Parallel()

	t.Run("clears row when no holder has returned", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		store := NewSessionStore(mock)
		mock.ExpectExec(`UPDATE sessions\s+SET container_id = NULL, sandbox_state = 'snapshotted'`).
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

func TestSessionStore_ClearContainerID_CAS_Clears(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	// WHERE clause must pin container_id = @expected AND no preview hold,
	// and the SET must also reset turn_holding_container=FALSE so a
	// crashed-turn flag doesn't get stranded.
	mock.ExpectExec(`UPDATE sessions\s+SET container_id = NULL,\s+turn_holding_container = FALSE\s+WHERE id = @id\s+AND org_id = @org_id\s+AND container_id = @expected\s+AND NOT EXISTS`).
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
	mock.ExpectQuery(`FROM sessions\s+WHERE container_id IS NOT NULL\s+AND id > @after_id\s+AND NOT EXISTS`).
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
