-- Drop the session FK first.
DROP INDEX IF EXISTS idx_sessions_automation_run;
ALTER TABLE sessions DROP COLUMN IF EXISTS automation_run_id;

-- Drop automation_runs.
DROP TABLE IF EXISTS automation_runs;

-- Drop automations.
DROP TABLE IF EXISTS automations;
