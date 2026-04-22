ALTER TABLE sessions DROP CONSTRAINT IF EXISTS fk_sessions_latest_diff_snapshot;
DROP TABLE IF EXISTS session_diff_snapshots;
ALTER TABLE sessions
    DROP COLUMN IF EXISTS latest_diff_snapshot_id,
    DROP COLUMN IF EXISTS diff_collected_at,
    DROP COLUMN IF EXISTS base_commit_sha;
