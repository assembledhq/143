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
	started        []models.SessionActivityPhase
	startTriggers  []models.ActivityPhaseTrigger
	completeErr    error
	completed      []uuid.UUID
	terminalStatus []models.ActivityPhaseStatus
	terminalReason []models.ActivityPhaseBoundaryReason
	reconciledOrgs []uuid.UUID
	reconcile      bool
	batches        []models.ThreadInboxDeliveryBatch
	humanRequests  []models.HumanInputRequest
}

type activityPhasePublisherFake struct {
	events []models.SessionStreamEvent
	order  *[]string
}

func (f *activityPhasePublisherFake) PublishEvent(_ context.Context, event models.SessionStreamEvent) error {
	f.events = append(f.events, event)
	if f.order != nil {
		*f.order = append(*f.order, "phase")
	}
	return nil
}

type atomicActivitySessionStoreFake struct {
	SessionStore
	order *[]string
}

func (f *atomicActivitySessionStoreFake) UpdateResultAndCompleteActivityPhase(_ context.Context, orgID, sessionID, phaseID uuid.UUID, _ models.SessionStatus, _ *models.SessionResult, phaseStatus models.ActivityPhaseStatus, reason models.ActivityPhaseBoundaryReason, completedAt time.Time) (models.SessionActivityPhase, models.Session, error) {
	*f.order = append(*f.order, "commit")
	return models.SessionActivityPhase{ID: phaseID, OrgID: orgID, SessionID: sessionID, Status: phaseStatus, BoundaryReason: reason, CompletedAt: &completedAt}, models.Session{ID: sessionID, OrgID: orgID}, nil
}

func (f *atomicActivitySessionStoreFake) UpdateStatusAndCompleteActivityPhase(_ context.Context, orgID, sessionID, phaseID uuid.UUID, _ models.SessionStatus, phaseStatus models.ActivityPhaseStatus, reason models.ActivityPhaseBoundaryReason, completedAt time.Time) (models.SessionActivityPhase, models.Session, error) {
	*f.order = append(*f.order, "commit")
	return models.SessionActivityPhase{ID: phaseID, OrgID: orgID, SessionID: sessionID, Status: phaseStatus, BoundaryReason: reason, CompletedAt: &completedAt}, models.Session{ID: sessionID, OrgID: orgID}, nil
}

func (f *atomicActivitySessionStoreFake) UpdateTurnCompleteAndCompleteActivityPhase(_ context.Context, orgID, sessionID, phaseID uuid.UUID, _ int, _ *models.SessionResult, _, _ string, phaseStatus models.ActivityPhaseStatus, reason models.ActivityPhaseBoundaryReason, completedAt time.Time) (models.SessionActivityPhase, error) {
	*f.order = append(*f.order, "commit")
	return models.SessionActivityPhase{ID: phaseID, OrgID: orgID, SessionID: sessionID, Status: phaseStatus, BoundaryReason: reason, CompletedAt: &completedAt}, nil
}

func (f *atomicActivitySessionStoreFake) PublishCommittedSessionStatus(_ context.Context, _ uuid.UUID, _ models.Session) {
	*f.order = append(*f.order, "session")
}

func (f *activityPhaseStoreFake) StartPhase(_ context.Context, orgID, sessionID, threadID uuid.UUID, turn int, runtimeID, _ *uuid.UUID, trigger models.ActivityPhaseTrigger, startedAt time.Time) (models.SessionActivityPhase, error) {
	phase := models.SessionActivityPhase{ID: uuid.New(), OrgID: orgID, SessionID: sessionID, ThreadID: threadID, TurnNumber: turn, RuntimeID: runtimeID, TriggerKind: trigger.Kind, StartedAt: startedAt}
	f.started = append(f.started, phase)
	f.startTriggers = append(f.startTriggers, trigger)
	return phase, nil
}

func (f *activityPhaseStoreFake) GetInboxDeliveryBatch(_ context.Context, orgID, batchID uuid.UUID) (models.ThreadInboxDeliveryBatch, error) {
	for _, batch := range f.batches {
		if batch.ID == batchID && batch.OrgID == orgID {
			return batch, nil
		}
	}
	return models.ThreadInboxDeliveryBatch{}, errors.New("batch not found")
}

