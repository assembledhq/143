UPDATE automations
    SET schedule_type = 'interval',
        interval_value = COALESCE(interval_value, 1),
        interval_unit = COALESCE(interval_unit, 'days'),
        next_run_at = NULL
    WHERE schedule_type = 'none';

ALTER TABLE automations
    DROP CONSTRAINT chk_automations_schedule_type;

ALTER TABLE automations
    ADD CONSTRAINT chk_automations_schedule_type CHECK (schedule_type IN ('interval', 'cron'));
