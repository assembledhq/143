# Brainstorm: Organizing Sessions & Projects by Repository

**Date:** 2025-03-15
**Status:** Brainstorm / RFC

---

## Problem Statement

Today, all sessions and projects live in a single flat list. As users connect more repositories, these lists become noisy and hard to navigate. Users need a way to focus on work relevant to a specific repo without losing the ability to see everything at once.

---

## Current State

- **Sessions** are listed flat with status-based filter tabs (All, Active, Needs Guidance, Failed, Done, Decisions). When "All" is selected, they group automatically by status. Sessions link to repos indirectly through issues.
- **Projects** are listed flat with status-based filter tabs (All, Active, Scheduled, Draft, Completed, Paused). Projects link directly to a `repository_id`.
- **Left Nav** has three main items: Overview, Sessions, Projects. No repo awareness.
- **Repo Settings** exist at `/repositories/[id]` but are admin/config pages, not navigational anchors.

---

## User Personas to Consider

| Persona | Repos | Session Volume | Primary Need |
|---------|-------|---------------|--------------|
| **Solo dev, single repo** | 1 | Low-medium | Simplicity. No overhead from repo organization. |
| **Solo dev, multi-repo** | 3-8 | Medium | Quick switching between repos. Clear separation. |
| **Small team, monorepo** | 1 | High | Status-based filtering is more important than repo filtering. |
| **Small team, multi-repo** | 5-15 | High | Repo-scoped views. Possibly different team members own different repos. |
| **Org/enterprise, many repos** | 15-50+ | Very high | Must have repo scoping or it's unusable. Search, favorites, recent repos. |

---

## Ideas

### Idea 1: Repo Ribbons in Left Nav (User's Idea A)

**Concept:** Add a list of connected repositories in the left sidebar. Each repo expands to show its own Sessions and Projects sub-nav items. Clicking "Sessions" under "repo-x" shows only sessions for that repo.

```
Left Nav:
├── Overview
├── Sessions (all)
├── Projects (all)
├── ─────────────
├── owner/repo-a
│   ├── Sessions
│   └── Projects
├── owner/repo-b
│   ├── Sessions
│   └── Projects
└── Settings ▾
```

**Pros:**
- Always visible, one-click access to any repo's work
- Familiar pattern (GitHub, GitLab, Slack channels)
- Clear mental model: "I'm looking at repo X's stuff"
- Status indicators (pinging dots) can be per-repo, giving at-a-glance awareness

**Cons:**
- Doesn't scale past ~8-10 repos without scrolling or collapsing
- Adds visual weight for single-repo users who get no benefit
- Duplicates nav items (Sessions appears N+1 times)
- Sidebar width (currently w-64) may feel cramped with long repo names like `organization/my-very-long-repo-name`

**Mitigations:**
- Auto-hide the repo section when only 1 repo is connected
- Add a "starred/pinned repos" concept so heavy users can curate
- Collapse repos by default, expand on click
- Truncate long repo names with tooltip on hover

**Verdict:** Strong for 2-8 repo users. Needs escape hatches for 1-repo and 15+ repo users.

---

### Idea 2: Repo as Filter/Group-By on List Pages (User's Idea B)

**Concept:** Keep the nav simple. Add a repo filter dropdown or group-by toggle on the Sessions and Projects pages themselves. Similar to how status filtering works today.

```
Sessions Page:
┌──────────────────────────────────────────┐
│ Sessions                                  │
│ [All] [Active] [Failed] [Done]           │
│ Repo: [All repos ▾]  ← new filter       │
│                                          │
│ ▸ owner/repo-a (3 active)               │
│   • Session abc...                        │
│   • Session def...                        │
│ ▸ owner/repo-b (1 active)               │
│   • Session ghi...                        │
└──────────────────────────────────────────┘
```

