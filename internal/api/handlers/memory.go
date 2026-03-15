package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type MemoryHandler struct {
	memoryStore  *db.MemoryStore
	commentStore *db.ReviewCommentStore
}

func NewMemoryHandler(memoryStore *db.MemoryStore, commentStore *db.ReviewCommentStore) *MemoryHandler {
	return &MemoryHandler{
		memoryStore:  memoryStore,
		commentStore: commentStore,
	}
}

// ListByRepo returns memories for a specific repo.
func (h *MemoryHandler) ListByRepo(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repo := chi.URLParam(r, "*")
	if repo == "" {
		writeError(w, http.StatusBadRequest, "MISSING_REPO", "repo path is required")
		return
	}

	limit := queryInt(r, "limit", 50)
	filters := db.MemoryFilters{
		Status: r.URL.Query().Get("status"),
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
	}

	memories, err := h.memoryStore.ListByRepo(r.Context(), orgID, repo, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list memories")
		return
	}
	if memories == nil {
		memories = []models.Memory{}
	}

	var nextCursor string
	if len(memories) > 0 && len(memories) == limit {
		nextCursor = memories[len(memories)-1].ID.String()
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.Memory]{
		Data: memories,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

// UpdateStatus updates a memory's status (active, dismissed).
func (h *MemoryHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	memoryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid memory ID")
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

	memory, err := h.memoryStore.UpdateMemoryAndGet(r.Context(), orgID, memoryID, nil, &req.Status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update memory status")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.Memory]{Data: memory})
}

// UpdateRule updates a memory's rule text.
func (h *MemoryHandler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	memoryID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid memory ID")
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

	memory, err := h.memoryStore.UpdateMemoryAndGet(r.Context(), orgID, memoryID, &req.Rule, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update memory rule")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.Memory]{Data: memory})
}

// ListComments returns review comments, optionally filtered by PR.
func (h *MemoryHandler) ListComments(w http.ResponseWriter, r *http.Request) {
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

// Create creates a new manually-curated memory.
func (h *MemoryHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	var req struct {
		Repo         string   `json:"repo"`
		Rule         string   `json:"rule"`
		Category     string   `json:"category"`
		Scope        string   `json:"scope"`
		FilePatterns []string `json:"file_patterns,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Rule == "" {
		writeError(w, http.StatusBadRequest, "MISSING_RULE", "rule text is required")
		return
	}
	if req.Category == "" {
		req.Category = "general"
	}
	if req.Scope == "" {
		req.Scope = "repo"
	}
	if req.Scope != "repo" && req.Scope != "org" {
		writeError(w, http.StatusBadRequest, "INVALID_SCOPE", "scope must be 'repo' or 'org'")
		return
	}
	if req.Scope == "repo" && req.Repo == "" {
		writeError(w, http.StatusBadRequest, "MISSING_REPO", "repo is required for repo-scoped memories")
		return
	}

	// Validate file patterns are syntactically valid globs.
	for _, pattern := range req.FilePatterns {
		if pattern == "" {
			writeError(w, http.StatusBadRequest, "INVALID_PATTERN", "file patterns must not be empty")
			return
		}
		// Strip "**" segments before validation since filepath.Match doesn't
		// support them, but our matchPattern function does.
		testPattern := strings.ReplaceAll(pattern, "**/", "")
		testPattern = strings.ReplaceAll(testPattern, "/**", "")
		if testPattern == "**" {
			continue // pure recursive glob, always valid
		}
		if testPattern == "" {
			writeError(w, http.StatusBadRequest, "INVALID_PATTERN", fmt.Sprintf("invalid glob pattern %q: trailing separator after **", pattern))
			return
		}
		if _, err := filepath.Match(testPattern, ""); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_PATTERN", fmt.Sprintf("invalid glob pattern %q: %v", pattern, err))
			return
		}
	}

	memory := &models.Memory{
		OrgID:           orgID,
		Repo:            req.Repo,
		Rule:            req.Rule,
		Category:        req.Category,
		SourceCommentIDs: []uuid.UUID{},
		OccurrenceCount: 1,
		Status:          "active", // manual memories start active
		ManuallyCurated: true,
		Scope:           req.Scope,
		Source:          "manual",
		FilePatterns:    req.FilePatterns,
	}

	if err := h.memoryStore.Create(r.Context(), memory); err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "failed to create memory")
		return
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Memory]{Data: *memory})
}
