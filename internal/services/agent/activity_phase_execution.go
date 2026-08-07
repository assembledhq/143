package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// activityPhaseExecution owns the mutable phase identity for one provider
// invocation. Live steering may replace the active phase while log streaming
// is still running, so transcript writers resolve the identity at write time.
type activityPhaseExecution struct {
	mu sync.RWMutex

	service    *ActivityPhaseService
	logger     zerolog.Logger
	orgID      uuid.UUID
	sessionID  uuid.UUID
	threadID   uuid.UUID
	turnNumber int
	runtimeID  *uuid.UUID
	leaseToken *uuid.UUID
	phase      *models.SessionActivityPhase
}

type atomicActivityPhaseSessionStore interface {
	UpdateResultAndCompleteActivityPhase(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.SessionStatus, *models.SessionResult, models.ActivityPhaseStatus, models.ActivityPhaseBoundaryReason, time.Time) (models.SessionActivityPhase, models.Session, error)
	UpdateStatusAndCompleteActivityPhase(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.SessionStatus, models.ActivityPhaseStatus, models.ActivityPhaseBoundaryReason, time.Time) (models.SessionActivityPhase, models.Session, error)
	UpdateTurnCompleteAndCompleteActivityPhase(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int, *models.SessionResult, string, string, models.ActivityPhaseStatus, models.ActivityPhaseBoundaryReason, time.Time) (models.SessionActivityPhase, error)
	PublishCommittedSessionStatus(context.Context, uuid.UUID, models.Session)
}

func newActivityPhaseExecution(
	ctx context.Context,
	service *ActivityPhaseService,
	logger zerolog.Logger,
	orgID, sessionID, threadID uuid.UUID,
	turnNumber int,
	runtimeID, leaseToken *uuid.UUID,
	trigger models.ActivityPhaseTrigger,
) (*activityPhaseExecution, error) {
	if service == nil || threadID == uuid.Nil {
		return nil, nil
	}
	phase, err := service.StartPhase(ctx, orgID, sessionID, threadID, turnNumber, runtimeID, leaseToken, trigger)
	if err != nil {
		return nil, err
	}
	return &activityPhaseExecution{
		service: service, logger: logger,
		orgID: orgID, sessionID: sessionID, threadID: threadID,
		turnNumber: turnNumber, runtimeID: runtimeID, leaseToken: leaseToken,
		phase: &phase,
	}, nil
}

