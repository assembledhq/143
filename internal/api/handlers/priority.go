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

type PriorityHandler struct {
	priorityScores      *db.PriorityScoreStore
	complexityEstimates *db.ComplexityEstimateStore
	jobStore            *db.JobStore
}

func NewPriorityHandler(
	priorityScores *db.PriorityScoreStore,
	complexityEstimates *db.ComplexityEstimateStore,
	jobStore *db.JobStore,
) *PriorityHandler {
	return &PriorityHandler{
		priorityScores:      priorityScores,
		complexityEstimates: complexityEstimates,
		jobStore:            jobStore,
	}
}

// GetPriorityScore returns the priority score for a specific issue.
func (h *PriorityHandler) GetPriorityScore(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	issueID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid issue ID")
		return
	}

	score, err := h.priorityScores.GetByIssueID(r.Context(), orgID, issueID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "priority score not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PriorityScore]{Data: score})
}

// GetComplexity returns the complexity estimate for a specific issue.
func (h *PriorityHandler) GetComplexity(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	issueID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid issue ID")
		return
	}

	estimate, err := h.complexityEstimates.GetByIssueID(r.Context(), orgID, issueID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "complexity estimate not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.ComplexityEstimate]{Data: estimate})
}

// ListPriorityScores returns priority scores for the org, optionally filtered to eligible-only.
func (h *PriorityHandler) ListPriorityScores(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	eligibleOnly := r.URL.Query().Get("eligible_only") == "true"
	limit := queryInt(r, "limit", 50)

	scores, err := h.priorityScores.ListByOrg(r.Context(), orgID, eligibleOnly, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list priority scores")
		return
	}
	if scores == nil {
		scores = []models.PriorityScore{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.PriorityScore]{
		Data: scores,
		Meta: models.PaginationMeta{},
	})
}

// Reprioritize enqueues a prioritize job for the specified issue.
func (h *PriorityHandler) Reprioritize(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	issueID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid issue ID")
		return
	}

	dedupeKey := fmt.Sprintf("prioritize:%s", issueID.String())
	_, err = h.jobStore.Enqueue(r.Context(), orgID, "default", "prioritize", map[string]string{
		"issue_id": issueID.String(),
		"org_id":   orgID.String(),
	}, 3, &dedupeKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue prioritize job")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

