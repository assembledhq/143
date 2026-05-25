-- Pre-aggregated hourly usage rollup table for the billing & usage dashboard.
-- Populated by a periodic background job; raw events remain the source of truth.
CREATE TABLE usage_hourly (
    id                        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                    UUID NOT NULL REFERENCES organizations(id),
    hour_utc                  TIMESTAMPTZ NOT NULL,  -- truncated to hour, always UTC

    -- Dimensional keys (NULL = "all" for that dimension)
    user_id                   UUID REFERENCES users(id),
    capacity_tier             TEXT,           -- e.g. "2cpu_4096mb_10240diskmb", NULL = all tiers

    -- Container aggregates
    total_container_minutes   DOUBLE PRECISION NOT NULL DEFAULT 0,
    total_sessions            INT              NOT NULL DEFAULT 0,
    total_container_starts    INT              NOT NULL DEFAULT 0,
    peak_concurrent           INT              NOT NULL DEFAULT 0,
    avg_duration_sec          DOUBLE PRECISION NOT NULL DEFAULT 0,
    p95_duration_sec          DOUBLE PRECISION NOT NULL DEFAULT 0,

    -- LLM token aggregates
    total_input_tokens        BIGINT NOT NULL DEFAULT 0,
    total_output_tokens       BIGINT NOT NULL DEFAULT 0,
    total_llm_cost_usd        DOUBLE PRECISION NOT NULL DEFAULT 0,

    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Unique index using COALESCE so that NULL user_id / capacity_tier values
-- participate in conflict detection (plain UNIQUE treats NULL != NULL).
CREATE UNIQUE INDEX idx_usage_hourly_upsert
    ON usage_hourly (
        org_id,
        hour_utc,
        COALESCE(user_id, '00000000-0000-0000-0000-000000000000'),
        COALESCE(capacity_tier, '')
    );

-- Org-level and tier-level queries (user_id IS NULL)
CREATE INDEX idx_usage_hourly_org_hour
    ON usage_hourly (org_id, hour_utc DESC);

-- Per-user queries (user_id IS NOT NULL)
CREATE INDEX idx_usage_hourly_org_user_hour_nonnull
    ON usage_hourly (org_id, user_id, hour_utc DESC)
    WHERE user_id IS NOT NULL;

-- Per-tier queries (user_id IS NULL, capacity_tier IS NOT NULL)
CREATE INDEX idx_usage_hourly_org_tier_hour
    ON usage_hourly (org_id, capacity_tier, hour_utc DESC)
    WHERE user_id IS NULL AND capacity_tier IS NOT NULL;
