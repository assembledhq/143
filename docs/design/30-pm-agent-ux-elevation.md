# 30 - PM Agent UX Elevation

> Make the PM agent feel like the intelligent orchestrator it is, not a settings form with textareas.

## Problem

The PM agent is the most sophisticated part of the system -- it reads your codebase, traces stack traces to source files, learns from past decisions, clusters related issues, and orchestrates autonomous agents with code-grounded guidance. But the UI tells none of that story. Today:

- **Prioritization page** (`/prioritization`): A settings form with textareas for philosophy/direction, tag inputs for focus/avoid areas, and sliders for priority weights. It looks like a config panel, not a command center.
- **Plans page** (`/plans`): Shows the latest plan output as flat cards with badges. No sense of the analysis journey, no visibility into what the PM actually read or considered.
- **Navigation**: PM-related pages are buried in a user dropdown menu alongside "General" and "Team" settings -- treating it as admin config rather than a core workflow.
- **No live presence**: When the PM agent is running, there's a small blue banner. No sense of what it's doing, what it's reading, what it's deciding.

The result: users see textareas and sliders, not intelligence. The PM agent feels like a settings page, not an orchestra conductor.

## Design Principles

1. **Show the thinking, not just the output** -- Surface what the PM read, considered, and decided against
2. **Promote to primary navigation** -- The PM is a first-class workflow, not a settings submenu
3. **Live presence when active** -- The PM should feel alive when it's working
4. **Earned trust through transparency** -- Show the evidence behind every decision

---

## Proposed Changes

### 1. Promote PM Agent to Primary Navigation

**Current**: PM pages hidden in user avatar dropdown alongside settings.

**Proposed**: Add a dedicated top-level nav group in the sidebar.

```
Overview
Sessions
Issues
─────────
PM Agent        <-- new top-level section
  Dashboard     <-- replaces /plans, new richer view
  Context       <-- replaces /prioritization
  Decision Log  <-- new page
```

The PM Agent gets its own nav section with an icon that communicates intelligence/orchestration (e.g., `Brain`, `Sparkles`, or `Orbit` from lucide). This immediately signals "this is a core capability, not a settings panel."

**Implementation**: Update `navItems` in `authenticated-layout.tsx` to add a collapsible PM section. Move `/plans` to `/pm` (or `/pm/dashboard`), `/prioritization` to `/pm/context`.

---

### 2. PM Dashboard (replaces /plans)

Replace the current plans page with a proper dashboard that shows the PM as an active intelligence layer.

#### 2a. Status Hero Banner

At the top, a persistent status card that shows the PM's current state:

```
┌─────────────────────────────────────────────────────────────────┐
│  PM Agent                                              Active  │
│                                                                │
│  Last analysis: 2h ago  ·  12 issues reviewed  ·  Next: in 2h │
│  3 tasks delegated  ·  1 completed  ·  2 in progress           │
│                                                                │
│  [Analyze Now]                                                 │
└─────────────────────────────────────────────────────────────────┘
```

When the PM is actively running, this transforms into a live activity view:

```
┌─────────────────────────────────────────────────────────────────┐
│  ● PM Agent is analyzing...                          Running   │
│                                                                │
│  Reading codebase structure...                                 │
│  ├─ Read CLAUDE.md, README.md                                  │
│  ├─ Scanned git history (20 commits)                           │
│  ├─ Reviewing 14 open issues                                   │
│  └─ Checking 3 in-flight agent runs                            │
│                                                                │
│  Phase: Prioritizing and clustering issues                     │
└─────────────────────────────────────────────────────────────────┘
```

This requires the backend to emit progress events (or the frontend to poll a status endpoint that includes the current phase).

#### 2b. Situation Analysis as a First-Class Section

Instead of a plain text card, render the PM's analysis as a structured narrative:

```
┌─────────────────────────────────────────────────────────────────┐
│  Situation Analysis                                 2h ago     │
│                                                                │
│  "Your authentication service has 3 related issues that share  │
│   a root cause in token validation. Two customer-facing bugs   │
│   are trending upward in occurrence count. The team's recent   │
│   commits suggest active work on the payments module, so I'm   │
│   avoiding changes there."                                     │
│                                                                │
│  Context considered:                                           │
│  ┌──────────────┐ ┌──────────────┐ ┌──────────────┐            │
│  │ 14 issues    │ │ 3 in-flight  │ │ 8 past runs  │            │
│  │ reviewed     │ │ agent runs   │ │ learned from │            │
│  └──────────────┘ └──────────────┘ └──────────────┘            │
│  ┌──────────────┐ ┌──────────────┐ ┌──────────────┐            │
│  │ 5 recent PRs │ │ 12 past      │ │ 20 commits   │            │
│  │ checked      │ │ decisions    │ │ analyzed     │            │
│  └──────────────┘ └──────────────┘ └──────────────┘            │
│                                                                │
└─────────────────────────────────────────────────────────────────┘
```

