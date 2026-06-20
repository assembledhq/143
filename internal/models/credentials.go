package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ProviderName is a string enum for credential providers.
type ProviderName string

const (
	ProviderAnthropic ProviderName = "anthropic"
	ProviderOpenAI    ProviderName = "openai"
	ProviderGemini    ProviderName = "gemini"
	ProviderAmp       ProviderName = "amp"
	ProviderPi        ProviderName = "pi"
	ProviderOpenCode  ProviderName = "opencode"
	// ProviderOpenAISubscription is the canonical name for Codex subscription
	// credentials on the unified coding_credentials table.
	ProviderOpenAISubscription ProviderName = "openai_subscription"
	// ProviderAnthropicSubscription is the canonical name for Claude Code
	// subscription credentials on the unified coding_credentials table.
	// Subscription tokens are a distinct provider from ProviderAnthropic
	// API keys, each with a dedicated config struct.
	ProviderAnthropicSubscription ProviderName = "anthropic_subscription"
	ProviderOpenRouter            ProviderName = "openrouter"
	ProviderGitHubApp             ProviderName = "github_app"
	ProviderGitHubAppUser         ProviderName = "github_app_user"
	ProviderGitHubOAuth           ProviderName = "github_oauth"
	ProviderSentry                ProviderName = "sentry"
	ProviderLinear                ProviderName = "linear"
	ProviderPagerDuty             ProviderName = "pagerduty"
	ProviderSlack                 ProviderName = "slack"
	ProviderNotion                ProviderName = "notion"
	ProviderCircleCI              ProviderName = "circleci"
	ProviderVictoriaLogs          ProviderName = "victorialogs"
	ProviderMezmo                 ProviderName = "mezmo"
)

// AllProviders is the canonical list of credential providers.
var AllProviders = []ProviderName{
	ProviderAnthropic, ProviderAnthropicSubscription,
	ProviderOpenAI, ProviderOpenAISubscription,
	ProviderGemini, ProviderAmp, ProviderPi, ProviderOpenCode, ProviderOpenRouter,
	ProviderGitHubApp, ProviderGitHubAppUser, ProviderGitHubOAuth,
	ProviderSentry, ProviderLinear, ProviderPagerDuty, ProviderSlack, ProviderNotion,
	ProviderCircleCI, ProviderVictoriaLogs, ProviderMezmo,
}

// LLMProviders is the subset of providers that serve LLM completions.
var LLMProviders = []ProviderName{
	ProviderAnthropic, ProviderOpenAI, ProviderGemini, ProviderOpenRouter,
}

// Valid returns true if the provider name is in the canonical list.
func (p ProviderName) Valid() bool {
	for _, v := range AllProviders {
		if p == v {
			return true
		}
	}
	return false
}

// IsCodingAgentProvider returns true if the provider is used for coding agents.
func (p ProviderName) IsCodingAgentProvider() bool {
	for _, v := range CodingAgentProviders {
		if p == v {
			return true
		}
	}
	return false
}

// IsLLMProvider returns true if the provider serves LLM completions.
func (p ProviderName) IsLLMProvider() bool {
	for _, v := range LLMProviders {
		if p == v {
			return true
		}
	}
	return false
}

// ProviderConfig is implemented by every per-provider config struct.
type ProviderConfig interface {
	Provider() ProviderName
	Validate() error
	MaskedSummary() CredentialSummary
}

// --- Per-provider config structs ---

type AnthropicConfig struct {
	APIKey  string `json:"api_key,omitempty"` // #nosec G117 -- JSON config field
	BaseURL string `json:"base_url,omitempty"`
}

