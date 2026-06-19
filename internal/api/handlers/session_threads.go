package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/linear"
	"github.com/assembledhq/143/internal/services/thread"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// ThreadService defines the interface for thread business logic.
type ThreadService interface {
	CreateThread(ctx context.Context, input thread.CreateThreadInput) (*models.SessionThread, error)
	UpdateThread(ctx context.Context, input thread.UpdateThreadInput) (*models.SessionThread, error)
	ArchiveThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	ListThreads(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error)
	GetThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	SendMessage(ctx context.Context, input thread.SendMessageInput) (*thread.SendMessageResult, error)
	EndThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	GetMessages(ctx context.Context, orgID, sessionID, threadID uuid.UUID) ([]models.SessionMessage, error)
	GetMessageWindow(ctx context.Context, orgID, sessionID, threadID uuid.UUID, opts db.SessionMessageWindowOptions) (thread.MessageWindowResult, error)
	GetLogs(ctx context.Context, orgID, sessionID, threadID uuid.UUID, opts db.SessionLogFilterOptions) ([]models.SessionLog, error)
	CancelThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (models.SessionThread, error)
	ListFileEvents(ctx context.Context, orgID, sessionID uuid.UUID, since *time.Time) ([]models.SessionThreadFileEvent, error)
	ListRecoverableInboxEntries(ctx context.Context, orgID, sessionID, threadID uuid.UUID) ([]models.ThreadInboxEntry, error)
	RetryInboxEntry(ctx context.Context, orgID, sessionID, threadID, entryID uuid.UUID, allowUnknownDelivery bool) (models.ThreadInboxEntry, error)
	ForkThread(ctx context.Context, input thread.ForkInput) (thread.ForkResult, error)
	RevertThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID, userID *uuid.UUID) (thread.ForkResult, error)
	GetTranscriptWindow(ctx context.Context, orgID, sessionID, threadID uuid.UUID, opts db.SessionTranscriptWindowOptions) (thread.TranscriptWindowResult, error)
}

type SessionThreadHandler struct {
	svc          ThreadService
	audit        *db.AuditEmitter
	logger       zerolog.Logger
	linearLinker atomic.Pointer[linearLinkerHolder]
}

func NewSessionThreadHandler(svc ThreadService) *SessionThreadHandler {
	return &SessionThreadHandler{svc: svc}
}

