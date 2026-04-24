# Design: Worker-Affine Session Resume Jobs

> **Status:** Not Started | **Last reviewed:** 2026-04-24

## Summary

`continue_session` currently uses the global job queue with no worker affinity.
That is correct for snapshot-backed resumes, but it is incorrect for sessions
that still have a live sandbox container on a specific worker. In that case,
the resume job must execute on the worker that already owns the container.

This design adds hard worker affinity for live-container session resumes while
preserving the existing queue, lease, and recovery model. The near-term goal is
to eliminate wrong-worker resume attempts that surface as sandbox ownership
errors. The longer-term goal is to establish a general queue primitive for
node-affine work without bypassing the job system.

## Problem

The current system has three contracts that are individually reasonable but
collectively inconsistent:

1. `sessions.worker_node_id` is the durable source of truth for which worker
   owns a live session container.
2. `continue_session` is enqueued as a generic agent job with only `session_id`
   and `org_id`.
3. workers claim pending jobs from a single global queue with no node routing.

That creates a race-free but wrong execution path:

1. A session has a live container on worker A.
2. The user sends a follow-up message.
3. The API enqueues `continue_session` with no worker target.
4. Worker B claims the job first.
5. The orchestrator reuses the live `container_id` but then fails to persist
   `worker_node_id = worker-b` because the row already records `worker-a`.
6. The user sees a sandbox ownership failure even though the underlying problem
   was incorrect dispatch.

This is not just a bad error message. It means queue ownership and sandbox
ownership are out of sync.

## Goals

1. Ensure a `continue_session` job for a live container only runs on the worker
   that already owns that container.
2. Preserve the existing job queue, leases, retries, and dead-letter behavior.
3. Preserve safe crash recovery when the owning worker dies.
4. Avoid queue bypasses or ad hoc API-to-worker execution paths.
5. Keep the primitive general enough that future node-affine job types can
   reuse it.

## Non-Goals

1. Live migration of containers between workers.
2. Replacing the Postgres-backed job queue.
3. Solving every long-running session ownership problem in one patch.
4. Making fresh `run_agent` jobs node-affine by default.

## Design Principles

### Container ownership beats queue convenience

If a live container exists, the queue must route to that owner rather than
asking an arbitrary worker to discover it cannot proceed.

### Hard affinity, not soft preference

For live-container resumes, affinity must be mandatory. Soft preference still
permits the wrong worker to claim the job under load or skew, which preserves
the production bug.

### Queue remains the execution control plane

The fix should not special-case session resumes into a direct worker RPC path.
Queue-based execution still gives us dedupe, retry, leases, drain behavior, and
one place to reason about recovery.

### Worker identity must be stable

Hard affinity only works if `worker_node_id` and `target_worker_node_id`
identify durable logical workers, not per-process random IDs. Production
deploys and restarts may replace a process, but they must not silently create a
brand-new node identity for the same routable worker slot.

### Dead owners must degrade to snapshot-backed recovery

Worker affinity is only valid while that worker is alive and the container is
still usable. Once the owner is dead, the system should stop insisting on that
worker and fall back to the existing snapshot/recovery contract.

### User-visible behavior should describe recovery, not routing internals

The user should never have to understand `worker_node_id`, queue affinity, or
container CAS failures. When ownership changes underneath a session, the
product should describe the outcome in user terms:

- "resuming your existing environment" when affinity works
- "waiting for your environment's worker" when the owner is still valid but
  temporarily busy
- "rebuilding from the last saved checkpoint" when the owner is gone

Low-level ownership-routing failures should remain internal signals used for
retry and repair, not end-user copy.

## Proposed Design

### 1. Add explicit job affinity metadata

Add a nullable `target_worker_node_id` column on `jobs`.

Semantics:

- `NULL`: any worker may claim the job.
- non-`NULL`: only that worker may claim the job while the target remains
  valid.

This is intentionally generic. Although the immediate producer is
`continue_session`, the field models node-affine work directly rather than
encoding session-specific behavior into the queue.

Recommended index:

```sql
CREATE INDEX idx_jobs_pending_target_worker
  ON jobs (target_worker_node_id, priority DESC, created_at ASC)
  WHERE status = 'pending' AND target_worker_node_id IS NOT NULL;
```

