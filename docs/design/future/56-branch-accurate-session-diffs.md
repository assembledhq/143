# Design: Branch-Accurate Session Diffs

> **Status:** Not Started | **Last reviewed:** 2026-04-21

> Make the session diff view fast by default and accurate to the session's real branch basis, with an explicit diff snapshot model instead of ad hoc `git diff` strings and JSONB history.

## Problem

Today the diff view is fast because it renders from the stored `sessions.diff` snapshot, not from a live repo walk on every page load. That part is good.

The accuracy model is weaker:

- The diff is currently collected with plain `git diff` inside the sandbox after the agent finishes.
- Plain `git diff` means "working tree vs current `HEAD`", not "working branch vs immutable base commit".
- The system stores `target_branch` and `working_branch`, but not the exact base commit the session branched from.
- Per-pass history is stored in `sessions.diff_history` JSONB, which is fine for a small feature but weak as a long-term persistence boundary.
- Diff capture logic is duplicated across adapters and workers instead of being centralized behind one abstraction.

This creates three concrete risks:

1. **Semantic drift**. If the target branch moves after the session starts, we cannot explain what the diff is relative to.
2. **Future commit bugs**. If a sandbox workflow starts creating commits locally, plain `git diff` will stop representing the real branch delta.
3. **Hard-to-evolve persistence**. `diff_history` JSONB has no typed schema for base commit, capture source, or refresh generations.

## Goals

1. Keep the diff view fast on open.
2. Make the stored diff explicitly relative to an immutable recorded base commit.
3. Support future workflows where the workspace may have local commits as well as uncommitted edits.
4. Introduce durable, typed diff snapshot persistence rather than extending `diff_history` JSONB.
5. Keep rollout low risk through dual-write and backward-compatible API shapes.

## Non-Goals

- Running a live `git diff` on every diff page render.
- Replacing the existing diff viewer UI in this project.
- Reworking PR creation itself; this is about session diff correctness and serving.
- Removing the current `sessions.diff` and `sessions.diff_stats` cache in the first migration.

## Design Principles

1. **Snapshot first, refresh second**. The UI should render immediately from cached data, then optionally refresh when there is a good reason.
2. **Diffs need provenance**. A diff string without a recorded basis commit is not a reliable artifact.
3. **One service owns diff semantics**. The codebase should have one place that defines how we compute "the session diff".
4. **Typed persistence beats JSON blobs**. History should live in a first-class table, not in append-only JSONB forever.
5. **Preserve the fast path**. Latest diff data stays denormalized on `sessions` for list/detail performance.

## Current State

### Post-PR-468 reality

PR creation has already moved in the right direction:

- PR creation now restores the sandbox snapshot and pushes the real working tree instead of reconstructing repository state from `sessions.diff`.
- The session row now has an asynchronous PR creation state machine (`pr_creation_state`, `pr_creation_error`) so the UI can show progress and retries.
- Snapshot lifecycle matters more now because the PR path depends on `snapshot_key` being present.

That was the correct move for PR fidelity, and this design should treat it as the baseline rather than undoing it blindly.

However, PR #468 does **not** solve session diff provenance:

- it does not add an immutable per-session `base_commit_sha`
- it does not centralize diff capture semantics
- it does not replace `diff_history` JSONB with typed history
- it does not define one authoritative source for "the session diff"

So the long-term sustainable direction is:

- **keep** the snapshot-backed PR push idea
- **keep** `pr_creation_state` as an orthogonal PR workflow state machine
- **remove** any remaining architectural assumption that `sessions.diff` is authoritative for shipping code
- **add** a proper typed diff architecture for review and history

### Write path

- The orchestrator clones the target/default branch and immediately creates a working branch.
- Session diff capture is still defined by ad hoc `git diff` collection during/after agent execution.
- `internal/services/agent/adapters/codex.go` and `internal/services/agent/adapters/claude_code.go` still call shared diff collection logic that shells out to plain `git diff`.
- Eval and worker helper paths also still collect `git diff HEAD` directly in a few places, which reinforces that diff semantics are not centrally owned yet.
- The resulting text is stored in `sessions.diff`.
- `sessions.diff_stats` and `sessions.diff_history` are updated alongside it.
- PR creation no longer trusts `sessions.diff` to recreate repository state; it restores the session snapshot and pushes the real workspace.

### Read path

- The frontend reads `session.diff` from the session payload.
- The diff parser builds file/hunk structures client-side.
- File context expansion hits the sandbox container only for surrounding file lines, not for diff generation.

### Strengths

- Opening the diff view is fast because there is no container round-trip.
- Snapshot-backed PR creation means shipping code is now much closer to the real workspace state than before.

### Weaknesses