// AnthropicSubscription holds OAuth tokens issued by the Claude Code CLI
// authorization-code + PKCE flow. This is the runtime token shape returned by
// claudecodeauth.GetValidToken and injected into sandboxes; the persisted row
// shape is AnthropicSubscriptionConfig (provider anthropic_subscription),
// which carries the identical field set.
//
// Field provenance:
//   - AccessToken/RefreshToken/ExpiresAt come from the /v1/oauth/token endpoint.
//   - Scopes comes from that endpoint's space-separated `scope` response field.
//   - AccountType / RateLimitTier come from a best-effort follow-up fetch of
//     /api/oauth/profile and may be empty if the profile call failed. They are
//     display-only — Claude Code CLI inside the sandbox rebuilds them itself.
type AnthropicSubscription struct {
	AccessToken   string    `json:"access_token"`  // #nosec G117 -- JSON config field
	RefreshToken  string    `json:"refresh_token"` // #nosec G117 -- JSON config field
	ExpiresAt     time.Time `json:"expires_at"`
	AccountType   string    `json:"account_type,omitempty"`    // e.g. "claude_max", "claude_pro"
	RateLimitTier string    `json:"rate_limit_tier,omitempty"` // e.g. "default_claude_max_20x"
	Scopes        []string  `json:"scopes,omitempty"`

	// Pending PKCE-auth fields — only populated between InitiateOAuth and
	// CompleteOAuth. Persisted so the flow survives server restarts.
	//   State        — opaque CSRF token echoed back by Anthropic; verified
	//                  against the user-supplied `code#state` paste.
	//   CodeVerifier — the PKCE verifier whose SHA-256 we sent as
	//                  code_challenge; required to complete the exchange.
	//   AuthorizeURL — the fully-formed /cai/oauth/authorize URL we handed
	//                  to the UI; kept for observability + resume support.
	State        string `json:"state,omitempty"`
	CodeVerifier string `json:"code_verifier,omitempty"` // #nosec G117 -- JSON config field
	AuthorizeURL string `json:"authorize_url,omitempty"`
}

// IsExpired returns true if the access token has passed its expiry.
func (s AnthropicSubscription) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

// NeedsRefresh returns true if the access token will expire within window.
func (s AnthropicSubscription) NeedsRefresh(window time.Duration) bool {
	return time.Now().Add(window).After(s.ExpiresAt)
}

type OpenAIConfig struct {
	APIKey  string `json:"api_key"` // #nosec G117 -- JSON config field
	BaseURL string `json:"base_url,omitempty"`
	APIType string `json:"api_type"`
}

type GeminiConfig struct {
	APIKey string `json:"api_key"` // #nosec G117 -- JSON config field
	Model  string `json:"model,omitempty"`
}

type AmpConfig struct {
	APIKey string `json:"api_key"` // #nosec G117 -- JSON config field
}

type PiConfig struct {
	APIKey string `json:"api_key"` // #nosec G117 -- JSON config field
}

type OpenCodeConfig struct {
	APIKey          string       `json:"api_key"` // #nosec G117 -- JSON config field
	BackingProvider ProviderName `json:"backing_provider,omitempty"`
	BaseURL         string       `json:"base_url,omitempty"`
	Model           string       `json:"model,omitempty"`
}

type OpenRouterConfig struct {
	APIKey  string `json:"api_key"` // #nosec G117 -- JSON config field
	BaseURL string `json:"base_url,omitempty"`
	AppName string `json:"app_name,omitempty"`
	SiteURL string `json:"site_url,omitempty"`
}

type GitHubAppConfig struct {
	AppID         int64  `json:"app_id"`
	PrivateKey    string `json:"private_key"` // #nosec G117 -- JSON config field
	WebhookSecret string `json:"webhook_secret"`
}

type GitHubOAuthConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"` // #nosec G117 -- JSON config field
	AccessToken  string `json:"access_token,omitempty"`  // #nosec G117 -- JSON config field
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

type GitHubAppUserConfig struct {
	AccessToken           string    `json:"access_token"`  // #nosec G117 -- JSON config field
	RefreshToken          string    `json:"refresh_token"` // #nosec G117 -- JSON config field
	TokenType             string    `json:"token_type,omitempty"`
	ExpiresAt             time.Time `json:"expires_at"`
	RefreshTokenExpiresAt time.Time `json:"refresh_token_expires_at"`
}

type SentryConfig struct {
	WebhookSecret string `json:"webhook_secret,omitempty"`
	AccessToken   string `json:"access_token,omitempty"`  // #nosec G117 -- JSON config field
	RefreshToken  string `json:"refresh_token,omitempty"` // #nosec G117 -- JSON config field
	TokenType     string `json:"token_type,omitempty"`
	OrgSlug       string `json:"org_slug,omitempty"`
	OrgName       string `json:"org_name,omitempty"`
}

