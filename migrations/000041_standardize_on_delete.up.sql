-- Standardize ON DELETE behavior across all foreign keys.
--
-- Policy:
--   CASCADE  — owned content that has no meaning without its parent
--   RESTRICT — cross-domain references where the child has independent value
--
-- org_id FKs are always RESTRICT: deleting an org should be a deliberate,
-- multi-step process, never an accidental cascade.
--
-- To change ON DELETE behavior, PostgreSQL requires dropping and recreating
-- the constraint. We use NOT VALID + VALIDATE to avoid full table scans on
-- large tables during the ADD step.

-- =============================================================================
-- ADD CASCADE: owned content that currently defaults to RESTRICT
-- =============================================================================

-- session_questions are meaningless without their session.
ALTER TABLE session_questions DROP CONSTRAINT IF EXISTS agent_run_questions_agent_run_id_fkey;
ALTER TABLE session_questions DROP CONSTRAINT IF EXISTS session_questions_session_id_fkey;
ALTER TABLE session_questions
    ADD CONSTRAINT session_questions_session_id_fkey
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE session_questions VALIDATE CONSTRAINT session_questions_session_id_fkey;

-- Validations are meaningless without their session.
ALTER TABLE validations DROP CONSTRAINT IF EXISTS validations_agent_run_id_fkey;
ALTER TABLE validations DROP CONSTRAINT IF EXISTS validations_session_id_fkey;
ALTER TABLE validations
    ADD CONSTRAINT validations_session_id_fkey
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE validations VALIDATE CONSTRAINT validations_session_id_fkey;

-- Project tasks are owned by their project.
ALTER TABLE project_tasks DROP CONSTRAINT IF EXISTS project_tasks_project_id_fkey;
ALTER TABLE project_tasks
    ADD CONSTRAINT project_tasks_project_id_fkey
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE project_tasks VALIDATE CONSTRAINT project_tasks_project_id_fkey;

-- Project cycles are owned by their project.
ALTER TABLE project_cycles DROP CONSTRAINT IF EXISTS project_cycles_project_id_fkey;
ALTER TABLE project_cycles
    ADD CONSTRAINT project_cycles_project_id_fkey
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE project_cycles VALIDATE CONSTRAINT project_cycles_project_id_fkey;

-- Issue events are owned by their issue.
ALTER TABLE issue_events DROP CONSTRAINT IF EXISTS issue_events_issue_id_fkey;
ALTER TABLE issue_events
    ADD CONSTRAINT issue_events_issue_id_fkey
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE issue_events VALIDATE CONSTRAINT issue_events_issue_id_fkey;

-- PM decision log entries are owned by their plan.
ALTER TABLE pm_decision_log DROP CONSTRAINT IF EXISTS pm_decision_log_plan_id_fkey;
ALTER TABLE pm_decision_log
    ADD CONSTRAINT pm_decision_log_plan_id_fkey
    FOREIGN KEY (plan_id) REFERENCES pm_plans(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE pm_decision_log VALIDATE CONSTRAINT pm_decision_log_plan_id_fkey;

-- Deploys are owned by their pull request.
ALTER TABLE deploys DROP CONSTRAINT IF EXISTS deploys_pull_request_id_fkey;
ALTER TABLE deploys
    ADD CONSTRAINT deploys_pull_request_id_fkey
    FOREIGN KEY (pull_request_id) REFERENCES pull_requests(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE deploys VALIDATE CONSTRAINT deploys_pull_request_id_fkey;

-- Review comments are owned by their pull request.
ALTER TABLE review_comments DROP CONSTRAINT IF EXISTS review_comments_pull_request_id_fkey;
ALTER TABLE review_comments
    ADD CONSTRAINT review_comments_pull_request_id_fkey
    FOREIGN KEY (pull_request_id) REFERENCES pull_requests(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE review_comments VALIDATE CONSTRAINT review_comments_pull_request_id_fkey;

-- Priority scores are derived data owned by their issue.
ALTER TABLE priority_scores DROP CONSTRAINT IF EXISTS priority_scores_issue_id_fkey;
ALTER TABLE priority_scores
    ADD CONSTRAINT priority_scores_issue_id_fkey
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE priority_scores VALIDATE CONSTRAINT priority_scores_issue_id_fkey;

-- Complexity estimates are derived data owned by their issue.
ALTER TABLE complexity_estimates DROP CONSTRAINT IF EXISTS complexity_estimates_issue_id_fkey;
ALTER TABLE complexity_estimates
    ADD CONSTRAINT complexity_estimates_issue_id_fkey
    FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE complexity_estimates VALIDATE CONSTRAINT complexity_estimates_issue_id_fkey;

-- =============================================================================
-- REMOVE CASCADE: org_id FKs that should be RESTRICT
-- =============================================================================

-- pm_documents.org_id should not cascade on org deletion.
ALTER TABLE pm_documents DROP CONSTRAINT IF EXISTS pm_documents_org_id_fkey;
ALTER TABLE pm_documents
    ADD CONSTRAINT pm_documents_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(id) NOT VALID;
ALTER TABLE pm_documents VALIDATE CONSTRAINT pm_documents_org_id_fkey;

-- repository_pr_templates.org_id should not cascade on org deletion.
ALTER TABLE repository_pr_templates DROP CONSTRAINT IF EXISTS repository_pr_templates_org_id_fkey;
ALTER TABLE repository_pr_templates
    ADD CONSTRAINT repository_pr_templates_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(id) NOT VALID;
ALTER TABLE repository_pr_templates VALIDATE CONSTRAINT repository_pr_templates_org_id_fkey;
