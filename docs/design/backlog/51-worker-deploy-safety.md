# Design Doc 51: Worker Deploy Safety For Long-Running Sessions

> **Status:** Historical design, mostly implemented | **Last reviewed:** 2026-05-20
>
> **Implementation notes:** Job leases, fencing tokens, draining workers, dead-worker recovery, deploy guardrails, bootstrap checkpointing, and durable per-session executors are implemented. Richer intra-turn checkpointing remains future work.

## Summary

143 currently treats worker restarts as acceptable brief downtime because jobs
are asynchronous. That assumption breaks down for long-running coding sessions.
Today, a worker deploy or crash can leave a `run_agent` job stuck in
`running`, a session stuck in `running`, and an in-flight sandbox with no live
coordinator.

This document proposes a two-stage architecture:

1. **Phase 1: harden the existing worker model**
   Add draining, job leases, fencing tokens, and dead-worker recovery so
   planned deploys are safe and crashes do not wedge queue state.
2. **Phase 2: move `run_agent` to durable per-session executors**
   Keep the queue worker lightweight and make the long-running run owner
   independent from routine worker deploys.

The user-visible recovery contract is:

- **planned deploys:** do not interrupt accepted long-running runs
- **unplanned worker loss:** recover from the **latest durable checkpoint**

## Implementation Status

The initial hardening and checkpoint-recovery phases described here are now in production code:

- jobs carry renewable leases plus fencing tokens (`lease_expires_at`,
  `lock_token`, `run_owner_id`)
- worker terminal writes are guarded by the current `lock_token`
- session runner lease renewal refuses to keep `run_agent` or
  `continue_session` alive after the referenced session reaches a terminal
  state
- nodes advertise `draining` status and heartbeat metadata
- a recovery loop marks stale nodes `dead` and re-queues lost running jobs
- worker deploys now use `drain -> replace` instead of blind stop/recreate,
  and worker shutdown verifies DB-owned running jobs for the node are zero
  before the process is considered fully drained
- reclaimed `run_agent` work now resumes from the latest committed session
  checkpoint when one exists, and otherwise restarts from scratch
- recovery logs include `checkpoint_available`, `restart_from_scratch`, and
  checkpoint capability fields so operators can see when intra-turn work was
  lost because no durable checkpoint existed
- startup sandbox cleanup preserves containers for sessions that are still
  `running` or in queued/recovering recovery state; it only destroys containers
  whose DB ownership is terminal or conclusively stale
- the committed checkpoint boundary is the last fully persisted turn
  (`current_turn`, `snapshot_key`, `agent_session_id`, stored messages/diff);
  blob-only snapshots not referenced by the session row are intentionally ignored

Phase 3 durable per-session executors are implemented; see
[82-durable-session-executors.md](../implemented/82-durable-session-executors.md).
Richer intra-turn checkpointing remains future work; the current recovery boundary
is bootstrap, graceful stop, or last completed turn.

The system does **not** promise uninterrupted live process continuation across
worker or host failure.

## Why This Exists

The current system has three weaknesses:

- `deploy/scripts/deploy.sh` stops and recreates the worker container on every
  worker deploy
- `internal/worker/worker.go` marks jobs `running` but does not renew a lease
  while the job is active
- `internal/cluster/node.go` writes node heartbeats, but there is no
  implemented dead-node cleanup that re-queues jobs locked by dead workers

The session reaper eventually marks stale sessions failed, but that is only a
UI/state cleanup path. It is not job recovery.

For short best-effort jobs this is survivable. For multi-hour `run_agent`
sessions it is not.

## Goals

This design must satisfy five guarantees:

1. **No permanent orphaning.** A worker death must not leave a job wedged in
   `running` indefinitely.
2. **No split-brain ownership.** At most one live owner may commit terminal
   state for a logical run.
3. **Planned deploy safety.** Routine worker deploys must not interrupt
   accepted long-running sessions unless an operator explicitly forces that
   outcome.
4. **Bounded crash recovery.** After worker or host loss, the system must
   detect ownership loss and recover within a short bounded window.
5. **Version-skew safety.** During draining deploys, old and new worker
   versions may coexist for hours without corrupting queue or session state.

## Non-Goals

This design does not attempt to guarantee:

- hot-swapping the worker binary underneath a running process
- uninterrupted live process continuation after worker or host crash
- migration of arbitrary in-memory agent state across machines without a
  checkpoint
- immediate executor isolation for every job type in the system

## Recovery Contract

The recovery contract must be explicit because the architecture depends on it.

### Planned Deploys

For planned worker deploys, the platform should preserve active runs by
draining workers before replacement.

### Unplanned Worker Or Host Loss

