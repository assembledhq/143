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
	ErrSessionNotFound     = errors.New("session not found")
	ErrSessionTerminal     = errors.New("cannot add threads to a completed session")
	ErrInvalidAgentType    = errors.New("invalid agent type")
	ErrInvalidModel        = errors.New("invalid model")
	ErrEnqueueFailed       = errors.New("enqueue failed")
	ErrThreadNotFound      = errors.New("thread not found")
	ErrThreadNotEditable   = errors.New("thread is not editable")
	ErrThreadNotIdle       = errors.New("thread must be idle to send a message")
	ErrActiveThreadExists  = errors.New("another thread is already active")
	ErrThreadCannotBeEnded = errors.New("thread cannot be ended in its current state")
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
type SessionStore interface {
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	ClaimIdle(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status string) error
}

// ThreadStore defines the thread DB operations needed by the thread service.
type ThreadStore interface {
	Create(ctx context.Context, thread *models.SessionThread, maxThreads int) error
	GetByID(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionThread, error)
	ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error)
	ClaimIdleForSession(ctx context.Context, orgID, sessionID, threadID uuid.UUID, maxRunning int) (models.SessionThread, error)
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

// MessageStore defines the message DB operations needed by the thread service.
type MessageStore interface {
	Create(ctx context.Context, msg *models.SessionMessage) error
	ListByThread(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionMessage, error)
}

// LogStore defines the log DB operations needed by the thread service.
type LogStore interface {
	ListByThread(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionLog, error)
}

// JobStore defines the job DB operations needed by the thread service.
type JobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
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

type UpdateThreadInput struct {
	SessionID uuid.UUID
	OrgID     uuid.UUID
	ThreadID  uuid.UUID
	AgentType string
	Model     string
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
}

// SendMessageResult carries everything callers need to finish handling a
// successful thread-message send: the created message, plus any review
// comments that were resolved as part of the same transaction. The handler
// uses ResolvedComments to emit one audit row per resolved comment after
// the tx commits — matching the post-commit audit pattern of session-level
// SendMessage.
type SendMessageResult struct {
	Message          *models.SessionMessage
	ResolvedComments []models.SessionReviewComment
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
	txStarter          db.TxStarter                  // optional — required for SendMessage with ResolveReviewCommentIDs
	reviewCommentStore *db.SessionReviewCommentStore // optional — required for SendMessage with ResolveReviewCommentIDs
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

// isSessionAlreadyRunning is the fallback predicate used by SendMessage when
// a session-level ClaimIdle fails: if the session is already in 'running'
// state because another tab is mid-turn, the new tab does not need to claim.
// Returns true on the happy path; on any error we err on the side of caution
// and propagate the original claim failure.
func isSessionAlreadyRunning(ctx context.Context, store SessionStore, orgID, sessionID uuid.UUID) bool {
	session, err := store.GetByID(ctx, orgID, sessionID)
	if err != nil {
		return false
	}
	return session.Status == string(models.SessionStatusRunning)
}

func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "pr_created", "failed", "cancelled", "skipped":
		return true
	}
	return false
}

