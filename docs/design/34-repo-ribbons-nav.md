# 34 - Repo Ribbons: Repository-Scoped Navigation

> Give multi-repo users one-click access to repo-scoped Sessions and Projects in the sidebar, while keeping single-repo users' experience unchanged.

**Status:** Proposal
**Depends on:** [03-frontend.md](03-frontend.md), [29-projects.md](29-projects.md), [30-pm-agent-ux-elevation.md](30-pm-agent-ux-elevation.md)

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

**The key constraint:** most users have 1-2 repos. We need a design that adds zero overhead for single-repo users, feels great at 2-3 repos, and still works at 5. We are explicitly NOT designing for 50-repo enterprise scale — if that becomes real, we'll evolve from this foundation.

---

## 2. Design Principles

1. **Invisible for one** — Single-repo users should see zero change. No repo section, no badges, no new concepts.
2. **Visible for two+** — The moment a second repo is connected, repo-scoped navigation appears automatically.
3. **One click, not two** — Scoping to a repo should be a single nav click, not "open dropdown → select repo."
4. **Ambient awareness** — Users should see which repos have active work without clicking into anything.
5. **Additive, not restructuring** — The top-level Sessions and Projects nav items stay as "all repos" views. Repo ribbons add scoped shortcuts below them.

---

## 3. What Changes

### 3.1 Sidebar Navigation

When the org has **2+ connected repositories**, a repo section appears in the sidebar below the main nav items, separated by a divider.

**Current sidebar (unchanged for 1 repo):**
```
┌──────────────────────────┐
│  143.dev                 │
├──────────────────────────┤
│  Overview                │
│  Sessions  ●             │
│  Projects                │
│                          │
│  ┌──────────────────┐    │
│  │ 👤 User ▾        │    │
│  └──────────────────┘    │
└──────────────────────────┘
```

**New sidebar (2+ repos):**
```
┌──────────────────────────┐
│  143.dev                 │
├──────────────────────────┤
│  Overview                │
│  Sessions (7) ●          │  ← all repos, unchanged
│  Projects (4)            │  ← all repos, unchanged
│  ────────────────────    │  ← divider (new)
│  acme/api-server    ● 3  │  ← repo row: name, dot, active count
│    Sessions              │    ← scoped sub-nav (collapsed by default)
│    Projects              │
│  acme/web-app        1   │
│    Sessions              │
│    Projects              │
│                          │
│  ┌──────────────────┐    │
│  │ 👤 User ▾        │    │
│  └──────────────────┘    │
└──────────────────────────┘
```

#### Repo row anatomy

Each repo row in the sidebar contains:

```
[icon] owner/repo-name    [status-dot] [active-count]
```

- **Icon:** `GitBranch` from lucide-react (14px, muted)
- **Repo name:** `full_name` from the Repository model, displayed as `owner/repo`. Truncated with `text-ellipsis` if longer than available width; full name shown on hover via `title` attribute.
- **Status dot:** Same dot system as the Sessions nav item:
  - Pulsing blue: a session for this repo is currently `running`
  - Solid green: most recent session `completed` successfully
  - Solid amber: a session is in `needs_human_guidance` or `awaiting_input`
  - Solid red: most recent session `failed` or `cancelled`
  - No dot: idle (no recent activity)
- **Active count:** Number of sessions in `running`, `pending`, `needs_human_guidance`, or `awaiting_input` status. Hidden when 0.

#### Expand/collapse behavior

- Repos are **collapsed by default** — only the repo row is visible, not the sub-nav items.
- Clicking the repo row **toggles expand/collapse**, revealing Sessions and Projects sub-links.
- Clicking a sub-link (Sessions or Projects) navigates to the repo-scoped list.
- Expand/collapse state is stored in `localStorage` so it persists across page loads (key: `sidebar-repo-expanded-{repoId}`).
- If a user is currently on a repo-scoped page (e.g., `/sessions?repo=abc`), that repo auto-expands in the sidebar.

#### Ordering

Repos are sorted by:
1. Active session count (descending) — repos with active work surface first
2. Alphabetical by `full_name` (ascending) — stable fallback

This means the "hottest" repo is always at the top of the repo section.

### 3.2 List Pages: Repo Badge on Rows

