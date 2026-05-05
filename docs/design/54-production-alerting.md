# Design: Production Alerting

> **Status:** Partially Implemented | **Last reviewed:** 2026-04-21

This document defines the recommended production alerting stack for 143.dev.

## Summary

143.dev should use a **split-brain observability model**:

- **Sentry** for application exceptions, frontend crashes, release regressions, and developer triage
- **Grafana** for service health, error-rate thresholds, job/worker health, database saturation, log-derived alerts, and routing notifications

The goal is not "one tool for everything." The goal is:

1. Developers get rich exception context where it matters
2. Operators get low-noise alerts based on symptoms that actually require action
3. Costs stay predictable as volume grows

## Current State

As of 2026-04-21:

- Frontend Sentry is configured via `@sentry/nextjs` in:
  - `frontend/sentry.client.config.ts`
  - `frontend/sentry.server.config.ts`
  - `frontend/sentry.edge.config.ts`
  - `frontend/src/app/global-error.tsx`
  - `frontend/src/components/error-boundary.tsx`
  - `frontend/src/lib/errors.ts`
- Backend logs are structured JSON via `zerolog`
- Logs are shipped by Vector to VictoriaLogs and explored in Grafana
- Production log queries are available through `make logs-query`
- The Go API now initializes backend Sentry and automatically captures:
  - recovered panics in the HTTP stack
  - `5xx` API responses emitted through the normal JSON error path
- Vector now extracts `status`, `error_code`, and `error_message` from structured API logs so Grafana alerts can target these fields directly

Recent production logs already show actionable backend failures, for example repeated `/api/v1/usage/breakdown` errors and SSE stream write timeouts. Those are visible in logs today, and `5xx`/panic failures in the API path should now also create Sentry events automatically.

## Recommendation

### 1. Use Sentry as the exception system of record

Sentry should be the primary place for:

- Unhandled frontend exceptions
- Handled frontend exceptions that are important enough to investigate
- Unhandled Go API panics
- Go API internal errors that indicate broken behavior, not routine client mistakes
- Release regression tracking by deploy/release

Sentry should **not** be the only paging system. It is best used for exception intelligence, grouping, stack traces, regressions, ownership, and debugging workflow.

### 2. Use Grafana as the alerting and routing control plane

Grafana should own alerts for:

- HTTP 5xx rate
- Worker/job failure rate
- Queue backlog / stuck jobs
- Database connectivity and latency
- Host/container saturation
- Error spikes derived from logs
- Missing heartbeats / scheduled jobs not running

Grafana is already close to the operational truth in 143 because the system has structured logs and a working centralized log pipeline.

### 3. Page on symptoms, not on every exception

Modern teams, especially AI product teams, page on:

- user-visible outage
- sustained elevated 5xx rate
- core async pipeline failure
- inability to process jobs
- severe regression after deploy

They do **not** page on every new stack trace.

Sentry issues should usually go to Slack first. Grafana should page when aggregated symptoms cross an operational threshold.

## Best-in-Class Model for 143

For a modern AI company, the practical best-in-class setup is:

1. **Exception monitoring:** Sentry
2. **Metrics/logs/service alerting:** Grafana
3. **Paging/on-call:** PagerDuty if budget allows, otherwise Grafana IRM or Better Stack
4. **Incident chat channel:** Slack
5. **Release correlation:** tag releases in both Sentry and Grafana annotations where possible

This is better than a Datadog-only model for 143 right now because:

- Sentry is better than generic observability tools at exception grouping and developer triage
- Grafana fits the existing logging architecture
- alerting costs stay tied to the signals that matter, instead of expanding into full Datadog ingestion/APM pricing

## Concrete Alert Classes

### Page immediately

- API 5xx rate above threshold for sustained window
- worker cannot claim/process jobs for sustained window
- database unavailable / readiness failing
- deploy causes sharp spike in backend error rate
- webhook ingestion dead for critical integrations

### Slack only