func (f *activityPhaseStoreFake) ListStrandedRunningAcrossOrgs(_ context.Context, _ time.Time, _ int) ([]models.SessionActivityPhase, error) {
	return f.phases, nil
}

func (f *activityPhaseStoreFake) ReconcileAbandonedInboxBatchesAcrossOrgs(_ context.Context, _, _ time.Time, _ int) ([]models.ThreadInboxDeliveryBatch, error) {
	return f.batches, nil
}

func (f *activityPhaseStoreFake) CompletePhaseWithTransition(_ context.Context, _ uuid.UUID, phaseID uuid.UUID, status models.ActivityPhaseStatus, reason models.ActivityPhaseBoundaryReason, completedAt time.Time) (models.SessionActivityPhase, bool, error) {
	if f.completeErr != nil {
		return models.SessionActivityPhase{}, false, f.completeErr
	}
	f.completed = append(f.completed, phaseID)
	f.terminalStatus = append(f.terminalStatus, status)
	f.terminalReason = append(f.terminalReason, reason)
	phase := models.SessionActivityPhase{ID: phaseID, Status: status, BoundaryReason: reason, CompletedAt: &completedAt}
	for _, started := range f.started {
		if started.ID == phaseID {
			phase.OrgID = started.OrgID
			phase.SessionID = started.SessionID
			phase.ThreadID = started.ThreadID
			break
		}
	}
	return phase, true, nil
}

func TestActivityPhaseExecutionProviderIndependentTerminalMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status models.ActivityPhaseStatus
		reason models.ActivityPhaseBoundaryReason
	}{
		{name: "final response", status: models.ActivityPhaseStatusCompleted, reason: models.ActivityPhaseBoundaryFinalResponse},
		{name: "provider error", status: models.ActivityPhaseStatusFailed, reason: models.ActivityPhaseBoundaryError},
		{name: "user cancellation", status: models.ActivityPhaseStatusCancelled, reason: models.ActivityPhaseBoundaryCancelled},
		{name: "policy stop", status: models.ActivityPhaseStatusCancelled, reason: models.ActivityPhaseBoundaryStopped},
		{name: "maintenance interruption", status: models.ActivityPhaseStatusInterrupted, reason: models.ActivityPhaseBoundaryMaintenance},
		{name: "runtime loss", status: models.ActivityPhaseStatusInterrupted, reason: models.ActivityPhaseBoundaryRuntimeLost},
		{name: "capacity suspension", status: models.ActivityPhaseStatusInterrupted, reason: models.ActivityPhaseBoundaryCapacitySuspended},
		{name: "generic interruption", status: models.ActivityPhaseStatusInterrupted, reason: models.ActivityPhaseBoundaryInterrupted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &activityPhaseStoreFake{}
			execution, err := newActivityPhaseExecution(
				context.Background(), NewActivityPhaseService(store, zerolog.Nop()), zerolog.Nop(),
				uuid.New(), uuid.New(), uuid.New(), 1, nil, nil,
				models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial},
			)
			require.NoError(t, err, "provider-independent lifecycle harness should start the phase")

			err = execution.complete(context.Background(), tt.status, tt.reason)

			require.NoError(t, err, "provider-independent lifecycle harness should complete the phase")
			require.Equal(t, []models.ActivityPhaseStatus{tt.status}, store.terminalStatus, "terminal transition should preserve the exact status")
			require.Equal(t, []models.ActivityPhaseBoundaryReason{tt.reason}, store.terminalReason, "terminal transition should preserve the exact boundary reason")
			require.Nil(t, execution.phaseID(), "terminal transition should clear the transcript association")
		})
	}
}

func (f *activityPhaseStoreFake) CreateAssistantMessageAndCompletePhase(_ context.Context, _ uuid.UUID, phaseID uuid.UUID, _ *models.SessionMessage, reason models.ActivityPhaseBoundaryReason, _ time.Time) (models.SessionActivityPhase, error) {
	f.completed = append(f.completed, phaseID)
	return models.SessionActivityPhase{ID: phaseID, Status: models.ActivityPhaseStatusCompleted, BoundaryReason: reason}, nil
}

