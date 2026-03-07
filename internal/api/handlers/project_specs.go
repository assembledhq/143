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

type ProjectSpecHandler struct {
	specStore    *db.ProjectSpecStore
	projectStore *db.ProjectStore
}

func NewProjectSpecHandler(
	specStore *db.ProjectSpecStore,
	projectStore *db.ProjectStore,
) *ProjectSpecHandler {
	return &ProjectSpecHandler{
		specStore:    specStore,
		projectStore: projectStore,
	}
}

func (h *ProjectSpecHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}

	specs, err := h.specStore.ListByProject(r.Context(), orgID, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list specs")
		return
	}
	if specs == nil {
		specs = []models.ProjectSpec{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.ProjectSpec]{
		Data: specs,
		Meta: models.PaginationMeta{},
	})
}

func (h *ProjectSpecHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid project ID")
		return
	}

	if _, err := h.projectStore.GetByID(r.Context(), orgID, projectID); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "project not found")
		return
	}

	var req struct {
		Title    string  `json:"title"`
		Content  *string `json:"content"`
		SpecType *string `json:"spec_type"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD", "title is required")
		return
	}

	specType := "prd"
	if req.SpecType != nil {
		specType = *req.SpecType
	}

	content := ""
	if req.Content != nil {
		content = *req.Content
	}

	spec := models.ProjectSpec{
		ProjectID: projectID,
		OrgID:     orgID,
		Title:     req.Title,
		Content:   content,
		SpecType:  specType,
		CreatedBy: &user.ID,
	}

	if err := h.specStore.Create(r.Context(), &spec); err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "failed to create spec")
		return
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.ProjectSpec]{Data: spec})
}

func (h *ProjectSpecHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	specID, err := uuid.Parse(chi.URLParam(r, "specId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid spec ID")
		return
	}

	spec, err := h.specStore.GetByID(r.Context(), orgID, specID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "spec not found")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.ProjectSpec]{Data: spec})
}

func (h *ProjectSpecHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	specID, err := uuid.Parse(chi.URLParam(r, "specId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid spec ID")
		return
	}

	spec, err := h.specStore.GetByID(r.Context(), orgID, specID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "spec not found")
		return
	}

	var req struct {
		Title    *string `json:"title"`
		Content  *string `json:"content"`
		SpecType *string `json:"spec_type"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Title != nil {
		spec.Title = *req.Title
	}
	if req.Content != nil {
		spec.Content = *req.Content
	}
	if req.SpecType != nil {
		spec.SpecType = *req.SpecType
	}

	if err := h.specStore.Update(r.Context(), &spec); err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update spec")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.ProjectSpec]{Data: spec})
}

func (h *ProjectSpecHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	specID, err := uuid.Parse(chi.URLParam(r, "specId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid spec ID")
		return
	}

	if err := h.specStore.Delete(r.Context(), orgID, specID); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete spec")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
