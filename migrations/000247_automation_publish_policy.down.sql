ALTER TABLE automations
    DROP CONSTRAINT IF EXISTS chk_automations_publish_policy;

ALTER TABLE automations
    DROP COLUMN IF EXISTS publish_policy;
