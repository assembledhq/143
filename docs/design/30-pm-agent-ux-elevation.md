# 30 - PM Agent UX Elevation

> Surface the PM agent's intelligence through the UI. Add projects as the primary organizing concept.

## Problem

The PM agent reads your codebase, traces stack traces, learns from past decisions, clusters related issues, and delegates work to agents with specific guidance. But the UI hides all of that. Today:

- **Plans page** (`/plans`): Flat cards with badges. No visibility into what the PM actually read or considered.
- **Prioritization page** (`/prioritization`): Settings form buried in the user dropdown. Fine where it is, but disconnected from the PM's output.
- **Navigation**: PM lives in a user dropdown alongside "General" and "Team" -- treated as config, not a core workflow.
- **No project concept**: Work is organized as a flat list of issues. No way to group related work into named projects that track progress over time.

## Design Principles

1. **Projects as the organizing concept** -- Related work groups into named projects you can track over time
2. **Show the thinking, not just the output** -- Surface what the PM read, considered, and decided against
3. **One page, two tabs** -- Everything PM-related lives on a single page
4. **Keep it simple** -- Plain labels, minimal new UI patterns

---

## Final Navigation Layout

```
┌──────────────────────────────┐
│  Overview                    │
│  Sessions                    │
│  Issues                      │
│  PM Agent  ●                 │  <-- new top-level item, dot shows status
└──────────────────────────────┘

User dropdown (unchanged):
  General
  Integrations
  Agent
  Prioritization                   <-- stays here, it's settings
  Team
  Log out
```

One new sidebar item. Prioritization stays in the dropdown. The `/plans` route moves to `/pm`.

---

## Projects

A **project** is a named container that groups related issues, agent runs, and PM decisions together over time. Think "Auth Overhaul" or "API Rate Limiting" -- ongoing efforts, not one-off analyses.

### Where projects come from

Both user-created and PM-suggested:

1. **User creates**: Name a project, optionally assign issues to it
2. **PM suggests**: When the PM runs its global analysis and clusters related issues, it can propose a new project. The user approves or dismisses the suggestion. PM clusters that map to an existing project get filed there automatically.

### How PM analysis works with projects

PM still runs globally -- it analyzes all open issues, all in-flight runs, all past decisions. But when it produces its plan, it sorts tasks and clusters into projects:

- Tasks linked to issues in an existing project go under that project
- New clusters with no project get surfaced as a "suggested project"
- Uncategorized tasks (one-off fixes, no clear grouping) appear in an "Unassigned" section

This keeps the PM simple (one global run) while giving users project-level organization.

### Project data model

```
Project {
  id
  org_id
  name                    // "Auth Overhaul"
  description             // optional, brief summary
  status                  // active, completed, archived
  created_by              // "user" or "pm_suggestion"
  created_at
  updated_at
}
```

Issues get a nullable `project_id` foreign key. Agent runs inherit project from their issue. PM tasks reference project_id when sorted.

---

## The PM Agent Page (`/pm`)

Single page with two tabs: **Projects** (default) and **Decisions**.

### Tab 1: Projects (default)

#### Status Banner (top of page, above tabs)

```
┌─────────────────────────────────────────────────────────────────┐
│  PM Agent                                              Active   │
│                                                                 │
│  Last run: 2h ago  ·  14 issues reviewed  ·  Next run: in 2h   │
│                                                                 │
│  Context considered:                                            │
│  14 issues · 3 in-flight runs · 12 past decisions · 20 commits │
│                                                                 │
│  [Analyze Now]                                                  │
└─────────────────────────────────────────────────────────────────┘
```

When running:

```
┌─────────────────────────────────────────────────────────────────┐
│  ● PM Agent is analyzing...                          Running    │
│                                                                 │
│  ├─ Read CLAUDE.md, README.md                                   │
│  ├─ Scanned git history (20 commits)                            │
│  ├─ Reviewing 14 open issues                                    │
│  └─ Checking 3 in-flight agent runs                             │
│                                                                 │
│  Phase: Prioritizing and clustering issues                      │
└─────────────────────────────────────────────────────────────────┘
```

