-- PR readiness is a removed product subsystem. This migration is deliberately
-- limited to the jobs table: golang-migrate executes each file as one
-- transaction, so holding the jobs lock while also changing settings, leases,
-- and readiness tables can deadlock with application transactions that touch
-- those relations before enqueueing work. Migration 000271 performs the
-- cross-table cleanup after this barrier transaction commits.
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM jobs
        WHERE status = 'running'
          AND job_type = 'run_pr_readiness'
    ) THEN
        RAISE EXCEPTION
            'PR readiness removal requires all running readiness jobs to be drained before migration';
    END IF;
END
$$;

CREATE FUNCTION reject_removed_pr_readiness_jobs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.job_type = 'run_pr_readiness' THEN
        RAISE EXCEPTION 'job type % was removed with the PR readiness subsystem', NEW.job_type
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER trg_reject_removed_pr_readiness_jobs
BEFORE INSERT OR UPDATE OF job_type ON jobs
FOR EACH ROW
EXECUTE FUNCTION reject_removed_pr_readiness_jobs();

-- Cancel queued work instead of deleting it: historical job rows stay readable
-- for operators (matching the PM shutdown in 000259), and a DELETE over the
-- whole table has no usable index on job_type alone, so it would hold the jobs
-- lock above for a full sequential scan. Cancelled rows are not covered by
-- delete_expired_completed_jobs, so they persist; the queued readiness backlog
-- is small and bounded, exactly as the PM shutdown left its cancelled rows.
UPDATE jobs
SET status = 'cancelled',
    updated_at = now(),
    last_error = 'cancelled: PR readiness subsystem removed'
WHERE status = 'pending'
  AND job_type = 'run_pr_readiness';
