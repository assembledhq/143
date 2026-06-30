# Design: Persistent Sessions Layout

> **Status:** Implemented | **Last reviewed:** 2026-06-30

The sessions surface uses a persistent `/sessions` shell that owns the list, selection, and detail pane. The selected session id is derived from the route, not from locally duplicated selection state. Navigating between sessions updates the address and selected id while preserving the mounted shell, sidebar filters, search state, optimistic rows, list pagination, and detail-pane frame.

This is a frontend architecture refactor. It should not require API or database changes.

## Implementation Notes

The first implementation keeps `/sessions`, `/sessions/new`, and `/sessions/:id` as valid App Router pages, but those pages are thin route markers. `SessionsLayout` owns the visible content through a persistent shell, derives route state from the selected layout segment, and keys the detail content by selected session id so detail-local state resets while the sidebar shell stays mounted.

Resolved product decisions:

- Bare `/sessions` shows an explicit no-selection workspace state. It does not auto-select the first visible session, because automatic navigation would make mobile/sidebar behavior less predictable and could fight users changing filters or search.
- `/sessions/new` keeps the normal sessions sidebar shell on desktop. Creation stays part of the sessions workspace rather than becoming a separate full-width task surface.
- A link audit found primary navigation and post-auth/post-org flows use `/sessions` as a workspace destination, while explicit creation entry points already use `/sessions/new` or the create-session dialog. No caller needed migration from `/sessions` to `/sessions/new`.

## Problem

Before this refactor, the sessions route tree had the right product instincts but an awkward ownership boundary:

- `frontend/src/app/(dashboard)/sessions/layout.tsx` kept the sidebar mounted and preloaded the heavy detail chunk.
- `/sessions/[id]` still owned the session detail page lifecycle.
- `/sessions` rendered the manual-session composer, while `/sessions/new` also existed as the explicit create route.
- The sidebar already derived `selectedId` from the selected route segment, but the detail pane itself was still mounted by the `[id]` child route.

That shape made route navigation do more work than the product model needed. Moving from one session to another should feel like selecting a row in a persistent workspace, not like leaving one page and entering another page. The old shape also made `/sessions` carry two meanings: the sessions workspace and the new-session composer.

## Product Principle

The sessions page is a workspace. The route selects what the workspace is looking at.

```text
/sessions           -> sessions shell, no selected session
/sessions/:id       -> same shell, selected session id = :id
/sessions/new       -> same shell, create-session content pane
```

The selected id is addressable and shareable, but it should not own the shell lifecycle.

## Goals

1. Keep the sessions shell mounted across session-to-session navigation.
2. Treat the session id as route-derived selected state.
3. Preserve sidebar state across detail changes: search, filters, people scope, pagination, keyboard focus, optimistic session rows, and hover/polling guards.
4. Preserve deep links for `/sessions/:id`.
5. Keep `/sessions/new` as the explicit create route.
6. Improve perceived responsiveness when switching sessions.
7. Reduce route-specific special cases over time by centralizing session route interpretation.

## Non-Goals

1. Changing session API contracts.
2. Changing backend session, thread, transcript, diff, preview, or PR state models.
3. Rebuilding the session detail UI.
4. Removing the existing dynamic import or detail-cache seeding optimizations.
5. Changing mobile split-pane behavior beyond making it consistent with the new route model.

## Target Route Ownership

Introduce a persistent sessions shell component, for example `SessionsShellContent`, mounted at the `/sessions` route boundary. It owns the content pane decision:

| Route | Shell state | Content pane |
|---|---|---|
| `/sessions` | `selectedSessionId = null`, `mode = index` | Empty/no-selection state |
| `/sessions/:id` | `selectedSessionId = id`, `mode = detail` | `SessionDetailContent id={id}` |
| `/sessions/new` | `selectedSessionId = null`, `mode = create` | `ManualSessionCreatePageContent` |
| `/sessions/:id/...` | `mode = unsupported` | Unsupported-route state until explicitly modeled |

