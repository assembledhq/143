package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type codeReviewReviewerDispatchSender interface {
	SendMessage(context.Context, threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error)
}

type codeReviewReviewerDispatchMode uint8

const (
	codeReviewObserveRetainedOnly codeReviewReviewerDispatchMode = iota
	codeReviewAllowPendingDispatch
)

func codeReviewDispatchScope(job runCodeReviewPayload) db.ReviewerDispatchControllerScope {
	return db.ReviewerDispatchControllerScope{MetadataID: job.MetadataID, SessionID: job.SessionID, ReviewOutputKey: job.OutputKey}
}

func readCodeReviewReviewerOwner(ctx context.Context, transactions threadsvc.SendTransactions, job runCodeReviewPayload, key string) (db.CodeReviewReviewerOwner, error) {
	if !fullAssessmentTransactionsAvailable(transactions) {
		return db.CodeReviewReviewerOwner{}, fmt.Errorf("reviewer dispatch transaction capability unavailable")
	}
	var owner db.CodeReviewReviewerOwner
	err := transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		owner = db.CodeReviewReviewerOwner{}
		var err error
		owner, err = db.ReadCodeReviewReviewerOwnerInTx(unitCtx, job.OrgID, tx, codeReviewDispatchScope(job), key)
		return err
	})
	if err != nil {
		return db.CodeReviewReviewerOwner{}, err
	}
	return owner, nil
}

// The mode is fixed before fresh creation. This final metadata lock waits out
// an original in-flight preparation and adopts its exact key instead of making
// another owner. Every callback replay clears its provisional result. No lane,
// prompt, send, observer or notification is called while the unit is owned.
func prepareCodeReviewReviewerOwner(ctx context.Context, transactions threadsvc.SendTransactions, job runCodeReviewPayload, reviewerKey string, result *models.CodeReviewAgentResult, command threadsvc.CallerSendCommand, wake json.RawMessage, retained bool) (models.CodeReviewAgentResult, bool, error) {
	if !fullAssessmentTransactionsAvailable(transactions) || result == nil {
		return models.CodeReviewAgentResult{}, false, fmt.Errorf("reviewer preparation dependencies unavailable")
	}
	original := *result
	var committed models.CodeReviewAgentResult
	var retainedOwner bool
	err := transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		committed = models.CodeReviewAgentResult{}
		retainedOwner = false
		owner, err := db.ReadCodeReviewReviewerOwnerInTx(unitCtx, job.OrgID, tx, codeReviewDispatchScope(job), reviewerKey)
		if err != nil {
			return err
		}
		if owner.Found {
			committed = owner.Result
			retainedOwner = owner.Retained
			return nil
		}
		// Only a genuinely fresh owner uses the current candidate. Recovery
		// adopts the retained original before validating changed roster/prompt
		// inputs, and never needs to encode a replacement command.
		state, valid := parseCodeReviewReviewerStructuredResult(original.StructuredResult)
		if original.ID == uuid.Nil || original.OrgID != job.OrgID || original.SessionID != job.SessionID || original.Role != models.CodeReviewAgentRoleReviewer || !valid || state.ReviewerKey != reviewerKey {
			return db.ErrReviewerDispatchInput
		}
		if retained && command.OwnerID() != original.ID {
			return db.ErrReviewerDispatchInput
		}
		candidate := original
		if !retained {
			committed, err = db.CreateLegacyCodeReviewReviewerInTx(unitCtx, job.OrgID, tx, candidate)
			return err
		}
		// Never select legacy here if the chosen S1 mode becomes unavailable.
		encoded, err := command.Encode()
		if err != nil {
			return err
		}
		input := command.SendInput()
		prepared, err := db.PrepareCodeReviewDispatchInTx(unitCtx, job.OrgID, tx, db.CodeReviewDispatchPreparation{
			ResultID: original.ID, SessionID: job.SessionID, MetadataID: job.MetadataID, ThreadID: input.ThreadID,
			AgentProvider: original.AgentProvider, AgentModel: original.AgentModel, RawOutput: original.RawOutput, StructuredResult: original.StructuredResult,
			Version: 1, Key: command.ClientMessageID(), Digest: command.Digest(), ReviewOutputKey: job.OutputKey, Input: encoded, Wake: wake,
		})
		if err != nil {
			return err
		}
		committed = prepared.Result()
		retainedOwner = true
		return nil
	})
	if err != nil {
		return models.CodeReviewAgentResult{}, false, fmt.Errorf("prepare reviewer owner: %w", err)
	}
	return committed, retainedOwner, nil
}

