# Design: Accurate, Navigable Session Diffs

> **Status:** Implemented | **Last reviewed:** 2026-05-01

> Unifies two gaps in the current review surface: GitHub-style movement through surrounding file context and diff snapshots that are explicitly tied to the immutable branch basis for the session.
>
> Phases 1–3 and Phase 6 are live. Phases 4–5 (richer diff-endpoint metadata, JSONB cleanup, and review-surface polish) are tracked below as deferred follow-ups — none of them gate a functioning feature; they are parked here so future work does not have to rediscover them. When neither a live container nor a usable snapshot is available, the diff still falls back to disabled expanders with explicit "Additional file context unavailable for this session" copy.

## Problem

The current review surface is fast, but only partially trustworthy and only partially navigable.

On the navigation side, the diff viewer shows parsed diff hunks and supports a single context expansion path only for gaps between two hunks. That is not enough for real review work:

- You cannot expand upward from the first visible hunk.
- You cannot expand downward from the last visible hunk.
- Between two hunks, expansion is one-shot rather than incremental.
- Once a gap is expanded, the UI loses the notion of what context is still hidden.
- The full-file repo explorer exists, but it is a mode switch that breaks diff position and inline review flow.

This makes it hard to answer common review questions such as:

- "What function am I inside right now?"
- "What happens immediately before this change?"
- "How far away is the surrounding conditional or helper?"
- "Can I inspect another 20 lines without leaving the diff?"

On the accuracy side, the diff itself is still a cached patch collected from ad hoc sandbox `git diff` calls:

- the stored diff is not explicitly tied to an immutable `base_commit_sha`
- review history still lives in `sessions.diff_history` JSONB
- diff semantics are duplicated across adapters and worker helpers
- PR creation now uses snapshot-backed workspace pushes, so review and ship paths no longer share a strong provenance model

That creates the wrong long-term shape: the UI loads quickly, but it cannot cleanly explain what the diff is relative to, and it cannot progressively reveal more file context with confidence.

## Current State

The current implementation is split across:

- `frontend/src/components/code-review/file-diff-section.tsx`
- `frontend/src/components/code-review/context-expander.tsx`
- `frontend/src/components/code-review/diff-pane.tsx`
- `internal/api/handlers/session_files.go`
- `internal/services/sandbox/docker_filereader.go`
- `internal/services/agent/adapters/claude_code.go`
- `internal/services/agent/adapters/codex.go`
- `internal/db/session_store.go`
- `internal/services/github/pr.go`

Current behavior:

- The diff parser produces only hunks and lines from the patch.
- `FileDiffSection` computes hidden lines only between consecutive hunks.
- `ContextExpander` fetches a single window centered in that gap and replaces the gap with one synthetic context hunk.
- The session file API already supports arbitrary line-window reads through `GET /api/v1/sessions/{id}/files/context`.
- The underlying diff itself still comes from stored session diff text, not a live repo walk when the reviewer opens the page.
- That stored diff is still collected via plain sandbox `git diff` or `git diff HEAD` calls in adapter and worker code.
- `sessions.diff_stats` and `sessions.diff_history` are still the persistence model for review-time diff state.
- PR creation already moved onto snapshot-backed workspace pushes, so shipping fidelity is better than review-path provenance.

Current limitations:

- No UI model for "hidden context before first hunk" or "after last hunk".
- No persistent gap state such as `expanded_start`, `expanded_end`, `remaining_above`, `remaining_below`.
- No incremental "show more above" / "show more below" controls after an expansion.
- The current expander fetches from the midpoint of a gap, which is fine for one-shot reveal but wrong for directional navigation.
- The current test coverage only exercises the one-shot gap insertion path.
- The API can fetch surrounding file lines, but it does not yet expose enough metadata to tell the UI whether there is still hidden content above or below the visible region.
- No immutable per-session `base_commit_sha` is recorded for review diffs.
- Diff capture semantics are not centrally owned by one service.
- Review history depends on weakly typed JSONB instead of a typed diff snapshot model.

## Goals

