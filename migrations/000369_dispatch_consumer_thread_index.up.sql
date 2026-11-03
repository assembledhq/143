-- All-status reverse evidence lookup; retained uncertainty survives lease/status
-- changes. Standalone concurrent build keeps old writers live during the scan.
CREATE INDEX CONCURRENTLY idx_dispatch_consumer_thread
    ON session_input_dispatches(org_id,thread_id,id);
