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

> **Key insight (updated 2025-03-15):** Most users will realistically have **1-2 repos**. Power users may reach **3-5 repos**. Having 6+ repos is a rare outlier, not a design target. This dramatically changes which approaches make sense — we should optimize for what feels great at 1-2 repos and still works at 5, rather than designing for 50.

| Persona | Repos | % of Users (est.) | Session Volume | Primary Need |
|---------|-------|--------------------|---------------|--------------|
| **Solo dev, single repo** | 1 | ~50% | Low-medium | Simplicity. Zero overhead from repo organization. |
| **Solo dev or small team, 2 repos** | 2 | ~25% | Medium | Clear separation without ceremony. See both at a glance. |
| **Multi-repo user** | 3-5 | ~20% | Medium-high | Quick switching, ambient awareness across repos. |
| **Heavy multi-repo (outlier)** | 6+ | ~5% | High | Scalable navigation. But we should NOT over-index on this persona. |

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

**Verdict:** **Strong — now the top recommendation.** With a realistic max of ~5 repos, the scaling concern (the main knock against this approach) evaporates. Five repos with nested Sessions/Projects fits comfortably in a sidebar. For 1-repo users, simply don't show the repo section — their experience is unchanged. This is the sweet spot for the actual user base.

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

**Verdict:** **Weakened by the repo count reality.** A dropdown filter is overkill for 2-3 repos — it's an extra click to open a menu that shows two items. It hides repos behind an interaction when you could just show them all in the nav. This approach screams "enterprise software" when the reality is much simpler. Still viable as a secondary mechanism on list pages, but not recommended as the primary approach.

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

**Verdict:** **Over-abstracted for the typical user.** A dropdown that toggles between "repo-a" and "repo-b" works but feels like ceremony — you can see both repos at once in the nav, so why hide one behind a selector? The "where did my stuff go?" risk is real and the payoff (hiding 1-4 other repos) isn't worth the UX cost. Not recommended as primary approach.

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

**Verdict:** **Not recommended.** With only 1-2 repos, a dashboard of repo cards is a useless landing page showing one or two cards. The drill-down adds navigation depth (3 clicks to a session) for no real benefit. Even at 5 repos, the Overview page can surface this information more efficiently inline rather than as a dedicated card grid.

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

**Verdict:** **Not recommended.** At 2-5 repos the tabs work visually, but they create two competing navigation axes (tabs for repos + sidebar for sections) that are confusing. The sidebar ribbons (Idea 1) give the same one-click switching without the cognitive overhead of a second nav layer.

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

**Verdict:** **Interesting but unnecessary.** The progressive tiers (1 repo, 2-5, 6+, 15+) are elegant in theory, but if 95% of users land in the 1-5 range, you're building and maintaining 4 UI modes for a distribution that barely spans two of them. The simpler approach: build one UI that works for 1-5 (Idea 1 with auto-hide for single repo) and don't worry about the other tiers until real usage demands it.

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

**Verdict:** **Over-engineered for the actual user base.** This is the "Linear/Notion" approach and it's powerful, but it's solving for a scale that doesn't exist. Two mental models (global scope vs. inline groups), complex state management, and a repo switcher dropdown — all to manage 2-3 repos. The nav ribbons (Idea 1) give you the same scoping with far less machinery.

---

## Recommendation

> **Updated 2025-03-15** — Revised based on the insight that most users have 1-2 repos, with a realistic ceiling of ~5. The original recommendation (filter dropdown + repo switcher) was over-built for this reality.

### Primary Recommendation: Idea 1 (Repo Ribbons in Left Nav)

**Go with Idea 1 (Repo Ribbons)** as the primary and likely only approach needed.

The main concern with nav ribbons was always scaling — but that concern evaporates when the realistic max is 5 repos. Five repos with nested Sessions/Projects is ~15 lines of sidebar nav, which is completely comfortable.

#### How it works by repo count:

**For 1 repo (majority of users):**
- No change to current UI whatsoever
- No repo section in the nav
- Sessions and Projects show everything (already scoped to one repo implicitly)
- These users never even know repo organization exists

**For 2-5 repos:**
- Repos appear in the sidebar below the main nav items
- Each repo expands/collapses to show Sessions and Projects counts
- Clicking a repo's Sessions shows a scoped list
- Top-level "Sessions" and "Projects" nav items remain as the "all repos" view
- Status dots/counts on each repo give ambient awareness without clicking

```
├── Overview
├── Sessions (7)         ← all repos
├── Projects (4)         ← all repos
├── ─────────────
├── owner/repo-a    ● 3
│   ├── Sessions
│   └── Projects
├── owner/repo-b      1
│   ├── Sessions
│   └── Projects
```

The status dots and counts give ambient awareness ("repo-a has 3 active sessions, one needs attention") without clicking into anything.

**On the list pages themselves:** Add a subtle repo badge on each session/project row so users in the "all repos" view can tell which repo an item belongs to. No dropdown filter needed — the nav handles scoping.

#### Why this wins now:

- **Simplest possible change** for the actual user base
- **Zero overhead** for single-repo users (auto-hidden)
- **One-click scoping** for multi-repo users (no dropdown ceremony)
- **Ambient awareness** across repos without navigating anywhere
- **Familiar pattern** (GitHub sidebar, Slack channels, VS Code explorer)
- **Low implementation cost** — just nav items with filtered list views
- **No complex state management** — scoping is just a route/URL, not global state

### What I would NOT recommend (given ~1-5 repos reality):

| Idea | Why it's now a poor fit |
|------|------------------------|
| **Idea 2: Filter Dropdown** | Overkill. A dropdown that shows 2 items feels like enterprise software. Hides repos behind an interaction when you could just show them all. |
| **Idea 3: Global Repo Switcher** | Over-abstracted. A selector toggling between 2 repos is ceremony, not efficiency. You can see both repos in the nav — why hide one? Also risks "where did my stuff go?" confusion. |
| **Idea 4: Repo Cards Dashboard** | A dashboard showing 1-2 cards is a useless landing page. Adds navigation depth for no benefit. |
| **Idea 5: Tabs/Workspaces** | Creates two competing navigation axes. The sidebar ribbons give the same one-click switching without the cognitive overhead. |
| **Idea 6: Progressive Disclosure** | Building 4 UI modes for a distribution that barely spans 2 of them. Just build one UI that works for 1-5. |
| **Idea 7: Hybrid Switcher + Grouping** | Two mental models, complex state management, and a repo switcher dropdown — all to manage 2-3 repos. The nav ribbons give the same scoping with far less machinery. |

### If we're wrong about repo counts:

If usage data eventually shows a meaningful segment of users with 6+ repos, the nav ribbons degrade gracefully:
- Add a **"pinned repos"** concept so users curate which repos appear expanded
- Collapse non-pinned repos into a compact list
- Add a small search/filter within the repo section

This is a natural evolution of Idea 1, not a rewrite. But don't build it until the data says you need it.

---

## Implementation Considerations

See [34-repo-ribbons-nav.md](design/backlog/34-repo-ribbons-nav.md) for the full PRD with API specs, SQL queries, frontend implementation details, and edge cases.

---

## Open Questions

1. How do **issues** fit in? Today they're accessed through the PM decisions flow. Should they also be repo-grouped?
2. Should repo organization also affect **notifications/alerts**? (e.g., "repo-a has a failing session" vs. generic "a session failed")
3. For teams: should repo scoping intersect with **team member assignment**? (e.g., "show me sessions for repos I own")
