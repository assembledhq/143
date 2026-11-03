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

func TestBindSizeLimitOverrideToHead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *ReviewRequestContext
		expected *ReviewRequestContext
	}{
		{name: "no request context"},
		{name: "plain request stays unbound", input: &ReviewRequestContext{Body: "Look again."}, expected: &ReviewRequestContext{Body: "Look again."}},
		{
			name:     "new override binds to the observed head",
			input:    &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true},
			expected: &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "new-head"},
		},
		{
			name:     "retargeted override keeps its original head",
			input:    &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "confirmed-head"},
			expected: &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "confirmed-head"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, bindSizeLimitOverrideToHead(tt.input, "new-head"), "an override should stay bound to the head it was requested on")
		})
	}
}

func TestNormalizeReviewRequestContextKeepsSizeLimitOverrideHead(t *testing.T) {
	t.Parallel()

	input := &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: " head "}
	require.Equal(t, "head", normalizeReviewRequestContext(input).OverrideSizeLimitsHeadSHA, "the bound head should survive persistence and job dispatch")
}

func TestReviewRequestHashSeparatesSizeLimitOverride(t *testing.T) {
	t.Parallel()

	plain, err := reviewRequestHash([16]byte{1}, models.CodeReviewReviewNow, nil, false)
	require.NoError(t, err, "hash a plain request")
	override, err := reviewRequestHash([16]byte{1}, models.CodeReviewReviewNow, &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true}, false)
	require.NoError(t, err, "hash an override request")
	require.NotEqual(t, plain, override, "an override request should not dedupe into a plain request")
}
