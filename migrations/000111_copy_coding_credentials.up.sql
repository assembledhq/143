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
--
-- 10s (not 5s) so the rare contended deploy still completes — most legacy
-- writes are sub-second, but a co-running janitor sweep can hold the lock
-- for several seconds. statement_timeout is 120s; the runbook calls out
-- that the migration must complete in well under that on typical clusters.
SET LOCAL lock_timeout = '10s';
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
  AND provider IN ('openai', 'anthropic', 'gemini', 'amp', 'pi', 'openrouter')
ON CONFLICT (id) DO NOTHING;

-- 3. Team-default rows become org-scoped rows. We mint a fresh id (the
--    legacy user_credentials.id could collide with an org_credentials id
--    from step 1) and stamp `team_default_origin_user_id` so the mirror
--    cleanup and the down migration can identify these rows without
--    string-matching the human-readable label.
--
--    The label still encodes the originating user_id because the unique
--    (org_id, user_id, provider, label) index dedups on natural key when
--    the dual-write mirror cannot match by id, and two team-default rows
--    from different originating users in the same org need distinct labels
--    to coexist under that index. Code keys all logic on the marker column.
--
--    They land at the bottom of the org stack so explicit org rows still win.
--    Priority is computed via a per-row subquery against the just-inserted
--    coding_credentials rows from step 1. Postgres evaluates the subquery
--    against the statement-start snapshot, so every team-default row in the
--    same org sees the same MAX and lands on the same priority slot —
--    including team-default rows for *different* providers in the same org,
--    which all collide on one priority value. The resolver only ever walks
--    rows of the requested provider and tie-breaks within a tier on
--    `created_at`, so the cross-provider collision is invisible at read time.
--    Acceptable for a one-shot migration; not worth the complexity of a
--    per-provider window allocator.
INSERT INTO coding_credentials
    (org_id, user_id, provider, label, config, priority, status, created_by,
     last_verified_at, created_at, updated_at, team_default_origin_user_id)
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
    uc.updated_at,
    uc.user_id
FROM user_credentials uc
WHERE uc.is_team_default = true
  AND uc.provider IN ('openai', 'anthropic', 'gemini', 'amp', 'pi', 'openrouter')
-- Idempotency: skip rows already migrated by a prior run. Keyed on the
-- marker column so a manually-renamed label (or any future label-format
-- change) does not cause duplicate inserts.
AND NOT EXISTS (
    SELECT 1 FROM coding_credentials cc
    WHERE cc.org_id = uc.org_id
      AND cc.user_id IS NULL
      AND cc.provider = uc.provider
      AND cc.team_default_origin_user_id = uc.user_id
);