func (e *activityPhaseExecution) writeAssociation() (*uuid.UUID, *models.ActivityPhaseWriteGuard) {
	if e == nil {
		return nil, nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.phase == nil {
		return nil, nil
	}
	id := e.phase.ID
	if e.runtimeID == nil || e.leaseToken == nil {
		return &id, nil
	}
	return &id, &models.ActivityPhaseWriteGuard{
		RuntimeID:  *e.runtimeID,
		LeaseToken: *e.leaseToken,
	}
}

func (e *activityPhaseExecution) phaseID() *uuid.UUID {
	phaseID, _ := e.writeAssociation()
	return phaseID
}

func (e *activityPhaseExecution) complete(ctx context.Context, status models.ActivityPhaseStatus, reason models.ActivityPhaseBoundaryReason) error {
	if e == nil || e.service == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.phase == nil {
		return nil
	}
	phase, err := e.service.CompletePhase(ctx, e.orgID, e.phase.ID, status, reason, time.Now().UTC())
	if err != nil {
		return err
	}
	e.phase = nil
	e.logger.Info().
		Str("activity_phase_id", phase.ID.String()).
		Str("activity_phase_status", string(phase.Status)).
		Str("activity_phase_boundary_reason", string(phase.BoundaryReason)).
		Msg("activity phase completed")
	return nil
}

func (e *activityPhaseExecution) persistAssistantAndComplete(ctx context.Context, message *models.SessionMessage, reason models.ActivityPhaseBoundaryReason) error {
	if e == nil || e.service == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.phase == nil {
		return nil
	}
	phase, err := e.service.CreateAssistantMessageAndCompletePhase(ctx, e.orgID, e.phase.ID, message, reason)
	if err != nil {
		return err
	}
	e.phase = nil
	e.logTerminal(phase)
	return nil
}

func (e *activityPhaseExecution) persistHumanInputAndComplete(ctx context.Context, request *models.HumanInputRequest, reason models.ActivityPhaseBoundaryReason) error {
	if e == nil || e.service == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.phase == nil {
		return nil
	}
	phase, err := e.service.CreateHumanInputRequestAndCompletePhase(ctx, e.orgID, e.phase.ID, request, reason)
	if err != nil {
		return err
	}
	e.phase = nil
	e.logTerminal(phase)
	return nil
}

func (e *activityPhaseExecution) persistSessionResultAndComplete(ctx context.Context, sessions SessionStore, status models.SessionStatus, result *models.SessionResult, phaseStatus models.ActivityPhaseStatus, reason models.ActivityPhaseBoundaryReason) error {
	if e == nil {
		return fmt.Errorf("persist terminal session result: activity phase execution is nil")
	}
	if e.service == nil {
		return sessions.UpdateResult(ctx, e.orgID, e.sessionID, status, result)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.phase == nil {
		return sessions.UpdateResult(ctx, e.orgID, e.sessionID, status, result)
	}
	atomicStore, ok := sessions.(atomicActivityPhaseSessionStore)
	if !ok {
		if err := sessions.UpdateResult(ctx, e.orgID, e.sessionID, status, result); err != nil {
			return err
		}
		phase, err := e.service.CompletePhase(ctx, e.orgID, e.phase.ID, phaseStatus, reason, time.Now().UTC())
		if err != nil {
			return err
		}
		e.phase = nil
		e.logTerminal(phase)
		return nil
	}
	phase, session, err := atomicStore.UpdateResultAndCompleteActivityPhase(ctx, e.orgID, e.sessionID, e.phase.ID, status, result, phaseStatus, reason, time.Now().UTC())
	if err != nil {
		return err
	}
	e.phase = nil
	e.service.recordPersistedTerminal(ctx, phase)
	atomicStore.PublishCommittedSessionStatus(ctx, e.orgID, session)
	e.logTerminal(phase)
	return nil
}

func (e *activityPhaseExecution) persistSessionStatusAndComplete(ctx context.Context, sessions SessionStore, status models.SessionStatus, phaseStatus models.ActivityPhaseStatus, reason models.ActivityPhaseBoundaryReason) error {
	if e == nil {
		return fmt.Errorf("persist terminal session status: activity phase execution is nil")
	}
	if e.service == nil {
		return sessions.UpdateStatus(ctx, e.orgID, e.sessionID, status)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.phase == nil {
		return sessions.UpdateStatus(ctx, e.orgID, e.sessionID, status)
	}
	atomicStore, ok := sessions.(atomicActivityPhaseSessionStore)
	if !ok {
		if err := sessions.UpdateStatus(ctx, e.orgID, e.sessionID, status); err != nil {
			return err
		}
		phase, err := e.service.CompletePhase(ctx, e.orgID, e.phase.ID, phaseStatus, reason, time.Now().UTC())
		if err != nil {
			return err
		}
		e.phase = nil
		e.logTerminal(phase)
		return nil
	}
	phase, session, err := atomicStore.UpdateStatusAndCompleteActivityPhase(ctx, e.orgID, e.sessionID, e.phase.ID, status, phaseStatus, reason, time.Now().UTC())
	if err != nil {
		return err
	}
	e.phase = nil
	e.service.recordPersistedTerminal(ctx, phase)
	atomicStore.PublishCommittedSessionStatus(ctx, e.orgID, session)
	e.logTerminal(phase)
	return nil
}

func (e *activityPhaseExecution) persistTurnCompleteAndComplete(ctx context.Context, sessions SessionStore, turn int, result *models.SessionResult, agentSessionID, snapshotKey string, phaseStatus models.ActivityPhaseStatus, reason models.ActivityPhaseBoundaryReason) error {
	if e == nil {
		return fmt.Errorf("persist completed session turn: activity phase execution is nil")
	}
	if e.service == nil {
		return sessions.UpdateTurnComplete(ctx, e.orgID, e.sessionID, turn, result, agentSessionID, snapshotKey)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.phase == nil {
		return sessions.UpdateTurnComplete(ctx, e.orgID, e.sessionID, turn, result, agentSessionID, snapshotKey)
	}
	atomicStore, ok := sessions.(atomicActivityPhaseSessionStore)
	if !ok {
		if err := sessions.UpdateTurnComplete(ctx, e.orgID, e.sessionID, turn, result, agentSessionID, snapshotKey); err != nil {
			return err
		}
		phase, err := e.service.CompletePhase(ctx, e.orgID, e.phase.ID, phaseStatus, reason, time.Now().UTC())
		if err != nil {
			return err
		}
		e.phase = nil
		e.logTerminal(phase)
		return nil
	}
	phase, err := atomicStore.UpdateTurnCompleteAndCompleteActivityPhase(ctx, e.orgID, e.sessionID, e.phase.ID, turn, result, agentSessionID, snapshotKey, phaseStatus, reason, time.Now().UTC())
	if err != nil {
		return err
	}
	e.phase = nil
	e.service.recordPersistedTerminal(ctx, phase)
	e.logTerminal(phase)
	return nil
}

func (e *activityPhaseExecution) logTerminal(phase models.SessionActivityPhase) {
	e.logger.Info().
		Str("activity_phase_id", phase.ID.String()).
		Str("activity_phase_status", string(phase.Status)).
		Str("activity_phase_boundary_reason", string(phase.BoundaryReason)).
		Msg("activity phase completed")
}

func (e *activityPhaseExecution) acknowledgeAndResume(ctx context.Context, sequenceEnd int64) error {
	if e == nil || e.service == nil || e.runtimeID == nil || e.leaseToken == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var activePhaseID *uuid.UUID
	if e.phase != nil {
		id := e.phase.ID
		activePhaseID = &id
	}
	batch, err := e.service.AcknowledgeInboxBatch(
		ctx, e.orgID, e.sessionID, e.threadID, *e.runtimeID, *e.leaseToken,
		activePhaseID, sequenceEnd,
	)
	if err != nil {
		return err
	}
	phase, err := e.service.StartPhase(
		ctx, e.orgID, e.sessionID, e.threadID, e.turnNumber,
		e.runtimeID, e.leaseToken,
		models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInboxBatch, BatchID: &batch.ID},
	)
	if err != nil {
		if _, abandonErr := e.service.AbandonInboxBatch(ctx, e.orgID, batch.ID); abandonErr != nil {
			e.logger.Warn().Err(abandonErr).
				Str("thread_inbox_delivery_batch_id", batch.ID.String()).
				Msg("failed to abandon acknowledged inbox batch after phase resume failure")
		}
		return fmt.Errorf("start steered activity phase: %w", err)
	}
	e.phase = &phase
	e.logger.Info().
		Str("activity_phase_id", phase.ID.String()).
		Str("thread_inbox_delivery_batch_id", batch.ID.String()).
		Int64("sequence_start", batch.SequenceStart).
		Int64("sequence_end", batch.SequenceEnd).
		Msg("activity phase resumed from acknowledged inbox batch")
	return nil
}

func completeActivityPhaseDetached(execution *activityPhaseExecution, status models.ActivityPhaseStatus, reason models.ActivityPhaseBoundaryReason, logger zerolog.Logger) {
	if execution == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), threadRuntimeStateUpdateTimeout)
	defer cancel()
	if err := execution.complete(ctx, status, reason); err != nil {
		logger.Error().Err(err).
			Str("activity_phase_status", string(status)).
			Str("activity_phase_boundary_reason", string(reason)).
			Msg("failed to complete activity phase")
	}
}
