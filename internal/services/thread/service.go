package thread

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// Sentinel errors returned by the thread service. Handlers should match on
// these with errors.Is rather than inspecting error strings.
var (
	ErrSessionNotFound         = errors.New("session not found")
	ErrSessionTerminal         = errors.New("cannot add threads to a completed session")
	ErrSessionNotResumable     = errors.New("session must be idle, running, awaiting input, need guidance, or otherwise resumable to send a message")
	ErrSessionSnapshotExpired  = errors.New("session sandbox snapshot has expired and can no longer be continued")
	ErrInvalidAgentType        = errors.New("invalid agent type")
	ErrInvalidModel            = errors.New("invalid model")
	ErrEnqueueFailed           = errors.New("enqueue failed")
	ErrThreadNotFound          = errors.New("thread not found")
	ErrThreadNotEditable       = errors.New("thread is not editable")
	ErrThreadNotIdle           = errors.New("thread must be idle to send a message")
	ErrThreadActive            = errors.New("thread is active")
	ErrCannotArchiveLastThread = errors.New("cannot archive the last visible thread")
	ErrActiveThreadExists      = errors.New("another thread is already active")
	ErrThreadCannotBeEnded     = errors.New("thread cannot be ended in its current state")
	// ErrRunningLimitReached is returned when sending to an idle tab would
	// exceed the per-session running-thread cap. The composer should fall
	// back to queueing the message for delivery once an active sibling
	// frees a slot.
	ErrRunningLimitReached = errors.New("session running thread limit reached")
	// ErrThreadNotCancellable is returned when a thread is not in a state
	// where SIGINT is meaningful (e.g. it is already idle, completed, or
	// failed). Surfaced to clients so the cancel button can be hidden when
	// it would do nothing.
	ErrThreadNotCancellable = errors.New("thread is not cancellable")
	// ErrReviewCommentsNotConfigured is returned when SendMessage is called
	// with ResolveReviewCommentIDs but the service was constructed without
	// the review-comment plumbing (txStarter + reviewCommentStore). Handlers
	// should surface this as a 400 — the client requested a feature the
	// server isn't running.
	ErrReviewCommentsNotConfigured = errors.New("review comment resolution is not configured")
)

// SessionStore defines the session DB operations needed by the thread service.
//
// ClaimForResume is the fallback when ClaimIdle fails because the session is
// in a terminal or paused status (completed, pr_created, failed, cancelled,
// awaiting_input, needs_human_guidance) — mirrors the session-level
// SendMessage handler so a follow-up sent through a thread tab can resume the
// same set of session statuses as one sent through the legacy session
// endpoint. Without this, the thread surface 500s on any non-idle/non-running
// session.
type SessionStore interface {
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	ClaimIdle(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	ClaimForResume(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status models.SessionStatus) error
}

// QuestionStore is the optional clarifying-question surface used by
// SendMessage to flip the latest pending question to 'answered' when a
// follow-up message resumes an awaiting_input session. Mirrors the session-
// level handler's txQuestionStore.AnswerLatestPendingBySession behavior so
// question state stays in sync with the resumed run regardless of which
// surface (session-level or thread-level) the user sent through.
type QuestionStore interface {
	AnswerLatestPendingBySession(ctx context.Context, orgID, sessionID uuid.UUID, answerText string, answeredBy uuid.UUID) (models.SessionQuestion, error)
}

// HumanInputRequestStore is the optional durable human-input surface used by
// SendMessage to flip the latest pending free-text request on the target
// thread to 'answered' when a composer send resumes an awaiting_input tab.
type HumanInputRequestStore interface {
	AnswerLatestPendingFreeTextByThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID, answerText string, answeredBy uuid.UUID) (models.HumanInputRequest, error)
}

// ThreadStore defines the thread DB operations needed by the thread service.
type ThreadStore interface {
	Create(ctx context.Context, thread *models.SessionThread, maxThreads int) error
	GetByID(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionThread, error)
	ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error)
	Archive(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	ClaimIdleForSession(ctx context.Context, orgID, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, error)
	ClaimForResumeInSession(ctx context.Context, orgID, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, error)
	UpdateEditable(ctx context.Context, thread *models.SessionThread) error
	UpdateStatus(ctx context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus) error
	IncrementPendingMessages(ctx context.Context, orgID, threadID uuid.UUID) error
	MarkCancelRequested(ctx context.Context, orgID, threadID uuid.UUID) error
}

// FileEventStore defines the operations the thread service needs for the
// file-attribution surfaces (overlap detection, Changes-view filters).
type FileEventStore interface {
	ListBySession(ctx context.Context, orgID, sessionID uuid.UUID, since *time.Time) ([]models.SessionThreadFileEvent, error)
}

// ThreadCanceller cancels a thread's in-flight agent run. Implemented by the
// orchestrator's thread-scoped cancel registry. Optional: when nil, cancel
// requests still flip the thread's cancel_requested_at timestamp so the
// orchestrator picks up the intent on its next checkpoint, but no SIGINT is
// sent to the agent process.
type ThreadCanceller interface {
	CancelThread(threadID uuid.UUID) bool
}

type OwnerLossOrchestrator interface {
	RecoverLostOwner(ctx context.Context, orgID, sessionID uuid.UUID, threadID *uuid.UUID) error
}

// MessageStore defines the message DB operations needed by the thread service.
type MessageStore interface {
	Create(ctx context.Context, msg *models.SessionMessage) error
	ListByThread(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionMessage, error)
	ListWindowByThread(ctx context.Context, orgID, threadID uuid.UUID, opts db.SessionMessageWindowOptions) (db.SessionMessageWindow, error)
}

// LogStore defines the log DB operations needed by the thread service.
type LogStore interface {
	ListByThread(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionLog, error)
	ListByThreadTurns(ctx context.Context, orgID, threadID uuid.UUID, turnNumbers []int) ([]models.SessionLog, error)
}

// JobStore defines the job DB operations needed by the thread service.
type JobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
	EnqueueWithOpts(ctx context.Context, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error)
}

// CreateThreadInput holds the input for creating a new thread.
type CreateThreadInput struct {
	SessionID    uuid.UUID
	OrgID        uuid.UUID
	AgentType    string
	Model        string
	Label        string
	Instructions string
	FileScope    []string
}

