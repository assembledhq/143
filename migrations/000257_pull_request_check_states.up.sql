CREATE SEQUENCE pull_request_check_state_projection_version_seq;

CREATE TABLE pull_request_check_states (
    org_id UUID NOT NULL REFERENCES organizations(id),
    pull_request_id UUID NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    head_sha TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('check_run', 'commit_status')),
    external_key TEXT NOT NULL,
    name TEXT NOT NULL,
    category TEXT NOT NULL CHECK (category IN ('test', 'lint', 'build', 'deploy', 'unknown')),
    status TEXT NOT NULL CHECK (status IN ('passed', 'failed', 'pending')),
    provider TEXT NOT NULL DEFAULT '',
    details_url TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    provider_event_id TEXT NOT NULL DEFAULT '',
    provider_sequence BIGINT NOT NULL DEFAULT 0,
    provider_updated_at TIMESTAMPTZ NOT NULL,
    projection_version BIGINT NOT NULL DEFAULT nextval('pull_request_check_state_projection_version_seq'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (pull_request_id, head_sha, source, external_key)
);

ALTER SEQUENCE pull_request_check_state_projection_version_seq
    OWNED BY pull_request_check_states.projection_version;

ALTER TABLE pull_request_health_current
    ADD COLUMN check_state_version BIGINT NOT NULL DEFAULT 0;

CREATE INDEX idx_pull_request_check_states_org_pr_head
    ON pull_request_check_states (org_id, pull_request_id, head_sha, projection_version);
