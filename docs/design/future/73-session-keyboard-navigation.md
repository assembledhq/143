# Design: Session Keyboard Navigation

> **Status:** Not Started | **Last reviewed:** 2026-05-07
>
> **Depends on:** [../03-frontend.md](../03-frontend.md), [../implemented/45-global-command-palette.md](../implemented/45-global-command-palette.md), [../implemented/36-code-review-display.md](../implemented/36-code-review-display.md)

## Problem

The Sessions area is an operator console, but the main session workflow still assumes a mouse for several high-frequency actions:

- selecting the next or previous session in the left sidebar
- paging through a long transcript
- switching agent tabs inside a session
- opening and closing the session detail panel
- creating, pushing, repairing, viewing, or merging a PR
- moving focus directly to the follow-up composer
- discovering which shortcuts exist

The codebase already establishes a keyboard-first baseline in [../03-frontend.md](../03-frontend.md): list pages should support `j` / `k`, and the command palette should stay globally available via `Cmd+K` / `Ctrl+K`. The diff review surface has its own scoped shortcuts in `useDiffKeyboardNav`, but the broader session shell does not yet have an equivalent navigation model.

## Current State

### Surfaces

- `/sessions` renders a table in `frontend/src/app/(dashboard)/sessions/sessions-page-content.tsx`. Rows are clickable, but they are not an arrow-key navigable collection.
- `/sessions/*` uses `SessionSidebar` from `frontend/src/app/(dashboard)/sessions/session-sidebar.tsx`. Session rows are links inside swipe/action wrappers. They support normal tab focus, but there is no roving selection, next/previous session command, or keyboard archive shortcut.
- Session detail lives in `frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx`. It has:
  - a transcript scroll container inside `ChatPanel`
  - a shared follow-up `SessionComposer`
  - a right-side/mobile `Session details` panel with Overview, Changes, Validation, and Preview tabs
  - an `AgentTabStrip` for same-sandbox agent tabs
  - PR actions in the detail panel and PR health banner
- Diff review already supports scoped single-key shortcuts through `frontend/src/hooks/use-diff-keyboard-nav.ts`: `j` / `k`, `n` / `p`, `f`, `u`, `s`, `e`, `c`, `x`, `m`, `Enter`, `Esc`, and `?`.

### Gaps

- `j` / `k` are already used inside diff review for file navigation, but not at the session-shell level.
- The session transcript scroll container is not directly focusable and has no page/up/down command bridge.
- Agent tabs rely on Radix Tabs focus behavior when focused, but there is no global "next/previous agent tab" command.
- `Shift+Tab` should not be used for tab switching. It is a browser and assistive-technology focus traversal command, and overriding it would make keyboard accessibility worse.
- PR actions are available as buttons, but users must tab to them or use the command palette indirectly.
- The code review help overlay exists, but the session shell has no equivalent `?` help surface.

## Design Goals

- Make the common session workflow usable without a mouse.
- Reuse established conventions instead of inventing app-specific gestures:
  - `Cmd+K` / `Ctrl+K` for global command/search
  - `j` / `k` and arrow keys for list-style movement
  - `PageUp` / `PageDown`, `Home`, and `End` for scroll regions
  - `Ctrl+Tab` / `Ctrl+Shift+Tab` for next/previous tabs
  - `[` / `]` as an app-scoped fallback for previous/next tab where browser handling of `Ctrl+Tab` cannot be intercepted
  - `Escape` to close the currently open transient panel
  - `?` for keyboard help
- Never intercept normal typing in inputs, textareas, selects, content-editable areas, command palette input, dialogs, or menus.
- Keep shortcuts scoped by mode so chat, session navigation, and diff review do not fight each other.
- Make shortcuts discoverable through tooltips, command palette hints, and one help overlay.

## Interaction Model

### Keyboard Layers

Use three layers, in priority order:

| Layer | Scope | Examples |
|---|---|---|
| Text entry | Inputs, textareas, content-editable, command palette | Normal typing, composer `Enter`, mention picker arrows |
| Modal/transient surfaces | Dialogs, sheets, dropdowns, command palette | `Escape` closes, `Tab` traps or follows primitive behavior |
| Session shell | `/sessions` and `/sessions/:id` when not typing and no transient surface is active | session navigation, transcript paging, panel toggles, PR actions |

