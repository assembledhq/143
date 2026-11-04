package codereview

import (
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAssessmentRequestContextOverrideSizeLimits(t *testing.T) {
	t.Parallel()

	sourceID := uuid.New()
	longReason := strings.Repeat("x", reviewRequestContextMaxRunes+1)
	tests := []struct {
		name     string
		req      ScheduleRequestInput
		expected *ReviewRequestContext
	}{
		{name: "review now without the opt-in has no context", req: ScheduleRequestInput{Mode: models.CodeReviewReviewNow}},
		{
			name:     "plain explicit reason stays intact before hashing",
			req:      ScheduleRequestInput{Mode: models.CodeReviewRecheck, Reason: longReason},
			expected: &ReviewRequestContext{Source: "ui", Body: longReason},
		},
		{
			name:     "review now opt-in does not need a reason",
			req:      ScheduleRequestInput{Mode: models.CodeReviewReviewNow, OverrideSizeLimits: true, OverrideReviewSessionID: &sourceID},
			expected: &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideReviewSessionID: &sourceID},
		},
		{
			name:     "review now opt-in preserves supplied request text",
			req:      ScheduleRequestInput{Mode: models.CodeReviewReviewNow, OverrideSizeLimits: true, OverrideReviewSessionID: &sourceID, RequestContext: &ReviewRequestContext{Source: "ui", Body: " Look again. "}},
			expected: &ReviewRequestContext{Source: "ui", Body: "Look again.", OverrideSizeLimits: true, OverrideReviewSessionID: &sourceID},
		},
		{
			name:     "other modes ignore the opt-in",
			req:      ScheduleRequestInput{Mode: models.CodeReviewForceFresh, Reason: "Look again.", OverrideSizeLimits: true, OverrideReviewSessionID: &sourceID},
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

func TestBindSizeLimitOverrideToComparison(t *testing.T) {
	t.Parallel()

	snapshot := ghservice.CodeReviewPullRequestSnapshot{HeadSHA: " head ", BaseSHA: " base ", BaseRef: " Main "}
	tests := []struct {
		name     string
		input    *ReviewRequestContext
		expected *ReviewRequestContext
	}{
		{name: "no request context"},
		{name: "plain request stays unbound", input: &ReviewRequestContext{Body: "Look again."}, expected: &ReviewRequestContext{Body: "Look again."}},
		{
			name:     "new override binds to the observed comparison",
			input:    &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true},
			expected: &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "head", OverrideSizeLimitsBaseSHA: "base", OverrideSizeLimitsBaseRef: "Main"},
		},
		{
			name:     "retargeted override keeps its original comparison",
			input:    &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "original-head", OverrideSizeLimitsBaseSHA: "original-base", OverrideSizeLimitsBaseRef: "original-ref"},
			expected: &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "original-head", OverrideSizeLimitsBaseSHA: "original-base", OverrideSizeLimitsBaseRef: "original-ref"},
		},
		{
			name:     "legacy override does not bind to the new base",
			input:    &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "head"},
			expected: &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "head"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, bindSizeLimitOverrideToComparison(tt.input, snapshot), "an override should stay bound to the comparison it was requested on")
		})
	}
}

func TestReviewRequestContextForComparison(t *testing.T) {
	t.Parallel()

	bound := ReviewRequestContext{Source: "ui", Body: "Look again.", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "head", OverrideSizeLimitsBaseSHA: "base", OverrideSizeLimitsBaseRef: "Main"}
	plain := ReviewRequestContext{Source: "ui", Body: "Look again."}
	tests := []struct {
		name     string
		head     string
		base     string
		baseRef  string
		expected ReviewRequestContext
	}{
		{name: "exact comparison preserves override", head: " HEAD ", base: " BASE ", baseRef: " Main ", expected: bound},
		{name: "new head", head: "new-head", base: "base", baseRef: "Main", expected: plain},
		{name: "new base SHA", head: "head", base: "new-base", baseRef: "Main", expected: plain},
		{name: "new base ref", head: "head", base: "base", baseRef: "main", expected: plain},
		{name: "unavailable base ref", head: "head", base: "base", expected: plain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, &tt.expected, reviewRequestContextForComparison(&bound, tt.head, tt.base, tt.baseRef), "dispatch must keep the reason and enforce the full override scope")
			require.True(t, bound.OverrideSizeLimits, "dispatch must not mutate the original intent")
		})
	}
}

func TestReviewRequestContextForComparisonDiscardsHeadOnlyOverride(t *testing.T) {
	t.Parallel()

	input := &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "head"}
	require.Nil(t, reviewRequestContextForComparison(input, "head", "base", "main"), "a legacy head-only override cannot waive the limits")
}

func TestNormalizeReviewRequestContextKeepsSizeLimitOverrideComparison(t *testing.T) {
	t.Parallel()

	input := &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: " head ", OverrideSizeLimitsBaseSHA: " base ", OverrideSizeLimitsBaseRef: " Main "}
	expected := &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideSizeLimitsHeadSHA: "head", OverrideSizeLimitsBaseSHA: "base", OverrideSizeLimitsBaseRef: "Main"}
	require.Equal(t, expected, normalizeReviewRequestContext(input), "the bound comparison should survive persistence and job dispatch")
}

func TestReviewRequestHashSeparatesSizeLimitOverride(t *testing.T) {
	t.Parallel()

	plain, err := reviewRequestHash([16]byte{1}, models.CodeReviewReviewNow, nil, false)
	require.NoError(t, err, "hash a plain request")
	firstSource := uuid.New()
	override, err := reviewRequestHash([16]byte{1}, models.CodeReviewReviewNow, &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideReviewSessionID: &firstSource}, false)
	require.NoError(t, err, "hash an override request")
	require.NotEqual(t, plain, override, "an override request should not dedupe into a plain request")
	secondSource := uuid.New()
	secondOverride, err := reviewRequestHash([16]byte{1}, models.CodeReviewReviewNow, &ReviewRequestContext{Source: "ui", OverrideSizeLimits: true, OverrideReviewSessionID: &secondSource}, false)
	require.NoError(t, err, "hash an override request from another source review")
	require.NotEqual(t, override, secondOverride, "overrides from different review links should be distinct request intents")
}
