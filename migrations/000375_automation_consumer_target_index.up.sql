-- All-status reverse evidence lookup; retained uncertainty survives lease/status
-- changes. Standalone concurrent build keeps old writers live during the scan.
CREATE INDEX CONCURRENTLY idx_automation_consumer_target
    ON automation_runs(org_id,target_id,target_generation,id);
