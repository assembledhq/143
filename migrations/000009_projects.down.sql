-- Remove project_task_id from agent_runs first (depends on project_tasks).
ALTER TABLE agent_runs DROP COLUMN IF EXISTS project_task_id;

-- Drop tables in reverse dependency order.
DROP TABLE IF EXISTS project_cycles;
DROP TABLE IF EXISTS project_tasks;
DROP TABLE IF EXISTS projects;
