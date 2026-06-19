# Design: 143-tools Local Install & Team Auth

> **Status:** Implemented | **Last reviewed:** 2026-06-19

## Implementation notes (2026-06-09)

All four phases shipped. Deltas from the text below:

- **Migration numbers**: 000164 was taken by the Mezmo integration while this
  doc was in review, so the Phase 2 tables landed as `000165_cli_auth` and
  Phase 3 as `000166_org_join_tokens`.
- **Version-skew enforcement**: `/api/v1/cli/version` + the 426
  `CLI_UPDATE_REQUIRED` gate (`middleware.CLIVersionGate`) are implemented;
  enforcement engages only when `CLI_MIN_SUPPORTED_VERSION` is set to an
  orderable version. Git-SHA builds (the current default for
  `version.BuildSHA`) are never blocked — comparing SHAs is meaningless. The
  CLI's "newer CLI available" hint compares against `/api/v1/cli/version`
  during `whoami`/`update` rather than reading a header off every response.
- **Per-token join rate limiting**: no dedicated limiter was added. Join
  validation only ever runs inside the GitHub OAuth callback (a full OAuth
  round-trip with a state cookie per attempt) behind the global per-IP rate
  limiter, which throttles enumeration harder than a per-token bucket would.
- **Secret scanning**: the repo runs no secret scanner today; the `143u_`/
  `143j_` patterns are documented in `docs/guides/cli-install.md` for teams
  that do, and the sandbox log-redaction layer strips both shapes.
- **Recoverable join links**: as of migration `000209`, newly created
  `org_join_tokens` rows keep `token_hash` as the validation key and also store
  `raw_token_encrypted` with the same AES-GCM application encryption used for
  credentials. Admins can fetch the install command again for active,
  unexpired, unexhausted links through a separate endpoint. Successful reveals
  emit `org.join_token_revealed` without the raw token in audit details. Rows
  created before `000209` remain unrecoverable because only the hash and
  display prefix were retained.
- **Key files**: distribution `internal/api/handlers/cli_distribution.go`
  (+ `assets/install.sh.tmpl`); login flow `internal/api/handlers/auth_cli.go`;
  gateway `internal/api/handlers/cli_tools.go` +
  `internal/services/mcp/org_registry.go`; laptop CLI `internal/cli/`;
  stores `internal/db/{user_cli_tokens,cli_auth_codes,org_join_tokens}.go`;
  UI `frontend/src/components/cli-{join-tokens,sessions}-card.tsx`.

## Goal

Let anyone on a 143 team install the `143-tools` CLI on their laptop and authenticate
with a single command — even if they don't have a 143 account yet:

```bash
curl -fsSL https://143.com/install/<JOIN_TOKEN> | sh
```

One command total: the installer downloads the binary, writes the config, and
chains straight into `143-tools login`, so the browser opens for GitHub sign-in
as the install finishes. The join token lives in the install URL itself, so the
server can template the script with the server URL *and* the token already baked
in — nothing to pass after `| sh`, nothing to configure.

The join token is optional — the tokenless form installs and logs in exactly the
same way, for existing users or anyone without a join link:

```bash
curl -fsSL https://143.com/install.sh | sh
```

`login` opens the browser, the user signs in with GitHub (existing OAuth), and if they
have no 143 account one is created and joined to the org on the spot (JIT provisioning).
The CLI ends up holding a personal, revocable token. No pre-registration, no manual
server-URL configuration, no copy-pasting API keys.

**The first consumer is engineers' local coding agents.** Today every engineer
hand-configures their own MCP servers (Sentry, Linear, Notion, Slack) with their own
credentials. After this ships, the 143-tools install replaces that zoo: one binary,
one login, and every integration the org has connected becomes available to the
engineer's local agents — with every tool call flowing through the 143 server, where
org credentials live and per-user audit happens. See "Local agent gateway" below.

