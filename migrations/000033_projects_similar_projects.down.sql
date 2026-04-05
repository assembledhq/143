DROP INDEX IF EXISTS idx_projects_similar_projects;
ALTER TABLE projects DROP COLUMN IF EXISTS similar_projects;
