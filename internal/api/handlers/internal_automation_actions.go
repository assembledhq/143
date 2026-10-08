package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/automationactions"
	"github.com/rs/zerolog"
)

type automationActionService interface {
	Execute(context.Context, models.AutomationActionActor, models.AutomationActionKey, *models.AutomationActionRequest) (models.AutomationActionResult, error)
	Status(context.Context, models.AutomationActionActor, string) (models.AutomationActionResult, error)
}
type InternalAutomationActionHandler struct {
	sessions internalSessionGetter
	service  automationActionService
	secret   string
}

func NewInternalAutomationActionHandler(sessions internalSessionGetter, service automationActionService, secret string) *InternalAutomationActionHandler {
	return &InternalAutomationActionHandler{sessions: sessions, service: service, secret: secret}
}
func (h *InternalAutomationActionHandler) Request(w http.ResponseWriter, r *http.Request) {
	h.execute(w, r, false)
}
func (h *InternalAutomationActionHandler) Resume(w http.ResponseWriter, r *http.Request) {
	h.execute(w, r, true)
}
func (h *InternalAutomationActionHandler) authorize(w http.ResponseWriter, r *http.Request, scope string) (models.AutomationActionActor, bool) {
	claims, _, ok := authorizeInternalSession(w, r, h.secret, h.sessions)
	if !ok {
		return models.AutomationActionActor{}, false
	}
	if claims.AutomationRunID == nil || claims.AutomationAttemptToken == nil || claims.AutomationJobID == nil || claims.ThreadID == nil || !models.HasToolScope(claims.AllowedToolScopes, models.ToolScope(scope)) {
		writeError(w, r, http.StatusForbidden, "TOOL_NOT_ALLOWED", "This tool requires an authorized automation turn")
		return models.AutomationActionActor{}, false
	}
	return models.AutomationActionActor{OrgID: claims.OrgID, RepositoryID: claims.RepoID, SessionID: *claims.SessionID, ThreadID: *claims.ThreadID, RunID: *claims.AutomationRunID, AttemptToken: *claims.AutomationAttemptToken, JobID: *claims.AutomationJobID}, true
}
func (h *InternalAutomationActionHandler) execute(w http.ResponseWriter, r *http.Request, resume bool) {
	actor, ok := h.authorize(w, r, "automation:execute-action")
	if !ok {
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	var body models.AutomationActionRequest
	var request *models.AutomationActionRequest
	var key models.AutomationActionKey
	var err error
	if resume {
		err = dec.Decode(&key)
	} else {
		err = dec.Decode(&body)
		request = &body
		key = body.AutomationActionKey
	}
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ACTION_REQUEST", "Invalid automation request", err)
		return
	}
	if err = dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "INVALID_ACTION_REQUEST", "Expected one JSON object")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 11*time.Second)
	defer cancel()
	result, err := h.service.Execute(ctx, actor, key, request)
	h.respond(w, r, result, err)
}
func (h *InternalAutomationActionHandler) Status(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.authorize(w, r, "automation:action-status")
	if !ok {
		return
	}
	result, err := h.service.Status(r.Context(), actor, r.URL.Query().Get("operation_key"))
	h.respond(w, r, result, err)
}
func (h *InternalAutomationActionHandler) respond(w http.ResponseWriter, r *http.Request, result models.AutomationActionResult, err error) {
	if err != nil {
		status, code, message := http.StatusBadGateway, "AUTOMATION_ACTION_FAILED", "Could not complete the request; inspect action status before retrying"
		switch {
		case errors.Is(err, db.ErrAutomationActionUnauthorized):
			status, code, message = http.StatusForbidden, "CAPABILITY_REQUIRED", "The current turn is not authorized for automation actions"
		case errors.Is(err, automationactions.ErrBudgetExhausted):
			status, code, message = http.StatusServiceUnavailable, "BUDGET_EXHAUSTED", "No send started; resume the pending action with the same keys"
		case errors.Is(err, automationactions.ErrInvalidRequest), errors.Is(err, db.ErrAutomationActionLimit):
			status, code, message = http.StatusBadRequest, "INVALID_ACTION_REQUEST", "Invalid action kind, key, or content"
		case errors.Is(err, automationactions.ErrStaleHead):
			status, code, message = http.StatusConflict, "STALE_HEAD", "The pull request changed; inspect action status before proceeding"
		case errors.Is(err, db.ErrAutomationActionConflict):
			status, code, message = http.StatusConflict, "ACTION_PAYLOAD_CONFLICT", "This automation action has different recorded content or destinations; inspect status"
		case errors.Is(err, automationactions.ErrIntegrationNotReady):
			status, code, message = http.StatusFailedDependency, "INTEGRATION_NOT_READY", "Check the configured provider credentials and Notion schema"
		}
		if len(result.Actions) > 0 {
			zerolog.Ctx(r.Context()).Warn().Err(err).Str("code", code).Msg("automation actions stopped with recorded receipts")
			result.ErrorCode = code
			writeJSON(w, http.StatusOK, models.SingleResponse[models.AutomationActionResult]{Data: result})
		} else {
			writeError(w, r, status, code, message, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.AutomationActionResult]{Data: result})
}
