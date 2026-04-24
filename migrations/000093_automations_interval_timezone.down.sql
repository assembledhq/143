-- Restore the interval=UTC constraint. If interval rows have picked up a
-- non-UTC timezone in the meantime, the ADD CONSTRAINT will fail because
-- Postgres validates existing rows. Normalise them back to UTC first so
-- rollback is idempotent; any meaningful zone info is already baked into
-- the stored next_run_at (UTC), so wiping the zone string does not shift
-- when scheduled runs will fire on the next tick.
UPDATE automations
    SET timezone = 'UTC'
    WHERE schedule_type = 'interval' AND timezone <> 'UTC';

ALTER TABLE automations
    ADD CONSTRAINT chk_automations_timezone_interval CHECK (schedule_type = 'cron' OR timezone = 'UTC');
