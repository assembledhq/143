# 36 - Code Review Display

> **Status:** Implemented | **Last reviewed:** 2026-05-06
>
> **Implementation notes:** The rich diff viewer is shipped: parsed file/hunk rendering, unified and split modes, syntax highlighting, file tree navigation, inline review comments, keyboard navigation, repo explorer integration, basic between-hunk context expansion, and scroll-synced active-file highlighting between the diff pane and file tree. PR creation has also moved onto snapshot-backed workspace pushes. The two remaining design-area concerns — (1) GitHub-style incremental context navigation from the current diff position and (2) stronger diff provenance so the rendered diff is explicitly tied to an immutable branch basis instead of an ad hoc stored patch — were spun out into [55-code-diff-context-navigation.md](55-code-diff-context-navigation.md).

> A rich, GitHub-quality code review experience embedded in each session, with inline commenting that feeds directly into the next agent pass.

## Problem

The current diff viewer is a raw `<pre>` block with basic color coding. It has no syntax highlighting, no file grouping, no line numbers, no way to expand context, and no ability to leave comments. Developers reviewing agent-generated code changes are forced to either squint at a flat wall of green/red text or jump to GitHub to do a real review — breaking their flow and losing the ability to feed corrections back to the agent inline.

For a tool targeting developers, the code review surface is the highest-leverage UI. It's where trust is built or lost. If a developer can't quickly scan changes, understand context, and drop precise feedback, they'll treat the agent as a black box instead of a collaborator.

## Design Principles

1. **GitHub-familiar, not GitHub-clone** — Use conventions developers already know (unified diff, file tree, inline comments) but optimize for the agent feedback loop, not PR ceremony.
2. **Context on demand** — Show the diff by default, but let users pull in surrounding code and browse the full repo without leaving the session.
3. **Comments are commands** — Every inline comment is a directive to the agent. The review UI is the steering wheel, not just a read-only display.
4. **Keyboard-first** — File navigation, comment creation, and approval should all be possible without touching a mouse.
5. **Progressive disclosure** — Start with the diff summary, expand to file-level detail, drill into full repo exploration. Never force all the complexity up front.

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│ Session Detail Page                                             │
│ ┌───────────────────────┬──────────────────────────────────────┐│
│ │ Chat Panel            │ Detail Panel                         ││
│ │                       │ [Overview] [Changes] [Validation]    ││
│ │                       │                                      ││
│ │                       │  ┌──────────────────────────────────┐││
│ │                       │  │ Code Review Display              │││
│ │                       │  │                                  │││
│ │                       │  │  File Tree │ Diff / File Viewer  │││
│ │                       │  │            │                     │││
│ │                       │  │            │  inline comments    │││
│ │                       │  │            │  context expansion  │││
│ │                       │  │            │  syntax highlighting│││
│ │                       │  └──────────────────────────────────┘││
│ │                       │                                      ││
│ │  ┌──────────────────────────────────────────────────────────┐││
│ │  │ Footer: Status │ +433 / -33 (clickable)                 │││
│ │  └──────────────────────────────────────────────────────────┘││
│ └───────────────────────┴──────────────────────────────────────┘│
└─────────────────────────────────────────────────────────────────┘
```

The code review display lives inside the existing **Changes tab** of the session detail panel. When the diff stat badge in the session footer is clicked, the Changes tab opens and scrolls to the diff.

For full-screen review, a **maximize button** expands the Changes tab to take over the full viewport (hiding the chat panel), giving developers the horizontal space they need for side-by-side diffs and full-file browsing.

---

## Detailed Design

### 1. Diff Stats Badge (Entry Point)

A compact, always-visible badge in the session row and session header that shows change volume and acts as the primary entry point to the review.

**Location:** Two places:
- **Session sidebar row** — next to the status dot, e.g. `+433 / -33`
- **Session header bar** — right-aligned, next to the status badge

**Behavior:**
- Click opens the Changes tab in the detail panel
- If the detail panel is collapsed, clicking the badge opens it
- Badge colors: green text for additions, red for deletions (following git convention)
- Badge shows nothing when `session.diff` is empty/null

**Calculation:** Parse `session.diff` on the frontend:
```typescript
function parseDiffStats(diff: string): { added: number; removed: number } {
  let added = 0, removed = 0;
  for (const line of diff.split('\n')) {
    if (line.startsWith('+') && !line.startsWith('+++')) added++;
    else if (line.startsWith('-') && !line.startsWith('---')) removed++;
  }
  return { added, removed };
}
```

### 2. File Tree Panel

A collapsible left panel within the Changes tab that lists all modified files, grouped by directory.

```
┌─────────────────────────────────┐
│ 12 files changed               │
│ ┌─────────────────────────────┐ │
│ │ 🔍 Filter files...          │ │
│ ├─────────────────────────────┤ │
│ │ ▾ src/components/           │ │
│ │   ● diff-viewer.tsx   +120  │ │
│ │   ○ file-tree.tsx      +85  │ │
│ │ ▾ src/lib/                  │ │
│ │   ● api.ts             +12  │ │
│ │ ▾ internal/api/             │ │
│ │   ○ sessions.go        +45  │ │
│ └─────────────────────────────┘ │
└─────────────────────────────────┘

