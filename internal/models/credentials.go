package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ProviderName is a string enum for credential providers.
type ProviderName string

const (
	ProviderAnthropic     ProviderName = "anthropic"
	ProviderOpenAI        ProviderName = "openai"
	ProviderGemini        ProviderName = "gemini"
	ProviderOpenAIChatGPT ProviderName = "openai_chatgpt"
	ProviderOpenRouter    ProviderName = "openrouter"
	ProviderGitHubApp     ProviderName = "github_app"
	ProviderGitHubOAuth   ProviderName = "github_oauth"
	ProviderSentry        ProviderName = "sentry"
	ProviderLinear        ProviderName = "linear"
)

// AllProviders is the canonical list of credential providers.
var AllProviders = []ProviderName{
	ProviderAnthropic, ProviderOpenAI, ProviderGemini, ProviderOpenAIChatGPT, ProviderOpenRouter,
	ProviderGitHubApp, ProviderGitHubOAuth,
	ProviderSentry, ProviderLinear,
}

// LLMProviders is the subset of providers that serve LLM completions.
var LLMProviders = []ProviderName{
	ProviderAnthropic, ProviderOpenAI, ProviderOpenRouter,
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
	APIKey  string `json:"api_key"` // #nosec G117 -- JSON config field
	BaseURL string `json:"base_url,omitempty"`
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
	ClientSecret string `json:"client_secret"` // #nosec G117 -- JSON config field
}

type SentryConfig struct {
	WebhookSecret string `json:"webhook_secret"`
}

type LinearConfig struct {
	WebhookSecret string `json:"webhook_secret"`
}

type OpenAIChatGPTConfig struct {
	AccessToken  string    `json:"access_token"`  // #nosec G117 -- JSON config field
	RefreshToken string    `json:"refresh_token"` // #nosec G117 -- JSON config field
	ExpiresAt    time.Time `json:"expires_at"`
	AccountType  string    `json:"account_type"` // "plus", "pro", "team", "enterprise"

	// Pending device auth fields — only populated during the device code flow.
	// Stored encrypted so the pending state survives server restarts.
	DeviceAuthID    string `json:"device_auth_id,omitempty"`
	UserCode        string `json:"user_code,omitempty"`
	VerificationURI string `json:"verification_uri,omitempty"`
	PollInterval    int    `json:"poll_interval,omitempty"`
}

// IsExpired returns true if the access token has expired.
func (c OpenAIChatGPTConfig) IsExpired() bool {
	return time.Now().After(c.ExpiresAt)
}

// NeedsRefresh returns true if the access token will expire within the given window.
func (c OpenAIChatGPTConfig) NeedsRefresh(window time.Duration) bool {
	return time.Now().Add(window).After(c.ExpiresAt)
}

// --- Provider() implementations ---

func (c AnthropicConfig) Provider() ProviderName     { return ProviderAnthropic }
func (c OpenAIConfig) Provider() ProviderName        { return ProviderOpenAI }
func (c GeminiConfig) Provider() ProviderName        { return ProviderGemini }
func (c OpenRouterConfig) Provider() ProviderName    { return ProviderOpenRouter }
func (c GitHubAppConfig) Provider() ProviderName     { return ProviderGitHubApp }
func (c GitHubOAuthConfig) Provider() ProviderName   { return ProviderGitHubOAuth }
func (c SentryConfig) Provider() ProviderName        { return ProviderSentry }
func (c LinearConfig) Provider() ProviderName        { return ProviderLinear }
func (c OpenAIChatGPTConfig) Provider() ProviderName { return ProviderOpenAIChatGPT }

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
	if c.ClientID == "" {
		return errors.New("client_id is required")
	}
	if c.ClientSecret == "" {
		return errors.New("client_secret is required")
	}
	return nil
}

func (c SentryConfig) Validate() error {
	if c.WebhookSecret == "" {
		return errors.New("webhook_secret is required")
	}
	return nil
}

func (c LinearConfig) Validate() error {
	if c.WebhookSecret == "" {
		return errors.New("webhook_secret is required")
	}
	return nil
}

func (c OpenAIChatGPTConfig) Validate() error {
	if c.AccessToken == "" {
		return errors.New("access_token is required")
	}
	if c.RefreshToken == "" {
		return errors.New("refresh_token is required")
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

func (c OpenAIChatGPTConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:    ProviderOpenAIChatGPT,
		Configured:  true,
		MaskedKey:   MaskKey(c.AccessToken),
		AccountType: c.AccountType,
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
	case ProviderOpenAIChatGPT:
		var cfg OpenAIChatGPTConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid openai_chatgpt config: %w", err)
		}
		return cfg, nil
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

// --- DB models ---

// OrgCredential is the DB row representation. Config is encrypted bytea.
type OrgCredential struct {
	ID             uuid.UUID    `db:"id"`
	OrgID          uuid.UUID    `db:"org_id"`
	Provider       ProviderName `db:"provider"`
	Config         []byte       `db:"config"`
	Status         string       `db:"status"`
	LastVerifiedAt *time.Time   `db:"last_verified_at"`
	CreatedAt      time.Time    `db:"created_at"`
	UpdatedAt      time.Time    `db:"updated_at"`
}

// DecryptedCredential pairs DB metadata with the strongly-typed, decrypted config.
type DecryptedCredential struct {
	ID             uuid.UUID      `json:"id"`
	OrgID          uuid.UUID      `json:"org_id"`
	Provider       ProviderName   `json:"provider"`
	Config         ProviderConfig `json:"-"`
	Status         string         `json:"status"`
	LastVerifiedAt *time.Time     `json:"last_verified_at,omitempty"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// --- API response types ---

// CredentialSummary is the API-safe representation. Never contains full keys.
type CredentialSummary struct {
	Provider       ProviderName `json:"provider"`
	Configured     bool         `json:"configured"`
	Status         string       `json:"status,omitempty"`
	MaskedKey      string       `json:"masked_key,omitempty"`
	LastVerifiedAt *time.Time   `json:"last_verified_at,omitempty"`

	// Provider-specific non-secret fields.
	APIType     string `json:"api_type,omitempty"`
	AppName     string `json:"app_name,omitempty"`
	AppID       int64  `json:"app_id,omitempty"`
	AccountType string `json:"account_type,omitempty"` // OpenAI ChatGPT account tier
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
