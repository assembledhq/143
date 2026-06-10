ALTER TABLE coding_credentials
    ADD COLUMN version_id uuid,
    ADD COLUMN active boolean NOT NULL DEFAULT true;

UPDATE coding_credentials
SET version_id = gen_random_uuid()
WHERE version_id IS NULL;

ALTER TABLE coding_credentials
    ALTER COLUMN version_id SET DEFAULT gen_random_uuid(),
    ALTER COLUMN version_id SET NOT NULL;

CREATE TABLE coding_credential_runtime_state (
    version_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    credential_id uuid NOT NULL,
    org_id uuid NOT NULL REFERENCES organizations(id),
    user_id uuid REFERENCES users(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'active',
    last_verified_at timestamptz,
    rate_limited_until timestamptz,
    rate_limited_observed_at timestamptz,
    rate_limit_message text,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_coding_credential_runtime_state_status
        CHECK (status IN ('active', 'disabled', 'pending_auth', 'invalid'))
);

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

CREATE TRIGGER coding_credential_runtime_state_guard_trigger
    BEFORE INSERT ON coding_credential_runtime_state
    FOR EACH ROW
    EXECUTE FUNCTION coding_credential_runtime_state_guard();

INSERT INTO coding_credential_runtime_state (
    credential_id,
    org_id,
    user_id,
    status,
    last_verified_at,
    rate_limited_until,
    rate_limited_observed_at,
    rate_limit_message,
    active,
    created_at
)
SELECT
    id,
    org_id,
    user_id,
    status,
    last_verified_at,
    rate_limited_until,
    rate_limited_observed_at,
    rate_limit_message,
    true,
    updated_at
FROM coding_credentials;

DROP INDEX IF EXISTS coding_credentials_scope_provider_label_idx;
DROP INDEX IF EXISTS coding_credentials_resolver_idx;
DROP INDEX IF EXISTS coding_credentials_user_idx;
DROP INDEX IF EXISTS coding_credentials_org_idx;
DROP INDEX IF EXISTS coding_credentials_pending_auth_ttl_idx;
DROP INDEX IF EXISTS idx_coding_credentials_rate_limited_until;

ALTER TABLE coding_credentials
    DROP CONSTRAINT coding_credentials_pkey,
    ADD CONSTRAINT coding_credentials_pkey PRIMARY KEY (version_id);

CREATE UNIQUE INDEX coding_credentials_active_logical_id_idx
    ON coding_credentials (id)
    WHERE active = true;

CREATE UNIQUE INDEX coding_credentials_scope_provider_label_idx
    ON coding_credentials (org_id, user_id, provider, label) NULLS NOT DISTINCT
    WHERE active = true;

CREATE INDEX coding_credentials_resolver_idx
    ON coding_credentials (org_id, provider, user_id, priority, created_at)
    WHERE active = true;

CREATE INDEX coding_credentials_user_idx
    ON coding_credentials (org_id, user_id, priority)
    WHERE user_id IS NOT NULL AND active = true;

CREATE INDEX coding_credentials_org_idx
    ON coding_credentials (org_id, priority)
    WHERE user_id IS NULL AND active = true;

CREATE INDEX coding_credentials_pending_auth_ttl_idx
    ON coding_credential_runtime_state (created_at)
    WHERE active = true AND status = 'pending_auth';

CREATE UNIQUE INDEX coding_credential_runtime_state_active_idx
    ON coding_credential_runtime_state (credential_id)
    WHERE active = true;

CREATE INDEX coding_credential_runtime_state_rate_limited_until_idx
    ON coding_credential_runtime_state (rate_limited_until)
    WHERE active = true AND rate_limited_until IS NOT NULL;
