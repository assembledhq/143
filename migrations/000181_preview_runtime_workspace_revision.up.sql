ALTER TABLE preview_instances
    ADD COLUMN runtime_workspace_revision bigint,
    ADD COLUMN runtime_workspace_revision_updated_at timestamptz,
    ADD COLUMN runtime_workspace_revision_source text NOT NULL DEFAULT '';

ALTER TABLE preview_instances
    ADD CONSTRAINT chk_preview_runtime_workspace_revision_source
    CHECK (runtime_workspace_revision_source IN ('', 'launch', 'recycle', 'hmr', 'file_event'));

CREATE INDEX idx_preview_instances_session_runtime_workspace_revision
    ON preview_instances (org_id, session_id, runtime_workspace_revision)
    WHERE session_id IS NOT NULL;
