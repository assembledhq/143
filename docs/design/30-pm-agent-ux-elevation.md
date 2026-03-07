# 30 - PM Agent UX Elevation

> Surface the PM agent's intelligence through the UI without adding complexity.

## Problem

The PM agent reads your codebase, traces stack traces, learns from past decisions, clusters related issues, and delegates work to agents with specific guidance. But the UI hides all of that. Today:

- **Plans page** (`/plans`): Flat cards with badges. No visibility into what the PM actually read or considered.
- **Prioritization page** (`/prioritization`): Settings form buried in the user dropdown. Fine where it is, but disconnected from the PM's output.
- **Navigation**: PM lives in a user dropdown alongside "General" and "Team" -- treated as config, not a core workflow.
- **No live presence**: Small blue banner when running. No sense of what it's doing.

## Design Principles

1. **Show the thinking, not just the output** -- Surface what the PM read, considered, and decided against
2. **One page, not many** -- Everything PM-related lives on a single page with two tabs
3. **Keep it simple** -- Plain labels, no marketing copy, minimal new UI patterns
4. **Earned trust through transparency** -- Show the evidence behind every decision

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

One new sidebar item. Prioritization stays in the dropdown -- it's configuration, not a daily workflow. The `/plans` route moves to `/pm`.

**Implementation**: Add one entry to `navItems` in `authenticated-layout.tsx`. Remove `/plans` if it existed in nav.

---

## The PM Agent Page (`/pm`)

Single page with two tabs: **Plan** and **Decisions**.

### Tab 1: Plan (default)

Shows the latest PM analysis with status and context.

#### Status Banner

```
┌─────────────────────────────────────────────────────────────────┐
│  PM Agent                                              Active   │
│                                                                 │
│  Last run: 2h ago  ·  12 issues reviewed  ·  Next run: in 2h   │
│  3 tasks delegated  ·  1 completed  ·  2 in progress            │
│                                                                 │
│  [Analyze Now]                                                  │
└─────────────────────────────────────────────────────────────────┘
```

When running:

```
┌─────────────────────────────────────────────────────────────────┐
│  ● PM Agent is analyzing...                          Running    │
│                                                                 │
│  Reading codebase structure...                                  │
│  ├─ Read CLAUDE.md, README.md                                   │
│  ├─ Scanned git history (20 commits)                            │
│  ├─ Reviewing 14 open issues                                    │
│  └─ Checking 3 in-flight agent runs                             │
│                                                                 │
│  Phase: Prioritizing and clustering issues                      │
└─────────────────────────────────────────────────────────────────┘
```

#### Situation Analysis + Context Stats

```
┌─────────────────────────────────────────────────────────────────┐
│  Situation Analysis                                  2h ago     │
│                                                                 │
│  "Your authentication service has 3 related issues that share   │
│   a root cause in token validation. Two customer-facing bugs    │
│   are trending upward. The team's recent commits suggest active │
│   work on the payments module, so I'm avoiding changes there."  │
│                                                                 │
│  Context considered:                                            │
│  ┌──────────────┐ ┌──────────────┐ ┌──────────────┐             │
│  │ 14 issues    │ │ 3 in-flight  │ │ 8 past runs  │             │
│  │ reviewed     │ │ agent runs   │ │ learned from │             │
│  └──────────────┘ └──────────────┘ └──────────────┘             │
│  ┌──────────────┐ ┌──────────────┐ ┌──────────────┐             │
│  │ 5 recent PRs │ │ 12 past      │ │ 20 commits   │             │
│  │ checked      │ │ decisions    │ │ analyzed     │             │
│  └──────────────┘ └──────────────┘ └──────────────┘             │
└─────────────────────────────────────────────────────────────────┘
```

These stat cards are the single biggest win -- users don't know the PM reads git history, past decisions, in-flight runs, etc. All data already gathered in `context.go`, just count and return.

**Backend**: Extend PM plan API response with: issues_reviewed, in_flight_runs_checked, past_outcomes_reviewed, recent_prs_checked, past_decisions_reviewed, commits_analyzed.

#### Task Cards

```
┌─────────────────────────────────────────────────────────────────┐
│  #1 · Fix token refresh race condition                          │
│  ┌────────┐ ┌────────┐ ┌───────────┐                           │
│  │ simple │ │ high   │ │ delegated │                           │
│  └────────┘ └────────┘ └───────────┘                           │
│                                                                 │
│  Reasoning                                                      │
│  "3 issues share a root cause in auth/token.go:142. Customer    │
│   impact is rising (47 affected users, up 30% this week)."      │
│                                                                 │
│  Approach                                                       │
│  "The race condition is in refreshToken() at auth/token.go:142  │
│   where the mutex isn't held across the network call. Add test  │
│   coverage for concurrent refresh scenarios in token_test.go."  │
│                                                                 │
│  Files identified                                               │
│  auth/token.go:142  ·  auth/token_test.go                      │
│                                                                 │
│  Risk: Low -- isolated change, existing test file               │
│  ───────────────────────────────────────────────────────────     │
│  Agent run: Running (2m 14s)                     [View Run →]   │
└─────────────────────────────────────────────────────────────────┘
```

