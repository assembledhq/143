ALTER TABLE pr_readiness_bypasses DROP CONSTRAINT IF EXISTS pr_readiness_bypasses_changeset_scope_fkey, DROP COLUMN IF EXISTS changeset_id;
ALTER TABLE pr_readiness_checks DROP CONSTRAINT IF EXISTS pr_readiness_checks_changeset_scope_fkey, DROP COLUMN IF EXISTS changeset_id;
DROP TRIGGER IF EXISTS trg_pr_readiness_assign_primary_changeset ON pr_readiness_runs;
DROP FUNCTION IF EXISTS assign_pr_readiness_primary_changeset();
DROP INDEX IF EXISTS idx_pr_readiness_runs_changeset_latest;
CREATE INDEX IF NOT EXISTS idx_pr_readiness_runs_session_latest ON pr_readiness_runs (org_id, session_id, created_at DESC, id DESC);
ALTER TABLE pr_readiness_runs DROP CONSTRAINT IF EXISTS pr_readiness_runs_changeset_scope_fkey, DROP COLUMN IF EXISTS evaluated_head_sha, DROP COLUMN IF EXISTS changeset_id;
DROP INDEX IF EXISTS session_changesets_one_materializing_per_session;
ALTER TABLE session_changesets
    DROP CONSTRAINT IF EXISTS session_changesets_materialized_worktree,
    DROP COLUMN IF EXISTS materialized_diff,
    DROP COLUMN IF EXISTS materialization_error,
    DROP COLUMN IF EXISTS worktree_path;
DROP TABLE IF EXISTS session_changeset_split_omissions;
DROP TABLE IF EXISTS session_changeset_split_paths;
DROP TABLE IF EXISTS session_changeset_split_plans;
ALTER TABLE session_diff_snapshots DROP CONSTRAINT IF EXISTS session_diff_snapshots_tenant_identity;