● = has comments    ○ = no comments
```

**Features:**
- **Directory grouping** with collapsible folders
- **File filter** — text input to search file names (useful for large diffs)
- **Change stats per file** — `+N / -M` next to each file name
- **Comment indicators** — filled dot for files with comments, hollow for none
- **Review status per file** — optional checkmark when a file has been reviewed (persisted locally, not to DB)
- **Click to scroll** — clicking a file scrolls the diff pane to that file's section
- **Scroll-linked active selection** — scrolling the diff pane updates the tree highlight to the file currently at the top of the reading position
- **Keyboard nav** — `j`/`k` to move between files, `Enter` to jump to diff

**Ordering strategy:** Files are ordered to optimize review flow:
1. Files with existing comments (need attention)
2. Largest changes first (most impactful)
3. Alphabetical within each group

### 3. Diff Viewer (Core)

Replaces the current `DiffViewer` component with a rich, interactive diff display.

#### 3a. Syntax Highlighting

Use a lightweight syntax highlighter that works on diff content. Options:

- **Shiki** (recommended) — VS Code-quality highlighting, supports 200+ languages, works server-side and client-side. Tree-shakeable. Theme can match the app's dark/light mode.
- Fallback: Prism.js if bundle size is a concern.

Each line in the diff gets syntax-highlighted according to its file extension, with the diff coloring (green/red background) layered on top.

#### 3b. Unified and Split View

Toggle between two display modes:

**Unified view** (default):
```
  10   const foo = bar;
  11 - const result = oldFunction(foo);
     + const result = newFunction(foo, options);
  12   return result;
```

**Split view:**
```
  10  const foo = bar;              │  10  const foo = bar;
  11  const result = oldFunction(…) │  11  const result = newFunction(…)
  12  return result;                │  12  return result;
```

- Toggle via toolbar button or keyboard shortcut (`u` for unified, `s` for split)
- Preference is persisted in localStorage
- Split view requires the maximized/full-width layout to be useful

#### 3c. Line Numbers

Two-column line numbers (old file / new file) displayed in a gutter to the left of the code. Styled in `text-muted-foreground` at `text-[11px]`.

- Clicking a line number selects/highlights that line
- Shift-clicking selects a range
- Line numbers are links (URL updates to `#file-path-L42` for shareability)

#### 3d. Context Expansion

By default, show the standard unified diff context (3 lines above/below each hunk). Between hunks, show a clickable expander:

```
  45   return result;
  ─── Show 20 hidden lines ───    ← click to expand
  66   function nextThing() {
```

**Expansion behavior:**
- Click loads 20 more lines of context from the full file
- "Show all" option expands the entire gap
- Requires a new API endpoint to fetch file content (see API section)

#### 3e. File Headers

Each file section has a sticky header:

```
┌──────────────────────────────────────────────────────────┐
│ src/components/diff-viewer.tsx        +120 / -8     📋 🔗│
└──────────────────────────────────────────────────────────┘
```

- **File path** — full path, clickable to open in the file explorer
- **Stats** — additions/deletions for this file
- **Copy button** (📋) — copies the file path
- **Link button** (🔗) — opens the file in the full repo explorer
- Sticky positioning so the header stays visible while scrolling through a long file diff

### 4. Inline Comments

The core differentiator: every line in the diff is commentable, and comments feed directly into the agent's next pass.

#### 4a. Adding Comments

