-- Eval bootstrap runs: tracks PR history scanning sessions for auto-discovering eval task candidates.
CREATE TABLE eval_bootstrap_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repo_id         UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    candidates      JSONB,
    session_id      UUID,
    created_by      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    error_message   TEXT
);

CREATE INDEX idx_eval_bootstrap_runs_org ON eval_bootstrap_runs (org_id, created_at DESC);
