package db

import (
	"context"
	"database/sql"
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

const DefaultSessionMessageWindowLimit = 60
const MaxSessionMessageWindowLimit = 200

type SessionMessageWindowOptions struct {
	BeforeID int64
	Limit    int
}

type SessionMessageWindow struct {
	Messages                 []models.SessionMessage
	NextOlderCursor          string
	HasOlder                 bool
	LatestAssistantMessageID int64
	LiveEdgeMessageID        int64
}

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

func (s *SessionMessageStore) GetByID(ctx context.Context, orgID uuid.UUID, id int64) (models.SessionMessage, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+sessionMessageSelectColumns+`
		FROM session_messages
		WHERE org_id = @org_id AND id = @id`, pgx.NamedArgs{
		"org_id": orgID,
		"id":     id,
	})
	if err != nil {
		return models.SessionMessage{}, fmt.Errorf("query session message: %w", err)
	}
	msg, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionMessage])
	if err != nil {
		return models.SessionMessage{}, err
	}
	return msg, nil
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

func (s *SessionMessageStore) ListWindowByThread(ctx context.Context, orgID, threadID uuid.UUID, opts SessionMessageWindowOptions) (SessionMessageWindow, error) {
	limit := normalizeSessionMessageWindowLimit(opts.Limit)
	query := `
		SELECT ` + sessionMessageSelectColumns + `
		FROM session_messages
		WHERE org_id = @org_id AND thread_id = @thread_id
		  AND (@before_id::bigint = 0 OR id < @before_id)
		ORDER BY id DESC
		LIMIT @limit`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":    orgID,
		"thread_id": threadID,
		"before_id": opts.BeforeID,
		"limit":     limit + 1,
	})
	if err != nil {
		return SessionMessageWindow{}, fmt.Errorf("query thread message window: %w", err)
	}
	descMessages, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionMessage])
	if err != nil {
		return SessionMessageWindow{}, fmt.Errorf("collect thread message window: %w", err)
	}

	hasOlder := len(descMessages) > limit
	if hasOlder {
		descMessages = descMessages[:limit]
	}

	window := SessionMessageWindow{HasOlder: hasOlder}
	liveEdgeMessageID, latestAssistantMessageID, err := s.getThreadMessageAnchorMetadata(ctx, orgID, threadID)
	if err != nil {
		return SessionMessageWindow{}, err
	}
	window.LiveEdgeMessageID = liveEdgeMessageID
	window.LatestAssistantMessageID = latestAssistantMessageID

	messages := make([]models.SessionMessage, len(descMessages))
	for i := range descMessages {
		messages[len(descMessages)-1-i] = descMessages[i]
	}
	window.Messages = messages
	if hasOlder && len(messages) > 0 {
		window.NextOlderCursor = fmt.Sprintf("%d", messages[0].ID)
	}

	return window, nil
}

func (s *SessionMessageStore) getThreadMessageAnchorMetadata(ctx context.Context, orgID, threadID uuid.UUID) (int64, int64, error) {
	query := `
		SELECT
			max(id) AS live_edge_message_id,
			max(id) FILTER (WHERE role = 'assistant') AS latest_assistant_message_id
		FROM session_messages
		WHERE org_id = @org_id AND thread_id = @thread_id`

	var liveEdge sql.NullInt64
	var latestAssistant sql.NullInt64
	if err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":    orgID,
		"thread_id": threadID,
	}).Scan(&liveEdge, &latestAssistant); err != nil {
		return 0, 0, fmt.Errorf("query thread message anchor metadata: %w", err)
	}

	var liveEdgeID int64
	if liveEdge.Valid {
		liveEdgeID = liveEdge.Int64
	}
	var latestAssistantID int64
	if latestAssistant.Valid {
		latestAssistantID = latestAssistant.Int64
	}
	return liveEdgeID, latestAssistantID, nil
}

func normalizeSessionMessageWindowLimit(limit int) int {
	if limit <= 0 {
		return DefaultSessionMessageWindowLimit
	}
	if limit > MaxSessionMessageWindowLimit {
		return MaxSessionMessageWindowLimit
	}
	return limit
}
