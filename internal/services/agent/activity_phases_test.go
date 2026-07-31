package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type activityPhaseStoreFake struct {
	phases         []models.SessionActivityPhase
	completeErr    error
	completed      []uuid.UUID
	reconciledOrgs []uuid.UUID
	reconcile      bool
	batches        []models.ThreadInboxDeliveryBatch
}

func (f *activityPhaseStoreFake) StartPhase(_ context.Context, orgID, sessionID, threadID uuid.UUID, turn int, runtimeID, _ *uuid.UUID, trigger models.ActivityPhaseTrigger, startedAt time.Time) (models.SessionActivityPhase, error) {
	return models.SessionActivityPhase{OrgID: orgID, SessionID: sessionID, ThreadID: threadID, TurnNumber: turn, RuntimeID: runtimeID, TriggerKind: trigger.Kind, StartedAt: startedAt}, nil
}

func (f *activityPhaseStoreFake) ListStrandedRunningAcrossOrgs(_ context.Context, _ time.Time, _ int) ([]models.SessionActivityPhase, error) {
	return f.phases, nil
}

func (f *activityPhaseStoreFake) ReconcileAbandonedInboxBatchesAcrossOrgs(_ context.Context, _, _ time.Time, _ int) ([]models.ThreadInboxDeliveryBatch, error) {
	return f.batches, nil
}

func (f *activityPhaseStoreFake) CompletePhaseWithTransition(_ context.Context, _ uuid.UUID, phaseID uuid.UUID, _ models.ActivityPhaseStatus, _ models.ActivityPhaseBoundaryReason, _ time.Time) (models.SessionActivityPhase, bool, error) {
	if f.completeErr != nil {
		return models.SessionActivityPhase{}, false, f.completeErr
	}
	f.completed = append(f.completed, phaseID)
	return models.SessionActivityPhase{ID: phaseID}, true, nil
}

func (f *activityPhaseStoreFake) ListStrandedRunning(_ context.Context, _ uuid.UUID, _ time.Time, _ int) ([]models.SessionActivityPhase, error) {
	return f.phases, nil
}

func (f *activityPhaseStoreFake) ReconcileStrandedPhase(_ context.Context, orgID uuid.UUID, phaseID uuid.UUID, _, _ time.Time) (bool, error) {
	if f.completeErr != nil {
		return false, f.completeErr
	}
	f.completed = append(f.completed, phaseID)
	f.reconciledOrgs = append(f.reconciledOrgs, orgID)
	return f.reconcile, nil
}

func (f *activityPhaseStoreFake) AcknowledgeInboxBatchWithTransition(_ context.Context, _, _, _, _, _ uuid.UUID, activePhaseID *uuid.UUID, _ int64, _ time.Time) (models.ThreadInboxDeliveryBatch, bool, error) {
	return models.ThreadInboxDeliveryBatch{}, activePhaseID != nil, nil
}

func (f *activityPhaseStoreFake) AbandonInboxBatch(_ context.Context, _, _ uuid.UUID, _ time.Time) (models.ThreadInboxDeliveryBatch, error) {
	return models.ThreadInboxDeliveryBatch{}, nil
}

func TestActivityPhaseServiceReconcileStrandedPhases(t *testing.T) {
	t.Parallel()

	first, second := uuid.New(), uuid.New()
	tests := []struct {
		name          string
		store         *activityPhaseStoreFake
		expectedCount int
		expectedIDs   []uuid.UUID
		wantErr       bool
	}{
		{
			name: "closes every stranded phase",
			store: &activityPhaseStoreFake{reconcile: true, phases: []models.SessionActivityPhase{
				{ID: first}, {ID: second},
			}},
			expectedCount: 2,
			expectedIDs:   []uuid.UUID{first, second},
		},
		{
			name:          "stops and reports transition failure",
			store:         &activityPhaseStoreFake{phases: []models.SessionActivityPhase{{ID: first}}, completeErr: errors.New("db unavailable")},
			expectedCount: 0,
			expectedIDs:   nil,
			wantErr:       true,
		},
		{
			name:          "skips a phase recovered after listing",
			store:         &activityPhaseStoreFake{phases: []models.SessionActivityPhase{{ID: first}}},
			expectedCount: 0,
			expectedIDs:   []uuid.UUID{first},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service := NewActivityPhaseService(tt.store, zerolog.Nop())
			count, err := service.ReconcileStrandedPhases(context.Background(), uuid.New(), time.Now(), 100)
			if tt.wantErr {
				require.Error(t, err, "reconciliation should propagate a terminal transition failure")
			} else {
				require.NoError(t, err, "reconciliation should close all listed stranded phases")
			}
			require.Equal(t, tt.expectedCount, count, "reconciliation should report the exact completed transition count")
			require.Equal(t, tt.expectedIDs, tt.store.completed, "reconciliation should transition the expected phases in order")
		})
	}
}

func TestActivityPhaseServiceReconcileAllStrandedPhasesPreservesTenantScope(t *testing.T) {
	t.Parallel()

	firstOrg, secondOrg := uuid.New(), uuid.New()
	first, second := uuid.New(), uuid.New()
	store := &activityPhaseStoreFake{
		reconcile: true,
		phases: []models.SessionActivityPhase{
			{ID: first, OrgID: firstOrg},
			{ID: second, OrgID: secondOrg},
		},
	}
	service := NewActivityPhaseService(store, zerolog.Nop())

	count, err := service.ReconcileAllStrandedPhases(context.Background(), time.Now(), 100)

	require.NoError(t, err, "cross-org reconciliation should close every bounded stranded phase")
	require.Equal(t, 2, count, "cross-org reconciliation should return the exact transition count")
	require.Equal(t, []uuid.UUID{firstOrg, secondOrg}, store.reconciledOrgs, "each reconciliation transition should use the phase's owning organization")
}

func TestActivityPhaseServiceReconcileAbandonedInboxBatches(t *testing.T) {
	t.Parallel()

	store := &activityPhaseStoreFake{
		batches: []models.ThreadInboxDeliveryBatch{
			{ID: uuid.New(), Status: models.InboxDeliveryBatchAbandoned},
			{ID: uuid.New(), Status: models.InboxDeliveryBatchAbandoned},
		},
	}
	service := NewActivityPhaseService(store, zerolog.Nop())

	count, err := service.ReconcileAbandonedInboxBatches(context.Background(), time.Now(), 100)

	require.NoError(t, err, "batch reconciliation should return durable abandoned batches without error")
	require.Equal(t, 2, count, "batch reconciliation should report the exact abandonment count")
}
