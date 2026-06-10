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

const sessionMessageSelectColumns = `id, session_id, org_id, thread_id, user_id, turn_number, role, content, attachments, "references", commands, token_usage, source, created_at`

const DefaultSessionMessageWindowLimit = 60
const MaxSessionMessageWindowLimit = 200

type SessionMessageWindowPosition string

const (
	SessionMessageWindowPositionLatest SessionMessageWindowPosition = "latest"
	SessionMessageWindowPositionOlder  SessionMessageWindowPosition = "older"
	SessionMessageWindowPositionNewer  SessionMessageWindowPosition = "newer"
	SessionMessageWindowPositionAround SessionMessageWindowPosition = "around"
)

type SessionMessageWindowOptions struct {
	Position        SessionMessageWindowPosition
	BeforeID        int64
	AfterID         int64
	AnchorMessageID int64
	Limit           int
}

type SessionMessageWindow struct {
	Messages                 []models.SessionMessage
	NextOlderCursor          string
	HasOlder                 bool
	NextNewerCursor          string
	HasNewer                 bool
	AnchorMessageID          int64
	AnchorFound              bool
	LatestAssistantMessageID int64
	LiveEdgeMessageID        int64
	Position                 SessionMessageWindowPosition
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

func (s *SessionMessageStore) CreateWithSource(ctx context.Context, msg *models.SessionMessage) error {
	query := `
		INSERT INTO session_messages (session_id, org_id, thread_id, user_id, turn_number, role, content, attachments, "references", commands, token_usage, source)
		VALUES (@session_id, @org_id, @thread_id, @user_id, @turn_number, @role, @content, @attachments, @references_data, @commands, @token_usage, @source)
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
		"source":          msg.Source,
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
	return pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.SessionMessage])
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
	msg, err := pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.SessionMessage])
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
	return pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.SessionMessage])
}

func (s *SessionMessageStore) ListWindowByThread(ctx context.Context, orgID, threadID uuid.UUID, opts SessionMessageWindowOptions) (SessionMessageWindow, error) {
	position := normalizeSessionMessageWindowPosition(opts)
	switch position {
	case SessionMessageWindowPositionNewer:
		return s.listNewerWindowByThread(ctx, orgID, threadID, opts)
	case SessionMessageWindowPositionAround:
		return s.listAroundWindowByThread(ctx, orgID, threadID, opts)
	default:
		return s.listLatestOrOlderWindowByThread(ctx, orgID, threadID, opts, position)
	}
}

func (s *SessionMessageStore) listLatestOrOlderWindowByThread(ctx context.Context, orgID, threadID uuid.UUID, opts SessionMessageWindowOptions, position SessionMessageWindowPosition) (SessionMessageWindow, error) {
	limit := normalizeSessionMessageWindowLimit(opts.Limit)
	query := `
		SELECT ` + sessionMessageSelectColumns + `
		FROM session_messages
		WHERE org_id = @org_id AND thread_id = @thread_id
		  AND (@before_id::bigint = 0 OR id < @before_id)
			ORDER BY created_at DESC, id DESC
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
	descMessages, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.SessionMessage])
	if err != nil {
		return SessionMessageWindow{}, fmt.Errorf("collect thread message window: %w", err)
	}

	hasOlder := len(descMessages) > limit
	if hasOlder {
		descMessages = descMessages[:limit]
	}

	window := SessionMessageWindow{HasOlder: hasOlder, Position: position}
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

