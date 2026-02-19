# 25 — Dashboard Credential Management

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

### Plaintext structure (before encryption)

Each provider's config blob contains different fields:

```jsonc
// provider = 'anthropic'
{ "api_key": "sk-ant-...", "base_url": "" }

// provider = 'openai'
{ "api_key": "sk-...", "base_url": "", "api_type": "chat" }

// provider = 'openrouter'
{ "api_key": "sk-or-...", "base_url": "", "app_name": "143", "site_url": "" }

// provider = 'github_app'
{ "app_id": 12345, "private_key": "-----BEGIN RSA...", "webhook_secret": "whsec_..." }

// provider = 'github_oauth'
{ "client_id": "Iv1...", "client_secret": "..." }

// provider = 'sentry'
{ "webhook_secret": "..." }

// provider = 'linear'
{ "webhook_secret": "..." }
```

### LLM model selection

The selected LLM model is stored in `organizations.settings` (existing JSONB column), not in `org_credentials`:

```jsonc
// organizations.settings
{
  "autonomy_level": "auto_simple",
  "llm_model": "claude-sonnet-4-5",
  // ... other existing settings
}
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

```go
type OrgCredentialStore struct {
    db     DBTX
    crypto *crypto.Service  // nil in dev = plaintext storage
}

func (s *OrgCredentialStore) Upsert(ctx context.Context, orgID uuid.UUID, provider string, config json.RawMessage) error
func (s *OrgCredentialStore) Get(ctx context.Context, orgID uuid.UUID, provider string) (*DecryptedCredential, error)
func (s *OrgCredentialStore) ListByOrg(ctx context.Context, orgID uuid.UUID) ([]CredentialSummary, error)
func (s *OrgCredentialStore) Disable(ctx context.Context, orgID uuid.UUID, provider string) error
```

`DecryptedCredential` contains the full plaintext config. `CredentialSummary` contains only masked keys and status — this is what the API returns.

### Masking

```go
func maskKey(key string) string {
    if len(key) <= 8 {
        return "****"
    }
    prefix := key[:6]  // keep provider prefix (e.g., "sk-ant-")
    suffix := key[len(key)-4:]
    return prefix + "..." + suffix
}
```

## LLM client changes

### New: `NewClientFromCredentials`

The current `NewClient(cfg Config, logger)` builds providers from a static config struct. Add a new constructor that builds from DB-stored credentials:

```go
// NewClientFromCredentials builds a Client from org-specific DB credentials.
// This is the primary constructor used at request time.
func NewClientFromCredentials(model string, creds map[string]*DecryptedCredential, logger zerolog.Logger) (Client, error)
```

Where `creds` is a map of provider name to decrypted credential (e.g., `{"anthropic": ..., "openai": ...}`).

The existing `NewClient(cfg Config, ...)` can be removed once all callers migrate to the credential-based constructor.

### Per-request client construction

Because credentials are per-org, the LLM client must be built per-request (or cached per-org with invalidation on credential update). The flow:

```
HTTP request → middleware extracts org_id
    → handler loads org settings (for llm_model)
    → handler loads org credentials
    → llm.NewClientFromCredentials(model, creds, logger)
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
    verifiers   map[string]CredentialVerifier
    clientCache *llm.ClientCache
}
```

### Credential verification

Each provider has a lightweight verification function:

```go
type CredentialVerifier interface {
    Verify(ctx context.Context, config json.RawMessage) error
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

For webhooks with org-specific secrets (Sentry, Linear):

1. Parse the webhook payload minimally to extract an org identifier (e.g., Sentry project slug, Linear team ID).
2. Look up the org via the integration table.
3. Load the org's webhook secret from `org_credentials`.
4. Verify the signature.

This replaces the current middleware approach with per-handler verification:

```go
func (h *WebhookHandler) HandleSentry(w http.ResponseWriter, r *http.Request) {
    // 1. Read body
    // 2. Extract org identifier from payload
    // 3. Look up org + credentials
    // 4. Verify signature against org's sentry webhook secret
    // 5. Process webhook
}
```

For GitHub webhooks, this already works via the GitHub App installation ID → integration → org lookup. The GitHub webhook secret stays as a single env var (`GITHUB_WEBHOOK_SECRET`) since there's one GitHub App, not one per org.

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