1. Let reviewers expand context above or below their current visible region without leaving the diff.
2. Support top-of-file, between-hunk, and bottom-of-file navigation.
3. Keep the diff view fast on open.
4. Make the stored diff explicitly relative to an immutable recorded base commit.
5. Preserve inline comments, keyboard navigation, syntax highlighting, and split/unified modes.
6. Keep the implementation compatible with session sandboxes and the existing file-context API shape where possible.

## Non-Goals

- Replacing the repo explorer.
- Rendering entire large files by default.
- Building a full editor with arbitrary scrolling through unchanged files.
- Rehydrating or recreating expired session sandbox containers purely to support context expansion. Reading directly from the persisted workspace snapshot tar without a container is in scope (Phase 6).
- Running a live `git diff` on every diff page render.
- Reworking PR creation away from snapshot-backed workspace pushes.

## Design Principles

1. **Fast first paint**. The review surface should still open from cached diff data, not a live repo walk.
2. **Diffs need provenance**. A patch without a recorded basis commit is not a durable review artifact.
3. **Directional context, not mode switching**. Reviewers should be able to move up and down from the current hunk without bouncing into the repo explorer.
4. **One service owns diff semantics**. The codebase should define "the session diff" in one place.
5. **Typed persistence beats JSON blobs**. Long-term review history should not keep growing inside `diff_history` JSONB.

## Recommended Product Behavior

Each file section should behave like a sequence of visible diff blocks separated by expandable context gaps, and the entire diff should carry explicit provenance about what branch basis it was computed from.

For every hidden range, the UI should support:

- `Show 20 above`
- `Show 20 below`
- `Show all`

Expected behavior:

- Before the first hunk, show a top gap if the first changed line is not line 1.
- Between hunks, show an expandable gap with directional controls instead of a single midpoint fetch.
- After the last hunk, show a bottom gap when the file has trailing content.
- After expanding part of a gap, keep controls visible until the gap is fully revealed.
- Expanded context lines should render exactly like existing context lines in unified and split views, including line numbers and comment affordances.
- Session detail should be able to expose, at minimum, the diff capture timestamp and the immutable base commit used to compute the cached patch.
- If a session has a live sandbox, file context should be served from the container as today.
- If a session has no live sandbox but still has a stored workspace snapshot, file context should be served from the snapshot (see Phase 6). The reviewer should not be able to tell the difference apart from a small first-read latency for cold snapshots.
- Only when a session has neither a live sandbox nor a usable snapshot should the diff fall back to cached patch data alone, with provenance metadata still shown and context expanders rendered in a disabled state with explicit copy such as `Additional file context unavailable for this session`.

## Recommended Technical Design

### 0. Define the review diff as a cached snapshot with provenance

Keep the current fast load path:

- render the diff UI from cached session diff data
- fetch surrounding file lines on demand from the sandbox

But change the contract of the cached diff so it means:

> the session workspace state compared against the immutable `base_commit_sha` recorded when the workspace was created

Authoritative semantics:

- `base_commit_sha` is the exact commit checked out immediately after clone and before any agent edits. It is not recomputed later from `merge-base`, and it does not drift if the target branch moves.
- The collector compares the current workspace against that recorded commit, so local commits created later in the session remain part of the diff.
- Staged and unstaged tracked-file changes are included.
- Untracked files must also be included in the authoritative review diff. The collector should materialize them into the snapshot as synthetic additions so the UI and PR review surface do not silently omit new files.
- Ignored files are excluded.
- Submodule pointer changes, if they appear, should be represented as ordinary git diff output and not expanded with file-context APIs.

Reference cases:

| Workspace state | Included in authoritative diff? | Notes |
|---|---|---|
| Modified tracked file, unstaged | Yes | Standard patch against `base_commit_sha` |
| Modified tracked file, staged | Yes | Same visible result as unstaged |
| Local commit created after session start | Yes | Comparison remains against recorded `base_commit_sha` |
| New untracked file | Yes | Must appear as a file addition in cached review diff |
| Deleted tracked file | Yes | Standard deletion patch |
| Ignored/generated file | No | Excluded from review diff |

That implies:

- add immutable `base_commit_sha` on `sessions`
- keep `sessions.diff` and `sessions.diff_stats` as the fast latest cache
- add typed historical diff snapshots instead of relying on `diff_history` JSONB forever
- centralize diff collection behind a dedicated service rather than scattered `git diff` calls

