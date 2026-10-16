package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type captureHandoffLifecycle struct {
	codeReviewLifecycleStub
	valid    bool
	cause    error
	requests [][3]uuid.UUID
}

func (s *captureHandoffLifecycle) ValidateScheduledExecution(_ context.Context, orgID, prID, sessionID uuid.UUID) (bool, error) {
	s.requests = append(s.requests, [3]uuid.UUID{orgID, prID, sessionID})
	return s.valid, s.cause
}

type captureHandoffProbe struct {
	result   codereviewsvc.AssessmentInputCaptureResult
	cause    error
	requests []codereviewsvc.AssessmentInputCaptureRequest
	onCall   func(context.Context)
}

func (c *captureHandoffProbe) CaptureAssessmentInputs(ctx context.Context, in codereviewsvc.AssessmentInputCaptureRequest) (codereviewsvc.AssessmentInputCaptureResult, error) {
	c.requests = append(c.requests, in)
	if c.onCall != nil {
		c.onCall(ctx)
	}
	return c.result, c.cause
}

func TestCodeReviewPreflightCaptureFallback(t *testing.T) {
	t.Parallel()
	providerFailure := errors.New("provider unavailable")
	tests := []struct {
		name      string
		disabled  bool
		missing   bool
		cause     error
		syncCause error
		want      error
		captures  int
		syncs     int
		pending   bool
	}{
		{name: "disabled assessment keeps ordinary refresh", disabled: true, syncs: 1},
		{name: "missing capture keeps ordinary refresh", missing: true, syncs: 1},
		{name: "incomplete fingerprint keeps ordinary refresh", cause: codereviewsvc.ErrAssessmentReuseUnavailable, captures: 1, syncs: 1},
		{name: "ineligible capture preserves lifecycle refresh", cause: codereviewsvc.ErrReviewIneligible, want: codereviewsvc.ErrReviewIneligible, captures: 1, syncs: 1},
		{name: "ineligible lifecycle refresh failure propagates", cause: codereviewsvc.ErrReviewIneligible, syncCause: providerFailure, want: providerFailure, captures: 1, syncs: 1},
		{name: "capture failure never repeats provider work", cause: providerFailure, want: providerFailure, captures: 1},
		{name: "capture pending keeps bounded wait", cause: fmt.Errorf("capture: %w", ghservice.ErrPullRequestMergeabilityPending), want: ghservice.ErrPullRequestMergeabilityPending, captures: 1, pending: true},
		{name: "legacy refresh pending keeps bounded wait", disabled: true, syncCause: ghservice.ErrPullRequestMergeabilityPending, want: ghservice.ErrPullRequestMergeabilityPending, syncs: 1, pending: true},
		{name: "disconnected legacy refresh remains a no-op", disabled: true, syncCause: ghservice.ErrPullRequestRepositoryDisconnected, syncs: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job := runCodeReviewPayload{OrgID: uuid.New(), RepositoryID: uuid.New(), PullRequestID: uuid.New(), SessionID: uuid.New()}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the isolated startup assessment store")
			defer mock.Close()
			if tt.captures != 0 {
				mock.ExpectQuery("SELECT .* FROM code_review_revision_assessments WHERE org_id=\\$1 AND session_id=\\$2").WithArgs(job.OrgID, job.SessionID).WillReturnError(pgx.ErrNoRows)
			}
			validator := &captureHandoffLifecycle{valid: true}
			capture := &captureHandoffProbe{cause: tt.cause}
			capture.onCall = func(context.Context) {
				require.Equal(t, [][3]uuid.UUID{{job.OrgID, job.PullRequestID, job.SessionID}}, validator.requests, "scheduled ownership must be checked before any provider capture")
			}
			syncs := 0
			services := &Services{CodeReviewAssessmentsEnabled: !tt.disabled, CodeReviewInputCapture: capture, CodeReviewLifecycle: validator, PR: &stubPRService{syncPullRequestStateFn: func(ctx context.Context, orgID, prID uuid.UUID) error {
				syncs++
				require.Equal(t, job.OrgID, orgID, "fallback must refresh only the owning organization")
				require.Equal(t, job.PullRequestID, prID, "fallback must refresh the same pull request")
				require.Equal(t, ghservice.PullRequestSyncReasonCodeReview, ghservice.PullRequestSyncReasonFromContext(ctx), "fallback must retain the independent review safety-read reason")
				return tt.syncCause
			}}}
			if tt.missing {
				services.CodeReviewInputCapture = nil
			}
			assessment, captured, err := prepareCodeReviewPreflight(context.Background(), &Stores{CodeReviewAssessments: db.NewCodeReviewAssessmentStore(mock)}, services, zerolog.Nop(), job, models.CodeReviewSessionMetadata{})
			if tt.want == nil {
				require.NoError(t, err, "fallback should preserve a usable legacy preflight")
			} else {
				require.ErrorIs(t, err, tt.want, "startup must preserve the original provider or eligibility failure")
			}
			if tt.pending {
				var retry *RetryableError
				require.ErrorAs(t, err, &retry, "mergeability must remain a typed dependency wait")
				require.Equal(t, new(5*time.Second), retry.RetryAfter, "startup capture must retain prompt mergeability polling")
				require.Equal(t, new(githubRateLimitMaxRetryDuration), retry.MaxRetryDuration, "startup capture must retain the finite GitHub recovery window")
				require.False(t, retry.ConsumeAttempt, "indeterminate mergeability must preserve the ordinary attempt budget")
			}
			require.Nil(t, assessment, "incomplete or failed capture must not create reusable assessment authority")
			require.Nil(t, captured, "incomplete or failed capture must not escape as a successful handoff")
			require.Equal(t, tt.captures, len(capture.requests), "each startup attempt must capture at most once")
			require.Equal(t, tt.syncs, syncs, "only successful complete capture may suppress the ordinary health refresh")
			require.Equal(t, [][3]uuid.UUID{{job.OrgID, job.PullRequestID, job.SessionID}}, validator.requests, "startup must validate the exact scheduled identity once")
			require.NoError(t, mock.ExpectationsWereMet(), "startup must not insert assessments or query another tenant on fallback")
		})
	}
}

