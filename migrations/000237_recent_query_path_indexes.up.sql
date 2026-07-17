SET LOCAL lock_timeout = '5s';

CREATE INDEX IF NOT EXISTS idx_session_messages_token_usage_created
  ON session_messages (created_at, org_id, session_id)
  WHERE token_usage IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_pull_request_repair_runs_auto_thread_latest
  ON pull_request_repair_runs (org_id, session_id, thread_id, created_at DESC)
  WHERE auto_attempt = true;
