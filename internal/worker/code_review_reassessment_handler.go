package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const codeReviewReassessmentWait = 5 * time.Second

func newStartCodeReviewReassessmentHandler(stores *Stores, services *Services, logger zerolog.Logger) JobHandler {
	return func(ctx context.Context, _ string, payload json.RawMessage) error {
		if stores == nil || stores.PullRequests == nil {
			return fmt.Errorf("pull request store unavailable for code review reassessment")
		}
		if services == nil || services.CodeReviewLifecycle == nil {
			return fmt.Errorf("code review lifecycle service unavailable")
		}
		var input codereviewsvc.ReviewChangedInput
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("decode code review reassessment starter payload: %w", err)
		}
		if input.OrgID == uuid.Nil || input.PullRequestID == uuid.Nil {
			return fmt.Errorf("org_id and pull_request_id are required for code review reassessment")
		}
		if input.TriggeringDisputeID != nil && services.CodeReviewDisputes != nil {
			jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, deadLetterErr error) {
				writeCtx, cancel := context.WithTimeout(context.WithoutCancel(hookCtx), 10*time.Second)
				defer cancel()
				detail := "The reassessment could not start after repeated attempts. The objection remains recorded for a policy owner."
				if err := services.CodeReviewDisputes.FailTriage(writeCtx, input.OrgID, *input.TriggeringDisputeID, detail); err != nil {
					logger.Warn().Err(err).Str("dispute_id", input.TriggeringDisputeID.String()).Msg("failed to terminalize dead-lettered code review reassessment starter")
				}
			})
		}
		pr, err := stores.PullRequests.GetByID(ctx, input.OrgID, input.PullRequestID)
		if err != nil {
			return fmt.Errorf("load current pull request for code review reassessment: %w", err)
		}
		currentHead := strings.TrimSpace(stringPtrValue(pr.HeadSHA))
		if input.TriggeringDisputeID != nil && strings.TrimSpace(input.HeadSHA) != currentHead {
			if stores.CodeReviewDisputes == nil || services.CodeReviewDisputes == nil {
				return fmt.Errorf("code review dispute dependencies unavailable for reassessment head guard")
			}
			detail := fmt.Sprintf("The pull request changed after this objection was filed; the current head is %s.", shortCodeReviewSHA(currentHead))
			if err := stores.CodeReviewDisputes.MarkHeadChanged(ctx, input.OrgID, *input.TriggeringDisputeID, detail); err != nil {
				return err
			}
			if err := services.CodeReviewDisputes.EnqueueReply(ctx, input.OrgID, *input.TriggeringDisputeID, "head_changed"); err != nil {
				return err
			}
			return nil
		}
		input.GitHubRepo = pr.GitHubRepo
		input.GitHubPRNumber = pr.GitHubPRNumber
		input.GitHubPRURL = pr.GitHubPRURL
		input.PullRequestTitle = pr.Title
		input.BaseSHA = strings.TrimSpace(stringPtrValue(pr.BaseSHA))
		input.HeadSHA = currentHead

		result, err := services.CodeReviewLifecycle.HandleReviewChanged(ctx, input)
		if err != nil {
			return fmt.Errorf("start queued code review reassessment: %w", err)
		}
		if result.Deferred {
			logger.Info().
				Str("org_id", input.OrgID.String()).
				Str("pull_request_id", input.PullRequestID.String()).
				Str("prior_session_id", input.PriorSessionID.String()).
				Msg("waiting for older code review before reassessment")
			delay := codeReviewReassessmentWait
			return &RetryableError{
				Err:                    fmt.Errorf("older code review assessment is still active"),
				RetryAfter:             &delay,
				BypassMaxRetryDuration: true,
			}
		}
		if input.TriggeringDisputeID != nil && result.SessionID == uuid.Nil {
			detail := "The reassessment could not start. The objection remains recorded for a policy owner."
			if result.IgnoredReason != "" {
				detail = "The reassessment could not start (" + strings.ReplaceAll(result.IgnoredReason, "_", " ") + "). The objection remains recorded for a policy owner."
			}
			if services.CodeReviewDisputes == nil {
				return fmt.Errorf("code review dispute service unavailable after reassessment was not started")
			}
			return services.CodeReviewDisputes.FailTriage(ctx, input.OrgID, *input.TriggeringDisputeID, detail)
		}
		if input.TriggeringDisputeID != nil && result.SessionID != uuid.Nil {
			if stores.CodeReviewDisputes == nil {
				return fmt.Errorf("code review dispute store unavailable after reassessment start")
			}
			if err := stores.CodeReviewDisputes.MarkReassessmentStarted(ctx, input.OrgID, *input.TriggeringDisputeID, result.SessionID); err != nil {
				return err
			}
		}
		return nil
	}
}

func shortCodeReviewSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 7 {
		return value[:7]
	}
	if value == "" {
		return "the latest commit"
	}
	return value
}
