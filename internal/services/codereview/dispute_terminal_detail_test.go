package codereview

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type terminalReplyDisputeStore struct {
	captureDisputeStore
	completed bool
	detail    string
}

func (s *terminalReplyDisputeStore) CompleteReassessment(_ context.Context, _, _, _ uuid.UUID, _ models.CodeReviewSessionStatus, _ *models.CodeReviewDecision, detail string) error {
	s.completed, s.detail = true, detail
	return nil
}

func TestDisputeService_BuildReplyTerminalDetail(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		status    models.CodeReviewSessionStatus
		message   string
		expected  string
		completed bool
	}{
		{name: "stale with cleared status message", status: models.CodeReviewSessionStatusStale, completed: true,
			expected: "The reassessment was superseded because the review target changed. The objection remains recorded."},
		{name: "stale does not retain an earlier wait", status: models.CodeReviewSessionStatusStale, message: "Waiting for sandbox capacity.", completed: true,
			expected: "The reassessment was superseded because the review target changed. The objection remains recorded."},
		{name: "failed preserves actionable status", status: models.CodeReviewSessionStatusFailed, message: "  The provider is unavailable. Try again later.  ", completed: true,
			expected: "The provider is unavailable. Try again later."},
		{name: "failed without a public status", status: models.CodeReviewSessionStatusFailed, completed: true,
			expected: "The reassessment stopped before it could complete. View the reassessment for details."},
		{name: "cancelled has an explanation", status: models.CodeReviewSessionStatusCancelled, completed: true,
			expected: "The reassessment was cancelled before it could complete."},
		{name: "missing decision has an explanation", status: models.CodeReviewSessionStatusCompleted, completed: true,
			expected: "The reassessment ended without a decision."},
		{name: "queued is not settled", status: models.CodeReviewSessionStatusQueued},
		{name: "running is not settled", status: models.CodeReviewSessionStatusRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, disputeID, ownerID := uuid.New(), uuid.New(), uuid.New()
			store := &terminalReplyDisputeStore{captureDisputeStore: captureDisputeStore{current: models.CodeReviewDispute{
				ID: disputeID, OrgID: orgID, ReassessmentSessionID: &ownerID, ReassessmentStatus: models.CodeReviewDisputeReassessmentRunning,
			}}}
			privateReason := "private diagnostic must remain on the owning review"
			metadata := models.CodeReviewSessionMetadata{SessionID: ownerID, OrgID: orgID, Status: tt.status,
				StatusMessage: &tt.message, FailureReason: &privateReason}
			service := NewDisputeService(store, disputeReviewStoreStub{metadata: metadata}, disputePullRequestStoreStub{}, nil, nil, "", zerolog.Nop())
			_, _, err := service.BuildReply(context.Background(), orgID, disputeID)
			require.NoError(t, err, "building a reply should reconcile only terminal owning review metadata")
			require.Equal(t, tt.completed, store.completed, "active review metadata must not settle the reassessment")
			require.Equal(t, tt.expected, store.detail, "terminal reconciliation should retain a safe, useful explanation without private diagnostics")
		})
	}
}
