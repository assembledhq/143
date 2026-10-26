-- No active protocol or parked intent may be silently downgraded or rewritten.
SET LOCAL lock_timeout = '5s';
LOCK TABLE public.sessions, public.session_lifecycle_cutovers, public.jobs IN ACCESS EXCLUSIVE MODE;
SELECT lifecycle143_assert_protocol_rollback_safe();
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM jobs WHERE status='awaiting_start_control') THEN
        RAISE EXCEPTION 'retained lifecycle parked intent prevents rollback' USING ERRCODE='55000';
    END IF;
END $$;
ALTER TABLE jobs ADD CONSTRAINT chk_jobs_status_legacy CHECK (status IN (
    'pending', 'running', 'succeeded', 'failed', 'cancelled', 'dead_letter'
)) NOT VALID;
-- Leave validation to 351 down, in a separate weaker-lock transaction. The
-- exclusive rollback unit above already proved that no parked row remains.
