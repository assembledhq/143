-- Stage 4 of design doc 48: drop the per-project schedule fields now that
-- automations own recurring scheduling. Migration 067 already backfilled every
-- scheduled project into automations and flipped schedule_enabled=false on
-- projects, so no live rows depend on these columns.
--
-- The index must go first; Postgres won't let you drop a column while a
-- dependent partial index still references it.

DROP INDEX IF EXISTS idx_projects_schedule_due;

ALTER TABLE projects DROP CONSTRAINT IF EXISTS chk_projects_schedule_unit;

ALTER TABLE projects
    DROP COLUMN IF EXISTS schedule_enabled,
    DROP COLUMN IF EXISTS schedule_interval,
    DROP COLUMN IF EXISTS schedule_unit,
    DROP COLUMN IF EXISTS next_run_at;
