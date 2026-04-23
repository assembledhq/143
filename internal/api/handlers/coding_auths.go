package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

type codingAuthStore interface {
	ListCodingAuths(ctx context.Context, orgID uuid.UUID) ([]models.CodingAuth, error)
	ListByProvider(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error)
	ReorderCodingAuths(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) error
	CreateCodingAuth(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, input models.CreateCodingAuthInput) (*models.CodingAuth, error)
	UpdateCodingAuth(ctx context.Context, orgID uuid.UUID, id uuid.UUID, input models.UpdateCodingAuthInput) (*models.CodingAuth, error)
	DisableCodingAuth(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error
}

type codingAuthOrgStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error)
	Update(ctx context.Context, org *models.Organization) error
}

type CodingAuthHandler struct {
	store       codingAuthStore
	orgStore    codingAuthOrgStore
	invalidator OrgSettingsInvalidator
}

func NewCodingAuthHandler(store codingAuthStore, orgStore codingAuthOrgStore) *CodingAuthHandler {
	return &CodingAuthHandler{store: store, orgStore: orgStore}
}

func (h *CodingAuthHandler) SetOrgSettingsInvalidator(invalidator OrgSettingsInvalidator) {
	h.invalidator = invalidator
}

func (h *CodingAuthHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	rows, err := h.store.ListCodingAuths(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list coding auths", err)
		return
	}
	if rows == nil {
		rows = []models.CodingAuth{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.CodingAuth]{Data: rows})
}

func (h *CodingAuthHandler) Reorder(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if len(body.IDs) == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_IDS", "ids are required")
		return
	}

	ids := make([]uuid.UUID, 0, len(body.IDs))
	for _, raw := range body.IDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ID", "ids must be valid UUIDs")
			return
		}
		ids = append(ids, id)
	}

	if err := h.store.ReorderCodingAuths(r.Context(), orgID, ids); err != nil {
		writeError(w, r, http.StatusInternalServerError, "REORDER_FAILED", "failed to reorder coding auths", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CodingAuthHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}

	var input models.CreateCodingAuthInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if err := input.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}

	var org *models.Organization
	if len(input.AgentDefaults) > 0 {
		if h.orgStore == nil {
			writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "agent default writes are unavailable")
			return
		}
		currentOrg, err := h.orgStore.GetByID(r.Context(), orgID)
		if err != nil {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "organization not found")
			return
		}
		mergedSettings, err := mergeCodingAuthAgentDefaults(currentOrg.Settings, input)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_INPUT", err.Error())
			return
		}
		currentOrg.Settings = mergedSettings
		org = &currentOrg
	}

	row, err := h.store.CreateCodingAuth(r.Context(), orgID, &user.ID, input)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create coding auth", err)
		return
	}

	if org != nil {
		if err := h.orgStore.Update(r.Context(), org); err != nil {
			if rollbackErr := h.store.DisableCodingAuth(r.Context(), orgID, row.ID); rollbackErr != nil {
				err = fmt.Errorf("persist agent defaults: %w; rollback coding auth: %v", err, rollbackErr)
			}
			writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to save coding auth defaults", err)
			return
		}
		if h.invalidator != nil {
			h.invalidator.InvalidateOrg(orgID)
		}
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodingAuth]{Data: *row})
}

func (h *CodingAuthHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, ok := parseCodingAuthID(w, r)
	if !ok {
		return
	}

	var input models.UpdateCodingAuthInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	row, err := h.store.UpdateCodingAuth(r.Context(), orgID, id, input)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update coding auth", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodingAuth]{Data: *row})
}

func (h *CodingAuthHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, ok := parseCodingAuthID(w, r)
	if !ok {
		return
	}

	if err := h.store.DisableCodingAuth(r.Context(), orgID, id); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to disable coding auth", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CodingAuthHandler) LegacyStatus(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	status, err := h.legacyStatus(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LEGACY_STATUS_FAILED", "failed to inspect legacy coding auth settings", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.LegacyCodingAuthStatus]{Data: status})
}

