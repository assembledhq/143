-- Withdraw ordinary/answer/consumer readers and inhibit activation and
-- lifecycle/recovery/domain writers throughout the entire inactive rollback
-- maintenance operation, or repair forward. These locks protect this fresh
-- preflight only; they cannot span later standalone concurrent index drops.
-- Activation must revalidate the complete schema/index floor under its own
-- schema-protecting locks before any compatible-writer floor is enabled.
SET LOCAL lock_timeout = '5s';
LOCK TABLE public.sessions, public.session_lifecycle_cutovers, public.jobs,
    public.session_input_dispatches, public.thread_runtimes,
    public.session_runtime_start_preparations, public.pull_request_repair_runs,
    public.session_questions, public.session_human_input_requests
    IN ACCESS EXCLUSIVE MODE;
SELECT lifecycle143_assert_protocol_rollback_safe();
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM sessions WHERE workspace_recovery_id IS NOT NULL
               OR workspace_recovery_token IS NOT NULL OR workspace_recovery IS NOT NULL
               OR workspace_recovery_next_attempt_at IS NOT NULL)
       OR EXISTS (SELECT 1 FROM session_runtime_start_preparations)
       OR EXISTS (SELECT 1 FROM session_input_dispatches WHERE admission_state IS NOT NULL
                  OR job_id IS NOT NULL OR runtime_id IS NOT NULL)
       OR EXISTS (SELECT 1 FROM thread_runtimes WHERE job_id IS NOT NULL)
       OR EXISTS (SELECT 1 FROM jobs WHERE status='awaiting_start_control'
                  OR job_type='snapshot_object_cleanup') THEN
        RAISE EXCEPTION 'retained lifecycle consumers prevent admission index rollback' USING ERRCODE='55000';
    END IF;
END $$;
