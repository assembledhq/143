DROP TRIGGER IF EXISTS trg_issues_soft_delete ON issues;
DROP TRIGGER IF EXISTS trg_projects_soft_delete ON projects;
DROP TRIGGER IF EXISTS trg_sessions_soft_delete ON sessions;
DROP FUNCTION IF EXISTS prevent_hard_delete();

DROP INDEX IF EXISTS idx_issues_deleted;
ALTER TABLE issues DROP COLUMN IF EXISTS deleted_at;

DROP INDEX IF EXISTS idx_projects_deleted;
ALTER TABLE projects DROP COLUMN IF EXISTS deleted_at;

DROP INDEX IF EXISTS idx_sessions_deleted;
ALTER TABLE sessions DROP COLUMN IF EXISTS deleted_at;
