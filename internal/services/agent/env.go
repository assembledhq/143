package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// AuthError is returned by CheckAuth and parsePlan's auth-detection heuristic
// when an agent run cannot authenticate. Callers can errors.As to distinguish
// auth failures from generic errors — the PM service uses this to persist a
// descriptive failure on the plan record so the UI can show actionable guidance
// ("PM paused: configure Codex") instead of an opaque parse error.
type AuthError struct {
	AgentType models.AgentType
	Detail    string
}

func (e *AuthError) Error() string {
	if e.AgentType != "" {
		return fmt.Sprintf("agent auth failed (%s): %s", e.AgentType, e.Detail)
	}
	return fmt.Sprintf("agent auth failed: %s", e.Detail)
}

// AgentEnv owns the logic for shaping the sandbox environment and auth
// setup for a coding agent run. It is the single source of truth for:
//   - per-agent-type provider credential resolution (user → team → org)
//   - integration credentials (Sentry / Linear / Notion)
//   - agent_config overrides for non-secret agent defaults (Amp / Pi only)
//   - auth pre-flight checks
//   - Codex auth.json injection
//
// Both Orchestrator (interactive sessions) and the PM service (cron-triggered
// agent runs) depend on AgentEnv so that any change to agent auth lives in
// exactly one place — no more "PM works for Claude Code but breaks for Codex".
type AgentEnv struct {
	credentials      CredentialProvider
	userCredentials  UserCredentialProvider
	orgs             OrgStore
	orgSettingsCache *OrgSettingsCache
	codexAuth        CodexAuthProvider
	provider         SandboxProvider
	logger           zerolog.Logger
}

// AgentEnvDeps holds the dependencies for constructing an AgentEnv. Named
// AgentEnvDeps (rather than AgentEnvConfig) to avoid confusion with
// models.AgentEnvConfig, which is a per-org override map consumed by this
// helper.
type AgentEnvDeps struct {
	Credentials      CredentialProvider
	UserCredentials  UserCredentialProvider // optional — enables personal/team resolution
	Orgs             OrgStore               // optional — enables agent_config overrides
	OrgSettingsCache *OrgSettingsCache      // optional — caches agent_config lookups
	CodexAuth        CodexAuthProvider      // optional — enables ChatGPT OAuth for Codex
	Provider         SandboxProvider        // required for InjectCodexAuth
	Logger           zerolog.Logger
}

// NewAgentEnv constructs an AgentEnv. The Provider is required; all other
// dependencies are optional and disable the corresponding feature when nil
// (e.g. no UserCredentials → personal/team resolution is skipped and only
// org-scoped credentials are consulted).
func NewAgentEnv(deps AgentEnvDeps) *AgentEnv {
	return &AgentEnv{
		credentials:      deps.Credentials,
		userCredentials:  deps.UserCredentials,
		orgs:             deps.Orgs,
		orgSettingsCache: deps.OrgSettingsCache,
		codexAuth:        deps.CodexAuth,
		provider:         deps.Provider,
		logger:           deps.Logger,
	}
}

// integrationCredentials holds the resolved Sentry, Linear, and Notion configs for an org.
type integrationCredentials struct {
	Sentry *models.SentryConfig
	Linear *models.LinearConfig
	Notion *models.NotionConfig
}

// fetchIntegrationCredentials retrieves the Sentry, Linear, and Notion configs
// for an org from the credential provider. Returns zero-value configs (nil
// pointers inside the returned struct) when a credential is unavailable —
// callers should nil-check each pointer before use.
func (e *AgentEnv) fetchIntegrationCredentials(ctx context.Context, orgID uuid.UUID) integrationCredentials {
	var ic integrationCredentials
	if e.credentials == nil {
		return ic
	}

	if cred, err := e.credentials.Get(ctx, orgID, models.ProviderSentry); err == nil && cred != nil {
		if cfg, ok := cred.Config.(models.SentryConfig); ok {
			ic.Sentry = &cfg
		}
	}
	if cred, err := e.credentials.Get(ctx, orgID, models.ProviderLinear); err == nil && cred != nil {
		if cfg, ok := cred.Config.(models.LinearConfig); ok {
			ic.Linear = &cfg
		}
	}
	if cred, err := e.credentials.Get(ctx, orgID, models.ProviderNotion); err == nil && cred != nil {
		if cfg, ok := cred.Config.(models.NotionConfig); ok {
			ic.Notion = &cfg
		}
	}
	return ic
}

