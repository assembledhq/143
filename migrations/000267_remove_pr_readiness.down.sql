-- Restore only the rolling-deploy jobs barrier. Migration 000271 owns the
-- shared lease contract and destructive readiness-state cleanup.
DROP TRIGGER IF EXISTS trg_reject_removed_pr_readiness_jobs ON jobs;
DROP FUNCTION IF EXISTS reject_removed_pr_readiness_jobs();
