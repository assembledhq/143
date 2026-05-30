package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/thread"
)

// CancelThread handles POST /sessions/{id}/threads/{tid}/cancel — sends
// SIGINT to one tab's in-flight agent without disturbing siblings.
func (h *SessionThreadHandler) CancelThread(w http.ResponseWriter, r *http.Request) {
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
	t, err := h.svc.CancelThread(r.Context(), orgID, sessionID, threadID)
	if err != nil {
		switch {
		case errors.Is(err, thread.ErrThreadNotFound):
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread not found")
		case errors.Is(err, thread.ErrThreadNotCancellable):
			writeError(w, r, http.StatusConflict, "NOT_CANCELLABLE", "thread is not currently running")
		default:
			writeError(w, r, http.StatusInternalServerError, "CANCEL_FAILED", "failed to cancel thread", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionThread]{Data: t})
}

// ListThreadFileEvents handles GET /sessions/{id}/thread-file-events — the
// raw timeline of "tab T touched path P at turn N". The frontend rolls this
// up into overlap signals for the tab strip.
//
// Accepts an optional `?since=<RFC3339>` query parameter. The frontend
// passes the most recent observed_at it has cached so polling on a long
// session does not re-transfer the entire timeline every cycle. Bad
// timestamps fall back to "no filter" rather than 400 — partial data is
// better than a hard error for an advisory feed.
func (h *SessionThreadHandler) ListThreadFileEvents(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	var since *time.Time
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		if parsed, parseErr := time.Parse(time.RFC3339Nano, raw); parseErr == nil {
			since = &parsed
		}
	}
	events, err := h.svc.ListFileEvents(r.Context(), orgID, sessionID, since)
	if err != nil {
		if errors.Is(err, thread.ErrSessionNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list file events", err)
		return
	}
	if events == nil {
		events = []models.SessionThreadFileEvent{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionThreadFileEvent]{Data: events})
}

// ForkThread handles POST /sessions/{id}/threads/{tid}/fork — copies a tab
// into its own session for risky divergent work. Returns a job ID the UI
// can poll until the new session is ready.
func (h *SessionThreadHandler) ForkThread(w http.ResponseWriter, r *http.Request) {
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
		Label string `json:"label"`
	}
	if r.ContentLength > 0 {
		if decErr := json.NewDecoder(r.Body).Decode(&body); decErr != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
			return
		}
	}
	user := middleware.UserFromContext(r.Context())
	var userID *uuid.UUID
	if user != nil {
		userID = &user.ID
	}
	result, err := h.svc.ForkThread(r.Context(), thread.ForkInput{
		SourceSessionID: sessionID,
		SourceThreadID:  threadID,
		OrgID:           orgID,
		UserID:          userID,
		Label:           strings.TrimSpace(body.Label),
	})
	if err != nil {
		switch {
		case errors.Is(err, thread.ErrThreadNotFound):
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread not found")
		case errors.Is(err, thread.ErrSessionNotFound):
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		case errors.Is(err, thread.ErrEnqueueFailed):
			writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue fork job", err)
		default:
			writeError(w, r, http.StatusInternalServerError, "FORK_FAILED", "failed to fork thread", err)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, models.SingleResponse[thread.ForkResult]{Data: result})
}

// RevertThread handles POST /sessions/{id}/threads/{tid}/revert — applies the
// tab's diff in reverse against the shared sandbox when the patch is clean.
func (h *SessionThreadHandler) RevertThread(w http.ResponseWriter, r *http.Request) {
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
	user := middleware.UserFromContext(r.Context())
	var userID *uuid.UUID
	if user != nil {
		userID = &user.ID
	}
	result, err := h.svc.RevertThread(r.Context(), orgID, sessionID, threadID, userID)
	if err != nil {
		switch {
		case errors.Is(err, thread.ErrThreadNotFound):
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread not found")
		case errors.Is(err, thread.ErrEnqueueFailed):
			writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue revert job", err)
		default:
			writeError(w, r, http.StatusUnprocessableEntity, "REVERT_FAILED", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusAccepted, models.SingleResponse[thread.ForkResult]{Data: result})
}