For unplanned worker loss, the platform does **not** promise that the same
agent process survives. Instead it promises:

1. detect lost ownership
2. reclaim the job safely
3. resume from the latest durable checkpoint

### Resume vs Restart

This document uses:

- **resume**: continue from the latest durable checkpoint
- **restart**: start again from an earlier durable boundary

The recommended contract for 143 is **resume from the latest durable
checkpoint**. If the earliest implementation only supports "resume from the
last completed turn", that still satisfies this contract as long as that turn
boundary is the latest fully committed checkpoint.

## Recommended Architecture Path

The recommended path is:

1. **Phase 1:** harden the existing worker model
2. **Phase 2:** add checkpoint-aware recovery using the latest durable
   checkpoint contract
3. **Phase 3:** move `run_agent` onto durable per-session executors

This is intentionally not a "big bang" redesign. The first phase removes the
current production footgun. The third phase is the stable long-term end state.

## Phase 1: Harden The Existing Worker Model

Phase 1 keeps the current worker process as the run owner, but makes that
ownership safe.

### What Changes

- workers support `draining` and stop claiming new jobs before deploy
- running jobs hold renewable leases
- terminal writes are guarded by fencing tokens
- dead or expired owners are reclaimed automatically
- worker deploys become `drain -> replace`, not blind `stop -> recreate`

### Why Start Here

This is the smallest implementation delta that fixes the current production bug
class. It also gives the system a stable platform for checkpoint-based recovery
before introducing executor isolation.

### Tradeoffs

Pros:

- smallest implementation delta
- fixes the current orphaned-job failure mode
- no second scheduling plane
- establishes the ownership and checkpoint model needed by the long-term design

Cons:

- worker deploys can remain blocked by long runs
- the worker still owns both dispatch and execution
- version-skew and recovery logic stay concentrated in one service

## Phase 2: Checkpoint-Aware Recovery

Once ownership and recovery are safe, the next step is making recovery good.

For `run_agent`, recovery should be:

- **planned deploy:** avoid interruption via draining
- **unplanned worker loss:** resume from latest durable checkpoint

### Checkpointing Requirements

At minimum, the design must define:

- **what is checkpointed:** filesystem snapshot, session diff, message history,
  and agent resume token/session identifier when available
- **checkpoint boundary:** after each turn at minimum; optionally intra-turn
  later for long autonomous runs
- **consistency contract:** recovery resumes only from the latest fully
  committed checkpoint, never a partially-written one
- **cost tradeoff:** more frequent checkpoints reduce lost work but increase
  I/O, storage, and latency overhead

The first implementation can use "last completed turn" as the latest durable
checkpoint if intra-turn checkpoints are too expensive initially.

## Phase 3: Durable Per-Session Executors

The long-term design is to move `run_agent` out of the queue worker and into a
dedicated executor per session.

### Why `run_agent` Is Special

`run_agent` is not like short jobs such as `sync_sentry`, `validate`, or
`open_pr`. It has:

- long wall-clock time
- expensive live state
- interactive/log-streaming expectations
- attached sandbox lifecycle
- higher user-visible cost of interruption

It should not permanently share the same execution contract as short
background jobs.

### End-State Model

- the queue worker claims work and starts a dedicated executor
- the executor owns exactly one long-running session
- the dispatcher can be redeployed without killing active executors
- crashes recover from the latest durable checkpoint

### Why This Is The Recommended End State

Pros:

- the worker becomes a lightweight dispatcher
- dispatcher deploys do not kill already-running executors
- failures are isolated to one session
- best fit for multi-hour runs
- cleanest long-term boundary for checkpoint-based recovery

Cons:

- more moving pieces
- new lifecycle and observability work
- explicit compatibility contract required between dispatcher and executor

Despite the extra complexity, this is the most stable long-term architecture
for multi-hour sessions.

## Deploy Model

### App Deploys

- can remain rolling and frequent
- must not touch active worker-owned or executor-owned sessions

### Worker Deploys In Phase 1

1. mark node `draining`
2. stop claiming new jobs
3. wait for zero active owned runs
4. replace worker service

This can delay worker rollout by as long as the longest in-flight run.

### Worker Deploys In Phase 3

1. mark dispatcher node `draining`
2. stop claiming new jobs
3. replace dispatcher service
4. leave active executors alone

This is the main operational reason to make per-session executors the end
state.

### Emergency Worker Deploys

If a security incident requires immediate replacement:

- workers may be terminated before drain completes
- jobs must be recoverable after lease expiry
- the user-facing contract remains recovery from latest durable checkpoint
- no job may remain wedged in `running`

## Compatibility Rules

Drain-and-replace means old and new versions can coexist for hours. That
imposes a compatibility contract:

- job payloads must be backward-compatible across one deploy window
- worker-used DB migrations must be backward-compatible before rollout
- checkpoint formats must be readable across adjacent worker versions
- sandbox image changes must define whether they apply only to new runs or also
  to resumed runs

If a change breaks these assumptions, it is not a normal draining deploy. It
is a coordinated worker migration.

## Rollout Plan

1. Add observability for orphaned `running` jobs, dead-node lag, and lease age
2. Implement dead-node requeue for jobs
3. Add lease fields and lock-token-guarded terminal updates
4. Add draining mode and integrate it into worker deploys
5. Split app deploys from worker deploys operationally
6. Add checkpoint-aware recovery for `run_agent`
7. Move `run_agent` and `continue_session` to durable per-session executors

This rollout sequence has shipped. Remaining hardening belongs in operational
dashboards/alerts and richer intra-turn checkpoint coverage, not in keeping
long-running turns inside the deployable worker process.

## Operational Targets

The system should be measured against these targets:

- dead worker detected within 90 seconds
- orphaned running job reclaimed within 2 minutes
- drained worker stops claiming new jobs within 10 seconds
- active sessions interrupted by planned worker deploy: 0
- stale job wedged in `running` beyond reclaim window: 0
- user-visible lost work on unplanned worker loss: bounded by checkpoint
  interval

## Failure Scenarios

The implementation should be validated against:

1. planned deploy while `run_agent` is active
2. worker `SIGTERM` during normal drain
3. worker `SIGKILL`
4. full host reboot
5. DB outage during lease renewal
6. network partition where a stale worker keeps running but cannot renew lease
7. lease expiry while a terminal write is in flight
8. old worker version draining while new worker version processes fresh jobs

## Appendix A: Ownership Model

The storage schema may remain simple, but the conceptual ownership state
machine should be explicit:

1. `pending` — no owner
2. `claimed` — owner assigned, lease established
3. `running` — handler actively executing and renewing lease
4. `reclaiming` — previous owner considered lost; another node is taking over
5. terminal states — `succeeded`, `failed`, `dead_letter`

Critical rule:

- terminal updates must be conditional on the current `lock_token`

That is what prevents an old owner from racing a new owner after lease expiry.

## Appendix B: Schema Sketches

These are not final migrations, but they make the intended concurrency model
concrete.

### `jobs`

```sql
ALTER TABLE jobs
  ADD COLUMN lease_expires_at timestamptz,
  ADD COLUMN lock_token uuid,
  ADD COLUMN run_owner_id text;

CREATE INDEX idx_jobs_reclaimable
  ON jobs (status, lease_expires_at)
  WHERE status = 'running';

CREATE INDEX idx_jobs_locked_by_node
  ON jobs (locked_by_node_id, status)
  WHERE locked_by_node_id IS NOT NULL;
```

Field meanings:

- `lease_expires_at`: when the current owner loses its claim unless renewed
- `lock_token`: fencing token required for terminal writes
- `run_owner_id`: future-proof slot for durable executor ownership; initially it
  can match `locked_by_node_id`

### `nodes`

The existing `nodes.status` column should start being used actively.

Recommended `metadata` shape:

```json
{
  "build_sha": "abc123",
  "active_job_count": 4,
  "active_run_agent_count": 2,
  "drain_requested_at": "2026-04-21T23:40:00Z"
}
```

### `sessions` (Optional)

```sql
ALTER TABLE sessions
  ADD COLUMN worker_node_id text,
  ADD COLUMN executor_id text;

CREATE INDEX idx_sessions_worker_node
  ON sessions (worker_node_id, status)
  WHERE worker_node_id IS NOT NULL;
```

These fields are optional for Phase 1 but useful for observability and for
eventual executor routing.

## Appendix C: SQL Sketches

### Claim Lease

```sql
UPDATE jobs
SET status = 'running',
    locked_by_node_id = $node_id,
    run_owner_id = $owner_id,
    lock_token = $lock_token,
    locked_at = now(),
    lease_expires_at = now() + interval '60 seconds',
    attempts = attempts + 1,
    updated_at = now()
WHERE id = $job_id;
```

### Renew Lease

```sql
UPDATE jobs
SET lease_expires_at = now() + interval '60 seconds',
    updated_at = now()
WHERE id = $job_id
  AND status = 'running'
  AND lock_token = $lock_token;
```

If this affects zero rows, the worker has lost ownership and must stop trying
to write terminal state.

### Mark Succeeded

