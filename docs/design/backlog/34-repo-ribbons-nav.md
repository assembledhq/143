# 34 - Repo Context Switcher: Repository-Scoped Navigation

> **Status:** Backlog | **Last reviewed:** 2026-05-06
>
> **Implementation notes:** Backend repo summary endpoint and filtering implemented. Missing/unclear: frontend `RepoContextSwitcher` component, repo badges on rows, localStorage/URL param context persistence.

> Give multi-repo users a global repo context selector that scopes all navigation, while keeping single-repo users' experience unchanged.

**Depends on:** [03-frontend.md](../03-frontend.md), [29-projects.md](../implemented/29-projects.md), [30-pm-agent-ux-elevation.md](30-pm-agent-ux-elevation.md)

---

## 1. Problem

Today, all Sessions and Projects live in flat lists. When a user connects a second repository, their Sessions page shows work from both repos interleaved. There is no way to focus on one repo's work without manually scanning titles, and no ambient visibility into which repos have active or failing sessions.

This matters now because the product is growing beyond single-repo usage — but not by much. Based on expected usage patterns:

| Segment | Repos | Est. % of users |
|---------|-------|-----------------|
| Single repo | 1 | ~50% |
| Two repos | 2 | ~25% |
| Multi-repo | 3-5 | ~20% |
| Heavy multi-repo | 6+ | ~5% |

**The key constraint:** most users have 1-2 repos. We need a design that adds zero overhead for single-repo users, feels great at 2-3 repos, and still works at 10+. We are explicitly NOT designing for 50-repo enterprise scale — if that becomes real, we'll evolve from this foundation.

---

## 2. Design Principles

1. **Invisible for one** — Single-repo users should see zero change. No repo selector, no badges, no new concepts.
2. **Visible for two+** — The moment a second repo is connected, the repo context selector appears automatically.
3. **Set once, navigate freely** — Selecting a repo scopes all pages. Users don't re-select per page.
4. **Ambient awareness** — The context selector shows which repos have active work via status indicators.
5. **Additive, not restructuring** — The sidebar nav items (Overview, Sessions, Projects) are unchanged. The context selector is a new, orthogonal control. "All repositories" is the default context and always available.

---

## 3. What Changes

### 3.1 Repo Context Selector in the Header

When the org has **2+ connected repositories**, a repo context selector appears in the header bar, next to the org name. This is a global control — selecting a repo scopes Sessions, Projects, and Overview to that repository.

**Current header (unchanged for 1 repo):**
```
┌──────────────────────────────────────────────────────┐
│  143.dev                                              │
├──────────────────────────────────────────────────────┤
```

**New header (2+ repos):**
```
┌──────────────────────────────────────────────────────┐
│  143.dev  ·  All repositories ▾                       │
├──────────────────────────────────────────────────────┤
```

Clicking "All repositories ▾" opens a dropdown:

```
┌──────────────────────────────┐
│  🔍 Search repos...          │
├──────────────────────────────┤
│  ✓ All repositories          │
│  ─────────────────────────── │
│  acme/api-server      ● 3   │
│  acme/web-app          1    │
│  acme/mobile-app             │
│  acme/infra-tools            │
└──────────────────────────────┘
```

When a repo is selected, the header updates to reflect the active context:

```
┌──────────────────────────────────────────────────────┐
│  143.dev  ·  api-server ▾  ●                          │
├──────────────────────────────────────────────────────┤
```

#### Dropdown anatomy

Each row in the dropdown contains:

```
[icon] owner/repo-name    [status-indicator] [active-count]
```

- **Icon:** `GitBranch` from lucide-react (14px, muted)
- **Repo name:** `full_name` from the Repository model, displayed as `owner/repo`.
- **Status indicator:** A colored dot conveying the most urgent state for this repo:
  - Pulsing blue: a session for this repo is currently `running`
  - Solid amber: a session is in `needs_human_guidance` or `awaiting_input`
  - Solid red: most recent session `failed` or `cancelled`
  - No dot: idle (no active or recently-failed sessions)
- **Active count:** Number of sessions in `running`, `pending`, `needs_human_guidance`, or `awaiting_input` status. Hidden when 0.
- **Search input:** Visible at the top of the dropdown when the org has 4+ repos. For 2-3 repos, the list is short enough that search adds clutter. The search filters by repo name as the user types.

