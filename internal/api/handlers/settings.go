package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type SettingsHandler struct {
	orgStore      *db.OrganizationStore
	agentDefaults map[string]map[string]string
	llmDefaults   map[string]string // provider name → masked key (from server env)
	platformModel string            // cheap model used for internal features (e.g. "gpt-5-nano")
	audit         *db.AuditEmitter
}

// SetAuditEmitter injects the audit emitter for logging settings events.
func (h *SettingsHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

func NewSettingsHandler(orgStore *db.OrganizationStore, agentDefaults map[string]map[string]string, llmDefaults map[string]string, platformModel string) *SettingsHandler {
	return &SettingsHandler{orgStore: orgStore, agentDefaults: agentDefaults, llmDefaults: llmDefaults, platformModel: platformModel}
}

func (h *SettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	org, err := h.orgStore.GetByID(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "organization not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Organization]{Data: org})
}

// GetAgentDefaults returns the server-level agent environment variable defaults
// with API keys masked. Allows the frontend to show what's configured.
func (h *SettingsHandler) GetAgentDefaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": h.agentDefaults})
}

// GetLLMDefaults returns which LLM providers have platform-level API keys
// configured, with keys masked. This lets the frontend show whether a platform
// fallback is available when the org hasn't configured their own key.
func (h *SettingsHandler) GetLLMDefaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"data":           h.llmDefaults,
		"platform_model": h.platformModel,
	})
}

// GetLLMModels returns the available LLM models grouped by provider.
// This is the source of truth — the frontend should use this instead of
// maintaining its own hardcoded list.
func (h *SettingsHandler) GetLLMModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": models.LLMModelsByProvider()})
}

func (h *SettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	var req struct {
		Name     *string          `json:"name"`
		Settings *json.RawMessage `json:"settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	if req.Settings != nil {
		var parsedSettings models.OrgSettings
		if err := json.Unmarshal(*req.Settings, &parsedSettings); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid settings JSON")
			return
		}
		if err := models.ValidateSettingsModels(parsedSettings); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SETTINGS", err.Error())
			return
		}
	}

	org, err := h.orgStore.GetByID(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "organization not found")
		return
	}

	if req.Name != nil {
		org.Name = *req.Name
	}
	if req.Settings != nil {
		merged, mergeErr := mergeSettingsJSON(org.Settings, *req.Settings)
		if mergeErr != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to merge settings")
			return
		}
		org.Settings = merged
	}

	if err := h.orgStore.Update(r.Context(), &org); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update organization", err)
		return
	}

	orgIDStr := orgID.String()
	emitUserAudit(h.audit, r, models.AuditActionSettingsUpdated, models.AuditResourceSettings, &orgIDStr, nil)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Organization]{Data: org})
}

func mergeSettingsJSON(existing, patch json.RawMessage) (json.RawMessage, error) {
	base := map[string]json.RawMessage{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &base); err != nil {
			return nil, err
		}
	}
	incoming := map[string]json.RawMessage{}
	if err := json.Unmarshal(patch, &incoming); err != nil {
		return nil, err
	}
	for k, v := range incoming {
		base[k] = v
	}
	return json.Marshal(base)
}