The existing pending-job index remains responsible for untargeted jobs.

### 2. Set affinity only when reusing a live container

When the API enqueues `continue_session`, it should inspect the claimed session:

- if `container_id` is non-empty
- and `sandbox_state = 'running'`
- and `worker_node_id` is non-empty

then enqueue the job with `target_worker_node_id = session.worker_node_id`.

Otherwise enqueue the job with `target_worker_node_id = NULL`.

This keeps snapshot-backed resumes portable while making live-container resumes
deterministic.

If a session reports a live `container_id` but has no `worker_node_id`, treat
that container as non-reusable for enqueue purposes. Do not create a new class
of targeted jobs for ownership metadata we do not trust. The safe behavior is
to enqueue untargeted and let the resume path either rebuild from snapshot or
repair the stale live-container state.

### 3. Change claim semantics

Workers should claim jobs with the following precedence:

1. jobs targeted to `this node`
2. untargeted jobs
3. never jobs targeted to a different node

This can be implemented with one SQL statement using two candidate CTEs:

- `targeted_job`: next runnable job where `target_worker_node_id = @node_id`
- `generic_job`: next runnable job where `target_worker_node_id IS NULL`, only
  if no targeted job was selected

This ordering avoids starvation of node-affine resumes behind generic work on
the correct worker.

Retries must preserve affinity by default. A retry of a targeted job should
remain targeted unless an explicit repair path clears or rewrites the target.
This applies to transient retries, dead-letter retries, and any "retry without
consuming attempt" helper.

### 4. Draining workers may still claim their own targeted resume jobs

Current draining behavior stops all new claims. That is too blunt once jobs can
be explicitly bound to a node.

Refine drain semantics:

- **active worker**: may claim targeted-for-self and untargeted jobs
- **draining worker**: may claim only targeted-for-self jobs
- **dead worker**: may claim nothing

Rationale:

- It preserves interactive sessions that already have a live container on that
  worker.
- It matches the broader deploy-safety contract that accepted session work
  should not be interrupted by routine rollouts.
- It avoids letting a draining node take on unrelated new work.

This may extend drain duration for nodes that still own active interactive
sessions. That is an acceptable tradeoff for correctness and user experience.
Operational tooling should make this visible instead of silently breaking the
session.

### 5. Add a defense-in-depth preflight in resume execution

Even after queue affinity lands, the resume path should explicitly reject
wrong-worker execution before sandbox mutation begins.

If a worker starts `continue_session` and sees:

- `session.container_id` set
- `session.sandbox_state = running`
- `session.worker_node_id` set
- `session.worker_node_id != local node`

then it should return a retryable ownership-routing error immediately.

It should not:

- create or hydrate a sandbox
- attempt to overwrite `worker_node_id`
- surface a user-visible "ownership changed" message

This protects the invariant during rollout, mixed-version fleets, and any
future producer mistakes.

If the job itself is untargeted but the session row clearly identifies a
different live owner, the retry path should retarget the job to that owner when
possible instead of blindly putting the same untargeted job back on the queue.
That gives the system a self-healing path during mixed-version deploys and
after producer bugs.

### 6. Repair pending targeted jobs when the owner is dead

Hard affinity requires a matching dead-owner repair path so jobs do not wait
forever for a node that will never return.

Apply repair in two places:

1. **pending targeted jobs** whose target points to a dead node
2. **reclaimed running jobs** that were re-queued after a dead targeted owner

Both cases must converge to the same end state: the job becomes portable again
and the session stops advertising a dead live container.

For `continue_session`, repair should:

1. identify jobs targeted to dead workers
2. look up the referenced session
3. if the session still points at the dead worker, clear stale live-container
   ownership on the session:
   - `container_id = NULL`
   - `worker_node_id = NULL`
   - `sandbox_state = 'snapshotted'` when `snapshot_key` exists, otherwise
     `'none'`
4. set `jobs.target_worker_node_id = NULL`
5. wake the queue so another worker can claim the now-portable resume

This keeps the recovery contract aligned with the existing "resume from latest
durable checkpoint" model. If the dead worker took the only live container with
it, the job must stop pretending the container is reusable.

The session cleanup write must be CAS-scoped on `worker_node_id = dead node` so
it does not clobber a session that already moved on.