> **Status dot simplification:** Unlike a 4-state system (blue/green/amber/red), we drop the "green = most recent completed" state. "Nothing is wrong" is the default — it doesn't need a dot. The active count already conveys "work is happening." This reduces visual noise and avoids ambiguity when a repo has both completed and running sessions simultaneously.

#### Keyboard shortcut

`Cmd+K` (or a dedicated shortcut like `Cmd+R`) opens the dropdown with the search input focused, allowing power users to switch repos without touching the mouse: `Cmd+R → type "api" → Enter`.

#### Selected state persistence

The selected repo context is stored in `localStorage` (key: `selected-repo-context`) and persists across page loads and browser sessions. It is also reflected in the URL via a `repo` query parameter so that links are shareable and bookmarkable.

When a user navigates to a URL with a `?repo=` param, the context selector updates to match. When they navigate to a URL without it, the selector resets to "All repositories."

### 3.2 Sidebar Navigation

The sidebar is **unchanged**. No repo rows, no sub-nav items, no expand/collapse. The existing nav items work exactly as before:

```
┌──────────────────────────┐
│  Overview                │
│  Sessions  ●             │
│  Projects                │
│                          │
│  ┌──────────────────┐    │
│  │ 👤 User ▾        │    │
│  └──────────────────┘    │
└──────────────────────────┘
```

The counts on Sessions (e.g., `Sessions (3) ●`) reflect the currently selected repo context. When "All repositories" is selected, counts are org-wide. When a specific repo is selected, counts are scoped to that repo.

### 3.3 Scoped Page Behavior

When a repo is selected in the context switcher, **all list pages automatically filter** to that repository. The behavior per page:

#### Sessions page (`/sessions?repo={repository_id}`)

- **URL param:** `repo` query parameter, managed with `nuqs` (already used for `status` filtering).
- **Page title:** Shows `Sessions — {repo short name}` instead of just `Sessions`.
- **Filter tabs:** All existing status filter tabs (All, Active, Needs Guidance, Failed, Done, Decisions) still work — they compose with the repo filter.
- **Filter banner:** A visible banner at the top of the list makes the scoped state unambiguous:
  ```
  ┌─────────────────────────────────────────────┐
  │ Filtered to: acme/api-server    [Show all ×]│
  └─────────────────────────────────────────────┘
  ```
  Clicking "Show all" clears the repo context (both the URL param and the header selector).

#### Projects page (`/projects?repo={repository_id}`)

Same pattern as Sessions.

#### Overview page

When a repo is selected, the Overview page shows stats scoped to that repo. When "All repositories" is selected, Overview shows org-wide stats (current behavior). This is a natural benefit of the context-based approach — scoping is automatic across all pages.

> **Naming note:** The user-facing URL param is `repo` (short, readable). The backend API query param is `repository_id` (matches the DB column). The frontend translates between them: `useQueryState('repo')` provides the value, which is passed as `repository_id` to the API client.

### 3.4 List Pages: Repo Badge on Rows

When the org has 2+ repos **and** the user is viewing "All repositories" context, add a subtle repo badge to each row on the Sessions and Projects list pages. This helps users scanning the unfiltered view understand which repo an item belongs to.

**Sessions list row (current):**
```
● Active  PM Analysis · 3 tasks · 1 running                    2h ago
```

**Sessions list row (new, 2+ repos, "All repositories" context):**
```
● Active  PM Analysis · 3 tasks · 1 running       api-server   2h ago
```

- Badge shows the **repo short name** (just the repo portion of `owner/repo`, e.g., `api-server` not `acme/api-server`). Full name on hover.
- Styled as muted text, `text-xs`, positioned to the left of the timestamp.
- Hidden when: org has only 1 repo, OR user has a specific repo selected (redundant since all rows are the same repo).

**Projects list row** follows the same pattern. The project already has a `repository_id` so the data is trivially available.

---

## 4. What Does NOT Change

