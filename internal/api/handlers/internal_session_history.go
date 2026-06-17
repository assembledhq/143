package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type InternalSessionHistoryHandler struct {
	history       *db.SessionHistoryStore
	sessions      *db.SessionStore
	messages      *db.SessionMessageStore
	signingSecret string
}

func NewInternalSessionHistoryHandler(history *db.SessionHistoryStore, sessions *db.SessionStore, messages *db.SessionMessageStore, signingSecret string) *InternalSessionHistoryHandler {
	return &InternalSessionHistoryHandler{history: history, sessions: sessions, messages: messages, signingSecret: signingSecret}
}

func (h *InternalSessionHistoryHandler) Search(w http.ResponseWriter, r *http.Request) {
	claims, _, ok := h.authorize(w, r)
	if !ok {
		return
	}
	filters := db.SessionHistoryFilters{
		Query:  strings.TrimSpace(r.URL.Query().Get("q")),
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Cursor: strings.TrimSpace(r.URL.Query().Get("cursor")),
		Limit:  10,
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_LIMIT", "limit must be a number", err)
			return
		}
		filters.Limit = limit
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("created_after")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CREATED_AFTER", "created_after must be RFC3339", err)
			return
		}
		filters.CreatedAfter = &t
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("created_before")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CREATED_BEFORE", "created_before must be RFC3339", err)
			return
		}
		filters.CreatedBefore = &t
	}
	items, err := h.history.Search(r.Context(), claims.OrgID, claims.RepoID, *claims.SessionID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SESSION_HISTORY_FAILED", "failed to search session history", err)
		return
	}
	next := ""
	if len(items) == filters.Limit && len(items) > 0 {
		next = items[len(items)-1].ID.String()
	}
	writeJSON(w, http.StatusOK, models.ListResponse[db.SessionHistorySummary]{Data: items, Meta: models.PaginationMeta{NextCursor: next}})
}

func (h *InternalSessionHistoryHandler) Get(w http.ResponseWriter, r *http.Request) {
	claims, _, ok := h.authorize(w, r)
	if !ok {
		return
	}
	sessionID, err := uuid.Parse(chi.URLParam(r, "session_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID", err)
		return
	}
	item, err := h.history.Get(r.Context(), claims.OrgID, claims.RepoID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "SESSION_HISTORY_FAILED", "failed to get session history", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[db.SessionHistorySummary]{Data: item})
}

const messagesDefaultLimit = 200
const messagesMaxLimit = 500

func (h *InternalSessionHistoryHandler) Messages(w http.ResponseWriter, r *http.Request) {
	claims, _, ok := h.authorize(w, r)
	if !ok {
		return
	}
	sessionID, err := uuid.Parse(chi.URLParam(r, "session_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID", err)
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "thread_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_THREAD_ID", "invalid thread ID", err)
		return
	}
	limit := messagesDefaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_LIMIT", "limit must be a positive integer", err)
			return
		}
		if n > messagesMaxLimit {
			n = messagesMaxLimit
		}
		limit = n
	}
	if _, err := h.history.Get(r.Context(), claims.OrgID, claims.RepoID, sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		} else {
			writeError(w, r, http.StatusInternalServerError, "SESSION_HISTORY_FAILED", "failed to get session", err)
		}
		return
	}
	okThread, err := h.history.ThreadBelongsToSession(r.Context(), claims.OrgID, sessionID, threadID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SESSION_HISTORY_FAILED", "failed to check thread", err)
		return
	}
	if !okThread {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread not found")
		return
	}
	messages, err := h.messages.ListByThreadLatest(r.Context(), claims.OrgID, threadID, limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SESSION_HISTORY_FAILED", "failed to list messages", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionMessage]{Data: messages})
}

func (h *InternalSessionHistoryHandler) authorize(w http.ResponseWriter, r *http.Request) (*auth.InternalTokenClaims, models.Session, bool) {
	claims, session, ok := authorizeInternalSession(w, r, h.signingSecret, h.sessions)
	if !ok {
		return nil, models.Session{}, false
	}
	if !sessionHasCapability(session.CapabilitySnapshot, models.AgentCapabilitySessionHistory) {
		writeError(w, r, http.StatusForbidden, "CAPABILITY_DENIED", "session_history is not enabled for this agent run")
		return nil, models.Session{}, false
	}
	return claims, session, true
}

func sessionHasCapability(snapshot []models.AgentCapabilitySnapshotItem, id models.AgentCapabilityID) bool {
	for _, item := range snapshot {
		if item.ID == id {
			return true
		}
	}
	return false
}
