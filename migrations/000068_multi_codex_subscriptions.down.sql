-- Revert: remove label and last_used_at columns, restore original unique constraint.
-- First delete credentials whose status would violate the original CHECK constraint
-- (only 'active' / 'disabled' were allowed pre-migration).
DELETE FROM org_credentials WHERE status IN ('pending_auth', 'invalid');

-- Then delete duplicate credentials per org+provider (keep the oldest).
DELETE FROM org_credentials
WHERE id NOT IN (
  SELECT DISTINCT ON (org_id, provider) id
  FROM org_credentials
  ORDER BY org_id, provider, created_at
);

DROP INDEX IF EXISTS idx_org_credentials_round_robin;

ALTER TABLE org_credentials DROP CONSTRAINT chk_org_credentials_status;
ALTER TABLE org_credentials
    ADD CONSTRAINT chk_org_credentials_status CHECK (status IN (
        'active', 'disabled'
    )) NOT VALID;
ALTER TABLE org_credentials VALIDATE CONSTRAINT chk_org_credentials_status;

ALTER TABLE org_credentials DROP CONSTRAINT org_credentials_org_id_provider_label_key;
ALTER TABLE org_credentials ADD CONSTRAINT org_credentials_org_id_provider_key UNIQUE (org_id, provider);

ALTER TABLE org_credentials DROP COLUMN created_by;
ALTER TABLE org_credentials DROP COLUMN last_used_at;
ALTER TABLE org_credentials DROP COLUMN label;
