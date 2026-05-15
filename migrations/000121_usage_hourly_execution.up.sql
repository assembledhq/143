CREATE TABLE usage_hourly_execution (
    org_id UUID NOT NULL REFERENCES organizations(id),
    hour_utc TIMESTAMPTZ NOT NULL,
    agent_type TEXT NOT NULL,
    model_used TEXT NOT NULL,
    reasoning_effort TEXT NOT NULL,
    capacity_key TEXT NOT NULL,
    total_container_minutes DOUBLE PRECISION NOT NULL DEFAULT 0,
    total_sessions INT NOT NULL DEFAULT 0,
    total_container_starts INT NOT NULL DEFAULT 0,
    peak_concurrent INT NOT NULL DEFAULT 0,
    total_input_tokens BIGINT NOT NULL DEFAULT 0,
    total_output_tokens BIGINT NOT NULL DEFAULT 0,
    total_tokens BIGINT NOT NULL DEFAULT 0,
    total_llm_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, hour_utc, agent_type, model_used, reasoning_effort, capacity_key)
);

CREATE INDEX idx_usage_hourly_execution_org_hour
    ON usage_hourly_execution (org_id, hour_utc DESC);

CREATE INDEX idx_usage_hourly_execution_org_agent_hour
    ON usage_hourly_execution (org_id, agent_type, hour_utc DESC);

CREATE INDEX idx_usage_hourly_execution_org_model_hour
    ON usage_hourly_execution (org_id, model_used, hour_utc DESC);

CREATE INDEX idx_usage_hourly_execution_org_reasoning_hour
    ON usage_hourly_execution (org_id, reasoning_effort, hour_utc DESC);
