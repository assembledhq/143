package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// executionScopeMessages includes legacy threadless history only for the
// session's primary thread. Copies bind that history to its execution scope;
// durable message rows remain unchanged.
func executionScopeMessages(messages []models.SessionMessage, threadID, primaryThreadID *uuid.UUID) []models.SessionMessage {
	scoped := make([]models.SessionMessage, 0, len(messages))
	for _, message := range messages {
		if messageMatchesThreadScope(message, threadID) {
			scoped = append(scoped, message)
		} else if message.ThreadID == nil && threadID != nil && primaryThreadID != nil && *threadID == *primaryThreadID {
			message.ThreadID = threadID
			scoped = append(scoped, message)
		}
	}
	return scoped
}

func (o *Orchestrator) unappliedThreadMessages(ctx context.Context, session *models.Session, threadID *uuid.UUID, messages []models.SessionMessage) ([]models.SessionMessage, map[int64]bool, error) {
	if o.threadInbox == nil || threadID == nil || *threadID == uuid.Nil {
		return messages, nil, nil
	}
	ids := threadRuntimeSeedMessageIDs(messages)
	if len(ids) == 0 {
		return messages, nil, nil
	}
	appliedIDs, err := o.threadInbox.ListAppliedMessageIDs(ctx, session.OrgID, session.ID, *threadID, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("load admitted thread input: %w", err)
	}
	applied := make(map[int64]bool, len(appliedIDs))
	for _, id := range appliedIDs {
		applied[id] = true
	}
	pending := make([]models.SessionMessage, 0, len(messages))
	for _, message := range messages {
		if !applied[message.ID] {
			pending = append(pending, message)
		}
	}
	return pending, applied, nil
}

func validateContinuationPurpose(session *models.Session, opts *ContinueSessionOptions) error {
	if opts == nil {
		return nil
	}
	if opts.ExecutionPurpose == "" {
		if opts.RecoveryRuntimeID != nil {
			return fmt.Errorf("recovery runtime requires a recovery purpose")
		}
		return nil
	}
	if err := opts.ExecutionPurpose.Validate(); err != nil {
		return err
	}
	recovery := opts.ExecutionPurpose == models.SessionExecutionPurposeCheckpointRecovery
	if !recovery && (opts.RecoveryRuntimeID != nil || len(opts.RecoveryMessageIDs) > 0) {
		return fmt.Errorf("recovery runtime requires job recovery purpose")
	}
	if opts.ExecutionPurpose != models.SessionExecutionPurposeCheckpointRetry && !recovery {
		return nil
	}
	if opts.HumanInputRequestID != nil || opts.PRRepair != nil || opts.PRFeedback != nil || opts.AutomationTurn != nil || opts.CodeReviewTurn != nil || opts.ChangesetID != nil {
		return fmt.Errorf("task recovery cannot combine with another continuation mode")
	}
	if recovery {
		if len(opts.RecoveryMessageIDs) > 1000 {
			return fmt.Errorf("recovery task input exceeds preserved identity limit")
		}
		for _, id := range opts.RecoveryMessageIDs {
			if id <= 0 {
				return fmt.Errorf("recovery task input requires positive message identities")
			}
		}
		if opts.RecoveryRuntimeID == nil || *opts.RecoveryRuntimeID == uuid.Nil || opts.ThreadID == nil || opts.QueuedMessageID == nil {
			return fmt.Errorf("job recovery requires an exact runtime, thread, and original input")
		}
	} else {
		if opts.QueuedMessageID != nil {
			return fmt.Errorf("checkpoint retry cannot combine with new input")
		}
		if _, ok := latestDurableCheckpoint(session); !ok {
			return fmt.Errorf("checkpoint retry requires a durable checkpoint")
		}
	}
	return nil
}

// ErrContinuationInputAlreadyApplied is a successful job no-op: the requested
// input already belongs to an admitted provider phase and must not run again.
var ErrContinuationInputAlreadyApplied = errors.New("continuation input already applied")

// A final provider response alone cannot acknowledge the job when subsequent
// checkpoint/session/thread completion has not been durably confirmed.
var ErrContinuationRecoveryNeedsReconciliation = errors.New("provider turn completed but continuation lifecycle requires reconciliation")

// Invalid input uses the existing terminal setup-failure path; dependency
// failures remain retryable and must not consume a review thread's quorum.
var errInvalidContinuationInput = errors.New("invalid continuation input")

type continuationInput struct {
	messages       []models.SessionMessage
	scopedMessages []models.SessionMessage
	latest         *models.SessionMessage
	pending        []models.SessionMessage
	recoveryTask   []models.SessionMessage
	threadID       *uuid.UUID
	alreadyApplied bool
}

