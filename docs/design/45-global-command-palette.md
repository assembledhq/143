# 45 - Global Search & Command Palette

> **Status:** Proposed | **Last reviewed:** 2026-04-07

## Problem

For a team of 50, discoverability is everything. Today, there is no `Cmd+K` command palette, no search bar, and no way to quickly jump to a session, project, or setting. Users must navigate through sidebar clicks and dropdown menus to reach anything.

Settings are buried behind the user avatar dropdown in the sidebar (General, Integrations, Coding Agents, LLM, Autopilot Settings, Evals, Team, Audit Log). There is no unified way to search across entities (sessions, projects, repositories) or discover available actions.

This is table stakes for developer tools and directly impacts daily efficiency.

## Design Goal

Provide a single, keyboard-first entry point that lets any user:

- Navigate to any page in under two keystrokes
- Search across sessions, projects, and repositories by name
- Discover settings and actions without memorizing menu locations
- Execute quick actions (create session, switch repo context, toggle theme)

## Core Principles

### 1. Instant and keyboard-first

The palette opens with `Cmd+K` (Mac) or `Ctrl+K` (Windows/Linux) from any authenticated page. No mouse required. Arrow keys navigate, Enter selects, Escape closes.

### 2. Static actions are always available

Navigation items, settings pages, and quick actions appear immediately on open — before the user types anything. Typing filters them client-side with fuzzy matching.

### 3. Search is progressive

Static items filter instantly. Dynamic entity search (sessions, projects) fires only after 2+ characters with a 200ms debounce. Results augment the static list, never replace it.

### 4. Minimal footprint

No new context providers, no global state stores, no new dependencies beyond `cmdk`. The palette is self-contained: one component mounted in the authenticated layout.

---

## Technology

