package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
	automationservice "github.com/assembledhq/143/internal/services/automations"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (h *AutomationHandler) CreateDraftGoalImprovement(w http.ResponseWriter, r *http.Request) {
	if h.goalImprovement == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GOAL_IMPROVEMENT_UNAVAILABLE", "automation goal improvement is not available")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	var req struct {
		Mode         models.AutomationGoalImprovementMode `json:"mode"`
		Name         string                               `json:"name"`
		Goal         string                               `json:"goal"`
		RepositoryID *uuid.UUID                           `json:"repository_id"`
		Scope        *string                              `json:"scope"`
		Config       json.RawMessage                      `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if req.Mode == "" {
		req.Mode = models.AutomationGoalImprovementModeFast
	}
	improvement, err := h.goalImprovement.ImproveDraft(r.Context(), orgID, automationservice.DraftGoalImprovementRequest{
		Mode:         req.Mode,
		Name:         req.Name,
		Goal:         req.Goal,
		RepositoryID: req.RepositoryID,
		Scope:        req.Scope,
		Config:       req.Config,
		CreatedBy:    userIDPtr(user),
	})
	if err != nil {
		h.writeGoalImprovementError(w, r, err)
		return
	}
	h.emitGoalImprovementAudit(r, models.AuditActionAutomationGoalImprovementRequested, nil, improvement)
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.AutomationGoalImprovement]{Data: improvement})
}

func (h *AutomationHandler) CreateSavedGoalImprovement(w http.ResponseWriter, r *http.Request) {
	if h.goalImprovement == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GOAL_IMPROVEMENT_UNAVAILABLE", "automation goal improvement is not available")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return
	}
	var req struct {
		Mode              models.AutomationGoalImprovementMode `json:"mode"`
		IncludeRecentRuns int                                  `json:"include_recent_runs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if req.Mode == "" {
		req.Mode = models.AutomationGoalImprovementModeFast
	}
	improvement, err := h.goalImprovement.ImproveSaved(r.Context(), orgID, automationservice.SavedGoalImprovementRequest{
		Mode:              req.Mode,
		AutomationID:      automationID,
		IncludeRecentRuns: req.IncludeRecentRuns,
		CreatedBy:         userIDPtr(user),
	})
	if err != nil {
		h.writeGoalImprovementError(w, r, err)
		return
	}
	h.emitGoalImprovementAudit(r, models.AuditActionAutomationGoalImprovementRequested, &automationID, improvement)
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.AutomationGoalImprovement]{Data: improvement})
}

func (h *AutomationHandler) GetGoalImprovement(w http.ResponseWriter, r *http.Request) {
	if h.goalImprovement == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GOAL_IMPROVEMENT_UNAVAILABLE", "automation goal improvement is not available")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	improvementID, err := uuid.Parse(chi.URLParam(r, "improvement_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid improvement ID")
		return
	}
	improvement, err := h.goalImprovement.Get(r.Context(), orgID, improvementID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "automation goal improvement not found", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.AutomationGoalImprovement]{Data: improvement})
}

func (h *AutomationHandler) ListGoalImprovements(w http.ResponseWriter, r *http.Request) {
	if h.goalImprovement == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GOAL_IMPROVEMENT_UNAVAILABLE", "automation goal improvement is not available")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return
	}
	limit := 10
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_LIMIT", "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	improvements, err := h.goalImprovement.ListByAutomation(r.Context(), orgID, automationID, limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list automation goal improvements", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.AutomationGoalImprovement]{Data: improvements})
}

func (h *AutomationHandler) CancelGoalImprovement(w http.ResponseWriter, r *http.Request) {
	if h.goalImprovement == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GOAL_IMPROVEMENT_UNAVAILABLE", "automation goal improvement is not available")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	improvementID, err := uuid.Parse(chi.URLParam(r, "improvement_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid improvement ID")
		return
	}
	improvement, err := h.goalImprovement.Get(r.Context(), orgID, improvementID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "automation goal improvement not found", err)
		return
	}
	switch improvement.Status {
	case models.AutomationGoalImprovementStatusPending, models.AutomationGoalImprovementStatusRunning:
	default:
		writeError(w, r, http.StatusConflict, "IMPROVEMENT_NOT_CANCELABLE", "only pending or running automation goal improvements can be canceled")
		return
	}
	if err := h.goalImprovement.Cancel(r.Context(), orgID, improvementID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CANCEL_FAILED", "failed to cancel automation goal improvement", err)
		return
	}
	if improvement.AnalysisSessionID != nil && h.canceller != nil {
		h.canceller.CancelSession(*improvement.AnalysisSessionID)
	}
	improvement.Status = models.AutomationGoalImprovementStatusCanceled
	msg := "proposal was canceled by the user"
	improvement.ErrorMessage = &msg
	h.emitGoalImprovementAudit(r, models.AuditActionAutomationGoalImprovementCanceled, improvement.AutomationID, improvement)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.AutomationGoalImprovement]{Data: improvement})
}

