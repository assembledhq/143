-- Review depth was dead config: stored only inside the agent_roster JSONB and
-- rendered as informational text in the reviewer/orchestrator prompts, with no
-- code branching on it. Drop the stale key from existing policies.
UPDATE code_review_policies
SET agent_roster = agent_roster - 'review_depth'
WHERE agent_roster ? 'review_depth';
