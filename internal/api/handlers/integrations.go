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

func (h *IntegrationHandler) ConnectLinear(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	activeIntegrations, err := h.integrationStore.ListByOrgAndProvider(r.Context(), orgID, string(models.IntegrationProviderLinear))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CONNECT_LINEAR_FAILED", "failed to connect linear integration")
		return
	}

	if len(activeIntegrations) > 0 {
		writeJSON(w, http.StatusOK, models.SingleResponse[models.Integration]{Data: activeIntegrations[0]})
		return
	}

	integration := &models.Integration{
		OrgID:    orgID,
		Provider: models.IntegrationProviderLinear,
		Status:   models.IntegrationStatusActive,
	}
	if err := h.integrationStore.Create(r.Context(), integration); err != nil {
		writeError(w, http.StatusInternalServerError, "CONNECT_LINEAR_FAILED", "failed to connect linear integration")
		return
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Integration]{Data: *integration})
}
