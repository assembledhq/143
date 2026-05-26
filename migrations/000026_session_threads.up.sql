-- Multi-agent sessions: add session_threads table for parallel agent threads
-- within a single session, and add thread_id to session_messages and session_logs.

CREATE TABLE session_threads (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id          uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    org_id              uuid        NOT NULL REFERENCES organizations(id),

    -- Agent identity
    agent_type          text        NOT NULL,
    model_override      text,

    -- Thread metadata
    label               text        NOT NULL,
    instructions        text,
    file_scope          text[],

    -- Execution state
    status              text        NOT NULL DEFAULT 'pending',
    agent_session_id    text,
    current_turn        int         NOT NULL DEFAULT 0,
    last_activity_at    timestamptz,

    -- Results
    result_summary      text,
    diff                text,
    failure_explanation text,
    failure_category    text,

    -- Timestamps
    started_at          timestamptz,
    completed_at        timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_session_threads_session ON session_threads(session_id);
CREATE INDEX idx_session_threads_org_status ON session_threads(org_id, status);

-- Add thread_id to session_messages so messages belong to a specific thread.
-- NULL thread_id means session-level message (e.g., initial instructions).
ALTER TABLE session_messages ADD COLUMN thread_id uuid REFERENCES session_threads(id);
CREATE INDEX idx_session_messages_thread ON session_messages(thread_id);

-- Add thread_id to session_logs so logs can be filtered by thread.
-- NULL thread_id means session-level log (e.g., from before threads existed).
ALTER TABLE session_logs ADD COLUMN thread_id uuid REFERENCES session_threads(id);
CREATE INDEX idx_session_logs_thread ON session_logs(thread_id);
