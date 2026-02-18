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

var logColumns = []string{
	"id", "agent_run_id", "timestamp", "level", "message", "metadata",
}

func newLogRow(id int64, agentRunID uuid.UUID, now time.Time) []any {
	return []any{
		id, agentRunID, now, "info", "doing something", json.RawMessage(`{}`),
	}
}

func TestAgentRunLogStore_Create_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewAgentRunLogStore(mock)
	now := time.Now()

	log := &models.AgentRunLog{
		AgentRunID: uuid.New(),
		Level:      "info",
		Message:    "started execution",
		Metadata:   json.RawMessage(`{"step": 1}`),
	}

	mock.ExpectQuery("INSERT INTO agent_run_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "timestamp"}).
				AddRow(int64(1), now),
		)

	err = store.Create(context.Background(), log)
	require.NoError(t, err, "should create agent run log without error")
	require.Equal(t, int64(1), log.ID, "should set the generated ID on the log")
	require.Equal(t, now, log.Timestamp, "should set the timestamp on the log")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAgentRunLogStore_ListByRunID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewAgentRunLogStore(mock)
	agentRunID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM agent_run_logs WHERE agent_run_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(logColumns).
				AddRow(newLogRow(1, agentRunID, now)...).
				AddRow(newLogRow(2, agentRunID, now)...),
		)

	logs, err := store.ListByRunID(context.Background(), agentRunID)
	require.NoError(t, err, "should list agent run logs without error")
	require.Len(t, logs, 2, "should return both log entries for the agent run")
	require.Equal(t, int64(1), logs[0].ID, "first log entry should have the correct ID")
	require.Equal(t, agentRunID, logs[0].AgentRunID, "first log entry should have the correct agent run ID")
	require.Equal(t, "info", logs[0].Level, "first log entry should have the correct level")
	require.Equal(t, "doing something", logs[0].Message, "first log entry should have the correct message")
	require.Equal(t, int64(2), logs[1].ID, "second log entry should have the correct ID")
	require.Equal(t, agentRunID, logs[1].AgentRunID, "second log entry should have the correct agent run ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAgentRunLogStore_ListByRunID_Empty(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewAgentRunLogStore(mock)

	mock.ExpectQuery("SELECT .+ FROM agent_run_logs WHERE agent_run_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(logColumns))

	logs, err := store.ListByRunID(context.Background(), uuid.New())
	require.NoError(t, err, "should return no error for empty result set")
	require.Empty(t, logs, "should return empty slice when no logs exist")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAgentRunLogStore_ListByRunIDSince_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewAgentRunLogStore(mock)
	agentRunID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM agent_run_logs WHERE agent_run_id .+ AND id >").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(logColumns).
				AddRow(newLogRow(5, agentRunID, now)...),
		)

	logs, err := store.ListByRunIDSince(context.Background(), agentRunID, 4)
	require.NoError(t, err, "should list agent run logs since given ID without error")
	require.Len(t, logs, 1, "should return only log entries after the specified ID")
	require.Equal(t, int64(5), logs[0].ID, "returned log entry should have the correct ID")
	require.Equal(t, agentRunID, logs[0].AgentRunID, "returned log entry should have the correct agent run ID")
	require.Equal(t, "info", logs[0].Level, "returned log entry should have the correct level")
	require.Equal(t, "doing something", logs[0].Message, "returned log entry should have the correct message")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
