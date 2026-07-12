# Design: Scalable Low-Latency Live Updates Over SSE

> **Status:** Implemented | **Last reviewed:** 2026-07-12

## Summary

143 should standardize live UI refresh on a shared Server-Sent Events architecture that is designed for both low tail latency and bounded production cost:

- Postgres remains the source of truth.
- User-visible state changes and a short-lived live-event outbox row commit atomically.
- A low-latency publisher writes each event to a per-org Redis replay Stream and a fixed-shard Redis live bus.
- Each API process holds only a bounded number of Redis live-bus subscriptions, independent of active org or browser count, and fans events out in-process.
- Browsers apply safe, versioned status projections immediately for the most visible transitions, then selectively refetch canonical REST data.
- Long polling intervals are correctness backstops, not the normal update path.

The first migration targets the highest-polling dashboard surfaces:

- `/sessions`
- session detail
- `/previews`
- preview detail and embedded preview panels
- `/automations`
- automation detail and runs

The same transport and frontend dispatcher replaces the one-off SSE implementations across code reviews, PR health, eval batches, and eval bootstrap. Their obsolete compatibility routes have been removed; focused high-volume session append traffic remains on its resource stream and shares one typed backend reader per session.

The following decisions are load-bearing and fixed in this design:

1. **Bounded Redis fan-out.** Live delivery uses a fixed number of Redis bus shards per API process, not one blocking Redis reader per active org or browser.
2. **Replay is separate from live delivery.** A per-org Redis Stream provides short replay through one-shot range reads; API processes do not block on one `XREAD` per org.
3. **No commit/publish gap.** State changes and a short-lived outbox row commit in the same Postgres transaction. Redis remains non-authoritative, but a process crash after commit cannot silently lose the low-latency notification.
4. **Stream tiering.** The org stream carries low/medium-frequency list, summary, and ordinary detail updates. High-frequency logs, transcript appends, and file-event feeds stay resource-scoped and deliver append entries rather than repeatedly invalidating large queries.
5. **Two UI paths.** Small typed and versioned projections update critical status UI immediately; selective REST refetch reconciles full canonical state.

## Problem

Several frontend surfaces keep data fresh with fixed polling loops, many at 3s, 5s, or 10s intervals. Background polling usually stops when a tab is hidden, but foreground pages still run many concurrent polling queries against data that is unchanged most of the time.

The worst offender is session detail, which can run 11 or more simultaneous polling queries while a session is active:

- session detail, transcript, human-input requests, readiness checks, and timeline every 3s
- PR health and file events every 5s
- runtime status every 15s
- additional queries that refetch on related activity

Other hot patterns include:

- sessions list, counts, and sidebar every 10s
- previews index sections every 5-30s
- preview detail and panels every 3-15s
- automations list, detail, and runs every 10s
- automation goal-improvement queries every 3s

A single active session-detail tab can sustain more than one request per second. At thousands of foreground tabs, that becomes avoidable API and Postgres load while still making users wait for the next polling boundary.

The product requirement is the inverse: near-zero work when nothing changes, very low latency when something does, and immediate visual acknowledgement for the acting user's own mutation.

## Goals

1. Make the acting user's own mutation visible immediately through optimistic intent state or the mutation response.
2. Deliver critical status changes to other clients with p95 under 750ms and p99 under 2s after commit when the live path is healthy.
3. Reduce idle and unchanged foreground polling to near zero.
4. Collapse the session-detail polling fan-out without replacing it with event-triggered refetch fan-out.
5. Keep Postgres authoritative and use Redis only for replay and low-latency delivery.
6. Preserve low-latency notification across process crashes between database commit and Redis publication.
7. Bound Redis connections by a fixed shard count rather than active orgs, resources, or clients.
8. Bound browser-to-Postgres amplification during write bursts, reconnects, deploys, and Redis recovery.
9. Suppress refetches in hidden tabs and perform one explicit catch-up when a tab becomes visible.
10. Use resource-scoped append streams for any high-volume feed.
11. Centralize event schemas, publication, replay, connection handling, visibility registration, query scheduling, and fallback polling.
12. Preserve multi-org and narrower resource authorization for long-lived connections.
13. Make event publication lag, replay health, connection health, browser freshness, and fallback load observable.

## Non-Goals

1. Replacing Postgres reads with streamed full records.
2. Guaranteeing delivery of every individual state-transition event to every browser. Each materialized ordinary or aggregate outbox event is published to Redis at least once and idempotently; source rows may be folded, and browser delivery is best-effort and collapsible, with eventual canonical-state convergence through replay, resync, visibility catch-up, and polling.
3. Building a user-visible notification inbox.
4. Introducing WebSockets for dashboard invalidation.
5. Moving existing session logs off their proven resource stream in the first deployment.
6. Making Redis required for durable state changes.
7. Streaming full transcripts, diffs, prompts, or other large records through the org event channel.

Small, typed status projections and durable append entries on resource streams are explicitly allowed. They are not substitutes for canonical REST records.

## Current Baseline

The codebase already has several SSE primitives with different cost profiles:

- `SessionStreams` uses Redis Streams. The session SSE handler currently subscribes to three separate per-session readers—logs, status, and narrow events. Each blocking `XREAD` goroutine holds a dedicated Redis connection, fans out to local clients, and exits when no clients remain. Logs have an in-memory replay ring and Postgres catch-up.
- `PullRequestStreams`, `CodeReviewStreams`, `EvalBatchStreams`, and `EvalBootstrapStreams` use Redis Pub/Sub but open one subscription per browser connection.
- `useResourceSSE` closes a failed `EventSource`, constructs a new one with exponential delay, and stops after five failures. Constructing a new object loses the browser-maintained `Last-Event-ID` unless the application carries it explicitly.
- Existing SSE handlers accept `?org_id=` because EventSource cannot send the active-org header. Session logs also accept `?last_event_id=` for manual reconnection.
- The shared SSE writer sends comment heartbeats and clears the server write deadline, but comment frames are invisible to browser JavaScript and writes do not currently have a per-frame deadline.
- TanStack Query globally disables `refetchOnWindowFocus`, so a query marked stale while hidden will not automatically refetch merely because the document becomes visible again.

The correct parts are durable state first, lightweight live delivery, replay or polling fallback, and optimistic local state. The gaps are unbounded Redis-reader cardinality, the commit/publish failure window, incomplete replay semantics, and insufficient control over browser refetch amplification.

## Architecture

### End-To-End Flow

Every low/medium-frequency live update follows this sequence:

1. A service writes canonical state and a validated `live_event_outbox` row in the same Postgres transaction.
2. The transaction commits.
3. The caller signals a process-local live-event dispatcher and returns without waiting on Redis.
4. The dispatcher claims the outbox row and publishes it through the global coalescer.
5. Publication performs `XADD` to the org replay Stream, obtains the Redis Stream ID, and publishes the event plus Stream ID to one fixed Redis live-bus shard.
6. Every API process has a bounded subscription to the fixed live-bus shards and routes the event only to matching local org subscribers.
7. Each SSE handler writes the Stream ID as the SSE `id:` field.
8. The browser dispatcher applies a newer safe projection immediately when available, marks affected caches stale, and selectively refetches only visible registered queries.
9. Sparse, jittered polling and explicit visibility catch-up remain correctness backstops.

