# Design: S3-Backed Session Snapshot Storage

> **Status:** Implemented | **Last reviewed:** 2026-04-21

## 1. Motivation

Interactive sessions depend on snapshot persistence for:

- follow-up turns after an idle pause
- preview hydrate after the live sandbox has been torn down
- recovery after worker restarts or deploys

The current runtime wiring constructs a local filesystem snapshot store in [cmd/server/main.go](/Users/wangjohn/.codex/worktrees/ac7f/143/cmd/server/main.go) via `storage.NewFileSnapshotStore(cfg.SnapshotStorageDir)`. That works on a single machine, but it is incorrect for split app/worker deployments:

- the worker writes snapshots to its own local disk
- the API node later tries to hydrate previews from its own different local disk
- snapshots are therefore invisible across nodes even when the DB row still points at them

Recent production failures also showed that the current relative default path (`.data/snapshots`) is brittle in containerized runtime environments when the process working directory is not what we expect.

We need a shared snapshot backend that:

- is visible to both API and worker nodes
- preserves the existing `SnapshotStore` abstraction
- keeps Postgres as the source of truth for session lifecycle
- fails safely when objects are missing or storage is degraded

S3-compatible object storage is the simplest fit.

---

## 2. Goals

- Store session snapshots in a shared S3-compatible object store.
- Keep the existing `storage.SnapshotStore` interface unchanged.
- Support AWS S3 and S3-compatible providers such as Cloudflare R2 or MinIO.
- Make snapshot backend selection explicit at startup.
- Preserve the current reaper-driven retention model.
- Allow a safe rollout without migrating historical local snapshots.

## 3. Non-goals

- Replacing uploads with a unified generic object-store framework.
- Migrating existing local snapshot files into S3.
- Changing snapshot key structure or session restore semantics.
- Making Redis or any other cache part of snapshot durability.

---

## 4. Current State

### 4.1 Existing primitives

- `storage.SnapshotStore` already exists in [internal/services/storage/snapshot.go](/Users/wangjohn/.codex/worktrees/ac7f/143/internal/services/storage/snapshot.go).
- `storage.S3SnapshotStore` already exists in [internal/services/storage/s3.go](/Users/wangjohn/.codex/worktrees/ac7f/143/internal/services/storage/s3.go).
- `storage.FileSnapshotStore` exists in [internal/services/storage/file.go](/Users/wangjohn/.codex/worktrees/ac7f/143/internal/services/storage/file.go).
- Snapshot save/load/delete is already abstracted throughout the agent and preview codepaths.

### 4.2 Implemented wiring

The runtime now includes:

- config fields for snapshot S3 selection
- startup wiring that chooses file vs S3 explicitly
- support for custom S3 endpoints and path-style access for snapshots
- startup logs that clearly indicate which snapshot backend is active

Deployment credentials and per-environment rollout still need to be supplied operationally, but the application wiring is complete.

### 4.3 Existing upload pattern

Uploads already have optional S3 support in [internal/api/router.go](/Users/wangjohn/.codex/worktrees/ac7f/143/internal/api/router.go), but that implementation was not sufficient to copy directly:

- it does not currently use `UPLOAD_S3_ENDPOINT`
- it does not expose path-style config
- it is scoped to uploads, not snapshots

Snapshot storage should reuse the broad pattern, but with a cleaner shared S3 client builder.

---

## 5. Proposed Design

### 5.1 Config surface

Add snapshot-specific object storage config alongside the existing file fallback:

```go
// Interactive session snapshots
SnapshotStorageDir       string `env:"SNAPSHOT_STORAGE_DIR" envDefault:".data/snapshots"`
SnapshotS3Bucket         string `env:"SNAPSHOT_S3_BUCKET"`
SnapshotS3Prefix         string `env:"SNAPSHOT_S3_PREFIX" envDefault:"snapshots"`
SnapshotS3Region         string `env:"SNAPSHOT_S3_REGION" envDefault:"us-east-1"`
SnapshotS3Endpoint       string `env:"SNAPSHOT_S3_ENDPOINT"`
SnapshotS3UsePathStyle   bool   `env:"SNAPSHOT_S3_USE_PATH_STYLE" envDefault:"false"`
```

Rules:

- if `SNAPSHOT_S3_BUCKET` is non-empty, use S3-backed snapshots
- otherwise, fall back to `SNAPSHOT_STORAGE_DIR`
- `SNAPSHOT_STORAGE_DIR` remains the default for local dev and single-node deployments

We intentionally do **not** reuse `UPLOAD_S3_BUCKET` for snapshots. Uploads and snapshots have different:

- retention expectations
- data sensitivity
- object sizes
- read/write patterns
- IAM least-privilege needs

Separate config avoids accidental coupling and makes rollout reversible.

### 5.2 Store selection at startup

`cmd/server/main.go` now builds a single snapshot store via a helper and injects that same instance into both API and worker paths.

The selected store is still shared by:

- API preview hydrate paths
- worker orchestrator snapshot save/load paths
- session reaper cleanup paths

This preserves the existing “construct once, inject everywhere” model.

### 5.3 Shared S3 client builder

The runtime now uses a small shared helper in `internal/services/storage/s3_client.go` that builds an AWS SDK client with:

- region
- optional endpoint override
- optional path-style access
- default AWS credential chain

Expected behavior:

- AWS S3 works with only bucket + region + credentials
- MinIO/R2/Backblaze-style deployments work with endpoint override
- path-style can be turned on when virtual-hosted style is unsupported

This helper should be used by snapshots immediately, and uploads can migrate to it later.

### 5.4 Object key layout

Keep the current session snapshot key shape:

```text
snapshots/<org-id>/<session-id>/workspace.tar.zst
```

The S3 store should prepend the configured prefix, so the full object key becomes:

```text
<SNAPSHOT_S3_PREFIX>/snapshots/<org-id>/<session-id>/workspace.tar.zst
```

If the extra `snapshots/` repetition feels redundant, we can normalize keys later, but rollout should preserve the current logical key pattern to avoid touching orchestrator semantics in the same change.

### 5.5 Encryption

Keep the current baseline behavior in [internal/services/storage/s3.go](/Users/wangjohn/.codex/worktrees/ac7f/143/internal/services/storage/s3.go):

- `ServerSideEncryption: AES256`

This is acceptable for the first rollout. KMS support can be added later if needed, but should not block initial adoption.

### 5.6 Source of truth

Postgres remains authoritative for:

- whether a session believes a snapshot should exist (`snapshot_key`)
- whether a session is restorable
- retention and cleanup timing

S3 is just the snapshot blob store.

If S3 returns “not found” for a key still referenced by Postgres:

- backend should surface snapshot-unavailable semantics
- the row should not be trusted as durable evidence that the snapshot still exists

This matches the current `ErrSnapshotNotFound` / `ErrSnapshotMissing` handling model.

---

## 6. Security Model

### 6.1 IAM model

Preferred production setup:

- worker identity: `GetObject`, `PutObject`, `DeleteObject` on snapshot bucket/prefix
- API identity: `GetObject` on snapshot bucket/prefix

Pragmatic first rollout:

- both app and worker get read/write/delete on the snapshot prefix

That is simpler operationally and acceptable initially, but we should document the tighter split as the target state.

### 6.2 Bucket isolation

Use either:

- a dedicated bucket for session snapshots, or
- a dedicated snapshot-only prefix in a shared bucket

Dedicated bucket is preferred because it simplifies:

- lifecycle rules
- IAM
- cost tracking
- accidental cross-feature deletes

### 6.3 Data sensitivity

Snapshots may contain:

- repository working tree contents
- generated diffs not yet pushed anywhere
- agent CLI state under home directories
- credentials or tokens accidentally written into workspace files by user code

Because of that, snapshots must be treated as sensitive internal storage, not as user-upload media.

---

## 7. Retention and Cleanup

The application reaper remains the primary retention mechanism.

Current model:

- session rows track snapshot references
- reaper deletes old snapshots based on `SESSION_MAX_SNAPSHOT_AGE`
- reaper updates session sandbox state afterward

That should continue unchanged.

### 7.1 S3 lifecycle policy

Add an S3 bucket lifecycle rule as a **safety net**, not as the primary retention engine.

Recommendation:

- application retention: 30 days
- S3 lifecycle expiration: 35 to 45 days

This avoids a race where the object store deletes a snapshot before the application has decided it expired.

### 7.2 Delete semantics

