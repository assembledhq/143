DROP INDEX IF EXISTS idx_automation_trigger_dedupes_expires_at;
DROP TABLE IF EXISTS automation_trigger_dedupes;

ALTER TABLE automations
    DROP COLUMN IF EXISTS github_event_filters;