If the immediate dispatcher or Redis fails, the committed outbox row remains pending. A background outbox worker retries it. The user-facing state change does not depend on Redis availability.

### Subscription Topology

Use two product-facing stream tiers:

| Layer | Browser scope | Carries | Frequency and semantics |
| --- | --- | --- | --- |
| Org event stream | One logical connection per active org per browser profile; per-tab fallback where sharing is unavailable | List/summary invalidations, ordinary detail changes, small versioned status projections | Low/medium; collapsible and replayable |
| Resource stream | One connection per focused high-volume resource | Logs, transcript/message appends, file-event appends, preview log appends | High; append entries with stable IDs and gap recovery |

If an event can fire many times per second for one resource, grows an append-only feed, or only matters while a specific viewer is focused, it belongs on a resource stream. The org stream must remain sparse because its Redis bus message is received by every API process and its matching event is delivered to every connected browser for that org.

For a focused active session, the resource stream owns detail-status and append updates. The org stream still wakes list/count/summary surfaces, but the page must not refetch the same session detail twice from both streams. Resource-stream registrations take precedence for that focused resource.

### Redis Delivery Topology

Live delivery and replay use different Redis structures:

1. **Per-org replay Stream**
   - Key: `143:stream:{org:<org_id>}:live_events`.
   - Initial hard cap `MAXLEN ~ 1024`; the final cap is derived from measured event size/rate and the fleet replay-memory budget rather than chosen independently per org.
   - Every publication pipelines `XADD`, `XTRIM MINID` for entries older than five minutes, and expiry refresh. This makes ordinary trimming write-driven and avoids a fleet-wide key scan.
   - Key expiry refreshed to one hour on publication so inactive org keys disappear.
   - Replay is capped at 1,000 returned entries per reconnect; a larger gap collapses to `live.resync`.
   - A bounded active-key registry supports reconciliation of streams that missed write-driven trimming; maintenance scans the registry in small batches, never the full Redis keyspace.

2. **Fixed-shard live bus**
   - Default 32 stable shards, configurable only through an explicit capacity change.
   - An org maps to one shard through a stable hash of `org_id`.
   - Standalone/Sentinel deployments use bounded process-level Pub/Sub subscriptions.
   - Redis Cluster uses sharded Pub/Sub (`SPUBLISH`/`SSUBSCRIBE`) so traffic is routed to the owning cluster shard.
   - Every API process subscribes to the fixed shard set and filters by `org_id` in memory.
   - Each local shard subscription has an explicit health epoch. It becomes healthy only after subscription acknowledgement and becomes unhealthy immediately on disconnect or read failure.

The number of long-lived Redis live-bus connections is therefore bounded by the configured bus shards and Redis topology. It does not grow with active orgs or browsers. The tradeoff is that API processes see sparse events for orgs without local clients; this is acceptable for the deliberately low/medium-frequency org stream and must be measured.

Replay storage has a fleet-level budget, initially 25% of configured Redis `maxmemory`. The publisher records average encoded event bytes and active Stream count and estimates replay working-set size. At 20% it warns and tightens retention for the noisiest Streams; at 25% it keeps live Pub/Sub delivery but trims affected replay Streams to a minimal tail so reconnecting clients receive `live.resync` instead of allowing replay data to pressure unrelated Redis workloads. Rollout configuration must provide a finite `maxmemory`; an unknown or unbounded Redis memory limit is not acceptable for enabling replay.

An alternative fixed-partition Stream reader topology may be reconsidered only if measured Pub/Sub bandwidth dominates. It must preserve the same fixed upper bound and cannot regress to one blocking reader per active org.

## Backend Design

### Event Envelope

Add typed live-event contracts in `internal/models`:

```go
type LiveEventType string
type LiveResourceType string
type LiveEventScope string
type LiveAudienceScope string

const (
    LiveEventScopeResource   LiveEventScope = "resource"
    LiveEventScopeCollection LiveEventScope = "collection"

    LiveAudienceOrg        LiveAudienceScope = "org"
    LiveAudienceRepository LiveAudienceScope = "repository"
    LiveAudienceResource   LiveAudienceScope = "resource"
)

type LiveEvent struct {
    SchemaVersion int                 `json:"schema_version"`
    EventID       uuid.UUID           `json:"event_id"`
    StreamID      string              `json:"-"` // assigned after XADD; emitted as SSE id
    Type          LiveEventType       `json:"type"`
    Scope         LiveEventScope      `json:"scope"`
    OrgID         uuid.UUID           `json:"org_id"`
    ResourceType  LiveResourceType    `json:"resource_type"`
    ResourceID    *uuid.UUID          `json:"resource_id,omitempty"`
    ParentType    *LiveResourceType   `json:"parent_type,omitempty"`
    ParentID      *uuid.UUID          `json:"parent_id,omitempty"`
    RepositoryID  *uuid.UUID          `json:"repository_id,omitempty"`
    Audience      LiveAudienceScope   `json:"audience"`
    Version       *int64              `json:"version,omitempty"`
    CausationID   *uuid.UUID          `json:"causation_id,omitempty"`
    ChangedAt     time.Time           `json:"changed_at"`
    Payload       json.RawMessage     `json:"payload"`
}
```

`EventID` is stable across retries and duplicate Redis entries. `StreamID` is Redis transport metadata and is assigned after `XADD`; it is not serialized into the stored payload. The SSE handler writes `StreamID` as `id:`. A Redis Stream ID looks like `1783684800123-0`.

`LiveEventType`, `LiveResourceType`, `LiveEventScope`, and `LiveAudienceScope` have named constants and `Validate() error` methods. `LiveEvent.Validate()` enforces:

- supported schema version
- event type and payload match
- collection events have no `ResourceID`
- resource events have a `ResourceID`
- repository/resource audiences carry the identifiers needed for filtering
- patchable projection events carry a positive monotonic `Version`
- payload size remains within the transport limit

`Payload` is never populated from an arbitrary map. Each event type has a typed payload and constructor, for example:

```go
type SessionUpdatedPayload struct {
    StatusProjection *SessionLiveProjection `json:"status_projection,omitempty"`
    ListAffected     bool                   `json:"list_affected"`
    CountsAffected   bool                   `json:"counts_affected"`
}

type SessionLiveProjection struct {
    Status          SessionStatus `json:"status"`
    PRCreationState PRActionState `json:"pr_creation_state"`
    PRPushState     PRActionState `json:"pr_push_state"`
}
```

Payloads must not include logs, transcripts, diffs, prompts, branch names, titles, repository names, arbitrary user text, secrets, or full list records.

### Monotonic Live Versions

Critical projections require a monotonic resource revision so reordered delivery cannot roll the UI backward.

- Add `live_version bigint NOT NULL DEFAULT 1` to the session, preview, automation, and automation-run records that emit projections.
- Increment `live_version` in the same statement or transaction as any field represented in a live projection.
- Return `live_version` from the corresponding REST endpoints.
- Set the event envelope `Version` from the committed row.
- A browser applies a projection only when its version is newer than the cached entity version.