For running jobs reclaimed via the existing lease/dead-node recovery path, the
requeue step should clear `target_worker_node_id` as part of the same recovery
decision rather than relying on a later sweeper pass. Otherwise a reclaimed job
can immediately wedge itself again on the dead node.

## Why This Design

### Why not direct API-to-worker resume?

That would reduce queue latency for this one path, but it would split execution
semantics:

- no shared retry policy
- no shared lease/fencing model
- no shared drain behavior
- more custom auth and routing code

The system already has a durable execution control plane. The fix should teach
that control plane about affinity instead of bypassing it.

### Why not soft affinity with fallback to any worker?

Because the failure mode is correctness, not efficiency. If the wrong worker
can claim the job, we are back to CAS failures and user-visible retries.

### Why not solve this only in the orchestrator?

An orchestrator-side guard is necessary but not sufficient. It prevents a bad
user experience, but it still allows the queue to deliver work to the wrong
place and burn retries. Routing belongs in the queue.

### Why not hard-fail sessions with missing worker ownership metadata?

That is appropriate for new invariants we fully control, but it is a poor
product choice for resumed sessions where the best available fallback is often
"rebuild from snapshot and continue." Missing ownership metadata should trigger
safe degradation, not permanent user-facing failure.

## Rollout Plan

### Preconditions

Before enabling affinity writes in production, verify that `NODE_ID` is stable
across routine restarts and deploys for each worker slot. If worker identities
churn on every rollout, targeted jobs will degrade into artificial dead-owner
repair traffic and poor resume UX.

### Phase 1: schema and readers

1. add `jobs.target_worker_node_id`
2. add the pending targeted-job index
3. deploy workers that understand targeted claims and draining affinity-only
   behavior
4. deploy the defense-in-depth wrong-worker preflight

At this point the field is unused by producers, so behavior should remain
unchanged.

### Phase 2: producers

Enable affinity writes for `continue_session` enqueue when a live container is
present.

Guard this behind a feature flag or config gate during rollout. Reader-first
deployment is important: if old workers are still claiming jobs, targeted jobs
will not help.

### Phase 3: dead-owner repair

Deploy the pending-target repair loop and related observability.

This can follow shortly after Phase 2, but it should not be omitted. Hard
affinity without dead-owner repair creates a new wedge class for pending jobs.

## Implementation Details

This section describes the concrete patch shape against the current codebase.

### Files likely touched

- `migrations/<new>_job_target_worker_affinity.up.sql`
- `migrations/<new>_job_target_worker_affinity.down.sql`
- `internal/models/models.go`
- `internal/db/jobs.go`
- `internal/db/job_store_test.go`
- `internal/api/handlers/sessions.go`
- `internal/api/handlers/sessions_test.go`
- `internal/worker/worker.go`
- `internal/worker/worker_test.go`
- `internal/worker/handlers.go`
- `internal/worker/handlers_test.go`
- `internal/cluster/recovery.go`
- `internal/cluster/recovery_test.go`
- `internal/services/agent/orchestrator.go`
- `internal/services/agent/orchestrator_test.go`

### 1. Schema change

Add a nullable target field on `jobs`.

Suggested migration:

```sql
ALTER TABLE jobs
  ADD COLUMN target_worker_node_id text;

CREATE INDEX idx_jobs_pending_target_worker
  ON jobs (target_worker_node_id, priority DESC, created_at ASC)
  WHERE status = 'pending' AND target_worker_node_id IS NOT NULL;
```

Suggested down migration:

```sql
DROP INDEX IF EXISTS idx_jobs_pending_target_worker;

ALTER TABLE jobs
  DROP COLUMN IF EXISTS target_worker_node_id;
```

No backfill is required.

### 2. Model change

Extend `models.Job` with:

```go
TargetWorkerNodeID *string `db:"target_worker_node_id" json:"target_worker_node_id,omitempty"`
```

Update `claimedJobColumns` and all job scans in `internal/db/jobs.go` and
associated tests.

### 3. Enqueue API change

The current `JobStore.Enqueue` / `EnqueueInTx` signatures are too narrow for
the new routing metadata. Rather than adding one more positional argument, add
an options struct and keep the existing helpers as wrappers.

Suggested shape:

