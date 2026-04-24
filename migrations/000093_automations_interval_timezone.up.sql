-- Allow interval schedules to evaluate interval_run_at in a user-specified
-- timezone. Previously chk_automations_timezone_interval forced interval rows
-- to timezone='UTC' because NextRunTime used duration arithmetic only; now
-- that NextRunTimeAt evaluates the HH:MM wall-clock target in the stored
-- timezone, non-UTC zones are meaningful for interval rows too.
ALTER TABLE automations
    DROP CONSTRAINT IF EXISTS chk_automations_timezone_interval;
