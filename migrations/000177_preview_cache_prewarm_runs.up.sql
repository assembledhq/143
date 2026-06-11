CREATE TABLE preview_cache_prewarm_runs (
    id                        uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                    uuid        NOT NULL REFERENCES organizations(id),
    repo_id                   uuid        NOT NULL REFERENCES repositories(id),
    source                    text        NOT NULL,
    source_id                 text        NOT NULL,
    cache_scope_key           text        NOT NULL,
    job_id                    uuid        REFERENCES jobs(id) ON DELETE SET NULL,
    worker_node_id            text        NOT NULL DEFAULT '',
    status                    text        NOT NULL,
    package_manager_cache_key text        NOT NULL DEFAULT '',
    dependency_cache_key      text        NOT NULL DEFAULT '',
    config_digest             text        NOT NULL DEFAULT '',
    commit_sha                text        NOT NULL DEFAULT '',
    workspace_revision        bigint      NOT NULL DEFAULT 0,
    error                     text        NOT NULL DEFAULT '',
    started_at                timestamptz,
    completed_at              timestamptz,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_preview_cache_prewarm_runs_scope
    ON preview_cache_prewarm_runs (org_id, repo_id, cache_scope_key);

CREATE INDEX idx_preview_cache_prewarm_runs_status
    ON preview_cache_prewarm_runs (org_id, status, updated_at DESC);

CREATE INDEX idx_preview_cache_prewarm_runs_source
    ON preview_cache_prewarm_runs (org_id, source, source_id, updated_at DESC);
