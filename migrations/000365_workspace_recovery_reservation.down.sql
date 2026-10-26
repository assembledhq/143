SET LOCAL lock_timeout = '5s';
LOCK TABLE public.sessions, public.session_lifecycle_cutovers IN ACCESS EXCLUSIVE MODE;
SELECT lifecycle143_assert_protocol_rollback_safe();
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM sessions WHERE workspace_recovery_id IS NOT NULL
               OR workspace_recovery_token IS NOT NULL OR workspace_recovery IS NOT NULL
               OR workspace_recovery_next_attempt_at IS NOT NULL) THEN
        RAISE EXCEPTION 'retained lifecycle recovery prevents rollback' USING ERRCODE='55000';
    END IF;
END $$;
DROP TRIGGER trg_sessions_retain_workspace_recovery ON sessions;
DROP TRIGGER trg_sessions_recovery_no_truncate ON sessions;
DROP FUNCTION lifecycle143_retain_workspace_recovery();
ALTER TABLE sessions DROP CONSTRAINT sessions_workspace_recovery_shape_check,
    DROP COLUMN workspace_recovery_id, DROP COLUMN workspace_recovery_token,
    DROP COLUMN workspace_recovery, DROP COLUMN workspace_recovery_next_attempt_at;
