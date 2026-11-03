package codereview

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestAssessmentRequestContextIncludePriorFindings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		req      ScheduleRequestInput
		expected *ReviewRequestContext
	}{
		{name: "review now without the opt-in has no context", req: ScheduleRequestInput{Mode: models.CodeReviewReviewNow}},
		{
			name:     "review now opt-in does not need a reason",
			req:      ScheduleRequestInput{Mode: models.CodeReviewReviewNow, IncludePriorFindings: true},
			expected: &ReviewRequestContext{Source: "ui", IncludePriorFindings: true},
		},
		{
			name:     "reason and opt-in travel together",
			req:      ScheduleRequestInput{Mode: models.CodeReviewReviewNow, Reason: " Look again. ", IncludePriorFindings: true},
			expected: &ReviewRequestContext{Source: "ui", Body: "Look again.", IncludePriorFindings: true},
		},
		{
			name:     "other modes ignore the opt-in",
			req:      ScheduleRequestInput{Mode: models.CodeReviewForceFresh, Reason: "Look again.", IncludePriorFindings: true},
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

func TestNormalizeReviewRequestContextKeepsPriorFindingsOptIn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *ReviewRequestContext
		expected *ReviewRequestContext
	}{
		{name: "nil stays nil"},
		{name: "empty body without the opt-in is dropped", input: &ReviewRequestContext{Source: "ui", Body: "  "}},
		{
			name:     "empty body with the opt-in is kept",
			input:    &ReviewRequestContext{Source: " ui ", Body: "  ", IncludePriorFindings: true},
			expected: &ReviewRequestContext{Source: "ui", IncludePriorFindings: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, normalizeReviewRequestContext(tt.input), "the opt-in should survive persistence and job dispatch")
		})
	}
}
