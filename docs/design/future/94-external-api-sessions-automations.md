# 94 - External API for Sessions and Automations

> **Status:** Future design
> **Last reviewed:** 2026-06-05
>
> **Depends on:** organization membership auth, audit logs, session creation APIs, automation APIs, job queue idempotency, and public docs/API reference.

## Problem

143 has an early preview API for branch previews, backed by preview-scoped bearer tokens. That token model is useful proof that external callers can authenticate without browser cookies, but it is too narrow to become the official API surface for sessions, automations, and future resources.

The official external API should let customers and integrations create coding sessions, inspect their state, create/manage automations, trigger automation runs, and later control previews and PR actions from CI, internal tools, Slack-like surfaces, or customer-owned orchestration.

The API needs to feel familiar to developers who already use OpenAI, Anthropic, Vercel, Stripe, GitHub, and similar platforms:

- Opaque bearer keys passed in the `Authorization` header.
- Keys shown once at creation time, never retrievable later.
- Scoped permissions rather than one all-powerful secret.
- Stable JSON response envelopes and structured errors.
- Idempotency for retryable mutations.
- Clear rate limits and audit trails.
- Versioning that can preserve behavior as the API grows.

## Goals

- Promote external access from preview-only to a general official API for sessions and automations.
- Introduce a durable auth model based on org-scoped API clients/service accounts, not human browser sessions.
- Keep all external API access org-scoped and compatible with the existing multi-tenancy invariant.
- Make request and response shapes consistent with the existing `/api/v1` envelope conventions.
- Support safe retries for create/run operations through idempotency keys.
- Keep preview API compatibility while establishing a path to migrate preview-specific tokens into the general API token model.
- Publish enough API shape in design now that implementation can proceed test-first without repeatedly revisiting naming.

## Non-goals

- Do not expose every existing authenticated product route as a public API.
- Do not support OAuth apps or third-party delegated OAuth in v1. Bearer API keys are sufficient for server-to-server integrations.
- Do not create user personal access tokens in v1. Human-scoped tokens can be added later if CLI workflows need them.
- Do not allow API tokens to bypass repository, org, role, or product guardrails.
- Do not make a separate API product deployment unless operational needs justify it.

## Domain Decision

Use the existing product domain for v1:

```text
https://143.dev/api/v1
```

Do not create `api.143.dev` yet.

Using the existing domain is sufficient because:

- The API already lives under `/api/v1`; adding a bearer-token route group preserves the current app shape.
- Browser-cookie routes and external bearer-token routes can be separated by middleware rather than hostname.
- CSRF can remain required for cookie-authenticated browser mutations while explicitly skipped for bearer-token external API routes.
- Existing reverse proxy, observability, deployment, and public docs links stay simpler.
- Customers can copy examples using one canonical base URL.

Reserve `api.143.dev` for a future operational split if one of these becomes true:

- API traffic needs independent rate-limit, WAF, CDN, or regional routing policy.
- Public API uptime needs to be decoupled from frontend/app-shell deploys.
- Enterprise customers require a clearly separate API hostname for egress allowlists.
- API versioning or gateway concerns become complex enough to justify a dedicated edge layer.
- OAuth/OIDC, app marketplace callbacks, or event ingestion require a different trust boundary from the web app.

If a future split happens, `https://api.143.dev/v1` should be an alias for the same API contract. The path contract should not depend on the hostname.

## API Auth Model

### API clients

Add an org-scoped `api_clients` resource. This is the durable actor for machine access.

Conceptually this is a service account:

- It belongs to exactly one organization.
- It has a display name and optional description.
- It can be enabled or disabled.
- It owns one or more API tokens.
- It is the actor recorded in audit logs for external API calls.
- It is created by a human admin, but it must not stop working merely because that admin leaves the organization.

The token is the secret credential. The API client is the non-secret principal that the credential authenticates as.

Keeping these separate gives us:

- **Rotation without identity churn:** CI can create a replacement token, deploy it, and revoke the old token while audit logs and ownership still point to the same `production-ci` client.
- **Multiple credentials for one integration:** staging, production, and fallback deployment systems can each have separate tokens with separate `last_used_at` and revocation state while sharing one integration identity.
- **Cleaner audit attribution:** session and automation events can say "created by API client production-ci" rather than by whichever token happened to be active that day.
- **Disable-all control:** an admin can disable the API client once to stop every token for that integration, instead of finding and revoking tokens one by one.
- **Future policy attachment:** client-level defaults such as owner team, contact, allowed repositories, rate-limit tier, webhook subscriptions, or trusted IP rules can be added without changing every token row.
- **Safer creator lifecycle:** the API client remains an org-owned machine actor even if the human who created the first token leaves the organization.

If v1 stored only tokens, those concerns would either be duplicated onto every token or become ambiguous when a customer rotates keys. The client abstraction is intentionally small, but it gives the API a stable principal separate from the disposable secret.

This improves on the current preview token behavior, where a token is tied to the creator's active membership. That creator check is reasonable for preview-only tokens, but it is brittle for CI/CD and long-lived integrations.

### API tokens

Add `api_tokens` owned by `api_clients`.

Token behavior:

- Plaintext token is returned only once from the creation endpoint.
- Store only a hash at rest.
- Prefix tokens with `143_sk_` for general API tokens.
- Include a short public key ID/prefix in responses so users can identify a token without revealing it.
- Support `expires_at` but do not require it in v1.
- Support `last_used_at`, `last_used_ip`, and `last_used_user_agent` for admin review.
- Support `revoked_at`.
- Support scopes and optional repository allowlists.

Recommended plaintext format:

```text
143_sk_<base64url-random-32-bytes>
```

Do not encode org IDs, token IDs, scopes, or timestamps into the token itself. Token metadata belongs in the database so revocation and scope changes are immediately authoritative.

### Headers

External API requests use:

```http
Authorization: Bearer 143_sk_...
Content-Type: application/json
Idempotency-Key: optional-client-generated-key
143-Version: optional-date-version
```

`143-Version` is optional in v1. Add it before any breaking semantic changes are introduced. If absent, requests use the default behavior for the deployed `/api/v1` contract.

### Scopes

Scopes should be resource/action strings:

```text
sessions:read
sessions:create
sessions:write
sessions:cancel
sessions:publish
automations:read
automations:create
automations:write
automations:run
previews:read
previews:create
previews:stop
```

Avoid wildcard scopes in v1. Explicit scopes make admin review easier and prevent accidentally widening older integrations when new resources are added.

Repository allowlists apply after scope checks. A token with `sessions:create` and `repository_ids = [repo-a]` can create sessions only for `repo-a`.

### Role and guardrail mapping

External API tokens should not reuse human roles like `admin`, `member`, `builder`, or `viewer` in request context. Add an API-client context identity and require handlers to check scopes.

For operations that already have product guardrails, the API should still call the same service path:

- PR creation still honors builder/member/admin shipping guardrails and review requirements.
- Session creation still validates repository ownership and active repository state.
- Automation creation still validates schedule, identity scope, model, repo, and concurrency settings.
- Personal credential use from service-account-created automations is not allowed unless a human identity is explicitly attached by a future delegated-auth feature.

In v1, API-client-created sessions and automations should default to org-scoped coding credentials.

### Token management endpoints

Token and API-client management uses browser cookie auth and admin role checks. External bearer tokens cannot create or escalate other tokens.

```http
GET    /api/v1/api-clients
POST   /api/v1/api-clients
GET    /api/v1/api-clients/{id}
PATCH  /api/v1/api-clients/{id}
DELETE /api/v1/api-clients/{id}

GET    /api/v1/api-clients/{id}/tokens
POST   /api/v1/api-clients/{id}/tokens
DELETE /api/v1/api-clients/{id}/tokens/{token_id}
```

Create token request:

```json
{
  "name": "production-ci",
  "scopes": ["sessions:create", "sessions:read", "automations:run"],
  "repository_ids": ["00000000-0000-0000-0000-000000000000"],
  "expires_at": "2027-06-05T00:00:00Z"
}
```

Create token response:

```json
{
  "data": {
    "id": "00000000-0000-0000-0000-000000000000",
    "api_client_id": "00000000-0000-0000-0000-000000000001",
    "name": "production-ci",
    "token": "143_sk_...",
    "token_prefix": "143_sk_abcd",
    "scopes": ["sessions:create", "sessions:read", "automations:run"],
    "repository_ids": ["00000000-0000-0000-0000-000000000000"],
    "expires_at": "2027-06-05T00:00:00Z",
    "created_at": "2026-06-05T00:00:00Z"
  }
}
```

List responses must never include `token`.

## Idempotency

Support `Idempotency-Key` on external mutating endpoints that create resources or enqueue work:

- `POST /api/v1/sessions`
- `POST /api/v1/sessions/{id}/messages`
- `POST /api/v1/sessions/{id}/pr`
- `POST /api/v1/sessions/{id}/branch`
- `POST /api/v1/automations`
- `POST /api/v1/automations/{id}/run`
- Future preview create/restart endpoints.

Persist idempotency records by:

```text
org_id
api_client_id
token_id
method
path
idempotency_key
request_body_hash
response_status
response_body
created_at
expires_at
```

Rules:

- Same key + same method/path/body returns the original response.
- Same key + different body returns `409 IDEMPOTENCY_KEY_REUSED`.
- Retain records for at least 24 hours.
- Only external bearer-token requests require this store. Cookie-authenticated product routes can remain unchanged unless they later opt in.

## Error Shape

Use the existing error envelope:

```json
{
  "error": {
    "code": "INVALID_SCOPE",
    "message": "API token is not allowed to create sessions",
    "details": {
      "required_scope": "sessions:create"
    }
  }
}
```

External API errors should include stable machine-readable codes. Avoid leaking internal DB, queue, provider, prompt, or secret details.

Recommended auth errors:

- `UNAUTHORIZED`: missing, malformed, unknown, expired, or revoked bearer token.
- `FORBIDDEN`: token is valid but lacks scope or repository access.
- `API_CLIENT_DISABLED`: token belongs to a disabled API client.
- `INVALID_SCOPE`: token creation requested an unsupported scope.
- `IDEMPOTENCY_KEY_REUSED`: same idempotency key was reused with different request content.

## Rate Limits

Add external API-specific rate limits rather than reusing browser IP defaults.

Recommended v1 defaults:

- Per API token: 60 requests/minute.
- Per org: 600 requests/minute across API tokens.
- Mutating endpoints: 30 requests/minute per token.
- Session/automation run creation: lower concurrency-aware limits should still use existing product capacity checks.

Return standard-ish headers:

```http
Retry-After: 10
X-RateLimit-Limit: 60
X-RateLimit-Remaining: 0
X-RateLimit-Reset: 1791139200
```

## Audit Events

Every token management mutation and external resource mutation should emit audit entries.

Add audit resource types:

```text
api_client
api_token
```

Add audit actions:

```text
api_client.created
api_client.updated
api_client.disabled
api_token.created
api_token.revoked
api_token.used
```

`api_token.used` should be sampled or aggregated if per-request audit entries become too noisy. Resource mutations such as `session.created` and `automation.created` should include API client identity in details.

Audit details must not include plaintext tokens, authorization headers, prompts beyond short summaries, uploaded file contents, or secret values.

## Session API

### Endpoints

```http
GET    /api/v1/sessions
POST   /api/v1/sessions
GET    /api/v1/sessions/{id}
POST   /api/v1/sessions/{id}/messages
POST   /api/v1/sessions/{id}/cancel
POST   /api/v1/sessions/{id}/end
POST   /api/v1/sessions/{id}/retry
POST   /api/v1/sessions/{id}/pr
POST   /api/v1/sessions/{id}/branch
GET    /api/v1/sessions/{id}/messages
GET    /api/v1/sessions/{id}/logs
GET    /api/v1/sessions/{id}/diff
GET    /api/v1/sessions/{id}/pr
```

### Create session request

Use `POST /api/v1/sessions` as the official API endpoint. Keep `POST /api/v1/sessions/manual` as an app compatibility route, but do not document it as the public API.

