package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Scope identifies the owner of a coding credential. UserID == nil means
// org-scoped (visible to every user in the org as a fallback); UserID != nil
// means personal (visible only to that user, ahead of any org row).
//
// Scope is the parameter every CodingCredentialStore mutation takes. Even
// when the row id uniquely identifies the target, the store re-asserts in
// transaction that the loaded row's (org_id, user_id) matches the supplied
// Scope and returns ErrCodingCredentialNotFound on mismatch — this turns
// "forgot to scope-check" into a compile-time invariant rather than a
// code-review item, and prevents id enumeration across scopes.
type Scope struct {
	OrgID  uuid.UUID
	UserID *uuid.UUID
}

// IsPersonal reports whether the scope refers to a single user's stack.
func (s Scope) IsPersonal() bool { return s.UserID != nil }

// IsOrg reports whether the scope refers to the org-wide stack.
func (s Scope) IsOrg() bool { return s.UserID == nil }

// CodingCredentialScopeOrg is the JSON tag used in the API surface for
// org-scoped rows.
const CodingCredentialScopeOrg = "org"

// CodingCredentialScopePersonal is the JSON tag used in the API surface for
// user-scoped rows.
const CodingCredentialScopePersonal = "personal"

// ScopeLabel renders a Scope for telemetry and API responses.
func (s Scope) Label() string {
	if s.IsPersonal() {
		return CodingCredentialScopePersonal
	}
	return CodingCredentialScopeOrg
}

// CodingCredentialStatus enumerates the lifecycle of a row.
const (
	CodingCredentialStatusActive      = "active"
	CodingCredentialStatusDisabled    = "disabled"
	CodingCredentialStatusPendingAuth = "pending_auth"
	CodingCredentialStatusInvalid     = "invalid"
)

// CodingCredential is the DB row representation. Config is encrypted bytea.
type CodingCredential struct {
	ID             uuid.UUID    `db:"id"`
	OrgID          uuid.UUID    `db:"org_id"`
	UserID         *uuid.UUID   `db:"user_id"`
	Provider       ProviderName `db:"provider"`
	Label          string       `db:"label"`
	Config         []byte       `db:"config"`
	Priority       int          `db:"priority"`
	Status         string       `db:"status"`
	CreatedBy      *uuid.UUID   `db:"created_by"`
	LastVerifiedAt *time.Time   `db:"last_verified_at"`
	CreatedAt      time.Time    `db:"created_at"`
	UpdatedAt      time.Time    `db:"updated_at"`
}

