package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type codeReviewDisputeJobPayload struct {
	OrgID     uuid.UUID `json:"org_id"`
	DisputeID uuid.UUID `json:"dispute_id"`
}

const codeReviewDisputeMachineCycleBudget = 2

func newTriageCodeReviewDisputeHandler(service codeReviewDisputeService) JobHandler {
	return func(ctx context.Context, _ string, payload json.RawMessage) error {
		var input codeReviewDisputeJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("decode code review dispute triage payload: %w", err)
		}
		if input.OrgID == uuid.Nil || input.DisputeID == uuid.Nil {
			return fmt.Errorf("org_id and dispute_id are required")
		}
		jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, deadLetterErr error) {
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(hookCtx), 10*time.Second)
			defer cancel()
			detail := "We could not classify this objection automatically. It remains recorded for review."
			if err := service.FailTriage(writeCtx, input.OrgID, input.DisputeID, detail); err != nil {
				zerolog.Ctx(writeCtx).Warn().Err(err).
					Str("dispute_id", input.DisputeID.String()).
					Msg("failed to terminalize dead-lettered code review dispute triage")
			}
		})
		return service.Triage(ctx, input.OrgID, input.DisputeID)
	}
}

func newReplyCodeReviewDisputeHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, _ string, payload json.RawMessage) error {
		var input codeReviewDisputeJobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("decode code review dispute reply payload: %w", err)
		}
		if input.OrgID == uuid.Nil || input.DisputeID == uuid.Nil {
			return fmt.Errorf("org_id and dispute_id are required")
		}
		jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, deadLetterErr error) {
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(hookCtx), 10*time.Second)
			defer cancel()
			detail := "GitHub reply publication exhausted its retries. The objection remains recorded in 143."
			if err := stores.CodeReviewDisputes.MarkReplyFailed(writeCtx, input.OrgID, input.DisputeID, detail); err != nil {
				logger.Warn().Err(err).
					Str("dispute_id", input.DisputeID.String()).
					AnErr("dead_letter_error", deadLetterErr).
					Msg("failed to terminalize dead-lettered code review dispute reply")
			}
		})
		dispute, body, err := services.CodeReviewDisputes.BuildReply(ctx, input.OrgID, input.DisputeID)
		if err != nil {
			return err
		}
		if !shouldPublishCodeReviewDisputeReply(dispute) {
			return nil
		}
		if services.CodeReviewDisputePublisher == nil {
			return fmt.Errorf("code review dispute GitHub publisher unavailable")
		}
		reserved, firstAttempt, err := stores.CodeReviewDisputes.ReserveReplyCycle(ctx, input.OrgID, input.DisputeID, codeReviewDisputeMachineCycleBudget)
		if err != nil {
			return err
		}
		if !reserved {
			logger.Warn().
				Str("org_id", input.OrgID.String()).
				Str("dispute_id", input.DisputeID.String()).
				Msg("stopped code review dispute reply at machine cycle budget")
			return nil
		}
		repo, err := stores.Repositories.GetByID(ctx, input.OrgID, dispute.RepositoryID)
		if err != nil {
			return fmt.Errorf("load dispute repository: %w", err)
		}
		pr, err := stores.PullRequests.GetByID(ctx, input.OrgID, dispute.PullRequestID)
		if err != nil {
			return fmt.Errorf("load dispute pull request: %w", err)
		}
		commentID, err := services.CodeReviewDisputePublisher.PublishCodeReviewDisputeReply(ctx, ghservice.CodeReviewDisputeReplyRequest{
			OrgID: input.OrgID, InstallationID: repo.InstallationID, Repository: pr.GitHubRepo,
			PullRequestNumber: pr.GitHubPRNumber, ThreadRootCommentID: dispute.GitHubThreadRootCommentID,
			KnownReplyCommentID: dispute.ReplyCommentID, Body: body,
			// The reservation commits before publication, so only a retry can
			// have left an orphaned reply behind. Scanning the comment list on
			// a first attempt would page a busy pull request for a marker that
			// cannot be there yet.
			SearchExistingReply: !firstAttempt,
		})
		if err != nil {
			if markErr := stores.CodeReviewDisputes.MarkReplyFailed(ctx, input.OrgID, input.DisputeID, "GitHub reply publication failed; retrying."); markErr != nil {
				logger.Warn().Err(markErr).Str("dispute_id", input.DisputeID.String()).Msg("failed to record code review dispute reply failure")
			}
			return fmt.Errorf("publish code review dispute reply: %w", err)
		}
		return stores.CodeReviewDisputes.MarkReplyPublished(ctx, input.OrgID, input.DisputeID, &commentID)
	}
}

func shouldPublishCodeReviewDisputeReply(dispute models.CodeReviewDispute) bool {
	// A superseded dispute shares the live one's reply comment, so publishing
	// would overwrite the current answer with a stale one. This is checked on
	// superseded_by_dispute_id rather than reply_status because every lifecycle
	// transition -- including the CompleteReassessment that BuildReply performs
	// immediately before this call -- resets reply_status back to 'pending'.
	if dispute.SupersededByDisputeID != nil {
		return false
	}
	if dispute.ReplyStatus == models.CodeReviewDisputeReplyNotApplicable {
		return false
	}
	return dispute.Source == models.CodeReviewDisputeSourceGitHubComment ||
		(dispute.Direction != nil && *dispute.Direction == models.CodeReviewDisputeDirectionShouldNotHaveApproved)
}
