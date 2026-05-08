# Design: Thread Runtime Handles and Durable Inbox

> **Status:** Not Started | **Last reviewed:** 2026-05-08
>
> **Related docs:** [overall.md](../overall.md), [60-agent-runtime-timeouts-and-checkpointed-shutdown.md](60-agent-runtime-timeouts-and-checkpointed-shutdown.md), [68-sandbox-agent-tabs-and-threads.md](../implemented/68-sandbox-agent-tabs-and-threads.md), [70-live-agent-command-handles.md](70-live-agent-command-handles.md), [51-worker-deploy-safety.md](../backlog/51-worker-deploy-safety.md)

## Summary

143 should move thread follow-up delivery onto a two-layer runtime model:

1. a **durable platform-managed per-thread inbox** as the canonical source of
   truth for ordering, recovery, audit, and backpressure
2. a **live per-thread runtime handle** as the low-latency transport for
   delivering follow-ups into the active coding-agent process

This is the recommended long-term architecture for production coding
environments at large scale.

The platform should stop treating "queue in the app" and "send directly into
the agent" as mutually exclusive choices.

The scalable design is:

- every user follow-up is durably appended to a thread-owned inbox/event log
- if the thread currently owns a live runtime handle, the owning worker pushes
  that follow-up to the process immediately
- if the handle is absent, disconnected, paused, or owned elsewhere, the inbox
  remains the durable backlog until the thread runtime is resumed
- delivery progress is tracked by explicit cursors, not by assuming the agent's
  opaque internal queue is authoritative

This preserves the benefits of direct interactivity while keeping platform
ownership of correctness, fairness, observability, and recovery.

## Problem

The current architecture is safe but not the ideal long-term shape.

Today:

- follow-up messages are durably stored in `session_messages`
- a turn-bounded `continue_session` job owns execution
- when a thread is already running, the platform appends more messages and lets
  the worker drain them later
- threads add extra queue bookkeeping such as `pending_message_count`

This works, but it leaves three long-term problems:

1. **The runtime primitive is still turn-oriented.**
   - A follow-up is not delivered to a first-class thread runtime object.
   - It is delivered indirectly by appending durable state and waiting for the
     current worker loop to consume it.

2. **Direct conversational control is incomplete.**
   - Cancel and output streaming have runtime handles today.
   - Follow-up input delivery still does not.

3. **Queue semantics are split across layers.**
   - Message persistence is durable.
   - Some delivery policy lives in thread service state transitions.
   - Some consumption policy lives in the worker/orchestrator.
   - The coding agent's own conversational model is not exposed as a
     first-class platform abstraction.

At small scale this is acceptable. At large scale, production operator trust
depends on cleaner boundaries:

- durable ordering must belong to the platform
- live delivery must belong to a runtime handle
- backpressure and fairness must be explicit
- recovery must not depend on a single in-memory worker loop

## Recommendation

Adopt a **hybrid architecture**:

- `thread inbox` is the durable control plane
- `thread runtime handle` is the live delivery plane
- agent-native queueing is an optimization, not the source of truth

If the platform must choose an authority, the authority is the durable inbox,
never the agent process.

## Goals

1. **Direct interactivity.**
   - A running thread should accept follow-ups with low latency through a live
     runtime handle instead of waiting for the next turn boundary.

2. **Durable correctness.**
   - Every follow-up must be durably recorded before delivery is considered
     accepted.

3. **Per-thread ownership.**
   - The platform must know which worker currently owns each live thread
     runtime, and must route follow-ups to that owner deterministically.

4. **Recovery and replay.**
   - Worker loss, deploy drain, or runtime teardown must not lose accepted
     follow-ups. A replacement worker must be able to resume from durable
     state.

5. **Observable delivery.**
   - Operators should be able to answer:
     - was the message accepted?
     - was it delivered to the runtime?
     - did the runtime acknowledge it?
     - is it still pending?

6. **Scalable fairness.**
   - The system must support org-level concurrency, worker-level capacity, and
     per-thread backpressure without relying on hidden agent behavior.

7. **Thread-safe abstractions.**
   - The runtime model should work for one thread, many threads in one sandbox,
     and future thread-to-sandbox splits without redesigning the contract.

## Non-Goals

This design does not require:

- trusting the agent CLI as the sole queue implementation
- preserving one live process forever with no checkpoint boundary
- eliminating snapshots or durable resume
- making every coding agent support the same native conversational protocol on
  day one
- guaranteeing transparent live-process migration across hosts

## Principles

### 1. Durable log first, live transport second

The platform should only acknowledge a user follow-up after it is durably
persisted.

Direct runtime delivery improves latency, but it is not durable acceptance.

### 2. Platform-owned ordering, not agent-owned ordering

The agent's internal queue may exist, but the platform must own:

- message sequencing
- idempotency
- visibility
- replay
- backpressure

Otherwise correctness becomes opaque and unrecoverable.

### 3. One runtime handle per active thread

A running thread should have a single owning runtime handle. Ownership should
be explicit and leased, not inferred from whichever worker most recently wrote a
log line.

