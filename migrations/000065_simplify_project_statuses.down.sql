-- Drop the PM-proposal partial index.
DROP INDEX IF EXISTS idx_projects_org_proposed_by_pm_status;

-- Revert the CHECK constraint to allow all 7 original statuses.
-- Note: data migration is not reversible — rows that were mapped to the
-- simplified statuses will remain in their new status.
ALTER TABLE projects DROP CONSTRAINT IF EXISTS chk_projects_status;
ALTER TABLE projects
    ADD CONSTRAINT chk_projects_status CHECK (status IN (
        'proposed', 'draft', 'planning', 'active', 'paused', 'completed', 'cancelled'
    )) NOT VALID;
ALTER TABLE projects VALIDATE CONSTRAINT chk_projects_status;