func codeReviewReviewerSendSettlement(result *threadsvc.SendMessageResult, err error) (db.ReviewerDispatchSettlement, error) {
	// Unknown wins over wrapped/joined domain or contention causes. It never
	// returns to pending, even if an inner error would normally be retryable.
	if errors.Is(err, threadsvc.ErrCommandOutcomeUnknown) {
		return db.ReviewerDispatchUnknownAcknowledgement(), nil
	}
	if err == nil {
		if result == nil || result.Message == nil || result.Message.ID <= 0 {
			return db.ReviewerDispatchUnknownAcknowledgement(), nil
		}
		return db.ReviewerDispatchSendAcknowledged(result.Message.ID)
	}
	var contention *db.LifecycleContentionError
	switch {
	case errors.As(err, &contention):
		return db.ReviewerDispatchRetryAfterKnownAbort(db.ReviewerDispatchAbortContention)
	case errors.Is(err, threadsvc.ErrSendNormalizing):
		return db.ReviewerDispatchRetryAfterKnownAbort(db.ReviewerDispatchAbortNormalizing)
	case errors.Is(err, db.ErrThreadSendModeChanged):
		return db.ReviewerDispatchRetryAfterKnownAbort(db.ReviewerDispatchAbortModeChanged)
	case errors.Is(err, db.ErrMessageAdmissionReplayObservationNeeded):
		return db.ReviewerDispatchRetryAfterKnownAbort(db.ReviewerDispatchAbortReplayObservation)
	case errors.Is(err, threadsvc.ErrCallerCommandRequiresLegacy):
		return db.ReviewerDispatchKnownRefusal(db.ReviewerDispatchStampedActiveUnsupported)
	default:
		return db.ReviewerDispatchKnownRefusal(db.ReviewerDispatchSendRefused)
	}
}

func settleCodeReviewReviewerDispatch(ctx context.Context, transactions threadsvc.SendTransactions, job runCodeReviewPayload, e db.ReviewerDispatchExpectation, settlement db.ReviewerDispatchSettlement) error {
	if !fullAssessmentTransactionsAvailable(transactions) {
		return fmt.Errorf("reviewer settlement transaction capability unavailable")
	}
	return transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		// Validate the immutable original wake as well as the primitive's tuple;
		// another metadata owner in this session cannot lend authority.
		_, _, found, err := db.ReadCodeReviewReviewerDispatchInTx(unitCtx, job.OrgID, tx, codeReviewDispatchScope(job), e.OwnerRef().OwnerID)
		if err != nil {
			return err
		}
		if !found {
			return db.ErrReviewerDispatchInput
		}
		i, changed, err := db.SettleReviewerDispatchInTx(unitCtx, job.OrgID, tx, e, settlement)
		if err != nil || !changed {
			return err
		}
		switch i.State() {
		case db.ReviewerDispatchAccepted:
			_, err = db.UpdateCodeReviewReviewerContributionInTx(unitCtx, job.OrgID, tx, codeReviewDispatchScope(job), e.OwnerRef().OwnerID, models.CodeReviewAgentResultStatusRunning, nil, nil, time.Time{})
		case db.ReviewerDispatchRefused:
			raw := "Reviewer command was refused before acceptance."
			_, err = db.UpdateCodeReviewReviewerContributionInTx(unitCtx, job.OrgID, tx, codeReviewDispatchScope(job), e.OwnerRef().OwnerID, models.CodeReviewAgentResultStatusFailed, &raw, nil, time.Time{})
		}
		return err
	})
}

