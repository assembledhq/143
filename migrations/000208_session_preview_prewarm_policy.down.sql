DROP INDEX IF EXISTS idx_session_preview_prewarm_runs_session;
DROP INDEX IF EXISTS idx_session_preview_prewarm_runs_active;
DROP INDEX IF EXISTS idx_session_preview_prewarm_runs_scope;
DROP TABLE IF EXISTS session_preview_prewarm_runs;

ALTER TABLE preview_instances
    DROP CONSTRAINT IF EXISTS preview_instances_stopped_reason_check,
    ADD CONSTRAINT preview_instances_stopped_reason_check
        CHECK (stopped_reason IN ('', 'user', 'expired', 'warm_policy',
                                  'pr_closed', 'drain', 'error'));

ALTER TABLE repository_preview_policies
    DROP CONSTRAINT IF EXISTS repository_preview_policies_session_prewarm_mode_check,
    DROP COLUMN IF EXISTS session_prewarm_untrusted_fork,
    DROP COLUMN IF EXISTS session_prewarm_mode;