func (h *AutomationHandler) ApplyGoalImprovement(w http.ResponseWriter, r *http.Request) {
	if h.goalImprovement == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GOAL_IMPROVEMENT_UNAVAILABLE", "automation goal improvement is not available")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	automationID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid automation ID")
		return
	}
	improvementID, err := uuid.Parse(chi.URLParam(r, "improvement_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid improvement ID")
		return
	}
	var req struct {
		ExpectedBaseGoalHash string `json:"expected_base_goal_hash"`
		ProposedGoal         string `json:"proposed_goal"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if strings.TrimSpace(req.ExpectedBaseGoalHash) == "" || strings.TrimSpace(req.ProposedGoal) == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "expected_base_goal_hash and proposed_goal are required")
		return
	}
	goal := strings.TrimSpace(req.ProposedGoal)
	if len(goal) > automationGoalMaxLength {
		writeError(w, r, http.StatusBadRequest, "INVALID_FIELD", "goal is too long")
		return
	}
	applied, err := h.goalImprovement.ApplySaved(r.Context(), orgID, automationservice.ApplySavedGoalImprovementRequest{
		AutomationID:         automationID,
		ImprovementID:        improvementID,
		ExpectedBaseGoalHash: req.ExpectedBaseGoalHash,
		ProposedGoal:         goal,
		AppliedBy:            userIDPtr(user),
	})
	if err != nil {
		h.writeApplyGoalImprovementError(w, r, err)
		return
	}
	idStr := applied.Automation.ID.String()
	details := automationAuditDiff(&applied.Before, &applied.Automation)
	details["automation_goal_improvement_id"] = improvementID.String()
	details["mode"] = string(applied.Improvement.Mode)
	details["base_goal_hash"] = applied.Improvement.BaseGoalHash
	emitUserAuditWithSession(h.audit, r, models.AuditActionAutomationGoalImprovementApplied, models.AuditResourceAutomation, &idStr, nil, nil,
		marshalAuditDetails(h.logger, details))
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Automation]{Data: applied.Automation})
}

func (h *AutomationHandler) emitGoalImprovementAudit(r *http.Request, action models.AuditAction, automationID *uuid.UUID, improvement models.AutomationGoalImprovement) {
	var resourceID *string
	if automationID != nil {
		idStr := automationID.String()
		resourceID = &idStr
	}
	details := map[string]any{
		"automation_goal_improvement_id": improvement.ID.String(),
		"mode":                           string(improvement.Mode),
		"status":                         string(improvement.Status),
		"base_goal_hash":                 improvement.BaseGoalHash,
	}
	if improvement.AnalysisSessionID != nil {
		details["analysis_session_id"] = improvement.AnalysisSessionID.String()
	}
	emitUserAuditWithSession(h.audit, r, action, models.AuditResourceAutomation, resourceID, improvement.AnalysisSessionID, nil,
		marshalAuditDetails(h.logger, details))
}

func (h *AutomationHandler) writeGoalImprovementError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, automationservice.ErrGoalImprovementLLMUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "LLM_NOT_CONFIGURED", "automation goal improvement is not available: no LLM provider configured", err)
	case errors.Is(err, automationservice.ErrGoalImprovementAlreadyRunning):
		writeError(w, r, http.StatusConflict, "DEEP_IMPROVEMENT_RUNNING", "a deep goal improvement is already running for this automation", err)
	case errors.Is(err, automationservice.ErrGoalRequired):
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "goal is required", err)
	case errors.Is(err, automationservice.ErrGoalRequiresRepository):
		writeError(w, r, http.StatusBadRequest, "INVALID_GOAL_IMPROVEMENT_REQUEST", err.Error(), err)
	default:
		writeError(w, r, http.StatusInternalServerError, "GOAL_IMPROVEMENT_FAILED", "failed to improve automation goal", err)
	}
}

func (h *AutomationHandler) writeApplyGoalImprovementError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, automationservice.ErrGoalImprovementNotCompleted):
		writeError(w, r, http.StatusConflict, "IMPROVEMENT_NOT_COMPLETED", "automation goal improvement is not completed", err)
	case errors.Is(err, automationservice.ErrGoalImprovementStaleGoal):
		writeError(w, r, http.StatusConflict, "STALE_GOAL", "automation goal changed since this improvement was generated", err)
	case errors.Is(err, automationservice.ErrGoalImprovementProposedGoalRequired):
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "expected_base_goal_hash and proposed_goal are required", err)
	default:
		writeError(w, r, http.StatusInternalServerError, "APPLY_FAILED", "failed to apply automation goal improvement", err)
	}
}
