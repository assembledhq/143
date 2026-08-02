package worker

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestShouldPublishCodeReviewDisputeReply(t *testing.T) {
	t.Parallel()

	unsafeDirection := models.CodeReviewDisputeDirectionShouldNotHaveApproved
	safeDirection := models.CodeReviewDisputeDirectionShouldHaveApproved
	tests := []struct {
		name     string
		dispute  models.CodeReviewDispute
		expected bool
	}{
		{
			name: "publishes GitHub intake acknowledgements",
			dispute: models.CodeReviewDispute{
				Source: models.CodeReviewDisputeSourceGitHubComment,
			},
			expected: true,
		},
		{
			name: "publishes in-app unsafe approval notifications",
			dispute: models.CodeReviewDispute{
				Source: models.CodeReviewDisputeSourceAppUI, Direction: &unsafeDirection,
			},
			expected: true,
		},
		{
			name: "keeps ordinary in-app reconsideration in the application",
			dispute: models.CodeReviewDispute{
				Source: models.CodeReviewDisputeSourceAppUI, Direction: &safeDirection,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := shouldPublishCodeReviewDisputeReply(tt.dispute)
			require.Equal(t, tt.expected, actual, "reply publication should follow the dispute source and safety direction")
		})
	}
}
