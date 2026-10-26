-- All-status reverse evidence lookup; retained uncertainty survives lease/status
-- changes. Standalone concurrent build keeps old writers live during the scan.
CREATE INDEX CONCURRENTLY idx_automation_consumer_thread
    ON automation_runs(org_id,thread_id,id);
