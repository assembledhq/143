DROP INDEX IF EXISTS idx_projects_schedule_due;
ALTER TABLE projects DROP COLUMN IF EXISTS next_run_at;
ALTER TABLE projects DROP COLUMN IF EXISTS schedule_unit;
ALTER TABLE projects DROP COLUMN IF EXISTS schedule_interval;
ALTER TABLE projects DROP COLUMN IF EXISTS schedule_enabled;
