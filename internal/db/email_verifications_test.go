package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

var emailVerificationColumns = []string{"id", "user_id", "email", "token", "expires_at", "consumed_at", "created_at"}

func TestEmailVerificationStore_Create_ReplacesPendingTokens(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewEmailVerificationStore(mock)
	userID := uuid.New()
	generatedID := uuid.New()
	now := time.Now()

	// Older pending links are invalidated before the new one is minted, so
	// at most one live link exists per account.
	mock.ExpectExec("DELETE FROM email_verification_tokens").
		WithArgs(userID).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))
	mock.ExpectQuery("INSERT INTO email_verification_tokens").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(generatedID, now))

	tok := &models.EmailVerificationToken{
		UserID:    userID,
		Email:     "bob@assembledhq.com",
		Token:     "tok",
		ExpiresAt: now.Add(EmailVerificationTokenTTL),
	}
	require.NoError(t, store.Create(context.Background(), tok))
	require.Equal(t, generatedID, tok.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEmailVerificationStore_Consume(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewEmailVerificationStore(mock)
	userID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("UPDATE email_verification_tokens").
		WithArgs("tok").
		WillReturnRows(pgxmock.NewRows(emailVerificationColumns).
			AddRow(uuid.New(), userID, "bob@assembledhq.com", "tok", now.Add(time.Hour), &now, now))

	got, err := store.Consume(context.Background(), "tok")
	require.NoError(t, err)
	require.Equal(t, userID, got.UserID)
	require.Equal(t, "bob@assembledhq.com", got.Email)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEmailVerificationStore_Consume_InvalidReturnsNoRows(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewEmailVerificationStore(mock)

	// Expired / consumed / unknown all match zero rows in the claim UPDATE.
	mock.ExpectQuery("UPDATE email_verification_tokens").
		WithArgs("stale").
		WillReturnRows(pgxmock.NewRows(emailVerificationColumns))

	_, err = store.Consume(context.Background(), "stale")
	require.ErrorIs(t, err, pgx.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}
