package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// MarkCancelRequestedForTurn leaves a later turn untouched when interrupt
// delivery resumes after the originally selected turn has already drained.
func (s *SessionThreadStore) MarkCancelRequestedForTurn(ctx context.Context, orgID, sessionID, threadID uuid.UUID, expectedTurn int) (bool, error) {
	if expectedTurn < 1 {
		return false, fmt.Errorf("expected cancellation turn must be positive")
	}
	tag, err := s.db.Exec(ctx, `UPDATE session_threads SET cancel_requested_at=COALESCE(cancel_requested_at,now())
 WHERE org_id=$1 AND session_id=$2 AND id=$3 AND current_turn=$4-1
 AND archived_at IS NULL AND status IN ('pending','running','awaiting_input')`, orgID, sessionID, threadID, expectedTurn)
	return tag.RowsAffected() > 0, err
}
