DROP INDEX IF EXISTS idx_pull_request_repair_runs_auto_attempts;

ALTER TABLE pull_request_repair_runs
  DROP COLUMN IF EXISTS triggered_by_user_id,
  DROP COLUMN IF EXISTS triggered_by_source,
  DROP COLUMN IF EXISTS trigger_reason,
  DROP COLUMN IF EXISTS auto_attempt;
