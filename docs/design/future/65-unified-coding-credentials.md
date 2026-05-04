# Design: Unified Coding Credentials

> **Status:** PRs 1–4 landed; PR 5 (cleanup) outstanding | **Last reviewed:** 2026-04-27 (scalability pass)
>
> **Implementation log (2026-04-27):**
>
> **PR 1 — schema & migration tooling (landed):**
> - `migrations/000110_coding_credentials.{up,down}.sql` — unified table, four partial indexes, `coding_credentials_migrations` sentinel.
> - `migrations/000111_copy_coding_credentials.{up,down}.sql` — idempotent SQL data copy from `org_credentials` (with `openai_chatgpt → openai_subscription` rename) and `user_credentials` (team-default rows promoted to org-scoped).
> - `cmd/migrate-coding-credentials-anthropic-split/` — standalone batched encrypted-blob post-step (cursor-paginated by `(created_at, id)`, per-row `statement_timeout`, dry-run flag, sentinel write on completion). `make migrate-coding-credentials-anthropic-split` target.
>
> **PR 2 — store, resolver, dual-write (landed):**
> - `internal/models/coding_credential.go` — `Scope`, `CodingCredential`, `DecryptedCodingCredential`, `CodingCredentialSummary`, `AnthropicSubscriptionConfig`, `OpenAISubscriptionConfig`, `MoveCodingCredentialInput`, plus the new `ProviderAnthropicSubscription` / `ProviderOpenAISubscription` constants and `ParseCodingProviderConfig`. `CodingAgentProviders` extended.
> - `internal/db/coding_credentials.go` — full `CodingCredentialStore`: `Get`, `GetByProviderAndLabel`, `ListByScope`, `ListByProvider`, `ListResolvable` (two-half partial-index seek, no sort), `PickRunnable` (random-with-shedding via in-process LRU health cache), `Create`/`InsertPendingAuth`/`PromotePending`/`UpdateConfig`/`Rename`/`UpdateStatus`/`Disable`/`Reorder`/`Move` — every mutation re-asserts `Scope` in transaction and conflates scope mismatch with not-found. 30s in-process resolver cache with scope-targeted invalidation. Per-(scope, provider) advisory lock around priority allocation. `JanitorDeletePendingAuthOlderThan` for the pending-auth TTL sweep.
> - `internal/db/coding_credentials_mirror.go` — dual-write mirror. `OrgCredentialStore` and `UserCredentialStore` get `SetCodingMirror(...)` and reflect every coding-provider INSERT/UPDATE/DELETE into `coding_credentials` using the legacy row's id as the unified row's id. `mirrorProviderForOrg` translates `openai_chatgpt → openai_subscription` and splits `AnthropicConfig.Subscription` into a separate `anthropic_subscription` row on the way through. Best-effort: a mirror failure logs but never fails the legacy write.
> - `internal/services/agent/orchestrator.go` + `env.go` — `CodingCredentialProvider` interface added; `AgentEnv.resolveProviderConfig` now consults `ListResolvable` first (with subscription-twin fallback so a request for `anthropic` finds `anthropic_subscription` rows automatically) and walks the legacy 3-step cascade only if the unified resolver returns nothing. Wired through `cmd/server/main.go` and `internal/api/router.go`.
> - Unit tests: cache TTL, health-cache shed/expire, tier grouping, scope helpers, validation, config round-trips.
>
> **PR 3 — OAuth services + new API surface (landed):**
> - `internal/api/handlers/coding_credentials.go` — new `/api/v1/coding-credentials` endpoints: `GET ?scope=org|personal|resolved`, `POST` (API-key create), `PATCH /{id}` (rename/status), `DELETE /{id}` (soft-delete), `PATCH /{id}/move` (single-row drag-drop), `PATCH /reorder` (bulk). Scope is asserted on every mutation; personal scope always coerces to the requester's user_id, never trusting the body.
> - Routes registered in `internal/api/router.go` — personal-scope mutations live in the admin+member group; bulk reorder is admin-only.
> - `frontend/src/lib/api.ts` + `types.ts` — new `api.codingCredentials.*` client and `CodingCredentialSummary` / `CodingCredentialScope` types.
> - The codexauth/claudecodeauth services keep their existing `CredentialStore` interface; their writes through `OrgCredentialStore` are automatically mirrored, so no signature change was needed. The full `Scope`-taking refactor is deferred to PR 5.
>
> **PR 4 — frontend (landed):**
> - `/settings/account` rebuilt against the unified API. Three cards: "My coding agents" (personal stack with Add auth dialog), "Org fallback" (read-only with admin hint), and the effective-resolution line ("Personal #1 → Personal #2 → Org #1") that surfaces what the resolver will pick. Reuses the existing `CodingAuthDialog` provider-card component.
> - `/settings/account/page.test.tsx` rewritten for the new API; all 1469 frontend tests pass.
> - `/settings/agent` left untouched. Its writes flow through the legacy `codingAuths` client, which still hits `OrgCredentialStore`, which mirrors into `coding_credentials`. The unified resolver therefore picks up admin-side changes without any frontend swap. The optional UX improvements from the doc (Used-by column, shared add-auth dialog parameterised by scope) are deferred to a follow-up PR.
>
> **PR 5 — cleanup (outstanding):**
> - Drop coding-provider rows from `org_credentials`, drop `user_credentials`, drop `is_team_default` and the legacy `personal/team_default/org` cascade in `agent/env.go`, remove `AnthropicConfig.Subscription`, rename `OpenAIChatGPTConfig` → `OpenAISubscriptionConfig` everywhere (~20 files), retire the `coding_credentials_mirror.go` dual-write helpers, and 410-Gone the `/api/v1/settings/credentials/personal,team` + `/api/v1/settings/coding-auths` paths.
> - Boot-time refusal-to-serve guard against an unwritten `anthropic_split` sentinel — wire as a startup check before serving traffic.
> - Switch `/settings/agent` to the unified API and extract the shared add-auth dialog parameterised by scope.
>
> **Migration runbook for tomorrow's user switch:**
> 1. Apply migrations `000110` and `000111`. The legacy tables are untouched.
> 2. Run `make migrate-coding-credentials-anthropic-split` (idempotent; writes the sentinel on completion). Use `--dry-run` first to inspect counts.
> 3. Deploy the new server. The mirror is auto-installed (`SetCodingMirror`); the resolver flips to the unified table; legacy reads/writes continue to work because the mirror keeps both tables in lockstep.
> 4. Rollback: `make migrate-down` reverses 000111 (deletes from `coding_credentials`) and 000110 (drops the table). Legacy data is intact.

