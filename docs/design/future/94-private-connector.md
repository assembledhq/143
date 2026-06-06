# Design: 143 Private Connector

> **Status:** Future
> **Last reviewed:** 2026-06-06

143 needs to help coding agents work with production-adjacent systems that live inside customer private networks: logs and databases. The product problem is that those systems are intentionally behind firewalls, private VPCs, VPNs, or office networks, and customers should not have to expose them broadly to the public internet just so 143 can debug issues or run realistic queries.

The 143 Private Connector is a customer-installed, outbound-only bridge from a customer's private network to 143. It should make private infrastructure available to 143 through explicit, narrowly scoped capabilities rather than broad network access.

The first supported capabilities are:

1. VictoriaLogs querying for coding agents and operators.
2. Read-only database inspection/querying for coding agents.

## Product Problem

High-value 143 workflows depend on operational context that is usually private:

- A coding agent needs production logs to understand a Sentry error, failed job, or customer-impacting regression.
- A coding agent needs to inspect database schema or run a read-only query to understand current state.

The current alternatives are not productized enough:

- Ask the customer to expose VictoriaLogs, Postgres, or another system publicly.
- Ask the customer to configure `vmauth`, reverse proxies, TLS, firewall rules, IP allowlists, and credentials by hand.
- Ask 143 sandboxes to reach directly into customer networks.
- Use SSH tunnels or bespoke VPN setup as the integration path.

Those options are workable for expert operators, but they are not close to one-click and they create support and security problems. The desired product shape is:

> Install a lightweight 143 connector inside your private network. It opens an outbound connection to 143, so your logs and databases stay private. 143 can then request specific approved actions, with local policy enforcement and full audit logging.

## Goals

- Avoid requiring inbound firewall openings for the primary setup path.
- Keep customer infrastructure private by default.
- Make setup as close to one command as possible with a native shell-script installer.
- Give agents access to useful private context without giving them raw network access.
- Enforce policy both in 143 and locally inside the connector.
- Provide clear health, diagnostics, audit trails, token rotation, and disable controls in the product UI.
- Design the connector as a reusable private-infrastructure bridge, even though the first release only supports VictoriaLogs and read-only DB.

## Non-Goals

- Do not build a general-purpose VPN into customer networks.
- Do not expose arbitrary TCP connectivity from sandboxes into private networks.
- Do not give coding agents direct database credentials.
- Do not support every observability/database provider in the first release.
- Do not replace customer-managed direct HTTPS endpoint integrations for teams that already want to expose a secure gateway.

## Product Surface

The product should expose this as **Settings -> Integrations -> Private Connector**.

The connector is the top-level installed component. Individual private resources are configured under it:

- Logs -> VictoriaLogs
- Databases -> Read-only Postgres

The customer-facing copy should avoid "expose your logs/database to 143" and instead say:

> Keep private systems private. Install a connector inside your network and grant 143 scoped access to approved resources.

## Onboarding Flow

### 1. Start Install

The user opens:

`Settings -> Integrations -> Private Connector -> Add Connector`

143 asks for:

- Connector name, for example `Production VPC`.
- Environment label, for example `production`.
- Install target. The first release should default to a shell script that installs a native system service.
- Optional region preference if 143 operates regional connector gateways.

143 creates:

- A connector record scoped to the org.
- A connector deployment token, defaulting to single-use with an approximately 24-hour expiry for interactive setup.
- A recommended install command.

### 2. One-Command Install

The primary install path should be a shell command that works on a broad range of Linux hosts without requiring Docker or Kubernetes:

```bash
curl -fsSL https://get.143.dev/private-connector.sh | \
  sudo 143_CONNECTOR_TOKEN='<deployment-token>' bash
```

The install script should:

- Detect architecture and operating system.
- Download the correct signed connector binary.
- Verify the binary signature before executing it. The 143 signing public key is pinned inline in the install script — it is never fetched from the same server as the binary. Signature verification uses Sigstore cosign. If verification fails the script aborts with a clear error and never silently proceeds.
- Create a dedicated `143-connector` user.
- Create `/etc/143/connector.yaml` if it does not exist.
- Install a systemd unit.
- Start the connector.
- Print the local config path and service status command.