Today `143-tools` (`cmd/tools/`) is only built into the sandbox Docker image
(`sandbox/Dockerfile`) and authenticates with sandbox-injected env tokens. This design
adds: standalone binary distribution served by the 143 server, a browser-based CLI login
flow, per-user CLI tokens, and multi-use org join tokens.

## Design decisions (and the alternatives rejected)

1. **Binaries are served by the 143 server itself**, not GitHub Releases/Homebrew.
   The install script comes *from* the server, so it can bake the server URL into the
   CLI config — zero-config for self-hosted deployments, and the CLI version always
   matches the server version. GitHub Releases + a tap can be added later for the
   public OSS audience; it is out of scope here.

2. **The shared "single team token" is a join token, not an API token.** A shared
   credential that grants API access would destroy per-user audit trails and make
   offboarding revoke everyone at once. The join token grants exactly one right:
   "a GitHub-authenticated person may become a member of this org." Every person
   still ends up with their own user row and their own CLI token.

3. **Login uses browser + localhost callback** (the `gcloud`/`fly` pattern), reusing
   the existing GitHub OAuth handler (`internal/api/handlers/auth.go`). The OAuth
   device-code flow (for SSH/headless boxes) is deferred.

4. **The browser never sees the real token.** The callback redirects to
   `http://127.0.0.1:<port>/callback?code=<one-time-code>`; the CLI exchanges the
   code (plus a PKCE-style verifier) for the token via a direct POST. This keeps
   long-lived credentials out of browser history and proxy logs, and the verifier
   prevents another local process from racing the exchange.

5. **CLI tokens are user-scoped session-equivalents**, stored hashed using the same
   prefix + hash scheme as `api_tokens` (`internal/models/api_client.go`). They are
   a new table rather than a reuse of `api_tokens` because api tokens belong to org
   API clients with explicit scopes, while CLI tokens act *as a user* (org resolution
   via memberships + `X-Active-Org-ID`, same as sessions).

## UX flows

### First-time install + join (no 143 account)

```
admin:  Settings → Members → "CLI install link" → copies one-liner into Slack
        curl -fsSL https://143.com/install/143j_Ab3x9k | sh

user:   pastes the one-liner
        → server returns the install script with server URL + join token templated in
        → script detects OS/arch, downloads binary to ~/.local/bin/143-tools
        → writes ~/.config/143-tools/config.json {server_url, pending join token}
        → chains into `143-tools login`:
            → CLI starts listener on 127.0.0.1:<random port>, opens browser to
              {server}/api/v1/auth/cli/start?port=...&challenge=...&join=143j_Ab3...
            → GitHub OAuth (existing flow)
            → no matching user → join token validated → user + membership created
            → browser shows "You're in — return to your terminal"
            → CLI receives one-time code, exchanges it, stores token
            → prints "Logged in as @octocat (Acme Org)"
```

### Existing 143 user, new laptop

Same one-liner without the token segment — `curl -fsSL https://143.com/install.sh | sh` —
which likewise chains into `login`. The OAuth callback matches their existing GitHub
ID and mints a CLI token for the new device. (The tokened link also works fine for existing users —
the join token is simply a no-op for someone already in the org — so admins only ever
need to share one link.)

### Day-2 commands

```
143-tools whoami                  # user, org, role, token prefix, server
143-tools logout                  # revokes the CLI token server-side, clears config
143-tools update                  # re-downloads the binary from the server (version skew check)
143-tools preview create --wait   # repo/branch inferred from cwd git context → prints 143.dev URL
```

Every authenticated API response may include the server version; the CLI prints a
one-line "newer CLI available, run `143-tools update`" hint when out of date.

## Local agent gateway (the first consumer)

In sandboxes, `143-tools` executes integration tools (registry:
`internal/services/mcp/tools.go`, runner: `internal/services/mcp/cli.go`) by
calling providers **directly** with sandbox-injected env credentials
(`SENTRY_AUTH_TOKEN=...`). That model must NOT be replicated locally — org
integration credentials should never land on laptops. Instead, local mode keeps
the same tool surface but swaps the execution backend:

