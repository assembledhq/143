-- Add CHECK constraints to all text-based enum columns.
-- These enforce valid values at the database level, preventing garbage data
-- from application bugs.
--
-- Uses NOT VALID + VALIDATE CONSTRAINT to avoid holding ACCESS EXCLUSIVE lock
-- while scanning the entire table. NOT VALID adds the constraint for future
-- writes immediately, then VALIDATE scans existing rows with a weaker lock.
--
-- NOTE: Adding a new enum value to any column protected by a CHECK constraint
-- requires a new migration to ALTER the constraint (DROP + re-ADD). Plan for
-- this when extending status/type enums in application code.

-- =============================================================================
-- users
-- =============================================================================
ALTER TABLE users
    ADD CONSTRAINT chk_users_role CHECK (role IN ('admin', 'member', 'viewer')) NOT VALID;
ALTER TABLE users VALIDATE CONSTRAINT chk_users_role;

-- =============================================================================
-- sessions
-- =============================================================================
ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_status CHECK (status IN (
        'pending', 'running', 'idle', 'awaiting_input', 'needs_human_guidance',
        'completed', 'pr_created', 'failed', 'cancelled', 'skipped'
    )) NOT VALID,
    ADD CONSTRAINT chk_sessions_sandbox_state CHECK (sandbox_state IN (
        'none', 'running', 'snapshotted', 'destroyed'
    )) NOT VALID,
    ADD CONSTRAINT chk_sessions_autonomy_level CHECK (autonomy_level IN (
        'full', 'semi', 'supervised'
    )) NOT VALID,
    ADD CONSTRAINT chk_sessions_token_mode CHECK (token_mode IN (
        'low', 'high'
    )) NOT VALID,
    ADD CONSTRAINT chk_sessions_agent_type CHECK (agent_type IN (
        'claude_code', 'gemini_cli', 'codex', 'pm_agent'
    )) NOT VALID;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_status;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_sandbox_state;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_autonomy_level;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_token_mode;
ALTER TABLE sessions VALIDATE CONSTRAINT chk_sessions_agent_type;

-- =============================================================================
-- session_threads
-- =============================================================================
ALTER TABLE session_threads
    ADD CONSTRAINT chk_session_threads_status CHECK (status IN (
        'pending', 'running', 'idle', 'awaiting_input',
        'completed', 'failed', 'cancelled'
    )) NOT VALID;
ALTER TABLE session_threads VALIDATE CONSTRAINT chk_session_threads_status;

-- =============================================================================
-- session_questions
-- =============================================================================
ALTER TABLE session_questions
    ADD CONSTRAINT chk_session_questions_status CHECK (status IN (
        'pending', 'answered', 'timed_out', 'skipped'
    )) NOT VALID;
ALTER TABLE session_questions VALIDATE CONSTRAINT chk_session_questions_status;

-- =============================================================================
-- session_logs
-- Note: CHECK constraint for session_logs.level is added in migration 037
-- (partitioning) since that migration recreates the table.
-- =============================================================================

-- =============================================================================
-- jobs
-- =============================================================================
ALTER TABLE jobs
    ADD CONSTRAINT chk_jobs_status CHECK (status IN (
        'pending', 'running', 'succeeded', 'failed', 'cancelled', 'dead_letter'
    )) NOT VALID;
ALTER TABLE jobs VALIDATE CONSTRAINT chk_jobs_status;

-- =============================================================================
-- projects
-- =============================================================================
ALTER TABLE projects
    ADD CONSTRAINT chk_projects_status CHECK (status IN (
        'proposed', 'draft', 'planning', 'active', 'paused', 'completed', 'cancelled'
    )) NOT VALID,
    ADD CONSTRAINT chk_projects_execution_mode CHECK (execution_mode IN (
        'sequential', 'parallel', 'dependency_graph'
    )) NOT VALID,
    ADD CONSTRAINT chk_projects_schedule_unit CHECK (schedule_unit IN (
        'hours', 'days', 'weeks'
    )) NOT VALID;
ALTER TABLE projects VALIDATE CONSTRAINT chk_projects_status;
ALTER TABLE projects VALIDATE CONSTRAINT chk_projects_execution_mode;
ALTER TABLE projects VALIDATE CONSTRAINT chk_projects_schedule_unit;

