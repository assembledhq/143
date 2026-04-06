-- Eval Tasks: reproducible challenges grounded in real codebase history.
CREATE TABLE eval_tasks (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                  UUID NOT NULL REFERENCES organizations(id),
    repo_id                 UUID NOT NULL REFERENCES repositories(id),
    name                    TEXT NOT NULL,
    description             TEXT NOT NULL DEFAULT '',

    -- Codebase snapshot
    base_commit_sha         TEXT NOT NULL,
    solution_commit_sha     TEXT,
    solution_diff           TEXT,

    -- Problem definition
    issue_description       TEXT NOT NULL,
    issue_context           JSONB NOT NULL DEFAULT '{}',

    -- Input configuration (frozen references, see doc 43)
    server_deploy_sha       TEXT,
    pm_document_set_pin_id  UUID,
    org_settings_version_id UUID,
    memory_snapshot         JSONB,
    sandbox_image_digest    TEXT,
    context_overrides       JSONB NOT NULL DEFAULT '{}',

    -- Scoring
    scoring_criteria        JSONB NOT NULL DEFAULT '[]',
    pass_threshold          DOUBLE PRECISION NOT NULL DEFAULT 0.7,

    -- Metadata
    source                  TEXT NOT NULL DEFAULT 'manual',
    source_pr_number        INT,
    complexity              TEXT NOT NULL DEFAULT 'moderate',
    tags                    TEXT[] NOT NULL DEFAULT '{}',
    created_by              UUID REFERENCES users(id),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at             TIMESTAMPTZ
);

CREATE INDEX idx_eval_tasks_org_id ON eval_tasks (org_id, created_at DESC) WHERE archived_at IS NULL;
CREATE INDEX idx_eval_tasks_org_archived ON eval_tasks (org_id, created_at DESC) WHERE archived_at IS NOT NULL;
CREATE INDEX idx_eval_tasks_repo_id ON eval_tasks (repo_id);
CREATE INDEX idx_eval_tasks_source ON eval_tasks (org_id, source) WHERE archived_at IS NULL;

-- Eval Batches: group multiple eval runs for comparison.
CREATE TABLE eval_batches (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL REFERENCES organizations(id),
    name        TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'pending',
    task_count  INT NOT NULL DEFAULT 0,
    run_count   INT NOT NULL DEFAULT 0,
    created_by  UUID REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX idx_eval_batches_org_id ON eval_batches (org_id, created_at DESC);

-- Eval Runs: a single execution of an eval task with specific configuration.
CREATE TABLE eval_runs (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id                 UUID NOT NULL REFERENCES eval_tasks(id),
    org_id                  UUID NOT NULL REFERENCES organizations(id),
    batch_id                UUID REFERENCES eval_batches(id),

    -- Configuration used
    input_manifest          JSONB,
    model                   TEXT NOT NULL,
    server_deploy_sha       TEXT,
    pm_document_set_pin_id  UUID,
    config_ref              TEXT,
    context_overrides       JSONB NOT NULL DEFAULT '{}',

    -- Output
    agent_diff              TEXT,
    agent_trace             JSONB,
    token_usage             JSONB,

    -- Scoring
    criterion_results       JSONB,
    final_score             DOUBLE PRECISION,
    passed                  BOOLEAN,

    -- Metadata
    status                  TEXT NOT NULL DEFAULT 'pending',
    duration_seconds        INT,
    sandbox_id              TEXT,
    started_at              TIMESTAMPTZ,
    completed_at            TIMESTAMPTZ,
    error_message           TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_eval_runs_task_id ON eval_runs (task_id, created_at DESC);
CREATE INDEX idx_eval_runs_org_id ON eval_runs (org_id, created_at DESC);
CREATE INDEX idx_eval_runs_batch_id ON eval_runs (batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX idx_eval_runs_status ON eval_runs (org_id, status) WHERE status IN ('pending', 'running');
