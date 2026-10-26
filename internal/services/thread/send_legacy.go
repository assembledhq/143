package thread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrCommandOutcomeUnknown = errors.New("COMMAND_OUTCOME_UNKNOWN")

// CommandOutcomeUnknownError preserves the original command identity. A failed
// receipt lookup cannot prove abort; only a positive exact receipt can resolve
// this result. It never authorizes compensation or another write attempt.
type CommandOutcomeUnknownError struct {
	Ingress         db.LifecycleIngress
	DiagnosticID    uuid.UUID
	ClientMessageID string
	Cause           error
}

func (e *CommandOutcomeUnknownError) Error() string { return ErrCommandOutcomeUnknown.Error() }
func (e *CommandOutcomeUnknownError) Unwrap() error { return e.Cause }
func (e *CommandOutcomeUnknownError) Is(target error) bool {
	return target == ErrCommandOutcomeUnknown
}

type sendWorkError struct{ cause error }

func (e *sendWorkError) Error() string { return e.cause.Error() }
func (e *sendWorkError) Unwrap() error { return e.cause }

type sendGuardError struct{ cause error }

func (e *sendGuardError) Error() string { return e.cause.Error() }
func (e *sendGuardError) Unwrap() error { return e.cause }
func sendMustStop(err error) bool {
	var guard *sendGuardError
	return errors.Is(err, ErrCommandOutcomeUnknown) || errors.As(err, &guard)
}
func sendClaimMustStop(err error) bool {
	if sendMustStop(err) {
		return true
	}
	_, confirmed := err.(*db.LifecycleContentionError)
	return confirmed
}

type sendIngressContextKey struct{}
type sendDiagnosticContextKey struct{}
type sendDiagnostic struct {
	id        uuid.UUID
	clientKey string
}

func legacyInboxParams(input SendMessageInput, msg *models.SessionMessage) (db.AppendThreadInboxEntryParams, error) {
	payload, err := json.Marshal(map[string]any{"message_id": msg.ID, "turn_number": msg.TurnNumber, "plan_mode": input.PlanMode, "content": msg.Content})
	if err != nil {
		return db.AppendThreadInboxEntryParams{}, fmt.Errorf("marshal inbox payload: %w", err)
	}
	return db.AppendThreadInboxEntryParams{SessionID: input.SessionID, ThreadID: input.ThreadID, MessageID: msg.ID, ClientMessageID: input.ClientMessageID, EntryType: models.ThreadInboxEntryTypeUserMessage, Payload: payload}, nil
}

// runSendUnit relies on the concrete runner's exact callback-error return after
// confirmed rollback. A join, wrapper, or failure after callback success is not
// that proof, including ErrTxCommitRollback. Keep Unwrap for SQLSTATE retries.
func (s *Service) runSendUnit(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	var lastFailure *sendWorkError
	entered := false
	err := s.send.Transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		entered, lastFailure = true, nil
		if err := work(unitCtx, tx); err != nil {
			lastFailure = &sendWorkError{cause: err}
			return lastFailure
		}
		return nil
	})
	if err == nil {
		return nil
	}
	if lastFailure != nil && err == lastFailure {
		return lastFailure.cause
	}
	if _, confirmed := err.(*db.LifecycleContentionError); confirmed || !entered {
		return err
	}
	ingress, _ := ctx.Value(sendIngressContextKey{}).(db.LifecycleIngress)
	diagnostic, _ := ctx.Value(sendDiagnosticContextKey{}).(sendDiagnostic)
	return &CommandOutcomeUnknownError{Ingress: ingress, DiagnosticID: diagnostic.id, ClientMessageID: diagnostic.clientKey, Cause: err}
}

func (s *Service) runLegacySendUnit(ctx context.Context, orgID, sessionID, threadID uuid.UUID, work func(context.Context, pgx.Tx) error) error {
	return s.runSendUnit(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		if err := db.RequireLegacyThreadSendScopeInTx(unitCtx, tx, orgID, sessionID, threadID); err != nil {
			return &sendGuardError{cause: err}
		}
		return work(unitCtx, tx)
	})
}

func (s *Service) claimLegacyThread(ctx context.Context, input SendMessageInput, resume bool) (models.SessionThread, error) {
	var row models.SessionThread
	var effects db.ThreadClaimPersistenceEffects
	var miss bool
	err := s.runLegacySendUnit(ctx, input.OrgID, input.SessionID, input.ThreadID, func(unitCtx context.Context, tx pgx.Tx) error {
		row, effects, miss = models.SessionThread{}, db.ThreadClaimPersistenceEffects{}, false
		var err error
		if resume {
			row, effects, err = s.send.Legacy.ClaimResumeThreadInTx(unitCtx, tx, input.OrgID, input.SessionID, input.ThreadID, models.MaxRunningThreadsPerSession)
		} else {
			row, effects, err = s.send.Legacy.ClaimIdleThreadInTx(unitCtx, tx, input.OrgID, input.SessionID, input.ThreadID, models.MaxRunningThreadsPerSession)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			miss = true
			return nil
		}
		return err
	})
	if err != nil {
		return models.SessionThread{}, err
	}
	if !miss {
		s.send.Effects.PublishThreadClaim(ctx, input.OrgID, effects)
		return row, nil
	}
	// The diagnostic deliberately runs after the original mutation unit closes.
	var limited bool
	if resume {
		limited, err = s.send.Legacy.IsAtResumeClaimLimit(ctx, input.OrgID, input.SessionID, input.ThreadID, models.MaxRunningThreadsPerSession)
	} else {
		limited, err = s.send.Legacy.IsAtIdleClaimLimit(ctx, input.OrgID, input.SessionID, input.ThreadID, models.MaxRunningThreadsPerSession)
	}
	if err != nil {
		s.logger.Warn().Err(err).Str("thread_id", input.ThreadID.String()).Msg("failed to diagnose legacy thread claim limit")
	} else if limited {
		return models.SessionThread{}, db.ErrThreadRunningLimitReached
	}
	return models.SessionThread{}, pgx.ErrNoRows
}

