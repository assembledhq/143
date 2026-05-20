# Design: Agent Runtime Timeouts and Checkpointed Shutdown

> **Status:** Future
>
> **Related docs:** [51-worker-deploy-safety.md](../backlog/51-worker-deploy-safety.md), [overall.md](../overall.md)

## Summary

143 should keep the current `25 minute` default as a **shared-infrastructure soft
budget**, but it should stop treating that number as the only kill switch for a
coding session.

The product contract should become:

1. A coding session gets a default runtime budget of 25 minutes.
2. If the agent is still making meaningful progress near that budget, the
   system may extend the run within policy rather than killing it immediately.
3. When the platform does need to stop a run, it first attempts a **graceful,
   checkpoint-producing shutdown**.
4. If graceful shutdown fails, the system force-stops the process and resumes
   later from the latest durable checkpoint rather than pretending no work was
   lost.

This gives users the experience they expect from a coding agent:

- short or stuck runs do not silently burn capacity forever
- legitimately long runs are not punished just because they crossed a single
  wall-clock threshold
- interruption is recoverable and legible
- the UI explains whether the system preserved the session state, rolled back to
  the previous checkpoint, or lost the in-flight turn

## Problem

The current system has a useful but incomplete timeout model.

What exists today:

- org settings default `max_session_duration_seconds` to 25 minutes
- worker handlers wrap `run_agent` and `continue_session` in
  `session timeout + cleanup buffer`
- completed turns are snapshotted and persisted
- explicit user cancel tries a graceful SIGINT path before force-cancelling
- worker crash recovery reclaims the job and resumes from the last committed
  checkpoint

What is still wrong:

- timeout expiry is effectively a **hard failure path**, not a graceful stop path
- in-flight work from the current turn may be discarded on timeout
- a single wall-clock budget does not distinguish "stuck" from "still making
  healthy progress"
- graceful cancellation support is uneven across agents
- resume fidelity is uneven across agents
- the user-facing contract is not explicit about what survives interruption

That means `25 minutes` is currently serving two jobs badly:

1. protecting shared capacity
2. deciding when a run should die

These should be separate concerns.

## Current State

### Current defaults

- Default per-session timeout: `25 minutes`
- Current org-level minimum: `2 minutes`
- Current org-level maximum: `2 hours`
- Worker handler cleanup buffer: `2 minutes`

### Current recovery boundary

Today the durable recovery boundary is the **last fully committed turn**:

- `current_turn`
- `snapshot_key`
- `agent_session_id` when available
- stored messages
- stored diff / summary

This is good enough to recover from worker loss, but it is not equivalent to
"the exact live process continues running."

### Current user-visible weakness

If a run times out during a turn, the platform may fail the session without
producing a new checkpoint for that turn. For users, this reads as:

- "the run took too long"
- "please retry"

That is operationally safe, but it is not a good coding-agent experience.

## Goals

This design should satisfy seven guarantees:

1. **Soft default, not arbitrary kill.** `25 minutes` remains the default
   budget, not the only control.
2. **Progress-aware execution.** A healthy long-running session should be
   extendable within policy.
3. **Graceful-first shutdown.** Stop the agent cleanly before force-killing it.
4. **Checkpointed interruption.** Preserve enough state that continuation is
   easy and predictable.
5. **Explicit capability model.** The platform must know which agents support
   full conversational resume vs filesystem-only resume.
6. **Legible UX.** Users should understand whether the session was extended,
   paused safely, rolled back to an earlier checkpoint, or lost in-flight work.
7. **Shared-capacity protection.** The system must still bound runaway or wedged
   sessions.
8. **Cluster fairness.** Long-running sessions must not starve short jobs,
   block worker drain indefinitely, or monopolize org capacity merely because
   they are still alive.
9. **Checkpoint sustainability.** Stronger checkpointing must remain affordable
   and predictable at large worker counts.
10. **Deterministic admission.** Extension and recovery decisions must be
    serialized through a shared source of truth so workers do not make
    contradictory decisions from stale local views.
