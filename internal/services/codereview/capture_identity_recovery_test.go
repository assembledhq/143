package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestQueueCapturedIdentityReplacementLegacy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		queueErr  error
		newer     bool
		missingID bool
	}{
		{name: "same head active source queues current policy starter"},
		{name: "durable admission failure retains source", queueErr: errors.New("queue unavailable")},
		{name: "replay joins newer review", newer: true},
		{name: "source session is required", missingID: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := newReviewChangedInput()
			input.PriorSessionID = uuid.New()
			input.RequestContext = &ReviewRequestContext{Source: "issue_comment", AuthorLogin: "reviewer", Body: "Check the save behavior", URL: "https://example.invalid/comment/1"}
			input.RequestedReviewerLogin, input.RequestedTeamSlug = "143-code-reviewer", "reviewers"
			input.TriggerSource = models.CodeReviewTriggerSourceTeamReviewer
			metadata := &metadataStub{latest: models.CodeReviewSessionMetadata{ID: uuid.New(), SessionID: input.PriorSessionID, PullRequestID: input.PullRequestID, HeadSHA: input.HeadSHA, Status: models.CodeReviewSessionStatusRunning}}
			metadata.getBySession = metadata.latest
			if tt.newer {
				metadata.latest.SessionID = uuid.New()
				metadata.latest.ReviewOutputKey = explicitReviewRequestOutputKey(input.PullRequestID, "capture_identity:"+input.PriorSessionID.String())
				metadata.byOutputKeys = map[string]models.CodeReviewSessionMetadata{metadata.latest.ReviewOutputKey: metadata.latest}
			}
			if tt.missingID {
				input.PriorSessionID = uuid.Nil
			}
			jobs := &jobStub{jobID: uuid.New(), err: tt.queueErr, active: tt.newer}
			policies := newPolicyStub()
			policies.resolved.Policy = &models.CodeReviewPolicyRecord{ID: uuid.New(), Version: 1}
			metadata.latest.PolicyID, metadata.latest.BaseSHA = policies.resolved.Policy.ID, input.BaseSHA
			service := NewService(policies, metadata, &sessionStub{}, jobs, zerolog.Nop(), Config{})
			result, err := service.QueueCapturedIdentityReplacement(context.Background(), input)
			if tt.missingID || tt.queueErr != nil {
				require.Error(t, err, "missing provenance or failed durable admission must be returned")
				require.Equal(t, ReviewRequestedResult{}, result, "failed admission must not acknowledge replacement work")
				require.Equal(t, 0, metadata.staleCalls, "original work must remain active until a replacement is durable")
				return
			}
			require.NoError(t, err, "capture identity recovery should accept or join current work")
			if tt.newer {
				expected := reusedReviewRequestedResult(metadata.latest, metadata.latest.TriggerSource)
				expected.DispatchConfirmed = true
				require.Equal(t, expected, result, "an obsolete replay should retain the already-dispatched recovery attempt")
				require.Equal(t, 0, jobs.enqueueCalls, "an obsolete replay must not create work that could revoke the newer attempt")
				return
			}
			require.Equal(t, ReviewRequestedResult{Processed: true, JobID: jobs.jobID}, result, "acceptance must point to the durable starter")
			expected := input
			expected.ExplicitRequest = true
			expected.ChangeKey = "capture_identity:" + input.PriorSessionID.String()
			expected.GitHubDeliveryID = expected.ChangeKey
			expected.ChangeReason = captureIdentityChangeReason
			require.Equal(t, expected, jobs.reassessmentPayload, "replacement should preserve human request and reviewer provenance")
			require.Nil(t, jobs.opts.RunAt, "capture drift should not spend another quiet period on obsolete work")
			require.Equal(t, "code_review_reassessment:"+input.PullRequestID.String()+":"+expected.ChangeKey, jobs.dedupeKey, "source session must govern deterministic replacement admission")
			require.Equal(t, 0, metadata.staleCalls, "caller must retire the old review only after accepting the durable replacement")
		})
	}
}

func TestCaptureIdentityRecoveryAuthorization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		raw       json.RawMessage
		trigger   models.CodeReviewTriggerSource
		automatic bool
		invalid   bool
	}{
		{name: "legacy synchronization inherits reviewer trigger", raw: json.RawMessage(`{"change_reason":"pull_request.synchronize"}`), trigger: models.CodeReviewTriggerSourceTeamReviewer, automatic: true},
		{name: "legacy human review picker remains explicit", raw: json.RawMessage(`{"change_reason":"pull_request.review_requested"}`), trigger: models.CodeReviewTriggerSourceTeamReviewer},
		{name: "automatic provenance survives repeated replacement", raw: json.RawMessage(`{"change_reason":"code_review.capture_identity_changed","capture_identity_automatic":true}`), trigger: models.CodeReviewTriggerSourceTeamReviewer, automatic: true},
		{name: "human provenance survives inherited automatic trigger", raw: json.RawMessage(`{"capture_identity_automatic":false}`), trigger: models.CodeReviewTriggerSourceAutoPolicy},
		{name: "legacy empty automatic context", trigger: models.CodeReviewTriggerSourceAutoPolicy, automatic: true},
		{name: "malformed authoritative context fails closed", raw: json.RawMessage(`{"change_reason":`), trigger: models.CodeReviewTriggerSourceTeamReviewer, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			automatic, err := captureIdentityAutomaticSource(tt.raw, tt.trigger)
			if tt.invalid {
				require.Error(t, err, "malformed immutable admission provenance must be returned rather than elevated to human authorization")
				return
			}
			require.NoError(t, err, "valid source admission provenance should decode")
			require.Equal(t, tt.automatic, automatic, "recovery must preserve original automatic versus human admission")
		})
	}
}