```go
type EnqueueOptions struct {
    DedupeKey          *string
    TargetWorkerNodeID *string
}

func (s *JobStore) EnqueueWithOptions(
    ctx context.Context,
    orgID uuid.UUID,
    queue, jobType string,
    payload any,
    priority int,
    opts EnqueueOptions,
) (uuid.UUID, error)

func (s *JobStore) EnqueueInTxWithOptions(
    ctx context.Context,
    tx pgx.Tx,
    orgID uuid.UUID,
    queue, jobType string,
    payload any,
    priority int,
    opts EnqueueOptions,
) (uuid.UUID, error)
```

Existing `Enqueue` and `EnqueueInTx` become convenience wrappers that pass
`EnqueueOptions{DedupeKey: dedupeKey}`.

That keeps the rest of the codebase stable while giving this patch a clean way
to add routing metadata.

### 4. `SendMessage` enqueue logic

In `internal/api/handlers/sessions.go`, the enqueue decision should be made
after `ClaimIdle` / `ClaimForResume`, using the claimed session row inside the
transaction.

Concrete rules:

```go
var targetWorkerNodeID *string
if session.ContainerID != nil &&
   *session.ContainerID != "" &&
   session.SandboxState == string(models.SandboxStateRunning) &&
   session.WorkerNodeID != nil &&
   *session.WorkerNodeID != "" {
    targetWorkerNodeID = session.WorkerNodeID
}
```

Then:

```go
_, err := h.jobStore.EnqueueInTxWithOptions(
    r.Context(),
    tx,
    orgID,
    "agent",
    "continue_session",
    payload,
    5,
    db.EnqueueOptions{TargetWorkerNodeID: targetWorkerNodeID},
)
```

Important detail: use the claimed session returned by the store, not the
earlier pre-transaction read.

### 5. Claim path change

The worker currently asks the job store for "the next runnable job" with no
claim mode. That is no longer sufficient once draining workers should still
claim targeted-for-self jobs.

Introduce:

```go
type ClaimMode string

const (
    ClaimModeNormal       ClaimMode = "normal"        // targeted-for-self, then untargeted
    ClaimModeAffinityOnly ClaimMode = "affinity_only" // targeted-for-self only
)
```

Then change the job store interface:

```go
ClaimNextRunnable(
    ctx context.Context,
    nodeID, ownerID string,
    lockToken uuid.UUID,
    leaseDuration time.Duration,
    mode ClaimMode,
) (*models.Job, error)
```

Worker behavior:

- non-draining worker uses `ClaimModeNormal`
- draining worker uses `ClaimModeAffinityOnly`

This is better than a boolean because the queue contract is likely to grow.

### 6. Claim SQL

Recommended SQL shape:

```sql
WITH targeted_job AS (
    SELECT id
    FROM jobs
    WHERE status = 'pending'
      AND run_at <= now()
      AND target_worker_node_id = @node_id
    ORDER BY priority DESC, created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
),
generic_job AS (
    SELECT id
    FROM jobs
    WHERE @allow_generic
      AND status = 'pending'
      AND run_at <= now()
      AND target_worker_node_id IS NULL
      AND NOT EXISTS (SELECT 1 FROM targeted_job)
    ORDER BY priority DESC, created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT 1
),
next_job AS (
    SELECT id FROM targeted_job
    UNION ALL
    SELECT id FROM generic_job
)
UPDATE jobs j
SET status = 'running',
    locked_by_node_id = @node_id,
    run_owner_id = @owner_id,
    lock_token = @lock_token,
    locked_at = now(),
    lease_expires_at = now() + (@lease_seconds * interval '1 second'),
    attempts = attempts + 1,
    updated_at = now()
FROM next_job
WHERE j.id = next_job.id
RETURNING ...
```

Map `@allow_generic` from claim mode.

This guarantees:

- self-targeted jobs win over generic jobs
- draining workers can still claim self-targeted work
- no worker can claim a job targeted to another node

### 7. Retry semantics

Targeted jobs must preserve `target_worker_node_id` unless a specific override
changes it.

That means the current retry SQL should continue to update:

- `status`
- `last_error`
- `run_at`
- lease fields

but should **not** clear or rewrite `target_worker_node_id` by default.

To support self-healing retarget on wrong-worker preflight, add an override-capable retry path.