#### Project List

Below the status banner, a list of active projects:

```
┌─────────────────────────────────────────────────────────────────┐
│  Projects                                     [+ New Project]   │
│                                                                 │
│  ┌─ Auth Overhaul ──────────────────────────────── active ────┐ │
│  │  5 issues · 3 resolved · 2 agent runs in progress          │ │
│  │  Last activity: 1h ago                                     │ │
│  └────────────────────────────────────────────────────────────┘ │
│                                                                 │
│  ┌─ API Rate Limiting ─────────────────────────── active ────┐ │
│  │  3 issues · 1 resolved · 1 agent run completed             │ │
│  │  Last activity: 3h ago                                     │ │
│  └────────────────────────────────────────────────────────────┘ │
│                                                                 │
│  ┌─ PM suggestion ──────────────────────────── needs review ─┐ │
│  │  "3 issues share a root cause in database connection       │ │
│  │   pooling. Consider grouping as a project."                │ │
│  │  3 issues · [Accept] [Dismiss]                             │ │
│  └────────────────────────────────────────────────────────────┘ │
│                                                                 │
│  Unassigned tasks                                    2 tasks    │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │  #1 · Fix CORS header on /api/health  · simple · high     │ │
│  │  #2 · Update deprecated lodash call   · trivial · high    │ │
│  └────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

Key elements:
- Active projects show issue count, resolved count, in-flight runs
- PM suggestions appear as a distinct card type with Accept/Dismiss actions
- Unassigned tasks (from latest PM plan, no project) appear at the bottom
- "+ New Project" button to create manually

#### Project Detail (click into a project)

Clicking a project navigates to `/pm/{project_id}`. Back link returns to `/pm`.

```
┌─ ← Back to PM Agent
│
│  Auth Overhaul                                         active
│  5 issues · 3 resolved · 60% complete
│
│  ┌─────────────────────────────────────────────────────────┐
│  │  Situation                                              │
│  │  "3 related auth issues share a root cause in token     │
│  │   validation. 2 have been resolved by agent runs,       │
│  │   1 is in progress."                                    │
│  └─────────────────────────────────────────────────────────┘
│
│  Tasks
│  ┌─────────────────────────────────────────────────────────┐
│  │  #1 · Fix token refresh race condition                  │
│  │  simple · high · delegated                              │
│  │                                                         │
│  │  Reasoning                                              │
│  │  "Root cause in auth/token.go:142. Customer impact      │
│  │   rising (47 affected users, up 30% this week)."        │
│  │                                                         │
│  │  Approach                                               │
│  │  "Race condition in refreshToken() at auth/token.go:142 │
│  │   -- mutex not held across network call. Add test       │
│  │   coverage in token_test.go."                           │
│  │                                                         │
│  │  Files: auth/token.go:142 · auth/token_test.go          │
│  │  Risk: Low                                              │
│  │  ─────────────────────────────────────────────────────── │
│  │  Agent run: Running (2m 14s)            [View Run →]    │
│  └─────────────────────────────────────────────────────────┘
│
│  ┌─────────────────────────────────────────────────────────┐
│  │  #2 · Add null check in validateToken()                 │
│  │  trivial · high · ✓ completed                           │
│  │  Agent run: Succeeded · PR #142 merged  [View Run →]    │
│  └─────────────────────────────────────────────────────────┘
│
│  Clusters
│  ┌─ Token validation failures ────────────────────────────┐
│  │  ● AUTH-3f2a  ● AUTH-7b1c  ● AUTH-9d4e                 │
│  │  Root cause: Missing null check in validateToken()      │
│  │  Strategy: Fix the shared validation path               │
│  └────────────────────────────────────────────────────────┘
│
│  Skipped                                          1 issue
│  ┌─ AUTH-f1e2 ───────────────── already in flight ───────┐
│  │  "Agent run #47 is already working on this."          │
│  └───────────────────────────────────────────────────────┘
│
└─────────────────────────────────────────────────────────────
```

The detail view shows everything scoped to this project: its tasks, clusters, skipped issues, and completed work. Completed tasks show their outcome inline.

---

### Tab 2: Decisions

Global view across all projects. Shows the PM's overall track record.

```
  Decisions                                             Last 30 days

  Success rate: 73% (11/15 delegated tasks succeeded)

  ┌──────────┬──────────────────┬───────────┬────────────┬──────────────────┐
  │ Date     │ Project          │ Issue     │ Decision   │ Outcome          │
  ├──────────┼──────────────────┼───────────┼────────────┼──────────────────┤
  │ Mar 5    │ Auth Overhaul    │ AUTH-3f2a │ Delegated  │ ✓ Succeeded      │
  │ Mar 5    │ —                │ PAY-7b1c  │ Skipped    │ — Still open     │
  │ Mar 4    │ Auth Overhaul    │ UI-9d4e   │ Delegated  │ ✗ Failed         │
  │ Mar 3    │ API Rate Limit   │ API-2e5f  │ Clustered  │ ✓ Succeeded      │
  └──────────┴──────────────────┴───────────┴────────────┴──────────────────┘
