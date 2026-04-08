-- Container usage events for billing observability.
-- Each row records a single container lifecycle (create → destroy) with its
-- resource allocation and computed duration in minutes.
CREATE TABLE container_usage_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id),
    session_id      UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    container_id    TEXT NOT NULL,
    provider        TEXT NOT NULL DEFAULT 'docker',

    -- Resource allocation at creation time
    cpu_limit       DOUBLE PRECISION NOT NULL DEFAULT 2,
    memory_limit_mb INT              NOT NULL DEFAULT 4096,
    image           TEXT             NOT NULL DEFAULT '143-sandbox:latest',

    -- Lifecycle timestamps
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    stopped_at      TIMESTAMPTZ,

    -- Computed billing fields (populated on stop)
    duration_ms     BIGINT,           -- wall-clock milliseconds the container ran
    container_minutes DOUBLE PRECISION, -- duration_ms / 60000, for billing convenience

    -- Exit metadata
    exit_reason     TEXT,             -- "completed", "failed", "reaped", "timeout"

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Index for org-level usage queries (billing dashboards, aggregation)
CREATE INDEX idx_container_usage_events_org_started
    ON container_usage_events (org_id, started_at DESC);

-- Index for session-level lookups
CREATE INDEX idx_container_usage_events_session
    ON container_usage_events (session_id);

-- Partial index for orphan cleanup (stopped_at IS NULL) — avoids full table scan
CREATE INDEX idx_container_usage_events_orphans
    ON container_usage_events (started_at) WHERE stopped_at IS NULL;
