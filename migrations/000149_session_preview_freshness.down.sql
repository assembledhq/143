DROP INDEX IF EXISTS idx_preview_instances_session_workspace_revision;

ALTER TABLE preview_instances
    DROP COLUMN IF EXISTS source_workspace_revision,
    DROP COLUMN IF EXISTS source_workspace_revision_updated_at;

ALTER TABLE sessions
    DROP COLUMN IF EXISTS workspace_revision_updated_at,
    DROP COLUMN IF EXISTS workspace_revision;
