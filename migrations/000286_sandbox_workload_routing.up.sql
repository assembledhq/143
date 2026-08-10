-- Classify sandbox-producing work so dispatch and local admission can reserve
-- capacity for interactive sessions during background code-review bursts.
ALTER TABLE jobs
    ADD COLUMN workload_class text NOT NULL DEFAULT 'interactive',
    ADD COLUMN sandbox_slot_reserved_until timestamptz;

ALTER TABLE jobs
    ADD CONSTRAINT chk_jobs_workload_class
    CHECK (workload_class IN ('interactive', 'code_review')) NOT VALID;

ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_workload_class;

-- The router scans only fresh sandbox starts: unpinned jobs and expired
-- scheduling reservations. Existing-sandbox affinity pins remain untouched.
CREATE INDEX idx_jobs_sandbox_routing
    ON jobs (status, run_at, priority DESC, created_at ASC)
    WHERE status = 'pending'
      AND job_type IN ('run_agent', 'continue_session');

CREATE INDEX idx_jobs_active_workload
    ON jobs (org_id, workload_class, status)
    WHERE job_type IN ('run_agent', 'continue_session')
      AND status IN ('pending', 'running');
