# Design: Session Preview Dependency Cache

> **Status:** Implemented | **Last reviewed:** 2026-05-30

## Summary

Session previews should be able to reuse expensive dependency and build-cache artifacts across cold starts without reusing another session's source workspace.

The platform already has two preview acceleration mechanisms:

- Live session sandbox reuse: fastest path, but only works while the session container is still running.
- Branch/PR preview startup snapshots: worker-local full-workspace snapshots keyed by commit, lockfiles, and preview config.

Session previews cannot safely use full-workspace snapshots from another preview because they must reflect unpushed agent edits. This design adds a default-on dependency artifact cache for `preview.install`. By default, 143 caches repo-declared `clean_paths` and a small set of conservative paths inferred from known dependency files. Repos can opt out or add extra cache paths such as package-manager stores or framework build caches.

## Goals

1. Reduce cold session preview boot time after a session snapshot has to be hydrated.
2. Preserve exact session workspace semantics: source files always come from the session snapshot, not from cache.
3. Make caching the default behavior for common repos, even when they have not perfectly declared `clean_paths`.
4. Keep the existing `preview.install` success marker as the correctness gate.
5. Keep worker-local storage and eviction behavior consistent with existing branch preview startup cache.
6. Surface enough logs and metrics to prove whether cache restore/save improves real startup latency.

## Non-Goals

1. Full session workspace caching.
2. Cross-region or cross-cloud blob replication.
3. Automatically detecting package-manager-specific cache paths beyond the conservative dependency-file inference listed below.
4. Caching runtime secret files or generated files that may contain secrets.
5. Guaranteeing package-manager correctness for repos that declare unsafe cache paths.
6. Adding a required user-facing HTTP control surface for cache management in the first implementation.

## Current Startup Path

For session previews, the durable `start_preview` job:

1. Reuses a live sandbox when `sessions.container_id` is alive.
2. Otherwise creates a new sandbox and hydrates the exact session snapshot.
3. Reads `.143/config.json` when the client did not send config.
4. Calls `Manager.LaunchPreview`.
5. `DockerPreviewProvider.StartPreview` provisions infrastructure, waits for health, runs init scripts, runs `preview.install`, writes runtime secret files, starts services, and waits for readiness.

Branch/PR previews additionally call `maybeRestoreBranchPreviewStartupCache` before launch and `createBranchPreviewStartupCache` after successful launch. Session previews intentionally do not use that full-workspace cache because they may include unpushed agent changes.

## Proposed Behavior

Session preview cold starts add a dependency-cache restore step between session snapshot hydration and `preview.install`.

The new flow:

1. Hydrate the exact session snapshot.
2. Resolve preview config.
3. Resolve the effective dependency cache policy:
   - Caching is enabled by default when `preview.install` has at least one `lockfiles` entry and at least one effective cache path.
   - Effective cache paths are `preview.install.clean_paths`, optional `preview.install.cache.paths`, and conservative paths inferred from known dependency files.
   - `preview.install.cache.enabled: false` disables restore and save.
4. If caching is enabled:
   - Compute a dependency cache key from install config, declared lockfiles, sandbox runtime/image, and effective cache paths.
   - Look up the shared L2 blob metadata by `(org_id, repo_id, cache_key)`. The DB metadata is authoritative for effective paths and checksum.
   - If metadata is found, check worker-local L1 by `cache_key` and use it when the checksum matches; otherwise stream the L2 blob from object storage into a bounded worker temp file.
   - Preflight the recorded compressed size, verify checksum, validate tar members against the stored effective paths on the worker, stage downloaded compressed blobs under the worker-local dependency cache staging directory instead of `/tmp`, stream extraction into the sandbox over stdin, populate worker-local L1 when configured, and upsert the worker's L1 location hint.
   - If both L1 and L2 miss, continue to the normal install path.
5. Run the existing `preview.install` flow:
   - Compute the existing install marker key.
   - If the dependency-cache restore did not fail and the marker and `verify_paths` are present, skip install.
   - Otherwise clean `clean_paths`, run `command`, then write the marker.
6. If install succeeds and caching is enabled, archive the effective cache paths once, write the archive to worker-local L1 when configured, upload it to shared L2 object storage, upsert the durable L2 metadata row, and upsert the worker's L1 location hint.
7. Start preview services and readiness as today.

This means cache restore is an accelerator, not the source of truth. The hydrated session snapshot remains authoritative for source files.

**Restore and marker interaction:** The restore step runs before the install marker check. When the session snapshot already contains the install marker and the restored artifacts satisfy `verify_paths`, the marker check passes and install is skipped entirely — this is the primary fast path. If dependency-cache restore fails for any reason, the marker is treated as untrusted for that launch and the normal `preview.install` command runs. This is conservative: some failures happen before sandbox mutation, but forcing install prevents a partially cleaned or partially extracted dependency tree from being accepted because an old marker still exists. When the marker is absent (e.g. a new session that has never completed install), effective cache paths that overlap with `clean_paths` will be wiped before install runs, making the prior restore wasteful for install commands such as `npm ci` that unconditionally reinstall. A future optimization may check for marker-file existence before deciding to restore and skip the restore when the marker is absent; the implementation restores unconditionally and accepts the I/O cost on first-ever cold starts, since the common case is returning to a session that has already run install.

### Cache-Key-Aware Scheduling

Shared object storage prevents cache correctness from depending on worker affinity, but it does not remove the latency cost of downloading large dependency blobs. At a fleet size of hundreds or thousands of workers, random scheduling makes worker-local L1 cache hits unlikely. Session preview scheduling should therefore prefer workers that are likely to already have the relevant dependency cache on local disk.

Use two keys:

- `cache_key`: exact restore key, computed after snapshot hydration from lockfile contents, install config, effective cache paths, sandbox provider, and image metadata.
- `placement_key`: approximate scheduler key, computed before worker selection from data the API already knows, such as `org_id`, `repo_id`, preview config name or digest, install command, install cwd, lockfile paths, and effective cache paths. It intentionally does not include lockfile contents because those may only be available after hydrating the session snapshot.

`placement_key` improves locality only. Restore correctness still depends on the exact `cache_key` computed inside the hydrated workspace. If scheduling picks a worker based on a stale or approximate placement key, the worst outcome is a local L1 miss followed by an object-storage restore or a cold install.

