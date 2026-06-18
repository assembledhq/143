ALTER TABLE automation_runs
    DROP CONSTRAINT IF EXISTS chk_automation_runs_capability_snapshot_array,
    DROP COLUMN IF EXISTS capability_snapshot;

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_capability_snapshot_array,
    DROP COLUMN IF EXISTS capability_snapshot;

DROP TABLE IF EXISTS agent_capability_policy_grants;
DROP TABLE IF EXISTS agent_capability_policies;
