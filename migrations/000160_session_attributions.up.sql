CREATE TABLE session_attributions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    source text NOT NULL,
    source_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_session_attributions_source CHECK (source IN ('slack')),
    CONSTRAINT uq_session_attributions_session UNIQUE (session_id)
);

CREATE INDEX idx_session_attributions_session
    ON session_attributions (org_id, session_id, created_at DESC);
