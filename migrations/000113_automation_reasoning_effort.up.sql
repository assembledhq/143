ALTER TABLE automations
    ADD COLUMN reasoning_effort TEXT,
    ADD CONSTRAINT chk_automations_reasoning_effort
        CHECK (reasoning_effort IS NULL OR reasoning_effort IN ('low', 'medium', 'high', 'xhigh', 'max'));
