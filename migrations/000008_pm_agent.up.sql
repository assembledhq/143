CREATE TABLE pm_plans (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                   UUID NOT NULL REFERENCES organizations(id),
    status                   TEXT NOT NULL DEFAULT 'executing',
    analysis                 TEXT,
    tasks                    JSONB NOT NULL DEFAULT '[]',
    clusters                 JSONB NOT NULL DEFAULT '[]',
    skipped_issues           JSONB NOT NULL DEFAULT '[]',
    issues_reviewed          INT NOT NULL DEFAULT 0,
    product_context_snapshot JSONB,
    token_usage              JSONB,
    triggered_by             TEXT NOT NULL DEFAULT 'cron',
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at             TIMESTAMPTZ
);

CREATE INDEX idx_pm_plans_org_created ON pm_plans(org_id, created_at DESC);

CREATE TABLE pm_decision_log (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL REFERENCES organizations(id),
    plan_id    UUID NOT NULL REFERENCES pm_plans(id),
    issue_id   UUID REFERENCES issues(id),
    decision   TEXT NOT NULL,
    reasoning  TEXT NOT NULL,
    outcome    TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_pm_decision_log_org_created ON pm_decision_log(org_id, created_at DESC);
CREATE INDEX idx_pm_decision_log_plan ON pm_decision_log(plan_id);
CREATE INDEX idx_pm_decision_log_issue ON pm_decision_log(issue_id);

ALTER TABLE agent_runs
    ADD COLUMN pm_plan_id UUID REFERENCES pm_plans(id),
    ADD COLUMN pm_approach TEXT,
    ADD COLUMN pm_reasoning TEXT;