```json
{
  "repository_id": "00000000-0000-0000-0000-000000000000",
  "message": "Fix the checkout crash when the cart contains an expired coupon.",
  "attachments": [],
  "references": [],
  "agent_type": "codex",
  "model": "gpt-5-codex",
  "reasoning_effort": "medium",
  "autonomy_level": "semi",
  "token_mode": "low",
  "target_branch": "main",
  "metadata": {
    "external_id": "support-ticket-123",
    "source": "internal-support-tool"
  }
}
```

Field guidance:

- `repository_id` is required for external API-created sessions.
- `message` is required unless attachments or supported references provide enough starting context.
- `attachments` should initially accept first-party uploaded file references, not raw file bytes.
- `agent_type`, `model`, `reasoning_effort`, `autonomy_level`, and `token_mode` should match existing typed model enums.
- `metadata` is caller-owned, bounded JSON for correlation. It must not drive authorization or scheduling behavior.

### Create session response

```json
{
  "data": {
    "id": "00000000-0000-0000-0000-000000000000",
    "status": "pending",
    "origin": "external_api",
    "repository_id": "00000000-0000-0000-0000-000000000001",
    "title": "Fix checkout crash",
    "created_at": "2026-06-05T00:00:00Z"
  }
}
```

Add `external_api` to the allowed `sessions.origin` values.

## Automation API

### Endpoints

```http
GET    /api/v1/automations
POST   /api/v1/automations
GET    /api/v1/automations/{id}
PATCH  /api/v1/automations/{id}
DELETE /api/v1/automations/{id}

POST   /api/v1/automations/{id}/run
POST   /api/v1/automations/{id}/pause
POST   /api/v1/automations/{id}/resume

GET    /api/v1/automations/{id}/runs
GET    /api/v1/automations/{id}/runs/{run_id}
GET    /api/v1/automations/{id}/stats
```

The existing product endpoints can back these routes, but external auth checks must be scope-aware.

### Create automation request

Prefer grouped request objects for new public API docs. Internally this can adapt to the existing flat `AutomationHandler.Create` shape.

```json
{
  "name": "Weekly dependency cleanup",
  "goal": "Update minor dependencies and open a PR when tests pass.",
  "repository_id": "00000000-0000-0000-0000-000000000000",
  "scope": "Only package manifests and lockfiles.",
  "schedule": {
    "type": "cron",
    "cron": "0 9 * * 1",
    "timezone": "America/Los_Angeles"
  },
  "execution": {
    "mode": "sequential",
    "max_concurrent": 1
  },
  "agent": {
    "type": "codex",
    "model": "gpt-5-codex",
    "reasoning_effort": "medium"
  },
  "pull_request": {
    "base_branch": "main",
    "pre_pr_review_loops": 1
  },
  "identity": {
    "scope": "org"
  },
  "enabled": true,
  "metadata": {
    "external_id": "dep-cleanup"
  }
}
```

Schedule shape:

- Cron schedules use `schedule.type = "cron"`, `schedule.cron`, and `schedule.timezone`.
- Interval schedules use `schedule.type = "interval"`, `schedule.interval_value`, `schedule.interval_unit`, optional `schedule.run_at`, and optional `schedule.timezone`.

### Trigger automation response

```json
{
  "data": {
    "id": "00000000-0000-0000-0000-000000000000",
    "automation_id": "00000000-0000-0000-0000-000000000001",
    "status": "pending",
    "triggered_by": "manual",
    "created_at": "2026-06-05T00:00:00Z"
  }
}
```

Consider adding `triggered_by = "api"` in a future migration if distinguishing product manual runs from external API-triggered runs becomes useful. If added, update `AutomationTriggeredBy` enum and tests.

## Pagination and Filtering

Keep the existing response convention:

```json
{
  "data": [],
  "meta": {
    "next_cursor": "opaque"
  }
}
```

External list endpoints should support:

- `limit`, capped at 100.
- `cursor`.
- Stable filters already supported by the product endpoint.
- RFC3339 time filters where useful, such as `created_after`, `created_before`, `triggered_after`, `triggered_before`.

Cursor values should be opaque. Do not document raw UUID or timestamp internals.

## Data Model Additions

Add:

```sql
api_clients (
  id uuid primary key,
  org_id uuid not null references organizations(id),
  name text not null,
  description text,
  status text not null,
  created_by_user_id uuid references users(id) on delete set null,
  disabled_by_user_id uuid references users(id) on delete set null,
  disabled_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
)
```

