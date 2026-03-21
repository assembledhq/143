package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// credentialStore is the interface the handler depends on.
type credentialStore interface {
	Upsert(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error
	ListSummaries(ctx context.Context, orgID uuid.UUID) ([]models.CredentialSummary, error)
	Disable(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error
	UpdateStatus(ctx context.Context, orgID uuid.UUID, provider models.ProviderName, status string) error
}

// CredentialHandler serves the /api/v1/settings/credentials endpoints.
type CredentialHandler struct {
	store credentialStore
	audit *db.AuditEmitter
}

// SetAuditEmitter injects the audit emitter for logging credential events.
func (h *CredentialHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
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

	emitUserAudit(h.audit, r, models.AuditActionCredentialUpdated, models.AuditResourceCredential, &providerStr, nil)
	summary := cfg.MaskedSummary()
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

	emitUserAudit(h.audit, r, models.AuditActionCredentialDeleted, models.AuditResourceCredential, &providerStr, nil)
	w.WriteHeader(http.StatusNoContent)
}
