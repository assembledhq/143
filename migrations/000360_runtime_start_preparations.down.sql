SET LOCAL lock_timeout = '5s';
LOCK TABLE public.sessions, public.session_lifecycle_cutovers,
    public.session_runtime_start_preparations IN ACCESS EXCLUSIVE MODE;
SELECT lifecycle143_assert_protocol_rollback_safe();
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM session_runtime_start_preparations) THEN
        RAISE EXCEPTION 'retained lifecycle preparation prevents rollback' USING ERRCODE='55000';
    END IF;
END $$;
DROP TABLE session_runtime_start_preparations;
DROP FUNCTION lifecycle143_retain_preparation_operation();