Diff review remains its own mode. When `review=active`, keep the existing diff shortcuts authoritative. Session-shell shortcuts that remain valid in review should be limited to globally useful commands such as `?`, focus composer, and close details if they do not conflict.

### Navigation State

The sidebar and table should use a roving active item:

- The active item is initialized from the selected session route when present.
- `j` / `ArrowDown` moves the active item to the next visible session row.
- `k` / `ArrowUp` moves the active item to the previous visible session row.
- `Enter` opens the active item.
- `Space` peeks the active item once a preview surface exists; until then, do not bind `Space`.
- `Home` / `End` move to the first/last loaded visible row.
- If the user reaches the end of loaded rows and there is a `Show more` cursor, `PageDown` may load the next page and keep the active item near the same relative position.

Use `aria-activedescendant` or roving `tabIndex` on the list/table container, and expose row labels that include title, status, PR state, and updated time. Keep normal links inside rows so browser link behavior remains available.

## Proposed Shortcuts

### Global And App Shell

| Shortcut | Action | Notes |
|---|---|---|
| `Cmd+K` / `Ctrl+K` | Open command palette | Already implemented globally. |
| `?` | Open keyboard shortcuts overlay | Session shell version; diff review may show diff-specific help. |
| `Esc` | Close open sheet/menu/dialog, then detail panel if it was opened by shortcut | Do not navigate away from a session. |

Cross-section navigation (Sessions, Projects, Automations, etc.) belongs in the command palette. Two-key `g`-sequences add a hidden mode without saving a meaningful number of keystrokes over `Cmd+K` plus a few characters. Do not advertise unimplemented shortcuts in the command palette; the palette design requires shortcut hints to correspond to registered shortcuts.

### Session List And Sidebar

| Shortcut | Action | Notes |
|---|---|---|
| `j` | Move to next visible session | Works on `/sessions` table and session sidebar. Always means "next session" regardless of where focus lives in the session shell. |
| `k` | Move to previous visible session | Same. |
| `ArrowDown` / `ArrowUp` | Same as `j` / `k`, but only when the sidebar/table has roving focus | When focus is not in the list, arrow keys belong to the transcript (see Session Transcript). |
| `Enter` | Open focused/active session | Preserve current filters via existing `filterSuffix`. |
| `Home` | Move to first loaded session | Standard list behavior. |
| `End` | Move to last loaded session | Standard list behavior. |
| `PageDown` | Move down one viewport of rows; load more near end | Do not steal when transcript scroll container is focused. |
| `PageUp` | Move up one viewport of rows | Same. |
| `/` | Focus session search | Standard search shortcut in list contexts. |
| `a` | Archive/unarchive active session | Only when the session list/sidebar has active roving focus; use a confirmation or undo toast if needed. |
| `n` | New session | Sessions-context shortcut; equivalent to the visible New session action. |

### Session Transcript

| Shortcut | Action | Notes |
|---|---|---|
| `ArrowDown` | Scroll transcript down by one short step | Active when transcript scroll container has focus, or as the default arrow target on session-detail routes when no list/picker has focus. |
| `ArrowUp` | Scroll transcript up by one short step | Same scope. |
| `PageDown` | Scroll transcript down one page | Same scope. |
| `PageUp` | Scroll transcript up one page | Same scope. |
| `Space` / `Shift+Space` | Scroll transcript down/up | Only if focus is not on an actionable control. |
| `Home` | Jump to top of transcript | Use native scroll semantics. |
| `End` | Jump to latest transcript entry | Same as "Jump to latest". |
| `.` | Jump to latest transcript entry | Optional faster alias if `End` feels too destructive on long pages. |
| `i` | Focus follow-up composer | Works from session detail, including review mode when a composer is available. |
| `Cmd+Enter` / `Ctrl+Enter` | Send composer message | Add as a supplement to current `Enter` send behavior. Keep `Shift+Enter` for newline. |
| `Esc` | Blur composer if focused and empty, or close active picker/sheet first | Do not discard text. |

