# Design: PagerDuty Integration

> **Status:** Implemented V1 | **Last reviewed:** 2026-06-20

## Summary

PagerDuty should become a first-class incident source and automation trigger for
143. The integration should turn PagerDuty incidents into normalized 143 issues,
allow teams to start coding sessions from an incident, and let automations run
when selected incident conditions are met.

## Implementation Status

The v1 path is implemented: PagerDuty integration records and encrypted
credentials, OAuth/connect flows, service discovery, service-to-repository
mappings, webhook subscription setup, signed/shared-secret webhook ingress,
inbound event durability, incident-to-issue normalization,
`pagerduty_incidents` mirroring, periodic reconciliation polling, authenticated
and PagerDuty-side manual session start, PagerDuty event-triggered automation
runs, settings UI with OAuth install/reauthorize and PagerDuty setup endpoints,
automation composer UI, the sandbox `143-tools pagerduty` namespace with
write tools gated by the integration writeback setting, audit/metrics coverage,
health surfacing for webhook/writeback failures, and concise writebacks for
session start, non-no-op automation completion/failure, and PR creation.

Remaining work is product polish rather than a design gap: richer dedicated
incident dashboards, service-mapping suggestions, configurable annotation
commands, and broader PagerDuty-side mutation policies.

The product shape should be:

- PagerDuty incidents are ingested into the existing `issues` pipeline.
- Incidents can start normal 143 sessions, either automatically through
  automation triggers or manually through a PagerDuty action.
- Automatic incident triggers are a first-class v1 path: teams can map
  PagerDuty services to repos, configure incident filters, and have 143 start
  work as soon as matching incidents arrive.
- Manual PagerDuty actions remain available for ad hoc incidents, unmapped
  services, and responder-controlled follow-up work.
- Automation runs preserve a snapshot of the PagerDuty incident context, then
  create sessions through the same durable execution path as scheduled
  automations.
- Sandbox agents get a compact `143-tools pagerduty ...` surface for incident,
  service, on-call, notes, and post-incident context.
- PagerDuty remains the incident-response source of truth; 143 owns coding
  sessions, diffs, previews, PRs, and automation run history.

## Research Notes

PagerDuty's current AI/developer integrations point at a clear pattern:
operational context should be available inside coding-agent workflows, and active
incidents should be able to trigger investigation or remediation work.

- PagerDuty MCP supports hosted and self-hosted modes and lets AI tools retrieve
  incident data, manage services, and update on-call schedules. Cursor's
  PagerDuty plugin connects to the hosted MCP endpoint and exposes incident,
  on-call, service, and service-management context inside the IDE.
- PagerDuty's Claude Code plugin focuses on pre-commit risk scoring. It compares
  local diffs against recent incident history and returns a risk score with
  recommendations before code is committed.
- PagerDuty's AI ecosystem announcement calls out Cursor automatically triggering
  agents to investigate logs and summarize likely root causes when incidents
  occur.
- OpenAI Codex supports MCP servers in the CLI and IDE extension, and Codex
  plugins can bundle skills, app integrations, and MCP server configuration.
  I did not find a first-party OpenAI/PagerDuty Codex integration in the
  official Codex docs; the applicable Codex pattern is packaging PagerDuty MCP
  or a 143-owned PagerDuty skill/tool surface into the agent runtime.
- PagerDuty V3 webhooks support incident events such as triggered,
  acknowledged, reassigned, escalated, priority updated, annotated, reopened,
  resolved, status updates, and workflow started/completed.
- PagerDuty Custom Incident Actions let responders add incident-page buttons
  that POST incident context to an external endpoint. This is the right fit for a
  manual `Start 143 session` action.
- PagerDuty Incident Workflows support incident type, conditional, manual, API,
  and integration triggers. Workflow Integrations include a Web API connection
  model, which is the right fit for customers who want PagerDuty-side workflows
  to call into 143.

Sources checked:

- https://support.pagerduty.com/main/docs/pagerduty-mcp-server
- https://github.com/PagerDuty/cursor-plugin
- https://support.pagerduty.com/main/changelog/pagerduty-plug-in-for-claude-code-now-generally-available
- https://www.pagerduty.com/newsroom/pagerduty-expands-ai-ecosystem-to-supercharge-ai-agents/
- https://developers.openai.com/codex/mcp
- https://developers.openai.com/codex/plugins
- https://support.pagerduty.com/main/docs/webhooks
- https://support.pagerduty.com/main/docs/custom-incident-actions
- https://support.pagerduty.com/main/docs/incident-workflows
- https://support.pagerduty.com/main/docs/workflow-integrations

