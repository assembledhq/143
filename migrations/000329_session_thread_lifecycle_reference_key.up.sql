-- Establish exact tenant/session/lane identity for future lifecycle foreign keys.
-- Standalone concurrent build: no enclosing transaction or additional statement.
-- Do not mask a leftover invalid or mismatched index with IF NOT EXISTS.
CREATE UNIQUE INDEX CONCURRENTLY idx_session_threads_org_session_id
    ON session_threads (org_id, session_id, id);
