ALTER TABLE session_threads
    DROP CONSTRAINT IF EXISTS session_threads_reasoning_effort_check;

ALTER TABLE session_threads
    DROP COLUMN IF EXISTS reasoning_effort;
