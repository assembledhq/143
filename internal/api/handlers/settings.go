package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
)

// OrgSettingsInvalidator drops cached org settings so that a write here is
// observed by the orchestrator's Amp/Pi config lookup immediately, rather
// than waiting for the cache TTL to expire. Declared locally (not imported
// from services/agent) so this handler doesn't pull in the agent package.
type OrgSettingsInvalidator interface {
	InvalidateOrg(orgID uuid.UUID)
}

type SettingsHandler struct {
	orgStore    *db.OrganizationStore
	llmDefaults map[string]string // provider name → masked key (from server env)
	audit       *db.AuditEmitter
	logger      zerolog.Logger
	invalidator OrgSettingsInvalidator
}

// SetAuditEmitter injects the audit emitter for logging settings events.
func (h *SettingsHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// SetLogger wires a logger used by marshalAuditDetails so a failure to
// JSON-encode the settings diff surfaces in logs instead of silently
// dropping the audit payload.
func (h *SettingsHandler) SetLogger(logger zerolog.Logger) {
	h.logger = logger
}

// SetOrgSettingsInvalidator injects the cache invalidator. When set, the
// Update handler will call InvalidateOrg after a successful write so the
// orchestrator's Amp/Pi env lookup picks up the new config on the next
// session start without waiting for the cache TTL.
func (h *SettingsHandler) SetOrgSettingsInvalidator(invalidator OrgSettingsInvalidator) {
	h.invalidator = invalidator
}

func NewSettingsHandler(orgStore *db.OrganizationStore, llmDefaults map[string]string) *SettingsHandler {
	return &SettingsHandler{
		orgStore:    orgStore,
		llmDefaults: llmDefaults,
		logger:      zerolog.Nop(),
	}
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

// GetLLMDefaults returns which LLM providers have platform-level API keys
// configured, with keys masked. This lets the frontend show whether a platform
// fallback is available when the org hasn't configured their own key.
func (h *SettingsHandler) GetLLMDefaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"data": h.llmDefaults,
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

	// Snapshot the pre-update values so the audit entry can record a precise
	// before/after diff. The mutations below replace these in place, so the
	// capture has to happen up front.
	beforeName := org.Name
	beforeSettings := append(json.RawMessage(nil), org.Settings...)

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

	// Drop any cached agent_config for this org so the next Amp/Pi session
	// start observes the write. Skipping invalidation would leave stale env
	// overrides in place until the cache TTL expires.
	if h.invalidator != nil {
		h.invalidator.InvalidateOrg(orgID)
	}

	// Build the audit diff from what actually changed. Skip the emit entirely
	// when nothing changed so a no-op PATCH (client re-saving the current
	// values) doesn't pollute the timeline with empty "updated settings" rows.
	changes := map[string]any{}
	if req.Name != nil && org.Name != beforeName {
		changes["name"] = map[string]any{"before": beforeName, "after": org.Name}
	}
	if req.Settings != nil {
		for k, v := range settingsAuditDiff(beforeSettings, org.Settings) {
			changes[k] = v
		}
	}
	if len(changes) > 0 {
		orgIDStr := orgID.String()
		emitUserAudit(h.audit, r, models.AuditActionSettingsUpdated, models.AuditResourceSettings, &orgIDStr,
			marshalAuditDetails(h.logger, map[string]any{"changes": changes}))
	}
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