### 4. Shared sandbox does not mean shared conversation

Multiple threads may share one filesystem or sandbox, but each thread's inbox,
runtime ownership, status, and delivery cursor remain thread-scoped.

### 5. Delivery state should be explicit, not implied

The platform should not infer "probably delivered" from thread status alone.
Delivery must advance through explicit cursor transitions.

## Target Architecture

### High-level model

Each thread gets three related but distinct concepts:

1. **Durable inbox**
   - append-only follow-up records in platform storage
   - ordered by per-thread sequence number

2. **Live runtime**
   - a provider-owned interactive command handle for the currently active agent
     process
   - owned by exactly one worker while live

3. **Delivery cursor state**
   - the platform's record of what the runtime has seen and what has been
     durably acknowledged

### Control plane vs delivery plane

Control plane:

- thread message append
- sequencing
- lease ownership
- runtime registry
- backpressure
- recovery and replay
- audit

Delivery plane:

- live stdin/input writes
- stdout/stderr streaming
- graceful interrupts
- process exit detection

The same worker often hosts both, but they are separate concerns in the model.

## Data Model

### 1. Thread inbox entries

Add an explicit durable inbox abstraction for accepted thread inputs.

Recommended shape:

- `thread_inbox_entries`
  - `id`
  - `org_id`
  - `session_id`
  - `thread_id`
  - `sequence_no`
  - `message_id`
  - `entry_type` (`user_message`, later `system_control`, `tool_reply`, etc.)
  - `delivery_state` (`pending`, `delivered`, `acked`, `dead_letter`)
  - `accepted_at`
  - `delivered_at`
  - `acked_at`
  - `owner_node_id` nullable
  - `delivery_attempts`
  - `last_error`

The existing `session_messages` row remains the user-visible transcript record.
The inbox entry is the delivery contract layered on top.

Why a separate inbox row instead of only `session_messages`:

- transcript and delivery lifecycle evolve at different rates
- retries and cursor movement become simpler
- control messages should not require fake transcript rows later

### 2. Thread runtime registry

Add an explicit registry for live thread runtime ownership.

Recommended shape:

- `thread_runtimes`
  - `thread_id`
  - `org_id`
  - `session_id`
  - `runtime_id` provider/runtime identifier
  - `owner_node_id`
  - `lease_token`
  - `lease_expires_at`
  - `status` (`starting`, `live`, `draining`, `lost`, `closed`)
  - `sandbox_id`
  - `agent_type`
  - `model`
  - `last_delivered_sequence`
  - `last_acked_sequence`
  - `last_heartbeat_at`
  - `started_at`
  - `closed_at`

The durable authority for routing a follow-up to a live handle is this row, not
process-local memory.

### 3. Derived queue count

`pending_message_count` should become derived from inbox state rather than a
separately mutated truth source. It may remain materialized temporarily for UI
performance, but the long-term authority should be:

- `count(thread_inbox_entries where delivery_state in ('pending', 'delivered') and sequence_no > last_acked_sequence)`

## Runtime Contract

### Live thread handle

Promote the current interactive command handle from a turn helper to a true
thread runtime primitive.

Required abilities:

- stream stdout/stderr
- write incremental input
- close stdin when appropriate
- graceful interrupt
- force kill
- wait for exit
- expose stable runtime ID

The provider-facing runtime contract already exists in partial form. The design
change here is lifecycle and ownership, not just transport API.

### Thread runtime session

Each active thread should behave like a runtime session with:

- durable owner
- durable delivery cursors
- explicit close/drain
- resumable bootstrap from checkpoints when the live runtime disappears

## Delivery Flow

### Accepting a follow-up

1. Validate request.
2. Persist `session_messages` row.
3. Persist matching `thread_inbox_entries` row in the same transaction.
4. Commit.
5. Return success to the caller.
6. Asynchronously notify the owning worker if a live runtime exists.

### Fast path: live runtime exists on this owner

1. Owning worker receives notification or polls.
2. Worker reads inbox entries with `sequence_no > last_delivered_sequence`.
3. Worker writes the entry to the live runtime handle.
4. Worker marks the entry `delivered`.
5. When the runtime reaches the platform-defined acknowledgement boundary,
   worker advances `acked`.

### Slow path: no live runtime

1. Entry remains `pending`.
2. The next resume/start bootstrap reads from the inbox.
3. The worker rehydrates the thread runtime from durable checkpoints.
4. It replays pending entries in order.

### Acknowledgement boundary

The platform should define a concrete ack point. Recommended initial rule:

- `delivered`: bytes were successfully written into the runtime transport
- `acked`: the runtime consumed the message into an agent turn boundary known
  to the platform

If some agents cannot expose a strong ack boundary, the system may temporarily
collapse `acked` to "accepted by platform for next turn" for those agents, but
the type should exist from the start.

## Scheduling and Ownership

### Worker ownership

Each live thread runtime is owned by one worker node under a renewable lease.

The owner is responsible for:

- holding the provider runtime handle
- delivering inbox entries
- streaming output
- updating cursors
- heartbeat
- graceful drain

### Routing

When a follow-up arrives:

- if `thread_runtimes.status = live` and lease is valid, notify that owner
- otherwise leave the inbox entry pending; the scheduler may start/resume later

The API path should not block on worker availability once durable acceptance is
complete.

### Backpressure

The durable inbox gives the platform a place to enforce:

- max pending entries per thread
- max queued bytes per thread
- max live runtimes per org
- max live runtimes per worker
- fairness between active orgs

At large scale this is necessary. Agent-native queues alone cannot provide it.

## Shared Sandbox Model

The recommended long-term model still works whether:

- one sandbox hosts one active thread
- one sandbox hosts multiple live threads
- one thread is split into its own sandbox later

The inbox and runtime registry are thread-scoped, so the scheduling layer can
evolve independently of the conversation contract.

That is the key abstraction win.

## Failure and Recovery Model

### Worker loss

If the owner worker dies:

- lease expires
- runtime row becomes reclaimable
- a recovery worker marks the runtime `lost`
- pending or only-delivered-not-acked inbox entries remain durable
- recovery either restarts the runtime from checkpoint or pauses the thread in a
  legible recoverable state

### Deploy drain

Drain behavior should be:

1. stop accepting new runtime ownership
2. finish or gracefully pause current live runtimes
3. persist cursor and checkpoint state
4. release leases

### Agent/process loss

If the process exits unexpectedly:

- mark runtime closed/lost
- do not lose inbox entries beyond `last_acked_sequence`
- allow resume from durable boundary

## API and Product Contract

The user-visible contract should be:

- send is accepted once the platform durably records it
- active threads usually receive follow-ups immediately
- if live delivery is unavailable, the message remains queued durably and will
  be replayed in order
- the UI can distinguish:
  - accepted
  - delivered
  - waiting for runtime
  - paused/recovering

The product should stop implying that "queued" means "we dropped it into an
opaque worker hole." It should mean "the platform durably accepted it and is
waiting to deliver or resume it."

## Abstractions and Code Shape

### Recommended packages

Long-term, the core logic should be centered around a dedicated runtime layer,
not duplicated between handlers and thread services.

Recommended responsibilities:

- `threadinbox`
  - append entries
  - query pending ranges
  - advance delivery/ack cursors
  - backpressure helpers

- `threadruntime`
  - registry / lease ownership
  - runtime lifecycle
  - owner notification
  - drain / close / recovery hooks

- `session/thread service`
  - product-facing validation and policy
  - transcript creation
  - calls into inbox/runtime layers

- `orchestrator`
  - execution semantics
  - checkpointing
  - runtime bootstrap
  - adapter interactions

### Anti-patterns to avoid

- storing authoritative queue state in multiple counters
- letting handlers own multi-step runtime logic
- hiding delivery state in process-local memory only
- using agent-internal queue behavior as the only replay mechanism
- coupling thread identity to session identity in dedupe or routing

## Migration Plan

### Phase 1: Durable inbox without direct delivery

- introduce `thread_inbox_entries`
- write inbox rows alongside thread follow-up messages
- keep current worker drain behavior
- derive `pending_message_count` from inbox state where practical

Outcome:

- one durable queue abstraction
- no product behavior regression

### Phase 2: Runtime registry

- add `thread_runtimes`
- track owner leases and runtime IDs
- make thread ownership durable rather than process-local only

Outcome:

- platform can route follow-ups to the owning worker deterministically

### Phase 3: Direct live delivery

- add inbox-to-runtime delivery loop
- write follow-ups into the live handle when present
- persist delivery and ack cursors

Outcome:

- active threads become truly interactive

### Phase 4: Recovery and drain hardening

- worker-loss reclaim
- deploy drain semantics
- inbox replay from checkpoints
- stronger UI delivery states

### Phase 5: Optional multi-thread live concurrency

- if/when one sandbox can host multiple live runtimes safely, reuse the same
  inbox/runtime contract with a richer scheduler

## Why This Is Better Than the Alternatives

### Better than pure app-layer queueing

- lower latency
- better cancellation/input semantics
- cleaner runtime ownership
- closer to how users expect coding agents to behave

### Better than pure agent-owned queueing

- durable acceptance
- explicit audit trail
- recoverable ordering
- org-level fairness and backpressure
- multi-node correctness
- platform-owned observability

## Open Questions

1. Which coding agents can accept incremental follow-up input in a single live
   process with strong acknowledgement semantics?
2. For agents without strong acks, what is the minimum acceptable platform ack
   boundary?
3. Should inbox rows be a separate table from transcript rows permanently, or
   can the transcript row become the inbox record for some entry types?
4. When a thread is live but the sandbox is under destructive shared-workspace
   pressure, should the scheduler delay delivery or deliver with policy
   warnings?
5. Should a thread runtime lease be renewed by output activity, explicit
   heartbeat, or both?

## Recommendation

The long-term architecture should be:

- **durable per-thread inbox as control plane**
- **live per-thread runtime handle as delivery plane**
- **platform-owned delivery cursors as authority**

This is the most scalable and operationally defensible design for a production
multi-tenant coding-agent platform serving thousands of engineers.