Worker selection order:

1. Existing live session sandbox worker, when the session container is still running.
2. Healthy workers with a recent local L1 cache location for the same `(org_id, repo_id, placement_key)`, subject to capacity and region constraints.
3. The top N workers from rendezvous hashing over `(org_id, repo_id, placement_key)` among healthy workers in the preferred region, subject to capacity. Start with N between 4 and 8 so load can spill while preserving locality.
4. Least-loaded healthy worker in the preferred region.
5. Cross-region fallback only when no preferred-region worker can accept the preview.

When config cannot be fully resolved before worker selection, compute a conservative repo-level placement key from `(org_id, repo_id, default preview config name if known)`. This still clusters repeated previews for the same repo better than random scheduling and remains safe because the exact restore key is verified later.

The scheduler should not block preview startup while querying cache locations. If the location lookup fails or times out, fall back to rendezvous hashing and then least-loaded scheduling.

## Repo Config API

### `.143/config.json`

Add an optional `cache` object under `preview.install`. The object is only needed to opt out or to add cache paths beyond `clean_paths`.

```json
{
  "preview": {
    "install": {
      "command": ["npm", "ci"],
      "lockfiles": ["package-lock.json"],
      "clean_paths": ["node_modules"],
      "verify_paths": ["node_modules/.bin/next"],
      "cache": {
        "paths": [".next/cache"]
      }
    }
  }
}
```

With this config, the effective cache paths are `node_modules` and `.next/cache`: `node_modules` comes from `clean_paths`, while `.next/cache` is additive.

To opt out:

```json
{
  "preview": {
    "install": {
      "command": ["npm", "ci"],
      "lockfiles": ["package-lock.json"],
      "clean_paths": ["node_modules"],
      "verify_paths": ["node_modules/.bin/next"],
      "cache": {
        "enabled": false
      }
    }
  }
}
```

### Field Contract

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `preview.install.cache` | object | no | Optional cache controls. Caching defaults on when `lockfiles` and effective cache paths are present. |
| `preview.install.cache.enabled` | boolean | no | Defaults to `true`. Set to `false` to disable dependency cache restore/save for this preview config. |
| `preview.install.cache.paths` | `string[]` | no | Additional repo-relative paths that 143 may restore before install and save after successful install. These are added to `clean_paths`. |

There is no `mode` field. The cache always means: "143 may persist and restore install output artifacts from `clean_paths`, known safe inferred paths, plus any extra `cache.paths` between session preview cold starts."

### Inferred Paths

`cache.paths` remains useful for package-manager stores or framework caches, but common JavaScript, Python, and Go repos should get useful caching even if they forgot to declare `clean_paths`.

Initial inferred path rules:

| Dependency file | Inferred cache path |
|-----------------|---------------------|
| `package-lock.json` | `node_modules` |
| `npm-shrinkwrap.json` | `node_modules` |
| `pnpm-lock.yaml` | `node_modules` |
| `yarn.lock` | `node_modules` |
| `bun.lock` | `node_modules` |
| `bun.lockb` | `node_modules` |
| `poetry.lock` | `.venv` |
| `uv.lock` | `.venv` |
| `Pipfile.lock` | `.venv` |
| `pdm.lock` | `.venv` |
| `requirements.txt` | `.venv` |
| `requirements-dev.txt` | `.venv` |
| `go.mod` | `vendor` |
| `go.sum` | `vendor` |

> **`requirements.txt` warning:** Unlike `poetry.lock`, `uv.lock`, and other generated lock files, `requirements.txt` may contain unpinned version specifiers (e.g. `flask>=2.0`). Two installs against the same `requirements.txt` content at different times can produce different package versions, so a restored `.venv` may be stale even when the file hash has not changed. Repos using unpinned `requirements.txt` should either pin all dependencies, switch to a lock-file–based tool, or set `cache.enabled: false`. The docs must surface this warning prominently.

Inference rules:

- Only infer paths for dependency files explicitly listed in `preview.install.lockfiles`.
- Infer paths in the same directory as the dependency file. For example, `frontend/package-lock.json` infers `frontend/node_modules`, and `services/api/poetry.lock` infers `services/api/.venv`.
- JavaScript inference only covers local `node_modules`.
- Python inference only covers local virtual environments named `.venv`; repos using `venv`, `env`, or tool-specific cache directories should declare those through `cache.paths`.
- Go inference only covers `vendor`. Go's module cache and build cache normally live outside the repo, so repos that want to cache repo-local Go caches should set `GOMODCACHE`/`GOCACHE` to repo-relative paths in their install/service commands and declare those paths in `cache.paths`. The `vendor` inference is a no-op when the project does not use vendor mode — if `vendor/` is absent after install, save is skipped for that path, which is correct.
- Do not infer package-manager global stores such as `.pnpm-store`, `.yarn/cache`, pip caches, Go module caches, or Go build caches; those require explicit `cache.paths`.
- Do not infer additional language ecosystems until there is a clearly safe convention and tests for it.
- De-duplicate inferred paths with `clean_paths` and explicit `cache.paths`.

### Validation

Validation should live with the existing preview config validation in `internal/services/preview/config.go`.

Rules:

- `preview.install.cache.enabled` defaults to true when omitted.
- If `cache.paths` is non-empty, `preview.install.lockfiles` must be non-empty so cache invalidation has a stable input.
- If no effective cache paths can be resolved, caching is disabled for that launch without failing preview config validation.
- Effective paths are `clean_paths + cache.paths + inferred paths from known dependency files`.
- Each effective cache path must be repo-relative.
- Absolute paths are rejected.
- `..` traversal is rejected.
- `.git` and any path below `.git` are rejected.
- `.143/cache` and descendants are rejected so restored cache blobs cannot include platform-owned preview state or forge install markers.
- Empty paths and `.` are rejected because they are too broad.
- Shell metacharacters should be rejected using the same conservative character policy as `clean_paths`, unless path handling is fully tar-list based and never shell-interpolated.

