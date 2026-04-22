-- Track the state of PR creation for a session so the UI can show progress.
-- The session status already captures the lifecycle of the agent run itself;
-- pr_creation_state is orthogonal and only moves off 'idle' once the user
-- clicks "Create PR" on a completed session.
ALTER TABLE sessions
    ADD COLUMN pr_creation_state text NOT NULL DEFAULT 'idle',
    ADD COLUMN pr_creation_error text;

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_pr_creation_state CHECK (pr_creation_state IN (
        'idle', 'queued', 'pushing', 'succeeded', 'failed'
    )) NOT VALID;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_pr_creation_state;
