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
	ErrThreadInboxBackpressure     = errors.New("too many undelivered thread messages")
)

const (
	deliverThreadInboxJobType = "deliver_thread_inbox"
	deliverThreadInboxQueue   = "agent"
	cancelThreadJobType       = "cancel_thread"
	maxThreadInboxPending     = 200
	maxSessionInboxPending    = 1000
	maxThreadMessageBytes     = 256 * 1024
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
	ListByIDs(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) ([]models.Session, error)
	ClaimIdle(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	ClaimForResume(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	UpdateStatus(ctx context.Context, orgID, sessionID uuid.UUID, status models.SessionStatus) error
	// RejectIfAutomationOwned returns a *models.SessionAutomationOwnedError
	// when a per-target automation generation owns the session (design doc
	// 125). Such a session accepts no human turn: its thread belongs to the
	// automation's next turn, and the person's way out is the target's
	// Reset action.
	RejectIfAutomationOwned(ctx context.Context, orgID, sessionID uuid.UUID) error
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
	MarkCancelRequestedBySessions(ctx context.Context, orgID uuid.UUID, sessionIDs []uuid.UUID) ([]models.SessionThread, error)
}

// FileEventStore defines the operations the thread service needs for
// file-attribution surfaces such as overlap detection.
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
	GetByID(ctx context.Context, orgID uuid.UUID, id int64) (models.SessionMessage, error)
	ListByThread(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionMessage, error)
	ListWindowByThread(ctx context.Context, orgID, threadID uuid.UUID, opts db.SessionMessageWindowOptions) (db.SessionMessageWindow, error)
}

type messageStoreWithSource interface {
	CreateWithSource(ctx context.Context, msg *models.SessionMessage) error
}

type threadStoreWithProvenance interface {
	CreateWithProvenance(ctx context.Context, thread *models.SessionThread, maxThreads int) error
}

// LogStore defines the log DB operations needed by the thread service.
type LogStore interface {
	ListByThread(ctx context.Context, orgID, threadID uuid.UUID) ([]models.SessionLog, error)
	ListByThreadTurns(ctx context.Context, orgID, threadID uuid.UUID, turnNumbers []int) ([]models.SessionLog, error)
	ListByThreadLatestTurns(ctx context.Context, orgID, threadID uuid.UUID, latestTurns int) ([]models.SessionLog, error)
}

// TranscriptStore defines the DB operations needed to serve the transcript
// window endpoint.
type TranscriptStore interface {
	ListThreadWindow(ctx context.Context, orgID, threadID uuid.UUID, opts db.SessionTranscriptWindowOptions) (db.SessionTranscriptWindow, error)
}

// JobStore defines the job DB operations needed by the thread service.
type JobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
	EnqueueWithOpts(ctx context.Context, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error)
}

// ThreadInboxStore defines the durable per-thread input log used by the
// runtime control plane. When wired, every accepted user message gets an inbox
// entry before any worker enqueue is attempted.
type ThreadInboxStore interface {
	AppendForMessage(ctx context.Context, orgID uuid.UUID, params db.AppendThreadInboxEntryParams) (models.ThreadInboxEntry, error)
	GetByClientMessageID(ctx context.Context, orgID, threadID uuid.UUID, clientMessageID string) (models.ThreadInboxEntry, error)
	ListDeliverySummariesBySession(ctx context.Context, orgID, sessionID uuid.UUID) (map[uuid.UUID]models.ThreadInboxDeliverySummary, error)
	GetDeliverySummaryByThread(ctx context.Context, orgID, threadID uuid.UUID) (models.ThreadInboxDeliverySummary, error)
	ListRecoverableByThread(ctx context.Context, orgID, threadID uuid.UUID, limit int) ([]models.ThreadInboxEntry, error)
	RetryRecoverable(ctx context.Context, orgID, threadID, entryID uuid.UUID, allowUnknownDelivery bool) (models.ThreadInboxEntry, error)
	CountPendingByThread(ctx context.Context, orgID, threadID uuid.UUID) (int, error)
	CountPendingBySession(ctx context.Context, orgID, sessionID uuid.UUID) (int, error)
}