11. **Recovery containment.** Losing one worker must not trigger an unbounded
    restore storm that degrades the rest of the fleet.

## Non-Goals

This design does not promise:

- indefinite execution with no upper bound
- uninterrupted live process continuation across host failure
- perfect in-memory state migration for every agent CLI
- solving deploy independence on its own without the durable executor work from
  [51-worker-deploy-safety.md](../backlog/51-worker-deploy-safety.md)

## Principles

### 1. Separate policy from mechanism

The system should distinguish:

- **soft runtime budget**: when the platform starts asking whether to extend,
  checkpoint, or stop
- **idle/no-progress budget**: when the agent appears wedged
- **absolute runtime ceiling**: the hard upper bound for shared infrastructure

### 2. Preserve work before killing work

If a stop is intentional and the process is still responsive, the platform
should attempt to:

1. ask the agent to wind down
2. capture state
3. persist the checkpoint
4. then terminate the container

### 3. Be honest about capability differences

Some agents can resume an actual conversation. Some can only continue from a
restored filesystem. The product should model this explicitly rather than
pretending all resumes are equivalent.

### 4. Prefer progressive intervention

The correct escalation order is:

1. observe
2. extend if healthy
3. gracefully stop and checkpoint
4. force-stop only if necessary

## Recommended Runtime Model

### Default budgets

Recommended defaults:

- **Soft runtime budget:** 25 minutes
- **No-progress timeout:** 15 minutes without meaningful progress
- **Graceful shutdown window:** 30 seconds
- **Checkpoint finalization window:** 30 seconds
- **Absolute runtime ceiling:** 90 minutes by default, with org-configurable cap

Rationale:

- 25 minutes is still a good default threshold for "this run is getting long"
- 15 minutes of no output/tool activity is a better stuckness signal than raw
  wall clock
- 90 minutes is long enough for legitimately heavy repo work, but still bounded
  for shared infrastructure

The exact numbers can be tuned later, but the three-budget model is the real
design change.

### Progress signals

A run should count as making progress when any of these move recently:

- assistant output
- tool-use events
- command completion events
- diff growth
- explicit agent checkpoint event

An active parsed tool or sandbox command is not treated as idle just because it
is temporarily quiet. The no-progress timer applies when the agent is between
tools or otherwise not emitting output; long-running active commands remain
bounded by the soft budget, extension policy, and absolute ceiling.

The adapter-level CLI process itself is not an active tool. A long-lived
Codex/Claude/Gemini process can be alive while the platform has not parsed any
meaningful tool lifecycle or sandbox-command event. That state must not
suppress no-progress shutdown; otherwise silent agent hangs drift into soft
budget stops instead of the more accurate `no_progress` stop reason.

Not all signals are equally strong. The system should classify them as:

- **Strong progress**
  - successful tool completion
  - diff changed
  - file mutation observed
  - explicit checkpoint written
  - user question reached a new blocking state

- **Weak progress**
  - assistant output
  - reasoning text
  - tool-use event with no successful completion yet

### Terminal session and job invariant

Runtime watchdog failure is a user-visible terminal decision. Once a watchdog
fails/cancels/completes a session, the matching `run_agent` or
`continue_session` job must not continue renewing its lease or later write a
second terminal state.

The invariant is:

```sql
SELECT COUNT(*)
FROM jobs j
JOIN sessions s ON s.org_id = j.org_id
  AND s.id::text = j.payload->>'session_id'
WHERE j.status = 'running'
  AND j.job_type IN ('run_agent', 'continue_session')
  AND s.status IN ('completed', 'failed', 'cancelled', 'skipped');
```

The expected value is always zero. The session reaper terminalizes running
session jobs after runtime-control watchdog failure, and `RenewLease` refuses
to renew session runner jobs whose referenced session is already terminal.

### Cancellation observability

Interactive cancellation logs should expose the full lifecycle, not only the
requested stop reason. Force-stop paths include structured fields for graceful
interrupt delivery, kill delivery, Docker exec transport closure, and bounded
force-stop timeout. If the exec wait path hangs after SIGKILL/transport close,
the kill path returns under the bounded context so the worker handler can lose
ownership/cancel and detached cleanup can proceed.
  - repeated retries of the same failing command

