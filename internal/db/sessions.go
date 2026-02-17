package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type SessionStore struct {
	db DBTX
}

func NewSessionStore(db DBTX) *SessionStore {
	return &SessionStore{db: db}
}

func (s *SessionStore) Create(ctx context.Context, session *models.Session) error {
	query := `
		INSERT INTO sessions (user_id, org_id, token, expires_at)
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

func (s *SessionStore) GetByToken(ctx context.Context, token string) (models.Session, error) {
	query := `
		SELECT id, user_id, org_id, token, expires_at, created_at
		FROM sessions
		WHERE token = @token AND expires_at > now()`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"token": token})
	if err != nil {
		return models.Session{}, fmt.Errorf("query session: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
}

func (s *SessionStore) DeleteByToken(ctx context.Context, token string) error {
	query := `DELETE FROM sessions WHERE token = @token`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{"token": token})
	return err
}

func (s *SessionStore) DeleteByUserID(ctx context.Context, userID uuid.UUID) error {
	query := `DELETE FROM sessions WHERE user_id = @user_id`
	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{"user_id": userID})
	return err
}