```
local agent ──stdio MCP──► 143-tools mcp serve ──HTTPS + 143u_ token──► 143 server
                                                   │ executes tool with org creds
                                                   │ (integrations table), per-user
                                                   ▼ audit + policy + rate limits
                                              Sentry / Linear / Notion / Slack
```

- `143-tools mcp serve` — a stdio MCP server exposing the same tool registry.
  Registration is one line per agent, e.g.
  `claude mcp add 143 -- 143-tools mcp serve` (equivalents for Cursor/Codex).
  Agents that prefer shell tools can also just invoke the existing
  `143-tools <namespace> <action>` commands; both paths share the backend.
- New endpoint: `POST /api/v1/cli/tools/invoke` — bearer auth (`143u_`), body
  `{"tool": "<registry name>", "args": {...}}`, response is the tool result.
  The server resolves the active org (same resolution as everywhere else),
  loads that org's integration credentials, executes via the same service-layer
  implementations the sandbox/server already use, and emits a per-user audit
  event per call (`cli.tool_invoked`, with tool name — not args, which can
  contain sensitive content).
- Tool *availability* mirrors the org's connected integrations: the MCP server
  fetches the tool list from the server at startup (`GET /api/v1/cli/tools`)
  rather than hardcoding it, so an org without Notion connected doesn't surface
  Notion tools to local agents.
- Execution-backend selection is automatic: sandbox env credentials present →
  direct mode (existing behavior, unchanged); config-file token present →
  server-proxied mode. Same binary, no flags.
