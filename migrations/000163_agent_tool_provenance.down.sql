ALTER TABLE session_messages
DROP COLUMN source;

ALTER TABLE session_threads
DROP COLUMN created_by_thread_id,
DROP COLUMN created_by_source;
