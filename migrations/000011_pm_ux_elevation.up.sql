-- PM UX Elevation: context counts on pm_plans and project_id on issues.

-- Context counts track what the PM agent considered during each analysis cycle.
ALTER TABLE pm_plans
    ADD COLUMN in_flight_runs_checked   INT NOT NULL DEFAULT 0,
    ADD COLUMN past_outcomes_reviewed   INT NOT NULL DEFAULT 0,
    ADD COLUMN recent_prs_checked       INT NOT NULL DEFAULT 0,
    ADD COLUMN past_decisions_reviewed  INT NOT NULL DEFAULT 0,
    ADD COLUMN commits_analyzed         INT NOT NULL DEFAULT 0;

-- Allow issues to optionally belong to a project.
ALTER TABLE issues ADD COLUMN project_id UUID REFERENCES projects(id);
CREATE INDEX idx_issues_project ON issues(project_id) WHERE project_id IS NOT NULL;
