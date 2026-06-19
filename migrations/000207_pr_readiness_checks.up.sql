CREATE TABLE pr_readiness_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    repository_id uuid REFERENCES repositories(id) ON DELETE SET NULL,
    status text NOT NULL CHECK (status IN ('queued', 'running', 'passed', 'warnings', 'blocked', 'failed')),
    evaluated_workspace_revision bigint NOT NULL DEFAULT 0,
    evaluated_snapshot_key text,
    summary text NOT NULL DEFAULT '',
    review_packet jsonb,
    triggered_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_pr_readiness_runs_session_latest
    ON pr_readiness_runs (org_id, session_id, created_at DESC, id DESC);

CREATE INDEX idx_pr_readiness_runs_status
    ON pr_readiness_runs (org_id, status, created_at DESC);

CREATE TABLE pr_readiness_checks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    run_id uuid NOT NULL REFERENCES pr_readiness_runs(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    check_type text NOT NULL CHECK (check_type IN (
        'freshness',
        'agent_review_clean',
        'diff_collected',
        'test_evidence_present',
        'risk_flags',
        'dependency_config_risk',
        'generated_file_churn',
        'context_complete',
        'review_packet_draftable'
    )),
    status text NOT NULL CHECK (status IN ('passed', 'warning', 'failed', 'skipped')),
    enforcement text NOT NULL CHECK (enforcement IN ('off', 'advisory', 'blocking')),
    title text NOT NULL,
    summary text NOT NULL,
    details jsonb,
    action text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_pr_readiness_checks_run
    ON pr_readiness_checks (org_id, run_id, check_type);

CREATE INDEX idx_pr_readiness_checks_session
    ON pr_readiness_checks (org_id, session_id, created_at DESC);