- **Hover** any line to see a `+` icon appear in the gutter
- **Click the `+`** to open a comment input box below that line
- **Keyboard shortcut**: `c` while a line is selected opens the comment box
- Comment box is a simple textarea with markdown support
- **Submit**: `Cmd+Enter` or click "Add comment"
- **Cancel**: `Escape`

```
  11 + const result = newFunction(foo, options);
     ┌──────────────────────────────────────────────┐
     │ This should handle the null case for `foo`.   │
     │ Consider adding a guard clause.               │
     │                                               │
     │              [Cancel]  [Add comment ⌘↵]       │
     └──────────────────────────────────────────────┘
  12   return result;
```

#### 4b. Comment Display

Comments appear inline, below the line they reference:

```
  11 + const result = newFunction(foo, options);
     ┌──────────────────────────────────────────────┐
     │ 💬 You (just now)                             │
     │ This should handle the null case for `foo`.   │
     │                              [Edit] [Delete]  │
     └──────────────────────────────────────────────┘
```

- Comments show author (always "You" for now, multi-user later), timestamp
- Editable and deletable
- Multiple comments on the same line stack vertically
- Comments are visually distinct from code (subtle background, left border accent)

#### 4c. Comment Resolution

Comments have two states:
- **Open** — visible inline, will be included in agent directives
- **Resolved** — collapsed to a single line, excluded from next pass

A resolved comment shows as:
```
  11 + const result = newFunction(foo, options);
     ── ✓ 1 resolved comment ──    ← click to expand
```

#### 4d. Inline Comment Composer Width On Long Lines

When a diff line is very long and forces horizontal scrolling, the inline comment composer must not expand into a huge full-width interaction surface. The core usability goal is to keep the composer actions physically close to the text input and visually close to the line anchor, so users can type and submit without chasing buttons across a very wide row.

The current implementation risk is a comment area rendered as a full-width block below the diff row. That preserves alignment with the diff, but on long lines it makes the composer feel detached from its own actions.

Preferred design direction:

- Render the composer as a **bounded card** anchored to the commented line, not as a full-width strip.
- Keep the editable surface in a readable range such as **420-560px** wide on desktop.
- Place the action row directly under the textarea inside the same bounded card.
- Let the diff maintain its own horizontal scroll behavior independently from the comment composer.
- Fall back to full width only on narrow mobile/tablet breakpoints.

Potential layout options:

**Option A: Anchored floating card below the line** (recommended)

```
line content ────────────────────────────────────────────────>
           ┌──────────────────────────────────────┐
           │ Leave a comment…                     │
           │                                      │
           │         Cancel   Add comment  ⌘↵     │
           └──────────────────────────────────────┘
```

- The diff row stays full-width and horizontally scrollable.
- The composer appears below the selected line, left-aligned near the gutter/comment anchor.
- The composer card has a max width and does not attempt to span the hunk.
- This is the best tradeoff between implementation cost and UX improvement.

**Option B: Right-side comment rail**

```
┌──────────────────────────────┬──────────────────────┐
│ diff row (scrolls sideways)  │ comment composer     │
│                              │ actions stay fixed   │
└──────────────────────────────┴──────────────────────┘
```

- Reserve a fixed-width rail on the right for active composers and threads.
- The code pane scrolls horizontally while the comment controls stay stable.
- Strong option for dense review workflows, but more invasive because it changes the overall diff balance and reduces code width.

**Option C: Full-width thread background with bounded inner composer**

```
┌────────────────────────────────────────────────────────────┐
│ full-width thread background                              │
│  ┌──────────────────────────────────────┐                  │
│  │ Leave a comment…                     │                  │
│  │         Cancel   Add comment  ⌘↵     │                  │
│  └──────────────────────────────────────┘                  │
└────────────────────────────────────────────────────────────┘
```

- Keep the existing full-width inline thread zone for continuity with the diff.
- Constrain only the actual composer card inside that zone.
- Lowest-risk visual change, but still leaves some of the oversized-container feel in place.

Recommendation:

- Ship **Option A** first.
- If the review surface later evolves toward many simultaneous open threads, revisit **Option B**.
- Avoid designs where the action bar itself spans the full thread width. Even if the thread container remains wide, the actionable cluster should stay compact.

#### 4e. Comments Summary Panel

A collapsible panel at the top of the Changes tab:

```
┌──────────────────────────────────────────────────────────┐
│ 3 comments (2 open, 1 resolved)     [Send to agent ▶]   │
│                                                          │
│ • diff-viewer.tsx:11 — "Handle null case for foo"        │
│ • api.ts:45 — "Use consistent error format"              │
│ • ✓ sessions.go:23 — "Add validation" (resolved)         │
└──────────────────────────────────────────────────────────┘
```

- Lists all comments with file + line references
- Click any comment to scroll to its location in the diff
- **"Send to agent"** button compiles open comments into a structured directive and sends it as a message in the chat panel

#### 4f. Sending Comments to the Agent

When the user clicks "Send to agent", comments are formatted as a chat message:

```
Please address the following code review comments:

1. src/components/diff-viewer.tsx:11
   "This should handle the null case for `foo`. Consider adding a guard clause."

2. src/lib/api.ts:45
   "Use consistent error format here — see the pattern in sessions.ts"
```

This message appears in the chat panel and triggers a new agent turn. The agent can then make changes and produce a new diff, starting the review cycle again.

### 5. Full Repository Explorer

A dedicated mode within the Changes tab that lets users browse the full repository, not just changed files.

#### 5a. Entry Points

- Click any file path in the diff to open it in the explorer
- Click the 🔗 icon in a file header
- Toolbar button: "Browse repository"
- File tree panel shows a toggle: "Changed files" / "All files"

#### 5b. File Browser

```
┌─────────────────────────────────────────────────────────┐
│ Repository Browser              [← Back to diff]        │
│ ┌──────────┬────────────────────────────────────────────┐│
│ │ ▾ src/   │  src/components/diff-viewer.tsx            ││
│ │  ▾ comp/ │  ─────────────────────────────────         ││
│ │   ● diff │  1  interface DiffViewerProps {            ││
│ │     file │  2    diff: string;                        ││
│ │     tree │  3  }                                      ││
│ │  ▸ lib/  │  4                                         ││
│ │  ▸ app/  │  5  export function DiffViewer(...) {      ││
│ │ ▸ inter/ │  ...                                       ││
│ └──────────┴────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────┘
```

- Full directory tree on the left
- File content with syntax highlighting on the right
- Changed lines in browsed files are highlighted with a subtle gutter indicator (yellow bar) so you can see modifications in context
- Line numbers are clickable for commenting (same as in diff view)
- Breadcrumb navigation at the top: `src / components / diff-viewer.tsx`

#### 5c. API Requirement

The repo explorer requires fetching file content and directory listings from the container:

```
GET /api/v1/sessions/{id}/files              → directory listing
GET /api/v1/sessions/{id}/files/{path}       → file content
GET /api/v1/sessions/{id}/files/{path}?context_around=L45&lines=20  → context expansion
```

These endpoints proxy to the Docker container running the session's sandbox, executing `git show` or reading from the working tree.

### 6. Diff-Between-Passes View

When an agent runs multiple passes (e.g., after receiving review comments), show what changed between passes — a "diff of diffs."

```
┌──────────────────────────────────────────────────────────┐
│ Viewing: [Pass 1 → Pass 2 ▾]    [Pass 1 → Latest ▾]    │
│                                                          │
│  Changes in this pass:                                   │
│  11 - const result = newFunction(foo, options);          │
│  11 + const result = foo ? newFunction(foo, options)     │
│  12 +   : defaultResult;                                 │
│                                                          │
│  ── addresses comment: "Handle null case for foo" ──     │
└──────────────────────────────────────────────────────────┘
```

**Implementation:** Store the diff string at each pass. When the user selects a pass range, compute the delta between the two diffs. Link resolved comments to the pass that addressed them.

**Data model addition:**
```sql
ALTER TABLE sessions ADD COLUMN diff_history jsonb DEFAULT '[]';
-- Each entry: { "pass": 1, "diff": "...", "timestamp": "..." }
```

### 7. Session Footer Bar

A new persistent footer bar at the bottom of the session view (below the chat panel).

```
┌──────────────────────────────────────────────────────────┐
│ ● Running  │  Turn 5/8  │  +433 / -33  │  3 comments    │
└──────────────────────────────────────────────────────────┘
```

- **Status** — session status with StatusDot
- **Turn counter** — current/total turns
- **Diff stats** — clickable, opens Changes tab. Green for `+`, red for `-`
- **Comment count** — number of open review comments, clickable to open comment summary
- Fixed to bottom of the session view, always visible
- Height: `h-8`, consistent with the app's dense rhythm

### 8. Keyboard Shortcuts