Collection-only invalidations do not require a version. Versioning is not used as a global ordering mechanism across different resources.

### Transactional Outbox

Add a short-lived outbox table:

```sql
CREATE TABLE live_event_outbox (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id),
    event_type text NOT NULL,
    coalesce_key text,
    event jsonb NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    claim_owner text,
    claim_expires_at timestamptz,
    aggregate boolean NOT NULL DEFAULT false,
    published_at timestamptz,
    folded_into_event_id uuid,
    last_error text,
    originated_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX live_event_outbox_pending_idx
    ON live_event_outbox (available_at, claim_expires_at, created_at)
    WHERE published_at IS NULL AND folded_into_event_id IS NULL;

CREATE INDEX live_event_outbox_pending_age_idx
    ON live_event_outbox (originated_at)
    WHERE published_at IS NULL AND folded_into_event_id IS NULL;
```

The outbox is a delivery mechanism, not a durable product event history:

- State and outbox insert are atomic.
- A process-local dispatcher receives an after-commit wake-up and claims the row immediately.
- `live_event_outbox.id` is the event envelope's stable `EventID`.
- `originated_at` is the original state-transition time used for lag SLOs. A materialized aggregate inherits the earliest `originated_at` of its source rows so coalescing cannot reset or hide delivery lag.
- A claimant starts a short Postgres transaction, selects eligible rows with `FOR UPDATE SKIP LOCKED`, and atomically sets `claim_owner`, a bounded `claim_expires_at`, and `attempts`. It commits before any Redis I/O.
- Redis publication occurs outside the database transaction. Only the current unexpired owner may mark the row published; losing a lease can create a duplicate publication but cannot lose the row.
- A crashed or timed-out claim becomes eligible again when `claim_expires_at <= now()`. Workers use a unique process/worker claim owner, a default 30-second lease, and bounded renewal only when a measured publish attempt genuinely needs longer.
- A background worker claims pending or expired rows with exponential backoff.
- Successfully published or folded rows are deleted in bounded batches after a short retention period, initially one hour.
- An alert fires when the oldest pending row exceeds the live-update latency SLO.
- Validate static event inputs before opening the state transaction. Construct and validate the final event, including returned resource ID and `live_version`, inside the transaction before commit so a schema error rolls back both state and outbox insertion.

Redis unavailability never rolls back an already committed state change. A general Postgres failure may still fail the transaction, as it does for any other durable write.

### Publication And Global Coalescing

Coalescing must work across API and worker processes. Per-process timers are not a correctness or load bound.

Each outbox row may carry a stable `coalesce_key`, such as:

- `<org_id>:session:list`
- `<org_id>:session:<session_id>`
- `<org_id>:preview:index`
- `<org_id>:automation:stats`

The global publisher follows leading-plus-trailing semantics:

1. The first row for a quiet key acquires a short Redis lease and publishes immediately.
2. Rows arriving while the lease is active remain pending rather than being marked delivered.
3. At the end of the 250-500ms window, the outbox dispatcher—or the next fast-path publisher to acquire the expired lease—groups every pending row for the key.
4. In one Postgres transaction it takes a transaction-scoped advisory lock derived from `(org_id, coalesce_key)`, locks the eligible source rows, builds the deterministic typed aggregate, inserts that aggregate as a new `live_event_outbox` row with a stable UUID/EventID and `aggregate = true`, and sets every source row's `folded_into_event_id` to the aggregate row ID. The Redis lease controls timing; the Postgres lock prevents two expired lease holders from materializing competing aggregates.
5. Only after that transaction commits does a normal outbox claimant publish the materialized aggregate. A crash or retry therefore reuses the same event identity and payload.
6. Continued writes create at most one trailing aggregate per window.
7. If Redis is unavailable, the outbox worker materializes aggregates from pending rows before retry publication so recovery does not replay every original write.

The coalescer must preserve all affected families. It may combine many resource changes into a collection event with `ResourceID == nil`, but it must not fold a required detail update into an unrelated broad list event.

Publication order for one event is:

1. `XADD` the validated event to the per-org replay Stream.
2. Capture the returned Redis Stream ID.
3. Publish `{stream_id, event}` to the org's fixed live-bus shard.
4. Mark the outbox row published.

A crash between these steps can create a duplicate, which is safe because both ordinary and aggregate `EventID` values are durably stable and browser handling is idempotent. It cannot create a permanent silent gap because the materialized outbox row remains retryable until marked published.

### In-Process Fan-Out And Backpressure

Each API process maintains:

- one bounded live-bus subscriber set
- an in-memory map from `org_id` to local SSE subscribers
- no blocking Redis reader per org

SSE health is coupled to the complete Redis delivery path for the org. It requires both a healthy local bus-shard subscription and publisher/outbox lag below the live-update degradation threshold:

- A local shard disconnect/read failure atomically marks that shard unhealthy.
- Repeated `XADD`/Pub/Sub publication failures or oldest-pending outbox age above two seconds mark the shared live service degraded even if subscriber sockets remain open.
- The fan-out manager stops emitting healthy heartbeats for affected orgs, writes one local `live.degraded` control frame where possible, and closes their SSE connections within the bounded frame deadline.
- The frontend immediately enters degraded polling and application-controlled reconnect.
- The Redis subscriber must re-establish and acknowledge the shard subscription before new SSE handshakes for that shard return `live.ready`.
- Existing browser connections are never kept open across a shard subscription gap. Reconnection is the replay barrier that recovers events published while the API node was disconnected.
- A failure of one shard closes only connections for orgs mapped to that shard; a client-wide Redis failure closes all org live streams on the node.

This prevents API-generated heartbeats from masking a disconnected subscriber, failed publisher, or growing outbox backlog. Bus disconnects degrade immediately. Lag/command health uses hysteresis: degrade after two consecutive one-second samples above the two-second lag threshold (or a configured consecutive command-failure threshold), and recover only after subscription acknowledgement, a successful publish probe, and three consecutive samples below 500ms. This avoids turning a single slow publish into a fleet reconnect wave.

Each API node runs one bounded live-health monitor, not one monitor per connection. It samples the indexed oldest-pending outbox age at most once per second, combines it with local Redis command/subscription health, and publishes one in-process health state to all local handlers. This bounded database read is part of the capacity budget and is disabled when no org SSE connections exist on the node.

Each browser subscriber uses a small collapsible mailbox, initially 16 distinct pending keys:

- Resource events collapse by `(event_type, resource_id)` and retain the newest `Version`.
- Collection events collapse by `(event_type, scope)`.
- A one-slot wake channel tells the SSE writer that mailbox work exists.
- If the number of distinct pending keys exceeds the limit, the mailbox clears and sets `needs_resync` instead of buffering hundreds of events.
- The handler captures the latest replay high-water ID, emits `live.resync` with that checkpoint, flushes, and closes the HTTP connection. It does not continue delivering live events past an unacknowledged gap.

Every SSE frame and flush uses a bounded write deadline, initially five seconds. A client that cannot accept a heartbeat or control frame within the deadline is disconnected as a slow network consumer. The frontend reconnects and replays or resyncs. The shared SSE writer must expose flush errors rather than silently ignoring them.

### Race-Free Replay

On connection or manual reconnect:

1. Validate authentication, active org, and authorization before opening the stream.
2. Register the browser subscriber in buffering mode so newly published live-bus events cannot race past catch-up.
3. Capture the current replay Stream tail as the connection high-water mark.
4. If no cursor is supplied, do not replay older entries and set `initial_sync_required: true` for the eventual `live.ready`. The browser must complete one canonical synchronization of visible registered queries before acknowledging that high-water mark as its resume cursor. Events published after the high-water mark remain buffered/delivered normally and mark affected queries dirty.
5. If a cursor is supplied, compare it with the Stream's first and last IDs.
6. If the cursor is retained, run a bounded `XRANGE` from the cursor through the captured high-water mark.
7. Deliver replay entries in Stream-ID order, deduplicating by `EventID`.
8. Drain buffered live events, skipping Stream IDs at or below the high-water mark.
9. Switch the subscriber to live mode.
10. Emit and flush `live.ready` with the high-water mark, initial-sync flag, and current bus-health epoch.

An org with no replay Stream or no entries uses the sentinel high-water `0-0`, which is a valid application resume cursor before the first auto-generated Redis Stream ID.

If the cursor is older than the first retained entry, malformed, ahead of the current Stream, or would require more than 1,000 replay entries:

1. Capture the current replay high-water mark.
2. Send `live.resync` with `through_stream_id` set to that mark.
3. Flush and close the SSE connection.
4. The frontend pauses reconnect, performs one canonical synchronization of visible registered query families, and retains its old resume cursor if synchronization fails.
5. After successful synchronization, the frontend advances its application-managed resume cursor to `through_stream_id` and reconnects. Events published after that checkpoint replay on the new connection.

A resync is a correctness fallback, not an HTTP error. The client tracks separate `received_cursor` and `resume_cursor` values; only successfully processed ordinary events or a successfully completed initial/resync synchronization advance `resume_cursor`.

Synthetic control events (`live.ready`, `live.heartbeat`, `live.degraded`, `live.resync`, and `server.draining`) do not carry SSE IDs and therefore do not implicitly advance the browser cursor. `live.ready` and `live.resync` carry explicit high-water fields whose acknowledgement follows the rules above.

### Publish Sources

Initial org-stream events:

| Domain | Event | Scope | Projection | Publish when |
| --- | --- | --- | --- | --- |
| Sessions | `session.created` | Collection | optional new-row summary deferred | session and primary thread commit |
| Sessions | `session.updated` | Resource or collection aggregate | status/PR action projection | status, archive, title, PR/branch/push, sandbox, or linked-summary version commits |
| Previews | `preview.updated` | Resource or collection aggregate | preview status/freshness projection | preview instance/target/current/freshness/prewarm state commits |
| Automations | `automation.updated` | Resource or collection aggregate | enabled/status projection | create/update/pause/resume/delete/config commits |
| Automation runs | `automation.run.updated` | Resource or collection aggregate | run status projection | run creation/status/linked-session commits |
| Code reviews | `code_review.updated` | Resource or collection aggregate | status/decision projection where safe | current code-review update commits |
| Pull requests | `pull_request.updated` | Resource | health/action projection where safe | current PR health update commits |
| Evals | `eval_batch.updated` | Resource | status projection | batch/run state commits |
| Evals | `eval_bootstrap.updated` | Resource | status projection | bootstrap state commits |

Initial resource-stream events:

| Resource | Event | Semantics |
| --- | --- | --- |
| Session | log/message append | deliver the durable append entry with stable ID |
| Session | `session.thread.updated` | focused detail status projection/invalidation |
| Session | `session.human_input.updated` | typed request transition or narrow invalidation |
| Session | `session.file_event.appended` | deliver durable append entries, batch where appropriate |
| Preview | `preview.log.appended` | deliver durable append entries while the log panel is visible |

Do not emit an org `session.updated` detail refetch when the focused session resource stream already carries the same version. The org event may still update session lists, counts, and other tabs.

### Authorization And Mid-Stream Revocation

The org stream must enforce the same visibility boundary as REST. IDs alone are not a security boundary.

At handshake, the server computes a cached authorization snapshot for the user and org:

- org-visible resources accept `Audience == org`
- repository-scoped events require the repository in the snapshot
- resource-scoped/private events require an explicit resource grant or a narrower stream

Filtering is performed in memory from the event envelope and snapshot. It must not query Postgres once per event or once per browser connection on every heartbeat.

Membership and authorization changes use an event-driven connection registry:

1. Membership/permission state commits.
2. A control event is written through the same transactional outbox and published to all API nodes after commit.
3. Each node closes matching `(user_id, org_id)` connections or forces their authorization snapshot to refresh.
4. As a Redis-loss safety net, authorization is singleflight-revalidated per unique `(user_id, org_id)` at most once every 60 seconds, shared by all local connections for that pair.
5. Connections also have a randomized maximum lifetime of 15-30 minutes and must reauthorize on reconnect.

Target revocation latency is under five seconds when the control bus is healthy and under 60 seconds during a Redis outage. Session expiry is enforced independently through the connection's auth expiry timer.

## SSE API Contract

Add:

```text
GET /api/v1/events/stream?org_id=<uuid>&last_event_id=<redis-stream-id>
```

`org_id` is required. `last_event_id` is optional and is used when application-controlled reconnect constructs a new EventSource. The server also accepts the standard `Last-Event-ID` header for native browser reconnects.

Handshake errors:

| Status | Code | Meaning |
| --- | --- | --- |
| 400 | `INVALID_ORG_ID` | malformed or missing `org_id` |
| 400 | `INVALID_LAST_EVENT_ID` | malformed cursor; client should reconnect without it and resync |
| 401 | `UNAUTHORIZED` | no valid session |
| 403 | `FORBIDDEN` | user cannot subscribe to the org |
| 503 | `LIVE_EVENTS_UNAVAILABLE` | live bus unavailable or node over capacity |

Response headers:

- `Content-Type: text/event-stream`
- `Cache-Control: no-cache, no-transform`
- proxy-specific no-buffer header where supported
- compression disabled for the event stream

Events:

```text
event: live.ready
data: {"server_time":"2026-07-11T12:00:00Z","schema_version":1,"initial_sync_required":false,"through_stream_id":"1783684800123-0","bus_health_epoch":17}

id: 1783684800124-0
event: live.event
data: {"schema_version":1,"event_id":"8f...","type":"session.updated","scope":"resource","org_id":"...","resource_type":"session","resource_id":"...","audience":"org","version":42,"changed_at":"2026-07-11T12:00:00Z","payload":{"status_projection":{"status":"running"},"list_affected":true,"counts_affected":false}}

event: live.heartbeat
data: {"server_time":"2026-07-11T12:00:15Z","bus_health_epoch":17}

event: live.degraded
data: {"cause":"redis_bus_disconnected","bus_shard":7}

event: live.resync
data: {"cause":"replay_window_missed","through_stream_id":"1783684800999-0"}

event: server.draining
data: {"retry_after_ms":3742}
```