- new Sentry issue above severity threshold
- repeated exception in non-critical endpoint
- SSE stream write failures above a low threshold
- sync job failures with automatic retry remaining
- single integration auth failure for one org

### Ticket/report only

- long-tail exceptions with low frequency
- noisy but known errors
- usage approaching quota in Sentry or Grafana Cloud
- experiment outcome regressions that do not affect core availability

## Minimal Initial Alert Set

The initial production setup should be intentionally small:

1. **Grafana: API 5xx rate**
2. **Grafana: worker job failure rate**
3. **Grafana: jobs stuck in queue / backlog age**
4. **Grafana: DB readiness failures**
5. **Grafana: no successful scheduler heartbeat in N minutes**
6. **Sentry: critical frontend issue alerts to Slack**
7. **Sentry: critical backend exception alerts to Slack**

If an alert does not have a clear owner and action, it should not exist yet.

## Concrete Grafana Alert Plan

These are the first Grafana-managed alert rules 143.dev should create.

### 1. API 5xx burst

**Signal**

- Log-derived count from VictoriaLogs
- Query shape: `service:api AND level:error AND _msg:"request failed" AND status:range[500,599] AND _time:[now-5m,now]`

**Initial threshold**

- Warning: `>= 10` in 5 minutes
- Critical/page: `>= 25` in 5 minutes

**Route**

- Warning to Slack
- Critical to on-call

### 2. Repeating endpoint-specific backend failure

**Signal**

- Same as above, grouped by `path` or `error_code`
- Start with known current offender patterns such as:
  - `/api/v1/usage/breakdown`
  - `error_code=...` once stable error-code distributions are known

**Initial threshold**

- `>= 5` for the same `path` in 10 minutes

**Route**

- Slack only

This catches "something is broken, but not yet a whole-app incident."

### 3. Worker fatal failures

**Signal**

- `service:worker AND level:error AND _time:[now-10m,now]`
- Narrow to terminal job failure messages as dashboards mature

**Initial threshold**

- Warning: `>= 5` in 10 minutes
- Critical/page: `>= 20` in 10 minutes

**Route**

- Warning to Slack
- Critical to on-call

### 4. Scheduler / heartbeat missing

**Signal**

- Alert when the worker does not emit its expected recurring scheduler/heartbeat logs within a window
- If a dedicated heartbeat log line does not yet exist, add one before enabling this alert

**Initial threshold**

- No heartbeat for `10m`

**Route**

- Page

### 5. Readiness failure

**Signal**

- `service:api AND level:warn AND _msg:\"readiness check failed: database unavailable\" AND _time:[now-5m,now]`

**Initial threshold**

- `>= 3` in 5 minutes

**Route**

- Page

### 6. SSE/log streaming degradation

**Signal**

- `service:api AND level:error AND _msg:\"failed to write log event to SSE stream\" AND _time:[now-15m,now]`

**Initial threshold**

- `>= 10` in 15 minutes

**Route**

- Slack only

This is user-visible but usually not page-worthy unless it becomes sustained and correlated with larger failure.

## How to Create the Grafana Alerts

1. Confirm the VictoriaLogs datasource in Grafana sees the extracted fields `service`, `level`, `path`, `status`, `status_class`, `duration_ms`, `error_code`, and `error_message`.
2. Build a small dashboard first in Explore to validate each query against recent data.
3. For log-derived alerts, keep the rule definitions in version-controlled `vmalert` YAML instead of Grafana-managed rules. The current VictoriaLogs Grafana datasource is excellent for querying and dashboards, but it is not a dependable source of truth for provisioning Grafana-managed log alerts end to end.
4. Use Alertmanager as the runtime notification router for `vmalert` rules. Grafana remains the operator UI via a provisioned Alertmanager datasource.
5. Add labels on every rule:
   - `service=api` or `service=worker`
   - `severity=warning|critical`
   - `team=platform`
   - `signal=logs`
6. Route labels through Grafana notification policies:
   - warning -> Slack
   - critical -> on-call destination
7. Group notifications by `service` and `severity` so one incident does not fan out into many pages.
8. Run every alert in Slack-only mode first, tune thresholds for a few days, then enable paging on the critical subset.

