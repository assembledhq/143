package worker

import (
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAssessmentStatusPublicationUsesLatestDecision(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		assessment models.CodeReviewAssessmentStatus
		expected   models.CodeReviewSessionStatus
	}{
		{"approved recheck", models.CodeReviewAssessmentCompleted, models.CodeReviewSessionStatusCompleted},
		{"active recheck", models.CodeReviewAssessmentRunning, models.CodeReviewSessionStatusRunning},
		{"reserved recheck", models.CodeReviewAssessmentReserved, models.CodeReviewSessionStatusRunning},
		{"publishing recheck", models.CodeReviewAssessmentPublishing, models.CodeReviewSessionStatusRunning},
		{"failed recheck", models.CodeReviewAssessmentFailed, models.CodeReviewSessionStatusFailed},
		{"cancelled recheck", models.CodeReviewAssessmentCancelled, models.CodeReviewSessionStatusCancelled},
		{"superseded recheck", models.CodeReviewAssessmentSuperseded, models.CodeReviewSessionStatusCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			oldDecision := models.CodeReviewDecisionNeedsHumanReview
			newDecision := models.CodeReviewDecisionApproved
			oldBody, newBody := "Screenshot missing", "Updated screenshot satisfies visual requirement"
			session := uuid.New()
			legacy := models.CodeReviewSessionMetadata{SessionID: session, Status: models.CodeReviewSessionStatusCompleted, Decision: &oldDecision, FinalReviewBody: &oldBody, ReviewOutputKey: "old-output"}
			a := models.CodeReviewAssessment{Status: tt.assessment, SessionID: session, HeadSHA: "same-head", Decision: &newDecision, RenderedBody: &newBody, PublicationKey: "assessment-output", CreatedAt: time.Now()}
			expected := legacy
			expected.Status, expected.HeadSHA, expected.Decision, expected.FinalReviewBody, expected.ReviewOutputKey, expected.CreatedAt = tt.expected, a.HeadSHA, a.Decision, a.RenderedBody, a.PublicationKey, a.CreatedAt
			if tt.assessment == models.CodeReviewAssessmentFailed {
				message := "Evidence recheck failed. Open this assessment for details."
				expected.StatusMessage = &message
			}
			got := codeReviewAssessmentStatusMetadata(legacy, a)
			require.Equal(t, expected, got, "current rolling publication must use assessment result and marker")
			require.Equal(t, oldBody, *legacy.FinalReviewBody, "historical full body must remain unchanged")
		})
	}
}