## Goals

- Ingest PagerDuty incidents as normalized 143 issues with stable dedupe and
  useful severity/status mapping.
- Let users create 143 sessions from PagerDuty incidents from either 143 or
  PagerDuty.
- Add PagerDuty incident events as automation triggers, analogous to GitHub PR
  event entry points.
- Make automatic incident-started sessions useful and trustworthy by requiring
  explicit service/repo mapping, clear trigger filters, run history, and concise
  PagerDuty writeback.
- Preserve PagerDuty incident context on issue links, automation runs, and
  session prompts so agents can reason from the exact incident that triggered
  work.
- Let agents query PagerDuty operational context through `143-tools` without
  bypassing 143 auth, audit, and tenancy.
- Optionally write concise progress back to PagerDuty as notes and/or status
  updates.

## V1 Product Slice

The highest-leverage v1 is an automatic incident-to-session loop that improves
on existing Cursor-style incident triggers while staying inside 143's team-owned
automation, run-history, PR, and preview model:

1. An admin installs PagerDuty through OAuth and maps critical PagerDuty
   services to repositories.
2. The team creates one or more PagerDuty incident triggers on automations, such
   as "P1/P2 incident on API service" or "incident annotated with `143:fix`."
3. When a matching incident arrives, 143 upserts the issue, snapshots the
   incident context, creates an automation run, and starts a normal session.
4. The agent investigates with PagerDuty context, linked logs/tools, and repo
   context, then opens a PR or produces a clear diagnostic summary.
5. 143 writes concise status back to PagerDuty and keeps canonical run/session
   history in 143.

Manual `Start 143 session` remains important, but it is a companion path for
unmapped services, one-off incidents, and human-directed follow-up. The core v1
product should make automatic triggers visible, configurable, and better
governed than a personal IDE-level trigger.

## Non-Goals

- Do not replace PagerDuty incident management, responder routing, escalation,
  or incident workflow orchestration.
- Do not auto-resolve PagerDuty incidents in v1. A 143 PR may help resolve an
  incident, but incident state changes remain human-controlled unless a later
  explicit policy is designed.
- Do not expose broad arbitrary PagerDuty mutation tools to sandbox agents in
  v1.
- Do not build on PagerDuty MCP as the product control plane. Use native REST
  client calls for durable product workflows and expose a small 143-owned CLI
  surface to agents.
- Do not require every PagerDuty service to map to a repo before ingestion.
  Unmapped incidents should still appear as issues and can be mapped later.

## Product Decisions

### PagerDuty incidents become issues

PagerDuty should plug into the existing ingestion model from
[04-ingestion.md](04-ingestion.md). A PagerDuty incident is a
source issue with:

- `source = "pagerduty"`
- `external_id = incident.id`
- `fingerprint = "pagerduty:" + incident.id`
- `title = incident.title` or incident summary
- `description = incident summary + service + urgency + latest notes`
- `severity` derived from priority/urgency
- `tags` derived from service, escalation policy, teams, priority, incident
  type, and custom fields
- `raw_data` preserving the sanitized webhook/API incident payload

Issue state should mirror PagerDuty incident status enough for prioritization:
triggered and acknowledged are active; resolved is closed/resolved; reopened
re-activates the issue.

### OAuth is the integration auth model

PagerDuty should use OAuth from v1, not account-level API tokens. The long-term
product needs admin-friendly installation, least-privilege scopes, revocation,
health checks, marketplace readiness, and clear audit attribution. Supporting
API keys first would create a second credential path that we would later need to
migrate away from.

Implementation should prefer PagerDuty Scoped OAuth when the required incident,
service, webhook, note, and status-update endpoints support it. If a required
endpoint only supports Classic User OAuth, the integration may temporarily use
Classic User OAuth, but the install/status UI should label that clearly and the
stored `scopes` should preserve the effective permission set. Do not expose
API-token setup in the normal product UI.

### Use REST for 143 workflows, not MCP

MCP is useful inspiration for the agent experience: it shows that responders and
coding agents benefit from quick access to incident, service, on-call, and notes
context. It is not the right foundation for 143's durable product workflows.

