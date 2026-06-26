ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_origin;

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_origin
    CHECK (origin IN (
        'issue_trigger',
        'manual',
        'project',
        'automation',
        'revision',
        'slack',
        'external_api',
        'eval_bootstrap',
        'eval_run',
        'automation_goal_improvement',
        'code_review'
    ));

CREATE TABLE code_review_policies (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id uuid REFERENCES repositories(id) ON DELETE CASCADE,
    active boolean NOT NULL DEFAULT true,
    version int NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    approval_mode text NOT NULL CONSTRAINT chk_code_review_policies_approval_mode CHECK (approval_mode IN ('comment_only', 'approve_acceptable')),
    description_policy jsonb NOT NULL,
    risk_policy jsonb NOT NULL,
    agent_roster jsonb NOT NULL,
    inline_comment_limit int NOT NULL DEFAULT 4 CHECK (inline_comment_limit BETWEEN 1 AND 10),
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_code_review_policies_org_active
    ON code_review_policies (org_id)
    WHERE active = true AND repository_id IS NULL;

CREATE UNIQUE INDEX idx_code_review_policies_repo_active
    ON code_review_policies (org_id, repository_id)
    WHERE active = true AND repository_id IS NOT NULL;

CREATE UNIQUE INDEX idx_code_review_policies_scope_version
    ON code_review_policies (org_id, COALESCE(repository_id, '00000000-0000-0000-0000-000000000000'::uuid), version);

CREATE INDEX idx_code_review_policies_history
    ON code_review_policies (org_id, repository_id, created_at DESC);

CREATE TABLE code_review_session_metadata (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    repository_id uuid NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    pull_request_id uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    policy_id uuid NOT NULL REFERENCES code_review_policies(id) ON DELETE RESTRICT,
    base_sha text NOT NULL,
    head_sha text NOT NULL,
    trigger_source text NOT NULL CONSTRAINT chk_code_review_session_metadata_trigger_source CHECK (trigger_source IN ('app_reviewer', 'alias_reviewer', 'team_reviewer', 'slash_command', 'auto_policy')),
    status text NOT NULL CONSTRAINT chk_code_review_session_metadata_status CHECK (status IN ('queued', 'running', 'completed', 'failed', 'stale', 'cancelled')),
    decision text CONSTRAINT chk_code_review_session_metadata_decision CHECK (decision IS NULL OR decision IN ('approved', 'comment_only', 'needs_human_review', 'blocked')),
    acceptable boolean,
    stale boolean NOT NULL DEFAULT false,
    superseded_by_session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
    review_output_key text NOT NULL,
    prompt_artifact_key text,
    github_review_id bigint,
    github_review_url text,
    final_review_body text,
    failure_reason text,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_code_review_metadata_output_key
    ON code_review_session_metadata (org_id, review_output_key);

CREATE UNIQUE INDEX idx_code_review_metadata_active_head
    ON code_review_session_metadata (org_id, pull_request_id, head_sha, policy_id)
    WHERE status IN ('queued', 'running');

CREATE INDEX idx_code_review_metadata_reviews
    ON code_review_session_metadata (org_id, repository_id, created_at DESC);

CREATE INDEX idx_code_review_metadata_session
    ON code_review_session_metadata (org_id, session_id);

CREATE TABLE code_review_agent_results (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    agent_provider text NOT NULL,
    agent_model text,
    role text NOT NULL CONSTRAINT chk_code_review_agent_results_role CHECK (role IN ('reviewer', 'orchestrator')),
    status text NOT NULL CONSTRAINT chk_code_review_agent_results_status CHECK (status IN ('queued', 'running', 'completed', 'failed', 'timed_out')),
    raw_output text,
    structured_result jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_code_review_agent_results_session
    ON code_review_agent_results (org_id, session_id, created_at DESC);

CREATE TABLE code_review_findings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    agent_result_id uuid REFERENCES code_review_agent_results(id) ON DELETE SET NULL,
    dedupe_key text NOT NULL,
    severity text NOT NULL CONSTRAINT chk_code_review_findings_severity CHECK (severity IN ('info', 'low', 'medium', 'high', 'critical')),
    confidence text NOT NULL CONSTRAINT chk_code_review_findings_confidence CHECK (confidence IN ('low', 'medium', 'high')),
    path text,
    start_line int,
    end_line int,
    summary text NOT NULL,
    body text NOT NULL,
    selected_for_inline boolean NOT NULL DEFAULT false,
    github_comment_id bigint,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_code_review_findings_dedupe
    ON code_review_findings (org_id, session_id, dedupe_key);

CREATE INDEX idx_code_review_findings_session
    ON code_review_findings (org_id, session_id, severity, created_at DESC);