## Problem

Coding-agent credentials live in two parallel-but-different tables that disagree about almost every dimension that matters:

| | `org_credentials` (surfaced as `coding_auths`) | `user_credentials` |
|---|---|---|
| Multiple per provider | Yes, by `label` | No — `UNIQUE(org_id, user_id, provider)` |
| Priority / fallback order | Yes (`priority` column) | No |
| Subscription auth supported | Yes (Codex via `openai_chatgpt`, Claude via `AnthropicConfig.Subscription`; renamed and split in this design) | No |
| Status lifecycle | `active`, `disabled`, `pending_auth`, `invalid` | `active`, `disabled` |
| Created-by tracking | Yes (`created_by`) | Implicit (`user_id`) |
| Reorder API | Yes | No |
| Resolver path | Bottom of fallback chain | Top of fallback chain |

The split has accumulated as features landed unevenly. The org side became the rich side because that is where stacks, labels, and OAuth flows shipped first. The personal side stayed simple because personal originally meant "one API key per provider." That assumption has expired:

- Users want personal subscriptions (ChatGPT Pro, Claude Max), not just API keys.
- Users want fallbacks within their personal stack — a Pro sub with an API-key safety net.
- Admins want every personal credential to slot cleanly into the same fallback semantics as org credentials.
- Future providers (Amp, Pi, others) will add subscription flows. Each one currently has to be ported to two storage paths and two resolvers.

The split also corrupts the runtime resolver. `personal → team_default → org` was a reasonable v1, but `team_default` is a workaround for the absence of real org auth flows, not a long-lived primitive. Today it overlaps awkwardly with org-level credentials and forces the resolver to consult three sources for what is conceptually one ordered list.

This document proposes collapsing both tables into a single credential table and redrawing the surrounding service, API, and UI surfaces around it.

## Goals

- One source of truth for every coding-agent credential, regardless of who owns it.
- Personal and org credentials share the same lifecycle: label, priority, status, OAuth flows, reordering, last-used tracking.
- A new subscription provider (Amp, future Pi, etc.) ships with one storage path and one resolver path, automatically usable at both scopes.
- Resolution is a single ordered query: personal rows first, then org rows, by priority within each.
- Personal and org settings UIs use the same row component and the same mental model.
- An admin can read either page and explain to a teammate exactly why a session picked a given credential.

## Non-Goals

- Cross-org credential sharing.
- Per-team-within-org credential scoping. Scope is binary: org or user.
- Per-session credential pinning ("always use this auth for this repo"). Possible follow-up; out of scope here.
- Replacing the underlying OAuth flows with anything other than what Codex CLI / Claude Code CLI already use.
- Removing the existing read-only fallback stack rendering for non-admins. It stays.

## Design Principles

- **One credential is one row.** Whatever scope, whatever provider, whatever auth type — one row per credential.
- **Scope is a column, not a table.** The shape of a personal credential is identical to the shape of an org credential except for `user_id`.
- **Provider name encodes auth type.** `(agent, auth_type)` maps to exactly one `ProviderName`. No optional embedded subscription fields.
- **Priority is per-stack.** Personal rows have their own priority order. Org rows have their own. The resolver concatenates stacks in a fixed precedence (personal then org), never interleaves them by priority.
- **Old workarounds get retired, not preserved.** `is_team_default` was load-bearing only because we lacked proper org subscription flows. We have those now. It goes away.
- **Pre-MVP is for fixing foundations.** Do the migration in one focused PR rather than maintaining a compatibility shim that survives.

## Architecture Overview

```
                        coding_credentials
                       (single ordered table)
                                |
          +---------------------+-----------------------+
          |                                             |
   user_id IS NOT NULL                          user_id IS NULL
   (personal scope)                             (org scope)
          |                                             |
   personal stack                                 org stack
   priority 1..N                                  priority 1..N
          |                                             |
          +---------------------+-----------------------+
                                |
                       Resolver (ordered)
                                |
                +---------------+---------------+
                | 1. personal stack by priority |
                | 2. org stack by priority      |
                | 3. nil                        |
                +-------------------------------+
```

Two anchors:

1. **One table.** `coding_credentials` replaces `org_credentials` and `user_credentials`. `user_id` is nullable; null means org-scoped.
2. **One provider-per-method.** `OpenAIConfig` is API-key-only. The existing `OpenAIChatGPTConfig` is renamed to `OpenAISubscriptionConfig` and lives under provider `openai_subscription` (renamed from `openai_chatgpt` for symmetry with the new Claude provider). We migrate `AnthropicConfig.Subscription` out of `AnthropicConfig` into a new `AnthropicSubscriptionConfig` under provider `anthropic_subscription`. Every future subscription gets a sibling `<vendor>_subscription` provider name, never an embedded optional field.

Everything else — services, APIs, UI — falls out of those two decisions.

## Data Model

### Table: `coding_credentials`

