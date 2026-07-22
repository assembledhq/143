package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
	automationservice "github.com/assembledhq/143/internal/services/automations"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type automationOutcomeReporter interface {
	Report(ctx context.Context, orgID uuid.UUID, req automationservice.ReportOutcomeRequest) (models.AutomationRunOutcome, error)
}

type InternalAutomationOutcomeHandler struct {
	service       automationOutcomeReporter
	sessionStore  internalSessionLookup
	signingSecret string
	logger        zerolog.Logger
}

func NewInternalAutomationOutcomeHandler(service automationOutcomeReporter, sessionStore internalSessionLookup, signingSecret string, logger zerolog.Logger) *InternalAutomationOutcomeHandler {
	return &InternalAutomationOutcomeHandler{
		service: service, sessionStore: sessionStore, signingSecret: signingSecret, logger: logger,
	}
}

type automationOutcomeToolScope struct {
	OrgID     uuid.UUID
	SessionID uuid.UUID
	RunID     uuid.UUID
}

func (h *InternalAutomationOutcomeHandler) Report(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var params integration.ReportAutomationOutcomeParams
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&params); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	decision := models.AutomationOutcomeDecision(strings.TrimSpace(params.Decision))
	if err := decision.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_DECISION", err.Error(), err)
		return
	}
	actionType := models.AutomationExternalActionType(strings.TrimSpace(params.ExternalActionType))
	if actionType != "" {
		if err := actionType.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_EXTERNAL_ACTION", err.Error(), err)
			return
		}
	}
	outcome, err := h.service.Report(r.Context(), scope.OrgID, automationservice.ReportOutcomeRequest{
		SessionID:          scope.SessionID,
		RunID:              scope.RunID,
		Decision:           decision,
		Reason:             params.Reason,
		PullRequestTitle:   params.PullRequestTitle,
		HeadSHA:            params.HeadSHA,
		ExternalActionType: actionType,
		ExternalActionURL:  params.ExternalActionURL,
		ExternalActionID:   params.ExternalActionID,
	})
	if err != nil {
		h.writeReportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[integration.ReportAutomationOutcomeResult]{
		Data: integration.ReportAutomationOutcomeResult{
			OutcomeID: outcome.ID.String(), AutomationRunID: outcome.AutomationRunID.String(),
			Decision: string(outcome.Decision), Status: "recorded",
		},
	})
}

func (h *InternalAutomationOutcomeHandler) authorize(w http.ResponseWriter, r *http.Request) (automationOutcomeToolScope, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "missing sandbox token")
		return automationOutcomeToolScope{}, false
	}
	claims, err := auth.ValidateInternalToken(h.signingSecret, token)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "invalid sandbox token", err)
		return automationOutcomeToolScope{}, false
	}
	if claims.SessionID == nil || *claims.SessionID == uuid.Nil || claims.ThreadID == nil || *claims.ThreadID == uuid.Nil {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "sandbox token is not scoped to a session thread")
		return automationOutcomeToolScope{}, false
	}
	if claims.SessionOrigin != string(models.SessionOriginAutomation) || !hasInternalToolScope(claims.AllowedToolScopes, "automation-run:report-outcome") {
		writeError(w, r, http.StatusForbidden, "AUTOMATION_OUTCOME_TOOL_NOT_AVAILABLE", "sandbox token does not allow automation-run:report-outcome")
		return automationOutcomeToolScope{}, false
	}
	session, err := h.sessionStore.GetByID(r.Context(), claims.OrgID, *claims.SessionID)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", claims.SessionID.String()).Msg("session lookup failed during automation outcome tool auth")
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "sandbox token is not authorized for this session")
		return automationOutcomeToolScope{}, false
	}
	if session.RepositoryID == nil || *session.RepositoryID != claims.RepoID || session.Origin != models.SessionOriginAutomation || session.AutomationRunID == nil || *session.AutomationRunID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "AUTOMATION_OUTCOME_TOOL_NOT_AVAILABLE", "structured outcomes are only available to automation sessions")
		return automationOutcomeToolScope{}, false
	}
	return automationOutcomeToolScope{OrgID: claims.OrgID, SessionID: session.ID, RunID: *session.AutomationRunID}, true
}

func (h *InternalAutomationOutcomeHandler) writeReportError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, automationservice.ErrOutcomeReasonRequired),
		errors.Is(err, automationservice.ErrOutcomeActionRequired),
		errors.Is(err, automationservice.ErrOutcomeActionInvalid):
		writeError(w, r, http.StatusBadRequest, "OUTCOME_VALIDATION", err.Error(), err)
	case errors.Is(err, automationservice.ErrOutcomeTargetUnavailable):
		writeError(w, r, http.StatusUnprocessableEntity, "OUTCOME_TARGET_UNAVAILABLE", err.Error(), err)
	case errors.Is(err, automationservice.ErrOutcomeSessionMismatch):
		writeError(w, r, http.StatusForbidden, "OUTCOME_FORBIDDEN", err.Error(), err)
	case errors.Is(err, automationservice.ErrOutcomeAlreadyReported):
		writeError(w, r, http.StatusConflict, "OUTCOME_ALREADY_REPORTED", err.Error(), err)
	default:
		writeError(w, r, http.StatusInternalServerError, "OUTCOME_REPORT_FAILED", "failed to report automation outcome", err)
	}
}