// CreateThread validates inputs and creates a blank idle thread.
func (s *Service) CreateThread(ctx context.Context, input CreateThreadInput) (*models.SessionThread, error) {
	// Verify session exists and belongs to org.
	session, err := s.sessionStore.GetByID(ctx, input.OrgID, input.SessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionNotFound, err)
	}

	// Only allow adding threads to active sessions.
	if isTerminalStatus(session.Status) {
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
	if isTerminalStatus(session.Status) {
		return nil, ErrSessionTerminal
	}

	thread, err := s.threadStore.GetByID(ctx, input.OrgID, input.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	if thread.SessionID != input.SessionID {
		return nil, ErrThreadNotFound
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
	if input.Model != "" {
		if err := models.ValidateModelForAgentType(agentType, input.Model); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidModel, err)
		}
		thread.ModelOverride = &input.Model
	} else if input.AgentType != "" {
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
	if thread.SessionID != sessionID {
		return models.SessionThread{}, ErrThreadNotFound
	}
	return thread, nil
}

// SendMessage claims an idle thread, creates a message, and enqueues a
// continue_session job. When ResolveReviewCommentIDs is non-empty, the
// message create and the comment resolution share a single transaction so
// the user-visible invariant — "submitted comments disappear once the
// follow-up message is sent" — holds even if the request fails partway
// through.
//
// ClaimIdleForSession serializes sibling-thread admission in the database
// while allowing up to MaxRunningThreadsPerSession concurrent tabs. The
// session-level ClaimIdle is best-effort: when another tab is already
// running, the session is already in 'running' state and the orchestrator's
// idempotent UpdateStatus("running") handles the rest. If subsequent message
// creation or job enqueue fails we best-effort revert the thread to idle.
func (s *Service) SendMessage(ctx context.Context, input SendMessageInput) (*SendMessageResult, error) {
	// Reject early — before any state mutation — when the caller asked to
	// resolve comments but the service was constructed without the plumbing
	// to do so. Pushing this check above the claim avoids leaving the thread
	// stuck in 'running' if the configuration is wrong.
	resolvingComments := len(input.ResolveReviewCommentIDs) > 0
	if resolvingComments && (s.txStarter == nil || s.reviewCommentStore == nil) {
		return nil, ErrReviewCommentsNotConfigured
	}

	thread, err := s.threadStore.ClaimIdleForSession(ctx, input.OrgID, input.SessionID, input.ThreadID, models.MaxRunningThreadsPerSession)
	if err != nil {
		if errors.Is(err, db.ErrThreadRunningLimitReached) {
			return nil, ErrRunningLimitReached
		}
		// Check if thread exists at all to provide a better error.
		existing, lookupErr := s.threadStore.GetByID(ctx, input.OrgID, input.ThreadID)
		if lookupErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrThreadNotFound, lookupErr)
		}
		if existing.SessionID != input.SessionID {
			return nil, ErrThreadNotFound
		}
		// The target tab itself is busy with its own turn. The composer
		// should fall back to queueing — we cannot interleave two turns on
		// one thread.
		return nil, ErrThreadNotIdle
	}

	// Verify thread belongs to the session.
	if thread.SessionID != input.SessionID {
		if revertErr := s.threadStore.UpdateStatus(ctx, input.OrgID, input.ThreadID, models.ThreadStatusIdle); revertErr != nil {
			s.logger.Error().Err(revertErr).Str("thread_id", input.ThreadID.String()).Msg("failed to revert thread to idle after session mismatch")
		}
		return nil, ErrThreadNotFound
	}

	// Best-effort session-level claim. Treat ErrNoRows-style failures as
	// "already running due to a sibling tab" and proceed — the session
	// state machine is idempotent at running. Any other error reverts the
	// claim and propagates.
	if _, claimErr := s.sessionStore.ClaimIdle(ctx, input.OrgID, input.SessionID); claimErr != nil {
		if isSessionAlreadyRunning(ctx, s.sessionStore, input.OrgID, input.SessionID) {
			s.logger.Debug().
				Str("session_id", input.SessionID.String()).
				Str("thread_id", input.ThreadID.String()).
				Msg("session already running due to sibling thread; proceeding without re-claim")
		} else {
			if revertErr := s.threadStore.UpdateStatus(ctx, input.OrgID, input.ThreadID, models.ThreadStatusIdle); revertErr != nil {
				s.logger.Error().Err(revertErr).Str("thread_id", input.ThreadID.String()).Msg("failed to revert thread to idle after parent session claim failure")
			}
			return nil, fmt.Errorf("claim parent session: %w", claimErr)
		}
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

	var resolvedComments []models.SessionReviewComment
	if resolvingComments {
		resolvedComments, err = s.createMessageWithResolvedComments(ctx, msg, input)
	} else {
		err = s.messageStore.Create(ctx, msg)
	}
	if err != nil {
		s.revertAfterSendFailure(ctx, input.OrgID, input.SessionID, input.ThreadID, "message creation failure")
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

	// Reuse the session continuation worker for phase 1. The latest user
	// message carries thread_id, so the orchestrator attributes assistant
	// messages and streamed logs back to this tab while still operating on the
	// single shared sandbox. Dedupe at the session level — only one
	// continue_session can hold the shared sandbox at a time, regardless of
	// which thread fired it.
	dedupeKey := db.ContinueSessionDedupeKey(thread.SessionID)
	payload := map[string]string{
		"session_id": thread.SessionID.String(),
		"thread_id":  input.ThreadID.String(),
		"org_id":     input.OrgID.String(),
	}
	if _, err := s.jobStore.Enqueue(ctx, input.OrgID, "agent", "continue_session", payload, 5, &dedupeKey); err != nil {
		// Note: we do NOT roll back the resolved comments here. The message
		// has been committed and is durably in the timeline; the orchestrator
		// will retry the enqueue on the next dedupe-eligible event. Reverting
		// the resolved comments would create a worse inconsistency where the
		// user sees their addressed comments re-appear despite the message
		// already being in the conversation.
		s.revertAfterSendFailure(ctx, input.OrgID, input.SessionID, input.ThreadID, "enqueue failure")
		return nil, fmt.Errorf("%w: %w", ErrEnqueueFailed, err)
	}

	return &SendMessageResult{
		Message:          msg,
		ResolvedComments: resolvedComments,
	}, nil
}

// createMessageWithResolvedComments wraps the message insert and the
// review-comment resolution in a single transaction so the
// "addressing-comments → message-sent → comments-resolved" invariant cannot
// be violated by a partial failure between the two writes.
//
// Pre-condition: caller has already verified s.txStarter and
// s.reviewCommentStore are non-nil (via the ErrReviewCommentsNotConfigured
// guard at the top of SendMessage).
func (s *Service) createMessageWithResolvedComments(
	ctx context.Context,
	msg *models.SessionMessage,
	input SendMessageInput,
) ([]models.SessionReviewComment, error) {
	// Fetch the session inside the same critical section to read CurrentTurn
	// for the resolution pass. We use the post-claim session state (the
	// ClaimIdle above already moved it to 'running' if it was idle), so the
	// recorded pass matches what session-level SendMessage records on the
	// same path.
	session, err := s.sessionStore.GetByID(ctx, input.OrgID, input.SessionID)
	if err != nil {
		return nil, fmt.Errorf("get session for resolution pass: %w", err)
	}

	tx, err := s.txStarter.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
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
	txCommentStore := db.NewSessionReviewCommentStore(tx)

	if err := txMessageStore.Create(ctx, msg); err != nil {
		return nil, err
	}
	resolved, err := txCommentStore.ValidateAndResolveByIDs(
		ctx, input.OrgID, input.SessionID, input.ResolveReviewCommentIDs, resolutionPass(&session))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}
	committed = true
	return resolved, nil
}

