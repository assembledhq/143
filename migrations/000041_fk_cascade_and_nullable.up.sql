-- =============================================================================
-- Issue 11: session_messages.thread_id should SET NULL, not CASCADE
-- Messages have archival/audit value and should survive thread deletion.
-- =============================================================================

-- session_messages is a partitioned table, so we need to drop and re-add the constraint.
ALTER TABLE session_messages DROP CONSTRAINT IF EXISTS fk_session_messages_thread;
ALTER TABLE session_messages
    ADD CONSTRAINT fk_session_messages_thread
    FOREIGN KEY (thread_id) REFERENCES session_threads(id) ON DELETE SET NULL NOT VALID;
ALTER TABLE session_messages VALIDATE CONSTRAINT fk_session_messages_thread;

-- Also fix session_logs.thread_id — logs should also survive thread deletion.
ALTER TABLE session_logs DROP CONSTRAINT IF EXISTS fk_session_logs_thread;
ALTER TABLE session_logs
    ADD CONSTRAINT fk_session_logs_thread
    FOREIGN KEY (thread_id) REFERENCES session_threads(id) ON DELETE SET NULL NOT VALID;
ALTER TABLE session_logs VALIDATE CONSTRAINT fk_session_logs_thread;

-- =============================================================================
-- Issue 15: pull_requests.session_id should be nullable
-- Allows tracking manual PRs or PRs imported from external sources.
-- =============================================================================
ALTER TABLE pull_requests ALTER COLUMN session_id DROP NOT NULL;

-- =============================================================================
-- Issue 18: session_review_comments — ensure org_id FK is consistent
-- The org_id FK should not block org deletion if the session cascade would
-- handle cleanup. Make both FKs explicit about their cascade behavior.
-- =============================================================================
-- org_id already defaults to RESTRICT which is correct — org deletion should
-- be blocked until all data is cleaned up. The session_id CASCADE handles
-- cleanup when a session is deleted. No change needed, but add a comment
-- in the down migration for clarity.
