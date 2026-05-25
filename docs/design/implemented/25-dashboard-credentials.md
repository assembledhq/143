# 25 — Dashboard Credential Management

> **Status:** Implemented | **Last reviewed:** 2026-03-25

**Status**: proposed
**Depends on**: 01-database-schema, 20-security-architecture

## Problem

API keys (LLM providers, GitHub App, Sentry, Linear) are currently configured via environment variables. This means:

- Every key change requires a redeploy or server restart.
- Multi-org setups are impossible — all orgs share one set of keys.
- New users must edit `.env` files or set up SOPS before they can try the product.
- There's no visibility into which keys are configured or broken.

## Goal

All API keys are configured through the dashboard settings page, stored per-org in the database, encrypted at rest. No env vars needed for API keys (env vars are still used for infrastructure config like `DATABASE_URL`, `PORT`, `SESSION_SECRET`, `ENCRYPTION_MASTER_KEY`).

## What stays as env vars

These are infrastructure concerns that exist before the app boots — they can't live in the database:

| Variable | Why |
|----------|-----|
| `DATABASE_URL` | Needed to connect to DB in the first place |
| `PORT` | Needed before HTTP server starts |
| `LOG_LEVEL` | Needed before first log line |
| `SESSION_SECRET` | Needed for cookie signing before any request |
| `ENCRYPTION_MASTER_KEY` | Needed to decrypt DB-stored credentials |
| `MODE` | Controls whether worker/server starts |
| `BASE_URL`, `FRONTEND_URL` | Used in OAuth redirects, webhook URLs |
| `CORS_ALLOWED_ORIGINS` | Needed before first request |

## What moves to the dashboard

| Category | Keys | Current location |
|----------|------|-----------------|
| **LLM** | `LLM_MODEL`, `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `OPENAI_API_TYPE`, `OPENROUTER_API_KEY` + base URLs | env vars |
| **GitHub App** | `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY`, `GITHUB_WEBHOOK_SECRET` | env vars |
| **GitHub OAuth** | `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET` | env vars |
| **Sentry** | `SENTRY_WEBHOOK_SECRET` | env vars |
| **Linear** | `LINEAR_WEBHOOK_SECRET` | env vars |

## Database Design

### New table: `org_credentials`

Separate from `org_settings` (which is the `organizations.settings` JSONB column) because credentials have different access patterns: they're encrypted, never returned in full to the frontend, and need distinct RBAC.

```sql
CREATE TABLE org_credentials (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      uuid        NOT NULL REFERENCES organizations(id),
    provider    text        NOT NULL,  -- 'anthropic', 'openai', 'openrouter', 'github_app', 'github_oauth', 'sentry', 'linear'
    config      bytea       NOT NULL,  -- AES-256-GCM encrypted JSON blob
    status      text        NOT NULL DEFAULT 'active',  -- 'active', 'invalid', 'disabled'
    last_verified_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, provider)
);

CREATE INDEX idx_org_credentials_org_id ON org_credentials(org_id);
```

**Why `bytea` instead of JSONB?** The entire config blob is encrypted — there's no queryable structure. Using `bytea` makes it clear this is opaque encrypted data, not something you can `->>'key'` into. This differs from the existing `integrations.config` column (JSONB) which we should eventually migrate to encrypted `bytea` as well.

**Why a separate table instead of using `integrations`?** The `integrations` table represents external service connections that are created during onboarding flows (GitHub App install, Sentry project linking). Credentials are simpler — they're just API keys that the user pastes in. Mixing them would complicate the integration lifecycle (status, last_synced_at, installation_id queries).

## Go type system

Every layer — DB storage, encryption, API requests, API responses — uses strongly-typed Go structs. No `json.RawMessage` for credential data. The type system enforces correctness at each boundary.

### Provider config types (`internal/models/credentials.go`)

Each provider has its own Go struct. These are the plaintext types that get serialized to JSON before encryption.

```go
package models

// ProviderName is a string enum for credential providers.
type ProviderName string

const (
    ProviderAnthropic   ProviderName = "anthropic"
    ProviderOpenAI      ProviderName = "openai"
    ProviderOpenRouter  ProviderName = "openrouter"
    ProviderGitHubApp   ProviderName = "github_app"
    ProviderGitHubOAuth ProviderName = "github_oauth"
    ProviderSentry      ProviderName = "sentry"
    ProviderLinear      ProviderName = "linear"
)

// AllProviders is the canonical list. Used for validation and iteration.
var AllProviders = []ProviderName{
    ProviderAnthropic, ProviderOpenAI, ProviderOpenRouter,
    ProviderGitHubApp, ProviderGitHubOAuth,
    ProviderSentry, ProviderLinear,
}

