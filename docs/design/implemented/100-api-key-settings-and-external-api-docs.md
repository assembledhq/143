# Design: API Key Settings and External API Docs

> **Status:** Implemented | **Last reviewed:** 2026-06-12

## Context

The general external API backend exists: org-scoped API clients own one or more `143_sk_` bearer tokens, tokens carry explicit scopes and optional repository allowlists, and bearer requests are checked against the route-to-scope map before reaching session, automation, and preview handlers. The implemented backend contract is documented in [94-external-api-sessions-automations.md](94-external-api-sessions-automations.md).

This implementation closed two gaps:

1. Admins now have a first-party dashboard surface for issuing and rotating general API keys.
2. The Fumadocs external API page now documents API-client issuance, scopes, endpoint coverage, repository restrictions, idempotency coverage, and exact route behavior.

This design covered the Settings UI and public docs update. It did not redesign the existing backend auth model.

## Goals

- Add an admin-only Settings page for external API clients and tokens.
- Make key issuance self-service without exposing raw tokens after creation.
- Make token scope and repository restrictions understandable before the admin creates a key.
- Support practical key rotation, revocation, and whole-client disable flows.
- Expand public Fumadocs so customers can discover how to get a key and how to call every supported external API route.
- Keep the docs aligned with the actual scope whitelist rather than implying that every app route is external-token compatible.

## Non-Goals

- Do not add OAuth apps or delegated third-party app installs.
- Do not add human personal access tokens.
- Do not let API tokens create or manage other API tokens.
- Do not expose project, eval, settings, team, credential, audit-log, or integration routes to external API tokens in this iteration.
- Do not replace legacy preview API tokens in one step. The Preview settings page can keep them with migration copy.
- Do not make "all access" mean every authenticated product/admin route. It means all routes intentionally exposed through the external API scope map.

## Current Backend Contract

### Token management routes

API-key management exists under admin-only browser-auth routes:

```http
GET    /api/v1/api-keys
POST   /api/v1/api-keys
GET    /api/v1/api-keys/{id}
PATCH  /api/v1/api-keys/{id}
DELETE /api/v1/api-keys/{id}

GET    /api/v1/api-keys/{id}/tokens
POST   /api/v1/api-keys/{id}/tokens
DELETE /api/v1/api-keys/{id}/tokens/{token_id}
```

These routes require normal app authentication, org context, and `admin` role. External API bearer tokens must not be allowed to reach them because the external API scope map intentionally has no token-management scopes.

The internal backend still uses API clients as durable service-account identities and API tokens as disposable secrets, but the HTTP surface presents those resources as API keys. Client-only `/api/v1/api-clients` routes are not registered.

### External-token route map

External API tokens are currently allowed only when `requiredAPIScope(method, path)` returns a scope:

| Route pattern | Methods | Scope |
|---|---:|---|
| `/api/v1/sessions`, `/api/v1/sessions/{id}...` | `GET` | `sessions:read` |
| `/api/v1/sessions` | `POST` | `sessions:create` |
| `/api/v1/sessions/{id}/messages` | `POST` | `sessions:write` |
| `/api/v1/sessions/{id}/retry` | `POST` | `sessions:write` |
| `/api/v1/sessions/{id}/cancel` | `POST` | `sessions:cancel` |
| `/api/v1/sessions/{id}/end` | `POST` | `sessions:cancel` |
| `/api/v1/sessions/{id}/pr` | `POST` | `sessions:publish` |
| `/api/v1/sessions/{id}/branch` | `POST` | `sessions:publish` |
| `/api/v1/automations`, `/api/v1/automations/{id}...` | `GET` | `automations:read` |
| `/api/v1/automations` | `POST` | `automations:create` |
| `/api/v1/automations/{id}` | `PATCH`, `DELETE` | `automations:write` |
| `/api/v1/automations/{id}/pause` | `POST` | `automations:write` |
| `/api/v1/automations/{id}/resume` | `POST` | `automations:write` |
| `/api/v1/automations/{id}/run` | `POST` | `automations:run` |
| `/api/v1/previews...` | `GET` | `previews:read` |
| `/api/v1/previews` | `POST` | `previews:create` |
| `/api/v1/previews/{id}/stop` | `POST` | `previews:stop` |
| `/api/v1/previews/{id}/restart` | `POST` | `previews:stop` |

All other protected routes return `403 FORBIDDEN` for API-token callers even if the token has valid scopes.

### Scope presets and resource-family scopes

The Settings UI should offer fast scope presets for trusted internal systems, but the backend representation needs to avoid accidentally granting future endpoint families.

When the admin selects `Full external API access`, the frontend submits every currently supported explicit resource/action scope:

```json
{
  "scopes": [
    "sessions:read",
    "sessions:create",
    "sessions:write",
    "sessions:cancel",
    "sessions:publish",
    "automations:read",
    "automations:create",
    "automations:write",
    "automations:run",
    "previews:read",
    "previews:create",
    "previews:stop"
  ]
}
```

This is the v1 full-access strategy. Do not add a backend all-access wildcard.

Use resource-family scopes for the common middle ground where an admin wants to grant all current and future actions within one external API resource family:

```text
sessions:all
automations:all
previews:all
```

Resource-family scopes satisfy any required scope in the same family:

| Scope | Satisfies |
|---|---|
| `sessions:all` | `sessions:read`, `sessions:create`, `sessions:write`, `sessions:cancel`, `sessions:publish` |
| `automations:all` | `automations:read`, `automations:create`, `automations:write`, `automations:run` |
| `previews:all` | `previews:read`, `previews:create`, `previews:stop` |

When future scopes are added inside an existing family, the code change that adds the new scope must explicitly decide whether the family `*:all` scope satisfies it. That decision should be covered by table-driven authorization tests. Do not implement family scopes as blind string-prefix matching without tests for every required scope.

This gives admins a compact way to grant "all sessions" or "all previews" without turning full API access into a future-expanding wildcard.

Repository allowlists still apply. A token created with full external API access and `repository_ids = [repo-a]` has all selected external API actions only for `repo-a` where a route names or resolves a repository.

## Database Schema

One schema migration is required for optional IP restrictions. The remaining API-key UI and docs work should use the existing external API tables.

The implementation should use the existing tables introduced by the external API work:

### `api_clients`

| Column | Purpose |
|---|---|
| `id uuid` | API client ID. |
| `org_id uuid` | Tenant scope. Every query filters by this. |
| `name text` | Human-readable service-account name. |
| `description text nullable` | Optional operational context. |
| `status text` | `enabled` or `disabled`. |
| `created_by_user_id uuid nullable` | Admin who created it. |
| `disabled_by_user_id uuid nullable` | Admin who disabled it. |
| `disabled_at timestamptz nullable` | Disable timestamp. |
| `created_at timestamptz` | Creation timestamp. |
| `updated_at timestamptz` | Last metadata/status update timestamp. |

### `api_tokens`

| Column | Purpose |
|---|---|
| `id uuid` | Token row ID. |
| `org_id uuid` | Tenant scope. Every query filters by this. |
| `api_client_id uuid` | Owning API client. |
| `name text` | Token label, such as `production-ci`. |
| `token_hash text` | Hash only; never returned. |
| `token_prefix text` | Short public identifier like `143_sk_abcd`. |
| `scopes text[]` | Explicit allowed scopes. |
| `repository_ids uuid[]` | Empty means all current/future org repositories; non-empty allowlist restricts repo-scoped operations. |
| `expires_at timestamptz nullable` | Optional expiry. |
| `last_used_at timestamptz nullable` | Usage metadata. |
| `last_used_ip text nullable` | Usage metadata for admin review. |
| `last_used_user_agent text nullable` | Usage metadata for admin review. |
| `revoked_by_user_id uuid nullable` | Admin who revoked it. |
| `revoked_at timestamptz nullable` | Revocation timestamp. |
| `created_by_user_id uuid nullable` | Admin who created it. |
| `created_at timestamptz` | Creation timestamp. |
| `allowed_ip_cidrs text[] NOT NULL DEFAULT '{}'` | Optional source IP/CIDR allowlist. Empty means any source IP. |

### `api_idempotency`

No UI is needed for idempotency records. The docs should describe the behavior and 24-hour retention.

### Future schema candidates

Do not add these now, but leave room in UI copy and API types:

- `api_clients.owner_user_id` or `owner_team_id` for operational ownership.
- `api_clients.contact` for rotation notifications.
- `api_tokens.last_error_at` or `last_denied_scope` for debugging failed calls.

### Backend schema/API changes

Adding optional IP restrictions and a first-class create-key endpoint requires small backend changes:

- Add `allowed_ip_cidrs text[] NOT NULL DEFAULT '{}'` to `api_tokens`.
- Validate CIDR/IP strings when tokens are created.
- In token auth middleware, after token lookup and before scope checks, reject requests whose remote IP is not in the allowlist.
- Add an atomic `POST /api/v1/api-keys` endpoint for the primary UI flow.
- Add `sessions:all`, `automations:all`, and `previews:all` to `APITokenScope.Validate()`.
- Update `RequireAPIScope` so a family `*:all` scope satisfies explicitly listed scopes in that family.
- Keep exact route allowlisting through `requiredAPIScope`. Family scopes satisfy required scopes; they do not make unmapped routes accessible.
- Keep `Full external API access` as a frontend preset that submits expanded explicit scopes, not a backend wildcard.

