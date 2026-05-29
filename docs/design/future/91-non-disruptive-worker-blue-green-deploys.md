# Non-Disruptive Worker Blue/Green Deploys

> **Status:** Partially implemented foundation | **Last reviewed:** 2026-05-28
>
> **Related docs:** [overall.md](../overall.md), [51-worker-deploy-safety.md](../backlog/51-worker-deploy-safety.md), [82-durable-session-executors.md](../implemented/82-durable-session-executors.md), [88-preview-runtime-ownership-drain.md](88-preview-runtime-ownership-drain.md), [60-agent-runtime-timeouts-and-checkpointed-shutdown.md](60-agent-runtime-timeouts-and-checkpointed-shutdown.md)

Routine deploys must not interrupt accepted coding-agent turns or live
previews. The long-term worker deploy model is blue/green at the worker
generation boundary: start a green generation, exclude blue from new work, keep
blue routable for the live runtime leases it already owns, and let those leases
finish or expire under explicit policy.

This design records the production-grade requirements for making that true at
small scale now and at future scale with hundreds of deploys per day across
thousands of worker machines.

The first implementation adds DB-backed drain intent, worker deploy status and
audit events, a `worker-deployctl` rollout helper, admission-drain polling in
workers, routine deploy script fail-closed behavior, and runtime-aware
old-generation retirement. A future always-on deploy controller can build on
the same ownership and drain tables.

## Implementation Status

This design is **not yet completely implemented**. The current code implements
the safety-critical foundation for routine worker admission drain, but several
requirements below remain partial or future work.

### Implemented

- `nodes` now records drain intent, requested-at, budget-expiry, operator, and
  reason fields.
- `session_executors` now records runtime deadline and drain intent fields.
- `worker_deploy_events` records cluster-scoped deploy drain audit events.
- `worker-deployctl` can run routine preflight, mark a node draining, report
  node drain status, require a fresh DB heartbeat, mark over-budget executors
  with `deploy_budget_expired`, and gate retirement with `retire-ready`.
- Workers poll their own DB node state and stop local job admission when the
  node is marked `draining`, without shutting down owned preview/control paths.
- `ClaimNextRunnable` rechecks that the claiming node is active and fresh
  before accepting new work.
- Routine deploy script behavior no longer sends `docker stop` immediately to
  old worker generations. It starts green, marks old nodes draining, and runs a
  background retire loop that stops old worker containers only after
  `worker-deployctl retire-ready` succeeds.
- Routine deploys verify the green worker generation has published a fresh DB
  heartbeat before any blue generation is marked draining.
- The old-generation retire loop marks active executors whose absolute runtime
  deadline has elapsed as `deploy_budget_expired`; executor heartbeats observe
  that intent and request a typed orchestrator checkpoint/requeue stop instead
  of sending a routine SIGTERM.
- Routine worker deploys fail closed when no safe blue/green worker port is
  available.
- Routine worker deploys skip support-service recreation and block Docker/runsc
  mutation paths unless the operator explicitly chooses maintenance-style
  behavior.
- Maintenance/emergency drains require reason/operator metadata and refuse to
  proceed across active runtime ownership unless `--force` or
  `FORCE_INTERRUPT_ACTIVE_RUNTIMES=1` is supplied.
- Executor container `StopTimeout` is configurable and defaults to a long
  window.
- Executor context cancellation no longer synthesizes a planned-rollout
  `worker_drain` stop request.
- Existing preview endpoint reuse checks remain hard blockers for routine port
  selection.
- Log alerts now cover deploy-budget-expired checkpoint stops and rejected
  deploy stop requests.
- Session detail payloads and UI expose `deploy_budget_expired` as a distinct
  recovery reason when a deploy-time runtime ceiling causes checkpoint/requeue.

### Partially Implemented

- **Preflight:** deploy script and `worker-deployctl preflight` verify a safe
  candidate port path, but preflight does not yet verify spare CPU/memory,
  DB schema compatibility, or support-service config diffs.
- **Human-input release:** the orchestrator already checkpoints and marks
  sessions `awaiting_input`; there is not yet a drain-aware path that explicitly
  records `human_input_checkpoint` intent or proves blue ownership is released
  immediately because the node is draining.
