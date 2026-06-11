-- Partial snapshot invalidation for branch preview startup caches.
--
-- snapshot_key is an exact hash of (lockfiles, commit SHA, config digest), so
-- every new commit on a branch is a full cache miss even when dependencies
-- and config are unchanged. base_key drops the commit from the hash and
-- commit_sha records which commit the blob was built at, so a start at a new
-- commit can restore the closest base snapshot and apply a git diff on top
-- instead of rebuilding from scratch.
ALTER TABLE preview_startup_cache
    ADD COLUMN base_key   TEXT NOT NULL DEFAULT '',
    ADD COLUMN commit_sha TEXT NOT NULL DEFAULT '';

-- Newest-first lookup of base-compatible snapshots on a worker. Pre-migration
-- rows have base_key = '' and are simply never base-matched; they continue to
-- serve exact snapshot_key hits until LRU eviction retires them.
CREATE INDEX idx_preview_startup_cache_base_lookup
    ON preview_startup_cache (org_id, repo_id, base_key, worker_node_id, created_at DESC)
    WHERE base_key <> '';