## API Contract for the Settings UI

The frontend should call admin routes through the shared API client and TanStack Query. Add one convenience endpoint for the primary create-key flow, while keeping the lower-level API-client and token endpoints for listing, editing, rotation, and revocation.

### API clients vs. API tokens

API clients and API tokens are separate on purpose.

An **API client** is the durable service-account principal: for example `production-ci`, `incident-bot`, or `internal-release-tool`. It is the named actor admins see in Settings and audit logs. Disabling the client disables the whole integration.

An **API token** is one disposable secret credential for that client. Tokens are what callers put in `Authorization: Bearer 143_sk_...`. They can be rotated, revoked, expired, and scoped independently without changing the integration identity.

This separation is worth keeping because it solves operational cases that a single token table would make awkward:

- **Rotation without identity churn:** create a new token for `production-ci`, deploy it, revoke the old token, and keep audit history under the same client.
- **Multiple environments:** one client can own `staging`, `production`, and emergency fallback tokens with different prefixes, last-used metadata, expirations, scopes, or repository allowlists.
- **Disable-all control:** disabling the client stops every credential for that integration at once.
- **Cleaner audit logs:** external actions can attribute to the stable client plus the specific token prefix, instead of treating each rotated token as a new actor.
- **Future policy attachment:** owner/contact, trusted IPs, rate-limit tiers, or repository defaults belong naturally on the client while secret lifecycle remains on tokens.

So yes, the separation is necessary for an admin-facing API-key product. A token is the secret. A client is the machine identity that owns those secrets.

The product UI should still keep this lightweight. Users should land on **API keys**, not a conceptual tutorial about two backend tables. The first creation flow should be "Create API key" and can create both the API client and its first token in sequence behind one dialog when the admin is setting up a new integration. The client/token split should appear as simple operational grouping:

- Client row: the integration or service account, such as `production-ci`.
- Token row: a concrete key under that integration, such as `production`, `staging`, or `rotation-2026-06`.

This matches the direction of modern API platforms without copying their complexity into the first-run experience. Stripe exposes restricted keys and rotation/IP controls directly on keys. OpenAI scopes keys under projects and supports service-account ownership. Datadog separates organization API keys from scoped application keys, including service-account-owned keys. Slack and GitHub model durable app identities separately from the tokens those apps or users receive. The common pattern is not "one flat forever-token per user"; it is durable identity plus disposable, scoped credentials. Our UI should present the simplest version of that pattern.

### Create API key

```http
POST /api/v1/api-keys
Content-Type: application/json
```

This is the primary UI endpoint. It creates an API client and its first token in one database transaction.

Request:

```json
{
  "integration_name": "production-ci",
  "description": "GitHub Actions deploy workflow",
  "token_name": "production",
  "scopes": ["sessions:create", "sessions:read"],
  "repository_ids": ["00000000-0000-0000-0000-000000000100"],
  "expires_at": "2027-06-12T00:00:00Z",
  "allowed_ip_cidrs": ["203.0.113.10/32"]
}
```

Response:

```json
{
  "data": {
    "client": {
      "id": "00000000-0000-0000-0000-000000000000",
      "org_id": "00000000-0000-0000-0000-000000000001",
      "name": "production-ci",
      "description": "GitHub Actions deploy workflow",
      "status": "enabled",
      "created_at": "2026-06-12T00:00:00Z",
      "updated_at": "2026-06-12T00:00:00Z"
    },
    "token": {
      "id": "00000000-0000-0000-0000-000000000010",
      "api_client_id": "00000000-0000-0000-0000-000000000000",
      "name": "production",
      "token": "143_sk_...",
      "token_prefix": "143_sk_abcd",
      "scopes": ["sessions:create", "sessions:read"],
      "repository_ids": ["00000000-0000-0000-0000-000000000100"],
      "expires_at": "2027-06-12T00:00:00Z",
      "allowed_ip_cidrs": ["203.0.113.10/32"],
      "created_at": "2026-06-12T00:00:00Z"
    }
  }
}
```

Rules:

- Transactional: if token creation fails, the API client is not committed.
- Raw `token` is returned only in this response.
- `allowed_ip_cidrs` is optional. Empty or omitted means any source IP.
- The frontend can still call lower-level endpoints when adding another token to an existing integration.

Errors:

| Status | Code | Meaning |
|---:|---|---|
| `400` | `INVALID_JSON` | Body cannot be decoded. |
| `400` | `MISSING_FIELD` | Integration name, token name, or scopes are missing. |
| `400` | `INVALID_SCOPE` | A requested scope is unsupported. |
| `400` | `INVALID_IP_ALLOWLIST` | One or more IP/CIDR values are invalid. |
| `401` | `UNAUTHORIZED` | Browser session missing. |
| `403` | `FORBIDDEN` | Caller is not an org admin. |

