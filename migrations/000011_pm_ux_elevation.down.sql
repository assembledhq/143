DROP INDEX IF EXISTS idx_issues_project;
ALTER TABLE issues DROP COLUMN IF EXISTS project_id;

ALTER TABLE pm_plans
    DROP COLUMN IF EXISTS in_flight_runs_checked,
    DROP COLUMN IF EXISTS past_outcomes_reviewed,
    DROP COLUMN IF EXISTS recent_prs_checked,
    DROP COLUMN IF EXISTS past_decisions_reviewed,
    DROP COLUMN IF EXISTS commits_analyzed;