func (h *CodingAuthHandler) MigrateLegacy(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}
	if h.orgStore == nil {
		writeError(w, r, http.StatusInternalServerError, "MIGRATION_UNAVAILABLE", "legacy coding auth migration is unavailable")
		return
	}

	org, err := h.orgStore.GetByID(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "organization not found")
		return
	}

	legacy, err := inspectLegacyCodingAuthSettings(org.Settings)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "MIGRATION_FAILED", "failed to parse legacy coding auth settings", err)
		return
	}

	ampRows, err := h.store.ListByProvider(r.Context(), orgID, models.ProviderAmp)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "MIGRATION_FAILED", "failed to inspect Amp auth rows", err)
		return
	}
	piRows, err := h.store.ListByProvider(r.Context(), orgID, models.ProviderPi)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "MIGRATION_FAILED", "failed to inspect Pi auth rows", err)
		return
	}

	result := models.LegacyCodingAuthMigrationResult{}
	if legacy.AmpKey != "" && len(ampRows) == 0 {
		if _, err := h.store.CreateCodingAuth(r.Context(), orgID, &user.ID, models.CreateCodingAuthInput{
			Agent:    models.AgentTypeAmp,
			AuthType: models.CodingAuthTypeAPIKey,
			Label:    "Amp API key",
			APIKey:   legacy.AmpKey,
		}); err != nil {
			writeError(w, r, http.StatusInternalServerError, "MIGRATION_FAILED", "failed to migrate legacy Amp auth", err)
			return
		}
		result.MigratedAmp = true
	}
	if legacy.PiKey != "" && len(piRows) == 0 {
		if _, err := h.store.CreateCodingAuth(r.Context(), orgID, &user.ID, models.CreateCodingAuthInput{
			Agent:    models.AgentTypePi,
			AuthType: models.CodingAuthTypeAPIKey,
			Label:    "Pi API key",
			APIKey:   legacy.PiKey,
		}); err != nil {
			writeError(w, r, http.StatusInternalServerError, "MIGRATION_FAILED", "failed to migrate legacy Pi auth", err)
			return
		}
		result.MigratedPi = true
	}

	cleanedSettings, removedSecrets, err := scrubLegacyCodingAuthSecrets(org.Settings)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "MIGRATION_FAILED", "failed to clean legacy coding auth settings", err)
		return
	}
	if removedSecrets {
		org.Settings = cleanedSettings
		if err := h.orgStore.Update(r.Context(), &org); err != nil {
			writeError(w, r, http.StatusInternalServerError, "MIGRATION_FAILED", "failed to persist cleaned coding auth settings", err)
			return
		}
		if h.invalidator != nil {
			h.invalidator.InvalidateOrg(orgID)
		}
		result.RemovedLegacySecrets = true
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.LegacyCodingAuthMigrationResult]{Data: result})
}

func parseCodingAuthID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	raw := chi.URLParam(r, "id")
	if raw == "" {
		raw = r.PathValue("id")
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}

type legacyCodingAuthSettings struct {
	AmpKey        string
	AmpMaskedKey  string
	PiKey         string
	PiMaskedKey   string
	HasPiDefaults bool
}

func (h *CodingAuthHandler) legacyStatus(ctx context.Context, orgID uuid.UUID) (models.LegacyCodingAuthStatus, error) {
	var status models.LegacyCodingAuthStatus
	if h.orgStore == nil {
		return status, nil
	}

	org, err := h.orgStore.GetByID(ctx, orgID)
	if err != nil {
		return status, err
	}

	legacy, err := inspectLegacyCodingAuthSettings(org.Settings)
	if err != nil {
		return status, err
	}

	ampRows, err := h.store.ListByProvider(ctx, orgID, models.ProviderAmp)
	if err != nil {
		return status, err
	}
	piRows, err := h.store.ListByProvider(ctx, orgID, models.ProviderPi)
	if err != nil {
		return status, err
	}

	status.HasLegacyAmpSecret = legacy.AmpKey != ""
	status.AmpMaskedKey = legacy.AmpMaskedKey
	status.HasAmpCredential = len(ampRows) > 0
	status.HasLegacyPiSecret = legacy.PiKey != ""
	status.PiMaskedKey = legacy.PiMaskedKey
	status.HasLegacyPiDefaults = legacy.HasPiDefaults
	status.HasPiCredential = len(piRows) > 0
	status.PiRequiresManualAuth = legacy.HasPiDefaults && legacy.PiKey == "" && len(piRows) == 0

	return status, nil
}

