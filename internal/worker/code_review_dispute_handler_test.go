package worker

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestShouldPublishCodeReviewDisputeReply(t *testing.T) {
	t.Parallel()

	unsafeDirection := models.CodeReviewDisputeDirectionShouldNotHaveApproved
	safeDirection := models.CodeReviewDisputeDirectionShouldHaveApproved
	supersededBy := uuid.New()
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
		{
			// A superseded dispute shares the live one's reply comment, so
			// publishing would overwrite the current answer with a stale one.
			// reply_status is deliberately 'pending' here: BuildReply calls
			// CompleteReassessment immediately before this check, and that resets
			// reply_status, so a retirement recorded there would not survive.
			name: "stays silent for a dispute an edit superseded",
			dispute: models.CodeReviewDispute{
				Source:                models.CodeReviewDisputeSourceGitHubComment,
				ReplyStatus:           models.CodeReviewDisputeReplyPending,
				SupersededByDisputeID: &supersededBy,
			},
			expected: false,
		},
		{
			name: "stays silent for a superseded unsafe approval notification",
			dispute: models.CodeReviewDispute{
				Source: models.CodeReviewDisputeSourceAppUI, Direction: &unsafeDirection,
				ReplyStatus:           models.CodeReviewDisputeReplyPending,
				SupersededByDisputeID: &supersededBy,
			},
			expected: false,
		},
		{
			name: "stays silent when the store marks the reply not applicable",
			dispute: models.CodeReviewDispute{
				Source:      models.CodeReviewDisputeSourceGitHubComment,
				ReplyStatus: models.CodeReviewDisputeReplyNotApplicable,
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
