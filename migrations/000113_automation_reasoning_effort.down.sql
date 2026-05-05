ALTER TABLE automations
    DROP CONSTRAINT IF EXISTS chk_automations_reasoning_effort,
    DROP COLUMN IF EXISTS reasoning_effort;
