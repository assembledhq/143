package handlers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// UsageHandler exposes container usage data for billing dashboards.
type UsageHandler struct {
	usageStore *db.ContainerUsageStore
}

// NewUsageHandler creates a UsageHandler.
func NewUsageHandler(usageStore *db.ContainerUsageStore) *UsageHandler {
	return &UsageHandler{usageStore: usageStore}
}

// GetSummary returns aggregated container usage for the org over a time period.
//
//	GET /api/v1/orgs/{orgID}/usage?start=2026-04-01T00:00:00Z&end=2026-05-01T00:00:00Z
//
// Defaults to the current calendar month if start/end are omitted.
func (h *UsageHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)

	if s := r.URL.Query().Get("start"); s != "" {
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "start must be RFC3339 format")
			return
		}
		start = parsed
	}
	if e := r.URL.Query().Get("end"); e != "" {
		parsed, err := time.Parse(time.RFC3339, e)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "end must be RFC3339 format")
			return
		}
		end = parsed
	}

	summary, err := h.usageStore.GetUsageSummary(r.Context(), orgID, start, end)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "failed to fetch usage summary", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[*models.UsageSummary]{Data: summary})
}

// ListBySession returns all container usage events for a given session.
//
//	GET /api/v1/orgs/{orgID}/sessions/{sessionID}/usage
func (h *UsageHandler) ListBySession(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PARAM", "invalid session ID")
		return
	}

	events, err := h.usageStore.ListBySession(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "failed to fetch session usage", err)
		return
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.ContainerUsageEvent]{
		Data: events,
	})
}
