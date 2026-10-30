SET LOCAL lock_timeout = '5s';
LOCK TABLE public.sessions, public.session_lifecycle_cutovers,
    public.session_input_dispatches IN ACCESS EXCLUSIVE MODE;
SELECT lifecycle143_assert_protocol_rollback_safe();
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM session_input_dispatches WHERE runtime_id IS NOT NULL) THEN
        RAISE EXCEPTION 'retained lifecycle runtime binding prevents rollback' USING ERRCODE='55000';
    END IF;
END $$;
ALTER TABLE session_input_dispatches DROP CONSTRAINT session_input_dispatches_consumer_runtime_fk;