**Trust model for the install script itself:** The trust anchor for `private-connector.sh` is the `get.143.dev` domain over HTTPS — the same pattern used by Tailscale, Homebrew, and most production devtools installers. The acknowledged risk is that a compromise of that domain could deliver a malicious script. The mitigation is treating that domain with the same security posture as the 143 production domain. The binary signature verification inside the script provides a second independent trust layer: even if the script is tampered with in transit, a manipulated binary will fail the inline key verification.

For automation environments where `curl | bash` is not acceptable, a checksum manifest at `https://get.143.dev/private-connector-checksums.txt` allows pre-downloading and verifying the binary out of band before running the installer with a local binary path.

Docker, Docker Compose, and Kubernetes packaging can be added later for customers with strong platform preferences, but they should not be part of the primary onboarding flow. A single shell command keeps the first setup screen simpler and works across more private-network environments, including small VPS deployments, bare-metal hosts, and internal utility boxes where Docker or Kubernetes may not be available.

### 3. Deployment Token Model

The connector should use one bootstrap credential type: a **connector deployment token**. This avoids making customers choose between "manual registration tokens" and "deployment tokens." A manual one-command install is just a deployment token with short expiry and single-use defaults.

The connector should not depend on the deployment token after it has registered. The token is only the bootstrap credential used to pair a connector instance with a 143 organization.

After registration:

- The connector exchanges the deployment token for a durable connector instance identity.
- The connector generates an Ed25519 key pair. The private key is stored at `/var/lib/143-connector/identity.key` with permissions `0600`, owned by the `143-connector` user. The corresponding public key is registered with 143.
- Future outbound sessions authenticate with the connector instance identity (mTLS client certificate derived from the Ed25519 key), not the original deployment token.
- Rotating or expiring the deployment token does not break already-registered connector instances.

**Compromise response:** If the identity directory is read by an attacker, they can impersonate the connector to 143 until the identity is explicitly revoked. Operators must be able to revoke a connector instance identity from the UI (Settings → Integrations → Private Connector → [connector] → Revoke Instance) independently of revoking the deployment token. Revocation takes effect within one heartbeat cycle on the gateway side.

**Key rotation:** "Rotate connector identity" in the UI triggers the connector to generate a new Ed25519 key pair, submit the new public key to 143, and receive a new registration acknowledgment. The old identity is revoked server-side only after the new registration is confirmed. The connector continues serving requests under the old identity during the handoff, so rotation causes no interruption.

The product should expose deployment token presets:

1. **Interactive install preset**
   - Valid for approximately 24 hours.
   - Max registrations defaults to 1.
   - Intended for a human operator installing one connector from the UI.
   - Revoked automatically after successful first use when configured as single-use.

2. **Automation preset**
   - Intended for Terraform, Ansible, cloud-init, golden images, or internal runbooks.
   - Can be long-lived, for example 30 days, 90 days, or no fixed expiry.
   - Must be revocable.
   - Should support optional controls such as max registrations, allowed connector group, allowed region, allowed source CIDRs when useful, and last-used timestamp.
   - **Shell history exposure:** passing a long-lived token as an inline environment variable leaves it in shell history. Store automation tokens in a secrets manager and inject them via an environment variable that is set before the shell session, or write the token to a restricted file (`chmod 600`) and pass the path: `sudo 143_CONNECTOR_TOKEN_FILE=/run/secrets/143-token bash`.

The default product path should generate an interactive install preset, because it is safest for a human copying a command from the UI. The same screen should offer "Use in automation" to create or select a longer-lived deployment token. Customers should not need a fresh UI-generated token every time infrastructure is replaced. They should store an automation deployment token in their own secret manager and use the same curl install command in server provisioning.

Example automated install:

```bash
curl -fsSL https://get.143.dev/private-connector.sh | \
  sudo 143_CONNECTOR_TOKEN="$143_CONNECTOR_DEPLOYMENT_TOKEN" bash
```

For high availability, customers should usually run a small connector pool per private network, not one connector on every application server. Multiple connector instances can join the same connector group and advertise access to the same configured private resources.

Connector group semantics:

- **Action requests** (log queries, DB reads) are stateless. 143 routes each request to any healthy connector in the group using round-robin with health-weighted fallback. Any connector can serve any action request for a shared resource.
- **Health detection** uses the heartbeat stream, not last-heartbeat timestamp. A connector is considered offline when its heartbeat stream lapses for more than two consecutive intervals (default 10 seconds each). 143 stops routing to it immediately and routes to other group members.

The install page should stream setup status:

- Waiting for connector.
- Connector registered.
- Connector online.
- Version detected.
- Network egress OK.
- Ready to add resources.

### 4. Add A Resource

After the connector is online, the UI prompts the customer to add one of the supported resource types. Resource configuration is entered in the 143 UI and pushed to the connector over its established session — the operator does not need SSH access to the connector host to configure resources. The connector applies the pushed config immediately and confirms success or returns a validation error. Local file configuration (`/etc/143/connector.yaml`) takes precedence over UI-pushed config when both are present; operators using Terraform or Ansible can manage config via file and the UI reflects it read-only.

For VictoriaLogs:

- Display name.
- Local VictoriaLogs URL, for example `http://victorialogs:9428`.
- Optional default filters, for example `environment: production`.
- Max query window.
- Max returned rows.
- Query timeout.

For read-only DB:

- Display name.
- Database type. First release: Postgres.
- Host/port or DSN.
- Read-only credential source.
- Allowed schemas.
- Denied tables.
- PII columns or column patterns.
- Max rows.
- Query timeout.
- Whether sensitive-table access requires approval.

### 5. Test Connection

The UI should provide a first-class test for each resource:

- VictoriaLogs: run a bounded field discovery query.
- Read-only DB: connect, verify read-only transaction, inspect schemas.

The test result should explain failures in operator language:

- Connector offline.
- Cannot resolve host from connector.
- Authentication failed.
- TLS failed.
- Permission denied.
- Read-only role is not actually read-only.

### 6. Done State

The completed setup page shows:

- Connector online/offline.
- Last heartbeat.
- Connector version and available update, if any.
- Enabled resources.
- Last successful request per resource.
- Last error per resource.
- Health alert destination (Slack webhook or generic POST URL).
- Rotate token.
- Copy reinstall command.
- Trigger connector update.
- Disable connector.
- View audit events.

## Architecture

```
Customer private network                         143 production

VictoriaLogs                                     143 app/API
Postgres                         outbound        connector gateway
                    <---->  143 Private Connector <----> agents/jobs
```

The connector initiates and maintains an outbound connection to a 143 connector gateway over **WebSocket over port 443**. WebSocket over 443 is the default because it traverses corporate HTTP proxies and standard firewalls without special configuration. 143 never opens inbound connections to customer networks.

The multiplexed WebSocket session carries:

- A heartbeat stream (connector → gateway, default 5-second interval).
- Action request/response channels (gateway → connector, one per in-flight request).

A secondary gRPC-over-443 path is available for connectors whose network infrastructure handles gRPC well, selectable via connector config. The shell installer detects which protocol succeeds during bootstrap and records it in the connector identity.

The connector exposes one class of capability: **action capabilities** for agents and tools. Agent access is constrained and read-only.

## Reconnection Behavior

### Drop scenarios

**Session drop with no in-flight request** — the connector detects the drop via a heartbeat timeout or TCP RST, reconnects with backoff, and resumes. No state to reconcile.

**Session drop with an in-flight action request** — when the gateway detects a session drop, it immediately fails all pending requests on that session with a retriable error code and returns them to the tool. It does not wait for the connector to reconnect. This is safe because all action capabilities are read-only and idempotent. The tool layer may transparently retry once on a retriable error before surfacing a failure to the agent.

Two sub-cases exist, both handled the same way:

- Drop before the connector received the request: the gateway gets a delivery error immediately.
- Drop after the connector started executing: the connector ran the query but the response was lost. The gateway has already failed the request on the caller side; the connector discards the result when it reconnects.

**Gateway-side drop** — from the connector's perspective this is identical to a session drop. The connector reconnects. Requests queued in the gateway that were not yet dispatched to the connector are retried against another gateway node.

### Reconnect schedule

The connector reconnects automatically without operator intervention:

