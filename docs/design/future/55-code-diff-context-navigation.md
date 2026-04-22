# Design: Accurate, Navigable Session Diffs

> **Status:** Not Started | **Last reviewed:** 2026-04-21

> Unifies two gaps in the current review surface: GitHub-style movement through surrounding file context and diff snapshots that are explicitly tied to the immutable branch basis for the session.

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
- Supporting context expansion for sessions with no sandbox container.
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

## Recommended Technical Design

### 0. Define the review diff as a cached snapshot with provenance

Keep the current fast load path:

- render the diff UI from cached session diff data
- fetch surrounding file lines on demand from the sandbox

But change the contract of the cached diff so it means:

> the session workspace state compared against the immutable `base_commit_sha` recorded when the workspace was created

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

### Phase 4: Rich diff metadata and cleanup

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

### Phase 5: Polish and scale

Scope:

- Better button copy and loading states.
- Optional sticky mini-controls for long expanded ranges.
- Performance tuning for large files and repeated expansions.

Possible optimizations:

- Cache fetched context ranges per file and merge overlapping reads.
- Highlight expanded lines in a subtler style than changed lines.
- Add analytics for expansion usage to validate reviewer demand.
- Optionally support explicit diff refresh for sessions with a live container.

## Testing Requirements

Frontend:

- `context-expander` tests for directional fetch requests
- `file-diff-section` tests for top, middle, and bottom gaps
- split and unified rendering tests for expanded context
- tests for repeated expansion and "show all"
- tests that comments still attach to expanded lines

Backend:

- store tests for the new `base_commit_sha` and latest snapshot metadata
- migration tests for `session_diff_snapshots`
- service tests for authoritative diff collection against `base_commit_sha`
- handler and file-reader tests for richer file-context metadata
- compatibility tests proving existing session payloads still populate `diff`, `diff_stats`, and `diff_history`

Backend:

- handler tests for new metadata response fields
- file-reader tests for start-of-file and end-of-file ranges
- tests for very small files and large requested windows

## Risks

### 1. Incorrect line-number mapping in split view

Expanded context lines use both old and new line numbers. If gap range math is off by one, comments and visual anchors will land on the wrong lines.

### 2. Bottom-of-file behavior is under-specified today

The current API cannot confidently tell the UI how much unchanged content remains after the last hunk without extra metadata.

### 3. Rendering complexity will keep growing if gap state stays ad hoc

It is tempting to keep patching `expandedGaps`, but that will make top/bottom support and repeated expansion brittle. This feature is the point where the diff renderer should adopt an explicit gap model.

## Recommendation

Build this as a targeted refactor of `FileDiffSection` and `ContextExpander`, not as a brand-new diff renderer.

The backend already has the critical primitive: read arbitrary file line windows from the sandbox. The frontend is the main blocker. The only backend extension I would treat as likely necessary is richer metadata for top/bottom navigation.

The lowest-risk sequence is:

1. Rework middle-gap expansion to be directional and repeatable.
2. Add top and bottom gaps with metadata support.
3. Polish keyboard behavior and caching afterward.