type ThreadRuntimeOwnerStore interface {
	GetActiveByThread(ctx context.Context, orgID, threadID uuid.UUID) (models.ThreadRuntime, error)
}

// CreateThreadInput holds the input for creating a new thread.
type CreateThreadInput struct {
	// ID is an internal retained-start identity. HTTP callers never populate it.
	ID                uuid.UUID
	SessionID         uuid.UUID
	OrgID             uuid.UUID
	AgentType         string
	Model             string
	ReasoningEffort   *models.ReasoningEffort
	Label             string
	Instructions      string
	FileScope         []string
	ExecutionMode     models.ThreadExecutionMode
	FilesystemMode    models.ThreadFilesystemMode
	CreatedBySource   models.ThreadCreatedBySource
	CreatedByThreadID *uuid.UUID
}

type ListThreadsOptions struct {
	IncludeArchived bool
}

type threadStoreWithListOptions interface {
	ListBySessionWithOptions(ctx context.Context, orgID, sessionID uuid.UUID, includeArchived bool) ([]models.SessionThread, error)
}

// UpdateThreadInput patches an editable (idle, current_turn=0) thread. Model
// uses *string to distinguish three states the handler maps from the wire:
//   - nil:       field absent from the patch — keep the existing override
//   - non-nil "": field present as JSON null or empty string — clear the override
//   - non-nil v: field present with a value — set/validate to v
//
// ReasoningEffort is internal-only today: nil preserves the existing value and
// a non-nil value replaces it after agent compatibility validation.
type UpdateThreadInput struct {
	SessionID       uuid.UUID
	OrgID           uuid.UUID
	ThreadID        uuid.UUID
	AgentType       string
	Model           *string
	ReasoningEffort *models.ReasoningEffort
	Label           string
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
	SessionID   uuid.UUID
	OrgID       uuid.UUID
	ThreadID    uuid.UUID
	ChangesetID *uuid.UUID
	// LegacyChangesetPolicy preserves adapter-only v0 preflight. It is not
	// semantic message intent and is ignored by the active protocol.
	LegacyChangesetPolicy   LegacyChangesetPolicy
	ClientMessageID         string
	UserID                  *uuid.UUID
	Message                 string
	Images                  []string
	References              models.SessionInputReferences
	Commands                models.SessionInputCommands
	MessageSource           models.SessionMessageSource
	PlanMode                bool
	ResolveReviewCommentIDs []uuid.UUID
	PostSuccessAction       string
	PostSuccessAuthorMode   string
	// ContinuationDedupeKeyOverride is for system-generated follow-up turns
	// created while the current same-thread worker job is still marked running.
	// User sends should leave it nil so rapid-fire messages keep normal thread
	// dedupe behavior.
	ContinuationDedupeKeyOverride *string
	// CallerCommand is private correlation transport, not semantic intent or
	// admission authority. It must never be exposed through public JSON.
	CallerCommand *CallerSendCommandProof `json:"-"`
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
	// Admission is present only for a canonical v1 receipt. It exposes no
	// start permit and keeps waiting/replayed acceptance distinct from launch.
	Admission          *db.Admission
	Message            *models.SessionMessage
	InboxEntry         *models.ThreadInboxEntry
	ThreadStatus       models.ThreadStatus
	DeliveryState      models.ThreadInboxDeliveryState
	ResolvedComments   []models.SessionReviewComment
	AnsweredQuestion   *models.SessionQuestion
	AnsweredHumanInput *models.HumanInputRequest
}

type MessageWindowResult struct {
	Window       db.SessionMessageWindow
	ThreadStatus models.ThreadStatus
}

