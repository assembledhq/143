package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/assembledhq/143/internal/services/mcp"
)

// cliToolsCredentialProvider is the slice of OrgCredentialStore the gateway
// needs: one batched read of the org's integration credentials.
type cliToolsCredentialProvider interface {
	GetAllIntegrations(ctx context.Context, orgID uuid.UUID, providers []models.ProviderName) (map[models.ProviderName]*models.DecryptedCredential, error)
}

// cliToolsLinearTokenResolver mirrors agent.LinearTokenResolver: it returns
// a currently-valid Linear access token, refreshing server-side when needed.
type cliToolsLinearTokenResolver interface {
	GetValidAccessToken(ctx context.Context, orgID uuid.UUID) (string, error)
}

type cliToolsPrivateConnectorLogProviderSource interface {
	LogProviders(ctx context.Context, orgID uuid.UUID) ([]integration.LogProvider, error)
}

type cliToolsPrivateConnectorDatabaseProviderSource interface {
	DatabaseProviders(ctx context.Context, orgID uuid.UUID) ([]integration.DatabaseProvider, error)
}

// CLIToolsHandler is the local agent gateway: it exposes the org's
// integration tool registry to logged-in CLIs, executing every call
// server-side with org credentials (which never land on laptops) and a
// per-user audit event per call. Revoking a user's CLI token — or removing
// them from the org — instantly cuts their local agents off from every
// integration.
type CLIToolsHandler struct {
	credentials  cliToolsCredentialProvider
	linearTokens cliToolsLinearTokenResolver
	privateLogs  cliToolsPrivateConnectorLogProviderSource
	privateDBs   cliToolsPrivateConnectorDatabaseProviderSource
	audit        *db.AuditEmitter
	logger       zerolog.Logger
}

func NewCLIToolsHandler(credentials cliToolsCredentialProvider, logger zerolog.Logger) *CLIToolsHandler {
	return &CLIToolsHandler{credentials: credentials, logger: logger}
}

// SetLinearTokenResolver wires refresh-aware Linear token resolution.
// Optional: without it, Linear tools use the stored access token as-is.
func (h *CLIToolsHandler) SetLinearTokenResolver(resolver cliToolsLinearTokenResolver) {
	h.linearTokens = resolver
}

func (h *CLIToolsHandler) SetPrivateConnectorLogProviderSource(source cliToolsPrivateConnectorLogProviderSource) {
	h.privateLogs = source
}

func (h *CLIToolsHandler) SetPrivateConnectorDatabaseProviderSource(source cliToolsPrivateConnectorDatabaseProviderSource) {
	h.privateDBs = source
}

