-- PagerDuty is a first-class issue source and automation event provider.
ALTER TABLE integrations
    DROP CONSTRAINT IF EXISTS chk_integrations_provider;

ALTER TABLE integrations
    ADD CONSTRAINT chk_integrations_provider CHECK (provider IN (
        'github', 'sentry', 'linear', 'slack', 'notion', 'circleci', 'mezmo', 'victorialogs', 'pagerduty'
    )) NOT VALID;

ALTER TABLE integrations VALIDATE CONSTRAINT chk_integrations_provider;

ALTER TABLE issues
    DROP CONSTRAINT IF EXISTS chk_issues_source;

ALTER TABLE issues
    ADD CONSTRAINT chk_issues_source CHECK (source IN (
        'sentry', 'linear', 'pagerduty', 'manual', 'pm_agent'
    )) NOT VALID;

ALTER TABLE issues VALIDATE CONSTRAINT chk_issues_source;

CREATE TABLE pagerduty_integrations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    integration_id uuid REFERENCES integrations(id),

    account_subdomain text,
    service_region text NOT NULL DEFAULT 'us',
    oauth_mode text NOT NULL DEFAULT 'scoped',
    credential_ref text NOT NULL,
    webhook_secret_ref text,
    status text NOT NULL DEFAULT 'active',
    scopes text[] NOT NULL DEFAULT '{}',
    last_synced_at timestamptz,
    last_health_check_at timestamptz,
    last_error text,

    default_repository_id uuid REFERENCES repositories(id),
    writeback_enabled boolean NOT NULL DEFAULT true,
    auto_create_webhook boolean NOT NULL DEFAULT false,

    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,

    CONSTRAINT chk_pagerduty_integrations_oauth_mode CHECK (oauth_mode IN ('scoped', 'classic_user')),
    CONSTRAINT chk_pagerduty_integrations_status CHECK (status IN ('active', 'degraded', 'inactive')),
    CONSTRAINT chk_pagerduty_integrations_service_region CHECK (service_region IN ('us', 'eu'))
);

CREATE UNIQUE INDEX idx_pagerduty_integrations_org_account_active
    ON pagerduty_integrations (org_id, COALESCE(account_subdomain, ''), service_region)
    WHERE deleted_at IS NULL;

CREATE TABLE pagerduty_service_repo_mappings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    pagerduty_integration_id uuid NOT NULL REFERENCES pagerduty_integrations(id),

    pagerduty_service_id text NOT NULL,
    pagerduty_service_name text NOT NULL,
    pagerduty_team_id text,
    repository_id uuid NOT NULL REFERENCES repositories(id),
    base_branch text,
    enabled boolean NOT NULL DEFAULT true,

    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    UNIQUE (org_id, pagerduty_integration_id, pagerduty_service_id)
);

CREATE INDEX idx_pagerduty_service_repo_mappings_repo
    ON pagerduty_service_repo_mappings (org_id, repository_id)
    WHERE enabled = true;

CREATE TABLE pagerduty_incidents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    pagerduty_integration_id uuid NOT NULL REFERENCES pagerduty_integrations(id),
    issue_id uuid REFERENCES issues(id),

    incident_id text NOT NULL,
    incident_number bigint,
    html_url text,
    title text NOT NULL,
    status text NOT NULL,
    urgency text,
    priority_id text,
    priority_name text,
    service_id text,
    service_name text,
    escalation_policy_id text,
    escalation_policy_name text,
    incident_type text,
    assigned_user_ids text[] NOT NULL DEFAULT '{}',
    team_ids text[] NOT NULL DEFAULT '{}',
    latest_note text,
    raw_data jsonb NOT NULL DEFAULT '{}'::jsonb,

    triggered_at timestamptz,
    acknowledged_at timestamptz,
    resolved_at timestamptz,
    last_event_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    UNIQUE (org_id, pagerduty_integration_id, incident_id)
);

CREATE INDEX idx_pagerduty_incidents_org_status
    ON pagerduty_incidents (org_id, status, last_event_at DESC);
CREATE INDEX idx_pagerduty_incidents_service
    ON pagerduty_incidents (org_id, service_id, last_event_at DESC);
CREATE INDEX idx_pagerduty_incidents_issue
    ON pagerduty_incidents (org_id, issue_id)
    WHERE issue_id IS NOT NULL;

CREATE TABLE pagerduty_inbound_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    pagerduty_integration_id uuid REFERENCES pagerduty_integrations(id),
    webhook_delivery_id uuid REFERENCES webhook_deliveries(id),

    provider_event_id text NOT NULL,
    event_type text NOT NULL,
    resource_type text,
    incident_id text,
    occurred_at timestamptz,
    payload jsonb NOT NULL,
    headers jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'received',
    error_message text,

    created_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,

    UNIQUE (org_id, provider_event_id),
    CONSTRAINT chk_pagerduty_inbound_events_status CHECK (status IN ('received', 'processed', 'failed', 'ignored'))
);

CREATE INDEX idx_pagerduty_inbound_events_incident
    ON pagerduty_inbound_events (org_id, incident_id, occurred_at DESC);
CREATE INDEX idx_pagerduty_inbound_events_status
    ON pagerduty_inbound_events (org_id, status, created_at ASC);

CREATE TABLE automation_event_triggers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    automation_id uuid NOT NULL REFERENCES automations(id) ON DELETE CASCADE,

    provider text NOT NULL,
    event_types text[] NOT NULL DEFAULT '{}',
    filter jsonb NOT NULL DEFAULT '{}'::jsonb,
    repository_id uuid REFERENCES repositories(id),
    enabled boolean NOT NULL DEFAULT true,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT chk_automation_event_triggers_provider CHECK (provider IN ('pagerduty', 'github')),
    CONSTRAINT chk_automation_event_triggers_nonempty_events CHECK (array_length(event_types, 1) IS NOT NULL)
);

CREATE INDEX idx_automation_event_triggers_provider
    ON automation_event_triggers (org_id, provider)
    WHERE enabled = true;
CREATE INDEX idx_automation_event_triggers_automation
    ON automation_event_triggers (org_id, automation_id);

ALTER TABLE automation_runs
    ADD COLUMN trigger_id uuid REFERENCES automation_event_triggers(id),
    ADD COLUMN provider text,
    ADD COLUMN provider_event_id text,
    ADD COLUMN trigger_context jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE automation_runs
    DROP CONSTRAINT IF EXISTS chk_automation_runs_triggered_by;

ALTER TABLE automation_runs
    ADD CONSTRAINT chk_automation_runs_triggered_by CHECK (triggered_by IN ('schedule', 'manual', 'github', 'provider_event'));

ALTER TABLE automation_runs
    ADD CONSTRAINT chk_automation_runs_provider CHECK (provider IS NULL OR provider IN ('pagerduty', 'github'));

CREATE UNIQUE INDEX idx_automation_runs_provider_event
    ON automation_runs (automation_id, provider, provider_event_id)
    WHERE provider_event_id IS NOT NULL;