### List API keys

```http
GET /api/v1/api-keys
```

Response:

```json
{
  "data": [
    {
      "id": "00000000-0000-0000-0000-000000000000",
      "org_id": "00000000-0000-0000-0000-000000000001",
      "name": "production-ci",
      "description": "Deploy pipeline",
      "status": "enabled",
      "created_by_user_id": "00000000-0000-0000-0000-000000000002",
      "disabled_by_user_id": null,
      "disabled_at": null,
      "created_at": "2026-06-12T00:00:00Z",
      "updated_at": "2026-06-12T00:00:00Z"
    }
  ]
}
```

### Update or disable API key integration

```http
PATCH /api/v1/api-keys/{id}
DELETE /api/v1/api-keys/{id}
```

`PATCH` request:

```json
{
  "name": "production-ci",
  "description": "Main deployment pipeline",
  "status": "disabled"
}
```

`DELETE` disables the client and all its tokens by setting disabled state on the client. It does not hard-delete rows.

### List tokens for a client

```http
GET /api/v1/api-keys/{id}/tokens
```

Response:

```json
{
  "data": [
    {
      "id": "00000000-0000-0000-0000-000000000010",
      "org_id": "00000000-0000-0000-0000-000000000001",
      "api_client_id": "00000000-0000-0000-0000-000000000000",
      "name": "production",
      "token_prefix": "143_sk_abcd",
      "scopes": ["sessions:create", "sessions:read"],
      "repository_ids": [],
      "allowed_ip_cidrs": [],
      "expires_at": null,
      "last_used_at": null,
      "last_used_ip": null,
      "last_used_user_agent": null,
      "revoked_by_user_id": null,
      "revoked_at": null,
      "created_by_user_id": "00000000-0000-0000-0000-000000000002",
      "created_at": "2026-06-12T00:00:00Z"
    }
  ]
}
```

The response must never include `token_hash` or the raw token.

### Create token

```http
POST /api/v1/api-keys/{id}/tokens
Content-Type: application/json
```

Request:

```json
{
  "name": "production",
  "scopes": ["sessions:create", "sessions:read"],
  "repository_ids": ["00000000-0000-0000-0000-000000000100"],
  "expires_at": "2027-06-12T00:00:00Z",
  "allowed_ip_cidrs": ["203.0.113.10/32"]
}
```

Response:

```json
{
  "data": {
    "id": "00000000-0000-0000-0000-000000000010",
    "api_client_id": "00000000-0000-0000-0000-000000000000",
    "name": "production",
    "token": "143_sk_...",
    "token_prefix": "143_sk_abcd",
    "scopes": ["sessions:create", "sessions:read"],
    "repository_ids": ["00000000-0000-0000-0000-000000000100"],
    "allowed_ip_cidrs": ["203.0.113.10/32"],
    "expires_at": "2027-06-12T00:00:00Z",
    "created_at": "2026-06-12T00:00:00Z"
  }
}
```

The raw `token` field is shown only in this response. The frontend must hold it only in component state for the one-time reveal dialog and must not persist it into React Query cache beyond the mutation result needed for display.

### Revoke token

```http
DELETE /api/v1/api-keys/{id}/tokens/{token_id}
```

Response: `204 No Content`

Revoked tokens remain listed with `revoked_at` so admins can audit past credentials.

## Frontend Product Shape

### Navigation

Add a Settings page:

```text
Settings
  ...
  API keys
```

Recommended route:

```text
/settings/api-keys
```

This page is admin-only. If the settings nav is visible to non-admins, the item should either be hidden or route to the standard insufficient-permissions state used elsewhere in Settings. Prefer hiding the nav item for non-admins if role information is already available in the layout; otherwise rely on the backend 403 and render an error state.

### Page responsibilities

The page should let admins:

- See all API-key integrations.
- See whether an integration is enabled or disabled.
- Create an API key in one guided flow.
- Edit integration name/description.
- Disable an integration.
- List tokens under each client.
- Create a token with scopes, repository restrictions, and optional expiry.
- Copy the newly created token from a one-time reveal dialog.
- Revoke a token.
- Identify tokens by prefix, scopes, repository restrictions, created date, expiry, and last-used metadata.

### Information architecture

Use a dense settings-management layout rather than a marketing-style card grid. Each integration/client is a repeated item with a compact header and a nested token table/list.

