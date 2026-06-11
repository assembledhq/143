-- Best-effort rollback of the credentials cleanup. Schema shape is restored;
-- DATA IS NOT: the coding-provider rows deleted from org_credentials and
-- user_credentials are gone. coding_credentials remains the source of truth,
-- so a rolled-back binary that reads the legacy tables will see no coding
-- credentials until they are re-added (or re-mirrored by hand).

ALTER TABLE user_credentials
    ADD COLUMN is_team_default boolean NOT NULL DEFAULT false;

ALTER TABLE coding_credentials
    ADD COLUMN status text NOT NULL DEFAULT 'active',
    ADD COLUMN last_verified_at timestamptz,
    ADD COLUMN rate_limited_until timestamptz,
    ADD COLUMN rate_limited_observed_at timestamptz,
    ADD COLUMN rate_limit_message text,
    ADD COLUMN team_default_origin_user_id uuid,
    ADD CONSTRAINT chk_coding_credentials_team_default_marker
        CHECK (team_default_origin_user_id IS NULL OR user_id IS NULL);

CREATE INDEX coding_credentials_team_default_origin_idx
    ON coding_credentials (org_id, provider, team_default_origin_user_id)
    WHERE team_default_origin_user_id IS NOT NULL;

-- Re-sync the restored legacy columns from the active runtime rows so a
-- rolled-back reader sees coherent values.
UPDATE coding_credentials cc
SET
    status = rt.status,
    last_verified_at = rt.last_verified_at,
    rate_limited_until = rt.rate_limited_until,
    rate_limited_observed_at = rt.rate_limited_observed_at,
    rate_limit_message = rt.rate_limit_message
FROM coding_credential_runtime_state rt
WHERE cc.id = rt.credential_id
  AND cc.org_id = rt.org_id
  AND cc.active = true
  AND rt.active = true;

-- Restore the syncing variant of the runtime guard (the 000167 version).
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

    UPDATE coding_credentials cc
    SET status = NEW.status,
        last_verified_at = NEW.last_verified_at,
        rate_limited_until = NEW.rate_limited_until,
        rate_limited_observed_at = NEW.rate_limited_observed_at,
        rate_limit_message = NEW.rate_limit_message
    WHERE cc.id = NEW.credential_id
      AND cc.org_id = NEW.org_id
      AND cc.active = true;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
