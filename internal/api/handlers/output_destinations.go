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

type OutputDestinationHandler struct {
	store *db.OutputDestinationStore
}

func NewOutputDestinationHandler(store *db.OutputDestinationStore) *OutputDestinationHandler {
	return &OutputDestinationHandler{store: store}
}

func (h *OutputDestinationHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project id")
		return
	}

	dests, err := h.store.ListByProject(r.Context(), orgID, projectID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list output destinations", err)
		return
	}
	if dests == nil {
		dests = []models.OutputDestination{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"data": dests})
}

func (h *OutputDestinationHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project id")
		return
	}

	var body struct {
		DestinationType string          `json:"destination_type"`
		Label           string          `json:"label"`
		Config          json.RawMessage `json:"config"`
		Enabled         *bool           `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	destType := models.OutputDestinationType(body.DestinationType)
	if err := destType.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_TYPE", err.Error())
		return
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	dest := &models.OutputDestination{
		ProjectID:       projectID,
		OrgID:           orgID,
		DestinationType: destType,
		Label:           body.Label,
		Config:          body.Config,
		Enabled:         enabled,
	}
	if err := h.store.Create(r.Context(), dest); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create output destination", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"data": dest})
}

func (h *OutputDestinationHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	destID, err := uuid.Parse(chi.URLParam(r, "destId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid destination id")
		return
	}

	var body struct {
		DestinationType string          `json:"destination_type"`
		Label           string          `json:"label"`
		Config          json.RawMessage `json:"config"`
		Enabled         *bool           `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	destType := models.OutputDestinationType(body.DestinationType)
	if err := destType.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_TYPE", err.Error())
		return
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	dest, err := h.store.Update(r.Context(), orgID, destID, destType, body.Label, body.Config, enabled)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update output destination", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"data": dest})
}

func (h *OutputDestinationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	destID, err := uuid.Parse(chi.URLParam(r, "destId"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid destination id")
		return
	}

	if err := h.store.Delete(r.Context(), orgID, destID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete output destination", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
