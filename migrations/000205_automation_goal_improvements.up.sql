CREATE TABLE automation_goal_improvements (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID NOT NULL REFERENCES organizations(id),
    automation_id       UUID REFERENCES automations(id),
    repository_id       UUID REFERENCES repositories(id),
    mode                TEXT NOT NULL CHECK (mode IN ('fast', 'deep')),
    status              TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed', 'canceled')),
    input_name          TEXT,
    input_goal          TEXT NOT NULL,
    input_config        JSONB NOT NULL DEFAULT '{}'::jsonb,
    base_goal_hash      TEXT NOT NULL,
    evidence_snapshot   JSONB NOT NULL DEFAULT '{}'::jsonb,
    proposed_goal       TEXT,
    proposal            JSONB NOT NULL DEFAULT '{}'::jsonb,
    confidence          TEXT,
    warnings            JSONB NOT NULL DEFAULT '[]'::jsonb,
    error_message       TEXT,
    analysis_session_id UUID REFERENCES sessions(id),
    created_by          UUID REFERENCES users(id),
    applied_by          UUID REFERENCES users(id),
    applied_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_automation_goal_improvements_org_automation
    ON automation_goal_improvements (org_id, automation_id, created_at DESC);

CREATE INDEX idx_automation_goal_improvements_org_created
    ON automation_goal_improvements (org_id, created_at DESC);

CREATE UNIQUE INDEX uniq_automation_goal_improvements_running_deep
    ON automation_goal_improvements (org_id, automation_id)
    WHERE automation_id IS NOT NULL
      AND mode = 'deep'
      AND status IN ('pending', 'running');

CREATE INDEX idx_automation_goal_improvements_analysis_session
    ON automation_goal_improvements (analysis_session_id)
    WHERE analysis_session_id IS NOT NULL;