- No immutable base commit is recorded.
- Diff capture semantics are not centrally owned.
- Review/history still depend on weakly typed diff storage.
- The system now has two partially separate truths:
  - snapshot-backed workspace state for PR creation
  - text diff snapshots for UI review
  These need a shared provenance model.

## Recommended Architecture

### 1. Define the authoritative diff basis

For every session with a repository, record an immutable `base_commit_sha` at the moment the workspace is cloned and before any agent modifications occur.

That base commit becomes the authoritative answer to:

- "What branch state did this session start from?"
- "What is this diff relative to?"

The session diff should then always mean:

> the current workspace state compared against `base_commit_sha`

This works for:

- uncommitted working tree changes
- staged changes
- local commits created later in the workspace

Because the comparison is against the immutable base commit, not just against current `HEAD`.

### 2. Centralize session diff and snapshot provenance in a dedicated service

Introduce a small service boundary, for example:

`internal/services/sessiondiff/`

Recommended responsibilities:

- `BasisRecorder`: captures and persists the immutable base commit after clone.
- `Collector`: computes the authoritative diff snapshot from a sandbox.
- `SnapshotWriter`: persists the latest cache plus historical snapshot rows.
- `RefreshPolicy`: decides whether a live refresh is warranted when a container still exists.
- `SnapshotMetadata`: exposes session-level provenance needed by both review and PR flows.

This service should own all git diff commands and all session-level diff provenance. Adapters should stop collecting their own diff strings.

### 2a. Preserve the good parts of PR #468, but reduce coupling

PR #468 introduced useful behavior, but it coupled PR creation tightly to raw session snapshot lifecycle. The sustainable version should keep the product behavior while tightening the abstraction boundary:

- `PRService` should depend on a higher-level session workspace artifact interface, not on ad hoc knowledge of `snapshot_key` semantics.
- Snapshot cleanup should remain possible, but cleanup policy should be owned by one lifecycle component rather than spread across session archive and PR webhook code paths.
- Session review diff history and PR creation should both derive from the same immutable session basis (`base_commit_sha`) even if they use different materializations.

Concretely, I recommend eventually evolving:

- from: `PRService` directly restoring `snapshot_key`
- to: `SessionWorkspaceResolver.ResolveForPR(session)`

That resolver can still use the snapshot under the hood, but the PR layer should not own low-level snapshot semantics forever.

### 3. Keep two storage layers with distinct purposes

#### Latest cache on `sessions`

Keep:

- `sessions.diff`
- `sessions.diff_stats`

These remain the fast UI cache for session list and session detail render paths.

#### Authoritative history in a new table

Add a new `session_diff_snapshots` table that stores the immutable metadata and full historical snapshots.

This table becomes the durable source of truth for:

- pass history
- provenance metadata
- future live refreshes within the same turn

## Schema Changes

### A. Extend `sessions`

Add these columns:

- `base_commit_sha text`
- `diff_collected_at timestamptz`
- `latest_diff_snapshot_id uuid references session_diff_snapshots(id)`

Semantics:

- `base_commit_sha` is immutable once set for a session.
- `diff_collected_at` is when the current `sessions.diff` cache was generated.
- `latest_diff_snapshot_id` points to the authoritative latest snapshot row.

Notes:

- `target_branch` and `working_branch` remain useful and should stay.
- `sessions.diff` and `sessions.diff_stats` remain as denormalized cache columns in phase 1.
- `pr_creation_state` and `pr_creation_error` introduced by PR #468 should stay. They solve a different problem and are orthogonal to diff provenance.

### B. Add `session_diff_snapshots`

Recommended schema:

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| session_id | uuid | FK -> sessions |
| turn_number | int | logical turn this snapshot belongs to |
| sequence_number | int | allows multiple snapshots within one turn |
| source | text | `turn_complete`, `resume_refresh`, `manual_refresh`, `legacy_backfill` |
| base_commit_sha | text | immutable comparison base |
| head_commit_sha | text | `git rev-parse HEAD` at capture time |
| working_branch | text | denormalized for debugging/audit |
| target_branch | text | denormalized for debugging/audit |
| diff | text | full unified diff |
| files_changed | int | typed stat |
| lines_added | int | typed stat |
| lines_removed | int | typed stat |
| captured_at | timestamptz | when the diff was computed |
| created_at | timestamptz | row creation time |

Constraints:

- unique `(session_id, turn_number, sequence_number)`
- check `sequence_number >= 1`
- check `turn_number >= 0`
- check `source` is in the allowed set

Indexes:

- `(org_id, session_id, captured_at desc)` for session detail/history queries
- `(org_id, session_id, turn_number desc, sequence_number desc)` for latest-per-turn lookups

### Why a table instead of more JSONB?

Because this feature needs typed, queryable metadata:

- immutable base commit
- capture source
- multiple snapshots per turn
- explicit timestamps

