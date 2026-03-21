DROP FUNCTION IF EXISTS delete_expired_webhook_deliveries(int);
DROP FUNCTION IF EXISTS delete_expired_session_logs(int);
DROP FUNCTION IF EXISTS delete_expired_completed_jobs(int);

DROP INDEX IF EXISTS idx_webhook_deliveries_created_at;
DROP INDEX IF EXISTS idx_session_logs_timestamp;
DROP INDEX IF EXISTS idx_jobs_status_updated_at;