// dispatchRetainedCodeReviewReviewer uses the existing bounded capability,
// never a nested owner or a private retry loop. Read/claim/send/settle are
// separate units. A failed or unknown claim acknowledgement releases NO send.
// Missing receipts, terminal business status and expired attempts are never
// permission to retry an uncertain command.
func dispatchRetainedCodeReviewReviewer(ctx context.Context, transactions threadsvc.SendTransactions, sender codeReviewReviewerDispatchSender, job runCodeReviewPayload, resultID uuid.UUID, deadline time.Time) error {
	return advanceRetainedCodeReviewReviewer(ctx, transactions, sender, job, resultID, deadline, codeReviewAllowPendingDispatch)
}

// Entry reconciliation may observe uncertain commands before terminal/fast
// paths, but only the post-parent-gate phase may claim a pending command. An
// ordinary explicit user send can resume an ended session; autonomous review
// recovery must first preserve its existing cancellation/saved-work policy.
func advanceRetainedCodeReviewReviewer(ctx context.Context, transactions threadsvc.SendTransactions, sender codeReviewReviewerDispatchSender, job runCodeReviewPayload, resultID uuid.UUID, deadline time.Time, mode codeReviewReviewerDispatchMode) error {
	if ctx == nil || !fullAssessmentTransactionsAvailable(transactions) || deadline.IsZero() || (mode != codeReviewObserveRetainedOnly && mode != codeReviewAllowPendingDispatch) {
		return db.ErrReviewerDispatchInput
	}
	var owner db.RetainedCallerSend
	var intent db.ReviewerDispatchIntent
	var found bool
	err := transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		owner = db.RetainedCallerSend{}
		intent = db.ReviewerDispatchIntent{}
		found = false
		var err error
		owner, intent, found, err = db.ReadCodeReviewReviewerDispatchInTx(unitCtx, job.OrgID, tx, codeReviewDispatchScope(job), resultID)
		return err
	})
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	e := intent.Expectation()
	switch intent.State() {
	case db.ReviewerDispatchPending:
		if mode == codeReviewObserveRetainedOnly {
			if intent.MetadataEligible() {
				return nil
			}
			// Historical ineligible pending work can be cancelled, never
			// claimed. The ordered-lock reread must still prove ineligibility.
			return transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
				_, err := db.CancelIneligibleCodeReviewReviewerDispatchInTx(unitCtx, job.OrgID, tx, e)
				return err
			})
		}
		// Contribution closure runs in harvest, before timeout cancellation. At
		// or beyond the existing deadline no pending owner may start a command.
		if !time.Now().Before(deadline) && intent.MetadataEligible() && intent.ContributionStatus() == "queued" {
			return nil
		}
		command, err := threadsvc.DecodeCallerSendCommand(owner.Raw(), owner.Digest(), threadsvc.CallerSendOwnerScope{
			Purpose: threadsvc.CallerSendCodeReviewReviewerDispatch, OwnerID: resultID, OrgID: job.OrgID, SessionID: job.SessionID, ThreadID: owner.ThreadID(), ClientMessageID: owner.Key(),
		})
		if err != nil {
			return err
		}
		var claimed db.ReviewerDispatchIntent
		var changed bool
		attemptID := uuid.New()
		err = transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
			claimed = db.ReviewerDispatchIntent{}
			changed = false
			if _, _, found, err := db.ReadCodeReviewReviewerDispatchInTx(unitCtx, job.OrgID, tx, codeReviewDispatchScope(job), resultID); err != nil {
				return err
			} else if !found {
				return db.ErrReviewerDispatchInput
			}
			var err error
			claimed, changed, err = db.ClaimCodeReviewReviewerBeforeDeadlineInTx(unitCtx, job.OrgID, tx, e, attemptID, deadline)
			return err
		})
		if err != nil {
			return err
		}
		if !changed || claimed.State() != db.ReviewerDispatchAttempting {
			return nil
		}
		if sender == nil {
			return fmt.Errorf("reviewer sender unavailable after committed claim")
		}
		input := command.SendInput()
		proof, err := command.Proof()
		if err != nil {
			return err
		}
		input.CallerCommand = proof
		result, sendErr := sender.SendMessage(ctx, input)
		if sendErr != nil {
			zerolog.Ctx(ctx).Warn().Str("reviewer_result_id", resultID.String()).Str("error_type", fmt.Sprintf("%T", sendErr)).Msg("retained reviewer send requires settlement")
		}
		settlement, err := codeReviewReviewerSendSettlement(result, sendErr)
		if err != nil {
			return err
		}
		return settleCodeReviewReviewerDispatch(ctx, transactions, job, claimed.Expectation(), settlement)
	case db.ReviewerDispatchAttempting:
		if intent.Due() {
			return settleCodeReviewReviewerDispatch(ctx, transactions, job, e, db.ReviewerDispatchStaleAttempt())
		}
		return nil
	case db.ReviewerDispatchUnknown, db.ReviewerDispatchAttention:
		if !intent.Due() {
			return nil
		}
		observation, err := threadsvc.ObserveCallerSend(ctx, transactions, job.OrgID, e.OwnerRef())
		if err != nil {
			return err
		}
		switch observation.Reason() {
		case threadsvc.CallerSendCommandMalformed, threadsvc.CallerSendVersionUnsupported, threadsvc.CallerSendOwnerLegacy, threadsvc.CallerSendOwnerMissing:
			return db.ErrReviewerDispatchInput
		}
		observed, err := db.ReviewerDispatchExpectationFromRetained(job.MetadataID, observation.Owner())
		if err != nil {
			return err
		}
		if observed != e {
			return nil
		}
		settlement := db.ReviewerDispatchNegativeObservation()
		if messageID, accepted := observation.MessageID(); accepted {
			settlement, err = db.ReviewerDispatchObservedAccepted(observed, messageID, observation.StampCreatedAt(), false)
			if err != nil {
				return err
			}
		}
		return settleCodeReviewReviewerDispatch(ctx, transactions, job, e, settlement)
	default:
		return nil
	}
}

