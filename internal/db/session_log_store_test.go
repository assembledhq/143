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
	"id", "session_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number",
}

func newLogRow(id int64, sessionID uuid.UUID, now time.Time) []any {
	return []any{
		id, sessionID, nil, now, "info", "doing something", json.RawMessage(`{}`), 0,
	}
}

func TestSessionLogStore_Create_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionLogStore(mock)
	now := time.Now()

	log := &models.SessionLog{
		SessionID: uuid.New(),
		Level:      "info",
		Message:    "started execution",
		Metadata:   json.RawMessage(`{"step": 1}`),
	}

	mock.ExpectQuery("INSERT INTO session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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

func TestSessionLogStore_ListByRunID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionLogStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_logs .+ JOIN sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(logColumns).
				AddRow(newLogRow(1, sessionID, now)...).
				AddRow(newLogRow(2, sessionID, now)...),
		)

	logs, err := store.ListByRunID(context.Background(), orgID, sessionID)
	require.NoError(t, err, "should list session logs without error")
	require.Len(t, logs, 2, "should return both log entries for the session")
	require.Equal(t, int64(1), logs[0].ID, "first log entry should have the correct ID")
	require.Equal(t, sessionID, logs[0].SessionID, "first log entry should have the correct session ID")
	require.Equal(t, "info", logs[0].Level, "first log entry should have the correct level")
	require.Equal(t, "doing something", logs[0].Message, "first log entry should have the correct message")
	require.Equal(t, int64(2), logs[1].ID, "second log entry should have the correct ID")
	require.Equal(t, sessionID, logs[1].SessionID, "second log entry should have the correct session ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionLogStore_ListByRunID_Empty(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionLogStore(mock)

	mock.ExpectQuery("SELECT .+ FROM session_logs .+ JOIN sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(logColumns))

	logs, err := store.ListByRunID(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err, "should return no error for empty result set")
	require.Empty(t, logs, "should return empty slice when no logs exist")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionLogStore_ListByRunIDSince_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionLogStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_logs sl JOIN sessions s ON .+ WHERE .+\\.id >").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(logColumns).
				AddRow(newLogRow(5, sessionID, now)...),
		)

	logs, err := store.ListByRunIDSince(context.Background(), orgID, sessionID, 4)
	require.NoError(t, err, "should list session logs since given ID without error")
	require.Len(t, logs, 1, "should return only log entries after the specified ID")
	require.Equal(t, int64(5), logs[0].ID, "returned log entry should have the correct ID")
	require.Equal(t, sessionID, logs[0].SessionID, "returned log entry should have the correct session ID")
	require.Equal(t, "info", logs[0].Level, "returned log entry should have the correct level")
	require.Equal(t, "doing something", logs[0].Message, "returned log entry should have the correct message")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
