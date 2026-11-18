package codereview

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAssessmentRequestContextReviewHistory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		request  ScheduleRequestInput
		expected *ReviewRequestContext
	}{
		{name: "plain request remains empty", request: ScheduleRequestInput{Mode: models.CodeReviewReviewNow}},
		{
			name:     "history opt-in needs no request text",
			request:  ScheduleRequestInput{Mode: models.CodeReviewReviewNow, UseReviewHistory: true},
			expected: &ReviewRequestContext{Source: "ui", UseReviewHistory: true},
		},
		{
			name:     "history option merges with an existing request",
			request:  ScheduleRequestInput{Mode: models.CodeReviewReviewNow, UseReviewHistory: true, RequestContext: &ReviewRequestContext{Source: "issue_comment", Body: " Check the parser. "}},
			expected: &ReviewRequestContext{Source: "issue_comment", Body: "Check the parser.", UseReviewHistory: true},
		},
		{
			name:     "persisted history intent survives normalization",
			request:  ScheduleRequestInput{Mode: models.CodeReviewReviewNow, RequestContext: &ReviewRequestContext{Source: "ui", UseReviewHistory: true}},
			expected: &ReviewRequestContext{Source: "ui", UseReviewHistory: true},
		},
		{
			name:     "blank supplied context still accepts the explicit option",
			request:  ScheduleRequestInput{Mode: models.CodeReviewReviewNow, UseReviewHistory: true, RequestContext: &ReviewRequestContext{}},
			expected: &ReviewRequestContext{Source: "ui", UseReviewHistory: true},
		},
		{
			name:    "a recheck cannot introduce the option",
			request: ScheduleRequestInput{Mode: models.CodeReviewRecheck, UseReviewHistory: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, assessmentRequestContext(tt.request), "request normalization should retain only the intended review options")
		})
	}
}

func TestReviewHistoryContextAcrossComparisonChanges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		context  *ReviewRequestContext
		expected *ReviewRequestContext
	}{
		{
			name:     "history alone survives new code",
			context:  &ReviewRequestContext{Source: "ui", UseReviewHistory: true},
			expected: &ReviewRequestContext{Source: "ui", UseReviewHistory: true},
		},
		{
			name:     "expiring a size override retains history",
			context:  &ReviewRequestContext{Source: "ui", UseReviewHistory: true, OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "old-head", OverrideSizeLimitsBaseSHA: "old-base", OverrideSizeLimitsBaseRef: "main"},
			expected: &ReviewRequestContext{Source: "ui", UseReviewHistory: true},
		},
		{
			name:    "plain expired override remains empty",
			context: &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "old-head", OverrideSizeLimitsBaseSHA: "old-base", OverrideSizeLimitsBaseRef: "main"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, reviewRequestContextForComparison(tt.context, "new-head", "new-base", "main"), "a new comparison should expire only its bound size override")
		})
	}
}

func TestReviewHistoryChangesRequestIdentity(t *testing.T) {
	t.Parallel()
	prID := uuid.New()
	plain, err := reviewRequestHash(prID, models.CodeReviewReviewNow, nil, false)
	require.NoError(t, err, "hash the ordinary review request")
	history, err := reviewRequestHash(prID, models.CodeReviewReviewNow, &ReviewRequestContext{Source: "ui", UseReviewHistory: true}, false)
	require.NoError(t, err, "hash the opted-in request")
	require.NotEqual(t, plain, history, "changing history access must not reuse the same request identity")
}

func TestRequestScheduledReviewRejectsHistoryOnOtherModes(t *testing.T) {
	t.Parallel()
	tests := []models.CodeReviewRequestMode{models.CodeReviewEnsureCurrent, models.CodeReviewRecheck, models.CodeReviewForceFresh}
	for _, mode := range tests {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			_, err := (&Service{}).RequestScheduledReview(context.Background(), ScheduleRequestInput{Mode: mode, RequestID: uuid.New(), UseReviewHistory: true})
			require.EqualError(t, err, "use_review_history requires review_now", "service callers must also obey the opt-in request contract")
		})
	}
}