- **Single-repo users** see zero UI changes. The context selector, badges, and scoped views all require 2+ repos to appear.
- **Sidebar structure** is completely unchanged. No new nav items, no repo rows, no expand/collapse.
- **Session detail pages** are unchanged. The `/sessions/{id}` page shows the same content regardless of how you got there.
- **Repo settings pages** (`/repositories/[id]`) remain in their current location, accessed through the settings flow.
- **PM agent behavior** is unaffected. The PM still analyzes across all repos.
- **URL structure** for non-scoped views is unchanged. `/sessions` without a `?repo` param shows all repos.

---

## 5. Data Model & API Changes

### 5.1 Sessions → Repository Relationship

**Current state:** Sessions have no direct `repository_id`. They connect to repositories indirectly through issues:

```
sessions.issue_id → issues.repository_id → repositories.id
```

**Decision: Join through issues, don't denormalize (yet).**

For the context switcher counts and list filtering, we need to resolve session → repo. Two options:

| Approach | Pros | Cons |
|----------|------|------|
| **A: JOIN through issues** | No schema change. Single source of truth. | Slightly more complex queries. JOIN on every list call. |
| **B: Denormalize `repository_id` onto sessions** | Simple queries. Can index directly. | Schema migration. Must keep in sync. Backfill needed. |

**Go with A for now.** At our expected scale (low hundreds of sessions per org), the JOIN is trivial. If performance becomes an issue, we can denormalize later with a migration + backfill — but don't pay that complexity cost upfront.

### 5.2 API Changes

#### `GET /api/v1/sessions` — Add `repository_id` filter

**New optional query param:** `repository_id` (uuid)

When provided, the query becomes:

```sql
SELECT s.* FROM sessions s
INNER JOIN issues i ON s.issue_id = i.id
WHERE s.org_id = @org_id
  AND i.repository_id = @repository_id
  [AND s.status = @status]  -- existing filter
ORDER BY s.created_at DESC
```

When not provided, behavior is unchanged (all sessions for the org).

**Backend file:** `internal/db/session_store.go` — update `ListByOrg` to accept optional `RepositoryID` in `SessionFilters`.

**Handler file:** `internal/api/handlers/sessions.go` — parse `repository_id` from query params, pass to store.

#### `GET /api/v1/projects` — Add `repository_id` filter

**New optional query param:** `repository_id` (uuid)

Simpler than sessions since projects already have a direct `repository_id` column:

```sql
SELECT * FROM projects
WHERE org_id = @org_id
  AND repository_id = @repository_id
  [AND status = @status]
ORDER BY priority ASC, created_at DESC, id DESC
```

**Backend file:** `internal/db/projects.go` — update `ListByOrg` to accept optional `RepositoryID` in `ProjectFilters`.

**Handler file:** `internal/api/handlers/projects.go` — parse `repository_id` from query params, pass to store.

#### `GET /api/v1/repositories/summary` — New endpoint

Returns per-repo session counts for the context switcher dropdown. This powers the active count badges and status indicators without the frontend needing to fetch all sessions.

**Request:** `GET /api/v1/repositories/summary`

**Response:**
```json
{
  "data": [
    {
      "repository_id": "uuid-1",
      "full_name": "acme/api-server",
      "active_session_count": 3,
      "latest_session_status": "running",
      "active_project_count": 2
    },
    {
      "repository_id": "uuid-2",
      "full_name": "acme/web-app",
      "active_session_count": 1,
      "latest_session_status": "completed",
      "active_project_count": 0
    }
  ]
}
```

**SQL:**
```sql
SELECT
  r.id AS repository_id,
  r.full_name,
  COUNT(DISTINCT s.id) FILTER (
    WHERE s.status IN ('running', 'pending', 'needs_human_guidance', 'awaiting_input')
  ) AS active_session_count,
  (
    SELECT s2.status FROM sessions s2
    JOIN issues i2 ON s2.issue_id = i2.id
    WHERE i2.repository_id = r.id AND s2.org_id = r.org_id
    ORDER BY s2.created_at DESC LIMIT 1
  ) AS latest_session_status,
  COUNT(DISTINCT p.id) FILTER (
    WHERE p.status IN ('active', 'planning')
  ) AS active_project_count
FROM repositories r
LEFT JOIN issues i ON i.repository_id = r.id
LEFT JOIN sessions s ON s.issue_id = i.id
LEFT JOIN projects p ON p.repository_id = r.id AND p.org_id = r.org_id
WHERE r.org_id = @org_id AND r.status = 'active'
GROUP BY r.id, r.full_name
ORDER BY active_session_count DESC, r.full_name ASC;
```