// Resolve builds the sandbox env vars for the given agent type. It checks
// credentials in order: user personal → team default → org credential.
// Codex CLI auth is handled via auth.json injection (InjectCodexAuth), not
// env vars.
//
// Invariant: sandbox env must only come from org-scoped DB credentials. Do
// NOT fall back to server-level env vars (e.g. cfg.AnthropicAPIKey,
// cfg.OpenAIAPIKey) — those are 143.dev-level platform credentials and would
// leak across orgs in a multi-tenant deployment. Server-level LLM keys are
// reserved for 143's own internal LLM calls via Config.LLMConfig().
func (e *AgentEnv) Resolve(ctx context.Context, orgID uuid.UUID, agentType models.AgentType, userID *uuid.UUID) map[string]string {
	merged := make(map[string]string)

	switch agentType {
	case models.AgentTypeClaudeCode:
		cfg := e.resolveProviderConfig(ctx, orgID, userID, models.ProviderAnthropic)
		if ac, ok := cfg.(models.AnthropicConfig); ok {
			if ac.APIKey != "" {
				merged["ANTHROPIC_API_KEY"] = ac.APIKey
			}
			if ac.BaseURL != "" {
				merged["ANTHROPIC_BASE_URL"] = ac.BaseURL
			}
		}
	case models.AgentTypeCodex:
		// Codex CLI authenticates via ~/.codex/auth.json (injected by
		// InjectCodexAuth), NOT via the CODEX_API_KEY env var. The env var
		// makes Codex call api.openai.com/v1/responses which requires the
		// api.responses.write scope — a scope the ChatGPT OAuth token does
		// not carry. The auth.json path uses the ChatGPT backend instead,
		// which accepts the OAuth token as-is.
		//
		// Inject the general OpenAI API key as OPENAI_API_KEY for other
		// tools in the sandbox (not used by Codex CLI itself).
		cfg := e.resolveProviderConfig(ctx, orgID, userID, models.ProviderOpenAI)
		if oc, ok := cfg.(models.OpenAIConfig); ok {
			if oc.APIKey != "" {
				merged["OPENAI_API_KEY"] = oc.APIKey
			}
			if oc.BaseURL != "" {
				merged["OPENAI_BASE_URL"] = oc.BaseURL
			}
		}
		// Skip Codex CLI's internal bwrap (bubblewrap) sandboxing. The
		// container is already isolated by Docker + gVisor (dropped caps,
		// read-only rootfs, non-root user, PID limits), so bwrap is
		// redundant and fails because gVisor doesn't support the
		// unprivileged user namespaces that bwrap requires.
		merged["CODEX_UNSAFE_ALLOW_NO_SANDBOX"] = "1"
	case models.AgentTypeGeminiCLI:
		cfg := e.resolveProviderConfig(ctx, orgID, userID, models.ProviderGemini)
		if gc, ok := cfg.(models.GeminiConfig); ok {
			if gc.APIKey != "" {
				merged["GEMINI_API_KEY"] = gc.APIKey
			}
			if gc.Model != "" {
				merged["GEMINI_MODEL"] = gc.Model
			}
		}
	case models.AgentTypeAmp:
		cfg := e.resolveProviderConfig(ctx, orgID, userID, models.ProviderAmp)
		if amp, ok := cfg.(models.AmpConfig); ok && amp.APIKey != "" {
			merged["AMP_API_KEY"] = amp.APIKey
		}
	case models.AgentTypePi:
		cfg := e.resolveProviderConfig(ctx, orgID, userID, models.ProviderPi)
		if pi, ok := cfg.(models.PiConfig); ok && pi.APIKey != "" {
			merged["PI_API_KEY"] = pi.APIKey
		}
	}

	// Integration credentials — consumed by the 143-tools CLI (preferred)
	// and 143-mcp binary inside the sandbox. Agents use the CLI via shell
	// commands; the MCP server is only for IDE integrations. See
	// internal/services/mcp/AGENTS.md.
	ic := e.fetchIntegrationCredentials(ctx, orgID)
	if ic.Sentry != nil {
		if ic.Sentry.AccessToken != "" {
			merged["SENTRY_AUTH_TOKEN"] = ic.Sentry.AccessToken
		}
		if ic.Sentry.OrgSlug != "" {
			merged["SENTRY_ORG_SLUG"] = ic.Sentry.OrgSlug
		}
	}
	if ic.Linear != nil {
		if ic.Linear.AccessToken != "" {
			merged["LINEAR_ACCESS_TOKEN"] = ic.Linear.AccessToken
		}
	}
	if ic.Notion != nil {
		if ic.Notion.AccessToken != "" {
			merged["NOTION_ACCESS_TOKEN"] = ic.Notion.AccessToken
		}
	}

	// Apply per-agent env overrides from org settings (agent_config.<type>.*).
	// Scoped to Amp and Pi only — these are non-secret runtime defaults
	// (AMP_MODE, PI_MODEL, PI_MODEL_CUSTOM), while auth itself comes from the
	// credential stores. For claude_code/codex/gemini_cli we keep the legacy
	// behavior: provider creds come exclusively from resolveProviderConfig,
	// and agent_config is treated as model-default metadata (validated,
	// stored, but not injected here) — changing that would silently flip
	// existing orgs' active keys.
	if agentType == models.AgentTypeAmp || agentType == models.AgentTypePi {
		e.applyAgentConfigOverrides(ctx, orgID, agentType, merged)
	}

	if len(merged) == 0 {
		return nil
	}

	return merged
}

