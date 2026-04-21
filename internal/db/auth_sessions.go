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
		INSERT INTO auth_sessions (user_id, org_id, last_org_id, token, expires_at)
		VALUES (@user_id, @org_id, @last_org_id, @token, @expires_at)
		RETURNING id, created_at`

	// last_org_id is left NULL unless the caller explicitly supplies it.
	// During the multi-org compat window session.OrgID tracks the user's
	// legacy primary org, which may already point at an org the user has
	// since been removed from. Prepopulating last_org_id from it would
	// mean fresh logins fire the revoked-org header on their very first
	// request. Instead we rely on the auth middleware's normal resolution
	// (header → hint → OldestForUser) and persist the result back once on
	// first use, so the hint always points at a live membership.
	args := pgx.NamedArgs{
		"user_id": session.UserID,
		// lint:allow-auth-session-orgid reason="Create writes org_id on INSERT during the sunset window; removed when the column is dropped"
		"org_id":      session.OrgID,
		"last_org_id": session.LastOrgID,
		"token":       session.Token,
		"expires_at":  session.ExpiresAt,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&session.ID, &session.CreatedAt)
}

// GetByToken returns the active session matching the opaque token, if any.
// lint:allow-no-orgid reason="pre-auth session lookup; token is opaque and identifies the org"
func (s *AuthSessionStore) GetByToken(ctx context.Context, token string) (models.AuthSession, error) {
	query := `
		SELECT id, user_id, org_id, last_org_id, token, expires_at, created_at
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

// UpdateLastOrgID persists the server-side hint for which membership should be
// activated when a session is next used without an X-Active-Org-ID header.
// Passing nil clears the hint (e.g. user's last org was deleted).
// lint:allow-no-orgid reason="identifies session by opaque token; org scoping enforced upstream"
func (s *AuthSessionStore) UpdateLastOrgID(ctx context.Context, token string, lastOrgID *uuid.UUID) error {
	query := `UPDATE auth_sessions SET last_org_id = @last_org_id WHERE token = @token`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"token":       token,
		"last_org_id": lastOrgID,
	})
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
