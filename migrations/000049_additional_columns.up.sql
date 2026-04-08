-- =============================================================================
-- Issue 16: Make projects.repository_id nullable
-- Allows projects that span multiple repositories or are not yet linked.
-- =============================================================================
ALTER TABLE projects ALTER COLUMN repository_id DROP NOT NULL;

-- =============================================================================
-- Issue 19: Add repository_id to pm_plans for repo-scoped filtering
-- PM plans currently have no direct link to a repository, requiring joins
-- through sessions/issues to determine scope.
-- =============================================================================
ALTER TABLE pm_plans ADD COLUMN repository_id uuid REFERENCES repositories(id);
CREATE INDEX idx_pm_plans_repository ON pm_plans (org_id, repository_id) WHERE repository_id IS NOT NULL;