The handler flushes `live.ready` after applicable replay entries and the connection's buffered-through-high-water entries have been written. `initial_sync_required` is true whenever no trusted resume cursor was supplied. A handler must not return `live.ready` while its bus shard or the shared publisher/outbox health is degraded. Heartbeats are real named events every 15-25 seconds with per-connection jitter so browser JavaScript can detect a stalled connection; they may be emitted only while the complete delivery path remains healthy at the advertised epoch. Optional comment heartbeats may also be sent for intermediary compatibility, but they do not count as client health evidence.

### Connection And Process Lifecycle

- Browser-to-edge HTTP/2 or HTTP/3 is required to avoid HTTP/1.1 per-origin connection starvation. Upstream proxy hops may use another protocol if they preserve streaming, capacity, flush behavior, and idle timeouts.
- Every proxy hop must disable event-stream buffering and use an idle timeout longer than the heartbeat interval.
- Admission control has global, per-org, and per-user connection budgets. Rejected connections return `503` with `Retry-After` where possible.
- Rate limiting for ordinary API requests must not turn a legitimate reconnect wave into a permanent lockout; stream handshakes use a dedicated limiter.
- On shutdown, the node first becomes unhealthy for new traffic, then sends `server.draining` with randomized retry guidance, flushes, and closes live connections before `http.Server.Shutdown` waits for completion.
- Maximum connection age is jittered so routine reauthorization does not synchronize clients.

## Frontend Design

### Optimistic Mutations And Causation

The acting user's own action must never wait on SSE.

- Generate a UUID `client_mutation_id` for each user mutation and send it through `X-Client-Mutation-ID`.
- On `onMutate`, cancel relevant stale reads, write an honest pending/intent state into affected caches, and keep rollback data.
- On mutation failure, roll back or show a scoped error state.
- On success, merge the mutation response into all affected caches.
- The backend propagates the ID into `LiveEvent.CausationID` and into follow-up job payloads where practical.
- The originating tab suppresses redundant refetches that would only echo its still-current optimistic transition. Other tabs and users process the event normally.
- A later higher `Version` from backend-driven convergence always wins over the optimistic state.

Optimism must not claim a terminal success before the backend has achieved it. For multi-step PR, branch, push, preview-start, and automation workflows, the immediate state is `starting`, `queued`, or equivalent intent.

### Shared Connection And Reconnect

Add `useLiveEventStream()` once in the authenticated dashboard layout.

The v1 transport uses EventSource with application-controlled reconnect:

- track separate `received_cursor` and safely acknowledged `resume_cursor` values
- rebuild the URL from `resume_cursor` when constructing a new EventSource
- use `withCredentials`
- close and recreate on active-org change
- treat `live.ready` as transport establishment, but do not consider live queries synchronized until any required initial synchronization completes
- when `initial_sync_required` is true, mark all visible registered live-query families dirty and complete one canonical synchronization before acknowledging `through_stream_id`
- on `live.resync`, pause reconnect, complete canonical synchronization, acknowledge `through_stream_id` only on success, and then reconnect so post-checkpoint events replay
- on `live.degraded`, immediately mark the stream unhealthy, enter degraded polling, honor reconnect jitter, and allow the server to close the connection
- reset a liveness timer on `live.heartbeat` and `live.event`
- mark unhealthy after two missed heartbeats or any EventSource error
- use full-jitter backoff: 1s, 2s, 4s, 8s, 15s, then 30-120s probes indefinitely
- pause reconnect timers while offline
- when no shared leader exists, pause aggressive reconnect while hidden and resume immediately on visibility
- never stop retrying permanently after a fixed attempt count

Native EventSource does not expose useful structured HTTP error bodies. After repeated failures, the hook may use the existing TanStack auth/membership query to determine whether the active org was revoked; otherwise it reports a generic unavailable state and continues low-rate probes.

### Same-Browser Connection Sharing

Before rollout beyond internal dogfood, one browser profile should hold at most one org EventSource where platform support permits:

- Prefer a SharedWorker as the connection owner.
- Use BroadcastChannel to distribute events, cursor, and health state to tabs.
- If a SharedWorker is unavailable, use a BroadcastChannel leader with a lease/election mechanism.
- Fall back to one EventSource per tab only when neither sharing mechanism is available.
- Follower tabs never independently reconnect while a healthy leader exists.

Cross-tab sharing reduces browser and API connections; it does not imply cross-tab TanStack cache sharing. Hidden follower tabs record dirty event families without refetching and catch up once visible.

### Visibility-Aware Query Registration

TanStack's “active” query state is not sufficient to decide whether a user can see a query. Add a shared registration API, for example:

```ts
useLiveQueryRegistration({
  queryKey,
  families: ["session.detail"],
  resourceId: sessionId,
  priority: "critical",
  visible: documentVisible && panelVisible,
});
```

Pages and shared components register live query families with:

- exact query key
- resource ID where applicable
- `critical`, `secondary`, or `inactive` priority
- actual visibility, including document, selected tab, collapsed panel, and focused resource
- whether a resource stream already owns the focused resource

When `document.visibilityState == "hidden"`, the dispatcher performs no event-triggered REST refetch. It records the newest cursor, projection versions, and dirty families. On visibility, it performs one prioritized catch-up of currently visible dirty queries because global `refetchOnWindowFocus` is disabled.

### Version-Safe Query Results

A projection must not be rolled back by a REST request that began before the represented commit.

All migrated live queries follow these rules:

- Detail/entity queries use a shared version-aware structural-sharing helper. When an incoming REST entity has a lower `live_version` than the cached entity, accept canonical non-projected fields but preserve the newer cached projection fields and version, then keep the query dirty for reconciliation.
- List helpers merge existing rows by resource ID and preserve newer projected fields when an incoming row has a lower `live_version`.
- Every migrated query function consumes TanStack's `AbortSignal` and passes it to the API request.
- If an affecting event arrives while a query is fetching and the request may have observed an older snapshot, the scheduler cancels that fetch once for the current dirty generation, applies the projection, and schedules one replacement fetch. Subsequent events in the same burst only update the dirty/version watermark; they do not repeatedly cancel and restart the replacement.
- Creation, deletion, or ordering events that cannot be protected by per-row versions always cancel a possibly stale list request once and require a replacement list fetch. They are never resolved solely through row merging.
- A cancelled or superseded response must not commit to TanStack cache even if the underlying network stack completes later.

The scheduler keeps a `minimum_accepted_version` per `(query_key, resource_id)` and a dirty generation per query key. Query helpers clear those guards only after a canonical response at or above the watermark is committed.

### Invalidation Scheduler

Do not call broad `invalidateQueries` directly from event listeners. The central scheduler:

1. Maps typed events to registered query families.
2. Applies a safe newer projection synchronously with immutable `setQueryData`/`setQueriesData`.
3. Marks matching queries stale with `refetchType: "none"`.
4. Starts refetches only for currently visible registered queries.
5. Maintains per-query `idle`, `fetching`, and `dirty_during_fetch` state.
6. Allows at most one active fetch and one trailing fetch per query key.
7. Uses leading-edge execution for critical queries and a 250-500ms debounce for secondary queries.
8. Cancels at most one potentially stale in-flight request per dirty generation and never repeatedly restarts its replacement during an event burst.
9. Marks inactive/unmounted cache entries stale without fetching them.
10. On resync, reconnect miss, or visibility catch-up, refreshes only visible registered queries with cause-specific jitter.