func (s *SessionMessageStore) listNewerWindowByThread(ctx context.Context, orgID, threadID uuid.UUID, opts SessionMessageWindowOptions) (SessionMessageWindow, error) {
	limit := normalizeSessionMessageWindowLimit(opts.Limit)
	query := `
		SELECT ` + sessionMessageSelectColumns + `
		FROM session_messages
		WHERE org_id = @org_id AND thread_id = @thread_id
		  AND id > @after_id
		ORDER BY created_at ASC, id ASC
		LIMIT @limit`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":    orgID,
		"thread_id": threadID,
		"after_id":  opts.AfterID,
		"limit":     limit + 1,
	})
	if err != nil {
		return SessionMessageWindow{}, fmt.Errorf("query newer thread message window: %w", err)
	}
	messages, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.SessionMessage])
	if err != nil {
		return SessionMessageWindow{}, fmt.Errorf("collect newer thread message window: %w", err)
	}

	hasNewer := len(messages) > limit
	if hasNewer {
		messages = messages[:limit]
	}

	window := SessionMessageWindow{
		Messages: messages,
		HasNewer: hasNewer,
		Position: SessionMessageWindowPositionNewer,
	}
	if hasNewer && len(messages) > 0 {
		window.NextNewerCursor = fmt.Sprintf("%d", messages[len(messages)-1].ID)
	}
	liveEdgeMessageID, latestAssistantMessageID, err := s.getThreadMessageAnchorMetadata(ctx, orgID, threadID)
	if err != nil {
		return SessionMessageWindow{}, err
	}
	window.LiveEdgeMessageID = liveEdgeMessageID
	window.LatestAssistantMessageID = latestAssistantMessageID
	return window, nil
}

