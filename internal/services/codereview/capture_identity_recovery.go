package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const captureIdentityChangeReason = "code_review.capture_identity_changed"

// CaptureIdentitySourceCompletionPendingReason preserves the source instead of
// acknowledging a replacement handoff. It grants no queue or send authority.
const CaptureIdentitySourceCompletionPendingReason = "source_confirmed_completion_pending"

// A scheduling callback returns this private marker to roll back its whole
// no-op transaction, including the schedule wrapper's ordinary updated_at write.
var errCaptureIdentitySourceCompletionPending = errors.New("capture identity source completion is pending")

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
	source, pending, err := s.captureIdentitySource(ctx, input)
	if err != nil {
		return ReviewRequestedResult{}, err
	}
	if source.Status != models.CodeReviewSessionStatusQueued && source.Status != models.CodeReviewSessionStatusRunning {
		return retiredCaptureIdentitySource(captureIdentitySourceMetadata(source)), nil
	}
	if pending {
		return preservedCaptureIdentitySource(source), nil
	}
	metadataID := source.MetadataID
	input.PriorMetadataID = &metadataID
	if s.SchedulingEnabled() {
		// Ordinary review-now admission can join a running same-head review even
		// when its pinned policy changed. Force a fresh immutable full attempt.
		return s.scheduleReview(ctx, input, models.CodeReviewReviewNow, true, nil)
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
		pending, err := s.metadata.HasConfirmedFullAssessmentCompletionPending(ctx, input.OrgID, existing.ID)
		if err != nil {
			return ReviewRequestedResult{}, fmt.Errorf("confirm captured-identity replacement completion ownership: %w", err)
		}
		if pending {
			return reusedReviewRequestedResult(existing, existing.TriggerSource), nil
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

func (s *Service) captureIdentitySource(ctx context.Context, input ReviewChangedInput) (db.CodeReviewCaptureIdentitySource, bool, error) {
	source, err := s.metadata.ResolveCaptureIdentitySource(ctx, input.OrgID, input.PriorSessionID, input.PullRequestID, input.PriorMetadataID)
	if err != nil {
		return db.CodeReviewCaptureIdentitySource{}, false, fmt.Errorf("load captured-identity source review: %w", err)
	}
	if source.MetadataID == uuid.Nil || source.OrgID != input.OrgID || source.SessionID != input.PriorSessionID || source.PullRequestID != input.PullRequestID || (input.PriorMetadataID != nil && source.MetadataID != *input.PriorMetadataID) {
		return db.CodeReviewCaptureIdentitySource{}, false, fmt.Errorf("captured-identity source scope does not match")
	}
	if source.Status != models.CodeReviewSessionStatusQueued && source.Status != models.CodeReviewSessionStatusRunning {
		return source, false, nil
	}
	pending, err := s.metadata.HasConfirmedFullAssessmentCompletionPending(ctx, input.OrgID, source.MetadataID)
	if err != nil {
		return db.CodeReviewCaptureIdentitySource{}, false, fmt.Errorf("confirm captured-identity source completion ownership: %w", err)
	}
	return source, pending, nil
}

func (s *Service) lockCaptureIdentitySource(ctx context.Context, tx pgx.Tx, input ReviewChangedInput) (db.CodeReviewCaptureIdentitySource, bool, error) {
	// Resolve at most two candidates before locking one exact owner. Legacy
	// session-only ambiguity is an error, not permission to choose a sibling.
	source, err := s.metadata.ResolveCaptureIdentitySource(ctx, input.OrgID, input.PriorSessionID, input.PullRequestID, input.PriorMetadataID)
	if err != nil {
		return db.CodeReviewCaptureIdentitySource{}, false, fmt.Errorf("resolve captured-identity source lock: %w", err)
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND pull_request_id=$3 AND id=$4 FOR UPDATE`, input.OrgID, input.PriorSessionID, input.PullRequestID, source.MetadataID).Scan(&id); err != nil {
		return db.CodeReviewCaptureIdentitySource{}, false, fmt.Errorf("lock captured-identity source review: %w", err)
	}
	if id != source.MetadataID {
		return db.CodeReviewCaptureIdentitySource{}, false, fmt.Errorf("captured-identity source lock does not match")
	}
	input.PriorMetadataID = &id
	// Both status and positive receipt observation use later statements after the
	// exact metadata wait, on the already supplied transaction.
	return s.captureIdentitySource(ctx, input)
}

func captureIdentitySourceMetadata(source db.CodeReviewCaptureIdentitySource) models.CodeReviewSessionMetadata {
	return models.CodeReviewSessionMetadata{ID: source.MetadataID, OrgID: source.OrgID, SessionID: source.SessionID, PullRequestID: source.PullRequestID, Status: source.Status, TriggerSource: source.TriggerSource}
}

func preservedCaptureIdentitySource(source db.CodeReviewCaptureIdentitySource) ReviewRequestedResult {
	result := reusedReviewRequestedResult(captureIdentitySourceMetadata(source), source.TriggerSource)
	result.IgnoredReason = CaptureIdentitySourceCompletionPendingReason
	return result
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
