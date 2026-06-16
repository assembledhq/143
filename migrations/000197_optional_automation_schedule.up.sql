ALTER TABLE automations
    DROP CONSTRAINT chk_automations_schedule_type;

ALTER TABLE automations
    ADD CONSTRAINT chk_automations_schedule_type CHECK (schedule_type IN ('interval', 'cron', 'none'));
