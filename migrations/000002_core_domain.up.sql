-- =============================================================================
-- webhook_deliveries
-- =============================================================================
CREATE TABLE webhook_deliveries (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid        NOT NULL REFERENCES organizations(id),
    integration_id   uuid        NOT NULL REFERENCES integrations(id),
    provider         text        NOT NULL,
    delivery_id      text,
    event_type       text        NOT NULL,
    signature_valid  boolean,
    received_at      timestamptz NOT NULL DEFAULT now(),
    processed_at     timestamptz,
    status           text        NOT NULL DEFAULT 'received',
    attempts         int         NOT NULL DEFAULT 0,
    error            text,
    payload          jsonb,
    headers          jsonb,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_webhook_deliveries_org_received ON webhook_deliveries (org_id, received_at DESC);
CREATE INDEX idx_webhook_deliveries_integration ON webhook_deliveries (integration_id, received_at DESC);
CREATE UNIQUE INDEX idx_webhook_deliveries_idempotency ON webhook_deliveries (provider, delivery_id) WHERE delivery_id IS NOT NULL;
CREATE INDEX idx_webhook_deliveries_status ON webhook_deliveries (status, received_at);

-- =============================================================================
-- integration_sync_runs
-- =============================================================================
CREATE TABLE integration_sync_runs (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid        NOT NULL REFERENCES organizations(id),
    integration_id    uuid        NOT NULL REFERENCES integrations(id),
    started_at        timestamptz NOT NULL DEFAULT now(),
    completed_at      timestamptz,
    status            text        NOT NULL DEFAULT 'running',
    issues_fetched    int         NOT NULL DEFAULT 0,
    issues_upserted   int         NOT NULL DEFAULT 0,
    events_inserted   int         NOT NULL DEFAULT 0,
    api_calls         int         NOT NULL DEFAULT 0,
    rate_limited_count int        NOT NULL DEFAULT 0,
    error             text,
    metadata          jsonb,
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_sync_runs_integration ON integration_sync_runs (integration_id, started_at DESC);
CREATE INDEX idx_sync_runs_org ON integration_sync_runs (org_id, started_at DESC);
CREATE INDEX idx_sync_runs_status ON integration_sync_runs (status, started_at DESC);

-- =============================================================================
-- issues
-- =============================================================================
CREATE TABLE issues (
    id                      uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                  uuid        NOT NULL REFERENCES organizations(id),
    external_id             text        NOT NULL,
    source                  text        NOT NULL,
    source_integration_id   uuid        REFERENCES integrations(id),
    repository_id           uuid        REFERENCES repositories(id),
    title                   text        NOT NULL,
    description             text,
    raw_data                jsonb,
    status                  text        NOT NULL DEFAULT 'open',
    first_seen_at           timestamptz NOT NULL DEFAULT now(),
    last_seen_at            timestamptz NOT NULL DEFAULT now(),
    occurrence_count        int         NOT NULL DEFAULT 1,
    affected_customer_count int         NOT NULL DEFAULT 0,
    severity                text        NOT NULL DEFAULT 'medium',
    tags                    text[],
    fingerprint             text        NOT NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_issues_fingerprint ON issues (org_id, fingerprint);
CREATE UNIQUE INDEX idx_issues_source_external ON issues (org_id, source, external_id);
CREATE INDEX idx_issues_status ON issues (org_id, status);
CREATE INDEX idx_issues_last_seen ON issues (org_id, last_seen_at DESC);
CREATE INDEX idx_issues_repository ON issues (repository_id);

-- =============================================================================
-- issue_events
-- =============================================================================
CREATE TABLE issue_events (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id    uuid        NOT NULL REFERENCES issues(id),
    external_id text,
    event_type  text        NOT NULL,
    data        jsonb,
    customer_id text,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_issue_events_issue ON issue_events (issue_id, occurred_at DESC);

-- =============================================================================
-- priority_scores
-- =============================================================================
CREATE TABLE priority_scores (
    id                    uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id              uuid        NOT NULL REFERENCES issues(id) UNIQUE,
    org_id                uuid        NOT NULL REFERENCES organizations(id),
    score                 float       NOT NULL DEFAULT 0,
    customer_impact_score float       NOT NULL DEFAULT 0,
    severity_score        float       NOT NULL DEFAULT 0,
    recency_score         float       NOT NULL DEFAULT 0,
    revenue_risk_score    float       NOT NULL DEFAULT 0,
    direction_alignment   float       NOT NULL DEFAULT 0,
    factors               jsonb,
    eligible_for_agent    boolean     NOT NULL DEFAULT true,
    computed_at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_priority_scores_top ON priority_scores (org_id, score DESC);
CREATE INDEX idx_priority_scores_eligible ON priority_scores (org_id, eligible_for_agent, score DESC);

-- =============================================================================
-- complexity_estimates
-- =============================================================================
CREATE TABLE complexity_estimates (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id         uuid        NOT NULL REFERENCES issues(id) UNIQUE,
    org_id           uuid        NOT NULL REFERENCES organizations(id),
    tier             int         NOT NULL,
    label            text        NOT NULL,
    confidence       float       NOT NULL DEFAULT 0,
    issue_type       text,
    reasoning        text,
    estimated_files  text[],
    estimated_tokens int,
    model_used       text,
    computed_at      timestamptz NOT NULL DEFAULT now(),
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_complexity_org_tier ON complexity_estimates (org_id, tier);

-- =============================================================================
-- agent_runs
-- =============================================================================
CREATE TABLE agent_runs (
    id                    uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    issue_id              uuid        NOT NULL REFERENCES issues(id),
    org_id                uuid        NOT NULL REFERENCES organizations(id),
    agent_type            text        NOT NULL DEFAULT 'claude_code',
    status                text        NOT NULL DEFAULT 'pending',
    autonomy_level        text        NOT NULL DEFAULT 'manual',
    token_mode            text        NOT NULL DEFAULT 'low',
    complexity_tier       int,
    container_id          text,
    started_at            timestamptz,
    completed_at          timestamptz,
    token_usage           jsonb,
    failure_explanation   text,
    failure_category      text,
    failure_next_steps    text[],
    failure_retry_advised boolean,
    parent_run_id         uuid        REFERENCES agent_runs(id),
    revision_context      jsonb,
    error                 text,
    result_summary        text,
    diff                  text,
    created_at            timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_agent_runs_org_status ON agent_runs (org_id, status, created_at DESC);
CREATE INDEX idx_agent_runs_issue ON agent_runs (org_id, issue_id);
CREATE INDEX idx_agent_runs_org_created ON agent_runs (org_id, created_at DESC);
CREATE INDEX idx_agent_runs_parent ON agent_runs (parent_run_id) WHERE parent_run_id IS NOT NULL;

-- =============================================================================
-- agent_run_logs
-- =============================================================================
CREATE TABLE agent_run_logs (
    id           bigserial   PRIMARY KEY,
    agent_run_id uuid        NOT NULL REFERENCES agent_runs(id),
    timestamp    timestamptz NOT NULL DEFAULT now(),
    level        text        NOT NULL DEFAULT 'info',
    message      text        NOT NULL,
    metadata     jsonb
);

CREATE INDEX idx_agent_run_logs_run ON agent_run_logs (agent_run_id, timestamp);

-- =============================================================================
-- agent_run_questions
-- =============================================================================
CREATE TABLE agent_run_questions (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_run_id  uuid        NOT NULL REFERENCES agent_runs(id),
    org_id        uuid        NOT NULL REFERENCES organizations(id),
    question_text text        NOT NULL,
    options       text[],
    context       text,
    blocks_phase  text,
    answer_text   text,
    answered_by   uuid        REFERENCES users(id),
    answered_at   timestamptz,
    status        text        NOT NULL DEFAULT 'pending',
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_agent_run_questions_run ON agent_run_questions (agent_run_id, created_at);
CREATE INDEX idx_agent_run_questions_pending ON agent_run_questions (org_id, status);

-- =============================================================================
-- validations
-- =============================================================================
CREATE TABLE validations (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_run_id         uuid        NOT NULL REFERENCES agent_runs(id),
    org_id               uuid        NOT NULL REFERENCES organizations(id),
    status               text        NOT NULL DEFAULT 'pending',
    direction_check      text        NOT NULL DEFAULT 'skip',
    correctness_check    text        NOT NULL DEFAULT 'skip',
    quality_check        text        NOT NULL DEFAULT 'skip',
    security_scan        text        NOT NULL DEFAULT 'skip',
    regression_test_check text       NOT NULL DEFAULT 'skip',
    coverage_delta       jsonb,
    ci_check             text        NOT NULL DEFAULT 'skip',
    details              jsonb,
    started_at           timestamptz,
    completed_at         timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_validations_run ON validations (agent_run_id);
CREATE INDEX idx_validations_org ON validations (org_id, status);

-- =============================================================================
-- pull_requests
-- =============================================================================
CREATE TABLE pull_requests (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_run_id     uuid        NOT NULL REFERENCES agent_runs(id),
    org_id           uuid        NOT NULL REFERENCES organizations(id),
    github_pr_number int         NOT NULL,
    github_pr_url    text        NOT NULL,
    github_repo      text        NOT NULL,
    title            text        NOT NULL,
    body             text,
    status           text        NOT NULL DEFAULT 'open',
    review_status    text        NOT NULL DEFAULT 'pending',
    merged_at        timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_pull_requests_run ON pull_requests (agent_run_id);
CREATE INDEX idx_pull_requests_org ON pull_requests (org_id, status);

-- =============================================================================
-- deploys
-- =============================================================================
CREATE TABLE deploys (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    pull_request_id  uuid        NOT NULL REFERENCES pull_requests(id),
    org_id           uuid        NOT NULL REFERENCES organizations(id),
    environment      text        NOT NULL DEFAULT 'production',
    deployed_at      timestamptz NOT NULL DEFAULT now(),
    commit_sha       text,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_deploys_pr ON deploys (pull_request_id);
CREATE INDEX idx_deploys_org ON deploys (org_id, deployed_at DESC);
