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
	CompletePhaseWithTransition(context.Context, uuid.UUID, uuid.UUID, models.ActivityPhaseStatus, models.ActivityPhaseBoundaryReason, time.Time) (models.SessionActivityPhase, bool, error)
	ListStrandedRunning(context.Context, uuid.UUID, time.Time, int) ([]models.SessionActivityPhase, error)
	ListStrandedRunningAcrossOrgs(context.Context, time.Time, int) ([]models.SessionActivityPhase, error)
	ReconcileStrandedPhase(context.Context, uuid.UUID, uuid.UUID, time.Time, time.Time) (bool, error)
	ReconcileAbandonedInboxBatchesAcrossOrgs(context.Context, time.Time, time.Time, int) ([]models.ThreadInboxDeliveryBatch, error)
	AcknowledgeInboxBatchWithTransition(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, *uuid.UUID, int64, time.Time) (models.ThreadInboxDeliveryBatch, bool, error)
	AbandonInboxBatch(context.Context, uuid.UUID, uuid.UUID, time.Time) (models.ThreadInboxDeliveryBatch, error)
}

type ActivityPhaseService struct {
	store  ActivityPhaseStore
	logger zerolog.Logger
	now    func() time.Time
}

func NewActivityPhaseService(store ActivityPhaseStore, logger zerolog.Logger) *ActivityPhaseService {
	return &ActivityPhaseService{store: store, logger: logger, now: time.Now}
}

func (s *ActivityPhaseService) StartPhase(ctx context.Context, orgID, sessionID, threadID uuid.UUID, turnNumber int, runtimeID, runtimeLeaseToken *uuid.UUID, trigger models.ActivityPhaseTrigger) (models.SessionActivityPhase, error) {
	phase, err := s.store.StartPhase(ctx, orgID, sessionID, threadID, turnNumber, runtimeID, runtimeLeaseToken, trigger, s.now())
	if err != nil {
		return models.SessionActivityPhase{}, fmt.Errorf("start activity phase: %w", err)
	}
	metrics.RecordActivityPhaseStarted(ctx, string(phase.TriggerKind))
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
	}
	return phase, nil
}

func (s *ActivityPhaseService) AcknowledgeInboxBatch(ctx context.Context, orgID, sessionID, threadID, runtimeID, leaseToken uuid.UUID, activePhaseID *uuid.UUID, sequenceEnd int64) (models.ThreadInboxDeliveryBatch, error) {
	batch, phaseTransitioned, err := s.store.AcknowledgeInboxBatchWithTransition(ctx, orgID, sessionID, threadID, runtimeID, leaseToken, activePhaseID, sequenceEnd, s.now())
	if err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("acknowledge inbox delivery batch: %w", err)
	}
	if phaseTransitioned {
		metrics.RecordActivityPhaseTerminal(ctx, string(models.ActivityPhaseStatusCompleted), string(models.ActivityPhaseBoundarySteered))
	}
	return batch, nil
}

func (s *ActivityPhaseService) AbandonInboxBatch(ctx context.Context, orgID, batchID uuid.UUID) (models.ThreadInboxDeliveryBatch, error) {
	batch, err := s.store.AbandonInboxBatch(ctx, orgID, batchID, s.now())
	if err != nil {
		return models.ThreadInboxDeliveryBatch{}, fmt.Errorf("abandon inbox delivery batch: %w", err)
	}
	return batch, nil
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
		transitioned, err := s.store.ReconcileStrandedPhase(ctx, phase.OrgID, phase.ID, leaseExpiredBefore, s.now())
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
		}
	}
	return reconciled, errors.Join(transitionErrors...)
}