// TranscriptWindowResult wraps the store result with thread status.
type TranscriptWindowResult struct {
	Window       db.SessionTranscriptWindow
	ThreadStatus models.ThreadStatus
}

// Service handles thread business logic.
type Service struct {
	threadStore        ThreadStore
	sessionStore       SessionStore
	messageStore       MessageStore
	logStore           LogStore
	jobStore           JobStore
	send               SendDependencies
	inboxStore         ThreadInboxStore              // optional — enables durable per-thread input delivery state
	runtimeOwnerStore  ThreadRuntimeOwnerStore       // optional — pins delivery notifications to the live runtime owner
	fileEvents         FileEventStore                // optional — enables overlap and attribution surfaces
	canceller          ThreadCanceller               // optional — enables in-flight SIGINT
	txStarter          db.TxStarter                  // optional — required for SendMessage with ResolveReviewCommentIDs or awaiting_input answer
	reviewCommentStore *db.SessionReviewCommentStore // optional — required for SendMessage with ResolveReviewCommentIDs
	questionStore      QuestionStore                 // optional — required to answer pending questions on awaiting_input resume
	humanInputStore    HumanInputRequestStore        // optional — required to answer pending human-input requests on awaiting_input resume
	ownerLoss          OwnerLossOrchestrator         // optional — proactively recovers lost runtime owners after queue-only sends
	transcriptStore    TranscriptStore               // optional — required for GetTranscriptWindow
	logger             zerolog.Logger
}

// NewService requires explicit send transaction, persistence and postcommit
// dependencies. Missing or typed-nil send dependencies are construction errors,
// never a legacy fallback. Optional surfaces remain configured through setters;
// their legacy txStarter is independent of the mandatory send transaction runner.
func NewService(
	threadStore ThreadStore,
	sessionStore SessionStore,
	messageStore MessageStore,
	logStore LogStore,
	jobStore JobStore,
	send SendDependencies,
	logger zerolog.Logger,
) (*Service, error) {
	if err := send.Validate(); err != nil {
		return nil, fmt.Errorf("construct thread service: %w", err)
	}
	return &Service{
		threadStore:  threadStore,
		sessionStore: sessionStore,
		messageStore: messageStore,
		logStore:     logStore,
		jobStore:     jobStore,
		send:         send,
		logger:       logger,
	}, nil
}

// SetFileEventStore wires the optional file-event store post-construction.
// Kept separate so the existing NewService signature does not change and so
// tests can omit it.
func (s *Service) SetFileEventStore(store FileEventStore) {
	s.fileEvents = store
}

// SetThreadInboxStore wires the durable per-thread inbox. The inbox is the
// platform-owned source of truth for accepted input delivery; session_messages
// remains the user-facing transcript.
func (s *Service) SetThreadInboxStore(store ThreadInboxStore, txStarter ...db.TxStarter) {
	s.inboxStore = store
	if len(txStarter) > 0 && txStarter[0] != nil {
		s.txStarter = txStarter[0]
	}
}

