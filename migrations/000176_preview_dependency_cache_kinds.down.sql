DROP INDEX IF EXISTS idx_preview_dependency_cache_locations_placement;
DROP INDEX IF EXISTS idx_preview_dependency_cache_locations_unique;
DROP INDEX IF EXISTS idx_preview_dependency_cache_placement;
DROP INDEX IF EXISTS idx_preview_dependency_cache_lookup;

CREATE UNIQUE INDEX idx_preview_dependency_cache_lookup
    ON preview_dependency_cache (org_id, repo_id, cache_key);

CREATE INDEX idx_preview_dependency_cache_placement
    ON preview_dependency_cache (org_id, repo_id, placement_key, last_used_at DESC);

CREATE UNIQUE INDEX idx_preview_dependency_cache_locations_unique
    ON preview_dependency_cache_locations (org_id, repo_id, cache_key, worker_node_id);

CREATE INDEX idx_preview_dependency_cache_locations_placement
    ON preview_dependency_cache_locations (org_id, repo_id, placement_key, last_used_at DESC);

ALTER TABLE preview_dependency_cache_locations
    DROP COLUMN IF EXISTS cache_kind;

ALTER TABLE preview_dependency_cache
    DROP COLUMN IF EXISTS cache_kind;
