package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type orderedReviewLoopReconciler struct {
	stubWorkerReviewLoops
	order         []string
	err           error
	strandedErr   error
	strandedOrg   uuid.UUID
	strandedLimit int
	org           uuid.UUID
	limit         int
}

func (s *orderedReviewLoopReconciler) ReconcileRetainedStarts(_ context.Context, org uuid.UUID, limit int) (int, error) {
	s.order = append(s.order, "retained")
	s.org, s.limit = org, limit
	return 0, s.err
}
func (s *orderedReviewLoopReconciler) ReconcileStrandedPublicationLoops(_ context.Context, org uuid.UUID, _ time.Time, limit int) (int, error) {
	s.order = append(s.order, "stranded")
	s.strandedOrg, s.strandedLimit = org, limit
	return 0, s.strandedErr
}
func TestReviewLoopWakeRecoversRetainedBeforeStranded(t *testing.T) {
	t.Parallel()
	retainedFailure := errors.New("synthetic retained uncertainty")
	strandedFailure := errors.New("synthetic stranded failure")
	tests := []struct {
		name                     string
		retainedErr, strandedErr error
	}{
		{name: "recovery first"},
		{name: "retained uncertainty permits independently guarded stranded progress", retainedErr: retainedFailure},
		{name: "stranded failure remains visible", strandedErr: strandedFailure},
		{name: "both failures remain joined", retainedErr: retainedFailure, strandedErr: strandedFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			org := uuid.New()
			reviews := &orderedReviewLoopReconciler{err: tt.retainedErr, strandedErr: tt.strandedErr}
			err := newReconcileSessionReviewLoopHandler(&Services{ReviewLoops: reviews}, zerolog.Nop())(context.Background(), "reconcile_session_review_loop", json.RawMessage(`{"org_id":"`+org.String()+`","limit":7}`))
			if tt.retainedErr != nil {
				require.ErrorIs(t, err, tt.retainedErr, "retained uncertainty must remain visible after eligible stranded work runs")
			}
			if tt.strandedErr != nil {
				require.ErrorIs(t, err, tt.strandedErr, "stranded reconciliation failure must survive any simultaneous retained error")
			}
			if tt.retainedErr == nil && tt.strandedErr == nil {
				require.NoError(t, err, "successful retained and guarded stranded reconciliation must finish normally")
			}
			require.Equal(t, []string{"retained", "stranded"}, reviews.order, "retained recovery precedes but never suppresses the independently guarded stranded scan")
			require.Equal(t, [2]uuid.UUID{org, org}, [2]uuid.UUID{reviews.org, reviews.strandedOrg}, "both reconciliation paths must use the wake tenant")
			require.Equal(t, [2]int{7, 7}, [2]int{reviews.limit, reviews.strandedLimit}, "both reconciliation paths retain the bounded requested batch")
		})
	}
}