-- =============================================================================
-- project_tasks
-- =============================================================================
ALTER TABLE project_tasks
    ADD CONSTRAINT chk_project_tasks_status CHECK (status IN (
        'pending', 'blocked', 'delegated', 'running',
        'completed', 'failed', 'skipped', 'cancelled'
    )) NOT VALID;
ALTER TABLE project_tasks VALIDATE CONSTRAINT chk_project_tasks_status;

-- =============================================================================
-- integrations
-- =============================================================================
ALTER TABLE integrations
    ADD CONSTRAINT chk_integrations_status CHECK (status IN (
        'active', 'inactive', 'error'
    )) NOT VALID,
    ADD CONSTRAINT chk_integrations_provider CHECK (provider IN (
        'github', 'sentry', 'linear', 'slack', 'notion'
    )) NOT VALID;
ALTER TABLE integrations VALIDATE CONSTRAINT chk_integrations_status;
ALTER TABLE integrations VALIDATE CONSTRAINT chk_integrations_provider;

-- =============================================================================
-- repositories
-- =============================================================================
ALTER TABLE repositories
    ADD CONSTRAINT chk_repositories_status CHECK (status IN (
        'active', 'paused', 'disconnected'
    )) NOT VALID;
ALTER TABLE repositories VALIDATE CONSTRAINT chk_repositories_status;

-- =============================================================================
-- issues
-- =============================================================================
ALTER TABLE issues
    ADD CONSTRAINT chk_issues_status CHECK (status IN (
        'open', 'triaged', 'in_progress', 'fixed', 'wont_fix', 'duplicate'
    )) NOT VALID,
    ADD CONSTRAINT chk_issues_severity CHECK (severity IN (
        'critical', 'high', 'medium', 'low'
    )) NOT VALID,
    ADD CONSTRAINT chk_issues_source CHECK (source IN (
        'sentry', 'linear', 'manual', 'pm_agent'
    )) NOT VALID;
ALTER TABLE issues VALIDATE CONSTRAINT chk_issues_status;
ALTER TABLE issues VALIDATE CONSTRAINT chk_issues_severity;
ALTER TABLE issues VALIDATE CONSTRAINT chk_issues_source;

-- =============================================================================
-- pull_requests
-- =============================================================================
ALTER TABLE pull_requests
    ADD CONSTRAINT chk_pull_requests_status CHECK (status IN (
        'open', 'merged', 'closed'
    )) NOT VALID,
    ADD CONSTRAINT chk_pull_requests_review_status CHECK (review_status IN (
        'pending', 'approved', 'changes_requested'
    )) NOT VALID;
ALTER TABLE pull_requests VALIDATE CONSTRAINT chk_pull_requests_status;
ALTER TABLE pull_requests VALIDATE CONSTRAINT chk_pull_requests_review_status;

-- =============================================================================
-- validations
-- =============================================================================
ALTER TABLE validations
    ADD CONSTRAINT chk_validations_status CHECK (status IN (
        'pending', 'running', 'passed', 'failed'
    )) NOT VALID;
ALTER TABLE validations VALIDATE CONSTRAINT chk_validations_status;

-- =============================================================================
-- invitations
-- =============================================================================
ALTER TABLE invitations
    ADD CONSTRAINT chk_invitations_status CHECK (status IN (
        'pending', 'accepted', 'revoked'
    )) NOT VALID;
ALTER TABLE invitations VALIDATE CONSTRAINT chk_invitations_status;

-- =============================================================================
-- review_comments
-- =============================================================================
ALTER TABLE review_comments
    ADD CONSTRAINT chk_review_comments_filter_status CHECK (filter_status IN (
        'pending', 'filtered_structural', 'filtered_not_actionable', 'accepted'
    )) NOT VALID;
ALTER TABLE review_comments VALIDATE CONSTRAINT chk_review_comments_filter_status;

-- =============================================================================
-- memories
-- =============================================================================
ALTER TABLE memories
    ADD CONSTRAINT chk_memories_status CHECK (status IN (
        'candidate', 'active', 'dismissed'
    )) NOT VALID;
ALTER TABLE memories VALIDATE CONSTRAINT chk_memories_status;

