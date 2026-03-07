-- Projects: persistent, goal-oriented containers spanning multiple PM cycles.
CREATE TABLE projects (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID NOT NULL REFERENCES organizations(id),
    repository_id       UUID NOT NULL REFERENCES repositories(id),

    -- User-defined
    title               TEXT NOT NULL,
    goal                TEXT NOT NULL,
    scope               TEXT,
    completion_criteria TEXT,

    -- Lifecycle
    status              TEXT NOT NULL DEFAULT 'draft',
    priority            INT NOT NULL DEFAULT 50,

    -- Execution config
    execution_mode      TEXT NOT NULL DEFAULT 'sequential',
    max_concurrent      INT NOT NULL DEFAULT 2,
    auto_merge          BOOLEAN NOT NULL DEFAULT false,
    base_branch         TEXT NOT NULL DEFAULT 'main',

    -- PM memory
    current_phase       TEXT,
    lessons_learned     JSONB DEFAULT '[]',
    approach_history    JSONB DEFAULT '[]',

    -- Progress (denormalized)
    total_tasks         INT NOT NULL DEFAULT 0,
    completed_tasks     INT NOT NULL DEFAULT 0,
    failed_tasks        INT NOT NULL DEFAULT 0,

    -- Provenance (PM-proposed projects)
    proposed_by_pm      BOOLEAN NOT NULL DEFAULT false,
    source_issue_ids    UUID[],
    proposal_reasoning  TEXT,

    -- Ownership
    created_by          UUID REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ
);

CREATE INDEX idx_projects_org_status ON projects(org_id, status);
CREATE INDEX idx_projects_org_priority ON projects(org_id, priority);

-- Project tasks: created incrementally by the PM each cycle.
CREATE TABLE project_tasks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id),
    org_id          UUID NOT NULL REFERENCES organizations(id),

    -- Task definition
    title           TEXT NOT NULL,
    description     TEXT,
    approach        TEXT,
    reasoning       TEXT,

    -- Ordering and dependencies
    sort_order      INT NOT NULL DEFAULT 0,
    depends_on      UUID[],
    batch_number    INT NOT NULL,

    -- Status
    status          TEXT NOT NULL DEFAULT 'pending',
    complexity      TEXT,
    confidence      TEXT,

    -- Execution links
    agent_run_id    UUID REFERENCES agent_runs(id),
    issue_id        UUID REFERENCES issues(id),
    branch_name     TEXT,
    pr_url          TEXT,

    -- PM reflection
    outcome_notes   TEXT,
    retry_count     INT NOT NULL DEFAULT 0,
    max_retries     INT NOT NULL DEFAULT 2,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX idx_project_tasks_project ON project_tasks(project_id, sort_order);
CREATE INDEX idx_project_tasks_status ON project_tasks(project_id, status);
CREATE INDEX idx_project_tasks_batch ON project_tasks(project_id, batch_number);

-- Project cycles: audit trail of each PM planning cycle for a project.
CREATE TABLE project_cycles (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id),
    org_id          UUID NOT NULL REFERENCES organizations(id),
    pm_plan_id      UUID REFERENCES pm_plans(id),

    cycle_number    INT NOT NULL,
    analysis        TEXT NOT NULL,
    decisions       JSONB NOT NULL,
    progress_pct    INT,

    tasks_completed_this_cycle  INT NOT NULL DEFAULT 0,
    tasks_failed_this_cycle     INT NOT NULL DEFAULT 0,
    tasks_created_this_cycle    INT NOT NULL DEFAULT 0,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_project_cycles_project ON project_cycles(project_id, cycle_number);

-- Link agent runs to project tasks.
ALTER TABLE agent_runs ADD COLUMN project_task_id UUID REFERENCES project_tasks(id);