The transcript scroll container should be focusable with `tabIndex={0}` and an accessible label such as `Session conversation`. This lets screen reader and keyboard users intentionally enter the scroll region, and gives `ArrowUp` / `ArrowDown` an unambiguous target.

Arrow-key disambiguation between sidebar and transcript:

- `j` / `k` are reserved for the sidebar/table session list. They never scroll the transcript.
- `ArrowUp` / `ArrowDown` mean "scroll transcript" on session-detail routes, except when the sidebar list has roving focus (then they mirror `k` / `j`).
- On entering a session via `Enter` from the sidebar, focus moves into the transcript scroll container so arrow keys immediately scroll the conversation. To return arrow control to the sidebar, the user clicks back into it or presses a future "focus sidebar" shortcut. Keeping the two surfaces on different default keys (`j`/`k` vs arrows) avoids needing a focus-mode indicator.

### Agent Tabs

| Shortcut | Action | Notes |
|---|---|---|
| `Ctrl+Tab` | Next agent tab | Established tab-cycling pattern, but browsers may reserve it. Use where reliably catchable. |
| `Ctrl+Shift+Tab` | Previous agent tab | Same. |
| `]` | Next agent tab | App-scoped fallback, easy to learn from diff tools and editors. |
| `[` | Previous agent tab | App-scoped fallback. |
| `t` | Add agent tab | Only in session detail when not typing. |
| `Shift+F4` | Open active tab actions menu | Optional; less discoverable than direct command palette actions. |

Do not use `Shift+Tab` to switch agent tabs. It must remain previous-focus traversal.

### Detail Panel And Detail Tabs

| Shortcut | Action | Notes |
|---|---|---|
| `d` | Toggle session details panel; on open, move focus to the active detail tab trigger | Desktop: right panel. Mobile: bottom sheet. Disabled in review mode if file tree/details are required. |
| `ArrowLeft` / `ArrowRight` | Cycle detail tabs (Overview → Changes → Validation → Preview) when the detail tab strip has focus | Native Radix Tabs behavior. No new key needed; `d` is the way in. |
| `Shift+]` | Next detail tab from anywhere in session detail | Mirrors agent tabs `]` / `[`, with Shift to disambiguate the secondary tab strip. Wraps. |
| `Shift+[` | Previous detail tab from anywhere in session detail | Same. |
| `Esc` | Close details when open and not required; if focus is on a detail tab trigger, return focus to where it came from before closing | Match existing close-button behavior. |
| `r` | Open/return to review diff | Session detail only, when changes exist. |
| `m` | Back to conversation from review | Already used by diff review for maximize/back behavior; preserve existing review semantics. |

The detail tab strip continues to use Radix Tabs so focused tabs support standard arrow-key navigation. The interaction model:

1. `d` opens the panel and focuses the currently active tab trigger.
2. `ArrowLeft` / `ArrowRight` cycles to adjacent tabs (Radix native).
3. `Tab` moves focus into the tab content; `Shift+Tab` returns to the trigger.
4. `Esc` collapses focus back out of the panel and, if pressed again with no focus inside, closes the panel.
5. `Shift+]` / `Shift+[` are accelerators for users who want to cycle without first focusing the strip — symmetric with `]` / `[` for agent tabs but explicitly different so the two tab axes never collide.

This replaces an earlier proposal of `Alt+1`–`Alt+4`. Direct numeric accelerators are tempting but introduce four new bindings for a four-tab surface where most users only switch between two of them in practice (Overview ↔ Changes), and on macOS the `Alt`/`Option` modifier produces special characters in some inputs which makes scoping fragile.

### PR And Shipping Actions

| Shortcut | Action | Notes |
|---|---|---|
| `p` then `c` | Create PR / retry PR creation | Use a two-key sequence to avoid accidental destructive-ish actions. |
| `p` then `v` | View PR | Opens external PR when present. |
| `p` then `p` | Push changes | Only when `has_unpushed_changes` is true. |
| `p` then `t` | Fix tests | Only when PR health exposes failing tests. |
| `p` then `r` | Resolve conflicts | Only when PR health exposes conflicts. |
| `p` then `m` | Merge PR | Must show the existing merge/auth confirmation flow. Never merge immediately on a bare shortcut. |

