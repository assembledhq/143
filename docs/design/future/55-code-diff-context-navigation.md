# Design: Code Diff Context Navigation

> **Status:** Not Started | **Last reviewed:** 2026-04-21

> Extends the existing code review display so a reviewer can move up and down through surrounding file context directly from the diff, similar to GitHub's "show more context" flow.

## Problem

The current review surface shows parsed diff hunks and supports a single context expansion path only for gaps between two hunks. That is not enough for real review work:

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

## Current State

The current implementation is split across:

- `frontend/src/components/code-review/file-diff-section.tsx`
- `frontend/src/components/code-review/context-expander.tsx`
- `frontend/src/components/code-review/diff-pane.tsx`
- `internal/api/handlers/session_files.go`
- `internal/services/sandbox/docker_filereader.go`

Current behavior:

- The diff parser produces only hunks and lines from the patch.
- `FileDiffSection` computes hidden lines only between consecutive hunks.
- `ContextExpander` fetches a single window centered in that gap and replaces the gap with one synthetic context hunk.
- The session file API already supports arbitrary line-window reads through `GET /api/v1/sessions/{id}/files/context`.
- The underlying diff itself still comes from stored session diff text, not a live repo walk when the reviewer opens the page.

Current limitations:

- No UI model for "hidden context before first hunk" or "after last hunk".
- No persistent gap state such as `expanded_start`, `expanded_end`, `remaining_above`, `remaining_below`.
- No incremental "show more above" / "show more below" controls after an expansion.
- The current expander fetches from the midpoint of a gap, which is fine for one-shot reveal but wrong for directional navigation.
- The current test coverage only exercises the one-shot gap insertion path.
- The API can fetch surrounding file lines, but it does not yet expose enough metadata to tell the UI whether there is still hidden content above or below the visible region.

## Goals

1. Let reviewers expand context above or below their current visible region without leaving the diff.
2. Support top-of-file, between-hunk, and bottom-of-file navigation.
3. Preserve inline comments, keyboard navigation, syntax highlighting, and split/unified modes.
4. Keep the implementation compatible with session sandboxes and the existing file-context API shape where possible.

## Non-Goals

- Replacing the repo explorer.
- Rendering entire large files by default.
- Building a full editor with arbitrary scrolling through unchanged files.
- Supporting context expansion for sessions with no sandbox container.

## Recommended Product Behavior

Each file section should behave like a sequence of visible diff blocks separated by expandable context gaps.

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

## Recommended Technical Design

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

### 2. Build gaps explicitly for top, middle, and bottom

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

### 3. Extend the API response to include range metadata

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

### 4. Replace midpoint fetching with directional fetching

Current behavior fetches a chunk centered inside the hidden gap. That prevents repeated movement from the current boundary.

Instead:

- `Show above` should fetch lines adjacent to the current top visible boundary.
- `Show below` should fetch lines adjacent to the current bottom visible boundary.
- `Show all` should fetch the entire remaining range for that gap.

This makes the UX predictable and matches GitHub's mental model.

### 5. Preserve comment behavior on expanded context lines

Expanded lines become part of the review surface, so they should inherit the existing line-comment behavior:

- unified and split views must map the synthetic context lines to the correct `old` / `new` side
- `commentsByLine` lookup must work for expanded lines
- active comment input should still render under expanded lines

This is already mostly compatible with the existing `DiffLine` shape; the main work is to ensure expanded ranges continue to reuse `DiffHunk` / `SplitDiffHunk` rendering paths.

### 6. Keep hunk keyboard navigation stable

`DiffPane` currently navigates by `[data-hunk-header]`.

That is acceptable for phase 1, but context navigation introduces two follow-on questions:

- Should expanded context blocks count as navigation stops?
- Should `j` / `k` move only among changed hunks, while context expansion uses mouse buttons only?

Recommendation:

- Keep keyboard navigation scoped to actual diff hunks for now.
- Expanded context blocks should not introduce new keyboard stops in the first version.

## Phased Plan

### Phase 1: Directional middle-gap expansion

Scope:

- Replace one-shot midpoint gap expansion between hunks with directional expansion.
- Support repeated `Show 20 above`, `Show 20 below`, and `Show all` within middle gaps.
- Keep using the existing file-context endpoint initially.
- Leave diff provenance untouched in this phase; this work should layer cleanly on top of the separate branch-accurate diff snapshot plan.

Frontend changes:

- Refactor `ContextExpander` into a directional control component.
- Replace `expandedGaps: Map<number, DiffLine[]>` with richer gap state.
- Render partially expanded gap content plus remaining controls.
- Add tests covering multiple sequential expansions.

Backend changes:

- None required if we accept a limited first pass that works only for known middle-gap bounds.

### Phase 2: Top and bottom file navigation

Scope:

- Add expanders above the first hunk and below the last hunk.
- Support repeated movement toward file start/end.

Frontend changes:

- Teach `FileDiffSection` to derive top/bottom gaps.
- Render top and bottom control rows consistently with middle gaps.

Backend changes:

- Extend the context API with range metadata or total line count.
- Add handler and file reader tests for the new response fields.

### Phase 3: Polish and scale

Scope:

- Better button copy and loading states.
- Optional sticky mini-controls for long expanded ranges.
- Performance tuning for large files and repeated expansions.

Possible optimizations:

- Cache fetched context ranges per file and merge overlapping reads.
- Highlight expanded lines in a subtler style than changed lines.
- Add analytics for expansion usage to validate reviewer demand.

## Testing Requirements

Frontend:

- `context-expander` tests for directional fetch requests
- `file-diff-section` tests for top, middle, and bottom gaps
- split and unified rendering tests for expanded context
- tests for repeated expansion and "show all"
- tests that comments still attach to expanded lines

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
