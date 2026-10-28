package thread

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// legacySendAttempt contains provisional values from exactly one attempt. The
// transaction owner discards all of it unless the entire send commits.
type legacySendAttempt struct {
	result            *SendMessageResult
	claim             db.ThreadClaimPersistenceEffects
	publishClaim      bool
	pending           db.ThreadPendingMessagePersistenceEffects
	publishPending    bool
	jobID             uuid.UUID
	notifyJob         bool
	queued            bool
	missingQuestion   bool
	missingHumanInput bool
}

type legacyMessageWrites struct {
	resolved           []models.SessionReviewComment
	answered           *models.SessionQuestion
	answeredHumanInput *models.HumanInputRequest
	inbox              *models.ThreadInboxEntry
	pending            db.ThreadPendingMessagePersistenceEffects
	missingQuestion    bool
	missingHumanInput  bool
}

func (s *Service) sendLegacyMessage(ctx context.Context, input SendMessageInput, holder *uuid.UUID) (*SendMessageResult, error) {
	if err := s.rejectIfCodeReviewOwned(ctx, input.OrgID, input.SessionID); err != nil {
		return nil, err
	}
	if err := s.sessionStore.RejectIfAutomationOwned(ctx, input.OrgID, input.SessionID); err != nil {
		return nil, err
	}
	if len(input.ResolveReviewCommentIDs) > 0 && (s.txStarter == nil || s.reviewCommentStore == nil) {
		return nil, ErrReviewCommentsNotConfigured
	}

	attempt, err := s.runLegacySendUnit(ctx, input.OrgID, input.SessionID, input.ThreadID, func(unitCtx context.Context, tx pgx.Tx) (legacySendAttempt, error) {
		return s.writeLegacySendInTx(unitCtx, tx, input, holder)
	})
	if err != nil {
		return nil, err
	}
	if attempt.publishClaim {
		s.send.Effects.PublishThreadClaim(ctx, input.OrgID, attempt.claim)
	}
	if attempt.publishPending {
		s.send.Effects.PublishThreadPending(ctx, input.OrgID, attempt.pending)
	}
	if attempt.notifyJob {
		s.send.Effects.NotifyJob(ctx, attempt.jobID)
	}
	if attempt.missingQuestion {
		s.logger.Warn().Str("session_id", input.SessionID.String()).Msg("awaiting_input session resumed without a pending question to answer")
	}
	if attempt.missingHumanInput {
		s.logger.Warn().Str("session_id", input.SessionID.String()).Str("thread_id", input.ThreadID.String()).Msg("awaiting_input thread resumed without a pending free-text human input request to answer")
	}
	if attempt.queued {
		s.recoverLostOwnerAfterQueuedSend(ctx, input.OrgID, input.SessionID, input.ThreadID)
	}
	return attempt.result, nil
}