All PR shortcuts should route through the same disabled-state and auth-intercept logic as the buttons. A shortcut should either perform the same action as clicking the enabled button or focus/open the surface explaining why the action is unavailable.

## Flows To Support

### 1. Triage sessions without a mouse

1. User opens `/sessions` or any `/sessions/:id` route.
2. Presses `j` / `k` or arrow keys to move through sessions.
3. The active row is visibly highlighted and announced.
4. Presses `Enter` to open a session.
5. Current filters and repository scope are preserved.

### 2. Read a long session transcript

1. User opens a session.
2. `PageDown` / `PageUp` scroll the transcript, not the browser page.
3. `End` or `.` jumps to the live edge.
4. `i` focuses the follow-up composer.
5. `Esc` returns from composer focus without losing draft text.

### 3. Work across agent tabs

1. User opens a multi-tab session.
2. `]` / `[` cycles through agent tabs.
3. `Ctrl+Tab` / `Ctrl+Shift+Tab` work as optional platform-conventional aliases where possible.
4. `t` opens the add-tab dialog.
5. The active tab's transcript and detail state update exactly as a click would.

### 4. Inspect and close session details

1. User presses `d` to open/close the detail panel.
2. User presses `Alt+1` through `Alt+4` to switch detail tabs.
3. `Esc` closes the detail sheet/panel if it is not required by the current mode.
4. On mobile, the same shortcut opens the bottom sheet and focus moves into it.

### 5. Ship a completed session

1. User presses `p c` to create or retry PR creation.
2. If GitHub user auth is required, the existing auth prompt opens.
3. Once a PR exists, `p v` opens the PR, `p p` pushes changes if needed, and PR health commands become available based on health state.
4. `p m` opens the merge flow only when the app already considers merge available. It must never bypass existing PR health and confirmation rules.

### 6. Resume writing quickly

1. User presses `i` from anywhere in session detail where typing is not already active.
2. Focus moves to the shared composer.
3. The page does not scroll unexpectedly unless the composer is currently off-screen on mobile.
4. Mention/slash-command picker behavior remains unchanged once typing starts.

### 7. Discover shortcuts

1. User hovers an action button and sees the action plus shortcut, e.g. `Create PR (p c)`.
2. User opens `?` and sees grouped session shortcuts.
3. User opens `Cmd+K` and sees registered shortcut hints on actions that have direct shortcuts.
4. Empty states and list/search controls can mention a single high-value shortcut, e.g. the search input can expose `Focus search (/)` in its tooltip or `aria-keyshortcuts`.

## Discovery

### Tooltips

Use existing shadcn/Radix tooltip primitives. For icon-only buttons, tooltip content should be:

- action label
- shortcut hint in a `<kbd>`-style secondary span
- disabled reason when unavailable

Examples:

- `Show details` + `d`
- `Message agent` + `i`
- `Create PR` + `p c`
- `Add agent tab` + `t`
- `Search sessions` + `/`

Keep `title` only as a fallback for native browser tooltip behavior; prefer the existing `Tooltip` and `DisabledTooltip` patterns for consistent styling.

### Help Overlay

Extend the current code-review-only help overlay into a reusable app component, or create a session-specific overlay beside it. The session overlay should group shortcuts by:

- Navigation
- Conversation
- Agent tabs
- Details and review
- PR actions

The overlay should:

- open with `?`
- close with `?`, `Esc`, close button, or backdrop click
- trap focus while open
- return focus to the previously active element when closed
- use shadcn/ui `Button` and `Table` components rather than raw elements when a matching local component exists

### Command Palette

Registered direct shortcuts should be reflected in `command-palette-actions.ts` for static actions and session-specific command results where practical. Do not show shortcuts that are not implemented.

Useful palette additions:

- `Focus session input`
- `Toggle session details`
- `Create PR`
- `Push changes`
- `Merge PR`
- `Fix PR checks`
- `Resolve PR conflicts`
- `Add agent tab`

These can be command entries even if they are only available on session-detail routes. Disabled or unavailable actions should not clutter the default palette; they can appear after query if there is a clear disabled state.

## Accessibility Requirements

