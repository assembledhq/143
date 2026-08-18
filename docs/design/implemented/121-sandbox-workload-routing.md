# Design: Workload-aware sandbox routing

> **Status:** Implemented | **Last reviewed:** 2026-08-10

## Problem

`run_agent` checked org concurrency and local sandbox capacity, while
`continue_session` could reach sandbox creation without the same shared
admission boundary. Initial jobs were claimed before worker capacity was
considered, so a code-review burst could repeatedly land on a full worker even
when another worker had space. The fixed 10-second retry therefore represented
both real fleet saturation and avoidable placement misses.

## Durable contract

`run_code_review` remains the parent controller. Reviewer and orchestrator work
continues to run as normal session threads through `continue_session`; the code
review service does not bypass thread lifecycle, transcript, or recovery
contracts.

Sandbox-producing jobs carry one of two typed workload classes:

- `interactive` for normal sessions.
- `code_review` for reviewer and orchestrator turns belonging to a code-review
  session.

The `jobs.workload_class` column defaults to `interactive` for rolling-deploy
compatibility. `jobs.sandbox_slot_reserved_until` records a short-lived worker
slot reservation made before claim.

## Database schema

Migration `000287_sandbox_workload_routing` extends the tenant-scoped `jobs`
table:

| Column | Type | Contract |
|---|---|---|
| `workload_class` | `text NOT NULL DEFAULT 'interactive'` | Check-constrained to `interactive` or `code_review`. |
| `sandbox_slot_reserved_until` | `timestamptz NULL` | Expiry for a pre-claim worker slot reservation; null also distinguishes normal session-affinity pins. |

`idx_jobs_sandbox_routing` is a partial dequeue index over pending
`run_agent`/`continue_session` work, ordered to match priority,
interactive-first workload class, and FIFO dispatch.
`idx_jobs_active_sandbox_turns` supports shared org admission counts for pending
and running sandbox turns.
Because `jobs` is hot and migrations are transactional, production pre-builds
both indexes concurrently; the migration uses `IF NOT EXISTS` and a short lock
timeout as the rollout-safe fallback. No new table is introduced;
`jobs.org_id` remains the tenant boundary.

## API contract

No new route or setting is introduced. The existing authenticated settings
APIs expose `max_concurrent_runs`, and the Runtime settings page describes it as
the shared limit for interactive and code-review turns. Existing settings
errors remain unchanged: `400 INVALID_JSON` or `400 INVALID_SETTINGS`.

Worker capacity reservation is an internal queue/heartbeat contract, not a
public API. Heartbeat metadata adds
`interactive_reserved_sandbox_slots` and `sandbox_turn_reserved_count`; no SSE
or external event payload changes.

## Admission and routing

Both `RunAgent` and `ContinueSession` enter the same sandbox-turn admission
layer. Every workload draws from `max_concurrent_runs`; workload class only
affects placement and worker-level interactive capacity reservation. A
continuation reusing its existing container still passes org admission but does
not consume a second sandbox slot.

Before normal queue claim, a dispatcher transaction:

1. locks one due, unbound `run_agent` or `continue_session` job;
2. locks the organization admission row and checks all active interactive and
   code-review turns against `max_concurrent_runs`;
3. chooses a fresh active worker from heartbeat capacity metadata;
4. obtains a transaction-scoped advisory lock for that worker and rechecks its
   live, local-reserved, and durable-reserved counts without counting the same
   running sandbox-turn reservation twice;
5. persists the target worker and expiring reservation atomically.

Unbound sandbox jobs cannot be claimed before this routing step. Dispatchers
prefer interactive work at equal queue priority and continue routing after a
capacity deferral, so an older org-limited review cannot monopolize each poll.

Existing sandbox affinity remains authoritative and is only released through
the existing dead/draining-worker recovery path. Claiming any affinity-bound
sandbox job is nevertheless serialized by the same organization-row lock and
shared active-turn check. The claim transaction preserves the worker target
while atomically changing the admitted job to `running`; an org-limited
affinity job keeps its affinity pin and is briefly deferred so other runnable
work can be considered. Each candidate is handled in its own transaction, so
committing an org-limit deferral releases that organization's row lock before
the dispatcher examines another tenant. This prevents cross-org lock-order
deadlocks and keeps runtime-settings writes independent of a long claim scan.

If a worker's authoritative local gate still rejects a fresh sandbox, the
running job immediately performs the same atomic reservation against an
alternate worker. A successful alternate reservation retries with zero delay.
The existing 10-second delay is retained only when no fleet slot is available.
The shared org limit uses a shorter policy retry, while advisory-lock contention
leaves the job immediately runnable so another dispatcher can retry without
pretending the fleet is full.

If fresh workers exist but none advertises usable `max_active_sandboxes`
metadata, the router does not treat the fleet as saturated for eight minutes.
It immediately uses the fresh-worker compatibility route and emits the
`capacity_metadata_unavailable` routing reason as an operational warning. This
keeps lightweight/self-hosted workers usable while making incomplete heartbeat
configuration visible.

Retry-time retargeting is fenced by the running attempt's `lock_token` in both
the locked read and every placement write. If shared admission rejects a
pre-routed turn, it clears that attempt's durable reservation and scheduling
target before returning the retryable error. Normal affinity pins have no
durable reservation and are not cleared by this rejection cleanup.

Capacity deferral is bounded to eight minutes. Once an unbound turn reaches
that age, routing assigns it to a fresh active worker as a terminal probe
without claiming a capacity reservation. The authoritative local/org gate then
rejects it through the normal claimed-job retry path, which invokes the
existing user-visible failure updates, dead-letter hooks, and operational
signals instead of leaving the job silently pending forever.

Accepted `continue_session` work waiting on an org turn limit is a durable user
input, not a generic transient dependency. It retries on the short admission
cadence without consuming attempts or the generic eight-minute retry window;
session/thread terminal-state checks remain the independent termination path.
After any successful requeue, the worker publishes the queue wake-up only once
the pending transition (including a new target) has committed.

## Capacity isolation

`WORKER_INTERACTIVE_RESERVED_SANDBOXES` defaults to `1`. Code-review routing
and local admission subtract this value from the worker's usable capacity;
interactive work may use the full `WORKER_MAX_ACTIVE_SANDBOXES` value. The
reserve is clamped to at most `max_active_sandboxes - 1`, so a single-slot
self-hosted worker remains usable for code review.

`max_concurrent_runs` is the single organization limit for interactive and
code-review turns. The router and affinity-aware claim path serialize this
decision per organization and count both workload classes in the same active
pool. The shared local admission layer remains the authoritative final fence
after claim.

Worker heartbeats publish
`interactive_reserved_sandbox_slots` and `sandbox_turn_reserved_count`
alongside live, total local-reserved, and max sandbox counts. The router treats
pending durable reservations as additional load, but overlaps running durable
reservations with the heartbeat's sandbox-turn reservation subtype using the
larger count. Non-turn local reservations, such as preview startup, remain
additive. This prevents one admitted turn from appearing as both a local and a
durable reservation while preserving independent capacity consumers.

Durable reservations expire after 30 seconds, bridging the heartbeat interval
without permanently pinning work if a dispatcher dies. A fenced executor
clears its durable reservation as soon as sandbox creation or hydration
finishes, avoiding double-counting the reservation and live container for the
remainder of the TTL.

Pressure cleanup runs only when the worker is physically full. A code-review
request rejected solely because it reached the interactive reserve boundary
does not reap healthy sandboxes while physical capacity remains.