- **First retry:** within 500ms of detecting the drop. Covers transient blips.
- **Backoff:** exponential starting at 2s, doubling each attempt, capped at 60s.
- **Jitter:** ±25% applied to every backoff interval. Prevents connector pools from synchronizing and hitting the gateway simultaneously after a shared outage.
- **No retry limit:** the connector retries indefinitely. An operator stopping the service is the only intended stop condition.
- **On successful reconnect:** the connector re-sends its capability advertisement and resource health so the gateway immediately resumes routing.

### Reconnecting vs. offline

The gateway distinguishes two states for a connector that has stopped heartbeating:

- **Reconnecting** (lapsed for less than ~30 seconds): the gateway stops routing new requests to that connector and routes them to other group members instead. No health alert fires. The UI shows no change. In-flight requests on the dropped session are failed immediately.
- **Offline** (lapsed past the alert threshold, default 60 seconds): the gateway fires the health alert webhook, marks the connector offline in the UI, and returns "Connector offline" errors to agent tools rather than routing or queuing.

The threshold gap between "reconnecting" and "offline" absorbs routine network blips and connector restarts without paging operators. Teams that want tighter alerting can lower the threshold in connector settings.

## Action Capabilities

Action capabilities are request/response operations mediated by 143.

Supported:

- `victorialogs.query`
- `victorialogs.fields`
- `victorialogs.context`
- `victorialogs.stats`
- `postgres.schema`
- `postgres.read_query`
- `postgres.explain`
- `postgres.sample_rows`
- `postgres.indexes`

Flow:

1. Agent calls `143-tools log_query` or a DB tool, specifying the resource by name or letting 143 resolve the default for that capability type.
2. Sandbox calls 143, not the private resource.
3. 143 authorizes the actor, org, session, and repository, and resolves the target resource ID.
4. 143 signs an action request containing the org ID, connector ID, resource ID, capability, request ID, and expiry, and dispatches it to the connector gateway.
5. The connector receives the request over its outbound session and validates the signature, expiry, nonce, and resource ID before doing anything else.
6. The connector validates local policy for the resolved resource.
7. The connector executes the action against the private resource.
8. The connector caps/redacts results.
9. 143 records an audit event and returns results to the tool.

Action capabilities must never become arbitrary shell commands or arbitrary network requests.

## First Release Capabilities

### VictoriaLogs Connector

Purpose: let coding agents query private VictoriaLogs deployments without customers exposing VictoriaLogs publicly.

Supported actions:

- `victorialogs.query`
- `victorialogs.context`
- `victorialogs.fields`
- `victorialogs.stats`

Policy:

- Require bounded time windows.
- Enforce maximum query window.
- Enforce maximum row count.
- Enforce query timeout. The timeout is the primary cost bound for arbitrary query shapes — all other limits protect result size, but expensive queries (high-cardinality aggregations, broad stream selects) can consume significant VictoriaLogs CPU within a bounded time window.
- Enforce `max_series_cardinality` for `victorialogs.fields` and `victorialogs.stats` queries (default: 1000 distinct values). High-cardinality field discovery is expensive regardless of the time window.
- Enforce `max_requests_per_minute` per resource (default: 60). The connector returns a throttle error that the agent surfaces rather than queuing silently. This protects the VictoriaLogs server from agents in tight debugging loops.
- Allow default filters, such as `environment=production`.
- Support optional field allow/deny rules.
- Redact configured fields before returning logs to 143.

Runtime path:

- `143-tools log_query --provider victorialogs` calls 143.
- 143 routes through the connector.
- Sandboxes never receive VictoriaLogs credentials or private hostnames.

### Read-Only DB For Coding Agents

Purpose: let coding agents inspect schema and run bounded read-only queries during debugging.

First provider: Postgres.

Supported actions:

- `postgres.schema`
- `postgres.read_query`
- `postgres.explain`
- `postgres.sample_rows`
- `postgres.indexes`

Required database protections:

- Customer must provide a database role with read-only permissions. This is the non-negotiable final enforcement layer; all other controls are defense-in-depth on top of it.
- Connector wraps every query in an explicit `READ ONLY` transaction. If the database role has write permissions, the transaction mode still prevents writes.
- Connector sets `statement_timeout` before executing.
- Connector caps returned rows at the configured limit via query rewrite (`SELECT ... LIMIT n`).
- Connector denies `COPY` statements by string match before execution (belt-and-suspenders on top of the read-only role).
- Connector applies table/schema allowlists or denylists.
- Connector redacts configured PII columns from result sets. Redacted values are replaced with the string `[REDACTED]`; null values in PII columns are left as null to avoid confusing missing data with redaction. Column name matching is exact by default; use `pii_column_patterns` for regex-based matching to cover variations like `user_email`, `email_address`, and `billing_email` with one rule. JSONB columns containing PII sub-fields are out of scope for v1 — document this limitation in the resource configuration UI.
- WHERE clause predicate values are never included in audit event fingerprints, regardless of column name, to prevent PII from leaking through the audit log.
- Enforce `max_requests_per_minute` per resource (default: 30). Lower default than VictoriaLogs because DB queries carry more risk of connection exhaustion on the target server.

**`postgres.sample_rows` requires explicit opt-in.** This is the highest-risk capability because it returns actual row data. It is disabled by default and must be explicitly enabled in the resource config (`allow_sample_rows: true`). When enabled:
- Row cap is fixed at 10 rows, regardless of the resource-level `max_rows` setting.
- PII column redaction is always applied; it cannot be disabled for sample rows specifically.
- Every invocation generates an audit event with the table name and row count, regardless of the general audit configuration.

The connector does not use a SQL parser for validation. Writing a parser that correctly handles all of Postgres's syntax (CTEs, dollar-quoting, multi-statement, server-side functions, `SELECT INTO`) is a source of ongoing bypass reports and not worth the complexity relative to what the database role and transaction mode already enforce. The enforcement stack above is auditable and sufficient for v1.

## Security Model

Security should be enforced at multiple layers.

143-side enforcement:

- Actor authorization.
- Org and repository scoping.
- Session scoping.
- Resource capability checks.
- Short-lived signed requests.
- Audit events for every action.
- Result caps before returning to agents.

Connector-side enforcement:

- Validate request signature against the pinned 143 gateway public key.
- Validate org ID and connector ID against the connector's registered identity.
- Validate resource ID in the signed request against the connector's configured resource list. Reject any request targeting an unknown resource ID.
- Check nonce cache; reject replayed request IDs.
- Reject requests outside the 30-second expiry window (tolerating up to 10 seconds of clock skew).
- Validate local policy for the resolved resource.
- Enforce timeouts, row limits, and query windows.
- Redact configured fields.
- Deny unsupported endpoints/actions.
- Deny all write or mutation operations for agent DB actions.
- Use local credentials; never send private credentials to 143.
- Fail closed when config is invalid.

Provider-side enforcement:

- Read-only DB role for agent DB access.
- VictoriaLogs endpoint reachable only from connector network.
- Optional internal firewall rules limiting connector egress to configured targets.

### Request Signing and Replay Protection

143 signs every action request dispatched to the connector gateway. The connector validates the signature before executing any action.

Signed payload fields:

- org ID
- connector ID
- resource ID
- capability name
- request ID (random UUID, serves as nonce)
- issued-at timestamp (Unix seconds)
- expiry timestamp (issued-at + 30 seconds)

The connector rejects requests where:

- The signature does not verify against the pinned 143 gateway public key.
- The expiry has passed.
- The org ID or connector ID does not match the connector's registered identity.
- The resource ID is not in the connector's configured resource list.
- The request ID (nonce) has been seen before within the nonce cache TTL.

**Nonce cache:** The connector maintains an in-memory nonce cache with a TTL equal to the expiry window (30 seconds). Any request whose nonce appears in this cache is rejected. This prevents replay attacks within the expiry window even if an attacker captures a valid signed request in transit.

**Clock skew:** The connector tolerates up to 10 seconds of clock skew between its system clock and the signed request's issued-at timestamp. If a request is rejected solely due to clock skew, the connector returns a distinct error code (`CLOCK_SKEW`) that the gateway surfaces to operators. Connector hosts must run NTP or equivalent time synchronization; this is checked and warned during the install step.

Audit events should record:

- actor,
- org,
- repository/session when available,
- connector ID,
- resource ID,
- capability,
- time range,
- query hash or normalized statement fingerprint,
- affected schema/table names when available,
- result count,
- duration,
- success/failure,
- denial reason.

