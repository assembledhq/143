package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// credentialStore is the interface the handler depends on.
type credentialStore interface {
	Upsert(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error
	ListSummaries(ctx context.Context, orgID uuid.UUID) ([]models.CredentialSummary, error)
	Disable(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error
	UpdateStatus(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, status models.CredentialStatus) error
}

// orgSettingsMutator is the slice of OrganizationStore needed to self-heal an
// org's persisted llm_model after a credential is removed. Defined narrowly
// so tests can stub it without faking the whole store.
type orgSettingsMutator interface {
	GetByID(ctx context.Context, orgID uuid.UUID) (models.Organization, error)
	Update(ctx context.Context, org *models.Organization) error
}

// CredentialHandler serves the /api/v1/settings/credentials endpoints.
type CredentialHandler struct {
	store       credentialStore
	audit       *db.AuditEmitter
	orgStore    orgSettingsMutator
	llmDefaults map[string]string
}

// SetAuditEmitter injects the audit emitter for logging credential events.
func (h *CredentialHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// SetSelfHeal wires the dependencies Delete uses to reset the org's
// persisted llm_model when removing a credential would otherwise route it
// through 143's capped platform-default key. When unset, Delete still
// works — it just skips the self-heal step.
func (h *CredentialHandler) SetSelfHeal(orgStore orgSettingsMutator, llmDefaults map[string]string) {
	h.orgStore = orgStore
	h.llmDefaults = llmDefaults
}

// NewCredentialHandler creates a new credential handler.
func NewCredentialHandler(store credentialStore) *CredentialHandler {
	return &CredentialHandler{store: store}
}

// List returns masked credential summaries for all known providers.
func (h *CredentialHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	summaries, err := h.store.ListSummaries(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list credentials", err)
		return
	}
	if summaries == nil {
		summaries = []models.CredentialSummary{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.CredentialSummary]{Data: summaries})
}

// Update upserts a credential for the given provider.
func (h *CredentialHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	providerStr := chi.URLParam(r, "provider")
	provider := models.ProviderName(providerStr)

	if !provider.Valid() {
		writeError(w, r, http.StatusBadRequest, "INVALID_PROVIDER", "unknown provider: "+providerStr)
		return
	}

	// Read the raw JSON body, then parse into the correct typed config.
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	cfg, err := models.ParseProviderConfig(provider, raw)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CONFIG", err.Error())
		return
	}

	if err := cfg.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CONFIG", err.Error())
		return
	}

	if err := h.store.Upsert(r.Context(), orgID, cfg); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPSERT_FAILED", "failed to save credential", err)
		return
	}

	summary := cfg.MaskedSummary()
	emitUserAudit(h.audit, r, models.AuditActionCredentialUpdated, models.AuditResourceCredential, &providerStr,
		marshalAuditDetails(*zerolog.Ctx(r.Context()), credentialAuditDetails(provider, &summary)))
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CredentialSummary]{Data: summary})
}

// Delete soft-deletes (disables) a credential for the given provider.
func (h *CredentialHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	providerStr := chi.URLParam(r, "provider")
	provider := models.ProviderName(providerStr)

	if !provider.Valid() {
		writeError(w, r, http.StatusBadRequest, "INVALID_PROVIDER", "unknown provider: "+providerStr)
		return
	}

	if err := h.store.Disable(r.Context(), orgID, provider); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to disable credential", err)
		return
	}

	// Self-heal: if the org's persisted llm_model is no longer accessible
	// without this key (i.e., would now route through 143's capped platform
	// default), reset it so runtime calls don't bill the platform for an
	// expensive model the org silently lost access to.
	h.maybeResetLLMModel(r.Context(), orgID)

	emitUserAudit(h.audit, r, models.AuditActionCredentialDeleted, models.AuditResourceCredential, &providerStr,
		marshalAuditDetails(*zerolog.Ctx(r.Context()), credentialAuditDetails(provider, nil)))
	w.WriteHeader(http.StatusNoContent)
}

// maybeResetLLMModel clears the org's llm_model setting when, after the
// just-completed credential change, no key path serves it without bumping
// into the platform-default cost cap. A best-effort step: failures are
// logged but never fail the credential delete itself.
func (h *CredentialHandler) maybeResetLLMModel(ctx context.Context, orgID uuid.UUID) {
	if h.orgStore == nil {
		return
	}
	logger := zerolog.Ctx(ctx)

	org, err := h.orgStore.GetByID(ctx, orgID)
	if err != nil {
		logger.Warn().Err(err).Str("org_id", orgID.String()).
			Msg("self-heal: failed to load org settings after credential delete")
		return
	}

	var settings models.OrgSettings
	if len(org.Settings) > 0 {
		if err := json.Unmarshal(org.Settings, &settings); err != nil {
			logger.Warn().Err(err).Str("org_id", orgID.String()).
				Msg("self-heal: failed to parse org settings after credential delete")
			return
		}
	}
	if settings.LLMModel == "" {
		return
	}

	platformAvailable := map[string]bool{}
	for provider := range h.llmDefaults {
		platformAvailable[provider] = true
	}

	if err := models.ValidateLLMModelAccess(settings.LLMModel, nil, platformAvailable); err == nil {
		return
	}

	prev := settings.LLMModel
	patch := json.RawMessage(`{"llm_model":""}`)
	merged, mergeErr := mergeSettingsJSON(org.Settings, patch)
	if mergeErr != nil {
		logger.Warn().Err(mergeErr).Str("org_id", orgID.String()).
			Msg("self-heal: failed to clear capped llm_model")
		return
	}
	org.Settings = merged
	if err := h.orgStore.Update(ctx, &org); err != nil {
		logger.Warn().Err(err).Str("org_id", orgID.String()).
			Msg("self-heal: failed to persist cleared llm_model")
		return
	}
	logger.Info().
		Str("org_id", orgID.String()).
		Str("previous_model", prev).
		Msg("self-heal: cleared llm_model that would now route through capped platform default")
}
