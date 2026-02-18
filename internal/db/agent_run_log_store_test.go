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
	require.NoError(t, err)
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
	require.NoError(t, err)
	assert.Equal(t, int64(1), log.ID)
	assert.Equal(t, now, log.Timestamp)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunLogStore_ListByRunID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
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
	require.NoError(t, err)
	assert.Len(t, logs, 2)
	assert.Equal(t, int64(1), logs[0].ID)
	assert.Equal(t, int64(2), logs[1].ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunLogStore_ListByRunID_Empty(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAgentRunLogStore(mock)

	mock.ExpectQuery("SELECT .+ FROM agent_run_logs WHERE agent_run_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(logColumns))

	logs, err := store.ListByRunID(context.Background(), uuid.New())
	require.NoError(t, err)
	assert.Empty(t, logs)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAgentRunLogStore_ListByRunIDSince_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
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
	require.NoError(t, err)
	assert.Len(t, logs, 1)
	assert.Equal(t, int64(5), logs[0].ID)
	assert.NoError(t, mock.ExpectationsWereMet())
}