- Add `aria-keyshortcuts` on controls with direct shortcuts where the shortcut maps to a specific visible control.
- Roving list focus must expose a single active descendant and not place every row in the tab order.
- Preserve native `Tab` and `Shift+Tab` focus traversal.
- Do not capture single-letter shortcuts while focus is inside `input`, `textarea`, `select`, content-editable, command dialog, dropdown menu, popover, sheet, or modal dialog.
- Announce active session changes through visible focus and accessible labels. Avoid live-region chatter on every arrow press unless testing shows screen readers need it.
- Keep all actions reachable through normal tab order as well as shortcuts.
- Shortcuts that perform network mutations must use the same confirmation, disabled, auth, and error paths as button clicks.
- Avoid destructive instant actions. Archive can use undo. Merge must use the existing guarded merge flow.

## Implementation Notes

### Shared Shortcut Hook

Add a small `useSessionKeyboardShortcuts` hook for session-shell commands rather than growing ad hoc document listeners in individual components. It should:

- accept booleans for mode (`isSessionRoute`, `isReviewMode`, `isMobile`, `detailsRequired`)
- accept command callbacks for navigation, transcript scroll, composer focus, details, tabs, and PR actions
- ignore text-entry and transient-surface targets
- support two-key sequences with a short timeout, e.g. `p` then `c`
- expose the registered shortcuts to the help overlay so docs and runtime stay aligned

### Sidebar/Table Navigation

`SessionSidebar` and `SessionsPageContent` currently compute visible rows separately. Each surface should keep its own active-row state from the visible row IDs and selected route ID. The implementation can start with the sidebar because it is present throughout session detail.

### Transcript Scroll

`ChatPanel` already owns `scrollRef` and exposes `scrollToLiveEdge` through `onRegisterScrollToLiveEdge`. Extend this registration to include:

- `scrollPageDown`
- `scrollPageUp`
- `scrollToTop`
- `scrollToLatest`

This keeps transcript scrolling local to the component that owns the actual scroll container.

### Agent Tabs

`AgentTabStrip` already receives `threads`, `activeThreadId`, and `onActiveThreadChange`. Add next/previous helpers in `SessionDetailContent` and route `[` / `]` through the same callback used by Radix Tabs.

### PR Actions

PR shortcut handlers should call the existing mutation wrappers:

- `createPRMutation.mutate(undefined)`
- `pushChangesMutation.mutate(undefined)`
- `startRepairMutation.mutate("fix_tests")`
- `startRepairMutation.mutate("resolve_conflicts")`
- `mergeMutation.mutate()` through `handleMergeAction`

Before calling any mutation, reuse the same availability booleans used by button rendering. If unavailable, focus/open the detail panel Overview tab so the user can see the current PR state.

## Testing

Frontend tests should be added before implementation:

- sidebar/table list navigation moves active row with `j` / `k`, arrow keys, `Home`, and `End`
- `Enter` opens the active session and preserves filter query params
- shortcuts are ignored while typing in the search input, composer textarea, Linear input, command palette, menus, and dialogs
- transcript paging calls the registered scroll handlers
- `i` focuses the composer without changing draft text
- `d` toggles desktop details and opens mobile details sheet, and on open moves focus to the active detail tab trigger
- `Shift+]` / `Shift+[` cycle detail tabs (Overview → Changes → Validation → Preview) and maintain the current review/preview URL behavior
- `ArrowLeft` / `ArrowRight` cycle detail tabs when the tab strip has focus (Radix native)
- `[` / `]` switch agent tabs
- `ArrowUp` / `ArrowDown` scroll the transcript when focus is in the transcript scroll container, and do not steal sidebar navigation when the list has roving focus
- PR key sequences call the same mutation handlers as buttons and respect disabled/auth states
- `?` opens and closes the help overlay with focus trapped and restored

After frontend implementation, run from `frontend/`:

```bash
npm run typecheck
npm run lint
npm run build
```

## Rollout

1. Add help overlay, shortcut registry, and non-mutating shortcuts (`?`, `i`, transcript paging, details toggle).
2. Add sidebar/table roving navigation.
3. Add agent-tab shortcuts.
4. Add PR command sequences with confirmation/disabled-state parity.
5. Add tooltip and command palette hints only for shortcuts that shipped.

