ALTER TABLE sessions
    ADD COLUMN workspace_generation bigint NOT NULL DEFAULT 0;

CREATE TABLE thread_runtimes (
    id                           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                       uuid        NOT NULL REFERENCES organizations(id),
    session_id                   uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    thread_id                    uuid        NOT NULL REFERENCES session_threads(id) ON DELETE CASCADE,
    sandbox_id                   uuid        NOT NULL DEFAULT gen_random_uuid(),
    container_id                 text        NOT NULL DEFAULT '',
    runtime_handle_id            text        NOT NULL DEFAULT '',
    agent_type                   text        NOT NULL,
    model                        text,
    status                       text        NOT NULL DEFAULT 'starting',
    owner_node_id                text        NOT NULL,
    lease_token                  uuid        NOT NULL,
    last_delivered_sequence      bigint      NOT NULL DEFAULT 0,
    last_acked_sequence          bigint      NOT NULL DEFAULT 0,
    base_workspace_generation    bigint      NOT NULL DEFAULT 0,
    current_workspace_generation bigint      NOT NULL DEFAULT 0,
    started_at                   timestamptz NOT NULL DEFAULT now(),
    heartbeat_at                 timestamptz,
    lease_expires_at             timestamptz,
    closed_at                    timestamptz,
    stop_reason                  text,
    last_error                   text,
    created_at                   timestamptz NOT NULL DEFAULT now(),
    updated_at                   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_thread_runtimes_status CHECK (status IN (
        'starting', 'live', 'paused', 'draining', 'lost', 'closed', 'failed'
    )),
    CONSTRAINT chk_thread_runtimes_sequences_nonneg CHECK (
        last_delivered_sequence >= 0 AND last_acked_sequence >= 0
    ),
    CONSTRAINT chk_thread_runtimes_workspace_generations_nonneg CHECK (
        base_workspace_generation >= 0 AND current_workspace_generation >= 0
    )
);

CREATE UNIQUE INDEX idx_thread_runtimes_one_active
    ON thread_runtimes (org_id, thread_id)
    WHERE status IN ('starting', 'live', 'paused', 'draining');

CREATE INDEX idx_thread_runtimes_session_status
    ON thread_runtimes (org_id, session_id, status);

CREATE INDEX idx_thread_runtimes_owner_heartbeat
    ON thread_runtimes (owner_node_id, status, heartbeat_at)
    WHERE status IN ('starting', 'live', 'paused', 'draining');

CREATE TABLE thread_inbox_entries (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid        NOT NULL REFERENCES organizations(id),
    session_id         uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    thread_id          uuid        NOT NULL REFERENCES session_threads(id) ON DELETE CASCADE,
    sequence_no        bigint      NOT NULL,
    message_id         bigint,
    client_message_id  text,
    entry_type         text        NOT NULL,
    payload            jsonb       NOT NULL DEFAULT '{}'::jsonb,
    delivery_state     text        NOT NULL DEFAULT 'pending',
    delivery_attempts  integer     NOT NULL DEFAULT 0,
    last_error         text,
    owner_node_id      text,
    runtime_id         uuid        REFERENCES thread_runtimes(id) ON DELETE SET NULL,
    accepted_at        timestamptz NOT NULL DEFAULT now(),
    delivered_at       timestamptz,
    acked_at           timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_thread_inbox_entries_entry_type CHECK (entry_type IN (
        'user_message', 'human_input_answer', 'control'
    )),
    CONSTRAINT chk_thread_inbox_entries_delivery_state CHECK (delivery_state IN (
        'pending', 'delivering', 'delivered', 'unknown_delivery', 'acked', 'dead_letter'
    )),
    CONSTRAINT chk_thread_inbox_entries_delivery_attempts_nonneg CHECK (delivery_attempts >= 0),
    CONSTRAINT chk_thread_inbox_entries_sequence_positive CHECK (sequence_no > 0)
);

CREATE UNIQUE INDEX idx_thread_inbox_entries_sequence
    ON thread_inbox_entries (org_id, thread_id, sequence_no);

CREATE UNIQUE INDEX idx_thread_inbox_entries_message
    ON thread_inbox_entries (org_id, message_id)
    WHERE message_id IS NOT NULL;

CREATE UNIQUE INDEX idx_thread_inbox_entries_client_message
    ON thread_inbox_entries (org_id, thread_id, client_message_id)
    WHERE client_message_id IS NOT NULL;

CREATE INDEX idx_thread_inbox_entries_thread_delivery
    ON thread_inbox_entries (org_id, thread_id, delivery_state, sequence_no);

CREATE INDEX idx_thread_inbox_entries_session_delivery
    ON thread_inbox_entries (org_id, session_id, delivery_state);

CREATE INDEX idx_thread_inbox_entries_thread_unacked
    ON thread_inbox_entries (org_id, thread_id, sequence_no)
    WHERE delivery_state <> 'acked';

CREATE INDEX idx_thread_inbox_entries_session_unacked
    ON thread_inbox_entries (org_id, session_id, thread_id, sequence_no)
    WHERE delivery_state <> 'acked';

CREATE TABLE session_sandbox_holders (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid        NOT NULL REFERENCES organizations(id),
    session_id    uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    container_id  text        NOT NULL,
    holder_kind   text        NOT NULL,
    holder_id     uuid        NOT NULL,
    owner_node_id text        NOT NULL,
    lease_token   uuid        NOT NULL,
    status        text        NOT NULL DEFAULT 'active',
    heartbeat_at  timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    released_at   timestamptz,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_session_sandbox_holders_holder_kind CHECK (holder_kind IN (
        'thread_runtime', 'preview', 'snapshot', 'operator'
    )),
    CONSTRAINT chk_session_sandbox_holders_status CHECK (status IN (
        'active', 'draining', 'released', 'expired'
    ))
);

CREATE UNIQUE INDEX idx_session_sandbox_holders_one_active
    ON session_sandbox_holders (org_id, session_id, holder_kind, holder_id)
    WHERE status IN ('active', 'draining');

CREATE INDEX idx_session_sandbox_holders_session_active
    ON session_sandbox_holders (org_id, session_id, expires_at)
    WHERE status IN ('active', 'draining');

CREATE INDEX idx_session_sandbox_holders_owner_active
    ON session_sandbox_holders (owner_node_id, status, heartbeat_at)
    WHERE status IN ('active', 'draining');

DROP INDEX IF EXISTS idx_session_executors_one_active;

CREATE UNIQUE INDEX idx_session_executors_one_active_unthreaded
    ON session_executors (org_id, session_id)
    WHERE thread_id IS NULL
      AND status IN ('starting', 'running', 'draining');

CREATE UNIQUE INDEX idx_session_executors_one_active_thread
    ON session_executors (org_id, session_id, thread_id)
    WHERE thread_id IS NOT NULL
      AND status IN ('starting', 'running', 'draining');
