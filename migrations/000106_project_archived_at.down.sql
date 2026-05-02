DROP INDEX IF EXISTS idx_projects_archived_priority;
DROP INDEX IF EXISTS idx_projects_not_archived_priority;

ALTER TABLE projects DROP COLUMN IF EXISTS archived_at;