Jitter depends on the cause:

- individual mailbox overflow: 0-250ms
- ordinary reconnect: 0-1s
- fleet event such as deploy or Redis recovery: server-provided 1-10s window

Resource-stream ownership suppresses duplicate org-stream detail work for the focused resource while still allowing list and count refreshes.

### Projection Fast Path

For critical status UI, a valid newer projection updates visible state before REST refetch:

- session status and PR/branch/push action state
- preview status and freshness state
- automation enabled/state
- automation-run status
- later, code-review and PR-health badges where the payload is safe and versioned

Projection application is optional per event type but strict when enabled:

- reject unknown schema versions
- reject missing or non-increasing versions
- never create a complex missing record from a projection
- never reorder lists solely from a projection unless the event provides a complete typed ordering contract
- always retain a canonical reconciliation refetch according to query priority

This fast path targets the extra event-to-REST round trip without turning Redis into the source of truth.

### Polling And Degraded-Mode Policy

Create `frontend/src/lib/live-refresh-policy.ts` and remove local raw intervals from migrated surfaces.

| Situation | Policy |
| --- | --- |
| SSE healthy, list/index backup | 2-5 minutes with stable per-query jitter |
| SSE healthy, active detail backup | 30-60 seconds with stable jitter |
| SSE newly unhealthy, active detail | immediate visible-query refresh, then 5-15 seconds |
| SSE newly unhealthy, visible list/index | immediate visible-query refresh, then 10-30 seconds |
| SSE unhealthy for more than 2 minutes | active detail 15-30 seconds; list/index 30-90 seconds |
| explicit server-side state machine converging | 2-5 seconds for that resource only |
| hidden document | no polling; record dirty state and catch up on visibility |
| browser offline | no polling or reconnect until online |

Jitter is generated once per `(browser_client_id, query_key, policy_state)` rather than recomputed on every React render. A fleet-wide degradation signal may widen intervals, but an isolated client failure should not make an active detail view stale for 90 seconds.

### Query Effect Matrix

| Event | Immediate projection | Scheduled canonical work |
| --- | --- | --- |
| `session.created` | none initially | visible sessions list, sidebar, and counts |
| `session.updated` resource | newer status/action projection | matching visible detail unless resource stream owns it; visible affected lists/counts |
| `session.updated` collection | none | visible sessions list/counts only |
| `session.thread.updated` | focused thread/session projection | matching visible detail; transcript only if payload says transcript metadata changed |
| `session.human_input.updated` | request state where cached | matching human-input query and detail |
| `session.file_event.appended` | append stable entry | gap recovery only; no full refetch per append |
| `preview.updated` | newer status/freshness projection | matching visible detail/panel and affected visible index section |
| `preview.log.appended` | append stable entry | gap recovery only |
| `automation.updated` | newer enabled/status projection | matching visible detail and visible list |
| `automation.run.updated` | newer run projection | visible current run/runs page; stats on terminal event or trailing debounce |
| `code_review.updated` | safe status projection when versioned | matching/list queries as registered |
| `pull_request.updated` | safe health/action projection when versioned | matching PR-health query |
| `eval_batch.updated` | newer status projection | matching batch query |
| `eval_bootstrap.updated` | newer status projection | matching bootstrap query |
| `live.resync` | retain current UI | one cause-jittered refresh of visible dirty queries |

## Surface Migration Plan

### Sessions List And Sidebar

Replace 10s polling with `session.created` and `session.updated` collection effects.

- Update safe list-row status projections by `session_id` and `live_version` when the row is already cached.
- Coalesce list and count queries independently.
- Hidden tabs never refetch from events.
- Healthy backup: 3 minutes.
- Newly unhealthy: immediate visible refresh, then 10-30s.
- Extended unhealthy: 30-90s.

### Session Detail

Session detail uses:

- the existing resource SSE route for log/message append delivery
- resource events for thread, human-input, file-event, and focused status changes
- the org stream for list/summary effects and detail changes not already owned by the resource stream
- versioned projections for session status and action state
- optimistic pending state for the user's PR/branch/push actions
- short scoped polling only for a server-side state machine that is actively converging

Healthy backup is 60s while active and disabled or 5 minutes after terminal settle. Newly unhealthy detail refreshes immediately and then every 5-15s. Transcript, human input, file events, timeline, readiness, and detail must not independently poll every 3-5s when the resource stream is healthy.

As a follow-up consolidation, merge the current three per-session Redis readers into one typed per-session Redis Stream so a focused session costs one backend blocking reader rather than separate log, status, and event readers. This is not required to prove the org bus but is required before resource-stream concurrency materially exceeds current levels.

### Previews

Publish `preview.updated` for every preview state transition in the same transaction through the outbox. Apply versioned status/freshness projections immediately.

- Healthy index backup: 2-3 minutes.
- Newly unhealthy index: immediate visible refresh, then 10-30s.
- Healthy starting detail/panel backup: 60s.
- Newly unhealthy starting detail: immediate refresh, then 5-15s.
- Terminal detail: disabled or 5 minutes.

Preview logs move to append events on a preview resource stream when visible traffic justifies replacing the current short poll. Do not implement a log-appended event that refetches the complete log repeatedly.

### Automations

Publish `automation.updated` and `automation.run.updated` through the outbox. Apply versioned status projections immediately.

- Healthy list/detail backup: 3 minutes.
- Newly unhealthy visible list/detail: immediate refresh, then 10-30s.
- Active run detail newly unhealthy: immediate refresh, then 5-15s.
- Stats refetch on terminal run transitions or one 10-30s trailing refresh while runs remain active.

Goal improvement publishes proposal and transcript status events. User accept/reject actions update optimistically with causation IDs; scoped polling remains only while the backend state machine is converging.

## Existing SSE Consolidation

The repository converges on:

- one outbox-backed org event publisher
- one bounded fixed-shard Redis live bus
- one per-org replay Stream contract
- one backend in-process org fan-out manager
- one shared SSE writer with bounded frame deadlines and error-aware flush
- one frontend live transport and cross-tab sharing layer
- one typed event registry
- one visibility registration and invalidation scheduler
- one fallback policy
- resource append streams for high-frequency feeds

Migration sequence:

1. Keep the current session resource stream while the org path is proven.
2. Introduce stable event IDs and schema-versioned typed payloads.
3. Bridge PR health and code-review invalidations through the outbox-backed org bus.
4. Bridge eval batch/bootstrap ordinary status events through the org bus.
5. Keep resource streams where event volume or append semantics justify them.
6. Remove per-client Redis Pub/Sub subscriptions and duplicated frontend route builders after consumers migrate.
7. Consolidate the three per-session Redis readers when resource-stream scale requires it.

New domain-specific SSE helpers require a documented frequency, authorization, and replay reason.

## Performance And Scalability Requirements

### Capacity Model

Track these dimensions separately:

```text
browser_sse_connections_per_node
redis_live_bus_connections_per_node <= bounded(topology, live_bus_shards)
local_org_fanout_groups_per_node
resource_stream_readers_per_node
events_per_second_per_bus_shard
fanout_deliveries_per_second
event_triggered_rest_requests_per_second
bytes_per_second_to_browsers
```