> **Mutable image tag warning:** The cache key includes sandbox image metadata. If a repo's preview config references a mutable image tag (e.g. `image: myapp:latest`) that is rebuilt and repushed, the cache key will not change and a stale dependency cache may be used. The docs must warn that immutable digests or versioned tags are strongly preferred, and that repos on rolling tags should set `cache.enabled: false` or accept the risk of stale installs.

### Named Config Merge Semantics

Preview configs support named configs (per-branch or per-session overrides). The `cache` sub-object must be merged by field, not replaced wholesale:

- If a named config omits `cache` entirely, it inherits the base config's `cache` settings.
- If a named config sets `cache.enabled: false`, that overrides the base config's `enabled` value; `cache.paths` from the base config is still visible but irrelevant since caching is disabled.
- If a named config sets only `cache.paths`, the base config's `enabled` value is inherited.
- If a named config sets only `cache.enabled: true`, the base config's `paths` are inherited.

These rules must be implemented in the config merging logic and covered by table-driven tests alongside the existing named-config merge tests.

### Documentation Updates

Update:

- `docs/guides/previews.md`
- `docs/guides/repo-config.md`
- `docs/public/guides/previews.mdx`
- `docs/public/reference/preview-config.mdx`

The docs must make clear that dependency caching is default-on for `clean_paths` and inferred dependency-file paths, that `cache.paths` is additive, and that `cache.enabled: false` opts out.

### Public Fumadocs Requirements

The public Fumadocs content must be updated in the same change as the implementation so users understand why preview startup behavior changed. Update both the guide and field reference:

- `docs/public/guides/previews.mdx`
- `docs/public/reference/preview-config.mdx`

Include:

- a short explanation that dependency caching is enabled by default for `preview.install`,
- the effective path formula: `clean_paths + cache.paths + inferred paths from known dependency files`,
- the initial inferred dependency-file table,
- an example with no `cache` object where `package-lock.json` infers `node_modules`,
- examples where `poetry.lock` infers `.venv` and `go.mod` infers `vendor`,
- an example using `cache.paths` for additive caches such as `.next/cache`, `.pnpm-store`, or `.turbo/cache`,
- an opt-out example using `cache.enabled: false`,
- warnings not to cache source directories, secret files, `.git`, or `.143/cache/preview-install`,
- troubleshooting guidance for stale dependencies: change lockfiles, clear the preview cache when an admin API exists, or temporarily opt out with `cache.enabled: false`.

## HTTP API Contract

No required public HTTP route changes for the first implementation.

Existing start/restart routes continue to accept the same preview config payloads:

- `POST /api/v1/sessions/{id}/preview`
- `POST /api/v1/sessions/{id}/preview/ensure`
- `POST /api/v1/sessions/{id}/preview/restart`
- branch preview start routes that already carry parsed preview config

Because the config JSON may now include `preview.install.cache`, these routes indirectly accept the new field through `models.PreviewConfig`.

### Response Shape

No required response shape change.

Preview status still returns `preview_instances`, services, infrastructure, logs, and snapshots as today.

### Error Codes

No new user-facing error code is required in v1.

Cache restore/save failures should not fail preview startup. They should:

- write preview logs at warning level,
- emit structured worker logs,
- record metrics,
- continue with a normal cold install path.

Existing install failures still surface as `PREVIEW_INSTALL_FAILED` because only the actual install command is correctness-critical.

### Optional Future Admin API

Not part of the first implementation, but the schema should not preclude:

- `GET /api/v1/repos/{id}/preview/cache`
- `DELETE /api/v1/repos/{id}/preview/cache`

These would require admin or member-level repo/settings permission and org scoping. They are intentionally deferred until operators need manual cache inspection or purge.

## Internal API Contract

### Models

Add to `internal/models/preview.go`:

```go
type PreviewInstallConfig struct {
    Command        []string                   `json:"command"`
    Cwd            string                     `json:"cwd,omitempty"`
    Lockfiles      []string                   `json:"lockfiles,omitempty"`
    CleanPaths     []string                   `json:"clean_paths,omitempty"`
    VerifyPaths    []string                   `json:"verify_paths,omitempty"`
    TimeoutSeconds int                        `json:"timeout_seconds,omitempty"`
    Cache          *PreviewInstallCacheConfig `json:"cache,omitempty"`
}

type PreviewInstallCacheConfig struct {
    Enabled *bool    `json:"enabled,omitempty"`
    Paths   []string `json:"paths,omitempty"`
}
```

Add table-driven validation tests for:

- default-on cache from `clean_paths`,
- default-on cache from inferred JavaScript dependency files,
- default-on cache from inferred Python dependency files,
- default-on cache from inferred Go dependency files,
- additive cache paths,
- explicit opt-out,
- valid single cache path,
- multiple valid cache paths,
- missing lockfiles,
- no effective cache paths disables cache without invalidating config,
- absolute path,
- path traversal,
- `.git`,
- `.143/cache/preview-install`,
- broad `.` path.

### Dependency Cache Service

Add a service in `internal/services/preview`, separate from `SnapshotCache` because the artifact shape and safety rules differ from full workspace snapshots.

```go
type DependencyCache interface {
    Find(ctx context.Context, orgID, repoID uuid.UUID, cacheKey string) (*DependencyCacheHit, error)
    // Restore reads EffectivePaths from hit.Entry.Metadata so the caller cannot
    // pass a mismatched path list. It removes those paths from the sandbox before
    // extracting so restore is idempotent.
    Restore(ctx context.Context, sb *agent.Sandbox, hit *DependencyCacheHit) error
    Save(ctx context.Context, sb *agent.Sandbox, cacheKey string, paths []string, metadata DependencyCacheMetadata) (DependencyCacheSaveResult, error)
}

type DependencyCacheSaveResult struct {
    SizeBytes int64
}

type DependencyCacheHit struct {
    Entry   models.PreviewDependencyCache
    BlobKey string // object storage key, derived from Entry.BlobKey
}

type DependencyCacheMetadata struct {
    OrgID               uuid.UUID         `json:"org_id"`
    RepoID              uuid.UUID         `json:"repo_id"`
    SessionID           uuid.UUID         `json:"session_id"`
    InstallCommand      []string          `json:"install_command"`
    EffectivePaths      []string          `json:"effective_paths"`
    LockfileHashes      map[string]string `json:"lockfile_hashes"`
    ChecksumSHA256      string            `json:"checksum_sha256"`
    ArchiveBytes        int64             `json:"archive_bytes,omitempty"`
    ArchivePayloadBytes int64             `json:"archive_payload_bytes,omitempty"`
    ArchiveFileCount    int64             `json:"archive_file_count,omitempty"`
}
```

