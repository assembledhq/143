# Durable Session Executors

> **Status:** Enabled by default for worker-dispatched session turns | **Last reviewed:** 2026-05-20

Long-running `run_agent` and `continue_session` jobs should not be owned by the deployable worker process for the full turn. The durable executor design splits workers into short-lived dispatchers and per-session executor containers that own one active session turn until it completes, checkpoints, drains, or fails.

## Goals

- Routine worker deploys affect dispatch capacity, not already-accepted long-running sessions.
- The Postgres job `lock_token` remains the fencing primitive for lease renewal and terminal writes.
- Executors run on the existing worker Docker hosts, outside Docker Compose, so `docker compose up --force-recreate worker` does not recreate active executors.
- Recovery remains checkpoint-based. Executors prevent routine worker replacement from becoming a normal no-checkpoint failure source; they do not provide hot in-memory migration after host, Docker daemon, DB, sandbox, or agent process loss.

## Current Implementation

- `session_executors` records executor identity, session/thread/job ownership, host node, status, build/image metadata, heartbeat, lease expiry, and terminal state.
- `jobs.owner_kind` distinguishes `worker` from `session_executor` ownership. Reclaim/retry/terminal paths reset ownership back to `worker` when a job leaves active executor ownership.
- Worker handlers hand off `run_agent` and `continue_session` jobs when a durable executor dispatcher is configured.
- The handoff dispatcher creates an executor row, launches a Docker executor container, transfers the running job to `owner_kind=session_executor`, and preserves the existing `lock_token`. Launch and handoff failures mark the reserved executor failed; a launched container is force-removed if handoff does not land.
- Worker poll treats `HandoffError` as successful dispatch and skips terminal writes.
- `session-executor` is a first-class binary entrypoint that reuses the worker orchestrator dependencies without starting the API/router.
- Executor boot validates the executor row, running job state, `owner_kind=session_executor`, and matching lock token. It waits briefly for the dispatcher handoff to become visible because the container is launched before the handoff update.
- Executors heartbeat every 10s, renew the job lease every 20s, and fence terminal writes by the preserved lock token plus executor owner id. Final job writes use bounded contexts detached from process SIGTERM so graceful exits can persist terminal state.
- Executor SIGTERM marks the row `draining`, asks the orchestrator cancel registry for a graceful session interrupt, then relies on the existing graceful checkpoint path before the process exits.
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