// Observe-only reconciliation precedes terminal shortcuts. Pending dispatch
// runs only after the old parent cancellation/ended-work gates, and before
// in-flight shortcuts. The boolean reports retained owners, not send authority.
// I9 owns subsequent wakes after this controller terminates.
func reconcileRetainedCodeReviewDispatches(ctx context.Context, stores *Stores, logger zerolog.Logger, job runCodeReviewPayload, mode codeReviewReviewerDispatchMode) (bool, error) {
	if stores == nil || !fullAssessmentTransactionsAvailable(stores.ThreadSendDependencies.Transactions) {
		return false, fmt.Errorf("reviewer reconciliation transaction capability unavailable")
	}
	transactions := stores.ThreadSendDependencies.Transactions
	var ids []uuid.UUID
	err := transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		ids = nil
		var err error
		ids, err = db.ListCodeReviewRetainedReviewerIDsInTx(unitCtx, job.OrgID, tx, codeReviewDispatchScope(job))
		return err
	})
	if err != nil {
		return false, err
	}
	if len(ids) == 0 {
		return false, nil
	}
	metadata, err := stores.CodeReviews.GetBySessionID(ctx, job.OrgID, job.SessionID)
	if err != nil {
		return false, err
	}
	if metadata.ID != job.MetadataID || metadata.ReviewOutputKey != job.OutputKey {
		return false, db.ErrReviewerDispatchInput
	}
	policy, err := stores.CodeReviews.GetPolicyByID(ctx, job.OrgID, job.PolicyID)
	if err != nil {
		return false, err
	}
	threads, err := newWorkerThreadService(stores, logger)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if err := advanceRetainedCodeReviewReviewer(ctx, transactions, threads, job, id, codeReviewReviewDeadline(policy.Config(), metadata), mode); err != nil {
			return false, err
		}
	}
	return true, nil
}

