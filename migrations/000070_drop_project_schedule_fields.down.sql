-- Restore the per-project schedule columns. Rows left behind by automations
-- that were backfilled from a schedule will still have the automation row,
-- so reverting this migration does NOT re-enable project-level scheduling on
-- its own. Migration 067's down migration handles that side.

ALTER TABLE projects ADD COLUMN IF NOT EXISTS schedule_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS schedule_interval INTEGER NOT NULL DEFAULT 1;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS schedule_unit TEXT NOT NULL DEFAULT 'days';
ALTER TABLE projects ADD COLUMN IF NOT EXISTS next_run_at TIMESTAMPTZ;

ALTER TABLE projects
    ADD CONSTRAINT chk_projects_schedule_unit CHECK (schedule_unit IN (
        'minutes', 'hours', 'days', 'weeks'
    )) NOT VALID;
ALTER TABLE projects VALIDATE CONSTRAINT chk_projects_schedule_unit;

CREATE INDEX IF NOT EXISTS idx_projects_schedule_due
    ON projects (next_run_at)
    WHERE schedule_enabled = true AND status = 'active' AND next_run_at IS NOT NULL;