The shell should keep using the existing `SessionSidebar`, `OptimisticSessionsProvider`, detail preloading, and cache seeding patterns. The important change is that `[id]` no longer owns the detail pane as an independent page lifecycle.

## Route State Helper

Add a small route-state helper or hook so all session-route interpretation lives in one place:

```ts
type SessionsRouteState = {
  mode: "index" | "create" | "detail" | "unsupported";
  selectedSessionId: string | null;
  isCreatingSession: boolean;
  isUnsupportedRoute: boolean;
  mobileShow: "sidebar" | "content";
  routeKey: string;
};
```

Expected behavior:

- `/sessions` returns no selected id and shows the sidebar pane on mobile.
- `/sessions/new` returns `isCreatingSession = true` and shows content on mobile.
- `/sessions/:id` returns the selected id and shows content on mobile.
- Unknown nested session routes return `isUnsupportedRoute = true` until explicitly modeled.

This helper should replace scattered `usePathname()` and `useSelectedLayoutSegment()` checks in the layout, sidebar, and shell.

## Implementation Plan

### Phase 1 - Tests And Route-State Helper

Write failing tests before implementation.

Add route-state tests for:

- `/sessions`
- `/sessions/new`
- `/sessions/session-123`
- query strings preserved by navigation call sites
- mobile pane selection for the three route types

Then add the helper and wire it into `SessionsLayout` without changing visual behavior yet.

### Phase 2 - Persistent Shell

Create `SessionsShellContent` with tests for:

- no selected id renders a no-selection/index state
- create mode renders `ManualSessionCreatePageContent`
- detail mode renders `SessionDetailPageClient` or the dynamic `SessionDetailContent`
- the sidebar/layout wrapper remains outside detail selection

At this stage, `/sessions` should stop rendering the manual composer directly. The composer remains available at `/sessions/new`.

### Phase 3 - Move Detail Ownership

Move detail rendering responsibility from `sessions/[id]/page.tsx` into the persistent shell. Keep `[id]/page.tsx` as thin compatibility glue if the App Router still needs a child page for segment matching, but it should not introduce a new detail owner.

The desired outcome is:

- sidebar remains mounted
- shell remains mounted
- selected id changes from route state
- detail component swaps its `id` prop
- list/search/filter state survives navigation

### Phase 4 - Navigation Cleanup

Update sidebar and table navigation so row activation still calls `router.push('/sessions/:id?...')`, but the push is treated as selection/address update.

Keep:

- detail cache seeding on hover/mousedown
- detail chunk preloading
- filter suffix preservation
- keyboard navigation
- archive/unarchive behavior

Remove or reduce route-specific special cases where possible, especially checks that distinguish `/sessions` and `/sessions/new` in multiple components. The first implementation centralizes this in `sessions-route-state.ts`; `SessionSidebar` and `SessionsLayout` both consume that helper.

### Phase 5 - Verification

Run focused tests for changed files, then the required frontend checks from `frontend/`:

```bash
npm run typecheck
npm run lint
npm run build
```

Because this refactor touches route ownership and large client components, focused Vitest coverage should include at least:

- `sessions/layout.test.tsx`
- `sessions/session-sidebar.test.tsx`
- `sessions/page.test.tsx`
- `sessions/new/page.test.tsx`
- `[id]/session-detail-page-client.test.tsx`
- new shell and route-state tests

## User Experience Benefits

1. **Session switching feels lighter.** Users can move through sessions without the whole workspace feeling like it changed pages.
2. **Sidebar state is stable.** Search text, people filter, status filter, loaded pages, hover guards, and keyboard focus can survive detail changes naturally.
3. **The URL remains useful.** Deep links still point to `/sessions/:id`; browser back/forward still changes selection.
4. **The mental model is cleaner.** `/sessions` is the sessions workspace, `/sessions/new` is create, and `/sessions/:id` is selected detail.
5. **Future split-pane work gets easier.** Desktop multi-pane behavior, mobile pane transitions, and keyboard navigation all share one route-state contract.
6. **Less duplicated routing logic.** Central route interpretation reduces fragile `pathname === ...` checks across the sessions surface.

