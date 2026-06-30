# Durable Session Executors

> **Status:** Implemented | **Last reviewed:** 2026-06-30

Long-running `run_agent` and `continue_session` jobs should not be owned by the deployable worker process for the full turn. The durable executor design splits workers into short-lived dispatchers and per-session executor containers that own one active session turn until it completes, checkpoints, drains, or fails.

## Goals

- Routine worker deploys affect dispatch capacity, not already-accepted long-running sessions.
- The Postgres job `lock_token` remains the fencing primitive for lease renewal and terminal writes.
- Executors run on the existing worker Docker hosts, outside Docker Compose, so `docker compose up --force-recreate worker` does not recreate active executors.
- Recovery remains checkpoint-based. Executors prevent routine worker replacement from becoming a normal no-checkpoint failure source; they do not provide hot in-memory migration after host, Docker daemon, DB, sandbox, or agent process loss.

## Current Implementation

- `session_executors` records executor identity, session/thread/job ownership, host node, Docker `container_id`, status, build/image metadata, heartbeat, lease expiry, and terminal state.
- `jobs.owner_kind` distinguishes `worker` from `session_executor` ownership. Reclaim/retry/terminal paths reset ownership back to `worker` when a job leaves active executor ownership.
- Worker handlers hand off `run_agent` and `continue_session` jobs when a durable executor dispatcher is configured.
- The handoff dispatcher creates an executor row, launches a Docker executor container, transfers the running job to `owner_kind=session_executor`, and preserves the existing `lock_token`. Launch and handoff failures mark the reserved executor failed; a launched container is force-removed if handoff does not land.
- The dispatcher and executor emit structured lifecycle breadcrumbs for row creation, Docker create/start/inspect state, host memory/load snapshots, container-id persistence, DB handoff, executor boot validation, mark-running, handler completion, and cleanup. These logs are the primary incident-response surface for distinguishing Docker launch failures, pre-handoff worker loss, child process boot failures, container OOM/exit, and node CPU or memory pressure.
- Worker poll treats `HandoffError` as successful dispatch and skips terminal writes.
- `session-executor` is a first-class binary entrypoint that reuses the worker orchestrator dependencies without starting the API/router.
- Session executors do not own sandbox GitHub auth socket listeners. They use signed worker RPC to acquire and release holder leases from the worker-owned sandbox auth broker, which keeps `/var/run/143/sandbox-auth/<session>/sock` alive until the final active holder releases.
- Worker startup may rehydrate auth listeners for containers it already owns, but it must not broadly sweep the shared auth socket directory. Rolling deploys intentionally overlap worker generations on the same host, so a new worker cannot prove that another generation's session directory is stale. Cleanup is owner-local: listener close removes its own socket, and the next `Listen` for the same session replaces only that session's socket path.
- Executor boot validates the executor row, running job state, `owner_kind=session_executor`, and matching lock token. It waits briefly for the dispatcher handoff to become visible because the container is launched before the handoff update.
- Executors heartbeat every 10s, renew the job lease every 20s, and fence terminal writes by the preserved lock token plus executor owner id. Final job writes use bounded contexts detached from process SIGTERM so graceful exits can persist terminal state.
- Executor SIGTERM marks the row `draining` and asks the orchestrator cancel registry for a typed `worker_drain` graceful stop. This path is explicitly not a user cancel: a drain interruption restores a retryable session status, records `runtime_stop_reason='worker_drain'`, snapshots the workspace when possible without advancing `current_turn`, and requeues the original job instead of closing it as succeeded or terminally marking the session `cancelled`.
- `run_agent` publishes a bootstrap checkpoint after sandbox creation, repo clone, working branch creation, auth bootstrap, and attachment materialization, before agent execution starts. Executor-owned first turns fail closed if bootstrap checkpoint metadata cannot be published; `turn_complete` and `graceful_stop` remain the primary recovery boundaries.
- A worker-owned retry clears any active pre-handoff executor reservation for the same session job before creating a new executor. This keeps a dispatcher crash or deploy between row creation and job handoff from burning retry attempts on the active-executor uniqueness constraint while waiting for the periodic recovery loop.
- Recovery marks stale handed-off executors `lost` and reclaims their jobs before the generic running-job reclaim path. Stale pre-handoff `starting` rows are marked `failed` so the active-executor uniqueness constraint cannot strand a session.
- Executor containers are attached to the configured worker Compose network, defaulting to `143_default`, so out-of-Compose executors can reach service-name dependencies.
- Worker deploys run a DB-backed guardrail before draining: active inline worker-owned no-checkpoint sessions block by default; executor-owned active sessions warn but do not block. `FORCE_DEPLOY_WITH_ACTIVE_SESSIONS=1` overrides the block.

## Follow-Up Hardening

- Add production dashboards for executor start failures, lease renewal failures, heartbeat gaps, lost executors, and bootstrap checkpoint failures before broad enablement.
- Keep inline execution available only when no dispatcher is wired, primarily for local/dev tests and break-glass service construction.

## Failure Model

This design removes or materially reduces worker deploys killing first-turn sessions before checkpoint, worker drains blocked by multi-hour turns, repeated no-checkpoint loops caused by deploys, and ambiguous ownership between worker/job/session during recovery.

It does not remove worker host loss, Docker daemon loss, executor crash, DB outage long enough to lose leases, sandbox container death, or agent CLI crash. Those continue to recover from the latest durable checkpoint.