func (s *Service) writeLegacySendInTx(ctx context.Context, tx pgx.Tx, input SendMessageInput, holder *uuid.UUID) (legacySendAttempt, error) {
	if err := s.validateLegacyCallerStamp(input); err != nil {
		return legacySendAttempt{}, err
	}
	if err := s.send.Legacy.RejectIfOwnedInTx(ctx, tx, input.OrgID, input.SessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return legacySendAttempt{}, &legacySendDomainError{domain: ErrSessionNotFound, cause: err}
		}
		return legacySendAttempt{}, err
	}
	if existing, found, err := s.findExistingLegacyMessageInTx(ctx, tx, input); err != nil {
		return legacySendAttempt{}, err
	} else if found {
		if err := verifyLegacyCallerStampReplayInTx(ctx, tx, input, &existing); err != nil {
			return legacySendAttempt{}, err
		}
		return legacySendAttempt{result: &SendMessageResult{Message: &existing}}, nil
	}
	if err := s.validateLegacyInboxAdmissionInTx(ctx, tx, input); err != nil {
		return legacySendAttempt{}, err
	}
	thread, preClaimStatus, outcome, claim, err := s.claimLegacyThreadInTx(ctx, tx, input)
	if err != nil {
		return legacySendAttempt{}, err
	}
	queued := outcome != threadClaimClaimed
	capacityQueue := outcome == threadClaimQueueLimitReached
	var claimedSession models.Session
	var preClaimSessionStatus models.SessionStatus
	if !queued {
		claimedSession, preClaimSessionStatus, err = s.claimLegacySessionInTx(ctx, tx, input)
		if err != nil {
			return legacySendAttempt{}, err
		}
	}
	turn := thread.CurrentTurn + 1
	if outcome == threadClaimQueueMidTurn {
		turn++
	}
	content := input.Message
	if input.PlanMode {
		content = "[PLAN_MODE]\n" + content
	}
	message := &models.SessionMessage{SessionID: thread.SessionID, OrgID: input.OrgID, ThreadID: &input.ThreadID,
		UserID: input.UserID, TurnNumber: turn, Role: models.MessageRoleUser, Content: content,
		References: input.References, Commands: input.Commands, Source: input.MessageSource}
	if len(input.Images) > 0 {
		message.Attachments = input.Images
	}
	answerQuestion := preClaimSessionStatus == models.SessionStatusAwaitingInput && input.UserID != nil && s.questionStore != nil
	answerHumanInput := preClaimStatus == models.ThreadStatusAwaitingInput && input.UserID != nil && s.humanInputStore != nil
	if capacityQueue {
		// Capacity queues have no continuation payload to carry a durable
		// human-input answer, and do not resolve review comments.
		input.ResolveReviewCommentIDs = nil
		answerQuestion = thread.Status == models.ThreadStatusAwaitingInput && input.UserID != nil && s.questionStore != nil
		answerHumanInput = false
	}
	messageErrorPrefix := "create message"
	if capacityQueue {
		messageErrorPrefix = "create queued message"
	}
	if (len(input.ResolveReviewCommentIDs) > 0 || answerQuestion || answerHumanInput) && s.txStarter == nil {
		return legacySendAttempt{}, fmt.Errorf("%s: tx starter not configured", messageErrorPrefix)
	}
	writes, err := s.createLegacyMessageInTx(ctx, tx, message, input, claimedSession, answerQuestion, answerHumanInput, queued)
	if err != nil {
		var notInSession *db.ErrReviewCommentsNotInSession
		if errors.As(err, &notInSession) {
			return legacySendAttempt{}, err
		}
		if queued && errors.Is(err, pgx.ErrNoRows) {
			return legacySendAttempt{}, ErrThreadNotFound
		}
		return legacySendAttempt{}, fmt.Errorf("%s: %w", messageErrorPrefix, err)
	}
	if err := recordLegacyCallerStampInTx(ctx, tx, input, message, writes.inbox); err != nil {
		return legacySendAttempt{}, err
	}
	attempt := legacySendAttempt{
		result: &SendMessageResult{Message: message, InboxEntry: writes.inbox, ThreadStatus: thread.Status,
			DeliveryState: deliveryStateForInboxEntry(writes.inbox), ResolvedComments: writes.resolved,
			AnsweredQuestion: writes.answered, AnsweredHumanInput: writes.answeredHumanInput},
		claim: claim, publishClaim: !queued, pending: writes.pending,
		// Historically grouped pending writes had no stream publisher.
		publishPending: queued && !(s.inboxStore != nil && s.txStarter != nil), queued: queued,
		missingQuestion: writes.missingQuestion, missingHumanInput: writes.missingHumanInput,
	}
	if capacityQueue {
		return attempt, nil
	}
	var opts db.EnqueueOpts
	if queued {
		if s.inboxStore == nil {
			return attempt, nil
		}
		opts, err = s.legacyLiveDeliveryOptsInTx(ctx, tx, input)
		if err != nil {
			return legacySendAttempt{}, err
		}
	} else {
		opts = legacyContinuationOpts(input, holder, thread, claimedSession, writes.answeredHumanInput)
	}
	attempt.jobID, err = s.send.Legacy.EnqueueInTx(ctx, tx, input.OrgID, opts)
	if err != nil {
		return legacySendAttempt{}, &legacySendDomainError{domain: ErrEnqueueFailed, cause: err}
	}
	// A legacy dedupe collision returns (uuid.Nil, nil); retain its success
	// and notification behavior without introducing active-protocol policy.
	attempt.notifyJob = true
	return attempt, nil
}

