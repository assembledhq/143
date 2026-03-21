package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type SessionReviewCommentHandler struct {
	store        *db.SessionReviewCommentStore
	sessionStore *db.SessionStore
	logger       zerolog.Logger
	audit        *db.AuditEmitter
}

func NewSessionReviewCommentHandler(store *db.SessionReviewCommentStore, sessionStore *db.SessionStore, logger zerolog.Logger) *SessionReviewCommentHandler {
	return &SessionReviewCommentHandler{
		store:        store,
		sessionStore: sessionStore,
		logger:       logger,
	}
}

func (h *SessionReviewCommentHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

func (h *SessionReviewCommentHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	comments, err := h.store.ListBySession(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list review comments")
		return
	}

	if comments == nil {
		comments = []models.SessionReviewComment{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionReviewComment]{
		Data: comments,
	})
}

func (h *SessionReviewCommentHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	var body struct {
		FilePath   string `json:"file_path"`
		LineNumber int    `json:"line_number"`
		Side       string `json:"side"`
		Body       string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	if body.FilePath == "" || body.Body == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION", "file_path and body are required")
		return
	}

	side := body.Side
	if side == "" {
		side = "new"
	}
	if side != "old" && side != "new" {
		writeError(w, http.StatusBadRequest, "VALIDATION", "side must be 'old' or 'new'")
		return
	}

	// Look up the session's current turn to associate the comment with the right pass.
	passNumber := 1
	if h.sessionStore != nil {
		session, err := h.sessionStore.GetByID(r.Context(), orgID, sessionID)
		if err == nil && session.CurrentTurn > 0 {
			passNumber = session.CurrentTurn
		}
	}

	comment := &models.SessionReviewComment{
		SessionID:  sessionID,
		OrgID:      orgID,
		UserID:     user.ID,
		FilePath:   body.FilePath,
		LineNumber: body.LineNumber,
		DiffSide:   side,
		Body:       body.Body,
		PassNumber: passNumber,
	}

	if err := h.store.Create(r.Context(), comment); err != nil {
		h.logger.Error().Err(err).Msg("failed to create session review comment")
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "failed to create review comment")
		return
	}

	if h.audit != nil {
		resID := comment.ID.String()
		h.audit.EmitUserAction(r.Context(), db.UserActionParams{
			OrgID:        orgID,
			UserID:       user.ID,
			Action:       models.AuditActionSessionReviewCommentCreated,
			ResourceType: models.AuditResourceSessionReviewComment,
			ResourceID:   &resID,
			SessionID:    &sessionID,
		})
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.SessionReviewComment]{Data: *comment})
}

func (h *SessionReviewCommentHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	commentID, err := uuid.Parse(chi.URLParam(r, "commentId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid comment ID")
		return
	}

	var body struct {
		Body     *string `json:"body"`
		Resolved *bool   `json:"resolved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	// When resolving, link the comment to the current pass number.
	var resolvedByPass *int
	if body.Resolved != nil && *body.Resolved && h.sessionStore != nil {
		session, err := h.sessionStore.GetByID(r.Context(), orgID, sessionID)
		if err != nil {
			h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to look up session for resolved_by_pass")
		} else {
			turn := session.CurrentTurn
			if turn == 0 {
				turn = 1
			}
			resolvedByPass = &turn
		}
	}

	comment, err := h.store.Update(r.Context(), orgID, commentID, body.Body, body.Resolved, resolvedByPass)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "review comment not found")
		return
	}

	if h.audit != nil {
		resID := commentID.String()
		sid := comment.SessionID
		h.audit.EmitUserAction(r.Context(), db.UserActionParams{
			OrgID:        orgID,
			UserID:       user.ID,
			Action:       models.AuditActionSessionReviewCommentUpdated,
			ResourceType: models.AuditResourceSessionReviewComment,
			ResourceID:   &resID,
			SessionID:    &sid,
		})
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionReviewComment]{Data: comment})
}

func (h *SessionReviewCommentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	commentID, err := uuid.Parse(chi.URLParam(r, "commentId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid comment ID")
		return
	}

	if err := h.store.Delete(r.Context(), orgID, sessionID, commentID); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "review comment not found")
		return
	}

	if h.audit != nil {
		resID := commentID.String()
		h.audit.EmitUserAction(r.Context(), db.UserActionParams{
			OrgID:        orgID,
			UserID:       user.ID,
			Action:       models.AuditActionSessionReviewCommentDeleted,
			ResourceType: models.AuditResourceSessionReviewComment,
			ResourceID:   &resID,
			SessionID:    &sessionID,
		})
	}

	w.WriteHeader(http.StatusNoContent)
}

// SendToAgent compiles open review comments into a structured message.
// The frontend can use this to get the formatted message, then send it via
// the session's SendMessage endpoint.
func (h *SessionReviewCommentHandler) SendToAgent(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	comments, err := h.store.ListBySession(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list review comments")
		return
	}

	// Filter to open (unresolved) comments only.
	var open []models.SessionReviewComment
	for _, c := range comments {
		if !c.Resolved {
			open = append(open, c)
		}
	}

	if len(open) == 0 {
		writeError(w, http.StatusBadRequest, "NO_OPEN_COMMENTS", "no open review comments to send")
		return
	}

	// Format into a structured directive for the agent.
	var sb strings.Builder
	sb.WriteString("Please address the following code review comments:\n\n")
	for i, c := range open {
		sb.WriteString(fmt.Sprintf("%d. %s:%d\n", i+1, c.FilePath, c.LineNumber))
		sb.WriteString(fmt.Sprintf("   \"%s\"\n\n", c.Body))
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[struct {
		Message string `json:"message"`
	}]{Data: struct {
		Message string `json:"message"`
	}{Message: strings.TrimSpace(sb.String())}})
}
