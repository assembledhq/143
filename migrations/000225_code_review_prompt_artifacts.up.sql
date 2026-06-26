CREATE TABLE code_review_prompt_artifacts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    artifact_key text NOT NULL,
    role text NOT NULL CONSTRAINT chk_code_review_prompt_artifacts_role CHECK (role IN ('reviewer', 'orchestrator', 'description_policy')),
    agent_provider text NOT NULL DEFAULT '',
    content text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_code_review_prompt_artifacts_key
    ON code_review_prompt_artifacts (org_id, artifact_key);

CREATE INDEX idx_code_review_prompt_artifacts_session
    ON code_review_prompt_artifacts (org_id, session_id, created_at DESC);