func (p ProviderName) Valid() bool {
    for _, v := range AllProviders {
        if p == v {
            return true
        }
    }
    return false
}

// --- Per-provider config structs ---
// These are the plaintext types that get JSON-marshaled before encryption.

type AnthropicConfig struct {
    APIKey  string `json:"api_key"  validate:"required"`
    BaseURL string `json:"base_url"`  // optional override, defaults to https://api.anthropic.com
}

type OpenAIConfig struct {
    APIKey  string `json:"api_key"  validate:"required"`
    BaseURL string `json:"base_url"`  // optional override
    APIType string `json:"api_type" validate:"oneof=chat responses"`  // "chat" or "responses"
}

type OpenRouterConfig struct {
    APIKey  string `json:"api_key"  validate:"required"`
    BaseURL string `json:"base_url"`  // optional override
    AppName string `json:"app_name"`  // sent as X-Title header
    SiteURL string `json:"site_url"`  // sent as HTTP-Referer header
}

type GitHubAppConfig struct {
    AppID         int64  `json:"app_id"          validate:"required,gt=0"`
    PrivateKey    string `json:"private_key"     validate:"required"`
    WebhookSecret string `json:"webhook_secret"  validate:"required"`
}

type GitHubOAuthConfig struct {
    ClientID     string `json:"client_id"     validate:"required"`
    ClientSecret string `json:"client_secret" validate:"required"`
}

type SentryConfig struct {
    WebhookSecret string `json:"webhook_secret" validate:"required"`
}

type LinearConfig struct {
    WebhookSecret string `json:"webhook_secret" validate:"required"`
}
```

### `ProviderConfig` interface

All config types implement a common interface so the store and handler layers work uniformly:

```go
// ProviderConfig is implemented by every per-provider config struct.
// It enables the credential store and handlers to work generically while
// keeping each provider's fields strongly typed.
type ProviderConfig interface {
    // Provider returns the ProviderName this config belongs to.
    Provider() ProviderName
    // MaskedSummary returns a CredentialSummary with secrets masked.
    // This is the only way credential data reaches the API response layer.
    MaskedSummary() CredentialSummary
}

func (c AnthropicConfig) Provider() ProviderName   { return ProviderAnthropic }
func (c OpenAIConfig) Provider() ProviderName      { return ProviderOpenAI }
func (c OpenRouterConfig) Provider() ProviderName   { return ProviderOpenRouter }
func (c GitHubAppConfig) Provider() ProviderName    { return ProviderGitHubApp }
func (c GitHubOAuthConfig) Provider() ProviderName  { return ProviderGitHubOAuth }
func (c SentryConfig) Provider() ProviderName       { return ProviderSentry }
func (c LinearConfig) Provider() ProviderName       { return ProviderLinear }
```

### Deserialization: `ParseProviderConfig`

A single function maps `ProviderName` → concrete Go struct. This is the only place where raw JSON bytes touch the type system:

```go
// ParseProviderConfig deserializes JSON into the correct strongly-typed config
// struct for the given provider. Returns an error if the provider is unknown or
// the JSON doesn't match the expected schema.
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
            cfg.APIType = "chat" // default
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
    default:
        return nil, fmt.Errorf("unknown provider: %s", provider)
    }
}
```

### DB model (`internal/models/credentials.go`)

The DB row type stores the encrypted blob. Callers never work with the raw blob directly — they go through the credential store which decrypts and returns the typed config.

```go
// OrgCredential is the DB row representation. The Config field is an encrypted
// blob — it must be decrypted before use. Application code should never work
// with this type directly; use DecryptedCredential instead.
type OrgCredential struct {
    ID             uuid.UUID    `db:"id"`
    OrgID          uuid.UUID    `db:"org_id"`
    Provider       ProviderName `db:"provider"`
    Config         []byte       `db:"config"`  // encrypted bytea — opaque to application code
    Status         string       `db:"status"`
    LastVerifiedAt *time.Time   `db:"last_verified_at"`
    CreatedAt      time.Time    `db:"created_at"`
    UpdatedAt      time.Time    `db:"updated_at"`
}

