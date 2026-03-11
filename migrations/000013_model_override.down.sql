ALTER TABLE projects DROP COLUMN IF EXISTS model_override;
ALTER TABLE projects DROP COLUMN IF EXISTS agent_type;
ALTER TABLE agent_runs DROP COLUMN IF EXISTS model_override;
