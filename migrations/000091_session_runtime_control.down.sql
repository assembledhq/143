DROP INDEX IF EXISTS idx_sessions_recovery_queue;

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_recovery_state_check,
    DROP CONSTRAINT IF EXISTS sessions_checkpoint_capability_check,
    DROP CONSTRAINT IF EXISTS sessions_checkpoint_kind_check,
    DROP CONSTRAINT IF EXISTS sessions_runtime_stop_reason_check,
    DROP CONSTRAINT IF EXISTS sessions_runtime_last_progress_strength_check,
    DROP CONSTRAINT IF EXISTS sessions_runtime_last_progress_type_check;

ALTER TABLE sessions
    DROP COLUMN IF EXISTS recovery_attempt_count,
    DROP COLUMN IF EXISTS recovery_started_at,
    DROP COLUMN IF EXISTS recovery_queued_at,
    DROP COLUMN IF EXISTS recovery_state,
    DROP COLUMN IF EXISTS checkpoint_error,
    DROP COLUMN IF EXISTS checkpoint_size_bytes,
    DROP COLUMN IF EXISTS checkpoint_capability,
    DROP COLUMN IF EXISTS checkpoint_kind,
    DROP COLUMN IF EXISTS checkpointed_at,
    DROP COLUMN IF EXISTS runtime_graceful_stop_at,
    DROP COLUMN IF EXISTS runtime_stop_reason,
    DROP COLUMN IF EXISTS runtime_extension_seconds,
    DROP COLUMN IF EXISTS runtime_extension_count,
    DROP COLUMN IF EXISTS runtime_last_progress_strength,
    DROP COLUMN IF EXISTS runtime_last_progress_type,
    DROP COLUMN IF EXISTS runtime_last_progress_at,
    DROP COLUMN IF EXISTS runtime_hard_deadline_at,
    DROP COLUMN IF EXISTS runtime_soft_deadline_at;