func (s *Service) claimLegacyThreadInTx(ctx context.Context, tx pgx.Tx, input SendMessageInput) (models.SessionThread, models.ThreadStatus, threadClaimOutcome, db.ThreadClaimPersistenceEffects, error) {
	thread, effects, err := s.send.Legacy.ClaimIdleThreadInTx(ctx, tx, input.OrgID, input.SessionID, input.ThreadID, models.MaxRunningThreadsPerSession)
	if err == nil {
		if _, err := visibleThreadInSession(thread, input.SessionID); err != nil {
			return models.SessionThread{}, "", 0, db.ThreadClaimPersistenceEffects{}, err
		}
		return thread, models.ThreadStatusIdle, threadClaimClaimed, effects, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return models.SessionThread{}, "", 0, db.ThreadClaimPersistenceEffects{}, err
	}
	existing, err := s.send.Legacy.GetThreadInTx(ctx, tx, input.OrgID, input.ThreadID)
	if err != nil {
		return models.SessionThread{}, "", 0, db.ThreadClaimPersistenceEffects{}, &legacySendDomainError{domain: ErrThreadNotFound, cause: err}
	}
	existing, err = visibleThreadInSession(existing, input.SessionID)
	if err != nil {
		return models.SessionThread{}, "", 0, db.ThreadClaimPersistenceEffects{}, err
	}
	if threadStatusCanQueue(existing.Status) {
		if len(input.ResolveReviewCommentIDs) > 0 {
			return models.SessionThread{}, "", 0, db.ThreadClaimPersistenceEffects{}, ErrThreadNotIdle
		}
		return existing, existing.Status, threadClaimQueueMidTurn, db.ThreadClaimPersistenceEffects{}, nil
	}
	if existing.Status == models.ThreadStatusIdle || threadStatusIsResumable(existing.Status) {
		active, countErr := s.send.Legacy.CountActiveThreadsInTx(ctx, tx, input.OrgID, input.SessionID)
		if countErr != nil {
			return models.SessionThread{}, "", 0, db.ThreadClaimPersistenceEffects{}, fmt.Errorf("count active session threads: %w", countErr)
		}
		if isActiveStatus(existing.Status) {
			active-- // The original claim cap excludes the target itself.
		}
		if active >= models.MaxRunningThreadsPerSession {
			return existing, existing.Status, threadClaimQueueLimitReached, db.ThreadClaimPersistenceEffects{}, nil
		}
	}
	if !threadStatusIsResumable(existing.Status) {
		return models.SessionThread{}, "", 0, db.ThreadClaimPersistenceEffects{}, ErrThreadNotIdle
	}
	thread, effects, err = s.send.Legacy.ClaimResumeThreadInTx(ctx, tx, input.OrgID, input.SessionID, input.ThreadID, models.MaxRunningThreadsPerSession)
	if err == nil {
		return thread, existing.Status, threadClaimClaimed, effects, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return models.SessionThread{}, "", 0, db.ThreadClaimPersistenceEffects{}, fmt.Errorf("claim thread for resume: %w", err)
	}
	raced, err := s.send.Legacy.GetThreadInTx(ctx, tx, input.OrgID, input.ThreadID)
	if err != nil {
		return models.SessionThread{}, "", 0, db.ThreadClaimPersistenceEffects{}, &legacySendDomainError{domain: ErrThreadNotFound, cause: err}
	}
	raced, err = visibleThreadInSession(raced, input.SessionID)
	if err != nil {
		return models.SessionThread{}, "", 0, db.ThreadClaimPersistenceEffects{}, err
	}
	if threadStatusCanQueue(raced.Status) && len(input.ResolveReviewCommentIDs) == 0 {
		return raced, raced.Status, threadClaimQueueMidTurn, db.ThreadClaimPersistenceEffects{}, nil
	}
	return models.SessionThread{}, "", 0, db.ThreadClaimPersistenceEffects{}, ErrThreadNotIdle
}

