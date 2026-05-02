package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// LinearAgentSettingsHandler exposes the admin surface for the inbound
// agent feature: the team→repo mapping CRUD and the agent install status.
//
// Endpoints (all org-scoped; admin-only enforced by middleware upstream):
//   GET    /api/v1/integrations/linear/agent             — feature status
//   GET    /api/v1/integrations/linear/agent/mappings    — list mappings
//   POST   /api/v1/integrations/linear/agent/mappings    — create/update
//   DELETE /api/v1/integrations/linear/agent/mappings/{id}
//
// The settings panel is intentionally minimal in phase 2: list, create,
// delete. Per-team enable toggles + label-override docs live with phase 4
// polish.
type LinearAgentSettingsHandler struct {
	mappings    *db.LinearTeamRepoMappingStore
	credentials linearAgentCredentialReader
	settings    linearAgentOrgSettings
	logger      zerolog.Logger
}

// linearAgentCredentialReader is the narrow surface the handler needs from
// the credential store. Pulled into an interface so the install-status
// endpoint can be tested without the full encryption stack.
type linearAgentCredentialReader interface {
	Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
}

// linearAgentOrgSettings is the narrow surface for loading org settings to
// derive feature-enabled. nil-safe at the handler boundary.
type linearAgentOrgSettings interface {
	LoadAgentSettings(ctx context.Context, orgID uuid.UUID) (models.LinearAgentSettings, error)
}

// NewLinearAgentSettingsHandler wires the handler.
func NewLinearAgentSettingsHandler(mappings *db.LinearTeamRepoMappingStore, credentials linearAgentCredentialReader, settings linearAgentOrgSettings, logger zerolog.Logger) *LinearAgentSettingsHandler {
	return &LinearAgentSettingsHandler{
		mappings:    mappings,
		credentials: credentials,
		settings:    settings,
		logger:      logger.With().Str("component", "linear_agent_settings").Logger(),
	}
}

// LinearAgentInstallStatus is the GET /agent response. Captures everything
// the settings UI needs to render the "Install agent" / "Re-authorize"
// banner without a separate roundtrip.
type LinearAgentInstallStatus struct {
	// Enabled mirrors org_settings.linear_agent.enabled. Independent of
	// install status — an org can have the agent installed but disabled.
	Enabled bool `json:"enabled"`
	// AgentScopesGranted is true when the connected token holds the
	// `app:assignable` and `app:mentionable` scopes. False means a
	// re-authorize banner should fire.
	AgentScopesGranted bool `json:"agent_scopes_granted"`
	// AppUserName is the @-handle Linear assigned the agent user
	// (typically "143"). Empty when no agent install is present.
	AppUserName string `json:"app_user_name,omitempty"`
	// HasLinearIntegration is true when the org has *any* connected
	// Linear integration (legacy or agent). Surfaces a clearer "connect
	// Linear first" message when both flags are false.
	HasLinearIntegration bool `json:"has_linear_integration"`
}

// GetStatus returns the install / enabled state.
func (h *LinearAgentSettingsHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	status := LinearAgentInstallStatus{}
	if h.credentials != nil {
		cred, err := h.credentials.Get(r.Context(), orgID, models.ProviderLinear)
		if err == nil && cred != nil {
			status.HasLinearIntegration = true
			if linearCfg, ok := cred.Config.(models.LinearConfig); ok {
				status.AgentScopesGranted = linearCfg.HasAgentScopes()
				status.AppUserName = linearCfg.AppUserName
			}
		}
	}
	if h.settings != nil {
		settings, err := h.settings.LoadAgentSettings(r.Context(), orgID)
		if err != nil {
			h.logger.Warn().Err(err).Msg("failed to load agent settings; defaulting Enabled=false")
		} else {
			status.Enabled = settings.EffectiveEnabled()
		}
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[LinearAgentInstallStatus]{Data: status})
}

// ListMappings returns all team→repo mappings for the org.
func (h *LinearAgentSettingsHandler) ListMappings(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	mappings, err := h.mappings.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list linear team repo mappings", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[db.LinearTeamRepoMapping]{Data: mappings})
}

// upsertMappingRequest is the JSON body for POST /mappings.
type upsertMappingRequest struct {
	LinearTeamID    string    `json:"linear_team_id"`
	LinearProjectID string    `json:"linear_project_id,omitempty"`
	RepositoryID    uuid.UUID `json:"repository_id"`
	DefaultBranch   string    `json:"default_branch,omitempty"`
	Priority        int       `json:"priority,omitempty"`
}

// UpsertMapping creates or updates a mapping. POST /mappings replaces the
// row for (org, team, project) atomically; callers who want strict create-
// only semantics should pre-check via List first. The simpler upsert API
// is what the settings UI's edit flow expects.
func (h *LinearAgentSettingsHandler) UpsertMapping(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	var req upsertMappingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse request body", err)
		return
	}
	if req.LinearTeamID == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "linear_team_id is required")
		return
	}
	if req.RepositoryID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "repository_id is required")
		return
	}

	mapping, err := h.mappings.Upsert(r.Context(), orgID, db.UpsertInput{
		OrgID:           orgID,
		LinearTeamID:    req.LinearTeamID,
		LinearProjectID: req.LinearProjectID,
		RepositoryID:    req.RepositoryID,
		DefaultBranch:   req.DefaultBranch,
		Priority:        req.Priority,
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPSERT_FAILED", "failed to upsert linear team repo mapping", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[db.LinearTeamRepoMapping]{Data: *mapping})
}

// DeleteMapping removes a mapping by id.
func (h *LinearAgentSettingsHandler) DeleteMapping(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	idStr := r.PathValue("id")
	if idStr == "" {
		// chi's URLParam fallback for routers that didn't migrate to
		// PathValue yet.
		idStr = r.URL.Query().Get("id")
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid id")
		return
	}
	if err := h.mappings.Delete(r.Context(), orgID, id); err != nil {
		if errors.Is(err, db.ErrLinearTeamRepoMappingNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "mapping not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete mapping", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
