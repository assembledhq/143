SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS idx_session_messages_thread_turn_id;
DROP INDEX IF EXISTS idx_session_logs_thread_turn_id;
DROP INDEX IF EXISTS idx_session_human_input_requests_thread_turn_created;
