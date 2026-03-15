# Personal & Team Coding Agent Configuration Plan

## Problem Statement

Currently, coding agent credentials (Anthropic, OpenAI, Gemini keys) are configured at the **org level only** (`org_credentials` table with `UNIQUE(org_id, provider)`). Every team member shares the same API keys. This means:
- No way for individuals to bring their own keys
- No way to track/limit usage per person
- No fallback chain (personal → team default)
- Rate limits hit by one person affect the entire team

## Design Overview

Introduce **user-scoped coding agent configurations** that sit alongside the existing org-scoped ones. When a session runs, credentials resolve in this order:

```
User's personal credential → Org team default → Server env fallback
```

Each "coding agent config" bundles: provider + API key + optional model preference + scope (personal vs team).

---

## Database Changes

### New table: `user_credentials`

```sql
CREATE TABLE user_credentials (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id           uuid        NOT NULL REFERENCES organizations(id),
    provider         text        NOT NULL,  -- anthropic, openai, gemini, openrouter
    config           bytea       NOT NULL,  -- encrypted JSON (same as org_credentials)
    is_team_default  boolean     NOT NULL DEFAULT false,
    status           text        NOT NULL DEFAULT 'active',
    last_verified_at timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, user_id, provider)
);

CREATE INDEX idx_user_credentials_org_id ON user_credentials(org_id);
CREATE INDEX idx_user_credentials_user_id ON user_credentials(user_id);
```

Key design decisions:
- **`UNIQUE(org_id, user_id, provider)`** — one config per provider per user per org (supports users in multiple orgs)
- **`is_team_default`** — when true, this credential is used as the org-wide fallback for that provider. Only admins can set this. Only one user_credential per (org_id, provider) can be `is_team_default = true` (enforced in application logic; a partial unique index could enforce it in DB too)
- **`org_id`** — for multi-tenancy filtering (all queries filter by org_id)
- **`ON DELETE CASCADE`** on user_id — credentials are cleaned up when a user is removed
- Uses the same encryption scheme as `org_credentials` (AES-GCM via crypto.Service)

### Migration of existing `org_credentials` LLM entries

The existing `org_credentials` table stays as-is for non-LLM providers (github_app, sentry, linear, slack). For LLM providers (anthropic, openai, gemini, openrouter), the existing org_credentials continue to work as the lowest-priority fallback. No data migration needed — the resolution logic just checks `user_credentials` first.

---

## Model Changes

### New model: `UserCredential`

```go
// internal/models/credentials.go

type UserCredential struct {
    ID             uuid.UUID    `db:"id"`
    UserID         uuid.UUID    `db:"user_id"`
    OrgID          uuid.UUID    `db:"org_id"`
    Provider       ProviderName `db:"provider"`
    Config         []byte       `db:"config"`      // encrypted
    IsTeamDefault  bool         `db:"is_team_default"`
    Status         string       `db:"status"`
    LastVerifiedAt *time.Time   `db:"last_verified_at"`
    CreatedAt      time.Time    `db:"created_at"`
    UpdatedAt      time.Time    `db:"updated_at"`
}

type DecryptedUserCredential struct {
    ID             uuid.UUID      `json:"id"`
    UserID         uuid.UUID      `json:"user_id"`
    OrgID          uuid.UUID      `json:"org_id"`
    Provider       ProviderName   `json:"provider"`
    Config         ProviderConfig `json:"-"`
    IsTeamDefault  bool           `json:"is_team_default"`
    Status         string         `json:"status"`
    LastVerifiedAt *time.Time     `json:"last_verified_at,omitempty"`
    UpdatedAt      time.Time      `json:"updated_at"`
}

type UserCredentialSummary struct {
    Provider       ProviderName `json:"provider"`
    Configured     bool         `json:"configured"`
    IsTeamDefault  bool         `json:"is_team_default"`
    MaskedKey      string       `json:"masked_key,omitempty"`
    SetByUserID    *uuid.UUID   `json:"set_by_user_id,omitempty"`
    SetByUserName  *string      `json:"set_by_user_name,omitempty"`
    LastVerifiedAt *time.Time   `json:"last_verified_at,omitempty"`
}
```

### Session model change

Add `TriggeredByUserID` to `Session` so the orchestrator knows whose credentials to use:

```go
type Session struct {
    // ... existing fields ...
    TriggeredByUserID *uuid.UUID `db:"triggered_by_user_id" json:"triggered_by_user_id,omitempty"`
}
```

This requires a migration to add `triggered_by_user_id uuid REFERENCES users(id)` to the `sessions` table.

---

## DB Store: `UserCredentialStore`

New file: `internal/db/user_credentials.go`

Methods (mirrors `OrgCredentialStore` pattern):
- **`Upsert(ctx, userID, orgID, cfg ProviderConfig, isTeamDefault bool)`** — insert or update a user credential
- **`Get(ctx, userID, provider)`** — get a specific user credential
- **`GetForOrg(ctx, orgID, userID, provider)`** — get user's credential for a provider within an org
- **`GetTeamDefault(ctx, orgID, provider)`** — get the team default for a provider
- **`ListByUser(ctx, orgID, userID)`** — list all credentials for a user (for the settings UI)
- **`ListTeamDefaults(ctx, orgID)`** — list all team defaults (for the settings UI)
- **`Disable(ctx, userID, provider)`** — soft-delete
- **`ClearTeamDefault(ctx, orgID, provider)`** — unset is_team_default for a provider across all users in the org
- **`SetTeamDefault(ctx, orgID, userID, provider)`** — set a user's credential as team default (clears any existing team default for that provider first, in a transaction)

