-- Standalone concurrent reverse-reference index on the existing runtime table.
-- A leftover invalid index must fail visibly rather than be skipped.
CREATE INDEX CONCURRENTLY idx_thread_runtimes_consumer_job
    ON thread_runtimes (org_id, job_id) WHERE job_id IS NOT NULL;
