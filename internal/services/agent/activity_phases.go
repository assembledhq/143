package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
)

type ActivityPhaseStore interface {
	StartPhase(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int, *uuid.UUID, *uuid.UUID, models.ActivityPhaseTrigger, time.Time) (models.SessionActivityPhase, error)
	GetInboxDeliveryBatch(context.Context, uuid.UUID, uuid.UUID) (models.ThreadInboxDeliveryBatch, error)
	CompletePhaseWithTransition(context.Context, uuid.UUID, uuid.UUID, models.ActivityPhaseStatus, models.ActivityPhaseBoundaryReason, time.Time) (models.SessionActivityPhase, bool, error)
	CreateAssistantMessageAndCompletePhase(context.Context, uuid.UUID, uuid.UUID, *models.SessionMessage, models.ActivityPhaseBoundaryReason, time.Time) (models.SessionActivityPhase, error)
	CreateHumanInputRequestAndCompletePhase(context.Context, uuid.UUID, uuid.UUID, *models.HumanInputRequest, models.ActivityPhaseBoundaryReason, time.Time) (models.SessionActivityPhase, error)
	ListStrandedRunning(context.Context, uuid.UUID, time.Time, int) ([]models.SessionActivityPhase, error)
	ListStrandedRunningAcrossOrgs(context.Context, time.Time, int) ([]models.SessionActivityPhase, error)
	ReconcileStrandedPhase(context.Context, uuid.UUID, uuid.UUID, time.Time, time.Time) (bool, error)
	ReconcileAbandonedInboxBatchesAcrossOrgs(context.Context, time.Time, time.Time, int) ([]models.ThreadInboxDeliveryBatch, error)
	AcknowledgeInboxBatchWithTransition(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, *uuid.UUID, int64, time.Time) (models.ThreadInboxDeliveryBatch, *models.SessionActivityPhase, error)
	AbandonInboxBatch(context.Context, uuid.UUID, uuid.UUID, time.Time) (models.ThreadInboxDeliveryBatch, error)
}

type ActivityPhaseService struct {
	store     ActivityPhaseStore
	logger    zerolog.Logger
	now       func() time.Time
	publisher ActivityPhaseEventPublisher
}

type ActivityPhaseEventPublisher interface {
	PublishEvent(context.Context, models.SessionStreamEvent) error
}

type ActivityPhaseServiceOption func(*ActivityPhaseService)

func WithActivityPhaseEventPublisher(publisher ActivityPhaseEventPublisher) ActivityPhaseServiceOption {
	return func(service *ActivityPhaseService) { service.publisher = publisher }
}

func NewActivityPhaseService(store ActivityPhaseStore, logger zerolog.Logger, options ...ActivityPhaseServiceOption) *ActivityPhaseService {
	service := &ActivityPhaseService{store: store, logger: logger, now: time.Now}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *ActivityPhaseService) StartPhase(ctx context.Context, orgID, sessionID, threadID uuid.UUID, turnNumber int, runtimeID, runtimeLeaseToken *uuid.UUID, trigger models.ActivityPhaseTrigger) (models.SessionActivityPhase, error) {
	phase, err := s.store.StartPhase(ctx, orgID, sessionID, threadID, turnNumber, runtimeID, runtimeLeaseToken, trigger, s.now())
	if err != nil {
		return models.SessionActivityPhase{}, fmt.Errorf("start activity phase: %w", err)
	}
	metrics.RecordActivityPhaseStarted(ctx, string(phase.TriggerKind))
	s.publishPhase(ctx, models.SessionStreamEventActivityPhaseStarted, phase)
	if trigger.Kind == models.ActivityPhaseTriggerInboxBatch && trigger.BatchID != nil {
		batch, batchErr := s.store.GetInboxDeliveryBatch(ctx, orgID, *trigger.BatchID)
		if batchErr != nil {
			s.logger.Warn().Err(batchErr).
				Str("thread_inbox_delivery_batch_id", trigger.BatchID.String()).
				Msg("failed to load started inbox batch for lifecycle event")
		} else {
			s.publishBatch(ctx, models.SessionStreamEventInboxDeliveryStarted, batch)
		}
	}
	return phase, nil
}

func (s *ActivityPhaseService) ReconcileAllStrandedPhases(ctx context.Context, leaseExpiredBefore time.Time, limit int) (int, error) {
	phases, err := s.store.ListStrandedRunningAcrossOrgs(ctx, leaseExpiredBefore, limit)
	if err != nil {
		metrics.RecordActivityPhaseReconciliation(ctx, "list_failed", 0)
		return 0, fmt.Errorf("list stranded activity phases across orgs: %w", err)
	}
	if len(phases) > 0 {
		metrics.RecordStrandedActivityPhases(ctx, int64(len(phases)))
		s.logger.Warn().
			Int("stranded_phase_count", len(phases)).
			Time("lease_expired_before", leaseExpiredBefore).
			Msg("activity phase reconciliation found running phases without valid runtime leases")
	}
	reconciled, err := s.reconcilePhases(ctx, phases, leaseExpiredBefore)
	if err != nil {
		metrics.RecordActivityPhaseReconciliation(ctx, "transition_failed", int64(reconciled))
		return reconciled, err
	}
	metrics.RecordActivityPhaseReconciliation(ctx, "completed", int64(reconciled))
	return reconciled, nil
}

