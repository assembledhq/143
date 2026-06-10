DROP INDEX IF EXISTS coding_credential_runtime_state_rate_limited_until_idx;
DROP INDEX IF EXISTS coding_credential_runtime_state_active_idx;
DROP INDEX IF EXISTS coding_credentials_pending_auth_ttl_idx;
DROP INDEX IF EXISTS coding_credentials_org_idx;
DROP INDEX IF EXISTS coding_credentials_user_idx;
DROP INDEX IF EXISTS coding_credentials_resolver_idx;
DROP INDEX IF EXISTS coding_credentials_scope_provider_label_idx;
DROP INDEX IF EXISTS coding_credentials_active_logical_id_idx;

DROP TRIGGER IF EXISTS coding_credential_runtime_state_guard_trigger ON coding_credential_runtime_state;
DROP FUNCTION IF EXISTS coding_credential_runtime_state_guard();

UPDATE coding_credentials cc
SET
    status = rt.status,
    last_verified_at = rt.last_verified_at,
    rate_limited_until = rt.rate_limited_until,
    rate_limited_observed_at = rt.rate_limited_observed_at,
    rate_limit_message = rt.rate_limit_message,
    -- A config change after the last runtime change must not move
    -- updated_at backwards.
    updated_at = GREATEST(cc.updated_at, rt.created_at)
FROM coding_credential_runtime_state rt
WHERE cc.id = rt.credential_id
  AND cc.org_id = rt.org_id
  AND cc.active = true
  AND rt.active = true;

DELETE FROM coding_credentials
WHERE active = false;

ALTER TABLE coding_credentials
    DROP CONSTRAINT coding_credentials_pkey,
    ADD CONSTRAINT coding_credentials_pkey PRIMARY KEY (id);

CREATE UNIQUE INDEX coding_credentials_scope_provider_label_idx
    ON coding_credentials (org_id, user_id, provider, label) NULLS NOT DISTINCT;

CREATE INDEX coding_credentials_resolver_idx
    ON coding_credentials (org_id, provider, user_id, priority, created_at)
    WHERE status = 'active';

CREATE INDEX coding_credentials_user_idx
    ON coding_credentials (org_id, user_id, priority)
    WHERE user_id IS NOT NULL AND status != 'disabled';

CREATE INDEX coding_credentials_org_idx
    ON coding_credentials (org_id, priority)
    WHERE user_id IS NULL AND status != 'disabled';

CREATE INDEX coding_credentials_pending_auth_ttl_idx
    ON coding_credentials (created_at)
    WHERE status = 'pending_auth';

CREATE INDEX idx_coding_credentials_rate_limited_until
    ON coding_credentials (rate_limited_until)
    WHERE rate_limited_until IS NOT NULL;

DROP TABLE IF EXISTS coding_credential_runtime_state;

ALTER TABLE coding_credentials
    DROP COLUMN IF EXISTS active,
    DROP COLUMN IF EXISTS version_id;