Appending more structure into `diff_history` JSONB would keep write code deceptively short while making read paths, debugging, and future migrations worse.

## Diff Semantics

### Collector contract

Given a sandbox and `base_commit_sha`, the collector should:

1. Confirm the workspace is a git repository.
2. Read `HEAD` as `head_commit_sha`.
3. Compute the authoritative diff against `base_commit_sha`.
4. Compute typed stats.

Recommended command shape:

```bash
git diff --find-renames --binary <base_commit_sha> -- .
```

Why:

- compares the current workspace state to the immutable base
- includes local commits and uncommitted edits
- keeps rename detection enabled
- produces a patch format the existing UI can already parse

Do not use plain `git diff` as the durable session diff definition.

### PR interaction contract

Branch-accurate diffs should not force us to revert the snapshot-backed PR push flow.

Instead:

- the diff architecture becomes the source of truth for **review provenance**
- the snapshot-backed workspace remains the source of truth for **what gets pushed**
- both must point back to the same immutable `base_commit_sha`

That means we should explicitly store and expose:

- `base_commit_sha`
- `working_branch`
- current `head_commit_sha` at diff capture time

So that if review and PR behavior ever diverge, we can explain why.

### Stats

Use typed stats in persistence rather than JSON blobs as the primary representation.

The API can still expose:

```json
{ "added": 12, "removed": 3, "files_changed": 2 }
```

But those values should be derived from typed columns in `session_diff_snapshots` and/or denormalized onto `sessions`.

## API Changes

### Phase 1

No breaking change to existing session detail responses.

Keep returning:

- `diff`
- `diff_stats`
- `diff_history` for compatibility
- `pr_creation_state`
- `pr_creation_error`

Add optional metadata:

- `base_commit_sha`
- `diff_collected_at`

Implementation note:

- Keep `pr_creation_state`, `pr_creation_error`, and `snapshot_key` behavior introduced by PR #468 intact during this phase.
- The first migration should be additive only and should dual-write the new typed snapshot rows while continuing to populate `sessions.diff` and `sessions.diff_history`.

### Phase 2

Add a dedicated diff endpoint for richer, explicit diff metadata:

`GET /api/v1/sessions/{id}/diff`

Recommended response:

```json
{
  "diff": "...",
  "diff_stats": {
    "added": 12,
    "removed": 3,
    "files_changed": 2
  },
  "base_commit_sha": "abc123",
  "head_commit_sha": "def456",
  "captured_at": "2026-04-21T18:22:00Z",
  "turn_number": 3,
  "sequence_number": 1,
  "source": "turn_complete",
  "is_live": false
}
```

This lets the UI remain fast while being explicit about provenance.

### Optional refresh endpoint

For sessions with a live container, add:

`POST /api/v1/sessions/{id}/diff/refresh`

Behavior:

- only allowed when a live sandbox container exists
- recomputes the latest diff from `base_commit_sha`
- writes a new `session_diff_snapshots` row with incremented `sequence_number`
- updates `sessions.diff`, `sessions.diff_stats`, `sessions.diff_collected_at`, and `latest_diff_snapshot_id`

This endpoint should be opt-in and background-oriented, not required for page load.

### Non-goal for this work

Do **not** tie diff refresh to PR creation state transitions. The PR state machine is useful, but it should remain orthogonal to diff serving.

## Write-Path Changes

### 1. Record base commit after clone

After cloning the repo and before the agent starts modifying files:

- run `git rev-parse HEAD`
- persist the result as `sessions.base_commit_sha`

This must happen in the orchestrator, not in individual adapters.

### 2. Remove diff collection from adapters

Adapters should return agent output, summary, and token usage, but not own git diff semantics.

The orchestrator should call the `sessiondiff.Collector` once per turn.

### 3. Persist snapshots centrally

At turn completion:

- collect the authoritative diff snapshot
- insert into `session_diff_snapshots`
- update the `sessions` latest cache
- continue dual-writing `diff_history` during rollout if needed

### 4. Rationalize snapshot-backed PR creation behind a stable abstraction

After PR #468, this is now part of the implementation sequence, not a separate concern.

Add an abstraction such as:

```go
type SessionWorkspaceResolver interface {
    ResolveForReview(ctx context.Context, session *models.Session) (*WorkspaceHandle, error)
    ResolveForPR(ctx context.Context, session *models.Session) (*WorkspaceHandle, error)
}
```

Initial implementation:

- `ResolveForReview` returns cached diff metadata, optionally plus live refresh capability if a sandbox exists
- `ResolveForPR` restores the snapshot-backed sandbox exactly as today

This lets us keep PR #468's correctness benefits without freezing low-level snapshot operations into `PRService`.

## Read-Path Changes

### Session detail page

The page should continue rendering from the embedded session cache for immediate paint.

That preserves the current speed profile.

