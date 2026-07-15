CREATE TABLE preview_verification_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    preview_instance_id uuid REFERENCES preview_instances(id) ON DELETE SET NULL,
    workspace_revision bigint NOT NULL,
    config_digest text NOT NULL DEFAULT '',
    trigger text NOT NULL CHECK (trigger IN ('automatic', 'requested')),
    status text NOT NULL CHECK (status IN ('running', 'passed', 'failed', 'skipped', 'human_intervention_required')),
    attempt integer NOT NULL DEFAULT 1 CHECK (attempt > 0),
    max_attempts integer NOT NULL CHECK (max_attempts > 0),
    plan jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(plan) = 'array'),
    steps jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(steps) = 'array'),
    artifacts jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(artifacts) = 'array'),
    console_error_count integer NOT NULL DEFAULT 0 CHECK (console_error_count >= 0),
    summary text NOT NULL DEFAULT '',
    failure_reason text NOT NULL DEFAULT '',
    skip_reason text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_preview_verification_terminal_time CHECK (
        (status = 'running' AND completed_at IS NULL) OR
        (status <> 'running' AND completed_at IS NOT NULL)
    )
);

CREATE INDEX idx_preview_verification_runs_session
    ON preview_verification_runs (org_id, session_id, created_at DESC);

CREATE UNIQUE INDEX idx_preview_verification_runs_active_revision
    ON preview_verification_runs (org_id, session_id, workspace_revision)
    WHERE status = 'running';