func (f *activityPhaseStoreFake) CreateHumanInputRequestAndCompletePhase(_ context.Context, _ uuid.UUID, phaseID uuid.UUID, request *models.HumanInputRequest, reason models.ActivityPhaseBoundaryReason, _ time.Time) (models.SessionActivityPhase, error) {
	if f.completeErr != nil {
		return models.SessionActivityPhase{}, f.completeErr
	}
	f.completed = append(f.completed, phaseID)
	if request != nil {
		f.humanRequests = append(f.humanRequests, *request)
	}
	return models.SessionActivityPhase{ID: phaseID, Status: models.ActivityPhaseStatusCompleted, BoundaryReason: reason}, nil
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

func (f *activityPhaseStoreFake) AcknowledgeInboxBatchWithTransition(_ context.Context, _, _, _, _, _ uuid.UUID, activePhaseID *uuid.UUID, _ int64, completedAt time.Time) (models.ThreadInboxDeliveryBatch, *models.SessionActivityPhase, error) {
	if activePhaseID == nil {
		return models.ThreadInboxDeliveryBatch{ID: uuid.New(), SequenceStart: 1, SequenceEnd: 1}, nil, nil
	}
	phase := models.SessionActivityPhase{ID: *activePhaseID, Status: models.ActivityPhaseStatusCompleted, BoundaryReason: models.ActivityPhaseBoundarySteered, CompletedAt: &completedAt}
	for _, started := range f.started {
		if started.ID == *activePhaseID {
			phase = started
			phase.Status = models.ActivityPhaseStatusCompleted
			phase.BoundaryReason = models.ActivityPhaseBoundarySteered
			phase.CompletedAt = &completedAt
			break
		}
	}
	return models.ThreadInboxDeliveryBatch{ID: uuid.New(), SequenceStart: 1, SequenceEnd: 1}, &phase, nil
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

func TestActivityPhaseExecutionCompletesAndClearsAssociation(t *testing.T) {
	t.Parallel()

	store := &activityPhaseStoreFake{}
	service := NewActivityPhaseService(store, zerolog.Nop())
	execution, err := newActivityPhaseExecution(
		context.Background(), service, zerolog.Nop(), uuid.New(), uuid.New(), uuid.New(), 2,
		nil, nil, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial},
	)
	require.NoError(t, err, "starting an execution phase should succeed")
	require.NotNil(t, execution.phaseID(), "a started execution should expose its phase association")

	err = execution.complete(context.Background(), models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse)
	require.NoError(t, err, "completing an execution phase should succeed")
	require.Nil(t, execution.phaseID(), "a terminal execution should no longer associate new transcript entries")
	require.Equal(t, []uuid.UUID{store.started[0].ID}, store.completed, "completion should transition the exact active phase")
}

func TestActivityPhaseExecutionWriteAssociationCarriesRuntimeLease(t *testing.T) {
	t.Parallel()

	runtimeID, leaseToken := uuid.New(), uuid.New()
	execution, err := newActivityPhaseExecution(
		context.Background(), NewActivityPhaseService(&activityPhaseStoreFake{}, zerolog.Nop()), zerolog.Nop(),
		uuid.New(), uuid.New(), uuid.New(), 2, &runtimeID, &leaseToken,
		models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial},
	)
	require.NoError(t, err, "starting a runtime-backed activity phase should succeed")

	phaseID, guard := execution.writeAssociation()
	require.NotNil(t, phaseID, "running activity phase should expose its durable identity")
	require.Equal(t, &models.ActivityPhaseWriteGuard{RuntimeID: runtimeID, LeaseToken: leaseToken}, guard, "transcript writes should carry the exact runtime lease that opened the phase")

	err = execution.complete(context.Background(), models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse)
	require.NoError(t, err, "completing the runtime-backed activity phase should succeed")
	phaseID, guard = execution.writeAssociation()
	require.Nil(t, phaseID, "terminal activity phase should stop associating transcript writes")
	require.Nil(t, guard, "terminal activity phase should stop exposing its runtime lease")
}

