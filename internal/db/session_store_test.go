package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var sessionColumns = []string{
	"id", "user_id", "org_id", "token", "expires_at", "created_at",
}

func TestSessionStore_Create_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	session := &models.Session{
		UserID:    uuid.New(),
		OrgID:     uuid.New(),
		Token:     "session-token-abc123",
		ExpiresAt: now.Add(24 * time.Hour),
	}

	// 4 named args: user_id, org_id, token, expires_at
	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), session)
	require.NoError(t, err)
	assert.Equal(t, generatedID, session.ID)
	assert.Equal(t, now, session.CreatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_GetByToken_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	sessionID := uuid.New()
	userID := uuid.New()
	orgID := uuid.New()
	now := time.Now()
	expiresAt := now.Add(24 * time.Hour)

	// 1 named arg: token
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionColumns).
				AddRow(sessionID, userID, orgID, "session-token-abc123", expiresAt, now),
		)

	session, err := store.GetByToken(context.Background(), "session-token-abc123")
	require.NoError(t, err)
	assert.Equal(t, sessionID, session.ID)
	assert.Equal(t, userID, session.UserID)
	assert.Equal(t, orgID, session.OrgID)
	assert.Equal(t, "session-token-abc123", session.Token)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_GetByToken_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	// 1 named arg: token
	mock.ExpectQuery("SELECT .+ FROM sessions WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionColumns))

	_, err = store.GetByToken(context.Background(), "nonexistent-token")
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_DeleteByToken_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)

	// 1 named arg: token
	mock.ExpectExec("DELETE FROM sessions WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.DeleteByToken(context.Background(), "session-token-abc123")
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionStore_DeleteByUserID_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewSessionStore(mock)
	userID := uuid.New()

	// 1 named arg: user_id
	mock.ExpectExec("DELETE FROM sessions WHERE user_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 3))

	err = store.DeleteByUserID(context.Background(), userID)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