// SetAuditEmitter wires the audit emitter used by SendThreadMessage to record
// review-comment resolutions. Optional — when unset, the resolution still
// happens (it's an in-tx side effect of the message create) but no audit row
// is written. Mirrors SessionHandler.SetAuditEmitter.
func (h *SessionThreadHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// SetLogger wires the logger used for marshaling audit details. Optional —
// the zerolog Nop value is harmless when unset.
func (h *SessionThreadHandler) SetLogger(logger zerolog.Logger) {
	h.logger = logger
}

// SetLinearLinker injects the Linear session-linking service used by
// SendThreadMessage to detect and link Linear references in follow-up
// messages. When unset, follow-ups still send normally — Linear refs are
// treated as opaque text, same fail-soft contract as
// SessionHandler.SetLinearLinker. Stored via atomic.Pointer so a late-running
// test can wire the linker without racing the read path.
func (h *SessionThreadHandler) SetLinearLinker(linker linearSessionLinker) {
	if linker == nil {
		h.linearLinker.Store(nil)
		return
	}
	h.linearLinker.Store(&linearLinkerHolder{fn: linker})
}

// getLinearLinker returns the currently-wired linker (or nil if none).
func (h *SessionThreadHandler) getLinearLinker() linearSessionLinker {
	holder := h.linearLinker.Load()
	if holder == nil {
		return nil
	}
	return holder.fn
}

// maybeLinkLinearMidSession kicks off detection + async enqueue for a
// follow-up message body in a detached goroutine. Mirrors
// SessionHandler.maybeLinkLinearMidSession verbatim so a Linear ref typed
// into a thread tab gets the same fail-soft, off-path linking the legacy
// session surface already provides.
func (h *SessionThreadHandler) maybeLinkLinearMidSession(ctx context.Context, orgID, sessionID uuid.UUID, messageBody string, userID *uuid.UUID) {
	linker := h.getLinearLinker()
	if linker == nil {
		return
	}
	detached := context.WithoutCancel(ctx)
	go func() {
		bgCtx, cancel := context.WithTimeout(detached, midSessionLinkTimeout)
		defer cancel()
		if err := linker.ResolveAndLinkMidSession(bgCtx, linear.MidSessionInput{
			OrgID:       orgID,
			SessionID:   sessionID,
			MessageBody: messageBody,
			UserID:      userID,
		}); err != nil {
			h.logger.Warn().Err(err).
				Str("session_id", sessionID.String()).
				Msg("mid-session linear linking failed; thread follow-up was sent but no link row was created")
		}
	}()
}

// emitThreadAnsweredQuestionAudit records a SessionQuestionAnswered audit
// when a thread-scoped follow-up resumed an awaiting_input session and
// flipped a pending question to 'answered'. Same shape as the inline emit in
// the session-level handler so audit consumers see one consistent record per
// answered question.
func emitThreadAnsweredQuestionAudit(
	emitter *db.AuditEmitter,
	logger zerolog.Logger,
	r *http.Request,
	sessionID uuid.UUID,
	question models.SessionQuestion,
	userID uuid.UUID,
	answerLength int,
) {
	qIDStr := question.ID.String()
	details := map[string]any{
		"question_id":   question.ID.String(),
		"session_id":    question.SessionID.String(),
		"question_text": question.QuestionText,
		"status":        question.Status,
		"answer_length": answerLength,
		"answered_by":   userID.String(),
		"option_count":  len(question.Options),
		"auto_answered": true,
	}
	if question.BlocksPhase != nil {
		details["blocks_phase"] = *question.BlocksPhase
	}
	emitUserAuditWithSession(emitter, r, models.AuditActionSessionQuestionAnswered, models.AuditResourceSession, &qIDStr, &sessionID, nil, marshalAuditDetails(logger, details))
}

// emitThreadAnsweredHumanInputAudit records a SessionHumanInputAnswered audit
// when a thread composer answer clears a pending free-text human-input
// request. This mirrors the session-level send path so audit consumers see
// the same event whether the user answered through the dialog or the composer.
func emitThreadAnsweredHumanInputAudit(
	emitter *db.AuditEmitter,
	logger zerolog.Logger,
	r *http.Request,
	sessionID uuid.UUID,
	request models.HumanInputRequest,
	userID uuid.UUID,
	answerLength int,
) {
	requestIDStr := request.ID.String()
	details := map[string]any{
		"request_id":    request.ID.String(),
		"session_id":    request.SessionID.String(),
		"request_kind":  string(request.Kind),
		"status":        string(request.Status),
		"answer_length": answerLength,
		"answered_by":   userID.String(),
		"choice_count":  len(request.Choices),
		"auto_answered": true,
	}
	if request.ThreadID != nil {
		details["thread_id"] = request.ThreadID.String()
	}
	if request.BlocksPhase != nil {
		details["blocks_phase"] = *request.BlocksPhase
	}
	emitUserAuditWithSession(emitter, r, models.AuditActionSessionHumanInputAnswered, models.AuditResourceSession, &requestIDStr, &sessionID, nil, marshalAuditDetails(logger, details))
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

func (h *SessionThreadHandler) UpdateThread(w http.ResponseWriter, r *http.Request) {
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

	// Model uses json.RawMessage so we can distinguish three wire states the
	// way callers expect. A plain *string collapses JSON null and "field
	// absent" to the same nil pointer, which silently keeps stale overrides
	// when the client meant to clear them.
	var body struct {
		AgentType string          `json:"agent_type"`
		Model     json.RawMessage `json:"model"`
		Label     string          `json:"label"`
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

	// Wire convention:
	//   - field absent           → keep existing override (nil)
	//   - "model": null          → clear the override     (&"")
	//   - "model": ""            → clear the override     (&"")
	//   - "model": "value"       → set/validate the value (&value)
	var modelInput *string
	switch {
	case len(body.Model) == 0:
		modelInput = nil
	case bytes.Equal(bytes.TrimSpace(body.Model), []byte("null")):
		empty := ""
		modelInput = &empty
	default:
		var modelValue string
		if err := json.Unmarshal(body.Model, &modelValue); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid model field")
			return
		}
		modelInput = &modelValue
	}

	result, err := h.svc.UpdateThread(r.Context(), thread.UpdateThreadInput{
		SessionID: sessionID,
		OrgID:     orgID,
		ThreadID:  threadID,
		AgentType: body.AgentType,
		Model:     modelInput,
		Label:     body.Label,
	})
	if err != nil {
		switch {
		case errors.Is(err, thread.ErrSessionNotFound), errors.Is(err, thread.ErrThreadNotFound):
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session or thread not found")
		case errors.Is(err, thread.ErrSessionTerminal):
			writeError(w, r, http.StatusConflict, "SESSION_TERMINAL", "cannot edit tabs on a completed session")
		case errors.Is(err, thread.ErrThreadNotEditable):
			writeError(w, r, http.StatusConflict, "THREAD_NOT_EDITABLE", "only blank idle tabs can be edited")
		case errors.Is(err, thread.ErrInvalidAgentType):
			writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
		case errors.Is(err, thread.ErrInvalidModel):
			writeError(w, r, http.StatusBadRequest, "INVALID_MODEL", err.Error())
		default:
			writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update thread", err)
		}
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionThread]{Data: *result})
}

func (h *SessionThreadHandler) ArchiveThread(w http.ResponseWriter, r *http.Request) {
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

	result, err := h.svc.ArchiveThread(r.Context(), orgID, sessionID, threadID)
	if err != nil {
		switch {
		case errors.Is(err, thread.ErrSessionNotFound), errors.Is(err, thread.ErrThreadNotFound):
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session or thread not found")
		case errors.Is(err, thread.ErrCannotArchiveLastThread):
			writeError(w, r, http.StatusConflict, "THREAD_LAST_VISIBLE", "cannot close the last remaining tab")
		case errors.Is(err, thread.ErrThreadActive):
			writeError(w, r, http.StatusConflict, "THREAD_ACTIVE", "cancel this tab before closing it")
		default:
			writeError(w, r, http.StatusInternalServerError, "ARCHIVE_FAILED", "failed to archive thread", err)
		}
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionThread]{Data: result})
}