The "context considered" stat cards make visible the breadth of what the PM agent actually reads. This is the single biggest gap today -- users don't know the PM reads their git history, past decisions, in-flight runs, etc.

**Implementation**: The backend already captures `issues_reviewed` on the plan. Extend the PM plan response to include counts for: issues_reviewed, in_flight_runs_checked, past_outcomes_reviewed, recent_prs_checked, past_decisions_reviewed, commits_analyzed. These are already gathered in `context.go` -- just count them and return the counts.

#### 2c. Task Cards with Evidence Trail

Current task cards show reasoning/approach as plain text. Enhance them to show the evidence:

```
┌─────────────────────────────────────────────────────────────────┐
│  #1 · Fix token refresh race condition                         │
│  ┌────────┐ ┌────────┐ ┌───────────┐                          │
│  │ simple │ │ high   │ │ delegated │                          │
│  └────────┘ └────────┘ └───────────┘                          │
│                                                                │
│  Reasoning                                                     │
│  "3 issues share a root cause in auth/token.go:142. Customer   │
│   impact is rising (47 affected users, up 30% this week)."     │
│                                                                │
│  Approach                                                      │
│  "The race condition is in refreshToken() at auth/token.go:142 │
│   where the mutex isn't held across the network call. Add test │
│   coverage for concurrent refresh scenarios in token_test.go." │
│                                                                │
│  Files identified                                              │
│  auth/token.go:142  ·  auth/token_test.go                     │
│                                                                │
│  Risk: Low -- isolated change, existing test file              │
│  ──────────────────────────────────────────────────────────     │
│  Agent run: Running (2m 14s)                    [View Run →]   │
└─────────────────────────────────────────────────────────────────┘
```

Key changes:
- Keep labels simple and direct: **Reasoning**, **Approach**, **Risk** -- no need to rename what already works
- Add a **"Files identified"** section when the approach contains file paths (parse `path:line` patterns from the approach text)
- Show live run status inline with duration

#### 2d. Issue Clusters as a Visual Group

Instead of a flat list of clusters, show them as visual groupings that make the shared root cause obvious:

```
┌─────────────────────────────────────────────────────────────────┐
│  Clustered Issues                                  2 clusters  │
│                                                                │
│  ┌─ Token validation failures ─────────────────────────────┐   │
│  │                                                         │   │
│  │  ● AUTH-3f2a  ● AUTH-7b1c  ● AUTH-9d4e                 │   │
│  │                                                         │   │
│  │  Root cause: Missing null check in validateToken()      │   │
│  │  Strategy: Fix the shared validation path, all three    │   │
│  │  issues resolve with a single change                    │   │
│  └─────────────────────────────────────────────────────────┘   │
│                                                                │
└─────────────────────────────────────────────────────────────────┘
```

#### 2e. Skipped Issues with Clear Reasoning

Make the "skipped" section feel intentional rather than like a reject pile:

```
  Intentionally skipped                                3 issues

  ┌─ ISSUE-a1b2 ──────────────────────── already in flight ─┐
  │  "Agent run #47 is already working on this exact issue." │
  └──────────────────────────────────────────────────────────┘

  ┌─ ISSUE-c3d4 ────────────────────── in avoid area ───────┐
  │  "This touches the payments module which is in your      │
  │   avoid areas. Leaving for manual review."               │
  └──────────────────────────────────────────────────────────┘
```

Renaming from "Skipped Issues" to **"Intentionally skipped"** reinforces that the PM made a deliberate decision.

---

### 3. PM Context Page (replaces /prioritization)

The current prioritization page is a pure settings form. Restructure it to feel like you're briefing an intelligent agent, not filling out a form.

#### 3a. Conversational Framing

Instead of label/textarea pairs, frame each section as a briefing:

```
┌─────────────────────────────────────────────────────────────────┐
│  Brief Your PM Agent                                           │
│                                                                │
│  The PM agent reads this context before every analysis.        │
│  It uses your philosophy to make tradeoff decisions,           │
│  your direction to align priorities, and your focus/avoid      │
│  areas to filter what it works on.                             │
└─────────────────────────────────────────────────────────────────┘
```

#### 3b. Show How Context Gets Used

After each textarea, show a concrete example of how the PM uses that input:

```
  Philosophy
  ┌──────────────────────────────────────────────────────────┐
  │  "We prefer small, safe fixes over ambitious refactors.  │
  │   Always prioritize customer-facing bugs over internal   │
  │   tooling issues."                                       │
  └──────────────────────────────────────────────────────────┘

  ↳ This shapes decisions like: "Skipped ISSUE-x because it would
    require a large refactor, and your philosophy prefers small,
    safe fixes."
```

The "how it's used" hint can be static example text -- it just needs to exist to close the loop between "I typed something" and "the PM actually reads this."

#### 3c. Priority Weights with Previews

The sliders are good, but add a preview of what the current weights would produce:

```
  Priority Weights                              Sum: 1.00

  Customer Impact  ████████████████░░░░  0.35
  Severity         ██████████░░░░░░░░░░  0.25
  Recency          ████████░░░░░░░░░░░░  0.20
  Revenue Risk     ████████░░░░░░░░░░░░  0.20

  Preview: With these weights, a critical-severity issue affecting
  100 customers would score 0.87, while a low-severity issue seen
  once would score 0.12.
```

---

### 4. Decision Log (new page)

The backend already stores a `pm_decision_log` with outcomes. Expose this as a dedicated page that shows the PM's institutional memory.

```
  Decision History                                    Last 30 days

  ┌────────┬───────────┬────────────┬──────────────────────────┐
  │ Date   │ Issue     │ Decision   │ Outcome                  │
  ├────────┼───────────┼────────────┼──────────────────────────┤
  │ Mar 5  │ AUTH-3f2a │ Delegated  │ ✓ Succeeded (PR merged)  │
  │ Mar 5  │ PAY-7b1c  │ Skipped    │ — Still open             │
  │ Mar 4  │ UI-9d4e   │ Delegated  │ ✗ Failed (test failures) │
  │ Mar 3  │ API-2e5f  │ Clustered  │ ✓ Succeeded              │
  └────────┴───────────┴────────────┴──────────────────────────┘

  Success rate: 73% (11/15 delegated tasks succeeded)
```

This page builds trust by showing that the PM is learning and improving. It also surfaces the success rate, which is the strongest proof that the PM is actually valuable.

**Implementation**: Add a `GET /api/pm/decisions` endpoint that returns paginated decision log entries. Create a simple table view on the frontend. The `pm_decision_log` table already has all the data.

---

### 5. Sidebar Polish: Live PM Status Indicator

Add a small status indicator next to the PM Agent nav item that shows whether the PM is idle, running, or has a recent plan:

```
  PM Agent  ● (green dot = recent plan, pulsing blue = running)
```

This makes the PM feel alive even when you're on other pages. Implementation: poll the latest plan status on an interval and show a dot indicator.

---

## Summary of Changes

| Area | Current | Proposed |
|------|---------|----------|
| Navigation | Hidden in user dropdown | Top-level sidebar section |
| Plans page | Flat card output | Rich dashboard with status hero, context stats, evidence trails |
| Prioritization page | Generic settings form | "Brief your PM" with usage examples |
| Decision history | Not exposed | New dedicated table page |
| Live status | Small blue banner | Hero banner with activity feed + sidebar dot |
| Task cards | Plain text reasoning | Evidence trails with file references, inline run status |
| Skipped issues | "Skipped Issues" header | "Intentionally skipped" with deliberate framing |
| Clusters | Flat badge list | Visual groupings with shared root cause emphasis |

## Implementation Order

1. **Nav promotion** -- Move PM pages to top-level sidebar (low effort, high impact)
2. **Context stats on plan response** -- Backend: add counts to PM plan API response
3. **Dashboard hero + context cards** -- Frontend: build the status banner and stat cards
4. **Task card enhancements** -- Frontend: file extraction, inline run status
5. **Decision log page** -- Backend endpoint + frontend table (medium effort, high trust-building)
6. **Context page reframe** -- Frontend: conversational framing and usage hints
7. **Live status indicator** -- Frontend: sidebar dot + hero activity feed

## Backend Changes Required

1. **Extend PM plan API response** with context counts (issues reviewed, runs checked, decisions reviewed, commits analyzed, PRs checked)
2. **Add `GET /api/pm/decisions`** endpoint for decision log with pagination
3. **Optional**: Add a PM status/progress endpoint that reports current phase during analysis (requires adding phase tracking to the analyze flow)

## Non-Goals

- Changing the PM agent's actual intelligence or prompt -- this is purely a presentation layer change
- Adding new PM features or capabilities -- surface what already exists
- Redesigning the full app -- focused on PM-related pages only
