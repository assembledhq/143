# 30 - PM Agent UX Elevation

> **Status:** Backlog | **Last reviewed:** 2026-05-06
>
> **Implementation notes:** Core project infrastructure exists. Missing: PM agent status banner, context stats API, file identification from path:line patterns, sidebar status dot.

> Surface the PM agent's intelligence through the existing Sessions page. Add projects as a grouping concept.

## Problem

The PM agent reads your codebase, traces stack traces, learns from past decisions, clusters related issues, and delegates work to agents with specific guidance. But the UI hides all of that:

- **Sessions page** shows PM plans and manual runs as a flat list. No grouping, no context stats, no sense of what the PM considered.
- **Session detail** shows tasks/clusters/skipped but not what the PM read to make those decisions (how many issues reviewed, commits scanned, past decisions learned from).
- **No project concept**: Related work across multiple sessions has no grouping. You can't track "Auth Overhaul" as an ongoing effort.
- **Decision history**: The backend tracks outcomes (`pm_decision_log`) but the UI never shows them. Users can't see if the PM is getting better over time.
- **Prioritization page** is buried in user dropdown, disconnected from the PM's output.

## Design Principles

1. **Enhance, don't add** -- Build on Sessions, don't create new pages
2. **Projects group related work** -- Named containers that span multiple sessions
3. **Show the thinking** -- Surface what the PM read and considered
4. **Keep it simple** -- Plain labels, minimal new UI patterns

---

## Final Navigation Layout

```
┌──────────────────────────────┐
│  Overview                    │
│  Sessions  ●                 │  <-- enhanced, dot shows active PM run
│  Issues                      │
└──────────────────────────────┘

User dropdown (unchanged):
  General
  Integrations
  Agent
  Prioritization
  Team
  Log out
```

No new nav items. Sessions page gets enhanced with project grouping, context stats, and decision history. Prioritization stays in the dropdown.

---

## Projects

A **project** is a named container that groups related sessions, issues, and agent runs over time.

### Where projects come from

1. **User creates**: Click "+ New Project", give it a name and optional description
2. **PM suggests**: When PM analysis finds issue clusters that don't belong to any project, it surfaces them as a suggestion. User accepts (names the project) or dismisses.

### How they relate to sessions

- Sessions can optionally belong to a project
- PM plan sessions get auto-assigned to projects based on which issues they address
- A single PM plan session can touch multiple projects (its tasks get grouped by project in the UI)
- Manual sessions ("Fix This") can also be assigned to a project

### Data model

```
Project {
  id, org_id, name, description, status (active/completed/archived),
  created_by ("user" | "pm_suggestion"), created_at, updated_at
}
```

Issues get a nullable `project_id` FK. Agent runs inherit project from their issue. PM tasks reference project_id when sorted.

---

## Enhanced Sessions Page

### Status Banner (new, top of page)

Shows the PM agent's current state above the session list:

```
┌─────────────────────────────────────────────────────────────────┐
│  PM Agent                                              Active   │
│                                                                 │
│  Last run: 2h ago  ·  14 issues reviewed  ·  Next run: in 2h   │
│  73% success rate (11/15 delegated tasks)                       │
│                                                                 │
│  [Analyze Now]                            [+ New Manual Session] │
└─────────────────────────────────────────────────────────────────┘
```

When PM is running:

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

Replaces the current small blue "Analysis in progress" card with something that shows what the PM is actually doing.

### Session List with Project Grouping

Below the status banner, sessions grouped by project:

```
┌─────────────────────────────────────────────────────────────────┐
│  All  Active  Completed  Failed        [+ New Project]          │
│                                                                 │
│  ┌─ Auth Overhaul ───────────────────────────── active ───────┐ │
│  │  5 issues · 3 resolved · 2 sessions                        │ │
│  │                                                             │ │
│  │  ● Active  PM Analysis · 3 tasks · 1 running      2h ago  │ │
│  │  ✓ Done    PM Analysis · 2 tasks · 2 completed     1d ago  │ │
│  └─────────────────────────────────────────────────────────────┘ │
│                                                                  │
│  ┌─ API Rate Limiting ───────────────────────── active ───────┐ │
│  │  3 issues · 1 resolved · 1 session                         │ │
│  │                                                             │ │
│  │  ✓ Done    PM Analysis · 1 task · 1 completed      3h ago  │ │
│  └─────────────────────────────────────────────────────────────┘ │
│                                                                  │
│  ┌─ PM suggestion ─────────────────────────── needs review ───┐ │
│  │  "3 issues share a root cause in database connection        │ │
│  │   pooling. Group as a project?"                             │ │
│  │  [Accept] [Dismiss]                                         │ │
│  └─────────────────────────────────────────────────────────────┘ │
│                                                                  │
│  Ungrouped                                                       │
│  ┌─────────────────────────────────────────────────────────────┐ │
│  │  ● Active  Manual · Fix CORS header · fix_this     30m ago │ │
│  │  ✗ Failed  PM Analysis · 4 tasks                    2d ago │ │
│  └─────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

Key elements:
- Sessions grouped under their project with a collapsible header
- Project header shows issue count, resolved count, and status
- PM suggestions appear as a distinct card with Accept/Dismiss
- Ungrouped sessions (no project) appear at the bottom
- Existing status filter tabs still work (filter across all projects)
- "+ New Project" button to create manually

### Enhanced Session Detail (click into a session)

The existing session detail view gets two additions:

#### Context Stats (new section, plan sessions only)

Added below the session header, above the situation analysis:

```
  Context considered:
  ┌──────────────┐ ┌──────────────┐ ┌──────────────┐
  │ 14 issues    │ │ 3 in-flight  │ │ 8 past runs  │
  │ reviewed     │ │ agent runs   │ │ learned from │
  └──────────────┘ └──────────────┘ └──────────────┘
  ┌──────────────┐ ┌──────────────┐ ┌──────────────┐
  │ 5 recent PRs │ │ 12 past      │ │ 20 commits   │
  │ checked      │ │ decisions    │ │ analyzed     │
  └──────────────┘ └──────────────┘ └──────────────┘
```

This is the single biggest gap today -- users don't know the PM reads git history, past decisions, in-flight runs, etc. All data is already gathered in `context.go`, just count and return.

#### Files Identified (new section on task cards)

Parse `path:line` patterns from the approach text and show them:

```
  Files: auth/token.go:142 · auth/token_test.go
```

#### Inline Run Status (already exists, keep as-is)

Task cards already show run status, duration, and "View run details" links. No changes needed.

### Decision History (new filter/view on Sessions page)

Add a "Decisions" filter tab alongside All/Active/Completed/Failed:

```
  All  Active  Completed  Failed  Decisions
```

When "Decisions" is selected, the view switches to a table showing the PM's track record across all sessions:

```
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

This surfaces the `pm_decision_log` table data that's already being collected but never shown.

---

### Sidebar Status Dot

Small dot next to "Sessions" in the nav:

- Green: recent plan completed
- Pulsing blue: PM is running
- No dot: idle

---

## Summary

| Area | Current | Proposed |
|------|---------|----------|
| Navigation | Sessions (no indicator) | Sessions with status dot (no new items) |
| Session list | Flat list of all sessions | Grouped by project with project headers |
| Session detail | Tasks, clusters, skipped | Add context stats showing what PM considered |
| Decision history | Not exposed | "Decisions" filter tab on sessions page |
| Projects | Don't exist | Named containers grouping related sessions/issues |
| Project creation | N/A | User-created + PM-suggested from clusters |
| Task cards | Plain text | Add parsed file references |
| Status banner | Small blue card when running | Persistent banner with PM state, stats, live progress |
| Prioritization | In user dropdown | Stays in user dropdown (no change) |

## Implementation Order

1. **Project model + DB migration** -- Add projects table, project_id FK on issues
2. **Backend: project CRUD** -- Create, list, update, archive projects
3. **Backend: PM plan → project sorting** -- Extend PM service to sort tasks/clusters into projects, suggest new projects from unclaimed clusters
4. **Backend: context counts** -- Add counts to session/plan API response
5. **Status banner** -- Replace the blue analysis card with persistent PM status banner
6. **Project grouping on sessions list** -- Group sessions under projects, add project suggestions UI
7. **Context stats on session detail** -- Show what PM considered
8. **Backend: decisions endpoint** -- `GET /api/v1/pm/decisions` with pagination + project info
9. **Decisions filter tab** -- Table view with success rate on sessions page
10. **Status dot** -- Sidebar indicator on Sessions nav item

## Backend Changes Required

1. **New `projects` table** with id, org_id, name, description, status, created_by, timestamps
2. **Add `project_id`** nullable FK to issues table
3. **Project CRUD endpoints**: `GET/POST /api/v1/projects`, `GET/PATCH /api/v1/projects/{id}`
4. **Extend PM service** to sort plan output into projects and generate project suggestions
5. **Extend session API response** with context counts (issues_reviewed, in_flight_runs_checked, past_outcomes_reviewed, recent_prs_checked, past_decisions_reviewed, commits_analyzed)
6. **Add `GET /api/v1/pm/decisions`** endpoint with pagination and project join
7. **Optional**: PM status endpoint for live progress during analysis

## Non-Goals

- New pages -- everything lives on the enhanced Sessions page
- Full project management (milestones, deadlines, sprints) -- projects are just named groups
- Changing the PM agent's prompt or intelligence -- presentation layer only (except project sorting)
- Moving prioritization settings -- they're fine in the dropdown