The initial repo-owned files for this live in:

- `deploy/grafana/provisioning/datasources/alertmanager.yml`
- `deploy/grafana/provisioning/dashboards/errors.json`
- `deploy/grafana/provisioning/dashboards/platform-health.json`
- `deploy/vmalert/rules/production-alerts.yml`
- `docker-compose.logging.yml` for the `vmalert`, Alertmanager, Grafana, VictoriaLogs, and logging-node Vector runtime wiring

`deploy-logging` is the source-of-truth path for these files: it syncs Grafana provisioning, vmalert rules, the shared Vector compose include, and Vector config to `/opt/143` on the logging node, then recreates the logging stack so rules reload reliably. Grafana also watches the provisioned dashboard directory and removes dashboards whose JSON files have been deleted from the repo.

## Automation Policy for Backend Sentry

The API server implementation should require **no per-handler Sentry calls** for the normal case.

Automatic capture now works like this:

1. Router-level panic recovery captures recovered panics into Sentry
2. Router-level request logging captures all `5xx` JSON API responses into Sentry
3. Routine `4xx` responses are intentionally ignored to avoid noise

That means most backend alerting coverage comes from middleware, not from engineers remembering to call Sentry manually.

Manual `CaptureException` calls are still appropriate for:

- background jobs
- worker loops
- external API retries that fail after exhaustion
- best-effort paths where the request still returns `200` but something important broke

## Backend Sentry Requirements

To make Sentry trustworthy for 143, the Go backend should add:

- `sentry-go` SDK initialization in the API server
- panic/recover middleware wired into the HTTP stack
- request context tags: `request_id`, `org_id`, route, environment, release
- explicit capture for high-value handled errors
- filtering so expected `4xx` request failures do not create Sentry noise

Recommended policy:

- capture `5xx` server errors and panics
- do not capture routine validation/auth/permission `4xx` paths
- keep structured logs as the source for exhaustive event history

## Alert Routing Options

### Option A: Sentry + Grafana + Slack

Best for the current stage if pager fatigue would be worse than delayed response.

- Lowest operational complexity
- Cheapest
- Good enough for early-stage teams
- Weakest for true on-call escalation

### Option B: Sentry + Grafana + Grafana IRM

Best if you want one integrated alerting/on-call path without Datadog pricing.

- Good fit with existing Grafana investment
- Reasonable pricing for small teams
- Keeps alert definitions close to telemetry
- Less ecosystem depth than PagerDuty

### Option C: Sentry + Grafana + Better Stack

Best if you want cheaper paging and incident tooling without buying into Datadog.

- Strong cost/value ratio
- Built-in on-call and incident response
- Simpler than assembling many tools
- Less standard than PagerDuty for larger orgs

### Option D: Datadog + PagerDuty

Best only if the team wants full commercial observability consolidation and accepts the cost.

- Excellent product depth
- Strong APM and managed integrations
- Expensive as logs, traces, and host count grow
- Not aligned with the current 143 architecture

## Recommended Path for 143

### Phase 1: Fix coverage gaps

- Add backend Sentry instrumentation
- Ensure releases are set in frontend and backend
- Create a Slack channel for production alerts

### Phase 2: Add high-signal Grafana alerts

- alert on API 5xx rate
- alert on worker failure/backlog
- alert on DB/readiness failures
- alert on missing scheduler heartbeat

### Phase 3: Add paging only for true incidents

- Keep most Sentry rules Slack-only
- Route only Grafana P1/P2 alerts to on-call
- Start with Grafana IRM or Better Stack before PagerDuty unless incident volume proves the need

## Non-Goals

- No attempt to mirror every log error into Sentry
- No paging on every new exception group
- No large Datadog rollout solely to recreate capabilities already covered by Sentry + Grafana

## Related Docs

- [overall.md](overall.md)
- [09-observability.md](09-observability.md)
- [47-logging-victorialogs.md](47-logging-victorialogs.md)