// UpdateThreadInput patches an editable (idle, current_turn=0) thread. Model
// uses *string to distinguish three states the handler maps from the wire:
//   - nil:       field absent from the patch — keep the existing override
//   - non-nil "": field present as JSON null or empty string — clear the override
//   - non-nil v: field present with a value — set/validate to v
type UpdateThreadInput struct {
	SessionID uuid.UUID
	OrgID     uuid.UUID
	ThreadID  uuid.UUID
	AgentType string
	Model     *string
	Label     string
}

// SendMessageInput holds the input for sending a message to a thread.
//
// ResolveReviewCommentIDs, when non-empty, are validated and flipped to
// resolved=true atomically with the message create — preserving the
// "addressing comments" → "send follow-up" → "comments resolved" invariant
// that the session-level SendMessage already guarantees. Requires the
// service to be wired with SetTxStarter and SetReviewCommentStore; handler
// layers should reject the request with a 400 when those are absent.
type SendMessageInput struct {
	SessionID               uuid.UUID
	OrgID                   uuid.UUID
	ThreadID                uuid.UUID
	UserID                  *uuid.UUID
	Message                 string
	Images                  []string
	References              models.SessionInputReferences
	Commands                models.SessionInputCommands
	PlanMode                bool
	ResolveReviewCommentIDs []uuid.UUID
	// ContinuationDedupeKeyOverride is for system-generated follow-up turns
	// created while the current same-thread worker job is still marked running.
	// User sends should leave it nil so rapid-fire messages keep normal thread
	// dedupe behavior.
	ContinuationDedupeKeyOverride *string
}

// SendMessageResult carries everything callers need to finish handling a
// successful thread-message send: the created message, plus any review
// comments that were resolved as part of the same transaction. The handler
// uses ResolvedComments to emit one audit row per resolved comment after
// the tx commits — matching the post-commit audit pattern of session-level
// SendMessage.
//
// AnsweredQuestion is non-nil when the send resumed an awaiting_input session
// and a pending clarifying question was flipped to 'answered' alongside the
// message create. The handler uses it to emit a SessionQuestionAnswered audit
// after the tx commits — same shape as the session-level path.
type SendMessageResult struct {
	Message            *models.SessionMessage
	ResolvedComments   []models.SessionReviewComment
	AnsweredQuestion   *models.SessionQuestion
	AnsweredHumanInput *models.HumanInputRequest
}

type MessageWindowResult struct {
	Window       db.SessionMessageWindow
	ThreadStatus models.ThreadStatus
}

// Service handles thread business logic.
type Service struct {
	threadStore        ThreadStore
	sessionStore       SessionStore
	messageStore       MessageStore
	logStore           LogStore
	jobStore           JobStore
	fileEvents         FileEventStore                // optional — enables overlap and attribution surfaces
	canceller          ThreadCanceller               // optional — enables in-flight SIGINT
	txStarter          db.TxStarter                  // optional — required for SendMessage with ResolveReviewCommentIDs or awaiting_input answer
	reviewCommentStore *db.SessionReviewCommentStore // optional — required for SendMessage with ResolveReviewCommentIDs
	questionStore      QuestionStore                 // optional — required to answer pending questions on awaiting_input resume
	humanInputStore    HumanInputRequestStore        // optional — required to answer pending human-input requests on awaiting_input resume
	ownerLoss          OwnerLossOrchestrator         // optional — proactively recovers lost runtime owners after queue-only sends
	logger             zerolog.Logger
}

// NewService creates a new thread service. fileEvents and canceller are
// optional: passing nil disables the surfaces that depend on them but keeps
// the rest of the service functional. Tests typically wire only the stores
// they exercise.
func NewService(
	threadStore ThreadStore,
	sessionStore SessionStore,
	messageStore MessageStore,
	logStore LogStore,
	jobStore JobStore,
	logger zerolog.Logger,
) *Service {
	return &Service{
		threadStore:  threadStore,
		sessionStore: sessionStore,
		messageStore: messageStore,
		logStore:     logStore,
		jobStore:     jobStore,
		logger:       logger,
	}
}

// SetFileEventStore wires the optional file-event store post-construction.
// Kept separate so the existing NewService signature does not change and so
// tests can omit it.
func (s *Service) SetFileEventStore(store FileEventStore) {
	s.fileEvents = store
}

// SetCanceller wires the optional thread canceller. Provided by the agent
// orchestrator's thread-scoped cancel registry once it is constructed.
func (s *Service) SetCanceller(c ThreadCanceller) {
	s.canceller = c
}

func (s *Service) SetOwnerLossOrchestrator(orchestrator OwnerLossOrchestrator) {
	s.ownerLoss = orchestrator
}

// SetReviewCommentResolver wires the plumbing required to resolve review
// comments atomically with a thread-scoped message send. Both arguments must
// be non-nil for SendMessage to honor ResolveReviewCommentIDs; if either is
// missing, SendMessage rejects requests carrying comment IDs with
// ErrReviewCommentsNotConfigured. Kept as a single setter so the
// "configured" predicate is unambiguous (versus two independently-nilable
// fields).
func (s *Service) SetReviewCommentResolver(txStarter db.TxStarter, store *db.SessionReviewCommentStore) {
	s.txStarter = txStarter
	s.reviewCommentStore = store
}

// SetQuestionStore wires the optional clarifying-question store. When wired,
// SendMessage answers the latest pending question on the session as part of
// resuming an awaiting_input session — mirroring the session-level handler.
// Without it, follow-ups to awaiting_input sessions still create a message
// and resume the run, but the question row is left pending (the orchestrator
// will resolve it on the next checkpoint).
func (s *Service) SetQuestionStore(store QuestionStore) {
	s.questionStore = store
}

// SetHumanInputRequestStore wires the optional durable human-input request
// store. The txStarter must also be configured through SetReviewCommentResolver
// because the implicit answer is committed atomically with the message row.
func (s *Service) SetHumanInputRequestStore(store HumanInputRequestStore) {
	s.humanInputStore = store
}

