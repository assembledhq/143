-- All-status reverse evidence lookup; retained uncertainty survives lease/status
-- changes. Standalone concurrent build keeps old writers live during the scan.
CREATE INDEX CONCURRENTLY idx_executor_consumer_thread
    ON session_executors(org_id,thread_id,id);
