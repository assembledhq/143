-- CLI login flow state: per-user/per-device CLI credentials plus the
-- short-lived one-time codes that bridge the browser OAuth callback to the
-- CLI's loopback listener. See docs/design 96-cli-local-install-and-team-auth.

-- Per-user, per-device CLI credentials. Stored hashed using the same
-- deterministic "sha256:"+hex scheme as api_tokens, so token_hash itself is
-- the lookup key. token_prefix is display-only and intentionally NOT unique
-- (matches api_tokens, migration 000161).
CREATE TABLE user_cli_tokens (
    -- lint:no-org-id reason="user-scoped credential like auth_sessions; a user's CLI tokens follow them across orgs and active-org resolution happens per-request via memberships"
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash         TEXT NOT NULL,
    token_prefix       TEXT NOT NULL,
    device_name        TEXT NOT NULL DEFAULT '',
    last_org_id        UUID REFERENCES organizations(id) ON DELETE SET NULL,
    expires_at         TIMESTAMPTZ NOT NULL,
    last_used_at       TIMESTAMPTZ,
    last_used_ip       TEXT,
    revoked_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_user_cli_tokens_user ON user_cli_tokens(user_id);
CREATE UNIQUE INDEX idx_user_cli_tokens_hash ON user_cli_tokens(token_hash);

-- One-time codes minted by the OAuth callback and exchanged by the CLI for a
-- user_cli_tokens row. Stored in a table (not memory) so the handshake
-- survives server replicas and rolling deploys. Rows are single-use
-- (consumed_at set atomically on exchange), expire after 60 seconds, and are
-- garbage-collected opportunistically on insert.
CREATE TABLE cli_auth_codes (
    -- lint:no-org-id reason="60-second user-scoped login handshake state; the nullable org_id column below records the resolved login org (a zero-membership user can still complete login)"
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash      TEXT NOT NULL UNIQUE,
    challenge      TEXT NOT NULL,
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id         UUID REFERENCES organizations(id) ON DELETE CASCADE,
    device_name    TEXT NOT NULL DEFAULT '',
    expires_at     TIMESTAMPTZ NOT NULL,
    consumed_at    TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_cli_auth_codes_expires ON cli_auth_codes(expires_at);
