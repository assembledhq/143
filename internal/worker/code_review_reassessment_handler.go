package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
		pr, err := stores.PullRequests.GetByID(ctx, input.OrgID, input.PullRequestID)
		if err != nil {
			return fmt.Errorf("load current pull request for code review reassessment: %w", err)
		}
		input.GitHubRepo = pr.GitHubRepo
		input.GitHubPRNumber = pr.GitHubPRNumber
		input.GitHubPRURL = pr.GitHubPRURL
		input.PullRequestTitle = pr.Title
		input.BaseSHA = strings.TrimSpace(stringPtrValue(pr.BaseSHA))
		input.HeadSHA = strings.TrimSpace(stringPtrValue(pr.HeadSHA))

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
		return nil
	}
}