```sql
CREATE TABLE coding_credentials (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid        NOT NULL REFERENCES organizations(id),
    user_id          uuid             REFERENCES users(id) ON DELETE CASCADE,
    -- user_id IS NULL means org-scoped. user_id IS NOT NULL means personal.

    provider         text        NOT NULL,
    label            text        NOT NULL DEFAULT '',
    config           bytea       NOT NULL,                    -- AES-GCM encrypted JSON
    priority         integer     NOT NULL DEFAULT 1000,
    status           text        NOT NULL DEFAULT 'active',
    -- created_by uses ON DELETE SET NULL so removing a user does not block
    -- deletion of org rows that user happened to provision. Personal rows are
    -- already removed via the user_id CASCADE above.
    created_by       uuid             REFERENCES users(id) ON DELETE SET NULL,
    last_verified_at timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT chk_coding_credentials_status
        CHECK (status IN ('active', 'disabled', 'pending_auth', 'invalid'))
);

-- One credential per (scope, provider, label).
-- NULLs are distinct in PostgreSQL UNIQUE indexes, so org rows (user_id NULL)
-- and a given user's rows partition cleanly.
CREATE UNIQUE INDEX coding_credentials_scope_provider_label_idx
    ON coding_credentials (org_id, user_id, provider, label);

-- Resolver hot path. Every resolver call filters by org_id + provider, then
-- by user_id (the requester's own personal rows OR org rows where user_id IS
-- NULL). Putting `provider` in the key makes this an index-only seek instead
-- of an org-wide scan that filters by provider after the fact. user_id is in
-- the key so the planner can satisfy both halves of the personal/org OR from
-- the same index. Ordering suffix matches `ORDER BY priority, created_at`.
CREATE INDEX coding_credentials_resolver_idx
    ON coding_credentials (org_id, provider, user_id, priority, created_at)
    WHERE status = 'active';

-- Per-user listing for the personal settings page.
CREATE INDEX coding_credentials_user_idx
    ON coding_credentials (org_id, user_id, priority)
    WHERE user_id IS NOT NULL AND status != 'disabled';

-- Org listing for the admin settings page.
CREATE INDEX coding_credentials_org_idx
    ON coding_credentials (org_id, priority)
    WHERE user_id IS NULL AND status != 'disabled';

-- Janitor seek: find pending_auth rows past their TTL. Tiny partial index.
CREATE INDEX coding_credentials_pending_auth_ttl_idx
    ON coding_credentials (created_at)
    WHERE status = 'pending_auth';
```

Design notes:

- **`user_id` nullable, not a separate scope column.** A scope enum would be redundant with `user_id`. Keeping it implicit avoids drift between the two.
- **Priority is per-stack, not unique globally.** A user can have a personal row with `priority = 1` and the org can have a different row also with `priority = 1`. The resolver disambiguates by scope.
- **No `is_team_default`.** A team default is just an org-scoped row. The admin creates and manages it through the regular org flow.
- **`label` defaults to empty string** so existing single-credential semantics survive without forcing a label on legacy rows. New rows from the unified UI will always carry a label.
- **`created_by` is universal.** For org rows it records which admin set it up; for personal rows it equals `user_id` but is stored explicitly so future delegated provisioning ("I set up this personal cred for Bob during onboarding") is representable without schema change.

### Provider taxonomy

Every (agent, auth-type) pair maps to exactly one provider:

| Agent | Auth type | Provider name | Config struct |
|---|---|---|---|
| codex | api_key | `openai` | `OpenAIConfig` |
| codex | subscription | `openai_subscription` | `OpenAISubscriptionConfig` (renamed from `OpenAIChatGPTConfig`) |
| claude_code | api_key | `anthropic` | `AnthropicConfig` (cleaned up — no `Subscription` field) |
| claude_code | subscription | `anthropic_subscription` | `AnthropicSubscriptionConfig` (new — extracted from `AnthropicConfig.Subscription`) |
| gemini_cli | api_key | `gemini` | `GeminiConfig` |
| amp | api_key | `amp` | `AmpConfig` |
| pi | api_key | `pi` | `PiConfig` |

Future providers slot in by adding a row, not by adding optional fields to existing structs. When Amp adds OAuth, the addition is `(amp, subscription) → amp_subscription / AmpSubscriptionConfig`. The resolver, settings UI, and stores need no changes — they iterate over `CodingAgentProviders`, which we extend by one entry.

`CodingAgentProviders` becomes the single registry of every provider name a coding-agent credential can have. Today `openai_chatgpt` is excluded and gets renamed to `openai_subscription` and added as part of this work. `anthropic_subscription` joins on the same migration that splits Anthropic. The naming convention is `<vendor>_subscription` for every subscription provider, so the registry is self-documenting.

### Status lifecycle

Unified across scopes:

- `active` — usable.
- `pending_auth` — OAuth flow started, tokens not yet exchanged. (Personal rows can be in this state during a personal subscription flow.)
- `invalid` — tokens revoked or key rejected. Visible in the UI; not runnable.
- `disabled` — soft-deleted. Hidden from settings; not runnable.

`rate_limited` is *not* a stored status; it is a runtime-derived display state in the UI based on recent failure telemetry. (The current org table treats it the same way — no schema change needed.)

**Pending-auth TTL.** Personal subscriptions can be in `pending_auth` for a few minutes during an OAuth flow; if the user abandons the flow the row would otherwise live forever. A janitor (cron `*/15 * * * *` or equivalent) deletes `pending_auth` rows older than 24h. Backed by `coding_credentials_pending_auth_ttl_idx`, the sweep is a tiny partial-index scan. The user can always retry the OAuth flow — there is nothing valuable in a stale `pending_auth` row to preserve.

## Services

### Storage layer: one store

Replace `OrgCredentialStore` and `UserCredentialStore` with a single `CodingCredentialStore`. Every method takes an explicit scope:

```go
type Scope struct {
    OrgID  uuid.UUID
    UserID *uuid.UUID // nil means org-scoped
}

type CodingCredentialStore interface {
    // Lookup
    Get(ctx context.Context, scope Scope, id uuid.UUID) (*models.DecryptedCodingCredential, error)
    GetByProviderAndLabel(ctx context.Context, scope Scope, provider models.ProviderName, label string) (*models.DecryptedCodingCredential, error)
    ListByScope(ctx context.Context, scope Scope) ([]models.DecryptedCodingCredential, error)
    ListByProvider(ctx context.Context, scope Scope, provider models.ProviderName) ([]models.DecryptedCodingCredential, error)

    // Resolver hot path: personal then org, ordered by priority within scope.
    ListResolvable(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error)

    // Pick is ListResolvable + same-priority distribution. The runtime path for
    // session starts; settings UIs use ListResolvable directly.
    Pick(ctx context.Context, scope Scope, provider models.ProviderName) (*models.DecryptedCodingCredential, error)

    // Mutation. Every mutation takes Scope explicitly even when also keyed by
    // id. The store asserts in the same transaction that the loaded row's
    // (org_id, user_id) matches Scope; mismatch returns ErrNotFound. This makes
    // "forgot to scope-check" a compile error rather than a code-review item,
    // and prevents id enumeration across scopes.
    Create(ctx context.Context, scope Scope, label string, cfg models.ProviderConfig, opts CreateOpts) (*uuid.UUID, error)
    UpdateConfig(ctx context.Context, scope Scope, id uuid.UUID, cfg models.ProviderConfig) error
    Rename(ctx context.Context, scope Scope, id uuid.UUID, label string) error
    Reorder(ctx context.Context, scope Scope, orderedIDs []uuid.UUID) error
    Move(ctx context.Context, scope Scope, id uuid.UUID, pos MovePosition) error
    UpdateStatus(ctx context.Context, scope Scope, id uuid.UUID, status string) error
    Disable(ctx context.Context, scope Scope, id uuid.UUID) error

    // Pending-auth flows
    InsertPendingAuth(ctx context.Context, scope Scope, label string, cfg models.ProviderConfig) (*uuid.UUID, error)
    PromotePending(ctx context.Context, scope Scope, id uuid.UUID, cfg models.ProviderConfig) error
}

// MovePosition describes a single-row reorder. Exactly one of BeforeID,
// AfterID, ToTop, or ToBottom must be set. The store recomputes contiguous
// priorities for the affected stack inside the same transaction.
type MovePosition struct {
    BeforeID *uuid.UUID
    AfterID  *uuid.UUID
    ToTop    bool
    ToBottom bool
}
```

Design notes:

- **Every mutation takes `Scope`.** Even when also keyed by `id`. The store asserts in-transaction that the loaded row's `(org_id, user_id)` matches `Scope`, returning `ErrNotFound` on mismatch (never `ErrForbidden` — id enumeration must not be able to distinguish "exists in other scope" from "does not exist"). This makes scope-checking a property of the type system, not a discipline the handler layer has to remember.
- **`ListResolvable` is the only resolver-shaped method.** It returns a single ordered list — the caller iterates and picks the first runnable row. This is the entire fallback algorithm.
- **Two reorder shapes.** `Reorder` accepts a full ordered slice for the rare "reset everything" case and for tests; `Move` is the primitive the UI uses for drag-drop. `Move` only rewrites the priorities it must (the moved row plus any rows that shift past it), avoiding the bulk-list-on-every-drag pattern that scales poorly for power users with large stacks. Both run in one transaction.

### OAuth services: one signature for both scopes

`codexauth.Service` and `claudecodeauth.Service` collapse their org-only public methods to take a `Scope`. Both still produce credentials in the unified table; the only difference between an org subscription and a personal subscription is whether `Scope.UserID` is nil.

```go
// Before
func (s *Service) InitiateDeviceAuth(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, label string) (*DeviceAuthResponse, error)

// After
func (s *Service) InitiateDeviceAuth(ctx context.Context, scope Scope, createdBy uuid.UUID, label string) (*DeviceAuthResponse, error)
```

The same shape applies to `claudecodeauth.InitiateOAuth` and `claudecodeauth.CompleteOAuth`.

This is the change that pays for itself most quickly. Today, every new OAuth provider has to either (a) be added to two places or (b) ship as org-only and then later get retrofitted with a personal flow. Under the unified shape, a new provider is one Service implementation that works at both scopes from day one.

### Resolver: one ordered query

```go
// AgentEnv.resolveProviderConfig replaces today's three-step cascade.
func (e *AgentEnv) resolveProviderConfig(
    ctx context.Context,
    orgID uuid.UUID,
    userID *uuid.UUID,
    provider models.ProviderName,
) models.ProviderConfig {
    creds, err := e.store.ListResolvable(ctx, orgID, userID, provider)
    if err != nil {
        return nil
    }
    for _, cred := range creds {
        if cred.Status == "active" {
            return cred.Config
        }
    }
    return nil
}
```

That's it. There is no `team_default` anymore. There is no separate org fallback path. The store does the ordering work; the resolver picks the first runnable row.

#### `ListResolvable` query shape

Internally `ListResolvable` issues two narrow lookups against `coding_credentials_resolver_idx` and concatenates personal-then-org in app code. This lets the planner use the index's ordering suffix for each half without a sort step:

```sql
-- Half 1: personal stack for this user
SELECT id, provider, label, config, priority, status, created_at
FROM coding_credentials
WHERE org_id = $1 AND provider = $2 AND user_id = $3 AND status = 'active'
ORDER BY priority, created_at;

-- Half 2: org stack
SELECT id, provider, label, config, priority, status, created_at
FROM coding_credentials
WHERE org_id = $1 AND provider = $2 AND user_id IS NULL AND status = 'active'
ORDER BY priority, created_at;
```

When `userID` is nil (system/cron caller with no user context), the personal half is skipped. Both halves hit the same partial index; explain plans should be `Index Scan` with no `Sort`.

#### Same-priority distribution

Within a tier (a stack slice that shares a priority), multiple credentials can be runnable at once — two ChatGPT Pro subs at priority 1, three Claude API keys at priority 1. We need to spread load across them without serialising session starts on a shared cursor.

