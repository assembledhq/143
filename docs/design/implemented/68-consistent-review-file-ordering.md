# Consistent Review File Ordering

> **Status:** Implemented | **Last reviewed:** 2026-06-30

## Problem

The session detail review UI shows the same changed files in two different surfaces:

- the center diff viewer renders files in the parsed diff order
- the side `Changes` tab renders a grouped file tree

Those two surfaces drifted because the file tree renderer split each directory level into `directories first` and `files second`, even when the parsed diff order interleaved them. A simple case like `README.md`, then `src/...`, then `docs/...` would therefore render as `src/`, `docs/`, `README.md` in the sidebar while the diff viewer stayed in the original order.

## Decision

The changed-files sidebar should preserve the parsed diff's order exactly.

- When a grouped tree can represent that order, keep the grouped tree and preserve the **incoming first-seen order** of child entries at every directory level instead of applying an extra directory-first sort.
- When a grouped tree would still reorder the file sequence because a directory reappears after another sibling, fall back to a flat exact-order file list for that session state.

## Prior Limitation

A grouped tree with unique directory nodes cannot perfectly mirror every flat diff ordering. If the diff order is:

1. `src/a.ts`
2. `README.md`
3. `src/b.ts`

then a single `src/` node necessarily groups `a.ts` and `b.ts` together. The old implementation accepted that divergence. The current implementation detects that case and switches to a flat ordered list instead of forcing the grouped tree.

## Implementation

- `frontend/src/components/code-review/file-tree.tsx` renders tree children in insertion order instead of `dirs` then `files`
- `frontend/src/components/code-review/file-tree.tsx` also detects when the grouped tree would change the leaf-file order and falls back to a flat exact-order list
- `frontend/src/components/code-review/file-tree.test.tsx` covers both the root-file-versus-directory regression and the interleaved-directory fallback case
