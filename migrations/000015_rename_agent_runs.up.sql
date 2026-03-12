-- Rename agent_runs table to sessions (the primary work session entity).
ALTER TABLE agent_runs RENAME TO sessions;
ALTER INDEX idx_agent_runs_org_status RENAME TO idx_sessions_org_status;
ALTER INDEX idx_agent_runs_issue RENAME TO idx_sessions_issue;
ALTER INDEX idx_agent_runs_org_created RENAME TO idx_sessions_org_created;
ALTER INDEX idx_agent_runs_parent RENAME TO idx_sessions_parent;

-- Rename sub-tables.
ALTER TABLE agent_run_logs RENAME TO session_logs;
ALTER TABLE agent_run_logs_pkey RENAME TO session_logs_pkey;
ALTER TABLE agent_run_questions RENAME TO session_questions;
ALTER TABLE agent_run_questions_pkey RENAME TO session_questions_pkey;

-- Rename FK columns in sub-tables.
ALTER TABLE session_logs RENAME COLUMN agent_run_id TO session_id;
ALTER TABLE session_questions RENAME COLUMN agent_run_id TO session_id;

-- Rename FK columns in referencing tables.
ALTER TABLE validations RENAME COLUMN agent_run_id TO session_id;
ALTER TABLE pull_requests RENAME COLUMN agent_run_id TO session_id;
ALTER TABLE project_tasks RENAME COLUMN agent_run_id TO session_id;

-- Rename parent_run_id to parent_session_id in sessions table.
ALTER TABLE sessions RENAME COLUMN parent_run_id TO parent_session_id;

-- Rename indexes on sub-tables.
ALTER INDEX idx_agent_run_logs_run RENAME TO idx_session_logs_session;
ALTER INDEX idx_agent_run_questions_run RENAME TO idx_session_questions_session;
