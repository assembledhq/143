CREATE TABLE private_connector_groups (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    name               TEXT        NOT NULL,
    environment        TEXT        NOT NULL DEFAULT '',
    gateway_region     TEXT        NOT NULL DEFAULT 'us',
    status             TEXT        NOT NULL DEFAULT 'waiting',
    health_alert_url   TEXT,
    offline_alert_after_seconds INTEGER NOT NULL DEFAULT 60,
    created_by_user_id UUID        REFERENCES users(id) ON DELETE SET NULL,
    disabled_at        TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT private_connector_groups_name_nonempty
        CHECK (length(trim(name)) > 0),
    CONSTRAINT private_connector_groups_status_check
        CHECK (status IN ('waiting', 'online', 'reconnecting', 'offline', 'disabled')),
    CONSTRAINT private_connector_groups_offline_alert_after_positive
        CHECK (offline_alert_after_seconds > 0)
);

CREATE UNIQUE INDEX idx_private_connector_groups_org_name
    ON private_connector_groups (org_id, lower(name));

CREATE INDEX idx_private_connector_groups_org_status
    ON private_connector_groups (org_id, status, updated_at DESC);

CREATE TABLE private_connector_deployment_tokens (
    id                     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                 UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    connector_group_id     UUID        NOT NULL REFERENCES private_connector_groups(id) ON DELETE CASCADE,
    name                   TEXT        NOT NULL,
    token_hash             TEXT        NOT NULL,
    token_prefix           TEXT        NOT NULL,
    preset                 TEXT        NOT NULL,
    max_registrations      INTEGER,
    registration_count     INTEGER     NOT NULL DEFAULT 0,
    allowed_source_cidrs   TEXT[]      NOT NULL DEFAULT '{}',
    allowed_gateway_region TEXT,
    expires_at             TIMESTAMPTZ,
    last_used_at           TIMESTAMPTZ,
    revoked_at             TIMESTAMPTZ,
    revoked_by_user_id     UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_by_user_id     UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT private_connector_deployment_tokens_name_nonempty
        CHECK (length(trim(name)) > 0),
    CONSTRAINT private_connector_deployment_tokens_preset_check
        CHECK (preset IN ('interactive', 'automation')),
    CONSTRAINT private_connector_deployment_tokens_max_registrations_positive
        CHECK (max_registrations IS NULL OR max_registrations > 0),
    CONSTRAINT private_connector_deployment_tokens_registration_count_nonnegative
        CHECK (registration_count >= 0)
);

CREATE UNIQUE INDEX idx_private_connector_deployment_tokens_hash
    ON private_connector_deployment_tokens (token_hash);

CREATE INDEX idx_private_connector_deployment_tokens_group
    ON private_connector_deployment_tokens (org_id, connector_group_id, created_at DESC);

CREATE TABLE private_connector_instances (
    id                         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                     UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    connector_group_id         UUID        NOT NULL REFERENCES private_connector_groups(id) ON DELETE CASCADE,
    deployment_token_id        UUID        REFERENCES private_connector_deployment_tokens(id) ON DELETE SET NULL,
    instance_name              TEXT        NOT NULL,
    public_key                 TEXT        NOT NULL,
    status                     TEXT        NOT NULL DEFAULT 'online',
    version                    TEXT        NOT NULL DEFAULT '',
    protocol                   TEXT        NOT NULL DEFAULT 'websocket',
    gateway_region             TEXT        NOT NULL DEFAULT 'us',
    capabilities               TEXT[]      NOT NULL DEFAULT '{}',
    last_heartbeat_at          TIMESTAMPTZ,
    heartbeat_interval_seconds INTEGER     NOT NULL DEFAULT 5,
    online_at                  TIMESTAMPTZ,
    offline_at                 TIMESTAMPTZ,
    revoked_at                 TIMESTAMPTZ,
    revoked_by_user_id         UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT private_connector_instances_name_nonempty
        CHECK (length(trim(instance_name)) > 0),
    CONSTRAINT private_connector_instances_public_key_nonempty
        CHECK (length(trim(public_key)) > 0),
    CONSTRAINT private_connector_instances_status_check
        CHECK (status IN ('online', 'reconnecting', 'offline', 'revoked')),
    CONSTRAINT private_connector_instances_protocol_check
        CHECK (protocol IN ('websocket', 'grpc')),
    CONSTRAINT private_connector_instances_heartbeat_positive
        CHECK (heartbeat_interval_seconds > 0)
);

CREATE UNIQUE INDEX idx_private_connector_instances_active_key
    ON private_connector_instances (org_id, public_key)
    WHERE revoked_at IS NULL;

CREATE INDEX idx_private_connector_instances_group_health
    ON private_connector_instances (org_id, connector_group_id, status, last_heartbeat_at DESC);

CREATE TABLE private_connector_resources (
    id                         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                     UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    connector_group_id         UUID        NOT NULL REFERENCES private_connector_groups(id) ON DELETE CASCADE,
    display_name               TEXT        NOT NULL,
    resource_type              TEXT        NOT NULL,
    mode                       TEXT        NOT NULL,
    config                     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    config_source              TEXT        NOT NULL DEFAULT 'ui',
    config_version             BIGINT      NOT NULL DEFAULT 1,
    status                     TEXT        NOT NULL DEFAULT 'configured',
    last_test_status           TEXT,
    last_test_error            TEXT,
    last_successful_request_at TIMESTAMPTZ,
    last_error                 TEXT,
    created_by_user_id         UUID        REFERENCES users(id) ON DELETE SET NULL,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT private_connector_resources_name_nonempty
        CHECK (length(trim(display_name)) > 0),
    CONSTRAINT private_connector_resources_type_check
        CHECK (resource_type IN ('victorialogs', 'postgres')),
    CONSTRAINT private_connector_resources_mode_check
        CHECK (mode IN ('logs', 'agent_readonly', 'preview_runtime')),
    CONSTRAINT private_connector_resources_config_source_check
        CHECK (config_source IN ('file', 'ui')),
    CONSTRAINT private_connector_resources_status_check
        CHECK (status IN ('configured', 'ready', 'error', 'disabled')),
    CONSTRAINT private_connector_resources_config_version_positive
        CHECK (config_version > 0)
);