func (s *Service) SetThreadRuntimeStore(store ThreadRuntimeOwnerStore) {
	s.runtimeOwnerStore = store
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

// SetTranscriptStore wires the optional transcript store required for
// GetTranscriptWindow.
func (s *Service) SetTranscriptStore(store TranscriptStore) {
	s.transcriptStore = store
}

// SetHumanInputRequestStore wires the optional durable human-input request
// store. Configure the transaction starter through SetThreadInboxStore or
// SetReviewCommentResolver so the implicit answer can commit atomically with
// the message row.
func (s *Service) SetHumanInputRequestStore(store HumanInputRequestStore) {
	s.humanInputStore = store
}

// CreateThread validates inputs and creates a blank idle thread.
func (s *Service) CreateThread(ctx context.Context, input CreateThreadInput) (*models.SessionThread, error) {
	if input.ID != uuid.Nil {
		if existing, found, err := s.adoptRetainedThread(ctx, input); err != nil || found {
			return existing, err
		}
	}
	thread, err := s.prepareThreadCreation(ctx, input)
	if err != nil {
		return nil, err
	}

	var createErr error
	if input.ID != uuid.Nil {
		// adoptRetainedThread already required this explicit capability. Never
		// fall back to ordinary creation, which would allocate another identity.
		createErr = s.threadStore.(retainedThreadStore).CreateWithID(ctx, input.OrgID, thread, models.MaxThreadsPerSession)
	} else {
		createErr = s.createThread(ctx, thread, models.MaxThreadsPerSession)
	}
	if createErr != nil {
		if errors.Is(createErr, db.ErrThreadLimitReached) {
			return nil, db.ErrThreadLimitReached
		}
		return nil, fmt.Errorf("create thread: %w", createErr)
	}

	return thread, nil
}

// prepareThreadCreation is shared by read-only retained preparation and the
// actual row creation path. The store retains its visible-capacity insert predicate.
func (s *Service) prepareThreadCreation(ctx context.Context, input CreateThreadInput) (*models.SessionThread, error) {
	if err := s.rejectIfCodeReviewOwned(ctx, input.OrgID, input.SessionID); err != nil {
		return nil, err
	}
	// A sibling thread on an automation-owned session would share the
	// checkout the automation's turn is working in.
	if err := s.sessionStore.RejectIfAutomationOwned(ctx, input.OrgID, input.SessionID); err != nil {
		return nil, err
	}
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
	if input.ReasoningEffort != nil {
		if err := input.ReasoningEffort.Validate(); err != nil {
			return nil, err
		}
		if agentType.SupportsReasoningEffort() && !agentType.SupportsReasoningEffortLevel(*input.ReasoningEffort) {
			return nil, fmt.Errorf("reasoning effort %q is not supported by agent type %q", *input.ReasoningEffort, agentType)
		}
	}
	executionMode := input.ExecutionMode
	if executionMode == "" {
		executionMode = models.ThreadExecutionModeWork
	}
	if err := executionMode.Validate(); err != nil {
		return nil, err
	}
	filesystemMode := input.FilesystemMode
	if filesystemMode == "" {
		filesystemMode = models.ThreadFilesystemModeReadWrite
	}
	if err := filesystemMode.Validate(); err != nil {
		return nil, err
	}

	var instructions *string
	if input.Instructions != "" {
		instructions = &input.Instructions
	}

	return &models.SessionThread{
		ID:                input.ID,
		SessionID:         input.SessionID,
		OrgID:             input.OrgID,
		AgentType:         agentType,
		ModelOverride:     modelOverride,
		ReasoningEffort:   input.ReasoningEffort,
		Label:             input.Label,
		Instructions:      instructions,
		FileScope:         input.FileScope,
		Status:            models.ThreadStatusIdle,
		CreatedBySource:   input.CreatedBySource,
		CreatedByThreadID: input.CreatedByThreadID,
		ExecutionMode:     executionMode,
		FilesystemMode:    filesystemMode,
	}, nil
}

func (s *Service) createThread(ctx context.Context, thread *models.SessionThread, maxThreads int) error {
	if thread.CreatedBySource != "" || thread.CreatedByThreadID != nil {
		if store, ok := s.threadStore.(threadStoreWithProvenance); ok {
			return store.CreateWithProvenance(ctx, thread, maxThreads)
		}
	}
	return s.threadStore.Create(ctx, thread, maxThreads)
}

func (s *Service) createMessage(ctx context.Context, store MessageStore, msg *models.SessionMessage) error {
	if msg.Source != "" {
		if withSource, ok := store.(messageStoreWithSource); ok {
			return withSource.CreateWithSource(ctx, msg)
		}
	}
	return store.Create(ctx, msg)
}

func (s *Service) UpdateThread(ctx context.Context, input UpdateThreadInput) (*models.SessionThread, error) {
	if err := s.rejectIfCodeReviewOwned(ctx, input.OrgID, input.SessionID); err != nil {
		return nil, err
	}
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
	if input.ReasoningEffort != nil {
		if *input.ReasoningEffort == "" {
			return nil, fmt.Errorf("reasoning effort must be non-empty")
		}
		if err := input.ReasoningEffort.Validate(); err != nil {
			return nil, err
		}
		if agentType.SupportsReasoningEffort() && !agentType.SupportsReasoningEffortLevel(*input.ReasoningEffort) {
			return nil, fmt.Errorf("reasoning effort %q is not supported by agent type %q", *input.ReasoningEffort, agentType)
		}
		reasoningEffort := *input.ReasoningEffort
		thread.ReasoningEffort = &reasoningEffort
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
	if err := s.rejectIfCodeReviewOwned(ctx, orgID, sessionID); err != nil {
		return models.SessionThread{}, err
	}
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
			if isActiveStatus(target.Status) || target.PendingMessageCount > 0 {
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
	return s.ListThreadsWithOptions(ctx, orgID, sessionID, ListThreadsOptions{})
}

func (s *Service) ListThreadsWithOptions(ctx context.Context, orgID, sessionID uuid.UUID, opts ListThreadsOptions) ([]models.SessionThread, error) {
	// Verify session exists and belongs to org.
	if _, err := s.sessionStore.GetByID(ctx, orgID, sessionID); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSessionNotFound, err)
	}

	var threads []models.SessionThread
	var err error
	if opts.IncludeArchived {
		withOptions, ok := s.threadStore.(threadStoreWithListOptions)
		if !ok {
			return nil, fmt.Errorf("list threads with archived: store does not support include_archived")
		}
		threads, err = withOptions.ListBySessionWithOptions(ctx, orgID, sessionID, true)
	} else {
		threads, err = s.threadStore.ListBySession(ctx, orgID, sessionID)
	}
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	if err := s.attachInboxDeliverySummaries(ctx, orgID, sessionID, threads); err != nil {
		return nil, err
	}
	return threads, nil
}

// GetThread returns a single thread, validating it belongs to the given session.
func (s *Service) GetThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error) {
	thread, err := s.threadStore.GetByID(ctx, orgID, threadID)
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	thread, err = visibleThreadInSession(thread, sessionID)
	if err != nil {
		return models.SessionThread{}, err
	}
	if err := s.attachInboxDeliverySummary(ctx, orgID, &thread); err != nil {
		return models.SessionThread{}, err
	}
	return thread, nil
}