143 should call PagerDuty's REST API directly for ingestion enrichment,
polling, service mapping, manual incident starts, writeback notes, and
automation-triggered sessions because those paths need:

- stable typed errors and retries,
- per-org credential ownership and health,
- webhook/event idempotency,
- bounded request/response logging,
- explicit least-privilege scopes,
- tests around provider payload normalization,
- no dependency on an LLM tool protocol for backend correctness.

PagerDuty MCP can still be useful later as an optional sandbox-adjacent tool if
customers already use it, but 143-owned `143-tools pagerduty ...` commands
should be the default agent-facing surface so auth, audit, redaction, and
capability policy stay inside 143.

### Sessions stay canonical in 143

Starting from an incident creates a normal session with a primary PagerDuty
issue link. The session prompt should contain a structured incident block:

```text
PagerDuty incident
ID: PABC123
Number: 42
Status: triggered
Urgency: high
Priority: P1
Service: api
Escalation policy: Core Platform
Incident URL: https://...
Created: 2026-06-19T12:34:56Z
Latest notes:
- ...
```

The prompt should instruct the agent to investigate, produce a fix or a clear
diagnostic summary, and avoid mutating PagerDuty state unless the tool surface
explicitly supports the requested action.

### Automations get a thin event-trigger abstraction

Current automations support scheduled and manual runs. PagerDuty needs a
provider-event trigger model that can later serve GitHub PR events, Slack
events, and other integrations without adding provider-specific columns to
`automations`.

This abstraction is worth adding now because event-triggered PagerDuty
automations are a core requirement, and the existing `automations` table cannot
represent provider event type, filters, idempotency, or trigger context without
becoming a pile of nullable provider columns. Keep the abstraction narrow:
declarative provider/event/filter rows that match an inbound event and create an
`automation_run`. Do not build a general workflow/rules engine, cross-provider
boolean logic, branching actions, or transformation DSL in v1.

Do not migrate schedules or manual runs into this table in v1. Scheduled
automations already have a working model, and manual runs are explicit product
actions. The new table is only for external provider events.

The core idea:

- `automations` still owns the goal, repo, model, identity, pause state, and run
  history.
- `automation_event_triggers` declares which external provider events can start
  an automation run.
- `automation_runs` records the triggering event and context snapshot.
- Provider events are deduped before matching triggers.

PagerDuty should never create an implicit catch-all automatic trigger. Automatic
triggers are enabled when an admin/member creates them, and each trigger should
require explicit service plus urgency or priority filters. This preserves the
automatic incident-started workflow while preventing broad "all incidents"
automation from surprising responders or spending agent capacity during noisy
incident periods.

Incident-triggered automations should use the existing automation
`max_concurrent` control and should default to `max_concurrent = 1`. If multiple
matching incidents arrive while a run is active, later events should create
skipped or suppressed run records with a clear reason rather than launching
parallel sessions by surprise.

Do not automatically pause unrelated scheduled automations when a PagerDuty
incident fires. Deploy freezes and incident-mode pauses should use the existing
bulk pause controls or a future explicit org policy; coupling incident triggers
to scheduler behavior would make automation state harder to reason about.

Example automations:

- "When a P1/P2 incident opens on the API service, investigate likely root cause
  and open a PR if the fix is straightforward."
- "When an incident is resolved, create a follow-up issue or PR for durable
  cleanup if the incident notes mention a workaround."
- "When a PagerDuty incident is annotated with `143:fix`, start a coding
  session with the incident and notes as context."

### Manual PagerDuty action is a first-class path

Alongside automatic triggers, v1 should support a Custom Incident Action named
`Start 143 session`. Clicking it in PagerDuty sends a signed/custom-header POST
to 143. 143 resolves the org and incident, upserts the issue, creates or reuses
a session, and optionally posts a PagerDuty note with the session URL.

This path maps well to incident-response expectations: responders stay in
PagerDuty, explicitly choose when to involve 143, and get a link back to the
canonical session.

## Architecture

```text
PagerDuty OAuth setup
  -> pagerduty_integrations + encrypted credential
  -> service/team/repo mappings

PagerDuty V3 webhook / custom incident action / workflow Web API action
  -> verify signature or configured shared secret
  -> webhook_deliveries(provider='pagerduty')
  -> pagerduty_inbound_events
  -> ingest_pagerduty_event job
  -> upsert issue + incident mirror
  -> match automation_event_triggers
  -> automation_runs + sessions + run_agent jobs
  -> optional PagerDuty note/status update

Sandbox agent
  -> 143-tools pagerduty ...
  -> backend tool registry
  -> PagerDuty REST API using org-scoped credential
```

