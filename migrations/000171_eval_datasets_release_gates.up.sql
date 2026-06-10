CREATE TABLE eval_datasets (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id       UUID REFERENCES repositories(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    dataset_type        TEXT NOT NULL CHECK (dataset_type IN ('golden', 'shadow', 'adversarial')),
    status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    description         TEXT NOT NULL DEFAULT '',
    source_summary      TEXT NOT NULL DEFAULT '',
    created_by_user_id  UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_eval_datasets_type_status
    ON eval_datasets (org_id, dataset_type, status);

CREATE INDEX idx_eval_datasets_repo_created
    ON eval_datasets (org_id, repository_id, created_at DESC);

CREATE TABLE eval_dataset_tasks (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    dataset_id  UUID NOT NULL REFERENCES eval_datasets(id) ON DELETE CASCADE,
    task_id     UUID NOT NULL REFERENCES eval_tasks(id) ON DELETE CASCADE,
    slice_key   TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, dataset_id, task_id)
);

CREATE INDEX idx_eval_dataset_tasks_dataset
    ON eval_dataset_tasks (org_id, dataset_id, created_at);

CREATE TABLE eval_release_gates (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id               UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    gate_name            TEXT NOT NULL,
    enabled              BOOLEAN NOT NULL DEFAULT true,
    dataset_id           UUID REFERENCES eval_datasets(id) ON DELETE SET NULL,
    min_pass_at_1        DOUBLE PRECISION NOT NULL DEFAULT 0.8,
    min_pass_at_k        DOUBLE PRECISION NOT NULL DEFAULT 0.8,
    max_policy_violations INTEGER NOT NULL DEFAULT 0,
    max_regression_delta DOUBLE PRECISION NOT NULL DEFAULT 0,
    canary_stages        JSONB NOT NULL DEFAULT '[10,30,100]'::jsonb,
    rollback_rules       JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_by_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    active               BOOLEAN NOT NULL DEFAULT true,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX uq_eval_release_gates_active
    ON eval_release_gates (org_id, gate_name)
    WHERE active = true;

CREATE INDEX idx_eval_release_gates_recent
    ON eval_release_gates (org_id, created_at DESC);
