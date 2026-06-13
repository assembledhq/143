package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/thread"
)

type InternalSessionTabsHandler struct {
	svc           ThreadService
	sessionStore  internalSessionLookup
	orgStore      internalOrgLookup
	signingSecret string
	audit         *db.AuditEmitter
	logger        zerolog.Logger
}

type internalSessionLookup interface {
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
}

type internalOrgLookup interface {
	GetByID(ctx context.Context, orgID uuid.UUID) (models.Organization, error)
}

type internalThreadListerWithOptions interface {
	ListThreadsWithOptions(ctx context.Context, orgID, sessionID uuid.UUID, opts thread.ListThreadsOptions) ([]models.SessionThread, error)
}

func NewInternalSessionTabsHandler(svc ThreadService, sessionStore internalSessionLookup, orgStore internalOrgLookup, signingSecret string, logger zerolog.Logger) *InternalSessionTabsHandler {
	return &InternalSessionTabsHandler{
		svc:           svc,
		sessionStore:  sessionStore,
		orgStore:      orgStore,
		signingSecret: signingSecret,
		logger:        logger,
	}
}

func (h *InternalSessionTabsHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

type sandboxSessionTabScope struct {
	OrgID          uuid.UUID
	RepoID         uuid.UUID
	SessionID      uuid.UUID
	SourceThreadID *uuid.UUID
	Settings       models.OrgSettings
}

type sandboxTabListItem struct {
	ID                  uuid.UUID                          `json:"id"`
	Label               string                             `json:"label"`
	AgentType           models.AgentType                   `json:"agent_type"`
	ModelOverride       *string                            `json:"model_override,omitempty"`
	Status              models.ThreadStatus                `json:"status"`
	CurrentTurn         int                                `json:"current_turn"`
	LastActivityAt      *time.Time                         `json:"last_activity_at,omitempty"`
	PendingMessageCount int                                `json:"pending_message_count"`
	InboxDelivery       *models.ThreadInboxDeliverySummary `json:"inbox_delivery,omitempty"`
	CostCents           float64                            `json:"cost_cents"`
	ResultSummary       *string                            `json:"result_summary,omitempty"`
	CreatedAt           time.Time                          `json:"created_at"`
	CreatedBySource     models.ThreadCreatedBySource       `json:"created_by_source,omitempty"`
	CreatedByThreadID   *uuid.UUID                         `json:"created_by_thread_id,omitempty"`
	IsCurrent           bool                               `json:"is_current"`
}

type sandboxRecentFile struct {
	Path       string    `json:"path"`
	Operation  string    `json:"operation"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

func (h *InternalSessionTabsHandler) List(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.authorize(w, r)
	if !ok {
		return
	}
	includeArchived, err := parseOptionalBool(r.URL.Query().Get("include_archived"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_INCLUDE_ARCHIVED", "include_archived must be a boolean", err)
		return
	}
	threads, err := h.listThreads(r.Context(), scope, includeArchived)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_TABS_FAILED", "failed to list tabs", err)
		return
	}
	items := make([]sandboxTabListItem, 0, len(threads))
	for _, t := range threads {
		items = append(items, h.tabItem(t, scope.SourceThreadID))
	}
	writeJSON(w, http.StatusOK, models.ListResponse[sandboxTabListItem]{Data: items})
}

func (h *InternalSessionTabsHandler) Get(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.authorize(w, r)
	if !ok {
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "thread_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid tab ID")
		return
	}
	t, err := h.svc.GetThread(r.Context(), scope.OrgID, scope.SessionID, threadID)
	if err != nil {
		if errors.Is(err, thread.ErrThreadNotFound) || errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "TAB_NOT_FOUND", "tab not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_TAB_FAILED", "failed to get tab", err)
		return
	}
	files := h.recentFiles(r, scope, threadID)
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]any]{Data: map[string]any{
		"thread":       h.tabItem(t, scope.SourceThreadID),
		"recent_files": files,
	}})
}

func (h *InternalSessionTabsHandler) Create(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var body struct {
		Label        string `json:"label"`
		AgentType    string `json:"agent_type"`
		Model        string `json:"model"`
		Instructions string `json:"instructions"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	agentType := strings.TrimSpace(body.AgentType)
	if agentType == "" && scope.SourceThreadID != nil {
		if source, err := h.svc.GetThread(r.Context(), scope.OrgID, scope.SessionID, *scope.SourceThreadID); err == nil {
			agentType = string(source.AgentType)
			if body.Model == "" && source.ModelOverride != nil {
				body.Model = *source.ModelOverride
			}
		}
	}
	if agentType == "" {
		agentType = string(scope.Settings.DefaultAgentType)
	}
	label := strings.TrimSpace(body.Label)
	if label == "" {
		label = "Agent tab"
	}
	created, err := h.svc.CreateThread(r.Context(), thread.CreateThreadInput{
		SessionID:         scope.SessionID,
		OrgID:             scope.OrgID,
		AgentType:         agentType,
		Model:             strings.TrimSpace(body.Model),
		Label:             label,
		Instructions:      strings.TrimSpace(body.Instructions),
		CreatedBySource:   models.ThreadCreatedBySourceAgentTool,
		CreatedByThreadID: scope.SourceThreadID,
	})
	if err != nil {
		switch {
		case errors.Is(err, db.ErrThreadLimitReached):
			writeError(w, r, http.StatusConflict, "TAB_LIMIT_REACHED", "maximum tabs reached")
		case errors.Is(err, thread.ErrInvalidAgentType):
			writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
		case errors.Is(err, thread.ErrInvalidModel):
			writeError(w, r, http.StatusBadRequest, "INVALID_MODEL", err.Error())
		default:
			writeError(w, r, http.StatusInternalServerError, "CREATE_TAB_FAILED", "failed to create tab", err)
		}
		return
	}
	h.emitToolAudit(r, models.AuditActionSessionThreadCreatedByAgentTool, scope, created.ID, created.AgentType, created.ModelOverride, "session_tabs_create", 0)
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.SessionThread]{Data: *created})
}