func (h *CLIToolsHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

var cliToolsProviderNames = []models.ProviderName{
	models.ProviderSentry,
	models.ProviderLinear,
	models.ProviderNotion,
	models.ProviderCircleCI,
	models.ProviderMezmo,
}

// buildOrgToolSource assembles the per-request tool registry from the org's
// connected integrations. Built per request rather than cached: credential
// rotation, integration connects/disconnects, and Linear token refresh all
// take effect immediately, and registry construction is cheap (no I/O —
// the constructors just capture config).
func (h *CLIToolsHandler) buildOrgToolSource(ctx context.Context, orgID uuid.UUID) (*mcp.ToolRegistry, error) {
	creds, err := h.credentials.GetAllIntegrations(ctx, orgID, cliToolsProviderNames)
	if err != nil {
		return nil, err
	}

	var orgCreds mcp.OrgCredentials
	if cred := creds[models.ProviderSentry]; cred != nil {
		if cfg, ok := cred.Config.(models.SentryConfig); ok {
			orgCreds.Sentry = &cfg
		}
	}
	if cred := creds[models.ProviderNotion]; cred != nil {
		if cfg, ok := cred.Config.(models.NotionConfig); ok {
			orgCreds.Notion = &cfg
		}
	}
	if cred := creds[models.ProviderCircleCI]; cred != nil {
		if cfg, ok := cred.Config.(models.CircleCIConfig); ok {
			orgCreds.CircleCI = &cfg
		}
	}
	if cred := creds[models.ProviderMezmo]; cred != nil {
		if cfg, ok := cred.Config.(models.MezmoConfig); ok {
			orgCreds.Mezmo = &cfg
		}
	}

	// Linear: prefer the refresh-aware resolver so a near-expiring token is
	// rotated before use; fall back to the stored access token for wiring
	// (tests) without a resolver.
	switch {
	case h.linearTokens != nil:
		token, tokenErr := h.linearTokens.GetValidAccessToken(ctx, orgID)
		if tokenErr != nil {
			h.logger.Warn().Err(tokenErr).Str("org_id", orgID.String()).
				Msg("cli tools: linear token resolution failed; linear tools unavailable this request")
		} else {
			orgCreds.LinearAccessToken = token
		}
	default:
		if cred := creds[models.ProviderLinear]; cred != nil {
			if cfg, ok := cred.Config.(models.LinearConfig); ok {
				orgCreds.LinearAccessToken = cfg.AccessToken
			}
		}
	}

	registry := mcp.BuildRegistryFromOrg(orgCreds)
	if h.privateLogs != nil {
		providers, providerErr := h.privateLogs.LogProviders(ctx, orgID)
		if providerErr != nil {
			return nil, providerErr
		}
		for _, provider := range providers {
			registry.RegisterLogProvider(provider)
		}
	}
	if h.privateDBs != nil {
		providers, providerErr := h.privateDBs.DatabaseProviders(ctx, orgID)
		if providerErr != nil {
			return nil, providerErr
		}
		for _, provider := range providers {
			registry.RegisterDatabaseProvider(provider)
		}
	}
	return mcp.NewToolRegistry(registry), nil
}

// ListTools returns the tool definitions available to this org's local
// agents. The CLI's MCP server fetches this at startup instead of
// hardcoding a list, so availability mirrors the org's connected
// integrations.
func (h *CLIToolsHandler) ListTools(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}
	source, err := h.buildOrgToolSource(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CLI_TOOLS_UNAVAILABLE", "failed to load org integrations", err)
		return
	}
	tools := source.ListTools()
	if tools == nil {
		tools = []mcp.Tool{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"tools": tools}})
}

type cliToolInvokeRequest struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// Invoke executes one tool call with the org's server-side credentials and
// emits a per-user `cli.tool_invoked` audit event. The event records the
// tool name but never the args — args can contain sensitive content (issue
// text, log queries) that doesn't belong in a retained audit row.
func (h *CLIToolsHandler) Invoke(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	orgID := middleware.OrgIDFromContext(r.Context())
	if user == nil || orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "NO_ACTIVE_ORG", "no active organization")
		return
	}

	var body cliToolInvokeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Tool) == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "tool is required")
		return
	}
	if body.Args == nil {
		body.Args = json.RawMessage(`{}`)
	}

	source, err := h.buildOrgToolSource(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CLI_TOOLS_UNAVAILABLE", "failed to load org integrations", err)
		return
	}
	if !toolExists(source.ListTools(), body.Tool) {
		writeError(w, r, http.StatusNotFound, "TOOL_NOT_FOUND",
			"unknown tool — it may belong to an integration this org has not connected")
		return
	}

	h.emitToolInvoked(r, user.ID, orgID, body.Tool)

	result := source.CallTool(r.Context(), body.Tool, body.Args)
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

func toolExists(tools []mcp.Tool, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

func (h *CLIToolsHandler) emitToolInvoked(r *http.Request, userID, orgID uuid.UUID, toolName string) {
	if h.audit == nil {
		return
	}
	details, _ := json.Marshal(map[string]any{"tool": toolName})
	params := db.UserActionParams{
		OrgID:        orgID,
		UserID:       userID,
		Action:       models.AuditActionCLIToolInvoked,
		ResourceType: models.AuditResourceCLITool,
		ResourceID:   &toolName,
		Details:      details,
	}
	if ua := r.UserAgent(); ua != "" {
		params.UserAgent = &ua
	}
	if ip := parseClientIP(r); ip != nil {
		params.IPAddress = ip
	}
	h.audit.EmitUserAction(r.Context(), params)
}
