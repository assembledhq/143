-- Multi-turn conversational sessions: add fields to sessions for multi-turn
-- state tracking, create session_messages table for chat history, and add
-- turn_number to session_logs.

-- New columns on sessions for multi-turn state.
ALTER TABLE sessions
    ADD COLUMN agent_session_id text,
    ADD COLUMN current_turn int NOT NULL DEFAULT 0,
    ADD COLUMN last_activity_at timestamptz,
    ADD COLUMN sandbox_state text NOT NULL DEFAULT 'none',
    ADD COLUMN snapshot_key text;

-- Chat messages exchanged between user and agent across turns.
CREATE TABLE session_messages (
    id          bigserial   PRIMARY KEY,
    session_id  uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    org_id      uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id     uuid        REFERENCES users(id),
    turn_number int         NOT NULL,
    role        text        NOT NULL,
    content     text        NOT NULL,
    attachments text[],
    token_usage jsonb,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_session_messages_session ON session_messages (session_id, turn_number);
CREATE INDEX idx_session_messages_org ON session_messages (org_id);

-- Track which turn each log entry belongs to.
ALTER TABLE session_logs ADD COLUMN turn_number int NOT NULL DEFAULT 0;

-- Index for the reaper to find sessions with snapshots that need cleanup.
CREATE INDEX idx_sessions_snapshot_cleanup ON sessions (last_activity_at)
    WHERE sandbox_state = 'snapshotted';