`DependencyCacheMetadata.EffectivePaths` is the authoritative list of paths in the blob. `Restore` reads this field from `hit.Entry.Metadata` rather than accepting a separate `paths` argument. This eliminates the class of bugs where the caller passes a different path list than what was saved, and makes the restore self-contained. `LockfileHashes` is stored for debugging stale-cache incidents without requiring a session config lookup. Archive byte and file-count metadata is for operations/debugging and future cache admission policy; correctness still comes from checksum and member validation.

The concrete implementation should use the same executor shape as `SnapshotCache`:

- `Exec`
- `ReadFile`
- `WriteFile`
- `ExecWithStdin` for restore extraction of large archives without sandbox temp-file staging

### Cache Key

Compute a SHA-256 key from a stable JSON payload:

```go
type previewDependencyCacheKey struct {
    RuntimeVersion  string
    SandboxProvider string
    SandboxImage    string
    InstallCommand  []string
    InstallCwd      string
    Lockfiles       []previewInstallLockfileKey
    EffectivePaths  []string
}
```

Inputs:

- a new runtime version string, e.g. `preview-dependency-cache-v1` (provider-agnostic; the provider is already captured by `SandboxProvider`),
- sandbox provider,
- sandbox image metadata when available,
- install command,
- install cwd,
- sorted lockfile paths and SHA-256 contents,
- sorted effective cache paths.

Do not include raw `clean_paths` separately from `EffectivePaths`, and do not include `verify_paths` or `timeout_seconds` unless there is a concrete correctness reason. Those fields affect install execution policy, not the reusable dependency artifact identity.

### Placement Key

Compute a separate SHA-256 placement key from a stable JSON payload:

```go
type previewDependencyCachePlacementKey struct {
    RuntimeVersion string
    OrgID          uuid.UUID
    RepoID         uuid.UUID
    ConfigName     string
    ConfigDigest   string
    InstallCommand []string
    InstallCwd     string
    LockfilePaths  []string
    EffectivePaths []string
}
```

Inputs should be sorted and normalized in the same way as `cache_key` inputs, but lockfile contents are intentionally excluded. `ConfigDigest` should be a digest of the preview config known to the API, when available. If the client did not send config and the API cannot read repo config before worker assignment, use a repo-level placement key and let the worker compute the exact `cache_key` after hydration.

The placement key is not an invalidation mechanism. It is only a scheduling hint used to raise local L1 cache hit rate.

### Provider Integration

`DockerPreviewProvider.runPreviewInstall` currently owns install marker lookup and command execution. It should gain access to a dependency cache dependency, or the manager/start runner should wrap install with cache restore/save.

Preferred shape:

- Add `DependencyCache` to `DockerPreviewProvider`.
- Add `orgID`, `repoID`, and `sessionID` launch metadata to the provider call path.

The current provider interface is:

```go
StartPreview(ctx context.Context, sb *agent.Sandbox, cfg *models.PreviewConfig, extraEnv map[string]string, observer ServiceObserver) (*PreviewHandle, error)
```

Change it to:

```go
type StartPreviewOptions struct {
    OrgID        uuid.UUID
    RepositoryID uuid.UUID
    SessionID    uuid.UUID
    ConfigDigest string
    ExtraEnv     map[string]string
}

StartPreview(ctx context.Context, sb *agent.Sandbox, cfg *models.PreviewConfig, opts StartPreviewOptions, observer ServiceObserver) (*PreviewHandle, error)
```

Rationale:

- Dependency cache needs org/repo/session identity.
- Future preview provider options should not grow as positional parameters.
- `ExtraEnv` remains available without widening every provider call again.

This is an internal Go API change, not a public HTTP change. Update all provider implementations and tests.

### Service Observer

Extend `ServiceObserver` with optional cache events by interface assertion, mirroring phase observer behavior:

```go
type CacheObserver interface {
    OnDependencyCacheRestore(status string, cacheKey string, sizeBytes int64, err error)
    OnDependencyCacheSave(status string, cacheKey string, sizeBytes int64, err error)
}
```

Statuses for `OnDependencyCacheRestore` (called exactly once per restore attempt):

- `disabled` — cache is disabled for this preview config; `sizeBytes` is 0
- `miss` — no matching blob found; cold install follows; `sizeBytes` is 0
- `restored` — blob found, checksum verified, extraction complete; `sizeBytes` is the compressed blob size
- `restore_failed` — blob found but checksum or extraction failed; cold install follows; `err` is non-nil

Statuses for `OnDependencyCacheSave` (called exactly once per save attempt):

- `saved` — archive written and DB record upserted; `sizeBytes` is the compressed blob size
- `skipped` — no effective paths exist to archive, or cache is disabled; `sizeBytes` is 0
- `save_failed` — error during archive or upsert; preview remains ready; `err` is non-nil

`hit` is an internal intermediate state (blob found in DB, not yet extracted) and is not surfaced to the observer. Using `restored` as the single terminal success status keeps observer implementations simple and avoids double-firing.

`sizeBytes` always reflects the compressed on-disk size, which is the unit used for LRU disk-budget accounting.

The manager observer should persist concise preview logs for restore/save failures. Cache save success can remain structured worker log + metric only unless needed in UI.

## Database Schema

Add new org-scoped tables. Do not reuse `preview_startup_cache`; that table represents full-workspace snapshots and has different safety semantics.

### Metadata Store Choice: Postgres vs Redis

There are two different metadata categories:

- Durable L2 blob index: "Does an object-storage blob exist for this exact `(org_id, repo_id, cache_key)`, and what metadata/checksum/effective paths belong to it?"
- Ephemeral L1 location hints: "Which workers recently had a local disk copy for this `(org_id, repo_id, placement_key)`?"

Recommendation: keep both in Postgres for the first implementation, with object storage as the source of blob bytes. Revisit Redis only if scheduler lookup/write volume becomes a measured bottleneck.

