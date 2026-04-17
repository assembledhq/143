-- Phase 1, Stage 2: Backfill existing scheduled projects into the automations table.
-- Only copies projects that have schedule_enabled=true and are not deleted.
--
-- After insertion we disable the legacy project schedule so the scheduler does
-- not fire BOTH a project_cycle and an automation_run for the same workload.
--
-- Correlation uses a transient source_project_id column on automations (instead
-- of matching on (org_id, title, goal)) so a migration still works correctly
-- when two scheduled projects share the same name and goal.

ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS migrated_to_automation_id UUID REFERENCES automations(id);

ALTER TABLE automations
    ADD COLUMN IF NOT EXISTS source_project_id UUID REFERENCES projects(id);

INSERT INTO automations (
    org_id, repository_id, name, goal, scope, agent_type,
    model_override, execution_mode, max_concurrent, base_branch,
    schedule_type, interval_value, interval_unit, next_run_at, enabled,
    created_by, priority, created_at, updated_at, source_project_id
)
SELECT
    org_id, repository_id, title, goal, scope, agent_type,
    model_override, execution_mode, max_concurrent, base_branch,
    'interval', schedule_interval, schedule_unit, next_run_at, schedule_enabled,
    created_by, priority, created_at, updated_at, id
FROM projects
WHERE schedule_enabled = true AND deleted_at IS NULL;

UPDATE projects p
SET schedule_enabled = false,
    migrated_to_automation_id = a.id
FROM automations a
WHERE a.source_project_id = p.id;

-- Drop the transient correlation column so it doesn't bleed into the normal
-- schema. The down migration doesn't need it because rollback identifies rows
-- via projects.migrated_to_automation_id.
ALTER TABLE automations DROP COLUMN source_project_id;
