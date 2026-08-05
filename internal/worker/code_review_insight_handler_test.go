package worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type codeReviewInsightServiceStub struct {
	projectedSession *uuid.UUID
	reconciled       bool
	ranked           bool
}

func (s *codeReviewInsightServiceStub) ProjectDecision(_ context.Context, _, sessionID uuid.UUID) error {
	s.projectedSession = &sessionID
	return nil
}

func (s *codeReviewInsightServiceStub) ReconcileAndRank(context.Context, uuid.UUID) error {
	s.reconciled = true
	return nil
}

func (s *codeReviewInsightServiceStub) RankPendingBatches(context.Context, uuid.UUID, int, int) (int, bool, error) {
	s.ranked = true
	return 1, true, nil
}

func TestReconcileCodeReviewOutcomesHandler(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	sessionID := uuid.New()
	tests := []struct {
		name              string
		payload           codeReviewInsightJobPayload
		expectedSessionID *uuid.UUID
		expectedReconcile bool
		expectedRank      bool
	}{
		{name: "projects one terminal decision", payload: codeReviewInsightJobPayload{OrgID: orgID, SessionID: &sessionID}, expectedSessionID: &sessionID, expectedRank: true},
		{name: "reconciles an organization", payload: codeReviewInsightJobPayload{OrgID: orgID}, expectedReconcile: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service := &codeReviewInsightServiceStub{}
			payload, err := json.Marshal(tt.payload)
			require.NoError(t, err, "job payload should marshal")
			err = newReconcileCodeReviewOutcomesHandler(service)(context.Background(), models.JobTypeReconcileCodeReviewOutcomes, payload)
			require.NoError(t, err, "valid insight job should complete")
			require.Equal(t, tt.expectedSessionID, service.projectedSession, "handler should project the expected review session")
			require.Equal(t, tt.expectedReconcile, service.reconciled, "handler should run the expected organization reconciliation")
			require.Equal(t, tt.expectedRank, service.ranked, "single-session projection should refresh queue ranks")
		})
	}
}

func TestRankCodeReviewDisputeHandlerRejectsMissingOrg(t *testing.T) {
	t.Parallel()
	service := &codeReviewInsightServiceStub{}
	err := newRankCodeReviewDisputeHandler(service)(context.Background(), models.JobTypeRankCodeReviewDispute, json.RawMessage(`{}`))
	require.Error(t, err, "ranking without an organization should be rejected")
	require.False(t, service.ranked, "invalid ranking work should not reach the service")
}