Audit events must not store full raw logs, full SQL result sets, secrets, or database passwords.

## Connector Configuration

The connector uses a two-source config hierarchy with explicit precedence:

1. **Local file** (`/etc/143/connector.yaml`) — takes precedence when present. Designed for operators using Terraform, Ansible, cloud-init, or golden images. The UI reflects local file config as read-only and displays a "Managed via config file" badge.
2. **UI-pushed config** — active when no local file is present. The 143 UI pushes resource configuration to the connector over its established session; the operator never needs SSH access to the connector host. The connector confirms success or returns a structured validation error displayed inline in the UI.

This prevents split-brain: there is no merging between the two sources. The connector logs on startup which config source is active. When switching from file-managed to UI-managed, the operator deletes or moves the local file and triggers a connector reload from the UI.

**Config push authorization:** Session authentication alone (connector identity → org) does not authorize a config push. Each config push from the 143 gateway must carry an org-admin-scoped authorization token, separate from the session credential. The connector verifies this token before applying any change. This limits the blast radius of a compromised gateway: the gateway cannot reconfigure connectors without a valid org-admin token.

Config pushes are versioned. The connector records the version number of the last accepted push. A push carrying an older version is rejected, preventing rollback to a previous (potentially less restrictive) configuration. All config changes are logged locally with the authorizing identity and timestamp.

Secrets are never UI-managed. Whether config comes from file or UI, credential references must be environment variables, mounted files, or system secret stores on the connector host. The UI accepts the variable name (e.g., `PROD_READONLY_DATABASE_URL`); the connector resolves it locally.

Example:

```yaml
connector:
  name: production-vpc
  environment: production

resources:
  victorialogs_prod:
    type: victorialogs
    url: https://victorialogs:9428  # use http only on trusted same-segment networks
    default_filters:
      environment: production
    limits:
      max_time_range: 24h
      max_rows: 500
      max_series_cardinality: 1000
      timeout: 10s
      max_requests_per_minute: 60
    redact_fields:
      - authorization
      - cookie

  postgres_agent_readonly:
    type: postgres
    mode: agent_readonly
    dsn_env: PROD_READONLY_DATABASE_URL
    allowed_schemas:
      - public
    denied_tables:
      - public.password_resets
      - public.payment_methods
    pii_columns:
      - email
      - phone
    pii_column_patterns:
      - '.*_email$'
      - '.*_phone$'
    allow_sample_rows: false  # explicit opt-in required; see sample_rows policy
    limits:
      max_rows: 100
      timeout: 5s
      max_requests_per_minute: 30

```

## Failure Modes

The product must make connector failures understandable.

Common states:

- Connector reconnecting (transient, suppressed from alerts).
- Connector offline (sustained, triggers health alert).
- Connector version unsupported.
- Connector cannot resolve private hostname.
- Connector cannot reach resource.
- Resource authentication failed.
- Resource policy denied request.
- Query exceeded time/range/row limits.
- Request failed mid-flight due to session drop (retriable).

Agent tool failures should be concise and actionable:

- "VictoriaLogs connector is offline."
- "Query exceeds the 24h limit for this resource."
- "Read-only DB connector denied access to table `payment_methods`."

## Developer Experience

The private connector is set up by an operator, but the primary value is delivered to developers using 143 agents. The end-user experience should be seamless.

### Agent-Facing Log Access

When a VictoriaLogs connector is configured and online, the coding agent has access to a `log_query` tool. The agent uses it automatically when it has reason to look at logs (debugging a Sentry error, investigating a failed job, understanding production behavior). The developer does not need to configure anything; the agent discovers that log access is available from the session context.

Example agent invocation during a debugging session:

```
Agent: I can see the production logs for this service. Let me check what happened around the time of the error.

[querying victorialogs: service=api, time=last 30m, filter="error"]

Found 3 error entries:
  2026-06-06T14:22:01Z ERROR payment_processor: connection timeout after 5s (attempt 3/3)
  2026-06-06T14:22:01Z ERROR payment_processor: falling back to retry queue
  2026-06-06T14:22:04Z ERROR retry_worker: dead letter threshold reached for job 8821

This looks like a downstream timeout in the payment processor, not an application bug...
```

If the connector is offline, the agent surfaces it concisely:

```
Agent: Log access is unavailable — the VictoriaLogs connector is offline. I'll work from the code and stack trace instead.
```

### Agent-Facing Database Access

When a read-only Postgres connector is configured, the agent can inspect schema and run bounded queries during debugging. The agent announces this capability when it is useful:

```
Agent: I have read-only access to the production database. Let me check the schema for the payments table and look at a sample of recent rows.

[postgres.schema: table=payments]
[postgres.sample_rows: table=payments, limit=5, where="created_at > now() - interval '1 hour'"]
```

The developer never sees database credentials. The agent never gets them. The query is executed by the connector against the private database.

## Direct HTTPS Endpoint Compatibility

The Private Connector should become the recommended private-network path, but 143 should keep advanced direct endpoint setup for teams that already operate secure gateways.

Supported advanced paths:

- VictoriaLogs direct HTTPS endpoint.
- `vmauth` or another read-only HTTPS proxy.
- Grafana datasource proxy.
- Static 143 egress IP allowlisting.

The UI should present these as advanced options. The default recommended setup for firewalled/private resources should be the connector.

## Implementation Phases

### Phase 1: Connector Foundation + VictoriaLogs

- Connector records and deployment tokens.
- Deployment tokens for infrastructure automation.
- Connector instance identities after bootstrap token exchange.
- Connector groups with defined HA semantics (stateless action routing, health-weighted failover).
- Outbound connector gateway over WebSocket/443.
- Shell-script installer and systemd service flow.
- Connector heartbeat stream and health UI.
- Connector version display and UI-triggered upgrade flow.
- UI-pushed resource configuration over established connector session.
- Health alert webhooks: connector offline, connector back online, version unsupported.
- VictoriaLogs resource configuration.
- `victorialogs.query`, `context`, `fields`, and `stats`.
- Audit events.
- Agent `143-tools log_*` path through connector-backed provider.

### Phase 2: Read-Only Postgres For Agents

- Postgres read-only resource configuration.
- Schema inspection.
- Bounded read-only SQL.
- Explain query.
- Table/schema allow and deny policy.
- PII column redaction.
- Audit events with SQL fingerprints.

## Decisions

Previously open questions, now resolved:

**Config hierarchy:** Local file wins when present; UI-pushed config is active otherwise. No merging. The connector logs which source is active on startup. See the Connector Configuration section for the full precedence model.

**SQL validation strategy:** No SQL parser. Enforcement relies on: customer-provided read-only database role (mandatory) + connector-issued `READ ONLY` transaction + `statement_timeout` + row cap + `COPY` denial by string match. The database role is the security guarantee. The other layers are defense-in-depth. A parser would add complexity without meaningfully improving the security boundary.

**Connector upgrade strategy:** Connector checks for available updates on each reconnect and reports them to 143. The UI shows "Update available: vX.Y.Z" on the health page. An admin triggers the update from the UI; the connector downloads and verifies the signed binary, swaps it, and restarts the service. No silent auto-updates for production connectors. Operators using Terraform or Ansible can pin versions in their config and upgrade on their own schedule by rerunning the install command with a version pin.

**Regional connector gateways:** Yes, from Phase 1. EU is the minimum first-release region alongside US. Connector gateways are regional; connector records store the assigned gateway region. The install script detects the closest gateway by latency or uses a region flag if passed. Customers with data residency requirements must route connector traffic to a gateway in their required region; this is documented in the setup flow and selectable in the UI when creating a connector.

**Minimum audit detail for database access:** Actor + org + repository/session + connector ID + resource ID + capability + query fingerprint (normalized, not raw SQL) + affected schema and table names + result row count + duration + success/failure + denial reason. Full SQL result sets, raw query text beyond fingerprint, and any credential material must not appear in audit events. Customers who need full normalized SQL for compliance can enable enhanced audit mode per connector, which records the full normalized statement (parameters redacted) as an opt-in with explicit admin confirmation.

## Product Decision

143 should build the Private Connector as the default productized way to access firewalled customer logs and databases. The connector should not be a broad private-network tunnel. It should expose explicit action capabilities for agents.

The first release covers VictoriaLogs and read-only Postgres for coding agents. This keeps the product story narrow while establishing the reusable architecture for future private infrastructure integrations.
