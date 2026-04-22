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
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "user_id", "turn_number", "role", "content", "attachments", "references", "token_usage", "created_at"}).
				AddRow(int64(1), sessionID, orgID, nil, nil, 1, "user", "Fix the bug", nil, []byte(`[{"kind":"file","token":"@internal/api/handlers/sessions.go","path":"internal/api/handlers/sessions.go","display":"internal/api/handlers/sessions.go"}]`), nil, now).
				AddRow(int64(2), sessionID, orgID, nil, nil, 1, "assistant", "Done", nil, nil, nil, now),
		)

	msgs, err := store.ListBySession(context.Background(), orgID, sessionID)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	require.Equal(t, "Fix the bug", msgs[0].Content)
	require.Len(t, msgs[0].References, 1)
	require.Equal(t, models.SessionInputReferenceKindFile, msgs[0].References[0].Kind)
	require.Equal(t, "internal/api/handlers/sessions.go", msgs[0].References[0].Path)
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
			pgxmock.NewRows([]string{"id", "session_id", "org_id", "thread_id", "user_id", "turn_number", "role", "content", "attachments", "references", "token_usage", "created_at"}).
				AddRow(int64(1), sessionID, orgID, &threadID, nil, 1, "user", "Hello thread", nil, nil, nil, now),
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
