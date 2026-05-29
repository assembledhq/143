ALTER TABLE nodes
    ADD COLUMN drain_intent text NOT NULL DEFAULT 'none',
    ADD COLUMN drain_requested_at timestamptz,
    ADD COLUMN drain_budget_expires_at timestamptz,
    ADD COLUMN drain_requested_by text NOT NULL DEFAULT '',
    ADD COLUMN drain_reason text NOT NULL DEFAULT '';

ALTER TABLE nodes
    ADD CONSTRAINT chk_nodes_drain_intent CHECK (drain_intent IN (
        'none', 'planned_rollout', 'runtime_ceiling', 'human_input_checkpoint',
        'host_maintenance', 'emergency_force'
    ));

CREATE INDEX idx_nodes_worker_drain
    ON nodes (host, status, drain_intent, drain_budget_expires_at)
    WHERE mode IN ('worker', 'all');

ALTER TABLE session_executors
    ADD COLUMN runtime_deadline_at timestamptz,
    ADD COLUMN drain_intent text NOT NULL DEFAULT 'none',
    ADD COLUMN drain_requested_at timestamptz,
    ADD COLUMN drain_deadline_at timestamptz;

ALTER TABLE session_executors
    ADD CONSTRAINT chk_session_executors_drain_intent CHECK (drain_intent IN (
        'none', 'planned_rollout', 'runtime_ceiling', 'human_input_checkpoint',
        'host_maintenance', 'emergency_force'
    ));

CREATE INDEX idx_session_executors_host_active_deadline
    ON session_executors (host_node_id, status, runtime_deadline_at)
    WHERE status IN ('starting', 'running', 'draining');

CREATE TABLE worker_deploy_events (
    -- lint:no-org-id reason="cluster-scoped worker deploy audit events are not tenant-owned"
    id             bigserial   PRIMARY KEY,
    deploy_id      text        NOT NULL,
    node_id        text        NOT NULL,
    host           text        NOT NULL DEFAULT '',
    build_sha      text        NOT NULL DEFAULT '',
    event_type     text        NOT NULL,
    drain_intent   text        NOT NULL DEFAULT 'none',
    requested_by   text        NOT NULL DEFAULT '',
    reason         text        NOT NULL DEFAULT '',
    metadata       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_worker_deploy_events_drain_intent CHECK (drain_intent IN (
        'none', 'planned_rollout', 'runtime_ceiling', 'human_input_checkpoint',
        'host_maintenance', 'emergency_force'
    ))
);

CREATE INDEX idx_worker_deploy_events_deploy_created
    ON worker_deploy_events (deploy_id, created_at DESC);

CREATE INDEX idx_worker_deploy_events_node_created
    ON worker_deploy_events (node_id, created_at DESC);