func (s *Service) attachInboxDeliverySummaries(ctx context.Context, orgID, sessionID uuid.UUID, threads []models.SessionThread) error {
	if s.inboxStore == nil || len(threads) == 0 {
		return nil
	}
	summaries, err := s.inboxStore.ListDeliverySummariesBySession(ctx, orgID, sessionID)
	if err != nil {
		return fmt.Errorf("list thread inbox delivery summaries: %w", err)
	}
	for i := range threads {
		summary, ok := summaries[threads[i].ID]
		if !ok {
			summary = models.ThreadInboxDeliverySummary{ThreadID: threads[i].ID}
			summary.Normalize()
		}
		threads[i].InboxDelivery = &summary
	}
	return nil
}

func (s *Service) attachInboxDeliverySummary(ctx context.Context, orgID uuid.UUID, thread *models.SessionThread) error {
	if s.inboxStore == nil || thread == nil {
		return nil
	}
	summary, err := s.inboxStore.GetDeliverySummaryByThread(ctx, orgID, thread.ID)
	if err != nil {
		return fmt.Errorf("get thread inbox delivery summary: %w", err)
	}
	thread.InboxDelivery = &summary
	return nil
}

// threadClaimOutcome enumerates the successful legacy claim and queue branches.
// The "claimed"
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

