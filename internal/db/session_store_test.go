package db

import (
	"context"
	"encoding/json"
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
	"container_id", "started_at", "completed_at", "token_usage",
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
		nil, &startedAt, &completedAt, nil,
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