### 1. Introduce explicit per-file gap state

The main missing abstraction is that the UI only stores "extra lines inserted at gap index N". It should instead track hidden and visible ranges per gap.

Recommended shape:

```ts
interface ContextGapState {
  kind: "top" | "middle" | "bottom";
  filePath: string;
  hiddenStart: number;
  hiddenEnd: number;
  visibleStart?: number;
  visibleEnd?: number;
  lines: DiffLine[];
}
```

Key idea:

- `hiddenStart` / `hiddenEnd` represent the full unchanged range available for that gap.
- `visibleStart` / `visibleEnd` represent the currently revealed subset inside that range.
- Repeated expansions grow the visible range upward or downward until it covers the whole gap.

This avoids midpoint math and makes the UI directional.

### 2. Record the immutable diff basis and typed snapshot history

Extend `sessions` with:

- `base_commit_sha text`
- `diff_collected_at timestamptz`
- `latest_diff_snapshot_id uuid references session_diff_snapshots(id)`

Add a new `session_diff_snapshots` table with:

- session and org foreign keys
- `turn_number` and `sequence_number`
- `source`
- `base_commit_sha` and `head_commit_sha`
- denormalized `working_branch` and `target_branch`
- `diff`
- typed stats columns
- `captured_at`

This preserves the fast cache on `sessions` while creating a durable, queryable history model.

Migration and linkage details:

- `sessions.base_commit_sha`, `sessions.diff_collected_at`, and `sessions.latest_diff_snapshot_id` should all be nullable in the first migration.
- Create `session_diff_snapshots` first with a normal foreign key to `sessions(id)`.
- Add `sessions.latest_diff_snapshot_id` afterward as a nullable foreign key to `session_diff_snapshots(id)`.
- Use `ON DELETE CASCADE` from `session_diff_snapshots.session_id -> sessions.id` so snapshot rows disappear when the session is deleted.
- Use `ON DELETE SET NULL` for `sessions.latest_diff_snapshot_id -> session_diff_snapshots.id` so the session row can survive partial cleanup or backfill mistakes without violating integrity.
- Backfill order should be: add columns and table, dual-write new rows for fresh sessions, optionally backfill historical snapshots, then populate `latest_diff_snapshot_id` for rows where a snapshot exists.
- `sessions.latest_diff_snapshot_id` should not become required; historical rows and partially migrated rows may legitimately have cached `sessions.diff` without a typed snapshot row.

### 3. Build gaps explicitly for top, middle, and bottom

`FileDiffSection` should stop treating context expansion as "special rendering between hunk i-1 and hunk i". Instead it should derive a render sequence:

- optional top gap
- hunk 0
- middle gap 1
- hunk 1
- ...
- optional bottom gap

For middle gaps, the hidden range can be derived from adjacent hunk line numbers.

For top gaps:

- hidden range is `1 .. firstVisibleLine-1`

For bottom gaps:

- the UI needs to know whether more file lines exist after the last visible line

### 4. Centralize diff collection semantics

Introduce a small service boundary such as:

`internal/services/sessiondiff/`

Responsibilities:

- record `base_commit_sha` after clone and before agent edits
- collect the authoritative cached diff
- dual-write the latest session cache plus typed snapshot history
- expose provenance metadata for review and PR-adjacent flows

Recommended diff command:

```bash
git diff --find-renames --binary <base_commit_sha> -- .
```

This gives the existing UI a parseable patch while making the semantics explicit and stable even if local commits appear in the workspace later.

Implementation note:

- Plain `git diff <base_commit_sha> -- .` does not capture untracked files by itself, so the collector must explicitly detect and append untracked-file additions to the authoritative diff artifact.
- The service should own that normalization logic so adapters and worker helpers cannot drift.

### 5. Extend the API response to include range metadata

The current API returns only `lines[]`, which is enough for a single read but weak for directional navigation, especially for bottom-of-file behavior.

Recommended response:

```json
{
  "lines": [...],
  "start_line": 81,
  "end_line": 100,
  "has_more_above": true,
  "has_more_below": true,
  "total_lines": 248
}
```