- **Preview preservation:** active preview routing and endpoint ownership are
  preserved, but there are no deploy-specific preview unavailable reasons,
  dashboards, or alerts.
- **Maintenance mode:** `DEPLOY_MODE=maintenance` and `worker-deployctl
  force-maintenance` exist as primitives and require an explicit force override
  before crossing active runtime ownership, but maintenance preflight does not
  yet render a complete dry-run impact report by runtime identity.
- **Retire readiness:** `retire-ready` counts executors, active previews,
  DB-owned jobs, session container holds, sandbox holders, and endpoint
  blockers. It does not yet account for post-PR uploads or all detached cleanup
  work described in the Phase 4 target lifecycle.
- **Observability:** DB audit events, status output, and core log alerts exist,
  but the deploy metrics and dashboards in this document are not yet
  implemented.

### Not Yet Implemented

- Deploy-induced recovery metadata is persisted at the session runtime-stop
  level, but thread-level recovery metadata and richer operator-facing event
  history are not yet implemented.
- Fleet deploy waves, canaries, pause/resume, rollback orchestration, regional
  rate limits, and durable deploy-wave state are not implemented.
- A dedicated deploy controller service is not implemented; v1 remains
  SSH-script-driven.
- Operator controls for per-session drain extension and full dry-run impact
  reports are not implemented.
- Explicit active-executor image/tag retention for rollback beyond Docker's
  active-container protections is not implemented.
- Executor logs do not yet consistently include deploy generation and drain
  intent.

## Problem

Today the system has the right building blocks but the wrong routine-deploy
semantics in a few critical places:

- Worker generations can overlap, but old workers are still stopped with
  SIGTERM as part of rollout.
- Durable session executors run outside Docker Compose, but executor SIGTERM
  currently means "typed worker drain": interrupt the turn, checkpoint if
  possible, and requeue.
- Preview runtime ownership is durable enough to keep routing to an old worker,
  but host port reuse and support-service restarts can still force loss.
- Some deploy steps can restart Docker or shared support containers; those
  operations kill or disrupt runtime state below the app-level drain protocol.

The user-visible failure mode is bad: a deploy can interrupt a live turn, the
retry path may race with the old sandbox/runtime holder, and the UI can surface
a generic "unable to start" error even though the root cause was planned
rollout.

## Goals

1. **Routine deploys do not stop accepted work.** A coding-agent turn that has
   been accepted continues on its current executor until it completes, asks for
   human input, hits its normal runtime policy, or reaches the deploy drain
   budget.
2. **Previews remain reachable during routine deploys.** A preview runtime
   stays on the worker generation that owns it until the user stops it, it
   expires, or a preview-specific drain timeout marks it unavailable.
3. **New work moves to green immediately.** Once green is healthy, blue stops
   claiming new jobs and is removed from cold-start selection.
4. **Ownership is explicit and fenced.** Jobs, executors, previews, and node
   generations use durable identity plus leases so only one owner can commit
   terminal state.
5. **Deploy outcome is observable.** Operators can tell which sessions and
   previews are keeping a generation alive, how long they have left, and whether
   a deploy is waiting, complete, degraded, or force-interrupting.
6. **Scale is a first-class constraint.** The design must work with thousands
   of nodes, high deploy frequency, and partial regional rollouts without
   central hot rows or fleet-wide polling loops.

## Non-Goals

- Hot migration of an arbitrary running agent process between hosts.
- Preserving live runtime state after host loss, Docker daemon restart, kernel
  reboot, or executor crash.
- Infinite deploy blocking. Planned deploys may wait a bounded time; after the
  budget, the system must checkpoint/requeue or mark runtimes unavailable using
  explicit policy.
- Treating maintenance operations as routine deploys. Host patching, Docker
  daemon changes, and network/firewall replacement have a different safety
  contract.

## Definitions

- **Worker host:** The machine running Docker, sandbox containers, preview
  runtime containers, worker generation containers, and session executor
  containers.
- **Worker generation:** One deployable worker container instance with a unique
  `node_id`, build SHA, and preview/control-plane endpoint.
- **Dispatcher worker:** The worker process that claims queue jobs and starts
  durable session executors.