All shortcuts follow the app's keyboard-first design principle:

| Key | Action |
|-----|--------|
| `f` | Toggle file tree panel |
| `j` / `k` | Next / previous file in file tree |
| `n` / `p` | Next / previous change hunk |
| `Enter` | Jump to selected file in diff |
| `c` | Add comment on selected line |
| `Cmd+Enter` | Submit comment |
| `Escape` | Cancel comment / close panel |
| `u` | Unified diff view |
| `s` | Split diff view |
| `x` | Expand context around cursor |
| `e` | Toggle repo explorer mode |
| `m` | Maximize/restore Changes panel |
| `?` | Show keyboard shortcut help |

Shortcuts are scoped to the Changes tab — they don't interfere with global app shortcuts or the chat panel.

### 9. Maximized Review Mode

When the developer needs more horizontal space (especially for split view), the Changes tab can be maximized to fill the viewport.

**Trigger:** Click the maximize icon in the Changes tab header, or press `m`.

**Behavior:**
- Chat panel slides out to the left (hidden, not destroyed)
- Changes tab takes full width
- A "minimize" button or `m` / `Escape` restores the original layout
- URL updates to include `?review=maximized` so the state is preserved on refresh

This is critical for split-view diffs and repo exploration — these features are nearly unusable in a narrow side panel.

---

## API Changes

### New Endpoints

```
GET  /api/v1/sessions/{id}/files
     Returns directory listing from the session's container.
     Response: { data: [{ path, type: "file"|"dir", size }] }

GET  /api/v1/sessions/{id}/files/{path}
     Returns file content from the session's container.
     Query params: ?ref=HEAD (git ref, defaults to HEAD)
     Response: { data: { path, content, language } }

GET  /api/v1/sessions/{id}/files/{path}/context
     Returns additional context lines around a specific line.
     Query params: ?line=45&above=10&below=10
     Response: { data: { lines: [{ number, content }] } }

POST /api/v1/sessions/{id}/review-comments
     Creates a review comment on a specific line.
     Body: { file_path, line_number, side: "old"|"new", body }
     Response: { data: ReviewComment }

GET  /api/v1/sessions/{id}/review-comments
     Lists all review comments for a session.
     Response: { data: ReviewComment[], meta: { next_cursor } }

PATCH /api/v1/sessions/{id}/review-comments/{comment_id}
      Updates a comment (edit body, resolve/unresolve).
      Body: { body?, resolved? }

DELETE /api/v1/sessions/{id}/review-comments/{comment_id}

POST /api/v1/sessions/{id}/review-comments/send
     Compiles open comments into a message and sends to the agent.
     Response: { data: { message_id } }
```

### Modified Endpoints

```
GET  /api/v1/sessions/{id}
     Add to response: diff_stats: { added, removed, files_changed }
     This avoids client-side parsing of the full diff just to show the badge.
```

---

## Data Model

### New Table: `review_comments`

```sql
CREATE TABLE review_comments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    org_id UUID NOT NULL REFERENCES organizations(id),
    user_id UUID NOT NULL REFERENCES users(id),
    file_path TEXT NOT NULL,
    line_number INTEGER NOT NULL,
    diff_side TEXT NOT NULL DEFAULT 'new' CHECK (diff_side IN ('old', 'new')),
    body TEXT NOT NULL,
    resolved BOOLEAN NOT NULL DEFAULT false,
    resolved_at TIMESTAMPTZ,
    pass_number INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_review_comments_session ON review_comments(session_id);
CREATE INDEX idx_review_comments_session_file ON review_comments(session_id, file_path);
```

### Modified Table: `sessions`

```sql
ALTER TABLE sessions ADD COLUMN diff_stats jsonb;
-- { "added": 433, "removed": 33, "files_changed": 12 }

ALTER TABLE sessions ADD COLUMN diff_history jsonb DEFAULT '[]';
-- [{ "pass": 1, "diff": "...", "diff_stats": {...}, "created_at": "..." }]
```

`diff_stats` is computed server-side when the diff is stored, avoiding repeated client-side parsing.

---

## Component Hierarchy

