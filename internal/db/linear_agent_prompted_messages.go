package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LinearAgentPromptedMessageStore records prompted Linear comments that have
// already been appended to a 143 session.
type LinearAgentPromptedMessageStore struct {
	db DBTX
}

func NewLinearAgentPromptedMessageStore(db DBTX) *LinearAgentPromptedMessageStore {
	return &LinearAgentPromptedMessageStore{db: db}
}

func (s *LinearAgentPromptedMessageStore) Reserve(ctx context.Context, orgID, agentSessionRowID, sessionID uuid.UUID, linearCommentID string) (bool, error) {
	if linearCommentID == "" {
		return true, nil
	}
	var inserted bool
	err := s.db.QueryRow(ctx, `
		INSERT INTO linear_agent_prompted_messages
			(org_id, agent_session_row_id, session_id, linear_comment_id)
		VALUES (@org_id, @agent_session_row_id, @session_id, @linear_comment_id)
		ON CONFLICT (agent_session_row_id, linear_comment_id) DO NOTHING
		RETURNING true AS inserted`,
		pgx.NamedArgs{
			"org_id":               orgID,
			"agent_session_row_id": agentSessionRowID,
			"session_id":           sessionID,
			"linear_comment_id":    linearCommentID,
		}).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reserve linear agent prompted message: %w", err)
	}
	return inserted, nil
}

func (s *LinearAgentPromptedMessageStore) Exists(ctx context.Context, orgID, agentSessionRowID uuid.UUID, linearCommentID string) (bool, error) {
	if linearCommentID == "" {
		return false, nil
	}
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM linear_agent_prompted_messages
			WHERE org_id = @org_id
			  AND agent_session_row_id = @agent_session_row_id
			  AND linear_comment_id = @linear_comment_id
		)`,
		pgx.NamedArgs{
			"org_id":               orgID,
			"agent_session_row_id": agentSessionRowID,
			"linear_comment_id":    linearCommentID,
		}).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check linear agent prompted message: %w", err)
	}
	return exists, nil
}
