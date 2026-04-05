DROP INDEX IF EXISTS idx_issues_deleted;
ALTER TABLE issues DROP COLUMN IF EXISTS deleted_at;

DROP INDEX IF EXISTS idx_projects_deleted;
ALTER TABLE projects DROP COLUMN IF EXISTS deleted_at;

DROP INDEX IF EXISTS idx_sessions_deleted;
ALTER TABLE sessions DROP COLUMN IF EXISTS deleted_at;
