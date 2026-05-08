# Design: Global Command Palette

> **Status:** Implemented | **Last reviewed:** 2026-04-21
>
> **Depends on:** [03-frontend.md](../03-frontend.md), [34-repo-ribbons-nav.md](../backlog/34-repo-ribbons-nav.md)

## Problem

The app already assumes keyboard-first usage in multiple places, but there is still no single global entry point for navigation, search, and quick actions.

Today a user must:

- click through the sidebar to reach major surfaces
- open the avatar menu to discover settings destinations
- manually scan Sessions or Projects to jump to a specific item
- use the separate repo context switcher for repository scope changes

That is workable for first-run exploration, but not for daily usage. In a multi-repo, agent-heavy app, this creates too much UI travel and too little discoverability.

This is especially visible because [03-frontend.md](../03-frontend.md) already establishes `Cmd+K` as a cross-cutting UX pattern, but the product does not yet implement it.

## Design Goal

Provide a single palette that lets a user:

- open from any authenticated dashboard page with `Cmd+K` / `Ctrl+K`
- navigate to core pages and settings without hunting through menus
- jump directly to sessions and projects
- switch repository context without leaving the keyboard
- launch a new manual session from the current query when no exact match exists

The palette is not a second sidebar and not a mini page builder. It is a fast control surface for navigation, lightweight actions, and entity jump-to.

## Principles

### 1. Keyboard-first, always reachable

`Cmd+K` / `Ctrl+K` must open the palette regardless of where focus currently is — including inside `input`, `textarea`, `select`, and content-editable regions. This matches the convention established by Linear, VS Code, GitHub, and Slack. Users often want the palette most when they're already typing in a search box or form field. Suppressing the shortcut in those contexts would break the "always available" contract.

**Note:** The existing `useDiffKeyboardNav` hook explicitly skips shortcuts when modifier keys are held. The command palette shortcut handler must be registered independently (e.g., a dedicated `useEffect` in `AuthenticatedLayout`) — do not layer it on top of the existing keyboard nav system.

The palette itself must not consume normal typing when it is closed. When open, `Escape` closes it and restores focus to the previously active element. If the previously active element has been unmounted (e.g., a modal closed while the palette was open), fall back to focusing the palette trigger button.

### 2. Empty-state useful, typed-state precise

Opening the palette with no query should still be valuable. The default view should show high-signal actions, recent items, and repository switching. Typing should narrow results quickly and predictably.

### 3. Static actions are instant; dynamic search is additive

Navigation, settings, and quick actions appear immediately on open and are filtered client-side. Dynamic entity results augment the static actions after the user types at least 2 characters.

### 4. Repository context is first-class

This app already has a global repo scope model via the `repo` URL param and `RepoContextSwitcher`. The command palette must integrate with that model, not invent a second context system.

### 5. One mounted surface, no parallel state model

The palette should be mounted once in `AuthenticatedLayout` and controlled there. Do not introduce a separate global provider or duplicate state tree unless implementation pressure proves it necessary.

### 6. AI-forward means action, not novelty

The most useful AI-native behavior is not a chatbot inside the palette. It is letting the user turn an unmatched query into work: "Start manual session: <query>" in the current repository context.

## Scope

### In scope for MVP

- global open/close shortcut
- static navigation and settings actions
- direct session and project search
- repository context switching
- recent items
- "start manual session from query" action

### Explicitly out of scope for MVP

- full-text search across logs, diffs, or audit history
- nested multi-step command trees
- cross-device synced recents
- freeform AI chat inside the palette dialog

## UX Model

### Entry points

- `Cmd+K` on macOS, `Ctrl+K` on Windows/Linux
- a visible palette trigger in the sidebar/header area for discoverability and mouse users

The trigger should use shadcn/ui primitives, not a raw `<button>`. In this repo that means a `Button`-based trigger styled like a compact utility control.

### Default view on open

When the query is empty, show these groups in order:

1. `Recent`
2. `Quick Actions`
3. `Switch Repository`
4. `Navigation`
5. `Settings`

This ordering puts high-frequency creation actions ("New session", "New project") above lower-frequency settings navigation, reflecting the primary workflow of an AI-agent app where session creation is the most common action.

### Query behavior

