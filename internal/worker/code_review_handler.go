package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type runCodeReviewPayload struct {
	OrgID         uuid.UUID `json:"org_id"`
	SessionID     uuid.UUID `json:"session_id"`
	MetadataID    uuid.UUID `json:"metadata_id"`
	RepositoryID  uuid.UUID `json:"repository_id"`
	PullRequestID uuid.UUID `json:"pull_request_id"`
	PolicyID      uuid.UUID `json:"policy_id"`
	PolicyVersion int       `json:"policy_version"`
	HeadSHA       string    `json:"head_sha"`
	OutputKey     string    `json:"review_output_key"`
}

func newRunCodeReviewHandler(stores *Stores, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, _ string, payload json.RawMessage) error {
		if stores == nil || stores.CodeReviews == nil {
			return fmt.Errorf("code review store unavailable")
		}
		var job runCodeReviewPayload
		if err := json.Unmarshal(payload, &job); err != nil {
			return fmt.Errorf("decode code review job payload: %w", err)
		}
		if job.OrgID == uuid.Nil || job.SessionID == uuid.Nil {
			return fmt.Errorf("org_id and session_id are required")
		}
		if _, err := stores.CodeReviews.MarkRunning(ctx, job.OrgID, job.SessionID); err != nil {
			return fmt.Errorf("mark code review running: %w", err)
		}
		reason := "Automated reviewer agents and GitHub review submission are not configured for this worker."
		body := models.BuildCodeReviewFinalReviewBody(models.CodeReviewFinalReviewInput{
			Decision:      models.CodeReviewDecisionCommentOnly,
			Acceptable:    false,
			RiskReasons:   []string{reason},
			PolicyVersion: job.PolicyVersion,
			HeadSHA:       job.HeadSHA,
			Summary:       "143 recorded the review request and withheld automated approval.",
		})
		raw := "code review orchestration completed in conservative comment-only mode"
		result := &models.CodeReviewAgentResult{
			OrgID:         job.OrgID,
			SessionID:     job.SessionID,
			AgentProvider: "143",
			Role:          models.CodeReviewAgentRoleOrchestrator,
			Status:        models.CodeReviewAgentResultStatusCompleted,
			RawOutput:     &raw,
		}
		if err := stores.CodeReviews.CreateAgentResult(ctx, result); err != nil {
			return fmt.Errorf("create conservative code review result: %w", err)
		}
		if _, err := stores.CodeReviews.CompleteReview(ctx, job.OrgID, db.CompleteCodeReviewParams{
			SessionID:       job.SessionID,
			Decision:        models.CodeReviewDecisionCommentOnly,
			Acceptable:      false,
			FinalReviewBody: body,
		}); err != nil {
			return fmt.Errorf("complete conservative code review: %w", err)
		}
		logger.Info().
			Str("org_id", job.OrgID.String()).
			Str("session_id", job.SessionID.String()).
			Msg("completed conservative code review")
		return nil
	}
}
