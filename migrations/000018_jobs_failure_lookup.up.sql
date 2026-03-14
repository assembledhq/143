-- Index to efficiently look up the most recent failed job by org and type.
-- Used by the PM status endpoint to surface job errors in the UI.
CREATE INDEX idx_jobs_org_type_failed ON jobs (org_id, job_type, updated_at DESC)
    WHERE status IN ('failed', 'dead_letter');
