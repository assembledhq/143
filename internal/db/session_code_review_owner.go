package db

import (
	"context"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// LockAndRejectIfCodeReviewOwned holds the session row through a caller's
// publication transaction. Ownership claims update that same row, so a
// competing publish action cannot slip between HTTP preflight and enqueue.
func (s *SessionStore) LockAndRejectIfCodeReviewOwned(ctx context.Context, orgID, sessionID uuid.UUID) error {
	var prID pgtype.UUID
	if err := s.db.QueryRow(ctx, `SELECT code_review_owner_pr_id FROM sessions WHERE org_id=$1 AND id=$2 FOR UPDATE`, orgID, sessionID).Scan(&prID); err != nil {
		return err
	}
	if prID.Valid {
		return &models.SessionCodeReviewOwnedError{PullRequestID: uuid.UUID(prID.Bytes)}
	}
	return nil
}