## Database Schema

All new tables are tenant-owned and must include `org_id uuid NOT NULL
REFERENCES organizations(id)`.

### `pagerduty_integrations`

Org-scoped install/config record. This can either extend the generic
`integrations` table if the existing model supports provider-specific config, or
be a provider table keyed by `integration_id`.

```sql
CREATE TABLE pagerduty_integrations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    integration_id uuid REFERENCES integrations(id),

    account_subdomain text,
    service_region text NOT NULL DEFAULT 'us',
    oauth_mode text NOT NULL DEFAULT 'scoped', -- 'scoped' | 'classic_user'
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
    deleted_at timestamptz
);

CREATE UNIQUE INDEX idx_pagerduty_integrations_org_account_active
    ON pagerduty_integrations (org_id, account_subdomain, service_region)
    WHERE deleted_at IS NULL;
```

Allow multiple PagerDuty accounts per 143 org. Most customers will install one,
but larger companies may have separate regional, acquired-company, or
environment-specific PagerDuty accounts. A settings UI can keep the common case
simple by showing a single primary install first.

`oauth_mode` and `status` should be typed string model fields with constants and
`Validate() error` tests. `oauth_mode` distinguishes PagerDuty Scoped OAuth from
Classic User OAuth when endpoint support requires it. Do not add account API
keys as a supported product path without a separate design update.

### `pagerduty_service_repo_mappings`

Maps PagerDuty technical services to repositories.

```sql
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
```

Repository resolution order for incident-started work:

1. automation trigger override
2. exact PagerDuty service mapping
3. PagerDuty integration default repository
4. org default work repository
5. fail with a user-actionable unmapped-repo state

### `pagerduty_incidents`

Provider mirror for incident-specific state. The normalized `issues` row remains
the cross-provider work item.

```sql
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
```

Status, urgency, and priority names are external provider values. The normalized
143 issue severity should use existing issue severity constants.

### `pagerduty_inbound_events`

Provider-specific inbound ledger linked to the generic `webhook_deliveries`
durability table.

```sql
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

    UNIQUE (org_id, provider_event_id)
);

CREATE INDEX idx_pagerduty_inbound_events_incident
    ON pagerduty_inbound_events (org_id, incident_id, occurred_at DESC);
```

For Custom Incident Actions that do not provide a stable event ID, compute
`provider_event_id` from action webhook ID, incident ID, requester, and
PagerDuty-supplied timestamp if present. If no timestamp is present, use a short
dedupe window in the job payload rather than claiming permanent idempotency.

### `automation_event_triggers`

General event-trigger table for automations. This is intentionally not
PagerDuty-specific.

```sql
CREATE TABLE automation_event_triggers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    automation_id uuid NOT NULL REFERENCES automations(id) ON DELETE CASCADE,

    provider text NOT NULL, -- 'pagerduty', 'github', ...
    event_types text[] NOT NULL DEFAULT '{}',
    filter jsonb NOT NULL DEFAULT '{}'::jsonb,
    repository_id uuid REFERENCES repositories(id),
    enabled boolean NOT NULL DEFAULT true,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_automation_event_triggers_provider
    ON automation_event_triggers (org_id, provider)
    WHERE enabled = true;
CREATE INDEX idx_automation_event_triggers_automation
    ON automation_event_triggers (org_id, automation_id);
```

PagerDuty trigger filters should support:

```json
{
  "service_ids": ["P123"],
  "team_ids": ["T123"],
  "statuses": ["triggered", "acknowledged", "resolved"],
  "urgencies": ["high"],
  "priority_names": ["P1", "P2"],
  "incident_types": ["security_incident"],
  "title_contains": "checkout",
  "custom_fields": {
    "environment": ["production"]
  }
}
```

Use explicit typed string values for `provider`. Validate provider-specific
filter JSON at write time; invalid keys or impossible combinations should fail
with `400 INVALID_AUTOMATION_TRIGGER_FILTER` rather than being ignored.

### `automation_runs` changes

Extend `automation_runs` rather than adding a provider-specific run table.

