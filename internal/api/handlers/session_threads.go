package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/thread"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type SessionThreadHandler struct {
	svc *thread.Service
}

func NewSessionThreadHandler(svc *thread.Service) *SessionThreadHandler {
	return &SessionThreadHandler{svc: svc}
}

// CreateThread handles POST /sessions/{id}/threads — adds a new agent thread
// to an existing session.
func (h *SessionThreadHandler) CreateThread(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
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
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	body.Label = strings.TrimSpace(body.Label)
	if body.Label == "" {
		writeError(w, http.StatusBadRequest, "MISSING_LABEL", "label is required")
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
			writeError(w, http.StatusConflict, "THREAD_LIMIT", "maximum of 4 threads per session")
		case strings.Contains(err.Error(), "session not found"):
			writeError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
		case strings.Contains(err.Error(), "cannot add threads to a completed session"):
			writeError(w, http.StatusConflict, "SESSION_TERMINAL", "cannot add threads to a completed session")
		case strings.Contains(err.Error(), "invalid agent type"):
			writeError(w, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
		case strings.Contains(err.Error(), "invalid model"):
			writeError(w, http.StatusBadRequest, "INVALID_MODEL", err.Error())
		case strings.Contains(err.Error(), "enqueue"):
			writeError(w, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue thread agent job")
		default:
			writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "failed to create thread")
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
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	threads, err := h.svc.ListThreads(r.Context(), orgID, sessionID)
	if err != nil {
		if strings.Contains(err.Error(), "session not found") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list threads")
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
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "tid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid thread ID")
		return
	}

	t, err := h.svc.GetThread(r.Context(), orgID, sessionID, threadID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "thread not found")
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
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "tid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid thread ID")
		return
	}

	var body struct {
		Message string   `json:"message"`
		Images  []string `json:"images"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" {
		writeError(w, http.StatusBadRequest, "MISSING_MESSAGE", "message is required")
		return
	}

	user := middleware.UserFromContext(r.Context())
	var userID *uuid.UUID
	if user != nil {
		userID = &user.ID
	}

	msg, err := h.svc.SendMessage(r.Context(), thread.SendMessageInput{
		SessionID: sessionID,
		OrgID:     orgID,
		ThreadID:  threadID,
		UserID:    userID,
		Message:   body.Message,
		Images:    body.Images,
	})
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "thread not found"):
			writeError(w, http.StatusNotFound, "NOT_FOUND", "thread not found")
		case strings.Contains(err.Error(), "thread must be idle"):
			writeError(w, http.StatusConflict, "NOT_IDLE", "thread must be idle to send a message")
		case strings.Contains(err.Error(), "enqueue"):
			writeError(w, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue continue_thread job")
		default:
			writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "failed to create message")
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
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "tid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid thread ID")
		return
	}

	messages, err := h.svc.GetMessages(r.Context(), orgID, sessionID, threadID)
	if err != nil {
		if strings.Contains(err.Error(), "thread not found") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "thread not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list thread messages")
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
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "tid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid thread ID")
		return
	}

	t, err := h.svc.EndThread(r.Context(), orgID, sessionID, threadID)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "thread not found"):
			writeError(w, http.StatusNotFound, "NOT_FOUND", "thread not found")
		case strings.Contains(err.Error(), "cannot be ended"):
			writeError(w, http.StatusConflict, "INVALID_STATUS", "thread cannot be ended in its current state")
		default:
			writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "failed to end thread")
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
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "tid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid thread ID")
		return
	}

	logs, err := h.svc.GetLogs(r.Context(), orgID, sessionID, threadID)
	if err != nil {
		if strings.Contains(err.Error(), "thread not found") {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "thread not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list thread logs")
		return
	}
	if logs == nil {
		logs = []models.SessionLog{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionLog]{Data: logs})
}
