package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// LinearAgentSettingsHandler exposes the admin surface for the inbound
// agent feature: the team→repo mapping CRUD, install status, the per-org
// enable toggle, and the operator debug surface listing recent
// AgentSessions.
//
// Endpoints (all org-scoped; admin-only enforced by middleware upstream):
//
//	GET    /api/v1/integrations/linear/agent             — install status
//	PATCH  /api/v1/integrations/linear/agent             — enable/disable
//	GET    /api/v1/integrations/linear/agent/mappings    — list mappings
//	POST   /api/v1/integrations/linear/agent/mappings    — create/update
//	DELETE /api/v1/integrations/linear/agent/mappings/{id}
//	GET    /api/v1/integrations/linear/agent/sessions    — debug list
//	GET    /api/v1/integrations/linear/agent/sessions/{id} — debug detail
type LinearAgentSettingsHandler struct {
	mappings      *db.LinearTeamRepoMappingStore
	credentials   linearAgentCredentialReader
	settings      linearAgentOrgSettings
	agentSessions *db.LinearAgentSessionStore
	activities    *db.LinearAgentActivityLogStore
	orgs          linearAgentOrgWriter
	logger        zerolog.Logger
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

// linearAgentOrgWriter is the narrow surface for persisting per-org agent
// settings (enable toggle). Pulled into an interface so the route handler
// can be tested without standing up the full OrganizationStore.
type linearAgentOrgWriter interface {
	SetLinearAgentEnabled(ctx context.Context, orgID uuid.UUID, enabled bool) error
}

// LinearAgentSettingsConfig packages the wiring parameters. Optional
// fields (debug surface, enable toggle) may be left nil for boot stages
// that don't yet have those stores constructed.
type LinearAgentSettingsConfig struct {
	Mappings      *db.LinearTeamRepoMappingStore
	Credentials   linearAgentCredentialReader
	Settings      linearAgentOrgSettings
	AgentSessions *db.LinearAgentSessionStore
	Activities    *db.LinearAgentActivityLogStore
	Orgs          linearAgentOrgWriter
	Logger        zerolog.Logger
}

// NewLinearAgentSettingsHandler wires the handler.
func NewLinearAgentSettingsHandler(cfg LinearAgentSettingsConfig) *LinearAgentSettingsHandler {
	return &LinearAgentSettingsHandler{
		mappings:      cfg.Mappings,
		credentials:   cfg.Credentials,
		settings:      cfg.Settings,
		agentSessions: cfg.AgentSessions,
		activities:    cfg.Activities,
		orgs:          cfg.Orgs,
		logger:        cfg.Logger.With().Str("component", "linear_agent_settings").Logger(),
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

// PatchEnableRequest is the JSON body for PATCH /agent. Only the Enabled
// flag is honored today; future fields (per-team overrides, default repo)
// can extend this struct without breaking callers because absent fields
// leave existing settings untouched.
type PatchEnableRequest struct {
	Enabled *bool `json:"enabled"`
}

// PatchSettings flips the per-org enable flag.
func (h *LinearAgentSettingsHandler) PatchSettings(w http.ResponseWriter, r *http.Request) {
	if h.orgs == nil {
		writeError(w, r, http.StatusServiceUnavailable, "ORG_WRITER_UNAVAILABLE", "agent settings writer not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	var req PatchEnableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "failed to parse request body", err)
		return
	}
	if req.Enabled == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "enabled is required")
		return
	}
	if err := h.orgs.SetLinearAgentEnabled(r.Context(), orgID, *req.Enabled); err != nil {
		writeError(w, r, http.StatusInternalServerError, "PATCH_FAILED", "failed to update agent settings", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AgentSessionDebugSummary is the projection rendered by the debug list
// endpoint. Captures the join across linear_agent_sessions and the
// optional 143 session row so an operator can answer "for this Linear
// AgentSession, what's the 143-side state?" in one fetch.
type AgentSessionDebugSummary struct {
	ID                    uuid.UUID                      `json:"id"`
	LinearAgentSessionID  string                         `json:"linear_agent_session_id"`
	LinearIssueIdentifier string                         `json:"linear_issue_identifier,omitempty"`
	State                 models.LinearAgentSessionState `json:"state"`
	SessionID             *uuid.UUID                     `json:"session_id,omitempty"`
	CreatedAt             string                         `json:"created_at"`
	UpdatedAt             string                         `json:"updated_at"`
	LastEventReceivedAt   string                         `json:"last_event_received_at,omitempty"`
}

// ListSessions returns the most-recently-updated agent sessions for the
// org. Capped at the limit the operator passes via ?limit=N (default 50,
// max 200). Uses ListByOrg for an org-scoped indexed scan rather than
// scanning the cross-org pending-recovery list.
func (h *LinearAgentSettingsHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	if h.agentSessions == nil {
		writeError(w, r, http.StatusServiceUnavailable, "FEATURE_OFF", "agent feature not enabled in this deployment")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	limit := parseLimitParam(r, 50, 200)
	rows, err := h.agentSessions.ListByOrg(r.Context(), orgID, limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list agent sessions", err)
		return
	}
	out := make([]AgentSessionDebugSummary, 0, len(rows))
	for _, row := range rows {
		summary := AgentSessionDebugSummary{
			ID:                    row.ID,
			LinearAgentSessionID:  row.LinearAgentSessionID,
			LinearIssueIdentifier: row.LinearIssueIdentifier,
			State:                 row.State,
			SessionID:             row.SessionID,
			CreatedAt:             row.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			UpdatedAt:             row.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
		if row.LastEventReceivedAt != nil {
			summary.LastEventReceivedAt = row.LastEventReceivedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		out = append(out, summary)
	}
	writeJSON(w, http.StatusOK, models.ListResponse[AgentSessionDebugSummary]{Data: out})
}

// parseLimitParam parses ?limit=N from the request, clamped to
// [1, max]. Falls back to dflt when the param is absent or unparsable.
// Centralized so future debug endpoints can share the same conventions.
func parseLimitParam(r *http.Request, dflt, max int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return dflt
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return dflt
	}
	if n > max {
		return max
	}
	return n
}

// AgentSessionDebugDetail captures the full per-session debug view —
// the linear_agent_sessions row plus the activity log so an operator
// can replay what the agent has emitted to Linear.
type AgentSessionDebugDetail struct {
	Session    AgentSessionDebugSummary    `json:"session"`
	Activities []db.LinearAgentActivityLog `json:"activities"`
}

// GetSession returns the full debug view for a single agent session.
func (h *LinearAgentSettingsHandler) GetSession(w http.ResponseWriter, r *http.Request) {
	if h.agentSessions == nil || h.activities == nil {
		writeError(w, r, http.StatusServiceUnavailable, "FEATURE_OFF", "agent feature not enabled in this deployment")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	idStr := r.PathValue("id")
	if idStr == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "id is required")
		return
	}
	// The {id} path parameter accepts either the row id (uuid) or the
	// Linear AgentSessionID (a Linear-issued string). We try the uuid
	// shape first because that's what the /sessions list endpoint
	// returns; fallback to Linear ID lookup keeps the URL space stable
	// across UI refactors.
	var (
		row *db.LinearAgentSession
		err error
	)
	if rowID, parseErr := uuid.Parse(idStr); parseErr == nil {
		// Lookup-by-row-id: scan a bounded org page and filter. Bounded
		// because the operator surface only exposes recently-updated
		// rows; older sessions are accessed by Linear AgentSessionID
		// from logs, which routes through the second branch below.
		rows, listErr := h.agentSessions.ListByOrg(r.Context(), orgID, 200)
		if listErr != nil {
			writeError(w, r, http.StatusInternalServerError, "GET_FAILED", "failed to load agent session", listErr)
			return
		}
		for i := range rows {
			if rows[i].ID == rowID {
				row = &rows[i]
				break
			}
		}
		err = errors.New("not found")
		if row != nil {
			err = nil
		}
	} else {
		row, err = h.agentSessions.Lookup(r.Context(), orgID, idStr)
	}
	if err != nil || row == nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "agent session not found")
		return
	}
	activities, err := h.activities.ListForAgentSession(r.Context(), orgID, row.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "GET_FAILED", "failed to load agent activities", err)
		return
	}
	detail := AgentSessionDebugDetail{
		Session: AgentSessionDebugSummary{
			ID:                    row.ID,
			LinearAgentSessionID:  row.LinearAgentSessionID,
			LinearIssueIdentifier: row.LinearIssueIdentifier,
			State:                 row.State,
			SessionID:             row.SessionID,
			CreatedAt:             row.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			UpdatedAt:             row.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		},
		Activities: activities,
	}
	if row.LastEventReceivedAt != nil {
		detail.Session.LastEventReceivedAt = row.LastEventReceivedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[AgentSessionDebugDetail]{Data: detail})
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