Why this helps:

- The UI can render top and bottom expanders accurately.
- "Show all" can request the remainder without guessing.
- The frontend does not need to infer EOF from undersized responses.

Backend work:

- Extend `sandbox.FileReader` with a richer context response, or add a new method for range reads with metadata.
- Update `SessionFileHandler.GetFileContext`.
- Update the Docker file reader to compute `total_lines` or at least `has_more_above` / `has_more_below`.

If we want a smaller first step, we can keep the existing endpoint for phase 1 and add metadata only when implementing bottom-of-file expansion.

### 6. Replace midpoint fetching with directional fetching

Current behavior fetches a chunk centered inside the hidden gap. That prevents repeated movement from the current boundary.

Instead:

- `Show above` should fetch lines adjacent to the current top visible boundary.
- `Show below` should fetch lines adjacent to the current bottom visible boundary.
- `Show all` should fetch the entire remaining range for that gap.

This makes the UX predictable and matches GitHub's mental model.

### 7. Preserve comment behavior on expanded context lines

Expanded lines become part of the review surface, so they should inherit the existing line-comment behavior:

- unified and split views must map the synthetic context lines to the correct `old` / `new` side
- `commentsByLine` lookup must work for expanded lines
- active comment input should still render under expanded lines

This is already mostly compatible with the existing `DiffLine` shape; the main work is to ensure expanded ranges continue to reuse `DiffHunk` / `SplitDiffHunk` rendering paths.

Anchor model:

- Comments on expanded unchanged lines should be anchored by `(file_path, side, line_number)` rather than by hunk-relative position.
- `side` should match GitHub-style semantics: unchanged context can be anchored on the right/new side by default, while deleted lines remain left/old side anchors.
- If a cached diff is later refreshed and the exact anchor line no longer exists, the comment should remain attached to the prior diff snapshot rather than silently rebinding to a different line.
- The UI may show such comments as historical or outdated after refresh, but it should not mutate their anchor in place.

### 8. Keep hunk keyboard navigation stable

`DiffPane` currently navigates by `[data-hunk-header]`.

That is acceptable for phase 1, but context navigation introduces two follow-on questions:

- Should expanded context blocks count as navigation stops?
- Should `j` / `k` move only among changed hunks, while context expansion uses mouse buttons only?

Recommendation:

- Keep keyboard navigation scoped to actual diff hunks for now.
- Expanded context blocks should not introduce new keyboard stops in the first version.

### 9. Keep PR #468 behavior, but reduce long-term coupling

PR creation should remain snapshot-backed. That was the right change for shipping fidelity.

What should change over time is the abstraction boundary:

- keep `pr_creation_state`, `pr_creation_error`, and `snapshot_key` behavior intact in the near term
- eventually move PR-side snapshot restore behind a higher-level workspace resolver rather than letting PR code own raw snapshot semantics
- ensure both review diffs and PR pushes point back to the same immutable `base_commit_sha`

### 10. Snapshot-backed file context when no container is alive

The current file-context endpoint requires a live Docker container, which means the moment a session finishes and its sandbox is torn down, `Show 20 above` / `Show 20 below` stop working — even though the workspace state is already persisted as a snapshot tar in object storage and PR creation already reads from it. Reviewing a completed session is the most common reason to expand context, so this is the worst possible failure mode.

GitHub does not run a sandbox to serve `Show more` clicks. It reads from a stored artifact (git objects). We should follow the same shape: read from the artifact we already have (`session.snapshot_key`), without rehydrating any container.

Recommended shape:

- Introduce a `WorkspaceReader` interface in `internal/services/sandbox/` (or a new `internal/services/workspace/` package) with a method comparable to today's `ReadFileContext(ctx, sessionID, path, line, above, below) (FileContextResult, error)`.
- Two implementations:
  - `liveContainerReader` — wraps the existing `sandbox.FileReader` Docker exec path.
  - `snapshotReader` — fetches the session's snapshot tar from `storage.SnapshotStore.Load`, locates the entry at `<workdir>/<path>`, slices the requested line range, and computes `total_lines`, `has_more_above`, `has_more_below` from the file content.
