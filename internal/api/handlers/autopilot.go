package handlers

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type AutopilotHandler struct {
	queueStore *db.AutopilotQueueStore
}

func NewAutopilotHandler(queueStore *db.AutopilotQueueStore) *AutopilotHandler {
	return &AutopilotHandler{queueStore: queueStore}
}

func (h *AutopilotHandler) Queue(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	limit := clampListLimit(queryInt(r, "limit", 50), 50, 100)

	var repoID *uuid.UUID
	if raw := r.URL.Query().Get("repo_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REPO_ID", "invalid repo_id")
			return
		}
		repoID = &parsed
	}

	filters := db.AutopilotQueueFilters{
		Cursor:     r.URL.Query().Get("cursor"),
		Limit:      limit,
		Source:     models.IssueSource(r.URL.Query().Get("source")),
		RunState:   models.AutopilotRunState(r.URL.Query().Get("run_state")),
		Automation: r.URL.Query().Get("automation"),
		RepoID:     repoID,
		Query:      r.URL.Query().Get("q"),
		Sort:       r.URL.Query().Get("sort"),
	}
	if filters.Source != "" {
		if err := filters.Source.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SOURCE", "invalid source filter")
			return
		}
	}
	if filters.RunState != "" {
		if err := filters.RunState.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_RUN_STATE", "invalid run_state filter")
			return
		}
	}

	page, err := h.queueStore.ListQueue(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list autopilot queue", err)
		return
	}
	if page.Rows == nil {
		page.Rows = []models.AutopilotQueueRow{}
	}

	writeJSON(w, http.StatusOK, models.AutopilotQueueResponse{
		Data: page.Rows,
		Meta: models.AutopilotQueueMeta{
			NextCursor: page.NextCursor,
			Summary:    page.Summary,
		},
	})
}