- **Session executor:** A per-session-turn container that owns one active
  `run_agent` or `continue_session` job until the turn completes or is
  explicitly drained.
- **Preview runtime:** A live worker attachment that serves a durable preview
  URL for one preview instance/target.
- **Routine rollout:** A normal image/config deploy where the host, Docker
  daemon, sandbox network, and support services remain available.
- **Maintenance rollout:** Any operation that may restart Docker, replace host
  networking, restart shared support services, reboot the host, or reclaim
  local runtime storage.

## Core Invariants

These invariants are the deploy contract. Violating one should be treated as a
production incident or a blocked deploy.

### Routine Rollout Invariants

- Routine rollout must not send SIGTERM to active session executor containers.
- Routine rollout must not restart Docker.
- Routine rollout must not recreate `sandbox-dns`, Chrome, Vector, or other
  shared worker-host support services if active executors or previews depend on
  them.
- Routine rollout must not reuse a worker endpoint while any active
  `preview_runtimes.endpoint_url` still references it.
- Routine rollout must not remove sandbox containers held by active sessions,
  active previews, or draining runtimes.
- Blue workers must stop claiming new work before green is considered the
  default generation for the host.
- Green workers must be healthy and have published node metadata before blue is
  marked as draining for new work.

### Ownership Invariants

- Every running `run_agent` and `continue_session` job is owned by either a
  dispatcher worker for a short handoff window or a session executor.
- Session executor ownership is fenced by `jobs.lock_token`,
  `jobs.run_owner_id`, and `session_executors.id`.
- Preview runtime ownership is fenced by runtime ID, runtime epoch, worker node
  ID, endpoint URL, and lease expiry.
- Node liveness is advisory for scheduling, not sufficient for terminal writes.
  Terminal writes must be fenced by the relevant job/runtime owner.
- A draining owner can renew leases for already-owned work but is excluded from
  new cold-start selection.

### User Experience Invariants

- A planned deploy should never surface as "unable to start" unless the turn
  never reached a durable acceptance boundary and no checkpoint can be created.
- If a planned deploy interrupts a turn after the deploy drain budget, the
  session should say that it was checkpointed and resumed because of a deploy.
- If a preview becomes unavailable because the old owner was lost or force
  drained, the preview URL remains durable and restartable.
- The UI should distinguish "still running on old generation" from "stuck".

## Target Deploy Lifecycle

### Phase 0: Preflight

Before starting green on a host, deploy preflight checks:

- The host is healthy enough to run another worker generation.
- A free worker endpoint exists in the configured blue/green port range.
- No active preview runtime currently leases the candidate endpoint.
- Docker daemon config is already compatible; the deploy will not need to
  restart Docker.
- Shared support service config changes are either absent or classified as
  maintenance.
- The database schema is compatible with both blue and green.
- The host has enough spare memory/CPU for temporary overlap.

If a routine rollout cannot satisfy those checks, it fails closed. It must not
silently fall back to a blocking stop of active work.

### Phase 1: Start Green

Start the green generation with:

- unique `node_id`,
- new build SHA,
- unique worker host port,
- advertised preview/control-plane endpoint,
- normal worker health checks,
- node heartbeat metadata including build SHA, capacity, and preview
  capability.

Green can claim new work only after it is healthy and has published active
metadata. If green cannot start, blue keeps serving and the deploy fails without
touching active work.

### Phase 2: Drain Blue Admission

Mark blue as `draining` in the node table and local process state.

Draining means:

- no new generic jobs,
- no new `run_agent` or `continue_session` handoffs,
- no new preview cold starts,
- no new branch/session preview hydrate unless the runtime is already owned by
  that generation,
- continued lease renewal and control-plane handling for already-owned work.

This is an admission-control transition, not a kill signal.

### Phase 3: Preserve Owned Runtimes

Blue continues serving:

- session executor support paths required by active turns,
- preview proxy/control-plane paths for active preview runtimes,
- sandbox-auth sockets for active executors/sandboxes,
- cleanup paths for runtimes that finish naturally.

Session executors keep running until one of these happens:

- the turn completes,
- the turn asks for human input and commits a resumable state,
- normal runtime policy stops it,
- deploy drain budget expires and the deploy policy requests checkpoint/requeue,
- the host is lost.