func (s *ActivityPhaseService) ReconcileAbandonedInboxBatches(ctx context.Context, leaseExpiredBefore time.Time, limit int) (int, error) {
	batches, err := s.store.ReconcileAbandonedInboxBatchesAcrossOrgs(ctx, leaseExpiredBefore, s.now(), limit)
	if err != nil {
		metrics.RecordInboxDeliveryBatchReconciliation(ctx, "failed", 0)
		return 0, fmt.Errorf("reconcile abandoned inbox delivery batches: %w", err)
	}
	if len(batches) > 0 {
		s.logger.Warn().
			Int("abandoned_inbox_batch_count", len(batches)).
			Time("lease_expired_before", leaseExpiredBefore).
			Msg("activity phase reconciliation abandoned acknowledged batches whose runtimes cannot resume")
		metrics.RecordInboxDeliveryBatchReconciliation(ctx, "abandoned", int64(len(batches)))
		for _, batch := range batches {
			s.publishBatch(ctx, models.SessionStreamEventInboxDeliveryAbandoned, batch)
		}
	} else {
		metrics.RecordInboxDeliveryBatchReconciliation(ctx, "completed", 0)
	}
	return len(batches), nil
}

func (s *ActivityPhaseService) CompletePhase(ctx context.Context, orgID, phaseID uuid.UUID, status models.ActivityPhaseStatus, reason models.ActivityPhaseBoundaryReason, completedAt time.Time) (models.SessionActivityPhase, error) {
	phase, transitioned, err := s.store.CompletePhaseWithTransition(ctx, orgID, phaseID, status, reason, completedAt)
	if err != nil {
		return models.SessionActivityPhase{}, fmt.Errorf("complete activity phase: %w", err)
	}
	if transitioned {
		metrics.RecordActivityPhaseTerminal(ctx, string(phase.Status), string(phase.BoundaryReason))
		s.publishPhase(ctx, models.SessionStreamEventActivityPhaseTerminal, phase)
	}
	return phase, nil
}

// recordPersistedTerminal publishes a phase already closed by a larger
// platform-owned database transaction.
func (s *ActivityPhaseService) recordPersistedTerminal(ctx context.Context, phase models.SessionActivityPhase) {
	metrics.RecordActivityPhaseTerminal(ctx, string(phase.Status), string(phase.BoundaryReason))
	s.publishPhase(ctx, models.SessionStreamEventActivityPhaseTerminal, phase)
}

func (s *ActivityPhaseService) CreateAssistantMessageAndCompletePhase(ctx context.Context, orgID, phaseID uuid.UUID, message *models.SessionMessage, reason models.ActivityPhaseBoundaryReason) (models.SessionActivityPhase, error) {
	phase, err := s.store.CreateAssistantMessageAndCompletePhase(ctx, orgID, phaseID, message, reason, s.now())
	if err != nil {
		return models.SessionActivityPhase{}, fmt.Errorf("persist assistant boundary and complete activity phase: %w", err)
	}
	metrics.RecordActivityPhaseTerminal(ctx, string(phase.Status), string(phase.BoundaryReason))
	s.publishPhase(ctx, models.SessionStreamEventActivityPhaseTerminal, phase)
	return phase, nil
}

func (s *ActivityPhaseService) CreateHumanInputRequestAndCompletePhase(ctx context.Context, orgID, phaseID uuid.UUID, request *models.HumanInputRequest, reason models.ActivityPhaseBoundaryReason) (models.SessionActivityPhase, error) {
	phase, err := s.store.CreateHumanInputRequestAndCompletePhase(ctx, orgID, phaseID, request, reason, s.now())
	if err != nil {
		return models.SessionActivityPhase{}, fmt.Errorf("persist human input boundary and complete activity phase: %w", err)
	}
	metrics.RecordActivityPhaseTerminal(ctx, string(phase.Status), string(phase.BoundaryReason))
	s.publishPhase(ctx, models.SessionStreamEventActivityPhaseTerminal, phase)
	return phase, nil
}

func (s *ActivityPhaseService) AcknowledgeInboxBatch(ctx context.Context, orgID, sessionID, threadID, runtimeID, leaseToken uuid.UUID, activePhaseID *uuid.UUID, sequenceEnd int64) (models.ThreadInboxDeliveryBatch, error) {
	batch, completedPhase, err := s.store.AcknowledgeInboxBatchWithTransition(ctx, orgID, sessionID, threadID, runtimeID, leaseToken, activePhaseID, sequenceEnd, s.now())
	if err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("acknowledge inbox delivery batch: %w", err)
	}
	if completedPhase != nil {
		metrics.RecordActivityPhaseTerminal(ctx, string(completedPhase.Status), string(completedPhase.BoundaryReason))
		s.publishPhase(ctx, models.SessionStreamEventActivityPhaseTerminal, *completedPhase)
	}
	s.publishBatch(ctx, models.SessionStreamEventInboxDeliveryAcknowledged, batch)
	return batch, nil
}