// CreateThread validates inputs and creates a blank idle thread.
func (s *Service) CreateThread(ctx context.Context, input CreateThreadInput) (*models.SessionThread, error) {
	// Verify session exists and belongs to org.
	session, err := s.sessionStore.GetByID(ctx, input.OrgID, input.SessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionNotFound, err)
	}

	// Allow blank tabs on active sessions and on the same terminal statuses
	// the follow-up message path can already resume. This keeps "add tab"
	// aligned with "continue session" while still rejecting non-resumable
	// statuses such as skipped.
	if !models.SessionStatus(session.Status).CanAddThread() {
		return nil, ErrSessionTerminal
	}

	// Default agent type to the session's agent type.
	agentType := models.AgentType(input.AgentType)
	if agentType == "" {
		agentType = session.AgentType
	}
	if err := agentType.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAgentType, err)
	}

	var modelOverride *string
	if input.Model != "" {
		if err := models.ValidateModelForAgentType(agentType, input.Model); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidModel, err)
		}
		modelOverride = &input.Model
	}

	var instructions *string
	if input.Instructions != "" {
		instructions = &input.Instructions
	}

	thread := &models.SessionThread{
		SessionID:     input.SessionID,
		OrgID:         input.OrgID,
		AgentType:     agentType,
		ModelOverride: modelOverride,
		Label:         input.Label,
		Instructions:  instructions,
		FileScope:     input.FileScope,
		Status:        models.ThreadStatusIdle,
	}

	if err := s.threadStore.Create(ctx, thread, models.MaxThreadsPerSession); err != nil {
		if errors.Is(err, db.ErrThreadLimitReached) {
			return nil, db.ErrThreadLimitReached
		}
		return nil, fmt.Errorf("create thread: %w", err)
	}

	return thread, nil
}

func (s *Service) UpdateThread(ctx context.Context, input UpdateThreadInput) (*models.SessionThread, error) {
	session, err := s.sessionStore.GetByID(ctx, input.OrgID, input.SessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionNotFound, err)
	}
	if !models.SessionStatus(session.Status).CanAddThread() {
		return nil, ErrSessionTerminal
	}

	thread, err := s.threadStore.GetByID(ctx, input.OrgID, input.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	thread, err = visibleThreadInSession(thread, input.SessionID)
	if err != nil {
		return nil, err
	}
	if thread.Status != models.ThreadStatusIdle || thread.CurrentTurn != 0 {
		return nil, ErrThreadNotEditable
	}

	agentType := thread.AgentType
	if input.AgentType != "" {
		agentType = models.AgentType(input.AgentType)
		if err := agentType.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidAgentType, err)
		}
	}

	thread.AgentType = agentType
	thread.Label = input.Label
	switch {
	case input.Model != nil && *input.Model != "":
		if err := models.ValidateModelForAgentType(agentType, *input.Model); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidModel, err)
		}
		model := *input.Model
		thread.ModelOverride = &model
	case input.Model != nil:
		// Explicit empty model — user picked the agent default.
		thread.ModelOverride = nil
	case input.AgentType != "":
		// Agent switched without an explicit model: drop the inherited override
		// because it was scoped to the previous agent.
		thread.ModelOverride = nil
	}

	if err := s.threadStore.UpdateEditable(ctx, &thread); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrThreadNotEditable
		}
		return nil, fmt.Errorf("update thread: %w", err)
	}
	return &thread, nil
}

func (s *Service) ArchiveThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error) {
	if _, err := s.sessionStore.GetByID(ctx, orgID, sessionID); err != nil {
		return models.SessionThread{}, fmt.Errorf("%w: %w", ErrSessionNotFound, err)
	}

	archived, err := s.threadStore.Archive(ctx, orgID, sessionID, threadID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			target, getErr := s.threadStore.GetByID(ctx, orgID, threadID)
			if getErr != nil || target.SessionID != sessionID {
				return models.SessionThread{}, ErrThreadNotFound
			}
			if target.ArchivedAt != nil {
				return models.SessionThread{}, ErrThreadNotFound
			}
			if isActiveStatus(target.Status) {
				return models.SessionThread{}, ErrThreadActive
			}

			threads, listErr := s.threadStore.ListBySession(ctx, orgID, sessionID)
			if listErr != nil {
				return models.SessionThread{}, fmt.Errorf("list threads: %w", listErr)
			}
			if len(threads) <= 1 {
				return models.SessionThread{}, ErrCannotArchiveLastThread
			}
			return models.SessionThread{}, ErrThreadNotFound
		}
		return models.SessionThread{}, fmt.Errorf("archive thread: %w", err)
	}
	return archived, nil
}

func visibleThreadInSession(thread models.SessionThread, sessionID uuid.UUID) (models.SessionThread, error) {
	if thread.SessionID != sessionID || thread.ArchivedAt != nil {
		return models.SessionThread{}, ErrThreadNotFound
	}
	return thread, nil
}

// ListThreads returns all threads for a session.
func (s *Service) ListThreads(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error) {
	// Verify session exists and belongs to org.
	if _, err := s.sessionStore.GetByID(ctx, orgID, sessionID); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionNotFound, err)
	}

	threads, err := s.threadStore.ListBySession(ctx, orgID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	return threads, nil
}

// GetThread returns a single thread, validating it belongs to the given session.
func (s *Service) GetThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error) {
	thread, err := s.threadStore.GetByID(ctx, orgID, threadID)
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	return visibleThreadInSession(thread, sessionID)
}

// threadClaimOutcome enumerates the four possible results of
// claimThreadForSend so the caller's branching is explicit. The "claimed"
// outcomes are split from the "queue" outcomes because the post-claim flow
// (session claim, message create, enqueue) only applies when the thread row
// has actually moved to running.
type threadClaimOutcome int

const (
	// threadClaimClaimed: thread row transitioned from idle (or a resumable
	// terminal status) to running and is ready to receive a new turn.
	threadClaimClaimed threadClaimOutcome = iota
	// threadClaimQueueMidTurn: thread is mid-turn (pending/running); the
	// orchestrator drains the message queue when the in-flight turn ends.
	threadClaimQueueMidTurn
	// threadClaimQueueLimitReached: thread is otherwise claimable but the
	// session has already hit MaxRunningThreadsPerSession. Caller queues the
	// message against the still-idle thread; the next sibling to finish will
	// pick the work up via the orchestrator's drain.
	threadClaimQueueLimitReached
)