Preview runtimes keep serving until one of these happens:

- user stops the preview,
- preview expires,
- preview drain budget expires,
- owner is lost.

### Phase 4: Retire Blue

Blue can be stopped and removed when all of the following are true:

- no in-process dispatcher jobs remain,
- no DB-owned jobs are locked to blue,
- no active session executors report `host_node_id = blue`,
- no active preview runtimes report `worker_node_id = blue`,
- no active sandbox holds are exclusively routed through blue,
- post-PR snapshot uploads and other detached cleanup work have finished or are
  durably recoverable.

At this point stopping blue is non-disruptive.

### Phase 5: Garbage Collect

After blue is retired:

- prune old worker containers and images only after no active executor uses
  that image,
- keep enough prior images to support running executors and rollback,
- reclaim old ports only after runtime leases are gone,
- delete stale executor containers only when DB state says terminal or lost.

## Drain Classes

The current system uses a broad `worker_drain` concept. Long-term, drain intent
must be typed because different intents have different user promises.

### `planned_rollout`

Default for routine deploys.

Behavior:

- stop admission to old generation,
- do not stop active executors,
- do not stop active previews,
- wait for natural completion within configured budgets,
- checkpoint/requeue only after the per-turn deploy budget expires.

### `deploy_budget_expired`

Used when a planned rollout has waited long enough.

Behavior:

- request graceful checkpoint of active executor,
- requeue the same job for green if checkpoint succeeds,
- mark the session with deploy-specific recovery metadata,
- if checkpoint fails, fail with explicit deploy-drain failure metadata rather
  than a generic startup timeout.

### `host_maintenance`

Used when the host or Docker daemon must be restarted.

Behavior:

- block by default if active executors or previews exist,
- allow an explicit operator override,
- checkpoint/requeue executors where possible,
- mark previews unavailable/restartable if they cannot wait,
- emit maintenance-specific audit and incident events.

### `emergency_force`

Used for security, data-loss, or fleet-health emergencies.

Behavior:

- interrupt active work,
- checkpoint best-effort,
- mark affected sessions/previews with explicit force-drain reason,
- page or at least emit high-priority operator events.

## Session Executor Requirements

Session executors are the main boundary that makes non-disruptive worker deploys
possible. They must behave like durable per-turn owners, not children of a
dispatcher container.

Requirements:

- Executor containers are not managed by the worker Compose project lifecycle.
- Deploy scripts do not select executor containers as "old workers".
- Executor containers have a stop timeout at least as long as the planned turn
  drain budget, ideally configurable separately from worker container drain.
- Executor SIGTERM is not automatically equivalent to planned rollout. It must
  inspect or receive drain intent.
- Executor lease renewal continues while its host generation is draining.
- Executor final writes use detached bounded contexts so terminal state lands
  even if the process is shutting down.
- Executor cleanup removes containers only after terminal DB state is written
  or after recovery has marked ownership lost.
- A running executor must not be killed by image prune. Images referenced by
  active executor containers are protected by Docker, but deploy pruning must
  also avoid removing tags needed for rollback/recovery.
- Executor logs include `deploy_generation`, `host_node_id`, `executor_id`,
  `job_id`, `session_id`, and drain intent.

The routine deploy path should never depend on requeue to succeed. Requeue is a
fallback for budget expiry or unplanned loss, not the expected path.

## Preview Runtime Requirements

Previews are live processes behind durable URLs. Routine deploys should keep the
URL working by continuing to route to the old runtime owner.

Requirements:

- Preview routing uses `preview_runtimes.endpoint_url`, runtime ID, and epoch,
  not the newest worker node for a host.
- Draining workers continue serving preview traffic for owned runtimes.
- New preview cold starts exclude draining nodes.
- Host port reuse is blocked while any active runtime lease references the old
  endpoint.
- If no extra blue/green port exists, routine deploy fails closed rather than
  stopping active preview owners.
- If the owner is lost, the preview instance becomes `unavailable` and
  restartable; the user-facing URL remains stable.
- Preview drain timeout is separate from session turn drain timeout. A preview
  can reasonably outlive a coding turn.

