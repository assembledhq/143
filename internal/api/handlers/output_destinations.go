package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/output"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// maxConfigFieldLen caps string fields in destination configs to prevent abuse.
const maxConfigFieldLen = 2048

// maxDestinationsPerProject prevents unbounded destination creation.
const maxDestinationsPerProject = 20

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
	for i := range dests {
		dests[i].RedactSecrets()
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
	if err := h.store.Create(r.Context(), dest, maxDestinationsPerProject); err != nil {
		if errors.Is(err, db.ErrDestinationLimitReached) {
			writeError(w, r, http.StatusBadRequest, "LIMIT_REACHED", fmt.Sprintf("maximum of %d output destinations per project", maxDestinationsPerProject))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create output destination", err)
		return
	}
	dest.RedactSecrets()
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

	dest, err := h.store.Update(r.Context(), orgID, projectID, destID, destType, body.Label, body.Config, enabled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "output destination not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update output destination", err)
		return
	}
	dest.RedactSecrets()
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

	if err := h.store.Delete(r.Context(), orgID, projectID, destID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "output destination not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete output destination", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateDestinationConfig verifies the config JSON matches the expected schema
// for the given destination type. Returns an error for missing required fields.
func validateDestinationConfig(destType models.OutputDestinationType, raw json.RawMessage) error {
	// Normalize empty/null config to empty object for consistent unmarshaling.
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
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
		if len(cfg.ChannelID) > maxConfigFieldLen {
			return errFieldTooLong("channel_id", maxConfigFieldLen)
		}
	case models.OutputDestEmail:
		var cfg models.EmailOutputConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		if len(cfg.Recipients) == 0 {
			return errMissingField("recipients")
		}
		for _, addr := range cfg.Recipients {
			if _, err := mail.ParseAddress(addr); err != nil {
				return fmt.Errorf("invalid email address %q: %w", addr, err)
			}
		}
		if len(cfg.Subject) > maxConfigFieldLen {
			return errFieldTooLong("subject", maxConfigFieldLen)
		}
	case models.OutputDestNotion:
		var cfg models.NotionOutputConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		if cfg.PageID == "" {
			return errMissingField("page_id")
		}
		if len(cfg.PageID) > maxConfigFieldLen {
			return errFieldTooLong("page_id", maxConfigFieldLen)
		}
		if !models.NotionIDPattern.MatchString(cfg.PageID) {
			return fmt.Errorf("page_id must be a valid Notion UUID")
		}
	case models.OutputDestWebhook:
		var cfg models.WebhookOutputConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
		if cfg.URL == "" {
			return errMissingField("url")
		}
		if len(cfg.URL) > maxConfigFieldLen {
			return errFieldTooLong("url", maxConfigFieldLen)
		}
		if err := output.ValidateWebhookURL(cfg.URL); err != nil {
			return fmt.Errorf("invalid webhook URL: %w", err)
		}
		if cfg.Method != "" && cfg.Method != http.MethodPost && cfg.Method != http.MethodPut && cfg.Method != http.MethodPatch {
			return fmt.Errorf("webhook method must be POST, PUT, or PATCH")
		}
	}
	return nil
}

func errMissingField(field string) error {
	return &configError{Field: field, Reason: "missing required config field: " + field}
}

func errFieldTooLong(field string, max int) error {
	return &configError{Field: field, Reason: fmt.Sprintf("config field %q exceeds maximum length of %d", field, max)}
}

type configError struct {
	Field  string
	Reason string
}

func (e *configError) Error() string {
	return e.Reason
}