type LinearConfig struct {
	WebhookSecret string `json:"webhook_secret,omitempty"`
	AccessToken   string `json:"access_token,omitempty"`  // #nosec G117 -- JSON config field
	RefreshToken  string `json:"refresh_token,omitempty"` // #nosec G117 -- JSON config field
	// ExpiresAt is the absolute expiry of AccessToken. Zero value means
	// "unknown TTL" — applies to legacy connections created before Linear's
	// refresh-token rollout, where we stored only an access token with no
	// expires_in. IsExpired / NeedsRefresh treat zero as "never
	// expires" so legacy rows continue to work until the user reconnects.
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	TokenType     string    `json:"token_type,omitempty"`
	Scope         string    `json:"scope,omitempty"`
	WorkspaceID   string    `json:"workspace_id,omitempty"`
	WorkspaceName string    `json:"workspace_name,omitempty"`

	// AppUserID is the id of the @143 agent user provisioned by Linear when
	// the OAuth flow was completed with actor=app. Empty for legacy installs
	// that completed before the agent flow shipped — the health probe flips
	// the integration into a "needs re-authorize" state when this is empty
	// after the cutover. Persisted because filtering inbound webhooks by
	// AppUserID lets us distinguish events targeted at our agent from
	// arbitrary issue activity.
	AppUserID string `json:"app_user_id,omitempty"`
	// AppUserName is the display-name handle Linear assigned the agent user
	// (typically "143"). Cosmetic; surfaced in admin UIs so operators can
	// confirm the install paired the right OAuth app.
	AppUserName string `json:"app_user_name,omitempty"`
	// AgentScopesGranted is true when the OAuth Scope string contains both
	// app:assignable and app:mentionable. Cached at install time so the
	// runtime hot path doesn't re-parse Scope on every webhook. The health
	// probe re-derives this on each pass so a token rotated through a
	// non-agent flow (e.g. legacy "connect Linear") flips this back to false
	// and surfaces a re-authorize banner.
	AgentScopesGranted bool `json:"agent_scopes_granted,omitempty"`
}

// IsExpired returns true if the access token has a known expiry that has
// already passed. Connections without expiry info (legacy rows where Linear
// did not return expires_in) report false so existing tokens keep working
// until they hit a real 401.
func (c LinearConfig) IsExpired() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(c.ExpiresAt)
}

// NeedsRefresh returns true if the access token has a known expiry within the
// given window. Connections with no expiry are legacy "use until 401" rows and
// do not proactively refresh. A known-expiring row without a refresh token
// still reports true so callers can force the reconnect path instead of
// returning a token they know is stale or about to go stale.
func (c LinearConfig) NeedsRefresh(window time.Duration) bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(window).After(c.ExpiresAt)
}

type SlackConfig struct {
	AccessToken string   `json:"access_token"` // #nosec G117 -- JSON config field
	TeamID      string   `json:"team_id"`
	TeamName    string   `json:"team_name"`
	BotUserID   string   `json:"bot_user_id,omitempty"`
	BotID       string   `json:"bot_id,omitempty"`
	Scope       string   `json:"scope"`
	ChannelIDs  []string `json:"channel_ids"`
}

