package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var sessionColumns = []string{
	"id", "user_id", "org_id", "token", "expires_at", "created_at",
}

func TestSessionStore_Create(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
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

	mock.ExpectQuery("INSERT INTO sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), session)
	require.NoError(t, err, "Create should not return an error")
	require.Equal(t, generatedID, session.ID, "should set the generated ID on the session")
	require.Equal(t, now, session.CreatedAt, "should set the created_at timestamp on the session")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_GetByToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, sessionID, userID, orgID uuid.UUID, now time.Time, expiresAt time.Time)
		expectErr bool
	}{
		{
			name: "returns session when token is valid",
			setupMock: func(mock pgxmock.PgxPoolIface, sessionID, userID, orgID uuid.UUID, now time.Time, expiresAt time.Time) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE token").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(sessionColumns).
							AddRow(sessionID, userID, orgID, "session-token-abc123", expiresAt, now),
					)
			},
		},
		{
			name: "returns error when token is not found",
			setupMock: func(mock pgxmock.PgxPoolIface, sessionID, userID, orgID uuid.UUID, now time.Time, expiresAt time.Time) {
				mock.ExpectQuery("SELECT .+ FROM sessions WHERE token").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(sessionColumns))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionStore(mock)
			sessionID := uuid.New()
			userID := uuid.New()
			orgID := uuid.New()
			now := time.Now()
			expiresAt := now.Add(24 * time.Hour)
			tt.setupMock(mock, sessionID, userID, orgID, now, expiresAt)

			session, err := store.GetByToken(context.Background(), "session-token-abc123")
			if tt.expectErr {
				require.Error(t, err, "GetByToken should return an error when token is not found")
				return
			}
			require.NoError(t, err, "GetByToken should not return an error")
			require.Equal(t, sessionID, session.ID, "should return the correct session ID")
			require.Equal(t, userID, session.UserID, "should return the correct user ID")
			require.Equal(t, orgID, session.OrgID, "should return the correct org ID")
			require.Equal(t, "session-token-abc123", session.Token, "should return the correct session token")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionStore_DeleteByToken(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)

	mock.ExpectExec("DELETE FROM sessions WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.DeleteByToken(context.Background(), "session-token-abc123")
	require.NoError(t, err, "DeleteByToken should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionStore_DeleteByUserID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionStore(mock)
	userID := uuid.New()

	mock.ExpectExec("DELETE FROM sessions WHERE user_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 3))

	err = store.DeleteByUserID(context.Background(), userID)
	require.NoError(t, err, "DeleteByUserID should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