When the org has 2+ repos, add a subtle repo badge to each row on the Sessions and Projects list pages. This helps users scanning the "all repos" view understand which repo an item belongs to.

**Sessions list row (current):**
```
● Active  PM Analysis · 3 tasks · 1 running                    2h ago
```

**Sessions list row (new, 2+ repos):**
```
● Active  PM Analysis · 3 tasks · 1 running       api-server   2h ago
```

- Badge shows the **repo short name** (just the repo portion of `owner/repo`, e.g., `api-server` not `acme/api-server`). Full name on hover.
- Styled as muted text, `text-xs`, positioned to the left of the timestamp.
- Hidden when org has only 1 repo.

**Projects list row** follows the same pattern. The project already has a `repository_id` so the data is trivially available.

### 3.3 Repo-Scoped List Views

When a user clicks "Sessions" under a specific repo in the sidebar, they navigate to:

```
/sessions?repo={repository_id}
```

This is the same Sessions page, but filtered to only show sessions linked to that repository. The page behavior:

- **URL param:** `repo` query parameter, managed with `nuqs` (already used for `status` filtering).
- **Page title:** Shows `Sessions — {repo short name}` instead of just `Sessions`.
- **Filter tabs:** All existing status filter tabs (All, Active, Needs Guidance, Failed, Done, Decisions) still work — they compose with the repo filter.
- **Breadcrumb/context:** No breadcrumb needed. The sidebar's expanded repo and highlighted sub-link make the context clear.
- **"Clear filter" affordance:** A small `×` button or "Viewing repo-name — show all" link at the top of the list, so users can easily return to the unfiltered view.

Same pattern for Projects: `/projects?repo={repository_id}`.

> **Naming note:** The user-facing URL param is `repo` (short, readable). The backend API query param is `repository_id` (matches the DB column). The frontend translates between them: `useQueryState('repo')` provides the value, which is passed as `repository_id` to the API client.

### 3.4 Overview Page

No changes to the Overview page in this iteration. The sidebar repo ribbons already provide ambient awareness (active counts, status dots). A per-repo overview dashboard can be explored later if usage data supports it.

---

## 4. What Does NOT Change

- **Single-repo users** see zero UI changes. The repo section, badges, and scoped views all require 2+ repos to appear.
- **Top-level Sessions and Projects nav items** remain as the "all repos" entry point. They are not removed or demoted.
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

For the sidebar counts and list filtering, we need to resolve session → repo. Two options:

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

Returns per-repo session counts for the sidebar. This powers the active count badges and status dots without the frontend needing to fetch all sessions.

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

**Caching:** This endpoint is polled alongside PM status (every 30s). At our scale the query is fast, but if needed, results can be cached server-side with a 30s TTL.

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

### 6.1 Sidebar Component Changes

**File:** `frontend/src/components/authenticated-layout.tsx`

This is the main change. The `AuthenticatedLayout` component needs to:

1. **Fetch repo summary data** — new `useQuery` for `api.repositories.summary()`, polled every 30s (same interval as PM status).
2. **Conditionally render repo section** — only when `repoSummaries.length >= 2`.
3. **Render repo rows** — with expand/collapse, status dots, and active counts.
4. **Render sub-nav items** — Sessions and Projects links scoped to each repo.

**Expand/collapse state:**

```typescript
// In AuthenticatedLayout
const [expandedRepos, setExpandedRepos] = useState<Set<string>>(() => {
  // Initialize from localStorage
  const stored = localStorage.getItem('sidebar-expanded-repos');
  return stored ? new Set(JSON.parse(stored)) : new Set();
});

// Auto-expand repo when on a repo-scoped page
const currentRepoId = searchParams.get('repo');
useEffect(() => {
  if (currentRepoId && !expandedRepos.has(currentRepoId)) {
    setExpandedRepos(prev => new Set(prev).add(currentRepoId));
  }
}, [currentRepoId]);
```

**Active state highlighting:**

A repo's sub-nav "Sessions" link is highlighted when:
- `pathname === '/sessions'` AND `searchParams.get('repo') === repoId`

Same logic for Projects.

### 6.2 Sessions & Projects List Pages

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
// Fetch repo name for title
const repoName = repoSummaries?.find(r => r.repository_id === repo)?.full_name;
const pageTitle = repo && repoName
  ? `Sessions — ${repoName.split('/')[1]}`
  : 'Sessions';
