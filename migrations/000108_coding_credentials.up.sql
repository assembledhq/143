-- 65-unified-coding-credentials: introduce a single coding_credentials table
-- that absorbs both org-scoped and personal-scoped coding-agent credentials.
-- See docs/design/future/65-unified-coding-credentials.md.
--
-- This migration only creates the table, indexes, and a migration sentinel.
-- Data is copied across by a follow-up migration; an encrypted-blob post-step
-- (cmd/migrate-coding-credentials-anthropic-split) splits Anthropic
-- subscription rows out of AnthropicConfig.

CREATE TABLE coding_credentials (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid        NOT NULL REFERENCES organizations(id),
    -- user_id IS NULL means org-scoped. user_id IS NOT NULL means personal.
    -- ON DELETE CASCADE so removing a user takes their personal rows with them.
    user_id          uuid             REFERENCES users(id) ON DELETE CASCADE,

    provider         text        NOT NULL,
    label            text        NOT NULL DEFAULT '',
    config           bytea       NOT NULL,
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
-- NULLS NOT DISTINCT (PG 15+) is load-bearing here: without it, the default
-- "NULLs are distinct" semantics let two org-scoped rows (user_id IS NULL)
-- with the same (provider, label) coexist, silently breaking the uniqueness
-- the store's ON CONFLICT logic relies on.
CREATE UNIQUE INDEX coding_credentials_scope_provider_label_idx
    ON coding_credentials (org_id, user_id, provider, label) NULLS NOT DISTINCT;

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

-- Sentinel table tracking one-shot data-fixup jobs whose completion gates
-- application startup. The encrypted-blob Anthropic split is the first
-- entry; future migrations that mutate ciphertext can reuse this table.
CREATE TABLE coding_credentials_migrations ( -- lint:no-org-id reason="global migration-sentinel registry; one row per cross-org data-fixup job, intentionally not tenant-scoped"
    name         text        PRIMARY KEY,
    completed_at timestamptz NOT NULL DEFAULT now()
);