Postgres advantages:

- It is already the system-of-record database and fits the existing store/test/tenancy patterns.
- Durable L2 metadata should survive Redis eviction, deploys, and cache-node restarts; losing it would make existing S3 blobs undiscoverable until rebuilt.
- Org scoping, auditability, migrations, and cleanup jobs are straightforward.
- LRU and retention queries are simple SQL and can be kept consistent with S3 lifecycle cleanup.
- It avoids adding Redis as another required dependency for preview caching.

Postgres downsides:

- `last_used_at` touches and L1 location upserts add write traffic.
- Scheduler lookups add a DB read on preview start.
- Location rows are ephemeral, so storing them durably is more persistence than they strictly need.

Mitigations:

- Throttle `last_used_at` updates so a cache hit only touches the row if the existing timestamp is older than a small interval, e.g. 5-15 minutes.
- Bound scheduler location lookups with a small `LIMIT`, short timeout, and fallback to rendezvous hashing.
- Treat stale location rows as harmless hints and clean them with a periodic TTL job.

Redis advantages:

- Fast TTL-native storage is a natural fit for L1 location hints.
- It can absorb high-frequency worker heartbeat/location writes without increasing Postgres write load.
- Key expiry automatically handles dead workers and local disk eviction if workers refresh locations periodically.

Redis downsides:

- It is not a good source of truth for L2 metadata unless Redis persistence/backup is made operationally critical.
- Eviction or restart would make S3 blobs undiscoverable if durable metadata lived only in Redis.
- Multi-dimensional lookups such as `(org_id, repo_id, placement_key)` plus LRU/retention cleanup require custom key/index maintenance.
- It adds another production dependency and another failure mode to preview startup.

If Redis is introduced later, use it as an optional accelerator for `preview_dependency_cache_locations` only: workers write L1 location hints to Redis with TTL, the scheduler reads Redis first, and Postgres remains the durable L2 blob index. Do not move `preview_dependency_cache` to Redis unless the product is comfortable treating all dependency-cache metadata as disposable and letting S3 lifecycle cleanup orphaned blobs.

```sql
CREATE TABLE preview_dependency_cache (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repo_id       uuid        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    cache_key     text        NOT NULL,
    placement_key text        NOT NULL DEFAULT '',
    blob_key      text        NOT NULL DEFAULT '', -- object storage key, e.g. preview-dependency-cache/{org}/{repo}/{key}/{checksum}.tar.gz
    size_bytes    bigint      NOT NULL DEFAULT 0,
    metadata      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    last_used_at  timestamptz NOT NULL DEFAULT now(),
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- One blob per (org, repo, cache_key) — any worker can restore it.
CREATE UNIQUE INDEX idx_preview_dependency_cache_lookup
    ON preview_dependency_cache (org_id, repo_id, cache_key);

-- LRU cleanup scoped to a repo; used by the background eviction job.
CREATE INDEX idx_preview_dependency_cache_org_repo_lru
    ON preview_dependency_cache (org_id, repo_id, last_used_at);

-- Scheduler hint for finding likely cache holders by approximate key.
CREATE INDEX idx_preview_dependency_cache_placement
    ON preview_dependency_cache (org_id, repo_id, placement_key, last_used_at DESC);

CREATE TABLE preview_dependency_cache_locations (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    repo_id        uuid        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    cache_key      text        NOT NULL,
    placement_key  text        NOT NULL DEFAULT '',
    worker_node_id text        NOT NULL,
    size_bytes     bigint      NOT NULL DEFAULT 0,
    last_used_at   timestamptz NOT NULL DEFAULT now(),
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- One local L1 location per worker for each exact blob.
CREATE UNIQUE INDEX idx_preview_dependency_cache_locations_unique
    ON preview_dependency_cache_locations (org_id, repo_id, cache_key, worker_node_id);

-- Scheduler lookup for workers likely to have a local L1 cache for this placement key.
CREATE INDEX idx_preview_dependency_cache_locations_placement
    ON preview_dependency_cache_locations (org_id, repo_id, placement_key, last_used_at DESC);

-- Worker cleanup when a node is drained or its local cache is wiped.
CREATE INDEX idx_preview_dependency_cache_locations_worker
    ON preview_dependency_cache_locations (worker_node_id, last_used_at);
```

Tenancy:

- Every query must filter by `org_id`.
- Store methods should take `orgID uuid.UUID` as the first argument after `ctx`.

Suggested model:

```go
type PreviewDependencyCache struct {
    ID           uuid.UUID       `db:"id" json:"id"`
    OrgID        uuid.UUID       `db:"org_id" json:"org_id"`
    RepoID       uuid.UUID       `db:"repo_id" json:"repo_id"`
    CacheKey     string          `db:"cache_key" json:"cache_key"`
    PlacementKey string         `db:"placement_key" json:"placement_key"`
    BlobKey      string          `db:"blob_key" json:"-"` // excluded from JSON: internal object-storage key
    SizeBytes    int64           `db:"size_bytes" json:"size_bytes"`
    Metadata     json.RawMessage `db:"metadata" json:"metadata"`
    LastUsedAt   time.Time       `db:"last_used_at" json:"last_used_at"`
    CreatedAt    time.Time       `db:"created_at" json:"created_at"`
}

type PreviewDependencyCacheLocation struct {
    ID           uuid.UUID `db:"id" json:"id"`
    OrgID        uuid.UUID `db:"org_id" json:"org_id"`
    RepoID       uuid.UUID `db:"repo_id" json:"repo_id"`
    CacheKey     string    `db:"cache_key" json:"cache_key"`
    PlacementKey string    `db:"placement_key" json:"placement_key"`
    WorkerNodeID string    `db:"worker_node_id" json:"worker_node_id"`
    SizeBytes    int64     `db:"size_bytes" json:"size_bytes"`
    LastUsedAt   time.Time `db:"last_used_at" json:"last_used_at"`
    CreatedAt    time.Time `db:"created_at" json:"created_at"`
}
```

Suggested store methods:

```go
func (s *PreviewStore) FindDependencyCache(ctx context.Context, orgID, repoID uuid.UUID, cacheKey string) (*models.PreviewDependencyCache, error)
func (s *PreviewStore) UpsertDependencyCache(ctx context.Context, entry *models.PreviewDependencyCache) error
func (s *PreviewStore) TouchDependencyCache(ctx context.Context, orgID, id uuid.UUID) error
func (s *PreviewStore) DeleteDependencyCache(ctx context.Context, orgID, id uuid.UUID) error
// ListDependencyCacheLRU returns the oldest entries for a repo beyond the retention limit,
// used by the background eviction job to identify blobs to delete from object storage.
func (s *PreviewStore) ListDependencyCacheLRU(ctx context.Context, orgID, repoID uuid.UUID, keepNewest int) ([]models.PreviewDependencyCache, error)
func (s *PreviewStore) ListExpiredDependencyCaches(ctx context.Context, cutoff time.Time, limit int) ([]models.PreviewDependencyCache, error)
func (s *PreviewStore) ListDependencyCachesOverLimit(ctx context.Context, keepNewestPerRepo, limit int) ([]models.PreviewDependencyCache, error)
func (s *PreviewStore) UpsertDependencyCacheLocation(ctx context.Context, location *models.PreviewDependencyCacheLocation) error
func (s *PreviewStore) ListDependencyCacheWorkersByPlacement(ctx context.Context, orgID, repoID uuid.UUID, placementKey string, limit int) ([]models.PreviewDependencyCacheLocation, error)
func (s *PreviewStore) DeleteDependencyCacheLocation(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error
func (s *PreviewStore) DeleteExpiredDependencyCacheLocations(ctx context.Context, cutoff time.Time) (int64, error)
func (s *PreviewStore) DeleteDependencyCacheLocationByWorkerCacheKey(ctx context.Context, workerNodeID, cacheKey string) error
func (s *PreviewStore) DeleteDependencyCacheLocationsForWorker(ctx context.Context, workerNodeID string) error
```

`ListDependencyCacheLRU` is always org-scoped and does not require a lint exception. Cross-org cleanup methods are limited to background cleanup or worker-local hint deletion, and carry narrow lint exceptions because they do not expose tenant data.

## Blob Storage

> **Why shared object storage, not worker-local disk:** With a worker fleet in the thousands and random session scheduling, a worker-local cache would have a near-zero hit rate because a session's next cold start is statistically unlikely to land on the same worker that saved the blob. Shared object storage (S3-compatible) gives every worker access to every blob, making the hit rate depend only on lockfile stability, not on scheduling luck.

Blobs are stored in a shared S3-compatible bucket. The object key encodes enough context for human inspection and future prefix-based access policies:

```text
preview-dependency-cache/{org_id}/{repo_id}/{cache_key}/{checksum_sha256}.tar.gz
```

Add config:

```go
PreviewDependencyCacheBucket string `env:"PREVIEW_DEPENDENCY_CACHE_BUCKET" envDefault:""`
PreviewDependencyCachePrefix string `env:"PREVIEW_DEPENDENCY_CACHE_PREFIX" envDefault:"preview-dependency-cache"`
PreviewDependencyCacheS3Region string `env:"PREVIEW_DEPENDENCY_CACHE_S3_REGION" envDefault:""`
PreviewDependencyCacheS3Endpoint string `env:"PREVIEW_DEPENDENCY_CACHE_S3_ENDPOINT" envDefault:""`
PreviewDependencyCacheS3UsePathStyle bool `env:"PREVIEW_DEPENDENCY_CACHE_S3_USE_PATH_STYLE" envDefault:"false"`
PreviewDependencyCacheRetentionDays int `env:"PREVIEW_DEPENDENCY_CACHE_RETENTION_DAYS" envDefault:"30"`
PreviewDependencyCacheCleanupInterval time.Duration `env:"PREVIEW_DEPENDENCY_CACHE_CLEANUP_INTERVAL" envDefault:"1h"`
PreviewDependencyCacheKeepNewestPerRepo int `env:"PREVIEW_DEPENDENCY_CACHE_KEEP_NEWEST_PER_REPO" envDefault:"50"`
```

When `PREVIEW_DEPENDENCY_CACHE_BUCKET` is empty, dependency caching is disabled regardless of per-repo config. This is the safe default for environments that have not provisioned the bucket.
When `PREVIEW_DEPENDENCY_CACHE_BUCKET` is set, dependency caching is enabled by default; the former `PREVIEW_DEPENDENCY_CACHE_ENABLED` flag is obsolete and should not be required for new deployments.

### Worker-local download cache (default L1)

After streaming a blob from object storage, the worker may cache it on local disk so that repeated cold starts for the same repo on the same worker do not re-download the blob. This is a pure latency optimization and is not required for correctness or global cache availability.

```go
PreviewDependencyCacheLocalDir      string `env:"PREVIEW_DEPENDENCY_CACHE_LOCAL_DIR" envDefault:"/var/cache/143/preview-dependency-cache"`
PreviewDependencyCacheLocalMaxBytes int64  `env:"PREVIEW_DEPENDENCY_CACHE_LOCAL_MAX_BYTES" envDefault:"10737418240"`
```

When `PREVIEW_DEPENDENCY_CACHE_LOCAL_DIR` is left at its default, workers keep a bounded local L1 download cache at `/var/cache/143/preview-dependency-cache` and use a `.staging` directory beneath it for remote dependency-cache downloads and save archives. Production worker compose bind-mounts that host path into the worker container, and worker provisioning/cloud-init creates it as `1000:1000` with mode `0750`, so L1 blobs survive worker container recreation. This keeps large compressed blobs off small system tmpfs mounts and keeps DB cache-location hints useful across routine deploys. Operators may still set the env var to an explicit worker-local disk path if they also provide a matching compose bind mount. To disable local L1 while keeping shared L2 cache enabled, set `PREVIEW_DEPENDENCY_CACHE_LOCAL_DIR=off` (also accepts `none` or `disabled`); in that mode every restore streams through a process temp staging directory and no local location hints are written.

When local L1 is configured, successful restores and saves upsert `preview_dependency_cache_locations` with the worker's stable node ID. Local L1 eviction removes the oldest blobs when the worker-local byte budget is exceeded and best-effort deletes the matching location rows. Stale location rows are acceptable because they only affect scheduling preference; the worker still verifies local file existence before restore and falls back to object storage.