Suggested type:

```go
type RetryOptions struct {
    PreserveAttempts   bool
    RunAt              time.Time
    ErrMsg             string
    TargetWorkerNodeID *string
    OverrideTarget     bool
}
```

Or, if you want to minimize churn, add one new helper:

```go
RetryWithLeaseAndTarget(
    ctx context.Context,
    jobID, lockToken uuid.UUID,
    errMsg string,
    runAt time.Time,
    preserveAttempts bool,
    targetWorkerNodeID *string,
) (bool, error)
```

Recommended semantics:

- ordinary retry helpers preserve the current target
- the override helper explicitly rewrites the target
- a `nil` target with `OverrideTarget=true` clears affinity

### 8. Wrong-worker preflight

This should happen in the `continue_session` job execution path before sandbox
create/hydrate logic runs.

Suggested helper in the worker/orchestrator path:

```go
func validateResumeOwnership(session models.Session, localNodeID string) error
```

Rules:

- if no live container, return nil
- if live container but no `worker_node_id`, return nil and let downstream
  rebuild / repair logic handle it
- if `worker_node_id == localNodeID`, return nil
- otherwise return a retryable routing error that carries the desired target

Suggested error shape:

```go
type RetryableError struct {
    Err                error
    TargetWorkerNodeID *string
}
```

The worker retry path then uses the target override helper to requeue the job
onto the correct node without consuming an attempt.

This is a better contract than baking retargeting into the orchestrator
because the worker already owns retry scheduling.

### 9. Recovery loop changes

There are two concrete recovery changes.

#### 9a. Reclaimed running jobs

`internal/db/jobs.go:ReclaimLostRunningJobs` should clear
`target_worker_node_id` when the job is being reclaimed because the targeted
owner is dead.

That update should happen inside the same `UPDATE jobs ... SET status = 'pending'`
statement that already clears lease fields.

#### 9b. Pending targeted jobs

Add a new job store method:

```go
RepairTargetedPendingJobsForDeadNodes(
    ctx context.Context,
    staleBefore time.Time,
    limit int,
) (int64, error)
```

Suggested SQL shape:

1. compute `dead_nodes`
2. select pending jobs where `target_worker_node_id` references a dead node
3. join referenced sessions via `payload->>'session_id'`
4. CAS-clear stale session ownership where `sessions.worker_node_id = dead node`
5. clear `jobs.target_worker_node_id`
6. return count

This should be called from the same cluster recovery loop that already invokes
`ReclaimLostRunningJobs`.

Order recommendation:

1. reclaim lost running jobs
2. repair pending targeted jobs

That keeps recovery easy to reason about.

### 10. Session cleanup SQL

The session-side dead-owner cleanup should be a dedicated store method rather
than inline SQL in the recovery loop.

Suggested method:

```go
ClearDeadWorkerSessionOwnership(
    ctx context.Context,
    orgID, sessionID uuid.UUID,
    deadWorkerNodeID string,
) (bool, error)
```

Suggested SQL:

```sql
UPDATE sessions
SET container_id = NULL,
    worker_node_id = NULL,
    sandbox_state = CASE
        WHEN snapshot_key IS NULL OR snapshot_key = '' THEN 'none'
        ELSE 'snapshotted'
    END
WHERE org_id = @org_id
  AND id = @id
  AND worker_node_id = @dead_worker_node_id
```

This must be CAS-scoped on `worker_node_id` so we do not erase a session that
was already repaired or moved.

### 11. Notifications / wake-up

Do not add a targeted Redis channel for this patch. The existing global
notifier is good enough operationally, and the correct worker will wake on the
same publish.

That keeps the first implementation simpler and avoids coupling the job
notifier to affinity semantics.

### 12. Tests to write

#### Migration / store tests

- `models.Job` scan includes `target_worker_node_id`
- enqueue persists `target_worker_node_id`
- claim returns `target_worker_node_id`

#### Claim behavior tests

- normal mode prefers self-targeted jobs over untargeted jobs
- normal mode ignores jobs targeted to other workers
- affinity-only mode claims self-targeted jobs
- affinity-only mode skips untargeted jobs

#### Retry tests

- retry preserves existing target by default
- retarget retry rewrites the target
- clear-target retry removes affinity

#### API tests