// ListThreads handles GET /sessions/{id}/threads — returns all threads for a session.
// Unknown query parameters (including the legacy ?turn_numbers= filter that was
// removed) are silently ignored; all threads for the session are returned.
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

func (h *SessionThreadHandler) ListRecoverableInboxEntries(w http.ResponseWriter, r *http.Request) {
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

	entries, err := h.svc.ListRecoverableInboxEntries(r.Context(), orgID, sessionID, threadID)
	if err != nil {
		if errors.Is(err, thread.ErrThreadNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list recoverable inbox entries", err)
		return
	}
	if entries == nil {
		entries = []models.ThreadInboxEntry{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.ThreadInboxEntry]{Data: entries})
}

func (h *SessionThreadHandler) RetryInboxEntry(w http.ResponseWriter, r *http.Request) {
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
	entryID, err := uuid.Parse(chi.URLParam(r, "entry_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid inbox entry ID")
		return
	}

	var body struct {
		ReplayUnknownDelivery bool `json:"replay_unknown_delivery"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
			return
		}
	}

	entry, err := h.svc.RetryInboxEntry(r.Context(), orgID, sessionID, threadID, entryID, body.ReplayUnknownDelivery)
	if err != nil {
		if errors.Is(err, thread.ErrThreadNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread or inbox entry not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "RETRY_FAILED", "failed to retry inbox entry", err)
		return
	}
	// Audit the "replay something we may have already delivered" path
	// separately. Plain retries on dead-lettered entries are operator
	// hygiene; replaying an unknown_delivery entry is a dual-delivery risk
	// the operator is explicitly accepting, so we keep a record per call.
	if body.ReplayUnknownDelivery && h.audit != nil {
		entryIDStr := entry.ID.String()
		details := map[string]any{
			"session_id":     sessionID.String(),
			"thread_id":      threadID.String(),
			"entry_id":       entry.ID.String(),
			"sequence_no":    entry.SequenceNo,
			"delivery_state": string(entry.DeliveryState),
		}
		if entry.MessageID != 0 {
			details["message_id"] = entry.MessageID
		}
		if entry.LastError != nil {
			details["prior_last_error"] = *entry.LastError
		}
		emitUserAuditWithSession(h.audit, r, models.AuditActionSessionThreadInboxReplayed, models.AuditResourceSession, &entryIDStr, &sessionID, nil, marshalAuditDetails(h.logger, details))
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.ThreadInboxEntry]{Data: entry})
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
		Message                 string                        `json:"message"`
		ClientMessageID         string                        `json:"client_message_id"`
		Images                  []string                      `json:"images"`
		References              models.SessionInputReferences `json:"references"`
		Commands                models.SessionInputCommands   `json:"commands"`
		PlanMode                bool                          `json:"plan_mode"`
		ResolveReviewCommentIDs []string                      `json:"resolve_review_comment_ids"`
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

	// Parse and dedupe optional review-comment IDs to resolve atomically with
	// the message create. Mirrors session-level SendMessage so the wire shape
	// stays uniform across send paths — see review_comment_send.go for the
	// shared parser.
	resolveCommentIDs, parseErr := parseAndDedupeReviewCommentIDs(body.ResolveReviewCommentIDs)
	if parseErr != nil {
		parseErr.write(w, r)
		return
	}

	user := middleware.UserFromContext(r.Context())
	var userID *uuid.UUID
	if user != nil {
		userID = &user.ID
	}

	result, err := h.svc.SendMessage(r.Context(), thread.SendMessageInput{
		SessionID:               sessionID,
		OrgID:                   orgID,
		ThreadID:                threadID,
		ClientMessageID:         strings.TrimSpace(body.ClientMessageID),
		UserID:                  userID,
		Message:                 body.Message,
		Images:                  body.Images,
		References:              body.References,
		Commands:                body.Commands,
		PlanMode:                body.PlanMode,
		ResolveReviewCommentIDs: resolveCommentIDs,
	})
	if err != nil {
		// Comment-resolution errors take priority over the generic switch
		// because they're more specific (the request shape is well-formed,
		// but the comment IDs are bad). renderReviewCommentResolveError
		// handles ErrReviewCommentsNotInSession; the not-configured sentinel
		// is mapped explicitly here so the status/code match session-level
		// SendMessage.
		if errors.Is(err, thread.ErrReviewCommentsNotConfigured) {
			writeError(w, r, http.StatusBadRequest, "REVIEW_COMMENTS_NOT_CONFIGURED", "review comment resolution is not configured for this server")
			return
		}
		if renderReviewCommentResolveError(w, r, err) {
			return
		}
		switch {
		case errors.Is(err, thread.ErrThreadNotFound):
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread not found")
		case errors.Is(err, thread.ErrThreadInboxBackpressure):
			writeError(w, r, http.StatusTooManyRequests, "THREAD_INBOX_BACKPRESSURE", "this tab has too many undelivered messages; wait for delivery to catch up")
		case errors.Is(err, thread.ErrThreadNotIdle):
			writeError(w, r, http.StatusConflict, "NOT_IDLE", "thread must be idle to send a message")
		case errors.Is(err, thread.ErrRunningLimitReached):
			writeError(w, r, http.StatusConflict, "RUNNING_LIMIT", "this session already has the maximum number of tabs running concurrently")
		case errors.Is(err, thread.ErrActiveThreadExists):
			writeError(w, r, http.StatusConflict, "ACTIVE_THREAD_EXISTS", "another tab is already running in this sandbox")
		case errors.Is(err, thread.ErrSessionSnapshotExpired):
			writeError(w, r, http.StatusGone, "SNAPSHOT_EXPIRED", "this session's environment has expired and can no longer be continued")
		case errors.Is(err, thread.ErrSessionNotResumable):
			writeError(w, r, http.StatusConflict, "NOT_RESUMABLE", "session must be idle, running, awaiting input, need guidance, or otherwise resumable to send a message")
		case errors.Is(err, thread.ErrEnqueueFailed):
			writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue continue_session job", err)
		default:
			writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create message", err)
		}
		return
	}

	// Emit a SessionQuestionAnswered audit when the send resumed an
	// awaiting_input session and flipped a pending question to answered.
	// Same shape as the session-level path so audit consumers see one row
	// per answered question regardless of the surface.
	if result.AnsweredQuestion != nil && userID != nil {
		emitThreadAnsweredQuestionAudit(h.audit, h.logger, r, sessionID, *result.AnsweredQuestion, *userID, len(body.Message))
	}
	if result.AnsweredHumanInput != nil && userID != nil {
		emitThreadAnsweredHumanInputAudit(h.audit, h.logger, r, sessionID, *result.AnsweredHumanInput, *userID, len(body.Message))
	}

	// Audit one row per resolved comment after the tx commits — same shape
	// as session-level SendMessage so audit consumers see consistent
	// before/after values regardless of which surface triggered the
	// resolution.
	emitReviewCommentResolutionAudits(h.audit, h.logger, r, sessionID, result.Message.ID, result.ResolvedComments)

	// Mid-session Linear linking, fire-and-forget — mirrors the session-level
	// SendMessage hook so refs in a follow-up are detected and linked
	// regardless of whether the user sent through the session or thread
	// surface.
	h.maybeLinkLinearMidSession(r.Context(), orgID, sessionID, body.Message, userID)

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.SendThreadMessageResponse]{
		Data: models.SendThreadMessageResponse{
			Message:       *result.Message,
			InboxEntry:    result.InboxEntry,
			ThreadStatus:  result.ThreadStatus,
			DeliveryState: result.DeliveryState,
		},
	})
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

	query := r.URL.Query()
	if query.Has("position") || query.Has("before") || query.Has("after") || query.Has("anchor_message_id") || query.Has("limit") {
		opts := db.SessionMessageWindowOptions{Limit: db.DefaultSessionMessageWindowLimit}
		if before := strings.TrimSpace(query.Get("before")); before != "" {
			beforeID, err := parsePositiveInt64(before)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid before cursor")
				return
			}
			opts.BeforeID = beforeID
			opts.Position = db.SessionMessageWindowPositionOlder
		}
		if after := strings.TrimSpace(query.Get("after")); after != "" {
			afterID, err := parsePositiveInt64(after)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid after cursor")
				return
			}
			opts.AfterID = afterID
			opts.Position = db.SessionMessageWindowPositionNewer
		}
		if limitRaw := strings.TrimSpace(query.Get("limit")); limitRaw != "" {
			limit, err := parsePositiveInt(limitRaw)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_LIMIT", "invalid limit")
				return
			}
			opts.Limit = limit
		}
		position := strings.TrimSpace(query.Get("position"))
		if position != "" {
			switch position {
			case string(db.SessionMessageWindowPositionLatest):
				opts.Position = db.SessionMessageWindowPositionLatest
			case string(db.SessionMessageWindowPositionAround):
				opts.Position = db.SessionMessageWindowPositionAround
			default:
				writeError(w, r, http.StatusBadRequest, "INVALID_POSITION", "position must be latest or around")
				return
			}
		}
		if anchorRaw := strings.TrimSpace(query.Get("anchor_message_id")); anchorRaw != "" {
			anchorID, err := parsePositiveInt64(anchorRaw)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid anchor message id")
				return
			}
			opts.AnchorMessageID = anchorID
		}
		if opts.BeforeID > 0 && opts.AfterID > 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "before and after cursors cannot be combined")
			return
		}
		if opts.Position == db.SessionMessageWindowPositionLatest && (opts.BeforeID > 0 || opts.AfterID > 0) {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "before and after cursors cannot be combined with position=latest")
			return
		}
		if opts.Position == db.SessionMessageWindowPositionAround {
			if opts.AnchorMessageID <= 0 {
				writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "anchor_message_id is required for around position")
				return
			}
			if opts.BeforeID > 0 || opts.AfterID > 0 {
				writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "around position cannot be combined with before or after")
				return
			}
		}

		result, err := h.svc.GetMessageWindow(r.Context(), orgID, sessionID, threadID, opts)
		if err != nil {
			if errors.Is(err, thread.ErrThreadNotFound) {
				writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread not found")
				return
			}
			writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list thread messages", err)
			return
		}
		writeJSON(w, http.StatusOK, models.ThreadMessageWindowResponse{
			Data: result.Window.Messages,
			Meta: models.ThreadMessageWindowMeta{
				NextOlderCursor:          result.Window.NextOlderCursor,
				HasOlder:                 result.Window.HasOlder,
				NextNewerCursor:          result.Window.NextNewerCursor,
				HasNewer:                 result.Window.HasNewer,
				AnchorMessageID:          result.Window.AnchorMessageID,
				AnchorFound:              result.Window.AnchorFound,
				LatestAssistantMessageID: result.Window.LatestAssistantMessageID,
				LiveEdgeMessageID:        result.Window.LiveEdgeMessageID,
				WindowPosition:           string(result.Window.Position),
				ThreadStatus:             string(result.ThreadStatus),
			},
		})
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

	opts := db.SessionLogFilterOptions{
		TurnNumbers: parseTurnNumbers(r.URL.Query().Get("turn_numbers")),
		LatestTurns: parseLatestTurns(r.URL.Query().Get("latest_turns")),
	}
	logs, err := h.svc.GetLogs(r.Context(), orgID, sessionID, threadID, opts)
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

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionLogResponse]{
		Data: models.NewSessionLogResponses(logs),
	})
}

// maxLatestTurns bounds the latest_turns parameter so a client cannot turn
// the bounded bootstrap query back into an unbounded full-thread scan.
const maxLatestTurns = 200

func parseLatestTurns(raw string) int {
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0
	}
	if value > maxLatestTurns {
		return maxLatestTurns
	}
	return value
}

func parseTurnNumbers(raw string) []int {
	if raw == "" {
		return nil
	}
	seen := make(map[int]struct{})
	for _, part := range strings.Split(raw, ",") {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value < 0 || value > math.MaxInt32 {
			continue
		}
		seen[value] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	turnNumbers := make([]int, 0, len(seen))
	for value := range seen {
		turnNumbers = append(turnNumbers, value)
	}
	sort.Ints(turnNumbers)
	return turnNumbers
}

// GetThreadTranscript handles GET /sessions/{id}/threads/{tid}/transcript
func (h *SessionThreadHandler) GetThreadTranscript(w http.ResponseWriter, r *http.Request) {
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

	opts := db.SessionTranscriptWindowOptions{LimitTurns: db.DefaultTranscriptLimitTurns}
	query := r.URL.Query()

	// Parse position
	position := strings.TrimSpace(query.Get("position"))
	switch position {
	case "", "latest":
		opts.Position = models.TranscriptWindowPositionLatest
	case "around":
		opts.Position = models.TranscriptWindowPositionAround
	default:
		writeError(w, r, http.StatusBadRequest, "INVALID_POSITION", "position must be 'latest' or 'around'; use before= or after= cursor params for older/newer pages")
		return
	}

	// Parse before/after cursors
	if before := strings.TrimSpace(query.Get("before")); before != "" {
		cursor, err := models.DecodeTranscriptCursor(before, orgID, threadID)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid before cursor")
			return
		}
		opts.Before = &cursor
		opts.Position = models.TranscriptWindowPositionOlder
	}
	if after := strings.TrimSpace(query.Get("after")); after != "" {
		cursor, err := models.DecodeTranscriptCursor(after, orgID, threadID)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid after cursor")
			return
		}
		opts.After = &cursor
		opts.Position = models.TranscriptWindowPositionNewer
	}
	if opts.Before != nil && opts.After != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "before and after cannot be combined")
		return
	}

	// Parse around anchors (priority: anchor_entry_id > anchor_message_id > anchor_turn_number)
	opts.AnchorEntryID = strings.TrimSpace(query.Get("anchor_entry_id"))
	if rawAnchorMsg := strings.TrimSpace(query.Get("anchor_message_id")); rawAnchorMsg != "" {
		anchorID, err := parsePositiveInt64(rawAnchorMsg)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ANCHOR", "invalid anchor_message_id")
			return
		}
		opts.AnchorMessageID = anchorID
	}
	if rawTurn := strings.TrimSpace(query.Get("anchor_turn_number")); rawTurn != "" {
		turn, err := parsePositiveInt(rawTurn)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ANCHOR", "invalid anchor_turn_number")
			return
		}
		opts.AnchorTurnNumber = &turn
	}
	if opts.Position == models.TranscriptWindowPositionAround {
		if opts.AnchorEntryID == "" && opts.AnchorMessageID == 0 && opts.AnchorTurnNumber == nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ANCHOR", "position=around requires anchor_entry_id, anchor_message_id, or anchor_turn_number")
			return
		}
	}

	// Parse limit_turns
	if rawLimit := strings.TrimSpace(query.Get("limit_turns")); rawLimit != "" {
		limit, err := parsePositiveInt(rawLimit)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_LIMIT", "invalid limit_turns")
			return
		}
		opts.LimitTurns = limit
	}
	if rawInclude := strings.TrimSpace(query.Get("include")); rawInclude != "" {
		include, err := db.ParseTranscriptInclude(rawInclude)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_INCLUDE", "invalid include")
			return
		}
		opts.Include = include
	}

	result, err := h.svc.GetTranscriptWindow(r.Context(), orgID, sessionID, threadID, opts)
	if err != nil {
		if errors.Is(err, thread.ErrThreadNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "thread not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "TRANSCRIPT_FAILED", "failed to load transcript window", err)
		return
	}

	resp := buildTranscriptWindowResponse(result, orgID, threadID)
	writeJSON(w, http.StatusOK, resp)
}

// buildTranscriptEntry converts a raw store row to a SessionTranscriptEntry.
func buildTranscriptEntry(row db.SessionTranscriptRawRow) models.SessionTranscriptEntry {
	entry := models.SessionTranscriptEntry{
		Kind:      row.EntryKindHint,
		CreatedAt: row.EntryTime,
	}

	switch {
	case row.Message != nil:
		msg := row.Message
		msgID := msg.ID
		entry.ID = "msg_" + strconv.FormatInt(msgID, 10)
		entry.MessageID = &msgID
		entry.Role = msg.Role

		// Content truncation at SessionLogPreviewBytes (8192).
		fullContent := msg.Content
		fullContentBytes := len([]byte(fullContent))
		truncated := fullContentBytes > models.SessionLogPreviewBytes
		content := fullContent
		if truncated {
			content = truncateToValidUTF8(fullContent, models.SessionLogPreviewBytes)
		}
		entry.Content = content
		entry.ContentTruncated = truncated
		entry.ContentBytes = fullContentBytes
		entry.ContentChars = utf8RuneCount(fullContent)
		entry.Message = msg

	case row.Log != nil:
		log := row.Log
		logID := log.ID
		logResp := models.NewSessionLogResponse(*log)
		entry.Level = log.Level

		if row.EntryKindHint == models.TranscriptEntryKindToolUse {
			entry.ID = "tuse_" + strconv.FormatInt(logID, 10)
			// Extract tool name from metadata.
			var meta struct {
				ToolName string `json:"tool_name"`
				Tool     string `json:"tool"`
			}
			if len(log.Metadata) > 0 {
				_ = json.Unmarshal(log.Metadata, &meta)
			}
			entry.ToolName = firstNonEmptyTranscriptValue(meta.ToolName, meta.Tool)
			entry.Summary = oneLineTranscriptSummary(logResp.Message)
		} else if row.EntryKindHint == models.TranscriptEntryKindToolResult {
			entry.ID = "tres_" + strconv.FormatInt(logID, 10)
			var meta struct {
				ToolName string `json:"tool_name"`
				Tool     string `json:"tool"`
			}
			if len(log.Metadata) > 0 {
				_ = json.Unmarshal(log.Metadata, &meta)
			}
			entry.ToolName = firstNonEmptyTranscriptValue(meta.ToolName, meta.Tool)
			entry.Summary = oneLineTranscriptSummary(logResp.Message)
			entry.ContentTruncated = logResp.MessageTruncated
			entry.ContentBytes = logResp.MessageBytes
			entry.ContentChars = logResp.MessageChars
		} else {
			entry.ID = "log_" + strconv.FormatInt(logID, 10)
			entry.Content = logResp.Message
			entry.ContentTruncated = logResp.MessageTruncated
			entry.ContentBytes = logResp.MessageBytes
			entry.ContentChars = logResp.MessageChars
		}
		entry.LogID = &logID
		entry.Log = &logResp

	case row.HumanInput != nil:
		hir := row.HumanInput
		reqID := hir.ID
		entry.ID = "hiq_" + reqID.String()
		entry.RequestID = &reqID
		entry.HumanInput = hir
	}

	return entry
}

// buildTranscriptWindowResponse groups raw rows into turns and builds the full
// API response.
func buildTranscriptWindowResponse(result thread.TranscriptWindowResult, orgID, threadID uuid.UUID) models.SessionTranscriptWindowResponse {
	// Group rows by turn_number preserving order.
	type turnAccum struct {
		turnNumber int
		entries    []models.SessionTranscriptEntry
		startedAt  time.Time
		endedAt    time.Time
	}

	var turnOrder []int
	turnMap := make(map[int]*turnAccum)

	for _, row := range result.Window.Rows {
		t, ok := turnMap[row.TurnNumber]
		if !ok {
			turnOrder = append(turnOrder, row.TurnNumber)
			t = &turnAccum{
				turnNumber: row.TurnNumber,
				startedAt:  row.EntryTime,
				endedAt:    row.EntryTime,
			}
			turnMap[row.TurnNumber] = t
		}
		if row.EntryTime.Before(t.startedAt) {
			t.startedAt = row.EntryTime
		}
		if row.EntryTime.After(t.endedAt) {
			t.endedAt = row.EntryTime
		}
		t.entries = append(t.entries, buildTranscriptEntry(row))
	}

	turns := make([]models.SessionTranscriptTurn, 0, len(turnOrder))
	for _, tn := range turnOrder {
		accum := turnMap[tn]
		turn := models.SessionTranscriptTurn{
			TurnNumber: accum.turnNumber,
			StartedAt:  accum.startedAt,
			Entries:    accum.entries,
		}
		if !accum.endedAt.IsZero() && accum.endedAt != accum.startedAt {
			endedAt := accum.endedAt
			turn.EndedAt = &endedAt
		}
		turns = append(turns, turn)
	}

	meta := models.SessionTranscriptWindowMeta{
		Position:                 result.Window.Position,
		HasOlder:                 result.Window.HasOlder,
		NextOlderCursor:          result.Window.OlderCursor,
		HasNewer:                 result.Window.HasNewer,
		NextNewerCursor:          result.Window.NewerCursor,
		AnchorEntryID:            result.Window.AnchorEntryID,
		AnchorFound:              result.Window.AnchorFound,
		LatestAssistantEntryID:   result.Window.LatestAssistantEntryID,
		LatestAssistantMessageID: result.Window.LatestAssistantMessageID,
		LiveEdgeEntryID:          result.Window.LiveEdgeEntryID,
		LiveEdgeMessageID:        result.Window.LiveEdgeMessageID,
		ThreadStatus:             result.ThreadStatus,
	}

	return models.SessionTranscriptWindowResponse{
		Data: turns,
		Meta: meta,
	}
}

func firstNonEmptyTranscriptValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func oneLineTranscriptSummary(message string) string {
	line := strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n"))
	if idx := strings.Index(line, "\n"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	const maxSummaryRunes = 160
	runes := []rune(line)
	if len(runes) > maxSummaryRunes {
		return string(runes[:maxSummaryRunes]) + "..."
	}
	return line
}

func utf8RuneCount(s string) int {
	return utf8.RuneCountInString(s)
}

// truncateToValidUTF8 truncates s to at most maxBytes bytes while keeping valid UTF-8.
func truncateToValidUTF8(s string, maxBytes int) string {
	b := []byte(s)
	if len(b) <= maxBytes {
		return s
	}
	b = b[:maxBytes]
	for !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b)
}
