# 26 — Codex as Default Agent with ChatGPT OAuth

> **Status:** Implemented | **Last reviewed:** 2026-03-25

**Status**: implemented
**Depends on**: 06-agent-orchestrator, 20-security-architecture, 25-dashboard-credentials

## Problem

143.dev currently defaults to `claude_code` as the coding agent. This creates three problems:

1. **No access to gpt-5.3-codex**: OpenAI's most capable coding model (state-of-the-art on SWE-Bench Pro) is only available via ChatGPT-authenticated sessions — not the standard API. Users with ChatGPT Plus ($5 promotional credits) or Pro ($50 credits) subscriptions cannot use their quotas.

2. **No Codex adapter**: While the orchestrator design (doc 06) mentions a Codex adapter, none is implemented. Only `claude_code` and `gemini_cli` adapters exist.

3. **Agent setup is buried**: Agent configuration lives under "Advanced Settings" (collapsed by default) in the Settings page. Without it configured, the entire agent pipeline is dead — no runs, no PRs, no value. This is the most critical setup step, yet it's treated as optional.

## Goals

1. Implement a Codex CLI adapter (`AgentAdapter` for `"codex"`)
2. Support ChatGPT OAuth via Device Code Auth so users can use their subscription quota and access gpt-5.3-codex
3. Support API key fallback for pay-as-you-go users
4. Change the default agent from `claude_code` to `codex`
5. Make agent setup a first-class onboarding step (not an advanced setting)
6. Maintain full backward compatibility for existing `claude_code` and `gemini_cli` users

## Background: Codex CLI Authentication