```
ChangesTab
├── ReviewToolbar
│   ├── ViewToggle (unified/split)
│   ├── PassSelector (dropdown for diff-between-passes)
│   ├── RepoExplorerToggle
│   └── MaximizeButton
├── CommentsSummary
│   ├── CommentList (clickable, scrolls to location)
│   └── SendToAgentButton
├── FileTreePanel (collapsible left)
│   ├── FileFilter (search input)
│   └── FileTreeItem[] (grouped by directory)
├── DiffPane (main content)
│   ├── FileDiffSection[] (one per file)
│   │   ├── FileDiffHeader (sticky)
│   │   ├── DiffHunk[]
│   │   │   ├── DiffLine[] (syntax highlighted)
│   │   │   │   ├── LineGutter (line numbers, comment icon)
│   │   │   │   └── LineContent (highlighted code)
│   │   │   └── CommentThread[] (inline below commented lines)
│   │   └── ContextExpander (between hunks)
│   └── EmptyState (no diff available)
└── RepoExplorer (alternate mode)
    ├── DirectoryTree
    ├── FileBreadcrumb
    └── FileViewer (syntax highlighted, commentable)
```

---

## Frontend Dependencies

| Package | Purpose | Size |
|---------|---------|------|
| `shiki` | Syntax highlighting (VS Code quality) | ~2MB (lazy loaded per language) |
| `diff` (npm) | Diff parsing and computation for pass-to-pass comparison | ~15KB |

No other new dependencies. The file tree, comment system, and layout components are built with existing shadcn/ui primitives (`ScrollArea`, `Collapsible`, `Textarea`, `Button`, `Tooltip`, `DropdownMenu`).

Shiki grammars should be loaded lazily — only fetch the grammar for a language when a file of that type appears in the diff. This keeps the initial bundle small.

---

## Implementation Phases

### Phase 1: Rich Diff Viewer (replaces current DiffViewer)
- Parse unified diff into structured data (files, hunks, lines)
- Syntax highlighting with Shiki
- File headers with stats and sticky positioning
- Line numbers (old/new gutter)
- File tree panel with directory grouping
- Diff stats badge in session header (clickable, opens Changes tab)

### Phase 2: Context & Navigation
- Context expansion between hunks (requires file content API)
- Keyboard navigation (j/k files, n/p hunks)
- Unified/split view toggle
- Maximized review mode
- Session footer bar with diff stats

### Phase 3: Inline Comments
- Comment creation on any line
- Comment display, editing, deletion
- Comment resolution
- Comments summary panel
- "Send to agent" — compile comments into chat message
- `review_comments` table and API endpoints

### Phase 4: Full Repo Explorer
- Directory tree and file browser
- File content API endpoints (proxy to container)
- Syntax-highlighted file viewer
- Changed-line indicators in browsed files
- Breadcrumb navigation

### Phase 5: Diff Between Passes
- Store diff history per session
- Pass selector dropdown
- Delta computation between passes
- Link resolved comments to addressing pass

---

## UX Considerations

### Why Not Just Link to GitHub?

GitHub's review UI is excellent, but it breaks the agent feedback loop. Opening GitHub means context-switching, writing a PR comment that the agent can't see, and waiting for a CI-triggered re-run. The inline comment system here closes the loop: comment → send → agent fixes → new diff → review again. The cycle time drops from minutes to seconds.

### Handling Large Diffs

Agent-generated diffs can be large (1000+ lines across dozens of files). Mitigations:
- **File tree with stats** lets you prioritize which files to review
- **Collapse reviewed files** to reduce visual noise
- **File ordering by impact** (largest changes first) surfaces important files
- **Search within diff** to find specific patterns
- **Diff stats in file headers** so you can skip trivial changes (e.g., `+1 / -1` import fixes)

### Comment Persistence

Comments are stored server-side in `review_comments`. This means:
- Comments survive page refreshes
- Comments are associated with a specific pass number
- If the agent re-runs and produces a new diff, old comments on changed lines are marked as "outdated" (similar to GitHub's stale comment UX)
- Resolved comments remain accessible but collapsed

### Mobile / Narrow Viewport

The code review display is not designed for mobile. At viewports below 768px, the Changes tab shows a simplified view: file list with expandable diffs (no file tree panel, no split view). This is acceptable — code review is a desktop activity.

### Accessibility

- All interactive elements are keyboard-accessible
- Line numbers and file names use semantic HTML (`<a>`, `<button>`)
- Color is never the only indicator — additions/deletions also use `+`/`-` prefix text
- Comment threads use `aria-label` for screen reader context
- Focus management: opening a comment box focuses the textarea, closing returns focus to the line
