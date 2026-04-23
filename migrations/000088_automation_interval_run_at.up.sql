-- Allow interval schedules to target an exact run time (HH:MM UTC) in 5-min increments.
ALTER TABLE automations
    ADD COLUMN interval_run_at TEXT;

ALTER TABLE automations
    ADD CONSTRAINT chk_automations_interval_run_at_format CHECK (
        interval_run_at IS NULL OR (
            interval_run_at ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'
            AND (substring(interval_run_at from 4 for 2)::int % 5) = 0
        )
    );