// CheckAuth returns a user-facing error when an agent type has no chance of
// authenticating against its upstream because the required credential is
// missing from the resolved sandbox env. This is a pre-flight check intended
// to beat the generic "CLI exited 1" failure with something the user can act
// on — "configure Pi auth" instead of "pi: invalid api key".
//
// Invariant: callers must pass the already-merged sandbox env — i.e. after
// Resolve has run (which layers agent_config overrides on top of resolved
// provider creds) and after any per-run ModelOverride has been applied.
func (e *AgentEnv) CheckAuth(agentType models.AgentType, env map[string]string) error {
	switch agentType {
	case models.AgentTypeAmp:
		if env["AMP_API_KEY"] == "" {
			return &AuthError{
				AgentType: agentType,
				Detail:    "missing AMP_API_KEY: configure Amp under Settings → Default Agent → Amp before starting a session",
			}
		}
	case models.AgentTypePi:
		if env["PI_API_KEY"] == "" {
			return &AuthError{
				AgentType: agentType,
				Detail:    "missing PI_API_KEY: configure Pi under Settings → Default Agent or My settings before starting a session",
			}
		}
	}
	return nil
}

// applyAgentConfigOverrides layers agent_config.<agentType>.* entries from org
// settings on top of the already-resolved provider credentials in `merged`.
// Only called for Amp and Pi; agent_config stores their non-secret runtime
// defaults while auth is resolved from the credential stores. Non-empty values
// win over any prior env value.
//
// Reads go through OrgSettingsCache when configured so a burst of Amp/Pi
// session starts for the same org amortizes to one DB lookup per TTL window.
// The settings update handler invalidates the cache after a write, so
// configuration changes take effect immediately rather than waiting for the
// TTL to expire.
func (e *AgentEnv) applyAgentConfigOverrides(ctx context.Context, orgID uuid.UUID, agentType models.AgentType, merged map[string]string) {
	agentConfig, ok := e.loadAgentConfig(ctx, orgID, agentType)
	if !ok {
		return
	}
	for k, v := range agentConfig[string(agentType)] {
		if v != "" {
			merged[k] = v
		}
	}
}

