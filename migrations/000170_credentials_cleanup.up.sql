-- Unified coding credentials cleanup (design doc 65, PR 5).
--
-- The unified coding_credentials + coding_credential_runtime_state pair is
-- the sole source of truth for coding-agent credentials. This migration
-- removes the dual-write era's scaffolding:
--
--   1. The runtime-state guard trigger stops syncing the temporary legacy
--      runtime columns (it keeps the orphan guard).
--   2. coding_credentials drops the legacy runtime columns and the
--      team-default mirror marker.
--   3. org_credentials drops its coding-provider rows (mirrored copies of
--      what already lives in coding_credentials). Non-coding integrations
--      (github_app, sentry, linear, notion, slack, mezmo, …) are untouched.
--   4. user_credentials drops its coding-provider rows and is_team_default.
--      The table itself stays: github_app_user OAuth tokens live here and
--      are NOT part of the coding-credential migration.
--
-- Destructive: the deleted legacy rows are not restorable by the down
-- migration. Their contents were copied into coding_credentials by 000111
-- and kept in lockstep by the dual-write mirror since.

-- 1. Guard-only trigger: referential integrity for runtime rows, no legacy
--    column sync. Replaced before the columns it referenced are dropped.
CREATE OR REPLACE FUNCTION coding_credential_runtime_state_guard()
RETURNS trigger AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM coding_credentials cc
        WHERE cc.id = NEW.credential_id
          AND cc.org_id = NEW.org_id
          AND cc.user_id IS NOT DISTINCT FROM NEW.user_id
          AND cc.active = true
    ) THEN
        RAISE EXCEPTION 'coding credential runtime state references missing active credential_id %, org_id %, user_id %',
            NEW.credential_id, NEW.org_id, NEW.user_id
            USING ERRCODE = 'foreign_key_violation';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 2. Drop the legacy runtime columns and the team-default mirror marker.
--    Dependent objects (chk_coding_credentials_team_default_marker,
--    coding_credentials_team_default_origin_idx) drop with their columns.
ALTER TABLE coding_credentials
    DROP COLUMN status,
    DROP COLUMN last_verified_at,
    DROP COLUMN rate_limited_until,
    DROP COLUMN rate_limited_observed_at,
    DROP COLUMN rate_limit_message,
    DROP COLUMN team_default_origin_user_id;

-- 3. Remove mirrored coding rows from org_credentials. The provider list is
--    the legacy codingAuthProviders set (openai_chatgpt was the org-side
--    Codex subscription provider before the openai_subscription rename).
DELETE FROM org_credentials
WHERE provider IN ('anthropic', 'openai', 'openai_chatgpt', 'gemini', 'amp', 'pi');

-- 4. Remove coding rows and the team-default flag from user_credentials.
--    These are the providers 000111 copied into coding_credentials. The
--    github_app_user rows remain — they belong to GitHub user auth, not the
--    coding-credential system.
DELETE FROM user_credentials
WHERE provider IN ('anthropic', 'openai', 'gemini', 'amp', 'pi', 'openrouter');

ALTER TABLE user_credentials
    DROP COLUMN is_team_default;
