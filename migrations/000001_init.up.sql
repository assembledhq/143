CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- =============================================================================
-- organizations
-- =============================================================================
CREATE TABLE organizations (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL,
    slug        text        NOT NULL UNIQUE,
    settings    jsonb       NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- =============================================================================
-- users
-- =============================================================================
CREATE TABLE users (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid        NOT NULL REFERENCES organizations(id),
    email        text        NOT NULL UNIQUE,
    name         text        NOT NULL,
    role         text        NOT NULL DEFAULT 'member',
    github_id    bigint,
    github_login text,
    avatar_url   text,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_users_org_id ON users (org_id);
CREATE UNIQUE INDEX idx_users_github_id ON users (github_id) WHERE github_id IS NOT NULL;

-- =============================================================================
-- sessions
-- =============================================================================
CREATE TABLE sessions (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id     uuid        NOT NULL REFERENCES organizations(id),
    token      text        NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_sessions_token ON sessions (token);
CREATE INDEX idx_sessions_expires_at ON sessions (expires_at);

-- =============================================================================
-- integrations
-- =============================================================================
CREATE TABLE integrations (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid        NOT NULL REFERENCES organizations(id),
    provider       text        NOT NULL,
    config         jsonb       NOT NULL DEFAULT '{}',
    status         text        NOT NULL DEFAULT 'active',
    last_synced_at timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_integrations_org_id ON integrations (org_id);

-- =============================================================================
-- repositories
-- =============================================================================
CREATE TABLE repositories (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid        NOT NULL REFERENCES organizations(id),
    integration_id  uuid        NOT NULL REFERENCES integrations(id),
    github_id       bigint      NOT NULL,
    full_name       text        NOT NULL,
    default_branch  text        NOT NULL DEFAULT 'main',
    private         boolean     NOT NULL DEFAULT false,
    language        text,
    description     text,
    clone_url       text        NOT NULL,
    installation_id bigint      NOT NULL,
    status          text        NOT NULL DEFAULT 'active',
    last_synced_at  timestamptz,
    context_quality float,
    settings        jsonb       NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_repositories_org_github ON repositories (org_id, github_id);
CREATE INDEX idx_repositories_org_status ON repositories (org_id, status);
CREATE INDEX idx_repositories_org_full_name ON repositories (org_id, full_name);

-- =============================================================================
-- jobs
-- =============================================================================
CREATE TABLE jobs (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid        NOT NULL REFERENCES organizations(id),
    queue            text        NOT NULL,
    job_type         text        NOT NULL,
    payload          jsonb       NOT NULL DEFAULT '{}',
    priority         int         NOT NULL DEFAULT 0,
    status           text        NOT NULL DEFAULT 'pending',
    attempts         int         NOT NULL DEFAULT 0,
    max_attempts     int         NOT NULL DEFAULT 3,
    run_at           timestamptz NOT NULL DEFAULT now(),
    locked_by_node_id text,
    locked_at        timestamptz,
    last_error       text,
    dedupe_key       text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    completed_at     timestamptz
);

-- dequeue path
CREATE INDEX idx_jobs_dequeue ON jobs (status, run_at, priority DESC);
-- queue-specific workers
CREATE INDEX idx_jobs_queue_dequeue ON jobs (queue, status, run_at, priority DESC);
-- org job history
CREATE INDEX idx_jobs_org_history ON jobs (org_id, created_at DESC);
-- dead-worker recovery
CREATE INDEX idx_jobs_locked ON jobs (locked_by_node_id, locked_at);
-- in-flight dedupe
CREATE UNIQUE INDEX idx_jobs_dedupe ON jobs (queue, dedupe_key)
    WHERE dedupe_key IS NOT NULL AND status IN ('pending', 'running');

-- =============================================================================
-- nodes
-- =============================================================================
CREATE TABLE nodes (
    id                text        PRIMARY KEY,
    mode              text        NOT NULL DEFAULT 'all',
    host              text,
    started_at        timestamptz NOT NULL DEFAULT now(),
    last_heartbeat_at timestamptz NOT NULL DEFAULT now(),
    status            text        NOT NULL DEFAULT 'active',
    metadata          jsonb
);

-- =============================================================================
-- audit_log
-- =============================================================================
CREATE TABLE audit_log (
    id            bigserial   PRIMARY KEY,
    org_id        uuid        NOT NULL REFERENCES organizations(id),
    actor_type    text        NOT NULL,
    actor_id      text        NOT NULL,
    action        text        NOT NULL,
    resource_type text        NOT NULL,
    resource_id   uuid,
    details       jsonb,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_log_org_created ON audit_log (org_id, created_at DESC);
CREATE INDEX idx_audit_log_resource ON audit_log (org_id, resource_type, resource_id);

-- immutability trigger
CREATE OR REPLACE FUNCTION prevent_audit_log_modification()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % operations are not allowed', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_immutable
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW
    EXECUTE FUNCTION prevent_audit_log_modification();