// DecryptedCredential pairs the DB metadata with the strongly-typed,
// decrypted config. This is the type returned by OrgCredentialStore.Get().
type DecryptedCredential struct {
    ID             uuid.UUID      `json:"id"`
    OrgID          uuid.UUID      `json:"org_id"`
    Provider       ProviderName   `json:"provider"`
    Config         ProviderConfig `json:"-"`  // never serialized — use MaskedSummary for API responses
    Status         string         `json:"status"`
    LastVerifiedAt *time.Time     `json:"last_verified_at,omitempty"`
    UpdatedAt      time.Time      `json:"updated_at"`
}
```

### API response types (`internal/models/credentials.go`)

Separate types for what the frontend sees. Secrets are masked, non-secret metadata is visible:

```go
// CredentialSummary is the API-safe representation of a credential.
// It contains masked secrets and provider-specific metadata but never
// the full API key. This is the ONLY type returned to the frontend.
type CredentialSummary struct {
    Provider       ProviderName `json:"provider"`
    Configured     bool         `json:"configured"`
    Status         string       `json:"status,omitempty"`
    MaskedKey      string       `json:"masked_key,omitempty"`
    LastVerifiedAt *time.Time   `json:"last_verified_at,omitempty"`

    // Provider-specific non-secret fields (populated by MaskedSummary).
    APIType string `json:"api_type,omitempty"` // OpenAI only: "chat" or "responses"
    AppName string `json:"app_name,omitempty"` // OpenRouter only
    AppID   int64  `json:"app_id,omitempty"`   // GitHub App only
}

// MaskedSummary implementations for each provider:

func (c AnthropicConfig) MaskedSummary() CredentialSummary {
    return CredentialSummary{
        Provider:   ProviderAnthropic,
        Configured: true,
        MaskedKey:  maskKey(c.APIKey),
    }
}

func (c OpenAIConfig) MaskedSummary() CredentialSummary {
    return CredentialSummary{
        Provider:   ProviderOpenAI,
        Configured: true,
        MaskedKey:  maskKey(c.APIKey),
        APIType:    c.APIType,
    }
}

