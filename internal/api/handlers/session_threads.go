package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/thread"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// ThreadService defines the interface for thread business logic.
type ThreadService interface {
	CreateThread(ctx context.Context, input thread.CreateThreadInput) (*models.SessionThread, error)
	ListThreads(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error)
	GetThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	SendMessage(ctx context.Context, input thread.SendMessageInput) (*models.SessionMessage, error)
	EndThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	GetMessages(ctx context.Context, orgID, sessionID, threadID uuid.UUID) ([]models.SessionMessage, error)
	GetLogs(ctx context.Context, orgID, sessionID, threadID uuid.UUID) ([]models.SessionLog, error)
	CancelThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	ListFileEvents(ctx context.Context, orgID, sessionID uuid.UUID, since *time.Time) ([]models.SessionThreadFileEvent, error)
	ForkThread(ctx context.Context, input thread.ForkInput) (thread.ForkResult, error)
	RevertThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID, userID *uuid.UUID) (thread.ForkResult, error)
}

type SessionThreadHandler struct {
	svc ThreadService
}

func NewSessionThreadHandler(svc ThreadService) *SessionThreadHandler {
	return &SessionThreadHandler{svc: svc}
}

// CreateThread handles POST /sessions/{id}/threads — adds a new agent thread
// to an existing session.
func (h *SessionThreadHandler) CreateThread(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	var body struct {
		AgentType    string   `json:"agent_type"`
		Model        string   `json:"model"`
		Label        string   `json:"label"`
		Instructions string   `json:"instructions"`
		FileScope    []string `json:"file_scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	body.Label = strings.TrimSpace(body.Label)
	if body.Label == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_LABEL", "label is required")
		return
	}

	result, err := h.svc.CreateThread(r.Context(), thread.CreateThreadInput{
		SessionID:    sessionID,
		OrgID:        orgID,
		AgentType:    body.AgentType,
		Model:        body.Model,
		Label:        body.Label,
		Instructions: strings.TrimSpace(body.Instructions),
		FileScope:    body.FileScope,
	})
	if err != nil {
		switch {
		case errors.Is(err, db.ErrThreadLimitReached):
			writeError(w, r, http.StatusConflict, "THREAD_LIMIT", "maximum of 4 threads per session")
		case errors.Is(err, thread.ErrSessionNotFound):
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		case errors.Is(err, thread.ErrSessionTerminal):
			writeError(w, r, http.StatusConflict, "SESSION_TERMINAL", "cannot add threads to a completed session")
		case errors.Is(err, thread.ErrInvalidAgentType):
			writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
		case errors.Is(err, thread.ErrInvalidModel):
			writeError(w, r, http.StatusBadRequest, "INVALID_MODEL", err.Error())
		default:
			writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create thread", err)
		}
		return
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.SessionThread]{Data: *result})
}

// ListThreads handles GET /sessions/{id}/threads — returns all threads for a session.
func (h *SessionThreadHandler) ListThreads(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	threads, err := h.svc.ListThreads(r.Context(), orgID, sessionID)
	if err != nil {
		if errors.Is(err, thread.ErrSessionNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list threads", err)
		return
	}
	if threads == nil {
		threads = []models.SessionThread{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionThread]{Data: threads})
}

// GetThread handles GET /sessions/{id}/threads/{tid} — returns a single thread.
func (h *SessionThreadHandler) GetThread(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "tid"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid thread ID")
		return
	}

	t, err := h.svc.GetThread(r.Context(), orgID, sessionID, threadID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread not found")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionThread]{Data: t})
}

// SendThreadMessage handles POST /sessions/{id}/threads/{tid}/messages —
// sends a follow-up message to an idle thread.
func (h *SessionThreadHandler) SendThreadMessage(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "tid"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid thread ID")
		return
	}

	var body struct {
		Message    string                        `json:"message"`
		Images     []string                      `json:"images"`
		References models.SessionInputReferences `json:"references"`
		Commands   models.SessionInputCommands   `json:"commands"`
		PlanMode   bool                          `json:"plan_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_MESSAGE", "message is required")
		return
	}

	user := middleware.UserFromContext(r.Context())
	var userID *uuid.UUID
	if user != nil {
		userID = &user.ID
	}

	msg, err := h.svc.SendMessage(r.Context(), thread.SendMessageInput{
		SessionID:  sessionID,
		OrgID:      orgID,
		ThreadID:   threadID,
		UserID:     userID,
		Message:    body.Message,
		Images:     body.Images,
		References: body.References,
		Commands:   body.Commands,
		PlanMode:   body.PlanMode,
	})
	if err != nil {
		switch {
		case errors.Is(err, thread.ErrThreadNotFound):
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread not found")
		case errors.Is(err, thread.ErrThreadNotIdle):
			writeError(w, r, http.StatusConflict, "NOT_IDLE", "thread must be idle to send a message")
		case errors.Is(err, thread.ErrRunningLimitReached):
			writeError(w, r, http.StatusConflict, "RUNNING_LIMIT", "this session already has the maximum number of tabs running concurrently")
		case errors.Is(err, thread.ErrActiveThreadExists):
			writeError(w, r, http.StatusConflict, "ACTIVE_THREAD_EXISTS", "another tab is already running in this sandbox")
		case errors.Is(err, thread.ErrEnqueueFailed):
			writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue continue_session job", err)
		default:
			writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create message", err)
		}
		return
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.SessionMessage]{Data: *msg})
}

// GetThreadMessages handles GET /sessions/{id}/threads/{tid}/messages —
// returns messages for a specific thread.
func (h *SessionThreadHandler) GetThreadMessages(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "tid"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid thread ID")
		return
	}

	messages, err := h.svc.GetMessages(r.Context(), orgID, sessionID, threadID)
	if err != nil {
		if errors.Is(err, thread.ErrThreadNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list thread messages", err)
		return
	}
	if messages == nil {
		messages = []models.SessionMessage{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionMessage]{Data: messages})
}

// EndThread handles POST /sessions/{id}/threads/{tid}/end — ends a specific thread.
func (h *SessionThreadHandler) EndThread(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "tid"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid thread ID")
		return
	}

	t, err := h.svc.EndThread(r.Context(), orgID, sessionID, threadID)
	if err != nil {
		switch {
		case errors.Is(err, thread.ErrThreadNotFound):
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread not found")
		case errors.Is(err, thread.ErrThreadCannotBeEnded):
			writeError(w, r, http.StatusConflict, "INVALID_STATUS", "thread cannot be ended in its current state")
		default:
			writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to end thread", err)
		}
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionThread]{Data: t})
}

// GetThreadLogs handles GET /sessions/{id}/threads/{tid}/logs — returns logs
// for a specific thread.
func (h *SessionThreadHandler) GetThreadLogs(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "tid"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid thread ID")
		return
	}

	logs, err := h.svc.GetLogs(r.Context(), orgID, sessionID, threadID)
	if err != nil {
		if errors.Is(err, thread.ErrThreadNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list thread logs", err)
		return
	}
	if logs == nil {
		logs = []models.SessionLog{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionLog]{Data: logs})
}
