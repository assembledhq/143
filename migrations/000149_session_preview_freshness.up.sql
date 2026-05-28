ALTER TABLE sessions
    ADD COLUMN workspace_revision bigint NOT NULL DEFAULT 0,
    ADD COLUMN workspace_revision_updated_at timestamptz NOT NULL DEFAULT now();

ALTER TABLE preview_instances
    ADD COLUMN source_workspace_revision bigint,
    ADD COLUMN source_workspace_revision_updated_at timestamptz;

CREATE INDEX idx_preview_instances_session_workspace_revision
    ON preview_instances (org_id, session_id, source_workspace_revision)
    WHERE session_id IS NOT NULL;