Desktop:

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│ Settings / API keys                                                [New key] │
│ Create service-account keys for internal tools, CI, and automations.         │
├──────────────────────────────────────────────────────────────────────────────┤
│ Search integrations...                                 Status: [All v]       │
│                                                                              │
│ ┌──────────────────────────────────────────────────────────────────────────┐ │
│ │ production-ci                                      Enabled   [⋯]         │ │
│ │ GitHub Actions deploy workflow                                           │ │
│ │ Created Jun 12, 2026 · 2 active tokens · Last used 3h ago                │ │
│ │                                                                          │ │
│ │ Tokens                                            [Create token]         │ │
│ │ ┌────────────┬─────────────┬──────────────┬────────────┬──────────────┐ │ │
│ │ │ Name       │ Prefix      │ Scopes       │ Repos      │ Last used    │ │ │
│ │ ├────────────┼─────────────┼──────────────┼────────────┼──────────────┤ │ │
│ │ │ production │ 143_sk_abcd │ 4 scopes     │ 2 repos    │ 3h ago       │ │ │
│ │ │ staging    │ 143_sk_wxyz │ 2 scopes     │ All repos  │ Never        │ │ │
│ │ └────────────┴─────────────┴──────────────┴────────────┴──────────────┘ │ │
│ └──────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│ ┌──────────────────────────────────────────────────────────────────────────┐ │
│ │ legacy-preview-sync                              Disabled  [⋯]           │ │
│ │ Disabled Jun 8, 2026 · tokens no longer authenticate                     │ │
│ └──────────────────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────────────────┘
```

Mobile:

```text
┌──────────────────────────────┐
│ API keys                     │
│ Service-account access.      │
│ [New key]                    │
├──────────────────────────────┤
│ Search integrations...       │
│ Status [All v]               │
│                              │
│ production-ci        Enabled │
│ GitHub Actions deploy...     │
│ 2 active tokens              │
│ Last used 3h ago             │
│ [Create token] [⋯]           │
│                              │
│ production                   │
│ 143_sk_abcd · 4 scopes       │
│ 2 repos · Last used 3h ago   │
│ [Revoke]                     │
│                              │
│ staging                      │
│ 143_sk_wxyz · 2 scopes       │
│ All repos · Never used       │
│ [Revoke]                     │
└──────────────────────────────┘
```

### Empty state

```text
┌──────────────────────────────────────────────────────────────┐
│                            key icon                          │
│                     No API keys                              │
│ Create a service account for CI, internal tools, or backend   │
│ workflows that need to start sessions or run automations.     │
│                                                              │
│                         [Create API key]                      │
└──────────────────────────────────────────────────────────────┘
```

### Create API key dialog

The primary action should be `Create API key`, not `Create API client`. The dialog calls `POST /api/v1/api-keys`, which atomically creates the durable integration/client and its first token.

```text
┌──────────────────────────────────────────────┐
│ Create API key                          [x] │
├──────────────────────────────────────────────┤
│ Integration name                             │
│ [production-ci___________________________]   │
│                                              │
│ Description                                  │
│ [Used by GitHub Actions deploy workflow__]   │
│                                              │
│ Token name                                   │
│ [production_______________________________]  │
│                                              │
│ Expiration                                   │
│ (•) 1 year  ( ) 90 days  ( ) Custom          │
│ ( ) No expiration                            │
│                                              │
│ Scopes                                       │
│ [scope controls from Create token dialog]    │
│                                              │
│ Advanced security                            │
│ [ ] Restrict by source IP                    │
│     [203.0.113.10/32_____________________]   │
├──────────────────────────────────────────────┤
│                         [Cancel] [Create key]│
└──────────────────────────────────────────────┘
```

Validation:

- Integration name is required.
- Token name is required.
- At least one scope is required.
- Default expiration is `1 year`.
- `No expiration` is allowed but should show higher-risk helper copy.
- If source IP restriction is enabled, every value must be a valid IP or CIDR.
- `description` is optional.
- On success, invalidate the API-client list, open the created client row, and show the one-time token reveal.

### Edit integration dialog

Editing the durable integration identity can use a smaller dialog:

```text
┌──────────────────────────────────────────────┐
│ Edit integration                       [x] │
├──────────────────────────────────────────────┤
│ Name                                         │
│ [production-ci___________________________]   │
│                                              │
│ Description                                  │
│ [Used by GitHub Actions deploy workflow__]   │
├──────────────────────────────────────────────┤
│                                [Cancel] [Save]│
└──────────────────────────────────────────────┘
```

### Create token dialog

Use a dialog on desktop and the shared responsive modal/bottom-sheet pattern on mobile. The scope controls should be checkboxes grouped by product area, not a free-form text input.

```text
┌──────────────────────────────────────────────────────────────┐
│ Create token for production-ci                          [x]  │
├──────────────────────────────────────────────────────────────┤
│ Token name                                                   │
│ [production______________________________________________]   │
│                                                              │
│ Expiration                                                   │
│ (•) 1 year   ( ) 90 days   ( ) Custom [YYYY-MM-DD]           │
│ ( ) No expiration                                            │
│                                                              │
│ Repository access                                            │
│ (•) All repositories                                         │
│ ( ) Selected repositories                                    │
│     [Search repositories...]                                 │
│     [ ] assembledhq/web                                      │
│     [ ] assembledhq/api                                      │
│                                                              │
│ Access                                                       │
│ ( ) Full external API access                                 │
│ (•) Custom scopes                                            │
│                                                              │
│ Scopes                                                       │
│ Presets                                                      │
│ [ ] Sessions all  [ ] Automations all  [ ] Previews all      │
│                                                              │
│ Sessions                                                     │
│ [x] Create sessions      [x] Read sessions                   │
│ [ ] Send messages/retry  [ ] Cancel/end sessions             │
│ [ ] Publish PRs/branches                                     │
│                                                              │
│ Automations                                                  │
│ [ ] Create automations   [ ] Read automations                │
│ [ ] Update/pause/resume  [ ] Run automations                 │
│                                                              │
│ Previews                                                     │
│ [ ] Create previews      [ ] Read previews                   │
│ [ ] Stop/restart previews                                    │
│                                                              │
│ Advanced security                                            │
│ [ ] Restrict by source IP                                    │
│     [203.0.113.10/32_____________________________________]   │
├──────────────────────────────────────────────────────────────┤
│                                  [Cancel] [Create token]     │
└──────────────────────────────────────────────────────────────┘
```

Validation:

- `name` is required.
- At least one scope is required.
- Default expiration is `1 year`.
- Custom expiration must be a valid future date converted to RFC3339.
- `No expiration` is allowed but should show helper copy: `Use no expiration only for keys stored in a managed secret system with a rotation process.`
- If selected-repository mode is chosen, at least one repository is required.
- If source IP restriction is enabled, every value must be a valid IP or CIDR.

Scope labels:

| UI label | Scope |
|---|---|
| Full external API access | UI preset that expands to every explicit scope listed below |
| Sessions all | `sessions:all` |
| Read sessions | `sessions:read` |
| Create sessions | `sessions:create` |
| Send messages/retry | `sessions:write` |
| Cancel/end sessions | `sessions:cancel` |
| Publish PRs/branches | `sessions:publish` |
| Automations all | `automations:all` |
| Read automations | `automations:read` |
| Create automations | `automations:create` |
| Update/pause/resume | `automations:write` |
| Run automations | `automations:run` |
| Previews all | `previews:all` |
| Read previews | `previews:read` |
| Create previews | `previews:create` |
| Stop/restart previews | `previews:stop` |

The create-token dialog should expose full access as a top-level segmented or checkbox control above product-area scopes:

```text
Access
( ) Full external API access
(•) Custom scopes
```

When `Full external API access` is selected:

- Submit every currently supported explicit scope.
- Disable or collapse the individual product-area scope checkboxes.
- Keep repository access controls visible because repository allowlists still constrain all-access tokens.
- Show helper copy: `Full external API access includes all current external API scopes. New future endpoint groups may require updating or rotating this key.`

When a resource-family preset is selected:

- Submit the family scope, such as `sessions:all`.
- Disable or visually check the individual scopes it satisfies within that family.
- Keep other families configurable.
- Show helper copy: `Family all scopes grant every supported action in this API family. New actions in this family require an explicit backend authorization test before they are covered.`

### One-time token reveal

After successful token creation, show a blocking dialog that makes the one-time nature explicit.

```text
┌──────────────────────────────────────────────────────────────┐
│ Copy API token                                           [x] │
├──────────────────────────────────────────────────────────────┤
│ This token is shown once. Store it in your secret manager     │
│ before closing this dialog.                                  │
│                                                              │
│ ┌──────────────────────────────────────────────────────────┐ │
│ │ 143_sk_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx      │ │
│ └──────────────────────────────────────────────────────────┘ │
│                                      [Copy]                  │
│                                                              │
│ Authorization header                                         │
│ ┌──────────────────────────────────────────────────────────┐ │
│ │ Authorization: Bearer 143_sk_xxxxxxxxxxxxxxxxxxxxxxxxx   │ │
│ └──────────────────────────────────────────────────────────┘ │
│                                      [Copy]                  │
│                                                              │
│ Quickstart                                                   │
│ curl https://143.dev/api/v1/sessions \                      │
│   -H "Authorization: Bearer 143_sk_..." \                   │
│   -H "Content-Type: application/json"                       │
│                                      [Copy curl]             │
│                                                              │
│ Prefix: 143_sk_abcd                                          │
│ Scopes: sessions:create, sessions:read                       │
│ Repositories: assembledhq/web                                │
│                                                              │
│ [External API docs] [Raw Markdown] [llms.txt]                │
├──────────────────────────────────────────────────────────────┤
│                                           [I have saved it]  │
└──────────────────────────────────────────────────────────────┘
```

Requirements:

- The raw token should not appear in list rows after the dialog closes.
- Copy action uses the existing clipboard pattern and surfaces errors.
- Include copy buttons for the raw token, Authorization header, and a minimal curl example.
- Pick the curl example from selected scopes: session create first, then automation run, then preview create/read.
- Include links to the external API reference, raw Markdown docs, and `/llms.txt` so human users and coding agents can immediately ingest the contract.
- Closing with `x` should be allowed but use the same clear final action copy. Do not add a browser-blocking confirmation unless the app already has a shared pattern for this.

### Integration actions menu

```text
[⋯]
  Edit details
  Create token
  Disable integration
