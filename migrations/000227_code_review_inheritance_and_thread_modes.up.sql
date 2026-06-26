ALTER TABLE code_review_policies
    ADD COLUMN inheritance jsonb NOT NULL DEFAULT '{"inherit_org_defaults":false,"override_fields":[]}'::jsonb;

ALTER TABLE session_threads
    ADD COLUMN execution_mode text NOT NULL DEFAULT 'work',
    ADD COLUMN filesystem_mode text NOT NULL DEFAULT 'read_write';

ALTER TABLE session_threads
    ADD CONSTRAINT chk_session_threads_execution_mode CHECK (execution_mode IN ('work', 'review')),
    ADD CONSTRAINT chk_session_threads_filesystem_mode CHECK (filesystem_mode IN ('read_write', 'read_only'));
