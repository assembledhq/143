DROP TRIGGER IF EXISTS trg_reject_disabled_pm_jobs ON jobs;
DROP FUNCTION IF EXISTS reject_disabled_pm_jobs();

-- Cancelled jobs are intentionally not reactivated.
