-- Restore the legacy project schedules and remove the automations created by
-- the backfill. Identification is done via projects.migrated_to_automation_id
-- (set by the up migration) so we never delete user-created automations that
-- happen to share a name or goal with a project.

UPDATE projects
SET schedule_enabled = true
WHERE migrated_to_automation_id IS NOT NULL;

DELETE FROM automations
WHERE id IN (SELECT migrated_to_automation_id FROM projects WHERE migrated_to_automation_id IS NOT NULL);

ALTER TABLE projects DROP COLUMN IF EXISTS migrated_to_automation_id;
