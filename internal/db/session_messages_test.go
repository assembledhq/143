package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestSessionMessageStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionMessageStore(mock)
	now := time.Now()

	mock.ExpectQuery("INSERT INTO session_messages").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(int64(1), now),
		)

	msg := &models.SessionMessage{
		SessionID:  uuid.New(),
		OrgID:      uuid.New(),
		TurnNumber: 1,
		Role:       models.MessageRoleUser,
		Content:    "Hello",
		References: []models.SessionInputReference{
			{
				Kind:    models.SessionInputReferenceKindFile,
				Token:   "@internal/api/handlers/sessions.go",
				Path:    "internal/api/handlers/sessions.go",
				Display: "internal/api/handlers/sessions.go",
			},
		},
		Commands: []models.SessionInputCommand{
			{
				Kind:      "command",
				AgentType: models.AgentTypeClaudeCode,
				Name:      "review",
				Token:     "/review",
				Display:   "/review",
			},
		},
	}

	err = store.Create(context.Background(), msg)
	require.NoError(t, err)
	require.Equal(t, int64(1), msg.ID)
	require.Equal(t, now, msg.CreatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionMessageStore_ListBySession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionMessageStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_messages WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "user_id", "turn_number", "role", "content", "attachments", "references", "commands", "token_usage", "created_at"}).
				AddRow(int64(1), sessionID, orgID, nil, nil, 1, "user", "Fix the bug", nil, []byte(`[{"kind":"file","token":"@internal/api/handlers/sessions.go","path":"internal/api/handlers/sessions.go","display":"internal/api/handlers/sessions.go"}]`), []byte(`[{"kind":"command","agent_type":"claude_code","name":"review","token":"/review","display":"/review"}]`), nil, now).
				AddRow(int64(2), sessionID, orgID, nil, nil, 1, "assistant", "Done", nil, nil, nil, nil, now),
		)

	msgs, err := store.ListBySession(context.Background(), orgID, sessionID)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	require.Equal(t, "Fix the bug", msgs[0].Content)
	require.Len(t, msgs[0].References, 1)
	require.Equal(t, models.SessionInputReferenceKindFile, msgs[0].References[0].Kind)
	require.Equal(t, "internal/api/handlers/sessions.go", msgs[0].References[0].Path)
	require.Len(t, msgs[0].Commands, 1)
	require.Equal(t, "review", msgs[0].Commands[0].Name)
	require.Equal(t, models.AgentTypeClaudeCode, msgs[0].Commands[0].AgentType)
	require.Equal(t, "Done", msgs[1].Content)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionMessageStore_ListBySession_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionMessageStore(mock)

	mock.ExpectQuery("SELECT .+ FROM session_messages WHERE org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("db connection lost"))

	_, err = store.ListBySession(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "query session messages")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionMessageStore_Delete(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionMessageStore(mock)

	mock.ExpectExec("DELETE FROM session_messages WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.Delete(context.Background(), int64(42))
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionMessageStore_ListByThread(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionMessageStore(mock)
	orgID := uuid.New()
	threadID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_messages WHERE org_id .+ thread_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "user_id", "turn_number", "role", "content", "attachments", "references", "commands", "token_usage", "created_at"}).
				AddRow(int64(1), sessionID, orgID, &threadID, nil, 1, "user", "Hello thread", nil, nil, nil, nil, now),
		)

	msgs, err := store.ListByThread(context.Background(), orgID, threadID)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, "Hello thread", msgs[0].Content)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionMessageStore_ListByThread_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionMessageStore(mock)

	mock.ExpectQuery("SELECT .+ FROM session_messages WHERE org_id .+ thread_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("connection reset"))

	_, err = store.ListByThread(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "query thread messages")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionMessageStore_ListWindowByThread_Latest(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	store := NewSessionMessageStore(mock)
	orgID := uuid.New()
	threadID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_messages").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "user_id", "turn_number", "role", "content", "attachments", "references", "commands", "token_usage", "created_at"}).
				AddRow(int64(4), sessionID, orgID, &threadID, nil, 2, "assistant", "latest", nil, nil, nil, nil, now).
				AddRow(int64(3), sessionID, orgID, &threadID, nil, 2, "user", "ask", nil, nil, nil, nil, now).
				AddRow(int64(2), sessionID, orgID, &threadID, nil, 1, "assistant", "older", nil, nil, nil, nil, now),
		)
	mock.ExpectQuery("SELECT .+ max\\(id\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"live_edge_message_id", "latest_assistant_message_id"}).
			AddRow(int64(4), int64(4)))

	window, err := store.ListWindowByThread(context.Background(), orgID, threadID, SessionMessageWindowOptions{Limit: 2})
	require.NoError(t, err, "latest window query should not fail")
	require.True(t, window.HasOlder, "limit plus one row should mark older history available")
	require.Equal(t, "3", window.NextOlderCursor, "next older cursor should point at the oldest loaded message")
	require.Equal(t, int64(4), window.LiveEdgeMessageID, "live edge should be the newest message in the thread")
	require.Equal(t, int64(4), window.LatestAssistantMessageID, "latest assistant anchor should use newest assistant message")
	require.Equal(t, []int64{3, 4}, []int64{window.Messages[0].ID, window.Messages[1].ID}, "messages should be returned chronologically")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionMessageStore_ListWindowByThread_Before(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "mock pool should be created")
	defer mock.Close()

	store := NewSessionMessageStore(mock)
	orgID := uuid.New()
	threadID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM session_messages").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "user_id", "turn_number", "role", "content", "attachments", "references", "commands", "token_usage", "created_at"}).
				AddRow(int64(2), sessionID, orgID, &threadID, nil, 1, "assistant", "older", nil, nil, nil, nil, now).
				AddRow(int64(1), sessionID, orgID, &threadID, nil, 1, "user", "oldest", nil, nil, nil, nil, now),
		)
	mock.ExpectQuery("SELECT .+ max\\(id\\)").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"live_edge_message_id", "latest_assistant_message_id"}).
			AddRow(int64(5), int64(4)))

	window, err := store.ListWindowByThread(context.Background(), orgID, threadID, SessionMessageWindowOptions{BeforeID: 3, Limit: 2})
	require.NoError(t, err, "older window query should not fail")
	require.False(t, window.HasOlder, "exact page without extra row should not mark older history available")
	require.Empty(t, window.NextOlderCursor, "next older cursor should be empty when no older page remains")
	require.Equal(t, int64(5), window.LiveEdgeMessageID, "older window should still report the thread live edge")
	require.Equal(t, int64(4), window.LatestAssistantMessageID, "older window should still report the thread latest assistant anchor")
	require.Equal(t, []int64{1, 2}, []int64{window.Messages[0].ID, window.Messages[1].ID}, "older window should be returned chronologically")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