### Save

Save should archive only effective cache paths that exist. The archive is streamed out of the sandbox directly to a bounded worker temp file instead of first writing an unbounded sandbox temp archive:

```sh
cd "$WORKDIR" && tar czf - -- <path1> <path2> ...
```

The implementation must avoid shell injection by validating paths and shell-escaping every argument.

Upload the archive to a checksum-addressed object storage key with a SHA-256 content hash stored as object metadata or as a companion `.sha256` object. After a successful upload, upsert the DB record with that exact blob key.

**Concurrent saves:** If multiple sessions for the same repo start simultaneously and all miss the cache, they may each upload a blob for the same cache key concurrently. Blob objects are checksum-addressed, so every DB upsert points at the exact object whose checksum is recorded in metadata. Last DB writer wins for discoverability, but it cannot pair one writer's checksum with another writer's object.

If no effective cache paths exist after install, skip save and log `dependency cache save skipped: no effective paths exist`.

### Restore

Restore should:

1. Look up the DB record for `(org_id, repo_id, cache_key)`.
2. Use the DB metadata as the source of truth for effective paths and checksum. If local L1 is configured and has the blob for this `cache_key`, stage that local blob first; otherwise stream the blob from object storage to a bounded worker temp file.
3. Reject blobs whose recorded compressed size exceeds the restore cap before object-store download.
4. Verify checksum against stored hash.
5. Validate the gzip tar on the worker before sandbox mutation. Every member must be relative, must not traverse with `..`, must not target preview install markers, and must be contained by one of the stored effective paths.
6. Remove only the effective cache paths listed in `hit.Entry.Metadata.EffectivePaths` from the sandbox.
7. Stream the tarball into `tar xzf - -C <workdir>` so restore does not create an extra compressed archive under sandbox `/tmp`.
8. Touch the DB `last_used_at` entry.

On checksum mismatch or object-not-found, delete the DB record and fall through to a cold install. Other restore failures do not delete durable metadata unless they prove the blob is corrupt, but they still force the normal install path for that launch.

Do not extract absolute paths or parent traversal entries. Validate tar entries during save and again during restore. Effective paths come from the blob's stored metadata, not from the caller, so there is no risk of a path-list mismatch between save and restore.

### Eviction

Object storage lifecycle rules (e.g. S3 Object Lifecycle Policies) should expire blobs after a configurable number of days (default: 30). A background DB cleanup job periodically deletes DB records whose `last_used_at` is older than the same retention window, explicitly deletes corresponding objects if lifecycle rules are not configured, enforces `PREVIEW_DEPENDENCY_CACHE_KEEP_NEWEST_PER_REPO` with a cross-repo LRU scan, and deletes stale worker-local location hints older than the same retention window.

## Security and Secret Handling

1. Runtime secret files are written after `preview.install` today. Keep that order so dependency cache save cannot include runtime secret files.
2. Reject `.143/cache` and descendants in all dependency-cache paths so cache restore cannot persist platform-owned preview state or forge install success markers. Broad `clean_paths` may still be used for fresh installs, but unsafe paths are excluded from dependency artifact caching.
3. Reject `.git` to avoid credential remnants and repository metadata corruption.
4. Cache blobs are stored in shared object storage. Access must be restricted to the service's IAM role or equivalent; the bucket must not be public. If a worker-local L1 cache is used, those files should be `0600` and the directory `0750`.
5. Cache metadata must not include secret values, env dumps, install output, or file contents.
6. Cache restore/save failures are non-fatal so corrupted blobs degrade to cold starts.

## Observability

Add OpenTelemetry metrics:

- `preview.session.dependency_cache.restore_duration` histogram, seconds
- `preview.session.dependency_cache.save_duration` histogram, seconds
- `preview.session.dependency_cache.restores` counter with `result=disabled|restored|miss|restore_failed`
- `preview.session.dependency_cache.saves` counter with `result=saved|skipped|save_failed`
- `preview.session.dependency_cache.scheduler_decisions` counter with `decision=live_session|local_cache_holder|rendezvous|least_loaded|cross_region|fallback_error`
- `preview.session.phase_duration` histogram with `phase=hydrate|config|dependency_cache_restore|install_build|start_services|readiness`

**Metrics cardinality:** Do not use `repo_id` as a metric dimension. In a multi-tenant system with many repos, per-repo cardinality will exceed most metric backend limits. Use `org_id` as the finest-grained dimension for counters and histograms, and rely on preview logs (which include `repo_id` as a structured field) for per-repo debugging. Existing branch preview phase metrics should remain branch-specific and follow the same cardinality constraint.

Preview logs:

- cache miss: optional debug-level worker log only
- cache restored: preview info log with size and elapsed
- restore failed: preview warning log, continue cold
- save failed: preview warning log, preview remains ready

## Rollout Plan

1. Ship schema and store methods.
2. Ship config parser/model support while cache implementation is disabled; configs with `cache.enabled` and `cache.paths` should start validating successfully.
3. Ship dependency cache service and public Fumadocs with bucket-presence enablement: environments that set `PREVIEW_DEPENDENCY_CACHE_BUCKET` get dependency caching automatically, while environments without a bucket remain disabled.
4. Enable in development and one internal repo. Verify metrics, logs, S3 uploads, and eviction job behavior.

Repo config can opt out per preview config. Repos with `preview.install.lockfiles` and either `clean_paths`, `cache.paths`, or inferred JavaScript/Python/Go paths get caching by default whenever the deployment has an L2 cache bucket configured.

## Testing Plan

Backend tests:

- Config validation table tests for `preview.install.cache`.
- Effective cache path resolution tests:
  - defaults to `clean_paths`,
  - infers `node_modules` from JavaScript dependency files,
  - infers nested `frontend/node_modules` from `frontend/package-lock.json`,
  - infers `.venv` from Python dependency files,
  - infers nested `services/api/.venv` from `services/api/poetry.lock`,
  - infers `vendor` from Go dependency files,
  - adds `cache.paths`,
  - de-duplicates repeated paths,
  - disables with `cache.enabled: false`,
  - returns disabled/no-op when there are no effective paths.
