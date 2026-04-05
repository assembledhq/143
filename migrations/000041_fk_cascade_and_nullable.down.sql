-- Restore pull_requests.session_id to NOT NULL.
-- Delete rows with NULL session_id since they can't satisfy the NOT NULL constraint.
-- These are external/unlinked PRs that were created after the forward migration.
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
