package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type SettingsHandler struct {
	orgStore *db.OrganizationStore
}

func NewSettingsHandler(orgStore *db.OrganizationStore) *SettingsHandler {
	return &SettingsHandler{orgStore: orgStore}
}

func (h *SettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	org, err := h.orgStore.GetByID(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "organization not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Organization]{Data: org})
}

func (h *SettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	var req struct {
		Name     *string          `json:"name"`
		Settings *json.RawMessage `json:"settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	org, err := h.orgStore.GetByID(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "organization not found")
		return
	}

	if req.Name != nil {
		org.Name = *req.Name
	}
	if req.Settings != nil {
		org.Settings = *req.Settings
	}

	if err := h.orgStore.Update(r.Context(), &org); err != nil {
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update organization")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Organization]{Data: org})
}