Changes from current:
- **Files identified** section: parse `path:line` patterns from the approach text
- Inline agent run status with duration and link

#### Issue Clusters

```
┌─────────────────────────────────────────────────────────────────┐
│  Clusters                                            2 clusters │
│                                                                 │
│  ┌─ Token validation failures ────────────────────────────────┐ │
│  │  ● AUTH-3f2a  ● AUTH-7b1c  ● AUTH-9d4e                    │ │
│  │  Root cause: Missing null check in validateToken()         │ │
│  │  Strategy: Fix the shared validation path, all three       │ │
│  │  issues resolve with a single change                       │ │
│  └────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
```

#### Skipped Issues

```
  Skipped                                                 3 issues

  ┌─ ISSUE-a1b2 ──────────────────────── already in flight ─┐
  │  "Agent run #47 is already working on this exact issue." │
  └──────────────────────────────────────────────────────────┘

  ┌─ ISSUE-c3d4 ────────────────────── in avoid area ───────┐
  │  "This touches the payments module which is in your      │
  │   avoid areas. Leaving for manual review."               │
  └──────────────────────────────────────────────────────────┘
```

Show the skip reason badge inline with each issue so it's clear the PM made a deliberate call.

---

### Tab 2: Decisions

Shows the PM's track record from the existing `pm_decision_log` table.

```
  Decisions                                             Last 30 days

  Success rate: 73% (11/15 delegated tasks succeeded)

  ┌────────┬───────────┬────────────┬──────────────────────────┐
  │ Date   │ Issue     │ Decision   │ Outcome                  │
  ├────────┼───────────┼────────────┼──────────────────────────┤
  │ Mar 5  │ AUTH-3f2a │ Delegated  │ ✓ Succeeded (PR merged)  │
  │ Mar 5  │ PAY-7b1c  │ Skipped    │ — Still open             │
  │ Mar 4  │ UI-9d4e   │ Delegated  │ ✗ Failed (test failures) │
  │ Mar 3  │ API-2e5f  │ Clustered  │ ✓ Succeeded              │
  └────────┴───────────┴────────────┴──────────────────────────┘
```

Success rate at the top is the strongest proof the PM is valuable. Simple paginated table below it.

**Backend**: Add `GET /api/v1/pm/decisions` endpoint returning paginated decision log entries. The `pm_decision_log` table already has all the data.

---

### Sidebar Status Dot

Small dot next to "PM Agent" in the nav:

- Green dot: recent plan completed
- Pulsing blue: PM is currently running
- No dot: idle / no recent activity

Poll latest plan status on an interval. Makes the PM feel present even on other pages.

---

## Summary

| Area | Current | Proposed |
|------|---------|----------|
| Navigation | Hidden in user dropdown | Top-level sidebar item with status dot |
| Plans page | Flat card output | `/pm` page, Plan tab: status banner, context stats, task cards with file refs |
| Decision history | Not exposed | `/pm` page, Decisions tab: table with success rate |
| Prioritization | In user dropdown | Stays in user dropdown (no change) |
| Task cards | Plain text | Add file references parsed from approach, inline run status |
| Clusters | Flat list | Visual groupings with root cause |
| Skipped issues | List | Show skip reason badge inline |

## Implementation Order

1. **Nav item** -- Add "PM Agent" to sidebar, route to `/pm`
2. **Backend: context counts** -- Add counts to PM plan API response
3. **Plan tab** -- Status banner, context stat cards, enhanced task/cluster/skip views
4. **Backend: decisions endpoint** -- `GET /api/v1/pm/decisions` with pagination
5. **Decisions tab** -- Table with success rate
6. **Status dot** -- Sidebar indicator polling latest plan status

## Backend Changes Required

1. **Extend PM plan API response** with context counts (issues_reviewed, in_flight_runs_checked, past_outcomes_reviewed, recent_prs_checked, past_decisions_reviewed, commits_analyzed)
2. **Add `GET /api/v1/pm/decisions`** endpoint for decision log with pagination
3. **Optional**: PM status endpoint for live progress during analysis

## Non-Goals

- Changing the PM agent's prompt or intelligence -- presentation layer only
- Adding new PM features -- surface what already exists
- Moving prioritization settings -- they're fine where they are
- Adding new pages -- everything fits on one page with two tabs
