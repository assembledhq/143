ALTER TABLE session_threads
    ADD COLUMN runtime_stop_reason text NOT NULL DEFAULT '',
    ADD COLUMN runtime_graceful_stop_at timestamptz,
    ADD COLUMN recovery_state text NOT NULL DEFAULT '',
    ADD COLUMN recovery_reason text NOT NULL DEFAULT '',
    ADD COLUMN recovery_event_history jsonb NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE session_threads
    ADD CONSTRAINT chk_session_threads_runtime_stop_reason CHECK (
        runtime_stop_reason IN ('', 'user_cancel', 'soft_budget', 'no_progress', 'absolute_ceiling', 'force_kill', 'worker_recovery', 'worker_drain', 'deploy_budget_expired')
    ),
    ADD CONSTRAINT chk_session_threads_recovery_state CHECK (
        recovery_state IN ('', 'queued', 'recovering', 'unavailable')
    );

CREATE INDEX idx_session_threads_recovery_state
    ON session_threads (org_id, recovery_state, runtime_graceful_stop_at)
    WHERE recovery_state <> '';

ALTER TABLE preview_instances
    ADD COLUMN unavailable_reason text NOT NULL DEFAULT '';

ALTER TABLE preview_runtimes
    ADD COLUMN unavailable_reason text NOT NULL DEFAULT '';

ALTER TABLE preview_instances
    ADD CONSTRAINT chk_preview_instances_unavailable_reason CHECK (
        unavailable_reason IN ('', 'owner_lost', 'deploy_drain_timeout', 'host_maintenance', 'emergency_force', 'lease_expired')
    );

ALTER TABLE preview_runtimes
    ADD CONSTRAINT chk_preview_runtimes_unavailable_reason CHECK (
        unavailable_reason IN ('', 'owner_lost', 'deploy_drain_timeout', 'host_maintenance', 'emergency_force', 'lease_expired')
    );

CREATE TABLE worker_deploy_waves (
    -- lint:no-org-id reason="cluster-scoped worker deploy waves coordinate infrastructure, not tenant data"
    id              text        PRIMARY KEY,
    status          text        NOT NULL DEFAULT 'pending',
    mode            text        NOT NULL DEFAULT 'routine',
    build_sha       text        NOT NULL DEFAULT '',
    region          text        NOT NULL DEFAULT '',
    bucket          text        NOT NULL DEFAULT '',
    requested_by    text        NOT NULL DEFAULT '',
    reason          text        NOT NULL DEFAULT '',
    max_concurrent  integer     NOT NULL DEFAULT 1,
    canary_count    integer     NOT NULL DEFAULT 1,
    pause_reason    text        NOT NULL DEFAULT '',
    metadata        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    started_at      timestamptz,
    paused_at       timestamptz,
    completed_at    timestamptz,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_worker_deploy_waves_status CHECK (status IN ('pending', 'running', 'paused', 'rolled_back', 'completed', 'failed')),
    CONSTRAINT chk_worker_deploy_waves_mode CHECK (mode IN ('routine', 'maintenance', 'emergency')),
    CONSTRAINT chk_worker_deploy_waves_max_concurrent CHECK (max_concurrent > 0),
    CONSTRAINT chk_worker_deploy_waves_canary_count CHECK (canary_count > 0)
);

CREATE INDEX idx_worker_deploy_waves_status_created
    ON worker_deploy_waves (status, created_at DESC);

CREATE TABLE worker_deploy_wave_hosts (
    -- lint:no-org-id reason="cluster-scoped worker deploy wave host state coordinates infrastructure, not tenant data"
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    wave_id       text        NOT NULL REFERENCES worker_deploy_waves(id) ON DELETE CASCADE,
    node_id       text        NOT NULL,
    host          text        NOT NULL DEFAULT '',
    region        text        NOT NULL DEFAULT '',
    bucket        text        NOT NULL DEFAULT '',
    status        text        NOT NULL DEFAULT 'pending',
    deploy_id     text        NOT NULL DEFAULT '',
    last_error    text        NOT NULL DEFAULT '',
    metadata      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    started_at    timestamptz,
    completed_at  timestamptz,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (wave_id, node_id),
    CONSTRAINT chk_worker_deploy_wave_hosts_status CHECK (status IN ('pending', 'running', 'draining', 'retired', 'skipped', 'failed', 'rolled_back'))
);

CREATE INDEX idx_worker_deploy_wave_hosts_wave_status
    ON worker_deploy_wave_hosts (wave_id, status, updated_at DESC);

CREATE TABLE deploy_drain_extensions (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id     uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    thread_id      uuid        REFERENCES session_threads(id) ON DELETE SET NULL,
    executor_id    uuid        REFERENCES session_executors(id) ON DELETE SET NULL,
    node_id        text        NOT NULL DEFAULT '',
    deploy_id      text        NOT NULL DEFAULT '',
    requested_by   text        NOT NULL,
    reason         text        NOT NULL,
    extend_until   timestamptz NOT NULL,
    active         boolean     NOT NULL DEFAULT true,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_deploy_drain_extensions_active_session
    ON deploy_drain_extensions (org_id, session_id, COALESCE(thread_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE active = true;

CREATE INDEX idx_deploy_drain_extensions_node_active
    ON deploy_drain_extensions (node_id, extend_until)
    WHERE active = true;

CREATE TABLE worker_image_retention (
    -- lint:no-org-id reason="cluster-scoped image retention protects active infrastructure runtimes"
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    image          text        NOT NULL,
    build_sha      text        NOT NULL DEFAULT '',
    node_id        text        NOT NULL DEFAULT '',
    executor_id    uuid,
    deploy_id      text        NOT NULL DEFAULT '',
    reason         text        NOT NULL,
    expires_at     timestamptz NOT NULL,
    active         boolean     NOT NULL DEFAULT true,
    created_at     timestamptz NOT NULL DEFAULT now(),
    released_at    timestamptz
);

CREATE INDEX idx_worker_image_retention_active
    ON worker_image_retention (active, expires_at, image)
    WHERE active = true;
