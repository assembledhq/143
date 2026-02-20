package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type ReviewPatternHandler struct {
	patternStore *db.ReviewPatternStore
	commentStore *db.ReviewCommentStore
}

func NewReviewPatternHandler(patternStore *db.ReviewPatternStore, commentStore *db.ReviewCommentStore) *ReviewPatternHandler {
	return &ReviewPatternHandler{
		patternStore: patternStore,
		commentStore: commentStore,
	}
}

// ListByRepo returns review patterns for a specific repo.
func (h *ReviewPatternHandler) ListByRepo(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repo := chi.URLParam(r, "*")
	if repo == "" {
		writeError(w, http.StatusBadRequest, "MISSING_REPO", "repo path is required")
		return
	}

	limit := queryInt(r, "limit", 50)
	filters := db.ReviewPatternFilters{
		Status: r.URL.Query().Get("status"),
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
	}

	patterns, err := h.patternStore.ListByRepo(r.Context(), orgID, repo, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list review patterns")
		return
	}
	if patterns == nil {
		patterns = []models.ReviewPattern{}
	}

	var nextCursor string
	if len(patterns) > 0 && len(patterns) == limit {
		nextCursor = patterns[len(patterns)-1].ID.String()
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.ReviewPattern]{
		Data: patterns,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

// UpdateStatus updates a review pattern's status (active, dismissed).
func (h *ReviewPatternHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	patternID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid pattern ID")
		return
	}

	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Status != "active" && req.Status != "dismissed" {
		writeError(w, http.StatusBadRequest, "INVALID_STATUS", "status must be 'active' or 'dismissed'")
		return
	}

	pattern, err := h.patternStore.UpdatePatternAndGet(r.Context(), orgID, patternID, nil, &req.Status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update pattern status")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.ReviewPattern]{Data: pattern})
}

// UpdateRule updates a review pattern's rule text.
func (h *ReviewPatternHandler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	patternID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid pattern ID")
		return
	}

	var req struct {
		Rule string `json:"rule"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Rule == "" {
		writeError(w, http.StatusBadRequest, "MISSING_RULE", "rule text is required")
		return
	}

	pattern, err := h.patternStore.UpdatePatternAndGet(r.Context(), orgID, patternID, &req.Rule, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update pattern rule")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.ReviewPattern]{Data: pattern})
}

// ListComments returns review comments, optionally filtered by PR.
func (h *ReviewPatternHandler) ListComments(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	limit := queryInt(r, "limit", 50)
	filters := db.ReviewCommentFilters{
		FilterStatus: r.URL.Query().Get("filter_status"),
		Limit:        limit,
		Cursor:       r.URL.Query().Get("cursor"),
	}

	if prIDStr := r.URL.Query().Get("pull_request_id"); prIDStr != "" {
		prID, err := uuid.Parse(prIDStr)
		if err == nil {
			filters.PullRequestID = &prID
		}
	}

	comments, err := h.commentStore.ListByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list review comments")
		return
	}
	if comments == nil {
		comments = []models.ReviewComment{}
	}

	var nextCursor string
	if len(comments) > 0 && len(comments) == limit {
		nextCursor = comments[len(comments)-1].ID.String()
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.ReviewComment]{
		Data: comments,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}
