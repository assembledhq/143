ALTER TABLE repository_preview_policies
    ADD COLUMN session_prewarm_mode TEXT NOT NULL DEFAULT 'off',
    ADD CONSTRAINT repository_preview_policies_session_prewarm_mode_check
        CHECK (session_prewarm_mode IN ('off', 'cache', 'smart'));

ALTER TABLE preview_instances
    DROP CONSTRAINT IF EXISTS preview_instances_stopped_reason_check,
    ADD CONSTRAINT preview_instances_stopped_reason_check
        CHECK (stopped_reason IN ('', 'user', 'expired', 'warm_policy',
                                  'session_prewarm_policy', 'pr_closed', 'drain', 'error'));

CREATE TABLE session_preview_prewarm_runs (
    id                 UUID             PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             UUID             NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id      UUID             NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    session_id         UUID             NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    workspace_revision BIGINT           NOT NULL DEFAULT -1,
    config_digest      TEXT             NOT NULL DEFAULT '',
    mode               TEXT             NOT NULL,
    decision           TEXT             NOT NULL,
    confidence         DOUBLE PRECISION NOT NULL DEFAULT 0,
    reason             TEXT             NOT NULL DEFAULT '',
    explanation        TEXT             NOT NULL DEFAULT '',
    status             TEXT             NOT NULL,
    job_id             UUID             REFERENCES jobs(id) ON DELETE SET NULL,
    preview_id         UUID             REFERENCES preview_instances(id) ON DELETE SET NULL,
    preview_group_id   UUID             REFERENCES preview_groups(id) ON DELETE SET NULL,
    capacity_snapshot  JSONB            NOT NULL DEFAULT '{}'::jsonb,
    error              TEXT             NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ      NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ      NOT NULL DEFAULT now(),
    started_at         TIMESTAMPTZ,
    completed_at       TIMESTAMPTZ,
    panel_opened_at    TIMESTAMPTZ,
    CONSTRAINT session_preview_prewarm_runs_mode_check
        CHECK (mode IN ('off', 'cache', 'smart')),
    CONSTRAINT session_preview_prewarm_runs_decision_check
        CHECK (decision IN ('none', 'cache', 'warm_candidate')),
    CONSTRAINT session_preview_prewarm_runs_status_check
        CHECK (status IN ('decided', 'queued', 'running', 'skipped_capacity',
                          'skipped_superseded', 'skipped_user_started',
                          'skipped_cooldown', 'classifier_timeout',
                          'succeeded', 'failed'))
);

CREATE UNIQUE INDEX idx_session_preview_prewarm_runs_scope
    ON session_preview_prewarm_runs (org_id, session_id, workspace_revision, config_digest, decision);

CREATE INDEX idx_session_preview_prewarm_runs_active
    ON session_preview_prewarm_runs (org_id, repository_id, status, updated_at DESC)
    WHERE status IN ('queued', 'running');

CREATE INDEX idx_session_preview_prewarm_runs_session
    ON session_preview_prewarm_runs (org_id, session_id, created_at DESC);
