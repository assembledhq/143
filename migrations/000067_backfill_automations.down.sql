-- Restore the legacy project schedules and remove the automations created by
-- the backfill. Identification is done via projects.migrated_to_automation_id
-- (set by the up migration) so we never delete user-created automations that
-- happen to share a name or goal with a project.

-- Note: this re-enables schedule_enabled on EVERY migrated project, including:
--   1. Soft-deleted rows (schedule_enabled is inert there — no harm).
--   2. Projects whose users explicitly paused the *automation* (but not the
--      project) after the up migration. On rollback those projects will start
--      firing project_cycle jobs again. That's the intended fallback behavior:
--      a rollback is meant to restore the pre-migration scheduling surface,
--      and the automation's paused state is a post-migration artifact that
--      cannot be mapped back to a project attribute. Operators running this
--      rollback in production should expect to manually re-pause legacy
--      schedules for any automation a user had paused.
UPDATE projects
SET schedule_enabled = true
WHERE migrated_to_automation_id IS NOT NULL;

DELETE FROM automations
WHERE id IN (SELECT migrated_to_automation_id FROM projects WHERE migrated_to_automation_id IS NOT NULL);

ALTER TABLE projects DROP COLUMN IF EXISTS migrated_to_automation_id;