> **Scaling note:** The `latest_session_status` correlated subquery runs once per repository row. At 5 repos this is negligible, but if repo count grows significantly, rewrite as a window function or `LATERAL JOIN` to avoid per-row scans.

**Caching:** This endpoint is polled every 10s (lightweight aggregation query). At our scale the query is fast, but if needed, results can be cached server-side with a 10s TTL.

### 5.3 Frontend API Client Changes

**File:** `frontend/src/lib/api.ts`

```typescript
// Add to sessions
sessions: {
  list: (params?: { status?: string; cursor?: string; limit?: number; repository_id?: string }) => ...
}

// Add to projects
projects: {
  list: (params?: { status?: string; cursor?: string; limit?: number; repository_id?: string }) => ...
}

// Add to repositories
repositories: {
  ...existing,
  summary: () => get<ListResponse<RepoSummary>>('/api/v1/repositories/summary'),
}
```

### 5.4 New Frontend Types

**File:** `frontend/src/lib/types.ts`

```typescript
export interface RepoSummary {
  repository_id: string;
  full_name: string;
  active_session_count: number;
  latest_session_status: string;
  active_project_count: number;
}
```

---

## 6. Frontend Implementation

### 6.1 Repo Context Switcher Component

**New file:** `frontend/src/components/repo-context-switcher.tsx`

A self-contained component rendered in the header area of `AuthenticatedLayout`. Responsibilities:

1. **Fetch repo summary data** — `useQuery` for `api.repositories.summary()`, polled every 10s.
2. **Conditionally render** — only when `repoSummaries.length >= 2`. Returns `null` for single-repo orgs.
3. **Manage selected state** — syncs between URL `repo` query param and `localStorage`.
4. **Render dropdown** — with search (4+ repos), status dots, and active counts.

```typescript
// In repo-context-switcher.tsx
export function RepoContextSwitcher() {
  const { data: summaries } = useQuery({
    queryKey: ['repositories', 'summary'],
    queryFn: () => api.repositories.summary(),
    refetchInterval: 10_000,
  });

  const [repo, setRepo] = useQueryState('repo');

  // Don't render for single-repo orgs
  if (!summaries?.data || summaries.data.length < 2) return null;

  const selectedRepo = summaries.data.find(r => r.repository_id === repo);
  const label = selectedRepo
    ? selectedRepo.full_name.split('/')[1]
    : 'All repositories';

  return (
    <DropdownMenu>
      <DropdownMenuTrigger>
        <span>{label}</span>
        <ChevronDown />
        {selectedRepo && /* status dot */}
      </DropdownMenuTrigger>
      <DropdownMenuContent>
        {summaries.data.length >= 4 && <SearchInput />}
        <DropdownMenuItem onClick={() => setRepo(null)}>
          All repositories
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        {summaries.data.map(repo => (
          <DropdownMenuItem
            key={repo.repository_id}
            onClick={() => setRepo(repo.repository_id)}
          >
            <GitBranch size={14} />
            <span>{repo.full_name}</span>
            {repo.active_session_count > 0 && <Badge>{repo.active_session_count}</Badge>}
            <StatusDot status={repo.latest_session_status} />
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
```

### 6.2 Integrating with AuthenticatedLayout

**File:** `frontend/src/components/authenticated-layout.tsx`

Minimal changes:

1. **Render `<RepoContextSwitcher />`** in the header, after the org name.
2. **Pass `repo` to sidebar counts** — the Sessions nav item count should reflect the selected repo context. Read the `repo` query param and pass it to the sessions count query.

No structural changes to the sidebar. No new nav items.

### 6.3 Sessions & Projects List Pages

**Repo filter via URL param:**

Both pages already use `nuqs` for the `status` query param. Add `repo` as a second param:

```typescript
const [repo] = useQueryState('repo');

// Pass to API call
const { data } = useQuery({
  queryKey: ['sessions', { status, repo }],
  queryFn: () => api.sessions.list({ status, repository_id: repo }),
});
```