## Engineering Benefits

1. The route tree better matches the product model: sessions are a persistent workspace with selected state.
2. The detail pane can evolve independently from the shell.
3. Cache seeding and dynamic chunk preloading remain valid but become implementation details of selection, not page transition compensation.
4. Tests can assert route-state behavior once instead of reasserting it indirectly through every page.
5. This creates a cleaner path for future nested session modes, such as a detail subtab route, without remounting the whole workspace.

## Costs

| Cost | Expected size | Notes |
|---|---:|---|
| Frontend route refactor | Medium | Touches App Router page/layout ownership, sidebar selection, and tests. |
| Test updates | Medium | Existing tests assume `/sessions` renders the composer and `[id]` owns detail. |
| Regression risk | Medium | Session detail is a large component with many effects keyed by `id`. |
| Product decision | Small to medium | Need agreement that bare `/sessions` is the workspace/index state, not composer. |
| Rollout complexity | Small | Can ship behind tests without backend migration or data changes. |

This is not a trivial cleanup, but it is contained to frontend route ownership. It should be smaller than a detail-page rewrite and larger than a local component refactor.

## Explicit Risks

### 1. Bare `/sessions` Behavior Change

Before this refactor, `/sessions` rendered the manual-session composer. The implemented model makes `/sessions` the workspace/index route and keeps creation at `/sessions/new`.

Mitigation:

- Keep `/sessions/new` stable.
- Update tests and any internal links that expect bare `/sessions` to mean create.
- Do not redirect bare `/sessions`; it is now the workspace route.
- Preserve explicit creation affordances through `/sessions/new` and the create-session dialog.

### 2. Detail Effects May Assume Mount-Once Semantics

`SessionDetailContent` has many queries, mutations, effects, local states, scroll states, keyboard handlers, and refs keyed directly or indirectly by `id`. If the component instance persists while only `id` changes, stale state can leak between sessions.

The implementation audited these state groups before removing the `[id]` page as the detail owner:

- **Transcript window and scroll restoration:** `activeThreadId`, initial-anchor refs, saved scroll timers, loaded newer-message pages, live-log reconnect timers, `chatPanelScrollToLiveEdgeRef`, and `chatPanelKeyboardControls` are session/thread-specific. These should reset or rehydrate from session-scoped storage when `id` changes.
- **Thread selection and viewed-thread state:** `pendingThreadPreview`, `hasResolvedInitialThreadSelection`, `viewedThreadIds`, `viewedThreadIdsLoadedForSessionId`, and prefetch refs already partially key themselves by session id. Confirm they never carry the previous session's active thread or viewed markers into the next detail.
- **Optimistic composer and send state:** `optimisticMessages`, `optimisticMessageIDRef`, composer text, attachments, references, commands, upload state, queued-send refs, and in-flight agent update refs are tied to the current session. These should clear when selecting another session unless the product explicitly supports per-session draft persistence.
- **Stop/cancel and runtime action state:** `sessionStopRequest`, `sessionStopOutcome`, cancel mutation state, recoverable inbox retry state, and thread archive/revert mutations can show stale pending UI if they survive an id change.
- **Diff and review UI state:** `centerMode`, `detailTab`, `activeFileIndex`, `activeCommentLine`, review URL-param suppression refs, diff revision refs, empty-diff recovery refs, full-screen override, and mobile review drawers should either reset to route/query-derived defaults or be keyed by session id.
- **PR, branch, and repair actions:** local PR/branch/push state, action errors, pending repair/merge flags, auth prompts, resume-attempt refs, and previous PR/branch state refs are session-specific. They need reset-on-id-change behavior so queued/submitting states do not appear on a different session.
- **Accumulated polling cursors:** `fileEventsSinceRef`, `accumulatedFileEvents`, live logs, and any "since" cursor should reset when `id` changes so the new session does not skip earlier events or merge unrelated events.
- **Title and modal state:** rename, mobile detail, review setup, keyboard help, add-thread dialog, draft title, and editable-thread fields should be reviewed case by case. Some can safely close on navigation; others may need session-scoped persistence.