Capacity is not defined only by distinct users. Tests must cover both high fan-out and high org cardinality:

- one org with 1,000 connected foreground tabs
- 10,000 orgs with one connected tab each
- realistic average tabs per browser profile
- active resource streams in addition to the org stream
- slow and stalled browser sockets

Initial targets remain 10,000 logical org-stream clients and 20,000 total SSE connections per API node on the chosen production instance class, but rollout is gated on measured file descriptors, heap, goroutines, per-frame write latency, live-bus bandwidth, and reconnect behavior. Browser-profile connection sharing should materially reduce physical connections below logical tab count.

### Redis

- Org live delivery uses fixed-shard Pub/Sub, not blocking `XREAD` per org.
- Per-org Streams are read only for bounded replay and inspected for replay-window misses.
- Live-bus connection count is bounded by topology and configured shards.
- Replay Streams use both entry-count and age limits plus key expiry.
- Replay storage has a configured fleet budget and sheds replay retention—not live delivery—before exceeding it.
- A local bus-shard failure closes affected SSE connections; API heartbeats cannot report healthy through a Redis subscription gap.
- Payloads remain under 4KiB; critical projections should normally remain under 1KiB.
- Outbox publication is idempotent by stable `EventID`.
- Redis Cluster uses sharded Pub/Sub rather than non-sharded broadcast.
- High-volume resource appends remain resource-scoped.
- Redis failure never fails a committed user state change.

### Postgres

- Idle foreground dashboards produce no repeated 10s list reads.
- Hidden tabs produce no event-triggered reads.
- A write burst is bounded by global publication coalescing plus browser query scheduling.
- The outbox worker uses bounded batches, expiring claims, `SKIP LOCKED`, backoff, and cleanup without holding a database transaction across Redis I/O.
- Outbox insertion adds one small row in the state transaction; measure commit latency and WAL volume.
- Live-service health adds at most one indexed oldest-pending-outbox query per API node per second while that node has org SSE clients.
- Event handlers and authorization filtering create no per-event or per-connection N+1 reads.
- Existing REST endpoints retain org filters, cursor limits, and focused query plans.

### Latency Targets

Measure both projection and reconciliation paths:

| Path | Target |
| --- | --- |
| commit to outbox-dispatch wake | p95 < 10ms |
| commit to Redis replay append/live-bus publish | p95 < 50ms, p99 < 150ms |
| Redis publish to browser handler | p95 < 200ms, p99 < 750ms |
| browser handler to critical projection render | p95 < 50ms |
| event to critical REST request start | p95 < 50ms when no projection satisfies the surface |
| critical REST request start to response | p95 < 350ms |
| response to rendered canonical UI | p95 < 50ms |
| commit to visible critical UI, other clients | p95 < 750ms, p99 < 2s |
| acting user's click to visible intent state | next render frame |
| outbox oldest unpublished age | p99 < 2s while Redis is healthy |
| missed replay self-heal | immediate resync on reconnect; otherwise within fallback policy |

Targets must be segmented by client region and endpoint. The end-to-end SLO includes REST completion and React render, not only event delivery or invalidation scheduling.

## Observability

### Server Metrics

Use bounded-cardinality labels such as event type, scope, bus shard, and result. Never label metrics by org or resource ID.

- active physical SSE connections
- logical tabs served through shared browser leaders, where reported
- active local org fan-out groups
- Redis live-bus connections and reconnects
- live-bus shard health epoch, disconnect duration, and affected SSE closures
- replay Stream key count, memory estimate, and trim count
- replay budget utilization and retention-shedding count
- live-bus publish/receive rate and bytes by shard
- fan-out deliveries
- mailbox collapse/resync count
- SSE frame write and flush latency
- slow-consumer disconnects
- outbox inserted, published, folded, retried, failed, and cleaned
- oldest unpublished outbox row age
- commit-to-publish latency
- replay result: hit, cursor missed, too large, malformed
- membership-revocation disconnect latency
- fallback poll count and reason
- event-triggered REST QPS by query family

### Client Telemetry

Sample browser telemetry to control volume:

- connection establishment and duration
- reconnect attempts and backoff state
- initial synchronization and resync checkpoint acknowledgement/failure
- heartbeat age and liveness timeout
- event receive lag from `ChangedAt`
- projection applied/rejected and reason
- event-to-refetch-start latency
- refetch completion latency
- response-to-render latency where measurable
- dirty-during-fetch trailing refresh count
- hidden-tab refetch suppression count
- visibility catch-up count
- shared-leader/follower state and failover
- fallback polling state

Sampled telemetry must avoid payload contents and user-entered data.

### Dashboards And Alerts

Compare before and after rollout:

- API QPS and Postgres read IOPS for sessions, previews, and automations
- Postgres transaction/WAL cost from the outbox
- p50/p95/p99 end-to-end freshness
- outbox pending age and retry rate
- Redis memory, connections, bus traffic, and replay misses
- SSE write tail latency and slow-consumer rate
- physical connections per active browser/user/org
- event burst to REST amplification

Alert on sustained outbox lag, live-bus disconnects, replay-miss spikes, SSE write p99 regressions, unexpected Redis connection growth, or fallback polling that remains elevated after Redis recovery.

## Testing Strategy

### Backend

- Table-driven validation tests for every typed event enum and payload.
- Tests that resource/collection and audience invariants reject malformed envelopes.
- Transaction tests proving state and outbox rows commit or roll back together.
- Tests that Redis failure does not fail an already committed state change.
- Outbox claim expiry/reclaim, ownership, retry, cleanup, duplicate, and process-crash tests that prove no transaction remains open during Redis I/O.
- Global coalescing tests across two publisher instances, including process death before aggregate materialization, after materialization, and after Redis publication. Retries must reuse the persisted aggregate EventID and payload.
- Tests that fixed live-bus subscriptions do not grow with org/client count.
- Tests that a live-bus shard disconnect suppresses heartbeats, emits `live.degraded`, closes only affected connections, rejects premature handshakes, and requires replay after subscription acknowledgement; publisher-lag tests cover degradation/recovery hysteresis without flapping.
- Fan-out mailbox tests for resource/version collapse and `needs_resync` overflow.
- Replay tests for attach-before-range buffering, cursorless initial synchronization, high-water dedupe, malformed/ahead/trimmed cursors, duplicate EventIDs, replay limit overflow, resync checkpoint success, and resync failure that retains the old cursor.
- Handler tests for auth, audience filtering, initial ready flush, named heartbeat, per-frame deadline, slow consumer, server drain, and mid-stream revocation.
- Tests that authorization checks are shared per `(user, org)` rather than executed per connection.
- Tests that projection versions increment in the same transaction as represented state.

### Frontend