**Pros:**
- Minimal UI change, low implementation cost
- Composes naturally with existing status filters (repo + status)
- Zero overhead for single-repo users (filter just doesn't appear or shows one option)
- Scales to any number of repos (dropdown can search/scroll)
- Familiar pattern (Jira, Linear, Sentry all do filter-based scoping)

**Cons:**
- Two clicks to scope (open dropdown, select repo) vs. one click with nav ribbons
- No cross-page persistence unless repo filter is stored in URL or global state
- Loses ambient awareness of other repos' status while filtered
- Group-by repo can create very long pages if many repos have active work

**Mitigations:**
- Persist selected repo filter in URL params (already using `nuqs` for status)
- Add a "sticky" repo filter that persists across Sessions ↔ Projects navigation
- Show repo counts in the dropdown so users can spot active repos quickly

**Verdict:** Safe, scalable, low-risk. But less powerful for users who live in multi-repo workflows.

---

### Idea 3: Repo Switcher (Global Context Selector)

**Concept:** A prominent repo switcher at the top of the sidebar (or in a top bar) that sets a global context. When a repo is selected, ALL pages (Overview, Sessions, Projects) are scoped to that repo. An "All repos" option shows everything.

```
Left Nav:
┌─────────────────────┐
│ [owner/repo-a  ▾]   │  ← global repo selector
├─────────────────────┤
│ Overview             │
│ Sessions             │
│ Projects             │
│                      │
│ Repo Settings        │  ← contextual, shows for selected repo
└─────────────────────┘
```

**Pros:**
- Clean, minimal UI. One control governs everything.
- Matches mental model of "I'm working on repo X right now"
- Overview page becomes per-repo dashboard (very powerful)
- Scales infinitely (selector is a searchable dropdown)
- Single-repo users never even see it (auto-selects their one repo)
- Familiar from: Vercel (project switcher), AWS (region selector), Datadog (environment selector)

**Cons:**
- Hides cross-repo awareness entirely when scoped
- "All repos" mode needs to be obvious and easy to return to
- Global state is tricky — URL encoding, deep links, sharing links
- Users may not realize they're filtered and wonder where sessions went

**Mitigations:**
- Persist in URL: `/sessions?repo=repo-a` or use path prefix `/repo-a/sessions`
- Show a colored banner or indicator when scoped to a specific repo
- Show a subtle "viewing 3 of 12 sessions (filtered to repo-a)" message
- Keyboard shortcut to toggle scope (Cmd+K style repo switcher)

**Verdict:** Best for power users and teams. Clean UX. But risks "where did my stuff go?" confusion if not carefully communicated.

---

### Idea 4: Repo-First Dashboard (Repo Cards → Drill Down)

**Concept:** The main landing page shows repo cards with summary stats. Clicking a repo drills into a repo-specific view with its sessions, projects, and settings. A breadcrumb or back button returns to the overview.

```
Overview Page:
┌──────────────┐  ┌──────────────┐  ┌──────────────┐
│ owner/repo-a │  │ owner/repo-b │  │ owner/repo-c │
│              │  │              │  │              │
│ 3 active     │  │ 1 active     │  │ idle         │
│ 2 projects   │  │ 0 projects   │  │ 1 project    │
│ ● running    │  │ ⚠ guidance   │  │ ○ quiet      │
└──────────────┘  └──────────────┘  └──────────────┘

→ Click "repo-a" →

Repo-A Dashboard:
├── Sessions (scoped)
├── Projects (scoped)
├── Issues
└── Settings
```

**Pros:**
- Gives birds-eye view across all repos at once
- Natural drill-down flow
- Each repo becomes a "workspace" with full context
- Great for checking in on multiple repos quickly ("which repo needs attention?")
- Could show health indicators, last activity, confidence scores per repo

**Cons:**
- Adds a navigation layer (overview → repo → sessions/projects)
- Three clicks to reach a specific session (overview → repo → sessions → session)
- Doesn't help if user wants to see all sessions across repos at once
- Single-repo users get a useless overview page with one card

**Mitigations:**
- Keep top-level Sessions/Projects nav items for cross-repo views
- Auto-redirect single-repo users directly to their repo dashboard
- Add quick-action buttons on cards (e.g., "View active sessions" badge is clickable)

**Verdict:** Excellent for situational awareness. Works best as a complement to another approach, not standalone.

---

### Idea 5: Tabs/Workspaces Per Repo

**Concept:** Horizontal tabs at the top of the content area, one per repo. Users can pin repos they care about. Each tab maintains its own scroll position and filter state.

```
┌─────────────┬─────────────┬─────────────┬─────┐
│ All repos   │ repo-a      │ repo-b      │  +  │
└─────────────┴─────────────┴─────────────┴─────┘
┌──────────────────────────────────────────────────┐
│ Sessions for repo-a                               │
│ [All] [Active] [Failed] [Done]                   │
│ ...                                              │
└──────────────────────────────────────────────────┘
```

**Pros:**
- Very fast switching (single click, always visible)
- Each tab can maintain independent filter state
- "All repos" tab always available
- Browser-tab metaphor is universally understood
- Can show status indicators on tabs (colored dot, count badge)

**Cons:**
- Takes up vertical space
- Doesn't scale past 5-6 tabs visually
- Interacts awkwardly with Sessions/Projects nav (two axes of navigation)
- Adds complexity to URL state management

**Mitigations:**
- Pin/unpin repos to control which tabs appear
- Overflow menu for additional repos
- Tab order reflects recency or activity

**Verdict:** Fast and familiar, but adds UI complexity. Better suited as a feature within Sessions/Projects pages rather than a global concept.

---

### Idea 6: Smart Defaults with Progressive Disclosure

**Concept:** Don't force a structure. Instead, use intelligence to surface what matters. Default view is a smart feed sorted by relevance/recency. Repo grouping appears progressively as the user's repo count grows.

```
1 repo:   Flat list, no repo UI at all
2-5 repos: Subtle repo badges on items + optional group-by toggle
6+ repos:  Group-by repo default + repo filter dropdown
15+ repos: Full repo switcher + search
```

**Pros:**
- Zero complexity for new/simple users
- Adapts to user's actual scale
- Avoids premature abstraction
- Each tier builds on the previous one

**Cons:**
- Inconsistent UI across user segments (support/docs complexity)
- Users who grow from 1 → 5 repos experience UI shifts
- Harder to build and test (multiple modes)
- Power users can't "opt in" to advanced features early

**Mitigations:**
- Allow manual override in settings ("always show repo organization")
- Smooth transitions between tiers (e.g., animate in the repo filter)
- Feature flags for early adopters

**Verdict:** Best long-term UX philosophy, but higher engineering cost and test surface.

---

### Idea 7: Hybrid — Repo Switcher + Inline Grouping

**Concept:** Combine the global repo switcher (Idea 3) with inline repo grouping on list pages (Idea 2). The switcher sets a persistent scope, but in "All repos" mode, items are grouped by repo with collapsible sections.

```
Left Nav:
┌─────────────────────┐
│ [All repos      ▾]  │
├─────────────────────┤
│ Overview             │
│ Sessions (7)         │
│ Projects (4)         │

Sessions Page (All repos mode):
│ ▾ owner/repo-a          3 active │
│   • Session: Fix auth bug    ●   │
│   • Session: Add tests       ●   │
│ ▾ owner/repo-b          1 active │
│   • Session: Update deps     ●   │

Sessions Page (repo-a selected):
│ [All] [Active] [Failed] [Done]   │
│ • Session: Fix auth bug      ●   │
│ • Session: Add tests         ●   │
│ • Session: Refactor API      ✓   │
```

**Pros:**
- Best of both worlds: ambient awareness + focused work
- "All repos" mode gives cross-repo overview with structure
- Scoped mode gives clean, focused lists
- Scales from 1 to 50+ repos
- Single-repo users: switcher auto-selects, they never think about it

**Cons:**
- Most complex to implement
- Two mental models to learn (global scope vs. inline groups)
- URL/state management is more involved

**Verdict:** Highest ceiling. This is the "Linear/Notion" approach — powerful but polished.

---

## Recommendation

### For MVP (ship fast, learn):

**Go with Idea 2 (Filter/Group-By) + lightweight elements of Idea 3 (Repo Switcher).**

Specifically:
1. Add a **repo filter dropdown** to both Sessions and Projects pages, next to existing status filters
2. When a repo is selected, **persist it in URL params** (`?repo=repo-id`) using the existing `nuqs` setup
3. In "All repos" mode, add **repo badges** on each session/project row (the `full_name` from the repository)
4. Optionally add a **"Group by repo"** toggle that sections the list by repo with collapsible headers
5. On the **Overview page**, add per-repo summary cards showing active session count, project count, and health status

This gives you:
- **Zero overhead** for single-repo users (dropdown shows one option, badges are redundant, so hide both)
- **Useful filtering** for multi-repo users
- **Low implementation cost** (you already have repo data on projects via `repository_id`, and sessions via their linked issues)
- **Data to inform the next step** (track which repos people filter to most, whether they use group-by, etc.)

### For V2 (after learning from MVP):

If usage data shows that users frequently filter to one repo and stay there, upgrade to **Idea 7 (Hybrid)** with a proper global repo switcher in the sidebar. This is a natural evolution: the filter dropdown graduates to a first-class navigation element.

If usage data shows users prefer the birds-eye view, lean into **Idea 4 (Repo Cards)** on the Overview page and keep the flat lists with filters on Sessions/Projects.

### What I'd avoid:

- **Idea 1 (Nav Ribbons)** as the primary approach — it front-loads complexity and doesn't scale. Fine as a complement for pinned/favorite repos later.
- **Idea 5 (Tabs)** — creates two competing navigation axes and is hard to evolve.

---

## Implementation Considerations

### Data availability
- **Projects** already have `repository_id` — grouping/filtering is straightforward
- **Sessions** link to repos through `issue_id → issue.repository_id` — you may need to denormalize `repository_id` onto sessions, or join through issues in the API

### API changes needed
- Add `repository_id` filter param to `GET /api/v1/sessions`
- Add `repository_id` filter param to `GET /api/v1/projects` (if not already supported)
- Consider a `GET /api/v1/repositories/summary` endpoint that returns per-repo counts (active sessions, projects, etc.) for the overview cards

### URL state
- Extend `nuqs` usage: `?status=active&repo=abc123`
- Consider using repo `full_name` slug in URL for readability: `?repo=owner/repo-name`

### Performance
- Repo list should be cached/memoized (it changes rarely)
- Group-by-repo rendering should use virtualization if >50 items
- Per-repo counts can be derived client-side from existing list data initially, then moved to a dedicated API if performance becomes an issue

---

## Open Questions

1. Should the repo filter be **cross-page persistent** (selecting repo-a on Sessions also filters Projects)? This is more powerful but adds global state complexity.
2. Should the **Overview page** become repo-aware? Per-repo dashboards could be very valuable but are a bigger lift.
3. How do **issues** fit in? Today they're accessed through the PM decisions flow. Should they also be repo-grouped?
4. Should repo organization also affect **notifications/alerts**? (e.g., "repo-a has a failing session" vs. generic "a session failed")
5. For teams: should repo scoping intersect with **team member assignment**? (e.g., "show me sessions for repos I own")
