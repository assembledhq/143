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

// legacySendDomainError retains a handler-facing sentinel while preserving
// the cause's original error shape for the transaction runner. A synthetic
// two-error unwrap would hide a plain retryable SQLSTATE behind a join.
type legacySendDomainError struct {
	domain error
	cause  error
}

func (e *legacySendDomainError) Error() string        { return e.domain.Error() + ": " + e.cause.Error() }
func (e *legacySendDomainError) Is(target error) bool { return target == e.domain }
func (e *legacySendDomainError) Unwrap() error        { return e.cause }

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
	s.logger.Warn().Err(err).
		Str("diagnostic_id", diagnostic.id.String()).
		Str("ingress_kind", ingress.Kind()).
		Msg("thread message command outcome unknown")
	return &CommandOutcomeUnknownError{Ingress: ingress, DiagnosticID: diagnostic.id, ClientMessageID: diagnostic.clientKey, Cause: err}
}

// runLegacySendUnit owns the entire legacy send. Every retry clears all staged
// values before acquiring fresh mode, parent and ordered-lane guards.
func (s *Service) runLegacySendUnit(ctx context.Context, orgID, sessionID, threadID uuid.UUID, work func(context.Context, pgx.Tx) (legacySendAttempt, error)) (legacySendAttempt, error) {
	var staged legacySendAttempt
	err := s.runSendUnit(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		staged = legacySendAttempt{}
		if err := db.RequireLegacyThreadSendScopeInTx(unitCtx, tx, orgID, sessionID, threadID); err != nil {
			return &sendGuardError{cause: err}
		}
		attempt, err := work(unitCtx, tx)
		if err != nil {
			return err
		}
		staged = attempt
		return nil
	})
	if err != nil {
		return legacySendAttempt{}, err
	}
	return staged, nil
}

func (s *Service) appendLegacyInboxInTx(ctx context.Context, tx pgx.Tx, input SendMessageInput, message *models.SessionMessage) (models.ThreadInboxEntry, error) {
	params, err := legacyInboxParams(input, message)
	if err != nil {
		return models.ThreadInboxEntry{}, err
	}
	return s.send.Legacy.AppendInboxInTx(ctx, tx, input.OrgID, params)
}
