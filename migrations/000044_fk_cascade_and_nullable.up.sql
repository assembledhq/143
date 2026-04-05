-- =============================================================================
-- Issue 11: session_messages.thread_id should SET NULL, not CASCADE
-- Messages have archival/audit value and should survive thread deletion.
-- =============================================================================

-- session_messages and session_logs are partitioned tables.
-- PostgreSQL does not support NOT VALID foreign keys on partitioned tables,
-- so we drop and re-add the constraint directly (validates immediately).

ALTER TABLE session_messages DROP CONSTRAINT IF EXISTS fk_session_messages_thread;
ALTER TABLE session_messages
    ADD CONSTRAINT fk_session_messages_thread
    FOREIGN KEY (thread_id) REFERENCES session_threads(id) ON DELETE SET NULL;

-- Also fix session_logs.thread_id — logs should also survive thread deletion.
ALTER TABLE session_logs DROP CONSTRAINT IF EXISTS fk_session_logs_thread;
ALTER TABLE session_logs
    ADD CONSTRAINT fk_session_logs_thread
    FOREIGN KEY (thread_id) REFERENCES session_threads(id) ON DELETE SET NULL;

-- =============================================================================
-- Issue 15: pull_requests.session_id should be nullable
-- Allows tracking manual PRs or PRs imported from external sources.
-- =============================================================================
ALTER TABLE pull_requests ALTER COLUMN session_id DROP NOT NULL;
