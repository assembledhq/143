ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_checkpoint_kind_check,
    ADD CONSTRAINT sessions_checkpoint_kind_check
        CHECK (checkpoint_kind IN ('', 'turn_complete', 'graceful_stop'));