```

Includes project column so you can see patterns per project. Paginated.

**Backend**: Add `GET /api/v1/pm/decisions` endpoint returning paginated decision log entries with project info.

---

### Sidebar Status Dot

Small dot next to "PM Agent" in the nav:

- Green: recent plan completed
- Pulsing blue: PM is running
- No dot: idle

---

## Summary

| Area | Current | Proposed |
|------|---------|----------|
| Navigation | Hidden in user dropdown | Top-level sidebar item with status dot |
| Organizing concept | Flat list of issues/plans | Named projects grouping related work |
| Plans page | Flat card output | `/pm` page, Projects tab with list → detail drill-down |
| Decision history | Not exposed | `/pm` page, Decisions tab with global table + success rate |
| Prioritization | In user dropdown | Stays in user dropdown (no change) |
| Task cards | Plain text | Add file references, inline run status |
| Project creation | N/A | User-created + PM-suggested from clusters |

## Implementation Order

1. **Project model + DB migration** -- Add projects table, project_id FK on issues
2. **Nav item** -- Add "PM Agent" to sidebar, route to `/pm`
3. **Backend: project CRUD** -- Create, list, update, archive projects
4. **Backend: PM plan → project sorting** -- Extend PM service to sort tasks/clusters into projects, suggest new projects from unclaimed clusters
5. **Backend: context counts** -- Add counts to PM plan API response
6. **Projects tab** -- Status banner, project list, project detail view
7. **Backend: decisions endpoint** -- `GET /api/v1/pm/decisions` with pagination + project info
8. **Decisions tab** -- Table with success rate
9. **Status dot** -- Sidebar indicator

## Backend Changes Required

1. **New `projects` table** with id, org_id, name, description, status, created_by, timestamps
2. **Add `project_id`** nullable FK to issues table
3. **Project CRUD endpoints**: `GET/POST /api/v1/pm/projects`, `GET/PATCH /api/v1/pm/projects/{id}`
4. **Extend PM service** to sort plan output into projects and generate project suggestions
5. **Extend PM plan API response** with context counts
6. **Add `GET /api/v1/pm/decisions`** endpoint with pagination and project join
7. **Optional**: PM status endpoint for live progress during analysis

## Non-Goals

- Changing the PM agent's prompt or intelligence -- presentation layer only (except project sorting logic)
- Full project management features (milestones, deadlines, assignments) -- keep it simple
- Moving prioritization settings -- they're fine where they are
- Multiple pages -- one page with two tabs + detail drill-down
