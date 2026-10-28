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

// ThreadClaimPersistenceEffects is opaque post-commit notification data for a
// successful claim's returned row. Its snapshot is independent of the returned
// thread. The owner must discard it on rollback, failure or uncertain commit;
// it is not proof that a transaction committed or that a lifecycle fence held.
type ThreadClaimPersistenceEffects struct {
	runtime        models.ThreadRuntimeEvent
	runtimeChanged bool
}

// ThreadPendingMessagePersistenceEffects identifies the legacy inbox event to
// reload after a pending-count increment commits. It is not a snapshot or commit
// receipt. Its zero value has no effects, and rollback requires discarding it.
type ThreadPendingMessagePersistenceEffects struct {
	orgID, threadID uuid.UUID
	inboxQueued     bool
}

// ClaimIdleForSessionInTx composes the legacy idle claim in tx without starting,
// settling, retrying, configuring or publishing. The caller owns fresh mode,
// parent and ordered-lane checks; this persistence split adds no lifecycle fence.
// Only publish the returned effects after the entire unit's confirmed commit.
// A no-match returns raw pgx.ErrNoRows without diagnosing cap occupancy here.
// An atomic owner may use its guarded supplied-transaction deciding reads.
func (s *SessionThreadStore) ClaimIdleForSessionInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, ThreadClaimPersistenceEffects, error) {
	return s.claimForSessionInTx(ctx, tx, claimForSessionArgs{
		orgID: orgID, sessionID: sessionID, threadID: threadID,
		maxRunning: maxRunning, mode: claimModeIdle,
		claimableStatuses: []string{string(models.ThreadStatusIdle)},
	})
}

// ClaimForResumeInSessionInTx composes the unchanged resumable-status claim and
// cap policy in tx, without transaction ownership or publication. It is not a
// lifecycle fence. The owner must discard both results on failure or rollback
// and publish effects only after the entire unit's confirmed commit.
// A no-match returns raw pgx.ErrNoRows. The atomic owner uses its guarded
// supplied-transaction deciding reads; a standalone best-effort diagnostic must
// wait for confirmed commit and cannot resolve an uncertain outcome.
func (s *SessionThreadStore) ClaimForResumeInSessionInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, ThreadClaimPersistenceEffects, error) {
	return s.claimForSessionInTx(ctx, tx, claimForSessionArgs{
		orgID: orgID, sessionID: sessionID, threadID: threadID,
		maxRunning: maxRunning, mode: claimModeResume,
		claimableStatuses: threadStatusStrings(models.ResumableThreadStatuses),
	})
}

func (s *SessionThreadStore) claimForSessionInTx(ctx context.Context, tx pgx.Tx, args claimForSessionArgs) (models.SessionThread, ThreadClaimPersistenceEffects, error) {
	if nilLifecycleDependency(tx) {
		return models.SessionThread{}, ThreadClaimPersistenceEffects{}, fmt.Errorf("persist thread claim: transaction is required")
	}
	thread, _, err := s.claimForSessionMutationRow(ctx, tx, args)
	if err != nil {
		return models.SessionThread{}, ThreadClaimPersistenceEffects{}, err
	}
	return thread, ThreadClaimPersistenceEffects{
		runtime: cloneThreadClaimRuntimeEvent(models.NewThreadRuntimeEvent(thread)), runtimeChanged: true,
	}, nil
}

// IncrementPendingMessagesInTx increments the legacy pending counter using only
// tx, without transaction settlement or publication. Its visibility and missing
// row behavior are unchanged. It adds no admission authority; the owner alone
// commits or rolls back and publishes effects after confirmed commit.
func (s *SessionThreadStore) IncrementPendingMessagesInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, threadID uuid.UUID) (ThreadPendingMessagePersistenceEffects, error) {
	if nilLifecycleDependency(tx) {
		return ThreadPendingMessagePersistenceEffects{}, fmt.Errorf("persist pending thread message: transaction is required")
	}
	if err := s.incrementPendingMessagesRow(ctx, tx, orgID, threadID); err != nil {
		return ThreadPendingMessagePersistenceEffects{}, err
	}
	return ThreadPendingMessagePersistenceEffects{orgID: orgID, threadID: threadID, inboxQueued: true}, nil
}

// PublishCommittedThreadClaimEffects publishes the claimed row's exact runtime
// snapshot, not a by-ID reload. Only the transaction owner may call this after
// confirmed commit. This best-effort event is not durable or exactly-once proof.
func (s *SessionThreadStore) PublishCommittedThreadClaimEffects(ctx context.Context, orgID uuid.UUID, effects ThreadClaimPersistenceEffects) {
	if !effects.runtimeChanged {
		return
	}
	if effects.runtime.OrgID != orgID {
		s.logger.Error().Str("org_id", orgID.String()).Str("effects_org_id", effects.runtime.OrgID.String()).Msg("refusing to publish thread claim effects for another organization")
		return
	}
	if s.streams == nil {
		return
	}
	event := models.SessionStreamEvent{
		Type: models.SessionStreamEventThreadRuntimeUpdated, SessionID: effects.runtime.SessionID,
		OrgID: orgID, Data: cloneThreadClaimRuntimeEvent(effects.runtime),
	}
	if err := s.streams.PublishEvent(ctx, event); err != nil {
		s.logger.Warn().Err(err).Str("thread_id", effects.runtime.ThreadID.String()).Msg("failed to publish thread runtime event")
	}
}

// PublishCommittedThreadPendingMessageEffects preserves the legacy current-row
// inbox reload after a pending increment. The owner calls it only after confirmed
// commit using a usable database handle, not the settled transaction. A later
// write may be reflected in this best-effort event, as in the legacy wrapper.
func (s *SessionThreadStore) PublishCommittedThreadPendingMessageEffects(ctx context.Context, orgID uuid.UUID, effects ThreadPendingMessagePersistenceEffects) {
	if !effects.inboxQueued {
		return
	}
	if effects.orgID != orgID {
		s.logger.Error().Str("org_id", orgID.String()).Str("effects_org_id", effects.orgID.String()).Msg("refusing to publish pending thread message effects for another organization")
		return
	}
	s.publishThreadInboxByID(ctx, orgID, effects.threadID, models.SessionStreamEventThreadInboxQueued)
}

func cloneThreadClaimRuntimeEvent(event models.ThreadRuntimeEvent) models.ThreadRuntimeEvent {
	event.AgentSessionID = cloneThreadClaimEventValue(event.AgentSessionID)
	event.LastActivityAt = cloneThreadClaimEventValue(event.LastActivityAt)
	event.StartedAt = cloneThreadClaimEventValue(event.StartedAt)
	event.CompletedAt = cloneThreadClaimEventValue(event.CompletedAt)
	event.FailureExplanation = cloneThreadClaimEventValue(event.FailureExplanation)
	event.FailureCategory = cloneThreadClaimEventValue(event.FailureCategory)
	return event
}

func cloneThreadClaimEventValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
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