CREATE UNIQUE INDEX idx_private_connector_resources_group_name
    ON private_connector_resources (org_id, connector_group_id, lower(display_name));

CREATE INDEX idx_private_connector_resources_capability
    ON private_connector_resources (org_id, resource_type, mode, status);

CREATE TABLE private_connector_runtime_leases (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id               UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repository_id        UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    preview_id           UUID        NOT NULL REFERENCES preview_instances(id) ON DELETE CASCADE,
    preview_runtime_id   UUID        NOT NULL,
    connector_group_id   UUID        NOT NULL REFERENCES private_connector_groups(id) ON DELETE CASCADE,
    resource_id          UUID        NOT NULL REFERENCES private_connector_resources(id) ON DELETE CASCADE,
    status               TEXT        NOT NULL DEFAULT 'active',
    access_mode          TEXT        NOT NULL,
    target_host          TEXT        NOT NULL,
    target_port          INTEGER     NOT NULL,
    target_database      TEXT        NOT NULL DEFAULT '',
    lease_token_hash     TEXT        NOT NULL,
    lease_token_prefix   TEXT        NOT NULL,
    max_connections      INTEGER     NOT NULL DEFAULT 4,
    idle_timeout_seconds INTEGER     NOT NULL DEFAULT 300,
    byte_limit           BIGINT,
    expires_at           TIMESTAMPTZ NOT NULL,
    revoked_at           TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT private_connector_runtime_leases_status_check
        CHECK (status IN ('active', 'revoked', 'expired', 'failed')),
    CONSTRAINT private_connector_runtime_leases_access_mode_check
        CHECK (access_mode IN ('postgres_tcp')),
    CONSTRAINT private_connector_runtime_leases_target_host_nonempty
        CHECK (length(trim(target_host)) > 0),
    CONSTRAINT private_connector_runtime_leases_target_port_valid
        CHECK (target_port > 0 AND target_port <= 65535),
    CONSTRAINT private_connector_runtime_leases_max_connections_positive
        CHECK (max_connections > 0),
    CONSTRAINT private_connector_runtime_leases_idle_timeout_positive
        CHECK (idle_timeout_seconds > 0),
    CONSTRAINT private_connector_runtime_leases_byte_limit_positive
        CHECK (byte_limit IS NULL OR byte_limit > 0)
);

CREATE UNIQUE INDEX idx_private_connector_runtime_leases_token_hash
    ON private_connector_runtime_leases (lease_token_hash);

CREATE INDEX idx_private_connector_runtime_leases_preview_active
    ON private_connector_runtime_leases (org_id, preview_id, status, expires_at DESC);

CREATE INDEX idx_private_connector_runtime_leases_resource_active
    ON private_connector_runtime_leases (org_id, resource_id, status, expires_at DESC);

CREATE TABLE private_connector_actions (
    id                    UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    connector_group_id    UUID        NOT NULL REFERENCES private_connector_groups(id) ON DELETE CASCADE,
    connector_instance_id UUID        REFERENCES private_connector_instances(id) ON DELETE SET NULL,
    resource_id           UUID        NOT NULL REFERENCES private_connector_resources(id) ON DELETE CASCADE,
    capability            TEXT        NOT NULL,
    actor_type            TEXT        NOT NULL,
    actor_id              TEXT        NOT NULL,
    repository_id         UUID        REFERENCES repositories(id) ON DELETE SET NULL,
    session_id            UUID        REFERENCES sessions(id) ON DELETE SET NULL,
    preview_id            UUID        REFERENCES preview_instances(id) ON DELETE SET NULL,
    request_nonce         TEXT        NOT NULL,
    request_fingerprint   TEXT        NOT NULL,
    status                TEXT        NOT NULL DEFAULT 'pending',
    error_code            TEXT,
    error_message         TEXT,
    result_count          INTEGER,
    duration_ms           INTEGER,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at          TIMESTAMPTZ,
    CONSTRAINT private_connector_actions_capability_nonempty
        CHECK (length(trim(capability)) > 0),
    CONSTRAINT private_connector_actions_actor_type_check
        CHECK (actor_type IN ('user', 'agent', 'system', 'webhook', 'api_client')),
    CONSTRAINT private_connector_actions_status_check
        CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'denied')),
    CONSTRAINT private_connector_actions_result_count_nonnegative
        CHECK (result_count IS NULL OR result_count >= 0),
    CONSTRAINT private_connector_actions_duration_nonnegative
        CHECK (duration_ms IS NULL OR duration_ms >= 0)
);

CREATE UNIQUE INDEX idx_private_connector_actions_nonce
    ON private_connector_actions (org_id, request_nonce);

CREATE INDEX idx_private_connector_actions_resource_recent
    ON private_connector_actions (org_id, resource_id, created_at DESC);

CREATE INDEX idx_private_connector_actions_session_recent
    ON private_connector_actions (org_id, session_id, created_at DESC)
    WHERE session_id IS NOT NULL;
