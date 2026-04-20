package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type AuthSessionStore struct {
	db DBTX
}

func NewAuthSessionStore(db DBTX) *AuthSessionStore {
	return &AuthSessionStore{db: db}
}

func (s *AuthSessionStore) Create(ctx context.Context, session *models.AuthSession) error {
	query := `
		INSERT INTO auth_sessions (user_id, org_id, token, expires_at)
		VALUES (@user_id, @org_id, @token, @expires_at)
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"user_id":    session.UserID,
		"org_id":     session.OrgID,
		"token":      session.Token,
		"expires_at": session.ExpiresAt,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&session.ID, &session.CreatedAt)
}

// GetByToken returns the active session matching the opaque token, if any.
// lint:allow-no-orgid reason="pre-auth session lookup; token is opaque and identifies the org"
func (s *AuthSessionStore) GetByToken(ctx context.Context, token string) (models.AuthSession, error) {
	query := `
		SELECT id, user_id, org_id, token, expires_at, created_at
		FROM auth_sessions
		WHERE token = @token AND expires_at > now()`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"token": token})
	if err != nil {
		return models.AuthSession{}, fmt.Errorf("query session: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.AuthSession])
}

// Touch extends the session's expires_at to the given time. Used by the
// Auth middleware to implement sliding-window session refresh so active
// users aren't forcibly logged out at the 30-day boundary.
// lint:allow-no-orgid reason="touched by opaque token during request auth"
func (s *AuthSessionStore) Touch(ctx context.Context, token string, expiresAt time.Time) error {
	query := `UPDATE auth_sessions SET expires_at = @expires_at WHERE token = @token`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{"token": token, "expires_at": expiresAt})
	return err
}

// DeleteByToken removes the session for the given opaque token (logout).
// lint:allow-no-orgid reason="logout by opaque token"
func (s *AuthSessionStore) DeleteByToken(ctx context.Context, token string) error {
	query := `DELETE FROM auth_sessions WHERE token = @token`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{"token": token})
	return err
}

// DeleteByUserID removes every session belonging to the given user.
// lint:allow-no-orgid reason="user_id is globally unique; sessions are cascade-deleted when a user is removed"
func (s *AuthSessionStore) DeleteByUserID(ctx context.Context, userID uuid.UUID) error {
	query := `DELETE FROM auth_sessions WHERE user_id = @user_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{"user_id": userID})
	return err
}
