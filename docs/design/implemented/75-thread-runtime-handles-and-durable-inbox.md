# Design: Thread Runtime Handles and Durable Inbox

> **Status:** Implemented | **Last reviewed:** 2026-05-08
>
> **Related docs:** [overall.md](../overall.md), [60-agent-runtime-timeouts-and-checkpointed-shutdown.md](../future/60-agent-runtime-timeouts-and-checkpointed-shutdown.md), [68-sandbox-agent-tabs-and-threads.md](68-sandbox-agent-tabs-and-threads.md), [70-live-agent-command-handles.md](../future/70-live-agent-command-handles.md)

## Summary

Thread follow-ups now use a platform-owned two-layer runtime model:

1. `thread_inbox_entries` is the durable per-thread inbox and ordering source of truth.
2. `thread_runtimes` is the durable live-runtime registry for owner routing, leases, and cursors.

Accepted thread follow-ups are written to both `session_messages` and `thread_inbox_entries`. When a live runtime exists, the platform routes a node-targeted `deliver_thread_inbox` job to the owning worker and advances explicit delivery cursors. When no live runtime exists, the inbox remains pending until the next continuation turn resumes the thread.

## What shipped

### Durable inbox

- Added `thread_inbox_entries` with per-thread `sequence_no`, `message_id`, `entry_type`, `delivery_state`, delivery timestamps, owner attribution, and retry metadata.
- Thread follow-up acceptance now appends an inbox row alongside the transcript row.
- The thread service writes inbox rows on normal sends, queued mid-turn sends, sibling-queued sends, and resumptions from paused/terminal thread states.

### Runtime registry

- Added `thread_runtimes` with owner node, lease token, lease expiry, runtime status, sandbox metadata, and explicit `last_delivered_sequence` / `last_acked_sequence` cursors.
- Active thread continuations upsert a live runtime row, renew heartbeats while the turn is running, and close or mark the runtime lost/draining when the turn exits.

### Delivery flow

- Added the `deliver_thread_inbox` worker job. It is pinned to the owning node, reads pending inbox entries after the runtime cursor, and advances them to `delivered`.
- `continue_session` now snapshots the pre-run inbox boundary for the thread, marks that boundary delivered before the turn, and marks it `acked` only after the turn completes.
- Thread drain decisions now use durable inbox state for thread-scoped runs. Remaining unacked inbox entries trigger the next `continue_session`.

### Compatibility layer

- `pending_message_count` remains as a UI-facing materialized compatibility field.
- The durable authority is the inbox row set; the counter is now best-effort compatibility state rather than the primary queue contract.

## Current acknowledgement boundary

The platform now owns durable acceptance, delivery cursors, owner routing, and replay boundaries. The current agent adapters still execute follow-ups at turn boundaries rather than injecting arbitrary new user prompts into a single long-lived CLI conversation mid-turn.

That means the shipped semantics are:

- `pending`: durably accepted but not yet routed to the live runtime
- `delivered`: routed to the owning live runtime and accepted into the platform-managed live thread pipeline
- `acked`: consumed by the thread’s next platform-visible turn boundary

This matches the current agent transport surface while keeping the control plane architecture durable, explicit, and recoverable. If adapters later expose true mid-turn follow-up injection, the same inbox/runtime tables and cursor model can support that without redesign.

## Code shape

The implementation is centered around:

- `internal/db/thread_inbox_store.go`
- `internal/db/thread_runtime_store.go`
- `internal/services/thread/service.go`
- `internal/worker/handlers.go`
- `internal/services/agent/orchestrator.go`

## Operational outcome

The platform now has explicit answers to:

- Was the follow-up durably accepted?
- Is it still pending delivery?
- Was it delivered to the owning runtime?
- Was it acked by a completed thread turn?
- Which worker owned the live runtime when delivery happened?

That is the production boundary this design was intended to establish.
