DROP TABLE IF EXISTS code_review_findings;
DROP TABLE IF EXISTS code_review_agent_results;
DROP TABLE IF EXISTS code_review_session_metadata;
DROP TABLE IF EXISTS code_review_policies;

UPDATE sessions
SET origin = 'manual'
WHERE origin = 'code_review';

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
        'eval_run',
        'automation_goal_improvement'
    ));
