package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestConfirmedCompletionSameHeadReader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		force  bool
		failed bool
	}{
		{name: "same head preserves original without live job"},
		{name: "explicit rerequest retains deferred semantics", force: true},
		{name: "observation failure never authorizes takeover", failed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := newReviewRequestedInput(nil)
			policies := newPolicyStub()
			policies.resolved.Policy = &models.CodeReviewPolicyRecord{ID: uuid.New(), Version: 1}
			latest := models.CodeReviewSessionMetadata{ID: uuid.New(), OrgID: input.OrgID, SessionID: uuid.New(), PullRequestID: input.PullRequestID, PolicyID: policies.resolved.Policy.ID, HeadSHA: input.HeadSHA, Status: models.CodeReviewSessionStatusRunning, ReviewOutputKey: "synthetic-confirmed-owner", CreatedAt: time.Now().Add(-2 * codeReviewJobEnqueueGracePeriod)}
			metadata := &metadataStub{latest: latest, completionPending: map[uuid.UUID]bool{latest.ID: true}}
			cause := errors.New("synthetic completion observation unavailable")
			if tt.failed {
				metadata.completionErr = cause
			}
			sessions, jobs := &sessionStub{}, &jobStub{}
			service := NewService(policies, metadata, sessions, jobs, zerolog.Nop(), Config{AppReviewerLogins: []string{"143-code-reviewer"}})
			var result ReviewRequestedResult
			var err error
			if tt.force {
				result, err = service.startReview(context.Background(), input, reviewStartOptions{triggerSource: models.CodeReviewTriggerSourceAppReviewer, forceReassessment: true})
			} else {
				result, err = service.HandleReviewRequested(context.Background(), input)
			}
			if tt.failed {
				require.ErrorIs(t, err, cause, "observation refusal must not be treated as absent recovery ownership")
				require.Equal(t, ReviewRequestedResult{}, result, "failed observation must expose no replacement candidate")
			} else {
				require.NoError(t, err, "a confirmed original remains reusable beyond enqueue grace without a live job")
				want := reusedReviewRequestedResult(latest, models.CodeReviewTriggerSourceAppReviewer)
				want.Deferred = tt.force
				require.Equal(t, want, result, "reuse must preserve exact original identity and explicit defer semantics without inventing dispatch proof")
			}
			require.Equal(t, []uuid.UUID{latest.ID}, metadata.completionCalls, "the reader must observe the exact selected metadata rather than a latest assessment")
			require.Equal(t, [][2]uuid.UUID{{input.OrgID, latest.ID}}, metadata.completionScopes, "same-head recognition must retain the independent tenant and metadata binding")
			require.Equal(t, latest, metadata.latest, "the original metadata must remain completely unchanged")
			require.Equal(t, 0, metadata.failCalls, "confirmed ownership or observation failure must precede false orphan failure")
			require.Equal(t, 0, sessions.createCalls+sessions.updateStatusCalls+sessions.updateFailureCalls, "recognition must not replace or terminalize a parent")
			require.Equal(t, 0, jobs.enqueueCalls, "recognition must not create a recovery wake or replacement controller")
		})
	}
}

func TestConfirmedCompletionOlderRetryReader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		failed bool
	}{
		{name: "newer confirmed owner remains a conflict"},
		{name: "observation failure preserves both attempts", failed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			source := models.CodeReviewSessionMetadata{ID: uuid.New(), OrgID: uuid.New(), SessionID: uuid.New(), PullRequestID: uuid.New(), Status: models.CodeReviewSessionStatusFailed}
			latest := source
			latest.ID, latest.SessionID, latest.Status = uuid.New(), uuid.New(), models.CodeReviewSessionStatusRunning
			latest.CreatedAt = time.Now().Add(-2 * codeReviewJobEnqueueGracePeriod)
			metadata := &metadataStub{latest: source, latestByPullRequest: latest, completionPending: map[uuid.UUID]bool{latest.ID: true}}
			cause := errors.New("synthetic newer completion observation failure")
			if tt.failed {
				metadata.completionErr = cause
			}
			sessions, jobs := &sessionStub{}, &jobStub{}
			service := NewService(newPolicyStub(), metadata, sessions, jobs, zerolog.Nop(), Config{})
			err := service.ensureLatestRetrySource(context.Background(), source)
			if tt.failed {
				require.ErrorIs(t, err, cause, "retry must propagate an observation error without failing the newer owner")
			} else {
				var conflict *RetryReviewConflictError
				require.ErrorAs(t, err, &conflict, "newer confirmed ownership must preserve the existing retry conflict")
				require.Equal(t, RetryReviewConflictNewerAttempt, conflict.Code, "confirmed recovery is not an orphaned replacement")
			}
			require.Equal(t, source, metadata.latest, "an old retry source must not gain a false replacement link")
			require.Equal(t, latest, metadata.latestByPullRequest, "the newer original must retain its whole business state")
			require.Equal(t, []uuid.UUID{latest.ID}, metadata.completionCalls, "retry exclusion binds the actual newer metadata ID")
			require.Equal(t, [][2]uuid.UUID{{source.OrgID, latest.ID}}, metadata.completionScopes, "retry recognition must never observe another tenant's metadata")
			require.Equal(t, 0, metadata.failCalls+sessions.updateStatusCalls+sessions.updateFailureCalls+jobs.enqueueCalls, "no false failure, parent effect, or replacement may follow recognition or its error")
		})
	}
}

