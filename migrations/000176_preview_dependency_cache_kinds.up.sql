ALTER TABLE preview_dependency_cache
    ADD COLUMN cache_kind text NOT NULL DEFAULT 'install_artifact';

ALTER TABLE preview_dependency_cache_locations
    ADD COLUMN cache_kind text NOT NULL DEFAULT 'install_artifact';

DROP INDEX IF EXISTS idx_preview_dependency_cache_lookup;
DROP INDEX IF EXISTS idx_preview_dependency_cache_placement;
DROP INDEX IF EXISTS idx_preview_dependency_cache_locations_unique;
DROP INDEX IF EXISTS idx_preview_dependency_cache_locations_placement;

CREATE UNIQUE INDEX idx_preview_dependency_cache_lookup
    ON preview_dependency_cache (org_id, repo_id, cache_kind, cache_key);

CREATE INDEX idx_preview_dependency_cache_placement
    ON preview_dependency_cache (org_id, repo_id, cache_kind, placement_key, last_used_at DESC);

CREATE UNIQUE INDEX idx_preview_dependency_cache_locations_unique
    ON preview_dependency_cache_locations (org_id, repo_id, cache_kind, cache_key, worker_node_id);

CREATE INDEX idx_preview_dependency_cache_locations_placement
    ON preview_dependency_cache_locations (org_id, repo_id, cache_kind, placement_key, last_used_at DESC);