The Codex CLI (open source at [github.com/openai/codex](https://github.com/openai/codex)) supports three authentication methods:

| Method | How it works | Model access | Billing |
|--------|-------------|-------------|---------|
| **ChatGPT OAuth** | Browser-based OAuth 2.0 + PKCE via `auth.openai.com` | All models including gpt-5.3-codex | ChatGPT subscription quota |
| **API Key** | `OPENAI_API_KEY` environment variable | API-available models only (not gpt-5.3-codex) | Pay-as-you-go |
| **Device Code Auth** | Headless OAuth — user enters code at a URL | Same as ChatGPT OAuth | Same as ChatGPT OAuth |

Credentials are stored in `~/.codex/auth.json`:

```json
{
  "access_token": "cha_...",
  "refresh_token": "chr_...",
  "expires_at": "2026-03-15T12:00:00Z"
}
```

### Why Device Code Auth

The standard ChatGPT OAuth flow redirects to `http://localhost:1455/auth/callback`. OpenAI's auth server **only accepts localhost redirect URIs** — there is no mechanism to register custom callback URLs. Every tool in the ecosystem (Cline, Roo Code, Kilo Code, OpenCode) uses localhost because that is all OpenAI allows.

Since 143.dev is a web dashboard (not a desktop IDE), localhost redirects don't work. The **Device Code flow** (RFC 8628) is the correct solution:

- No browser needed on the server
- User authenticates on any device (phone, laptop, etc.)
- Explicitly designed for headless environments
- OpenAI supports it via `codex login --device-auth`

### Ecosystem Context: Public client_id

The entire third-party ecosystem reuses the Codex CLI's public OAuth client_id (`app_EMoamEEZ73f0CkXaXp7hrann`). This includes Cline, Roo Code, Kilo Code, and OpenCode. OpenAI has not provided a way for third parties to register their own OAuth clients, and has not responded to community requests for guidance ([OpenAI Forum thread](https://community.openai.com/t/best-practice-for-clientid-when-using-codex-oauth/1371778)). There is an open feature request for a formal "Sign in with ChatGPT" flow ([Issue #10974](https://github.com/openai/codex/issues/10974)) but it has not been implemented.

We follow the same de facto standard. If OpenAI ships a formal registration mechanism, we switch to our own client_id.

## Architecture

### Device Code Auth Flow

```
User clicks "Connect ChatGPT" in 143.dev dashboard
    │
    ▼
Backend: POST {issuer}/api/accounts/deviceauth/usercode
    Body: { client_id: "app_EMoamEEZ73f0CkXaXp7hrann" }
    Response: { device_code, user_code, verification_uri, interval, expires_in }
    │
    ▼
Frontend shows modal:
    "Go to auth.openai.com/codex/device and enter code: ABCD-1234"
    [Open link]  [Copy code]
    │
    ▼
User opens URL on any device, logs in to ChatGPT, enters code
    │
    ▼
Backend polls: POST {issuer}/api/accounts/deviceauth/token
    Body: { client_id, device_code, grant_type: "urn:ietf:params:oauth:grant-type:device_code" }
    Every {interval} seconds, up to {expires_in}
    │
    ▼
On success: { access_token, refresh_token, expires_in }
    │
    ▼
Backend stores tokens encrypted in org_credentials (provider: "openai_chatgpt")
    │
    ▼
Before each agent run: write ~/.codex/auth.json into sandbox container
```

### Dual Auth Support

Both auth methods are supported. The orchestrator checks which is available:

```
┌──────────────────────────────────────────────────────┐
│ Before sandbox execution:                             │
│                                                       │
│ 1. Check org_credentials for "openai_chatgpt"         │
│    ├── Found + valid → write auth.json into sandbox   │
│    │   (ChatGPT OAuth — supports gpt-5.3-codex)      │
│    └── Not found / expired / invalid                  │
│                                                       │
│ 2. Check resolveAgentEnv for OPENAI_API_KEY           │
│    ├── Found → inject as env var (existing behavior)  │
│    │   (API key — does NOT support gpt-5.3-codex)     │
│    └── Not found                                      │
│                                                       │
│ 3. Neither available → fail run with "no_credentials" │
└──────────────────────────────────────────────────────┘
```

### Token Lifecycle

```
┌──────────┐    ┌───────────┐    ┌──────────────┐    ┌──────────┐
│ Device   │───▶│  Store    │───▶│  Pre-run     │───▶│  Inject  │
│ Code     │    │  Encrypted│    │  Refresh     │    │  auth.json│
│ Auth     │    │  in DB    │    │  if needed   │    │  into    │
│          │    │           │    │  (< 5 min)   │    │  sandbox │
└──────────┘    └───────────┘    └──────────────┘    └──────────┘
                                        │
                                        ▼
                                 On refresh failure:
                                 mark "invalid",
                                 fall back to API key,
                                 notify user
```

## Credential Storage

### New Provider Type

Add `openai_chatgpt` to the existing `org_credentials` system (doc 25):

```go
const ProviderOpenAIChatGPT ProviderName = "openai_chatgpt"

type OpenAIChatGPTConfig struct {
    AccessToken  string    `json:"access_token"`
    RefreshToken string    `json:"refresh_token"`
    ExpiresAt    time.Time `json:"expires_at"`
    AccountType  string    `json:"account_type"` // "plus", "pro", "team", "enterprise"
}
```

This is separate from the existing `openai` provider (which stores API keys). An org can have both — ChatGPT OAuth for gpt-5.3-codex access, and an API key as fallback.

Tokens are encrypted using the same AES-256-GCM envelope encryption as all other credentials (doc 20). In dev mode without `ENCRYPTION_MASTER_KEY`, plaintext with `v0:` prefix.

### No Schema Migration Needed

The `org_credentials` table already accepts arbitrary `provider` text values. Adding `"openai_chatgpt"` is purely an application-level change.

## Codex CLI Adapter

### Interface Implementation

The adapter follows the same pattern as `claude_code.go` and `gemini_cli.go`:

```go
type CodexAdapter struct {
    logger zerolog.Logger
}

func (a *CodexAdapter) Name() string { return "codex" }

func (a *CodexAdapter) PreparePrompt(ctx, input) (*AgentPrompt, error) {
    // Reuses shared buildSystemPrompt() and buildUserPrompt()
    // from the adapters package (same functions used by claude_code).
    // Token limits: 50k (low mode), 200k (high mode).
}

func (a *CodexAdapter) Execute(ctx, sandbox, prompt, logCh) (*AgentResult, error) {
    // 1. Write prompt to .143-prompt.md
    // 2. Run: codex --full-auto -q "$(cat .143-prompt.md)"
    // 3. Parse output, extract confidence JSON
    // 4. Collect git diff via shared collectDiff()
}
```

### CLI Command

```bash
codex --full-auto -q "$(cat '/workspace/.143-prompt.md')"
```

- `--full-auto`: Auto-approves all tool calls (non-interactive)
- `-q`: Passes the prompt as a quoted string

### Output Parsing

The adapter parses Codex CLI's JSON output, extracting:
- Response text (for summary)
- Token usage (input/output tokens)
- Error messages

### Auth Detection

The Codex CLI automatically detects its auth method:
1. If `~/.codex/auth.json` exists → uses ChatGPT OAuth
2. Else if `OPENAI_API_KEY` is set → uses API key
3. Else → fails with auth error

The orchestrator handles injection before the adapter runs, so the adapter doesn't need to know which auth method is in use.

## Orchestrator Changes

### Auth File Injection

Add a new method to the orchestrator that runs between sandbox creation and repo clone:

```go
func (o *Orchestrator) injectCodexAuth(ctx, orgID, sandbox) error {
    // 1. Check if ChatGPT OAuth token exists for this org
    // 2. If yes: refresh if needed, write auth.json to sandbox
    // 3. If no: return nil (falls back to API key env var)
}
```

The injection uses `provider.WriteFile()` (already available in the SandboxProvider interface) to write `~/.codex/auth.json` into the container. The sandbox user's home directory is `/home/sandbox`, so the path is `/home/sandbox/.codex/auth.json`.

### Modified RunAgent Flow

```
1.  Check concurrency
2.  Update status → "running"
3.  Fetch issue
4.  Get repo details + token
5.  Get adapter for agent type
6.  Prepare prompt
7.  Create sandbox (inject env vars via resolveAgentEnv)
8.  >>> NEW: if agent_type == "codex", call injectCodexAuth() <<<
9.  Clone repo
10. Execute agent
11. Collect result
12. Follow-up jobs
```

### CodexAuthProvider Interface

```go
type CodexAuthProvider interface {
    GetValidToken(ctx context.Context, orgID uuid.UUID) (*models.OpenAIChatGPTConfig, error)
}
```

This abstracts the auth service so the orchestrator doesn't depend on HTTP clients or polling logic. Passed via `OrchestratorConfig`. Can be nil if ChatGPT OAuth is not configured server-wide.

## Default Agent Switch

### OrgSettings Change

Add `DefaultAgentType` to `OrgSettings`:

```go
type OrgSettings struct {
    // ... existing fields ...
    DefaultAgentType string `json:"default_agent_type,omitempty"`
}
```

In `ParseOrgSettings()`, default to `"codex"`:

```go
if s.DefaultAgentType == "" {
    s.DefaultAgentType = "codex"
}
```

### Prioritization Service

The auto-trigger path currently hardcodes `claude_code` (line ~320 in `service.go`):

```go
// Before:
AgentType: "claude_code",

// After:
AgentType: settings.DefaultAgentType,
```

### Runs Handler

The manual trigger path defaults to `"claude_code"` when no agent type is specified. Change to use the org's `DefaultAgentType`.

### Adapter Registration

Add the Codex adapter to the map in `cmd/server/main.go`:

```go
agentAdapters := map[string]agent.AgentAdapter{
    "claude_code": adapters.NewClaudeCodeAdapter(logger),
    "gemini_cli":  adapters.NewGeminiCLIAdapter(logger),
    "codex":       adapters.NewCodexAdapter(logger),
}
```

## Frontend Changes

### Settings Page: Agent Setup (Promoted)

Move agent configuration OUT of "Advanced Settings" into its own top-level section. The new layout:

```
Settings
├── General (org name)
├── Integrations (GitHub, Sentry, Linear)
├── Agent Setup                              ← NEW top-level section
│   ├── Default Agent selector (Codex / Claude Code / Gemini CLI)
│   ├── Codex auth (ChatGPT OAuth or API key)
│   ├── Claude Code auth (API key)
│   └── Gemini auth (API key)
├── Agent Execution (autonomy, aggressiveness, concurrency)
└── Advanced Settings
    └── Prioritization Weights
```

#### Codex Auth UI

When Codex is selected as the default agent:

```
┌──────────────────────────────────────────────────────┐
│  Codex                                                │
│                                                       │
│  Authentication                                       │
│                                                       │
│  ┌─ Sign in with ChatGPT (Recommended) ────────────┐ │
│  │                                                   │ │
│  │  Use your ChatGPT subscription. Required for     │ │
│  │  gpt-5.3-codex.                                  │ │
│  │                                                   │ │
│  │  Status: ✅ Connected (ChatGPT Pro)              │ │
│  │  [Disconnect]                                     │ │
│  │                                                   │ │
│  │  --- OR if not connected: ---                     │ │
│  │                                                   │ │
│  │  [Sign in with ChatGPT]                          │ │
│  └───────────────────────────────────────────────────┘ │
│                                                       │
│  ┌─ API Key (Alternative) ──────────────────────────┐ │
│  │                                                   │ │
│  │  Pay-as-you-go. Does not support gpt-5.3-codex. │ │
│  │  API Key: [________________]     server default   │ │
│  │  Model:   [________________]                      │ │
│  │  Base URL: [________________] (optional)          │ │
│  └───────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────┘
```

#### Device Code Modal

When user clicks "Sign in with ChatGPT":

```
┌──────────────────────────────────────────┐
│  Connect your ChatGPT account            │
│                                          │
│  1. Open this link:                      │
│     auth.openai.com/codex/device         │
│     [Open in new tab]                    │
│                                          │
│  2. Enter this code:                     │
│                                          │
│     ┌──────────────────────┐             │
│     │    ABCD - 1234       │  [Copy]     │
│     └──────────────────────┘             │
│                                          │
│  Waiting for authentication...           │
│  ████████░░░░░░ Expires in 12:34         │
│                                          │
│  [Cancel]                                │
└──────────────────────────────────────────┘
```

The frontend polls `GET /api/v1/settings/codex-auth/status` every 3 seconds. On success, closes modal and shows "Connected" state. On expiry, shows "Expired — try again" with retry button.

### Overview/Onboarding Page

Add an agent setup card after the GitHub connect card:

```
┌──────────────────────────────────────────────────────┐
│  Connect your coding agent                            │
│  143.dev uses AI agents to fix bugs. Connect your     │
│  ChatGPT account to get started.                      │
│                                                       │
│  [Sign in with ChatGPT]                              │
│                                                       │
│  Or configure an API key in Settings.                 │
└──────────────────────────────────────────────────────┘
```

This appears between the GitHub integration card and the "Once integrations are connected..." text. It's the critical second step in onboarding.

### API Client Updates

```typescript
// frontend/src/lib/api.ts
codexAuth: {
    initiate: () =>
        post<SingleResponse<{
            user_code: string;
            verification_uri: string;
            expires_in: number;
        }>>('/api/v1/settings/codex-auth/initiate'),
    status: () =>
        get<SingleResponse<{
            status: 'pending' | 'completed' | 'expired' | 'error';
            account_type?: string;
            message?: string;
        }>>('/api/v1/settings/codex-auth/status'),
    disconnect: () => post('/api/v1/settings/codex-auth/disconnect'),
},
```

### Type Updates

```typescript
// frontend/src/lib/types.ts
export interface OrgSettings {
    // ... existing fields ...
    default_agent_type?: 'codex' | 'claude_code' | 'gemini_cli';
}
```

## API Endpoints

### New Endpoints

```
POST /api/v1/settings/codex-auth/initiate     Admin only. Start device code flow.
GET  /api/v1/settings/codex-auth/status        Admin only. Check auth completion.
POST /api/v1/settings/codex-auth/disconnect    Admin only. Remove ChatGPT auth.
```

### Modified Endpoints

```
GET  /api/v1/settings/agent-defaults           Now includes "codex" in response.
PATCH /api/v1/settings                         Accepts default_agent_type field.
POST /api/v1/issues/{id}/fix                   Defaults to org's default_agent_type.
```

## Error Handling

### Token Errors

| Error | Detection | Recovery |
|-------|-----------|---------|
| Access token expired | `NeedsRefresh(5*time.Minute)` | Auto-refresh via `RefreshToken()` before sandbox creation |
| Refresh token revoked | Refresh endpoint returns 401/403 | Mark credential `"invalid"`, fall back to API key if available, notify user via UI badge |
| Refresh token expired | Specific error from OpenAI | Same as revoked — user must re-authenticate |
| ChatGPT quota exhausted | Codex CLI stderr or exit code | Fail run with `failure_category: "quota_exhausted"`, show "Quota exceeded — wait or upgrade plan" |
| Network error during refresh | HTTP timeout/connection error | Retry once with backoff, then fail run as retriable |

### Auth Flow Errors

| Error | Detection | Recovery |
|-------|-----------|---------|
| Device code expired | Polling past `expires_in` (default 15 min) | Frontend shows "Expired, try again" with retry button |
| User denied auth | Token endpoint returns `access_denied` | Frontend shows "Authentication denied" message |
| Slow down | Token endpoint returns `slow_down` | Double poll interval |
| Device code auth disabled | "Contact workspace admin" response | Show error with instructions to enable in OpenAI settings |

### Adapter Errors

| Error | Detection | Recovery |
|-------|-----------|---------|
| Codex CLI not in sandbox | Exit code 127 (command not found) | Fail with "Rebuild 143-sandbox image with Codex CLI (see sandbox/Dockerfile)" |
| No credentials at all | No API key AND no OAuth token | Fail with `failure_category: "no_credentials"`, prompt user to configure |
| gpt-5.3-codex with API key only | Model requires OAuth | Warn in UI when user selects model, fail gracefully if attempted |

## Security

1. **Encrypted storage**: OAuth tokens encrypted via AES-256-GCM envelope encryption in `org_credentials` — same as all other secrets (doc 20).

2. **Ephemeral sandbox auth**: `auth.json` only exists for the duration of the agent run. Sandbox containers are destroyed after every run.

3. **Refresh outside sandbox**: The orchestrator refreshes tokens before injection. The sandbox network policy does not allow access to `auth.openai.com` — only to `api.openai.com` for model inference.

4. **User-initiated only**: Device code flow requires user to actively visit OpenAI's site and enter a code. The backend never sees the user's ChatGPT password.

5. **Per-org isolation**: Tokens stored per-org in `org_credentials` with `org_id` filtering. Multi-tenant isolation maintained.

6. **No plaintext in logs**: Token values never logged. Only `MaskedSummary()` (first 6 + last 4 chars) reaches API responses.

7. **Public client_id**: We use the same public client_id as the entire ecosystem (Cline, Roo Code, Kilo Code). No client secret needed or stored. If OpenAI ships a registration mechanism, we switch to our own.

## Backward Compatibility

| Scenario | Behavior |
|----------|----------|
| Existing org using `claude_code` | Continues working. `DefaultAgentType` in settings is empty, but the `claude_code` adapter remains registered. |
| Existing org with explicit agent_config | Continues working. Org-level overrides still merged via `resolveAgentEnv()`. |
| New org, no setup done | Dashboard prompts to connect ChatGPT on Overview page. Default agent is `codex`. |
| `OPENAI_API_KEY` env var set on server | Still works. Codex adapter uses it as fallback when no OAuth token exists. |
| `ANTHROPIC_API_KEY` env var set on server | Still works. Claude Code adapter unaffected. |
| Org has both OAuth and API key | OAuth takes priority (written to auth.json). API key env var also present but unused by Codex CLI when auth.json exists. |

## Docker Image Update

The sandbox base image (`143-sandbox:latest`) must include the Codex CLI. This is now handled by `sandbox/Dockerfile` and `sandbox/install-agents.sh`, which installs all three agent CLIs (Claude Code, Codex, Gemini) at pinned versions from `sandbox/versions.json`.

## Implementation Phases

### Phase A: Credential Model + Auth Service (Low risk)

Add `ProviderOpenAIChatGPT` and `OpenAIChatGPTConfig` to the credential model. Implement the Device Code Auth service with `InitiateDeviceAuth`, `PollForToken`, `RefreshToken`, `GetValidToken`. Add API endpoints. No existing behavior changes.

**Files:**
- `internal/models/credentials.go` — new provider + config type
- `internal/services/codexauth/service.go` — new service
- `internal/api/handlers/codex_auth.go` — new handler
- `internal/api/router.go` — register endpoints

### Phase B: Codex Adapter (Low risk)

Implement `AgentAdapter` for Codex CLI following the claude_code.go pattern. Register in main.go. No default changes yet.

**Files:**
- `internal/services/agent/adapters/codex.go` — new adapter
- `cmd/server/main.go` — register adapter

### Phase C: Orchestrator Auth Injection (Low risk)

Add `CodexAuthProvider` interface and `injectCodexAuth()` to the orchestrator. Called only for `agent_type == "codex"`. Falls back silently if no OAuth token. Wire auth service into orchestrator.

**Files:**
- `internal/services/agent/orchestrator.go` — add injection method + interface
- `cmd/server/main.go` — wire dependencies

### Phase D: Default Agent Switch (Medium risk)

Add `DefaultAgentType` to `OrgSettings`. Change prioritization service and runs handler to use it instead of hardcoded `"claude_code"`. Default to `"codex"` for new orgs.

**Files:**
- `internal/models/org_settings.go` — add field + default
- `internal/services/prioritization/service.go` — use setting instead of hardcode
- `internal/api/handlers/runs.go` — use setting for manual trigger default

### Phase E: Frontend Overhaul (Low risk)

Promote agent setup from Advanced Settings. Add ChatGPT OAuth UI with device code modal. Add agent setup card to Overview page. Update API client and types.

**Files:**
- `frontend/src/app/(dashboard)/settings/page.tsx` — restructure
- `frontend/src/app/(dashboard)/overview/page.tsx` — add agent setup card
- `frontend/src/lib/api.ts` — add codexAuth methods
- `frontend/src/lib/types.ts` — add default_agent_type

### Rollback

Each phase is independently reversible:
- Phase A: Delete new provider type, endpoints still exist but unused
- Phase B: Remove adapter from registration map
- Phase C: `injectCodexAuth` returns nil → no-op
- Phase D: Change default back to `"claude_code"` in `ParseOrgSettings`
- Phase E: Revert frontend changes

## Testing

### Unit Tests

| Area | Tests |
|------|-------|
| `OpenAIChatGPTConfig` | Validate, MaskedSummary, IsExpired, NeedsRefresh, ParseProviderConfig |
| `codexauth.Service` | Initiate with mock HTTP, poll success/expiry/slow_down, refresh success/failure, GetValidToken auto-refresh |
| `CodexAdapter` | PreparePrompt with various inputs, Execute with mock sandbox, output parsing, confidence extraction |
| `Orchestrator.injectCodexAuth` | Token available → writes auth.json, no token → no-op, expired → refreshes then writes |
| `CodexAuthHandler` | Initiate/status/disconnect endpoints |

### Integration Tests

- Device code flow end-to-end with mock OpenAI auth server
- Codex adapter execution in Docker container with mock Codex CLI binary

### Manual Testing Checklist

- [ ] New org: Overview shows agent setup card
- [ ] Click "Sign in with ChatGPT" → device code modal appears
- [ ] Complete auth → modal closes, "Connected" shown
- [ ] Trigger manual fix → Codex runs with ChatGPT auth
- [ ] Disconnect ChatGPT → falls back to API key (if configured)
- [ ] Existing claude_code org → still works, no UI changes forced
- [ ] Settings page → agent setup is top-level, not under Advanced
- [ ] Select different default agent → auto-trigger uses it

## Connection with Other Design Docs

**Agent Orchestrator (doc 06)**: The Codex adapter follows the same `AgentAdapter` interface. The orchestrator's `resolveAgentEnv` continues to handle API key injection. Auth file injection is additive.

**Security Architecture (doc 20)**: OAuth tokens use the same encryption infrastructure. Sandbox isolation unchanged — auth.json is ephemeral.

**Onboarding / activation**: Agent setup becomes part of the onboarding checklist. The quick-start flow should trigger Codex by default.

**Dashboard Credentials (doc 25)**: The `openai_chatgpt` provider type follows the same `ProviderConfig` interface pattern. Stored in the same `org_credentials` table with the same encryption.

**Database Schema (doc 01)**: No schema changes. `org_credentials` already supports arbitrary providers. `organizations.settings` JSONB gains `default_agent_type` field (no migration needed).
