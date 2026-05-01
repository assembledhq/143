-- 65-unified-coding-credentials data copy.
--
-- Moves rows from org_credentials and user_credentials into the unified
-- coding_credentials table. This migration only handles provider-name
-- normalisation that pure SQL can do; the encrypted-blob rewrite that
-- splits AnthropicConfig.Subscription into AnthropicSubscriptionConfig
-- runs as a Go-side post-step (`make migrate-coding-credentials-anthropic-split`),
-- which writes its sentinel to coding_credentials_migrations on completion.
--
-- We do NOT delete from org_credentials or user_credentials here. The
-- legacy code paths still read those tables; the cleanup PR (PR 5 in the
-- design doc) is what drops them. Copying without deleting means the
-- application can run side-by-side until cleanup, and the migration is
-- idempotent on retry.

-- Bound how long this migration will wait on a row-level lock. Without
-- this, the per-row priority subquery in step 3 holds a snapshot for the
-- duration of the statement and can stall indefinitely behind concurrent
-- writes to coding_credentials, org_credentials, or user_credentials. With
-- a bound, a contended deploy fails fast and surfaces a retryable error
-- instead of locking up the migrations runner. SET LOCAL is scoped to the
-- migrations transaction so it does not leak into application sessions.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '120s';

-- 1. Org-scoped coding rows (priority/label/status/created_by/last_verified_at
--    all carry over). Rename openai_chatgpt → openai_subscription on the way
--    in so the unified table uses the canonical naming convention from day one.
INSERT INTO coding_credentials
    (id, org_id, user_id, provider, label, config, priority, status, created_by,
     last_verified_at, created_at, updated_at)
SELECT
    id, org_id, NULL,
    CASE provider WHEN 'openai_chatgpt' THEN 'openai_subscription' ELSE provider END,
    label, config, priority, status, created_by,
    last_verified_at, created_at, updated_at
FROM org_credentials
WHERE provider IN ('openai', 'openai_chatgpt', 'anthropic', 'gemini', 'amp', 'pi', 'openrouter')
ON CONFLICT (id) DO NOTHING;

-- 2. Personal rows (excluding team-default which becomes org-scoped below).
--    user_credentials had no label or priority columns; we initialise label=''
--    and priority=1 so the user's personal stack starts ordered.
INSERT INTO coding_credentials
    (id, org_id, user_id, provider, label, config, priority, status, created_by,
     last_verified_at, created_at, updated_at)
SELECT
    id, org_id, user_id, provider, '' AS label, config, 1 AS priority, status, user_id,
    last_verified_at, created_at, updated_at
FROM user_credentials
WHERE is_team_default = false
ON CONFLICT (id) DO NOTHING;

-- 3. Team-default rows become org-scoped rows. Use a deterministic label
--    keyed by the original owner's user_id so admins can audit the migration
--    and so the dual-write mirror in coding_credentials_mirror.go produces
--    the same label byte-for-byte (the unique (org_id, user_id, provider,
--    label) index dedups on natural key when the mirror cannot match by id).
--    They land at the bottom of the org stack so explicit org rows still win.
--
--    We mint a fresh id rather than reusing the user_credentials.id because
--    the same user_credentials row could in theory collide with an
--    org_credentials id from step 1 (different UUIDs across tables, but the
--    PK constraint would catch it; defaulting the id is simpler and the
--    user-facing reference is by label, not id).
--
--    Priority is computed via a per-row subquery against the
--    just-inserted coding_credentials rows from step 1. Postgres evaluates
--    the subquery against the snapshot taken when the statement starts, so
--    every team-default row in the same org sees the same MAX and lands on
--    the same priority slot. Tie-break inside the resolver is `created_at`,
--    which gives deterministic ordering — acceptable for a one-shot
--    migration.
INSERT INTO coding_credentials
    (org_id, user_id, provider, label, config, priority, status, created_by,
     last_verified_at, created_at, updated_at)
SELECT
    uc.org_id,
    NULL,
    uc.provider,
    'Team default (migrated from ' || uc.user_id::text || ')' AS label,
    uc.config,
    (
        SELECT COALESCE(MAX(priority), 0) + 1
        FROM coding_credentials cc
        WHERE cc.org_id = uc.org_id AND cc.user_id IS NULL
    ),
    uc.status,
    uc.user_id,
    uc.last_verified_at,
    uc.created_at,
    uc.updated_at
FROM user_credentials uc
WHERE uc.is_team_default = true
-- Idempotency: skip rows already migrated by a prior run. Detected via the
-- deterministic uuid-keyed label suffix.
AND NOT EXISTS (
    SELECT 1 FROM coding_credentials cc
    WHERE cc.org_id = uc.org_id
      AND cc.user_id IS NULL
      AND cc.provider = uc.provider
      AND cc.label = 'Team default (migrated from ' || uc.user_id::text || ')'
);