**Approach: random selection with health-aware shedding.** Strict round-robin requires shared mutable state per (scope, provider, priority). Under the contention pattern of "every session start asks for the next credential" that state becomes a hotspot the moment an org has meaningful traffic. Random selection achieves the same load distribution in expectation with zero coordination, and degrades gracefully — N concurrent picks just produce a uniform distribution rather than serialising on a row lock.

```go
func (s *codingCredentialStore) PickRunnable(
    ctx context.Context,
    scope Scope,
    provider models.ProviderName,
) (*models.DecryptedCodingCredential, error) {
    creds, err := s.ListResolvable(ctx, scope.OrgID, scope.UserID, provider)
    if err != nil {
        return nil, err
    }
    // Walk tiers (priority groups) in order. For each tier, drop rows that
    // are currently shed by the in-process health cache, then pick uniformly
    // at random. Move to the next tier only when the current one is empty.
    for _, tier := range groupByPriority(creds) {
        eligible := s.health.Filter(tier) // drops rows with a live shed marker
        if len(eligible) == 0 {
            continue
        }
        return eligible[s.rng.Intn(len(eligible))], nil
    }
    return nil, ErrNoCredential
}
```

**Health cache.** `s.health` is an in-process LRU keyed by `credential_id` that holds short-TTL (60–90s) "do not pick" markers. The agent runtime writes a marker when a credential returns 429 or auth-rejected; entries expire automatically. Properties:

- A rate-limited credential stops getting picked within seconds with no DB write.
- Random selection naturally rediscovers a credential once its marker expires — no separate "retry" bookkeeping.
- The cache is per-process. With multiple app hosts each host learns independently; given typical 60-90s TTLs and the cost of a wasted attempt being one rejected request, the eventual-consistency cost is acceptable. If we later want cross-host, swap the LRU for Redis with the same interface.

**Why not strict round-robin.** True round-robin needs either `SELECT … FOR UPDATE SKIP LOCKED` on a per-tier cursor row or atomic counter increments — both serialise selection per (scope, provider, priority) and become the bottleneck once a popular tier sees more than a few hundred picks/sec. Random + shedding scales to whatever Postgres can serve `ListResolvable` for, which the cache below makes effectively free.

**Determinism for tests.** `s.rng` is injectable. Tests pin a seed for sequence assertions or skip pinning and assert distribution shape over many calls.

#### Resolver caching

`ListResolvable` runs on every session start and every PM trigger. For an org with a stable credential stack and 1k sessions/min that is 1k identical reads against the same handful of rows. We add a small in-process cache to make the steady-state cost negligible:

- Key: `(org_id, user_id_or_zero, provider)`.
- Value: the resolved `[]DecryptedCodingCredential` slice.
- TTL: 30s. Short enough that any human-driven settings change is reflected promptly without explicit invalidation.
- Invalidation: every store mutation (`Create`, `UpdateConfig`, `Reorder`, `Move`, `UpdateStatus`, `Disable`, `PromotePending`) calls `cache.InvalidateScope(scope, provider)`. The mutation surface is small enough that exhaustive invalidation is tractable; combined with the 30s TTL, a missed invalidation site is self-healing.
- Negative results (no rows) are cached with the same TTL to keep the no-credential path cheap.

The cache is per-process (no Redis dependency in the hot path). An app restart drops it; that is fine because `ListResolvable` against the new partial index is already cheap — the cache exists to remove repeat work, not because the underlying query is slow.

## API Surface

The `coding_auths` API already speaks the right shape (id-based rows, reorder, label, status). We extend it to be scope-aware rather than building a third API.

### Read

```
GET /api/v1/coding-credentials?scope=org              → list org rows (admin)
GET /api/v1/coding-credentials?scope=personal         → list current user's personal rows
GET /api/v1/coding-credentials?scope=resolved         → ordered list (personal + org) for current user
                                                         (used by personal page to render the org fallback section)
```

The `scope=resolved` response includes a `scope` field per row so the UI can badge personal vs org without a second roundtrip.

### Mutate

```
POST   /api/v1/coding-credentials                     → create API-key credential
                                                         body: { scope, agent, auth_type: "api_key", label, api_key, agent_defaults? }
PATCH  /api/v1/coding-credentials/{id}                → rename / status update
                                                         body: { scope, label?, status? }
DELETE /api/v1/coding-credentials/{id}                → disable
                                                         body: { scope }
PATCH  /api/v1/coding-credentials/{id}/move           → move one row within its scope's stack (UI drag-drop)
                                                         body: { scope, before_id? | after_id? | to_top | to_bottom }
PATCH  /api/v1/coding-credentials/reorder             → bulk reorder a scope's stack (recovery / tests)
                                                         body: { scope, ordered_ids }
```

The per-row `move` endpoint is the default UI path: a drag-drop sends one ID and a position relative to one neighbour, regardless of stack size. The bulk `reorder` shape is kept for "reset to this exact ordering" flows and tests; it is not on the hot edit path.

Authorization:
- `scope=org` mutations require admin role.
- `scope=personal` mutations always operate on the requester's own rows.
- The handler resolves `id`-based mutations by reading the row first and rejecting if scope/ownership doesn't match.

### Subscription flows

Each subscription provider keeps its dedicated initiate/complete endpoints, but they accept scope:

```
POST /api/v1/coding-credentials/codex-auth/initiate
     body: { scope: "org" | "personal", label }
GET  /api/v1/coding-credentials/codex-auth/status?label=...&scope=...
POST /api/v1/coding-credentials/claude-code-auth/initiate
     body: { scope, label }
POST /api/v1/coding-credentials/claude-code-auth/complete
     body: { scope, label, code }
```

These supersede the current `/api/v1/settings/codex-auth/*` and `/api/v1/settings/claude-code-auth/*` endpoints. Old endpoints are removed (pre-MVP, no compatibility window required).

### Removed endpoints

The following retire entirely:

```
GET    /api/v1/settings/credentials/personal
PUT    /api/v1/settings/credentials/personal/{provider}
DELETE /api/v1/settings/credentials/personal/{provider}
GET    /api/v1/settings/credentials/team
PUT    /api/v1/settings/credentials/team/{provider}
DELETE /api/v1/settings/credentials/team/{provider}
GET    /api/v1/settings/credentials/resolved          # superseded by scope=resolved above
GET    /api/v1/settings/coding-auths                  # superseded by scope=org above
POST   /api/v1/settings/coding-auths
PATCH  /api/v1/settings/coding-auths/reorder
PATCH  /api/v1/settings/coding-auths/{id}
DELETE /api/v1/settings/coding-auths/{id}
```

This is intentional. Two parallel APIs is what we are leaving behind.

## UX

The UI redesign is small relative to the data and service work, because the org page (`/settings/agent`) is already the canonical UI shape. The personal page becomes a near-copy.

### `/settings/agent` (admin only)

Mostly unchanged. Behavioral differences after the unification:

- The "Add auth" dialog calls the new `POST /api/v1/coding-credentials` with `scope: "org"`.
- The reorder endpoint changes path; the page already optimistic-updates.
- A new "Used by" column (optional, behind progressive disclosure) shows how many users currently resolve to this row as their first runnable credential. Helps admins reason about churn before disabling a row.

### `/settings/account` (everyone)

Today this is a small "Configured personal auths" table with one row per provider and an API-key-only "Add auth" modal. It becomes:

```text
+------------------------------------------------------------------+
| My Coding Agents                                  [Add auth]     |
| Your auths run before any organization fallback.                 |
+------------------------------------------------------------------+
| Personal stack (drag to reorder)                                 |
|------------------------------------------------------------------|
| 1  ≡  Codex        Subscription   Personal Pro     Healthy DEFAULT |
| 2  ≡  Claude Code  API key        ...8LM2          Healthy        |
| 3  ≡  Codex        API key        ...1DF9          Never verified |
+------------------------------------------------------------------+

+------------------------------------------------------------------+
| Org fallback (read-only)                                         |
|------------------------------------------------------------------|
|    Codex        Subscription   Team seat A        Healthy        |
|    Claude Code  API key        Claude prod key    Healthy        |
+------------------------------------------------------------------+
| Effective resolution for you:                                    |
|   Personal #1 → Personal #2 → Personal #3 → Org #1 → Org #2      |
+------------------------------------------------------------------+
```

Key UX choices:

- **Same table component as `/settings/agent`.** Drag-to-reorder, status badges, default badge, side sheet on click. One visual language.
- **Org section is read-only**, with a hint about who can change it ("Contact an admin to change org auths").
- **Effective resolution is rendered explicitly.** This is the single most-asked support question today; making it ambient on the page eliminates the support load.
- **"Add auth" is the same dialog used by the org page**, parameterized by scope. Provider selector + auth-type radio + provider-specific completion. For subscriptions, opens the existing `CodexDeviceCodeModal` / `ClaudeCodeAuthModal` with `scope: "personal"` passed through.

### Add-auth dialog (shared component)

One dialog, both pages. Steps:

1. Provider (Codex / Claude Code / Gemini / Amp / Pi)
2. Auth type (Subscription / API key) — only shown if the provider supports both
3. Provider-specific step: API key input, OR launch OAuth flow
4. Label (defaults to a sensible auto-label like "Codex subscription" or "Codex API key")
5. Insertion point: Make default / Add as next fallback / Place manually

The dialog accepts scope as a prop; it is set by whichever page opened the dialog. There is no UI for cross-scope operations: a personal credential cannot be promoted to org through the personal page (that would conflate authorization boundaries). Admins promote by re-creating the auth at org scope; the personal row stays.

### What "team default" becomes

It disappears from the data model. In the UI, what was previously an admin-set team default becomes a regular org row. Anyone who had `is_team_default=true` on a user_credentials row is migrated by copying the row's encrypted config to a new `coding_credentials` row with `user_id=NULL` and a sensible default label like "Team default — &lt;provider&gt;". The original personal row is kept (so the user doesn't lose their own auth).

This is the right outcome: "team default" was always a workaround for orgs lacking real auth-management flows. Now they have those flows. The migration just turns the workaround into the real thing.

## Migration

One PR's worth of work. Pre-MVP — we are not maintaining a compatibility window.

### Migration `00010X_unified_coding_credentials`