func updateCodeReviewReviewerOutcome(ctx context.Context, stores *Stores, job runCodeReviewPayload, resultID uuid.UUID, status models.CodeReviewAgentResultStatus, raw *string, structured json.RawMessage, deadline time.Time) (models.CodeReviewAgentResult, error) {
	if stores == nil || !fullAssessmentTransactionsAvailable(stores.ThreadSendDependencies.Transactions) {
		return models.CodeReviewAgentResult{}, fmt.Errorf("reviewer outcome transaction capability unavailable")
	}
	var result models.CodeReviewAgentResult
	err := stores.ThreadSendDependencies.Transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		result = models.CodeReviewAgentResult{}
		var err error
		result, err = db.UpdateCodeReviewReviewerContributionInTx(unitCtx, job.OrgID, tx, codeReviewDispatchScope(job), resultID, status, raw, structured, deadline)
		return err
	})
	if err != nil {
		return models.CodeReviewAgentResult{}, err
	}
	return result, nil
}

func closeCodeReviewReviewerDispatchDeadline(ctx context.Context, stores *Stores, job runCodeReviewPayload, resultID uuid.UUID, raw *string, structured json.RawMessage, deadline time.Time) error {
	if stores == nil || !fullAssessmentTransactionsAvailable(stores.ThreadSendDependencies.Transactions) {
		return fmt.Errorf("reviewer deadline transaction capability unavailable")
	}
	return stores.ThreadSendDependencies.Transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		_, err := db.CloseCodeReviewReviewerDispatchDeadlineInTx(unitCtx, job.OrgID, tx, codeReviewDispatchScope(job), resultID, raw, structured, deadline)
		return err
	})
}

// This read-only controller projection annotates contribution presentation and
// slot occupancy; the annotation is never send or CAS authority. All authority
// continues to come from the separately validated original retained owner.
func codeReviewReviewerDispatchStates(ctx context.Context, stores *Stores, job runCodeReviewPayload) (map[uuid.UUID]db.ReviewerDispatchState, map[uuid.UUID]bool, error) {
	states := make(map[uuid.UUID]db.ReviewerDispatchState)
	protected := make(map[uuid.UUID]bool)
	if stores == nil || !fullAssessmentTransactionsAvailable(stores.ThreadSendDependencies.Transactions) {
		return nil, nil, fmt.Errorf("reviewer state transaction capability unavailable")
	}
	err := stores.ThreadSendDependencies.Transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		states = make(map[uuid.UUID]db.ReviewerDispatchState)
		protected = make(map[uuid.UUID]bool)
		ids, err := db.ListCodeReviewRetainedReviewerIDsInTx(unitCtx, job.OrgID, tx, codeReviewDispatchScope(job))
		if err != nil {
			return err
		}
		for _, id := range ids {
			owner, i, found, err := db.ReadCodeReviewReviewerDispatchInTx(unitCtx, job.OrgID, tx, codeReviewDispatchScope(job), id)
			if err != nil {
				return err
			}
			if !found {
				return db.ErrReviewerDispatchInput
			}
			states[id] = i.State()
			if codeReviewDispatchUnresolved(i.State()) {
				protected[owner.ThreadID()] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return states, protected, nil
}

func codeReviewDispatchUnresolved(state db.ReviewerDispatchState) bool {
	return state == db.ReviewerDispatchPending || state == db.ReviewerDispatchAttempting || state == db.ReviewerDispatchUnknown || state == db.ReviewerDispatchAttention
}

func codeReviewReviewerRetainsDispatch(result models.CodeReviewAgentResult) bool {
	state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult)
	return ok && state.DispatchUnresolved
}

func annotateCodeReviewReviewerDispatches(results []models.CodeReviewAgentResult, states map[uuid.UUID]db.ReviewerDispatchState) []models.CodeReviewAgentResult {
	annotated := append([]models.CodeReviewAgentResult(nil), results...)
	for i := range annotated {
		if !codeReviewDispatchUnresolved(states[annotated[i].ID]) {
			continue
		}
		state, ok := parseCodeReviewReviewerStructuredResult(annotated[i].StructuredResult)
		if !ok {
			continue
		}
		state.DispatchUnresolved = true
		annotated[i].StructuredResult = marshalCodeReviewReviewerStructuredResult(state)
	}
	return annotated
}