**Page title:**

```typescript
const repoName = repoSummaries?.find(r => r.repository_id === repo)?.full_name;
const pageTitle = repo && repoName
  ? `Sessions — ${repoName.split('/')[1]}`
  : 'Sessions';
```

**Filter banner:**

When `repo` is set, render a prominent banner above the list:

```typescript
{repo && repoName && (
  <div className="flex items-center gap-2 rounded-md bg-muted px-3 py-2 text-sm">
    <span>Filtered to: <strong>{repoName}</strong></span>
    <button onClick={() => setRepo(null)} className="ml-auto text-muted-foreground hover:text-foreground">
      Show all ×
    </button>
  </div>
)}
```

### 6.4 Enriching Session List Response

To show repo badges on session rows without N+1 lookups, the `GET /api/v1/sessions` response should include the repository name.

**Option: Add to the list query JOIN.**

Extend the sessions list query to JOIN through issues to repositories and return `repository_full_name`:

```sql
SELECT s.*, r.full_name AS repository_full_name
FROM sessions s
LEFT JOIN issues i ON s.issue_id = i.id
LEFT JOIN repositories r ON i.repository_id = r.id
WHERE s.org_id = @org_id
```

This adds the repo name to each session in the API response. The frontend `Session` type gets an optional `repository_full_name?: string` field.

This is preferable to a separate lookup because:
- It's one query, no N+1
- The JOIN is cheap (sessions already reference issues by FK)
- It's backwards-compatible (new field, no breaking changes)

---

## 7. Edge Cases

### Manual sessions without issues

Some sessions (manual "Fix This" sessions) may be created without a pre-existing issue, or the issue might not have a `repository_id`. These sessions:
- Won't appear when a specific repo is selected in the context switcher
- Will still appear when "All repositories" is selected
- The repo badge on the row will be absent — that's fine, it degrades gracefully

### Repo gets disconnected

If a user disconnects a repository:
- It disappears from the context switcher dropdown
- If it was the currently selected context, the selector resets to "All repositories"
- Sessions and Projects linked to it still appear in the "All repositories" view
- Direct URL access via `?repo=` for that repo returns an empty list (no special handling needed)

### User has exactly 1 repo

The entire feature is invisible. No context selector, no badges, no scoped views. The existing UI is 100% unchanged. The `repositories/summary` endpoint still returns data (1 item), but the `RepoContextSwitcher` component returns `null` when `summaries.length < 2`.

### Long repo names

The context selector in the header shows the **short name** (repo portion only, e.g., `api-server`). The dropdown shows the full `owner/repo` name. Long names in the dropdown are truncated with `text-ellipsis`; full name is available on hover via `title` attribute. The header short name keeps the top bar compact.

### Stale context after repo deletion

If a user's `localStorage` has a `selected-repo-context` value for a repo that no longer exists (deleted or disconnected), the component detects this when the summary data loads (selected ID not in the list) and resets to "All repositories."

### Shared URLs

A URL like `/sessions?repo=abc` works regardless of the recipient's `localStorage` state. The context switcher reads the URL param and updates accordingly. This means shared links always show the intended repo context.

---

## 8. Implementation Order

| Step | What | Where | Depends on |
|------|------|-------|------------|
| 1 | Add `repository_id` filter to sessions list query | `internal/db/session_store.go` | — |
| 2 | Add `repository_id` filter to projects list query | `internal/db/projects.go` | — |
| 3 | Add `repository_id` query param parsing to session + project handlers | `internal/api/handlers/sessions.go`, `projects.go` | Steps 1-2 |
| 4 | Create `/api/v1/repositories/summary` endpoint | `internal/api/handlers/repositories.go`, `internal/db/repository_store.go` | — |
| 5 | Enrich session list response with `repository_full_name` | `internal/db/session_store.go`, `internal/api/handlers/sessions.go` | Step 1 |
| 6 | Add `RepoSummary` type and API methods to frontend | `frontend/src/lib/types.ts`, `frontend/src/lib/api.ts` | Steps 3-5 |
| 7 | Build `RepoContextSwitcher` component | `frontend/src/components/repo-context-switcher.tsx` | Step 6 |
| 8 | Integrate context switcher into `AuthenticatedLayout` header | `frontend/src/components/authenticated-layout.tsx` | Step 7 |
| 9 | Add `repo` query param support + filter banner to Sessions page | `frontend/src/app/(dashboard)/sessions/page.tsx` | Step 6 |
| 10 | Add `repo` query param support + filter banner to Projects page | `frontend/src/app/(dashboard)/projects/page.tsx` | Step 6 |
| 11 | Add repo badge to session and project list rows | Session/project row components | Steps 5, 9-10 |