Only strong progress should justify repeated automatic extensions. Weak
liveness signals are useful to avoid false "wedged" detection, but they are not
enough on their own to keep extending a run.

The strongest signals are:

- successful tool completion
- diff changed
- explicit checkpoint written

### Extension policy

When the soft runtime budget is reached:

- if the run is still making progress, extend automatically by a bounded amount
- if the run is idle or wedged, begin shutdown

Recommended initial extension policy:

- extend in `10 minute` increments
- cap total automatic extension at `30 minutes` beyond the default budget
- emit an audit/log event for every extension

This keeps the default experience simple while avoiding bad kills on healthy
long-running work.

### Extension admission control

Automatic extension must be a **cluster-level admission decision**, not just a
per-session heuristic.

### Extension authority

The system must have a single source of truth for extension grants.

For the current worker model, the recommended authority is a **serialized
shared-state decision in Postgres** tied to the current lease holder, not an
in-memory worker-local heuristic.

Required properties:

- only the worker holding the current lease/fencing token may request an
  extension for that session
- the extension grant is committed through a compare-and-set style update on
  shared state
- the decision records the granting worker, the current lease token, the prior
  deadline, the new deadline, and the reason code
- a worker that loses ownership before the shared-state write completes must
  treat the extension as denied

This gives the system one auditable answer to "was this run extended?" and
avoids split-brain cases where two workers reach different conclusions from
slightly stale cluster views.

Before granting an extension, the platform should check:

- org concurrency usage
- queue age for waiting sessions
- cluster free-capacity threshold
- whether the owning node is draining
- whether the session has already consumed its extension budget
- whether the run has shown recent **strong** progress rather than only weak
  liveness signals

Recommended first policy:

- deny automatic extension when the owning node is draining
- deny automatic extension when the org is already over its fair-share target
- deny automatic extension when queue latency for pending sessions is above a
  configured threshold
- prefer short jobs over extending already-long jobs when the cluster is under
  pressure
- require the serialized shared-state extension write to succeed before the run
  is treated as extended

This matters for scale. Without admission control, healthy long-running sessions
can still degrade the overall user experience by monopolizing capacity and
blocking new work from starting.

## Shutdown Sequence

The platform should adopt a staged shutdown state machine for coding sessions.

### New internal sequence

1. **running**
2. **stopping_gracefully**
3. **checkpointing**
4. terminal outcome:
   - **idle** or **completed** with a new checkpoint
   - **failed** with prior checkpoint preserved
   - **failed** with in-flight work lost

These do not all need to become first-class DB statuses in the first rollout.
The minimum requirement is that the orchestrator and logs model them
structurally, even if the user-facing session status remains `running` until a
terminal write.

### Stop algorithm

When the platform decides to stop a run:

1. mark the session as entering graceful stop
2. send an agent-appropriate interrupt
3. wait for the graceful shutdown window
4. if the process exits, capture and persist checkpoint state
5. if the process does not exit, force-cancel execution
6. destroy the container
7. resume later from the latest durable checkpoint

### Ownership and fencing

Every graceful stop and checkpoint publication must be guarded by the current
run-ownership token.

This is required because a large distributed system can race in all of these
ways:

- the original worker is checkpointing while a recovery loop has already
  re-queued the job
- a user-triggered continuation is hydrating while an old owner is still
  cleaning up
- a draining worker is finishing a graceful stop while another worker is
  claiming resumed work

The design contract should therefore be:

- only the owner holding the current lease/fencing token may publish a new
  checkpoint pointer or terminal state
- a worker that loses ownership may still do best-effort local cleanup, but it
  must not publish resumable state
- recovery always trusts the latest checkpoint pointer that was committed by the
  current owner, never a blob that exists only in object storage

### Lease behavior during checkpointing

Checkpointing is still part of active run ownership and must keep that
ownership alive explicitly.

Required contract:

- entering `checkpointing` does **not** suspend lease renewal
- the owner must keep renewing the lease during checkpoint upload and metadata
  publication
- before starting a large checkpoint upload, the owner should renew or extend
  the lease so the remaining lease floor comfortably exceeds the expected upload
  plus publication time
- the recovery loop may reclaim only when the lease actually expires; it must
  not treat `checkpointing` as abandoned work on its own

Recommended first implementation:

- add a `checkpointing_started_at` marker to shared state for observability
- renew the lease on the normal cadence while checkpointing is in progress
- enforce a minimum lease floor before uploading a checkpoint blob
- if the owner cannot renew the lease during checkpointing, abort publication
  and fall back to the previous committed checkpoint

This prevents mid-upload reclamation from creating duplicate restore attempts,
orphan blobs, and user-visible recovery thrash.

### Checkpoint publication order

Checkpoint publication must be atomic from the platform's point of view.

Required order:

1. create checkpoint payload
2. upload blob to object storage
3. verify ownership token is still current
4. update DB row to point at the new checkpoint and its metadata
5. write terminal/session state that exposes resumability to the user

If ownership is lost between steps 2 and 4:

- the uploaded blob becomes an unreferenced orphan candidate
- the worker must not publish it as the active checkpoint
- recovery continues from the previous committed checkpoint

This prevents a later resume from hydrating a partially-published or stale
checkpoint.

### Mass recovery throttling

Recovery after worker loss must be admitted and rate-limited just like runtime
extensions.

If a node disappears while many sessions are active, the platform must not try
to restore all of them immediately. Doing so can stampede:

- checkpoint object storage
- sandbox/container startup
- git clone or repo hydration
- DB metadata writes

Required controls:

- per-org recovery concurrency cap
- cluster-wide recovery concurrency cap
- jitter/backoff between retries for the same session
- explicit prioritization between resumed work and fresh queued work
- observability on recovery queue depth and restore wait time

Recommended first policy:

- recovered sessions enter a bounded **resume queue**
- resumption competes with fresh work under explicit cluster-wide quotas
- give each org a capped share of concurrent recoveries
- prefer restoring sessions that already have a committed checkpoint over
  restarting sessions from scratch
- when the fleet is under pressure, degrade gracefully by delaying recovery
  rather than stampeding the platform

### Important distinction

The system must separate:

- **user cancel**
- **policy stop after healthy long run**
- **policy stop after no progress**
- **hard timeout / force-kill**

These should not all collapse into the same "timed out" user message.

## Checkpoint Contract

### Minimum checkpoint payload

A durable checkpoint should include:

- workspace snapshot
- agent state directory
- agent session id / resume token when supported
- turn number
- latest assistant summary
- latest diff and diff provenance
- latest user message reference
- checkpoint timestamp
- checkpoint capability class

### Checkpoint cost model and guardrails

The design should explicitly constrain checkpoint cost before expanding beyond
completed-turn snapshots.

Required controls:

- **frequency limits**
  - at most one graceful-stop checkpoint per stop attempt
  - intra-turn checkpoints must have a minimum spacing window
- **size caps**
  - enforce a maximum compressed checkpoint size
  - reject or degrade oversized checkpoints with a clear reason
- **retention tiers**
  - keep the latest committed checkpoint as the primary resume point
  - keep a bounded number of historical checkpoints for debugging/review
  - expire superseded intra-turn checkpoints more aggressively than completed
    turn checkpoints
- **deduplication / reuse**
  - avoid writing a fresh checkpoint when the workspace and agent-state payload
    are unchanged relative to the latest committed checkpoint
- **backpressure**
  - when object storage or metadata persistence is degraded, the platform should
    disable optional checkpoint creation before it degrades all worker
    throughput

Recommended initial policy:

- treat completed-turn checkpoints as mandatory
- treat graceful-stop checkpoints as best-effort but preferred
- keep intra-turn checkpoints disabled by default until storage cost and restore
  latency have been measured in production
- publish and review checkpoint size, upload latency, restore latency, and blob
  count thresholds before enabling intra-turn checkpoints by default

### Behavior under storage pressure

