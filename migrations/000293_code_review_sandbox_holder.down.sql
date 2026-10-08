-- Rollback is safe only after review traffic is drained and all live leases
-- have been released or expired. Fail rather than silently unprotecting a
-- container still held for a review handoff.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM session_sandbox_holders
        WHERE holder_kind = 'code_review' AND status IN ('active', 'draining')
    ) THEN
        RAISE EXCEPTION 'drain code review sandbox holders before rolling back migration 000293';
    END IF;
END $$;

DELETE FROM session_sandbox_holders WHERE holder_kind = 'code_review';

ALTER TABLE session_sandbox_holders
    DROP CONSTRAINT chk_session_sandbox_holders_holder_kind;

ALTER TABLE session_sandbox_holders
    ADD CONSTRAINT chk_session_sandbox_holders_holder_kind
        CHECK (holder_kind IN ('thread_runtime', 'preview', 'snapshot', 'operator'));
