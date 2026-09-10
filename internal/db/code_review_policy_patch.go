package db

import (
	"context"
	"encoding/json"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// PatchPolicy shares the policy lock, validation, and version insertion with
// legacy PUT, internal writers, and restore.
func (s *CodeReviewStore) PatchPolicy(ctx context.Context, orgID uuid.UUID, patch json.RawMessage, expectedVersion int, userID *uuid.UUID) (models.CodeReviewPolicyRecord, error) {
	return s.savePolicy(ctx, orgID, models.CodeReviewPolicyConfig{}, userID, &expectedVersion, patch)
}