- Cache key determinism tests:
  - path order does not matter,
  - lockfile content changes key,
  - command changes key,
  - sandbox image changes key,
  - verify paths do not change key.
- Dependency cache service tests:
  - save archives only declared existing paths and uploads to object storage,
  - restore streams from object storage, reads effective paths from blob metadata (not caller-supplied list), removes those paths before extraction,
  - L1 local cache is checked before S3 on restore when configured,
  - missing object (S3 404) deletes stale DB row and falls through to cold install,
  - checksum mismatch deletes DB row and falls through to cold install,
  - no effective cache paths exist skips save,
  - `vendor` inferred path is skipped when directory does not exist after install,
  - concurrent saves for the same cache key result in a consistent DB record (last upsert wins),
  - LRU eviction job deletes DB records and corresponding S3 objects beyond retention limit.
- Named config merge tests:
  - omitted `cache` in named config inherits base `cache` settings,
  - `cache.enabled: false` in named config overrides base config's enabled,
  - `cache.paths` in named config with no `enabled` field inherits base `enabled`.
- Store tests with pgxmock for all org-scoped queries.
- Scheduler tests:
  - existing live session worker wins over cache placement,
  - healthy worker with matching placement location is preferred,
  - stale/unhealthy/cache-holder workers are ignored,
  - capacity pressure spills to the next candidate,
  - rendezvous hashing is deterministic for the same placement key,
  - repo-level placement key is used when config is unavailable,
  - scheduler falls back when cache-location lookup fails.
- Provider tests:
  - cache hit restores before marker validation,
  - cache miss runs install then saves,
  - restore failure runs install,
  - save failure does not fail preview,
  - runtime secret files are not present during save,
  - concurrent saves for the same cache key do not corrupt the blob (last atomic rename wins).

Verification:

- `go vet ./...`
- `go build ./...`
- `go test ./...`
- `make lint-tenancy`

## Implementation Tasks

1. Add schema migration for `preview_dependency_cache` and `preview_dependency_cache_locations`.
2. Add `models.PreviewInstallCacheConfig` and `models.PreviewDependencyCache`.
3. Update preview config parsing, defaulting, cloning, merging, and validation, including the named-config field-level merge semantics for `cache`.
4. Add effective cache path resolver and tests for default-on, inferred paths, additive paths, de-duplication, and opt-out.
5. Add tests for config validation and named-config merge behavior (omit / partial / full override).
6. Add preview store methods for dependency cache lookup/upsert/touch/delete/LRU-list and local L1 location lookup/upsert/delete. Keep methods org-scoped except worker-node cleanup, which may need a narrow lint exception.
7. Add store tests and satisfy tenancy lints.
8. Add `DependencyCache` implementation backed by shared S3-compatible object storage. Restore finds durable DB metadata first, then checks optional worker-local L1 before streaming from S3 to a bounded worker temp file on L1 miss, derives effective paths from `DependencyCacheMetadata.EffectivePaths`, and upserts local L1 location rows after successful L1 population. Save uploads to S3, writes checksum sidecars, upserts the DB record, and registers local L1 location when configured. Background cleanup deletes expired DB metadata, objects, and stale location hints; worker-local L1 eviction enforces the local byte budget.
9. Add exact cache key computation using version string `preview-dependency-cache-v1`.
10. Add placement key computation and scheduler tests.
11. Update worker selection to prefer live session worker, matching local-cache holders, rendezvous candidates for placement key, least-loaded same-region fallback, then cross-region fallback.
12. Add cache metrics using `org_id` dimension only (not `repo_id`); align result labels with observer statuses and add scheduler-decision metrics.
13. Change `PreviewCapableProvider.StartPreview` to accept `StartPreviewOptions`; update all provider implementations and tests.
14. Thread org/repo/session metadata through `Manager.LaunchPreview` into provider options.
15. Wire dependency cache into worker/server startup config with `PREVIEW_DEPENDENCY_CACHE_BUCKET` defaulting to empty (disabled), a host-backed default `PREVIEW_DEPENDENCY_CACHE_LOCAL_DIR=/var/cache/143/preview-dependency-cache`, and `PREVIEW_DEPENDENCY_CACHE_LOCAL_MAX_BYTES` for the worker-local L1 cache. When the bucket is present, the cache starts automatically; set `PREVIEW_DEPENDENCY_CACHE_LOCAL_DIR=off` to disable only the L1 layer.
16. Integrate restore/save around `runPreviewInstall`; add a comment documenting the restore-then-clean trade-off for marker-absent cold starts.
17. Extend observer/logging for cache events; implement clarified status semantics (no `hit` terminal status; `restored` and `save_failed`/`saved`/`skipped` only).
18. Add provider tests for cache hit/miss/failure/concurrent-save paths.
19. Update public Fumadocs and internal preview docs alongside the service implementation: include the inferred dependency-file table, `requirements.txt` unpinned-deps warning, mutable image tag warning, additive `cache.paths` example, and opt-out example.
20. Make bucket presence the rollout gate.
21. Run full Go verification and tenancy lint.

## Open Questions

1. Should dependency cache eviction share a single global disk budget with branch preview startup snapshots?
2. Should cache paths be restricted to `preview.install.cwd`, or is repo-root relative sufficient?
3. ~~Should save happen synchronously before service startup or asynchronously after preview is ready?~~ **Resolved:** See Recommended First Cut — synchronous restore, asynchronous save.
4. Should branch/PR previews use the same dependency cache before falling back to full startup snapshots? The architecture should be designed to enable this without major refactoring, but it is deferred from the initial implementation.
5. Should admins eventually have a cache purge API, or is worker-local LRU enough?
6. Should dependency-cache prewarming be added after first-cut metrics prove preview-open probability is high enough to justify the extra compute and object-storage cost?

## Recommended First Cut

Implement synchronous restore before install and kick off asynchronous best-effort save immediately after install success. Do not wait for save before starting services.

Rationale:

- Restore is on the critical path because it can skip install work.
- Save is not required for the current preview to be correct, so it should not delay readiness.
- If async save overlaps with service startup, it still only reads effective cache paths. Use a bounded context and skip save if the preview stops.
