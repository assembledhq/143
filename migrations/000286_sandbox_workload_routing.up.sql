-- Classify sandbox-producing work so dispatch and local admission can reserve
-- capacity for interactive sessions during background code-review bursts.
-- Keep this before the first DDL so an unprepared or manually-run migration
-- fails quickly instead of waiting indefinitely for the hot jobs table.
SET LOCAL lock_timeout = '5s';

ALTER TABLE jobs
    ADD COLUMN IF NOT EXISTS workload_class text NOT NULL DEFAULT 'interactive',
    ADD COLUMN IF NOT EXISTS sandbox_slot_reserved_until timestamptz;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'chk_jobs_workload_class'
          AND conrelid = 'jobs'::regclass
    ) THEN
        ALTER TABLE jobs
            ADD CONSTRAINT chk_jobs_workload_class
            CHECK (workload_class IN ('interactive', 'code_review')) NOT VALID;
    END IF;
END $$;

-- Preserve the classification of active reviews that were queued by a binary
-- deployed before workload_class existed (or before it was populated).
WITH active_sandbox_jobs AS MATERIALIZED (
    SELECT j.id,
           j.org_id,
           CASE
               WHEN j.payload->>'session_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
               THEN (j.payload->>'session_id')::uuid
           END AS session_id
    FROM jobs j
    WHERE j.job_type IN ('run_agent', 'continue_session')
      AND j.status IN ('pending', 'running')
      AND j.workload_class <> 'code_review'
)
UPDATE jobs j
SET workload_class = 'code_review'
FROM active_sandbox_jobs active
JOIN sessions s
  ON s.id = active.session_id
 AND s.org_id = active.org_id
WHERE j.id = active.id
  AND j.org_id = active.org_id
  AND s.origin = 'code_review';

-- jobs is a hot queue table, and golang-migrate wraps this file in a
-- transaction. `migrate up` therefore adds the columns and pre-builds both
-- indexes concurrently before golang-migrate reaches this file. IF NOT EXISTS
-- keeps these statements as a replay-safe fallback for fresh or otherwise
-- empty databases that do not need the hot-table preparation.

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
