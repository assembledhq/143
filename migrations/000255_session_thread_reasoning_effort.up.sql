-- Migration 000255: allow each agent tab to override the parent session's
-- reasoning effort. Code-review reviewer tabs use this to run heterogeneous
-- reviewer rosters without changing the orchestrator's reasoning level.
ALTER TABLE session_threads
    ADD COLUMN reasoning_effort text;

ALTER TABLE session_threads
    ADD CONSTRAINT session_threads_reasoning_effort_check
    CHECK (reasoning_effort IS NULL OR reasoning_effort IN ('low', 'medium', 'high', 'xhigh', 'max'));