type NotionConfig struct {
	AccessToken   string `json:"access_token"` // #nosec G117 -- JSON config field
	WorkspaceID   string `json:"workspace_id,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
}

// CircleCIConfig stores a CircleCI personal API token plus the VCS-prefixed
// project slug (e.g. "gh/org/repo") used for all flaky-test queries.
type CircleCIConfig struct {
	AuthToken   string `json:"auth_token"` // #nosec G117 -- JSON config field
	ProjectSlug string `json:"project_slug"`
}

// MezmoConfig stores a Mezmo service key plus optional base URL (for
// self-hosted/enterprise endpoints, defaults to https://api.mezmo.com). Dataset
// is retained only to parse older saved credentials; new connections reject it
// because Mezmo's documented v2 export API does not support dataset scoping.
// This is the persisted credential shape; the runtime provider config lives in
// internal/services/integration.MezmoConfig, mirroring the CircleCI split.
type MezmoConfig struct {
	APIKey  string `json:"api_key"` // #nosec G117 -- JSON config field
	BaseURL string `json:"base_url,omitempty"`
	Dataset string `json:"dataset,omitempty"`
}

// IsExpired returns true if the access token has expired.
func (c GitHubAppUserConfig) IsExpired() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(c.ExpiresAt)
}

// NeedsRefresh returns true if the access token will expire within the given window.
func (c GitHubAppUserConfig) NeedsRefresh(window time.Duration) bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(window).After(c.ExpiresAt)
}

// RefreshTokenExpired returns true if the refresh token is expired.
func (c GitHubAppUserConfig) RefreshTokenExpired() bool {
	return !c.RefreshTokenExpiresAt.IsZero() && time.Now().After(c.RefreshTokenExpiresAt)
}

// --- Provider() implementations ---

func (c AnthropicConfig) Provider() ProviderName     { return ProviderAnthropic }
func (c OpenAIConfig) Provider() ProviderName        { return ProviderOpenAI }
func (c GeminiConfig) Provider() ProviderName        { return ProviderGemini }
func (c AmpConfig) Provider() ProviderName           { return ProviderAmp }
func (c PiConfig) Provider() ProviderName            { return ProviderPi }
func (c OpenCodeConfig) Provider() ProviderName      { return ProviderOpenCode }
func (c OpenRouterConfig) Provider() ProviderName    { return ProviderOpenRouter }
func (c GitHubAppConfig) Provider() ProviderName     { return ProviderGitHubApp }
func (c GitHubAppUserConfig) Provider() ProviderName { return ProviderGitHubAppUser }
func (c GitHubOAuthConfig) Provider() ProviderName   { return ProviderGitHubOAuth }
func (c SentryConfig) Provider() ProviderName        { return ProviderSentry }
func (c LinearConfig) Provider() ProviderName        { return ProviderLinear }
func (c PagerDutyConfig) Provider() ProviderName     { return ProviderPagerDuty }
func (c CircleCIConfig) Provider() ProviderName      { return ProviderCircleCI }
func (c MezmoConfig) Provider() ProviderName         { return ProviderMezmo }
func (c SlackConfig) Provider() ProviderName         { return ProviderSlack }
func (c NotionConfig) Provider() ProviderName        { return ProviderNotion }

// --- Validate() implementations ---

func (c AnthropicConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("api_key is required")
	}
	return nil
}

func (c OpenAIConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("api_key is required")
	}
	return nil
}

func (c GeminiConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("api_key is required")
	}
	return nil
}

func (c AmpConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("api_key is required")
	}
	return nil
}

func (c PiConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("api_key is required")
	}
	return nil
}

func (c OpenCodeConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("api_key is required")
	}
	switch c.NormalizedBackingProvider() {
	case ProviderOpenCode, ProviderAnthropic, ProviderOpenAI, ProviderGemini, ProviderOpenRouter:
		if err := ValidateOpenCodeBackingProviderModel(c.NormalizedBackingProvider(), c.Model); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported opencode backing_provider: %q", c.BackingProvider)
	}
}

func ValidateOpenCodeBackingProviderModel(backing ProviderName, model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	expectedPrefix := openCodeModelPrefixForBackingProvider(backing)
	if expectedPrefix == "" {
		return nil
	}
	if strings.HasPrefix(model, expectedPrefix+"/") {
		return nil
	}
	return fmt.Errorf("opencode backing_provider %q cannot run model %q; expected model prefix %q", backing, model, expectedPrefix+"/")
}

func openCodeModelPrefixForBackingProvider(backing ProviderName) string {
	switch backing {
	case ProviderOpenCode:
		return "opencode"
	case ProviderAnthropic:
		return "anthropic"
	case ProviderOpenAI:
		return "openai"
	case ProviderGemini:
		return "google"
	default:
		return ""
	}
}

func (c OpenCodeConfig) NormalizedBackingProvider() ProviderName {
	if c.BackingProvider == "" {
		return ProviderOpenCode
	}
	return c.BackingProvider
}

func (c OpenRouterConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("api_key is required")
	}
	return nil
}

func (c GitHubAppConfig) Validate() error {
	if c.AppID == 0 {
		return errors.New("app_id is required")
	}
	if c.PrivateKey == "" {
		return errors.New("private_key is required")
	}
	return nil
}

func (c GitHubOAuthConfig) Validate() error {
	if c.AccessToken != "" {
		return nil
	}
	if c.ClientID == "" {
		return errors.New("client_id or access_token is required")
	}
	if c.ClientSecret == "" {
		return errors.New("client_secret is required")
	}
	return nil
}

func (c GitHubAppUserConfig) Validate() error {
	if c.AccessToken == "" {
		return errors.New("access_token is required")
	}
	return nil
}

func (c SentryConfig) Validate() error {
	if c.WebhookSecret == "" && c.AccessToken == "" {
		return errors.New("access_token or webhook_secret is required")
	}
	return nil
}

func (c LinearConfig) Validate() error {
	if c.WebhookSecret == "" && c.AccessToken == "" {
		return errors.New("access_token or webhook_secret is required")
	}
	return nil
}

// HasAgentScopes returns true when the OAuth Scope string contains the
// scopes required by the Linear agent feature (app:assignable AND
// app:mentionable). Used by the health probe to decide whether to surface
// a "re-authorize Linear (admin required)" banner. Computed from Scope
// rather than the cached AgentScopesGranted bit so a token rotated through
// the legacy `read,write` flow demotes correctly even if the cache was
// stale.
func (c LinearConfig) HasAgentScopes() bool {
	if c.Scope == "" {
		return false
	}
	// Linear stores scopes as a comma-separated list (the same shape the
	// OAuth params used). Splitting on both `,` and ` ` is defensive — the
	// spec allows both and Linear has historically used commas.
	scopes := splitScopeString(c.Scope)
	wantA, wantM := false, false
	for _, s := range scopes {
		switch s {
		case LinearScopeAppAssignable:
			wantA = true
		case LinearScopeAppMentionable:
			wantM = true
		}
	}
	return wantA && wantM
}

// splitScopeString splits an OAuth scope string on commas or spaces. Linear
// uses commas, but the OAuth spec leaves the separator implementation-defined
// and other providers use spaces; accepting both keeps parsing robust.
func splitScopeString(scope string) []string {
	return strings.FieldsFunc(scope, func(r rune) bool {
		return r == ',' || r == ' '
	})
}

func (c SlackConfig) Validate() error {
	if c.AccessToken == "" {
		return errors.New("access_token is required")
	}
	return nil
}

func (c NotionConfig) Validate() error {
	if c.AccessToken == "" {
		return errors.New("access_token is required")
	}
	return nil
}

func (c CircleCIConfig) Validate() error {
	if c.AuthToken == "" {
		return errors.New("auth_token is required")
	}
	if c.ProjectSlug == "" {
		return errors.New("project_slug is required (e.g. gh/org/repo)")
	}
	return nil
}

func (c MezmoConfig) Validate() error {
	if c.APIKey == "" {
		return errors.New("api_key is required")
	}
	return nil
}

// --- MaskedSummary() implementations ---

func (c AnthropicConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderAnthropic,
		Configured: true,
		MaskedKey:  MaskKey(c.APIKey),
	}
}

func (c OpenAIConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderOpenAI,
		Configured: true,
		MaskedKey:  MaskKey(c.APIKey),
		APIType:    c.APIType,
	}
}

func (c GeminiConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderGemini,
		Configured: true,
		MaskedKey:  MaskKey(c.APIKey),
	}
}

func (c AmpConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderAmp,
		Configured: true,
		MaskedKey:  MaskKey(c.APIKey),
	}
}

func (c PiConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderPi,
		Configured: true,
		MaskedKey:  MaskKey(c.APIKey),
	}
}

func (c OpenCodeConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderOpenCode,
		Configured: true,
		MaskedKey:  MaskKey(c.APIKey),
		APIType:    string(c.NormalizedBackingProvider()),
	}
}

func (c OpenRouterConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderOpenRouter,
		Configured: true,
		MaskedKey:  MaskKey(c.APIKey),
		AppName:    c.AppName,
	}
}

func (c GitHubAppConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderGitHubApp,
		Configured: true,
		AppID:      c.AppID,
	}
}

func (c GitHubAppUserConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderGitHubAppUser,
		Configured: true,
		MaskedKey:  MaskKey(c.AccessToken),
	}
}

func (c GitHubOAuthConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderGitHubOAuth,
		Configured: true,
		MaskedKey:  MaskKey(c.ClientID),
	}
}

func (c SentryConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderSentry,
		Configured: true,
	}
}

func (c LinearConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderLinear,
		Configured: true,
	}
}

func (c SlackConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderSlack,
		Configured: true,
	}
}

func (c NotionConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderNotion,
		Configured: true,
	}
}

func (c CircleCIConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderCircleCI,
		Configured: true,
		MaskedKey:  MaskKey(c.AuthToken),
	}
}

func (c MezmoConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:   ProviderMezmo,
		Configured: true,
		MaskedKey:  MaskKey(c.APIKey),
	}
}

// --- ParseProviderConfig ---

// ParseProviderConfig deserializes JSON into the correct strongly-typed config
// struct for the given provider.
func ParseProviderConfig(provider ProviderName, data []byte) (ProviderConfig, error) {
	switch provider {
	case ProviderAnthropic:
		var cfg AnthropicConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid anthropic config: %w", err)
		}
		return cfg, nil
	case ProviderOpenAI:
		var cfg OpenAIConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid openai config: %w", err)
		}
		if cfg.APIType == "" {
			cfg.APIType = "chat"
		}
		return cfg, nil
	case ProviderGemini:
		var cfg GeminiConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid gemini config: %w", err)
		}
		return cfg, nil
	case ProviderAmp:
		var cfg AmpConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid amp config: %w", err)
		}
		return cfg, nil
	case ProviderPi:
		var cfg PiConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid pi config: %w", err)
		}
		return cfg, nil
	case ProviderOpenCode:
		var cfg OpenCodeConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid opencode config: %w", err)
		}
		cfg.BackingProvider = cfg.NormalizedBackingProvider()
		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("invalid opencode config: %w", err)
		}
		return cfg, nil
	case ProviderOpenRouter:
		var cfg OpenRouterConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid openrouter config: %w", err)
		}
		return cfg, nil
	case ProviderGitHubApp:
		var cfg GitHubAppConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid github_app config: %w", err)
		}
		return cfg, nil
	case ProviderGitHubAppUser:
		var cfg GitHubAppUserConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid github_app_user config: %w", err)
		}
		return cfg, nil
	case ProviderGitHubOAuth:
		var cfg GitHubOAuthConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid github_oauth config: %w", err)
		}
		return cfg, nil
	case ProviderSentry:
		var cfg SentryConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid sentry config: %w", err)
		}
		return cfg, nil
	case ProviderLinear:
		var cfg LinearConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid linear config: %w", err)
		}
		return cfg, nil
	case ProviderPagerDuty:
		var cfg PagerDutyConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid pagerduty config: %w", err)
		}
		return cfg, nil
	case ProviderOpenAISubscription:
		var cfg OpenAISubscriptionConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid openai_subscription config: %w", err)
		}
		return cfg, nil
	case ProviderAnthropicSubscription:
		var cfg AnthropicSubscriptionConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid anthropic_subscription config: %w", err)
		}
		return cfg, nil
	case ProviderSlack:
		var cfg SlackConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse slack config: %w", err)
		}
		return cfg, nil
	case ProviderNotion:
		var cfg NotionConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse notion config: %w", err)
		}
		return cfg, nil
	case ProviderCircleCI:
		var cfg CircleCIConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse circleci config: %w", err)
		}
		return cfg, nil
	case ProviderMezmo:
		var cfg MezmoConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parse mezmo config: %w", err)
		}
		return cfg, nil
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

// --- DB models ---

type CredentialStatus string

const (
	CredentialStatusActive      CredentialStatus = "active"
	CredentialStatusDisabled    CredentialStatus = "disabled"
	CredentialStatusPendingAuth CredentialStatus = "pending_auth"
	CredentialStatusInvalid     CredentialStatus = "invalid"
)

func (s CredentialStatus) Validate() error {
	switch s {
	case CredentialStatusActive, CredentialStatusDisabled, CredentialStatusPendingAuth, CredentialStatusInvalid:
		return nil
	default:
		return fmt.Errorf("invalid CredentialStatus: %q", s)
	}
}

// OrgCredential is the DB row representation. Config is encrypted bytea.
type OrgCredential struct {
	ID             uuid.UUID        `db:"id"`
	OrgID          uuid.UUID        `db:"org_id"`
	Provider       ProviderName     `db:"provider"`
	Label          string           `db:"label"`
	Config         []byte           `db:"config"`
	Status         CredentialStatus `db:"status"`
	Priority       int              `db:"priority"`
	LastVerifiedAt *time.Time       `db:"last_verified_at"`
	LastUsedAt     *time.Time       `db:"last_used_at"`
	CreatedBy      *uuid.UUID       `db:"created_by"`
	CreatedAt      time.Time        `db:"created_at"`
	UpdatedAt      time.Time        `db:"updated_at"`
}

// DecryptedCredential pairs DB metadata with the strongly-typed, decrypted config.
type DecryptedCredential struct {
	ID             uuid.UUID        `json:"id"`
	OrgID          uuid.UUID        `json:"org_id"`
	Provider       ProviderName     `json:"provider"`
	Label          string           `json:"label,omitempty"`
	Config         ProviderConfig   `json:"-"`
	Status         CredentialStatus `json:"status"`
	Priority       int              `json:"priority"`
	LastVerifiedAt *time.Time       `json:"last_verified_at,omitempty"`
	LastUsedAt     *time.Time       `json:"last_used_at,omitempty"`
	CreatedBy      *uuid.UUID       `json:"created_by,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// UserCredential is the DB row representation for per-user credentials.
type UserCredential struct {
	ID             uuid.UUID        `db:"id"`
	UserID         uuid.UUID        `db:"user_id"`
	OrgID          uuid.UUID        `db:"org_id"`
	Provider       ProviderName     `db:"provider"`
	Config         []byte           `db:"config"`
	Status         CredentialStatus `db:"status"`
	LastVerifiedAt *time.Time       `db:"last_verified_at"`
	CreatedAt      time.Time        `db:"created_at"`
	UpdatedAt      time.Time        `db:"updated_at"`
}

// DecryptedUserCredential pairs DB metadata with the strongly-typed, decrypted config.
type DecryptedUserCredential struct {
	ID             uuid.UUID        `json:"id"`
	UserID         uuid.UUID        `json:"user_id"`
	OrgID          uuid.UUID        `json:"org_id"`
	Provider       ProviderName     `json:"provider"`
	Config         ProviderConfig   `json:"-"`
	Status         CredentialStatus `json:"status"`
	LastVerifiedAt *time.Time       `json:"last_verified_at,omitempty"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// --- API response types ---

// CredentialSummary is the API-safe representation. Never contains full keys.
type CredentialSummary struct {
	Provider       ProviderName     `json:"provider"`
	Configured     bool             `json:"configured"`
	Status         CredentialStatus `json:"status,omitempty"`
	MaskedKey      string           `json:"masked_key,omitempty"`
	LastVerifiedAt *time.Time       `json:"last_verified_at,omitempty"`

	// Provider-specific non-secret fields.
	APIType     string `json:"api_type,omitempty"`
	AppName     string `json:"app_name,omitempty"`
	AppID       int64  `json:"app_id,omitempty"`
	AccountType string `json:"account_type,omitempty"` // OpenAI ChatGPT account tier
}

// ResolvedCredential describes which credential source would be used for a provider.
type ResolvedCredential struct {
	Provider  ProviderName `json:"provider"`
	Source    string       `json:"source"` // "personal", "team_default", "org", "none"
	MaskedKey string       `json:"masked_key,omitempty"`
}

type CodingAuthType string

const (
	CodingAuthTypeSubscription CodingAuthType = "subscription"
	CodingAuthTypeAPIKey       CodingAuthType = "api_key"
)

func (t CodingAuthType) Validate() error {
	switch t {
	case CodingAuthTypeSubscription, CodingAuthTypeAPIKey:
		return nil
	default:
		return fmt.Errorf("unknown coding auth type: %s", t)
	}
}

type CodingAuthStatus string

const (
	CodingAuthStatusHealthy     CodingAuthStatus = "healthy"
	CodingAuthStatusRateLimited CodingAuthStatus = "rate_limited"
	CodingAuthStatusNeedsReauth CodingAuthStatus = "needs_reauth"
	CodingAuthStatusInvalid     CodingAuthStatus = "invalid"
)

func (s CodingAuthStatus) Validate() error {
	switch s {
	case CodingAuthStatusHealthy, CodingAuthStatusRateLimited, CodingAuthStatusNeedsReauth, CodingAuthStatusInvalid:
		return nil
	default:
		return fmt.Errorf("unknown coding auth status: %s", s)
	}
}

type CodingAuth struct {
	ID               uuid.UUID             `json:"id"`
	OrgID            uuid.UUID             `json:"org_id"`
	Priority         int                   `json:"priority"`
	Agent            AgentType             `json:"agent"`
	AuthType         CodingAuthType        `json:"auth_type"`
	Label            string                `json:"label"`
	Scope            CodingCredentialScope `json:"scope"`
	Provider         ProviderName          `json:"provider"`
	Status           CodingAuthStatus      `json:"status"`
	IsDefault        bool                  `json:"is_default"`
	LastVerifiedAt   *time.Time            `json:"last_verified_at,omitempty"`
	RateLimitedUntil *time.Time            `json:"rate_limited_until,omitempty"`
	RateLimitMessage *string               `json:"rate_limit_message,omitempty"`
	LastUsedAt       *time.Time            `json:"last_used_at,omitempty"`
	UsageNote        string                `json:"usage_note,omitempty"`
	CreatedBy        *uuid.UUID            `json:"created_by,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

type CreateCodingAuthInput struct {
	Agent         AgentType         `json:"agent"`
	AuthType      CodingAuthType    `json:"auth_type"`
	Label         string            `json:"label"`
	APIKey        string            `json:"api_key,omitempty"`
	APIType       string            `json:"api_type,omitempty"`
	BaseURL       string            `json:"base_url,omitempty"`
	AgentDefaults map[string]string `json:"agent_defaults,omitempty"`
}

func (i CreateCodingAuthInput) Validate() error {
	if err := i.Agent.Validate(); err != nil {
		return err
	}
	if err := i.AuthType.Validate(); err != nil {
		return err
	}
	if i.AuthType == CodingAuthTypeAPIKey && i.APIKey == "" {
		return errors.New("api_key is required for api_key auth")
	}
	if i.AuthType == CodingAuthTypeSubscription {
		return errors.New("subscription auth must be created through the provider-specific auth flow")
	}
	if len(i.AgentDefaults) > 0 {
		if i.Agent != AgentTypeAmp && i.Agent != AgentTypePi && i.Agent != AgentTypeOpenCode {
			return errors.New("agent_defaults are only supported for amp, pi, and opencode")
		}
		if err := ValidateSettingsModels(OrgSettings{
			AgentConfig: AgentEnvConfig{
				string(i.Agent): i.AgentDefaults,
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

type UpdateCodingAuthInput struct {
	Label *string `json:"label,omitempty"`
}

// CodingAgentProviders lists the providers that can store a coding-agent
// credential on the unified coding_credentials table. Every (agent, auth_type)
// pair maps to exactly one entry: the API-key flavor and the subscription
// flavor are distinct providers, never an optional embedded field on a shared
// struct. Adding a new subscription provider is one append here plus one
// ProviderConfig struct — no changes to stores, resolvers, or the generic UI.
var CodingAgentProviders = []ProviderName{
	ProviderAnthropic, ProviderAnthropicSubscription,
	ProviderOpenAI, ProviderOpenAISubscription,
	ProviderAmp, ProviderPi, ProviderOpenCode, ProviderOpenRouter,
}

// MaskKey preserves the first 6 and last 4 characters of a key.
// Keys with 12 or fewer characters are fully masked to avoid leaking most of the key.
func MaskKey(key string) string {
	if len(key) <= 12 {
		return "****"
	}
	prefix := key[:6]
	suffix := key[len(key)-4:]
	return prefix + "..." + suffix
}
