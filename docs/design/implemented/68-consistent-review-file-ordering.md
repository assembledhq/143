# Consistent Review File Ordering

## Problem

The session detail review UI shows the same changed files in two different surfaces:

- the center diff viewer renders files in the parsed diff order
- the side `Changes` tab renders a grouped file tree

Those two surfaces drifted because the file tree renderer split each directory level into `directories first` and `files second`, even when the parsed diff order interleaved them. A simple case like `README.md`, then `src/...`, then `docs/...` would therefore render as `src/`, `docs/`, `README.md` in the sidebar while the diff viewer stayed in the original order.

## Decision

The grouped file tree should preserve the **incoming first-seen order** of child entries at every directory level instead of applying an extra directory-first sort. This keeps the sidebar aligned with the diff viewer whenever the tree structure can represent the same sequence.

## Limitation

A grouped tree still cannot perfectly mirror every flat diff ordering. If the diff order is:

1. `src/a.ts`
2. `README.md`
3. `src/b.ts`

then a single `src/` node necessarily groups `a.ts` and `b.ts` together, so one of the two surfaces must diverge. In those cases the goal is not perfect identity; it is to keep the tree as faithful as possible by honoring the first appearance of each sibling entry and avoiding any additional re-sorting.

## Implementation

- `frontend/src/components/code-review/file-tree.tsx` now renders tree children in insertion order instead of `dirs` then `files`
- `frontend/src/components/code-review/file-tree.test.tsx` covers the root-file-versus-directory ordering regression
