# Sandbox Disk Guardrails

> **Status:** Implemented | **Last reviewed:** 2026-06-24

Production workers run untrusted, write-heavy coding sandboxes on the same Docker host as the worker service. Disk exhaustion is therefore a platform availability risk, not just a per-session failure. The long-term guardrail is layered: reclaim routine deploy churn, make sandbox ownership visible to the host, reconcile leaked containers continuously, tighten lifecycle accounting, and require real Docker storage quotas in production.

## Goals

- Keep worker and app Docker hosts from filling with old images, stopped containers, and build cache after normal deploys.
- Make every sandbox container discoverable without relying on image tags or DB rows that may be missing after a crash.
- Remove leaked sandbox containers that no session owns, while avoiding destruction of preview-held or turn-held containers.
- Keep billing and active-container accounting aligned with actual container lifetime.
- Fail worker startup when the configured sandbox disk limit is not enforceable.

## Deploy Pruning

`deploy/scripts/deploy.sh` prunes unused Docker artifacts only after a successful app or worker rollout and health check. The default window is `DOCKER_PRUNE_UNTIL=24h`, so the newly pulled image remains protected by the running service container while old unused images, stopped containers, and builder cache are reclaimed. Operators can disable this with `DEPLOY_DOCKER_PRUNE=0`.

Worker deploys can run detached during drain-and-rollover. In that path, the prune helper is embedded into the detached rollover script and runs after the replacement worker is healthy. The parent deploy process skips pruning for detached worker deploys so it cannot remove an image before the detached script has started the replacement container.

Docker volumes are not pruned by default because they may contain stateful preview infrastructure. Worker volume pruning requires `DEPLOY_DOCKER_VOLUME_PRUNE=1`.

## Startup and Pressure Cleanup

Before worker loops begin claiming jobs, the worker runs a Docker-first sandbox GC pass. Any managed sandbox container already present on the host but absent from `sessions.container_id` is destroyed immediately rather than waiting for the normal unreferenced grace period. A DB-unreferenced container that predates the current worker process cannot belong to an in-flight turn in that process, so it should not consume admission capacity.

When local sandbox admission finds `live + reserved >= WORKER_MAX_ACTIVE_SANDBOXES`, the capacity gate runs one best-effort pressure GC pass and then recounts before returning `sandbox capacity reached`. This pressure pass uses a short unreferenced-container grace (`2m` by default), a short admission-path timeout (`5s` by default), and a small destroy-attempt cap (`2` by default) so Docker-only leaks can clear within the job retry window without letting one admission attempt spend unbounded time cleaning the host. If the pass fails or capacity remains full, admission still fails closed.

## Sandbox Labels

Every Docker sandbox created by `DockerProvider` carries the worker capacity labels used by the container setup:

- `143.sandbox=true`
- `143.session_id`
- `143.org_id`
- `143.purpose`

It also carries provider-owned labels used by host-local GC:

- `com.assembledhq.143.managed=true`
- `com.assembledhq.143.type=sandbox`
- `com.assembledhq.143.session_id`
- `com.assembledhq.143.org_id`
- `com.assembledhq.143.purpose`
- `com.assembledhq.143.created_at`

The worker-local GC accepts both label schemes and also treats legacy unlabeled containers whose image name contains `143-sandbox` as managed sandboxes when they are attached to the configured sandbox network, excluding the `143-sandbox-dns` sidecar. The image fallback is intentionally backward-compatible with older production sandboxes that predate ownership labels; new cleanup logic should prefer labels for safety, but must keep this fallback until those hosts no longer carry legacy containers.

## Worker-Local Sandbox GC

Each worker starts `SandboxGC` when `SANDBOX_GC_INTERVAL` is positive. Defaults:

- `SANDBOX_GC_INTERVAL=5m`
- `SANDBOX_GC_GRACE=30m`
- `SANDBOX_GC_HARD_MAX=24h`

The GC is host-local. It lists labeled Docker sandbox containers on the current worker and compares them to all `sessions.container_id` values in Postgres.

Unreferenced containers older than the grace period are destroyed immediately. This covers the failure mode where a container was created but the DB row never recorded its ID, so startup orphan reconciliation cannot see it.

Referenced containers are only hard-expired after `SANDBOX_GC_HARD_MAX`, and only through `SessionStore.FinalizeContainerDestroy(orgID, sessionID, expectedContainerID)`. That CAS requires there to be no active turn holder or preview holder before the DB reference is cleared. If the CAS loses, the GC leaves the container running.

When the GC destroys a container, it closes any open `container_usage_events` rows for that Docker container ID with a GC-specific exit reason.

## Lifecycle Accounting

`RunAgent` records `ContainerStarted` only after `AcquireTurnHold` publishes DB ownership of the new container. This prevents crashes between Docker create and DB ownership from creating open usage rows for containers the session table does not reference. `ContinueSession` already followed this order.

## Enforced Disk Quotas

Production worker env files set `SANDBOX_REQUIRE_DISK_QUOTA=true`. When enabled, Docker health checks create a tiny quota probe container with `StorageOpt{"size":"1G"}`. Sandbox creation also fails instead of retrying without `StorageOpt` if Docker rejects the configured per-container disk limit.

This is intentionally fail-closed. Docker only enforces `StorageOpt.size` for compatible storage setups, typically `overlay2` over XFS with project quotas. Hosts using ext4 or XFS without project quotas must be reprovisioned before this flag is enabled. Local development defaults remain permissive with `SANDBOX_REQUIRE_DISK_QUOTA=false`.

## Tmpfs Scratch Paths

Sandbox containers mount `/tmp` as a 256 MiB noexec tmpfs and `/var/tmp` as a 512 MiB exec tmpfs. These mounts count against the container memory limit and are intentionally small; they are not expanded by raising `SANDBOX_DISK_LIMIT_GB`, which only controls root filesystem quota.

Language toolchains and package managers can write large build scratch trees, compiler inputs, or dependency caches under `TMPDIR`, `/tmp`, `/var/tmp`, or ecosystem-specific cache locations. Shared sandbox runtime guidance therefore tells coding agents to run build, test, lint, and verification commands with large scratch/cache paths pointed at rootfs-backed storage under `/home/sandbox` or the repository. Examples include common `TMPDIR`/`TEMP`/`TMP`, Go `GOTMPDIR`/`GOCACHE`, Node package-manager cache dirs, Python `PIP_CACHE_DIR`, and Rust `CARGO_HOME`/`CARGO_TARGET_DIR`. This guidance prevents misleading `no space left on device` failures from tmpfs paths while keeping the tmpfs memory budget small.

## Operational Recipe

1. Reprovision production workers so Docker's data root uses a storage backend that supports per-container size quotas.
2. Confirm the worker can create a container with `--storage-opt size=1G` using the production runtime.
3. Keep `SANDBOX_REQUIRE_DISK_QUOTA=true` on workers.
4. Keep `SANDBOX_DISK_LIMIT_GB` sized to the host's concurrency target.
5. Leave deploy pruning enabled with the 24 hour default. Lower `DOCKER_PRUNE_UNTIL` only if image churn again threatens host capacity.
6. Use `DEPLOY_DOCKER_PRUNE=0` only for short-lived rollback/debug sessions.
