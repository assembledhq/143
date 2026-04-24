ALTER TABLE sessions
    ADD COLUMN runtime_soft_deadline_at timestamptz,
    ADD COLUMN runtime_hard_deadline_at timestamptz,
    ADD COLUMN runtime_last_progress_at timestamptz,
    ADD COLUMN runtime_last_progress_type text NOT NULL DEFAULT '',
    ADD COLUMN runtime_last_progress_strength text NOT NULL DEFAULT '',
    ADD COLUMN runtime_extension_count integer NOT NULL DEFAULT 0,
    ADD COLUMN runtime_extension_seconds integer NOT NULL DEFAULT 0,
    ADD COLUMN runtime_stop_reason text NOT NULL DEFAULT '',
    ADD COLUMN runtime_graceful_stop_at timestamptz,
    ADD COLUMN checkpointed_at timestamptz,
    ADD COLUMN checkpoint_kind text NOT NULL DEFAULT '',
    ADD COLUMN checkpoint_capability text NOT NULL DEFAULT '',
    ADD COLUMN checkpoint_size_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN checkpoint_error text,
    ADD COLUMN recovery_state text NOT NULL DEFAULT '',
    ADD COLUMN recovery_queued_at timestamptz,
    ADD COLUMN recovery_started_at timestamptz,
    ADD COLUMN recovery_attempt_count integer NOT NULL DEFAULT 0;

ALTER TABLE sessions
    ADD CONSTRAINT sessions_runtime_last_progress_type_check
        CHECK (runtime_last_progress_type IN ('', 'assistant_output', 'assistant_reasoning', 'tool_use', 'tool_result', 'diff_changed', 'checkpoint_written', 'question_blocked')),
    ADD CONSTRAINT sessions_runtime_last_progress_strength_check
        CHECK (runtime_last_progress_strength IN ('', 'weak', 'strong')),
    ADD CONSTRAINT sessions_runtime_stop_reason_check
        CHECK (runtime_stop_reason IN ('', 'user_cancel', 'soft_budget', 'no_progress', 'absolute_ceiling', 'force_kill', 'worker_recovery')),
    ADD CONSTRAINT sessions_checkpoint_kind_check
        CHECK (checkpoint_kind IN ('', 'turn_complete', 'graceful_stop')),
    ADD CONSTRAINT sessions_checkpoint_capability_check
        CHECK (checkpoint_capability IN ('', 'full_resume', 'filesystem_resume', 'no_durable_resume')),
    ADD CONSTRAINT sessions_recovery_state_check
        CHECK (recovery_state IN ('', 'queued', 'recovering', 'unavailable'));

CREATE INDEX idx_sessions_recovery_queue
    ON sessions (recovery_state, recovery_queued_at)
    WHERE recovery_state <> '';
