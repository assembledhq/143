package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var agentRunColumns = []string{
	"id", "issue_id", "org_id", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_run_id", "revision_context", "error", "result_summary", "diff", "created_at",
}

func newAgentRunRow(id, issueID, orgID uuid.UUID, now time.Time) []interface{} {
	return []interface{}{
		id, issueID, orgID, "fixer", "pending", "supervised", "standard",
		nil, nil, nil, []string{},
		nil, nil, nil, json.RawMessage(`{}`),
		nil, nil, []string{}, nil,
		nil, json.RawMessage(`{}`), nil, nil, nil, now,
	}
}

func TestAgentRunStore_ListByOrg_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunStore(mock)
	orgID := uuid.New()
	runID1 := uuid.New()
	runID2 := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM agent_runs WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(agentRunColumns).
				AddRow(newAgentRunRow(runID1, issueID, orgID, now)...).
				AddRow(newAgentRunRow(runID2, issueID, orgID, now)...),
		)

	runs, err := store.ListByOrg(context.Background(), orgID, AgentRunFilters{})
	require.NoError(t, err)
	assert.Len(t, runs, 2)
	assert.Equal(t, runID1, runs[0].ID)
	assert.Equal(t, runID2, runs[1].ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunStore_ListByOrg_WithStatusFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunStore(mock)
	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM agent_runs WHERE org_id .+ AND status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(agentRunColumns).
				AddRow(newAgentRunRow(runID, issueID, orgID, now)...),
		)

	runs, err := store.ListByOrg(context.Background(), orgID, AgentRunFilters{Status: "running"})
	require.NoError(t, err)
	assert.Len(t, runs, 1)
	assert.Equal(t, runID, runs[0].ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunStore_GetByID_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunStore(mock)
	orgID := uuid.New()
	runID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM agent_runs WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(agentRunColumns).
				AddRow(newAgentRunRow(runID, issueID, orgID, now)...),
		)

	run, err := store.GetByID(context.Background(), orgID, runID)
	require.NoError(t, err)
	assert.Equal(t, runID, run.ID)
	assert.Equal(t, issueID, run.IssueID)
	assert.Equal(t, "fixer", run.AgentType)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunStore_GetByID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunStore(mock)
	orgID := uuid.New()
	runID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM agent_runs WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(agentRunColumns))

	_, err = store.GetByID(context.Background(), orgID, runID)
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunStore_Create_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	run := &models.AgentRun{
		IssueID:       uuid.New(),
		OrgID:         uuid.New(),
		AgentType:     "fixer",
		Status:        "pending",
		AutonomyLevel: "supervised",
		TokenMode:     "standard",
	}

	mock.ExpectQuery("INSERT INTO agent_runs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), run)
	require.NoError(t, err)
	assert.Equal(t, generatedID, run.ID)
	assert.Equal(t, now, run.CreatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunStore_UpdateStatus_Running(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunStore(mock)
	orgID := uuid.New()
	runID := uuid.New()

	mock.ExpectExec("UPDATE agent_runs SET status .+ started_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, runID, "running")
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunStore_UpdateStatus_Completed(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunStore(mock)
	orgID := uuid.New()
	runID := uuid.New()

	mock.ExpectExec("UPDATE agent_runs SET status .+ completed_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, runID, "completed")
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunStore_ListByIssue_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunStore(mock)
	orgID := uuid.New()
	issueID := uuid.New()
	runID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM agent_runs WHERE org_id .+ AND issue_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(agentRunColumns).
				AddRow(newAgentRunRow(runID, issueID, orgID, now)...),
		)

	runs, err := store.ListByIssue(context.Background(), orgID, issueID)
	require.NoError(t, err)
	assert.Len(t, runs, 1)
	assert.Equal(t, runID, runs[0].ID)
	assert.Equal(t, issueID, runs[0].IssueID)
	assert.NoError(t, mock.ExpectationsWereMet())
}
