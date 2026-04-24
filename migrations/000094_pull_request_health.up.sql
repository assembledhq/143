ALTER TABLE pull_requests
    ADD COLUMN head_sha TEXT,
    ADD COLUMN base_sha TEXT,
    ADD COLUMN merge_state TEXT NOT NULL DEFAULT 'unknown',
    ADD COLUMN has_conflicts BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN failing_test_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN needs_agent_action BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN github_state_synced_at TIMESTAMPTZ,
    ADD COLUMN health_version BIGINT NOT NULL DEFAULT 0;

CREATE TABLE pull_request_health_snapshots (
    pull_request_id UUID NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    org_id UUID NOT NULL REFERENCES organizations(id),
    version BIGINT NOT NULL,
    head_sha TEXT NOT NULL,
    base_sha TEXT NOT NULL,
    summary_json JSONB NOT NULL,
    conflict_payload JSONB,
    failing_tests_payload JSONB,
    payload_size_bytes INTEGER NOT NULL DEFAULT 0,
    enrichment_status TEXT NOT NULL DEFAULT 'not_requested',
    enriched_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (pull_request_id, version)
);

CREATE TABLE pull_request_health_current (
    pull_request_id UUID PRIMARY KEY REFERENCES pull_requests(id) ON DELETE CASCADE,
    org_id UUID NOT NULL REFERENCES organizations(id),
    version BIGINT NOT NULL,
    head_sha TEXT NOT NULL,
    base_sha TEXT NOT NULL,
    summary_json JSONB NOT NULL,
    summary_preview_json JSONB,
    enrichment_status TEXT NOT NULL DEFAULT 'not_requested',
    enriched_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE pull_request_repair_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id),
    pull_request_id UUID NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    action_type TEXT NOT NULL,
    health_version BIGINT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT true,
    obsoleted_by_version BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_pull_requests_health_conflicts
    ON pull_requests (org_id, status, has_conflicts, github_state_synced_at DESC);

CREATE INDEX idx_pull_requests_health_tests
    ON pull_requests (org_id, status, failing_test_count, github_state_synced_at DESC);

CREATE INDEX idx_pull_requests_health_actions
    ON pull_requests (org_id, status, needs_agent_action, github_state_synced_at DESC);

CREATE INDEX idx_pull_requests_health_repo
    ON pull_requests (org_id, github_repo, status, github_state_synced_at DESC);

CREATE INDEX idx_pull_request_health_current_org
    ON pull_request_health_current (org_id, updated_at DESC);

CREATE INDEX idx_pull_request_health_snapshots_org_pr
    ON pull_request_health_snapshots (org_id, pull_request_id, version DESC);

CREATE UNIQUE INDEX idx_pull_request_repair_runs_active
    ON pull_request_repair_runs (pull_request_id, action_type, health_version)
    WHERE active = true;

CREATE INDEX idx_pull_request_repair_runs_org_pr
    ON pull_request_repair_runs (org_id, pull_request_id, created_at DESC);