`SnapshotStore.Delete` should remain best-effort:

- deleting a missing object is not an error condition
- storage transport failures should still be logged and surfaced to operators

---

## 8. Rollout Plan

### Phase 1: Code wiring

Completed:

- config fields
- shared snapshot-store builder
- S3 client helper
- startup logging for snapshot backend selection
- tests

File store remains the default when `SNAPSHOT_S3_BUCKET` is unset.

### Phase 2: Provision object storage

Provision:

- bucket
- prefix
- credentials / IAM policy
- optional lifecycle rule

Do **not** cut traffic yet.

### Phase 3: Worker rollout

Deploy worker nodes with:

- `SNAPSHOT_S3_*` configured

Workers will begin writing new snapshots to S3.

At this point, API nodes still using local file storage will not be able to hydrate those new snapshots, so this phase should be short-lived and coordinated. It is not a steady-state deployment.

### Phase 4: API rollout

Deploy API nodes with the same `SNAPSHOT_S3_*` config.

After this point:

- workers write snapshots to S3
- APIs read snapshots from S3
- preview hydrate and follow-up resume become cross-node safe

### Phase 5: Cleanup

After confidence is high:

- stop relying on local snapshot directories in production
- optionally remove snapshot bind mounts if they were only there for the old model

---

## 9. Migration Strategy

We explicitly do **not** migrate historical local snapshot files.

During cutover:

- sessions created before the switch may still point at local-only snapshots
- those snapshots will not be visible to the new S3-backed store
- users may need to send a new message to rebuild the sandbox

This is acceptable because:

- resumable sessions are transient, not long-term canonical artifacts
- bulk migration adds operational and correctness risk for low value

If we later need migration tooling, it should be a one-off offline script, not part of the steady-state runtime.

---

## 10. Failure Modes

### 10.1 S3 unavailable on save

Behavior:

- snapshot save fails
- session remains usable only while the live container still exists
- after teardown, follow-up restore is unavailable

Required product behavior:

- surface snapshot-unavailable messaging, not “expired”

### 10.2 S3 unavailable on load

Behavior:

- preview hydrate fails
- continue-session hydrate fails

Response:

- treat as infrastructure failure if the object should exist but storage is down
- treat as snapshot unavailable if the object is genuinely missing

### 10.3 Misconfigured endpoint or credentials

Behavior:

- startup should fail loudly, not silently fall back in production

Recommendation:

- in development, fallback-to-file is acceptable
- in production, if `SNAPSHOT_S3_BUCKET` is set and S3 initialization fails, server startup should fail

That prevents a split-brain deployment where one node writes to file while another expects S3.

---

## 11. Observability

The runtime now emits startup logs with:

- snapshot backend: `file` or `s3`
- bucket
- prefix
- endpoint host when set
- path-style flag

Structured logs should continue to distinguish:

- successful session snapshot saves, including `snapshot_key` and `snapshot_size_bytes`
- snapshot save failures
- snapshot load failures
- snapshot delete failures
- snapshot not found

The platform health Grafana dashboard tracks session snapshot size from the
`session snapshot saved` event so capacity planning can see p95 and largest
recent checkpoint sizes before adding workers in more regions or providers.

The important operational distinction is:

- transport/config outage
- expected expiry
- unexpected missing object

---

## 12. Concrete Implementation Checklist

### Code

1. Add `SNAPSHOT_S3_*` fields to [internal/config/config.go](/Users/wangjohn/.codex/worktrees/ac7f/143/internal/config/config.go). Done.
2. Add a snapshot-store builder used by [cmd/server/main.go](/Users/wangjohn/.codex/worktrees/ac7f/143/cmd/server/main.go). Done.
3. Add a shared S3 client builder that supports endpoint override and path-style. Done.
4. Wire `storage.NewS3SnapshotStore(...)` when snapshot S3 config is present. Done.
5. Keep `storage.NewFileSnapshotStore(...)` as fallback for local/dev. Done.
6. Add startup status logs for the chosen snapshot backend. Done.

### Tests

1. Config parsing tests for new env vars. Done.
2. Store-selection tests for file vs S3. Done.
3. S3 endpoint/path-style tests. Done.
4. Snapshot startup failure tests when S3 config is invalid. Done.