func isActiveStatus(status models.ThreadStatus) bool {
	return status == models.ThreadStatusPending || status == models.ThreadStatusRunning || status == models.ThreadStatusAwaitingInput
}

// SendMessage claims an idle thread (or resumes a paused/terminal one),
// creates a message, and enqueues a continue_session job. When
// ResolveReviewCommentIDs is non-empty, the message create and the comment
// resolution share a single transaction so the user-visible invariant —
// "submitted comments disappear once the follow-up message is sent" — holds
// even if the request fails partway through.
//
// claimThreadForSend serializes sibling-thread admission in the database
// while allowing up to MaxRunningThreadsPerSession concurrent tabs. It
// mirrors the session-level claimSessionForSend ordering: try the idle
// claim first, fall back to the resume claim for terminal/paused statuses
// (completed, failed, cancelled, awaiting_input), and only fail with
// ErrThreadNotIdle when neither succeeds. When another tab has already moved
// the session into running state, ClaimIdle's failure is treated as a no-op
// at the session layer since the orchestrator's idempotent
// UpdateStatus("running") handles the rest.
//
// On any downstream failure (message create, comment resolve, question
// answer, enqueue) we best-effort revert the thread to idle and the session
// to the status it had before the claim.
func (s *Service) SendMessage(ctx context.Context, input SendMessageInput) (*SendMessageResult, error) {
	// Reject early — before any state mutation — when the caller asked to
	// resolve comments but the service was constructed without the plumbing
	// to do so. Pushing this check above the claim avoids leaving the thread
	// stuck in 'running' if the configuration is wrong.
	resolvingComments := len(input.ResolveReviewCommentIDs) > 0
	if resolvingComments && (s.txStarter == nil || s.reviewCommentStore == nil) {
		return nil, ErrReviewCommentsNotConfigured
	}

	thread, preClaimThreadStatus, outcome, err := s.claimThreadForSend(ctx, input, resolvingComments)
	if err != nil {
		return nil, err
	}
	if outcome == threadClaimQueueLimitReached {
		return s.queueMessageWaitingForSlot(ctx, input)
	}
	queueOnly := outcome == threadClaimQueueMidTurn

	// Try claiming an idle session first, then fall back to resuming a
	// terminal/paused session — same order as sessions.SendMessage. revertStatus
	// records the pre-claim status so revertAfterSendFailure can put the
	// session back exactly where it was; an empty string signals "do not
	// touch the session" (sibling-running case).
	// Skipped on the queue-only path: the thread is already running, which
	// already implies the session is running.
	var claimedSession models.Session
	var revertStatus models.SessionStatus
	if !queueOnly {
		var claimErr error
		claimedSession, revertStatus, claimErr = s.claimSessionForSend(ctx, input.OrgID, input.SessionID)
		if claimErr != nil {
			s.releaseThread(ctx, input.OrgID, input.ThreadID, "parent session claim failure")
			return nil, claimErr
		}
		if revertStatus == "" {
			if resolvingComments {
				s.releaseThread(ctx, input.OrgID, input.ThreadID, "sibling-queued thread cannot resolve comments")
				return nil, ErrThreadNotIdle
			}
			return s.queueClaimedThreadBehindSibling(ctx, input, thread, preClaimThreadStatus)
		}
	}

	content := input.Message
	if input.PlanMode {
		content = "[PLAN_MODE]\n" + content
	}

	// On the queue-only path the thread is mid-turn, so its in-flight turn is
	// CurrentTurn+1 and the queued message belongs to the turn after that
	// (CurrentTurn+2). For the normal claim path, the thread just transitioned
	// to running and this turn is CurrentTurn+1.
	turnNumber := thread.CurrentTurn + 1
	if queueOnly {
		turnNumber = thread.CurrentTurn + 2
	}

	msg := &models.SessionMessage{
		SessionID:  thread.SessionID,
		OrgID:      input.OrgID,
		ThreadID:   &input.ThreadID,
		UserID:     input.UserID,
		TurnNumber: turnNumber,
		Role:       models.MessageRoleUser,
		Content:    content,
		References: input.References,
		Commands:   input.Commands,
	}
	if len(input.Images) > 0 {
		msg.Attachments = input.Images
	}

	// answerPendingQuestion is true when the session was paused on a
	// clarifying question and the caller has the plumbing to answer it.
	// Mirrors sessions.go's predicate: revertStatus == awaiting_input &&
	// userID != nil && questionStore != nil.
	answerPendingQuestion := revertStatus == models.SessionStatusAwaitingInput && input.UserID != nil && s.questionStore != nil
	answerPendingHumanInput := preClaimThreadStatus == models.ThreadStatusAwaitingInput && input.UserID != nil && s.humanInputStore != nil

	var (
		resolvedComments   []models.SessionReviewComment
		answeredQuestion   *models.SessionQuestion
		answeredHumanInput *models.HumanInputRequest
	)
	if resolvingComments || answerPendingQuestion || answerPendingHumanInput {
		resolvedComments, answeredQuestion, answeredHumanInput, err = s.createMessageInTx(ctx, msg, input, claimedSession, answerPendingQuestion, answerPendingHumanInput)
	} else {
		err = s.messageStore.Create(ctx, msg)
	}
	if err != nil {
		if !queueOnly {
			s.revertAfterSendFailure(ctx, input.OrgID, input.SessionID, input.ThreadID, revertStatus, "message creation failure")
		}
		// Comment-validation errors are surfaced verbatim so the handler can
		// match on *db.ErrReviewCommentsNotInSession; everything else gets
		// wrapped with a "create message" prefix to preserve historical log
		// shape for the no-comments path.
		var notInSession *db.ErrReviewCommentsNotInSession
		if errors.As(err, &notInSession) {
			return nil, err
		}
		return nil, fmt.Errorf("create message: %w", err)
	}

	// Queue-only path: bump pending_message_count so the UI's "queued (N)"
	// affordance reflects the buildup, then return without enqueueing a new
	// continue_session — the in-flight job will drain the queue when it
	// finishes (see ContinueSession's post-turn drain).
	if queueOnly {
		if incErr := s.threadStore.IncrementPendingMessages(ctx, input.OrgID, input.ThreadID); incErr != nil {
			s.logger.Warn().Err(incErr).
				Str("thread_id", input.ThreadID.String()).
				Msg("failed to increment pending_message_count for queued message")
		} else {
			s.recoverLostOwnerAfterQueuedSend(ctx, input.OrgID, input.SessionID, input.ThreadID)
		}
		return &SendMessageResult{
			Message:          msg,
			ResolvedComments: resolvedComments,
		}, nil
	}

	// Reuse the session continuation worker for phase 1. The latest user
	// message carries thread_id, so the orchestrator attributes assistant
	// messages and streamed logs back to this tab while still operating on the
	// single shared sandbox. Dedupe at the thread level so rapid-fire sends to
	// this tab collapse, while sibling sends that arrive during another tab's
	// turn are queued without enqueueing a second shared-sandbox job.
	dedupeKey := db.ContinueSessionDedupeKey(thread.ID)
	if input.ContinuationDedupeKeyOverride != nil && *input.ContinuationDedupeKeyOverride != "" {
		dedupeKey = *input.ContinuationDedupeKeyOverride
	}
	payload := map[string]string{
		"session_id": thread.SessionID.String(),
		"thread_id":  input.ThreadID.String(),
		"org_id":     input.OrgID.String(),
	}
	if answeredHumanInput != nil {
		payload["human_input_request_id"] = answeredHumanInput.ID.String()
	}
	if _, err := s.jobStore.EnqueueWithOpts(ctx, input.OrgID, db.EnqueueOpts{
		Queue:        "agent",
		JobType:      "continue_session",
		Payload:      payload,
		Priority:     5,
		DedupeKey:    &dedupeKey,
		TargetNodeID: models.SessionWorkerTarget(&claimedSession),
	}); err != nil {
		// Note: we do NOT roll back the resolved comments or answered
		// question here. The message has been committed and is durably in
		// the timeline; the orchestrator will retry the enqueue on the next
		// dedupe-eligible event. Reverting the resolved comments would
		// create a worse inconsistency where the user sees their addressed
		// comments re-appear despite the message already being in the
		// conversation.
		s.revertAfterSendFailure(ctx, input.OrgID, input.SessionID, input.ThreadID, revertStatus, "enqueue failure")
		return nil, fmt.Errorf("%w: %w", ErrEnqueueFailed, err)
	}

	return &SendMessageResult{
		Message:            msg,
		ResolvedComments:   resolvedComments,
		AnsweredQuestion:   answeredQuestion,
		AnsweredHumanInput: answeredHumanInput,
	}, nil
}