func (s *Service) claimLegacySessionInTx(ctx context.Context, tx pgx.Tx, input SendMessageInput) (models.Session, models.SessionStatus, error) {
	claimed, err := s.send.Legacy.ClaimIdleSessionInTx(ctx, tx, input.OrgID, input.SessionID)
	if err == nil {
		return claimed, models.SessionStatusIdle, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return models.Session{}, "", err
	}
	existing, err := s.send.Legacy.GetSessionInTx(ctx, tx, input.OrgID, input.SessionID)
	if err != nil {
		return models.Session{}, "", fmt.Errorf("inspect session for claim fallback: %w", err)
	}
	if existing.Status == models.SessionStatusRunning {
		return existing, "", nil
	}
	if existing.SandboxState == models.SandboxStateDestroyed {
		return models.Session{}, "", ErrSessionSnapshotExpired
	}
	resumed, err := s.send.Legacy.ClaimResumeSessionInTx(ctx, tx, input.OrgID, input.SessionID)
	if err != nil {
		return models.Session{}, "", &legacySendDomainError{domain: ErrSessionNotResumable, cause: err}
	}
	return resumed, existing.Status, nil
}

func (s *Service) createLegacyMessageInTx(ctx context.Context, tx pgx.Tx, message *models.SessionMessage, input SendMessageInput, claimedSession models.Session, answerQuestion, answerHumanInput, incrementPending bool) (legacyMessageWrites, error) {
	var writes legacyMessageWrites
	if err := s.send.Legacy.CreateMessageInTx(ctx, tx, input.OrgID, message); err != nil {
		return legacyMessageWrites{}, err
	}
	if len(input.ResolveReviewCommentIDs) > 0 {
		var err error
		writes.resolved, err = db.NewSessionReviewCommentStore(tx).ValidateAndResolveByIDs(ctx, input.OrgID, input.SessionID, input.ResolveReviewCommentIDs, resolutionPass(&claimedSession))
		if err != nil {
			return legacyMessageWrites{}, err
		}
	}
	if answerQuestion {
		question, err := db.NewSessionQuestionStore(tx).AnswerLatestPendingBySession(ctx, input.OrgID, input.SessionID, input.Message, *input.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			writes.missingQuestion = true
		} else if err != nil {
			return legacyMessageWrites{}, fmt.Errorf("answer pending question: %w", err)
		} else {
			writes.answered = &question
		}
	}
	if answerHumanInput {
		request, err := db.NewSessionHumanInputRequestStore(tx).AnswerLatestPendingFreeTextByThread(ctx, input.OrgID, input.SessionID, input.ThreadID, input.Message, *input.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			writes.missingHumanInput = true
		} else if err != nil {
			return legacyMessageWrites{}, fmt.Errorf("answer pending human input request: %w", err)
		} else {
			writes.answeredHumanInput = &request
		}
	}
	if s.inboxStore != nil {
		entry, err := s.appendLegacyInboxInTx(ctx, tx, input, message)
		if err != nil {
			return legacyMessageWrites{}, fmt.Errorf("append inbox entry: %w", err)
		}
		writes.inbox = &entry
	}
	if incrementPending {
		var err error
		writes.pending, err = s.send.Legacy.IncrementPendingMessagesInTx(ctx, tx, input.OrgID, input.ThreadID)
		if err != nil {
			return legacyMessageWrites{}, fmt.Errorf("increment queued message count: %w", err)
		}
	}
	return writes, nil
}

func (s *Service) findExistingLegacyMessageInTx(ctx context.Context, tx pgx.Tx, input SendMessageInput) (models.SessionMessage, bool, error) {
	if s.inboxStore == nil || input.ClientMessageID == "" {
		return models.SessionMessage{}, false, nil
	}
	entry, err := s.send.Legacy.GetInboxByClientMessageIDInTx(ctx, tx, input.OrgID, input.ThreadID, input.ClientMessageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.SessionMessage{}, false, nil
	}
	if err != nil {
		return models.SessionMessage{}, false, fmt.Errorf("get thread inbox entry by client message id: %w", err)
	}
	// A retained message/stamp cannot repair or conceal a differently scoped
	// inbox row. Proof-free callers retain the original lookup behavior.
	if input.CallerCommand != nil && !legacyCallerInboxScopeMatches(input, &entry, entry.MessageID) {
		return models.SessionMessage{}, false, &CallerCommandMismatchError{}
	}
	if entry.MessageID == 0 {
		return models.SessionMessage{}, false, nil
	}
	message, err := s.send.Legacy.GetMessageByIDInTx(ctx, tx, input.OrgID, entry.MessageID)
	if err != nil {
		return models.SessionMessage{}, false, fmt.Errorf("get idempotent thread message: %w", err)
	}
	return message, true, nil
}

