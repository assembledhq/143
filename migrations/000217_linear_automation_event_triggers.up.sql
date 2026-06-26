ALTER TABLE automation_event_triggers
    DROP CONSTRAINT IF EXISTS chk_automation_event_triggers_provider;

ALTER TABLE automation_event_triggers
    ADD CONSTRAINT chk_automation_event_triggers_provider CHECK (provider IN ('pagerduty', 'github', 'linear'));

ALTER TABLE automation_runs
    DROP CONSTRAINT IF EXISTS chk_automation_runs_provider;

ALTER TABLE automation_runs
    ADD CONSTRAINT chk_automation_runs_provider CHECK (provider IS NULL OR provider IN ('pagerduty', 'github', 'linear'));
