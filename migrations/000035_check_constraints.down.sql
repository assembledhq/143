-- Drop all CHECK constraints added in the up migration.
-- Order does not matter for independent ALTER TABLE drops.

ALTER TABLE users DROP CONSTRAINT IF EXISTS chk_users_role;

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_status,
    DROP CONSTRAINT IF EXISTS chk_sessions_sandbox_state,
    DROP CONSTRAINT IF EXISTS chk_sessions_autonomy_level,
    DROP CONSTRAINT IF EXISTS chk_sessions_token_mode,
    DROP CONSTRAINT IF EXISTS chk_sessions_agent_type;
-- Restore legacy default that existed before migration 35.
ALTER TABLE sessions ALTER COLUMN autonomy_level SET DEFAULT 'manual';

ALTER TABLE session_threads DROP CONSTRAINT IF EXISTS chk_session_threads_status;
ALTER TABLE session_questions DROP CONSTRAINT IF EXISTS chk_session_questions_status;
-- session_logs CHECK constraint is managed by migration 037 (partitioning).

ALTER TABLE jobs DROP CONSTRAINT IF EXISTS chk_jobs_status;

ALTER TABLE projects
    DROP CONSTRAINT IF EXISTS chk_projects_status,
    DROP CONSTRAINT IF EXISTS chk_projects_execution_mode,
    DROP CONSTRAINT IF EXISTS chk_projects_schedule_unit;

ALTER TABLE project_tasks DROP CONSTRAINT IF EXISTS chk_project_tasks_status;

ALTER TABLE integrations
    DROP CONSTRAINT IF EXISTS chk_integrations_status,
    DROP CONSTRAINT IF EXISTS chk_integrations_provider;

ALTER TABLE repositories DROP CONSTRAINT IF EXISTS chk_repositories_status;

ALTER TABLE issues
    DROP CONSTRAINT IF EXISTS chk_issues_status,
    DROP CONSTRAINT IF EXISTS chk_issues_severity,
    DROP CONSTRAINT IF EXISTS chk_issues_source;

ALTER TABLE pull_requests
    DROP CONSTRAINT IF EXISTS chk_pull_requests_status,
    DROP CONSTRAINT IF EXISTS chk_pull_requests_review_status;

ALTER TABLE validations DROP CONSTRAINT IF EXISTS chk_validations_status;
ALTER TABLE invitations DROP CONSTRAINT IF EXISTS chk_invitations_status;
ALTER TABLE review_comments DROP CONSTRAINT IF EXISTS chk_review_comments_filter_status;
ALTER TABLE memories DROP CONSTRAINT IF EXISTS chk_memories_status;
ALTER TABLE webhook_deliveries DROP CONSTRAINT IF EXISTS chk_webhook_deliveries_status;
ALTER TABLE integration_sync_runs DROP CONSTRAINT IF EXISTS chk_integration_sync_runs_status;
ALTER TABLE org_credentials DROP CONSTRAINT IF EXISTS chk_org_credentials_status;
ALTER TABLE user_credentials DROP CONSTRAINT IF EXISTS chk_user_credentials_status;

ALTER TABLE nodes
    DROP CONSTRAINT IF EXISTS chk_nodes_status,
    DROP CONSTRAINT IF EXISTS chk_nodes_mode;

ALTER TABLE pm_plans DROP CONSTRAINT IF EXISTS chk_pm_plans_status;

ALTER TABLE pm_documents
    DROP CONSTRAINT IF EXISTS chk_pm_documents_doc_type,
    DROP CONSTRAINT IF EXISTS chk_pm_documents_source_type;

ALTER TABLE project_attachments
    DROP CONSTRAINT IF EXISTS chk_project_attachments_file_type,
    DROP CONSTRAINT IF EXISTS chk_project_attachments_category;

ALTER TABLE project_specs DROP CONSTRAINT IF EXISTS chk_project_specs_spec_type;
ALTER TABLE deploys DROP CONSTRAINT IF EXISTS chk_deploys_environment;
