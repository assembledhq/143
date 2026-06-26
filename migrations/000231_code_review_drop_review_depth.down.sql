-- Restore the previous default so the down migration is reversible. The value
-- is purely informational and ignored on read.
UPDATE code_review_policies
SET agent_roster = jsonb_set(agent_roster, '{review_depth}', '"standard"', true)
WHERE NOT (agent_roster ? 'review_depth');
