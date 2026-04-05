-- Schema hardening: missing indexes, CHECK constraints, and redundant index cleanup.

-- =============================================================================
-- Issue 8: Missing indexes on FK columns
-- =============================================================================

-- sessions.repository_id (added in migration 28 with no index)
CREATE INDEX idx_sessions_repository_id ON sessions (repository_id) WHERE repository_id IS NOT NULL;

-- sessions.project_task_id (added in migration 9 with no index)
CREATE INDEX idx_sessions_project_task_id ON sessions (project_task_id) WHERE project_task_id IS NOT NULL;

-- sessions.pm_plan_id (added in migration 8 with no index)
CREATE INDEX idx_sessions_pm_plan_id ON sessions (pm_plan_id) WHERE pm_plan_id IS NOT NULL;

-- project_tasks.session_id (FK column with no index, needed for "which session runs this task?")
CREATE INDEX idx_project_tasks_session_id ON project_tasks (session_id) WHERE session_id IS NOT NULL;

-- issue_events compound index for org-scoped event queries after org_id addition in migration 35
CREATE INDEX idx_issue_events_org_issue ON issue_events (org_id, issue_id, occurred_at DESC);

-- =============================================================================
-- Issue 9: Remove redundant index on auth_sessions.token
-- The UNIQUE constraint on auth_sessions.token already creates an implicit index.
-- idx_auth_sessions_token is a duplicate that wastes storage and write amplification.
-- =============================================================================
DROP INDEX IF EXISTS idx_auth_sessions_token;

-- =============================================================================
-- Issue 12: Missing CHECK constraints on remaining enum columns
-- =============================================================================

-- issue_events.event_type
ALTER TABLE issue_events
    ADD CONSTRAINT chk_issue_events_event_type CHECK (event_type IN (
        'occurrence', 'comment', 'status_change', 'assignment'
    ));

-- session_messages.role
ALTER TABLE session_messages
    ADD CONSTRAINT chk_session_messages_role CHECK (role IN (
        'user', 'assistant', 'system'
    ));

-- complexity_estimates.tier (should be 1-5)
ALTER TABLE complexity_estimates
    ADD CONSTRAINT chk_complexity_estimates_tier CHECK (tier >= 1 AND tier <= 5);

-- complexity_estimates.label
ALTER TABLE complexity_estimates
    ADD CONSTRAINT chk_complexity_estimates_label CHECK (label IN (
        'trivial', 'simple', 'moderate', 'complex', 'very_complex'
    ));

-- review_comments.category (nullable, only checked when not null)
ALTER TABLE review_comments
    ADD CONSTRAINT chk_review_comments_category CHECK (category IS NULL OR category IN (
        'style', 'logic_bug', 'edge_case', 'wrong_approach',
        'missing_test', 'security', 'performance', 'nit'
    ));

-- session_review_comments.diff_side
ALTER TABLE session_review_comments
    ADD CONSTRAINT chk_session_review_comments_diff_side CHECK (diff_side IN ('old', 'new'));

-- project_cycles.cycle_number must be positive
ALTER TABLE project_cycles
    ADD CONSTRAINT chk_project_cycles_cycle_number CHECK (cycle_number > 0);

-- =============================================================================
-- Issue 17: Org-scoped job dequeue index for fair scheduling
-- =============================================================================
CREATE INDEX idx_jobs_org_dequeue ON jobs (org_id, queue, status, run_at, priority DESC);