```sql
api_tokens (
  id uuid primary key,
  org_id uuid not null references organizations(id),
  api_client_id uuid not null references api_clients(id) on delete cascade,
  name text not null,
  token_hash text not null,
  token_prefix text not null,
  scopes text[] not null,
  repository_ids uuid[] not null default '{}',
  expires_at timestamptz,
  last_used_at timestamptz,
  last_used_ip text,
  last_used_user_agent text,
  revoked_by_user_id uuid references users(id) on delete set null,
  revoked_at timestamptz,
  created_by_user_id uuid references users(id) on delete set null,
  created_at timestamptz not null default now()
)
```

```sql
api_idempotency_keys (
  id uuid primary key,
  org_id uuid not null references organizations(id),
  api_client_id uuid not null references api_clients(id) on delete cascade,
  api_token_id uuid not null references api_tokens(id) on delete cascade,
  idempotency_key text not null,
  method text not null,
  path text not null,
  request_body_hash text not null,
  response_status int,
  response_body jsonb,
  locked_at timestamptz,
  created_at timestamptz not null default now(),
  expires_at timestamptz not null
)
```

Indexes:

- Unique active token hash index on `api_tokens(token_hash)` where `revoked_at is null`.
- `api_tokens(org_id, api_client_id, created_at desc)`.
- Unique idempotency key index on `(org_id, api_client_id, method, path, idempotency_key)`.
- Cleanup index on `api_idempotency_keys(expires_at)`.

All three tables include `org_id` because they are org-scoped product data.

## Router and Middleware Shape

Add a new auth middleware:

```text
ExternalAPIAuth
```

Responsibilities:

- Require `Authorization: Bearer`.
- Hash and lookup token.
- Reject revoked, expired, or disabled-client tokens.
- Attach `org_id`, `api_client`, `api_token`, and external actor metadata to context.
- Do not attach a human user unless a future delegated-user token model exists.
- Apply external API rate limits.
- Skip CSRF.
- Use `LogContext` equivalent fields: `org_id`, `api_client_id`, `api_token_id`, request ID.

Add scope middleware:

```text
RequireAPIScope("sessions:create")
RequireAPIRepositoryAccess(repositoryID)
```

Where possible, keep HTTP handlers thin and move shared create/update behavior into services so browser routes and external API routes do not drift.

## Preview API Migration

Do not remove `preview_api_tokens` immediately.

Recommended path:

1. Implement general `api_clients` and `api_tokens`.
2. Allow general tokens with `previews:*` scopes to call preview endpoints.
3. Keep existing preview tokens working for compatibility.
4. Update the Preview settings UI copy from "Preview API tokens" to "API tokens" once session/automation scopes are available.
5. Add a migration or one-click conversion path later if there are active preview tokens in production.

## Implementation Plan

1. Add this design to `overall.md` as the high-level external API direction.
2. Add models and enum tests for API client status and token scopes.
3. Add migrations for `api_clients`, `api_tokens`, and `api_idempotency_keys`.
4. Add stores with strict `org_id` filters and tests.
5. Add `ExternalAPIAuth`, API context helpers, scope checks, repository allowlist checks, and tests.
6. Add admin token-management handlers and tests.
7. Add idempotency middleware/store and tests.
8. Add external session create/list/get routes backed by existing session services or a newly extracted service.
9. Add external automation create/list/get/run routes backed by automation services or extracted service code.
10. Add audit event coverage.
11. Add public docs/API reference examples after the implementation settles.

## Open Questions

- Should API-client-created sessions be attributed in UI as "143 API" or by API client name, such as "production-ci"?
- Should external API tokens be allowed to create PRs in v1, or should `sessions:publish` wait until session creation and automation run flows are proven?
- Should `triggered_by = "api"` be added now for automation runs, or deferred until reporting needs distinguish API-triggered runs from UI manual runs?
- Should metadata be indexed for `external_id` lookup, or remain opaque JSON until customers ask for reconciliation endpoints?
- Should webhook delivery for session/automation state changes be part of the same API launch or a follow-up design?
