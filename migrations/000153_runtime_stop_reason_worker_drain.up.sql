ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_runtime_stop_reason_check;

ALTER TABLE sessions
    ADD CONSTRAINT sessions_runtime_stop_reason_check
        CHECK (runtime_stop_reason IN ('', 'user_cancel', 'soft_budget', 'no_progress', 'absolute_ceiling', 'force_kill', 'worker_recovery', 'worker_drain'));