// resolveContinuationInput runs before allocating a runtime. Applied inputs
// remain history or explicit retry context, never a newly admitted seed.
func (o *Orchestrator) resolveContinuationInput(ctx context.Context, session *models.Session, opts *ContinueSessionOptions) (continuationInput, error) {
	var input continuationInput
	if opts != nil && opts.ExecutionPurpose == models.SessionExecutionPurposeCheckpointRecovery && o.threadInbox == nil {
		return input, fmt.Errorf("job recovery inbox receipt store unavailable")
	}
	messages, err := o.sessionMessages.ListBySession(ctx, session.OrgID, session.ID)
	if err != nil {
		return input, fmt.Errorf("fetch session messages: %w", err)
	}
	input.messages = messages
	scope := session.PrimaryThreadID
	if opts != nil && opts.ThreadID != nil {
		scope = opts.ThreadID
	}
	if scope != nil {
		messages = executionScopeMessages(messages, scope, session.PrimaryThreadID)
	}
	if opts != nil && opts.QueuedMessageID != nil {
		input.latest, err = queuedUserMessageInScope(messages, *opts.QueuedMessageID, scope)
		if err != nil {
			return input, fmt.Errorf("%w: select queued user message: %w", errInvalidContinuationInput, err)
		}
	} else if scope != nil {
		input.latest = latestUserMessageInScope(messages, scope)
	} else {
		input.latest = latestUserMessage(messages)
	}
	if input.latest == nil {
		return input, fmt.Errorf("%w: no user message found for continue_session", errInvalidContinuationInput)
	}
	input.threadID = input.latest.ThreadID
	input.scopedMessages = executionScopeMessages(input.messages, input.threadID, session.PrimaryThreadID)
	input.pending = unprocessedUserMessagesThrough(input.scopedMessages, input.threadID, input.latest.ID)
	var applied map[int64]bool
	input.pending, applied, err = o.unappliedThreadMessages(ctx, session, input.threadID, input.pending)
	if err != nil {
		return input, err
	}
	explicitRetry := opts != nil && (opts.ExecutionPurpose == models.SessionExecutionPurposeCheckpointRetry || opts.ExecutionPurpose == models.SessionExecutionPurposeCheckpointRecovery)
	if opts != nil && opts.ExecutionPurpose == models.SessionExecutionPurposeCheckpointRecovery && applied[input.latest.ID] {
		completed, settled, receiptErr := o.threadInbox.InspectContinuationRecovery(ctx, session.OrgID, session.ID, *input.threadID, *opts.RecoveryRuntimeID, input.latest.ID)
		if receiptErr != nil {
			return input, receiptErr
		}
		if completed {
			if !settled {
				return input, ErrContinuationRecoveryNeedsReconciliation
			}
			input.alreadyApplied = true
			return input, nil
		}
	}
	if explicitRetry && applied[input.latest.ID] {
		var requested []int64
		if opts.ExecutionPurpose == models.SessionExecutionPurposeCheckpointRecovery {
			requested = opts.RecoveryMessageIDs
		}
		ids, readErr := o.threadInbox.ListAppliedRecoveryMessageIDs(ctx, session.OrgID, session.ID, *input.threadID, input.latest.ID, requested)
		if readErr != nil {
			return input, readErr
		}
		wanted := make(map[int64]bool, len(ids))
		for _, id := range ids {
			wanted[id] = true
		}
		for _, message := range input.scopedMessages {
			if wanted[message.ID] {
				input.recoveryTask = append(input.recoveryTask, message)
			}
		}
		if len(input.recoveryTask) != len(wanted) {
			return input, fmt.Errorf("recovery task transcript lacks complete applied input proof")
		}
		sort.Slice(input.recoveryTask, func(i, j int) bool { return input.recoveryTask[i].ID < input.recoveryTask[j].ID })
	}
	systemInput := opts != nil && (opts.PRRepair != nil || opts.PRFeedback != nil || opts.AutomationTurn != nil || opts.CodeReviewTurn != nil || opts.HumanInputRequestID != nil)
	if !explicitRetry && !systemInput && o.threadInbox != nil && input.threadID != nil {
		input.alreadyApplied = applied[input.latest.ID] && (len(input.pending) == 0 || (opts != nil && opts.QueuedMessageID != nil))
		if !input.alreadyApplied {
			if len(input.pending) == 0 {
				return input, fmt.Errorf("no unadmitted user input found for continue_session")
			}
			latest := input.pending[len(input.pending)-1]
			input.latest = &latest
		}
	}
	return input, nil
}

type continuationAttemptRecorder interface {
	RecordContinuationAttemptWithLease(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, uuid.UUID, int64, []int64) (bool, error)
}

func (o *Orchestrator) recordContinuationAttempt(ctx context.Context, session *models.Session, control *threadRuntimeControl, message *models.SessionMessage, taskMessages []models.SessionMessage) error {
	if control == nil || message == nil || message.ID <= 0 {
		return nil
	}
	jobID, hasJob := jobctx.JobIDFromContext(ctx)
	token, hasToken := jobctx.LockTokenFromContext(ctx)
	if !hasJob || !hasToken {
		return nil
	}
	recorder, ok := o.jobs.(continuationAttemptRecorder)
	if !ok {
		return fmt.Errorf("owned continuation attempt recorder unavailable")
	}
	changed, err := recorder.RecordContinuationAttemptWithLease(ctx, session.OrgID, jobID, token, session.ID, control.runtime.ThreadID, control.runtime.ID, control.runtime.LeaseToken, message.ID, threadRuntimeSeedMessageIDs(taskMessages))
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("record continuation attempt: %w", ErrThreadRuntimeLeaseLost)
	}
	return nil
}
