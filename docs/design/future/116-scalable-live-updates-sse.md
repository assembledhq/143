# Design: Scalable Live Updates Over SSE

> **Status:** Not Started | **Last reviewed:** 2026-07-10

## Summary

143 should standardize live UI refresh on a single scalable Server-Sent Events architecture: durable database writes remain the source of truth, Redis-backed SSE events are low-latency invalidation hints, and long randomized polling intervals are only correctness backstops.

The first migration targets the highest-polling dashboard surfaces:

- `/sessions`
- session detail
- `/previews`
- preview detail and embedded preview panels
- `/automations`
- automation detail and runs

The same architecture should also become the shared replacement for the existing one-off SSE implementations across code reviews, PR health, eval batches, eval bootstrap, and session streams. Existing streams do not need to be removed in the first deployment, but new live-update work should use the shared event envelope and shared frontend subscription layer.

Two decisions are load-bearing for scale and are fixed up front in this revision:

1. **Fan-out model.** The shared org stream uses the per-process fan-out model already proven in `SessionStreams` (one Redis subscription per org per API process, multiplexed over a single connection, dispatched in-process to many clients). It does **not** copy the per-client subscription model used by `PullRequestStreams`/`CodeReviewStreams`, which opens one Redis subscription per connected browser and does not scale to large orgs. See [Backend Design](#backend-design).
2. **Stream tiering.** The org stream carries only low/medium-frequency, list-level invalidation hints. High-frequency per-resource events (session logs, file events, transcript tailing) stay on resource-scoped streams. See [Subscription Topology](#subscription-topology).

## Problem

Several frontend surfaces keep data fresh with fixed polling loops, many at 3s, 5s, or 10s intervals. This is simple and resilient, but it scales poorly in two ways that show up well before "thousands of users."

**Per-page query fan-out is the dominant cost, not idle tabs.** Background polling already stops when a tab is hidden (TanStack Query's default `refetchIntervalInBackground: false`, which the codebase relies on almost everywhere). The real load comes from *foreground* tabs running many concurrent polling queries against data that is usually unchanged. The worst offender today is **session detail, which runs 11+ simultaneous polling queries** while a session is active:

- session detail (3s), thread transcript (3s), human-input requests (3s), readiness checks (3s), timeline (3s)
- PR health (5s), file events (5s)
- runtime status (15s), plus several non-polling queries that refetch on the same activity

Across the rest of the product the hot patterns are:

- sessions list, counts, and sidebar polling every 10s
- previews index polling one section every 5s and three sections every 30s
- preview detail and preview panels polling every 3-15s
- automations list/detail/runs polling every 10s
- automation goal-improvement status polling several queries every 3s

Aggregate request rate for a foreground tab is roughly:

```text
request_rate = mounted_polling_queries / interval_seconds
```

A single active session-detail tab can sustain well over 1 request/second on its own. Multiply by foreground tabs at thousands of concurrent users and the list/detail polling becomes avoidable API and Postgres load even when nothing is changing.

The product requirement is the opposite of what polling optimizes for: users need low latency when something changes, not constant read traffic when nothing changes — and they need their *own* actions to feel instant.

## Goals

1. Deliver visible UI changes with p95 latency under 1s after the backend commits when SSE is healthy.
2. Make the acting user's own changes feel instant via optimistic updates, independent of SSE round-trip latency.
3. Reduce idle/unchanged foreground polling to near zero, and collapse the session-detail query fan-out.
4. Keep Postgres as the source of truth; SSE must never be the only correctness path.
5. Use long, jittered backup polling so missed events self-heal without synchronized thundering-herd reads.
6. Use one org-scoped stream per active dashboard tab for list and index surfaces.
7. Use resource-scoped streams for any high-volume per-resource event (session logs, file events, transcript tailing).
8. Bound load amplification: a burst of writes in one org must not produce an unbounded refetch storm across that org's connected tabs.
9. Centralize event schemas, backend stream plumbing, frontend connection handling, and query invalidation behavior.
10. Consolidate existing ad hoc SSE streams onto the same envelope and frontend hook over time.
11. Preserve multi-org tab isolation despite EventSource not supporting custom request headers, and revoke access on a stream when membership changes.
12. Make live-update health observable with connection, reconnect, event, fallback-poll, and DB-QPS metrics.

## Non-Goals

1. Replacing Postgres reads with streamed full records.
2. Guaranteeing exactly-once event delivery to browsers.
3. Building a user-visible notification inbox.
4. Introducing WebSockets for these dashboard data refreshes.
5. Removing the existing session log SSE stream before the new org event stream is proven.
6. Requiring Redis for correctness. Redis loss should degrade freshness, not data integrity.

## Current Baseline

The codebase already has several SSE primitives, and they do **not** all use the same fan-out model. This distinction drives the design:

- `SessionStreams` (`internal/cache/session_streams.go`) — backs session logs/status/events on **Redis Streams**. One `XRead`-blocking goroutine per resource **per API process** maintains an in-memory fan-out to many per-client buffered channels (256 slots each), with a 1000-entry ring buffer for short replay and a 4 KiB Redis payload clamp. **This is the scalable pattern.**
- `PullRequestStreams` and `CodeReviewStreams` (`internal/cache/*_streams.go`) — org-scoped, on **Redis pub/sub**, but they open **one Redis `SUBSCRIBE` per HTTP client**. This is fine at today's volumes but does not scale: every connected tab is its own Redis subscription, and in Redis Cluster non-sharded pub/sub fans every message to every node.
- `EvalBatchStreams` and `EvalBootstrapStreams` — resource-scoped pub/sub, same per-client subscription shape.
- `useResourceSSE` and `frontend/src/lib/sse.ts` — frontend EventSource handling (`withCredentials`, exponential backoff to a 15s ceiling, capped at 5 attempts, `healthy` flag that callers use to switch polling). Code reviews and bootstrap evals already implement the "healthy → 30s, unhealthy → 3-5s" polling switch, so the central policy below is partly proven in production.
- The SSE HTTP handlers already solve EventSource's missing-header problem by accepting an optional `?org_id=` query param and validating org membership at the handshake, returning 400/401/403 before the stream opens.

The architectural direction is correct: lightweight events wake clients, clients refetch canonical state, and polling remains a fallback. The gaps are (a) the pattern is not generalized, (b) the only generalized-looking helpers use the non-scalable per-client subscription model, and (c) major dashboard surfaces still poll independently.

## Architecture

### Principle

Every live update follows this sequence:

1. Backend writes canonical state to Postgres.
2. Transaction commits.
3. Backend publishes a small event to Redis (best-effort, possibly coalesced).
4. API SSE handler fans that event out to subscribed clients **from a single per-org subscription per process**.
5. Frontend coalesces events and invalidates affected TanStack Query keys.
6. Frontend refetches existing REST endpoints for canonical data.
7. Long backup polling self-heals if the event was missed.

SSE events are invalidation hints. They may be dropped, duplicated, delayed, or reordered. They must carry enough information to choose which cached queries to refresh, but they must not be trusted as authoritative UI state.

The acting user's own mutations are handled separately and optimistically — see [Optimistic Updates](#optimistic-updates-the-actors-own-changes). SSE is for propagating *other* clients' changes and backend-driven transitions.

### Subscription Topology

Use two layers, with a strict frequency rule that resolves the prior draft's inconsistency:

| Layer | Scope | Carries | Frequency |
| --- | --- | --- | --- |
| Org event stream | One EventSource per active org/tab in v1; one per active org/browser is a v2 optimization | List/index/detail-level invalidation hints: `session.created`, coarse `session.updated`, `preview.updated`, `automation.updated`, `automation.run.updated`, `code_review.updated`, `pull_request.updated`, eval index notifications | Low / medium |
| Resource stream | One EventSource per focused high-volume resource | Session logs, `session.file_events.changed`, transcript/thread tailing, and any future high-volume per-resource feed | High |

Rule: if an event can fire many times per second for a single resource, or only matters while a specific resource view is open, it belongs on a resource stream — never the org stream. The org stream must stay sparse, because every event on it is delivered to **every** connected tab in the org. This is what keeps a single busy session from flooding tabs that are sitting on the automations page.

The org stream is the default for list/detail freshness. Resource streams need (and now have) a frequency justification.

The v1 browser contract intentionally keeps the connection model simple: one org EventSource per active dashboard tab. This is acceptable only because HTTP/2 is required and because backup polling is reduced sharply. A follow-up optimization should evaluate a same-browser leader model (`BroadcastChannel` or a shared worker) so one tab owns the EventSource for an org and broadcasts events to sibling tabs. That optimization is not required for the first rollout, but it should become the next lever if connection count grows faster than user count.

## Backend Design

### Event Envelope

Add a shared event envelope in `internal/models`:

```go
type LiveEventType string

const (
    LiveEventSessionCreated       LiveEventType = "session.created"
    LiveEventSessionUpdated       LiveEventType = "session.updated"
    LiveEventSessionThreadUpdated LiveEventType = "session.thread.updated"
    LiveEventSessionInputUpdated  LiveEventType = "session.human_input.updated"
    LiveEventSessionFilesChanged  LiveEventType = "session.file_events.changed"
    LiveEventPreviewUpdated       LiveEventType = "preview.updated"
    LiveEventPreviewLogsChanged   LiveEventType = "preview.logs.changed"
    LiveEventAutomationUpdated    LiveEventType = "automation.updated"
    LiveEventAutomationRunUpdated LiveEventType = "automation.run.updated"
    LiveEventCodeReviewUpdated    LiveEventType = "code_review.updated"
    LiveEventPullRequestUpdated   LiveEventType = "pull_request.updated"
    LiveEventEvalBatchUpdated     LiveEventType = "eval_batch.updated"
    LiveEventEvalBootstrapUpdated LiveEventType = "eval_bootstrap.updated"
)

type LiveEvent struct {
    ID             string                 `json:"id"` // Redis Stream ID used as the SSE id field.
    Type           LiveEventType          `json:"type"`
    OrgID          uuid.UUID              `json:"org_id"`
    ResourceType   string                 `json:"resource_type"`
    ResourceID     uuid.UUID              `json:"resource_id"`
    ParentType     string                 `json:"parent_type,omitempty"`
    ParentID       *uuid.UUID             `json:"parent_id,omitempty"`
    Version        int64                  `json:"version,omitempty"`
    ChangedAt      time.Time              `json:"changed_at"`
    Reason         string                 `json:"reason,omitempty"`
    Hints          map[string]interface{} `json:"hints,omitempty"`
}
```

`Hints` is intentionally small and optional. It may include fields like `status`, `thread_id`, `preview_id`, `automation_id`, or `repository_id` to help the frontend avoid broad invalidation. It must not include logs, transcripts, diffs, prompt text, secrets, or full list records.

`ID` is the Redis Stream entry ID, and the SSE handler must write it as the SSE `id:` field. Clients send this value back through `Last-Event-ID` on reconnect so the API process can resume with `XREAD` from the Redis replay window. If a future durable event table needs a stable application UUID, add a separate field; do not overload the stream cursor.

`Version` is **optional and deferred for v1.** Because every handler refetches canonical state on invalidation, reordered or duplicated events are self-correcting (the last refetch reads DB truth). `Version` only buys dedup/coalescing optimization and a future durable cursor; do not block the first implementation on it. See [Open Questions](#open-questions).

### Redis Stream Helper

Add `internal/cache/live_event_streams.go`, modeled on **`SessionStreams`, not on the per-client pub/sub helpers**:

- `NewLiveEventStreams(client *cache.Client, logger zerolog.Logger) *LiveEventStreams`
- `Publish(ctx context.Context, event models.LiveEvent) error`
- `Subscribe(ctx context.Context, orgID uuid.UUID) (*LiveEventSubscription, error)`

Transport and fan-out:

- **Redis Streams, not bare pub/sub.** Channel/key per org: `143:stream:{org:<org_id>}:live_events`, written with `XADD` and a bounded `MAXLEN ~` cap (default 512). This gives a short replay window so a client that reconnects within seconds can resume from its last id instead of losing every event during the gap. This mirrors how `SessionStreams` already backs logs. Size memory as `active_org_stream_keys × maxlen × average_entry_bytes`, and alert before Redis memory pressure can evict unrelated cache state.
- **One subscription per org per process.** Each API process maintains at most one `XRead`-blocking reader per org that currently has ≥1 connected client, multiplexed over the shared Redis connection. New browser connections for an org attach to the existing in-process fan-out; they do **not** create new Redis subscriptions. When the last client for an org disconnects, the process tears down that org's reader. This is the single most important scalability property of the design.
- **In-process fan-out with collapsible overflow.** Per-subscriber bounded buffer, default 256. Because these are collapsible invalidation hints (not a must-deliver log), buffer overflow must **not** disconnect the client the way the log stream does. Instead, drop the buffered backlog and deliver a single `live.resync` sentinel telling the client to do one coalesced refetch of its visible queries. Disconnect-on-overflow is wrong here because it triggers reconnect → full refetch → more load.
- **Heartbeats** as SSE comments every 15-30s, matching existing handlers.
- **Publish errors** are best-effort: logged by the caller at warn level, never surfaced to the user-facing write path.

This becomes the preferred primitive for new low/medium-volume product updates. New domain-specific stream helpers are disallowed unless they document why org-scoped live events are insufficient.

### Per-Org Publish Coalescing

Org-wide fan-out means each published event hits every connected tab, and each tab may refetch a list. A write burst in one org (e.g. an automation spawning 50 sessions) must not become `50 events × N tabs` list refetches inside one window. Add publish-side coalescing for **list-level** events:

- Per org, per coalescable event class (e.g. `org_id:session:list`, `org_id:automation:list`, `org_id:preview:index`), debounce publishes to at most one event per ~500ms window, carrying a `Reason: "coalesced"` hint.
- Publish the first event in a quiet window immediately, then collapse trailing events during the debounce window. This preserves the <1s perceived-latency target for normal low-frequency updates while still bounding bursts.
- Coalesced list/index events should omit `resource_id` when multiple resources changed and should invalidate the relevant visible list/count queries only.
- Detail-level events use a key like `org_id:event_type:resource_id`. They are not coalesced across resources, but repeated events for the *same* resource within the window collapse to one trailing event.
- Coalescing must preserve all affected query families. If a burst touches both a session's detail and the sessions list, publish or encode enough events for both invalidations; do not rely on one broad event that wakes unrelated surfaces.
- This bounds the worst case to `O(distinct changed lists) × N tabs` per window instead of `O(writes) × N tabs`.

Server-side coalescing complements, and does not replace, the client-side coalescing in [Event Dispatcher](#event-dispatcher).

### Publish Rules

Publish only after durable state is committed. For explicit transactions, publish after `Commit`. If the code path cannot easily publish after commit, prefer a small post-commit service hook over publishing inside the transaction.

Publishing must be best-effort unless a future feature explicitly requires a durable event log. If Redis is unavailable, the user action still succeeds and backup polling eventually refreshes the UI.

### Event Sources

Initial publish points. Note the stream column — high-frequency session sub-resource events publish to the **resource** stream, not the org stream:

| Domain | Event | Stream | Publish when |
| --- | --- | --- | --- |
| Sessions | `session.created` | Org | manual/API/automation/session creation commits |
| Sessions | `session.updated` | Org | status, display state, archive state, PR/branch/push state, sandbox state, title, linked summary changes (coalesced for list refresh) |
| Threads | `session.thread.updated` | Resource (session) | thread status, active runtime state, label/model changes, archive state |
| Human input | `session.human_input.updated` | Resource (session) | request created, answered, cancelled |
| File events | `session.file_events.changed` | Resource (session) | thread file event batch persisted |
| Previews | `preview.updated` | Org | preview instance/target/current state changes, freshness changes, prewarm state changes |
| Previews | `preview.logs.changed` | Resource (preview) | startup/runtime logs appended when a viewer may be tailing |
| Automations | `automation.updated` | Org | create/update/pause/resume/delete, capability/config changes |
| Automation runs | `automation.run.updated` | Org | run created, status changes, linked session updates (coalesced for stats) |
| Existing SSE domains | current event names | Org | bridge through `LiveEventStreams` while preserving old routes during migration |

A session-detail tab therefore holds two streams: the **org stream** (for list/detail-level `session.updated`, plus everything else the dashboard shows) and a **session resource stream** (logs today; thread/human-input/file-event events as those migrate).

### API Contract

Add one new route:

```text
GET /api/v1/events/stream?org_id=<uuid>
```

Auth and authorization:

- Requires an authenticated browser session.
- `org_id` query param is required because EventSource cannot send the active-org header.
- The server must verify the authenticated user is a member of `org_id` at handshake (existing handlers already do this — return 400/401/403 before opening the stream).
- **Resource visibility gate.** Before enabling the org stream outside dogfood, the product/security decision must be explicit: either all authenticated org members may learn the existence and coarse state of every org-stream resource, or the stream must be filtered/scoped by the same repository/resource authorization used by the REST endpoints. The implementation must not rely on "IDs only" as the security boundary.
- **Re-authorization for long-lived connections.** Because a stream can outlive a membership change, the server must revoke access when membership is lost: either re-check membership on a bounded interval (e.g. every 60-120s) and close the stream on failure, or cap connection lifetime (e.g. 15 min) and force a reconnect that re-checks. A revoked user must stop receiving org events promptly, not at the next deploy.
- See [Authorization & Data Exposure](#authorization--data-exposure) for the RBAC gate that determines whether org-wide fan-out is even safe.

Response:

- `Content-Type: text/event-stream`
- heartbeats as SSE comments every 15-30s
- named event: `live_event`
- SSE `id:` is the Redis Stream ID for the delivered entry
- data: `models.LiveEvent` JSON
- optional `Last-Event-ID` support to resume from the Redis Streams replay window after a brief disconnect

Example:

```text
event: live_event
data: {"id":"01J...","type":"session.updated","org_id":"...","resource_type":"session","resource_id":"...","version":42,"changed_at":"2026-06-28T12:00:00Z","hints":{"status":"running"}}
```

Errors:

| Status | Code | Meaning |
| --- | --- | --- |
| 400 | `INVALID_ORG_ID` | malformed `org_id` |
| 401 | `UNAUTHORIZED` | no valid session |
| 403 | `FORBIDDEN` | user is not a member of org |
| 503 | `LIVE_EVENTS_UNAVAILABLE` | Redis/event backend unavailable |

Existing SSE routes remain during migration:

- `GET /api/v1/sessions/{id}/logs/stream`
- `GET /api/v1/pull-requests/stream`
- `GET /api/v1/code-reviews/stream`
- `GET /api/v1/evals/batch/{batchId}/stream`
- `GET /api/v1/evals/bootstrap/{runId}/stream`

After consumers are migrated, these routes may either become compatibility wrappers around the shared stream or be removed in a later breaking cleanup if no clients use them.

### Connection & Transport Requirements

These are prerequisites, not nice-to-haves, because each dashboard tab now holds **two** long-lived SSE connections (org stream + a resource stream) plus normal REST traffic:

- **HTTP/2 (or H3) is required on the API origin.** Under HTTP/1.1 browsers cap ~6 connections per origin; two SSE streams per tab, multiplied by a power user's open tabs, will starve REST calls. Confirm the edge/LB and any reverse proxy negotiate HTTP/2 end to end for the API origin, do not buffer `text/event-stream`, and use an idle timeout longer than the heartbeat interval.
- **Per-node capacity target.** Establish and load-test a concrete target before rollout. The initial target should be at least 10,000 concurrent org-stream SSE connections per API node and 20,000 total SSE connections per node including resource streams, bounded by file descriptors, memory, goroutine count, and write latency. Adjust the number only with measured production instance limits.
- **Admission and shedding.** If a node is over its active SSE connection budget or cannot allocate a new fan-out subscription, `/api/v1/events/stream` should return `503 LIVE_EVENTS_UNAVAILABLE` quickly with normal cache-control/no-buffer headers. Clients then use jittered fallback polling; they must not spin reconnects or immediately blanket-refetch.
- **Connection math uses tabs, not users.** One user with several open tabs is several connections. Size for connections, not distinct users.

### Authorization & Data Exposure

Org-wide fan-out means **every subscribed member of an org receives every org-stream event sent to that subscriber**, including coarse hints (`status`, `repository_id`, the existence of a `resource_id`). This is safe without filtering **only if 143's access model is flat at the org level for those resources.**

- **Required decision:** document whether coarse org-stream resource existence is visible to every org member. If yes, org-wide fan-out is fine and hints stay coarse. If no, the handler must apply per-subscriber filtering or the stream must be scoped below org.
- **If sub-org RBAC exists or is planned:** the org stream handler must filter events per subscriber against the same authorization the REST endpoints use, or scope streams below the org. Do not ship org-wide fan-out that leaks resource existence/metadata across an authorization boundary.
- **Repository/resource-scoped future proofing:** builder/viewer role restrictions, repository visibility, projects, or private sessions all count as possible narrower boundaries. Adding any such boundary later must include either stream filtering or a topology change before the boundary ships.
- Regardless: hints carry IDs and coarse status only — never names, titles, diffs, prompts, branch names, repository names, user-entered text, or secrets — so that even within an org the stream reveals no content beyond what a list endpoint would show.

This gate is a security-review item and must be resolved before the org stream is enabled beyond dogfood.

### Database Schema

No database schema is required for the first implementation. Events are transient invalidation hints backed by Redis Streams (short replay window) and never the source of truth.

If later operational data shows missed events are a frequent user-visible problem even with the Redis Streams replay window, add an optional durable event cursor table:

```sql
CREATE TABLE live_events (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id),
    event_type text NOT NULL,
    resource_type text NOT NULL,
    resource_id uuid NOT NULL,
    parent_type text,
    parent_id uuid,
    version bigint,
    changed_at timestamptz NOT NULL DEFAULT now(),
    hints jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX live_events_org_changed_at_idx
    ON live_events (org_id, changed_at DESC);
```

Do not add this table unless replay beyond the Redis window becomes a real requirement. The initial design intentionally avoids a new hot append-only table.

## Frontend Design

### Optimistic Updates (The Actor's Own Changes)

The largest perceived-latency win is not seeing other people's changes — it is the user's own action feeling instant. SSE must **not** be on the critical path for the acting user.

- For a user's own mutation, optimistically update the relevant query cache from the mutation's own response (or `onMutate`), immediately. Do not wait for an SSE round-trip or a fallback poll to reflect the user's click.
- SSE and backup polling then reconcile the optimistic state against canonical truth and propagate the change to the user's *other* tabs and to other users.
- This replaces the old "explicit user action in flight → 2-5s polling" pattern as the primary mechanism. Short scoped polling remains only as a convergence backstop for multi-step server-side state machines (e.g. PR/branch/push), not as the way the actor sees their own action land.

### Shared Connection

Add `useLiveEventStream()` mounted once in the authenticated dashboard layout:

- builds `/api/v1/events/stream?org_id=...`
- uses EventSource with credentials
- tracks health: `healthy`, `reconnecting`, `unavailable`
- uses capped exponential reconnect with jitter
- sends `Last-Event-ID` on reconnect to resume from the Redis replay window when possible
- emits events into a lightweight client-side dispatcher
- closes and recreates on active org change

The hook replaces one-off page-level EventSource setup for list/detail invalidations. A page must not open its own org-scoped SSE connection. Resource streams (session logs, etc.) remain page/component-scoped.

Same-browser multi-tab sharing is intentionally deferred from v1, but the hook should be written so the transport can later be swapped behind the same dispatcher. A future leader-tab implementation should keep exactly one EventSource per `org_id` per browser profile and broadcast received events and health state through `BroadcastChannel`; follower tabs should not independently reconnect unless leadership is lost.

### Event Dispatcher

Add a registry that maps event types to invalidation handlers:

```ts
type LiveEventHandler = {
  eventTypes: LiveEventType[];
  handle: (event: LiveEvent, queryClient: QueryClient) => void;
};
```

Handlers must:

- invalidate narrow query keys when the resource is known
- coalesce repeated invalidations for the same key within a window (~250-500ms, leading + trailing, so single low-frequency changes are not delayed against the <1s goal)
- only refetch mounted queries; never refetch unmounted ones
- prioritize visible, user-critical queries over mounted but collapsed/secondary panels
- avoid fan-out from noisy resources to unrelated pages
- never directly mutate complex canonical records from the event payload
- on a `live.resync` sentinel or on reconnect, perform **one** coalesced, jittered refetch of currently-visible queries — never a blanket invalidate-everything, which would synchronize thousands of clients into a refetch storm

Use three frontend priority classes:

| Priority | Examples | Behavior |
| --- | --- | --- |
| Critical visible | active session detail status, active preview detail state, modal/action result state | invalidate immediately, with only same-key micro-coalescing |
| Visible secondary | visible lists, sidebars, stats cards, visible but non-focused tabs | debounce within the standard 250-500ms window |
| Mounted inactive | collapsed panels, inactive tabs, offscreen sections | mark stale only; refetch when visible or on sparse backup polling |

If stream health is unhealthy on an active detail view, the UI may show a small non-blocking reconnecting state for live status surfaces. Do not show global warning banners for routine short reconnects.

### Polling Policy

Create a central polling policy module, for example `frontend/src/lib/live-refresh-policy.ts`. Code reviews and bootstrap evals already implement this shape, so this generalizes a proven pattern:

| Situation | Interval |
| --- | --- |
| SSE healthy, list/index backup | 2-5 minutes with jitter |
| SSE unhealthy, list/index fallback | 30-90 seconds with jitter |
| SSE healthy, active detail backup | 30-60 seconds with jitter |
| SSE unhealthy, active detail fallback | 10-30 seconds with jitter |
| explicit server-side state machine converging | 2-5 seconds, scoped to that resource only |
| hidden document | disabled unless a server-side state machine is mid-transition |

A user's own action is handled by [optimistic updates](#optimistic-updates-the-actors-own-changes), not by switching to aggressive polling. All remaining polling should use this policy instead of raw `10000`, `30000`, or local constants.

### Query Invalidation Matrix

| Event | Frontend effect |
| --- | --- |
| `session.created` | invalidate sessions list, sidebar list, session counts (coalesced) |
| `session.updated` | invalidate matching session detail; invalidate sessions list/counts if visible (coalesced) |
| `session.thread.updated` | (resource stream) invalidate matching session detail; invalidate active transcript only if the open thread matches |
| `session.human_input.updated` | (resource stream) invalidate human-input query and session detail for matching session |
| `session.file_events.changed` | (resource stream) invalidate file-events query only for matching session |
| `preview.updated` | invalidate preview detail/panel for matching preview/session; invalidate preview index sections with debounce |
| `preview.logs.changed` | (resource stream) invalidate/tail logs only when logs panel is visible |
| `automation.updated` | invalidate automations list and matching automation detail |
| `automation.run.updated` | invalidate latest run/runs tab first page; debounce stats refresh |
| `code_review.updated` | replace current code-review stream invalidation |
| `pull_request.updated` | replace current PR-health stream invalidation |
| `eval_batch.updated` | replace eval batch stream invalidation when migrated |
| `eval_bootstrap.updated` | replace eval bootstrap stream invalidation when migrated |
| `live.resync` | one coalesced jittered refetch of visible queries |

## Surface Migration Plan

### Sessions List And Sidebar

Replace the current 10s list/count polling with org events:

- `session.created`
- `session.updated`

Backup intervals:

- healthy SSE: 3 minutes
- unhealthy SSE: jittered 45-90 seconds
- hidden document: no backup polling

Coalesce list and count invalidations separately. A burst of ten session updates should produce at most one list refetch and one count refetch per coalescing window — enforced on both the publish side (per-org coalescing) and the client side.

### Session Detail

This page is the biggest win — it collapses 11+ concurrent polls into events plus a sparse backstop. Session detail should use:

- the existing session **log** stream for high-volume logs while active
- the **session resource stream** for thread/human-input/file-event invalidation
- the **org stream** for list/detail-level `session.updated`
- optimistic updates for the user's own PR/branch/push actions
- short scoped polling only as a convergence backstop for those server-side state machines

Backup intervals:

- active session with healthy SSE: 60 seconds
- active session with unhealthy SSE: jittered 10-30 seconds
- terminal session: 5 minutes or disabled after initial settle
- server-side PR/branch/push state machine converging: 2 seconds until terminal state

The detail page must not poll transcript, human input, file events, and detail independently every 3-5s when SSE is healthy.

### Previews

Publish `preview.updated` (org stream) for all preview state transitions. The preview index, preview detail page, PR preview page, and embedded preview panel listen through the shared org event stream.

Backup intervals:

- preview index healthy: 2-3 minutes
- preview index unhealthy: jittered 45-90 seconds
- starting preview detail/panel healthy: 60 seconds
- starting preview detail/panel unhealthy: jittered 10-20 seconds
- stopped/failed/expired preview: disabled or 5 minutes

Preview logs tailing may keep a short interval while visible, but should move to `preview.logs.changed` on the preview resource stream if startup log traffic becomes material.

### Automations

Publish `automation.updated` and `automation.run.updated` (org stream, run events coalesced for stats).

Backup intervals:

- automations list healthy: 3-5 minutes
- automations list unhealthy: jittered 60-120 seconds
- automation detail healthy: 2-3 minutes
- active runs visible and unhealthy: jittered 15-30 seconds

Automation stats should not refetch on every run event. It should refetch on terminal run events or on a 10-30s debounce if runs are actively changing.

Automation goal improvement should publish an event when the proposal status changes. The modal can keep short scoped polling during the transition, but the target architecture is event-driven status and transcript invalidation, with optimistic UI for the user's accept/reject action.

## Existing SSE Consolidation

The repo should converge on these shared pieces:

- one backend `LiveEventStreams` helper (per-org fan-out model) for low/medium-volume org events
- one frontend `useLiveEventStream` hook
- one event type registry in `frontend/src/lib/sse.ts` or a successor module
- one route builder for the org stream
- one reconnect/health model
- one polling fallback policy

Migration sequence:

1. Keep `SessionStreams` for logs and high-frequency session resource events.
2. Bridge `PullRequestStreams` events into `LiveEventStreams`; migrate frontend PR health invalidation to org stream.
3. Bridge `CodeReviewStreams` events into `LiveEventStreams`; remove page-level code review EventSource.
4. Bridge eval batch/bootstrap events into `LiveEventStreams` for ordinary page invalidation. Keep resource streams only if batch event volume makes org fan-out expensive.
5. Delete duplicated frontend route builders and page-specific SSE hooks once no longer used.
6. Prevent new domain-specific SSE helpers unless they document why org-scoped live events are insufficient.

As these migrate, retire the per-client subscription model in favor of the per-process fan-out model so we are not running two fan-out architectures long-term.

## Performance And Scalability Requirements

### Browser And API

- One org EventSource per dashboard tab; resource streams only for high-volume focused resources.
- Same-browser org-stream sharing through `BroadcastChannel` or shared worker is the preferred v2 connection-reduction path if tab multiplication becomes material.
- No list/index page opens a second org-scoped EventSource.
- HTTP/2 (or H3) required on the API origin; verify no proxy buffers `text/event-stream`.
- EventSource reconnects use jitter and capped exponential backoff; reconnect resumes via `Last-Event-ID` where possible and never blanket-invalidates.
- Over-capacity stream attempts fail fast with `503 LIVE_EVENTS_UNAVAILABLE`; clients rely on jittered fallback polling and capped reconnects.
- Heartbeats keep intermediaries from closing quiet streams.
- Event payloads stay under 4 KiB.
- Client invalidation is coalesced by query key.

### Redis

- **One subscription per org per API process**, multiplexed over the shared connection — not one per client. This is the core scaling property.
- Use Redis Streams (`XADD` with bounded `MAXLEN`) for a short replay window, not bare pub/sub.
- SSE `id:` and `Last-Event-ID` use Redis Stream IDs, not application UUIDs.
- Default `MAXLEN ~ 512` is the starting point; validate memory with `active_org_stream_keys × maxlen × average_entry_bytes` and revisit if reconnect miss-rate is user-visible.
- In Redis Cluster, account for non-sharded pub/sub broadcast behavior; the per-process subscription model keeps subscription count proportional to `orgs × nodes`, not `clients`. Evaluate sharded streams / keyspace partitioning if cluster fan-out becomes hot.
- Publish once per committed state transition, with per-org coalescing for list-level events.
- High-volume logs remain resource-scoped.
- Publish failure must not fail the user-facing write.

### Postgres

- Idle/unchanged foreground dashboards should not produce repeated list reads every 10s.
- The session-detail query fan-out (11+ polls) collapses to events plus a sparse backstop.
- **Burst amplification is bounded:** a write burst in one org must not produce `O(writes) × tabs` refetches. Server-side per-org coalescing plus client-side coalescing cap this at `O(distinct changed lists) × tabs` per window.
- Backup polling must be sparse and jittered.
- Existing REST endpoints remain optimized with org filters and cursor limits.
- Event handlers must not create new N+1 reads.

### Latency Targets

| Path | Target |
| --- | --- |
| commit to Redis publish | p95 < 50ms |
| publish to browser event handler | p95 < 500ms |
| event to query invalidation, critical visible query | p95 < 100ms |
| event to query invalidation, visible secondary query | p95 < 500ms including coalescing |
| commit to visible UI update (other clients) | p95 < 1s when SSE healthy |
| user's own action to visible UI update | immediate (optimistic), independent of SSE |
| missed event self-heal | within Redis replay window on reconnect, else within backup interval |

The <1s visible-update target depends on leading-edge event delivery. Server-side and client-side coalescing may delay trailing burst updates, but the first visible state transition after a quiet period must not wait for a full debounce window on both sides.

## Observability

Add metrics with labels for event type and stream scope:

- active SSE connections (gauge — does not exist today; required)
- active per-org in-process fan-out subscriptions (gauge)
- rejected SSE connections by reason (`over_capacity`, `redis_unavailable`, `auth_failed`)
- SSE connection duration
- reconnect count
- publish attempts
- publish failures
- events published vs events coalesced (to validate per-org coalescing)
- events sent to clients
- client buffer overflows collapsed to `live.resync`
- fallback poll count
- fallback poll reason: healthy backstop, stream unhealthy, state machine converging
- stream unavailable responses
- reconnect replay outcome: replayed from Redis window vs replay window missed

Add logs:

- warn on publish failure with `org_id`, event type, resource type, resource ID
- info/debug on stream subscribe/unsubscribe only if sampled
- warn on client buffer overflow / resync

Dashboards should compare before/after:

- API QPS for sessions/previews/automations endpoints
- Postgres CPU and read IOPS
- p95 freshness from backend commit to frontend refetch completion, where measurable
- Redis stream publish rate and per-process subscription count
- active SSE connection count, including a write-heavy multi-tab org

## Testing Strategy

### Backend

- Unit test event envelope validation.
- Unit test `LiveEventStreams.Publish` and `Subscribe`, specifically the **per-process single-subscription fan-out** (multiple subscribers for one org share one Redis reader) and the **overflow-collapses-to-resync** behavior.
- Unit test per-org publish coalescing windows.
- Handler tests for `/api/v1/events/stream`: auth, org membership, malformed `org_id`, Redis unavailable, over-capacity `503`, heartbeat, event serialization with SSE `id:`, `Last-Event-ID` resume, replay-window miss behavior, and **mid-stream re-authorization** (revoked membership closes the stream).
- Store/service tests for publish-after-commit behavior on sessions, previews, and automations.
- Regression tests that publish failure does not fail the committed write.

### Frontend

- Unit test event-to-query invalidation mappings.
- Unit test coalescing so event bursts produce bounded invalidations.
- Unit test priority classes so visible critical queries refetch immediately, visible secondary queries debounce, and mounted inactive queries are marked stale without eager refetch.
- Unit test optimistic-update paths so the actor's own change renders without SSE.
- Unit test that reconnect / `live.resync` produces one coalesced refetch, not blanket invalidation.
- Component tests for sessions/previews/automations pages with polling disabled and SSE events driving updates.
- Tests for fallback intervals when stream health changes.
- Tests that hidden documents do not keep list backup polls running.

### Load And Failure Testing

- Simulate at least 10,000 concurrent org-stream EventSource connections per API node and 20,000 total SSE connections per node including resource streams, or document a lower measured target for the chosen production instance class.
- Verify Redis subscription count scales with `orgs × nodes`, not with client count.
- Simulate one org with 1,000 foreground tabs connected to the same API fleet and a burst of at least 100 writes/second for 60 seconds.
- Simulate bursts of session/thread/preview/automation events **in a write-heavy, many-tab org**, and assert no Postgres QPS regression at the burst moment (per-org coalescing holds).
- Kill Redis and verify clients fall back to jittered intervals and writes still succeed.
- Restore Redis and verify clients reconnect, resume via replay window, and reduce fallback polling.
- Force replay-window misses and verify clients do one jittered visible-query resync rather than broad invalidation.
- Verify no synchronized thundering herd after deploy, Redis restart, or network blip.

## Rollout Plan

Staged to de-risk the two items that could make this net-negative (fan-out model and burst amplification) **before** touching six surfaces.

**Phase A — Infra + one surface, proven end to end:**

1. Add backend `LiveEventStreams` (per-process fan-out, Redis Streams, per-org coalescing), the `/api/v1/events/stream` route, the `useLiveEventStream` hook, and telemetry — all behind a feature flag.
2. Confirm the [HTTP/2](#connection--transport-requirements), capacity/admission, and [RBAC](#authorization--data-exposure) prerequisites. The RBAC decision must be written down before dogfood expands beyond internal trusted orgs.
3. Wire event publishing for **sessions** only. Dogfood with the org stream logging events and invalidations while polling still runs (accept the temporary double-load in dev only).
4. Migrate sessions list/sidebar and session detail to event-driven invalidation + optimistic updates + sparse backup polling.
5. **Gate:** measure the real Postgres/QPS delta and p95 freshness, and run the write-heavy multi-tab load test. Only proceed if burst amplification is bounded and QPS drops materially with no regression at burst.

**Phase B — Remaining product surfaces:**

6. Migrate automations list/detail/runs.
7. Migrate previews index/detail/panel.

**Phase C — Consolidation and cleanup:**

8. Bridge and then refactor code reviews, PR health, and eval streams onto the shared architecture, retiring the per-client subscription model.
9. Remove raw fixed intervals from migrated pages and enforce the central live-refresh policy.
10. Remove obsolete domain-specific stream helpers after all consumers have moved.
11. Re-evaluate same-browser multi-tab sharing once production telemetry shows connection count per active user and per org; implement it if tab multiplication materially affects capacity or cost.

## Acceptance Criteria

1. Sessions list/count/sidebar no longer poll every 10s when SSE is healthy.
2. Session detail no longer runs 11+ independent 3-5s polls while SSE is healthy.
3. The acting user's own session/preview/automation changes render optimistically, without waiting on SSE.
4. Previews index no longer polls running previews every 5s when SSE is healthy.
5. Automations list/detail/runs no longer poll every 10s when SSE is healthy.
6. The org stream uses one Redis subscription per org per API process; Redis subscription count scales with `orgs × nodes`, not with connected clients.
7. A write-heavy, many-tab org load test shows bounded refetch amplification and no Postgres QPS regression at the burst moment.
8. Healthy SSE p95 visible update latency is under 1s for session, preview, and automation status changes (other clients).
9. Redis outage does not break correctness and does not cause clients to synchronize on aggressive fallback polling; recovery resumes via the replay window.
10. A user removed from an org stops receiving that org's events promptly (mid-stream re-authorization), not at the next deploy.
11. Existing code-review, PR-health, and eval live updates use the shared frontend hook or are explicitly documented as temporary compatibility paths.
12. All new polling intervals in migrated surfaces come from the central live-refresh policy.
13. The org-stream authorization contract is resolved: either coarse resource existence is explicitly org-visible, or stream filtering/scoping matches REST resource authorization.
14. Over-capacity stream attempts fail fast and fall back to jittered polling without reconnect storms or blanket refetches.
15. `Last-Event-ID` resume is tested with Redis Stream IDs, including the replay-window-miss path.

## Open Questions

1. **Redis Streams replay window size.** This revision adopts Redis Streams (resolving the prior pub/sub-vs-streams question in favor of streams for the org channel). Remaining question: what `MAXLEN` / retention best balances reconnect recovery against memory for large orgs?
2. **Durable cursor.** `Version` and the optional `live_events` table are deferred. What production miss-rate (even with the replay window) would justify adding a durable per-resource version or cursor table?
3. **Automation stats cadence.** Refresh from terminal run events only, or from a debounced stream of all run changes?
4. **Preview startup logs.** Stay query-invalidated through `preview.logs.changed`, or get a dedicated resource-scoped log stream like session logs, if startup log traffic grows?
5. **Adaptive fallback SLO.** What production freshness SLO should trigger temporarily tightening fallback intervals?
6. **Sharded fan-out in cluster.** If org fan-out becomes hot in Redis Cluster, do we move to sharded streams / keyspace partitioning, and on what trigger?
