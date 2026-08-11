-- Classify sandbox-producing work so dispatch and local admission can reserve
-- capacity for interactive sessions during background code-review bursts.
ALTER TABLE jobs
    ADD COLUMN workload_class text NOT NULL DEFAULT 'interactive',
    ADD COLUMN sandbox_slot_reserved_until timestamptz;

ALTER TABLE jobs
    ADD CONSTRAINT chk_jobs_workload_class
    CHECK (workload_class IN ('interactive', 'code_review')) NOT VALID;

ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_workload_class;

-- jobs is a hot queue table, and golang-migrate wraps this file in a
-- transaction, so production must pre-build both indexes without blocking
-- queue writers:
--
--   CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_jobs_sandbox_routing
--     ON jobs (priority DESC, (CASE WHEN workload_class = 'interactive' THEN 0 ELSE 1 END), created_at ASC, run_at)
--     WHERE status = 'pending' AND job_type IN ('run_agent', 'continue_session');
--   CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_jobs_active_sandbox_turns
--     ON jobs (org_id, status)
--     WHERE job_type IN ('run_agent', 'continue_session') AND status IN ('pending', 'running');
--
-- IF NOT EXISTS makes the transactional migration a no-op after that rollout
-- step. The timeout prevents an unprepared deploy from waiting indefinitely
-- for a lock while agent jobs are being inserted and updated.
SET LOCAL lock_timeout = '5s';

-- Match the dispatcher's priority / interactive-first / FIFO ordering so the
-- planner can walk runnable sandbox work without a separate queue-wide sort.
CREATE INDEX IF NOT EXISTS idx_jobs_sandbox_routing
    ON jobs (
        priority DESC,
        (CASE WHEN workload_class = 'interactive' THEN 0 ELSE 1 END),
        created_at ASC,
        run_at
    )
    WHERE status = 'pending'
      AND job_type IN ('run_agent', 'continue_session');

CREATE INDEX IF NOT EXISTS idx_jobs_active_sandbox_turns
    ON jobs (org_id, status)
    WHERE job_type IN ('run_agent', 'continue_session')
      AND status IN ('pending', 'running');
