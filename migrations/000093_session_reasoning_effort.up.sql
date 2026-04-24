ALTER TABLE sessions
    ADD COLUMN reasoning_effort TEXT,
    ADD CONSTRAINT chk_sessions_reasoning_effort
        CHECK (reasoning_effort IS NULL OR reasoning_effort IN ('low', 'medium', 'high', 'xhigh', 'max'));
