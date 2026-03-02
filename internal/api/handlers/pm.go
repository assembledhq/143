package handlers

import (
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type PMHandler struct {
	planStore *db.PMPlanStore
	jobStore  *db.JobStore
}

func NewPMHandler(planStore *db.PMPlanStore, jobStore *db.JobStore) *PMHandler {
	return &PMHandler{planStore: planStore, jobStore: jobStore}
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