// loadAgentConfig returns the org's AgentEnvConfig, using the configured
// OrgSettingsCache as a front when present. Returns (nil, false) and logs a
// warning if the org can't be loaded; callers should treat that as "no
// overrides available" rather than failing the session start.
func (e *AgentEnv) loadAgentConfig(ctx context.Context, orgID uuid.UUID, agentType models.AgentType) (models.AgentEnvConfig, bool) {
	if e.orgs == nil {
		e.logger.Warn().
			Str("agent_type", string(agentType)).
			Str("org_id", orgID.String()).
			Msg("agent env helper has no orgs store; skipping agent_config overrides (agent may run without auth)")
		return nil, false
	}

	if e.orgSettingsCache != nil {
		if cached, hit := e.orgSettingsCache.Get(orgID); hit {
			return cached, true
		}
	}

	org, err := e.orgs.GetByID(ctx, orgID)
	if err != nil {
		e.logger.Warn().
			Err(err).
			Str("agent_type", string(agentType)).
			Str("org_id", orgID.String()).
			Msg("failed to load org for agent_config overrides; agent may run without auth")
		return nil, false
	}
	orgSettings, parseErr := models.ParseOrgSettings(org.Settings)
	if parseErr != nil {
		e.logger.Warn().
			Err(parseErr).
			Str("agent_type", string(agentType)).
			Str("org_id", orgID.String()).
			Msg("failed to parse org settings for agent_config overrides; agent may run without auth")
		return nil, false
	}

	if e.orgSettingsCache != nil {
		// Store the (possibly nil) AgentConfig so a second hit doesn't
		// re-fetch just to discover the org has no agent_config.
		e.orgSettingsCache.Set(orgID, orgSettings.AgentConfig)
	}

	return orgSettings.AgentConfig, true
}

// resolveProviderConfig returns the best ProviderConfig for a provider,
// checking in order: user personal → team default → org credential.
func (e *AgentEnv) resolveProviderConfig(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) models.ProviderConfig {
	// 1. Check user's personal credential.
	if userID != nil && e.userCredentials != nil {
		if cred, err := e.userCredentials.GetForUser(ctx, orgID, *userID, provider); err == nil && cred != nil {
			return cred.Config
		}
	}

	// 2. Check team default credential.
	if e.userCredentials != nil {
		if cred, err := e.userCredentials.GetTeamDefault(ctx, orgID, provider); err == nil && cred != nil {
			return cred.Config
		}
	}

	// 3. Fall back to org credential.
	if cfg := e.resolveOrgProviderConfig(ctx, orgID, provider); cfg != nil {
		return cfg
	}

	return nil
}

func (e *AgentEnv) resolveOrgProviderConfig(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) models.ProviderConfig {
	if e.credentials == nil {
		return nil
	}

	if provider.IsCodingAgentProvider() {
		if creds, err := e.credentials.ListByProvider(ctx, orgID, provider); err == nil {
			for _, cred := range creds {
				if cfg := compatibleCodingProviderConfig(provider, cred.Config); cfg != nil {
					return cfg
				}
			}
		}
	}

	if cred, err := e.credentials.Get(ctx, orgID, provider); err == nil && cred != nil {
		if provider.IsCodingAgentProvider() {
			return compatibleCodingProviderConfig(provider, cred.Config)
		}
		return cred.Config
	}

	return nil
}

func compatibleCodingProviderConfig(provider models.ProviderName, cfg models.ProviderConfig) models.ProviderConfig {
	switch provider {
	case models.ProviderAnthropic:
		anthropic, ok := cfg.(models.AnthropicConfig)
		if !ok || anthropic.APIKey == "" || anthropic.Subscription != nil {
			return nil
		}
		return anthropic
	case models.ProviderOpenAI:
		openAI, ok := cfg.(models.OpenAIConfig)
		if !ok || openAI.APIKey == "" {
			return nil
		}
		return openAI
	case models.ProviderGemini:
		gemini, ok := cfg.(models.GeminiConfig)
		if !ok || gemini.APIKey == "" {
			return nil
		}
		return gemini
	case models.ProviderOpenRouter:
		openRouter, ok := cfg.(models.OpenRouterConfig)
		if !ok || openRouter.APIKey == "" {
			return nil
		}
		return openRouter
	case models.ProviderAmp:
		amp, ok := cfg.(models.AmpConfig)
		if !ok || amp.APIKey == "" {
			return nil
		}
		return amp
	case models.ProviderPi:
		pi, ok := cfg.(models.PiConfig)
		if !ok || pi.APIKey == "" {
			return nil
		}
		return pi
	default:
		return nil
	}
}

