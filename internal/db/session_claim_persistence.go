package db

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ClaimIdleInTx composes the legacy idle claim and returned-row policy hydration
// in the caller's transaction, without beginning, settling, configuring, retrying
// or publishing. It adds no lifecycle fence or execution claim. The owner must
// perform fresh mode/scope checks and discard the result on rollback or uncertain
// commit. The legacy claim itself has no post-commit notifications.
func (s *SessionStore) ClaimIdleInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, sessionID uuid.UUID) (models.Session, error) {
	if nilLifecycleDependency(tx) {
		return models.Session{}, fmt.Errorf("persist idle session claim: transaction is required")
	}
	return s.claimIdleRow(ctx, tx, orgID, sessionID)
}

// ClaimForResumeInTx composes the unchanged legacy resumable-status claim and
// runtime-field reset in the caller's transaction. It neither owns the unit nor
// publishes, and is not a lifecycle fence, availability proof or logical claim.
// The caller must discard its returned row on failure, rollback or uncertain
// commit and enforce the actual lifecycle policy before composing this write.
func (s *SessionStore) ClaimForResumeInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, sessionID uuid.UUID) (models.Session, error) {
	if nilLifecycleDependency(tx) {
		return models.Session{}, fmt.Errorf("persist resumed session claim: transaction is required")
	}
	return s.claimForResumeRow(ctx, tx, orgID, sessionID)
}