// DecryptedCodingCredential pairs DB metadata with the strongly-typed,
// decrypted provider config. Returned by the store; never serialised
// directly — callers convert to CodingCredentialSummary before crossing
// the API boundary.
type DecryptedCodingCredential struct {
	ID             uuid.UUID      `json:"id"`
	OrgID          uuid.UUID      `json:"org_id"`
	UserID         *uuid.UUID     `json:"user_id,omitempty"`
	Provider       ProviderName   `json:"provider"`
	Label          string         `json:"label"`
	Config         ProviderConfig `json:"-"`
	Priority       int            `json:"priority"`
	Status         string         `json:"status"`
	CreatedBy      *uuid.UUID     `json:"created_by,omitempty"`
	LastVerifiedAt *time.Time     `json:"last_verified_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// Scope returns the Scope this credential belongs to.
func (c DecryptedCodingCredential) Scope() Scope {
	return Scope{OrgID: c.OrgID, UserID: c.UserID}
}

// CodingCredentialSummary is the API-safe representation. Mirrors the shape
// of CodingAuth (the existing org-only summary) but adds scope/user_id so
// the same row component can render personal and org stacks.
type CodingCredentialSummary struct {
	ID             uuid.UUID        `json:"id"`
	OrgID          uuid.UUID        `json:"org_id"`
	UserID         *uuid.UUID       `json:"user_id,omitempty"`
	Scope          string           `json:"scope"` // "org" | "personal"
	Priority       int              `json:"priority"`
	Agent          AgentType        `json:"agent"`
	AuthType       CodingAuthType   `json:"auth_type"`
	Provider       ProviderName     `json:"provider"`
	Label          string           `json:"label"`
	Status         CodingAuthStatus `json:"status"`
	IsDefault      bool             `json:"is_default"` // first runnable in this scope's stack
	UsageNote      string           `json:"usage_note,omitempty"`
	LastVerifiedAt *time.Time       `json:"last_verified_at,omitempty"`
	CreatedBy      *uuid.UUID       `json:"created_by,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// CreateCodingCredentialInput is the API body for POST /coding-credentials
// when creating an API-key credential. Subscription credentials are created
// through the provider-specific OAuth flow endpoints.
type CreateCodingCredentialInput struct {
	Scope         string            `json:"scope"` // "org" | "personal"
	Agent         AgentType         `json:"agent"`
	AuthType      CodingAuthType    `json:"auth_type"`
	Label         string            `json:"label"`
	APIKey        string            `json:"api_key,omitempty"`
	APIType       string            `json:"api_type,omitempty"`
	BaseURL       string            `json:"base_url,omitempty"`
	AgentDefaults map[string]string `json:"agent_defaults,omitempty"`
}

// Validate enforces the same shape rules as CreateCodingAuthInput plus a
// scope check.
func (i CreateCodingCredentialInput) Validate() error {
	switch i.Scope {
	case CodingCredentialScopeOrg, CodingCredentialScopePersonal:
	default:
		return fmt.Errorf("invalid scope: %q", i.Scope)
	}
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
		if i.Agent != AgentTypeAmp && i.Agent != AgentTypePi {
			return errors.New("agent_defaults are only supported for amp and pi")
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

// UpdateCodingCredentialInput is the API body for PATCH /coding-credentials/{id}.
type UpdateCodingCredentialInput struct {
	Scope  string  `json:"scope"`
	Label  *string `json:"label,omitempty"`
	Status *string `json:"status,omitempty"`
}

// MoveCodingCredentialInput is the body for PATCH /coding-credentials/{id}/move.
// Exactly one of BeforeID, AfterID, ToTop, or ToBottom must be set.
type MoveCodingCredentialInput struct {
	Scope    string     `json:"scope"`
	BeforeID *uuid.UUID `json:"before_id,omitempty"`
	AfterID  *uuid.UUID `json:"after_id,omitempty"`
	ToTop    bool       `json:"to_top,omitempty"`
	ToBottom bool       `json:"to_bottom,omitempty"`
}

// Validate enforces "exactly one" cardinality.
func (m MoveCodingCredentialInput) Validate() error {
	count := 0
	if m.BeforeID != nil {
		count++
	}
	if m.AfterID != nil {
		count++
	}
	if m.ToTop {
		count++
	}
	if m.ToBottom {
		count++
	}
	if count != 1 {
		return errors.New("exactly one of before_id, after_id, to_top, or to_bottom must be set")
	}
	return nil
}

// ReorderCodingCredentialsInput is the body for PATCH /coding-credentials/reorder.
type ReorderCodingCredentialsInput struct {
	Scope      string      `json:"scope"`
	OrderedIDs []uuid.UUID `json:"ordered_ids"`
}

// AnthropicSubscriptionConfig holds Claude Code OAuth tokens.
//
// Extracted from AnthropicConfig.Subscription as part of the unified-credentials
// redesign. Provider name is ProviderAnthropicSubscription. Fields and lifecycle
// match AnthropicSubscription exactly so the on-disk and over-the-wire JSON
// shapes are identical to the legacy nested form — making the encrypted-blob
// migration a pure rename of the surrounding wrapper.
type AnthropicSubscriptionConfig struct {
	AccessToken   string    `json:"access_token"`  // #nosec G117 -- JSON config field
	RefreshToken  string    `json:"refresh_token"` // #nosec G117 -- JSON config field
	ExpiresAt     time.Time `json:"expires_at"`
	AccountType   string    `json:"account_type,omitempty"`
	RateLimitTier string    `json:"rate_limit_tier,omitempty"`
	Scopes        []string  `json:"scopes,omitempty"`

	// Pending PKCE-auth fields — only populated between InitiateOAuth and
	// CompleteOAuth. Persisted so the flow survives server restarts.
	State        string `json:"state,omitempty"`
	CodeVerifier string `json:"code_verifier,omitempty"` // #nosec G117 -- JSON config field
	AuthorizeURL string `json:"authorize_url,omitempty"`
}

// IsExpired returns true if the access token has passed its expiry.
func (c AnthropicSubscriptionConfig) IsExpired() bool {
	return time.Now().After(c.ExpiresAt)
}

// NeedsRefresh returns true if the access token will expire within window.
func (c AnthropicSubscriptionConfig) NeedsRefresh(window time.Duration) bool {
	return time.Now().Add(window).After(c.ExpiresAt)
}

// Provider returns ProviderAnthropicSubscription.
func (c AnthropicSubscriptionConfig) Provider() ProviderName {
	return ProviderAnthropicSubscription
}

// Validate ensures the row carries either active OAuth tokens or a pending
// PKCE handshake — never both empty, never both populated incompletely.
func (c AnthropicSubscriptionConfig) Validate() error {
	hasTokens := c.AccessToken != "" && c.RefreshToken != ""
	hasPending := c.State != "" && c.CodeVerifier != ""
	if hasTokens || hasPending {
		return nil
	}
	return errors.New("subscription requires either (access_token + refresh_token) or (state + code_verifier) for a pending auth")
}

// MaskedSummary returns API-safe metadata. The access token tail is *not*
// surfaced because on a JWT it is part of the HMAC signature.
func (c AnthropicSubscriptionConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:    ProviderAnthropicSubscription,
		Configured:  true,
		AccountType: c.AccountType,
	}
}

// OpenAISubscriptionConfig holds Codex subscription OAuth tokens.
//
// Renamed from OpenAIChatGPTConfig. The on-disk JSON shape is identical to
// the legacy form — only Provider() differs (returns ProviderOpenAISubscription
// instead of ProviderOpenAIChatGPT). The data migration rewrites the surrounding
// row's provider column; the encrypted bytea content is unchanged.
type OpenAISubscriptionConfig struct {
	AccessToken  string    `json:"access_token"`       // #nosec G117 -- JSON config field
	RefreshToken string    `json:"refresh_token"`      // #nosec G117 -- JSON config field
	IDToken      string    `json:"id_token,omitempty"` // OIDC id_token from OAuth exchange
	ExpiresAt    time.Time `json:"expires_at"`
	AccountType  string    `json:"account_type"` // "plus", "pro", "team", "enterprise"

	// Pending device auth fields — only populated during the device code flow.
	DeviceAuthID    string `json:"device_auth_id,omitempty"`
	UserCode        string `json:"user_code,omitempty"`
	VerificationURI string `json:"verification_uri,omitempty"`
	PollInterval    int    `json:"poll_interval,omitempty"`
}

// IsExpired returns true if the access token has expired.
func (c OpenAISubscriptionConfig) IsExpired() bool {
	return time.Now().After(c.ExpiresAt)
}

// NeedsRefresh returns true if the access token will expire within window.
func (c OpenAISubscriptionConfig) NeedsRefresh(window time.Duration) bool {
	return time.Now().Add(window).After(c.ExpiresAt)
}

// Provider returns ProviderOpenAISubscription.
func (c OpenAISubscriptionConfig) Provider() ProviderName {
	return ProviderOpenAISubscription
}

// Validate enforces the same rules as OpenAIChatGPTConfig.Validate.
func (c OpenAISubscriptionConfig) Validate() error {
	if c.AccessToken == "" {
		return errors.New("access_token is required")
	}
	if c.RefreshToken == "" {
		return errors.New("refresh_token is required")
	}
	return nil
}

// MaskedSummary returns API-safe metadata.
func (c OpenAISubscriptionConfig) MaskedSummary() CredentialSummary {
	return CredentialSummary{
		Provider:    ProviderOpenAISubscription,
		Configured:  true,
		MaskedKey:   MaskKey(c.AccessToken),
		AccountType: c.AccountType,
	}
}

// AsOpenAIChatGPTConfig is a backwards-compat helper for code paths that
// still expect the legacy struct shape. Removed in the cleanup PR.
func (c OpenAISubscriptionConfig) AsOpenAIChatGPTConfig() OpenAIChatGPTConfig {
	return OpenAIChatGPTConfig{
		AccessToken:     c.AccessToken,
		RefreshToken:    c.RefreshToken,
		IDToken:         c.IDToken,
		ExpiresAt:       c.ExpiresAt,
		AccountType:     c.AccountType,
		DeviceAuthID:    c.DeviceAuthID,
		UserCode:        c.UserCode,
		VerificationURI: c.VerificationURI,
		PollInterval:    c.PollInterval,
	}
}

// FromOpenAIChatGPTConfig is the inverse of AsOpenAIChatGPTConfig.
func FromOpenAIChatGPTConfig(c OpenAIChatGPTConfig) OpenAISubscriptionConfig {
	return OpenAISubscriptionConfig{
		AccessToken:     c.AccessToken,
		RefreshToken:    c.RefreshToken,
		IDToken:         c.IDToken,
		ExpiresAt:       c.ExpiresAt,
		AccountType:     c.AccountType,
		DeviceAuthID:    c.DeviceAuthID,
		UserCode:        c.UserCode,
		VerificationURI: c.VerificationURI,
		PollInterval:    c.PollInterval,
	}
}

// FromAnthropicSubscription extracts an AnthropicSubscriptionConfig from the
// legacy AnthropicConfig.Subscription nested struct. Used by the Anthropic
// split post-step migration.
func FromAnthropicSubscription(s AnthropicSubscription) AnthropicSubscriptionConfig {
	return AnthropicSubscriptionConfig{
		AccessToken:   s.AccessToken,
		RefreshToken:  s.RefreshToken,
		ExpiresAt:     s.ExpiresAt,
		AccountType:   s.AccountType,
		RateLimitTier: s.RateLimitTier,
		Scopes:        s.Scopes,
		State:         s.State,
		CodeVerifier:  s.CodeVerifier,
		AuthorizeURL:  s.AuthorizeURL,
	}
}

// ParseCodingProviderConfig is the strict variant of ParseProviderConfig that
// accepts only the providers the unified coding_credentials table stores.
// Returns an error for non-coding-agent providers (GitHub/Sentry/Linear/etc.)
// — those still live in org_credentials and are read through the legacy path.
func ParseCodingProviderConfig(provider ProviderName, data []byte) (ProviderConfig, error) {
	switch provider {
	case ProviderAnthropicSubscription:
		var cfg AnthropicSubscriptionConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid anthropic_subscription config: %w", err)
		}
		return cfg, nil
	case ProviderOpenAISubscription:
		var cfg OpenAISubscriptionConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("invalid openai_subscription config: %w", err)
		}
		return cfg, nil
	}
	return ParseProviderConfig(provider, data)
}
