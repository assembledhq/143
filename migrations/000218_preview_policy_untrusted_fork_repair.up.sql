ALTER TABLE repository_preview_policies
    ADD COLUMN IF NOT EXISTS session_prewarm_untrusted_fork BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE session_preview_prewarm_runs
    DROP CONSTRAINT IF EXISTS session_preview_prewarm_runs_status_check,
    ADD CONSTRAINT session_preview_prewarm_runs_status_check
        CHECK (status IN ('decided', 'queued', 'running', 'skipped_capacity',
                          'skipped_superseded', 'skipped_user_started',
                          'skipped_cooldown', 'skipped_untrusted_fork',
                          'skipped_no_lockfiles', 'skipped_no_paths',
                          'classifier_timeout',
                          'succeeded', 'failed'));