**Steps 1, 2, and 4 can be done in parallel** (independent backend work).
**Steps 9 and 10 can be done in parallel** (independent page work).

Estimated scope: ~2-3 days of focused work for one full-stack engineer.

---

## 9. Future Considerations (NOT in this iteration)

These are explicitly out of scope but worth noting as natural evolutions:

- **Keyboard shortcut for repo switching** — `Cmd+R` to open the context switcher with search focused. Natural extension once the component exists.
- **Per-repo Overview dashboard** — with the context switcher in place, the Overview page can trivially become repo-aware by reading the `repo` query param.
- **Repo-scoped notifications** — "api-server has a failing session" instead of generic "a session failed."
- **Repo grouping/favorites** — if a user reaches 10+ repos, let them star favorites to pin them at the top of the dropdown.
- **Denormalizing `repository_id` onto sessions** — if the JOIN through issues becomes a performance issue, add the column with a migration + backfill.
- **Multi-repo selection** — the dropdown could support selecting multiple repos (shift-click) to view a subset. The URL would become `?repo=abc,def`.

---

## 10. Non-Goals

- **Sidebar repo rows or ribbons** — duplicates "Sessions" and "Projects" labels N times, clutters the sidebar at 3+ repos. The context switcher avoids this entirely.
- **Horizontal repo tabs** — creates two competing navigation axes and doesn't scale past 5 repos.
- **Filter dropdown on list pages** — per-page filtering means re-selecting on every page navigation. A global context is fewer total clicks.
- **Progressive disclosure / adaptive UI** — building 4 UI modes for a user distribution that spans 2 of them. One UI that works for 1-10 is simpler.
- **Changes to the PM agent** — this is purely a navigation/presentation change.

---

## 11. Why Context Switcher Over Sidebar Ribbons

The original proposal added per-repo rows with expand/collapse sub-nav items to the sidebar. The context switcher approach was chosen instead for these reasons:

| Concern | Sidebar ribbons | Context switcher |
|---------|----------------|-----------------|
| Sidebar clutter | "Sessions" appears N+1 times (once top-level, once per repo) | Sidebar unchanged, "Sessions" appears once |
| Scales to 10+ repos | Sidebar becomes unusably long | Dropdown with search handles any count |
| Clicks to scope | 1 click (expand) + 1 click (sub-link) = 2 clicks, then repeat per page | 2 clicks to set context (open dropdown + select), then 0 clicks as you navigate |
| Net clicks per workflow | Higher (re-select per page) | Lower (set once, navigate freely) |
| Implementation complexity | Expand/collapse state, localStorage per repo, sidebar layout changes | One dropdown component, one URL param |
| Overview page scoping | Deferred to future iteration | Works automatically |

---

## 12. Summary

| Area | Current | Proposed |
|------|---------|----------|
| Header | Org name only | Org name + repo context selector (2+ repos only) |
| Sidebar nav | Overview, Sessions, Projects | Unchanged — counts reflect selected context |
| Repo visibility | None | Context selector dropdown shows all repos with status + counts |
| Scoping to a repo | Not possible | Select repo in header, all pages filter automatically |
| All-repos view | Default (only option) | Still default, labeled "All repositories" |
| Session list rows | No repo indicator | Repo badge (short name) when viewing all repos (2+ repos only) |
| Project list rows | No repo indicator | Repo badge (short name) when viewing all repos (2+ repos only) |
| Single-repo users | Current UI | Identical — zero changes |
| API: sessions | No repo filter | Optional `repository_id` query param |
| API: projects | No repo filter | Optional `repository_id` query param |
| API: new endpoint | — | `GET /api/v1/repositories/summary` for context switcher data |
