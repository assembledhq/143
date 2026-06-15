SET LOCAL lock_timeout = '5s';

CREATE INDEX IF NOT EXISTS idx_session_messages_thread_turn_id
  ON session_messages (org_id, thread_id, turn_number, id);

CREATE INDEX IF NOT EXISTS idx_session_logs_thread_turn_id
  ON session_logs (org_id, thread_id, turn_number, id);

CREATE INDEX IF NOT EXISTS idx_session_human_input_requests_thread_turn_created
  ON session_human_input_requests (org_id, thread_id, turn_number, created_at, id);
