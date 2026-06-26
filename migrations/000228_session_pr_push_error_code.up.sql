ALTER TABLE sessions
    ADD COLUMN pr_push_error_code text;

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_pr_push_error_code CHECK (
        pr_push_error_code IS NULL OR pr_push_error_code IN (
            'branch_diverged',
            'push_rejected',
            'sandbox_auth_unavailable',
            'generic'
        )
    ) NOT VALID;

UPDATE sessions
SET pr_push_error_code = 'branch_diverged'
WHERE pr_push_state = 'failed'
  AND pr_push_error = 'The PR branch has changes that are not in this session checkpoint. Pull the latest PR branch into the session before pushing again.';

ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_pr_push_error_code;
