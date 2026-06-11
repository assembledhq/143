package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

var logColumns = []string{
	"id", "session_id", "org_id", "thread_id", "timestamp", "level", "message", "metadata", "turn_number",
}

func newLogRow(id int64, sessionID uuid.UUID, now time.Time) []any {
	return []any{
		id, sessionID, uuid.New(), nil, now, "info", "doing something", json.RawMessage(`{}`), 0,
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
		Level:     "info",
		Message:   "started execution",
		Metadata:  json.RawMessage(`{"step": 1}`),
	}

	mock.ExpectQuery("INSERT INTO session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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

func TestSessionLogStore_Create_PublishesToRedis(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	mr := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "Redis client should initialize")

	store := NewSessionLogStore(mock)
	store.SetLogger(zerolog.Nop())
	store.SetStreams(cache.NewSessionStreams(client, zerolog.Nop(), nil))

	now := time.Now()
	log := &models.SessionLog{
		SessionID: uuid.New(),
		OrgID:     uuid.New(),
		Level:     "info",
		Message:   "started execution",
		Metadata:  json.RawMessage(`{"step": 1}`),
	}

	mock.ExpectQuery("INSERT INTO session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "timestamp"}).
				AddRow(int64(7), now),
		)

	require.NoError(t, store.Create(context.Background(), log), "Create should publish the inserted log to Redis when streams are configured")
	require.True(t, mr.Exists("143:stream:{ses:"+log.SessionID.String()+"}:logs"), "published log should create a Redis stream")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionLogStore_Create_PublishFailureIsBestEffort(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	mr := miniredis.RunT(t)
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), nil)
	require.NotNil(t, client, "Redis client should initialize")
	mr.Close()

	store := NewSessionLogStore(mock)
	store.SetLogger(zerolog.Nop())
	store.SetStreams(cache.NewSessionStreams(client, zerolog.Nop(), nil))

	now := time.Now()
	log := &models.SessionLog{
		SessionID: uuid.New(),
		OrgID:     uuid.New(),
		Level:     "info",
		Message:   "started execution",
	}

	mock.ExpectQuery("INSERT INTO session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "timestamp"}).AddRow(int64(8), now))

	require.NoError(t, store.Create(context.Background(), log), "Create should succeed even when the best-effort Redis publish fails")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionLogStore_Create_ScanError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionLogStore(mock)
	log := &models.SessionLog{
		SessionID: uuid.New(),
		OrgID:     uuid.New(),
		Level:     "info",
		Message:   "started execution",
	}

	mock.ExpectQuery("INSERT INTO session_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id"}).
				AddRow(int64(1)),
		)

	err = store.Create(context.Background(), log)
	require.Error(t, err, "Create should surface row scan failures")
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

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
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
	require.Equal(t, models.SessionLogLevelInfo, logs[0].Level, "first log entry should have the correct level")
	require.Equal(t, "doing something", logs[0].Message, "first log entry should have the correct message")
	require.Equal(t, int64(2), logs[1].ID, "second log entry should have the correct ID")
	require.Equal(t, sessionID, logs[1].SessionID, "second log entry should have the correct session ID")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionLogStore_GetByID_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionLogStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.id .+ sl.session_id .+ sl.org_id").
		WithArgs(int64(42), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(logColumns).
				AddRow(int64(42), sessionID, orgID, nil, now, "output", "full output", json.RawMessage(`{"type":"tool_result"}`), 3),
		)

	log, err := store.GetByID(context.Background(), orgID, sessionID, 42)
	require.NoError(t, err, "GetByID should return the scoped log")
	require.Equal(t, int64(42), log.ID, "GetByID should return the requested log")
	require.Equal(t, sessionID, log.SessionID, "GetByID should preserve session ID")
	require.Equal(t, orgID, log.OrgID, "GetByID should preserve org ID")
	require.Equal(t, "full output", log.Message, "GetByID should return the full message")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionLogStore_ListByRunID_Empty(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionLogStore(mock)

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id").
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

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.session_id .+ sl.id >").
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
	require.Equal(t, models.SessionLogLevelInfo, logs[0].Level, "returned log entry should have the correct level")
	require.Equal(t, "doing something", logs[0].Message, "returned log entry should have the correct message")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionLogStore_MarkAssistantTranscriptDuplicate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID)
		expectErr string
	}{
		{
			name: "success",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				mock.ExpectExec("UPDATE session_logs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), (*uuid.UUID)(nil), 3, "Final answer").
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
		},
		{
			name: "database error",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID) {
				mock.ExpectExec("UPDATE session_logs").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), (*uuid.UUID)(nil), 3, "Final answer").
					WillReturnError(context.DeadlineExceeded)
			},
			expectErr: "mark assistant transcript duplicate",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool without error")
			defer mock.Close()

			store := NewSessionLogStore(mock)
			orgID := uuid.New()
			sessionID := uuid.New()
			tt.setupMock(mock, orgID, sessionID)

			err = store.MarkAssistantTranscriptDuplicate(context.Background(), orgID, sessionID, nil, 3, "Final answer")
			if tt.expectErr != "" {
				require.Error(t, err, "MarkAssistantTranscriptDuplicate should return an error")
				require.Contains(t, err.Error(), tt.expectErr, "MarkAssistantTranscriptDuplicate should wrap the store error")
			} else {
				require.NoError(t, err, "MarkAssistantTranscriptDuplicate should not return an error")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

// Pins the NULLIF guard in MarkAssistantTranscriptDuplicate. Pre-fix
// streamLogs persisted nil metadata as the JSONB value `null` (json.Marshal
// of a nil map returns "null") instead of SQL NULL; on those legacy rows
// `metadata || '{...}'::jsonb` resolves to `null` and would drop the
// duplicate marker. The NULLIF coalesces both shapes into '{}' before the
// merge. Removing the clause should fail this test.
func TestSessionLogStore_MarkAssistantTranscriptDuplicate_NormalizesLegacyJSONBNullMetadata(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewSessionLogStore(mock)
	mock.ExpectExec(`COALESCE\(NULLIF\(metadata, 'null'::jsonb\), '\{\}'::jsonb\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), (*uuid.UUID)(nil), 3, "Final answer").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.MarkAssistantTranscriptDuplicate(context.Background(), uuid.New(), uuid.New(), nil, 3, "Final answer")
	require.NoError(t, err, "MarkAssistantTranscriptDuplicate should succeed when the SQL normalizes legacy JSONB null metadata before the merge")
	require.NoError(t, mock.ExpectationsWereMet(), "MarkAssistantTranscriptDuplicate SQL must contain the NULLIF guard against legacy JSONB null metadata")
}

func TestSessionLogStore_ListByThread(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionLogStore(mock)
	orgID := uuid.New()
	threadID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.thread_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(logColumns).
				AddRow(newLogRow(1, sessionID, now)...),
		)

	logs, err := store.ListByThread(context.Background(), orgID, threadID)
	require.NoError(t, err, "ListByThread should not return an error")
	require.Len(t, logs, 1, "should return the log entry")
	require.Equal(t, int64(1), logs[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionLogStore_ListByThreadTurns(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewSessionLogStore(mock)
	orgID := uuid.New()
	threadID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_logs sl WHERE sl.thread_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(logColumns).
				AddRow(newLogRow(1, sessionID, now)...),
		)

	logs, err := store.ListByThreadTurns(context.Background(), orgID, threadID, []int{7, 5, 7})
	require.NoError(t, err, "ListByThreadTurns should not return an error")
	require.Len(t, logs, 1, "ListByThreadTurns should return matching log entries")
	require.Equal(t, int64(1), logs[0].ID, "ListByThreadTurns should scan the returned log")
	require.NoError(t, mock.ExpectationsWereMet(), "ListByThreadTurns should apply the expected SQL filter")
}

func TestSessionLogStore_ListByThreadLatestTurns(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewSessionLogStore(mock)
	orgID := uuid.New()
	threadID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery(`SELECT .+ FROM session_logs sl WHERE sl.thread_id = @thread_id AND sl.org_id = @org_id AND sl.turn_number > \( SELECT COALESCE\(MAX\(inner_sl.turn_number\), 0\) - @latest_turns`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(logColumns).
				AddRow(newLogRow(1, sessionID, now)...),
		)

	logs, err := store.ListByThreadLatestTurns(context.Background(), orgID, threadID, 50)
	require.NoError(t, err, "ListByThreadLatestTurns should not return an error")
	require.Len(t, logs, 1, "ListByThreadLatestTurns should return matching log entries")
	require.Equal(t, int64(1), logs[0].ID, "ListByThreadLatestTurns should scan the returned log")
	require.NoError(t, mock.ExpectationsWereMet(), "ListByThreadLatestTurns should anchor on the thread's max turn")
}

func TestSessionLogStore_ListByThreadLatestTurnsNonPositive(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	store := NewSessionLogStore(mock)

	logs, err := store.ListByThreadLatestTurns(context.Background(), uuid.New(), uuid.New(), 0)
	require.NoError(t, err, "non-positive window should be a no-op, not an error")
	require.Empty(t, logs, "non-positive window should return no logs without querying")
	require.NoError(t, mock.ExpectationsWereMet(), "non-positive window must not hit the database")
}

func TestSessionLogStore_DeleteExpired(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionLogStore(mock)

	mock.ExpectQuery("SELECT delete_expired_session_logs").
		WithArgs(30).
		WillReturnRows(pgxmock.NewRows([]string{"delete_expired_session_logs"}).AddRow(int64(100)))

	deleted, err := store.DeleteExpired(context.Background(), 30)
	require.NoError(t, err, "DeleteExpired should not return an error")
	require.Equal(t, int64(100), deleted, "should return the number of deleted rows")
	require.NoError(t, mock.ExpectationsWereMet())
}