---

## Credential Resolution Logic

New method on the orchestrator (or a dedicated resolver service):

```go
// ResolveCredential returns the best credential for the given agent type,
// checking in order: user personal → team default → org credential → nil.
func (o *Orchestrator) ResolveCredential(
    ctx context.Context,
    orgID uuid.UUID,
    userID *uuid.UUID,  // nil for auto-triggered runs
    agentType AgentType,
) (*DecryptedCredential, error)
```

Resolution order:
1. If `userID` is set, check `user_credentials` for that user + provider
2. If not found (or userID is nil), check `user_credentials` where `is_team_default = true` for org + provider
3. If not found, fall back to `org_credentials` for org + provider (existing behavior)
4. If nothing found, return nil (server env vars are the final fallback, handled in sandbox config)

The `resolveAgentEnv` method in the orchestrator gets updated to use this chain.

---

## API Endpoints

### Personal Credential Management

```
GET    /api/v1/settings/credentials/personal          → list user's own credentials
PUT    /api/v1/settings/credentials/personal/:provider → upsert personal credential
DELETE /api/v1/settings/credentials/personal/:provider → disable personal credential
```

### Team Default Management (admin only)

```
GET    /api/v1/settings/credentials/team               → list team defaults (shows who set each one)
PUT    /api/v1/settings/credentials/team/:provider      → set a credential as team default
DELETE /api/v1/settings/credentials/team/:provider      → remove team default for a provider
```

### Credential Resolution Preview

```
GET    /api/v1/settings/credentials/resolved            → show what credentials would be used for the current user
```

This endpoint returns, for each LLM provider, which credential would be used (personal, team default, or org) and a masked key.

---

## Session Trigger Changes

When a session is triggered (via `POST /api/v1/issues/{id}/trigger` or `POST /api/v1/sessions/manual`), the handler now:

1. Records `triggered_by_user_id` from the auth context
2. Passes it through the job payload to the worker
3. The orchestrator's `resolveAgentEnv` uses this user ID for credential resolution

For auto-scheduled sessions (no user trigger), `triggered_by_user_id` is nil, so resolution falls through to team default → org credential.

---

## Frontend Changes

### Settings Page: New "Coding Agents" section

Two tabs:
- **My Agents** — manage personal credentials per provider
  - Card per provider (Anthropic, OpenAI, Gemini, OpenRouter)
  - Shows masked key if configured, "Not configured" otherwise
  - "Add Key" / "Update Key" / "Remove" actions
  - Toggle: "Use as team default" (admin only)

- **Team Defaults** — view/manage team-wide defaults (admin only)
  - Shows which user's key is the team default for each provider
  - Shows the resolution chain: "Your key → Team default (set by Alice) → Org fallback"

### Session Detail

Show which credential source was used (personal/team/org) in the session detail view, so users can debug credential issues.

---

## Implementation Order

1. [x] **Migration**: Add `user_credentials` table (`000020`) + `triggered_by_user_id` column on sessions (`000019`)
2. [x] **Models**: Add `UserCredential`, `DecryptedUserCredential`, `UserCredentialSummary`, `ResolvedCredential` types + `CodingAgentProviders` list
3. [x] **DB Store**: Implement `UserCredentialStore` with all methods (Upsert, GetForUser, GetTeamDefault, ListByUser, ListTeamDefaults, Disable, SetTeamDefault, RemoveTeamDefault)
4. [x] **Credential Resolver**: Add `resolveProviderConfig` method and `UserCredentialProvider` interface in orchestrator; update `resolveAgentEnv` to accept `userID`
5. [x] **API Handlers**: `UserCredentialHandler` with ListPersonal, UpsertPersonal, DeletePersonal, ListTeamDefaults, SetTeamDefault, DeleteTeamDefault, ListResolved
6. [x] **Router + Wiring**: Register routes in `router.go`, wire `UserCredentialStore` into orchestrator via `cmd/server/main.go`
7. [x] **Session Trigger**: `triggered_by_user_id` captured in TriggerFix/CreateManual handlers, passed through to orchestrator
8. [x] **Frontend API Client**: Added `api.userCredentials` methods (listPersonal, upsertPersonal, deletePersonal, listTeamDefaults, setTeamDefault, removeTeamDefault, listResolved) + `UserCredentialSummary`/`ResolvedCredential` types
9. [x] **Frontend UI**: "My Agents" page with tabs (My Keys, Team Defaults, Active Config) + nav menu entry
10. [x] **Tests**: Handler tests for ListPersonal, UpsertPersonal, DeletePersonal, ListTeamDefaults, ListResolved with mock stores

---

## Rate Limit Sharing Considerations

With personal keys, rate limits are naturally distributed:
- Each person's key has its own rate limit with the provider
- Team default key is shared, but only used as fallback
- The system could track which key was used for each session (store `credential_source` on the session) for usage analytics
- Future enhancement: usage dashboard showing API spend per user/key

---

## Security Notes

- Same AES-GCM encryption as existing `org_credentials`
- Personal keys are never visible to other users (masked in API responses)
- Team default keys show masked version + who set them
- `ON DELETE CASCADE` ensures credential cleanup on user removal
- All queries filter by `org_id` for multi-tenancy isolation
