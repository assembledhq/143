ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_reasoning_effort,
    DROP COLUMN IF EXISTS reasoning_effort;
