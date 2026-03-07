package handlers

import (
	"fmt"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type PMHandler struct {
	planStore        *db.PMPlanStore
	decisionLogStore *db.PMDecisionLogStore
	jobStore         *db.JobStore
}

func NewPMHandler(planStore *db.PMPlanStore, decisionLogStore *db.PMDecisionLogStore, jobStore *db.JobStore) *PMHandler {
	return &PMHandler{planStore: planStore, decisionLogStore: decisionLogStore, jobStore: jobStore}
}

// Analyze enqueues a PM analysis job.
func (h *PMHandler) Analyze(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	payload := map[string]string{
		"org_id":  orgID.String(),
		"trigger": string(models.PMTriggerManual),
	}
	jobID, err := h.jobStore.Enqueue(r.Context(), orgID, "default", "pm_analyze", payload, 5, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue pm analyze job")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"data": map[string]string{"job_id": jobID.String()},
	})
}

func (h *PMHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	limit := queryInt(r, "limit", 50)
	filters := db.PMPlanFilters{
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
	}

	plans, err := h.planStore.ListByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list pm plans")
		return
	}
	if plans == nil {
		plans = []models.PMPlan{}
	}

	var nextCursor string
	if len(plans) > 0 && len(plans) == filters.Limit {
		nextCursor = db.FormatPMPlanCursor(plans[len(plans)-1])
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.PMPlan]{
		Data: plans,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

func (h *PMHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	planID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid plan ID")
		return
	}

	plan, err := h.planStore.GetByID(r.Context(), orgID, planID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "plan not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PMPlan]{Data: plan})
}

func (h *PMHandler) Latest(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	plan, err := h.planStore.GetLatestByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "plan not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PMPlan]{Data: plan})
}

// decisionsResponse is the response for the decisions endpoint.
type decisionsResponse struct {
	Data    []models.PMDecisionView `json:"data"`
	Summary models.PMDecisionSummary `json:"summary"`
	Meta    models.PaginationMeta    `json:"meta"`
}

// Decisions returns the PM decision history with success rate and project info.
func (h *PMHandler) Decisions(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	limit := queryInt(r, "limit", 50)
	filters := db.PMDecisionFilters{
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
	}

	decisions, err := h.decisionLogStore.ListDecisionViews(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list pm decisions")
		return
	}
	if decisions == nil {
		decisions = []models.PMDecisionView{}
	}

	summary, err := h.decisionLogStore.GetDecisionSummary(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SUMMARY_FAILED", "failed to get decision summary")
		return
	}

	var nextCursor string
	if len(decisions) > 0 && len(decisions) == limit {
		nextCursor = decisions[len(decisions)-1].ID.String()
	}

	writeJSON(w, http.StatusOK, decisionsResponse{
		Data:    decisions,
		Summary: summary,
		Meta:    models.PaginationMeta{NextCursor: nextCursor},
	})
}

// Status returns the PM agent's current state for the status banner.
func (h *PMHandler) Status(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	status := models.PMStatus{}

	// Get latest plan to determine last run and whether running.
	latestPlan, err := h.planStore.GetLatestByOrg(r.Context(), orgID)
	if err == nil {
		status.LastRunAt = &latestPlan.CreatedAt
		status.LastRunStatus = string(latestPlan.Status)
		status.IssuesReviewed = latestPlan.IssuesReviewed
		status.IsRunning = latestPlan.Status == models.PMPlanStatusExecuting
	}

	// Get decision success rate.
	summary, err := h.decisionLogStore.GetDecisionSummary(r.Context(), orgID)
	if err == nil {
		status.TotalDelegated = summary.TotalDelegated
		status.SuccessCount = summary.Succeeded
		if summary.TotalDelegated > 0 {
			status.SuccessRate = float64(summary.Succeeded) / float64(summary.TotalDelegated)
		}
	}

	// Estimate next run from org settings (pm_schedule_hours).
	if status.LastRunAt != nil && !status.IsRunning {
		// Default schedule is 4 hours; compute approximate next run.
		nextRunIn := fmt.Sprintf("in ~4h")
		status.NextRunIn = &nextRunIn
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.PMStatus]{Data: status})
}