func (h *InternalSessionTabsHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.authorize(w, r)
	if !ok {
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "thread_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid tab ID")
		return
	}
	var body struct {
		Message         string `json:"message"`
		ClientMessageID string `json:"client_message_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" {
		writeError(w, r, http.StatusBadRequest, "EMPTY_MESSAGE", "message is required")
		return
	}
	result, err := h.svc.SendMessage(r.Context(), thread.SendMessageInput{
		SessionID:       scope.SessionID,
		OrgID:           scope.OrgID,
		ThreadID:        threadID,
		ClientMessageID: strings.TrimSpace(body.ClientMessageID),
		Message:         body.Message,
		MessageSource:   models.SessionMessageSourceAgentTool,
	})
	if err != nil {
		switch {
		case errors.Is(err, thread.ErrThreadNotFound):
			writeError(w, r, http.StatusNotFound, "TAB_NOT_FOUND", "tab not found")
		case errors.Is(err, thread.ErrThreadInboxBackpressure):
			writeError(w, r, http.StatusConflict, "THREAD_INBOX_BACKPRESSURE", "too many undelivered messages")
		default:
			writeError(w, r, http.StatusInternalServerError, "SEND_MESSAGE_FAILED", "failed to send message", err)
		}
		return
	}
	delivery := result.DeliveryState
	targetThread, threadErr := h.svc.GetThread(r.Context(), scope.OrgID, scope.SessionID, threadID)
	if threadErr != nil {
		h.logger.Warn().Err(threadErr).Str("thread_id", threadID.String()).Msg("failed to load target thread after agent-tool message")
		targetThread = models.SessionThread{ID: threadID, Status: result.ThreadStatus}
	}
	h.emitToolAudit(r, models.AuditActionSessionThreadMessagedByAgentTool, scope, threadID, targetThread.AgentType, targetThread.ModelOverride, "session_tabs_send", len(body.Message))
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]any]{Data: map[string]any{
		"message":        result.Message,
		"thread":         map[string]any{"id": threadID, "status": targetThread.Status, "pending_message_count": targetThread.PendingMessageCount},
		"delivery_state": delivery,
	}})
}

func (h *InternalSessionTabsHandler) Messages(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.authorize(w, r)
	if !ok {
		return
	}
	threadID, err := uuid.Parse(chi.URLParam(r, "thread_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid tab ID")
		return
	}
	query := r.URL.Query()
	limit := 0
	if rawLimit := strings.TrimSpace(query.Get("limit")); rawLimit != "" {
		parsedLimit, err := strconv.Atoi(rawLimit)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_LIMIT", "limit must be an integer", err)
			return
		}
		limit = parsedLimit
	}
	beforeID := int64(0)
	if rawBefore := strings.TrimSpace(query.Get("before")); rawBefore != "" {
		parsedBefore, err := strconv.ParseInt(rawBefore, 10, 64)
		if err != nil || parsedBefore < 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_BEFORE", "before must be a message cursor", err)
			return
		}
		beforeID = parsedBefore
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	position := strings.TrimSpace(strings.ToLower(query.Get("position")))
	if position != "" && position != "latest" {
		writeError(w, r, http.StatusBadRequest, "INVALID_POSITION", "position must be latest")
		return
	}
	// include_tool_events is parsed for validation only; v1 always filters tool
	// events at the service layer regardless of the flag value.
	if _, err := parseOptionalBool(query.Get("include_tool_events")); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_INCLUDE_TOOL_EVENTS", "include_tool_events must be a boolean", err)
		return
	}
	window, err := h.svc.GetMessageWindow(r.Context(), scope.OrgID, scope.SessionID, threadID, db.SessionMessageWindowOptions{BeforeID: beforeID, Limit: limit})
	if err != nil {
		if errors.Is(err, thread.ErrThreadNotFound) || errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "TAB_NOT_FOUND", "tab not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "LIST_MESSAGES_FAILED", "failed to list tab messages", err)
		return
	}
	msgs := window.Window.Messages
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionMessage]{
		Data: msgs,
		Meta: models.PaginationMeta{NextCursor: window.Window.NextOlderCursor},
	})
}

func (h *InternalSessionTabsHandler) authorize(w http.ResponseWriter, r *http.Request) (sandboxSessionTabScope, bool) {
	tokenStr := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tokenStr == "" {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "missing sandbox token")
		return sandboxSessionTabScope{}, false
	}
	claims, err := auth.ValidateInternalToken(h.signingSecret, tokenStr)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "invalid sandbox token", err)
		return sandboxSessionTabScope{}, false
	}
	if claims.SessionID == nil || *claims.SessionID == uuid.Nil {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "sandbox token is not scoped to a session")
		return sandboxSessionTabScope{}, false
	}
	session, err := h.sessionStore.GetByID(r.Context(), claims.OrgID, *claims.SessionID)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", claims.SessionID.String()).Msg("session lookup failed during tab-tool auth")
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "sandbox token is not authorized for this session")
		return sandboxSessionTabScope{}, false
	}
	if session.RepositoryID == nil || *session.RepositoryID != claims.RepoID {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "sandbox token is not authorized for this session")
		return sandboxSessionTabScope{}, false
	}
	org, err := h.orgStore.GetByID(r.Context(), claims.OrgID)
	if err != nil {
		h.logger.Warn().Err(err).Str("org_id", claims.OrgID.String()).Msg("org lookup failed during tab-tool auth")
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "sandbox token organization is unavailable")
		return sandboxSessionTabScope{}, false
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		h.logger.Warn().Err(err).Str("org_id", claims.OrgID.String()).Msg("failed to parse org settings for session tab tool")
		writeError(w, r, http.StatusForbidden, "TAB_TOOLS_DISABLED", "agent tab tools are disabled for this organization")
		return sandboxSessionTabScope{}, false
	}
	if !settings.EffectiveCodingAgentTabToolsEnabled() {
		writeError(w, r, http.StatusForbidden, "TAB_TOOLS_DISABLED", "agent tab tools are disabled for this organization")
		return sandboxSessionTabScope{}, false
	}
	return sandboxSessionTabScope{
		OrgID:          claims.OrgID,
		RepoID:         claims.RepoID,
		SessionID:      *claims.SessionID,
		SourceThreadID: claims.ThreadID,
		Settings:       settings,
	}, true
}

func (h *InternalSessionTabsHandler) tabItem(t models.SessionThread, sourceThreadID *uuid.UUID) sandboxTabListItem {
	isCurrent := sourceThreadID != nil && *sourceThreadID == t.ID
	return sandboxTabListItem{
		ID:                  t.ID,
		Label:               t.Label,
		AgentType:           t.AgentType,
		ModelOverride:       t.ModelOverride,
		Status:              t.Status,
		CurrentTurn:         t.CurrentTurn,
		LastActivityAt:      t.LastActivityAt,
		PendingMessageCount: t.PendingMessageCount,
		InboxDelivery:       t.InboxDelivery,
		CostCents:           t.CostCents,
		ResultSummary:       t.ResultSummary,
		CreatedAt:           t.CreatedAt,
		CreatedBySource:     t.CreatedBySource,
		CreatedByThreadID:   t.CreatedByThreadID,
		IsCurrent:           isCurrent,
	}
}

func (h *InternalSessionTabsHandler) listThreads(ctx context.Context, scope sandboxSessionTabScope, includeArchived bool) ([]models.SessionThread, error) {
	if includeArchived {
		if lister, ok := h.svc.(internalThreadListerWithOptions); ok {
			return lister.ListThreadsWithOptions(ctx, scope.OrgID, scope.SessionID, thread.ListThreadsOptions{IncludeArchived: true})
		}
	}
	return h.svc.ListThreads(ctx, scope.OrgID, scope.SessionID)
}

func parseOptionalBool(value string) (bool, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case "":
		return false, nil
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value %q", value)
	}
}

func (h *InternalSessionTabsHandler) recentFiles(r *http.Request, scope sandboxSessionTabScope, threadID uuid.UUID) []sandboxRecentFile {
	events, err := h.svc.ListFileEvents(r.Context(), scope.OrgID, scope.SessionID, nil)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", scope.SessionID.String()).Msg("failed to list recent files for session tab tool")
		return []sandboxRecentFile{}
	}
	byPath := map[string]sandboxRecentFile{}
	for _, event := range events {
		if event.ThreadID == nil || *event.ThreadID != threadID {
			continue
		}
		existing, ok := byPath[event.Path]
		if !ok || existing.LastSeenAt.Before(event.ObservedAt) {
			byPath[event.Path] = sandboxRecentFile{Path: event.Path, Operation: string(event.EventType), LastSeenAt: event.ObservedAt}
		}
	}
	out := make([]sandboxRecentFile, 0, len(byPath))
	for _, item := range byPath {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeenAt.Equal(out[j].LastSeenAt) {
			return out[i].LastSeenAt.After(out[j].LastSeenAt)
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func (h *InternalSessionTabsHandler) emitToolAudit(r *http.Request, action models.AuditAction, scope sandboxSessionTabScope, targetThreadID uuid.UUID, agentType models.AgentType, model *string, toolName string, messageLength int) {
	if h.audit == nil {
		return
	}
	resourceID := targetThreadID.String()
	details := buildSessionTabToolAuditDetails(scope, targetThreadID, agentType, model, toolName, messageLength)
	h.audit.EmitSystemAction(r.Context(), db.SystemActionParams{
		OrgID:        scope.OrgID,
		ActorID:      "agent_tool",
		Action:       action,
		ResourceType: models.AuditResourceSession,
		ResourceID:   &resourceID,
		Details:      marshalAuditDetails(h.logger, details),
		SessionID:    &scope.SessionID,
	})
}

func buildSessionTabToolAuditDetails(scope sandboxSessionTabScope, targetThreadID uuid.UUID, agentType models.AgentType, model *string, toolName string, messageLength int) map[string]any {
	details := map[string]any{
		"session_id":       scope.SessionID.String(),
		"target_thread_id": targetThreadID.String(),
		"tool_name":        toolName,
		"message_length":   messageLength,
	}
	if scope.SourceThreadID != nil {
		details["source_thread_id"] = scope.SourceThreadID.String()
	}
	if agentType != "" {
		details["agent_type"] = string(agentType)
	}
	if model != nil {
		details["model"] = *model
	}
	return details
}
