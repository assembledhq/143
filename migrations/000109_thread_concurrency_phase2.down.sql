DROP TABLE IF EXISTS session_thread_file_events;

DROP INDEX IF EXISTS idx_session_threads_running;

ALTER TABLE session_threads
    DROP COLUMN IF EXISTS base_snapshot_key,
    DROP COLUMN IF EXISTS cost_cents,
    DROP COLUMN IF EXISTS pending_message_count,
    DROP COLUMN IF EXISTS cancel_requested_at;
