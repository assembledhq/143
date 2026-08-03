-- Databases that applied this branch's former 000274 dependency-cache migration
-- advanced past main's different 000274 migration without creating its index.
-- Replay the displaced operation under a new version. IF NOT EXISTS makes this
-- a no-op for production and fresh databases that already ran main's 000274.
SET LOCAL lock_timeout = '5s';

CREATE INDEX IF NOT EXISTS idx_code_review_metadata_pull_request_created
    ON code_review_session_metadata (org_id, pull_request_id, created_at DESC, id DESC);