- Static items filter immediately via cmdk's client-side matching.
- Dynamic entity search starts at 2+ characters.
- Use React's `useDeferredValue` to defer the search query passed to dynamic fetch hooks. This keeps the input responsive without manual debounce timers and integrates naturally with React's concurrent rendering.
- Dynamic results appear above static groups, but static groups remain visible.
- While dynamic results are loading, show a `<Command.Loading>` indicator in the dynamic results area so the user knows a fetch is in progress. Do not block or hide static results during loading.

### Result ranking

cmdk supports a custom `filter` function. Use it to implement recent-first, frequency-aware ranking rather than purely alphabetical matching:

1. Exact prefix matches rank highest.
2. Items the user has visited recently (from the recent-items list) rank above cold matches.
3. Within a tie, items with more recent activity (e.g., session updated more recently) rank higher.

This makes the palette feel predictive rather than encyclopedic.

### Keyboard shortcut hints

Static action rows should display shortcut hints on the right side of the row when a keyboard shortcut exists for that action (e.g., `G then S` for "Go to Sessions"). This makes the palette double as a shortcut discovery surface and helps users graduate from palette usage to direct shortcuts over time. Only display hints for shortcuts that are actually registered in the app — do not show aspirational shortcuts.

### Result anatomy

Each dynamic result row should include enough context to disambiguate entities in a multi-repo product:

- primary label: title or best human-readable fallback
- secondary label: repo short name
- state badge: session status or project status
- optional metadata: updated time or task counts

Avoid raw UUID-only rows unless there is no better label.

### No-result behavior

If the user typed a query and there are no matching entities or actions:

- show a clear empty state
- offer `Start manual session: "<query>"` as the primary action
- preserve current repo context when launching that session flow

That keeps the palette useful even when search misses.

## Information Architecture

### Static groups

The static actions must match the current route structure in the repo, not an abstract future sitemap.

#### Navigation

Sidebar-primary pages (these match the current sidebar `navItems` in `authenticated-layout.tsx`):

- `Autopilot` → `/autopilot`
- `Sessions` → `/sessions`
- `Projects` → `/projects`

Palette-only pages (these routes exist but are not in the sidebar — the palette is the primary keyboard discovery path for them, so labels and icons must be clear):

- `Analytics` → `/analytics`
- `Costs` → `/costs`
- `Automations` → `/automations`

#### Settings and admin destinations

- `General` → `/settings`
- `Integrations` → `/integrations`
- `Coding agents` → `/agent`
- `LLM` → `/llm`
- `Autopilot settings` → `/settings/autopilot`
- `Evals` → `/settings/evals`
- `Team` → `/team`
- `Audit log` → `/settings/audit-log` (`admin` only)

**Role-based filtering:** Items marked `admin` only must be excluded from the palette for non-admin users. Read `user.role` from the existing `useAuth()` hook (already available in `AuthenticatedLayout`) and filter the static action list before passing it to cmdk. Do not render admin items and hide them with CSS — omit them from the data entirely so they are not reachable via keyboard navigation or screen readers.

#### Quick actions

- `New session` → `/sessions/new`
- `New project` → `/projects/new`
- `Create eval task` → `/settings/evals/new`
- `Log out` → imperative action via existing auth hook

### Dynamic groups

#### Sessions

Search across session title first, with fallback matching against related issue title where available.

#### Projects

Search across project title and goal.

#### Repositories

Do not make repository switching a phase-4 add-on. It is part of the global navigation model already and should be present from the first version.

Repository rows should mirror the existing `RepoContextSwitcher` summary model:

- repo full name
- active session count
- most urgent latest status dot

Selecting a repository updates the shared `repo` query state using the same `nuqs` model as the existing switcher.

## Architecture

### Mount point

Mount a single `<CommandPalette />` inside [frontend/src/components/authenticated-layout.tsx](../../frontend/src/components/authenticated-layout.tsx).

That keeps the palette available on every authenticated dashboard route and allows the layout to own:

- open/close state
- shortcut registration
- trigger button wiring
- repo-context preservation

### Recommended structure

```text
frontend/src/
├── components/
│   ├── command-palette/
│   │   ├── command-palette.tsx
│   │   ├── command-palette-trigger.tsx
│   │   ├── command-palette-actions.ts
│   │   ├── use-command-palette-search.ts
│   │   └── use-recent-palette-items.ts
│   └── ui/
│       └── command.tsx          ← does not exist yet
```