```sql
ALTER TABLE automation_runs
    ADD COLUMN trigger_id uuid REFERENCES automation_event_triggers(id),
    ADD COLUMN provider text,
    ADD COLUMN provider_event_id text,
    ADD COLUMN trigger_context jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE UNIQUE INDEX idx_automation_runs_provider_event
    ON automation_runs (automation_id, provider, provider_event_id)
    WHERE provider_event_id IS NOT NULL;
```

For PagerDuty-triggered runs, `trigger_context` stores a sanitized incident
snapshot and the matched trigger/filter information. The automation goal still
goes in `goal_snapshot`.

### `session_issue_link_provider_state`

Add a PagerDuty provider-state shape to the existing session issue link model:

```json
{
  "pagerduty": {
    "incident_id": "PABC123",
    "incident_number": 42,
    "incident_url": "https://...",
    "service_id": "PXYZ",
    "service_name": "api",
    "trigger_event_id": "01H...",
    "writeback_note_ids": ["PNOTE1"]
  }
}
```

No schema change is required if this is already JSONB.

## API Contract

All endpoints are under `/api/v1`, org-scoped, and return existing response
envelopes.

### Browser settings endpoints

Admin-only:

```http
GET    /api/v1/integrations/pagerduty
POST   /api/v1/integrations/pagerduty
PATCH  /api/v1/integrations/pagerduty
DELETE /api/v1/integrations/pagerduty
POST   /api/v1/integrations/pagerduty/test
GET    /api/v1/integrations/pagerduty/services
GET    /api/v1/integrations/pagerduty/webhook-setup
POST   /api/v1/integrations/pagerduty/webhook-setup
```

Member/admin:

```http
GET    /api/v1/integrations/pagerduty/incidents
GET    /api/v1/integrations/pagerduty/incidents/{incident_id}
POST   /api/v1/integrations/pagerduty/incidents/{incident_id}/session
```

`POST /incidents/{incident_id}/session` request:

```json
{
  "repository_id": "00000000-0000-0000-0000-000000000000",
  "base_branch": "main",
  "message": "Investigate the incident, identify likely root cause, and open a PR if a small safe fix is available."
}
```

Response:

```json
{
  "data": {
    "id": "00000000-0000-0000-0000-000000000000",
    "org_id": "00000000-0000-0000-0000-000000000001",
    "primary_issue_id": "00000000-0000-0000-0000-000000000002",
    "status": "pending",
    "origin": "manual"
  }
}
```

Errors:

- `404 NOT_FOUND`
- `409 PAGERDUTY_SESSION_ALREADY_RUNNING`
- `400 REPOSITORY_UNMAPPED`
- `503 PAGERDUTY_UNAVAILABLE`

### Webhook endpoint

```http
POST /api/v1/webhooks/pagerduty?integration_id=<generic-integration-uuid>&pagerduty_integration_id=<pagerduty-install-uuid>
```

Behavior:

- Verify PagerDuty webhook signature or the configured shared header secret.
- Resolve `pagerduty_integration_id` to the exact PagerDuty install and verify
  against that install's credential; generic-only URLs remain supported for
  legacy single-account installs.
- Persist `webhook_deliveries` and `pagerduty_inbound_events`.
- Ack quickly with `200` after persistence and enqueue.
- Return `401` for invalid signatures/secrets.
- Return `500` only when persistence/enqueue fails so PagerDuty retries.

### PagerDuty Custom Incident Action endpoint

```http
POST /api/v1/webhooks/pagerduty/start-session?integration_id=<generic-integration-uuid>&pagerduty_integration_id=<pagerduty-install-uuid>
```

Behavior:

- Verify the same PagerDuty HMAC/shared-header secret before payload parsing.
- Accept a root `incident_id`, a root `incident.id`, or a normal PagerDuty event
  payload and resolve the mirrored incident by PagerDuty incident ID.
- Resolve the repository in this order: explicit `repository_id`, service
  mapping, then the PagerDuty integration default repository.
- Start through the same session starter as the authenticated 143 action, so
  duplicate active sessions return `409 PAGERDUTY_SESSION_ALREADY_RUNNING` and
  successful starts run the normal PagerDuty start writeback hook.
- Return `404 NOT_FOUND` when the incident has not been ingested yet and
  `400 REPOSITORY_UNMAPPED` when no repository can be resolved.

### Automation trigger API changes

Extend automation create/update/read payloads with `event_triggers`.

Create/update request fragment:

```json
{
  "event_triggers": [
    {
      "provider": "pagerduty",
      "event_types": ["incident.triggered", "incident.priority_updated"],
      "filter": {
        "service_ids": ["P123"],
        "urgencies": ["high"],
        "priority_names": ["P1", "P2"]
      }
    }
  ]
}
```

Read responses embed persisted trigger rows with IDs:

```json
{
  "id": "00000000-0000-0000-0000-000000000000",
  "name": "P1 PagerDuty triage",
  "event_triggers": [
    {
      "id": "11111111-1111-1111-1111-111111111111",
      "provider": "pagerduty",
      "event_types": ["incident.triggered"],
      "filter": {"service_ids": ["P123"]},
      "enabled": true
    }
  ]
}
```

Existing schedule fields remain the schedule contract. Provider event triggers
are an additive field on automation create/update/read responses.

### External API support

API clients with `automations:run` should be able to trigger the same path:

```http
POST /api/v1/automations/{id}/run
```

Add optional `trigger_context` to external run requests so PagerDuty Incident
Workflow Web API actions can call 143 directly if a customer prefers not to
install a full PagerDuty integration:

```json
{
  "triggered_by": "provider_event",
  "provider": "pagerduty",
  "provider_event_id": "custom-action:PABC123:2026-06-19T12:00:00Z",
  "trigger_context": {
    "incident_id": "PABC123",
    "incident_url": "https://..."
  }
}
```

This path should be treated as less rich than the full integration because 143
may not be able to fetch fresh incident details or write back notes.

## PagerDuty Event Handling

### Webhook events

Handle these event types in v1:

- `incident.triggered`
- `incident.acknowledged`
- `incident.unacknowledged`
- `incident.reassigned`
- `incident.escalated`
- `incident.priority_updated`
- `incident.annotated`
- `incident.status_update_published`
- `incident.reopened`
- `incident.resolved`

PagerDuty V3 payloads use resource-oriented event names in some docs and
`incident.<event>` names in API references. The parser should normalize both
forms into the internal event constants above.

### Polling reconciliation

Add a periodic `pagerduty_sync` job:

1. For each active integration, fetch incidents updated since `last_synced_at`.
2. Upsert `pagerduty_incidents` and normalized `issues`.
3. Do not trigger automations from polling by default unless the event has not
   been seen and the trigger explicitly allows reconciliation starts.

Polling exists to heal missed webhooks and keep issue state current; webhooks
remain the real-time trigger path.

### Dedupe

For issue ingestion, dedupe by PagerDuty incident ID.

For automation runs, dedupe by:

```text
automation_id + provider + provider_event_id
```

For event types that can fire repeatedly, such as notes/status updates, the
PagerDuty event ID should remain the idempotency key. A trigger may add a
secondary cooldown in `filter`, for example:

```json
{"cooldown_minutes": 30}
```

## Agent Tool Surface

Add a `pagerduty` namespace to `143-tools`.

Read-only v1 commands:

```bash
143-tools pagerduty list_incidents --status triggered,acknowledged --service-id P123 --limit 20
143-tools pagerduty get_incident --incident-id PABC123
143-tools pagerduty list_notes --incident-id PABC123 --limit 50
143-tools pagerduty list_log_entries --incident-id PABC123 --limit 100
143-tools pagerduty get_service --service-id P123
143-tools pagerduty list_oncalls --schedule-id P123 --limit 20
143-tools pagerduty find_related_incidents --incident-id PABC123 --days 90
```

Optional write commands, behind an explicit org setting and agent capability:

```bash
143-tools pagerduty add_note --incident-id PABC123 --body "143 session found..."
143-tools pagerduty create_status_update --incident-id PABC123 --body "Fix PR opened..."
```

Do not expose acknowledge, resolve, escalate, or reassign in v1. Those actions
change incident-response state and should require a separate approval model.

## Writeback

Writeback should be concise and configurable:

- On session start: add a note with the 143 session URL.
- On PR opened: add a note with PR URL and summary.
- On session failure: add a note with failure summary and 143 session URL.
- On no-op automation completion: skip PagerDuty writeback by default. Manual
  PagerDuty-started sessions still write terminal session status.

Do not stream raw agent logs into PagerDuty. Use 143 as the transcript.

## UI

### Settings

`/settings/integrations/pagerduty` should include:

- connection status and account/subdomain
- OAuth scope and credential health
- webhook setup status
- service-to-repository mappings
- default repository fallback
- writeback toggle
- manual setup instructions for Custom Incident Action and Incident Workflow Web
  API actions

