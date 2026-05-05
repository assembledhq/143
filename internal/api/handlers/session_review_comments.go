package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// sendToAgentResponse is the response shape for the SendToAgent endpoint.
type sendToAgentResponse struct {
	Message string `json:"message"`
	Sent    bool   `json:"sent"`
}

type SessionReviewCommentHandler struct {
	store        *db.SessionReviewCommentStore
	sessionStore *db.SessionStore
	messageStore *db.SessionMessageStore
	threadStore  *db.SessionThreadStore
	jobStore     *db.JobStore
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

// SetMessageAndJobStores wires the message, thread, and job stores needed by
// SendToAgent to send messages directly without a second frontend call. The
// thread store is required so the synthesized "address these review comments"
// message lands on the session's primary thread; without it the message has
// thread_id=NULL and the per-thread timeline query skips it, leaving the
// click looking like a no-op.
func (h *SessionReviewCommentHandler) SetMessageAndJobStores(messageStore *db.SessionMessageStore, threadStore *db.SessionThreadStore, jobStore *db.JobStore) {
	h.messageStore = messageStore
	h.threadStore = threadStore
	h.jobStore = jobStore
}

func (h *SessionReviewCommentHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

func (h *SessionReviewCommentHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	comments, err := h.store.ListBySession(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list review comments")
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
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	var body struct {
		FilePath   string `json:"file_path"`
		LineNumber int    `json:"line_number"`
		Side       string `json:"side"`
		Body       string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	if body.FilePath == "" || body.Body == "" {
		writeError(w, r, http.StatusBadRequest, "VALIDATION", "file_path and body are required")
		return
	}
	if len(body.FilePath) > 1024 {
		writeError(w, r, http.StatusBadRequest, "VALIDATION", "file_path must be 1024 characters or less")
		return
	}
	if len(body.Body) > 10240 {
		writeError(w, r, http.StatusBadRequest, "VALIDATION", "body must be 10KB or less")
		return
	}
	if body.LineNumber < 1 {
		writeError(w, r, http.StatusBadRequest, "VALIDATION", "line_number must be a positive integer")
		return
	}

	side := body.Side
	if side == "" {
		side = "new"
	}
	if side != "old" && side != "new" {
		writeError(w, r, http.StatusBadRequest, "VALIDATION", "side must be 'old' or 'new'")
		return
	}

	// Look up the session's current turn to associate the comment with the right pass.
	if h.sessionStore == nil {
		writeError(w, r, http.StatusInternalServerError, "SESSION_LOOKUP_FAILED", "session store not available")
		return
	}
	session, err := h.sessionStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		h.logger.Error().Err(err).Str("session_id", sessionID.String()).Msg("failed to look up session for review comment")
		writeError(w, r, http.StatusNotFound, "SESSION_NOT_FOUND", "session not found")
		return
	}
	passNumber := session.CurrentTurn
	if passNumber < 1 {
		passNumber = 1
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
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create review comment")
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
			Details: marshalAuditDetails(h.logger, map[string]any{
				"review_comment_id": comment.ID.String(),
				"session_id":        sessionID.String(),
				"file_path":         comment.FilePath,
				"line_number":       comment.LineNumber,
				"diff_side":         comment.DiffSide,
				"pass_number":       comment.PassNumber,
				"body_length":       len(comment.Body),
				"resolved":          comment.Resolved,
			}),
		})
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.SessionReviewComment]{Data: *comment})
}

