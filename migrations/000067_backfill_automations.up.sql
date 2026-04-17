-- Phase 1, Stage 2: Backfill existing scheduled projects into the automations table.
-- Only copies projects that have schedule_enabled=true and are not deleted.
--
-- After insertion we disable the legacy project schedule so the scheduler does
-- not fire BOTH a project_cycle and an automation_run for the same workload.
-- The down migration below restores schedule_enabled and removes the rows we
-- inserted (tracked via a marker column on the projects table so the rollback
-- can identify them precisely instead of name/goal matching).

ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS migrated_to_automation_id UUID REFERENCES automations(id);

WITH inserted AS (
    INSERT INTO automations (
        org_id, repository_id, name, goal, scope, agent_type,
        model_override, execution_mode, max_concurrent, base_branch,
        schedule_type, interval_value, interval_unit, next_run_at, enabled,
        created_by, priority, created_at, updated_at
    )
    SELECT
        org_id, repository_id, title, goal, scope, agent_type,
        model_override, execution_mode, max_concurrent, base_branch,
        'interval', schedule_interval, schedule_unit, next_run_at, schedule_enabled,
        created_by, priority, created_at, updated_at
    FROM projects
    WHERE schedule_enabled = true AND deleted_at IS NULL
    RETURNING id, org_id, name, goal
)
UPDATE projects p
SET schedule_enabled = false,
    migrated_to_automation_id = i.id
FROM inserted i
WHERE p.org_id = i.org_id
  AND p.title = i.name
  AND p.goal = i.goal
  AND p.schedule_enabled = true
  AND p.deleted_at IS NULL;
