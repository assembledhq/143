# Design: Agent Log Provider Tools

> **Status:** Not Started | **Last reviewed:** 2026-05-23

## Summary

Coding agents should query production logs through `143-tools`, using a provider-neutral logging interface with provider-native query passthrough. In production, sandbox agents should call a narrow server-side log proxy instead of receiving raw logging-provider credentials. The first providers are:

- `victorialogs`, backed by the existing self-hosted VictoriaLogs deployment.
- `mezmo`, backed by Mezmo's log analysis APIs.

The agent-facing tool shape should stay the same across providers, while the `query` field remains the provider's native query language. The CLI should expose one small command set (`log_query`, `log_context`, `log_fields`, and `log_stats`) with an optional `--provider` flag instead of provider-prefixed commands. This keeps the agent's prompt small and predictable without flattening LogsQL, Mezmo search syntax, or future provider-specific query features into a lowest-common-denominator language.

Unlike other integration tool categories in `143-tools` that use provider-prefixed names (e.g., `sentry_list_errors`, `linear_list_tasks`), log tools share a fixed command set and route by `--provider` flag. This is a deliberate divergence: the log tool set is small and stable, provider selection benefits from being an explicit argument rather than a name prefix, and agent prompts stay compact as additional providers are added. See [Registry And Tooling Changes](#registry-and-tooling-changes) for the dispatch model.

## Goals

- Give sandbox coding agents read-only access to production logs through `143-tools`.
- Keep the tool surface compact enough to inject into every relevant agent run.
- Support multiple logging backends through one Go interface.
- Preserve each backend's native query language and advanced search features.
- Require explicit time windows and bounded result limits so agents cannot accidentally scan full retention.
- Return stable, structured output that agents can summarize, filter with `jq`, and cite in session reasoning.
- Keep production provider credentials server-side by default.
- Keep direct MCP usage available for IDE or operator workflows, but not as the default sandbox-agent path.

## Non-Goals

- Do not build a universal log query language.
- Do not expose write operations, alert edits, dashboard edits, or pipeline mutations to coding agents.
- Do not require Mezmo MCP to be installed in every sandbox.
- Do not replace Grafana as the operator UI for VictoriaLogs.
- Do not change the production log shipping path as part of this design.

## Agent-Facing Commands

Use one fixed logging command set in the generated `143-tools` skills doc. Provider selection is a flag, not part of the command name:

```bash
143-tools log_query --provider victorialogs --query 'service:api AND level:error AND _time:[now-1h,now]' --since 1h --limit 100
143-tools log_context --provider victorialogs --query 'service:api AND level:error' --timestamp 2026-05-22T12:34:56Z --since 10m --before 25 --after 25

143-tools log_query --provider mezmo --query 'level:error service:api' --since 1h --limit 100
143-tools log_context --provider mezmo --query 'level:error service:api' --timestamp 2026-05-22T12:34:56Z --since 10m --before 25 --after 25
```

`log_context` should prefer the strongest anchor available from a prior `log_query` result:

1. `--cursor` when the provider returned an opaque event cursor.
2. `--id` when the provider returned a stable event/log ID.
3. `--timestamp` plus `--query` and bounded time flags as the portable fallback.

Required interface commands:

- `log_query`: run a native provider query over a bounded time range.
- `log_context`: fetch neighboring logs around a cursor, stable event ID, or timestamp + query anchor.
- `log_fields`: list common indexed/queryable fields for the provider or configured dataset.
- `log_stats`: run lightweight aggregate queries for counts over time or grouped counts.

The initial implementation should prioritize all four commands. `log_stats` is included in v1 for VictoriaLogs, which has native LogsQL stats support. For Mezmo, `log_stats` is deferred until the provider aggregation API surface is confirmed. Recent-log use cases should use `log_query --since <short-window> --limit <n>` instead of adding a separate tail command in v1.

### Provider Context Research

The context interface should not assume every provider has a stable per-log ID:

- VictoriaLogs is stream-oriented. LogsQL has `_stream_id` for selecting a log stream, a `stream_context before N after M` pipe for surrounding logs, and query endpoints for stream IDs, stream fields, field names, and field values over explicit time ranges. That means VictoriaLogs context can be strong when the target query identifies a specific stream/log, but the portable anchor is still `timestamp + query + bounded time range`.
- Mezmo's public Log Analysis API documentation emphasizes views, export/search query parameters, time ranges, and pagination-style retrieval. The search docs describe field and whole-log query syntax, but the docs do not establish a provider-agnostic stable log-line ID plus "surrounding lines" endpoint. That means Mezmo context should be implemented as a bounded query around a timestamp, optionally using any opaque cursor or event ID returned by the API when available.

The design consequence: `log_context` accepts `id`, `cursor`, or `timestamp + query`, but examples and agent instructions should teach the portable timestamp + query path first.

### Provider Selection

`--provider` should be optional:

- If exactly one log provider is configured, use it.
- If multiple providers are configured and the org's active logging integration config has a default provider, use that provider.
- For local development and self-hosted operator setups without org integration config, `LOG_PROVIDER_DEFAULT` may provide the same fallback.
- If multiple providers are configured and there is no default, return `ErrLogProviderAmbiguous` listing available providers and ask the agent to retry with `--provider`.
- If `--provider` is set to an unknown or unconfigured provider, return a short error listing configured providers.

This keeps the common case as terse as possible while still allowing side-by-side Mezmo and VictoriaLogs access:

```bash
143-tools log_query --query 'service:api AND level:error' --since 1h
```

## Go Interface

Add a new integration category in `internal/services/integration/types.go`:

```go
type LogProvider interface {
    Name() ProviderName
    QueryLogs(ctx context.Context, req LogQueryRequest) (*LogQueryResult, error)
    GetLogContext(ctx context.Context, req LogContextRequest) (*LogContextResult, error)
    ListLogFields(ctx context.Context, req LogFieldsRequest) (*LogFieldsResult, error)
    QueryLogStats(ctx context.Context, req LogStatsRequest) (*LogStatsResult, error)
}

// LogStatsProvider is kept for providers that cannot implement stats efficiently.
// Implement LogProvider.QueryLogStats to return ErrLogStatsUnsupported when the
// backend cannot handle the request. The tool layer checks for this sentinel and
// surfaces a provider-specific unsupported message instead of a generic error.
type LogStatsProvider interface {
    LogProvider
    SupportsStats() bool
}
```

### Error Types

Define sentinel errors in `internal/services/integration/log_errors.go`. The MCP/CLI dispatch layer checks these with `errors.Is` to produce agent-readable messages, following the same pattern as `ErrLinearUnauthorized` in the existing integration package:

```go
var (
    ErrLogProviderUnconfigured = errors.New("no log provider configured")
    ErrLogProviderAmbiguous    = errors.New("multiple log providers configured, specify --provider")
    ErrLogProviderUnknown      = errors.New("unknown or unconfigured log provider")
    ErrLogAnchorInsufficient   = errors.New("anchor insufficient for this provider")
    ErrLogTimeBoundRequired    = errors.New("time bound required: provide --since or --start_time and --end_time")
    ErrLogProviderUnauthorized = errors.New("log provider unauthorized")
    ErrLogRateLimited          = errors.New("log provider rate limit exceeded")
    ErrLogCursorInvalid        = errors.New("cursor is invalid, expired, or does not match current query parameters")
    ErrLogStatsUnsupported     = errors.New("log stats not supported by this provider")
)
```

### Request Types

```go
type LogQueryRequest struct {
    Query      string
    Since      *time.Duration
    StartTime  *time.Time
    EndTime    *time.Time
    Limit      *int
    Direction  *LogDirection
    Fields     []string
    Cursor     *string
    // IncludeRaw uses *bool rather than bool to let the proxy distinguish an
    // explicit false from an absent field. This prevents a future default change
    // from silently enabling raw payload delivery: nil means "apply org default
    // (currently false)"; explicit false means never return raw payloads for
    // this request regardless of org default.
    IncludeRaw *bool
}

type LogContextRequest struct {
    Anchor     LogAnchor
    Query      *string
    Since      *time.Duration
    StartTime  *time.Time
    EndTime    *time.Time
    Before     *int
    After      *int
    Fields     []string
    IncludeRaw *bool
}

type LogFieldsRequest struct {
    Query *string
    Since *time.Duration
    Limit *int
}

type LogStatsRequest struct {
    Query     string
    Since     *time.Duration
    StartTime *time.Time
    EndTime   *time.Time
    GroupBy   []string
    Interval  *time.Duration
    Limit     *int
}

type LogAnchor struct {
    ID        *string
    Cursor    *string
    Timestamp *time.Time
}

type LogDirection string

const (
    LogDirectionDesc LogDirection = "desc"
    LogDirectionAsc  LogDirection = "asc"
)
```

Optional scalar request fields use pointers so the service layer can distinguish "unset" from a real zero value. Slices can use `nil` to represent unset. `LogQueryRequest` should accept either `since` or `start_time`/`end_time`; the CLI should reject requests with neither. Providers may translate `since` into their native syntax or API time-range fields. If the provider's query language also embeds time filters, the explicit CLI time range still acts as the outer safety bound where the API supports it.

The `--provider` flag is a tool-layer selector, not a provider request field. The MCP/CLI dispatch layer always constructs a `LogToolSelector` first to resolve the provider, then constructs the appropriate request type:

```go
type LogToolSelector struct {
    Provider *string
}
```

### Result Types

```go
// LogEntry.Provider uses ProviderName — the same type used throughout
// internal/models/credentials.go — to ensure consistent provider identifiers.
type LogEntry struct {
    ID        string         `json:"id,omitempty"`
    Timestamp time.Time      `json:"timestamp"`
    Provider  ProviderName   `json:"provider"`
    Message   string         `json:"message,omitempty"`
    Level     string         `json:"level,omitempty"`
    Service   string         `json:"service,omitempty"`
    TraceID   string         `json:"trace_id,omitempty"`
    RequestID string         `json:"request_id,omitempty"`
    OrgID     string         `json:"org_id,omitempty"`
    Fields    map[string]any `json:"fields,omitempty"`
    Raw       map[string]any `json:"raw,omitempty"`
}

type LogQueryResult struct {
    Provider   ProviderName `json:"provider"`
    Query      string       `json:"query"`
    StartTime  time.Time    `json:"start_time"`
    EndTime    time.Time    `json:"end_time"`
    Entries    []LogEntry   `json:"entries"`
    NextCursor string       `json:"next_cursor,omitempty"`
    Truncated  bool         `json:"truncated"`
}

// LogContextResult includes PrevCursor and NextCursor for cases where the
// before/after window itself requires pagination (e.g. --before 100 --after 100
// hitting the 400KB response cap).
type LogContextResult struct {
    Provider   ProviderName `json:"provider"`
    Anchor     LogAnchor    `json:"anchor"`
    Before     []LogEntry   `json:"before"`
    Target     *LogEntry    `json:"target,omitempty"`
    After      []LogEntry   `json:"after"`
    PrevCursor string       `json:"prev_cursor,omitempty"`
    NextCursor string       `json:"next_cursor,omitempty"`
}

type LogFieldsResult struct {
    Provider ProviderName `json:"provider"`
    Fields   []LogField   `json:"fields"`
}

type LogField struct {
    Name         string `json:"name"`
    Type         string `json:"type,omitempty"`
    SampleValues []any  `json:"sample_values,omitempty"`
}

type LogStatsResult struct {
    Provider  ProviderName     `json:"provider"`
    Query     string           `json:"query"`
    StartTime time.Time        `json:"start_time"`
    EndTime   time.Time        `json:"end_time"`
    Series    []LogStatsSeries `json:"series"`
    Truncated bool             `json:"truncated"`
}

type LogStatsSeries struct {
    Group   map[string]string `json:"group,omitempty"`
    Buckets []LogStatsBucket  `json:"buckets"`
}

type LogStatsBucket struct {
    Timestamp time.Time `json:"timestamp,omitempty"`
    Count     int       `json:"count"`
}
```

The normalized fields are conveniences for agents. The original provider record should not be returned by default. It may be included in `raw` only when `IncludeRaw` is non-nil and true, after redaction and output-size limits are applied, and only when the caller's role is authorized for raw payloads (see [Authorization Model](#authorization-model)).

## CLI Semantics

### `log_query`

Flags:

- `--provider` optional. Selects the configured provider, such as `victorialogs` or `mezmo`.
- `--query` required. Passed through to the provider unchanged except for shell escaping.
- `--since` optional duration, such as `15m`, `1h`, `24h`.
- `--start_time` optional RFC3339 timestamp.
- `--end_time` optional RFC3339 timestamp.
- `--limit` optional, default `100`, max `1000`.
- `--direction` optional, `desc` default, `asc` allowed.
- `--fields` optional comma-separated field projection. If the provider does not support field projection, the proxy omits unrequested fields from the normalized result client-side; it does not return an error.
- `--cursor` optional provider-opaque pagination cursor from a prior response.
- `--include_raw` optional boolean, default `false`.

Validation:

- Require either `--since` or both `--start_time` and `--end_time`.
- Reject lookbacks beyond a configured max, initially `7d` for agents even if the backend retention is longer.
- Reject `--limit` above the configured max.
- Do not mutate provider query text. Provider adapters can add API-level time bounds, but they should not rewrite user search terms unless required by the backend SDK.
- If `--cursor` is provided, the proxy validates cursor integrity before dispatch. Cursors are HMAC-SHA256 signed by the proxy using a per-org signing key. The cursor payload encodes provider, query, start_time, end_time, direction, and fields. If the signature is invalid or the current request parameters do not match the cursor's encoded constraints, return `ErrLogCursorInvalid` without querying the backend.

Time-bound semantics:

- CLI time bounds are mandatory safety bounds.
- Provider-native time filters inside `--query` are allowed, but results must be the intersection of provider-native filters and CLI safety bounds.
- If a provider cannot safely apply API-level time bounds around a native query, reject the request rather than scanning outside the CLI bounds.

Response size limits:

- Individual entry `message` field: capped at 10KB.
- Individual non-message field values: capped at 1KB.
- Total response: capped at 200KB. If the result set would exceed this, truncate entries and set `truncated: true`.

Output:

- JSON only.
- Keep entries in deterministic timestamp order matching `direction`.
- Include `truncated: true` when the provider returns more data than the command emitted.
- Return `next_cursor` only when the provider can continue the exact same query safely and the cursor has been HMAC-signed by the proxy.

### `log_context`

Flags:

- `--provider` optional. Selects the configured provider.
- `--id` optional stable event ID anchor.
- `--cursor` optional provider-opaque event cursor anchor.
- `--timestamp` optional RFC3339 timestamp anchor.
- `--query` optional fallback scope when the backend needs the original query to find neighboring records.
- `--since` optional duration when timestamp anchoring needs a safety range.
- `--start_time` optional RFC3339 timestamp.
- `--end_time` optional RFC3339 timestamp.
- `--before` optional, default `20`, max `100`.
- `--after` optional, default `20`, max `100`.
- `--fields` optional comma-separated field projection.
- `--include_raw` optional boolean, default `false`.

Validation:

- Require one anchor: `--id`, `--cursor`, or `--timestamp`.
- When using `--timestamp`, require `--query` plus `--since` or `--start_time`/`--end_time` so the provider has a bounded neighborhood to search.
- If a provider cannot use the supplied anchor, return `ErrLogAnchorInsufficient` with a message naming the missing anchor shape, such as `log_context requires --timestamp and --query for victorialogs`.
- When the timestamp anchor matches multiple log entries (e.g., entries with identical millisecond timestamps), the provider should return the entry whose other fields best match `--query`. If disambiguation is not possible, return all matching candidates in the `target` position and document the ambiguity in the response.

Response size limits:

- Same per-entry limits as `log_query`.
- Total response: capped at 400KB. If the before/after window exceeds this, truncate the most distant entries first and populate `prev_cursor` or `next_cursor` for continuation.

For VictoriaLogs, context may need to use `_time` plus available stable fields because raw event IDs are not always present in the stored record. The provider should prefer a log `_stream_id`/offset/cursor if available; otherwise use the portable `timestamp + query + bounded time range` fallback.

### `log_fields`

Flags:

- `--provider` optional. Selects the configured provider.
- `--query` optional provider-native scope.
- `--since` optional, default `24h`, max `7d`.
- `--limit` optional, max number of field names or sampled records depending on provider.

Response size limit: 50KB total.

This helps agents discover whether fields like `request_id`, `trace_id`, `org_id`, `agent_run_id`, `service`, or `level` are present before writing narrower queries.

### `log_stats`

Flags:

- `--provider` optional. Selects the configured provider.
- `--query` required.
- `--since` or `--start_time`/`--end_time` required.
- `--group_by` optional comma-separated fields.
- `--interval` optional duration for time buckets.
- `--limit` optional, max grouped rows.

Response size limit: 100KB total.

Provider adapters should return `ErrLogStatsUnsupported` when they cannot implement stats efficiently. For VictoriaLogs, this maps naturally to LogsQL `stats` queries and is a v1 deliverable. For Mezmo, implementation depends on the available API aggregation surface; if unavailable, return `ErrLogStatsUnsupported` initially.

## Integration Settings And Credentials

Log provider setup should live in the existing frontend Integration settings page, not in a hidden admin-only surface. The product UX should expose a Logging/Observability integration card with provider choices for VictoriaLogs and Mezmo.

Credential storage should follow the existing integration pattern:

- Store provider credentials in `org_credentials`, scoped by `org_id`.
- Use provider identifiers such as `mezmo` and `victorialogs`.
- Store secrets such as Mezmo API keys encrypted in the credential payload.
- Store non-secret provider configuration, such as base URL, region, dataset/view identifiers, and default provider selection, in the active integration config record when that provider needs it.
- Resolve credentials server-side. For production org-connected providers, expose logs to sandboxes through a narrow 143 log proxy instead of injecting raw provider credentials.

Deployment-level environment variables remain useful for local development, self-hosted bootstrapping, and platform-owned VictoriaLogs access, but the normal product path for a customer-connected Mezmo or VictoriaLogs provider should be Integration settings -> `org_credentials` -> server-side provider registry -> server-side log proxy -> sandbox `143-tools`.

### Proxy Token Lifecycle

In production, sandboxes receive a short-lived proxy token rather than raw provider credentials.

- **Format:** JWT signed by the 143 backend, containing `org_id`, `session_id`, `allowed_providers` (list of `ProviderName`), `issued_at`, and `expires_at`.
- **TTL:** 1 hour. The orchestrator issues a fresh token at session start and re-issues on session resumption.
- **Refresh:** The orchestrator re-issues a replacement token before expiry. The sandbox does not refresh tokens directly; if a token expires mid-session, the proxy returns `ErrLogProviderUnauthorized` with a `retry_after` hint, and the orchestrator re-issues a token on the next tool invocation.
- **Cursor interaction:** Cursors encode the proxy token's `expires_at`. The proxy rejects cursor continuation attempts that reference an expired token, returning `ErrLogCursorInvalid`.
- **Scope:** Tokens are scoped to `allowed_providers` at issuance. Adding a new provider mid-session requires a token re-issue.

For local development, inject `VICTORIALOGS_URL` or `MEZMO_API_KEY` directly via environment variables. Do not use this path in production.

### VictoriaLogs Endpoint Types

For VictoriaLogs, the settings UX should ask for a **VictoriaLogs-compatible query endpoint**, not specifically for a raw VictoriaLogs server. The endpoint can be:

- VictoriaLogs directly, when the customer has enabled TLS/auth and accepts the exposure model.
- `vmauth`, which is the preferred lightweight VictoriaMetrics-native proxy for read/query endpoint restriction.
- Another HTTPS reverse proxy or ingress, such as Caddy, Nginx, Traefik, Envoy, HAProxy, or a cloud load balancer.
- Grafana's datasource proxy, when the customer already exposes Grafana securely and wants to use a Grafana service account token.

The UI should present `vmauth` or another read-only HTTPS proxy as the more secure recommended setup, because it can expose only the query/read path instead of the full VictoriaLogs HTTP surface. Direct VictoriaLogs should be supported for low-friction setup, but described as appropriate only when protected by TLS, strong auth, and preferably firewall allowlisting.

## Network Access For Firewalled Providers

Some customer VictoriaLogs deployments will sit behind a firewall or private network. The design should avoid requiring broad inbound access to VictoriaLogs and should never require sandbox containers to reach customer private networks directly.

Supported access patterns for v1, in order of recommendation:

1. **Customer-managed HTTPS read-only gateway.** The customer exposes a narrow HTTPS endpoint in front of VictoriaLogs that allows only the read/query endpoints needed by `log_query`, `log_context`, `log_fields`, and `log_stats`. `vmauth` is the VictoriaMetrics-native option here and is the recommended lightweight secure setup. Caddy, Nginx, Traefik, Envoy, HAProxy, or an existing cloud/API gateway are also acceptable. Require TLS, provider auth, request limits, and ideally mTLS.
2. **Direct VictoriaLogs with built-in auth.** For simpler deployments, customers can point 143 at VictoriaLogs directly if TLS and strong auth are enabled and the endpoint is appropriately firewalled. This is the lowest-friction setup, but it is less precise than a read-only proxy because direct VictoriaLogs exposes the broader VictoriaLogs HTTP surface unless the deployment is otherwise restricted.
3. **Static 143 egress allowlist.** For customers that prefer IP allowlisting, 143 can publish a small stable egress IP/CIDR set per region for the server-side log proxy. The customer firewall allows only those egress addresses to reach the query endpoint. This should still use TLS and application-layer auth; IP allowlisting alone is not sufficient. Treat this as a documented enterprise networking option with explicit region selection and change-management notice, not as the only supported private-logging pattern.

**Deferred — customer-side outbound connector.** A connector model where the customer runs a 143-managed relay inside their network that maintains an outbound mTLS connection to 143 is the ideal solution for strict private-network deployments: it avoids inbound firewall openings and keeps customer network policy simple. However, it requires a separate design covering connector certificate provisioning, deployment packaging, relay protocol, versioning, and lifecycle management. This is explicitly out of scope for v1. A follow-on design doc will be filed before implementing connector support.

The server-side log proxy remains the only component that talks to customer logging backends. Sandboxes receive only the proxy endpoint and a short-lived scoped token.

## Provider Implementations

### VictoriaLogs

Configuration:

- Org-scoped credential/configuration from Integration settings is primary.
- Store any customer-provided VictoriaLogs-compatible endpoint credential in `org_credentials`.
- Store non-secret endpoint details, such as endpoint type (`direct`, `vmauth`, `reverse_proxy`, `grafana_datasource`), query URL, region, datasource ID, or default dataset/scope, in the active integration config.
- For production sandbox agents, resolve the org-scoped configuration server-side and expose only a narrow 143 log proxy token/URL to `143-tools`.
- Inject direct runtime environment such as `VICTORIALOGS_URL` only for local development or self-hosted operator modes that explicitly bypass the proxy.
- Deployment-owned environment such as `VICTORIALOGS_HOST`, SSH host, or SSH key is only for platform-operated/self-hosted access paths and should not be the normal customer setup path.

Preferred runtime path:

1. For 143's own dogfooding and operator debugging, keep repo-local `make logs-query` unchanged.
2. Do not expose `make logs-query` to connected customer repositories or sandbox agents. It is a 143 repo/operator convenience, not the customer-facing logging interface.
3. For production sandbox agents, route VictoriaLogs queries through the server-side log proxy.
4. Do not give sandboxes broad private-network access to the logging host. The proxy should accept the same `LogQueryRequest` shape and execute the query server-side with org/session authorization.

Query behavior:

- `query` is raw LogsQL.
- The provider should require explicit time bounds even though LogsQL can embed `_time`.
- If the query does not contain `_time`, the adapter adds an API-level time range or wraps the query in the VictoriaLogs request parameters when supported.
- Existing docs already emphasize `_time` filters for `make logs-query`; this tool should enforce that safety property instead of relying on prompt instructions.
- `log_context` should use VictoriaLogs-native anchors when available: `_stream_id` narrows to a specific stream, and the LogsQL `stream_context before N after M` pipe can retrieve surrounding logs for matching records. When the target cannot be uniquely identified, fall back to `timestamp + query + bounded time range` and return the nearest matching records around the timestamp.
- `log_fields` maps to VictoriaLogs field/stream field discovery endpoints over the same bounded query range.
- `log_stats` maps to LogsQL `stats` queries and is a v1 deliverable.

Multi-tenant query isolation:

If 143 operates a shared VictoriaLogs deployment serving multiple orgs, the proxy must inject a mandatory org-isolation filter before executing any query. For example, the proxy wraps the agent query: `{org_id="<org_id>"} | <agent_query>`. The proxy must never forward a raw agent query to a multi-tenant deployment without first scoping it to the requesting org. Single-tenant VictoriaLogs deployments (one per customer) do not require injected filters, but the proxy must verify the configured endpoint belongs to the requesting org.

### Mezmo

Configuration:

- Org-scoped credential/configuration from Integration settings is primary.
- Store Mezmo API keys in `org_credentials`.
- Store non-secret provider-specific account, base URL, region, dataset, or view identifiers in the active integration config.
- For production sandbox agents, resolve Mezmo credentials server-side and expose only a narrow 143 log proxy token/URL to `143-tools`.
- Inject direct runtime environment such as `MEZMO_API_KEY` and `MEZMO_BASE_URL` only for local development or self-hosted operator modes that explicitly bypass the proxy.
- Direct deployment env configuration is acceptable only for local development and self-hosted operator setups.

Query behavior:

- `query` is raw Mezmo search syntax.
- Use API-level start/end timestamps whenever available.
- Normalize returned records into `LogEntry`.
- Preserve provider fields in `raw`, after redacting known secret-like keys, only when `IncludeRaw` is authorized and true.
- If Mezmo supports saved views or datasets, keep them as optional flags later rather than baking them into the base interface.
- Do not assume Mezmo has a stable per-line ID suitable for context lookup. If the API response includes an opaque ID or cursor, preserve it and allow `log_context --id` or `--cursor`; otherwise implement context through `timestamp + query + bounded time range`.
- `log_stats`: return `ErrLogStatsUnsupported` initially. Implement once the Mezmo API aggregation surface is confirmed.

MCP stance:

- Do not run Mezmo MCP inside normal coding-agent sandboxes.
- Direct Mezmo MCP remains acceptable for IDE integrations, external MCP clients, or future operator workflows where the user explicitly opts into a richer tool catalog.
- If a future Mezmo-only capability is valuable for agents and not exposed by the API client, add a narrow, allowlisted `143-tools` command for that capability rather than registering the whole MCP catalog.

## Registry And Tooling Changes

Integration package:

- Add `LogProvider` and `LogStatsProvider`.
- Add registry methods:
  - `RegisterLogProvider(provider LogProvider)`
  - `LogProviders() []LogProvider`

MCP/CLI registry:

Unlike other integration tool categories that use provider-prefixed names, log tools share a fixed command set routed by `--provider`. `ToolRegistry.ListTools()` emits the fixed set (`log_query`, `log_context`, `log_fields`, `log_stats`) exactly once, regardless of how many log providers are configured.

`ToolRegistry.CallTool()` cannot use prefix-matching for log tools. Add an explicit check before the provider-prefix loop:

```go
switch name {
case "log_query", "log_context", "log_fields", "log_stats":
    return tr.callLogTool(ctx, name, args)
}
// ... existing prefix-match loop for other providers
```

`callLogTool` parses a `LogToolSelector` from args, calls `resolveLogProvider(selector)` to select the concrete `LogProvider`, then constructs and dispatches the appropriate request type. Provider resolution errors (`ErrLogProviderUnconfigured`, `ErrLogProviderAmbiguous`, etc.) are returned directly without reaching the provider.

The generated CLI skills doc should show the small fixed command set plus the configured provider names. Include `log_stats` in the skills doc only if at least one configured provider's `SupportsStats()` returns true; if the selected provider returns `ErrLogStatsUnsupported`, surface a clear provider-specific unsupported message.

Environment builder:

- In production agent sandboxes, register one proxy-backed `LogProvider` per configured backend after the server has resolved org-scoped logging integrations and injected a short-lived proxy token/URL. For example, a proxy-backed provider whose `Name()` returns `mezmo` and another whose `Name()` returns `victorialogs`. This keeps provider resolution compatible with the existing registry model instead of adding a special multi-provider registry path.
- Direct `MEZMO_API_KEY`, `MEZMO_BASE_URL`, or `VICTORIALOGS_URL` env support is acceptable only for local development and self-hosted operator setups.
- Register VictoriaLogs against a deployment-owned safe query endpoint only when the sandbox policy explicitly allows it. Avoid falling back to SSH from inside regular coding-agent sandboxes.
- Resolve the default provider from the org's active logging integration config when multiple log providers are configured. Read `LOG_PROVIDER_DEFAULT` only as a local development or self-hosted fallback when no org config is available.

Orchestrator:

- Inject only the proxy endpoint and short-lived token needed by configured log providers in production.
- Do not inject raw provider credentials into production sandboxes.
- Include a short instruction in `IntegrationSkills`: always provide a time bound when querying logs.

## Authorization Model

Logging integrations are org-scoped and should follow the same authorization posture as other integration settings:

- Only admins can connect, disconnect, or edit logging-provider credentials.
- Members and builders may use configured log tools during sessions when the session itself belongs to the org and the selected repository/session is authorized for that user.
- Viewers should not trigger agent log queries through sessions unless the product explicitly grants read-only debugging access later.
- The server-side log proxy must authorize every request against `org_id`, session/thread identity where available, user role, configured provider status, and configured provider scope.

**`--include_raw` authorization:** Raw provider payloads are restricted to org admins. Member and builder roles may pass `--include_raw true` but the proxy will reject the request with a clear authorization error. Admins can also configure `raw_access_enabled: false` at the org level to disable raw access entirely, overriding role-based permissions. This restriction exists because raw payloads may contain provider metadata and partially-redacted content not visible in normalized output.

**Audit logging:** The proxy audits provider, query truncated to 500 characters (with a `query_truncated: true` flag when the query exceeds that length), time bounds, limit, result count, session/thread IDs, and caller metadata. Storing the query truncated — rather than hashed — preserves investigability for abuse review while bounding storage size. The proxy does not audit full raw log payloads.

## Rate Limiting

The server-side log proxy enforces per-org and per-session limits to prevent runaway agent sessions from generating excessive cost or impacting logging backend availability.

Default limits (configurable by org admins in Integration settings):

- Per-session: 60 log tool calls per minute, 500 total per session.
- Per-org: 300 log tool calls per minute across all active sessions.
- Max data egress per tool call: see response size limits in [CLI Semantics](#cli-semantics).
- Max data egress per session: 20MB.

When a rate limit is exceeded, the proxy returns `ErrLogRateLimited` with a `retry_after` hint in seconds. The tool layer surfaces this as a clear error message rather than silently truncating.

Mezmo-specific: because Mezmo charges per query and per GB processed, apply tighter default limits for Mezmo-backed providers: 20 tool calls per minute per session, 5MB per session egress. Admins can increase these limits in Integration settings.

## Security And Safety

- Read-only only.
- Require bounded time windows.
- Cap response sizes as specified per command in [CLI Semantics](#cli-semantics).
- Return normalized fields by default. Only include provider raw payloads when `--include_raw true` is explicitly set and the caller's role is authorized (see [Authorization Model](#authorization-model)).
- **v1 redaction scope:** Redact field values whose field names match sensitive patterns at all levels of nested JSON: `token`, `secret`, `password`, `cookie`, `authorization`, `api_key`, `session`, `key`, `credential`, `auth`, `private`. This is field-name-based matching only and applies before populating both `Fields` and `Raw`.
- **Future redaction (deferred from v1):** Pattern-based secret scanning within freeform message text, URLs, stack traces, and header values. This is deferred due to high false-positive risk in freeform log text. When implemented, reference an established secret-pattern library (e.g., trufflesecurity/trufflehog regexes) rather than an ad-hoc list.
- Cap each log entry `message` field at 10KB and each non-message field value at 1KB before returning data to the sandbox.
- Cursor integrity: all pagination cursors are HMAC-SHA256 signed by the proxy. Unsigned or tampered cursors return `ErrLogCursorInvalid` without querying the backend.
- Do not pass production provider credentials to agents; use the narrow internal proxy for org-connected providers.
- Treat `org_id` filters as strongly recommended but not universally required: platform debugging sometimes starts from `request_id`, `agent_run_id`, `trace_id`, or service-level symptoms. Product-facing workflows should include `org_id` when known.

## Testing Plan

- Unit-test CLI flag validation for time bounds, limits, and required query fields.
- Unit-test provider resolution for single-provider, default-provider, missing-provider, unknown-provider, and ambiguous-provider cases.
- Unit-test registry behavior so configured providers emit the fixed shared tool names, not one command per provider.
- Unit-test `callLogTool` dispatch routing: verify that `log_query`, `log_context`, `log_fields`, and `log_stats` are handled by the log dispatch path, not the provider-prefix loop.
- Unit-test server-side log proxy authorization for org, role, session/thread, inactive provider, provider-scope, and `--include_raw` role failures.
- Unit-test proxy token issuance, validation, and expiry behavior.
- Unit-test cursor HMAC signing: valid cursor passes, tampered cursor returns `ErrLogCursorInvalid`, expired-token cursor returns `ErrLogCursorInvalid`.
- Unit-test cursor constraint enforcement: cursor with mismatched query, time bounds, direction, or fields returns `ErrLogCursorInvalid`.
- Unit-test rate limiting: per-session and per-org limits trigger `ErrLogRateLimited` with `retry_after`.
- Unit-test multi-tenant query isolation: verify the proxy injects org-scoping filters for shared VictoriaLogs deployments and does not inject them for single-tenant deployments.
- Unit-test provider adapters with mocked HTTP responses.
- Add settings/validation tests for VictoriaLogs-compatible endpoint types: direct, `vmauth`, generic reverse proxy, Grafana datasource proxy.
- Add table-driven tests for field-name-based redaction across all nesting levels.
- Add table-driven tests for output truncation and response size limits per command.
- Add table-driven tests for raw-payload opt-in and role authorization.
- Add context-anchor tests for ID, cursor, timestamp + query, timestamp ambiguity (multiple matching entries), and insufficient-anchor errors.
- Add `LogContextResult` pagination tests: verify `PrevCursor` and `NextCursor` are populated when the before/after window exceeds the 400KB cap.
- Add `LogFieldsResult` shape tests: verify field names, types, and sample values are populated correctly per provider.
- Add VictoriaLogs query construction tests to ensure time bounds are always applied.
- Add `log_stats` tests for VictoriaLogs using mocked LogsQL stats responses.
- Add `ErrLogStatsUnsupported` path test for Mezmo.
- Add Mezmo response normalization tests using representative API payloads.
- Add integration tests behind explicit env flags for real provider calls; do not run them in normal CI.

## Rollout Plan

1. Add the interface, error types, and CLI tool definitions with a fake test provider. Verify `callLogTool` dispatch routing works independently of the provider-prefix loop.
2. Add the Integration settings UX and org-scoped credential storage in `org_credentials`.
3. Add VictoriaLogs endpoint-type support in settings: direct, `vmauth`, generic reverse proxy, Grafana datasource proxy. Present `vmauth` or another read-only proxy as the recommended secure path. Connector support is explicitly deferred; do not include it in the settings UX at this step.
4. Add the server-side log proxy with org/session/role authorization, proxy token issuance and validation (JWT, 1h TTL), HMAC cursor signing, audit logging with truncated query storage, time-bound enforcement, field-name redaction, response size truncation, and rate limiting.
5. Register the sandbox-side 143 log proxy provider through short-lived proxy tokens, not raw provider credentials.
6. Implement `log_query --provider victorialogs` against the proxy-backed VictoriaLogs adapter, including multi-tenant filter injection for shared deployments.
7. Implement `log_fields --provider victorialogs`, `log_context --provider victorialogs` with ID/cursor support where available plus timestamp + query fallback, and `log_stats --provider victorialogs` using LogsQL stats queries.
8. Implement `log_query --provider mezmo` and `log_fields --provider mezmo`.
9. Add `log_context --provider mezmo` once the stable event ID/cursor shape is confirmed, with timestamp + query fallback if provider IDs are unavailable.
10. Add `log_stats --provider mezmo` once the Mezmo API aggregation surface is confirmed; otherwise leave `ErrLogStatsUnsupported` in place.
11. Update agent prompt examples and production debugging docs.
12. File follow-on design doc for customer-side outbound connector (firewalled VictoriaLogs deployments).

## Provider Optimizations

- If Mezmo exposes a stable event identifier or opaque event cursor in the API response, preserve it in `LogEntry.ID` or cursor metadata and use it as the preferred `log_context` anchor. This is an optimization, not a v1 blocker, because `timestamp + query + bounded time range` is the portable fallback.
- If VictoriaLogs exposes per-entry stable identifiers in a future API version, adopt them as the preferred anchor over `_stream_id` + timestamp.
