-- Restore pull_requests.session_id to NOT NULL.
-- WARNING: This permanently deletes any PRs with NULL session_id (external/manual
-- PRs created after the forward migration). Back up before rolling back if the
-- forward migration has been live for any significant period:
--   CREATE TABLE pull_requests_nullable_session_backup AS
--     SELECT * FROM pull_requests WHERE session_id IS NULL;
DELETE FROM pull_requests WHERE session_id IS NULL;
ALTER TABLE pull_requests ALTER COLUMN session_id SET NOT NULL;

-- Restore session_logs.thread_id CASCADE.
ALTER TABLE session_logs DROP CONSTRAINT IF EXISTS fk_session_logs_thread;
ALTER TABLE session_logs
    ADD CONSTRAINT fk_session_logs_thread
    FOREIGN KEY (thread_id) REFERENCES session_threads(id) ON DELETE CASCADE;

-- Restore session_messages.thread_id CASCADE.
ALTER TABLE session_messages DROP CONSTRAINT IF EXISTS fk_session_messages_thread;
ALTER TABLE session_messages
    ADD CONSTRAINT fk_session_messages_thread
    FOREIGN KEY (thread_id) REFERENCES session_threads(id) ON DELETE CASCADE;
