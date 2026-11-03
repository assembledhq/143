package codereview

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestAssessmentRequestContextOverrideSizeLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		req      ScheduleRequestInput
		expected *ReviewRequestContext
	}{
		{name: "review now without the opt-in has no context", req: ScheduleRequestInput{Mode: models.CodeReviewReviewNow}},
		{
			name:     "review now opt-in does not need a reason",
			req:      ScheduleRequestInput{Mode: models.CodeReviewReviewNow, OverrideSizeLimits: true},
			expected: &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true},
		},
		{
			name:     "other modes ignore the opt-in",
			req:      ScheduleRequestInput{Mode: models.CodeReviewForceFresh, Reason: "Look again.", OverrideSizeLimits: true},
			expected: &ReviewRequestContext{Source: "ui", Body: "Look again."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, assessmentRequestContext(tt.req), "the request context should carry the opt-in only for review_now")
		})
	}
}

func TestNormalizeReviewRequestContextKeepsSizeLimitOverride(t *testing.T) {
	t.Parallel()

	require.Nil(t, normalizeReviewRequestContext(&ReviewRequestContext{Source: "ui", Body: "  "}), "an empty context without the opt-in should be dropped")
	require.Equal(t,
		&ReviewRequestContext{Source: "ui", OverrideSizeLimits: true},
		normalizeReviewRequestContext(&ReviewRequestContext{Source: " ui ", Body: "  ", OverrideSizeLimits: true}),
		"the opt-in should survive persistence and job dispatch")
}

func TestReviewRequestHashSeparatesSizeLimitOverride(t *testing.T) {
	t.Parallel()

	plain, err := reviewRequestHash([16]byte{1}, models.CodeReviewReviewNow, nil, false)
	require.NoError(t, err, "hash a plain request")
	override, err := reviewRequestHash([16]byte{1}, models.CodeReviewReviewNow, &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true}, false)
	require.NoError(t, err, "hash an override request")
	require.NotEqual(t, plain, override, "an override request should not dedupe into a plain request")
}
