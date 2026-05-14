# Coding-Agent Rate-Limit Fallback

> **Status:** Implemented | **Last reviewed:** 2026-05-13

Coding-agent credentials are durable ordered stacks, so temporary upstream failures need to be visible to every worker and every continuation path. Rate limits are treated as credential health metadata, not as permanent credential status.

## Credential Health

`coding_credentials` stores nullable rate-limit metadata:

- `rate_limited_until` — when the credential can be considered again.
- `rate_limited_observed_at` — when the agent runtime observed the limit.
- `rate_limit_message` — the provider/runtime message retained for audit and UI display.

The stored credential status remains `active`, `invalid`, `pending_auth`, or disabled-style values. API summaries derive `status = "rate_limited"` only when an active credential has `rate_limited_until > now`. Expired metadata remains visible for audit but is non-blocking.

Hard auth rejections are different: when an agent reports a non-recoverable invalid token/key signal, the picked credential is persisted as `invalid` so future workers do not retry it.

## Selection Rules

Credential pickers filter out active credentials with future `rate_limited_until` before applying the existing ordered stack rules:

1. Same runtime-compatible credential set.
2. Personal credentials before organization credentials.
3. Priority order within each scope.
4. Random selection across currently available credentials with the same priority.

The behavior is generic across coding-agent providers. Codex and Claude Code use twin provider stacks for API-key and subscription auths, while Gemini, Amp, and Pi use their API-key credential stacks. Future coding-agent providers should add compatible provider mappings and inherit the same filtering semantics.

## Continue Sessions

Every `continue_session` resolves credentials fresh for the session's agent type. If the previously used credential is rate-limited, the resolver treats it as temporarily unavailable and picks the next compatible credential when one exists.

If no compatible credential can run, the continuation fails before sandbox execution with a user-facing message naming the agent and reset time when known, for example:

> All Claude Code auths are rate limited until 8:50 AM. Try again then or add another Claude Code auth.

The blocked path logs structured metadata including `session_id`, `agent_type`, `provider`, `rate_limited_until`, and whether fallback candidates existed but were also unavailable.

## Runtime Signals

Agent results are normalized into a shared credential failure signal:

- Rate limits: generic `rate limit`, `rate_limited`, `429`, `too many requests`, `quota exceeded`, `usage limit`, plus provider-specific wording surfaced by Codex, Claude Code, Gemini, Amp, and Pi.
- Reset extraction: `Retry-After`, `retry-after=`, `try again at ...`, and comparable reset text.
- Auth rejection: `invalid_grant`, `refresh_token_reused`, Codex token revoked/invalidated signals, and 401-style invalid key/token messages.

If no reset time can be parsed, the runtime persists the raw message and a conservative default TTL.
