package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const captureIdentityChangeReason = "code_review.capture_identity_changed"

// QueueCapturedIdentityReplacement persists current-input work before the
// caller retires a full review whose queued identity no longer matches capture.
// Its source-session identity makes controller replay converge on one request.
func (s *Service) QueueCapturedIdentityReplacement(ctx context.Context, input ReviewChangedInput) (ReviewRequestedResult, error) {
	if input.PriorSessionID == uuid.Nil {
		return ReviewRequestedResult{}, fmt.Errorf("source review session is required for capture identity replacement")
	}
	input.ExplicitRequest = true
	input.ChangeKey = "capture_identity:" + input.PriorSessionID.String()
	input.GitHubDeliveryID = input.ChangeKey
	input.ChangeReason = captureIdentityChangeReason
	parent, err := s.sessions.GetByID(ctx, input.OrgID, input.PriorSessionID)
	if err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("load captured-identity source authorization: %w", err)
	}
	input.AutomaticRecovery, err = captureIdentityAutomaticSource(parent.RevisionContext, input.TriggerSource)
	if err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("decode captured-identity source authorization: %w", err)
	}
	if s.SchedulingEnabled() {
		// Ordinary review-now admission can join a running same-head review even
		// when its pinned policy changed. Force a fresh immutable full attempt.
		return s.scheduleReview(ctx, input, models.CodeReviewReviewNow, true, nil)
	}
	source, err := s.metadata.GetBySessionID(ctx, input.OrgID, input.PriorSessionID)
	if err != nil {
		return ReviewRequestedResult{}, fmt.Errorf("load captured-identity source review: %w", err)
	}
	if source.PullRequestID != input.PullRequestID {
		return ReviewRequestedResult{}, fmt.Errorf("captured-identity source does not belong to pull request")
	}
	if source.Status != models.CodeReviewSessionStatusQueued && source.Status != models.CodeReviewSessionStatusRunning {
		return retiredCaptureIdentitySource(source), nil
	}
	// Reuse only this source session's already-dispatched recovery request.
	// An unrelated newer row may carry different analysis or human intent.
	existing, err := s.metadata.GetByOutputKey(ctx, input.OrgID, explicitReviewRequestOutputKey(input.PullRequestID, input.GitHubDeliveryID))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ReviewRequestedResult{}, fmt.Errorf("lookup captured-identity replacement: %w", err)
	}
	if err == nil && !codeReviewAttemptNeverDispatched(existing) {
		if existing.Status != models.CodeReviewSessionStatusQueued && existing.Status != models.CodeReviewSessionStatusRunning {
			return reusedReviewRequestedResult(existing, existing.TriggerSource), nil
		}
		active, err := s.jobs.HasActiveByDedupeKey(ctx, input.OrgID, "agent", "code_review:"+existing.ReviewOutputKey)
		if err != nil {
			return ReviewRequestedResult{}, fmt.Errorf("confirm captured-identity replacement dispatch: %w", err)
		}
		if active {
			result := reusedReviewRequestedResult(existing, existing.TriggerSource)
			result.DispatchConfirmed = true
			return result, nil
		}
	}
	// The starter resolves policy and refreshes the provider snapshot only after
	// the obsolete session has retired. Explicit delivery identity bypasses
	// same-head reuse while automatic source approval gates remain in force.
	result, err := s.QueueReviewChanged(ctx, input)
	if err == nil && result.IgnoredReason == "already_approved" {
		// Approval intentionally retires automatic work. A replacement is not
		// required once the existing automatic-admission gate closes.
		result.Processed = true
	}
	return result, err
}

func retiredCaptureIdentitySource(source models.CodeReviewSessionMetadata) ReviewRequestedResult {
	result := reusedReviewRequestedResult(source, source.TriggerSource)
	result.IgnoredReason = "source_review_retired"
	return result
}

// Explicit identity dedupes recovery delivery, but does not turn an automatic
// source review into human authorization to bypass pause or approval policy.
func reviewAdmissionIsExplicit(input ReviewChangedInput) bool {
	return input.ExplicitRequest && !input.AutomaticRecovery
}

func captureIdentityAutomaticSource(raw json.RawMessage, trigger models.CodeReviewTriggerSource) (bool, error) {
	var source struct {
		AutomaticRecovery *bool  `json:"capture_identity_automatic"`
		ChangeReason      string `json:"change_reason"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &source); err != nil {
			return false, err
		}
		if source.AutomaticRecovery != nil {
			return *source.AutomaticRecovery, nil
		}
		// Legacy automatic reassessments inherited the original reviewer-picker
		// trigger. Their immutable change reason identifies automatic admission.
		if (strings.HasPrefix(source.ChangeReason, "pull_request.") && source.ChangeReason != explicitReviewRequestChangeReason) ||
			strings.HasPrefix(source.ChangeReason, "check_run.") || strings.HasPrefix(source.ChangeReason, "check_suite.") ||
			strings.HasPrefix(source.ChangeReason, "status.") || source.ChangeReason == "code_review.live_head_changed" {
			return true, nil
		}
	}
	return trigger == models.CodeReviewTriggerSourceAutoPolicy, nil
}