func TestCodeReviewPreflightRejectsUnownedScheduledExecution(t *testing.T) {
	t.Parallel()
	cause := errors.New("scheduled ownership unavailable")
	tests := []struct {
		name  string
		cause error
		want  error
	}{
		{name: "superseded scheduled generation", want: errCodeReviewSchedulingSuperseded},
		{name: "validation error", cause: cause, want: cause},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			job := runCodeReviewPayload{OrgID: uuid.New(), PullRequestID: uuid.New(), SessionID: uuid.New()}
			validator := &captureHandoffLifecycle{cause: tt.cause}
			capture := &captureHandoffProbe{}
			syncs := 0
			services := &Services{CodeReviewAssessmentsEnabled: true, CodeReviewInputCapture: capture, CodeReviewLifecycle: validator, PR: &stubPRService{syncPullRequestStateFn: func(context.Context, uuid.UUID, uuid.UUID) error { syncs++; return nil }}}
			assessment, captured, err := prepareCodeReviewPreflight(context.Background(), &Stores{}, services, zerolog.Nop(), job, models.CodeReviewSessionMetadata{})
			require.ErrorIs(t, err, tt.want, "invalid admission must retain its original scheduling outcome")
			require.Nil(t, assessment, "an unowned scheduled execution must not create an assessment")
			require.Nil(t, captured, "an unowned scheduled execution must not acquire capture authority")
			require.Equal(t, 0, len(capture.requests), "scheduling rejection must precede all provider captures")
			require.Equal(t, 0, syncs, "scheduling rejection must precede ordinary health refreshes")
			require.Equal(t, [][3]uuid.UUID{{job.OrgID, job.PullRequestID, job.SessionID}}, validator.requests, "validation must preserve the tenant, PR and session fence")
		})
	}
}
