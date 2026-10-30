-- All-status reverse evidence lookup; retained uncertainty survives lease/status
-- changes. Standalone concurrent build keeps old writers live during the scan.
CREATE INDEX CONCURRENTLY idx_automation_consumer_job
    ON automation_runs(org_id,job_id,id)
    WHERE job_id IS NOT NULL;