func TestActivityPhaseExecutionPublishesAtomicTerminalBoundaryInCommitOrder(t *testing.T) {
	t.Parallel()

	order := []string{}
	publisher := &activityPhasePublisherFake{order: &order}
	service := NewActivityPhaseService(&activityPhaseStoreFake{}, zerolog.Nop(), WithActivityPhaseEventPublisher(publisher))
	orgID, sessionID, threadID, phaseID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	execution := &activityPhaseExecution{
		service: service, logger: zerolog.Nop(), orgID: orgID, sessionID: sessionID, threadID: threadID,
		phase: &models.SessionActivityPhase{ID: phaseID, OrgID: orgID, SessionID: sessionID, ThreadID: threadID, Status: models.ActivityPhaseStatusRunning},
	}
	sessions := &atomicActivitySessionStoreFake{order: &order}

	err := execution.persistSessionResultAndComplete(
		context.Background(), sessions, models.SessionStatusFailed,
		&models.SessionResult{Error: strPtr("provider failed")},
		models.ActivityPhaseStatusFailed, models.ActivityPhaseBoundaryError,
	)

	require.NoError(t, err, "atomic terminal persistence should succeed")
	require.Equal(t, []string{"commit", "phase", "session"}, order, "phase lifecycle should publish after commit and before terminal session status")
	require.Nil(t, execution.phaseID(), "committed terminal boundary should clear the active phase association")
}

func TestActivityPhaseExecutionSteeringReplacesAssociation(t *testing.T) {
	t.Parallel()

	store := &activityPhaseStoreFake{}
	service := NewActivityPhaseService(store, zerolog.Nop())
	runtimeID, leaseToken := uuid.New(), uuid.New()
	execution, err := newActivityPhaseExecution(
		context.Background(), service, zerolog.Nop(), uuid.New(), uuid.New(), uuid.New(), 3,
		&runtimeID, &leaseToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerRecovery},
	)
	require.NoError(t, err, "starting the pre-steering execution phase should succeed")
	priorID := *execution.phaseID()

	err = execution.acknowledgeAndResume(context.Background(), 1)
	require.NoError(t, err, "acknowledging steering should start the replacement phase")
	require.NotEqual(t, priorID, *execution.phaseID(), "steering should replace the active phase association")
	require.Equal(t, []models.ActivityPhaseTriggerKind{models.ActivityPhaseTriggerRecovery, models.ActivityPhaseTriggerInboxBatch}, []models.ActivityPhaseTriggerKind{store.startTriggers[0].Kind, store.startTriggers[1].Kind}, "steering should resume from an explicit inbox batch trigger")
	require.NotNil(t, store.startTriggers[1].BatchID, "the resumed phase should retain its durable inbox batch identity")
}

func TestActivityPhaseServicePublishesLifecycleEventsAfterTransitions(t *testing.T) {
	t.Parallel()

	store := &activityPhaseStoreFake{}
	publisher := &activityPhasePublisherFake{}
	service := NewActivityPhaseService(store, zerolog.Nop(), WithActivityPhaseEventPublisher(publisher))
	orgID, sessionID, threadID := uuid.New(), uuid.New(), uuid.New()
	phase, err := service.StartPhase(context.Background(), orgID, sessionID, threadID, 1, nil, nil, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial})
	require.NoError(t, err, "phase start should succeed before publishing its event")
	_, err = service.CompletePhase(context.Background(), orgID, phase.ID, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, time.Now().UTC())
	require.NoError(t, err, "phase completion should succeed before publishing its event")

	require.Equal(t, []models.SessionStreamEventType{
		models.SessionStreamEventActivityPhaseStarted,
		models.SessionStreamEventActivityPhaseTerminal,
	}, []models.SessionStreamEventType{publisher.events[0].Type, publisher.events[1].Type}, "the service should publish start and terminal lifecycle events in commit order")
	require.NotEqual(t, uuid.Nil, publisher.events[0].ID, "the started event should have a stable non-empty event id")
	require.Equal(t, threadID, publisher.events[1].ThreadID, "the terminal event should retain the owning thread identity")
}
