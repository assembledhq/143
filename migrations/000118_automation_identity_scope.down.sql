ALTER TABLE automations
    DROP CONSTRAINT IF EXISTS chk_automations_identity_scope;

ALTER TABLE automations
    DROP COLUMN IF EXISTS identity_scope;