### Pass history

The current pass selector should move from `sessions.diff_history` JSONB to `session_diff_snapshots`.

During rollout:

- keep the response shape stable
- change only the backend source

### Live sessions

If a live container exists and the product wants "current workspace" fidelity beyond the last completed turn, the UI may optionally trigger `diff/refresh` in the background.

That should be a separate decision from initial render.

## Migration Plan

### Migration 1: Add typed schema

Add:

- `sessions.base_commit_sha`
- `sessions.diff_collected_at`
- `sessions.latest_diff_snapshot_id`
- `session_diff_snapshots` table

Do not remove old columns.
Do not remove `pr_creation_state` / `pr_creation_error`.

### Migration 2: Best-effort backfill

Backfill `session_diff_snapshots` from:

- `sessions.diff`
- `sessions.diff_stats`
- `sessions.diff_history`

Legacy rows should use:

- `source = 'legacy_backfill'`
- `base_commit_sha = NULL` if unknown
- `sequence_number = 1`

The goal is continuity of UI history, not perfect provenance for old rows.

### Migration 3: Dual-write

Update write paths so every new diff snapshot writes:

- new `session_diff_snapshots` row
- existing `sessions.diff` cache
- existing `sessions.diff_stats` cache
- existing `diff_history` JSONB for temporary compatibility

At the same time:

- keep `pr_creation_state` and `pr_creation_error` behavior unchanged
- do not make PR creation depend on the new diff table before the table is fully rolled out

### Migration 4: Switch reads

Move:

- pass selector
- detailed diff metadata
- future refresh features

to read from `session_diff_snapshots`.

Keep `sessions.diff` as the latest fast cache.
Keep PR creation sourcing from the snapshot-backed workspace path until a higher-level workspace resolver is in place.

### Migration 5: Retire `diff_history`

After read paths are fully switched and backfill is validated:

- stop dual-writing `sessions.diff_history`
- later drop it in a separate cleanup migration

This should be a final cleanup step, not part of the first rollout.

## Performance Strategy

### What stays fast

- Session list reads still use denormalized stats on `sessions`.
- Session detail still includes the latest cached diff.
- The diff UI still parses locally in the browser.

### What becomes more accurate

- Diff basis is immutable and explicit.
- Refreshes compare against the true session base commit.
- Future committed changes in the sandbox are represented correctly.
- The review surface and the PR flow share a common provenance model even if they materialize state differently.

### Why this is GitHub-like enough

GitHub feels fast because it serves precomputed branch comparison results and metadata caches, not because it shells out to git on every view.

This design matches that model:

- latest diff cache for instant render
- explicit basis metadata
- optional refresh only when needed
- snapshot-backed real-worktree pushes for the ship path

## Testing Requirements

### Backend

- orchestrator test: base commit recorded immediately after clone
- collector tests: branch delta computed from `base_commit_sha`
- store tests: snapshot insert + latest cache update
- migration/backfill tests for legacy session rows
- API tests for new diff metadata fields

### Frontend

- no UI semantic regression in current diff rendering
- pass selector works when history comes from typed snapshots instead of JSONB
- optional live refresh does not block initial paint

## Rollout Risks

### 1. Divergent sources during migration

If `sessions.diff`, `session_diff_snapshots`, and the snapshot-backed PR workspace diverge during dual-write, the system will be confusing. Add integrity logging and one-off audit tooling during rollout.

### 2. Legacy sessions without base commit

Old rows cannot be made perfectly branch-accurate. The system should treat them as legacy snapshots with unknown provenance rather than guessing.

### 3. Diff size growth

If diff payload sizes become material, we may later move snapshot bodies to blob storage and keep metadata in Postgres. That is not required for the first implementation.

## Recommended Implementation Sequence

1. Add typed schema and base commit recording.
2. Introduce `internal/services/sessiondiff` and move all diff capture there.
3. Add a stable session workspace resolver so PR creation no longer owns raw snapshot semantics directly.
4. Dual-write new snapshots plus current session cache.
5. Switch pass-history reads off `diff_history`.
6. Add optional live refresh endpoint for sessions with a live sandbox.
7. Remove `diff_history` writes, then drop the column later.

## Recommendation

Do not solve this by adding another field to `sessions.diff_history` or by issuing live git diff commands from the UI path.

Do not blindly revert PR #468 either. It fixed a real correctness problem in PR creation. The right move is to absorb it into a cleaner artifact/provenance architecture.

The stable architecture is:

- immutable recorded `base_commit_sha`
- centralized diff collector
- authoritative `session_diff_snapshots` table
- denormalized latest cache on `sessions`
- optional async refresh only for live sandboxes

That gets us the combination we want:

- fast like a cached branch comparison
- accurate to the real session branch basis
- maintainable as the sandbox and review workflows evolve
