-- This migration is also the rolling-deploy barrier for the PM shutdown.
-- Refuse to proceed while an old worker is actively executing PM code; the
-- operator must let it finish or drain it before retrying the deployment.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM jobs
        WHERE status = 'running'
          AND job_type IN ('pm_analyze', 'pm_bootstrap', 'pm_context_refresh', 'project_cycle')
    ) THEN
        RAISE EXCEPTION
            'PM shutdown requires all running PM jobs to be drained before migration';
    END IF;
END
$$;

CREATE FUNCTION reject_disabled_pm_jobs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.job_type IN ('pm_analyze', 'pm_bootstrap', 'pm_context_refresh', 'project_cycle') THEN
        RAISE EXCEPTION 'job type % is disabled by the PM shutdown', NEW.job_type
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END
$$;

-- CREATE TRIGGER takes a lock that serializes against concurrent INSERTs.
-- Once committed, old schedulers and API processes can no longer enqueue PM
-- work during the remainder of a rolling deployment.
CREATE TRIGGER trg_reject_disabled_pm_jobs
BEFORE INSERT OR UPDATE OF job_type ON jobs
FOR EACH ROW
EXECUTE FUNCTION reject_disabled_pm_jobs();

UPDATE jobs
SET status = 'cancelled',
    updated_at = now(),
    last_error = 'cancelled: PM and Autopilot shutdown'
WHERE status = 'pending'
  AND job_type IN ('pm_analyze', 'pm_bootstrap', 'pm_context_refresh', 'project_cycle');