func (s *SessionMessageStore) listAroundWindowByThread(ctx context.Context, orgID, threadID uuid.UUID, opts SessionMessageWindowOptions) (SessionMessageWindow, error) {
	limit := normalizeSessionMessageWindowLimit(opts.Limit)
	if opts.AnchorMessageID <= 0 {
		window, err := s.listLatestOrOlderWindowByThread(ctx, orgID, threadID, opts, SessionMessageWindowPositionLatest)
		window.AnchorMessageID = opts.AnchorMessageID
		window.AnchorFound = false
		return window, err
	}
	olderLimit := limit / 2
	newerLimit := limit - olderLimit - 1
	if newerLimit < 0 {
		newerLimit = 0
	}

	query := `
		WITH anchor_message AS (
			SELECT id, created_at
			FROM session_messages
			WHERE org_id = @org_id AND thread_id = @thread_id AND id = @anchor_id
		),
		older_messages AS (
			SELECT ` + prefixedSessionMessageSelectColumns("m") + `, 'older' AS window_side
			FROM session_messages m
			JOIN anchor_message a ON true
			WHERE m.org_id = @org_id AND m.thread_id = @thread_id
			  AND (m.created_at, m.id) < (a.created_at, a.id)
			ORDER BY m.created_at DESC, m.id DESC
			LIMIT @older_limit
		),
		anchor_row AS (
			SELECT ` + prefixedSessionMessageSelectColumns("m") + `, 'anchor' AS window_side
			FROM session_messages m
			JOIN anchor_message a ON m.id = a.id
			WHERE m.org_id = @org_id AND m.thread_id = @thread_id
		),
		newer_messages AS (
			SELECT ` + prefixedSessionMessageSelectColumns("m") + `, 'newer' AS window_side
			FROM session_messages m
			JOIN anchor_message a ON true
			WHERE m.org_id = @org_id AND m.thread_id = @thread_id
			  AND (m.created_at, m.id) > (a.created_at, a.id)
			ORDER BY m.created_at ASC, m.id ASC
			LIMIT @newer_limit
		)
		SELECT *
		FROM (
			SELECT * FROM older_messages
			UNION ALL
			SELECT * FROM anchor_row
			UNION ALL
			SELECT * FROM newer_messages
		) window_messages
		ORDER BY created_at ASC, id ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":      orgID,
		"thread_id":   threadID,
		"anchor_id":   opts.AnchorMessageID,
		"older_limit": olderLimit + 1,
		"newer_limit": newerLimit + 1,
	})
	if err != nil {
		return SessionMessageWindow{}, fmt.Errorf("query around thread message window: %w", err)
	}
	defer rows.Close()

	type sideMessage struct {
		message models.SessionMessage
		side    string
	}
	var sideMessages []sideMessage
	for rows.Next() {
		var msg models.SessionMessage
		var side string
		if err := rows.Scan(
			&msg.ID,
			&msg.SessionID,
			&msg.OrgID,
			&msg.ThreadID,
			&msg.UserID,
			&msg.TurnNumber,
			&msg.Role,
			&msg.Content,
			&msg.Attachments,
			&msg.References,
			&msg.Commands,
			&msg.TokenUsage,
			&msg.Source,
			&msg.CreatedAt,
			&side,
		); err != nil {
			return SessionMessageWindow{}, fmt.Errorf("scan around thread message window: %w", err)
		}
		sideMessages = append(sideMessages, sideMessage{message: msg, side: side})
	}
	if err := rows.Err(); err != nil {
		return SessionMessageWindow{}, fmt.Errorf("iterate around thread message window: %w", err)
	}

	if len(sideMessages) == 0 {
		window, err := s.listLatestOrOlderWindowByThread(ctx, orgID, threadID, SessionMessageWindowOptions{Limit: opts.Limit}, SessionMessageWindowPositionLatest)
		window.AnchorMessageID = opts.AnchorMessageID
		window.AnchorFound = false
		return window, err
	}

	olderCount := 0
	newerCount := 0
	for _, item := range sideMessages {
		switch item.side {
		case "older":
			olderCount++
		case "newer":
			newerCount++
		}
	}
	hasOlder := olderCount > olderLimit
	hasNewer := newerCount > newerLimit

	messages := make([]models.SessionMessage, 0, len(sideMessages))
	skippedOldestExtra := false
	for i, item := range sideMessages {
		if item.side == "older" && hasOlder && !skippedOldestExtra {
			skippedOldestExtra = true
			continue
		}
		if item.side == "newer" && hasNewer {
			remainingNewer := 0
			for _, rest := range sideMessages[i:] {
				if rest.side == "newer" {
					remainingNewer++
				}
			}
			if remainingNewer == 1 {
				continue
			}
		}
		messages = append(messages, item.message)
	}

	window := SessionMessageWindow{
		Messages:        messages,
		HasOlder:        hasOlder,
		HasNewer:        hasNewer,
		AnchorMessageID: opts.AnchorMessageID,
		AnchorFound:     true,
		Position:        SessionMessageWindowPositionAround,
	}
	if hasOlder && len(messages) > 0 {
		window.NextOlderCursor = fmt.Sprintf("%d", messages[0].ID)
	}
	if hasNewer && len(messages) > 0 {
		window.NextNewerCursor = fmt.Sprintf("%d", messages[len(messages)-1].ID)
	}
	liveEdgeMessageID, latestAssistantMessageID, err := s.getThreadMessageAnchorMetadata(ctx, orgID, threadID)
	if err != nil {
		return SessionMessageWindow{}, err
	}
	window.LiveEdgeMessageID = liveEdgeMessageID
	window.LatestAssistantMessageID = latestAssistantMessageID
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

func normalizeSessionMessageWindowPosition(opts SessionMessageWindowOptions) SessionMessageWindowPosition {
	switch opts.Position {
	case SessionMessageWindowPositionAround:
		return SessionMessageWindowPositionAround
	case SessionMessageWindowPositionNewer:
		return SessionMessageWindowPositionNewer
	case SessionMessageWindowPositionOlder:
		return SessionMessageWindowPositionOlder
	case SessionMessageWindowPositionLatest:
		return SessionMessageWindowPositionLatest
	}
	if opts.AnchorMessageID > 0 {
		return SessionMessageWindowPositionAround
	}
	if opts.AfterID > 0 {
		return SessionMessageWindowPositionNewer
	}
	if opts.BeforeID > 0 {
		return SessionMessageWindowPositionOlder
	}
	return SessionMessageWindowPositionLatest
}

func prefixedSessionMessageSelectColumns(prefix string) string {
	return prefix + `.id, ` +
		prefix + `.session_id, ` +
		prefix + `.org_id, ` +
		prefix + `.thread_id, ` +
		prefix + `.user_id, ` +
		prefix + `.turn_number, ` +
		prefix + `.role, ` +
		prefix + `.content, ` +
		prefix + `.attachments, ` +
		prefix + `."references", ` +
		prefix + `.commands, ` +
		prefix + `.token_usage, ` +
		prefix + `.source, ` +
		prefix + `.created_at`
}
