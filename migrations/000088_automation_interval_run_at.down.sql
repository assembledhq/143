ALTER TABLE automations
    DROP CONSTRAINT IF EXISTS chk_automations_interval_run_at_format;

ALTER TABLE automations
    DROP COLUMN IF EXISTS interval_run_at;
