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

type RepositoryHandler struct {
	repoStore *db.RepositoryStore
}

func NewRepositoryHandler(repoStore *db.RepositoryStore) *RepositoryHandler {
	return &RepositoryHandler{repoStore: repoStore}
}

func (h *RepositoryHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repos, err := h.repoStore.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list repositories")
		return
	}
	if repos == nil {
		repos = []models.Repository{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.Repository]{Data: repos})
}

func (h *RepositoryHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repoID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid repository ID")
		return
	}

	repo, err := h.repoStore.GetByID(r.Context(), orgID, repoID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "repository not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Repository]{Data: repo})
}

func (h *RepositoryHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repoID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid repository ID")
		return
	}

	var req struct {
		Status   *string          `json:"status"`
		Settings *json.RawMessage `json:"settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	repo, err := h.repoStore.GetByID(r.Context(), orgID, repoID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "repository not found")
		return
	}

	if req.Status != nil {
		repo.Status = *req.Status
	}
	if req.Settings != nil {
		// Validate PM settings if present.
		repoSettings, parseErr := models.ParseRepoSettings(*req.Settings)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "INVALID_SETTINGS", "invalid settings JSON")
			return
		}
		if repoSettings.PM != nil {
			if err := models.ValidateRepoPMSettings(*repoSettings.PM); err != nil {
				writeError(w, http.StatusBadRequest, "INVALID_SETTINGS", err.Error())
				return
			}
		}
		repo.Settings = *req.Settings
	}

	if err := h.repoStore.Update(r.Context(), &repo); err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update repository")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Repository]{Data: repo})
}

func (h *RepositoryHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repoID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid repository ID")
		return
	}

	if err := h.repoStore.Delete(r.Context(), orgID, repoID); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete repository")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
