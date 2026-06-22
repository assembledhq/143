ALTER TABLE pr_readiness_checks
    DROP CONSTRAINT IF EXISTS pr_readiness_checks_status_check;

ALTER TABLE pr_readiness_checks
    DROP CONSTRAINT IF EXISTS pr_readiness_checks_check_type_check;

ALTER TABLE pr_readiness_checks
    ADD CONSTRAINT pr_readiness_checks_status_check
    CHECK (status IN ('passed', 'warning', 'failed', 'skipped', 'error'));

ALTER TABLE pr_readiness_checks
    ADD CONSTRAINT pr_readiness_checks_check_type_check
    CHECK (check_type IN (
        'freshness',
        'agent_review_clean',
        'diff_collected',
        'test_evidence_present',
        'risk_flags',
        'dependency_config_risk',
        'generated_file_churn',
        'context_complete',
        'review_packet_draftable',
        'custom_prompt'
    ));

ALTER TABLE pr_readiness_checks
    ADD COLUMN IF NOT EXISTS check_key text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS enforcement_builder text NOT NULL DEFAULT 'off',
    ADD COLUMN IF NOT EXISTS enforcement_engineer text NOT NULL DEFAULT 'off',
    ADD COLUMN IF NOT EXISTS enforcement_admin text NOT NULL DEFAULT 'off',
    ADD COLUMN IF NOT EXISTS provenance text NOT NULL DEFAULT 'builtin',
    ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT '';

UPDATE pr_readiness_checks
SET check_key = check_type
WHERE check_key = '';

UPDATE pr_readiness_checks
SET enforcement_builder = enforcement
WHERE enforcement_builder = 'off' AND enforcement <> 'off';

ALTER TABLE pr_readiness_checks
    DROP CONSTRAINT IF EXISTS pr_readiness_checks_enforcement_builder_check,
    DROP CONSTRAINT IF EXISTS pr_readiness_checks_enforcement_engineer_check,
    DROP CONSTRAINT IF EXISTS pr_readiness_checks_enforcement_admin_check,
    DROP CONSTRAINT IF EXISTS pr_readiness_checks_provenance_check;

ALTER TABLE pr_readiness_checks
    ADD CONSTRAINT pr_readiness_checks_enforcement_builder_check
        CHECK (enforcement_builder IN ('off', 'advisory', 'blocking')),
    ADD CONSTRAINT pr_readiness_checks_enforcement_engineer_check
        CHECK (enforcement_engineer IN ('off', 'advisory', 'blocking')),
    ADD CONSTRAINT pr_readiness_checks_enforcement_admin_check
        CHECK (enforcement_admin IN ('off', 'advisory', 'blocking')),
    ADD CONSTRAINT pr_readiness_checks_provenance_check
        CHECK (provenance IN ('builtin', 'org_settings', 'repo_config'));

CREATE TABLE pr_readiness_policies (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id uuid REFERENCES repositories(id) ON DELETE CASCADE,
    config jsonb NOT NULL,
    active boolean NOT NULL DEFAULT true,
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_pr_readiness_policies_org_active
    ON pr_readiness_policies (org_id)
    WHERE active = true AND repository_id IS NULL;

CREATE UNIQUE INDEX idx_pr_readiness_policies_repo_active
    ON pr_readiness_policies (org_id, repository_id)
    WHERE active = true AND repository_id IS NOT NULL;

CREATE INDEX idx_pr_readiness_policies_history
    ON pr_readiness_policies (org_id, repository_id, created_at DESC);

CREATE TABLE pr_readiness_custom_checks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id uuid REFERENCES repositories(id) ON DELETE CASCADE,
    check_key text NOT NULL,
    name text NOT NULL,
    prompt text NOT NULL,
    path_filters jsonb NOT NULL DEFAULT '{}'::jsonb,
    enforcement jsonb NOT NULL DEFAULT '{}'::jsonb,
    source text NOT NULL CHECK (source IN ('org_settings', 'repo_config')),
    active boolean NOT NULL DEFAULT true,
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_pr_readiness_custom_checks_org_active
    ON pr_readiness_custom_checks (org_id, check_key)
    WHERE active = true AND repository_id IS NULL;

CREATE UNIQUE INDEX idx_pr_readiness_custom_checks_repo_active
    ON pr_readiness_custom_checks (org_id, repository_id, check_key)
    WHERE active = true AND repository_id IS NOT NULL;

CREATE INDEX idx_pr_readiness_custom_checks_scope
    ON pr_readiness_custom_checks (org_id, repository_id, active, created_at DESC);

CREATE TABLE pr_readiness_bypasses (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    readiness_run_id uuid NOT NULL REFERENCES pr_readiness_runs(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    repository_id uuid REFERENCES repositories(id) ON DELETE SET NULL,
    pull_request_id uuid REFERENCES pull_requests(id) ON DELETE SET NULL,
    bypassed_by_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    reason text NOT NULL,
    bypassed_checks jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_pr_readiness_bypasses_run
    ON pr_readiness_bypasses (org_id, readiness_run_id, created_at DESC);

CREATE INDEX idx_pr_readiness_bypasses_session
    ON pr_readiness_bypasses (org_id, session_id, created_at DESC);

CREATE INDEX idx_pr_readiness_bypasses_pull_request
    ON pr_readiness_bypasses (org_id, pull_request_id, created_at DESC)
    WHERE pull_request_id IS NOT NULL;

CREATE TABLE pr_readiness_contexts (
    org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    issue_less_reason text NOT NULL DEFAULT '',
    created_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, session_id)
);
