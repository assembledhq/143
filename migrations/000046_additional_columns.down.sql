DROP INDEX IF EXISTS idx_pm_plans_repository;
ALTER TABLE pm_plans DROP COLUMN IF EXISTS repository_id;

-- WARNING: This permanently deletes any projects created with NULL repository_id
-- after the forward migration ran. Consider backing up these rows before rolling
-- back if the forward migration has been live for any significant period.
DELETE FROM projects WHERE repository_id IS NULL;
ALTER TABLE projects ALTER COLUMN repository_id SET NOT NULL;
