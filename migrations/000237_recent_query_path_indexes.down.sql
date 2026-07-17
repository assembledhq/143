SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS idx_pull_request_repair_runs_auto_thread_latest;
DROP INDEX IF EXISTS idx_session_messages_token_usage_created;
