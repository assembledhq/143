ALTER TABLE automations
    ADD COLUMN pre_pr_review_loops INT NOT NULL DEFAULT 0;

ALTER TABLE automations
    ADD CONSTRAINT chk_automations_pre_pr_review_loops CHECK (pre_pr_review_loops BETWEEN 0 AND 5);

CREATE TABLE session_review_loops (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id),
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    automation_run_id UUID REFERENCES automation_runs(id),
    thread_id UUID REFERENCES session_threads(id),

    status TEXT NOT NULL,
    source TEXT NOT NULL,
    agent_type TEXT NOT NULL,
    max_passes INTEGER NOT NULL,
    completed_passes INTEGER NOT NULL DEFAULT 0,
    review_required BOOLEAN NOT NULL DEFAULT false,
    bypassed_by_user_id UUID REFERENCES users(id),
    bypass_reason TEXT,

    loop_start_checkpoint_key TEXT,
    latest_checkpoint_key TEXT,
    latest_summary TEXT,
    started_by_user_id UUID REFERENCES users(id),
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,

    CONSTRAINT chk_session_review_loops_status CHECK (status IN ('running', 'clean', 'needs_human_decision', 'failed', 'cancelled')),
    CONSTRAINT chk_session_review_loops_source CHECK (source IN ('manual', 'automation')),
    CONSTRAINT chk_session_review_loops_max_passes CHECK (max_passes BETWEEN 1 AND 5),
    CONSTRAINT chk_session_review_loops_completed_passes CHECK (completed_passes BETWEEN 0 AND 5)
);

CREATE INDEX idx_session_review_loops_session
    ON session_review_loops (org_id, session_id, started_at DESC);

CREATE UNIQUE INDEX idx_session_review_loops_one_running_per_session
    ON session_review_loops (org_id, session_id)
    WHERE status = 'running';

CREATE INDEX idx_session_review_loops_thread_running
    ON session_review_loops (org_id, thread_id, started_at DESC)
    WHERE status = 'running' AND thread_id IS NOT NULL;

CREATE INDEX idx_session_review_loops_automation_run
    ON session_review_loops (org_id, automation_run_id, started_at DESC)
    WHERE automation_run_id IS NOT NULL;

CREATE TABLE session_review_loop_passes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id UUID NOT NULL REFERENCES organizations(id),
    loop_id UUID NOT NULL REFERENCES session_review_loops(id) ON DELETE CASCADE,
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    pass_index INTEGER NOT NULL,

    -- session_messages is partitioned with PRIMARY KEY (id, created_at), so
    -- these are denormalized pointers rather than id-only foreign keys.
    review_message_id BIGINT,
    decision_message_id BIGINT,
    fix_message_id BIGINT,
    status TEXT NOT NULL,
    agent_decision TEXT,
    review_output TEXT,
    fix_summary TEXT,
    review_started_at TIMESTAMPTZ,
    review_completed_at TIMESTAMPTZ,
    fix_started_at TIMESTAMPTZ,
    fix_completed_at TIMESTAMPTZ,
    summary TEXT,

    CONSTRAINT chk_session_review_loop_passes_status CHECK (status IN ('reviewing', 'deciding', 'fixing', 'clean', 'needs_fix', 'failed')),
    CONSTRAINT chk_session_review_loop_passes_decision CHECK (agent_decision IS NULL OR agent_decision IN ('REVIEW_CLEAN', 'NEEDS_FIX_PASS')),
    CONSTRAINT chk_session_review_loop_passes_index CHECK (pass_index BETWEEN 1 AND 5),
    UNIQUE (org_id, loop_id, pass_index)
);

CREATE INDEX idx_session_review_loop_passes_loop
    ON session_review_loop_passes (org_id, loop_id, pass_index ASC);
