package handlers

import (
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type IssueHandler struct {
	issueStore *db.IssueStore
}

func NewIssueHandler(issueStore *db.IssueStore) *IssueHandler {
	return &IssueHandler{issueStore: issueStore}
}

func (h *IssueHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	limit := queryInt(r, "limit", 50)
	filters := db.IssueFilters{
		Status:   models.IssueStatus(r.URL.Query().Get("status")),
		Source:   models.IssueSource(r.URL.Query().Get("source")),
		Severity: models.IssueSeverity(r.URL.Query().Get("severity")),
		Sort:     r.URL.Query().Get("sort"),
		Limit:    limit,
		Cursor:   r.URL.Query().Get("cursor"),
	}

	issues, err := h.issueStore.ListByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list issues", err)
		return
	}
	if issues == nil {
		issues = []models.Issue{}
	}

	var nextCursor string
	if len(issues) > 0 && len(issues) == filters.Limit {
		nextCursor = issues[len(issues)-1].ID.String()
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.Issue]{
		Data: issues,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

func (h *IssueHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	issueID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid issue ID")
		return
	}

	issue, err := h.issueStore.GetByID(r.Context(), orgID, issueID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "issue not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Issue]{Data: issue})
}
