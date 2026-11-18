package models

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAutomationReviewFeedbackMatchesNormalizedGitHubBody(t *testing.T) {
	t.Parallel()

	actionID := uuid.MustParse("15a23766-e52b-4031-bf40-87ed69da41b0")
	const path = "src/app.ts"
	request := AutomationActionRequest{
		Text: "Use a shared pattern.\rReview details below.",
		Comments: []AutomationActionReviewComment{
			{Path: path, Line: 9, Body: " \tUse the shared button.\r\nKeep the same keyboard behavior.\r    Example"},
		},
	}
	marker := "<!-- 143-automation-review:15a23766-e52b-4031-bf40-87ed69da41b0 -->"
	tests := []struct {
		name     string
		feedback AutomationActionReviewFeedback
		wantID   uuid.UUID
		matches  bool
	}{
		{
			name: "normalized summary",
			feedback: AutomationActionReviewFeedback{
				PRAuthor: "pr-author",
				Body:     "  @pr-author Use a shared pattern.\nReview details below.\r\n\r\n" + marker + "\r\n ",
			},
			wantID:  actionID,
			matches: true,
		},
		{
			name: "normalized inline body",
			feedback: AutomationActionReviewFeedback{
				Inline: true,
				Path:   path,
				Body:   "Use the shared button.\nKeep the same keyboard behavior.\n    Example\n\n" + marker + "\r",
			},
			wantID:  actionID,
			matches: true,
		},
		{
			name: "changed internal indentation",
			feedback: AutomationActionReviewFeedback{
				Inline: true,
				Path:   path,
				Body:   "Use the shared button.\nKeep the same keyboard behavior.\n  Example\n\n" + marker,
			},
			wantID: actionID,
		},
		{
			name: "marker must be at the end",
			feedback: AutomationActionReviewFeedback{
				Inline: true,
				Path:   path,
				Body:   "Use the shared button.\n\n" + marker + "\nAnother comment.",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.wantID, tt.feedback.ActionID(), "find a canonical marker at the end of the body")
			require.Equal(t, tt.matches, tt.feedback.Matches(request, actionID), "normalize GitHub whitespace while preserving differences inside the body")
		})
	}
}