```sql
-- 1. Create the unified table.
CREATE TABLE coding_credentials (
    -- ... full schema from above ...
);

-- 2. Copy org credentials. Rename openai_chatgpt → openai_subscription on the way in.
--    provider, label, priority, status, etc. all carry over.
INSERT INTO coding_credentials
    (id, org_id, user_id, provider, label, config, priority, status, created_by,
     last_verified_at, created_at, updated_at)
SELECT
    id, org_id, NULL,
    CASE provider WHEN 'openai_chatgpt' THEN 'openai_subscription' ELSE provider END,
    label, config, priority, status, created_by,
    last_verified_at, created_at, updated_at
FROM org_credentials
WHERE provider IN ('openai', 'openai_chatgpt', 'anthropic', 'gemini', 'amp', 'pi', 'openrouter');

-- Non-coding-agent providers (github_app, sentry, linear, slack, notion, ...)
-- stay in org_credentials. coding_credentials is for coding-agent providers only.

-- 3. Copy personal credentials. Initial label='', priority=1.
INSERT INTO coding_credentials
    (id, org_id, user_id, provider, label, config, priority, status, created_by,
     last_verified_at, created_at, updated_at)
SELECT
    id, org_id, user_id, provider, '' AS label, config, 1 AS priority, status, user_id,
    last_verified_at, created_at, updated_at
FROM user_credentials
WHERE is_team_default = false;

-- 4. Copy team-default rows as ORG-scoped rows with user_id=NULL.
--    Use a deterministic label so there are no collisions.
INSERT INTO coding_credentials
    (org_id, user_id, provider, label, config, priority, status, created_by,
     last_verified_at, created_at, updated_at)
SELECT
    org_id, NULL, provider,
    'Team default (migrated from ' || (SELECT email FROM users WHERE id = uc.user_id) || ')' AS label,
    config,
    -- Place team-default migrations at the end of the org stack so existing
    -- explicit org rows still win.
    (SELECT COALESCE(MAX(priority), 0) FROM org_credentials oc WHERE oc.org_id = uc.org_id) + 1,
    status, user_id, last_verified_at, created_at, updated_at
FROM user_credentials uc
WHERE is_team_default = true;
-- Note: the user keeps their personal row (already copied in step 3).

-- 5. Migrate Anthropic subscriptions: rows with provider='anthropic' whose
--    encrypted config contains a non-null Subscription field need to become
--    provider='anthropic_subscription' rows.
--    This is *not* a pure SQL migration because config is encrypted. The
--    migration runs a Go-side post-step that decrypts each anthropic row,
--    branches on whether Subscription is set, and rewrites the row's provider
--    name + config struct. See `internal/db/migrations/post_step_split_anthropic.go`.

-- 6. Drop the old tables once the post-step finishes.
DROP TABLE user_credentials;
-- org_credentials is kept (still used for non-coding providers) but the rows
-- we copied in step 2 are deleted from it. (Note: 'openai_chatgpt' rows in
-- the source table are matched here by their original name; they were
-- renamed to 'openai_subscription' only inside coding_credentials.)
DELETE FROM org_credentials WHERE provider IN
    ('openai', 'openai_chatgpt', 'anthropic', 'gemini', 'amp', 'pi', 'openrouter');
```

### Why a Go-side post-step for Anthropic split

The encrypted-config rewrite cannot be done in pure SQL. We need to:

1. Decrypt the row.
2. JSON-parse into `AnthropicConfig`.
3. If `Subscription != nil`, write a new `AnthropicSubscriptionConfig` with the OAuth fields, change the row's `provider` to `anthropic_subscription`, and re-encrypt.
4. If `Subscription == nil`, leave it alone (it is already a clean API-key row).

**Run as a one-shot job, not at app startup.** Earlier drafts of this doc proposed running the post-step at boot. Don't. Even pre-MVP, gating every app start on "decrypt every anthropic row, branch, re-encrypt, write" is a recipe for slow boots and a hard dependency on KMS being healthy at restart time. Instead:

- Ship the post-step as a standalone command: `make migrate-coding-credentials-anthropic-split`.
- Process rows in batches (default: 500 rows per transaction), paginated by `(created_at, id)` so progress survives interruption.
- Apply a per-row `statement_timeout` so a single stuck row can't wedge the batch.
- Write a sentinel row to a new `coding_credentials_migrations` table on completion: `('anthropic_split', completed_at)`.
- The new code's startup checks the sentinel and refuses to serve traffic if the migration hasn't completed for the current schema version. Old code paths (which still understand `AnthropicConfig.Subscription`) keep working until the sentinel lands.
- The job is idempotent: it skips rows already at provider `anthropic_subscription` and re-runs cleanly after partial completion.

This pattern (batch + sentinel + boot guard) is the right shape for any future migration that touches encrypted blobs.

### Down migration

```sql
-- Recreate org_credentials rows for coding providers, recreate user_credentials,
-- collapse team_default rows back, undo the Anthropic split.
```

The down migration is mechanical but tedious. Given pre-MVP status, the down migration only needs to be correct enough to support local dev rollback, not production rollback.

### Code-level deletions

Concretely deleted in this PR:

- `internal/db/user_credentials.go` (replaced by `internal/db/coding_credentials.go`).
- `internal/api/handlers/user_credentials.go`'s personal/team endpoints (replaced by `internal/api/handlers/coding_credentials.go`).
- The `is_team_default` field on `UserCredential`, `UserCredentialSummary`, `DecryptedUserCredential`, and every call site.
- The three-step `personal → team_default → org` cascade in `internal/services/agent/env.go`. Replaced by `ListResolvable`.
- `AnthropicConfig.Subscription` field. Replaced by `AnthropicSubscriptionConfig` under provider `anthropic_subscription`.

Concretely added:

- `internal/db/coding_credentials.go`.
- `internal/api/handlers/coding_credentials.go`.
- `internal/models/coding_credential.go` with the unified `CodingCredential` / `DecryptedCodingCredential` / `CodingCredentialSummary` types.
- `internal/models/anthropic_subscription_config.go` with the new config struct.
- `OpenAIChatGPTConfig` is renamed to `OpenAISubscriptionConfig` (same fields, new name) and its `Provider()` method returns the new `ProviderOpenAISubscription` constant. The `ProviderOpenAIChatGPT` constant is removed.
- Scope-aware variants of `codexauth.Service.InitiateDeviceAuth`, `claudecodeauth.Service.InitiateOAuth`, etc.

## Authorization & Multi-Tenancy

- Every query filters by `org_id`. No exceptions. Same rule as today.
- `scope=org` write endpoints require admin role, validated via `middleware.ActiveRoleFromContext`.
- `scope=personal` write endpoints validate that the row's `user_id` matches the caller. The store helper `getOwnedByUser(scope, id)` returns 404 if the row exists but is owned by another user — same shape as the existing not-found path, no information leak about other users' rows.
- `ON DELETE CASCADE` on `user_id` ensures a user's personal rows disappear when the user is removed; org rows survive.
- Encryption key handling is unchanged (existing `crypto.Service`, AES-GCM).

## Observability