func (s *Service) claimLegacySession(ctx context.Context, orgID, sessionID, threadID uuid.UUID, resume bool) (models.Session, error) {
	var row models.Session
	var miss bool
	err := s.runLegacySendUnit(ctx, orgID, sessionID, threadID, func(unitCtx context.Context, tx pgx.Tx) error {
		row, miss = models.Session{}, false
		var err error
		if resume {
			row, err = s.send.Legacy.ClaimResumeSessionInTx(unitCtx, tx, orgID, sessionID)
		} else {
			row, err = s.send.Legacy.ClaimIdleSessionInTx(unitCtx, tx, orgID, sessionID)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			miss = true
			return nil
		}
		return err
	})
	if err != nil {
		return models.Session{}, err
	}
	if miss {
		return models.Session{}, pgx.ErrNoRows
	}
	return row, nil // Parent claims had no legacy notification.
}

func (s *Service) createLegacyMessage(ctx context.Context, input SendMessageInput, message *models.SessionMessage) error {
	original := *message
	err := s.runLegacySendUnit(ctx, input.OrgID, input.SessionID, input.ThreadID, func(unitCtx context.Context, tx pgx.Tx) error {
		*message = original // The established store API returns ID/time through this pointer.
		return s.send.Legacy.CreateMessageInTx(unitCtx, tx, input.OrgID, message)
	})
	if err != nil {
		*message = original
	}
	return err
}

func (s *Service) appendLegacyInboxInTx(ctx context.Context, tx pgx.Tx, input SendMessageInput, message *models.SessionMessage) (models.ThreadInboxEntry, error) {
	params, err := legacyInboxParams(input, message)
	if err != nil {
		return models.ThreadInboxEntry{}, err
	}
	return s.send.Legacy.AppendInboxInTx(ctx, tx, input.OrgID, params)
}

func (s *Service) incrementLegacyPending(ctx context.Context, input SendMessageInput) error {
	var effects db.ThreadPendingMessagePersistenceEffects
	err := s.runLegacySendUnit(ctx, input.OrgID, input.SessionID, input.ThreadID, func(unitCtx context.Context, tx pgx.Tx) error {
		effects = db.ThreadPendingMessagePersistenceEffects{}
		var err error
		effects, err = s.send.Legacy.IncrementPendingMessagesInTx(unitCtx, tx, input.OrgID, input.ThreadID)
		return err
	})
	if err == nil {
		s.send.Effects.PublishThreadPending(ctx, input.OrgID, effects)
	}
	return err
}

func (s *Service) enqueueLegacySend(ctx context.Context, input SendMessageInput, opts db.EnqueueOpts) error {
	var jobID uuid.UUID
	err := s.runLegacySendUnit(ctx, input.OrgID, input.SessionID, input.ThreadID, func(unitCtx context.Context, tx pgx.Tx) error {
		jobID = uuid.Nil
		var err error
		jobID, err = s.send.Legacy.EnqueueInTx(unitCtx, tx, input.OrgID, opts)
		return err
	})
	if err == nil {
		s.send.Effects.NotifyJob(ctx, jobID)
	}
	return err
}

func (s *Service) updateLegacyThreadStatus(ctx context.Context, orgID, sessionID, threadID uuid.UUID, status models.ThreadStatus) error {
	var effects db.ThreadPersistenceEffects
	err := s.runLegacySendUnit(ctx, orgID, sessionID, threadID, func(unitCtx context.Context, tx pgx.Tx) error {
		effects = db.ThreadPersistenceEffects{}
		var err error
		effects, err = s.send.Legacy.UpdateThreadStatusInTx(unitCtx, tx, orgID, threadID, status)
		return err
	})
	if err == nil {
		s.send.Effects.PublishThreadStatus(ctx, orgID, effects)
	}
	return err
}

func (s *Service) updateLegacySessionStatus(ctx context.Context, orgID, sessionID, threadID uuid.UUID, status models.SessionStatus) error {
	var row models.Session
	err := s.runLegacySendUnit(ctx, orgID, sessionID, threadID, func(unitCtx context.Context, tx pgx.Tx) error {
		row = models.Session{}
		var err error
		row, err = s.send.Legacy.UpdateSessionStatusInTx(unitCtx, tx, orgID, sessionID, status)
		return err
	})
	if err == nil {
		s.send.Effects.PublishSessionStatus(ctx, orgID, row)
	}
	return err
}

func (s *Service) enqueueLegacyLiveThreadInboxDelivery(ctx context.Context, input SendMessageInput) error {
	if s.inboxStore == nil {
		return nil
	}
	key := deliverThreadInboxJobType + ":" + input.ThreadID.String()
	var target *string
	if s.runtimeOwnerStore != nil {
		runtime, err := s.runtimeOwnerStore.GetActiveByThread(ctx, input.OrgID, input.ThreadID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("get live thread runtime owner: %w", err)
		}
		if err == nil && runtime.OwnerNodeID != "" {
			target = &runtime.OwnerNodeID
		}
	}
	return s.enqueueLegacySend(ctx, input, db.EnqueueOpts{Queue: deliverThreadInboxQueue, JobType: deliverThreadInboxJobType,
		Payload:  map[string]string{"session_id": input.SessionID.String(), "thread_id": input.ThreadID.String(), "org_id": input.OrgID.String()},
		Priority: 7, DedupeKey: &key, TargetNodeID: target})
}
