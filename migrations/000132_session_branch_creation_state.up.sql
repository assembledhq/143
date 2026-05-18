ALTER TABLE sessions
    ADD COLUMN branch_creation_state text NOT NULL DEFAULT 'idle',
    ADD COLUMN branch_creation_error text,
    ADD COLUMN branch_url text,
    ADD CONSTRAINT chk_sessions_branch_creation_state CHECK (branch_creation_state IN (
        'idle', 'queued', 'pushing', 'succeeded', 'failed'
    ));

ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_branch_creation_state;
