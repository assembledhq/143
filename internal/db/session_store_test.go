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
	"target_branch", "working_branch", "repository_id", "diff_stats", "diff_history", "input_manifest", "deleted_at", "created_at",
}

func newAgentSessionRow(sessionID, issueID, orgID uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		sessionID, issueID, orgID, "claude-code", "completed", "supervised", "low",
		nil, nil, nil, nil,
		nil, &now, &now, nil,
		nil, nil, nil, false,
		nil, nil, nil, nil, nil,
		nil, nil, nil, nil,
		nil, nil, nil,
		nil, 0, nil, "none", nil, // agent_session_id, current_turn, last_activity_at, sandbox_state, snapshot_key
		nil, // target_branch
		nil, // working_branch
		nil, // repository_id
		nil, // diff_stats
		nil, // diff_history
		nil, // input_manifest
		nil, // deleted_at
		now,
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

	mock.ExpectExec("UPDATE sessions SET pm_plan_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdatePMPlanID(context.Background(), uuid.New(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
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

func stringPtr(s string) *string { return &s }
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

	mock.ExpectExec("UPDATE sessions.+SET.+agent_session_id.+snapshot_key").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateSnapshotInfo(context.Background(), uuid.New(), uuid.New(), "agent-123", "snap-key")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_UpdateSandboxState(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectExec("UPDATE sessions SET sandbox_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateSandboxState(context.Background(), uuid.New(), uuid.New(), "snapshotted")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
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