```

For disabled integrations:

```text
[⋯]
  Edit details
```

Do not support re-enable in v1 unless backend semantics are confirmed. If re-enable is desired later, add an explicit `PATCH status=enabled` flow and tests.

### Token row actions

Active token:

```text
[Revoke]
```

Revoked token:

```text
Revoked Jun 12, 2026
```

Expired token:

```text
Expired Jun 12, 2027
```

Revoke confirmation:

```text
┌──────────────────────────────────────────────┐
│ Revoke token?                           [x] │
├──────────────────────────────────────────────┤
│ production (143_sk_abcd) will stop working   │
│ immediately. This cannot be undone.           │
├──────────────────────────────────────────────┤
│                         [Cancel] [Revoke]    │
└──────────────────────────────────────────────┘
```

### Repository picker

The create-token dialog can use the existing repository list endpoint:

```http
GET /api/v1/repositories
```

Use the same repository display label used elsewhere in the app. If the repository list is large, use local search over the loaded list in v1. Add server-side repository search only if performance requires it.

## Frontend Implementation Plan

Follow test-first development.

1. Add types in `frontend/src/lib/types.ts`:
   - `APIClientStatus`
   - `APIClient`
   - `APIToken`
   - `CreateAPITokenResponse`
   - `CreateAPIKeyResponse`
   - request body helper types if that matches local conventions.
2. Add API methods in `frontend/src/lib/api.ts`, preferably under `api.apiClients`:
   - `createKey` (`POST /api/v1/api-keys`)
   - `list`
   - `create`
   - `get`
   - `update`
   - `disable`
   - `listTokens`
   - `createToken`
   - `revokeToken`
3. Add MSW handlers for API-key/API-client routes in `frontend/src/test/mocks/handlers.ts`.
4. Add `frontend/src/app/(dashboard)/settings/api-keys/page.test.tsx` before implementation.
5. Implement `frontend/src/app/(dashboard)/settings/api-keys/page.tsx`.
6. Add a settings-nav entry and update settings page/index tests.
7. Ensure preview settings copy links or points toward the new API Keys page rather than only saying "prefer external API clients."

### Frontend tests

Tests should cover:

- Empty state renders and opens the create-key dialog.
- Client list renders enabled, disabled, active token, revoked token, expired token, and never-used states.
- Create key posts the expected atomic `/api/v1/api-keys` body, then invalidates/refetches the list.
- Create token requires a name and at least one scope before mutation.
- Create token posts scopes, repository IDs, allowed IP CIDRs, and RFC3339 expiration.
- Full external API access expands to every explicit scope, not a wildcard.
- Family scopes (`sessions:all`, `automations:all`, `previews:all`) satisfy only their documented explicit scopes.
- Adding a new required scope without updating family-scope tests should fail authorization tests.
- Default expiration is one year; no-expiration mode shows higher-risk helper copy.
- Raw token is displayed only in the one-time reveal dialog after successful mutation.
- Reveal dialog renders copyable raw token, Authorization header, curl example, docs link, raw Markdown link, and `/llms.txt` link.
- Revoke token calls the correct route and refetches token list.
- Disable client calls the correct route and updates list state.
- Repository-restricted token creation requires selected repositories.
- IP-restricted token creation requires valid IP/CIDR values.
- Backend error responses are surfaced through the existing error UI pattern.

## Fumadocs Update Plan

Update `docs/public/reference/external-api.mdx` into a complete v1 reference.

Recommended sections:

1. **Overview**
   - Explain service-account API clients and `143_sk_` bearer tokens.
   - State that external tokens use selected `/api/v1` routes, not the full app API.
2. **Get an API key**
   - Dashboard path: Settings -> API keys.
   - Create API key, save token once.
   - Explain token prefixes, revocation, expiry, disabled integrations, repository restrictions, and optional IP restrictions.
3. **Authentication**
   - `Authorization: Bearer 143_sk_...`
   - `143-Version` optional client-version header.
4. **Scopes**
   - List every supported scope with plain-English meaning.
   - Explain that `Full external API access` is a UI preset that expands to every current explicit scope; it is not a wildcard for future endpoint groups.
   - Explain `sessions:all`, `automations:all`, and `previews:all` as resource-family scopes.
5. **Endpoint scope table**
   - Mirror the route map in this design.
   - Include both required explicit scopes and accepted family scopes for each endpoint.
6. **Repository restrictions**
   - Empty `repository_ids` means all repositories.
   - Non-empty means calls that name or resolve a repository must stay inside the allowlist.
   - For preview list calls, repository-scoped tokens must include `repository_id`.
7. **IP restrictions**
   - Empty `allowed_ip_cidrs` means requests from any source IP are allowed.
   - Non-empty values must be IP or CIDR strings.
   - Requests from outside the allowlist return a structured auth error.
8. **Idempotency**
   - Header, 24-hour retention, same-key behavior.
   - Document currently idempotent POST routes:
     - `POST /api/v1/sessions`
     - `POST /api/v1/sessions/{id}/messages`
     - `POST /api/v1/sessions/{id}/pr`
     - `POST /api/v1/sessions/{id}/branch`
     - `POST /api/v1/automations`
     - `POST /api/v1/automations/{id}/run`
     - `POST /api/v1/previews`
     - `POST /api/v1/previews/{id}/restart`
9. **Rate limits**
   - Reads: 600 requests/minute per token.
   - Mutations: 120 requests/minute per token.
   - Headers: `X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`, `Retry-After`.
10. **Sessions**
   - Create, list, get, send message, retry, cancel/end, publish branch/PR.
   - Include request/response examples where shapes are stable.
11. **Automations**
   - Create, list/get, update/delete, run, pause/resume.
   - Clarify external-created automations must use `identity.scope=org`.
12. **Previews**
   - Create, list/get, stop, restart.
   - Clarify the difference between general API tokens and legacy preview-only tokens.
13. **Errors**
   - Standard `{error:{code,message,details}}` shape.
   - Common codes: `UNAUTHORIZED`, `FORBIDDEN`, `API_CLIENT_DISABLED`, `RATE_LIMITED`, `IDEMPOTENCY_KEY_REUSED`, `REPOSITORY_ID_REQUIRED`, `IP_NOT_ALLOWED`.
14. **For coding agents**
   - One compact copyable block with base URL, auth header, idempotency guidance, endpoint/scope table link, and minimal examples.
   - Link to raw Markdown and `/llms.txt`.
15. **Machine-readable reference**
   - Publish or prepare for `docs/public/reference/openapi.json`.
   - If OpenAPI generation is not part of this iteration, maintain explicit request/response schema tables in the MDX and list OpenAPI as the next step.

Docs should favor exact endpoint tables over broad statements like "same `/api/v1` resource paths as the application API" unless immediately qualified.

## Public Docs Tests

The existing public-docs tests should continue to pass. If the docs page gains new internal links, add or update tests only when the current test suite requires metadata/link coverage.

At minimum:

- Keep frontmatter fields present.
- Keep the page listed in `docs/public/reference/meta.json`.
- Keep examples free of raw production host assumptions; use `https://your-143.example.com` or `https://143.dev` consistently with the rest of docs.