// queueMessageWaitingForSlot queues a follow-up against a thread that could
// not get a running slot because the session-wide running-thread cap is
// already saturated. Accepts both idle threads and threads in a resumable
// terminal/paused status — both behave identically from the queue's
// perspective: the message is appended to the thread's pending queue and
// pending_message_count is bumped so the composer can show "queued (N)";
// no continue_session is enqueued because no slot is available — the next
// sibling to finish will pick the work up via the orchestrator's drain,
// which will idle-claim or resume-claim the thread as appropriate.
func (s *Service) queueMessageWaitingForSlot(ctx context.Context, input SendMessageInput) (*SendMessageResult, error) {
	thread, err := s.threadStore.GetByID(ctx, input.OrgID, input.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	thread, err = visibleThreadInSession(thread, input.SessionID)
	if err != nil {
		return nil, err
	}
	if thread.Status != models.ThreadStatusIdle && !threadStatusIsResumable(thread.Status) {
		return nil, ErrThreadNotIdle
	}

	content := input.Message
	if input.PlanMode {
		content = "[PLAN_MODE]\n" + content
	}
	msg := &models.SessionMessage{
		SessionID:  thread.SessionID,
		OrgID:      input.OrgID,
		ThreadID:   &input.ThreadID,
		UserID:     input.UserID,
		TurnNumber: thread.CurrentTurn + 1,
		Role:       models.MessageRoleUser,
		Content:    content,
		References: input.References,
		Commands:   input.Commands,
	}
	if len(input.Images) > 0 {
		msg.Attachments = input.Images
	}

	answeredQuestion, answeredHumanInput, err := s.createQueuedMessage(ctx, msg, input, thread.Status)
	if err != nil {
		return nil, fmt.Errorf("create queued message: %w", err)
	}
	if err := s.threadStore.IncrementPendingMessages(ctx, input.OrgID, input.ThreadID); err != nil {
		return nil, fmt.Errorf("increment queued message count: %w", err)
	}
	s.recoverLostOwnerAfterQueuedSend(ctx, input.OrgID, input.SessionID, input.ThreadID)
	return &SendMessageResult{Message: msg, AnsweredQuestion: answeredQuestion, AnsweredHumanInput: answeredHumanInput}, nil
}

func (s *Service) queueClaimedThreadBehindSibling(ctx context.Context, input SendMessageInput, thread models.SessionThread, preClaimStatus models.ThreadStatus) (*SendMessageResult, error) {
	if err := s.threadStore.UpdateStatus(ctx, input.OrgID, input.ThreadID, models.ThreadStatusIdle); err != nil {
		return nil, fmt.Errorf("release thread for queued sibling message: %w", err)
	}

	content := input.Message
	if input.PlanMode {
		content = "[PLAN_MODE]\n" + content
	}
	msg := &models.SessionMessage{
		SessionID:  thread.SessionID,
		OrgID:      input.OrgID,
		ThreadID:   &input.ThreadID,
		UserID:     input.UserID,
		TurnNumber: thread.CurrentTurn + 1,
		Role:       models.MessageRoleUser,
		Content:    content,
		References: input.References,
		Commands:   input.Commands,
	}
	if len(input.Images) > 0 {
		msg.Attachments = input.Images
	}

	answeredQuestion, answeredHumanInput, err := s.createQueuedMessage(ctx, msg, input, preClaimStatus)
	if err != nil {
		return nil, fmt.Errorf("create queued sibling message: %w", err)
	}
	if err := s.threadStore.IncrementPendingMessages(ctx, input.OrgID, input.ThreadID); err != nil {
		return nil, fmt.Errorf("increment queued sibling message count: %w", err)
	}
	s.recoverLostOwnerAfterQueuedSend(ctx, input.OrgID, input.SessionID, input.ThreadID)
	return &SendMessageResult{Message: msg, AnsweredQuestion: answeredQuestion, AnsweredHumanInput: answeredHumanInput}, nil
}

func (s *Service) recoverLostOwnerAfterQueuedSend(ctx context.Context, orgID, sessionID, threadID uuid.UUID) {
	if s.ownerLoss == nil {
		return
	}
	recoverCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.ownerLoss.RecoverLostOwner(recoverCtx, orgID, sessionID, &threadID); err != nil {
		s.logger.Warn().Err(err).
			Str("session_id", sessionID.String()).
			Str("thread_id", threadID.String()).
			Msg("proactive owner-loss recovery failed after queued send")
	}
}

func (s *Service) createQueuedMessage(ctx context.Context, msg *models.SessionMessage, input SendMessageInput, threadStatus models.ThreadStatus) (*models.SessionQuestion, *models.HumanInputRequest, error) {
	// Durable human-input requests are intentionally not answered here:
	// queued sends do not enqueue a continue_session job, so there is no
	// payload that can carry human_input_request_id to the worker. The
	// orchestrator answers the pending free-text request when it drains the
	// queued message and creates the actual resume job.
	answerPendingQuestion := threadStatus == models.ThreadStatusAwaitingInput && input.UserID != nil && s.questionStore != nil
	if !answerPendingQuestion {
		return nil, nil, s.messageStore.Create(ctx, msg)
	}
	_, answeredQuestion, _, err := s.createMessageInTx(ctx, msg, input, models.Session{}, true, false)
	return answeredQuestion, nil, err
}

func threadStatusCanQueue(status models.ThreadStatus) bool {
	switch status {
	case models.ThreadStatusPending, models.ThreadStatusRunning:
		return true
	default:
		return false
	}
}

// claimThreadForSend is the thread-layer counterpart to claimSessionForSend:
// try ClaimIdleForSession first, fall back to ClaimForResumeInSession when
// the thread is in a resumable terminal/paused status, and otherwise route
// the message to the appropriate queue path.
//
// The four return paths (claimed-from-idle, claimed-from-resume,
// queue-mid-turn, queue-limit-reached, error) are collapsed into a
// return tuple also carries the pre-claim thread status so queued answer
// paths can distinguish a thread resumed from awaiting_input from a generic
// running thread. resolvingComments is passed through because the
// queue-mid-turn path cannot honor comment resolution — the resolution pass
// is keyed on the in-flight turn's pass number, and we cannot atomically
// commit it alongside a queued message that will not be consumed until a
// later turn. Resume claims do not have this problem because the resumed
// turn IS the in-flight one.
func (s *Service) claimThreadForSend(ctx context.Context, input SendMessageInput, resolvingComments bool) (models.SessionThread, models.ThreadStatus, threadClaimOutcome, error) {
	// 1. Try the idle claim first — happy path for a thread that has been
	//    sitting idle waiting for the next user message.
	thread, err := s.threadStore.ClaimIdleForSession(ctx, input.OrgID, input.SessionID, input.ThreadID, models.MaxRunningThreadsPerSession)
	if err == nil {
		if thread.SessionID != input.SessionID {
			s.releaseThread(ctx, input.OrgID, input.ThreadID, "session mismatch after idle claim")
			return models.SessionThread{}, "", 0, ErrThreadNotFound
		}
		return thread, models.ThreadStatusIdle, threadClaimClaimed, nil
	}
	if errors.Is(err, db.ErrThreadRunningLimitReached) {
		return models.SessionThread{}, "", threadClaimQueueLimitReached, nil
	}

	// 2. Idle claim failed for status reasons. Inspect the thread to decide
	//    between queueing mid-turn, resuming a paused/terminal status, or
	//    rejecting the send outright. A non-ErrNoRows error here is a real
	//    DB failure and is surfaced as-is.
	existing, lookupErr := s.threadStore.GetByID(ctx, input.OrgID, input.ThreadID)
	if lookupErr != nil {
		return models.SessionThread{}, "", 0, fmt.Errorf("%w: %w", ErrThreadNotFound, lookupErr)
	}
	existing, lookupErr = visibleThreadInSession(existing, input.SessionID)
	if lookupErr != nil {
		return models.SessionThread{}, "", 0, lookupErr
	}

	// 3. Thread is mid-turn (pending/running): queue the message against the
	//    in-flight turn. Comment resolution is not supported on this path.
	if threadStatusCanQueue(existing.Status) {
		if resolvingComments {
			return models.SessionThread{}, "", 0, ErrThreadNotIdle
		}
		return existing, existing.Status, threadClaimQueueMidTurn, nil
	}

	// 4. Thread is in a resumable terminal/paused status: try to bring it
	//    back to running, mirroring SessionStore.ClaimForResume.
	if threadStatusIsResumable(existing.Status) {
		resumed, resumeErr := s.threadStore.ClaimForResumeInSession(ctx, input.OrgID, input.SessionID, input.ThreadID, models.MaxRunningThreadsPerSession)
		if resumeErr == nil {
			return resumed, existing.Status, threadClaimClaimed, nil
		}
		if errors.Is(resumeErr, db.ErrThreadRunningLimitReached) {
			return models.SessionThread{}, "", threadClaimQueueLimitReached, nil
		}
		// pgx.ErrNoRows here means the status changed under us between the
		// inspect and the resume claim (e.g. a sibling claim raced us). If
		// that race moved the thread into a mid-turn status, queue the
		// message behind the in-flight turn instead of bouncing it.
		if errors.Is(resumeErr, pgx.ErrNoRows) {
			racedThread, racedErr := s.threadStore.GetByID(ctx, input.OrgID, input.ThreadID)
			if racedErr != nil {
				return models.SessionThread{}, "", 0, fmt.Errorf("%w: %w", ErrThreadNotFound, racedErr)
			}
			racedThread, racedErr = visibleThreadInSession(racedThread, input.SessionID)
			if racedErr != nil {
				return models.SessionThread{}, "", 0, racedErr
			}
			if threadStatusCanQueue(racedThread.Status) {
				if resolvingComments {
					return models.SessionThread{}, "", 0, ErrThreadNotIdle
				}
				return racedThread, racedThread.Status, threadClaimQueueMidTurn, nil
			}
			return models.SessionThread{}, "", 0, ErrThreadNotIdle
		}
		return models.SessionThread{}, "", 0, fmt.Errorf("claim thread for resume: %w", resumeErr)
	}

	// 5. Anything else (unexpected status) is treated as not-idle.
	return models.SessionThread{}, "", 0, ErrThreadNotIdle
}

// releaseThread is a small wrapper around UpdateStatus(idle) so failure
// branches that need to undo a claim can do so without duplicating the
// error-logging boilerplate. Failures here are always best-effort — the
// caller already has a primary error to surface.
func (s *Service) releaseThread(ctx context.Context, orgID, threadID uuid.UUID, reason string) {
	if revertErr := s.threadStore.UpdateStatus(ctx, orgID, threadID, models.ThreadStatusIdle); revertErr != nil {
		s.logger.Error().Err(revertErr).
			Str("thread_id", threadID.String()).
			Str("reason", reason).
			Msg("failed to revert thread to idle")
	}
}

// threadStatusIsResumable mirrors models.ResumableThreadStatuses without
// allocating per-call: hot path of SendMessage on a non-idle thread.
func threadStatusIsResumable(status models.ThreadStatus) bool {
	for _, resumable := range models.ResumableThreadStatuses {
		if status == resumable {
			return true
		}
	}
	return false
}

// claimSessionForSend mirrors the ClaimIdle → ClaimForResume → fail ordering
// used by the session-level SendMessage handler. Returns the claimed session
// row, the status to revert to on failure (empty when the session was
// already running due to a sibling tab and no claim was taken), and a
// terminal error when neither claim succeeds.
func (s *Service) claimSessionForSend(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, models.SessionStatus, error) {
	claimed, claimErr := s.sessionStore.ClaimIdle(ctx, orgID, sessionID)
	if claimErr == nil {
		return claimed, models.SessionStatusIdle, nil
	}

	// ClaimIdle failed because the session is not 'idle'. Read the actual
	// status to decide between four branches:
	//   - running (sibling tab is mid-turn): no-op, no revert needed
	//   - sandbox destroyed: surface ErrSessionSnapshotExpired so the
	//     handler can render a 410 Gone (mirrors sessions.go:1835)
	//   - terminal/paused: try ClaimForResume, remember original status
	//   - anything else: surface ErrSessionNotResumable
	existing, getErr := s.sessionStore.GetByID(ctx, orgID, sessionID)
	if getErr != nil {
		return models.Session{}, "", fmt.Errorf("inspect session for claim fallback: %w", getErr)
	}
	if existing.Status == models.SessionStatusRunning {
		s.logger.Debug().
			Str("session_id", sessionID.String()).
			Msg("session already running due to sibling thread; proceeding without re-claim")
		return existing, "", nil
	}
	if existing.SandboxState == models.SandboxStateDestroyed {
		return models.Session{}, "", ErrSessionSnapshotExpired
	}

	resumed, resumeErr := s.sessionStore.ClaimForResume(ctx, orgID, sessionID)
	if resumeErr != nil {
		return models.Session{}, "", fmt.Errorf("%w: %w", ErrSessionNotResumable, resumeErr)
	}
	return resumed, existing.Status, nil
}

// createMessageInTx wraps the message insert plus any of (a) review-comment
// resolution, (b) pending-question answer in a single transaction so the
// user-visible invariants — "submitted comments disappear once the follow-up
// is sent" and "question state stays in sync with the resumed run" — cannot
// be violated by a partial failure between the writes.
//
// Pre-condition: when resolving comments, the caller has already verified
// s.txStarter and s.reviewCommentStore are non-nil (via the
// ErrReviewCommentsNotConfigured guard at the top of SendMessage). When
// answering a question, s.questionStore is non-nil and input.UserID is set.
// The tx itself requires s.txStarter; the awaiting_input path therefore
// needs the txStarter wired even when no comments are involved.
func (s *Service) createMessageInTx(
	ctx context.Context,
	msg *models.SessionMessage,
	input SendMessageInput,
	claimedSession models.Session,
	answerPendingQuestion bool,
	answerPendingHumanInput bool,
) ([]models.SessionReviewComment, *models.SessionQuestion, *models.HumanInputRequest, error) {
	if s.txStarter == nil {
		return nil, nil, nil, fmt.Errorf("tx starter not configured")
	}

	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
			s.logger.Error().Err(rollbackErr).
				Str("session_id", input.SessionID.String()).
				Str("thread_id", input.ThreadID.String()).
				Msg("failed to rollback thread send-message transaction")
		}
	}()

	txMessageStore := db.NewSessionMessageStore(tx)
	if err := txMessageStore.Create(ctx, msg); err != nil {
		return nil, nil, nil, err
	}

	var resolved []models.SessionReviewComment
	if len(input.ResolveReviewCommentIDs) > 0 {
		txCommentStore := db.NewSessionReviewCommentStore(tx)
		// Use the post-claim session state's CurrentTurn so the recorded
		// pass matches what session-level SendMessage records on the same
		// path.
		resolved, err = txCommentStore.ValidateAndResolveByIDs(
			ctx, input.OrgID, input.SessionID, input.ResolveReviewCommentIDs, resolutionPass(&claimedSession))
		if err != nil {
			return nil, nil, nil, err
		}
	}

	var answered *models.SessionQuestion
	if answerPendingQuestion {
		// Bind the question store to the tx so the answer commits or
		// rolls back atomically with the message and resolved comments.
		// pgx.ErrNoRows is benign here: it just means there is no pending
		// question to answer (the orchestrator already cleared it), so we
		// log and proceed instead of failing the user's send.
		txQuestionStore := db.NewSessionQuestionStore(tx)
		question, qerr := txQuestionStore.AnswerLatestPendingBySession(ctx, input.OrgID, input.SessionID, input.Message, *input.UserID)
		if qerr != nil {
			if errors.Is(qerr, pgx.ErrNoRows) {
				s.logger.Warn().
					Str("session_id", input.SessionID.String()).
					Msg("awaiting_input session resumed without a pending question to answer")
			} else {
				return nil, nil, nil, fmt.Errorf("answer pending question: %w", qerr)
			}
		} else {
			answered = &question
		}
	}

	var answeredHumanInput *models.HumanInputRequest
	if answerPendingHumanInput {
		txHumanInputStore := db.NewSessionHumanInputRequestStore(tx)
		request, herr := txHumanInputStore.AnswerLatestPendingFreeTextByThread(ctx, input.OrgID, input.SessionID, input.ThreadID, input.Message, *input.UserID)
		if herr != nil {
			if errors.Is(herr, pgx.ErrNoRows) {
				s.logger.Warn().
					Str("session_id", input.SessionID.String()).
					Str("thread_id", input.ThreadID.String()).
					Msg("awaiting_input thread resumed without a pending free-text human input request to answer")
			} else {
				return nil, nil, nil, fmt.Errorf("answer pending human input request: %w", herr)
			}
		} else {
			answeredHumanInput = &request
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("commit tx: %w", err)
	}
	committed = true
	return resolved, answered, answeredHumanInput, nil
}

