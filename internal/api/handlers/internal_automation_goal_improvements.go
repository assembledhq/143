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
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type automationGoalImprovementCompleter interface {
	CompleteDeepFromAgent(ctx context.Context, orgID uuid.UUID, req automationservice.CompleteDeepGoalImprovementRequest) (models.AutomationGoalImprovement, error)
}

type InternalAutomationGoalImprovementHandler struct {
	service       automationGoalImprovementCompleter
	sessionStore  internalSessionLookup
	signingSecret string
	logger        zerolog.Logger
}

func NewInternalAutomationGoalImprovementHandler(service automationGoalImprovementCompleter, sessionStore internalSessionLookup, signingSecret string, logger zerolog.Logger) *InternalAutomationGoalImprovementHandler {
	return &InternalAutomationGoalImprovementHandler{
		service:       service,
		sessionStore:  sessionStore,
		signingSecret: signingSecret,
		logger:        logger,
	}
}

func (h *InternalAutomationGoalImprovementHandler) Complete(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.authorizeAutomationGoalImprovementTool(w, r)
	if !ok {
		return
	}
	improvementID, err := uuid.Parse(chi.URLParam(r, "improvement_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid improvement_id", err)
		return
	}
	var params integration.CompleteAutomationGoalImprovementParams
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&params); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	if strings.TrimSpace(params.ImprovementID) != "" {
		bodyID, err := uuid.Parse(params.ImprovementID)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid body improvement_id", err)
			return
		}
		if bodyID != improvementID {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "sandbox token is not authorized for this improvement")
			return
		}
	}
	improvement, err := h.service.CompleteDeepFromAgent(r.Context(), scope.OrgID, automationservice.CompleteDeepGoalImprovementRequest{
		SessionID:     scope.SessionID,
		ImprovementID: improvementID,
		ProposedGoal:  params.ProposedGoal,
		Rationale:     params.Rationale,
		Changes:       params.Changes,
		Evidence:      params.Evidence,
		Risks:         params.Risks,
		Confidence:    params.Confidence,
		Warnings:      params.Warnings,
	})
	if err != nil {
		writeCompleteGoalImprovementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[integration.CompleteAutomationGoalImprovementResult]{
		Data: integration.CompleteAutomationGoalImprovementResult{
			ImprovementID: improvement.ID.String(),
			Status:        string(improvement.Status),
		},
	})
}

type automationGoalImprovementToolScope struct {
	OrgID     uuid.UUID
	SessionID uuid.UUID
}

func (h *InternalAutomationGoalImprovementHandler) authorizeAutomationGoalImprovementTool(w http.ResponseWriter, r *http.Request) (automationGoalImprovementToolScope, bool) {
	tokenStr := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tokenStr == "" {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "missing sandbox token")
		return automationGoalImprovementToolScope{}, false
	}
	claims, err := auth.ValidateInternalToken(h.signingSecret, tokenStr)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "invalid sandbox token", err)
		return automationGoalImprovementToolScope{}, false
	}
	if claims.SessionID == nil || *claims.SessionID == uuid.Nil {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "sandbox token is not scoped to a session")
		return automationGoalImprovementToolScope{}, false
	}
	if claims.SessionOrigin != string(models.SessionOriginAutomationGoalImprovement) || !hasInternalToolScope(claims.AllowedToolScopes, "automation-goal-improvement:complete") {
		writeError(w, r, http.StatusForbidden, "AUTOMATION_GOAL_IMPROVEMENT_TOOL_NOT_AVAILABLE", "sandbox token does not allow automation-goal-improvement:complete")
		return automationGoalImprovementToolScope{}, false
	}
	session, err := h.sessionStore.GetByID(r.Context(), claims.OrgID, *claims.SessionID)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", claims.SessionID.String()).Msg("session lookup failed during automation-goal-improvement-tool auth")
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "sandbox token is not authorized for this session")
		return automationGoalImprovementToolScope{}, false
	}
	if session.RepositoryID == nil || *session.RepositoryID != claims.RepoID {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "sandbox token is not authorized for this repository")
		return automationGoalImprovementToolScope{}, false
	}
	if session.Origin != models.SessionOriginAutomationGoalImprovement {
		writeError(w, r, http.StatusForbidden, "AUTOMATION_GOAL_IMPROVEMENT_TOOL_NOT_AVAILABLE", "automation goal improvement tools are only available to automation goal improvement sessions")
		return automationGoalImprovementToolScope{}, false
	}
	return automationGoalImprovementToolScope{OrgID: claims.OrgID, SessionID: *claims.SessionID}, true
}

func writeCompleteGoalImprovementError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, automationservice.ErrGoalImprovementNotDeep),
		errors.Is(err, automationservice.ErrGoalImprovementSessionMismatch):
		writeError(w, r, http.StatusForbidden, "COMPLETE_FORBIDDEN", err.Error(), err)
	case errors.Is(err, automationservice.ErrGoalImprovementNotRunning):
		writeError(w, r, http.StatusConflict, "COMPLETE_CONFLICT", err.Error(), err)
	case errors.Is(err, automationservice.ErrGoalImprovementProposedGoalRequired),
		errors.Is(err, automationservice.ErrGoalImprovementRationaleRequired):
		writeError(w, r, http.StatusBadRequest, "COMPLETE_VALIDATION", err.Error(), err)
	case errors.Is(err, automationservice.ErrGoalImprovementProposalRejected):
		writeError(w, r, http.StatusUnprocessableEntity, "PROPOSAL_REJECTED", err.Error(), err)
	default:
		writeError(w, r, http.StatusInternalServerError, "COMPLETE_FAILED", "failed to complete automation goal improvement", err)
	}
}