```sql
UPDATE jobs
SET status = 'succeeded',
    completed_at = now(),
    locked_by_node_id = NULL,
    run_owner_id = NULL,
    lock_token = NULL,
    locked_at = NULL,
    lease_expires_at = NULL,
    updated_at = now()
WHERE id = $job_id
  AND status = 'running'
  AND lock_token = $lock_token;
```

### Requeue Lost Job

```sql
UPDATE jobs
SET status = 'pending',
    last_error = $reason,
    locked_by_node_id = NULL,
    run_owner_id = NULL,
    lock_token = NULL,
    locked_at = NULL,
    lease_expires_at = NULL,
    run_at = now(),
    updated_at = now()
WHERE id = $job_id
  AND status = 'running'
  AND (
    lease_expires_at < now()
    OR locked_by_node_id = $dead_node_id
  );
```

In practice, reclamation should be batched and idempotent rather than performed
row-by-row.

### Batched Reclaim

```sql
WITH dead_nodes AS (
  SELECT id
  FROM nodes
  WHERE status = 'dead'
     OR last_heartbeat_at < now() - interval '90 seconds'
),
reclaimable AS (
  SELECT j.id
  FROM jobs j
  LEFT JOIN dead_nodes d ON d.id = j.locked_by_node_id
  WHERE j.status = 'running'
    AND (
      j.lease_expires_at < now()
      OR d.id IS NOT NULL
    )
  ORDER BY j.locked_at ASC
  LIMIT 100
)
UPDATE jobs j
SET status = 'pending',
    last_error = 'job ownership lost; re-queued by recovery loop',
    locked_by_node_id = NULL,
    run_owner_id = NULL,
    lock_token = NULL,
    locked_at = NULL,
    lease_expires_at = NULL,
    run_at = now(),
    updated_at = now()
FROM reclaimable r
WHERE j.id = r.id;
```

Correctness rule:

- lease expiry is the primary correctness signal
- dead-node detection is an accelerator and diagnostics aid

## Appendix D: Internal API Sketches

These are internal interfaces, not public HTTP API.

### Lease API

```go
type JobLease struct {
    JobID          uuid.UUID
    LockToken      uuid.UUID
    LeaseExpiresAt time.Time
}

type LeaseManager interface {
    Claim(ctx context.Context, nodeID, ownerID string, jobID uuid.UUID) (JobLease, error)
    Renew(ctx context.Context, lease JobLease) (JobLease, error)
    Succeed(ctx context.Context, lease JobLease) error
    Fail(ctx context.Context, lease JobLease, errMsg string) error
    Requeue(ctx context.Context, jobID uuid.UUID, reason string) error
}
```

Rule:

- a handler must never write terminal job state without a valid current lease

### Drain-Aware Node API

```go
type NodeStateReader interface {
    IsDraining(ctx context.Context, nodeID string) (bool, error)
    PublishHeartbeat(ctx context.Context, nodeID string, meta NodeHeartbeatMeta) error
}

type NodeHeartbeatMeta struct {
    BuildSHA            string
    ActiveJobCount      int
    ActiveRunAgentCount int
}
```

Rule:

- if a node is draining, it may renew existing leases but must not claim new
  jobs

### Recovery Loop API

```go
type JobRecovery interface {
    MarkDeadNodes(ctx context.Context, staleBefore time.Time) (int64, error)
    RequeueLostJobs(ctx context.Context, limit int) (int64, error)
}
```

Recommendation:

- run recovery on worker-capable nodes under a Postgres advisory lock, or under
  the existing scheduler/leader path

## Appendix E: Deploy Integration Sketch

The deploy script should stop treating worker replacement as a blind
`stop && recreate`.

Desired deploy flow:

1. mark node `draining`
2. poll node metadata until `active_run_agent_count = 0`
3. stop worker service
4. start new worker service
5. clear `draining` once healthy

```bash
mark_node_draining "$NODE_ID"
wait_for_zero_active_runs "$NODE_ID" 10800   # e.g. up to 3h
docker compose -f docker-compose.worker.yml stop worker
docker compose -f docker-compose.worker.yml up -d --no-deps --force-recreate worker
mark_node_active "$NODE_ID"
```

If the wait times out, the operator should choose explicitly between:

- keep waiting
- abort deploy
- force replacement and rely on lease recovery

That choice should not be implicit.

## Open Questions

1. Is "resume from last completed turn" sufficient for the first recovery
   iteration, or do we need intra-turn checkpoints?
2. Should dead-node job reclamation run on every worker with an advisory lock,
   or live in the scheduler/cluster leader?
3. What checkpoint payload is portable across worker versions?
4. Which sandbox image changes are allowed while old workers are still draining?
5. What is the thinnest possible dispatcher/executor protocol that still
   supports checkpoint resume, log streaming, and fenced terminal writes?