// revertAfterSendFailure puts the session and thread back to their pre-send
// state on a best-effort basis after a SendMessage failure. The thread always
// reverts to idle. The session reverts to revertStatus, which is captured at
// claim time so a resumed-from-completed session goes back to completed
// rather than incorrectly being parked at idle. An empty revertStatus means
// the claim path did not touch the session (sibling-running case) and the
// session row should be left alone — reverting it to idle would yank the
// running sibling out from under the orchestrator.
//
// Logs each revert error individually so partial reverts are debuggable, but
// never returns — callers always have a primary error to surface.
func (s *Service) revertAfterSendFailure(ctx context.Context, orgID, sessionID, threadID uuid.UUID, revertStatus models.SessionStatus, reason string) {
	if revertStatus != "" {
		if revertErr := s.sessionStore.UpdateStatus(ctx, orgID, sessionID, revertStatus); revertErr != nil {
			s.logger.Error().Err(revertErr).
				Str("session_id", sessionID.String()).
				Str("revert_status", string(revertStatus)).
				Str("reason", reason).
				Msg("failed to revert session after thread send failure")
		}
	}
	if revertErr := s.threadStore.UpdateStatus(ctx, orgID, threadID, models.ThreadStatusIdle); revertErr != nil {
		s.logger.Error().Err(revertErr).
			Str("thread_id", threadID.String()).
			Str("reason", reason).
			Msg("failed to revert thread to idle after send failure")
	}
}