- Add a `credential_id` field to the existing session-trigger telemetry so logs show exactly which row served a session.
- Add `credential_scope` ("personal" | "org") to make filtering by scope cheap.
- **No `last_used_at` column on the credential row.** Earlier drafts had one; it was removed because every session start would write to the same hot row for whichever credential the org currently prefers, producing pointless contention and HOT-update churn that bloats every partial index. Last-used and "used by which users in the last N days" are both derived on demand from the session-trigger telemetry stream above. The telemetry stream is the source of truth for usage; the credential row stores only configuration and lifecycle state.
- The `Used by` column on the admin page is a telemetry aggregation (`COUNT(DISTINCT user_id) WHERE credential_id = ? AND ts > now() - interval '7 days'`), cached at the page level. Same source feeds the personal page's "last used" hints if we ever add them.

## Testing

### Unit / Integration

- **Store tests.** `ListResolvable` ordering correctness across (personal, org) × (priority, status, created_at). Reorder atomicity. Pending-auth promotion.
- **Resolver tests.** Personal beats org. Inactive personal does not block org. Round-robin within scope. No personal user → falls through to org.
- **OAuth service tests.** Both `Scope` shapes. Personal pending-auth lifecycle. Race when two tabs initiate at once for the same scope+label.
- **Migration tests.** Copy correctness. Anthropic split correctness for both subscription and API-key rows. Idempotent re-run.

### Handler tests

- Authorization: non-admin cannot mutate org rows. User cannot mutate another user's personal rows. Cross-org access blocked.
- Scope routing: `scope=personal` always uses requester's user_id, regardless of body.
- 404 vs 403: not-found on cross-org/cross-user reads to avoid id enumeration.

### Frontend

- Personal page renders both stacks with correct read-only state on the org one.
- Add-auth dialog passes scope correctly through subscription modals.
- Drag-and-drop reorder works in personal stack and rejects drops into the org stack.
- Effective-resolution string matches what the resolver actually picks.

### End-to-end

- New user adds a personal Codex subscription via the UI. Verifies that a session triggered by them resolves to it. Disables it. Verifies fallback to org.
- Admin promotes a stack reorder. Existing user with personal auth still resolves to personal first.

## Rollout

Even pre-MVP, this change is too large for a single PR — it touches schema, an encrypted-blob post-step, two stores, four OAuth services, the API surface, and the frontend. A single 3k-line PR has no useful intermediate revertable state and forces a "restore from backup" if anything goes wrong. Stage it instead:

**PR 1 — schema and migration tooling.** Create `coding_credentials` and the partial indexes. Ship the standalone `migrate-coding-credentials-anthropic-split` command and the `coding_credentials_migrations` sentinel table. No code yet reads or writes `coding_credentials`; old code is unchanged. Run the data-copy migration plus the Anthropic post-step against a copy of prod to smoke-test, then in prod. Reversible: drop the table.

**PR 2 — store and resolver, behind a thin adapter.** Land `internal/db/coding_credentials.go`, the `CodingCredentialStore` interface, the cache, `Pick`, the random-with-shedding selection, and the new resolver in `agent.AgentEnv`. The old `OrgCredentialStore` and `UserCredentialStore` keep existing for now; an adapter routes their reads through `coding_credentials` and their writes through *both* tables (dual-write) so old code paths continue to work. This is the only PR that needs dual-write — keep it short-lived.

**PR 3 — OAuth services and API endpoints.** Cut `codexauth.Service` and `claudecodeauth.Service` over to scope-aware signatures. Add the new `/api/v1/coding-credentials` endpoints. Old `/api/v1/settings/...` endpoints return 410 Gone with a header pointing at the new path.

**PR 4 — frontend.** Swap the personal page to the new shape; swap the admin page to the new endpoints.

**PR 5 — delete the dual-write adapter and the old tables.** Drop `user_credentials` and the coding-provider rows from `org_credentials`. Remove `is_team_default` everywhere.

Each PR is independently revertable. The dual-write window in PR 2 is the price; pre-MVP we keep it open for hours not weeks. There is no feature flag — the rollout boundary is the PR sequence itself.

If timing is so tight that PRs 2–4 must land in one push, that is acceptable, but PR 1 (schema + post-step) and PR 5 (cleanup) should always be separate from the application-layer changes — those are the irreversible ones.

## Open Questions

- **Per-session credential pinning.** Worth it? "Use this exact auth for sessions in this repo" is a real ask but probably belongs in a separate design once usage data shows the pain. Out of scope here, but the unified row model accommodates it cleanly: add a `coding_credential_pins` table keyed by `(repo_id, credential_id)` later.
- **Personal subscription quotas.** Should an org admin be able to cap how many personal subscriptions a user can attach? We currently cap nothing. Probably defer until someone asks; the row model trivially supports a per-org policy.
- **Same-priority distribution UI.** Multiple credentials at the same priority within a stack are spread by random-with-shedding (see Resolver). The data model and runtime support this for both scopes from day one. Open question is purely UI: do we expose "tie at this priority" visually so users intentionally create tiers, or leave it as an emergent behaviour and surface a hint only when a tie exists.
- **OpenRouter and other API-router providers.** OpenRouter is in `CodingAgentProviders` today but not surfaced in the settings UI. Migration treats it as a coding provider; UI exposure is a separate decision.
- **Provider naming convention.** Resolved: every subscription provider is named `<vendor>_subscription` (`openai_subscription`, `anthropic_subscription`, future `amp_subscription`, etc.). The legacy `openai_chatgpt` name is renamed in the same migration that introduces `anthropic_subscription`, so there is never a window where the two providers use inconsistent conventions.

## Success Criteria

- `coding_credentials` is the only credential table for coding agents. `user_credentials` no longer exists; `org_credentials` only holds non-coding providers (GitHub, Sentry, Linear, etc.).
- Every coding-agent provider — including future ones — uses one storage path, one resolver path, and one settings UI for both personal and org scope.
- The personal account settings page supports subscriptions for any provider that the org page supports, with no provider-specific scaffolding.
- Adding subscription support to a new provider requires: one new `ProviderConfig` struct, one new entry in `CodingAgentProviders`, and one Service implementation. No changes to stores, resolvers, or generic UI.
- An admin or user can read either settings page and explain in one sentence which credential will run their next session.
