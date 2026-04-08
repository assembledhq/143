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
	store        *db.OutputDestinationStore
	projectStore *db.ProjectStore
}

func NewOutputDestinationHandler(store *db.OutputDestinationStore, projectStore *db.ProjectStore) *OutputDestinationHandler {
	return &OutputDestinationHandler{store: store, projectStore: projectStore}
}

// verifyProjectOwnership checks that the project exists and belongs to the authenticated org.
func (h *OutputDestinationHandler) verifyProjectOwnership(w http.ResponseWriter, r *http.Request, orgID, projectID uuid.UUID) bool {
	if h.projectStore != nil {
		if _, err := h.projectStore.GetByID(r.Context(), orgID, projectID); err != nil {
			writeError(w, r, http.StatusNotFound, "PROJECT_NOT_FOUND", "project not found")
			return false
		}
	}
	return true
}

func (h *OutputDestinationHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project id")
		return
	}

	if !h.verifyProjectOwnership(w, r, orgID, projectID) {
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

	if !h.verifyProjectOwnership(w, r, orgID, projectID) {
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

	// Validate that config matches the destination type.
	if err := validateDestinationConfig(destType, body.Config); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CONFIG", err.Error())
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
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project id")
		return
	}

	if !h.verifyProjectOwnership(w, r, orgID, projectID) {
		return
	}

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

	if err := validateDestinationConfig(destType, body.Config); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CONFIG", err.Error())
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
	projectID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid project id")
		return
	}

	if !h.verifyProjectOwnership(w, r, orgID, projectID) {
		return
	}

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

// validateDestinationConfig verifies the config JSON matches the expected schema
// for the given destination type. Returns an error for missing required fields.
func validateDestinationConfig(destType models.OutputDestinationType, raw json.RawMessage) error {
	if len(raw) == 0 {
		return json.Unmarshal([]byte("{}"), new(json.RawMessage))
	}

	switch destType {
	case models.OutputDestSlack:
		var cfg models.SlackOutputConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		if cfg.ChannelID == "" {
			return errMissingField("channel_id")
		}
	case models.OutputDestEmail:
		var cfg models.EmailOutputConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		if len(cfg.Recipients) == 0 {
			return errMissingField("recipients")
		}
	case models.OutputDestNotion:
		var cfg models.NotionOutputConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		if cfg.PageID == "" {
			return errMissingField("page_id")
		}
	case models.OutputDestWebhook:
		var cfg models.WebhookOutputConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		if cfg.URL == "" {
			return errMissingField("url")
		}
	}
	return nil
}

func errMissingField(field string) error {
	return &configError{Field: field}
}

type configError struct {
	Field string
}

func (e *configError) Error() string {
	return "missing required config field: " + e.Field
}