func (h *SessionReviewCommentHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	commentID, err := uuid.Parse(chi.URLParam(r, "commentId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid comment ID")
		return
	}

	// Parse body first to fail fast on bad input before doing DB lookups.
	var body struct {
		Body     *string `json:"body"`
		Resolved *bool   `json:"resolved"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	// Verify the requesting user owns this comment.
	existing, err := h.store.GetByID(r.Context(), orgID, commentID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "review comment not found")
		return
	}
	if existing.UserID != user.ID {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "you can only edit your own comments")
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

	comment, err := h.store.Update(r.Context(), orgID, sessionID, commentID, body.Body, body.Resolved, resolvedByPass)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "review comment not found")
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
			Details: marshalAuditDetails(h.logger, map[string]any{
				"review_comment_id": comment.ID.String(),
				"session_id":        sid.String(),
				"file_path":         comment.FilePath,
				"line_number":       comment.LineNumber,
				"diff_side":         comment.DiffSide,
				"pass_number":       comment.PassNumber,
				"body_length":       len(comment.Body),
				"changes": map[string]any{
					"body_length": auditChange(len(existing.Body), len(comment.Body)),
					"resolved":    auditChange(existing.Resolved, comment.Resolved),
				},
			}),
		})
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionReviewComment]{Data: comment})
}

func (h *SessionReviewCommentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	commentID, err := uuid.Parse(chi.URLParam(r, "commentId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid comment ID")
		return
	}

	// Verify the requesting user owns this comment.
	existing, err := h.store.GetByID(r.Context(), orgID, commentID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "review comment not found")
		return
	}
	if existing.UserID != user.ID {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "you can only delete your own comments")
		return
	}

	if err := h.store.Delete(r.Context(), orgID, sessionID, commentID); err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "review comment not found")
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
			Details: marshalAuditDetails(h.logger, map[string]any{
				"review_comment_id": existing.ID.String(),
				"session_id":        sessionID.String(),
				"file_path":         existing.FilePath,
				"line_number":       existing.LineNumber,
				"diff_side":         existing.DiffSide,
				"pass_number":       existing.PassNumber,
				"body_length":       len(existing.Body),
				"resolved":          existing.Resolved,
			}),
		})
	}

	w.WriteHeader(http.StatusNoContent)
}

// SendToAgent compiles open review comments into a structured message and
// sends it to the session as a follow-up message, enqueuing a continue_session job.
// If messageStore/jobStore are not configured, it falls back to returning the
// formatted message for the frontend to send manually.
func (h *SessionReviewCommentHandler) SendToAgent(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	comments, err := h.store.ListBySession(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list review comments")
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
		writeError(w, r, http.StatusBadRequest, "NO_OPEN_COMMENTS", "no open review comments to send")
		return
	}

	// Format into a structured directive for the agent.
	// Each comment includes file path, line number, and diff side for precise location.
	// Indent multi-line comment bodies so each line stays within the numbered block.
	var sb strings.Builder
	sb.WriteString("Please address the following code review comments:\n\n")
	for i, c := range open {
		sb.WriteString(fmt.Sprintf("%d. **%s:%d** (%s side)\n", i+1, c.FilePath, c.LineNumber, c.DiffSide))
		indented := strings.ReplaceAll(c.Body, "\n", "\n   ")
		sb.WriteString(fmt.Sprintf("   Comment: \"%s\"\n\n", indented))
	}
	message := strings.TrimSpace(sb.String())

	// If message and job stores are available, send the message directly.
	if h.messageStore != nil && h.jobStore != nil && h.sessionStore != nil {
		session, err := h.sessionStore.ClaimIdle(r.Context(), orgID, sessionID)
		if err != nil {
			// Distinguish "session not idle" (no row updated → pgx.ErrNoRows) from real DB errors.
			if errors.Is(err, pgx.ErrNoRows) {
				// Session isn't idle — fall back to returning the message for manual send.
				writeJSON(w, http.StatusOK, models.SingleResponse[sendToAgentResponse]{
					Data: sendToAgentResponse{Message: message, Sent: false},
				})
				return
			}
			h.logger.Error().Err(err).Str("session_id", sessionID.String()).Msg("failed to claim idle session")
			writeError(w, r, http.StatusInternalServerError, "CLAIM_FAILED", "failed to claim session")
			return
		}

		user := middleware.UserFromContext(r.Context())
		var userID *uuid.UUID
		if user != nil {
			userID = &user.ID
		}

		// Attribute the synthesized review-comment prompt to the session's
		// primary thread. ClaimIdle doesn't hydrate session.PrimaryThreadID,
		// so look it up here. ListBySession orders by created_at ASC so the
		// first entry is the seeded "Main" thread.
		var threadID *uuid.UUID
		if h.threadStore != nil {
			threads, err := h.threadStore.ListBySession(r.Context(), orgID, sessionID)
			if err != nil {
				if revertErr := h.sessionStore.UpdateStatus(r.Context(), orgID, sessionID, string(models.SessionStatusIdle)); revertErr != nil {
					h.logger.Error().Err(revertErr).Str("session_id", sessionID.String()).Msg("failed to revert session to idle after thread lookup failure")
				}
				writeError(w, r, http.StatusInternalServerError, "THREAD_LOOKUP_FAILED", "failed to look up session threads")
				return
			}
			if len(threads) > 0 {
				id := threads[0].ID
				threadID = &id
			}
		}

		msg := &models.SessionMessage{
			SessionID:  sessionID,
			OrgID:      orgID,
			ThreadID:   threadID,
			UserID:     userID,
			TurnNumber: session.CurrentTurn + 1,
			Role:       models.MessageRoleUser,
			Content:    message,
		}

		if err := h.messageStore.Create(r.Context(), msg); err != nil {
			if revertErr := h.sessionStore.UpdateStatus(r.Context(), orgID, sessionID, string(models.SessionStatusIdle)); revertErr != nil {
				h.logger.Error().Err(revertErr).Str("session_id", sessionID.String()).Msg("failed to revert session to idle after message creation failure")
			}
			writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create message")
			return
		}

		dedupeKey := db.ContinueSessionDedupeKey(sessionID)
		payload := map[string]string{
			"session_id": sessionID.String(),
			"org_id":     orgID.String(),
		}
		if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "continue_session", payload, 5, &dedupeKey); err != nil {
			// Delete the orphaned message and revert session status.
			if msg.ID != 0 {
				if delErr := h.messageStore.Delete(r.Context(), msg.ID); delErr != nil {
					h.logger.Error().Err(delErr).Int64("message_id", msg.ID).Msg("failed to delete orphaned message after enqueue failure")
				}
			}
			if revertErr := h.sessionStore.UpdateStatus(r.Context(), orgID, sessionID, string(models.SessionStatusIdle)); revertErr != nil {
				h.logger.Error().Err(revertErr).Str("session_id", sessionID.String()).Msg("failed to revert session to idle after enqueue failure")
			}
			writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue continue_session job")
			return
		}

		writeJSON(w, http.StatusOK, models.SingleResponse[sendToAgentResponse]{
			Data: sendToAgentResponse{Message: message, Sent: true},
		})
		return
	}

	// Fallback: return the formatted message for the frontend to send.
	writeJSON(w, http.StatusOK, models.SingleResponse[sendToAgentResponse]{
		Data: sendToAgentResponse{Message: message, Sent: false},
	})
}