-- =============================================================================
-- webhook_deliveries
-- =============================================================================
ALTER TABLE webhook_deliveries
    ADD CONSTRAINT chk_webhook_deliveries_status CHECK (status IN (
        'received', 'processed', 'failed', 'ignored'
    )) NOT VALID;
ALTER TABLE webhook_deliveries VALIDATE CONSTRAINT chk_webhook_deliveries_status;

-- =============================================================================
-- integration_sync_runs
-- =============================================================================
ALTER TABLE integration_sync_runs
    ADD CONSTRAINT chk_integration_sync_runs_status CHECK (status IN (
        'running', 'success', 'partial', 'failed'
    )) NOT VALID;
ALTER TABLE integration_sync_runs VALIDATE CONSTRAINT chk_integration_sync_runs_status;

-- =============================================================================
-- org_credentials / user_credentials
-- =============================================================================
ALTER TABLE org_credentials
    ADD CONSTRAINT chk_org_credentials_status CHECK (status IN (
        'active', 'disabled'
    )) NOT VALID;
ALTER TABLE org_credentials VALIDATE CONSTRAINT chk_org_credentials_status;

ALTER TABLE user_credentials
    ADD CONSTRAINT chk_user_credentials_status CHECK (status IN (
        'active', 'disabled'
    )) NOT VALID;
ALTER TABLE user_credentials VALIDATE CONSTRAINT chk_user_credentials_status;

-- =============================================================================
-- nodes
-- =============================================================================
ALTER TABLE nodes
    ADD CONSTRAINT chk_nodes_status CHECK (status IN (
        'active', 'draining', 'dead'
    )) NOT VALID,
    ADD CONSTRAINT chk_nodes_mode CHECK (mode IN (
        'all', 'api', 'worker'
    )) NOT VALID;
ALTER TABLE nodes VALIDATE CONSTRAINT chk_nodes_status;
ALTER TABLE nodes VALIDATE CONSTRAINT chk_nodes_mode;

-- =============================================================================
-- pm_plans
-- =============================================================================
ALTER TABLE pm_plans
    ADD CONSTRAINT chk_pm_plans_status CHECK (status IN (
        'executing', 'completed', 'failed'
    )) NOT VALID;
ALTER TABLE pm_plans VALIDATE CONSTRAINT chk_pm_plans_status;

-- =============================================================================
-- pm_documents
-- =============================================================================
ALTER TABLE pm_documents
    ADD CONSTRAINT chk_pm_documents_doc_type CHECK (doc_type IN (
        'roadmap', 'context'
    )) NOT VALID,
    ADD CONSTRAINT chk_pm_documents_source_type CHECK (source_type IN (
        'manual', 'url', 'notion', 'google_docs', 'confluence',
        'file_upload', 'autogenerated', 'refresh'
    )) NOT VALID;
ALTER TABLE pm_documents VALIDATE CONSTRAINT chk_pm_documents_doc_type;
ALTER TABLE pm_documents VALIDATE CONSTRAINT chk_pm_documents_source_type;

-- =============================================================================
-- project_attachments
-- =============================================================================
ALTER TABLE project_attachments
    ADD CONSTRAINT chk_project_attachments_file_type CHECK (file_type IN (
        'image', 'design', 'document'
    )) NOT VALID,
    ADD CONSTRAINT chk_project_attachments_category CHECK (category IN (
        'screenshot', 'mockup', 'wireframe', 'reference'
    )) NOT VALID;
ALTER TABLE project_attachments VALIDATE CONSTRAINT chk_project_attachments_file_type;
ALTER TABLE project_attachments VALIDATE CONSTRAINT chk_project_attachments_category;

-- =============================================================================
-- project_specs
-- =============================================================================
ALTER TABLE project_specs
    ADD CONSTRAINT chk_project_specs_spec_type CHECK (spec_type IN (
        'prd', 'technical', 'design', 'user_story'
    )) NOT VALID;
ALTER TABLE project_specs VALIDATE CONSTRAINT chk_project_specs_spec_type;

-- =============================================================================
-- deploys
-- =============================================================================
ALTER TABLE deploys
    ADD CONSTRAINT chk_deploys_environment CHECK (environment IN (
        'production', 'staging'
    )) NOT VALID;
ALTER TABLE deploys VALIDATE CONSTRAINT chk_deploys_environment;