func (s *ActivityPhaseService) AbandonInboxBatch(ctx context.Context, orgID, batchID uuid.UUID) (models.ThreadInboxDeliveryBatch, error) {
	batch, err := s.store.AbandonInboxBatch(ctx, orgID, batchID, s.now())
	if err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("abandon inbox delivery batch: %w", err)
	}
	s.publishBatch(ctx, models.SessionStreamEventInboxDeliveryAbandoned, batch)
	return batch, nil
}

func (s *ActivityPhaseService) publishPhase(ctx context.Context, eventType models.SessionStreamEventType, phase models.SessionActivityPhase) {
	if s.publisher == nil {
		return
	}
	event := models.SessionStreamEvent{
		ID:   uuid.NewSHA1(uuid.NameSpaceOID, []byte(string(eventType)+":"+phase.ID.String()+":"+string(phase.Status))),
		Type: eventType, SessionID: phase.SessionID, OrgID: phase.OrgID, ThreadID: phase.ThreadID,
		EmittedAt: s.now().UTC(), Data: models.ActivityPhaseEvent{Phase: phase},
	}
	if err := s.publisher.PublishEvent(ctx, event); err != nil {
		s.logger.Warn().Err(err).Str("activity_phase_id", phase.ID.String()).Str("event_type", string(eventType)).Msg("failed to publish activity phase event")
	}
}

func (s *ActivityPhaseService) publishBatch(ctx context.Context, eventType models.SessionStreamEventType, batch models.ThreadInboxDeliveryBatch) {
	if s.publisher == nil {
		return
	}
	event := models.SessionStreamEvent{
		ID:   uuid.NewSHA1(uuid.NameSpaceOID, []byte(string(eventType)+":"+batch.ID.String()+":"+string(batch.Status))),
		Type: eventType, SessionID: batch.SessionID, OrgID: batch.OrgID, ThreadID: batch.ThreadID,
		EmittedAt: s.now().UTC(), Data: models.ThreadInboxDeliveryBatchEvent{Batch: batch},
	}
	if err := s.publisher.PublishEvent(ctx, event); err != nil {
		s.logger.Warn().Err(err).Str("thread_inbox_delivery_batch_id", batch.ID.String()).Str("event_type", string(eventType)).Msg("failed to publish inbox delivery batch event")
	}
}

// ReconcileStrandedPhases closes a bounded page of running phases whose
// runtime is missing, terminal, or outside the caller-supplied lease grace
// window. A later pass handles any remainder.
func (s *ActivityPhaseService) ReconcileStrandedPhases(ctx context.Context, orgID uuid.UUID, leaseExpiredBefore time.Time, limit int) (int, error) {
	phases, err := s.store.ListStrandedRunning(ctx, orgID, leaseExpiredBefore, limit)
	if err != nil {
		return 0, fmt.Errorf("list stranded activity phases: %w", err)
	}
	return s.reconcilePhases(ctx, phases, leaseExpiredBefore)
}

func (s *ActivityPhaseService) reconcilePhases(ctx context.Context, phases []models.SessionActivityPhase, leaseExpiredBefore time.Time) (int, error) {
	reconciled := 0
	var transitionErrors []error
	for _, phase := range phases {
		completedAt := s.now()
		transitioned, err := s.store.ReconcileStrandedPhase(ctx, phase.OrgID, phase.ID, leaseExpiredBefore, completedAt)
		if err != nil {
			s.logger.Error().Err(err).
				Str("org_id", phase.OrgID.String()).
				Str("session_id", phase.SessionID.String()).
				Str("thread_id", phase.ThreadID.String()).
				Str("activity_phase_id", phase.ID.String()).
				Msg("failed to reconcile stranded activity phase")
			transitionErrors = append(transitionErrors, fmt.Errorf("reconcile stranded activity phase %s: %w", phase.ID, err))
			continue
		}
		if transitioned {
			reconciled++
			metrics.RecordActivityPhaseTerminal(ctx, string(models.ActivityPhaseStatusInterrupted), string(models.ActivityPhaseBoundaryRuntimeLost))
			phase.Status = models.ActivityPhaseStatusInterrupted
			phase.BoundaryReason = models.ActivityPhaseBoundaryRuntimeLost
			phase.CompletedAt = &completedAt
			s.publishPhase(ctx, models.SessionStreamEventActivityPhaseTerminal, phase)
		}
	}
	return reconciled, errors.Join(transitionErrors...)
}
