ALTER TABLE automations
    DROP CONSTRAINT IF EXISTS chk_automations_fallback_models;

ALTER TABLE automations
    DROP COLUMN IF EXISTS fallback_models;