func (s *Service) rejectIfCodeReviewOwned(ctx context.Context, orgID, sessionID uuid.UUID) error {
	owner, ok := s.sessionStore.(interface {
		RejectIfCodeReviewOwned(context.Context, uuid.UUID, uuid.UUID) error
	})
	if !ok {
		return nil
	}
	err := owner.RejectIfCodeReviewOwned(ctx, orgID, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", ErrSessionNotFound, err)
	}
	return err
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

func (s *Service) enqueueLiveThreadInboxDelivery(ctx context.Context, input SendMessageInput) error {
	if s.inboxStore == nil || s.jobStore == nil {
		return nil
	}
	dedupeKey := deliverThreadInboxJobType + ":" + input.ThreadID.String()
	payload := map[string]string{
		"session_id": input.SessionID.String(),
		"thread_id":  input.ThreadID.String(),
		"org_id":     input.OrgID.String(),
	}
	var targetNodeID *string
	if s.runtimeOwnerStore != nil {
		runtime, err := s.runtimeOwnerStore.GetActiveByThread(ctx, input.OrgID, input.ThreadID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("get live thread runtime owner: %w", err)
			}
		} else if runtime.OwnerNodeID != "" {
			targetNodeID = &runtime.OwnerNodeID
		}
	}
	_, err := s.jobStore.EnqueueWithOpts(ctx, input.OrgID, db.EnqueueOpts{
		Queue:        deliverThreadInboxQueue,
		JobType:      deliverThreadInboxJobType,
		Payload:      payload,
		Priority:     7,
		DedupeKey:    &dedupeKey,
		TargetNodeID: targetNodeID,
	})
	return err
}

// deliveryStateForInboxEntry returns the entry's delivery state, or the empty
// string when no inbox entry was created. Returning "" rather than "acked"
// keeps the response honest in deployments where the inbox is not wired —
// the client can render "no inbox" instead of mistaking it for a confirmed
// delivery.
func deliveryStateForInboxEntry(entry *models.ThreadInboxEntry) models.ThreadInboxDeliveryState {
	if entry == nil {
		return ""
	}
	return entry.DeliveryState
}

func threadStatusCanQueue(status models.ThreadStatus) bool {
	switch status {
	case models.ThreadStatusPending, models.ThreadStatusRunning:
		return true
	default:
		return false
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
	if err := s.rejectIfCodeReviewOwned(ctx, orgID, sessionID); err != nil {
		return models.SessionThread{}, err
	}
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
	switch {
	case len(opts.TurnNumbers) > 0:
		logs, err = s.logStore.ListByThreadTurns(ctx, orgID, threadID, opts.TurnNumbers)
	case opts.LatestTurns > 0:
		logs, err = s.logStore.ListByThreadLatestTurns(ctx, orgID, threadID, opts.LatestTurns)
	default:
		logs, err = s.logStore.ListByThread(ctx, orgID, threadID)
	}
	if err != nil {
		return nil, fmt.Errorf("list logs: %w", err)
	}
	return logs, nil
}

// GetTranscriptWindow returns a turn-grouped transcript window for a thread.
func (s *Service) GetTranscriptWindow(ctx context.Context, orgID, sessionID, threadID uuid.UUID, opts db.SessionTranscriptWindowOptions) (TranscriptWindowResult, error) {
	thread, err := s.threadStore.GetByID(ctx, orgID, threadID)
	if err != nil {
		return TranscriptWindowResult{}, fmt.Errorf("%w: %w", ErrThreadNotFound, err)
	}
	thread, err = visibleThreadInSession(thread, sessionID)
	if err != nil {
		return TranscriptWindowResult{}, err
	}
	if s.transcriptStore == nil {
		return TranscriptWindowResult{}, fmt.Errorf("transcript store not configured")
	}
	window, err := s.transcriptStore.ListThreadWindow(ctx, orgID, threadID, opts)
	if err != nil {
		return TranscriptWindowResult{}, fmt.Errorf("list transcript window: %w", err)
	}
	return TranscriptWindowResult{Window: window, ThreadStatus: thread.Status}, nil
}
