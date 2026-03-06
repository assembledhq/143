DELETE FROM agent_runs WHERE issue_id IS NULL;

ALTER TABLE agent_runs
    ALTER COLUMN issue_id SET NOT NULL;
