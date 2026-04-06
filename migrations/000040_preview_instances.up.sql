-- 000040: Preview System Tables
--
-- Implements the data model for the sandbox preview server (design doc 44).
-- Covers preview lifecycle, multi-service tracking, platform infrastructure,
-- screenshot timeline, access sessions, startup caching, and PR integration.

-- =============================================================================
-- preview_instances: core preview lifecycle
-- =============================================================================
CREATE TABLE preview_instances (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id        UUID        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    org_id            UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id           UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_name      TEXT        NOT NULL DEFAULT 'bootstrap',
    name              TEXT        NOT NULL DEFAULT '',
    status            TEXT        NOT NULL DEFAULT 'starting',
    provider          TEXT        NOT NULL DEFAULT 'docker',
    worker_node_id    TEXT        NOT NULL DEFAULT '',
    preview_handle    TEXT        NOT NULL DEFAULT '',
    primary_service   TEXT        NOT NULL DEFAULT '',
    port              INT         NOT NULL DEFAULT 0,
    config_digest     TEXT        NOT NULL DEFAULT '',
    base_commit_sha   TEXT        NOT NULL DEFAULT '',
    last_accessed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at        TIMESTAMPTZ NOT NULL DEFAULT now() + INTERVAL '30 minutes',
    stopped_at        TIMESTAMPTZ,
    last_path         TEXT        NOT NULL DEFAULT '/',
    memory_limit_mb   INT         NOT NULL DEFAULT 512,
    cpu_limit_millis  INT         NOT NULL DEFAULT 500,
    error             TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Lookup by session (most common query path).
CREATE INDEX idx_preview_instances_org_session
    ON preview_instances (org_id, session_id, created_at DESC);

-- Cleanup and routing by worker.
CREATE INDEX idx_preview_instances_worker_status
    ON preview_instances (worker_node_id, status);

-- Enforce at most one active preview per session.
CREATE UNIQUE INDEX idx_preview_instances_active_session
    ON preview_instances (session_id)
    WHERE status IN ('starting', 'ready', 'partially_ready', 'unhealthy');

-- =============================================================================
-- preview_services: per-service state for multi-service configs
-- =============================================================================
CREATE TABLE preview_services (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    preview_instance_id UUID        NOT NULL REFERENCES preview_instances(id) ON DELETE CASCADE,
    service_name        TEXT        NOT NULL,
    role                TEXT        NOT NULL DEFAULT 'support',
    status              TEXT        NOT NULL DEFAULT 'starting',
    command             TEXT[]      NOT NULL DEFAULT '{}',
    cwd                 TEXT        NOT NULL DEFAULT '',
    port                INT         NOT NULL DEFAULT 0,
    pid                 INT,
    error               TEXT        NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_preview_services_instance_name
    ON preview_services (preview_instance_id, service_name);

-- =============================================================================
-- preview_infrastructure: platform infra containers (PostgreSQL, Redis, etc.)
-- =============================================================================
CREATE TABLE preview_infrastructure (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    preview_instance_id UUID        NOT NULL REFERENCES preview_instances(id) ON DELETE CASCADE,
    infra_name          TEXT        NOT NULL,
    template            TEXT        NOT NULL,
    container_id        TEXT        NOT NULL DEFAULT '',
    status              TEXT        NOT NULL DEFAULT 'provisioning',
    host                TEXT        NOT NULL DEFAULT '',
    port                INT         NOT NULL DEFAULT 0,
    credentials_hash    TEXT        NOT NULL DEFAULT '',
    error               TEXT        NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_preview_infrastructure_instance_name
    ON preview_infrastructure (preview_instance_id, infra_name);

-- =============================================================================
-- preview_snapshots: screenshot timeline
-- =============================================================================
CREATE TABLE preview_snapshots (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    preview_instance_id UUID        NOT NULL REFERENCES preview_instances(id) ON DELETE CASCADE,
    trigger             TEXT        NOT NULL DEFAULT 'baseline',
    url_path            TEXT        NOT NULL DEFAULT '/',
    blob_ref            TEXT        NOT NULL DEFAULT '',
    viewport_width      INT         NOT NULL DEFAULT 1280,
    viewport_height     INT         NOT NULL DEFAULT 720,
    console_errors      JSONB       NOT NULL DEFAULT '[]'::jsonb,
    file_changes        JSONB,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_preview_snapshots_instance_created
    ON preview_snapshots (preview_instance_id, created_at);

-- =============================================================================
-- preview_logs: lifecycle and diagnostic logs
-- =============================================================================
CREATE TABLE preview_logs (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    preview_instance_id UUID        NOT NULL REFERENCES preview_instances(id) ON DELETE CASCADE,
    org_id              UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    level               TEXT        NOT NULL DEFAULT 'info',
    step                TEXT        NOT NULL DEFAULT 'start',
    message             TEXT        NOT NULL DEFAULT '',
    metadata            JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_preview_logs_instance_created
    ON preview_logs (preview_instance_id, created_at);

-- =============================================================================
-- preview_access_sessions: bootstrap token exchange sessions
-- =============================================================================
CREATE TABLE preview_access_sessions (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id             UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    preview_instance_id UUID        NOT NULL REFERENCES preview_instances(id) ON DELETE CASCADE,
    session_token_hash  TEXT        NOT NULL,
    issued_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at          TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ,
    last_accessed_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_preview_access_sessions_instance
    ON preview_access_sessions (preview_instance_id);

CREATE INDEX idx_preview_access_sessions_token
    ON preview_access_sessions (session_token_hash);

-- =============================================================================
-- preview_startup_cache: filesystem snapshot metadata for fast startup
-- =============================================================================
CREATE TABLE preview_startup_cache (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repo_id         UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    snapshot_key    TEXT        NOT NULL,
    blob_path       TEXT        NOT NULL DEFAULT '',
    size_bytes      BIGINT      NOT NULL DEFAULT 0,
    worker_node_id  TEXT        NOT NULL DEFAULT '',
    last_used_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_preview_startup_cache_lookup
    ON preview_startup_cache (org_id, repo_id, snapshot_key);

CREATE INDEX idx_preview_startup_cache_worker
    ON preview_startup_cache (worker_node_id, last_used_at);

-- =============================================================================
-- pr_preview_state: PR comment lifecycle tracking
-- =============================================================================
CREATE TABLE pr_preview_state (
    id                          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                      UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repo_id                     UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    pr_number                   INT         NOT NULL,
    github_comment_id           BIGINT,
    last_preview_instance_id    UUID        REFERENCES preview_instances(id) ON DELETE SET NULL,
    last_screenshot_blob_path   TEXT        NOT NULL DEFAULT '',
    last_visual_diff_blob_path  TEXT        NOT NULL DEFAULT '',
    base_snapshot_key           TEXT        NOT NULL DEFAULT '',
    status                      TEXT        NOT NULL DEFAULT 'never_started',
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_pr_preview_state_repo_pr
    ON pr_preview_state (org_id, repo_id, pr_number);
