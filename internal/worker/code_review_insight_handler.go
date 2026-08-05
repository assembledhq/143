package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type codeReviewInsightJobPayload struct {
	OrgID     uuid.UUID  `json:"org_id"`
	SessionID *uuid.UUID `json:"session_id,omitempty"`
}

func newReconcileCodeReviewOutcomesHandler(service codeReviewInsightService) JobHandler {
	return func(ctx context.Context, _ string, payload json.RawMessage) error {
		var input codeReviewInsightJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("decode code review outcome reconciliation payload: %w", err)
		}
		if input.OrgID == uuid.Nil {
			return fmt.Errorf("org_id is required")
		}
		if input.SessionID != nil {
			if err := service.ProjectDecision(ctx, input.OrgID, *input.SessionID); err != nil {
				return err
			}
			_, _, err := service.RankPendingBatches(ctx, input.OrgID, 500, 20)
			return err
		}
		return service.ReconcileAndRank(ctx, input.OrgID)
	}
}

func newRankCodeReviewDisputeHandler(service codeReviewInsightService) JobHandler {
	return func(ctx context.Context, _ string, payload json.RawMessage) error {
		var input models.CodeReviewInsightPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("decode code review dispute ranking payload: %w", err)
		}
		if err := input.Validate(); err != nil {
			return err
		}
		_, _, err := service.RankPendingBatches(ctx, input.OrgID, 500, 20)
		return err
	}
}