func TestConfirmedCaptureSourceAndReplacementReaders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		sourcePending      bool
		replacementPending bool
		replacementFailed  bool
		scheduled          bool
		starter            bool
		failed             bool
		ambiguous          bool
		wrongHint          bool
		wrongSource        bool
	}{
		{name: "source completion prevents legacy handoff", sourcePending: true},
		{name: "source completion prevents scheduled force", sourcePending: true, scheduled: true},
		{name: "queued legacy starter preserves confirmed source", sourcePending: true, starter: true},
		{name: "queued scheduled starter preserves confirmed source", sourcePending: true, starter: true, scheduled: true},
		{name: "replacement completion reuses replacement not source", replacementPending: true},
		{name: "replacement observation failure cannot create duplicate starter", replacementFailed: true},
		{name: "observation failure prevents all admission", failed: true},
		{name: "ambiguous legacy source refuses", ambiguous: true},
		{name: "mismatched metadata hint refuses", wrongHint: true},
		{name: "foreign resolver carrier refuses", wrongSource: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			input := newReviewChangedInput()
			input.PriorSessionID, input.ChangeReason = uuid.New(), captureIdentityChangeReason
			source := models.CodeReviewSessionMetadata{ID: uuid.New(), OrgID: input.OrgID, SessionID: input.PriorSessionID, PullRequestID: input.PullRequestID, Status: models.CodeReviewSessionStatusRunning, TriggerSource: models.CodeReviewTriggerSourceAppReviewer}
			input.PriorMetadataID = &source.ID
			metadata := &metadataStub{getBySession: source, latest: source, completionPending: map[uuid.UUID]bool{source.ID: tt.sourcePending}}
			replacement := source
			replacement.ID, replacement.SessionID = uuid.New(), uuid.New()
			if tt.replacementPending || tt.replacementFailed {
				key := explicitReviewRequestOutputKey(input.PullRequestID, "capture_identity:"+input.PriorSessionID.String())
				replacement.ReviewOutputKey = key
				metadata.byOutputKeys = map[string]models.CodeReviewSessionMetadata{key: replacement}
				metadata.completionPending[replacement.ID] = tt.replacementPending
			}
			cause := errors.New("synthetic capture receipt observation failure")
			if tt.failed {
				metadata.completionErr = cause
			}
			if tt.replacementFailed {
				metadata.completionErrors = map[uuid.UUID]error{replacement.ID: cause}
			}
			if tt.ambiguous {
				input.PriorMetadataID = nil
				metadata.captureSourceErr = db.ErrCodeReviewCaptureIdentitySourceAmbiguous
			}
			if tt.wrongHint {
				id := uuid.New()
				input.PriorMetadataID = &id
			}
			if tt.wrongSource {
				metadata.captureSource = &db.CodeReviewCaptureIdentitySource{MetadataID: source.ID, OrgID: uuid.New(), SessionID: input.PriorSessionID, PullRequestID: input.PullRequestID, Status: source.Status}
			}
			sessions, jobs := &sessionStub{}, &jobStub{}
			service := NewService(newPolicyStub(), metadata, sessions, jobs, zerolog.Nop(), Config{})
			if tt.scheduled {
				// A positive common fast path must not call this nil store or prepare
				// a provider snapshot; a transaction-bound recheck is tested separately.
				service.SetScheduling(nil, &confirmedCaptureSnapshotTrap{t: t})
			}
			var result ReviewRequestedResult
			var err error
			if tt.starter {
				result, err = service.HandleReviewChanged(context.Background(), input)
			} else {
				result, err = service.QueueCapturedIdentityReplacement(context.Background(), input)
			}
			if tt.failed || tt.replacementFailed || tt.ambiguous || tt.wrongHint || tt.wrongSource {
				require.Error(t, err, "failed, ambiguous, or mismatched source proof must refuse handoff")
				require.Equal(t, ReviewRequestedResult{}, result, "refused source observation must not acknowledge any replacement")
			} else {
				require.NoError(t, err, "exact confirmed ownership should be recognized without a live queue job")
				want := reusedReviewRequestedResult(source, source.TriggerSource)
				want.IgnoredReason = CaptureIdentitySourceCompletionPendingReason
				if tt.replacementPending {
					want = reusedReviewRequestedResult(replacement, replacement.TriggerSource)
				}
				require.Equal(t, want, result, "source preservation and replacement reuse must remain distinct dispositions without dispatch authority")
			}
			require.Equal(t, 0, jobs.enqueueCalls+metadata.createCalls+metadata.staleCalls+metadata.failCalls+sessions.updateStatusCalls, "recognition or refusal must not create, revoke, stale, or fail any owner")
			require.Equal(t, []*uuid.UUID{input.PriorMetadataID}, metadata.captureSourceHints, "the exact optional original metadata hint must reach the mandatory resolver")
		})
	}
}

func TestCaptureIdentityMetadataHintJSON(t *testing.T) {
	t.Parallel()
	input := ReviewChangedInput{PriorSessionID: uuid.New()}
	raw, err := json.Marshal(input)
	require.NoError(t, err, "legacy capture payload remains serializable")
	require.NotContains(t, string(raw), "prior_metadata_id", "legacy payload bytes must not gain a null or zero optional hint")
	id := uuid.New()
	input.PriorMetadataID = &id
	raw, err = json.Marshal(input)
	require.NoError(t, err, "known exact metadata identity may be retained in an internal payload")
	var restored ReviewChangedInput
	require.NoError(t, json.Unmarshal(raw, &restored), "internal exact identity must round-trip without new authority")
	require.Equal(t, input, restored, "the full payload must preserve the original optional metadata identity")
}
