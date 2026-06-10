package models

import (
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

type CodingCredentialScope string

// CodingCredentialScopeOrg is the JSON tag used in the API surface for
// org-scoped rows.
const CodingCredentialScopeOrg CodingCredentialScope = "org"

// CodingCredentialScopePersonal is the JSON tag used in the API surface for
// user-scoped rows.
const CodingCredentialScopePersonal CodingCredentialScope = "personal"

func (s CodingCredentialScope) Validate() error {
	switch s {
	case CodingCredentialScopeOrg, CodingCredentialScopePersonal:
		return nil
	default:
		return fmt.Errorf("invalid scope: %q", s)
	}
}

// Label renders a Scope for telemetry and API responses.
func (s Scope) Label() CodingCredentialScope {
	if s.IsPersonal() {
		return CodingCredentialScopePersonal
	}
	return CodingCredentialScopeOrg
}

type CodingCredentialRowStatus string

// CodingCredentialStatus enumerates the lifecycle of a row.
const (
	CodingCredentialStatusActive      CodingCredentialRowStatus = "active"
	CodingCredentialStatusDisabled    CodingCredentialRowStatus = "disabled"
	CodingCredentialStatusPendingAuth CodingCredentialRowStatus = "pending_auth"
	CodingCredentialStatusInvalid     CodingCredentialRowStatus = "invalid"
)

const (
	CodingCredentialRowStatusActive      = CodingCredentialStatusActive
	CodingCredentialRowStatusDisabled    = CodingCredentialStatusDisabled
	CodingCredentialRowStatusPendingAuth = CodingCredentialStatusPendingAuth
	CodingCredentialRowStatusInvalid     = CodingCredentialStatusInvalid
)

func (s CodingCredentialRowStatus) Validate() error {
	switch s {
	case CodingCredentialStatusActive, CodingCredentialStatusDisabled, CodingCredentialStatusPendingAuth, CodingCredentialStatusInvalid:
		return nil
	default:
		return fmt.Errorf("invalid CodingCredentialRowStatus: %q", s)
	}
}

// CodingCredentialLabelMax bounds Label inputs at every API surface that
// writes to coding_credentials. Matches the Codex/Claude subscription label
// caps (handlers/codex_auth.go, handlers/claude_code_auth.go) so a
// subscription completed via the OAuth flow and an API key created via the
// unified endpoint share the same upper bound.
const CodingCredentialLabelMax = 100

// CodingCredentialRateLimit carries a temporary upstream rate-limit marker
// for a credential. Until is the time the credential may be picked again.
type CodingCredentialRateLimit struct {
	Until   time.Time
	Message string
}

// CodingCredential is the DB row representation. Config is encrypted bytea.
type CodingCredential struct {
	ID                      uuid.UUID                 `db:"id"`
	VersionID               uuid.UUID                 `db:"version_id"`
	OrgID                   uuid.UUID                 `db:"org_id"`
	UserID                  *uuid.UUID                `db:"user_id"`
	Provider                ProviderName              `db:"provider"`
	Label                   string                    `db:"label"`
	Config                  []byte                    `db:"config"`
	Priority                int                       `db:"priority"`
	Status                  CodingCredentialRowStatus `db:"status"`
	CreatedBy               *uuid.UUID                `db:"created_by"`
	LastVerifiedAt          *time.Time                `db:"last_verified_at"`
	RateLimitedUntil        *time.Time                `db:"rate_limited_until"`
	RateLimitedObservedAt   *time.Time                `db:"rate_limited_observed_at"`
	RateLimitMessage        *string                   `db:"rate_limit_message"`
	TeamDefaultOriginUserID *uuid.UUID                `db:"team_default_origin_user_id"`
	Active                  bool                      `db:"active"`
	CreatedAt               time.Time                 `db:"created_at"`
	UpdatedAt               time.Time                 `db:"updated_at"`
}

// DecryptedCodingCredential pairs DB metadata with the strongly-typed,
// decrypted provider config. Returned by the store; never serialised
// directly — callers convert to CodingCredentialSummary before crossing
// the API boundary.
type DecryptedCodingCredential struct {
	ID uuid.UUID `json:"id"`
	// VersionID identifies the physical config-version row backing this
	// read. Internal until a credential-history API exposes versions
	// deliberately; omitempty is a no-op on a [16]byte array, so the field
	// is excluded outright rather than always serialising.
	VersionID             uuid.UUID                 `json:"-"`
	OrgID                 uuid.UUID                 `json:"org_id"`
	UserID                *uuid.UUID                `json:"user_id,omitempty"`
	Provider              ProviderName              `json:"provider"`
	Label                 string                    `json:"label"`
	Config                ProviderConfig            `json:"-"`
	Priority              int                       `json:"priority"`
	Status                CodingCredentialRowStatus `json:"status"`
	CreatedBy             *uuid.UUID                `json:"created_by,omitempty"`
	LastVerifiedAt        *time.Time                `json:"last_verified_at,omitempty"`
	RateLimitedUntil      *time.Time                `json:"rate_limited_until,omitempty"`
	RateLimitedObservedAt *time.Time                `json:"rate_limited_observed_at,omitempty"`
	RateLimitMessage      *string                   `json:"rate_limit_message,omitempty"`
	CreatedAt             time.Time                 `json:"created_at"`
	UpdatedAt             time.Time                 `json:"updated_at"`
}

// Scope returns the Scope this credential belongs to.
func (c DecryptedCodingCredential) Scope() Scope {
	return Scope{OrgID: c.OrgID, UserID: c.UserID}
}

// CodingCredentialSummary is the API-safe representation. Mirrors the shape
// of CodingAuth (the existing org-only summary) but adds scope/user_id so
// the same row component can render personal and org stacks.
type CodingCredentialSummary struct {
	ID               uuid.UUID             `json:"id"`
	OrgID            uuid.UUID             `json:"org_id"`
	UserID           *uuid.UUID            `json:"user_id,omitempty"`
	Scope            CodingCredentialScope `json:"scope"` // "org" | "personal"
	Priority         int                   `json:"priority"`
	Agent            AgentType             `json:"agent"`
	AuthType         CodingAuthType        `json:"auth_type"`
	Provider         ProviderName          `json:"provider"`
	Label            string                `json:"label"`
	Status           CodingAuthStatus      `json:"status"`
	IsDefault        bool                  `json:"is_default"` // first runnable in this scope's stack
	UsageNote        string                `json:"usage_note,omitempty"`
	LastVerifiedAt   *time.Time            `json:"last_verified_at,omitempty"`
	RateLimitedUntil *time.Time            `json:"rate_limited_until,omitempty"`
	RateLimitMessage *string               `json:"rate_limit_message,omitempty"`
	CreatedBy        *uuid.UUID            `json:"created_by,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

// CreateCodingCredentialInput is the API body for POST /coding-credentials
// when creating an API-key credential. Subscription credentials are created
// through the provider-specific OAuth flow endpoints.
type CreateCodingCredentialInput struct {
	Scope         CodingCredentialScope `json:"scope"` // "org" | "personal"
	Agent         AgentType             `json:"agent"`
	AuthType      CodingAuthType        `json:"auth_type"`
	Label         string                `json:"label"`
	APIKey        string                `json:"api_key,omitempty"`
	APIType       string                `json:"api_type,omitempty"`
	BaseURL       string                `json:"base_url,omitempty"`
	AgentDefaults map[string]string     `json:"agent_defaults,omitempty"`
}

// Validate enforces the same shape rules as CreateCodingAuthInput plus a
// scope check.
func (i CreateCodingCredentialInput) Validate() error {
	if err := i.Scope.Validate(); err != nil {
		return err
	}
	if err := i.Agent.Validate(); err != nil {
		return err
	}
	if err := i.AuthType.Validate(); err != nil {
		return err
	}
	if len(i.Label) > CodingCredentialLabelMax {
		return fmt.Errorf("label must be %d characters or fewer", CodingCredentialLabelMax)
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
	Scope  CodingCredentialScope      `json:"scope"`
	Label  *string                    `json:"label,omitempty"`
	Status *CodingCredentialRowStatus `json:"status,omitempty"`
}

// Validate enforces the label length bound when one is provided. Scope and
// status are checked at the handler layer (resolveScope, isAllowedHandlerStatus)
// so we only re-assert the bounded fields here.
func (i UpdateCodingCredentialInput) Validate() error {
	if i.Label != nil && len(*i.Label) > CodingCredentialLabelMax {
		return fmt.Errorf("label must be %d characters or fewer", CodingCredentialLabelMax)
	}
	return nil
}

// MoveCodingCredentialInput is the body for PATCH /coding-credentials/{id}/move.
// Exactly one of BeforeID, AfterID, ToTop, or ToBottom must be set.
type MoveCodingCredentialInput struct {
	Scope    CodingCredentialScope `json:"scope"`
	BeforeID *uuid.UUID            `json:"before_id,omitempty"`
	AfterID  *uuid.UUID            `json:"after_id,omitempty"`
	ToTop    bool                  `json:"to_top,omitempty"`
	ToBottom bool                  `json:"to_bottom,omitempty"`
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
	Scope      CodingCredentialScope `json:"scope"`
	OrderedIDs []uuid.UUID           `json:"ordered_ids"`
}

// Validate rejects empty / duplicate / nil ids before the request reaches the
// store. The store's per-row UPDATE would survive an empty slice (no-op) but
// duplicate ids produce inconsistent priorities — the second occurrence wins
// at SET while later siblings get rewritten to indices that overlap with
// already-applied UPDATEs. Catching the malformed shape here keeps the store
// a single authoritative caller-side guard.
func (i ReorderCodingCredentialsInput) Validate() error {
	if err := i.Scope.Validate(); err != nil {
		return err
	}
	if len(i.OrderedIDs) == 0 {
		return errors.New("ordered_ids must contain at least one id")
	}
	seen := make(map[uuid.UUID]struct{}, len(i.OrderedIDs))
	for _, id := range i.OrderedIDs {
		if id == uuid.Nil {
			return errors.New("ordered_ids contains an empty id")
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("ordered_ids contains a duplicate id %s", id)
		}
		seen[id] = struct{}{}
	}
	return nil
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
	return OpenAIChatGPTConfig(c)
}

// FromOpenAIChatGPTConfig is the inverse of AsOpenAIChatGPTConfig.
func FromOpenAIChatGPTConfig(c OpenAIChatGPTConfig) OpenAISubscriptionConfig {
	return OpenAISubscriptionConfig(c)
}

// FromAnthropicSubscription extracts an AnthropicSubscriptionConfig from the
// legacy AnthropicConfig.Subscription nested struct. Used by the Anthropic
// split post-step migration.
func FromAnthropicSubscription(s AnthropicSubscription) AnthropicSubscriptionConfig {
	return AnthropicSubscriptionConfig(s)
}

// ParseCodingProviderConfig is the strict variant of ParseProviderConfig that
// accepts only the providers the unified coding_credentials table stores.
// Returns an error for non-coding-agent providers (GitHub/Sentry/Linear/etc.)
// — those still live in org_credentials and are read through the legacy path.
//
// The allowlist is the load-bearing part: the unified table's CHECK constraint
// is on `status`, not `provider`, so an out-of-band INSERT under a stray
// provider name (e.g. 'sentry') would otherwise round-trip silently through
// decryptRow → ParseProviderConfig → typed config. Gating reads to the
// known coding-provider set turns that into a typed error instead.
//
// ProviderOpenAIChatGPT is intentionally excluded: the SQL data-copy migration
// renames it to ProviderOpenAISubscription on insert, so coding_credentials
// rows must never carry the legacy spelling.
func ParseCodingProviderConfig(provider ProviderName, data []byte) (ProviderConfig, error) {
	switch provider {
	case ProviderAnthropic,
		ProviderAnthropicSubscription,
		ProviderOpenAI,
		ProviderOpenAISubscription,
		ProviderGemini,
		ProviderAmp,
		ProviderPi,
		ProviderOpenRouter:
		return ParseProviderConfig(provider, data)
	}
	return nil, fmt.Errorf("provider %q is not a coding-credential provider", provider)
}
