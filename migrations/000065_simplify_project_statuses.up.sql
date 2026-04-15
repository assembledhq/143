-- Simplify project statuses from 7 (proposed, draft, planning, active, paused,
-- completed, cancelled) down to 3 (draft, active, completed).
--
-- Mapping:
--   proposed, planning → draft
--   paused             → active  (was temporarily stopped, now just active)
--   cancelled          → completed (terminal state, same as completed)

-- 1. Migrate existing rows to the new statuses.
UPDATE projects SET status = 'draft' WHERE status IN ('proposed', 'planning') AND deleted_at IS NULL;
UPDATE projects SET status = 'active' WHERE status = 'paused' AND deleted_at IS NULL;
UPDATE projects SET status = 'completed' WHERE status = 'cancelled' AND deleted_at IS NULL;

-- 2. Index for PM-proposal queries (proposal cap, proposal summary).
-- NOTE: For production, consider creating this index CONCURRENTLY before running
-- the migration to avoid locking.
CREATE INDEX IF NOT EXISTS idx_projects_org_proposed_by_pm_status
    ON projects (org_id, status) WHERE proposed_by_pm = true AND deleted_at IS NULL;

-- 3. Replace the CHECK constraint with the simplified set.
ALTER TABLE projects DROP CONSTRAINT IF EXISTS chk_projects_status;
ALTER TABLE projects
    ADD CONSTRAINT chk_projects_status CHECK (status IN (
        'draft', 'active', 'completed'
    )) NOT VALID;
ALTER TABLE projects VALIDATE CONSTRAINT chk_projects_status;