Mitigation:

- Keep tests for switching from one selected session to another and asserting detail-local state does not leak.
- Keep the keyed boundary around the detail pane:

```tsx
<SessionDetailContent key={selectedSessionId} id={selectedSessionId} />
```

This preserves the shell/sidebar while allowing detail-local state to reset safely. Future work can make individual state groups more granular if preserving per-session drafts or review position becomes valuable.

### 3. Mobile Pane Logic Can Regress

The old layout used route path checks to decide whether mobile shows the sidebar or content. The implemented route-state helper centralizes this, but mistakes will be highly visible.

Mitigation:

- Add direct mobile pane tests for `/sessions`, `/sessions/new`, and `/sessions/:id`.
- Keep the existing behavior unless a deliberate product change is made.

### 4. Browser Back/Forward Can Expose State Synchronization Bugs

If selected id, focused row, and detail content are not all route-derived, back/forward may highlight one row while showing another detail.

Mitigation:

- Make route state the source of truth for selected id.
- Treat keyboard focus as separate transient state, not selected state.
- Add tests for selection derived from route segment.

### 5. Query Params May Be Dropped

Session links preserve filters today with `filterSuffix`. During refactor, it is easy to lose `status`, `people`, `repo`, or `search`.

Mitigation:

- Keep the existing filter suffix helper.
- Add tests for generated session hrefs with active filters.

### 6. App Router Constraints May Force Thin Child Pages

Next.js App Router may still require child `page.tsx` files for segment matching, even if the parent shell owns visible content.

Mitigation:

- Accept thin compatibility pages if needed.
- Keep real rendering decisions in the shell and route-state helper.

### 7. Performance Could Regress If Detail Remount Boundaries Are Chosen Poorly

If the shell accidentally remounts on every id change, the refactor fails its main purpose. If the detail never remounts, stale local state may leak.

Mitigation:

- Verify shell/sidebar persistence with tests.
- Key only the detail subtree when needed.
- Keep query keys id-scoped.

## Cost-Benefit Analysis

### Benefits

| Benefit | Impact | Confidence |
|---|---:|---:|
| Better session-switching UX | High | High |
| Stable sidebar/search/filter state | High | High |
| Cleaner route mental model | Medium | High |
| Lower long-term routing complexity | Medium | Medium |
| Better foundation for future split-pane/detail modes | Medium | Medium |

### Costs And Risks

| Cost or risk | Impact | Likelihood |
|---|---:|---:|
| Updating tests and route assumptions | Medium | High |
| Regressing detail state on id changes | High | Medium |
| Breaking mobile pane behavior | Medium | Medium |
| Product confusion around `/sessions` no longer creating directly | Medium | Medium |
| App Router implementation friction | Medium | Low to medium |

### Recommendation

This refactor is worth doing if sessions are expected to remain the primary daily workspace. The quality and user-experience gains are meaningful because the current route model makes a high-frequency action, switching between sessions, behave more like page navigation than selection inside a stable work surface.

The work should be done deliberately, not opportunistically inside an unrelated feature. The most important guardrail is to preserve shell/sidebar mount stability while either keying or carefully resetting detail-local state on `id` changes. With route-state tests and focused detail regression tests in place, the risk is acceptable.

If the team is under short-term delivery pressure, defer this until the next sessions UX pass. If the next planned work touches session navigation, keyboard flows, mobile session panes, or detail subtabs, this should happen first because it reduces future complexity.

## Open Questions

No open product questions remain for the first implementation. Future work may revisit granular per-session draft/review-position preservation, but the implemented boundary intentionally resets detail-local state when the selected session id changes.

## Success Criteria

1. Switching between two sessions does not remount the sidebar.
2. Sidebar search/filter state survives session-to-session navigation.
3. `/sessions/:id` remains directly loadable and shareable.
4. `/sessions/new` remains the explicit create-session route.
5. Mobile still shows sidebar for `/sessions` and content for `/sessions/new` and `/sessions/:id`.
6. Frontend typecheck, lint, build, and focused route tests pass.
