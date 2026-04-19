DROP INDEX IF EXISTS idx_preview_startup_cache_lookup;

CREATE UNIQUE INDEX idx_preview_startup_cache_lookup
    ON preview_startup_cache (org_id, repo_id, snapshot_key, worker_node_id);
