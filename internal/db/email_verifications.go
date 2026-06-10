package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

// EmailVerificationStore manages single-use email-verification tokens for
// password-signup users (OAuth users are attested by their provider and
// never need one).
type EmailVerificationStore struct {
	db DBTX
}

func NewEmailVerificationStore(db DBTX) *EmailVerificationStore {
	return &EmailVerificationStore{db: db}
}

// Create replaces any outstanding tokens for the user with a fresh one —
// a resend invalidates earlier links so at most one live link exists per
// account, and a stale inbox entry can't race a newer one.
//
// lint:allow-no-orgid reason="user-scoped pre-membership identity table; no org_id column"
func (s *EmailVerificationStore) Create(ctx context.Context, t *models.EmailVerificationToken) error {
	if _, err := s.db.Exec(ctx,
		`DELETE FROM email_verification_tokens WHERE user_id = @user_id AND consumed_at IS NULL`,
		pgx.NamedArgs{"user_id": t.UserID}); err != nil {
		return fmt.Errorf("clear pending email verification tokens: %w", err)
	}
	row := s.db.QueryRow(ctx, `
		INSERT INTO email_verification_tokens (user_id, email, token, expires_at)
		VALUES (@user_id, @email, @token, @expires_at)
		RETURNING id, created_at`,
		pgx.NamedArgs{
			"user_id":    t.UserID,
			"email":      t.Email,
			"token":      t.Token,
			"expires_at": t.ExpiresAt,
		})
	return row.Scan(&t.ID, &t.CreatedAt)
}

// Consume atomically claims an unexpired, unconsumed token and returns its
// row. pgx.ErrNoRows means invalid, expired, already used, or superseded —
// the caller reports them identically so the endpoint isn't an oracle.
//
// lint:allow-no-orgid reason="token-based pre-auth claim; token is globally unique"
func (s *EmailVerificationStore) Consume(ctx context.Context, token string) (models.EmailVerificationToken, error) {
	query := `
		UPDATE email_verification_tokens
		SET consumed_at = now()
		WHERE token = @token AND consumed_at IS NULL AND expires_at > now()
		RETURNING id, user_id, email, token, expires_at, consumed_at, created_at`
	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"token": token})
	if err != nil {
		return models.EmailVerificationToken{}, fmt.Errorf("consume email verification token: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.EmailVerificationToken])
}

// EmailVerificationTokenTTL is how long a verification link stays valid.
// Long enough to survive a busy inbox, short enough that a forwarded or
// leaked link goes stale quickly; resend mints a fresh one in one click.
const EmailVerificationTokenTTL = 24 * time.Hour
