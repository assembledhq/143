package db

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ThreadPersistenceEffects is opaque post-commit notification data. Its zero
// value has no effects. It proves neither commit nor lifecycle authority: the
// transaction owner must discard it on failure, rollback or uncertain commit.
// It identifies a thread to reload, not an immutable snapshot of the write.
type ThreadPersistenceEffects struct {
	orgID          uuid.UUID
	threadID       uuid.UUID
	runtimeChanged bool
}

// UpdateStatusInTx persists the existing status mutation using only tx. It does
// not begin, settle, retry or publish a transaction. The caller must roll back
// the whole unit on error and publish effects only after confirmed commit.
// Existing status predicates are unchanged; this is not a lifecycle fence.
func (s *SessionThreadStore) UpdateStatusInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, threadID uuid.UUID, status models.ThreadStatus) (ThreadPersistenceEffects, error) {
	if nilLifecycleDependency(tx) {
		return ThreadPersistenceEffects{}, fmt.Errorf("persist thread status: transaction is required")
	}
	if err := s.updateStatusRow(ctx, tx, orgID, threadID, status); err != nil {
		return ThreadPersistenceEffects{}, err
	}
	return ThreadPersistenceEffects{orgID: orgID, threadID: threadID, runtimeChanged: true}, nil
}

// CompleteTurnInTx persists the legacy no-result turn mutation using only tx.
// Empty agentSessionID preserves the previous value, as in CompleteTurn. It
// neither settles the transaction nor publishes; the owner must discard its
// effects unless the entire unit commits successfully. It adds no authority.
func (s *SessionThreadStore) CompleteTurnInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, threadID uuid.UUID, turn int, agentSessionID string) (ThreadPersistenceEffects, error) {
	if nilLifecycleDependency(tx) {
		return ThreadPersistenceEffects{}, fmt.Errorf("persist thread turn: transaction is required")
	}
	if err := s.completeTurnRow(ctx, tx, orgID, threadID, turn, agentSessionID); err != nil {
		return ThreadPersistenceEffects{}, err
	}
	return ThreadPersistenceEffects{orgID: orgID, threadID: threadID, runtimeChanged: true}, nil
}

// UpdateResultInTx persists the existing result mutation using only tx, without
// transaction settlement or publication. Unlike the legacy wrapper, it rejects
// a nil result before writing. Errors require the owner to roll back the whole
// unit; returned effects may be published only after confirmed commit.
func (s *SessionThreadStore) UpdateResultInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, threadID uuid.UUID, status models.ThreadStatus, result *models.SessionResult) (ThreadPersistenceEffects, error) {
	if nilLifecycleDependency(tx) {
		return ThreadPersistenceEffects{}, fmt.Errorf("persist thread result: transaction is required")
	}
	if result == nil {
		return ThreadPersistenceEffects{}, fmt.Errorf("persist thread result: result is required")
	}
	if err := s.updateResultRow(ctx, tx, orgID, threadID, status, result); err != nil {
		return ThreadPersistenceEffects{}, err
	}
	return ThreadPersistenceEffects{orgID: orgID, threadID: threadID, runtimeChanged: true}, nil
}

// UpdateTurnCompleteInTx persists the existing result-bearing turn mutation
// using only tx, without beginning, settling, retrying or publishing. A nil
// result is rejected before writing; empty agentSessionID retains the legacy
// overwrite behavior. The owner alone commits or rolls back the whole unit.
func (s *SessionThreadStore) UpdateTurnCompleteInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, threadID uuid.UUID, turn int, result *models.SessionResult, agentSessionID string) (ThreadPersistenceEffects, error) {
	if nilLifecycleDependency(tx) {
		return ThreadPersistenceEffects{}, fmt.Errorf("persist thread turn result: transaction is required")
	}
	if result == nil {
		return ThreadPersistenceEffects{}, fmt.Errorf("persist thread turn result: result is required")
	}
	if err := s.updateTurnCompleteRow(ctx, tx, orgID, threadID, turn, result, agentSessionID); err != nil {
		return ThreadPersistenceEffects{}, err
	}
	return ThreadPersistenceEffects{orgID: orgID, threadID: threadID, runtimeChanged: true}, nil
}

// PublishCommittedThreadPersistenceEffects is explicitly called by the owner
// only after confirmed commit, on a store backed by a usable database handle,
// not the settled transaction. It preserves the legacy best-effort reload and
// notification behavior: the event describes the current visible thread,
// possibly a later write, not the earlier transaction's exact snapshot. It is
// neither durable/exactly-once delivery nor proof of lifecycle authority.
func (s *SessionThreadStore) PublishCommittedThreadPersistenceEffects(ctx context.Context, orgID uuid.UUID, effects ThreadPersistenceEffects) {
	if !effects.runtimeChanged {
		return
	}
	if effects.orgID != orgID {
		s.logger.Error().Str("org_id", orgID.String()).Str("effects_org_id", effects.orgID.String()).Msg("refusing to publish thread persistence effects for another organization")
		return
	}
	s.publishThreadRuntimeByID(ctx, orgID, effects.threadID)
}
