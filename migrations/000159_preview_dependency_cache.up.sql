CREATE TABLE preview_dependency_cache (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repo_id       uuid        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    cache_key     text        NOT NULL,
    placement_key text        NOT NULL DEFAULT '',
    blob_key      text        NOT NULL DEFAULT '',
    size_bytes    bigint      NOT NULL DEFAULT 0,
    metadata      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    last_used_at  timestamptz NOT NULL DEFAULT now(),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_preview_dependency_cache_lookup
    ON preview_dependency_cache (org_id, repo_id, cache_key);

CREATE INDEX idx_preview_dependency_cache_org_repo_lru
    ON preview_dependency_cache (org_id, repo_id, last_used_at);

CREATE INDEX idx_preview_dependency_cache_placement
    ON preview_dependency_cache (org_id, repo_id, placement_key, last_used_at DESC);

CREATE TABLE preview_dependency_cache_locations (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repo_id        uuid        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    cache_key      text        NOT NULL,
    placement_key  text        NOT NULL DEFAULT '',
    worker_node_id text        NOT NULL,
    size_bytes     bigint      NOT NULL DEFAULT 0,
    last_used_at   timestamptz NOT NULL DEFAULT now(),
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_preview_dependency_cache_locations_unique
    ON preview_dependency_cache_locations (org_id, repo_id, cache_key, worker_node_id);

CREATE INDEX idx_preview_dependency_cache_locations_placement
    ON preview_dependency_cache_locations (org_id, repo_id, placement_key, last_used_at DESC);

CREATE INDEX idx_preview_dependency_cache_locations_worker
    ON preview_dependency_cache_locations (worker_node_id, last_used_at);
