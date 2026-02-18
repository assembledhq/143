package handlers

import (
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type IntegrationHandler struct {
	integrationStore *db.IntegrationStore
}

func NewIntegrationHandler(integrationStore *db.IntegrationStore) *IntegrationHandler {
	return &IntegrationHandler{integrationStore: integrationStore}
}

func (h *IntegrationHandler) ListIntegrations(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	integrations, err := h.integrationStore.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list integrations")
		return
	}
	if integrations == nil {
		integrations = []models.Integration{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.Integration]{Data: integrations})
}
