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

Migration `000286_sandbox_workload_routing` extends the tenant-scoped `jobs`
table:

| Column | Type | Contract |
|---|---|---|
| `workload_class` | `text NOT NULL DEFAULT 'interactive'` | Check-constrained to `interactive` or `code_review`. |
| `sandbox_slot_reserved_until` | `timestamptz NULL` | Expiry for a pre-claim worker slot reservation; null also distinguishes normal session-affinity pins. |

`idx_jobs_sandbox_routing` is a partial dequeue index over pending
`run_agent`/`continue_session` work. `idx_jobs_active_workload` supports
org/workload admission counts for pending and running sandbox turns. No new
table is introduced; `jobs.org_id` remains the tenant boundary.

## API contract

No new route is introduced. The existing authenticated settings APIs expose
the org setting:

- `GET /api/v1/settings` may include
  `data.settings.code_review_max_concurrent_turns` as an integer.
- Admin-only `PATCH /api/v1/settings` accepts the existing JSON merge-patch
  body `{"settings":{"code_review_max_concurrent_turns": N}}`.
- Explicit values must be in the inclusive range `1..25`; absent/zero values
  resolve to `min(max_concurrent_runs, 10)`.
- Existing settings errors remain unchanged:
  `400 INVALID_JSON` or `400 INVALID_SETTINGS`.

Worker capacity reservation is an internal queue/heartbeat contract, not a
public API. Heartbeat metadata adds
`interactive_reserved_sandbox_slots` and `sandbox_turn_reserved_count`; no SSE
or external event payload changes.

## Admission and routing

Both `RunAgent` and `ContinueSession` enter the same sandbox-turn admission
layer. It applies the relevant org policy and reserves local capacity only when
the turn needs a fresh sandbox. A continuation reusing its existing container
still passes org admission but does not consume a second sandbox slot.

Before normal queue claim, a dispatcher transaction:

1. locks one due, unbound `run_agent` or `continue_session` job;
2. for `code_review`, locks the organization admission row and checks
   `code_review_max_concurrent_turns`;
3. chooses a fresh active worker from heartbeat capacity metadata;
4. obtains a transaction-scoped advisory lock for that worker and rechecks its
   live, local-reserved, and durable-reserved counts without counting the same
   running sandbox-turn reservation twice;
5. persists the target worker and expiring reservation atomically.

Unbound sandbox jobs cannot be claimed before this routing step. Dispatchers
prefer interactive work at equal queue priority and continue routing after a
capacity deferral, so an older org-limited review cannot monopolize each poll.

Existing sandbox affinity remains authoritative and is only released through
the existing dead/draining-worker recovery path. Claiming an affinity-bound
code-review job is nevertheless serialized by the same organization-row lock
and active-turn check. The claim transaction preserves the worker target while
atomically changing the admitted job to `running`; an org-limited affinity job
keeps its affinity pin and is briefly deferred so other runnable work can be
considered.

If a worker's authoritative local gate still rejects a fresh sandbox, the
running job immediately performs the same atomic reservation against an
alternate worker. A successful alternate reservation retries with zero delay.
The existing 10-second delay is retained only when no fleet slot is available.
The code-review org limit uses a shorter policy retry, while advisory-lock
contention leaves the job immediately runnable so another dispatcher can retry
without pretending the fleet is full.

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

## Capacity isolation

`WORKER_INTERACTIVE_RESERVED_SANDBOXES` defaults to `1`. Code-review routing
and local admission subtract this value from the worker's usable capacity;
interactive work may use the full `WORKER_MAX_ACTIVE_SANDBOXES` value. The
reserve is clamped to at most `max_active_sandboxes - 1`, so a single-slot
self-hosted worker remains usable for code review.

`code_review_max_concurrent_turns` defaults to the smaller of the org's
`max_concurrent_runs` and `10`, and is configurable from Runtime settings. The
router and affinity-aware claim path serialize this decision per organization,
and the shared local admission layer remains the authoritative final fence
after claim. Interactive admission counts only active `interactive` jobs, so
review turns do not consume the interactive org limit in addition to their own
review-specific limit.

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