// resolutionPass mirrors handlers.currentResolutionPass: the comment is
// being addressed during the current session turn (with a fallback to 1 for
// not-yet-started sessions). Kept in this package to avoid an import cycle
// with handlers; the two functions intentionally stay in lockstep.
func resolutionPass(session *models.Session) int {
	if session == nil || session.CurrentTurn == 0 {
		return 1
	}
	return session.CurrentTurn
}

// EndThread transitions an active thread to completed.
func (s *Service) EndThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error) {
	thread, err := s.threadStore.GetByID(ctx, orgID, threadID)
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	thread, err = visibleThreadInSession(thread, sessionID)
	if err != nil {
		return models.SessionThread{}, err
	}

	// Only pending, idle, running, or awaiting_input threads can be ended.
	switch thread.Status {
	case models.ThreadStatusPending, models.ThreadStatusIdle, models.ThreadStatusRunning, models.ThreadStatusAwaitingInput:
		// OK
	default:
		return models.SessionThread{}, ErrThreadCannotBeEnded
	}

	if err := s.threadStore.UpdateStatus(ctx, orgID, threadID, models.ThreadStatusCompleted); err != nil {
		return models.SessionThread{}, fmt.Errorf("update status: %w", err)
	}

	thread.Status = models.ThreadStatusCompleted
	return thread, nil
}

