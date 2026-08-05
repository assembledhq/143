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
		if services.PR == nil {
			return fmt.Errorf("pull request snapshot service unavailable for code review reassessment")
		}
		var input codereviewsvc.ReviewChangedInput
		if err := json.Unmarshal(payload, &input); err != nil {
			return fmt.Errorf("decode code review reassessment starter payload: %w", err)
		}
		if input.OrgID == uuid.Nil || input.PullRequestID == uuid.Nil {
			return fmt.Errorf("org_id and pull_request_id are required for code review reassessment")
		}
		if input.TriggeringDisputeID != nil && input.ReviewRequestDisputeID != nil {
			return fmt.Errorf("code review starter cannot be both a dispute reassessment and an ordinary classified request")
		}
		registerCodeReviewStarterDeadLetter(ctx, services, logger, input)
		pr, err := stores.PullRequests.GetByID(ctx, input.OrgID, input.PullRequestID)
		if err != nil {
			return fmt.Errorf("load current pull request for code review reassessment: %w", err)
		}
		queuedHeadSHA := strings.TrimSpace(input.HeadSHA)
		snapshot, err := services.PR.GetCodeReviewPullRequestSnapshot(ctx, input.OrgID, input.RepositoryID, pr.GitHubPRNumber)
		if err != nil {
			wrapped := fmt.Errorf("load latest pull request for code review reassessment: %w", err)
			return classifyGitHubJobError(wrapped, input.PullRequestID.String())
		}
		input.GitHubRepo = pr.GitHubRepo
		input.GitHubPRNumber = snapshot.Number
		input.GitHubPRURL = snapshot.HTMLURL
		input.PullRequestTitle = snapshot.Title
		input.PullRequestAuthor = snapshot.AuthorLogin
		input.BaseSHA = strings.TrimSpace(snapshot.BaseSHA)
		input.HeadSHA = strings.TrimSpace(snapshot.HeadSHA)
		input.FromFork = snapshot.FromFork
		if input.HeadSHA == "" {
			return fmt.Errorf("latest pull request head is missing for code review reassessment")
		}
		if !input.ExplicitRequest && queuedHeadSHA != input.HeadSHA {
			input.ChangeKey, err = codereviewsvc.MaterialChangeKey(input.HeadSHA)
			if err != nil {
				return fmt.Errorf("build latest code review material change key: %w", err)
			}
			queued, queueErr := services.CodeReviewLifecycle.QueueReviewChanged(ctx, input)
			if queueErr != nil {
				return fmt.Errorf("debounce newer code review head: %w", queueErr)
			}
			logEvent := logger.Info().
				Str("org_id", input.OrgID.String()).
				Str("pull_request_id", input.PullRequestID.String()).
				Str("queued_head_sha", queuedHeadSHA).
				Str("latest_head_sha", input.HeadSHA).
				Bool("reused", queued.Reused).
				Str("ignored_reason", queued.IgnoredReason)
			if queued.JobID != uuid.Nil {
				logEvent = logEvent.Str("job_id", queued.JobID.String())
			}
			logEvent.Msg("coalesced stale code review starter into latest debounced head")
			return nil
		}

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
		if input.ReviewRequestDisputeID != nil && result.SessionID == uuid.Nil {
			detail := "The requested code review could not start. Post a new reviewer mention to try again."
			if result.IgnoredReason != "" {
				detail = "The requested code review could not start (" + strings.ReplaceAll(result.IgnoredReason, "_", " ") + "). Post a new reviewer mention to try again."
			}
			if services.CodeReviewDisputes == nil {
				return fmt.Errorf("code review dispute service unavailable after classified review request was not started")
			}
			return services.CodeReviewDisputes.FailTriage(ctx, input.OrgID, *input.ReviewRequestDisputeID, detail)
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

func registerCodeReviewStarterDeadLetter(ctx context.Context, services *Services, logger zerolog.Logger, input codereviewsvc.ReviewChangedInput) {
	if services == nil || services.CodeReviewDisputes == nil {
		return
	}
	disputeID := input.TriggeringDisputeID
	detail := "The reassessment could not start after repeated attempts. The objection remains recorded for a policy owner."
	logMessage := "failed to terminalize dead-lettered code review reassessment starter"
	if input.ReviewRequestDisputeID != nil {
		disputeID = input.ReviewRequestDisputeID
		detail = "The requested code review could not start after repeated attempts. Post a new reviewer mention to try again."
		logMessage = "failed to terminalize dead-lettered classified code review request"
	}
	if disputeID == nil {
		return
	}
	jobctx.RegisterDeadLetterHook(ctx, func(hookCtx context.Context, deadLetterErr error) {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(hookCtx), 10*time.Second)
		defer cancel()
		if err := services.CodeReviewDisputes.FailTriage(writeCtx, input.OrgID, *disputeID, detail); err != nil {
			logger.Warn().Err(err).
				AnErr("dead_letter_error", deadLetterErr).
				Str("dispute_id", disputeID.String()).
				Msg(logMessage)
		}
	})
}
