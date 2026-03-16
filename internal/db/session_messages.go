package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type SessionMessageStore struct {
	db DBTX
}

func NewSessionMessageStore(db DBTX) *SessionMessageStore {
	return &SessionMessageStore{db: db}
}

const sessionMessageSelectColumns = `id, session_id, org_id, user_id, turn_number, role, content, attachments, token_usage, created_at`

func (s *SessionMessageStore) Create(ctx context.Context, msg *models.SessionMessage) error {
	query := `
		INSERT INTO session_messages (session_id, org_id, user_id, turn_number, role, content, attachments, token_usage)
		VALUES (@session_id, @org_id, @user_id, @turn_number, @role, @content, @attachments, @token_usage)
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"session_id":  msg.SessionID,
		"org_id":      msg.OrgID,
		"user_id":     msg.UserID,
		"turn_number": msg.TurnNumber,
		"role":        msg.Role,
		"content":     msg.Content,
		"attachments": msg.Attachments,
		"token_usage": msg.TokenUsage,
	}

	row := s.db.QueryRow(ctx, query, args)
	return row.Scan(&msg.ID, &msg.CreatedAt)
}

func (s *SessionMessageStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionMessage, error) {
	query := `
		SELECT ` + sessionMessageSelectColumns + `
		FROM session_messages
		WHERE org_id = @org_id AND session_id = @session_id
		ORDER BY turn_number ASC, id ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("query session messages: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionMessage])
}