### Issue and incident surfaces

PagerDuty issues should show:

- incident number and status
- service, priority, urgency, team, escalation policy
- incident URL
- latest notes/status updates
- mapped repository or unmapped warning
- `Start session` action

### Automation composer

The automation trigger picker should add a `PagerDuty incident` trigger option:

- event type: created, priority changed, annotated, resolved
- service/team filters
- urgency/priority filters
- optional title/custom-field filters
- cooldown

The goal editor should insert a structured starter prompt for incident work.

## Security

- Store PagerDuty OAuth credentials and webhook secrets in encrypted credential
  storage, not plaintext tables.
- Verify all inbound PagerDuty webhooks or shared custom-action headers before
  persistence.
- Sanitize and bound raw incident payloads before storing them in JSONB.
- Redact responder contact methods, tokens, custom headers, and any fields that
  look like secrets.
- Use org-scoped store methods and `org_id` filters everywhere.
- Audit admin setup changes, service mapping changes, manual session starts,
  automation-triggered starts, and PagerDuty writebacks.
- Add a kill switch such as `PAGERDUTY_INTEGRATION_ENABLED`.
- Treat PagerDuty incident text and notes as untrusted prompt input. Render them
  as quoted/contextual incident data in prompts, never as system instructions.

## Failure Modes

- **Unmapped service:** Ingest the incident and show it as unmapped. Manual start
  should ask for a repo; automatic triggers should mark the run skipped with
  `repository_unmapped` and optionally write back only if writeback is enabled.
- **Duplicate webhook delivery:** Rehydrate the existing inbound event through
  `provider_event_id`; do not create another issue, automation run, or session.
- **Webhook storm:** Apply trigger `cooldown_minutes`, automation
  `max_concurrent`, and job-level dedupe before session creation. Suppressed
  events should be visible in automation run history or integration health.
- **PagerDuty API outage:** Persist the inbound event first, retry enrichment in
  a job, and avoid starting a session with only a partial incident snapshot
  unless the trigger explicitly allows partial context.
- **Writeback failure:** Never fail a session or automation run because a
  PagerDuty note/status update failed. Log, audit, mark the integration
  degraded, and surface the writeback failure on integration health. A later
  successful writeback clears the degraded health state.
- **OAuth revoked or scopes reduced:** Mark the integration degraded, stop
  polling and writeback, keep accepting signed webhooks if verification still
  works, and show a re-authorize action in settings.

## Observability

Metrics:

- `pagerduty_webhook_events_total{event_type,result}`
- `pagerduty_ingest_jobs_total{result}`
- `pagerduty_automation_matches_total{event_type,result}`
- `pagerduty_writebacks_total{kind,result}`
- `pagerduty_api_requests_total{endpoint,result}`

Logs should include `org_id`, `integration_id`, `incident_id`,
`provider_event_id`, `automation_id`, `automation_run_id`, `session_id`, and
`request_id` when available.

Health should show recent webhook failures from `webhook_deliveries`, credential
health, last sync time, and writeback failures.

## Implementation Record

1. **Read and ingest.** PagerDuty integration setup, webhook verification,
   durable inbound events, incident mirrors, issue normalization, and polling
   reconciliation are implemented.
2. **Manual start.** Both authenticated 143 start-session and signed
   PagerDuty Custom Incident Action start-session paths are implemented with
   duplicate active-session protection and start writeback notes.
3. **Event triggers.** PagerDuty automation trigger matching, run context
   snapshots, repository resolution, cooldown/max-concurrency behavior, and
   composer UI are implemented.
4. **Agent tools.** The `143-tools pagerduty` namespace is implemented for
   incident, note, log-entry, service, on-call, related-incident, and bounded
   writeback commands. Read tools require the PagerDuty token; write tools also
   require `PAGERDUTY_WRITEBACK_ENABLED=true`, derived from the org integration
   setting.
5. **Writeback and observability.** Session start, non-no-op automation
   terminal status, and PR-open writebacks are implemented with PagerDuty-
   specific metrics, audit events, degraded-health updates on failure, and
   recovery health clearing on later success.

## Open Questions

- Should annotation triggers look for a fixed command like `143:fix`, a custom
  regex, or any note on selected incidents?
- What is the minimum PagerDuty writeback needed for incident commanders to
  trust 143 without adding notification noise?
