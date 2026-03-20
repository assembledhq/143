-- Reverse multi-agent sessions: drop thread_id columns and session_threads table.

DROP INDEX IF EXISTS idx_session_logs_thread;
ALTER TABLE session_logs DROP COLUMN IF EXISTS thread_id;

DROP INDEX IF EXISTS idx_session_messages_thread;
ALTER TABLE session_messages DROP COLUMN IF EXISTS thread_id;

DROP TABLE IF EXISTS session_threads;
