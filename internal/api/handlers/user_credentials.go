package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

// userCredentialStore is the interface the handler depends on.
type userCredentialStore interface {
	Upsert(ctx context.Context, userID, orgID uuid.UUID, cfg models.ProviderConfig, isTeamDefault bool) error
	GetForUser(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error)
	GetTeamDefault(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error)
	ListByUser(ctx context.Context, orgID, userID uuid.UUID) ([]models.DecryptedUserCredential, error)
	ListTeamDefaults(ctx context.Context, orgID uuid.UUID) ([]models.DecryptedUserCredential, error)
	Disable(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error
	SetTeamDefault(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error
	RemoveTeamDefault(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error
}

// orgCredentialReader reads org-level credentials for the resolution preview.
type orgCredentialReader interface {
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
}

// userLookup reads users by ID for showing who set team defaults.
type userLookup interface {
	GetByID(ctx context.Context, orgID, userID uuid.UUID) (models.User, error)
}

// UserCredentialHandler serves the personal and team credential endpoints.
type UserCredentialHandler struct {
	store    userCredentialStore
	orgCreds orgCredentialReader
	users    userLookup
}

// NewUserCredentialHandler creates a new user credential handler.
func NewUserCredentialHandler(store userCredentialStore, orgCreds orgCredentialReader, users userLookup) *UserCredentialHandler {
	return &UserCredentialHandler{store: store, orgCreds: orgCreds, users: users}
}

// ListPersonal returns the current user's credentials.
func (h *UserCredentialHandler) ListPersonal(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}

	creds, err := h.store.ListByUser(r.Context(), orgID, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list personal credentials")
		return
	}

	summaries := make([]models.UserCredentialSummary, 0, len(models.CodingAgentProviders))
	configured := make(map[models.ProviderName]models.UserCredentialSummary)
	for _, cred := range creds {
		s := models.UserCredentialSummary{
			Provider:       cred.Provider,
			Configured:     true,
			IsTeamDefault:  cred.IsTeamDefault,
			MaskedKey:      cred.Config.MaskedSummary().MaskedKey,
			SetByUserID:    &cred.UserID,
			Status:         cred.Status,
			LastVerifiedAt: cred.LastVerifiedAt,
		}
		configured[cred.Provider] = s
	}

	for _, p := range models.CodingAgentProviders {
		if s, ok := configured[p]; ok {
			summaries = append(summaries, s)
		} else {
			summaries = append(summaries, models.UserCredentialSummary{
				Provider:   p,
				Configured: false,
			})
		}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.UserCredentialSummary]{Data: summaries})
}

// UpsertPersonal creates or updates a personal credential.
func (h *UserCredentialHandler) UpsertPersonal(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}

	providerStr := chi.URLParam(r, "provider")
	provider := models.ProviderName(providerStr)
	if !provider.IsCodingAgentProvider() {
		writeError(w, http.StatusBadRequest, "INVALID_PROVIDER", "unsupported coding agent provider: "+providerStr)
		return
	}

	var body struct {
		Config        json.RawMessage `json:"config"`
		IsTeamDefault bool            `json:"is_team_default"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	cfg, err := models.ParseProviderConfig(provider, body.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CONFIG", err.Error())
		return
	}
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CONFIG", err.Error())
		return
	}

	// Only admins can set team defaults.
	if body.IsTeamDefault && user.Role != "admin" {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "only admins can set team defaults")
		return
	}

	if err := h.store.Upsert(r.Context(), user.ID, orgID, cfg, body.IsTeamDefault); err != nil {
		writeError(w, http.StatusInternalServerError, "UPSERT_FAILED", "failed to save credential")
		return
	}

	summary := models.UserCredentialSummary{
		Provider:      provider,
		Configured:    true,
		IsTeamDefault: body.IsTeamDefault,
		MaskedKey:     cfg.MaskedSummary().MaskedKey,
		SetByUserID:   &user.ID,
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.UserCredentialSummary]{Data: summary})
}

// DeletePersonal disables a personal credential.
func (h *UserCredentialHandler) DeletePersonal(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}

	providerStr := chi.URLParam(r, "provider")
	provider := models.ProviderName(providerStr)
	if !provider.IsCodingAgentProvider() {
		writeError(w, http.StatusBadRequest, "INVALID_PROVIDER", "unsupported coding agent provider: "+providerStr)
		return
	}

	if err := h.store.Disable(r.Context(), orgID, user.ID, provider); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "failed to disable credential")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListTeamDefaults returns the team default credentials for the org.
func (h *UserCredentialHandler) ListTeamDefaults(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	defaults, err := h.store.ListTeamDefaults(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list team defaults")
		return
	}

	summaries := make([]models.UserCredentialSummary, 0, len(defaults))
	for _, cred := range defaults {
		s := models.UserCredentialSummary{
			Provider:       cred.Provider,
			Configured:     true,
			IsTeamDefault:  true,
			MaskedKey:      cred.Config.MaskedSummary().MaskedKey,
			SetByUserID:    &cred.UserID,
			LastVerifiedAt: cred.LastVerifiedAt,
		}
		// Look up user name.
		if u, err := h.users.GetByID(r.Context(), orgID, cred.UserID); err == nil {
			s.SetByUserName = u.Name
		}
		summaries = append(summaries, s)
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.UserCredentialSummary]{Data: summaries})
}

// SetTeamDefault sets a credential as the team default (admin only).
func (h *UserCredentialHandler) SetTeamDefault(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	providerStr := chi.URLParam(r, "provider")
	provider := models.ProviderName(providerStr)
	if !provider.IsCodingAgentProvider() {
		writeError(w, http.StatusBadRequest, "INVALID_PROVIDER", "unsupported coding agent provider: "+providerStr)
		return
	}

	var body struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	userID, err := uuid.Parse(body.UserID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_USER_ID", "invalid user_id")
		return
	}

	if err := h.store.SetTeamDefault(r.Context(), orgID, userID, provider); err != nil {
		writeError(w, http.StatusInternalServerError, "SET_FAILED", "failed to set team default")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DeleteTeamDefault removes the team default for a provider (admin only).
func (h *UserCredentialHandler) DeleteTeamDefault(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	providerStr := chi.URLParam(r, "provider")
	provider := models.ProviderName(providerStr)
	if !provider.IsCodingAgentProvider() {
		writeError(w, http.StatusBadRequest, "INVALID_PROVIDER", "unsupported coding agent provider: "+providerStr)
		return
	}

	if err := h.store.RemoveTeamDefault(r.Context(), orgID, provider); err != nil {
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "failed to remove team default")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListResolved shows which credential source would be used for each provider.
func (h *UserCredentialHandler) ListResolved(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}

	// Fetch team defaults once (instead of per-provider).
	teamDefaults, _ := h.store.ListTeamDefaults(r.Context(), orgID)
	teamDefaultByProvider := make(map[models.ProviderName]models.DecryptedUserCredential, len(teamDefaults))
	for _, td := range teamDefaults {
		teamDefaultByProvider[td.Provider] = td
	}

	var resolved []models.ResolvedCredential
	for _, provider := range models.CodingAgentProviders {
		rc := models.ResolvedCredential{Provider: provider, Source: "none"}

		// 1. Check personal.
		if cred, err := h.store.GetForUser(r.Context(), orgID, user.ID, provider); err == nil && cred != nil {
			rc.Source = "personal"
			rc.MaskedKey = cred.Config.MaskedSummary().MaskedKey
			resolved = append(resolved, rc)
			continue
		}

		// 2. Check team default.
		if td, ok := teamDefaultByProvider[provider]; ok {
			rc.Source = "team_default"
			rc.MaskedKey = td.Config.MaskedSummary().MaskedKey
			resolved = append(resolved, rc)
			continue
		}

		// 3. Check org credential.
		if h.orgCreds != nil {
			if cred, err := h.orgCreds.Get(r.Context(), orgID, provider); err == nil && cred != nil {
				rc.Source = "org"
				rc.MaskedKey = cred.Config.MaskedSummary().MaskedKey
				resolved = append(resolved, rc)
				continue
			}
		}

		resolved = append(resolved, rc)
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.ResolvedCredential]{Data: resolved})
}
