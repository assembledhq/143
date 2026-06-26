ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS pm_plan_id UUID REFERENCES pm_plans(id),
    ADD COLUMN IF NOT EXISTS pm_approach TEXT,
    ADD COLUMN IF NOT EXISTS pm_reasoning TEXT,
    ADD COLUMN IF NOT EXISTS project_task_id UUID REFERENCES project_tasks(id),
    ADD COLUMN IF NOT EXISTS automation_run_id UUID REFERENCES automation_runs(id);

UPDATE sessions s
SET pm_plan_id = spm.pm_plan_id,
    pm_approach = spm.pm_approach,
    pm_reasoning = spm.pm_reasoning,
    project_task_id = spm.project_task_id
FROM session_pm_context spm
WHERE spm.org_id = s.org_id
  AND spm.session_id = s.id;

UPDATE sessions s
SET automation_run_id = sal.automation_run_id
FROM session_automation_links sal
WHERE sal.org_id = s.org_id
  AND sal.session_id = s.id;

CREATE INDEX IF NOT EXISTS idx_sessions_pm_plan_id
    ON sessions (pm_plan_id)
    WHERE pm_plan_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_sessions_project_task_id
    ON sessions (project_task_id)
    WHERE project_task_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_sessions_automation_run
    ON sessions (automation_run_id)
    WHERE automation_run_id IS NOT NULL;

DROP TABLE IF EXISTS session_automation_links;
DROP TABLE IF EXISTS session_pm_context;
