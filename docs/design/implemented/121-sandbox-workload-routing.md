# Design: Workload-aware sandbox routing

> **Status:** Implemented | **Last reviewed:** 2026-08-12

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

Migration `000286_sandbox_workload_routing` extends the tenant-scoped `jobs`
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
timeout as the rollout-safe fallback.

Migration `000288_shared_sandbox_capacity_reservations` adds the ephemeral,
worker-scoped `sandbox_capacity_reservations` table for final-admission leases
shared by every process using the same Docker host. These leases are not
tenant-owned data and therefore intentionally have no `org_id`. As ephemeral
runtime state, they avoid foreign keys to the hot `nodes` and `jobs` tables so
the rollout does not lock queue writes; `expires_at` bounds their admission
effect and worker-local cleanup removes expired rows. A partial unique index
ensures at most one final-admission lease exists for a job.

## API contract

No new route or setting is introduced. The existing authenticated settings
APIs expose `max_concurrent_runs`, and the Runtime settings page describes it as
the shared limit for interactive and code-review turns. The runtime-status
route accepts an optional tenant-scoped `session_id` query parameter and then
returns `capacity.session_waiting_for_capacity`; this distinguishes a pending
session blocked by other admitted turns from one whose own reservation is
already included in the aggregate count. A malformed identifier returns
`400 INVALID_SESSION_ID`. Existing settings errors remain unchanged:
`400 INVALID_JSON` or `400 INVALID_SETTINGS`.

Worker capacity reservation is an internal queue/heartbeat contract, not a
public API. Heartbeat metadata adds
`sandbox_capacity_node_id`, `interactive_reserved_sandbox_slots`, and
`sandbox_turn_reserved_count`; no SSE or external event payload changes.
Runtime status counts both running turns and pending turns holding a live
durable reservation, while final post-claim admission retains its running-only
count.

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
   live, local-reserved, durable-reserved, and shared final-admission counts
   without counting the same job reservation twice;
5. persists the target worker and expiring reservation atomically.

Unbound sandbox jobs cannot be claimed before this routing step. Dispatchers
prefer interactive work at equal queue priority and continue routing after a
capacity deferral, so an older org-limited review cannot monopolize each poll.
If a selected job has malformed settings or another deterministic routing
error, a savepoint rolls back only that routing attempt, records the error, and
durably moves that job behind other due work instead of stopping fleet dispatch.

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
The claim path also wraps sandbox-specific workload resolution and admission in
a per-candidate savepoint. A malformed tenant setting therefore records and
defers only that job; unrelated job types remain claimable on the worker.

If a worker's authoritative local gate still rejects a fresh sandbox, the
running job immediately performs the same atomic reservation against an
alternate worker. A successful alternate reservation becomes runnable
immediately and publishes the normal cross-worker wake-up. The existing
10-second delay is retained only when no fleet slot is available.
The shared org limit uses a shorter policy retry, while advisory-lock contention
uses a 500-millisecond floor so another dispatcher can retry promptly without
pretending the fleet is full or creating a tight loop.

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
Compatibility placements for an initial `run_agent` preserve the job creation
time as their terminal-probe deadline even when claim-time org admission
repeatedly defers them, so an older worker heartbeat format cannot leave the
job pending forever.
After any successful requeue, the worker publishes the queue wake-up only once
the pending transition (including a new target) has committed. Immediately
runnable work publishes at commit; retry delays up to ten seconds arm a
best-effort, worker-lifecycle-bound wake-up for `run_at`. Longer generic
backoffs and process exits rely on the normal database poll, avoiding detached
timers that outlive the worker.

## Capacity isolation

`WORKER_INTERACTIVE_RESERVED_SANDBOXES` defaults to `1`. Code-review routing
and local admission subtract this value from the worker's usable capacity;
interactive work may use the full `WORKER_MAX_ACTIVE_SANDBOXES` value. The
reserve is clamped to at most `max_active_sandboxes - 1`, so a single-slot
self-hosted worker remains usable for code review.

`max_concurrent_runs` is the single organization limit for interactive and
code-review turns. The router and affinity-aware claim path serialize this
decision per organization and count admitted sandbox-producing jobs from both
workload classes in the same active pool. This is deliberately a turn/job
limit, not a distinct-session limit: concurrent threads that can each exercise
the shared sandbox draw independently from the fence. The shared local
admission layer remains the authoritative final fence after claim.

Worker heartbeats publish
`sandbox_capacity_node_id`, `interactive_reserved_sandbox_slots`, and
`sandbox_turn_reserved_count`
alongside live, total local-reserved, and max sandbox counts. The router treats
pending durable reservations as additional load, but overlaps running durable
reservations with the heartbeat's sandbox-turn reservation subtype using the
larger count. Non-turn local reservations, such as preview startup, remain
additive. It also counts unexpired shared final-admission leases, deduplicated
against durable reservations for the same job and overlapped with the matching
heartbeat reservation subtype. This prevents one admitted turn or preview from
appearing twice while preserving independent capacity consumers running in
other processes.

Durable routing reservations expire after 30 seconds, bridging the heartbeat
interval without permanently pinning work if a dispatcher dies. At final
admission, the main worker, isolated session executors, and preview paths use
the same per-node advisory lock to atomically compare the current Docker count,
durable job reservations, and shared final-admission leases before inserting a
15-minute shared lease. Cross-process coordination has a 10-second bound while
the Docker inspection inside the lock keeps its shorter two-second bound. The
queue claim token fences each job-backed lease: admission transactionally
validates the current owner, a replacement attempt waits for a live prior lease
instead of sharing it, and release can delete only the acquiring attempt's row.
The lease is released under the same per-node admission lock as soon as sandbox
creation or hydration finishes. Transient release failures retry within the
shared coordination budget; the TTL bounds leaks if a process dies or the
database remains unavailable. Admission coordination failures emit the
structured `sandbox_capacity_coordination_failure` signal and timeout value for
alerting. After successful creation or hydration, a fenced executor also clears
its durable routing reservation. A worker-local startup failure instead records
the failed physical capacity-node identity in the job payload and atomically
reserves another host when one is available. Every later routing pass honors
the accumulated exclusions for a one-minute recovery window, so ordinary job
retries cannot bounce back to a broken host or its blue/green sibling
generation while a transiently failed host can eventually rejoin a small
fleet.

The capacity node ID identifies the physical Docker host, not a deploy
generation. Blue/green worker generations keep distinct routing node IDs but
share this host-stable identity for advisory locks and reservation accounting,
so an overlapping rollout cannot present one daemon as multiple capacity pools.

Pressure cleanup runs only when the worker is physically full. A code-review
request rejected solely because it reached the interactive reserve boundary
does not reap healthy sandboxes while physical capacity remains.