func (s *Service) validateLegacyInboxAdmissionInTx(ctx context.Context, tx pgx.Tx, input SendMessageInput) error {
	if len(input.Message) > maxThreadMessageBytes {
		return fmt.Errorf("%w: message is %d bytes, max is %d", ErrThreadInboxBackpressure, len(input.Message), maxThreadMessageBytes)
	}
	if s.inboxStore == nil {
		return nil
	}
	threadCount, err := s.send.Legacy.CountPendingInboxByThreadInTx(ctx, tx, input.OrgID, input.ThreadID)
	if err != nil {
		return fmt.Errorf("count pending thread inbox entries: %w", err)
	}
	if threadCount >= maxThreadInboxPending {
		return fmt.Errorf("%w: thread has %d undelivered messages", ErrThreadInboxBackpressure, threadCount)
	}
	sessionCount, err := s.send.Legacy.CountPendingInboxBySessionInTx(ctx, tx, input.OrgID, input.SessionID)
	if err != nil {
		return fmt.Errorf("count pending session inbox entries: %w", err)
	}
	if sessionCount >= maxSessionInboxPending {
		return fmt.Errorf("%w: session has %d undelivered messages", ErrThreadInboxBackpressure, sessionCount)
	}
	return nil
}

func legacyContinuationOpts(input SendMessageInput, holder *uuid.UUID, thread models.SessionThread, session models.Session, answered *models.HumanInputRequest) db.EnqueueOpts {
	key := db.ContinueSessionDedupeKey(thread.ID)
	if input.ContinuationDedupeKeyOverride != nil && *input.ContinuationDedupeKeyOverride != "" {
		key = *input.ContinuationDedupeKeyOverride
	}
	payload := map[string]string{"session_id": thread.SessionID.String(), "thread_id": input.ThreadID.String(), "org_id": input.OrgID.String()}
	if input.ChangesetID != nil {
		payload["changeset_id"] = input.ChangesetID.String()
		payload["changeset_lease_holder_id"] = holder.String()
	}
	if input.PostSuccessAction != "" {
		payload["post_success_action"] = input.PostSuccessAction
	}
	if input.PostSuccessAuthorMode != "" {
		payload["post_success_author_mode"] = input.PostSuccessAuthorMode
	}
	if answered != nil {
		payload["human_input_request_id"] = answered.ID.String()
	}
	return db.EnqueueOpts{Queue: "agent", JobType: "continue_session", Payload: payload, Priority: 5, DedupeKey: &key, TargetNodeID: models.SessionWorkerTarget(&session)}
}

func (s *Service) legacyLiveDeliveryOptsInTx(ctx context.Context, tx pgx.Tx, input SendMessageInput) (db.EnqueueOpts, error) {
	var target *string
	if s.runtimeOwnerStore != nil {
		runtime, err := s.send.Legacy.GetActiveThreadRuntimeInTx(ctx, tx, input.OrgID, input.ThreadID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return db.EnqueueOpts{}, fmt.Errorf("get live thread runtime owner: %w", err)
		}
		if err == nil && runtime.OwnerNodeID != "" {
			target = &runtime.OwnerNodeID
		}
	}
	key := deliverThreadInboxJobType + ":" + input.ThreadID.String()
	return db.EnqueueOpts{Queue: deliverThreadInboxQueue, JobType: deliverThreadInboxJobType,
		Payload:  map[string]string{"session_id": input.SessionID.String(), "thread_id": input.ThreadID.String(), "org_id": input.OrgID.String()},
		Priority: 7, DedupeKey: &key, TargetNodeID: target}, nil
}
