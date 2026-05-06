ALTER TABLE automations
    DROP CONSTRAINT IF EXISTS chk_automations_goal_length;

ALTER TABLE automations
    ADD CONSTRAINT chk_automations_goal_length CHECK (char_length(goal) BETWEEN 1 AND 4000);
