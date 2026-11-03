package worker

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestListCodeReviewPriorFindingsRequiresOptIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		context *codereviewsvc.ReviewRequestContext
	}{
		{name: "automatic review has no request context"},
		{name: "requested review did not opt in", context: &codereviewsvc.ReviewRequestContext{Source: "ui", Body: "Please look again."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create the mock pool")
			defer mock.Close()

			found, err := listCodeReviewPriorFindings(context.Background(), &Stores{CodeReviews: db.NewCodeReviewStore(mock)},
				runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New(), RequestContext: tt.context},
				models.CodeReviewSessionMetadata{PullRequestID: uuid.New()})

			require.NoError(t, err, "a review without the opt-in should not fail")
			require.Empty(t, found, "a review without the opt-in should see no prior findings")
			require.NoError(t, mock.ExpectationsWereMet(), "a review without the opt-in should not query prior findings")
		})
	}
}

func TestCodeReviewOutcomeProvenance(t *testing.T) {
	t.Parallel()

	const optedIn = "This review was requested with findings from earlier review rounds (experimental)."
	tests := []struct {
		name     string
		input    liveCodeReviewOutcomeInput
		expected string
	}{
		{name: "automatic review stays silent"},
		{
			name:  "requested review without the opt-in stays silent",
			input: liveCodeReviewOutcomeInput{Job: runCodeReviewPayload{RequestContext: &codereviewsvc.ReviewRequestContext{Body: "Please look again."}}},
		},
		{
			name:     "opted-in review says so",
			input:    liveCodeReviewOutcomeInput{Job: runCodeReviewPayload{RequestContext: &codereviewsvc.ReviewRequestContext{IncludePriorFindings: true}}},
			expected: optedIn,
		},
		{
			name: "existing provenance wins",
			input: liveCodeReviewOutcomeInput{
				ReviewProvenance: "Code review reused from assessment.",
				Job:              runCodeReviewPayload{RequestContext: &codereviewsvc.ReviewRequestContext{IncludePriorFindings: true}},
			},
			expected: "Code review reused from assessment.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, codeReviewOutcomeProvenance(tt.input), "the GitHub comment should disclose only opted-in reviews")
		})
	}
}

func TestCodeReviewAvailablePriorFindingsURL(t *testing.T) {
	t.Parallel()

	sessionID := uuid.MustParse("90d8a47d-d87e-4780-90af-040f5144685a")
	tests := []struct {
		name     string
		services *Services
		expected string
	}{
		{name: "missing services"},
		{name: "scheduling disabled", services: &Services{FrontendURL: "https://143.test", CodeReviewLifecycle: &statusCommentSchedulingStub{}}},
		{name: "missing frontend", services: &Services{CodeReviewLifecycle: &statusCommentSchedulingStub{enabled: true}}},
		{
			name:     "scheduling enabled",
			services: &Services{FrontendURL: "https://143.test/", CodeReviewLifecycle: &statusCommentSchedulingStub{enabled: true}},
			expected: "https://143.test/code-reviews?include_prior_findings=true&review_now=90d8a47d-d87e-4780-90af-040f5144685a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, codeReviewAvailablePriorFindingsURL(tt.services, sessionID), "the link should exist only where review requests are available")
		})
	}
}

func TestCodeReviewStatusCommentKeepsPriorFindingsLink(t *testing.T) {
	t.Parallel()

	const detail = "https://143.test/sessions/review"
	const priorFindings = "https://143.test/code-reviews?include_prior_findings=true&review_now=review"
	saved := models.WithCodeReviewCommentFooter("Review result.", models.CodeReviewCommentFooter{DetailURL: detail, PriorFindingsURL: priorFindings})
	decision := models.CodeReviewDecisionNeedsHumanReview

	body := codeReviewStatusCommentBody(models.CodeReviewSessionMetadata{Status: models.CodeReviewSessionStatusCompleted, Decision: &decision, FinalReviewBody: &saved}, nil, detail, "")

	require.Contains(t, body, "[Review with prior findings]("+priorFindings+")", "the status comment should keep the link saved on the final review")
}