// InjectCodexAuth writes a ~/.codex/auth.json file into the sandbox if a
// ChatGPT OAuth token exists for this org. This is the primary Codex auth
// mechanism — auth.json tells the CLI to use the ChatGPT backend which
// accepts the OAuth token without needing api.responses.write scope. Returns
// (true, nil) if auth was injected, (false, nil) if no OAuth token is
// available, or (false, err) on failure.
func (e *AgentEnv) InjectCodexAuth(ctx context.Context, orgID uuid.UUID, sandbox *Sandbox) (bool, error) {
	if e.codexAuth == nil {
		return false, nil
	}

	// Use round-robin selection across all active subscriptions for this org.
	// GetValidToken claims the least-recently-used credential, refreshing it
	// in-band if it's near expiry. This is the canonical path; the legacy
	// single-credential RefreshToken would always pick the same row and
	// bypass round-robin entirely.
	cfg, err := e.codexAuth.GetValidToken(ctx, orgID)
	if err != nil {
		return false, fmt.Errorf("get codex auth token: %w", err)
	}
	if cfg == nil {
		// No OAuth token — not an error, agent will use API key.
		return false, nil
	}

	// Omit the refresh_token from auth.json so the Codex CLI never attempts
	// to refresh the token itself. If the CLI refreshes the token inside the
	// sandbox, it consumes the refresh_token on OpenAI's servers, but the
	// sandbox-side token is lost when the container is destroyed. Our DB
	// then holds a stale refresh_token, and the next turn's RefreshToken()
	// call gets refresh_token_reused. By omitting refresh_token, the CLI
	// uses the fresh access_token (15-min TTL) as-is, and our server retains
	// sole ownership of the refresh_token for future turns.
	authJSON, err := json.Marshal(map[string]interface{}{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"access_token":  cfg.AccessToken,
			"refresh_token": "",
			"id_token":      cfg.IDToken,
		},
		"last_refresh": time.Now().Format(time.RFC3339),
	})
	if err != nil {
		return false, fmt.Errorf("marshal auth.json: %w", err)
	}

	// Write auth.json under $HOME/.codex. The sandbox env sets HOME to the
	// sandbox user's home dir (see RunAgent's sandbox setup) so the Codex
	// CLI resolves ~/.codex/auth.json to this path.
	authDir := path.Join(sandbox.HomeDir, ".codex")
	mkdirCmd := fmt.Sprintf("mkdir -p '%s'", shellEscapeSingleQuote(authDir))

	var mkdirOut, mkdirErr bytes.Buffer
	exitCode, err := e.provider.Exec(ctx, sandbox, mkdirCmd, &mkdirOut, &mkdirErr)
	if err != nil {
		return false, fmt.Errorf("create .codex dir: %w", err)
	}
	if exitCode != 0 {
		return false, fmt.Errorf("create .codex dir: mkdir exited with code %d: %s", exitCode, mkdirErr.String())
	}

	authPath := authDir + "/auth.json"
	if err := e.provider.WriteFile(ctx, sandbox, authPath, authJSON); err != nil {
		return false, fmt.Errorf("write auth.json: %w", err)
	}

	// Write config.toml to disable Codex's internal bwrap sandboxing. The
	// container is already isolated by Docker + gVisor so bwrap is redundant
	// and fails because gVisor doesn't support the unprivileged user
	// namespaces that bwrap requires.
	configTOML := []byte("sandbox_mode = \"danger-full-access\"\n")
	configPath := authDir + "/config.toml"
	if err := e.provider.WriteFile(ctx, sandbox, configPath, configTOML); err != nil {
		return false, fmt.Errorf("write config.toml: %w", err)
	}

	e.logger.Debug().
		Str("org_id", orgID.String()).
		Msg("injected codex auth.json and config.toml into sandbox")

	return true, nil
}
