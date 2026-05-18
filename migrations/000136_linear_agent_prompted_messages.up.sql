-- Durable idempotency for Linear AgentSessionEvent:prompted comments.
--
-- Worker jobs are at-least-once. If a prompted job commits the inbound
-- session_message and continuation job, then dies before marking itself
-- complete, the retry must recognize the same Linear comment and no-op
-- instead of appending a duplicate user turn.
CREATE TABLE linear_agent_prompted_messages (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                uuid NOT NULL REFERENCES organizations(id),
    agent_session_row_id  uuid NOT NULL REFERENCES linear_agent_sessions(id) ON DELETE CASCADE,
    session_id            uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    linear_comment_id     text NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (agent_session_row_id, linear_comment_id)
);

CREATE INDEX idx_linear_agent_prompted_messages_org_recent
    ON linear_agent_prompted_messages (org_id, created_at DESC);