The design should define what happens when checkpoint infrastructure is slow or
unhealthy.

Recommended behavior:

- if mandatory checkpoint publication fails, preserve the previous committed
  checkpoint and surface that rollback clearly to the user
- if optional checkpoint publication fails, proceed with stop/recovery but emit
  degraded-state telemetry
- if checkpoint storage is broadly degraded, pause automatic extension and
  optional checkpoint creation so the platform does not convert storage trouble
  into a fleet-wide worker outage

### Persistence-degraded admission mode

The platform should have an explicit degraded mode for runtime persistence.

When mandatory checkpoint durability is impaired, the system should not keep
admitting new long-running resumable work as if nothing changed.

Recommended behavior:

- reject or defer new resumable sessions when persistence health is below the
  mandatory checkpoint threshold
- lower concurrency caps for existing long-running work
- disable automatic extension
- allow operators to choose whether short best-effort/stateless runs may still
  proceed
- surface a clear user-facing error such as:
  - "Runtime persistence is temporarily degraded. New resumable sessions are
    paused until checkpoint storage recovers."

This prevents the platform from spending worker capacity on work it cannot
preserve safely.

### Capability classes

The platform should classify agents into three resume modes:

1. **Full resume**
   - restored filesystem
   - restored agent state
   - supported headless conversation/session resume

2. **Filesystem resume**
   - restored filesystem
   - maybe restored agent home state
   - no reliable headless conversation resume

3. **No durable resume**
   - no trustworthy continuation beyond best-effort reconstruction

Initial expected mapping from current behavior:

- `codex`: full resume
- `claude_code`: full resume
- `gemini_cli`: full resume
- `amp`: filesystem resume
- `pi`: filesystem resume

This capability must be stored in code, not inferred ad hoc in product copy.

### Checkpoint boundaries

Recommended rollout:

1. keep **completed turn** checkpoints as the durable baseline
2. add **graceful-stop checkpoints** next
3. add **intra-turn checkpoints** later for autonomous long runs

The platform should never claim to resume from an in-flight checkpoint until it
has a clear atomicity contract for partially-written state.

## User Experience

### What users should see

The system should expose four distinct experiences:

1. **Run extended**
   - "The session is still making progress. We extended its runtime."

2. **Paused safely**
   - "We stopped the session cleanly and saved its state. You can continue from
     the latest checkpoint."

3. **Rolled back to previous checkpoint**
   - "The current turn could not be checkpointed. Your previous checkpoint is
     still available."

4. **In-flight work lost**
   - "The session had to be force-stopped before state could be saved. Retry
     from the last saved point."

Two additional distributed-system states should also be first-class:

5. **Recovery in progress**
   - "The worker running this session disappeared. We are restoring it from the
     latest saved checkpoint."

6. **Saved state unavailable**
   - "We expected saved state for this session, but it could not be restored.
     You can retry from the previous checkpoint or start a fresh follow-up
     turn."

7. **Resume queued**
   - "We have your saved state, but recovery is waiting for capacity. We will
     resume automatically when a slot is available."

### Recovery SLA and fallback

`Recovery in progress` must be time-bounded.

Recommended product contract:

- if recovery has not started within a short bounded window, transition the user
  view to **Resume queued**
- if recovery repeatedly fails or exceeds the recovery SLA, transition to
  **Saved state unavailable** or an explicit **Retry required** state with a
  clear next action

Recommended initial SLOs:

- begin recovery attempt within `2 minutes` of ownership loss when capacity is
  available
- if recovery is capacity-constrained, communicate **Resume queued** instead of
  leaving the session in indefinite limbo
- if the session cannot be restored within `10 minutes`, stop showing
  `Recovery in progress` and surface a terminal recoverable state

The user should never be left watching an indefinite spinner with no next step.

### Product rule

Never tell the user only "timed out" when the more important truth is one of:

- state preserved
- prior checkpoint preserved
- current turn lost
- recovery in progress
- resume queued
- saved state unavailable

That distinction matters more than the raw timeout number.

## Observability

The runtime should record:

- configured soft budget
- configured no-progress timeout
- configured hard ceiling
- last progress timestamp
- progress type
- number of automatic extensions
- graceful shutdown attempted
- graceful shutdown succeeded
- checkpoint after graceful stop succeeded
- extension granted vs denied
- extension denial reason
- force-stop reason
- resumed from checkpoint
- checkpoint capability class
- checkpoint bytes written
- checkpoint upload latency
- checkpoint publication latency
- checkpoint restore failure reason
- queue age at extension decision time
- cluster free-capacity state at extension decision time
- storage degradation mode
- recovery queue depth
- recovery queue wait time
- recovery admission granted vs denied
- lease-renewal failures during checkpointing
- checkpoint orphan cleanup count

Recommended derived metrics:

- timeout rate by agent type
- extension rate by agent type
- extension denial rate by reason
- graceful-stop success rate
- percent of stops that preserved current-turn state
- percent of recoveries that rolled back to a previous checkpoint
- average lost work window
- checkpoint upload p95 / p99
- checkpoint restore p95 / p99
- checkpoint backpressure activation rate
- queue wait time vs extension rate correlation
- recovery queue latency p95 / p99
- resumed-vs-fresh admission ratio under pressure
- lease-renewal failure rate during checkpointing

## Rollout Plan

### Phase 1: Correctness and parity

- make graceful stop the standard timeout path, not just explicit user cancel
- extend graceful interrupt support to every coding agent
- classify each agent by resume capability
- update user-facing error copy to distinguish preserved vs lost state

### Phase 2: Progress-aware budgets

- add no-progress tracking
- add automatic bounded runtime extensions
- add cluster admission control for extension decisions
- add serialized shared-state extension grants tied to the current lease holder
- add strong-vs-weak progress scoring
- log and audit every extension decision

### Phase 3: Stronger checkpoints

- add graceful-stop checkpoints
- persist checkpoint metadata explicitly
- add checkpoint size/frequency/retention controls
- add fenced checkpoint publication and orphan cleanup
- keep lease renewal active during checkpoint upload/finalization
- surface "resume quality" in the session model and UI

### Phase 3.5: Recovery containment

- add bounded resume queue with per-org and cluster-wide caps
- add recovery backoff/jitter
- add user-facing `Resume queued` and recovery-SLA fallback states
- add persistence-degraded admission mode

### Phase 4: Durable executors

- combine this design with the per-session executor model from
  [51-worker-deploy-safety.md](../backlog/51-worker-deploy-safety.md)
- move long-running process ownership out of the general worker lifecycle

## Recommended Product Decision

The recommended product decision is:

- keep `25 minutes` as the default soft runtime budget
- do not use that number as the only kill condition
- stop long-running sessions with a graceful, checkpoint-first path
- extend healthy runs within policy
- expose the actual preservation outcome to users

That is the right balance between staff-engineering concerns
and user experience:

- infrastructure stays bounded
- healthy long runs are not punished
- interruptions are understandable
- continuation feels reliable instead of accidental

## Open Questions

1. Should automatic extension be org-configurable, or always on with a fixed
   cap?
2. Do we want explicit session statuses for `stopping_gracefully` and
   `checkpointing`, or are structured logs plus timeline messages enough for the
   first rollout?
3. For filesystem-resume agents, should the UI say "continue from workspace"
   instead of "resume conversation"?
4. When a graceful-stop checkpoint succeeds on a non-interactive run, should the
   session return to `idle`, `failed_recoverable`, or remain `failed` with a
   recoverable-action banner?
5. Should draining workers be allowed to finish an already-started extension
   window, or must all new extensions be denied once drain begins?
6. What compressed checkpoint size and upload-latency thresholds should trigger
   backpressure or automatic disabling of optional checkpoints?
7. Should fresh queued work or checkpoint-backed recoveries have priority when
   the cluster is saturated, and should that differ for manual vs automated
   sessions?
8. Do we want a dedicated scheduler/recovery coordinator process in the long
   term, or is a serialized Postgres-based authority sufficient through the
   worker-era architecture?