- EventSource tests that capture `lastEventId` as the received cursor, reconstruct `last_event_id` from the separately acknowledged resume cursor, and retry indefinitely with full jitter.
- Tests for distinct received/resume cursors, including cursorless first load, failed initial synchronization, and post-checkpoint event replay.
- Heartbeat timeout and recovery tests.
- Tests that `live.degraded` immediately enters fallback and that an API heartbeat cannot mask a bus outage.
- SharedWorker/leader election, follower, and leader-failover tests.
- Typed event decode and unknown-schema rejection tests.
- Projection tests for newer, duplicate, stale, missing-record, and causation-echo cases.
- Visibility registration tests covering hidden documents, collapsed panels, inactive tabs, and focused resources.
- Scheduler tests proving one active and at most one trailing fetch per query key.
- Tests where an old detail/list REST response completes after a newer projection; it must not roll back projected fields, remove a newly created row, or commit after cancellation.
- Tests that event bursts cancel at most one stale fetch per dirty generation and do not repeatedly cancel/restart the replacement.
- Tests that resource-stream ownership suppresses duplicate org detail refetches.
- Tests that hidden tabs make zero event-triggered REST requests and perform one catch-up on visibility.
- Optimistic mutation rollback and convergence tests.
- Fallback policy tests for newly degraded, sustained degraded, offline, and recovery states.

### Load And Failure Testing

- 10,000 distinct orgs with one logical client each per node.
- One org with 1,000 foreground clients and 100 writes/second for 60 seconds.
- 20,000 total physical SSE connections where per-tab fallback is active.
- Browser-profile connection sharing with multiple visible and hidden tabs.
- Slow readers, zero-window/stalled sockets, and clients that stop consuming heartbeats.
- Redis command failure between `XADD` and Pub/Sub publish.
- Publisher process death before and after outbox claim and during a coalescing window.
- Redis outage and restoration with outbox backlog coalescing.
- Replay-memory pressure across 10,000 active org Streams, including warning and hard retention-shedding thresholds without loss of live Pub/Sub delivery.
- Replay-window hits, misses, and oversized catch-up.
- Rolling API deployment with `server.draining` and reconnect jitter.
- Membership revocation during Redis availability and outage.
- Verify that burst-time Postgres QPS remains bounded and hidden tabs contribute no reads.

Run transport tests through the production-equivalent edge/proxy path, not only directly against a Go test server.

## Rollout Plan

### Phase A — Core Infrastructure And Sessions

1. Add typed events, monotonic live versions for session projections, and `live_event_outbox` behind a feature flag.
2. Add the fixed-shard Redis live bus, per-org replay Streams, global coalescer, outbox worker, and telemetry.
3. Add the race-free `/api/v1/events/stream` handler with authorization snapshots, named heartbeats, bounded writes, drain behavior, and admission control.
4. Add the EventSource transport, cursor persistence, visibility registration, invalidation scheduler, projection path, and polling policy.
5. Validate browser-to-edge HTTP/2/H3, proxy buffering, compression, idle timeout, and production-equivalent frame flush latency.
6. Resolve and test org/repository/resource audiences before any non-internal org receives events.
7. Wire session publication only. In internal development, compare event effects while existing polling still runs.
8. Migrate sessions list/sidebar, then session detail, to projections plus selective refetch and sparse fallback.
9. Enable same-browser connection sharing before expanding beyond dogfood, retaining per-tab fallback for unsupported browsers.
10. Gate on end-to-end freshness, bus-disconnect propagation, cursorless initial synchronization, resync checkpointing, stale-response rejection, outbox reclaim/materialization, replay-memory admission, 10,000-distinct-org capacity, write-heavy fan-out, slow-consumer, hidden-tab, and rolling-deploy tests.

### Phase B — Remaining Product Surfaces

11. Add live versions and migrate automations list/detail/runs.
12. Add live versions and migrate previews index/detail/panel.
13. Add preview resource append streaming if measured log polling remains material.

### Phase C — Consolidation

14. Bridge code reviews, PR health, and eval streams through the shared outbox/live bus.
15. Retire per-client Redis Pub/Sub subscriptions.
16. Remove raw fixed intervals and duplicated frontend SSE helpers from migrated surfaces.
17. Consolidate session logs/status/events to one per-session Redis resource Stream when resource-reader capacity warrants it.
18. Delete obsolete compatibility routes only after all supported clients migrate.

## Acceptance Criteria

1. Acting-user session, preview, and automation mutations render an honest optimistic state in the next frame and reconcile without flicker.
2. Other-client critical status changes render with p95 under 750ms and p99 under 2s when the live path is healthy.
3. Sessions list/count/sidebar no longer poll every 10s when live updates are healthy.
4. Session detail no longer runs independent 3-5s polls for detail, transcript, human input, timeline, readiness, and file events while streams are healthy.
5. Previews index no longer polls running previews every 5s when healthy.
6. Automations list/detail/runs no longer poll every 10s when healthy.
7. Redis live-bus connection count is bounded by configured shards/topology and does not grow with org/client count.
8. A 10,000-distinct-org test stays within measured Redis, API heap, file descriptor, and latency budgets; replay remains below its 25% fleet budget or sheds retention without disrupting live delivery.
9. A write-heavy 1,000-tab org produces bounded REST amplification and no hidden-tab reads.
10. State plus outbox is atomic; claims expire safely; aggregates are durably materialized with stable IDs; and a process crash after commit cannot permanently lose the event.
11. Redis publication/subscription degradation stops healthy SSE heartbeats and does not fail committed writes; recovery coalesces the backlog instead of replaying every write.
12. Replay is race-free and tested for cursorless initial synchronization, hit, duplicate, trimmed, malformed, ahead, oversized, and resync-checkpoint success/failure paths.
13. A new EventSource carries the last cursor explicitly; reconnect never depends accidentally on browser state from a destroyed object.
14. Named heartbeats detect a stalled connection, bus-shard loss closes affected streams before another healthy heartbeat, and slow network consumers cannot block a handler indefinitely.
15. Hidden tabs issue no event-triggered refetches and perform one prioritized catch-up when visible.
16. Projection updates reject stale versions, and an older in-flight REST response cannot roll back projected state or list membership.
17. Resource append streams do not refetch complete growing logs/transcripts per append.
18. Org, repository, and resource audiences match REST authorization; revocation meets the healthy and Redis-outage latency targets.
19. Rolling deploys drain live connections with randomized reconnect guidance and no synchronized refetch storm.
20. All migrated fallback intervals come from the central policy and continue low-rate reconnect probes indefinitely.
21. End-to-end browser telemetry measures event receipt, projection/render, REST reconciliation, and p99 freshness.

## Open Questions

1. **Live-bus shard count.** Default 32; finalize from measured bus throughput, Redis topology, and per-process subscription cost.
2. **Replay retention.** Initial five minutes, 1,024 entries, one-hour key expiry, and a 25% fleet replay-memory budget; tune downward or upward only from reconnect duration, miss rate, encoded event size, and Redis headroom.
3. **Projection coverage.** Session/preview/automation status is required initially. Which PR-health and code-review fields are safe and valuable enough to patch directly?
4. **Resource Stream consolidation timing.** At what active-session count do the current three backend session readers justify migration to one typed resource Stream?
5. **Automation stats cadence.** Terminal events only, or a trailing aggregate during long-running bursts?
6. **Preview logs.** At what visible polling QPS should preview logs move to append streaming?
7. **Degraded-mode control.** Should the API expose a small live-service status endpoint so clients can distinguish isolated failure from a fleet-wide outage and widen polling centrally?
