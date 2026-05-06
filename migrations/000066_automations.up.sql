-- Phase 1, Stage 1: Create automations and automation_runs tables.
-- Separates recurring agent work from finite project work (design doc §48).

CREATE TABLE automations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id),
    repository_id   UUID REFERENCES repositories(id),

    -- What to do
    name            TEXT NOT NULL,
    goal            TEXT NOT NULL,
    scope           TEXT,
    agent_type      TEXT,
    model_override  TEXT,
    execution_mode  TEXT NOT NULL DEFAULT 'sequential',
    max_concurrent  INT NOT NULL DEFAULT 1,
    base_branch     TEXT NOT NULL DEFAULT 'main',

    -- When to do it
    schedule_type   TEXT NOT NULL DEFAULT 'interval',
    interval_value  INT,
    interval_unit   TEXT,
    cron_expression TEXT,
    timezone        TEXT NOT NULL DEFAULT 'UTC',
    next_run_at     TIMESTAMPTZ,
    last_run_at     TIMESTAMPTZ,

    -- State
    enabled         BOOLEAN NOT NULL DEFAULT true,
    created_by      UUID REFERENCES users(id),
    paused_by       UUID REFERENCES users(id),
    paused_at       TIMESTAMPTZ,
    priority        INT NOT NULL DEFAULT 50,

    -- Timestamps
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

-- Efficient lookup for the scheduler's claim-and-fire loop.
CREATE INDEX idx_automations_due
    ON automations (next_run_at)
    WHERE enabled = true AND deleted_at IS NULL AND next_run_at IS NOT NULL;

-- Org-scoped listing.
CREATE INDEX idx_automations_org
    ON automations (org_id, created_at DESC)
    WHERE deleted_at IS NULL;

-- Validation constraints.
ALTER TABLE automations
    ADD CONSTRAINT chk_automations_schedule_type CHECK (schedule_type IN ('interval', 'cron'));
ALTER TABLE automations
    ADD CONSTRAINT chk_automations_execution_mode CHECK (execution_mode IN ('sequential', 'parallel', 'dependency_graph'));
-- interval_unit gates BulkUpdateEnabled's resume path, which builds a Postgres
-- interval via `(interval_value::text || ' ' || interval_unit)::interval`.
-- A bad unit would raise at runtime for every row in the bulk update — cheaper
-- to reject at write time.
ALTER TABLE automations
    ADD CONSTRAINT chk_automations_interval_unit CHECK (interval_unit IS NULL OR interval_unit IN ('hours', 'days', 'weeks'));
-- Interval schedules ignore timezone (NextRunTime uses fixed duration arithmetic),
-- so storing a non-UTC value would be misleading. Only cron schedules evaluate
-- the field. Enforce this at the DB layer so a buggy writer can't silently
-- persist a meaningless tz on an interval row.
ALTER TABLE automations
    ADD CONSTRAINT chk_automations_timezone_interval CHECK (schedule_type = 'cron' OR timezone = 'UTC');
-- Cap lengths to avoid a 10MB name/goal being accepted silently.
ALTER TABLE automations
    ADD CONSTRAINT chk_automations_name_length CHECK (char_length(name) BETWEEN 1 AND 200);
ALTER TABLE automations
    ADD CONSTRAINT chk_automations_goal_length CHECK (char_length(goal) BETWEEN 1 AND 4000);

-- automation_runs: each scheduled or manual trigger creates one run.
-- org_id is denormalized from automations for cheap, safe tenancy filtering
-- on every read (the parent automation's org_id is immutable).
CREATE TABLE automation_runs (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    automation_id        UUID NOT NULL REFERENCES automations(id),
    org_id               UUID NOT NULL REFERENCES organizations(id),

    -- Trigger context
    triggered_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    triggered_by         TEXT NOT NULL DEFAULT 'schedule',
    triggered_by_user_id UUID REFERENCES users(id),
    scheduled_time       TIMESTAMPTZ,

    -- Snapshot of config at trigger time
    goal_snapshot        TEXT NOT NULL,
    config_snapshot      JSONB,

    -- State
    status               TEXT NOT NULL DEFAULT 'pending',
    completed_at         TIMESTAMPTZ,
    result_summary       TEXT,

    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_automation_runs_automation
    ON automation_runs (automation_id, triggered_at DESC);
CREATE INDEX idx_automation_runs_org
    ON automation_runs (org_id, triggered_at DESC);

-- Idempotency: prevent double-firing for the same scheduled time.
CREATE UNIQUE INDEX idx_automation_runs_idempotency
    ON automation_runs (automation_id, scheduled_time)
    WHERE scheduled_time IS NOT NULL;

ALTER TABLE automation_runs
    ADD CONSTRAINT chk_automation_runs_triggered_by CHECK (triggered_by IN ('schedule', 'manual'));
ALTER TABLE automation_runs
    ADD CONSTRAINT chk_automation_runs_status CHECK (status IN ('pending', 'running', 'completed', 'completed_noop', 'failed', 'skipped'));

-- Link sessions to automation runs.
ALTER TABLE sessions ADD COLUMN automation_run_id UUID REFERENCES automation_runs(id);
CREATE INDEX idx_sessions_automation_run ON sessions (automation_run_id)
    WHERE automation_run_id IS NOT NULL;
