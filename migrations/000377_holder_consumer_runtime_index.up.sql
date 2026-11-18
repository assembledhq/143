-- All-status reverse evidence lookup; retained uncertainty survives lease/status
-- changes. Standalone concurrent build keeps old writers live during the scan.
CREATE INDEX CONCURRENTLY idx_holder_consumer_runtime
    ON session_sandbox_holders(org_id,holder_kind,holder_id,id);
