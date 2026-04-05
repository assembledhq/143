-- Add CHECK constraints to all text-based enum columns.
-- These enforce valid values at the database level, preventing garbage data
-- from application bugs.
--
-- NOTE: Adding a new enum value to any column protected by a CHECK constraint
-- requires a new migration to ALTER the constraint (DROP + re-ADD). Plan for
-- this when extending status/type enums in application code.

-- =============================================================================
-- users
-- =============================================================================
ALTER TABLE users
    ADD CONSTRAINT chk_users_role CHECK (role IN ('admin', 'member', 'viewer'));

-- =============================================================================
-- sessions
-- =============================================================================
ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_status CHECK (status IN (
        'pending', 'running', 'idle', 'awaiting_input', 'needs_human_guidance',
        'completed', 'pr_created', 'failed', 'cancelled', 'skipped'
    )),
    ADD CONSTRAINT chk_sessions_sandbox_state CHECK (sandbox_state IN (
        'none', 'running', 'snapshotted', 'destroyed'
    )),
    ADD CONSTRAINT chk_sessions_autonomy_level CHECK (autonomy_level IN (
        'full', 'semi', 'supervised'
    )),
    ADD CONSTRAINT chk_sessions_token_mode CHECK (token_mode IN (
        'low', 'high'
    )),
    ADD CONSTRAINT chk_sessions_agent_type CHECK (agent_type IN (
        'claude_code', 'gemini_cli', 'codex', 'pm_agent'
    ));

-- =============================================================================
-- session_threads
-- =============================================================================
ALTER TABLE session_threads
    ADD CONSTRAINT chk_session_threads_status CHECK (status IN (
        'pending', 'running', 'idle', 'awaiting_input',
        'completed', 'failed', 'cancelled'
    ));

-- =============================================================================
-- session_questions
-- =============================================================================
ALTER TABLE session_questions
    ADD CONSTRAINT chk_session_questions_status CHECK (status IN (
        'pending', 'answered', 'timed_out', 'skipped'
    ));

-- =============================================================================
-- session_logs
-- Note: CHECK constraint for session_logs.level is added in migration 36
-- (partitioning) since that migration recreates the table.
-- =============================================================================

-- =============================================================================
-- jobs
-- =============================================================================
ALTER TABLE jobs
    ADD CONSTRAINT chk_jobs_status CHECK (status IN (
        'pending', 'running', 'succeeded', 'failed', 'cancelled', 'dead_letter'
    ));

-- =============================================================================
-- projects
-- =============================================================================
ALTER TABLE projects
    ADD CONSTRAINT chk_projects_status CHECK (status IN (
        'proposed', 'draft', 'planning', 'active', 'paused', 'completed', 'cancelled'
    )),
    ADD CONSTRAINT chk_projects_execution_mode CHECK (execution_mode IN (
        'sequential', 'parallel', 'dependency_graph'
    )),
    ADD CONSTRAINT chk_projects_schedule_unit CHECK (schedule_unit IN (
        'hours', 'days', 'weeks'
    ));

-- =============================================================================
-- project_tasks
-- =============================================================================
ALTER TABLE project_tasks
    ADD CONSTRAINT chk_project_tasks_status CHECK (status IN (
        'pending', 'blocked', 'delegated', 'running',
        'completed', 'failed', 'skipped', 'cancelled'
    ));

-- =============================================================================
-- integrations
-- =============================================================================
ALTER TABLE integrations
    ADD CONSTRAINT chk_integrations_status CHECK (status IN (
        'active', 'inactive', 'error'
    )),
    ADD CONSTRAINT chk_integrations_provider CHECK (provider IN (
        'github', 'sentry', 'linear', 'slack', 'notion'
    ));

-- =============================================================================
-- repositories
-- =============================================================================
ALTER TABLE repositories
    ADD CONSTRAINT chk_repositories_status CHECK (status IN (
        'active', 'paused', 'disconnected'
    ));

-- =============================================================================
-- issues
-- =============================================================================
ALTER TABLE issues
    ADD CONSTRAINT chk_issues_status CHECK (status IN (
        'open', 'triaged', 'in_progress', 'fixed', 'wont_fix', 'duplicate'
    )),
    ADD CONSTRAINT chk_issues_severity CHECK (severity IN (
        'critical', 'high', 'medium', 'low'
    )),
    ADD CONSTRAINT chk_issues_source CHECK (source IN (
        'sentry', 'linear', 'manual', 'pm_agent'
    ));