**Prerequisite:** `command.tsx` does not exist in the codebase. Run `npx shadcn@latest add command` before starting implementation. This will install the `cmdk` dependency and generate the shadcn `Command` primitives into `frontend/src/components/ui/command.tsx`.

Use the existing `AuthenticatedLayout` as the owner of `open` state rather than creating a dedicated provider.

### State model

- `open` lives in `AuthenticatedLayout`
- the palette query lives inside `CommandPalette`
- the selected repo continues to live in the URL via `useQueryState("repo")`
- recent items live in `localStorage` (see [Recent Items](#recent-items) below)

This matches existing repo patterns: local UI state stays local; shared filter state uses `nuqs`.

### Recent Items

Recent items track palette selections so the most useful destinations appear first on re-open.

- **What counts as recent:** Any palette selection that results in a navigation or entity jump. Static actions (like "Log out") and repo-context switches do not count — they are utility actions, not destinations.
- **Storage:** `localStorage` under the key `143:command-palette:recents`. Value is a JSON array of `{ type, id, label, href, timestamp }` objects.
- **Max entries:** 10. When a new item would exceed the cap, evict the oldest.
- **Deduplication:** By `type + id` (e.g., `session:sess_abc123`). If an item already exists, move it to the front and update its timestamp and label (labels can change).
- **Display:** The `Recent` group renders the most recent 5 items. The full 10 are retained in storage so that removing a stale item still leaves a useful list.
- **Staleness:** Do not proactively prune deleted entities from recents. If a user selects a stale recent and gets a 404, remove it from the list at that point.
- **Cross-tab sync:** Listen for `window.addEventListener("storage", ...)` on the recents key so that a palette opened in a second tab reflects selections made in the first tab without requiring a page reload.

### Trigger wiring

The sidebar trigger and the keyboard shortcut both toggle the same layout-owned state.

Do not rely on a hook returning `setOpen` from inside the palette subtree while the trigger lives elsewhere. That coupling becomes awkward immediately and is unnecessary because the layout already owns both surfaces.

## Search Data Flow

### Frontend

- Static items: filter client-side with cmdk.
- Repositories: use the existing repository summary query already used by `RepoContextSwitcher`.
- Sessions/projects: fetch with TanStack Query when the deferred query length is `>= 2`.

Prefer using the existing centralized query-key factory in [frontend/src/lib/query-keys.ts](../../frontend/src/lib/query-keys.ts) instead of introducing new ad hoc keys in the palette code.

**Note:** `queryKeys.sessions.list` already exists and accepts a `repo` param. `queryKeys.projects` does not yet have a `list` key — add `projects.list` (and a search-accepting variant if needed) to `query-keys.ts` as part of Phase 2 implementation. Also add `repositories.summary` to the factory — `RepoContextSwitcher` currently uses a raw `["repositories", "summary"]` tuple, and the palette should use the centralized key.

### Backend

For MVP, add optional `search` support to the existing list endpoints instead of introducing a new global search endpoint:

- `GET /api/v1/sessions?search=<q>&limit=5`
- `GET /api/v1/projects?search=<q>&limit=5`

This keeps the first version aligned with current backend patterns:

- routes remain under `/api/v1/`
- handlers parse validated query params
- handlers call store/service methods
- queries remain org-scoped
- standard list response shape stays intact

If later we need blended ranking across many entity types, a dedicated `/api/v1/command/search` endpoint can be introduced. Do not pay that complexity cost in v1.

### Backend implementation constraints

Any new search support must follow the existing backend rules:

- every query filters by `org_id`
- handlers remain thin
- business logic belongs in services if ranking logic becomes non-trivial
- db access stays in `internal/db/*` store methods
- tests must verify org scoping and search filtering

## Repo Context Behavior

This is the biggest usability requirement for this app.

### Preserving context on navigation

When the user has a repository selected, palette navigation to repo-scoped list pages should preserve that context:

- `/sessions` should keep `?repo=<id>`
- `/projects` should keep `?repo=<id>`
- `/automations` should keep `?repo=<id>` if the page continues to honor repo scope

Settings pages should not inherit repo context unless that page already supports it.

### Switching repository context

The palette and the existing header switcher must produce identical outcomes:

- selecting `All repositories` clears the `repo` query param
- selecting a repo sets `repo=<repository_id>`
- if the current repo is disconnected, clear the param

Do not create a second persistence rule such as separate localStorage for repo context.

## AI-Forward Behavior

The palette should include one AI-native action path:

- if the query does not match a known entity or action, show `Start manual session: "<query>"`
- selecting it routes to `/sessions/new` with the draft prompt and current repo prefilled, or directly creates a draft session if that flow already exists by implementation time

This makes the palette feel like a control center for an AI product instead of a generic app launcher.

Do not turn the palette itself into a streaming chat surface. That would overload a tool meant for fast movement and lightweight actions.

### Future AI-forward actions (post-MVP)

These are not in scope for v1, but worth capturing as natural extensions:

- **Re-run failed session** — For recently failed sessions surfaced in dynamic search results, offer a "Re-run" action inline. This turns the palette into a recovery tool, not just a navigation tool.
- **Contextual suggestions** — If the user is on a session detail page and opens the palette, surface actions relevant to that session (e.g., "Create project from this session", "View PR") above generic navigation.

## Accessibility

- use shadcn/ui `Command` primitives built on `cmdk`
- keep full keyboard support: open, navigate, select, close
- trap focus while open
- restore focus to the trigger on close when appropriate
- expose clear group labels
- support reduced motion
- ensure loading and empty states are announced accessibly

## Testing Requirements

### Frontend

Add tests for:

- shortcut opens and closes the palette
- shortcut opens the palette even when focus is inside an input or textarea
- `Escape` closes the palette and restores focus to the previously active element
- admin-only actions are excluded from the rendered list for non-admin users
- admin-only actions are present for admin users
- selecting a repo updates URL state
- selecting a repo-scoped navigation item preserves `repo`
- empty search offers `Start manual session: "<query>"`
- recent items render in order, deduplicate by type+id, and cap at 5 visible
- stale recent items are removed on 404 navigation

### Backend

If `search` is added to sessions/projects endpoints, add:

- handler tests for valid and invalid search params
- store tests for search filtering
- tenancy coverage proving `org_id` is still enforced

## Implementation Phases

### Phase 1: Static palette + repo switching

Ship:

- mounted palette in `AuthenticatedLayout`
- keyboard shortcut and trigger button
- static nav/settings/quick actions
- repository context group backed by the existing repo summary query

This is the minimum version that already feels coherent in the current app.

### Phase 2: Session and project search

Ship:

- dynamic search for sessions and projects
- result metadata with repo labels and status
- preserved repo-scoped navigation

### Phase 3: Recent items + AI-forward fallback

Ship:

- recent items
- start-manual-session fallback from query

### Phase 4: Iteration

Evaluate usage and only then consider:

- slash prefixes
- nested sub-commands
- additional entity types
- a dedicated multi-entity backend search endpoint

## Risks

| Risk | Mitigation |
|------|------------|
| Palette shortcut interferes with text entry | `Cmd+K` always opens the palette (matching industry convention). The palette captures focus on open and restores it on close, so in-progress text entry is not lost. |
| Repo switching becomes inconsistent with the header switcher | Reuse the same `nuqs` query-state model and repository summary data. |
| Dynamic search adds backend complexity too early | Reuse existing list endpoints with optional `search` params first. |
| Search results are ambiguous in multi-repo orgs | Always show repo context in dynamic entity rows. |
| Palette becomes a dumping ground for every action | Keep MVP focused on navigation, repo context, entity jump-to, and one AI-native action. |

## Telemetry

Track these events to inform Phase 4 prioritization:

- **Palette opened** — distinguish keyboard shortcut vs. trigger button click
- **Item selected** — log the item type (navigation, session, project, repo switch, quick action, manual-session fallback) and the query length at selection time
- **Empty results** — log when the user typed a query and saw no matches (high frequency here signals missing search coverage)
- **Session-from-query used** — track how often users convert an unmatched query into a manual session

Use the existing analytics patterns in the app. Do not introduce a new analytics provider for the palette.

## Open Questions

1. Should `Issues` become a dynamic search source once that surface is reintroduced in the active app IA?
2. Should selecting `New session` from the palette open the existing page or create a draft session immediately?
3. If usage becomes heavy, is a blended `/api/v1/command/search` endpoint justified for cross-entity ranking?
