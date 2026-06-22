DROP INDEX IF EXISTS idx_automation_runs_provider_event;

ALTER TABLE automation_runs
    DROP CONSTRAINT IF EXISTS chk_automation_runs_provider;

ALTER TABLE automation_runs
    DROP CONSTRAINT IF EXISTS chk_automation_runs_triggered_by;

ALTER TABLE automation_runs
    ADD CONSTRAINT chk_automation_runs_triggered_by CHECK (triggered_by IN ('schedule', 'manual', 'github'));

ALTER TABLE automation_runs
    DROP COLUMN IF EXISTS trigger_context,
    DROP COLUMN IF EXISTS provider_event_id,
    DROP COLUMN IF EXISTS provider,
    DROP COLUMN IF EXISTS trigger_id;

DROP INDEX IF EXISTS idx_automation_event_triggers_automation;
DROP INDEX IF EXISTS idx_automation_event_triggers_provider;
DROP TABLE IF EXISTS automation_event_triggers;

DROP INDEX IF EXISTS idx_pagerduty_inbound_events_status;
DROP INDEX IF EXISTS idx_pagerduty_inbound_events_incident;
DROP TABLE IF EXISTS pagerduty_inbound_events;

DROP INDEX IF EXISTS idx_pagerduty_incidents_issue;
DROP INDEX IF EXISTS idx_pagerduty_incidents_service;
DROP INDEX IF EXISTS idx_pagerduty_incidents_org_status;
DROP TABLE IF EXISTS pagerduty_incidents;

DROP INDEX IF EXISTS idx_pagerduty_service_repo_mappings_repo;
DROP TABLE IF EXISTS pagerduty_service_repo_mappings;

DROP INDEX IF EXISTS idx_pagerduty_integrations_org_account_active;
DROP TABLE IF EXISTS pagerduty_integrations;

ALTER TABLE issues
    DROP CONSTRAINT IF EXISTS chk_issues_source;

ALTER TABLE issues
    ADD CONSTRAINT chk_issues_source CHECK (source IN (
        'sentry', 'linear', 'manual', 'pm_agent'
    )) NOT VALID;

ALTER TABLE issues VALIDATE CONSTRAINT chk_issues_source;

ALTER TABLE integrations
    DROP CONSTRAINT IF EXISTS chk_integrations_provider;

ALTER TABLE integrations
	ADD CONSTRAINT chk_integrations_provider CHECK (provider IN (
	    'github', 'sentry', 'linear', 'slack', 'notion', 'circleci', 'mezmo', 'victorialogs'
	)) NOT VALID;

ALTER TABLE integrations VALIDATE CONSTRAINT chk_integrations_provider;