-- =============================================================================
-- pull_requests
-- =============================================================================
ALTER TABLE pull_requests
    ADD CONSTRAINT chk_pull_requests_status CHECK (status IN (
        'open', 'merged', 'closed'
    )),
    ADD CONSTRAINT chk_pull_requests_review_status CHECK (review_status IN (
        'pending', 'approved', 'changes_requested'
    ));

-- =============================================================================
-- validations
-- =============================================================================
ALTER TABLE validations
    ADD CONSTRAINT chk_validations_status CHECK (status IN (
        'pending', 'running', 'passed', 'failed'
    ));

-- =============================================================================
-- invitations
-- =============================================================================
ALTER TABLE invitations
    ADD CONSTRAINT chk_invitations_status CHECK (status IN (
        'pending', 'accepted', 'revoked'
    ));

-- =============================================================================
-- review_comments
-- =============================================================================
ALTER TABLE review_comments
    ADD CONSTRAINT chk_review_comments_filter_status CHECK (filter_status IN (
        'pending', 'filtered_structural', 'filtered_not_actionable', 'accepted'
    ));

-- =============================================================================
-- memories
-- =============================================================================
ALTER TABLE memories
    ADD CONSTRAINT chk_memories_status CHECK (status IN (
        'candidate', 'active', 'dismissed'
    ));

-- =============================================================================
-- webhook_deliveries
-- =============================================================================
ALTER TABLE webhook_deliveries
    ADD CONSTRAINT chk_webhook_deliveries_status CHECK (status IN (
        'received', 'processed', 'failed', 'ignored'
    ));

-- =============================================================================
-- integration_sync_runs
-- =============================================================================
ALTER TABLE integration_sync_runs
    ADD CONSTRAINT chk_integration_sync_runs_status CHECK (status IN (
        'running', 'success', 'partial', 'failed'
    ));

-- =============================================================================
-- org_credentials / user_credentials
-- =============================================================================
ALTER TABLE org_credentials
    ADD CONSTRAINT chk_org_credentials_status CHECK (status IN (
        'active', 'disabled'
    ));

ALTER TABLE user_credentials
    ADD CONSTRAINT chk_user_credentials_status CHECK (status IN (
        'active', 'disabled'
    ));

-- =============================================================================
-- nodes
-- =============================================================================
ALTER TABLE nodes
    ADD CONSTRAINT chk_nodes_status CHECK (status IN (
        'active', 'draining', 'dead'
    )),
    ADD CONSTRAINT chk_nodes_mode CHECK (mode IN (
        'all', 'api', 'worker'
    ));

-- =============================================================================
-- pm_plans
-- =============================================================================
ALTER TABLE pm_plans
    ADD CONSTRAINT chk_pm_plans_status CHECK (status IN (
        'executing', 'completed', 'failed'
    ));

-- =============================================================================
-- pm_documents
-- =============================================================================
ALTER TABLE pm_documents
    ADD CONSTRAINT chk_pm_documents_doc_type CHECK (doc_type IN (
        'roadmap', 'context'
    )),
    ADD CONSTRAINT chk_pm_documents_source_type CHECK (source_type IN (
        'manual', 'url', 'notion', 'google_docs', 'confluence',
        'file_upload', 'autogenerated', 'refresh'
    ));

-- =============================================================================
-- project_attachments
-- =============================================================================
ALTER TABLE project_attachments
    ADD CONSTRAINT chk_project_attachments_file_type CHECK (file_type IN (
        'image', 'design', 'document'
    )),
    ADD CONSTRAINT chk_project_attachments_category CHECK (category IN (
        'screenshot', 'mockup', 'wireframe', 'reference'
    ));

-- =============================================================================
-- project_specs
-- =============================================================================
ALTER TABLE project_specs
    ADD CONSTRAINT chk_project_specs_spec_type CHECK (spec_type IN (
        'prd', 'technical', 'design', 'user_story'
    ));

-- =============================================================================
-- deploys
-- =============================================================================
ALTER TABLE deploys
    ADD CONSTRAINT chk_deploys_environment CHECK (environment IN (
        'production', 'staging'
    ));
