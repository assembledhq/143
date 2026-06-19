ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_origin;

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_origin
    CHECK (origin IN (
        'issue_trigger',
        'manual',
        'project',
        'automation',
        'revision',
        'slack',
        'external_api',
        'eval_bootstrap',
        'eval_run'
    ));