- `SessionFileHandler.GetFileContext` selection order:
  1. live container (`session.ContainerID != nil`) → existing fast path
  2. otherwise, persisted snapshot (`session.SnapshotKey != nil`) → snapshot reader
  3. otherwise, return the existing `NO_SANDBOX` error so the disabled-expander UI still kicks in

Caching:

- The snapshot tar is immutable once a session's snapshot is finalized, so the read path can safely cache.
- Recommended cache shape: extract the tar to a host-local LRU directory keyed by `snapshot_key` on first read, then serve subsequent reads of the same session as ordinary local-disk file reads.
- Cap the cache by total disk size with simple LRU eviction. The cache is purely a performance accelerator; correctness must not depend on its presence.

Cold-path latency:

- First read after sandbox teardown pays a tar fetch from object storage. For typical session workspaces this is in the few-hundred-millisecond range, which is acceptable for a discrete user action like "Show 20 above".
- Hot reads (same session, same or different file) are local-disk fast.

Path safety:

- Reuse the existing `validatePath` helper for query input.
- Add a tar-entry whitelist that rejects absolute paths, `..` traversal, symlinks pointing outside the workdir, and entries above a per-file size cap. The container path already does the analogous checks via `resolvePathInWorkDir`; the snapshot path needs its own equivalent because tar archives can contain hostile entries.

Out of scope for this section:

- Rehydrating a Docker container for a completed session (still a non-goal).
- Pushing every session's workspace to a git remote so that file content can be read via `git show` (the GitHub-style approach). That is a stronger model — it also gives blame and stable permalinks — but it is significantly bigger infra work, and our current snapshot already contains everything we need for read-only file navigation. Revisit if PR-style permalinks become a separate goal.
- Backfilling snapshots for historical sessions that completed before snapshot-backed PR creation shipped. Those will keep the disabled-expander UX.

## Phased Plan

### Phase 1: Establish accurate cached diff provenance

Scope:

- add `base_commit_sha`, `diff_collected_at`, and `latest_diff_snapshot_id`
- add `session_diff_snapshots`
- introduce centralized diff collection with dual-write
- keep existing session API responses backward compatible

Notes:

- keep `sessions.diff`, `sessions.diff_stats`, `sessions.diff_history`, `pr_creation_state`, and snapshot-backed PR creation intact during rollout
- this phase is additive and should not change the UI behavior yet

### Phase 2: Directional middle-gap expansion

Scope:

- Replace one-shot midpoint gap expansion between hunks with directional expansion.
- Support repeated `Show 20 above`, `Show 20 below`, and `Show all` within middle gaps.
- Keep using the existing file-context endpoint initially.
- Read from the same cached diff path introduced in phase 1.

Frontend changes:

- Refactor `ContextExpander` into a directional control component.
- Replace `expandedGaps: Map<number, DiffLine[]>` with richer gap state.
- Render partially expanded gap content plus remaining controls.
- Add tests covering multiple sequential expansions.

Backend changes:

- None required if we accept a limited first pass that works only for known middle-gap bounds.

### Phase 3: Top and bottom file navigation

Scope:

- Add expanders above the first hunk and below the last hunk.
- Support repeated movement toward file start/end.

Frontend changes:

- Teach `FileDiffSection` to derive top/bottom gaps.
- Render top and bottom control rows consistently with middle gaps.

Backend changes:

- Extend the context API with range metadata or total line count.
- Add handler and file reader tests for the new response fields.

### Phase 4 (Deferred): Rich diff metadata and cleanup

> **Status:** deferred — not required for a functioning feature. The data layer for this work is already in place (Phase 1 dual-writes to `session_diff_snapshots`); what is missing is a dedicated endpoint that surfaces it, a read-path migration off `diff_history` JSONB, and an abstraction cleanup. Cleared out of the critical path; documented here only so the cleanup is not forgotten if a future change makes it newly worthwhile.

Scope:

- add a dedicated session diff endpoint with explicit provenance metadata
- start migrating read paths away from `diff_history` JSONB
- introduce a higher-level workspace resolver abstraction for PR-adjacent code if needed

