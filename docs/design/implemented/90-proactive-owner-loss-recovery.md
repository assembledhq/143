# Proactive Owner-Loss Recovery

> **Status:** Implemented | **Last reviewed:** 2026-05-28

When a user sends a follow-up while a session thread is already busy, 143 now treats the message as durable input first and then immediately checks whether the active runtime owner has been lost. This narrows the window where a user would otherwise need to manually retry while the periodic recovery loop waits to reclaim the job.

## Behavior

- Queue-only thread sends persist the user message and increment `pending_message_count` before any recovery work starts.
- After that durable write, the thread service calls a best-effort owner-loss orchestrator.
- The orchestrator runs targeted recovery for the affected session:
  - stale `session_executors` are marked `lost` and their jobs are requeued
  - stale inline worker-owned `run_agent` / `continue_session` jobs are requeued
  - `sessions.recovery_state` is set to `queued` with `runtime_stop_reason = 'worker_recovery'`
- The worker queue is woken when a job is requeued, so checkpoint recovery can start without waiting for the periodic cluster sweep.
- Errors on the proactive path are logged and do not roll back the queued user message; the normal recovery loop remains the fallback.

## User Contract

Follow-up input during owner-loss recovery is accepted and shown as queued. The UI surfaces “Restoring runtime from checkpoint” while `recovery_state` is `queued` or `recovering`, keeps the composer enabled, and disables manual checkpoint retry until the automatic recovery attempt finishes.

Recovery remains checkpoint-based. The platform does not promise live process migration; it restores from the latest durable checkpoint and then drains queued messages.
