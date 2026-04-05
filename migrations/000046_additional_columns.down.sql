DROP INDEX IF EXISTS idx_pm_plans_repository;
ALTER TABLE pm_plans DROP COLUMN IF EXISTS repository_id;

-- Delete projects with NULL repository_id since they can't satisfy the NOT NULL
-- constraint. These are repo-unlinked projects created after the forward migration.
DELETE FROM projects WHERE repository_id IS NULL;
ALTER TABLE projects ALTER COLUMN repository_id SET NOT NULL;
