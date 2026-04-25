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

const sessionMessageSelectColumns = `id, session_id, org_id, thread_id, user_id, turn_number, role, content, attachments, "references", commands, token_usage, created_at`

func (s *SessionMessageStore) Create(ctx context.Context, msg *models.SessionMessage) error {
	query := `
		INSERT INTO session_messages (session_id, org_id, thread_id, user_id, turn_number, role, content, attachments, "references", commands, token_usage)
		VALUES (@session_id, @org_id, @thread_id, @user_id, @turn_number, @role, @content, @attachments, @references_data, @commands, @token_usage)
		RETURNING id, created_at`

	args := pgx.NamedArgs{
		"session_id":      msg.SessionID,
		"org_id":          msg.OrgID,
		"thread_id":       msg.ThreadID,
		"user_id":         msg.UserID,
		"turn_number":     msg.TurnNumber,
		"role":            msg.Role,
		"content":         msg.Content,
		"attachments":     msg.Attachments,
		"references_data": msg.References,
		"commands":        msg.Commands,
		"token_usage":     msg.TokenUsage,
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

// Delete removes a session message by ID. Used to clean up orphaned messages
// when a follow-up operation (e.g. job enqueue) fails after message creation.
// lint:allow-no-orgid reason="message id is a globally unique bigint; used only to clean up a message the caller just created"
func (s *SessionMessageStore) Delete(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM session_messages WHERE id = @id`, pgx.NamedArgs{"id": id})
	return err
}

func (s *SessionMessageStore) ListByThread(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionMessage, error) {
	query := `
		SELECT ` + sessionMessageSelectColumns + `
		FROM session_messages
		WHERE org_id = @org_id AND thread_id = @thread_id
		ORDER BY turn_number ASC, id ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":    orgID,
		"thread_id": threadID,
	})
	if err != nil {
		return nil, fmt.Errorf("query thread messages: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionMessage])
}
