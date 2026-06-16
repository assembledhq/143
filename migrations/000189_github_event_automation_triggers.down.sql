DROP INDEX IF EXISTS idx_automations_github_event_triggers;

ALTER TABLE automation_runs
    DROP CONSTRAINT IF EXISTS chk_automation_runs_triggered_by;

ALTER TABLE automation_runs
    ADD CONSTRAINT chk_automation_runs_triggered_by CHECK (triggered_by IN ('schedule', 'manual'));

ALTER TABLE automations
    DROP CONSTRAINT IF EXISTS chk_automations_github_event_triggers,
    DROP COLUMN IF EXISTS github_event_triggers;