## Support Service Requirements

Some worker-host services are shared beneath the deployable worker process. They
must be treated as host runtime dependencies, not as harmless sidecars.

Examples:

- Docker daemon,
- gVisor/runtime config,
- `sandbox-dns`,
- sandbox network/firewall/resolv.conf,
- Chrome/browser support,
- Vector/log shipping,
- sandbox-auth host directory.

Routine deploy rules:

- If a change requires Docker restart, classify the deploy as maintenance.
- If a change requires `sandbox-dns` replacement and active previews depend on
  it, classify the deploy as maintenance or run a proven dual-instance handoff.
- Host invariant reconciliation must be idempotent and non-disruptive in the
  steady state.
- Prune operations must never remove live runtime containers, active volumes,
  or images required by active containers.

## Scheduling And Capacity At Scale

At thousands of machines and hundreds of deploys per day, the control plane must
avoid global serialization and hot rows.

Requirements:

- Deploy orchestration operates per host or per shard, not as a fleet-wide DB
  transaction.
- Node generation IDs are immutable and unique.
- Scheduler selection filters on indexed node status, build generation,
  preview capability, capacity counters, and lease freshness.
- Capacity metadata is eventually consistent. Claim paths must recheck capacity
  with fenced durable updates before accepting work.
- Drain state changes are idempotent; retrying a deploy step must be safe.
- Deploy waves have concurrency limits by region, bucket, and fleet percentage.
- A bad build can be halted after a small canary without draining the whole
  fleet.
- Rollback starts a new generation from the previous image; it does not revive
  an already-retired generation.
- Old generations may coexist for hours. Cross-version compatibility is a
  product requirement, not an exception.

## Database And Schema Compatibility

Hundreds of deploys per day means blue and green code will routinely overlap.

Requirements:

- Migrations are expand/contract by default.
- Green code must be compatible with blue-owned runtimes that may keep running
  for hours.
- Old executors must be able to finish terminal writes after schema migrations.
- New required columns need defaults or nullable rollout phases.
- Enum changes must be additive before they are enforced.
- Background cleanup must tolerate old and new runtime metadata.
- Reapers must distinguish planned draining from lost ownership.

## Observability Requirements

Deploy safety needs direct, queryable signals.

Metrics:

- active worker generations by host/build/status,
- active executors by host/build/status/drain intent,
- active previews by host/build/status/runtime epoch,
- deploy wait time by phase,
- old-generation age,
- drain budget expirations,
- forced interruptions,
- checkpoint success/failure during drain,
- jobs requeued due to deploy,
- previews marked unavailable due to deploy or owner loss,
- endpoint reuse blocks,
- support-service restart blocks.

Logs:

- every drain intent transition,
- every executor stop request with intent and policy,
- every preview runtime loss/unavailable transition,
- every deploy fallback from routine to maintenance,
- every force override with operator identity when available.

Dashboards:

- fleet deploy health,
- per-host old generation drain status,
- top sessions/previews keeping old generations alive,
- deploy interruption rate,
- active executor heartbeat gaps,
- preview runtime endpoint reuse blockers.

Alerts:

- routine deploy interrupted any executor,
- preview unavailable count spikes after deploy,
- old generation exceeds maximum allowed age,
- endpoint reuse blocked across a whole host bucket,
- executor checkpoint failures during deploy budget expiry,
- scheduler is assigning new cold starts to draining nodes.

## Operator Controls

Routine deploy tooling should expose clear modes:

- `routine`: non-disruptive only; fail closed on active incompatible work.
- `maintenance`: may checkpoint/requeue or mark previews unavailable after
  confirmation.
- `emergency`: may interrupt immediately with explicit reason.

Force flags must be named after the consequence, not the implementation. For
example, prefer `FORCE_INTERRUPT_ACTIVE_RUNTIMES=1` over a generic
`force=true`.

Operators need:

- dry-run drain impact,
- list of active sessions/previews per host,
- estimated wait time and max budget,
- ability to pause a deploy wave,
- ability to mark a host unschedulable without stopping it,
- ability to extend drain for a specific high-value session,
- audit trail for forced interruption.

## Failure Handling

### Green Fails To Start

