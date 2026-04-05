-- Revert ON DELETE changes: restore original FK behavior.

-- =============================================================================
-- Revert CASCADE → RESTRICT (restore original RESTRICT)
-- =============================================================================

ALTER TABLE session_questions DROP CONSTRAINT IF EXISTS session_questions_session_id_fkey;
ALTER TABLE session_questions
    ADD CONSTRAINT session_questions_session_id_fkey
    FOREIGN KEY (session_id) REFERENCES sessions(id);

ALTER TABLE validations DROP CONSTRAINT IF EXISTS validations_session_id_fkey;
ALTER TABLE validations
    ADD CONSTRAINT validations_session_id_fkey
    FOREIGN KEY (session_id) REFERENCES sessions(id);

ALTER TABLE project_tasks DROP CONSTRAINT IF EXISTS project_tasks_project_id_fkey;
ALTER TABLE project_tasks
    ADD CONSTRAINT project_tasks_project_id_fkey
    FOREIGN KEY (project_id) REFERENCES projects(id);

ALTER TABLE project_cycles DROP CONSTRAINT IF EXISTS project_cycles_project_id_fkey;
ALTER TABLE project_cycles
    ADD CONSTRAINT project_cycles_project_id_fkey
    FOREIGN KEY (project_id) REFERENCES projects(id);

ALTER TABLE issue_events DROP CONSTRAINT IF EXISTS issue_events_issue_id_fkey;
ALTER TABLE issue_events
    ADD CONSTRAINT issue_events_issue_id_fkey
    FOREIGN KEY (issue_id) REFERENCES issues(id);

ALTER TABLE pm_decision_log DROP CONSTRAINT IF EXISTS pm_decision_log_plan_id_fkey;
ALTER TABLE pm_decision_log
    ADD CONSTRAINT pm_decision_log_plan_id_fkey
    FOREIGN KEY (plan_id) REFERENCES pm_plans(id);

ALTER TABLE deploys DROP CONSTRAINT IF EXISTS deploys_pull_request_id_fkey;
ALTER TABLE deploys
    ADD CONSTRAINT deploys_pull_request_id_fkey
    FOREIGN KEY (pull_request_id) REFERENCES pull_requests(id);

ALTER TABLE review_comments DROP CONSTRAINT IF EXISTS review_comments_pull_request_id_fkey;
ALTER TABLE review_comments
    ADD CONSTRAINT review_comments_pull_request_id_fkey
    FOREIGN KEY (pull_request_id) REFERENCES pull_requests(id);

ALTER TABLE priority_scores DROP CONSTRAINT IF EXISTS priority_scores_issue_id_fkey;
ALTER TABLE priority_scores
    ADD CONSTRAINT priority_scores_issue_id_fkey
    FOREIGN KEY (issue_id) REFERENCES issues(id);

ALTER TABLE complexity_estimates DROP CONSTRAINT IF EXISTS complexity_estimates_issue_id_fkey;
ALTER TABLE complexity_estimates
    ADD CONSTRAINT complexity_estimates_issue_id_fkey
    FOREIGN KEY (issue_id) REFERENCES issues(id);

-- =============================================================================
-- Revert RESTRICT → CASCADE (restore original CASCADE)
-- =============================================================================

ALTER TABLE pm_documents DROP CONSTRAINT IF EXISTS pm_documents_org_id_fkey;
ALTER TABLE pm_documents
    ADD CONSTRAINT pm_documents_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE CASCADE;

ALTER TABLE repository_pr_templates DROP CONSTRAINT IF EXISTS repository_pr_templates_org_id_fkey;
ALTER TABLE repository_pr_templates
    ADD CONSTRAINT repository_pr_templates_org_id_fkey
    FOREIGN KEY (org_id) REFERENCES organizations(id) ON DELETE CASCADE;