- **Platform tools, starting with previews.** The registry gains 143's own
  capabilities, not just third-party integrations. V1: `preview_create`,
  `preview_status`, `preview_list`, `preview_stop` — thin wrappers over the
  existing `/api/v1/previews*` REST endpoints, which already work under bearer
  auth (acts-as-user) with no new server code. The target experience: tell your
  local agent "spin up a preview of this branch" and get back a live
  `https://....143.dev` URL.
  - `preview_create` accepts a repository **name** (resolved server-side to the
    UUID; agents don't know UUIDs) and a branch, and returns
    `{preview_id, preview_url, status}` immediately — the agent polls
    `preview_status` until ready rather than holding a long-lived tool call.
  - The human command infers both arguments from the cwd:
    `143-tools preview create` reads the git remote → repository, `HEAD` →
    branch; `--wait` blocks and prints the URL when the preview is up.
  - The branch must exist on the remote (previews build from the pushed repo,
    not the local working tree). The tool returns a distinct
    `BRANCH_NOT_PUSHED` error naming the fix (`git push -u origin <branch>`)
    so agents recover without guessing — this will be the #1 failure mode for
    local use.

This is also the control-point argument for the whole project: revoking one
user's CLI token (or removing them from the org) instantly cuts their local
agents off from every integration, and the org's provider tokens only ever
exist server-side.

## Database schema

Three new tables (Phase 2 migration `000164`: `user_cli_tokens` + `cli_auth_codes`;
Phase 3 migration: `org_join_tokens`). Tenancy: `org_join_tokens` is org-scoped;
`user_cli_tokens` and `cli_auth_codes` are user-scoped (a user's tokens follow them
across orgs, like `auth_sessions`).

### `org_join_tokens` — multi-use, revocable join links

Deliberately a new table rather than extending `invitations`: invitations are
single-use and targeted at a person (email/github username required); join tokens
are multi-use and untargeted.

```sql
CREATE TABLE org_join_tokens (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    token_hash         TEXT NOT NULL,            -- same scheme as api_tokens
    token_prefix       TEXT NOT NULL,            -- "143j_" + first 8 chars, for UI display
    raw_token_encrypted BYTEA,                   -- encrypted plaintext for admin re-copy
    role               TEXT NOT NULL DEFAULT 'member',  -- role granted on join
    name               TEXT NOT NULL DEFAULT '', -- e.g. "Eng team link, June 2026"
    created_by_user_id UUID NOT NULL REFERENCES users(id),
    max_uses           INTEGER,                  -- NULL = unlimited
    use_count          INTEGER NOT NULL DEFAULT 0,
    expires_at         TIMESTAMPTZ,              -- NULL = no expiry
    revoked_at         TIMESTAMPTZ,
    revoked_by_user_id UUID REFERENCES users(id),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_org_join_tokens_org ON org_join_tokens(org_id);
-- Lookup key is the deterministic hash; token_prefix is display-only and
-- intentionally NOT unique (matches api_tokens, migration 000161).
CREATE UNIQUE INDEX idx_org_join_tokens_hash ON org_join_tokens(token_hash);
```

`role` follows the typed-enum convention: validated via `models.Role.Validate()` in
the handler; a CHECK constraint + migration-pin test per the project standard for
CHECK-constraint columns. Default and recommended role: `member` (admin can pick
`viewer`/`builder` when creating the link if they want a lower blast radius).

A join is valid when: not revoked, not expired, and (`max_uses IS NULL OR
use_count < max_uses`). `use_count` increments atomically inside the same
transaction that creates the membership (`UPDATE ... SET use_count = use_count + 1
WHERE ... AND (max_uses IS NULL OR use_count < max_uses)` — zero rows updated means
the token raced out of uses).

### `user_cli_tokens` — per-user, per-device CLI credentials

```sql
CREATE TABLE user_cli_tokens (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash         TEXT NOT NULL,            -- same scheme as api_tokens
    token_prefix       TEXT NOT NULL,            -- "143u_" + first 8 chars
    device_name        TEXT NOT NULL DEFAULT '', -- CLI sends hostname at login
    last_org_id        UUID REFERENCES organizations(id) ON DELETE SET NULL,
    expires_at         TIMESTAMPTZ NOT NULL,     -- default now() + 90 days
    last_used_at       TIMESTAMPTZ,
    last_used_ip       TEXT,
    revoked_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_user_cli_tokens_user ON user_cli_tokens(user_id);
-- Lookup key is the deterministic hash; token_prefix is display-only and
-- intentionally NOT unique (matches api_tokens, migration 000161).
CREATE UNIQUE INDEX idx_user_cli_tokens_hash ON user_cli_tokens(token_hash);
```

`last_org_id` mirrors `auth_sessions.last_org_id` so active-org resolution works
identically (header → token's last_org_id → oldest membership).

### CLI auth handshake state — `cli_auth_codes`

The one-time code from the OAuth callback must survive across requests and across
server replicas (rolling deploys), so it lives in a small table, not memory:

```sql
CREATE TABLE cli_auth_codes (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash      TEXT NOT NULL UNIQUE,         -- SHA-256 of the one-time code
    challenge      TEXT NOT NULL,                -- SHA-256(verifier) from the CLI, hex
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id         UUID REFERENCES organizations(id) ON DELETE CASCADE,  -- nullable: resolved like sessions (last_org_id → oldest membership); a zero-membership user can still complete login
    device_name    TEXT NOT NULL DEFAULT '',
    expires_at     TIMESTAMPTZ NOT NULL,         -- now() + 60 seconds
    consumed_at    TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Rows are single-use (`consumed_at` set atomically on exchange) and garbage-collected
opportunistically (`DELETE ... WHERE expires_at < now() - interval '1 hour'` on insert).

## API contract

### Distribution (public, no auth)

| Route | Method | Description |
|---|---|---|
| `/install.sh` | GET | Shell installer, templated with the server's own `BaseURL`. The path follows the dominant industry convention (`tailscale.com/install.sh`, `fly.io/install.sh`, `ollama.com/install.sh`, `claude.ai/install.sh`) — instantly recognizable and self-documenting. A `get.` subdomain (Docker/k3s style) was rejected: it would force every self-hoster to provision extra DNS + TLS, while a path works on whatever domain the server already has. |
| `/install/{join_token}` | GET | Same installer with the join token additionally templated into the config-write step. The token is a path segment rather than `?join=` query because an unquoted `?` breaks under zsh (the macOS default shell) with "no matches found". **Syntactic validation is mandatory**: the segment must match `^143j_[A-Za-z0-9]{12,64}$` or the route 404s — this is untrusted input being templated into a script the user pipes to `sh`, so anything outside that charset is a shell-injection vector, and the templating must single-quote the value besides. Existence/validity is deliberately **not** checked here (revoked links should still install the binary; the join is validated at login). |
| `/download/143-tools/{os}/{arch}` | GET | Binary. `os ∈ {darwin, linux}`, `arch ∈ {amd64, arm64}`. 404 for unknown pairs. Sets `X-Checksum-Sha256` header. |
| `/download/143-tools/checksums.txt` | GET | SHA-256 checksums for all four binaries; the installer verifies after download. |
| `/api/v1/cli/version` | GET | `{"data": {"version": "<git sha/tag>", "min_supported": "..."}}`. `min_supported` is enforced, not advisory: authenticated requests from a CLI sending a `User-Agent: 143-tools/<version>` below it get `426 CLI_UPDATE_REQUIRED` with a message naming `143-tools update`, so breaking API changes have a clean failure mode instead of confusing errors. |

These are served outside `/api/v1` (except version) because they are fetched by
`curl`/`sh`, not the JSON API client. **Routing caveat**: the production Caddyfile
(`deploy/Caddyfile`) routes only `/api/*` to the Go server — everything else falls
through to the Next.js frontend. These routes therefore need explicit `handle`
blocks (`/install*`, `/download/*`) pointing at the `api` upstream,
inserted before the frontend fallthrough, with the change covered by
`deploy/deploy_config_test.go`. Self-hosters with their own proxy need the same
two rules (document this in the install docs). Rate-limit modestly; responses
are static files read from `/opt/143/cli/` inside the server image.

### CLI login flow

| Route | Method | Auth | Description |
|---|---|---|---|
| `/api/v1/auth/cli/start` | GET | none | Query: `port` (1024–65535), `challenge` (64 hex chars), optional `join` (join token), optional `device` (hostname, ≤64 chars). Stores all three in short-lived HttpOnly cookies (`cli_port`, `cli_challenge`, `cli_device`, `pending_join` — same pattern as `pending_invitation`), then redirects into the existing GitHub OAuth `Login` handler. |
| `/api/v1/auth/github/callback` | GET | none | **Existing handler, extended.** After user resolution (existing / linked / JIT-created — see below), if the `cli_port` cookie is present: mint a one-time code, insert `cli_auth_codes` row, clear the CLI cookies, and redirect to `http://127.0.0.1:{port}/callback?code={code}` instead of `FrontendURL`. The normal web session + CSRF cookies are **still installed** on this branch (the user just completed a full OAuth login — leaving them signed into the web app too is free and expected). On CLI-side failure the browser still lands on a server-rendered "return to terminal" page. |
| `/api/v1/auth/cli/exchange` | POST | none | Body: `{"code": "...", "verifier": "..."}`. Validates: row exists by `SHA256(code)`, unconsumed, unexpired, and `SHA256(verifier) == challenge`. Mints a `user_cli_tokens` row and returns the plaintext token exactly once: `{"data": {"token": "143u_...", "user": {...}, "org": {"id", "name"}, "expires_at"}}`. Errors: `410 CLI_CODE_EXPIRED`, `400 CLI_VERIFIER_MISMATCH`, `400 INVALID_BODY`. **Must be registered outside the CSRF-wrapped auth group**: `middleware.CSRF` rejects cookie-less state-changing requests (the CLI has no CSRF cookie/header), and the one-time code + verifier binding is a strictly stronger anti-forgery guarantee than the double-submit cookie. |

JIT provisioning inside the callback (the only change to user-resolution logic):
when no user matches the GitHub ID/email **and** a valid `pending_join` cookie is
present, create the user + `organization_memberships` row (role from the join token,
`GrantAtLeast` semantics — never downgrades an existing membership) + increment
`use_count`, all in one transaction, mirroring `acceptInvitationAndUpsertUser`.
An *existing* user carrying a join token for an org they're not in gets a membership
granted the same way (matches `ClaimInvitation` behavior).

Invalid/exhausted join token + **new** user: unlike the web invitation flow (which
forgives and creates a personal org, because the user is in a browser and can
navigate), the CLI flow **fails closed** — no user is created, the browser gets a
server-rendered "this join link is expired or revoked — ask your admin for a new
one" page, and the loopback redirect carries `?error=JOIN_TOKEN_INVALID` so the
CLI prints the same message and exits non-zero. A forgiving fallback here would
silently log the CLI into a fresh single-member org, which is strictly worse than
the error for someone trying to join a team. Existing users with a bad join token
just log in normally (the token was a no-op for them anyway).

### Bearer auth for CLI tokens

The auth middleware gains one branch: `Authorization: Bearer 143u_...` → compute
`SHA256(token)` and look up `user_cli_tokens` by `token_hash` directly (the
existing api-token scheme in `internal/db/api_clients.go` is deterministic
`"sha256:" + hex`, so the hash itself is the lookup key — add a unique index on
`token_hash`; `token_prefix` is display-only). Check `revoked_at`/`expires_at`,
stamp `last_used_*` (throttled, e.g. once per minute), and populate the same user
context as a session cookie. Org resolution: `X-Active-Org-ID` header → token's
`last_org_id` → oldest membership — identical to sessions. The distinct `143u_`
prefix (vs. api tokens) routes which table the middleware consults; the existing
CSRF middleware already skips bearer-authenticated requests.

**Expiry is sliding, not fixed**: on authenticated use, extend `expires_at` to
`now() + 90 days` (piggybacked on the throttled `last_used_*` write). Active users
never get logged out; a laptop idle for 90 days does. A fixed 90-day expiry would
force the whole team through a quarterly re-login for no security benefit, since
revocation — not expiry — is the real control here.

### Token + join-link management

| Route | Method | Auth | Description |
|---|---|---|---|
| `/api/v1/org/join-tokens` | POST | session, admin | Body: `{"name", "role", "max_uses?", "expires_in_days?"}`. Returns the plaintext token and ready install command on creation: `{"data": {"id", "token": "143j_...", "install_command": "curl -fsSL .../install/143j_... \| sh"}}`; new rows also store an encrypted raw token for later admin re-copy. |
| `/api/v1/org/join-tokens` | GET | session, admin | List (prefix, name, role, use_count, status, `can_reveal`). Does not include plaintext links. |
| `/api/v1/org/join-tokens/{id}/link` | GET | session, admin | Returns `{"data": {"id", "token_prefix", "install_command"}}` for active recoverable links. Returns `409 JOIN_TOKEN_NOT_RECOVERABLE` for pre-`000209` legacy rows. |
| `/api/v1/org/join-tokens/{id}` | DELETE | session, admin | Revoke. |
| `/api/v1/auth/cli-tokens` | GET | any auth | List the caller's own CLI tokens (device, prefix, last_used). |
| `/api/v1/auth/cli-tokens/{id}` | DELETE | any auth | Revoke own token. `logout` calls this. |

Frontend: a "CLI" card on the Members settings page (create/copy join link, list
active links, re-copy active recoverable links on demand) and a "CLI sessions" list on the user's own settings page. Removing a
member from the org must also revoke their CLI tokens' access — this falls out
naturally because bearer auth resolves org access through memberships, but the
member-removal handler should additionally revoke `user_cli_tokens` rows for users
with zero remaining memberships.

## CLI changes (`cmd/tools/`)

New top-level commands, kept alongside the existing sandbox-mode commands (which
continue to read sandbox env vars and are unaffected):

- `143-tools login [--server URL] [--join TOKEN] [--no-browser]` — generates a
  32-byte verifier, starts a loopback listener on a random port, opens
  `{server}/api/v1/auth/cli/start?...` (prints the URL with `--no-browser`),
  waits ≤5 min for the callback, exchanges, persists. The loopback handler responds
  with a tiny "you can close this tab" page.
- `143-tools logout`, `143-tools whoami`, `143-tools update`.

Re-login on a device that already has a token revokes the older `user_cli_tokens`
rows with the same `device_name` after the new token is confirmed working — so the
"CLI sessions" list stays one-row-per-device instead of accreting duplicates.

Config file `~/.config/143-tools/config.json`, mode `0600`:

```json
{ "version": 1, "server_url": "https://143.com", "token": "143u_...", "org_id": "..." }
```

The `version` field exists so the format can evolve (the obvious future change is
multi-server profiles, gh-style `hosts` keyed by server URL — don't build it now,
but don't ship an unversioned format that makes it a breaking change later).

File-based storage at v1 (matches gh/fly precedent); OS keychain is a follow-up.
Precedence for credentials: sandbox env vars (existing behavior) → `--token` flag →
config file, so the same binary works in both worlds.

## Build & distribution build-out

1. **Makefile**: `make build-cli` cross-compiles `./cmd/tools` for
   `{darwin,linux} × {amd64,arm64}` with `CGO_ENABLED=0` and
   `-ldflags "-X main.version=$(GIT_SHA)"`, outputs to `dist/cli/` plus
   `checksums.txt`.
2. **Server image** (`Dockerfile` for `143-server`): a build stage runs
   `make build-cli`; final stage copies `dist/cli/` to `/opt/143/cli/`. Adds
   ~60 MB to the image — acceptable; revisit GHCR-hosted artifacts if it grows.
   deploy.yml already builds/pushes the server image; the only other deploy
   change is the Caddyfile routing rules called out in the API contract section.
3. **Server**: static handler serving `/opt/143/cli/` for the download routes;
   `install.sh` is a Go-templated asset (server URL, optional join token injected).
4. **Installer script** behavior: `uname -s/-m` detection, download + checksum
   verify, install to `~/.local/bin` (fall back to `/usr/local/bin` with sudo
   prompt), PATH hint if needed, write `server_url` (+ pending join token, both
   pre-templated by the server) into the config file, then **chain into
   `143-tools login`** so install + auth is one command. The chain is skipped —
   printing the `143-tools login` next step instead — when there's no TTY (CI,
   provisioning scripts) or when already logged in to this server; login itself
   reads nothing from stdin (it waits on the loopback socket), so running under
   `curl | sh` with stdin consumed by the pipe is fine. Idempotent — re-running
   upgrades in place. The entire script body
   must be wrapped in a function invoked on the last line, so a connection that
   drops mid-download executes nothing rather than half a script. macOS note:
   `curl` does not set the quarantine xattr, so unsigned Go binaries run without
   Gatekeeper friction — notarization only becomes a question if/when binaries
   are later distributed via browser download or Homebrew.

## Security considerations

- **Join token blast radius**: grants membership only (default `member`), never API
  access. Revocable and listable in the UI; optional `max_uses`/expiry. It will leak
  into shell history and — because it rides in the `/install/{join_token}` URL — into
  server access logs and any intermediary proxy logs, by design. That's acceptable
  for this privilege level, and one-click revoke is the mitigation; still, the
  server's request logger must redact the path segment (log as `/install/:token`), and
  the route must be HTTPS-only. Rate-limit join attempts per token.
- **Join link recovery**: recovering an existing link does not grant a new
  capability to admins who can already create equivalent links, but it does
  turn the raw join token into retrievable secret material at rest. Keep
  validation on `token_hash`, encrypt `raw_token_encrypted` with
  `ENCRYPTION_MASTER_KEY` when configured, omit plaintext from list responses,
  and only reveal active links through an explicit audited admin action.
- **One-time code**: 60-second TTL, single use, stored hashed, bound to the CLI's
  verifier (challenge = SHA-256). The browser/history only ever sees the code, never
  a credential.
- **Loopback redirect**: hardcode `127.0.0.1` (never `localhost`, which can resolve
  unexpectedly); port comes from the validated `cli_port` cookie set by `/cli/start`.
- **CLI tokens**: hashed at rest (same scheme as `api_tokens`), prefix-routed lookup,
  90-day default expiry, per-device rows so revocation is surgical, `last_used_*`
  for audit. Audit events for login/logout/JIT-join via the existing `AuditEmitter`
  (`auth.cli_login`, `auth.cli_logout`, `org.join_token_used`).
- **DemoMode**: CLI login depends on GitHub OAuth, which DemoMode disables —
  `/cli/start` returns `409 GITHUB_OAUTH_DISABLED` in that case.
- **Secret scanning**: the distinctive `143u_` / `143j_` prefixes exist partly so
  tokens are machine-findable when leaked. Register both patterns wherever the
  project runs secret scanning (gitleaks config / GitHub custom patterns), and
  the sandbox log-redaction layer should learn them too.
- **Sandbox guardrails**: inside a sandbox the binary must refuse `login` and
  `update` (env-detected, same signal the existing sandbox commands use) — agents
  should never be able to mint user credentials or self-replace the tool.

## Testability

The whole login flow is end-to-end testable in Go without touching real GitHub:
`AuthHandler.SetGitHubURLsForTest` already points the OAuth exchange at an
`httptest.Server`, and the CLI side is a loopback listener plus two HTTP calls —
a single test can run `/cli/start` → stubbed GitHub → callback → loopback →
`/cli/exchange` and assert a working bearer token comes out, including the
JIT-join and fail-closed join-token paths.

## Implementation plan

Phase 1 — distribution (shippable alone): Makefile target, Dockerfile stage,
download/install.sh/version endpoints. *Verify: fresh laptop, `curl | sh`, binary runs.*

Phase 2 — CLI tokens + login: migration 000164 (`user_cli_tokens`,
`cli_auth_codes`), models + stores (mirror `api_client.go` patterns), middleware
bearer branch, `/cli/start` + callback extension + `/cli/exchange`, CLI
`login/logout/whoami`, self-service token list/revoke. *Verify: existing user logs
in on a clean machine, `whoami` works, revoke kills it.*

Phase 3 — join tokens + JIT: `org_join_tokens` migration + CHECK-pin test, admin
CRUD endpoints, callback JIT path, Members-page UI card, `/install/{join_token}`
installer templating + access-log redaction. *Verify: brand-new GitHub user goes from Slack one-liner to `whoami`
showing the right org without touching the web app first.*

Phase 4 — local agent gateway (parallelizable with Phase 3): `GET /api/v1/cli/tools`
+ `POST /api/v1/cli/tools/invoke`, server-proxied execution backend in
`internal/services/mcp`, `143-tools mcp serve` stdio mode, preview tools
(registry wrappers over `/api/v1/previews*` + cwd-inferring `preview create`
command), `cli.tool_invoked` audit events. *Verify: `claude mcp add 143 --
143-tools mcp serve` on a laptop, then (a) agent fetches a Sentry issue with
zero provider credentials on the machine, and (b) "create a preview of this
branch" comes back with a live 143.dev URL.*

Out of scope (future): device-code flow for headless machines, Homebrew tap /
GitHub Releases via goreleaser, OS keychain storage, Windows support.
