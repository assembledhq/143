CREATE TABLE thread_inbox_entries (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid NOT NULL REFERENCES organizations(id),
    session_id        uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    thread_id         uuid NOT NULL REFERENCES session_threads(id) ON DELETE CASCADE,
    sequence_no       bigint NOT NULL,
    message_id        bigint NOT NULL REFERENCES session_messages(id) ON DELETE CASCADE,
    entry_type        text NOT NULL,
    delivery_state    text NOT NULL DEFAULT 'pending',
    accepted_at       timestamptz NOT NULL DEFAULT now(),
    delivered_at      timestamptz,
    acked_at          timestamptz,
    owner_node_id     text,
    delivery_attempts integer NOT NULL DEFAULT 0,
    last_error        text,
    CONSTRAINT uq_thread_inbox_thread_sequence UNIQUE (thread_id, sequence_no),
    CONSTRAINT uq_thread_inbox_thread_message UNIQUE (thread_id, message_id),
    CONSTRAINT chk_thread_inbox_entry_type CHECK (entry_type IN ('user_message', 'system_control', 'tool_reply')),
    CONSTRAINT chk_thread_inbox_delivery_state CHECK (delivery_state IN ('pending', 'delivered', 'acked', 'dead_letter')),
    CONSTRAINT chk_thread_inbox_delivery_attempts_nonneg CHECK (delivery_attempts >= 0)
);

CREATE INDEX idx_thread_inbox_pending
    ON thread_inbox_entries (org_id, thread_id, sequence_no)
    WHERE delivery_state IN ('pending', 'delivered');

CREATE INDEX idx_thread_inbox_session
    ON thread_inbox_entries (org_id, session_id, accepted_at);

CREATE TABLE thread_runtimes (
    thread_id                uuid PRIMARY KEY REFERENCES session_threads(id) ON DELETE CASCADE,
    org_id                   uuid NOT NULL REFERENCES organizations(id),
    session_id               uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    runtime_id               text NOT NULL,
    owner_node_id            text NOT NULL,
    lease_token              uuid NOT NULL,
    lease_expires_at         timestamptz NOT NULL,
    status                   text NOT NULL,
    sandbox_id               text,
    agent_type               text NOT NULL,
    model                    text,
    last_delivered_sequence  bigint NOT NULL DEFAULT 0,
    last_acked_sequence      bigint NOT NULL DEFAULT 0,
    last_heartbeat_at        timestamptz NOT NULL DEFAULT now(),
    started_at               timestamptz NOT NULL DEFAULT now(),
    closed_at                timestamptz,
    CONSTRAINT chk_thread_runtime_status CHECK (status IN ('starting', 'live', 'draining', 'lost', 'closed')),
    CONSTRAINT chk_thread_runtime_delivery_order CHECK (last_acked_sequence <= last_delivered_sequence)
);

CREATE INDEX idx_thread_runtimes_owner
    ON thread_runtimes (owner_node_id, lease_expires_at)
    WHERE status IN ('starting', 'live', 'draining');

CREATE INDEX idx_thread_runtimes_session
    ON thread_runtimes (org_id, session_id);