Blue remains active. No drain starts. Deploy fails.

### Green Starts But Cannot Claim Work

Blue remains available for existing work. Scheduler marks green unhealthy or
excluded. Deploy fails or pauses the wave.

### Blue Drain Exceeds Budget

For routine rollout:

- active executors receive `deploy_budget_expired`,
- checkpoint/requeue if possible,
- sessions get explicit deploy recovery metadata,
- previews remain until preview budget expires.

For maintenance:

- operator policy decides whether to wait, checkpoint/requeue, or mark
  unavailable.

### Host Lost During Drain

Recovery treats this as unplanned owner loss:

- executor jobs recover from latest checkpoint,
- previews become unavailable/restartable,
- scheduler excludes the dead node,
- deploy orchestration records degraded completion.

### Split-Brain Or Duplicate Ownership

Fencing must make this non-corrupting:

- only current lock token/owner can write terminal job state,
- duplicate active executor/runtime creation fails,
- recovery clears stale pre-handoff rows before retry,
- logs and metrics identify the stale owner.

## Implementation Plan

### Phase 1: Correct Routine Drain Semantics

- Introduce typed deploy drain intent distinct from executor SIGTERM.
- Change routine worker deploy so old dispatcher containers are marked
  draining for admission but active executor containers are not stopped.
- Make fallback-to-blocking-drain fail closed when active executors/previews
  exist and no blue/green port is available.
- Add deploy logs/metrics that identify every active runtime preserving an old
  generation.

### Phase 2: Executor Drain Policy

- Add executor stop timeout configuration.
- Make executor SIGTERM inspect intent or default to `host_maintenance`, not
  `planned_rollout`.
- Add `deploy_budget_expired` checkpoint/requeue path.
- Persist deploy recovery metadata on sessions/threads.
- Add tests that routine deploy does not call
  `RequestSessionStopByID(..., worker_drain)` for active executors.

### Phase 3: Preview Drain Enforcement

- Enforce endpoint reuse checks as hard routine-deploy preflight.
- Require explicit blue/green port range in production.
- Add active preview runtime drain dashboards and alerts.
- Ensure preview support-service changes are maintenance-only unless a
  dual-service handoff exists.

### Phase 4: Fleet Orchestration

- Add host/bucket deploy waves with canary, pause, rollback, and max concurrent
  drain limits.
- Store deploy-wave state durably so CI can hand off to a controller rather
  than running long SSH sessions.
- Add old-generation garbage collection with runtime-aware safety checks.
- Add per-region rate limits and deploy SLO reporting.

### Phase 5: Maintenance Mode

- Add explicit maintenance workflow for Docker daemon, gVisor, kernel, firewall,
  and support-service changes.
- Maintenance preflight lists active affected runtimes and blocks by default.
- Maintenance override records reason, operator, affected runtimes, and
  recovery outcome.

## Acceptance Criteria

A routine worker deploy is complete only if:

- green generation is healthy and claiming new work,
- blue generation is excluded from new work,
- no active executor was interrupted by the deploy before its deploy budget,
- no active preview was marked unavailable before its preview drain budget,
- all old endpoints are either still leased by live runtimes or safely retired,
- no generic startup-timeout failure was caused by deploy drain,
- deploy dashboards show old-generation drain status and affected runtimes.

At scale, the system is acceptable only if:

- deploy waves can run continuously without central lock contention,
- scheduler ignores draining nodes for new cold starts,
- old generations can coexist for hours without schema or routing corruption,
- forced interruption rate is measurable and near zero for routine deploys,
- operators can explain every deploy-induced interruption from logs and DB
  state alone.

## Open Questions

- What is the default routine deploy turn budget: 60 minutes, the current hard
  runtime ceiling, or an org/session policy?
- Should a session asking for human input during blue drain stay owned by blue
  until the user answers, or should it checkpoint and release immediately?
- Should long-lived previews have a shorter deploy drain budget than normal TTL
  to cap old-generation age?
- Do we need a dedicated deploy controller service before fleet size justifies
  it, or can SSH-driven per-host scripts remain acceptable for the next phase?
- Which support services can be made dual-instance so they can change during
  routine rollout instead of forcing maintenance mode?
