DROP INDEX IF EXISTS idx_eval_runs_session_thread;

ALTER TABLE eval_runs
    DROP COLUMN IF EXISTS thread_id,
    DROP COLUMN IF EXISTS session_id;

ALTER TABLE eval_runs
    DROP CONSTRAINT IF EXISTS chk_eval_runs_status;

ALTER TABLE eval_runs
    ADD CONSTRAINT chk_eval_runs_status
        CHECK (status IN ('pending', 'running', 'completed', 'failed'));

DROP INDEX IF EXISTS idx_eval_bootstrap_candidates_status;
DROP INDEX IF EXISTS idx_eval_bootstrap_candidates_run;
DROP TABLE IF EXISTS eval_bootstrap_candidates;

DROP INDEX IF EXISTS idx_eval_bootstrap_runs_session_thread;

ALTER TABLE eval_bootstrap_runs
    DROP COLUMN IF EXISTS thread_id;

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_origin;

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_origin
        CHECK (origin IN ('issue_trigger', 'manual', 'project', 'automation', 'revision', 'slack', 'external_api'));