func inspectLegacyCodingAuthSettings(raw json.RawMessage) (legacyCodingAuthSettings, error) {
	var legacy legacyCodingAuthSettings

	settings, err := decodeJSONObject(raw, true)
	if err != nil {
		return legacy, err
	}
	agentConfig, ok := settings["agent_config"].(map[string]any)
	if !ok {
		return legacy, nil
	}

	if ampConfig, ok := agentConfig["amp"].(map[string]any); ok {
		legacy.AmpKey = nestedStringValue(ampConfig, "AMP_API_KEY")
		if legacy.AmpKey != "" {
			legacy.AmpMaskedKey = models.MaskKey(legacy.AmpKey)
		}
	}
	if piConfig, ok := agentConfig["pi"].(map[string]any); ok {
		legacy.PiKey = nestedStringValue(piConfig, "PI_API_KEY")
		if legacy.PiKey != "" {
			legacy.PiMaskedKey = models.MaskKey(legacy.PiKey)
		}
		legacy.HasPiDefaults = nestedStringValue(piConfig, "PI_MODEL") != "" || nestedStringValue(piConfig, "PI_MODEL_CUSTOM") != ""
	}

	return legacy, nil
}

func scrubLegacyCodingAuthSecrets(raw json.RawMessage) (json.RawMessage, bool, error) {
	settings, err := decodeJSONObject(raw, true)
	if err != nil {
		return nil, false, err
	}
	agentConfig, ok := settings["agent_config"].(map[string]any)
	if !ok {
		return raw, false, nil
	}

	removed := false
	if ampConfig, ok := agentConfig["amp"].(map[string]any); ok {
		if _, exists := ampConfig["AMP_API_KEY"]; exists {
			delete(ampConfig, "AMP_API_KEY")
			removed = true
		}
		if len(ampConfig) == 0 {
			delete(agentConfig, "amp")
		}
	}
	if piConfig, ok := agentConfig["pi"].(map[string]any); ok {
		if _, exists := piConfig["PI_API_KEY"]; exists {
			delete(piConfig, "PI_API_KEY")
			removed = true
		}
		if len(piConfig) == 0 {
			delete(agentConfig, "pi")
		}
	}
	if len(agentConfig) == 0 {
		delete(settings, "agent_config")
	}
	if !removed {
		return raw, false, nil
	}

	cleaned, err := json.Marshal(settings)
	if err != nil {
		return nil, false, err
	}
	return cleaned, true, nil
}

func nestedStringValue(values map[string]any, key string) string {
	raw, ok := values[key]
	if !ok {
		return ""
	}
	str, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(str)
}

func mergeCodingAuthAgentDefaults(existing json.RawMessage, input models.CreateCodingAuthInput) (json.RawMessage, error) {
	if len(input.AgentDefaults) == 0 {
		return existing, nil
	}

	patch, err := json.Marshal(map[string]any{
		"agent_config": map[string]any{
			string(input.Agent): input.AgentDefaults,
		},
	})
	if err != nil {
		return nil, err
	}

	merged, err := mergeSettingsJSON(existing, patch)
	if err != nil {
		return nil, err
	}

	var parsed models.OrgSettings
	if err := json.Unmarshal(merged, &parsed); err != nil {
		return nil, err
	}
	if err := models.ValidateSettingsModels(parsed); err != nil {
		return nil, err
	}

	return merged, nil
}