// GetMessages returns messages for a thread, validating it belongs to the given session.
func (s *Service) GetMessages(ctx context.Context, orgID, sessionID, threadID uuid.UUID) ([]models.SessionMessage, error) {
	thread, err := s.threadStore.GetByID(ctx, orgID, threadID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	if _, err := visibleThreadInSession(thread, sessionID); err != nil {
		return nil, err
	}

	messages, err := s.messageStore.ListByThread(ctx, orgID, threadID)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	return messages, nil
}

// GetMessageWindow returns a bottom-first message window for a thread,
// validating it belongs to the given session before querying messages.
func (s *Service) GetMessageWindow(ctx context.Context, orgID, sessionID, threadID uuid.UUID, opts db.SessionMessageWindowOptions) (MessageWindowResult, error) {
	thread, err := s.threadStore.GetByID(ctx, orgID, threadID)
	if err != nil {
		return MessageWindowResult{}, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	thread, err = visibleThreadInSession(thread, sessionID)
	if err != nil {
		return MessageWindowResult{}, err
	}

	window, err := s.messageStore.ListWindowByThread(ctx, orgID, threadID, opts)
	if err != nil {
		return MessageWindowResult{}, fmt.Errorf("list message window: %w", err)
	}
	return MessageWindowResult{Window: window, ThreadStatus: thread.Status}, nil
}

// GetLogs returns logs for a thread, validating it belongs to the given session.
func (s *Service) GetLogs(ctx context.Context, orgID, sessionID, threadID uuid.UUID, opts db.SessionLogFilterOptions) ([]models.SessionLog, error) {
	thread, err := s.threadStore.GetByID(ctx, orgID, threadID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	if _, err := visibleThreadInSession(thread, sessionID); err != nil {
		return nil, err
	}

	var logs []models.SessionLog
	if len(opts.TurnNumbers) > 0 {
		logs, err = s.logStore.ListByThreadTurns(ctx, orgID, threadID, opts.TurnNumbers)
	} else {
		logs, err = s.logStore.ListByThread(ctx, orgID, threadID)
	}
	if err != nil {
		return nil, fmt.Errorf("list logs: %w", err)
	}
	return logs, nil
}
