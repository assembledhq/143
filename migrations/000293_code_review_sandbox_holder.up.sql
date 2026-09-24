ALTER TABLE session_sandbox_holders
    DROP CONSTRAINT chk_session_sandbox_holders_holder_kind;

ALTER TABLE session_sandbox_holders
    ADD CONSTRAINT chk_session_sandbox_holders_holder_kind
        CHECK (holder_kind IN ('thread_runtime', 'preview', 'snapshot', 'operator', 'code_review'));