### Deploy

1. Add `SNAPSHOT_S3_*` env vars to encrypted production env.
2. Ensure both app and worker roles receive them.
3. Provision bucket and credentials before rollout.
4. Roll worker and API nodes in a coordinated window.

---

## 13. Bucket Provisioning Instructions

The application-side work is complete, but production still needs a real bucket or bucket-equivalent prefix.

### 13.1 AWS S3

Recommended production shape:

- bucket name: dedicated, e.g. `143-prod-session-snapshots`
- region: explicit, e.g. `us-west-2`
- prefix: `sessions`
- default encryption: SSE-S3 (`AES256`)
- lifecycle expiration: 35 to 45 days
- block all public access: enabled

Example AWS CLI flow:

```bash
export AWS_REGION=us-west-2
export SNAPSHOT_BUCKET=143-prod-session-snapshots

aws s3api create-bucket \
  --bucket "$SNAPSHOT_BUCKET" \
  --region "$AWS_REGION" \
  --create-bucket-configuration LocationConstraint="$AWS_REGION"

aws s3api put-public-access-block \
  --bucket "$SNAPSHOT_BUCKET" \
  --public-access-block-configuration \
  'BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true'

aws s3api put-bucket-encryption \
  --bucket "$SNAPSHOT_BUCKET" \
  --server-side-encryption-configuration \
  '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'
```

Optional lifecycle safety net:

```bash
aws s3api put-bucket-lifecycle-configuration \
  --bucket "$SNAPSHOT_BUCKET" \
  --lifecycle-configuration \
  '{"Rules":[{"ID":"expire-session-snapshots","Status":"Enabled","Filter":{"Prefix":"sessions/"},"Expiration":{"Days":40}}]}'
```

Application env for AWS:

```bash
SNAPSHOT_S3_BUCKET=143-prod-session-snapshots
SNAPSHOT_S3_PREFIX=sessions
SNAPSHOT_S3_REGION=us-west-2
SNAPSHOT_S3_USE_PATH_STYLE=false
```

Leave `SNAPSHOT_S3_ENDPOINT` unset for AWS.

### 13.2 S3-Compatible Providers

For R2, MinIO, or similar providers:

- keep a dedicated bucket when possible
- set `SNAPSHOT_S3_ENDPOINT` to the provider endpoint
- set `SNAPSHOT_S3_USE_PATH_STYLE=true` only when the provider requires it
- keep `SNAPSHOT_S3_PREFIX=sessions`

Example env:

```bash
SNAPSHOT_S3_BUCKET=143-session-snapshots
SNAPSHOT_S3_PREFIX=sessions
SNAPSHOT_S3_REGION=auto
SNAPSHOT_S3_ENDPOINT=https://<account-or-cluster-endpoint>
SNAPSHOT_S3_USE_PATH_STYLE=true
```

### 13.3 Rollout Checklist

1. Provision the bucket and lifecycle rule before changing runtime env.
2. Add `SNAPSHOT_S3_*` to both worker and API environments.
3. Roll workers and APIs in one coordinated window.
4. Verify startup logs report `backend=s3` with the expected bucket, prefix, endpoint host, and path-style flag.
5. Start a manual session, wait for it to snapshot, then resume it from a different node boundary to verify cross-node hydrate.

---

## 14. Recommended Defaults

For production:

- `SNAPSHOT_S3_BUCKET`: dedicated snapshot bucket
- `SNAPSHOT_S3_PREFIX`: `sessions`
- `SNAPSHOT_S3_REGION`: explicit
- `SNAPSHOT_S3_USE_PATH_STYLE`: `false` on AWS, `true` only when required

For local development:

- leave `SNAPSHOT_S3_BUCKET` unset
- use `SNAPSHOT_STORAGE_DIR`

---

## 15. Decision Summary

We adopted S3-backed snapshot storage because the local-disk model was not correct for multi-node app/worker deployments. The repo already had the right abstraction and an S3 implementation; this change completed the missing startup wiring, config, prefix handling, and operator-visible startup behavior.

The design intentionally keeps:

- the existing `SnapshotStore` abstraction
- Postgres as the lifecycle source of truth
- file-backed snapshots for local/dev
- application-driven retention

The design intentionally does **not** attempt historical snapshot migration.
