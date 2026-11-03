package db

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RejectIfSendOwned observes both external owners in one statement. Callers
// performing a mutation must hold their session lock in the supplied store's
// transaction; this read itself grants no mutation authority.
func (s *SessionStore) RejectIfSendOwned(ctx context.Context, orgID, sessionID uuid.UUID) error {
	var reviewID, generationID, automationID, targetID *uuid.UUID
	var releasePending *bool
	err := s.db.QueryRow(ctx, `SELECT s.code_review_owner_pr_id,
		g.id, t.automation_id, g.target_id, g.ownership_release_pending
		FROM sessions s
		LEFT JOIN (automation_target_sessions g
			JOIN automation_targets t ON t.id=g.target_id AND t.org_id=g.org_id)
			ON g.id=s.automation_owner_generation_id AND g.org_id=s.org_id
		WHERE s.org_id=$1 AND s.id=$2`, pgx.QueryExecModeExec, orgID, sessionID).
		Scan(&reviewID, &generationID, &automationID, &targetID, &releasePending)
	if err != nil {
		return fmt.Errorf("read session send owner: %w", err)
	}
	if reviewID != nil {
		return &models.SessionCodeReviewOwnedError{PullRequestID: *reviewID}
	}
	if generationID != nil {
		return &models.SessionAutomationOwnedError{Owner: models.SessionAutomationOwner{
			GenerationID: *generationID, AutomationID: *automationID, TargetID: *targetID,
			ReleasePending: *releasePending, ResetURL: models.SessionAutomationResetURL(*automationID, *targetID),
		}}
	}
	return nil
}
