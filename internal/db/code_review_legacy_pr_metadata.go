package db

import (
	"context"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LegacyPRCodeReviewMetadata is the temporary terminal-metadata composition for
// the version-0 CodeReviewScheduleStore.WithLockedPR owner. It deliberately
// exposes only the terminal operations needed by that owner's service port.
// It grants no completion, cancellation, retirement or writer-version authority.
type LegacyPRCodeReviewMetadata struct {
	store *CodeReviewStore
}

// NewLegacyPRCodeReviewMetadataInTx retains the existing legacy PR transaction's
// caller context, locking, provider-under-lock freshness and settlement policy.
// Unlike NewCodeReviewStoreInTx, this named compatibility exception does not
// claim the canonical Run's whole-unit budget. Its terminal methods require
// actual READ COMMITTED, use only this supplied transaction, and never begin,
// settle, configure, acquire, retry or publish. Every error requires the outer
// PR owner to roll back its complete unit; results remain provisional until its
// known commit. A.5/G must replace this exception in the full scheduler conversion.
func NewLegacyPRCodeReviewMetadataInTx(tx pgx.Tx) (*LegacyPRCodeReviewMetadata, error) {
	store, err := newCodeReviewMetadataStoreInTx(tx)
	if err != nil {
		return nil, err
	}
	return &LegacyPRCodeReviewMetadata{store: store}, nil
}

func (s *LegacyPRCodeReviewMetadata) FailReview(ctx context.Context, orgID, sessionID uuid.UUID, reason string) (models.CodeReviewSessionMetadata, error) {
	if s == nil || s.store == nil {
		return models.CodeReviewSessionMetadata{}, ErrCodeReviewMetadataTerminalCapability
	}
	return s.store.FailReview(ctx, orgID, sessionID, reason)
}

func (s *LegacyPRCodeReviewMetadata) FailReviewWithStatus(ctx context.Context, orgID uuid.UUID, params FailCodeReviewParams) (models.CodeReviewSessionMetadata, error) {
	if s == nil || s.store == nil {
		return models.CodeReviewSessionMetadata{}, ErrCodeReviewMetadataTerminalCapability
	}
	return s.store.FailReviewWithStatus(ctx, orgID, params)
}

func (s *LegacyPRCodeReviewMetadata) MarkStaleForPullRequestExceptHead(ctx context.Context, orgID, pullRequestID uuid.UUID, currentHeadSHA string, supersededBySessionID *uuid.UUID) ([]models.CodeReviewSessionMetadata, error) {
	if s == nil || s.store == nil {
		return nil, ErrCodeReviewMetadataTerminalCapability
	}
	return s.store.MarkStaleForPullRequestExceptHead(ctx, orgID, pullRequestID, currentHeadSHA, supersededBySessionID)
}
