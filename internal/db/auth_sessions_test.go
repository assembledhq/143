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

func TestAuthSessionStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAuthSessionStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	session := &models.AuthSession{
		UserID:    uuid.New(),
		OrgID:     uuid.New(),
		Token:     "test-token",
		ExpiresAt: now.Add(24 * time.Hour),
	}

	err = store.Create(context.Background(), session)
	require.NoError(t, err)
	require.Equal(t, generatedID, session.ID)
	require.Equal(t, now, session.CreatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthSessionStore_GetByToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAuthSessionStore(mock)
	sessionID := uuid.New()
	userID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM auth_sessions WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "user_id", "org_id", "token", "expires_at", "created_at"}).
				AddRow(sessionID, userID, orgID, "test-token", now.Add(24*time.Hour), now),
		)

	session, err := store.GetByToken(context.Background(), "test-token")
	require.NoError(t, err)
	require.Equal(t, sessionID, session.ID)
	require.Equal(t, userID, session.UserID)
	require.Equal(t, "test-token", session.Token)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthSessionStore_GetByToken_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAuthSessionStore(mock)

	mock.ExpectQuery("SELECT .+ FROM auth_sessions WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("connection lost"))

	_, err = store.GetByToken(context.Background(), "bad-token")
	require.Error(t, err)
	require.Contains(t, err.Error(), "query session")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthSessionStore_DeleteByToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAuthSessionStore(mock)

	mock.ExpectExec("DELETE FROM auth_sessions WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.DeleteByToken(context.Background(), "test-token")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthSessionStore_DeleteByUserID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewAuthSessionStore(mock)

	mock.ExpectExec("DELETE FROM auth_sessions WHERE user_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 3))

	err = store.DeleteByUserID(context.Background(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
