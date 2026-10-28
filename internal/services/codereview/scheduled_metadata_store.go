package codereview

import (
	"context"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// scheduledMetadataStore retains the plain transaction-bound store for reads
// and row-only methods. Only these three terminal operations opt into the named
// temporary legacy PR composition; policy writes retain their existing owner.
type scheduledMetadataStore struct {
	MetadataStore
	tx pgx.Tx
}

var _ MetadataStore = (*scheduledMetadataStore)(nil)

func (s *scheduledMetadataStore) terminalMetadata() (*db.LegacyPRCodeReviewMetadata, error) {
	if s == nil {
		return nil, db.ErrCodeReviewMetadataTerminalCapability
	}
	return db.NewLegacyPRCodeReviewMetadataInTx(s.tx)
}

func (s *scheduledMetadataStore) FailReview(ctx context.Context, orgID, sessionID uuid.UUID, reason string) (models.CodeReviewSessionMetadata, error) {
	metadata, err := s.terminalMetadata()
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	return metadata.FailReview(ctx, orgID, sessionID, reason)
}

func (s *scheduledMetadataStore) FailReviewWithStatus(ctx context.Context, orgID uuid.UUID, params db.FailCodeReviewParams) (models.CodeReviewSessionMetadata, error) {
	metadata, err := s.terminalMetadata()
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	return metadata.FailReviewWithStatus(ctx, orgID, params)
}

func (s *scheduledMetadataStore) MarkStaleForPullRequestExceptHead(ctx context.Context, orgID, pullRequestID uuid.UUID, currentHeadSHA string, supersededBySessionID *uuid.UUID) ([]models.CodeReviewSessionMetadata, error) {
	metadata, err := s.terminalMetadata()
	if err != nil {
		return nil, err
	}
	return metadata.MarkStaleForPullRequestExceptHead(ctx, orgID, pullRequestID, currentHeadSHA, supersededBySessionID)
}