- `SendMessage` enqueues targeted resume for live running container with owner
- `SendMessage` enqueues untargeted resume when no live container exists
- `SendMessage` enqueues untargeted resume when owner metadata is missing

#### Execution-path tests

- wrong-worker preflight returns retryable error with desired target
- wrong-worker preflight does not create or destroy a sandbox
- correct worker proceeds normally

#### Recovery tests

- reclaim of running targeted job clears target on requeue
- pending targeted jobs for dead nodes are repaired and made claimable
- session ownership clear is CAS-safe

### 13. Rollout sequence in practice

Recommended deploy order:

1. migrate schema
2. deploy code that can read/write/scan the new field but does not yet enqueue
   targeted jobs
3. deploy worker claim-mode changes and retry/retarget logic everywhere
4. enable targeted enqueue behind a flag
5. deploy dead-owner repair
6. flip the flag on in production

The important rule is reader/claimer first, producer second.

## User Experience Contract

### Happy path

For a session that still has a healthy live container, follow-up messages
should feel faster than today's generic queue path because the system routes
directly back to the owning worker and avoids unnecessary rebuild logic.

### Draining owner

If the owning worker is draining but still alive, the product should prefer
continuity over aggressive migration. The session can continue to reuse the
same environment on that worker while operators see that the node is pinned by
live session affinity.

### Lost owner

If the owning worker is gone, the user should not see the current low-level
"failed to persist sandbox worker ownership" message. The desired experience is
an internal repair and then either:

- automatic resume from the latest saved checkpoint, or
- a clear message that the live environment was lost and the system is
  rebuilding from saved state

### Legacy or inconsistent metadata

If the system sees `container_id` without a trustworthy `worker_node_id`, it
should bias toward recoverability. Rebuild from snapshot is better than asking
the user to reason about an internal ownership invariant.

## Observability

Add structured logs and metrics for:

- `jobs.claimed_targeted_total`
- `jobs.claimed_untargeted_total`
- `jobs.targeted_claim_miss_total`
- `jobs.targeted_repaired_dead_owner_total`
- `sessions.resume_wrong_worker_total`
- `workers.draining_targeted_claims_total`
- `workers.draining_blocked_generic_claims_total`

Log fields should include:

- `job_id`
- `job_type`
- `session_id` when present
- `target_worker_node_id`
- `claiming_node_id`
- `session_worker_node_id`
- `container_id`

Add an operator-facing signal for "node drain pinned by live session affinity"
so long-lived drains are explicit.

## Testing Plan

### Store and queue tests

1. targeted jobs are claimed only by the matching node
2. untargeted jobs are still globally claimable
3. targeted jobs are preferred over untargeted jobs on the owning node
4. draining workers claim targeted-for-self jobs but not untargeted jobs
5. dead-owner repair clears stale affinity and retargets the job

### API enqueue tests

1. live-container session resume writes `target_worker_node_id`
2. snapshot-backed resume leaves `target_worker_node_id` null
3. sessions without `worker_node_id` remain untargeted

### Orchestrator / handler tests

1. wrong-worker preflight returns a retryable routing error
2. wrong-worker preflight does not create, hydrate, or destroy a sandbox
3. correct-worker resume still reuses the live container successfully

### End-to-end behavior tests

1. worker A owns a live container, worker B is idle, follow-up message arrives,
   worker A claims and executes the job
2. owning worker is draining, follow-up message arrives, worker A still claims
   the targeted resume
3. owning worker is marked dead, pending targeted resume is repaired and later
   resumed from snapshot on worker B

## Open Questions

1. Should targeted job repair live inside the existing dead-node recovery loop
   or run as a separate lightweight sweeper?
2. Should `run_agent` recovery eventually use the same affinity primitive when
   a live container already exists for a recovering session?
3. Do we want a user-visible "session is pinned to a draining worker" state, or
   is operator-only visibility sufficient for the first version?

## Recommendation

Implement this as a queue-level hard-affinity primitive with:

1. `jobs.target_worker_node_id`
2. targeted-first claim order
3. draining workers allowed to claim targeted-for-self jobs
4. wrong-worker preflight in resume execution
5. dead-owner repair for pending targeted jobs

That is the smallest patch that is both correct now and architecturally aligned
with the long-term worker-ownership model.
