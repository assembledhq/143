package db

import (
	"context"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SessionPersistenceEffects is opaque post-commit workspace notification data.
// Its zero value has no effects. It neither proves a commit nor carries lifecycle
// authority; the transaction owner must discard it on failure, rollback or an
// uncertain commit and publish only after a confirmed commit. Session-status and
// activity-phase publication remain the existing callers' responsibilities.
type SessionPersistenceEffects struct {
	orgID             uuid.UUID
	sessionID         uuid.UUID
	workspaceRevision workspaceRevisionUpdate
	workspaceChanged  bool
}

// UpdateResultAndCompleteActivityPhaseInTx persists the existing combined result
// and activity-phase boundary using only the caller's concrete transaction.
// It does not begin, settle, retry or publish the transaction. The caller must
// roll back the whole unit on error and publish returned effects after commit.
// This persistence seam adds no ownership, generation or admission validation.
func (s *SessionStore) UpdateResultAndCompleteActivityPhaseInTx(
	ctx context.Context,
	orgID uuid.UUID,
	tx pgx.Tx,
	runID, phaseID uuid.UUID,
	status models.SessionStatus,
	result *models.SessionResult,
	phaseStatus models.ActivityPhaseStatus,
	reason models.ActivityPhaseBoundaryReason,
	completedAt time.Time,
) (models.SessionActivityPhase, models.Session, SessionPersistenceEffects, error) {
	if nilLifecycleDependency(tx) {
		return models.SessionActivityPhase{}, models.Session{}, SessionPersistenceEffects{}, fmt.Errorf("persist session result and activity phase: transaction is required")
	}
	if result == nil {
		result = &models.SessionResult{}
	}
	diffStats := computeDiffStatsForResult(result)
	session, err := s.updateResultRow(ctx, tx, orgID, runID, status, result, diffStats)
	if err != nil {
		return models.SessionActivityPhase{}, models.Session{}, SessionPersistenceEffects{}, err
	}
	effects := SessionPersistenceEffects{}
	if shouldPersistDiffSnapshot(result) {
		updated, err := s.writeDiffSnapshot(ctx, tx, orgID, runID, 0, result, diffStats)
		if err != nil {
			return models.SessionActivityPhase{}, models.Session{}, SessionPersistenceEffects{}, err
		}
		effects = SessionPersistenceEffects{orgID: orgID, sessionID: runID, workspaceRevision: updated, workspaceChanged: true}
	}
	phase, err := completeActivityPhaseInTransaction(ctx, tx, orgID, phaseID, phaseStatus, reason, completedAt)
	if err != nil {
		return models.SessionActivityPhase{}, models.Session{}, SessionPersistenceEffects{}, err
	}
	return phase, session, effects, nil
}

// UpdateStatusAndCompleteActivityPhaseInTx is the status-only persistence seam.
// It uses only tx, leaves transaction settlement to the caller and emits no
// events. An error requires rollback of the entire caller-owned transaction.
// Existing status and phase predicates are unchanged, not lifecycle authority.
func (s *SessionStore) UpdateStatusAndCompleteActivityPhaseInTx(
	ctx context.Context,
	orgID uuid.UUID,
	tx pgx.Tx,
	runID, phaseID uuid.UUID,
	status models.SessionStatus,
	phaseStatus models.ActivityPhaseStatus,
	reason models.ActivityPhaseBoundaryReason,
	completedAt time.Time,
) (models.SessionActivityPhase, models.Session, error) {
	if nilLifecycleDependency(tx) {
		return models.SessionActivityPhase{}, models.Session{}, fmt.Errorf("persist session status and activity phase: transaction is required")
	}
	session, err := s.updateStatusRow(ctx, tx, orgID, runID, status)
	if err != nil {
		return models.SessionActivityPhase{}, models.Session{}, err
	}
	phase, err := completeActivityPhaseInTransaction(ctx, tx, orgID, phaseID, phaseStatus, reason, completedAt)
	if err != nil {
		return models.SessionActivityPhase{}, models.Session{}, err
	}
	return phase, session, nil
}

// UpdateTurnCompleteAndCompleteActivityPhaseInTx persists the unchanged turn,
// result, diff and activity-phase writes in tx, without transaction settlement
// or publication. On error the caller must roll back the whole unit. Returned
// effects may be published only after its confirmed commit; this is not an
// ownership, generation, checkpoint-readiness or admission check.
func (s *SessionStore) UpdateTurnCompleteAndCompleteActivityPhaseInTx(
	ctx context.Context,
	orgID uuid.UUID,
	tx pgx.Tx,
	sessionID, phaseID uuid.UUID,
	turn int,
	result *models.SessionResult,
	agentSessionID, snapshotKey string,
	phaseStatus models.ActivityPhaseStatus,
	reason models.ActivityPhaseBoundaryReason,
	completedAt time.Time,
) (models.SessionActivityPhase, SessionPersistenceEffects, error) {
	if nilLifecycleDependency(tx) {
		return models.SessionActivityPhase{}, SessionPersistenceEffects{}, fmt.Errorf("persist session turn and activity phase: transaction is required")
	}
	if result == nil {
		result = &models.SessionResult{}
	}
	diffStats := computeDiffStatsForResult(result)
	if err := s.updateTurnCompleteRow(ctx, tx, orgID, sessionID, turn, result, agentSessionID, snapshotKey, diffStats); err != nil {
		return models.SessionActivityPhase{}, SessionPersistenceEffects{}, err
	}
	effects := SessionPersistenceEffects{}
	if shouldPersistDiffSnapshot(result) {
		updated, err := s.writeDiffSnapshot(ctx, tx, orgID, sessionID, turn, result, diffStats)
		if err != nil {
			return models.SessionActivityPhase{}, SessionPersistenceEffects{}, err
		}
		effects = SessionPersistenceEffects{orgID: orgID, sessionID: sessionID, workspaceRevision: updated, workspaceChanged: true}
	}
	phase, err := completeActivityPhaseInTransaction(ctx, tx, orgID, phaseID, phaseStatus, reason, completedAt)
	if err != nil {
		return models.SessionActivityPhase{}, SessionPersistenceEffects{}, err
	}
	return phase, effects, nil
}

// PublishCommittedSessionPersistenceEffects publishes optional workspace wakeups
// only when explicitly called by the transaction owner after confirmed commit.
// It does not read or settle a transaction, publish status/phase events, retry
// work, or provide durable/exactly-once delivery. Notification failures keep the
// existing best-effort logging behavior and never reject already committed work.
func (s *SessionStore) PublishCommittedSessionPersistenceEffects(ctx context.Context, orgID uuid.UUID, effects SessionPersistenceEffects) {
	if !effects.workspaceChanged {
		return
	}
	if effects.orgID != orgID {
		s.logger.Error().Str("org_id", orgID.String()).Str("effects_org_id", effects.orgID.String()).Msg("refusing to publish session persistence effects for another organization")
		return
	}
	s.publishWorkspaceGenerationChanged(ctx, orgID, effects.sessionID, effects.workspaceRevision.Revision, effects.workspaceRevision.UpdatedAt, "diff_snapshot")
}
