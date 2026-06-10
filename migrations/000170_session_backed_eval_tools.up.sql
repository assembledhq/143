ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS chk_sessions_origin;

ALTER TABLE sessions
    ADD CONSTRAINT chk_sessions_origin
        CHECK (origin IN ('issue_trigger', 'manual', 'project', 'automation', 'revision', 'slack', 'external_api', 'eval_bootstrap', 'eval_run'));

ALTER TABLE eval_runs
    DROP CONSTRAINT IF EXISTS chk_eval_runs_status;

ALTER TABLE eval_runs
    ADD CONSTRAINT chk_eval_runs_status
        CHECK (status IN ('pending', 'running', 'grading', 'completed', 'failed'));

ALTER TABLE eval_bootstrap_runs
    ADD COLUMN IF NOT EXISTS thread_id UUID REFERENCES session_threads(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_eval_bootstrap_runs_session_thread
    ON eval_bootstrap_runs (org_id, session_id, thread_id)
    WHERE session_id IS NOT NULL AND thread_id IS NOT NULL;

CREATE TABLE eval_bootstrap_candidates (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    bootstrap_run_id    UUID NOT NULL REFERENCES eval_bootstrap_runs(id) ON DELETE CASCADE,
    session_id          UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    thread_id           UUID REFERENCES session_threads(id) ON DELETE SET NULL,
    repo_id             UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    candidate_index     INTEGER NOT NULL,
    pr_number           INTEGER NOT NULL,
    pr_title            TEXT NOT NULL,
    base_commit_sha     TEXT NOT NULL,
    solution_commit_sha TEXT NOT NULL,
    solution_diff       TEXT NOT NULL,
    issue_description   TEXT NOT NULL,
    scoring_criteria    JSONB NOT NULL,
    complexity          TEXT NOT NULL CHECK (complexity IN ('trivial', 'simple', 'moderate', 'complex')),
    fitness_score       DOUBLE PRECISION NOT NULL DEFAULT 0,
    fitness_reasoning   TEXT NOT NULL DEFAULT '',
    evidence            JSONB NOT NULL DEFAULT '{}',
    warnings            TEXT[] NOT NULL DEFAULT '{}',
    payload             JSONB NOT NULL,
    status              TEXT NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed', 'accepted', 'rejected', 'needs_revision')),
    rejection_reason    TEXT,
    created_by_tool     TEXT NOT NULL DEFAULT 'eval_add',
    reviewed_by         UUID REFERENCES users(id),
    reviewed_at         TIMESTAMPTZ,
    accepted_task_id    UUID REFERENCES eval_tasks(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, bootstrap_run_id, candidate_index)
);

CREATE INDEX idx_eval_bootstrap_candidates_run
    ON eval_bootstrap_candidates (org_id, bootstrap_run_id, created_at);

CREATE INDEX idx_eval_bootstrap_candidates_status
    ON eval_bootstrap_candidates (org_id, status, created_at);

ALTER TABLE eval_runs
    ADD COLUMN IF NOT EXISTS session_id UUID REFERENCES sessions(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS thread_id UUID REFERENCES session_threads(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_eval_runs_session_thread
    ON eval_runs (org_id, session_id, thread_id)
    WHERE session_id IS NOT NULL;
