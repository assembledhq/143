package thread

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// SessionStore defines the session DB operations needed by the thread service.
type SessionStore interface {
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
}

// ThreadStore defines the thread DB operations needed by the thread service.
type ThreadStore interface {
	Create(ctx context.Context, thread *models.SessionThread, maxThreads int) error
	GetByID(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionThread, error)
	ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error)
	ClaimIdle(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionThread, error)
	UpdateStatus(ctx context.Context, orgID, threadID uuid.UUID, status models.ThreadStatus) error
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

// SendMessageInput holds the input for sending a message to a thread.
type SendMessageInput struct {
	SessionID uuid.UUID
	OrgID     uuid.UUID
	ThreadID  uuid.UUID
	UserID    *uuid.UUID
	Message   string
	Images    []string
}

// Service handles thread business logic.
type Service struct {
	threadStore  ThreadStore
	sessionStore SessionStore
	messageStore MessageStore
	logStore     LogStore
	jobStore     JobStore
	logger       zerolog.Logger
}

// NewService creates a new thread service.
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

func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "pr_created", "failed", "cancelled", "skipped":
		return true
	}
	return false
}

// CreateThread validates inputs, creates a thread, and enqueues a run_thread job.
func (s *Service) CreateThread(ctx context.Context, input CreateThreadInput) (*models.SessionThread, error) {
	// Verify session exists and belongs to org.
	session, err := s.sessionStore.GetByID(ctx, input.OrgID, input.SessionID)
	if err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
	}

	// Only allow adding threads to active sessions.
	if isTerminalStatus(session.Status) {
		return nil, fmt.Errorf("cannot add threads to a completed session")
	}

	// Default agent type to the session's agent type.
	agentType := models.AgentType(input.AgentType)
	if agentType == "" {
		agentType = session.AgentType
	}
	if err := agentType.Validate(); err != nil {
		return nil, fmt.Errorf("invalid agent type: %w", err)
	}

	var modelOverride *string
	if input.Model != "" {
		if err := models.ValidateModelForAgentType(agentType, input.Model); err != nil {
			return nil, fmt.Errorf("invalid model: %w", err)
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
		Status:        models.ThreadStatusPending,
	}

	if err := s.threadStore.Create(ctx, thread, models.MaxThreadsPerSession); err != nil {
		if err == db.ErrThreadLimitReached {
			return nil, db.ErrThreadLimitReached
		}
		return nil, fmt.Errorf("create thread: %w", err)
	}

	// Enqueue a run_thread job so the agent process starts.
	payload := map[string]string{
		"session_id": input.SessionID.String(),
		"thread_id":  thread.ID.String(),
		"org_id":     input.OrgID.String(),
	}
	if _, err := s.jobStore.Enqueue(ctx, input.OrgID, "agent", "run_thread", payload, 5, nil); err != nil {
		s.logger.Error().Err(err).Str("thread_id", thread.ID.String()).Msg("failed to enqueue run_thread job")
		return nil, fmt.Errorf("enqueue run_thread: %w", err)
	}

	return thread, nil
}

// ListThreads returns all threads for a session.
func (s *Service) ListThreads(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error) {
	// Verify session exists and belongs to org.
	if _, err := s.sessionStore.GetByID(ctx, orgID, sessionID); err != nil {
		return nil, fmt.Errorf("session not found: %w", err)
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
		return models.SessionThread{}, fmt.Errorf("thread not found: %w", err)
	}
	if thread.SessionID != sessionID {
		return models.SessionThread{}, fmt.Errorf("thread not found")
	}
	return thread, nil
}

// SendMessage claims an idle thread, creates a message, and enqueues a continue_thread job.
func (s *Service) SendMessage(ctx context.Context, input SendMessageInput) (*models.SessionMessage, error) {
	thread, err := s.threadStore.ClaimIdle(ctx, input.OrgID, input.ThreadID)
	if err != nil {
		// Check if thread exists at all to provide a better error.
		if _, lookupErr := s.threadStore.GetByID(ctx, input.OrgID, input.ThreadID); lookupErr != nil {
			return nil, fmt.Errorf("thread not found: %w", lookupErr)
		}
		return nil, fmt.Errorf("thread must be idle to send a message")
	}

	// Verify thread belongs to the session.
	if thread.SessionID != input.SessionID {
		if revertErr := s.threadStore.UpdateStatus(ctx, input.OrgID, input.ThreadID, models.ThreadStatusIdle); revertErr != nil {
			s.logger.Error().Err(revertErr).Str("thread_id", input.ThreadID.String()).Msg("failed to revert thread to idle after session mismatch")
		}
		return nil, fmt.Errorf("thread not found")
	}

	msg := &models.SessionMessage{
		SessionID:  thread.SessionID,
		OrgID:      input.OrgID,
		ThreadID:   &input.ThreadID,
		UserID:     input.UserID,
		TurnNumber: thread.CurrentTurn + 1,
		Role:       models.MessageRoleUser,
		Content:    input.Message,
	}
	if len(input.Images) > 0 {
		msg.Attachments = input.Images
	}

	if err := s.messageStore.Create(ctx, msg); err != nil {
		if revertErr := s.threadStore.UpdateStatus(ctx, input.OrgID, input.ThreadID, models.ThreadStatusIdle); revertErr != nil {
			s.logger.Error().Err(revertErr).Str("thread_id", input.ThreadID.String()).Msg("failed to revert thread to idle after message creation failure")
		}
		return nil, fmt.Errorf("create message: %w", err)
	}

	// Enqueue continue_thread job.
	payload := map[string]string{
		"session_id": thread.SessionID.String(),
		"thread_id":  input.ThreadID.String(),
		"org_id":     input.OrgID.String(),
	}
	if _, err := s.jobStore.Enqueue(ctx, input.OrgID, "agent", "continue_thread", payload, 5, nil); err != nil {
		if revertErr := s.threadStore.UpdateStatus(ctx, input.OrgID, input.ThreadID, models.ThreadStatusIdle); revertErr != nil {
			s.logger.Error().Err(revertErr).Str("thread_id", input.ThreadID.String()).Msg("failed to revert thread to idle after enqueue failure")
		}
		return nil, fmt.Errorf("enqueue continue_thread: %w", err)
	}

	return msg, nil
}

// EndThread transitions an active thread to completed.
func (s *Service) EndThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error) {
	thread, err := s.threadStore.GetByID(ctx, orgID, threadID)
	if err != nil {
		return models.SessionThread{}, fmt.Errorf("thread not found: %w", err)
	}

	if thread.SessionID != sessionID {
		return models.SessionThread{}, fmt.Errorf("thread not found")
	}

	// Only idle, running, or awaiting_input threads can be ended.
	switch thread.Status {
	case models.ThreadStatusIdle, models.ThreadStatusRunning, models.ThreadStatusAwaitingInput:
		// OK
	default:
		return models.SessionThread{}, fmt.Errorf("thread cannot be ended in its current state")
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
		return nil, fmt.Errorf("thread not found: %w", err)
	}
	if thread.SessionID != sessionID {
		return nil, fmt.Errorf("thread not found")
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
		return nil, fmt.Errorf("thread not found: %w", err)
	}
	if thread.SessionID != sessionID {
		return nil, fmt.Errorf("thread not found")
	}

	logs, err := s.logStore.ListByThread(ctx, orgID, threadID)
	if err != nil {
		return nil, fmt.Errorf("list logs: %w", err)
	}
	return logs, nil
}
