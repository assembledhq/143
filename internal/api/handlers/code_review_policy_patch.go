package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
)

func (h *CodeReviewHandler) PatchPolicy(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, 401, "UNAUTHORIZED", "user is required")
		return
	}
	var req struct {
		Config          json.RawMessage                   `json:"config"`
		ExpectedVersion *int                              `json:"expected_version"`
		Source          models.CodeReviewPolicyEditSource `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ExpectedVersion == nil || *req.ExpectedVersion < 0 || len(req.Config) == 0 {
		writeError(w, r, 400, "CODE_REVIEW_POLICY_INVALID", "config and a nonnegative expected_version are required")
		return
	}
	var supplied map[string]json.RawMessage
	if err := json.Unmarshal(req.Config, &supplied); err != nil {
		writeError(w, r, 400, "CODE_REVIEW_POLICY_INVALID", "config must be an object")
		return
	}
	if _, present := supplied["scheduling_policy"]; present {
		scheduler, enabled := h.retryService.(codeReviewSchedulingService)
		if !enabled || !scheduler.SchedulingEnabled() {
			writeError(w, r, 409, "CODE_REVIEW_CAPABILITY_UNAVAILABLE", "review scheduling is not enabled")
			return
		}
	}
	if req.Source == "" {
		req.Source = models.CodeReviewPolicyEditSourceManual
	}
	if err := req.Source.Validate(); err != nil {
		writeError(w, r, 400, "INVALID_SOURCE", "invalid policy edit source", err)
		return
	}
	record, err := h.store.PatchPolicy(r.Context(), orgID, req.Config, *req.ExpectedVersion, &user.ID)
	if errors.Is(err, db.ErrCodeReviewPolicyVersionConflict) {
		current, loadErr := h.store.ResolvePolicy(r.Context(), orgID)
		if loadErr != nil {
			writeError(w, r, 500, "CODE_REVIEW_POLICY_LOAD_FAILED", "failed to load current policy", loadErr)
			return
		}
		version := 0
		if current.Policy != nil {
			version = current.Policy.Version
		}
		writeErrorWithDetails(w, r, 409, "CODE_REVIEW_POLICY_VERSION_CONFLICT", "Policy changed. Reload before saving your edits.", map[string]int{"current_version": version}, err)
		return
	}
	if err != nil {
		var validation *models.CodeReviewPolicyValidationError
		if errors.As(err, &validation) {
			writeErrorWithDetails(w, r, 400, "CODE_REVIEW_POLICY_INVALID", "invalid policy", map[string]string{"field": validation.Field}, err)
			return
		}
		writeError(w, r, 500, "CODE_REVIEW_POLICY_SAVE_FAILED", "failed to save policy", err)
		return
	}
	writeJSON(w, 200, models.SingleResponse[models.CodeReviewPolicyRecord]{Data: record})
	details := marshalAuditDetails(*zerolog.Ctx(r.Context()), map[string]any{"source": req.Source, "version": record.Version})
	id := record.ID.String()
	emitUserAudit(h.audit, r, models.AuditActionCodeReviewPolicyUpdated, models.AuditResourceCodeReviewPolicy, &id, details)
}