## Security and Audit

- Raw tokens are visible exactly once.
- Raw tokens are never logged, cached in persistent browser storage, or included in later list responses.
- Disable and revoke flows must use confirmation dialogs because they immediately break integrations.
- API-client and token management remains admin-only.
- External API token calls are audited as API actions with client and token identity.
- UI must show enough metadata for rotation decisions: prefix, created date, last used date, last used IP/user agent, expiration, revoked state.
- Optional IP restrictions are enforced before route scope checks. Tokens with `allowed_ip_cidrs` reject requests from outside the allowlist.
- Default token expiration is one year. No-expiration keys are allowed but should be visually marked as higher risk.

## Rollout

1. Ship UI behind normal admin settings access. No feature flag is required if backend routes are already live.
2. Update Fumadocs in the same PR so the Settings page can link to the reference.
3. Keep Preview settings legacy-token UI in place, but update copy to point new integrations to Settings -> API keys.
4. After adoption, consider a migration path from preview-only tokens to general API tokens with `previews:*` scopes.

## Open Questions

- Should disabled API clients be re-enableable in v1, or is disable intentionally terminal?
- Should repository allowlists be enforced at the client level as defaults in addition to token-level restrictions?
- Should OpenAPI be generated from Go route/types in this iteration, or should v1 start with manually maintained schema tables and add generated OpenAPI once the external API surface stabilizes?