// revertAfterSendFailure puts the session and thread back to idle on a
// best-effort basis after a SendMessage failure. Logs each revert error
// individually so partial reverts are debuggable, but never returns —
// callers always have a primary error to surface.
func (s *Service) revertAfterSendFailure(ctx context.Context, orgID, sessionID, threadID uuid.UUID, reason string) {
	if revertErr := s.sessionStore.UpdateStatus(ctx, orgID, sessionID, string(models.SessionStatusIdle)); revertErr != nil {
		s.logger.Error().Err(revertErr).
			Str("session_id", sessionID.String()).
			Str("reason", reason).
			Msg("failed to revert session to idle after thread send failure")
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

	if thread.SessionID != sessionID {
		return models.SessionThread{}, ErrThreadNotFound
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
	if thread.SessionID != sessionID {
		return nil, ErrThreadNotFound
	}

	messages, err := s.messageStore.ListByThread(ctx, orgID, threadID)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	return messages, nil
}

// GetLogs returns logs for a thread, validating it belongs to the given session.
func (s *Service) GetLogs(ctx context.Context, orgID, sessionID, threadID uuid.UUID) ([]models.SessionLog, error) {
	thread, err := s.threadStore.GetByID(ctx, orgID, threadID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	if thread.SessionID != sessionID {
		return nil, ErrThreadNotFound
	}

	logs, err := s.logStore.ListByThread(ctx, orgID, threadID)
	if err != nil {
		return nil, fmt.Errorf("list logs: %w", err)
	}
	return logs, nil
}