func (c OpenRouterConfig) MaskedSummary() CredentialSummary {
    return CredentialSummary{
        Provider:   ProviderOpenRouter,
        Configured: true,
        MaskedKey:  maskKey(c.APIKey),
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
        MaskedKey:  maskKey(c.ClientID),
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

// maskKey preserves the provider prefix and last 4 chars.
// "sk-ant-api03-abc...xyz" → "sk-ant-...xyz"
func maskKey(key string) string {
    if len(key) <= 8 {
        return "****"
    }
    prefix := key[:6]
    suffix := key[len(key)-4:]
    return prefix + "..." + suffix
}
```

### Org settings type (`internal/models/org_settings.go`)

The `organizations.settings` JSONB column is also strongly typed. Currently `OrgSettings` is defined inside the prioritization service — it should move to the models package so all services share a single canonical type:

```go
package models

// OrgSettings is the strongly-typed representation of organizations.settings JSONB.
// Every field has a JSON tag and a default value. The Parse function applies defaults
// so callers never have to nil-check or handle zero values.
type OrgSettings struct {
    // Execution behavior
    AutonomyLevel     string `json:"autonomy_level"`      // "manual", "auto_simple", "auto_all"
    Aggressiveness    int    `json:"execution_aggressiveness"`
    MaxConcurrentRuns int    `json:"max_concurrent_runs"`

    // Priority scoring weights (must sum to ~1.0)
    PriorityWeights PriorityWeights `json:"priority_weights"`

    MinPriorityThreshold float64 `json:"min_priority_threshold"`
    ProductDirection     string  `json:"product_direction"`

    // LLM model selection (e.g., "claude-sonnet-4-5", "gpt-4o")
    LLMModel string `json:"llm_model"`
}

type PriorityWeights struct {
    CustomerImpact float64 `json:"customer_impact"`
    Severity       float64 `json:"severity"`
    Recency        float64 `json:"recency"`
    RevenueRisk    float64 `json:"revenue_risk"`
}

// DefaultOrgSettings returns the default org settings.
const (
    DefaultAutonomyLevel        = "manual"
    DefaultAggressiveness       = 5
    DefaultMaxConcurrentRuns    = 3
    DefaultMinPriorityThreshold = 30.0

    DefaultWeightCustomerImpact = 0.35
    DefaultWeightSeverity       = 0.25
    DefaultWeightRecency        = 0.20
    DefaultWeightRevenueRisk    = 0.20
)

// ParseOrgSettings deserializes the JSONB settings column into OrgSettings,
// applying defaults for any missing or zero-valued fields.
func ParseOrgSettings(raw json.RawMessage) OrgSettings {
    var s OrgSettings
    if len(raw) > 0 {
        _ = json.Unmarshal(raw, &s)
    }

    if s.AutonomyLevel == "" {
        s.AutonomyLevel = DefaultAutonomyLevel
    }
    if s.Aggressiveness == 0 {
        s.Aggressiveness = DefaultAggressiveness
    }
    if s.MaxConcurrentRuns == 0 {
        s.MaxConcurrentRuns = DefaultMaxConcurrentRuns
    }
    if s.MinPriorityThreshold == 0 {
        s.MinPriorityThreshold = DefaultMinPriorityThreshold
    }
    if s.PriorityWeights == (PriorityWeights{}) {
        s.PriorityWeights = PriorityWeights{
            CustomerImpact: DefaultWeightCustomerImpact,
            Severity:       DefaultWeightSeverity,
            Recency:        DefaultWeightRecency,
            RevenueRisk:    DefaultWeightRevenueRisk,
        }
    }
    return s
}
```

### Type flow: end to end

Here's how types flow through each layer, with no `json.RawMessage` or `interface{}` in between:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ FRONTEND (TypeScript)                                                       │
│                                                                             │
│ PUT /credentials/anthropic                                                  │
│   Request body: { "api_key": "sk-ant-...", "base_url": "" }                │
│                                                                             │
│ GET /credentials                                                            │
│   Response:     CredentialSummary[] (masked_key, status, provider metadata) │
└────────────────────────────┬────────────────────────────────────────────────┘
                             │
┌────────────────────────────▼────────────────────────────────────────────────┐
│ API HANDLER (internal/api/handlers/credentials.go)                          │
│                                                                             │
│ PUT: json.Decode(body) → AnthropicConfig (strongly typed)                   │
│      validate struct fields → credStore.Upsert(orgID, provider, cfg)        │
│                                                                             │
│ GET: credStore.ListSummaries(orgID) → []CredentialSummary                   │
│      (summaries are pre-masked, never contain raw keys)                     │
└────────────────────────────┬────────────────────────────────────────────────┘
                             │
┌────────────────────────────▼────────────────────────────────────────────────┐
│ CREDENTIAL STORE (internal/db/org_credentials.go)                           │
│                                                                             │
│ Upsert: json.Marshal(AnthropicConfig) → []byte                             │
│         crypto.Encrypt([]byte) → encrypted bytea                            │
│         INSERT INTO org_credentials (config = encrypted)                    │
│                                                                             │
│ Get:    SELECT config FROM org_credentials WHERE provider = 'anthropic'     │
│         crypto.Decrypt(bytea) → []byte                                      │
│         ParseProviderConfig("anthropic", []byte) → AnthropicConfig          │
│         return DecryptedCredential{Config: AnthropicConfig}                 │
│                                                                             │
│ ListSummaries: for each row:                                                │
│         decrypt → ParseProviderConfig → cfg.MaskedSummary()                 │
│         return []CredentialSummary                                          │
└────────────────────────────┬────────────────────────────────────────────────┘
                             │
┌────────────────────────────▼────────────────────────────────────────────────┐
│ LLM CLIENT (internal/llm/client.go)                                         │
│                                                                             │
│ NewClientFromCredentials(model string, creds []DecryptedCredential, ...)     │
│                                                                             │
│ Uses type assertions to extract provider-specific fields:                    │
│   for _, cred := range creds {                                              │
│       switch cfg := cred.Config.(type) {                                    │
│       case AnthropicConfig:                                                 │
│           providers["anthropic"] = NewAnthropicProvider(cfg.APIKey,          │
│               WithAnthropicBaseURL(cfg.BaseURL))                            │
│       case OpenAIConfig:                                                    │
│           if cfg.APIType == "responses" {                                   │
│               providers["openai_responses"] = NewOpenAIResponsesProvider(   │
│                   cfg.APIKey, WithOpenAIResponsesBaseURL(cfg.BaseURL))      │
│           } else {                                                          │
│               providers["openai_chat"] = NewOpenAIChatProvider(             │
│                   cfg.APIKey, WithOpenAIChatBaseURL(cfg.BaseURL))           │
│           }                                                                 │
│       case OpenRouterConfig:                                                │
│           providers["openrouter"] = NewOpenRouterProvider(cfg.APIKey,        │
│               WithOpenRouterAppName(cfg.AppName), ...)                      │
│       }                                                                     │
│   }                                                                         │
│   return buildChainAndClient(model, providers)                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

### LLM model selection

The selected LLM model is stored in `organizations.settings` (existing JSONB column), not in `org_credentials`. It's part of the `OrgSettings` struct:

```go
settings := models.ParseOrgSettings(org.Settings)
model := settings.LLMModel  // e.g., "claude-sonnet-4-5"
```

This keeps the model choice visible in the normal settings API response and separate from encrypted credential storage.

## Encryption

Reuse the envelope encryption design from [20-security-architecture.md](20-security-architecture.md):

```
ENCRYPTION_MASTER_KEY (env var, 32+ chars)
    → HKDF derive KEK
        → per-row random DEK (AES-256-GCM)
            → encrypts config JSON
```

### `internal/crypto/encryption.go`

```go
type Service struct {
    kek []byte // derived from ENCRYPTION_MASTER_KEY via HKDF
}

func NewService(masterKey string) (*Service, error)
func (s *Service) Encrypt(plaintext []byte) ([]byte, error)
func (s *Service) Decrypt(ciphertext []byte) ([]byte, error)
```

When `ENCRYPTION_MASTER_KEY` is empty (local dev), credentials are stored as plaintext JSON with a `v0:` prefix. When set, they're stored with a `v1:` prefix followed by the encrypted envelope. This lets local dev work without encryption while production enforces it.

## API

### Endpoints

All under `/api/v1/settings/credentials`, admin-only.

#### `GET /api/v1/settings/credentials`

Returns all configured providers with masked keys and status. Never returns full keys.

```json
{
  "data": [
    {
      "provider": "anthropic",
      "status": "active",
      "configured": true,
      "masked_key": "sk-ant-...7x2Q",
      "last_verified_at": "2025-01-15T10:00:00Z"
    },
    {
      "provider": "openai",
      "status": "active",
      "configured": true,
      "masked_key": "sk-...4kF9",
      "api_type": "chat",
      "last_verified_at": "2025-01-15T10:00:00Z"
    },
    {
      "provider": "openrouter",
      "configured": false
    },
    {
      "provider": "github_app",
      "status": "active",
      "configured": true,
      "app_id": 12345,
      "last_verified_at": "2025-01-15T10:00:00Z"
    }
  ]
}
```

#### `PUT /api/v1/settings/credentials/{provider}`

Sets or updates credentials for a provider. Accepts the plaintext config, encrypts server-side, stores to DB.

```json
// PUT /api/v1/settings/credentials/anthropic
{
  "api_key": "sk-ant-...",
  "base_url": ""
}
```

Response: same as a single item from the GET response (masked key, status).

On save, the server immediately verifies the key by making a lightweight API call to the provider (e.g., a tiny completion request or a list-models call). Sets `status` to `active` or `invalid` and updates `last_verified_at`.

#### `DELETE /api/v1/settings/credentials/{provider}`

Removes credentials for a provider. Sets status to `disabled` (soft delete — row stays for audit trail).

#### `POST /api/v1/settings/credentials/{provider}/verify`

Re-verifies an existing credential without changing it. Updates `status` and `last_verified_at`.

### LLM model selection

Model selection is part of the existing settings API:

```json
// PATCH /api/v1/settings
{
  "settings": {
    "llm_model": "claude-sonnet-4-5"
  }
}
```

## Data access layer

### `internal/db/org_credentials.go`

The store handles encryption/decryption and type conversion. Callers pass and receive strongly-typed `ProviderConfig` values — never raw bytes or `json.RawMessage`.

```go
type OrgCredentialStore struct {
    db     DBTX
    crypto *crypto.Service  // nil in dev = plaintext storage
}

// Upsert encrypts and stores a strongly-typed provider config.
// The ProviderConfig interface ensures only valid config types can be passed.
func (s *OrgCredentialStore) Upsert(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error {
    provider := cfg.Provider()

    // Marshal the typed config to JSON.
    plaintext, err := json.Marshal(cfg)
    if err != nil {
        return fmt.Errorf("marshal %s config: %w", provider, err)
    }

    // Encrypt (or store plaintext in dev mode).
    encrypted, err := s.encrypt(plaintext)
    if err != nil {
        return fmt.Errorf("encrypt %s config: %w", provider, err)
    }

    query := `
        INSERT INTO org_credentials (org_id, provider, config, status)
        VALUES (@org_id, @provider, @config, 'active')
        ON CONFLICT (org_id, provider)
        DO UPDATE SET config = @config, status = 'active', updated_at = now()
        RETURNING id, created_at, updated_at`

    // ... execute query with encrypted bytes
}

// Get decrypts and returns a strongly-typed DecryptedCredential.
// The Config field is a concrete type (AnthropicConfig, OpenAIConfig, etc.),
// not json.RawMessage.
func (s *OrgCredentialStore) Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
    row := // ... SELECT from org_credentials

    // Decrypt the blob.
    plaintext, err := s.decrypt(row.Config)
    if err != nil {
        return nil, fmt.Errorf("decrypt %s config: %w", provider, err)
    }

    // Parse into the correct strongly-typed struct.
    cfg, err := models.ParseProviderConfig(provider, plaintext)
    if err != nil {
        return nil, fmt.Errorf("parse %s config: %w", provider, err)
    }

    return &models.DecryptedCredential{
        ID:             row.ID,
        OrgID:          row.OrgID,
        Provider:       provider,
        Config:         cfg,  // concrete type, not json.RawMessage
        Status:         row.Status,
        LastVerifiedAt: row.LastVerifiedAt,
        UpdatedAt:      row.UpdatedAt,
    }, nil
}

// GetAllLLM loads all LLM provider credentials for an org, returning only
// the LLM-relevant types. This is the primary method used to build an LLM client.
func (s *OrgCredentialStore) GetAllLLM(ctx context.Context, orgID uuid.UUID) ([]models.DecryptedCredential, error) {
    llmProviders := []models.ProviderName{
        models.ProviderAnthropic,
        models.ProviderOpenAI,
        models.ProviderOpenRouter,
    }
    // SELECT ... WHERE org_id = @org_id AND provider = ANY(@providers) AND status = 'active'
    // decrypt + ParseProviderConfig each row
    // ...
}

// ListSummaries returns masked credential info for all providers for an org.
// Returns a CredentialSummary for every known provider (configured or not).
func (s *OrgCredentialStore) ListSummaries(ctx context.Context, orgID uuid.UUID) ([]models.CredentialSummary, error) {
    // SELECT all rows for org_id
    // For each configured provider: decrypt → ParseProviderConfig → cfg.MaskedSummary()
    // For unconfigured providers: return CredentialSummary{Provider: p, Configured: false}
    // ...
}

// Disable soft-deletes a credential by setting status = 'disabled'.
func (s *OrgCredentialStore) Disable(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error
```

## LLM client changes

### New: `NewClientFromCredentials`

The current `NewClient(cfg Config, logger)` builds providers from a flat config struct. Replace it with a constructor that accepts strongly-typed credentials:

```go
// NewClientFromCredentials builds a Client from org-specific DB credentials.
// It uses type assertions on each DecryptedCredential.Config to extract
// provider-specific fields — no json.RawMessage or map[string]interface{}.
//
// This is the primary constructor used at request time.
func NewClientFromCredentials(model string, creds []models.DecryptedCredential, logger zerolog.Logger) (Client, error) {
    if model == "" {
        return nil, nil
    }

    httpClient := &http.Client{Timeout: 60 * time.Second}
    providers := map[string]Provider{}

    for _, cred := range creds {
        // Type switch on the concrete ProviderConfig — the compiler enforces
        // that we handle each type, and each case gets fully-typed access
        // to the config fields (no casting, no map lookups).
        switch cfg := cred.Config.(type) {
        case models.AnthropicConfig:
            var opts []AnthropicOption
            if cfg.BaseURL != "" {
                opts = append(opts, WithAnthropicBaseURL(cfg.BaseURL))
            }
            opts = append(opts, WithAnthropicHTTPClient(httpClient))
            providers["anthropic"] = NewAnthropicProvider(cfg.APIKey, opts...)

        case models.OpenAIConfig:
            if cfg.APIType == "responses" {
                var opts []OpenAIResponsesOption
                if cfg.BaseURL != "" {
                    opts = append(opts, WithOpenAIResponsesBaseURL(cfg.BaseURL))
                }
                opts = append(opts, WithOpenAIResponsesHTTPClient(httpClient))
                providers["openai_responses"] = NewOpenAIResponsesProvider(cfg.APIKey, opts...)
            } else {
                var opts []OpenAIChatOption
                if cfg.BaseURL != "" {
                    opts = append(opts, WithOpenAIChatBaseURL(cfg.BaseURL))
                }
                opts = append(opts, WithOpenAIChatHTTPClient(httpClient))
                providers["openai_chat"] = NewOpenAIChatProvider(cfg.APIKey, opts...)
            }

        case models.OpenRouterConfig:
            var opts []OpenRouterOption
            if cfg.BaseURL != "" {
                opts = append(opts, WithOpenRouterBaseURL(cfg.BaseURL))
            }
            if cfg.AppName != "" {
                opts = append(opts, WithOpenRouterAppName(cfg.AppName))
            }
            if cfg.SiteURL != "" {
                opts = append(opts, WithOpenRouterSiteURL(cfg.SiteURL))
            }
            opts = append(opts, WithOpenRouterHTTPClient(httpClient))
            providers["openrouter"] = NewOpenRouterProvider(cfg.APIKey, opts...)
        }
        // Non-LLM config types (GitHubAppConfig, etc.) are silently skipped.
    }

    if len(providers) == 0 {
        return nil, &NoProvidersError{Model: model}
    }

    chain, err := buildChain(model, providers)
    if err != nil {
        return nil, err
    }

    return &FallbackClient{chain: chain, logger: logger}, nil
}
```

The existing `NewClient(cfg Config, ...)` can be removed once all callers migrate to the credential-based constructor.

### Per-request client construction

Because credentials are per-org, the LLM client must be built per-request (or cached per-org with invalidation on credential update). The flow:

```
HTTP request → middleware extracts org_id
    → handler loads org → ParseOrgSettings(org.Settings) → OrgSettings.LLMModel
    → credStore.GetAllLLM(orgID) → []DecryptedCredential (strongly typed)
    → llm.NewClientFromCredentials(model, creds, logger) → Client
    → service.DoWork(ctx, client, ...)
```

### Caching

Building providers on every request is wasteful since credentials rarely change. Use a simple cache:

```go
type ClientCache struct {
    mu      sync.RWMutex
    clients map[uuid.UUID]cachedClient  // org_id → client
}

type cachedClient struct {
    client    Client
    updatedAt time.Time  // matches org_credentials.updated_at
}
```

Invalidation: on credential upsert/delete, evict the org's entry. The next request rebuilds. No TTL needed — credentials change infrequently.

## Handler changes

### New: `CredentialHandler`

```go
type CredentialHandler struct {
    credStore   *db.OrgCredentialStore
    verifiers   map[models.ProviderName]CredentialVerifier
    clientCache *llm.ClientCache
}
```

### API request parsing

The PUT handler uses `ParseProviderConfig` to decode the request body into the correct typed struct, then validates before storing:

```go
func (h *CredentialHandler) Update(w http.ResponseWriter, r *http.Request) {
    provider := models.ProviderName(chi.URLParam(r, "provider"))
    if !provider.Valid() {
        writeError(w, http.StatusBadRequest, "INVALID_PROVIDER", "unknown provider")
        return
    }

    // Read raw body and parse into the correct strongly-typed config.
    body, _ := io.ReadAll(r.Body)
    cfg, err := models.ParseProviderConfig(provider, body)
    if err != nil {
        writeError(w, http.StatusBadRequest, "INVALID_CONFIG", err.Error())
        return
    }

    // Validate struct fields (uses validate tags on the config structs).
    if err := validate.Struct(cfg); err != nil {
        writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
        return
    }

    // Verify credentials against the provider's API.
    if verifier, ok := h.verifiers[provider]; ok {
        if err := verifier.Verify(r.Context(), cfg); err != nil {
            // Store as 'invalid' but don't reject — user can fix later.
            // ...
        }
    }

    // Store. The credential store accepts ProviderConfig, not json.RawMessage.
    orgID := middleware.OrgIDFromContext(r.Context())
    if err := h.credStore.Upsert(r.Context(), orgID, cfg); err != nil {
        writeError(w, http.StatusInternalServerError, "STORE_FAILED", "failed to save credential")
        return
    }

    // Return masked summary.
    summary := cfg.MaskedSummary()
    writeJSON(w, http.StatusOK, models.SingleResponse[models.CredentialSummary]{Data: summary})
}
```

### Credential verification

Each provider has a strongly-typed verification function. The `ProviderConfig` interface means verifiers receive concrete types:

```go
// CredentialVerifier tests whether a credential is valid by making a
// lightweight API call. Each provider implements this for its own config type.
type CredentialVerifier interface {
    Verify(ctx context.Context, cfg models.ProviderConfig) error
}
```

Concrete verifiers use type assertions to access provider-specific fields:

```go
type anthropicVerifier struct{}

func (v anthropicVerifier) Verify(ctx context.Context, cfg models.ProviderConfig) error {
    ac, ok := cfg.(models.AnthropicConfig)
    if !ok {
        return fmt.Errorf("expected AnthropicConfig, got %T", cfg)
    }
    // Use ac.APIKey and ac.BaseURL to make a lightweight /v1/messages call.
    // ...
}
```

| Provider | Verification method |
|----------|-------------------|
| Anthropic | `POST /v1/messages` with 1-token max_tokens |
| OpenAI | `GET /v1/models` |
| OpenRouter | `GET /api/v1/models` |
| GitHub App | Generate JWT, call `GET /app` |
| Sentry | No remote check (webhook secret is verified on delivery) |
| Linear | No remote check (webhook secret is verified on delivery) |

## Webhook secret resolution

Currently, webhook signature verification uses secrets from `config.Config` (env vars):

```go
// router.go
r.Use(middleware.VerifyWebhookSignature("X-Sentry-Hook-Signature", cfg.SentryWebhookSecret, ""))
```

This needs to change to per-org lookup. But webhook routes don't have org context yet (they're unauthenticated — the webhook itself identifies the org).

### Resolution strategy

For webhooks with org-specific secrets (Sentry, Linear), the handler loads the typed credential and uses the concrete struct's fields:

```go
func (h *WebhookHandler) HandleSentry(w http.ResponseWriter, r *http.Request) {
    // 1. Read body, extract org identifier from payload.
    body, _ := io.ReadAll(r.Body)
    orgID := extractOrgFromSentryPayload(body)

    // 2. Load the org's Sentry credential (strongly typed).
    cred, err := h.credStore.Get(ctx, orgID, models.ProviderSentry)
    if err != nil {
        writeError(w, http.StatusUnauthorized, "NO_CREDENTIALS", "sentry not configured")
        return
    }

    // 3. Type-assert to SentryConfig — compiler guarantees this is safe
    //    because Get() with ProviderSentry always returns SentryConfig.
    sentryCfg := cred.Config.(models.SentryConfig)

    // 4. Verify signature using the typed field.
    if !verifyHMAC(r.Header.Get("X-Sentry-Hook-Signature"), body, sentryCfg.WebhookSecret) {
        writeError(w, http.StatusUnauthorized, "INVALID_SIGNATURE", "signature mismatch")
        return
    }

    // 5. Process webhook.
}
```

For GitHub webhooks, this already works via the GitHub App installation ID → integration → org lookup. The GitHub App credential uses the typed `GitHubAppConfig`:

```go
cred, _ := h.credStore.Get(ctx, orgID, models.ProviderGitHubApp)
ghCfg := cred.Config.(models.GitHubAppConfig)
// ghCfg.AppID, ghCfg.PrivateKey, ghCfg.WebhookSecret are all typed fields
```

## Migration plan

### Phase 1: Add infrastructure

1. Create `org_credentials` table (migration).
2. Implement `internal/crypto/encryption.go`.
3. Implement `OrgCredentialStore`.
4. Implement `CredentialHandler` with GET/PUT/DELETE/verify endpoints.
5. Wire into router (admin-only).

### Phase 2: Wire LLM client

1. Add `NewClientFromCredentials` constructor.
2. Add `ClientCache`.
3. Update services (validation, prioritization, complexity estimation) to accept `Client` per-request instead of at construction time.
4. Update worker handlers to load org credentials and build client per-job.

### Phase 3: Wire remaining providers

1. Move GitHub App credentials to per-org (update `ghservice.NewService` to accept per-org keys).
2. Move webhook secrets to per-org lookup.
3. Update GitHub OAuth to use org credentials (or keep as env var if single-tenant).

### Phase 4: Remove env var keys

1. Remove LLM env vars from `config.Config` (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, etc.).
2. Remove `config.LLMConfig()` method.
3. Remove `llm.NewClient(cfg Config, ...)` constructor.
4. Update `.env.example` to only show infrastructure vars.
5. Update docs.

## Frontend

### Settings page: Credentials tab

The settings page gets a new "API Keys" or "Integrations" section. For each provider:

```
┌─────────────────────────────────────────────────┐
│ Anthropic                          ✅ Connected  │
│ API Key: sk-ant-...7x2Q                         │
│ Last verified: 2 hours ago                      │
│ [Update Key]  [Verify]  [Remove]                │
├─────────────────────────────────────────────────┤
│ OpenAI                             ✅ Connected  │
│ API Key: sk-...4kF9                              │
│ API Type: Chat Completions                       │
│ Last verified: 2 hours ago                      │
│ [Update Key]  [Verify]  [Remove]                │
├─────────────────────────────────────────────────┤
│ OpenRouter                         ⚪ Not set    │
│ [Add Key]                                        │
└─────────────────────────────────────────────────┘
```

For the LLM model selector, a dropdown that lists available models filtered by which providers have keys configured.

### First-run experience

On first login, if no LLM credentials are configured, show a setup wizard:

1. "Pick your LLM provider" — Anthropic, OpenAI, or OpenRouter.
2. "Paste your API key" — single input field.
3. "Pick your model" — dropdown based on provider.
4. Verify key → show success → redirect to dashboard.

This replaces the current "edit `.env` and restart" flow for new users.

## Security considerations

- **Encryption at rest**: All credentials encrypted with AES-256-GCM via envelope encryption. Master key in env var.
- **No plaintext in logs**: Credential values are never logged. Masked versions only.
- **No plaintext in API responses**: GET endpoints return masked keys only. Full keys are write-only.
- **RBAC**: Only org admins can view/modify credentials.
- **Audit trail**: Soft deletes preserve history. `updated_at` tracks changes.
- **Dev mode**: When `ENCRYPTION_MASTER_KEY` is empty, credentials stored as plaintext JSON with `v0:` prefix. Server logs a warning at startup.
