ALTER INDEX idx_session_questions_session RENAME TO idx_agent_run_questions_run;
ALTER INDEX idx_session_logs_session RENAME TO idx_agent_run_logs_run;

ALTER TABLE sessions RENAME COLUMN parent_session_id TO parent_run_id;

ALTER TABLE project_tasks RENAME COLUMN session_id TO agent_run_id;
ALTER TABLE pull_requests RENAME COLUMN session_id TO agent_run_id;
ALTER TABLE validations RENAME COLUMN session_id TO agent_run_id;

ALTER TABLE session_questions RENAME COLUMN session_id TO agent_run_id;
ALTER TABLE session_logs RENAME COLUMN session_id TO agent_run_id;

ALTER TABLE session_questions RENAME TO agent_run_questions;
ALTER TABLE session_logs RENAME TO agent_run_logs;

ALTER INDEX idx_sessions_parent RENAME TO idx_agent_runs_parent;
ALTER INDEX idx_sessions_org_created RENAME TO idx_agent_runs_org_created;
ALTER INDEX idx_sessions_issue RENAME TO idx_agent_runs_issue;
ALTER INDEX idx_sessions_org_status RENAME TO idx_agent_runs_org_status;

ALTER TABLE sessions RENAME TO agent_runs;