Recommended response shape:

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

### Phase 5 (Deferred): Polish and scale

> **Status:** deferred — opportunistic polish, not required for a functioning feature. Pulled out of the critical path; left here as a parking spot for ideas worth picking up if reviewer load or large-file behavior justifies them.

Scope:

- Better button copy and loading states.
- Optional sticky mini-controls for long expanded ranges.
- Performance tuning for large files and repeated expansions.

Possible optimizations:

- Cache fetched context ranges per file and merge overlapping reads.
- Highlight expanded lines in a subtler style than changed lines.
- Add analytics for expansion usage to validate reviewer demand.
- Optionally support explicit diff refresh for sessions with a live container.

### Phase 6: Snapshot-backed file context for sessions without a live container

Scope:

- Stop returning `NO_SANDBOX` for any session that still has a usable workspace snapshot.
- Serve `Show 20 above`, `Show 20 below`, and `Show all` from the persisted snapshot tar without restoring a Docker container.
- Preserve today's behavior for sessions that have a live container (no regression on the hot path) and for sessions that have neither a container nor a snapshot (continue to render the disabled expander UI).

Backend changes:

- Introduce the `WorkspaceReader` interface and the two implementations described in technical design section 10.
- Update `SessionFileHandler.GetFileContext` (and `GetFileContent` / `ListFiles` if we want symmetric coverage) to select between live and snapshot readers using `session.ContainerID` and `session.SnapshotKey`.
- Add a host-side LRU cache for extracted snapshots, keyed by `snapshot_key`, with a configurable disk cap.
- Add path-safety validation specific to tar entries (reject absolute paths, `..`, symlinks escaping the workdir, oversize entries).
- Add a metric or log for "snapshot reader fallback used" so we can see how often live vs. snapshot reads happen and how often neither is available.

Frontend changes:

- None required for the happy path — the existing `getFileContext` API contract is unchanged.
- Optional: stop eagerly flipping `contextUnavailable` after the very first 409, since most sessions will succeed via the snapshot reader. The lifted state we shipped in PR #679 still applies as the terminal fallback.

Open questions:

- Should the snapshot reader populate `is_live: false` (or a similar flag) on the response so the UI can subtly indicate "this content is from the saved workspace, not a live filesystem"? Probably yes once Phase 4 lands a richer response shape; not required for Phase 6 itself.
- Does `ListFiles` (the repo explorer) deserve the same fallback? That gives reviewers a full read-only file tree of a finished session, which is high-value but also a bigger surface area. Defer until Phase 6 is in production for context expansion.

Out of scope:

- Rehydrating a sandbox container.
- Pushing session state to a git remote so file content can be read via git plumbing (revisit if we want permalinks, blame, or external sharing).
- Backfilling snapshots for sessions that finished before snapshot-backed PR creation shipped — those continue to fall through to the disabled-expander UI.

## Testing Requirements

### Phase 1 ownership

Backend:

- migration tests for `base_commit_sha`, `diff_collected_at`, `latest_diff_snapshot_id`, and `session_diff_snapshots`
- store tests for writing latest snapshot metadata onto `sessions`
- service tests for authoritative diff collection against `base_commit_sha`
- service tests covering tracked edits, deletions, local commits, and untracked file inclusion
- compatibility tests proving existing session payloads still populate `diff`, `diff_stats`, and `diff_history`

### Phase 2-3 ownership

Frontend:

- `context-expander` tests for directional fetch requests
- `file-diff-section` tests for top, middle, and bottom gaps
- split and unified rendering tests for expanded context
- tests for repeated expansion and `Show all`
- tests that comments still attach to expanded lines and preserve `(file_path, side, line_number)` anchors

Backend:

- handler tests for richer file-context metadata response fields
- file-reader tests for start-of-file and end-of-file ranges
- tests for very small files, large requested windows, and sessions where context expansion is unavailable because no sandbox can be read

### Phase 4-5 ownership (deferred)

Listed for future reference; no current owner. If the deferred work is picked back up:

Backend:

- handler tests for dedicated session diff metadata responses
- resolver tests if PR-adjacent workspace abstraction is introduced

Frontend:

- tests for provenance metadata display
- tests for disabled expander UX when only cached diff data is available

### Phase 6 ownership

Backend:

- `snapshotReader` unit tests covering: a file at the requested line range, requests near start-of-file and end-of-file, requests beyond `total_lines`, missing files, and missing snapshots.
- tar-safety tests: absolute paths, `..` traversal, symlinks pointing outside the workdir, oversize entries, and empty archives.
- handler integration tests for `GetFileContext` selection order: live container present → live path; container gone but snapshot present → snapshot path; neither → `NO_SANDBOX`.
- LRU cache tests covering eviction, concurrent reads of the same `snapshot_key`, and corrupted-cache recovery (cache corruption should not surface as a 500; the reader should re-fetch from object storage).

Frontend:

- regression tests confirming that a successful response served by the snapshot reader is rendered identically to a live-container response.
- the existing `contextUnavailable` UX continues to work when the backend returns `NO_SANDBOX` (sessions with neither container nor snapshot).

## Risks

### 1. Incorrect line-number mapping in split view

Expanded context lines use both old and new line numbers. If gap range math is off by one, comments and visual anchors will land on the wrong lines.

### 2. Bottom-of-file behavior is under-specified today

The current API cannot confidently tell the UI how much unchanged content remains after the last hunk without extra metadata.

### 3. Rendering complexity will keep growing if gap state stays ad hoc

It is tempting to keep patching `expandedGaps`, but that will make top/bottom support and repeated expansion brittle. This feature is the point where the diff renderer should adopt an explicit gap model.

### 4. Snapshot reads can be slow on cold paths

The first `Show 20 above` after a sandbox tear-down requires a tar fetch from object storage. For unusually large workspaces or remote object stores with high latency this can be visibly slower than a live read. Mitigations: per-snapshot LRU on host disk, prefetch on diff-page load, and a clear loading state. Watch for tail latency metrics after Phase 6 ships.

### 5. Hostile tar entries

Snapshots are produced by our own pipeline today, but the snapshot reader should still treat tar contents as untrusted input. A bug in the agent loop or a malicious workspace could write a tarball with absolute paths, `..` traversal, or oversized files. The path-safety checks in Phase 6 must run before any host-side filesystem write.

### 6. Snapshot drift from cached diff

The cached `sessions.diff` and the snapshot are written at slightly different points in the session lifecycle. If they disagree (e.g., the diff was captured at turn N and the snapshot at turn N+1), expanded context could show lines that don't match the diff hunks. Once Phase 1 lands and the snapshot has an explicit `head_commit_sha`, the snapshot reader should validate that the diff and the snapshot share a head commit, and fall back to disabled-expander UX if they drift.

### 7. Snapshot prefix drift on repo rename

The Phase 6 handler computes the in-tar workspace prefix from the *current* `repo.FullName` → `slug`. If a repository is renamed or transferred between snapshot capture and review, the recomputed slug no longer matches the prefix that was actually written into the tar, so `ListDir` / `ReadFileContext` surface FILE_NOT_FOUND even though the data is present at the legacy prefix. Phase 1 should store the in-tar prefix alongside `snapshot_key` on the session row so the reader uses the prefix that was actually written, not one inferred at read time. Until then, renames are rare enough that the failure mode (disabled-expander UI for renamed repos' historical sessions) is acceptable.

## Recommendation

Build this as a targeted refactor of `FileDiffSection` and `ContextExpander`, not as a brand-new diff renderer.

The backend already has the critical primitive: read arbitrary file line windows from the sandbox. The frontend is the main blocker. The only backend extension I would treat as likely necessary is richer metadata for top/bottom navigation.

The lowest-risk sequence is:

1. Establish accurate cached diff provenance first so the review surface has one authoritative diff contract before new navigation behavior is layered on top.
2. Rework middle-gap expansion to be directional and repeatable against that stable cached diff.
3. Add top and bottom gaps with metadata support.
4. Add the snapshot-backed file-context reader (Phase 6) so that a session's review surface keeps working after its sandbox container is torn down — the most common state for a session under review.
5. (Deferred) Migrate richer metadata, JSONB read paths, and reviewer polish later if the surface justifies it.