```

**Repo badge on rows:**

Add a `<span>` to each session/project row showing the repo short name. The session row component needs the repo name, which can be resolved client-side by joining `session.issue_id` → issues data, or more practically by including `repository_full_name` in the session list API response (see section 6.3).

### 6.3 Enriching Session List Response

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
- Won't appear under any repo ribbon (they have no repo association)
- Will still appear in the top-level "Sessions" view (all repos)
- The repo badge on the row will be absent — that's fine, it degrades gracefully

### Repo gets disconnected

If a user disconnects a repository:
- Its ribbon disappears from the sidebar
- Sessions and Projects linked to it still appear in the "all repos" views
- The `?repo=` filter for that repo returns an empty list (no special handling needed)

### User has exactly 1 repo

The entire feature is invisible. No repo section, no badges, no scoped views. The existing UI is 100% unchanged. The `repositories/summary` endpoint still returns data (1 item), but the sidebar component simply doesn't render the repo section when `repoSummaries.length < 2`.

### Long repo names

The sidebar is `w-64` (256px). Repo names like `my-organization/my-very-long-repository-name` will be truncated with `truncate` (text-overflow: ellipsis). The full name is available on hover via the `title` attribute. The active count and status dot are always visible (they're positioned with `ml-auto` and don't get truncated).

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
| 7 | Add repo ribbon section to sidebar | `frontend/src/components/authenticated-layout.tsx` | Step 6 |
| 8 | Add `repo` query param support to Sessions page | `frontend/src/app/(dashboard)/sessions/page.tsx` | Step 6 |
| 9 | Add `repo` query param support to Projects page | `frontend/src/app/(dashboard)/projects/page.tsx` | Step 6 |
| 10 | Add repo badge to session and project list rows | Session/project row components | Steps 5, 8-9 |

**Steps 1, 2, and 4 can be done in parallel** (independent backend work).
**Steps 8 and 9 can be done in parallel** (independent page work).

Estimated scope: ~2-3 days of focused work for one full-stack engineer.

---

## 9. Future Considerations (NOT in this iteration)

These are explicitly out of scope but worth noting as natural evolutions:

- **Per-repo Overview dashboard** — if users frequently scope to one repo and stay, the Overview page could become repo-aware.
- **Pinned/starred repos** — if a user reaches 6+ repos, let them pin favorites to the top of the repo section. The ribbon UI supports this naturally.
- **Repo-scoped notifications** — "repo-a has a failing session" instead of generic "a session failed."
- **Repo search in sidebar** — for the rare 10+ repo user, add a small search input above the repo list.
- **Denormalizing `repository_id` onto sessions** — if the JOIN through issues becomes a performance issue, add the column with a migration + backfill.

---

## 10. Non-Goals

- **Repo tabs or horizontal nav** — creates two competing navigation axes. The sidebar handles scoping.
- **Global repo context switcher** — over-abstracted for 2-5 repos. You can see all repos in the nav; no need to hide them behind a dropdown.
- **Filter dropdown on list pages** — overkill at this scale. An extra click to open a menu showing 2 items isn't good UX.
- **Progressive disclosure / adaptive UI** — building 4 UI modes for a user distribution that spans 2 of them. One UI that works for 1-5 is simpler.
- **Changes to the PM agent** — this is purely a navigation/presentation change.

---

## 11. Summary

| Area | Current | Proposed |
|------|---------|----------|
| Sidebar nav | Overview, Sessions, Projects | Same + repo ribbons section below divider (2+ repos only) |
| Repo visibility | None in nav | Each repo shown with status dot + active count |
| Scoping to a repo | Not possible | Click repo → Sessions in sidebar |
| All-repos view | Default (only option) | Still default, still top-level nav items |
| Session list rows | No repo indicator | Repo badge (short name) on each row (2+ repos only) |
| Project list rows | No repo indicator | Repo badge (short name) on each row (2+ repos only) |
| Single-repo users | Current UI | Identical — zero changes |
| API: sessions | No repo filter | Optional `repository_id` query param |
| API: projects | No repo filter | Optional `repository_id` query param |
| API: new endpoint | — | `GET /api/v1/repositories/summary` for sidebar data |
