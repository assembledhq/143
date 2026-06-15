DROP INDEX IF EXISTS idx_preview_instances_session_runtime_workspace_revision;

ALTER TABLE preview_instances
    DROP CONSTRAINT IF EXISTS chk_preview_runtime_workspace_revision_source;

ALTER TABLE preview_instances
    DROP COLUMN IF EXISTS runtime_workspace_revision_source,
    DROP COLUMN IF EXISTS runtime_workspace_revision_updated_at,
    DROP COLUMN IF EXISTS runtime_workspace_revision;
