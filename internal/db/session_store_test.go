package db

import (
	"context"
	"testing"
	"time"

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
	"pm_plan_id", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "triggered_by_user_id",
	"created_at",
}

func newAgentSessionRow(sessionID, issueID, orgID uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		sessionID, issueID, orgID, "claude-code", "completed", "supervised", "standard",
		nil, nil, nil, nil,
		nil, &now, &now, nil,
		nil, nil, nil, nil,
		nil, nil, nil, nil, nil,
		nil, nil, nil,
		nil, nil, nil,
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

	mock.ExpectQuery("SELECT .+ FROM sessions WHERE org_id .+ issue_id IN \\(SELECT id FROM issues WHERE repository_id").
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
