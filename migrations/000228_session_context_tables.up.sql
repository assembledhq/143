CREATE TABLE session_pm_context (
    session_id      UUID PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    org_id          UUID NOT NULL REFERENCES organizations(id),
    pm_plan_id      UUID REFERENCES pm_plans(id),
    pm_approach     TEXT,
    pm_reasoning    TEXT,
    project_task_id UUID REFERENCES project_tasks(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_session_pm_context_has_data CHECK (
        pm_plan_id IS NOT NULL
        OR pm_approach IS NOT NULL
        OR pm_reasoning IS NOT NULL
        OR project_task_id IS NOT NULL
    )
);

CREATE UNIQUE INDEX idx_session_pm_context_org_session
    ON session_pm_context (org_id, session_id);

CREATE INDEX idx_session_pm_context_pm_plan
    ON session_pm_context (org_id, pm_plan_id)
    WHERE pm_plan_id IS NOT NULL;

CREATE INDEX idx_session_pm_context_project_task
    ON session_pm_context (org_id, project_task_id)
    WHERE project_task_id IS NOT NULL;

INSERT INTO session_pm_context (
    session_id, org_id, pm_plan_id, pm_approach, pm_reasoning, project_task_id, created_at, updated_at
)
SELECT
    id, org_id, pm_plan_id, pm_approach, pm_reasoning, project_task_id, created_at, created_at
FROM sessions
WHERE pm_plan_id IS NOT NULL
   OR pm_approach IS NOT NULL
   OR pm_reasoning IS NOT NULL
   OR project_task_id IS NOT NULL;

CREATE TABLE session_automation_links (
    session_id        UUID PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    org_id            UUID NOT NULL REFERENCES organizations(id),
    automation_run_id UUID NOT NULL REFERENCES automation_runs(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_session_automation_links_org_session
    ON session_automation_links (org_id, session_id);

CREATE INDEX idx_session_automation_links_run
    ON session_automation_links (org_id, automation_run_id, created_at DESC);

INSERT INTO session_automation_links (session_id, org_id, automation_run_id, created_at)
SELECT id, org_id, automation_run_id, created_at
FROM sessions
WHERE automation_run_id IS NOT NULL;

DROP INDEX IF EXISTS idx_sessions_pm_plan_id;
DROP INDEX IF EXISTS idx_sessions_project_task_id;
DROP INDEX IF EXISTS idx_sessions_automation_run;

ALTER TABLE sessions
    DROP COLUMN IF EXISTS pm_plan_id,
    DROP COLUMN IF EXISTS pm_approach,
    DROP COLUMN IF EXISTS pm_reasoning,
    DROP COLUMN IF EXISTS project_task_id,
    DROP COLUMN IF EXISTS automation_run_id;