**[cmdk](https://github.com/pacocoursey/cmdk)** via shadcn/ui's `Command` component.

- `cmdk` is the standard React command palette primitive (used by Vercel, Linear, Raycast)
- shadcn/ui provides a pre-styled `Command` wrapper built on cmdk + Radix Dialog
- Integrates with the existing Tailwind CSS variables, dark mode, and "new-york" style

Install:

```bash
npm install cmdk
npx shadcn@latest add command
```

This generates `src/components/ui/command.tsx` — styled wrappers around `CommandDialog`, `CommandInput`, `CommandList`, `CommandGroup`, `CommandItem`, `CommandEmpty`.

---

## Architecture

```
src/
├── components/
│   ├── ui/
│   │   └── command.tsx                    # shadcn/ui primitives (generated)
│   ├── command-palette/
│   │   ├── command-palette.tsx            # Main component (dialog + groups)
│   │   ├── use-command-palette.ts         # Open/close state + Cmd+K listener
│   │   ├── command-actions.ts             # Static action registry
│   │   └── use-command-search.ts          # Debounced API search hook
├── hooks/
│   └── use-debounce.ts                    # Debounce utility hook
├── components/
│   └── authenticated-layout.tsx           # Mount point (modified)
```

### Integration point

`<CommandPalette />` is rendered inside `AuthenticatedLayout` at `src/components/authenticated-layout.tsx`. It sits as a sibling to the sidebar and main content — no provider wrapping needed.

```tsx
// authenticated-layout.tsx
return (
  <div className="flex h-screen">
    <CommandPalette />
    <aside className="w-64 ...">
      {/* existing sidebar */}
    </aside>
    <main>
      {children}
    </main>
  </div>
);
```

---

## Component Design

### Static Action Registry (`command-actions.ts`)

A flat array defining every navigable destination and quick action. This is the single source of truth for what appears in the palette when the user hasn't typed a search query.

```ts
export interface CommandAction {
  id: string;
  label: string;
  group: "Navigation" | "Settings" | "Quick Actions";
  icon: LucideIcon;
  href?: string;
  action?: () => void;
  keywords?: string[];
  adminOnly?: boolean;
}
```

**Navigation group** — mirrors the sidebar nav items:

| ID | Label | Route |
|----|-------|-------|
| `nav-autopilot` | Autopilot | `/autopilot` |
| `nav-sessions` | Sessions | `/sessions` |
| `nav-projects` | Projects | `/projects` |
| `nav-team` | Team | `/team` |
| `nav-analytics` | Analytics | `/analytics` |
| `nav-costs` | Costs | `/costs` |
| `nav-automations` | Automations | `/automations` |

**Settings group** — mirrors the avatar dropdown menu:

| ID | Label | Route |
|----|-------|-------|
| `set-general` | General Settings | `/settings` |
| `set-integrations` | Integrations | `/integrations` |
| `set-agent` | Coding Agents | `/agent` |
| `set-llm` | LLM Configuration | `/llm` |
| `set-autopilot` | Autopilot Settings | `/settings/autopilot` |
| `set-evals` | Evals | `/settings/evals` |
| `set-team` | Team Settings | `/settings/team` |
| `set-audit` | Audit Log | `/settings/audit-log` |

`set-audit` is marked `adminOnly: true` and filtered out for non-admin users at render time.

**Quick Actions group** — imperative shortcuts:

| ID | Label | Route / Action |
|----|-------|----------------|
| `act-new-session` | New Session | `/sessions/new` |
| `act-new-eval` | New Eval Task | `/settings/evals/new` |

### Keyboard Hook (`use-command-palette.ts`)

Manages the open/close boolean and the global keydown listener:

```ts
export function useCommandPalette() {
  const [open, setOpen] = useState(false);

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault();
        setOpen((prev) => !prev);
      }
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, []);

  return { open, setOpen };
}
```

The hook returns `setOpen` so the sidebar search button can also trigger the palette.

### Search Hook (`use-command-search.ts`)

Debounced search using React Query against list endpoints:

```ts
export function useCommandSearch(query: string) {
  const debouncedQuery = useDebounce(query, 200);

  const sessions = useQuery({
    queryKey: [...queryKeys.sessions.all, "search", debouncedQuery],
    queryFn: () => api.sessions.list({ search: debouncedQuery, limit: 5 }),
    enabled: debouncedQuery.length >= 2,
  });

  const projects = useQuery({
    queryKey: ["projects", "search", debouncedQuery],
    queryFn: () => api.projects.list({ search: debouncedQuery, limit: 5 }),
    enabled: debouncedQuery.length >= 2,
  });

  return {
    sessions: sessions.data?.data ?? [],
    projects: projects.data?.data ?? [],
    isLoading: sessions.isLoading || projects.isLoading,
  };
}
```

### Main Component (`command-palette.tsx`)

The palette renders a `CommandDialog` containing:

1. **Input** — placeholder: "Search sessions, projects, settings..."
2. **Dynamic results** — sessions and projects matching the query (shown when query >= 2 chars)
3. **Static groups** — Navigation, Settings, Quick Actions (always present, filtered by cmdk fuzzy match)
4. **Footer** — keyboard hint bar (`↑↓ navigate`, `↵ select`, `esc close`)

On item selection: call `router.push(href)` for navigation items, or invoke `action()` for imperative actions. Close the palette immediately on selection.

### Sidebar Search Button

A clickable hint in the sidebar for mouse users and discoverability, placed between the logo and the nav items:

```tsx
<button
  onClick={() => setCommandPaletteOpen(true)}
  className="mx-2.5 flex items-center gap-2 rounded-lg border
             border-border/50 px-2.5 py-1.5 text-xs
             text-muted-foreground hover:bg-sidebar-accent transition-colors"
>
  <Search className="h-3.5 w-3.5" />
  <span>Search...</span>
  <kbd className="ml-auto text-[10px] bg-muted px-1.5 py-0.5 rounded">
    ⌘K
  </kbd>
</button>
```

---

## Data Flow

```
User presses Cmd+K (or clicks sidebar button)
          │
          ▼
  CommandDialog opens
          │
          ├── Static actions shown immediately
          │   (filtered client-side by cmdk fuzzy match)
          │
          ├── User types 2+ characters
          │   │
          │   ▼
          │   200ms debounce
          │   │
          │   ▼
          │   React Query fetches:
          │     GET /api/v1/sessions?search=<q>&limit=5
          │     GET /api/v1/projects?search=<q>&limit=5
          │   │
          │   ▼
          │   Dynamic results merged into list
          │
          ▼
  User selects item
          │
          ├── href → router.push(href)
          └── action → action()
          │
          ▼
  Dialog closes
```

---

## Accessibility

- `CommandDialog` wraps Radix `Dialog` — provides focus trap, `aria-modal`, backdrop click-to-close
- cmdk provides `role="combobox"` on input, `role="listbox"` on results, `aria-selected` on active item
- Full keyboard navigation: `↑`/`↓` arrows, `Enter` to select, `Escape` to close
- Respects `prefers-reduced-motion` (skip entry/exit animations)
- Screen reader announces group headings and item count

---

## File Changelist

| File | Action | Description |
|------|--------|-------------|
| `frontend/package.json` | Modify | Add `cmdk` dependency |
| `frontend/src/components/ui/command.tsx` | Create | shadcn/ui Command primitives (generated) |
| `frontend/src/components/command-palette/command-palette.tsx` | Create | Main palette component with dialog, groups, and footer |
| `frontend/src/components/command-palette/use-command-palette.ts` | Create | Open/close state + global `Cmd+K` / `Ctrl+K` listener |
| `frontend/src/components/command-palette/command-actions.ts` | Create | Static action registry (nav, settings, quick actions) |
| `frontend/src/components/command-palette/use-command-search.ts` | Create | Debounced React Query search for sessions and projects |
| `frontend/src/hooks/use-debounce.ts` | Create | Generic debounce hook |
| `frontend/src/components/authenticated-layout.tsx` | Modify | Mount `<CommandPalette />` + add sidebar search button |

---

## Implementation Phases

### Phase 1: MVP — Static Navigation Palette

**Goal:** Ship a working `Cmd+K` palette with static navigation, settings, and quick actions. No API search. This alone solves the discoverability and navigation efficiency problems.

**Tasks:**

1. Install `cmdk` and generate shadcn/ui `command` component
2. Create `command-actions.ts` with the static action registry
3. Create `use-command-palette.ts` hook with `Cmd+K` / `Ctrl+K` listener
4. Create `command-palette.tsx` rendering static groups only
5. Mount `<CommandPalette />` in `authenticated-layout.tsx`
6. Add sidebar search button with `⌘K` hint
7. Filter `adminOnly` actions based on `user.role` from `useAuth()`
8. Add keyboard hint footer to the dialog
9. Write tests for the keyboard hook and action registry

**Acceptance criteria:**

- `Cmd+K` opens the palette from any authenticated page
- Typing filters navigation items, settings, and quick actions via fuzzy match
- Selecting an item navigates to the correct route
- `Escape` closes the palette
- Admin-only items (Audit Log) are hidden for non-admin users
- Sidebar shows a clickable search button with keyboard shortcut hint

---

### Phase 2: Dynamic Entity Search

**Goal:** Add server-side search for sessions and projects so users can jump directly to any entity by name.

**Tasks:**

1. Create `use-debounce.ts` hook
2. Create `use-command-search.ts` with debounced React Query calls
3. Update `command-palette.tsx` to render dynamic Sessions and Projects groups above static groups
4. Show loading state ("Searching...") while queries are in-flight
5. Display session status as a badge on each result item
6. Display project name and task count on each result item
7. Add `search` query parameter support to backend `GET /api/v1/sessions` endpoint (Go)
8. Add `search` query parameter support to backend `GET /api/v1/projects` endpoint (Go)
9. Write integration tests for search behavior

**Backend changes required:**

```go
// In the sessions list handler, add optional search filtering:
// WHERE title ILIKE '%' || $1 || '%'
// Parameterized to prevent SQL injection.
```

**Acceptance criteria:**

- Typing 2+ characters triggers a debounced search
- Session results show title (or ID fallback) and status badge
- Project results show name
- Selecting a result navigates to `/sessions/{id}` or `/projects/{id}`
- Empty search state shows "No results found."
- Queries are cached by React Query and don't re-fire on re-open with same term

**Fallback if backend changes are blocked:**

Client-side filter against the already-cached list data from React Query (the sessions and projects lists fetched by other pages). This avoids any backend work but limits results to already-loaded data.

---

### Phase 3: Recent Items & Personalization

**Goal:** Surface recently visited items for instant access on palette open, before the user types anything.

**Tasks:**

1. Create `use-recent-items.ts` hook backed by `localStorage`
2. Track navigation events: when a user navigates to a session, project, or settings page, record `{ type, id, label, href, timestamp }` in localStorage
3. Cap at 5 most recent items, deduplicated by href
4. Render a "Recent" group at the top of the palette when the query is empty
5. Add a "Clear recents" action at the bottom of the Recent group
6. Persist across page reloads but not across browsers

**Acceptance criteria:**

- Opening the palette with no query shows up to 5 recent items at the top
- Recent items update as the user navigates
- Clearing recents removes all entries
- Recent items are user-specific (tied to localStorage, which is per-browser)

---

### Phase 4: Extended Actions & Sub-Commands

**Goal:** Expand the palette beyond navigation into a true command center.

**Tasks:**

1. **Theme toggle** — Add "Switch to dark mode" / "Switch to light mode" action using `next-themes`' `useTheme()` hook
2. **Repo context switching** — Add "Switch repository" action that opens a nested list of repositories (fetched via `api.repositories.list()`)
3. **Nested sub-commands** — Implement a drill-down pattern: selecting "Settings" shows a sub-list of all settings pages. Back via `Backspace` on empty input.
4. **Slash-command filtering** — Typing `/session` scopes results to sessions only; `/project` scopes to projects only. Implemented as prefix detection in the filter logic.
5. **Logout action** — Add "Log out" to Quick Actions, wired to `useAuth().logout()`

**Acceptance criteria:**

- Theme toggle switches theme immediately without navigation
- Repo context switching updates the global repo filter
- Nested commands allow drilling into sub-menus
- Slash prefixes scope search to a single entity type
- All actions are discoverable via the palette's fuzzy search

---

### Phase 5: Analytics & Iteration

**Goal:** Understand how the palette is used and refine based on data.

**Tasks:**

1. Track palette open events (count, trigger method: keyboard vs click)
2. Track search queries (anonymized, for popular term analysis)
3. Track selected items (which actions/entities are most used)
4. Send events to the existing analytics pipeline
5. Review data after 2 weeks and adjust:
   - Re-order groups by usage frequency
   - Add missing actions users search for but don't find
   - Tune fuzzy match sensitivity

**Acceptance criteria:**

- Analytics events fire on open, search, and select
- Dashboard shows palette usage metrics
- At least one iteration based on data within 30 days of launch

---

## Risk & Mitigations

| Risk | Mitigation |
|------|------------|
| `Cmd+K` conflicts with browser "focus address bar" | Browsers allow `preventDefault()` on `Cmd+K`. Tested in Chrome, Firefox, Safari, Edge. |
| Search API latency causes jank | 200ms debounce + React Query caching. Loading state shown. Static items always available instantly. |
| Backend doesn't support `?search=` param | Phase 1 ships without API search. Phase 2 fallback: client-side filter from cache. |
| Palette keyboard conflicts with diff viewer (`j`/`k` nav) | Palette uses `Cmd+K` (modifier key required). Diff viewer uses single keys without modifiers. No conflict. Palette's Radix Dialog focus trap prevents key leak. |
| Large session/project lists slow down search | Server-side `LIMIT 5` on search queries. No full-list fetches for search. |

## Open Questions

1. **Should the palette support repository search?** Repos are less frequently navigated directly, but it could be useful for multi-repo teams. Candidate for Phase 4.
2. **Should we add a "Help" group with links to docs?** Low effort, high discoverability value. Could include links to keyboard shortcut reference, API docs, changelog.
3. **Should recent items sync across devices?** Would require backend storage. Likely not worth it for MVP — localStorage is sufficient.
