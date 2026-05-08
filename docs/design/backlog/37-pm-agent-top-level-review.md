# 35 - PM Agent: Operator Workspace

> **Status:** Backlog | **Last reviewed:** 2026-05-06
>
> **Implementation notes:** Decision log model/API and PM handler exist. Missing: split-view workspace, autonomy model, PM as first-class nav item. Plans still exposed as concept.

> Promote the PM agent to a first-class operator workspace alongside Sessions and Projects. Define how it differs from agents.md and design it as the place operators live.

## Decision Summary

**Recommendation**: Promote the PM Agent to a top-level nav item, designed as an operator workspace for a PM/engineer hybrid. Use a **split-view layout** (intelligence above, configuration below) with no user-facing "Plan" concept. The PM's output is expressed as actions on **Sessions** and **Projects** — users never see or manage "plans."

The goal is not "make the PM visible" in the abstract. The goal is to give one cross-functional owner a single place to:

1. understand what the system thinks should happen next
2. inspect why the PM made those choices
3. tune direction and constraints when the PM is off
4. build trust over time through visible outcomes and learning

This page should earn its place in the nav by supporting a repeated operating loop, not by housing PM-related configuration.

**Additional recommendations**:

- Make autonomy a first-class part of the PM model. New orgs should start in suggestion mode, but the path into higher-autonomy operation should be explicit, legible, and easy to adopt as trust grows.
- **Deprecate "Plan" as a user-facing concept.** The `pm_plans` table remains as internal execution infrastructure, but the UI never exposes plans as a named entity. The PM "analyzes and acts" — results flow into Sessions (reactive work) and Projects (strategic work). The decision log replaces the plan list as the audit trail. See Section 5a for details.

---

## 1. Primary User and Jobs To Be Done

### Primary user

The primary user is a **PM / engineer hybrid**: the modern product-minded technical operator who can move across prioritization, debugging, execution planning, and system tuning.

This user may look like:

- a product engineer who owns a business area end-to-end
- a tech lead with strong product judgment
- a founder/operator in an early-stage team
- an engineering manager who still works directly in the product and backlog

This is not a pure PM screen and not a pure engineering admin screen. It is designed for the person accountable for both **what gets worked on** and **whether the reasoning behind that work is sound**.

### Core jobs to be done

When this user opens the PM Agent page, they are usually trying to do one of four things:

1. **Review the current recommendation**: "What does the system think we should do next?"
2. **Audit the reasoning**: "Why did it pick these issues and skip others?"
3. **Correct the steering**: "What product context, constraints, or repo-specific guidance should I change?"
4. **Evaluate trust**: "Is the PM improving over time, or do I need to narrow its scope?"

They are also often making a fifth decision over time:

5. **Increase autonomy safely**: "Has the PM earned the right to do more on my behalf?"

### Product requirement implied by this persona

The PM Agent page should optimize for **fast strategic comprehension**. A user should be able to land on the page and, within a minute, answer:

- What is the PM recommending now?
- Why is it recommending that?
- What changed since the last analysis?
- What, if anything, should I change?
- Is the PM ready for more autonomy?

---

## 2. Current State Assessment

### Where PM lives today

The PM agent is fragmented across **four locations** with no clear home:

| Surface | What it shows | Discoverability |
|---------|--------------|-----------------|
| `/prioritization` (settings dropdown) | PM config: schedule, model, product context, docs, weights | Buried 2 clicks deep |
| `/plans` | PM plan output: analysis, tasks, clusters, skips | **No nav link at all** — orphaned page (to be removed) |
| Sessions list status banner | PM running/completed indicator | Visible but confusing (PM dot on Sessions) |
| Session detail | Tasks, clusters, skipped — the PM's decisions | Shown but not attributed to the PM |

The PM agent does more strategic work than anything else in the system, but it's the least visible concept in the UI.

### What the PM agent actually does (the full picture)

The PM agent is not a settings page. It's a **continuously learning strategic planner** that:

1. **Reads** the codebase (AGENTS.md, README, git history, stack traces at specific file:line locations)
2. **Gathers** open issues, past decisions, recent PRs, in-flight runs, active projects, Slack threads, and reference documents — with adaptive limits that scale by org size (small orgs: 30 issues/20 decisions, medium: 75/50, large: 150/75; see `internal/services/pm/constants.go`)
3. **Reasons** about priority using product context (philosophy, direction, focus/avoid areas) and configurable weights (customer impact, severity, recency, revenue risk)
4. **Decides** what to work on, what to skip, and why — with explicit reasoning for each decision
5. **Clusters** related issues that share root causes
6. **Allocates** capacity between reactive work and project work
7. **Delegates** to coding agents with specific approach guidance
8. **Learns** from outcomes — reads its own decision history to see what succeeded and failed
9. **Plans** project cycles iteratively, building on lessons learned and approach history
10. **Suggests** new projects when it identifies clusters of related issues
11. **Recommends** Linear issue management actions (re-prioritize, re-label, close)

This is a fundamentally different cognitive function from what coding agents do or what agents.md provides.

---

## 3. PM Agent vs. agents.md — The Core Distinction

### They serve different cognitive functions

| Dimension | agents.md | PM Agent |
|-----------|-----------|----------|
| **Analogy** | Muscle memory | Executive function |
| **Question it answers** | "How do we do things here?" | "What should we do next, and why?" |
| **Knowledge type** | Code conventions, repo structure, test patterns, architecture | Product philosophy, quarterly direction, customer impact, revenue risk, decision history |
| **Temporal scope** | Stateless — read once per agent run | Stateful — accumulates institutional memory across cycles |
| **Decision scope** | Within a single issue | Across the entire issue portfolio + active projects |
| **Input surface** | The repository | Issues + PRs + Slack + reference docs + decision log + project state + codebase |
| **Update cadence** | Manual, by developers | Continuous — the PM enriches its own context every cycle |
| **Who benefits** | The coding agent (executes better) | The engineering/product team (works on the right things) |
| **Learning loop** | None | Decision outcomes feed back into future planning |

### Why you can't replicate the PM by investing in agents.md

agents.md could theoretically include product context. But it can't:

1. **Maintain state across runs** — agents.md is a static file. The PM tracks what it decided last time, what succeeded, what failed, and adjusts. This is the `pm_decision_log` with outcome tracking.

2. **Reason across the full issue portfolio** — agents.md gives context to one agent working on one issue. The PM sees up to 150 issues at once (adaptive by org size) and makes cross-cutting decisions (clustering, prioritization, skip reasoning).

3. **Learn from its own mistakes** — the PM reads `PreviousDecisions` (up to 75 decisions with outcomes, adaptive by org size) and adjusts. A coding agent reading agents.md has no feedback loop.

4. **Manage capacity** — the PM does slot allocation (`SlotAllocation.Reactive` vs `SlotAllocation.Projects`) and decides what fits in available capacity. agents.md has no concept of resource constraints.

5. **Evolve project strategy** — the PM's `ProjectCycle` model tracks cycle-by-cycle analysis, lessons learned, and approach history. It adjusts strategy based on what worked. agents.md can't iterate on a multi-week goal.

**The one-line distinction**: agents.md is institutional knowledge (static). The PM agent is institutional intelligence (dynamic, learning, strategic).

---

## 4. The Promotion Question: Should PM Be Top-Level?

### Arguments for promoting

1. **It's already the most important thing in the system.** The PM decides what every coding agent works on. It's the orchestrator above Sessions and Projects. Hiding it in settings undersells the entire product.

2. **It's the key differentiator.** Every AI coding tool has "agents that write code." Almost none have a strategic PM layer that learns, prioritizes, and coordinates. This is the wedge. Burying it communicates it's a setting, not a feature.

3. **The current split is confusing.** Config in `/prioritization`, output in `/plans`, status on the Sessions nav item. Users can't build a mental model of "the PM" as a coherent concept when it's scattered.

4. **Projects already got promoted.** Design doc 30 explicitly said "No new nav items" and "Enhance, don't add." But Projects got its own top-level nav item anyway because it deserved it. The PM has a stronger case — it's what makes Projects smart.

5. **Company-specific context deserves visibility.** Product philosophy, direction, focus areas, reference documents, and priority weights are the soul of the PM. They shouldn't be buried in a settings dropdown.

### Arguments against promoting (and why they don't hold)

1. ~~**Cruft risk.** Four nav items starts to feel heavy.~~ **Resolved.** The PM *replaces* Overview instead of adding a fourth item. We go from 3 (Overview, Sessions, Projects) to 3 (PM Agent, Sessions, Projects). Net-zero nav items.

2. **The PM is infrastructure, not a destination.** Users don't "visit" the PM — they see its effects in Sessions and Projects. Making it a page might create a place nobody goes. **Counter:** The split-view design makes the PM page the place where you *steer* the system, not just observe it. Configuration, autonomy, and documents live here. If users don't visit, the PM runs on stale context — visiting is how they keep the system aligned.

3. **Settings vs. entity confusion.** Unlike Sessions and Projects (which are lists of things), the PM is a mix of config and output. It doesn't fit the "list → detail" pattern. **Counter:** This is the point. The PM is a *workspace*, not a list. The split-view makes this explicit — top half is intelligence (output), bottom half is direction (input). Different from Sessions/Projects, but intentionally so.

4. **Design doc 30's original instinct was right.** "Enhance, don't add" is a good principle. **Counter:** We're not adding — we're *replacing* Overview with something that earns the slot. The PM's value IS best shown through its effects on Sessions and Projects, but the PM page is where you understand and steer those effects.

### Decision: PM Agent as the **landing page** and operator workspace

The PM should not just be top-level — it should be the **first nav item and the default landing page**, replacing the current Overview. The PM is not a feature of 143; the PM *is* 143. Making it the entry point tells the product story from the moment a user signs up.

The current Overview page is a setup wizard that becomes dead weight after onboarding. Rather than maintaining a page that outlives its usefulness, the PM Agent page absorbs the setup flow as its empty state. The setup wizard is reframed as "help the PM get started" — every integration step serves the PM's intelligence, not generic admin.

This means:

- **Pre-onboarding**: The landing page shows the setup wizard, framed as giving the PM what it needs to work ("I need repo access to read your code", "More signal = better prioritization")
- **Post-onboarding, pre-first-analysis**: The page shows the "Your Direction" configuration zone with nudges to set philosophy, direction, and focus areas, plus a prominent "Analyze" button
- **Post-first-analysis**: The full split-view workspace with current recommendation, decisions, performance, activity, and configuration

The page grows *in place* as the user gives it more context. There is no transition between "setup" and "product." The product starts empty and becomes intelligent.

### Naming the nav item

The nav label is one of the most visible design decisions in the product. It appears on every page, it's the first word in the sidebar, and it frames how users think about what 143 is. The name should be short (1-2 words), convey intelligence/agency, and feel native to modern AI-forward tools.

#### Candidates

| Name | Feel | Why it works | Why it might not |
|------|------|-------------|-----------------|
| **Autopilot** | Confident, autonomous | Evokes autonomous operation. Tesla, Vercel use this pattern. Implies intelligence + delegation. Immediately communicates "this thing drives itself." | Could imply the user isn't in control. Slightly overused in tech. |
| **Command** | Authoritative, operator-native | Mission control framing. Short, active verb-energy. Implies the user is in command — the PM is their instrument. | Could feel too militaristic. Doesn't convey learning/intelligence. |
| **Bridge** | Narrative, distinctive | Star Trek metaphor — the captain's seat. You see everything, you give orders. Unusual in product nav, which makes it memorable. | Requires explanation. Could feel pretentious. |
| **Helm** | Nautical, directional | Where you steer the ship. Short. Implies direction-setting, which is exactly what the PM page is for. Uncommon in tech = distinctive. | Unfamiliar metaphor for some users. |
| **Ops** | Technical, clean | DevOps-native teams understand instantly. Feels like a real workspace, not a dashboard. Short. | Too generic — could mean anything operational. Doesn't communicate AI. |
| **Intel** | Sharp, specific | Short for "intelligence." The PM's core function is institutional intelligence. Stands out in a sidebar. | Could feel like surveillance. Military connotation. |
| **PM** | Honest, minimal | Everyone knows what a PM does. Two characters. Zero learning curve. Matches the internal concept exactly. | Doesn't convey that it's AI-powered. Could be confused with "project management." |
| **Strategy** | Clear, descriptive | What the PM actually does. No metaphor needed. Self-documenting. | Passive — doesn't convey a living system. Feels like a static page. |
| **Pulse** | Alive, continuous | Implies a living system with a heartbeat. The PM is continuously analyzing, learning, acting. The page IS the product's pulse. Linear uses this for their AI-summarized feed of what's happening. | Could mean monitoring/health rather than intelligence. Linear already owns this name in the dev tool space. |
| **Signal** | Sharp, focused | The PM cuts through noise to surface what matters. Clean, modern, one word. Pairs naturally with prioritization work — the PM finds the signal in your backlog. | Could be confused with the messaging app. Doesn't convey agency — signals are passive. |
| **Mission Control** | Authoritative, aspirational | NASA-style command. Positions the page as where you oversee everything and launch work. Has an aspirational, slightly playful tone that fits dev tools. | Two words is heavier in a sidebar. Could feel grandiose for small teams. |

#### Recommendation: **Autopilot**

**Autopilot** is the strongest choice for three reasons:

1. **It communicates the product's promise in one word.** When a new user sees "Autopilot" as the first nav item, they immediately understand: this thing runs itself. That's the entire pitch of 143 — an AI engineering team that works autonomously.

2. **It frames the autonomy slider naturally.** The PM page has a 3-level autonomy model (Suggest → Act → Operate). "Autopilot" makes the metaphor intuitive: you can have autopilot in advisory mode, partial autopilot, or full autopilot. The language scales with the feature.

3. **It differentiates from the word "PM."** Internally, the system is a PM agent. But "PM" as a nav label carries baggage — users might think project management, not AI intelligence. "Autopilot" sidesteps this entirely and positions 143 as forward-looking.

```
┌──────────────────────────────┐
│ ▎Autopilot                   │  ← Landing page. The product's brain.
│  Sessions                    │  ← The work being done.
│  Projects                    │  ← The long-term goals.
└──────────────────────────────┘
```

**Runner-up: Helm.** If "Autopilot" feels too autonomous for early trust-building, "Helm" puts the user in the driver's seat while still implying intelligence and direction. The sidebar reads: Helm → Sessions → Projects. "You're at the helm."

**Also strong: Signal.** If the emphasis is on the PM's prioritization intelligence — cutting through noise, surfacing what matters — "Signal" is clean and sharp. It's more about *awareness* than *agency*, which may be a better fit if the PM starts in Suggest mode.

**Decision**: Open — pick based on brand voice and positioning. Autopilot leans into AI-forward confidence. Helm leans into user control. Signal leans into intelligence.

The bar for this decision:

- users visit it repeatedly, not just during setup
- users can take a clear action from it
- it improves trust and control, rather than just exposing more internals
- it gives users a clear path from review-heavy usage into autonomous operation

The PM isn't a list view like Sessions or Projects. It's an **operator workspace** — the place where the person steering 143 spends their time. Unlike a settings page (configure and leave), a workspace is a destination you return to repeatedly to observe, steer, and learn.

An operator workspace is:
- **Observable**: see what the PM did, is doing, and will do next
- **Steerable**: adjust product context, weights, schedule, documents — and see the effects immediately
- **Accountable**: track the PM's success rate, review its decisions, build trust over time
- **Adaptive**: context gathering scales automatically with org size (no manual tuning)

```
┌──────────────────────────────┐
│ ▎Autopilot                   │  ← Landing page. Setup → workspace.
│  Sessions  ●                 │
│  Projects                    │
└──────────────────────────────┘
```

Three nav items. Autopilot first. The hierarchy tells the product story: intelligence → execution → strategy. The name "Autopilot" immediately communicates the product promise — this thing drives itself. (See "Naming the nav item" above for alternatives.)

Remove "Overview" as a nav concept. Remove "Prioritization" from the dropdown. Remove the orphaned `/plans` route. The Autopilot page is the single home for setup, intelligence, and steering.

---

## 5. Operator Workspace Structure

### Design principle

Do not make the PM page a dumping ground for all PM-related data. It should be organized around the user's operating loop: **observe → steer → verify**.

1. See what the PM recommends now (observe)
2. Inspect the reasoning and tradeoffs (observe)
3. Adjust context or constraints if needed (steer)
4. Verify whether the PM is getting better (verify)

### 5a. Deprecating "Plan" as a user-facing concept

The previous design used "Plans" as a first-class entity — the PM produced a "plan" that users could view, compare, and browse in history. This introduces an unnecessary concept between the PM's intelligence and its effects.

**The user's mental model should be:**

- The PM **analyzes** issues and projects
- The PM **recommends** what to work on next (expressed as Sessions and Project tasks)
- The PM **decides** what to skip and why
- The PM **learns** from outcomes over time

Users think in terms of Sessions, Projects, and the PM's reasoning — not "Plan #14."

**Backend approach:**

The `pm_plans` table stays as **internal execution infrastructure**. It records what the PM analyzed, what it decided, and what it executed — but this is never exposed as a named entity in the UI. Specifically:

- Keep `pm_plans` table and `PMPlanID` foreign keys on Sessions and ProjectCycles for backend traceability
- Keep the `pm_analyze` and `project_cycle` job types unchanged
- Keep `planToModel()` and `planToDecisionLog()` conversion functions
- **Remove** the `/api/v1/pm/plans`, `/api/v1/pm/plans/{id}`, and `/api/v1/pm/plans/latest` API endpoints from the frontend (keep for internal/debug use if needed)
- **Remove** the `/plans` frontend page and plan history UI
- **Rename** user-facing language: "plan" → "analysis cycle" or "recommendation" throughout the UI
- **Promote** the decision log (`/api/v1/pm/decisions`) as the primary audit trail
- **Add** a new `/api/v1/pm/current` endpoint that returns the PM's latest recommendation in a presentation-friendly format (current tasks, project actions, skipped issues, reasoning) without exposing the raw plan structure

**What this means for the entity model:**

```
Before (3 user concepts):
  Session ← Plan → Project
  User manages: Sessions, Plans, Projects

After (2 user concepts):
  Session ← [internal pm_plan] → Project
  User manages: Sessions, Projects
  User observes: PM recommendations, PM decisions, PM performance
```

The PM becomes a **verb** (it analyzes, recommends, acts) rather than a **noun** (it produces plans).

### Recommended page model: Split-view workspace

Use a **split-view layout** — intelligence above, configuration below — on a single scrollable page. No tabs. A clear visual divider separates "what the PM decided" (AI output) from "your direction" (human input).

This is the clearest expression of the human/AI boundary. Users always know: top = the PM's intelligence, bottom = my controls.

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  143.dev                                                                    │
├────────────┬─────────────────────────────────────────────────────────────────┤
│            │                                                                │
│ ▎Autopilot │  Autopilot                                        [✦ Analyze] │
│            │  ────────────────────────────────────────────────────────────  │
│  Sessions  │  Act on low-risk · 84% success · Last analyzed 2h ago          │
│            │                                                                │
│  Projects  │  ┌─ Current Recommendation ─────────────────────────────────┐ │
│            │  │                                                          │ │
│            │  │  "Payment reliability cluster: 3 issues share auth      │ │
│            │  │   middleware root cause. Aligns with Q1 hardening."      │ │
│            │  │                                                          │ │
│            │  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐   │ │
│            │  │  │ PAY-3a2d │ │ AUTH-142 │ │ AUTH-156 │ │ +1 more  │   │ │
│            │  │  │ ✓ Done   │ │ ● Active │ │ ○ Queued │ │          │   │ │
│            │  │  └──────────┘ └──────────┘ └──────────┘ └──────────┘   │ │
│            │  │                                                          │ │
│            │  │  Projects this cycle:                                    │ │
│            │  │  Payments Hardening → 2 new tasks · Auth Overhaul → 1   │ │
│            │  │                                                          │ │
│            │  │  Skipped (4)                                         ▾  │ │
│            │  └──────────────────────────────────────────────────────────┘ │
│            │                                                                │
│            │  ┌─ Decisions ──────────────┐  ┌─ Performance ──────────────┐ │
│            │  │                          │  │                            │ │
│            │  │  ✓ PAY-3a2d  Succeeded   │  │  84%  ████████████░░      │ │
│            │  │  ✗ AUTH-99   Failed      │  │                            │ │
│            │  │  ✓ DB-12     Succeeded   │  │  Auth    92%              │ │
│            │  │  ○ UI-9d4e   Skipped     │  │  Payment 67%              │ │
│            │  │                          │  │  Infra   88%              │ │
│            │  │  [View all →]            │  │                            │ │
│            │  └──────────────────────────┘  └────────────────────────────┘ │
│            │                                                                │
│            │  ┌─ Recent Activity ────────────────────────────────────────┐ │
│            │  │  Today: Analyzed 14 issues · 3 delegated · 4 skipped     │ │
│            │  │  Yesterday: 4/4 sessions succeeded ✓                     │ │
│            │  │  Mar 17: AUTH-99 failed — PM now requires high confidence │ │
│            │  │  [View all decisions →]                                   │ │
│            │  └──────────────────────────────────────────────────────────┘ │
│            │                                                                │
│            │  ═══════════════════════════════════════════════════════════   │
│            │  Your Direction                                                │
│            │  ═══════════════════════════════════════════════════════════   │
│            │                                                                │
│            │  ┌─ Product Context ─────────────────────────────────────────┐│
│            │  │                                                          ││
│            │  │  Philosophy                    Direction                  ││
│            │  │  "Ship reliability first"      "Payments hardening"      ││
│            │  │  [edit]                        [edit]                     ││
│            │  │                                                          ││
│            │  │  Focus areas                   Avoid areas               ││
│            │  │  slo, incident prevention      new-ui                    ││
│            │  │  [edit]                        [edit]                     ││
│            │  │                                                          ││
│            │  │  Priority weights                                        ││
│            │  │  Impact 0.35 · Severity 0.25 · Recency 0.20 · ...      ││
│            │  │  [edit]                                                   ││
│            │  └──────────────────────────────────────────────────────────┘│
│            │                                                                │
│            │  ┌─ Autonomy ────────────────┐  ┌─ Documents ───────────────┐│
│            │  │                            │  │                            ││
│            │  │  ○─────●─────○            │  │  roadmap.md    Read 2d ago ││
│            │  │  Suggest  Act  Operate    │  │  arch-guide    Read 5d ago ││
│            │  │           ▲               │  │                            ││
│            │  │                            │  │  [+ Add document]         ││
│            │  │  PM has 87% acceptance     │  │                            ││
│            │  │  over 9 reviewed cycles.   │  │  Context health:          ││
│            │  │  Ready to advance? →       │  │  ✓ Philosophy   Active    ││
│            │  │                            │  │  ⚠ Direction    45d ago   ││
│            │  └────────────────────────────┘  │  ✓ Focus areas  3 set    ││
│            │                                  │  ○ Docs         Add more  ││
│            │                                  └────────────────────────────┘│
│            │                                                                │
└────────────┴────────────────────────────────────────────────────────────────┘
```

### Why split-view, not tabs

**The human/AI boundary is the single most important design decision.** The PM is the first surface where AI reasoning and human steering coexist on the same page. The visual divider ("Your Direction") explicitly separates "what the PM decided" from "what you told it." This is the foundation of trust.

Previous designs used tabs (Plan / Decisions / Context / Documents). This had three problems:

1. **Tabs hide information.** The operator's core loop (observe → steer → verify) requires information from what would be multiple tabs simultaneously. A user checking whether the PM's recommendation aligns with their direction needs to see both the recommendation AND the direction at the same time. Tabs force mental context-switching.

2. **Tabs create a false equivalence.** The "Plan" tab and "Context" tab serve fundamentally different purposes — one is AI output, one is human input. Putting them in the same tab bar implies they're peer concepts. The split-view makes the hierarchy explicit: the PM's intelligence is the primary content; your configuration is the supporting structure beneath it.

3. **Configuration accessibility matters.** The PM/engineer hybrid persona's core loop is observe → steer → re-run. If steering requires clicking to a different tab, there's friction in the most important loop. The split-view puts context, weights, autonomy, and documents right on the page. The PM page isn't just for reading — it's for operating.

### Page zones explained

The page has five distinct zones, read top to bottom:

#### Zone 1: Control strip (always visible)

```
PM Agent                                                     [✦ Analyze]
────────────────────────────────────────────────────────────────────────
Act on low-risk · 84% success · Last analyzed 2h ago
```

Shows: current autonomy level, headline success rate, last analysis time, next scheduled run. Primary action: Analyze Now. This orients the operator instantly on every visit.

#### Zone 2: Current Recommendation (hero)

The PM's latest recommendation, expressed as **actions on issues and projects** — not as a "plan." The PM speaks in first person ("Focus on payment reliability cluster...") to make it feel like an intelligent collaborator.

Contents:
- One-sentence situational analysis with reasoning
- Issue cards showing what the PM delegated, with status (done/active/queued)
- Project actions this cycle (tasks created per active project, with progress)
- Collapsed "Skipped" section with skip reasoning (progressive disclosure)
- Capacity indicator (slots used / available)

This zone answers: "What does the PM think should happen next, and why?"

#### Zone 3: Decisions + Performance (side by side)

Two cards sitting next to each other:

**Decisions card** — Recent decision log entries with outcomes (succeeded/failed/skipped/still open). Links to resulting sessions. Filterable on full view. This is the primary audit trail, replacing the old plan history.

**Performance card** — 30-day success rate with per-domain breakdown (e.g., "auth: 92%, payment: 67%"). This is the trust signal — the operator glances here to decide whether to increase autonomy or add constraints.

These two cards together answer: "Is the PM getting smarter?"

#### Zone 4: Recent Activity (compact timeline)

A small, collapsed section showing the last 3-5 PM actions as a compact narrative:

```
Today: Analyzed 14 issues · 3 delegated · 4 skipped
Yesterday: 4/4 sessions succeeded ✓
Mar 17: AUTH-99 failed — PM now requires high confidence
```

This gives the page a sense that the PM is **alive and continuously working**, without requiring a full activity feed. Expandable to full decision history via "View all decisions →".

This zone answers: "What has the PM been doing?"

#### Zone 5: Your Direction (below the divider)

Everything below the `═══ Your Direction ═══` divider is human-authored steering. This zone contains:

**Product Context** — Philosophy, direction, focus/avoid areas, priority weights. Inline `[edit]` buttons for immediate adjustment. Shows how context influenced recent decisions (context health).

**Autonomy** — Single slider (Suggest / Act on low-risk / Operate broadly) with capability mapping. Shows readiness signals ("PM has 87% acceptance over 9 reviewed cycles. Ready to advance?").

**Documents** — Reference documents with "last read by PM" timestamps. Add/edit/delete. Document influence indicators.

**Context Health** — Inline indicators showing freshness and influence of each setting. Nudges like "Direction last updated 45d ago — consider refreshing."

This zone answers: "Does the PM have the right context and the right amount of autonomy?"

### The operator loop (no tabs needed)

The workspace supports the full operating loop on a single scrollable page:

```
1. OBSERVE  (Zone 2: Recommendation)  → What did the PM just decide?
2. VERIFY   (Zone 3: Decisions/Perf)  → Is the PM succeeding? Where is it struggling?
3. REVIEW   (Zone 4: Recent Activity) → What has the PM been doing over time?
4. STEER    (Zone 5: Your Direction)  → Adjust direction, weights, focus areas, docs
5. TRIGGER  (Zone 1: Control strip)   → Run analysis with updated context
→ Back to OBSERVE
```

This loop works without any navigation. The user scrolls down to steer, scrolls up to observe. The divider makes the boundary between AI output and human input unmistakable.

### Why this works for the empty state

The PM Agent page is the landing page for all users — including brand new ones. It must handle three states gracefully, each growing into the next without a page transition:

#### State 1: Pre-onboarding (no integrations connected)

The setup wizard lives *inside* the landing page, framed as helping the PM get started. Every integration step is presented from the PM's perspective. The layout uses the same sidebar and page structure as the populated workspace — the user is already "in the product," not in a separate onboarding flow.

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  143.dev                                                                    │
├────────────┬─────────────────────────────────────────────────────────────────┤
│            │                                                                │
│ ▎Autopilot │  Autopilot                                                     │
│            │  ────────────────────────────────────────────────────────────  │
│  Sessions  │  0 of 2 connected                                              │
│            │                                                                │
│  Projects  │  ┌──────────────────────────────────────────────────────────┐  │
│            │  │                                                          │  │
│            │  │  I'm your AI project manager. I analyze your issues,     │  │
│            │  │  learn what works, and decide what your coding agents     │  │
│            │  │  should build next.                                       │  │
│            │  │                                                          │  │
│            │  │  I need a few things to get started.                      │  │
│            │  │                                                          │  │
│            │  └──────────────────────────────────────────────────────────┘  │
│            │                                                                │
│            │  ┌─ 1 ─────────────────────────────────────────────────────┐  │
│            │  │  Connect a coding agent                                  │  │
│            │  │  I'll delegate work to this agent on your behalf.        │  │
│            │  │                                                          │  │
│            │  │  ┌────────────┐ ┌────────────┐ ┌────────────┐          │  │
│            │  │  │   Codex    │ │ Claude Code│ │ Gemini CLI │          │  │
│            │  │  │ recommended│ │            │ │            │          │  │
│            │  │  └────────────┘ └────────────┘ └────────────┘          │  │
│            │  └──────────────────────────────────────────────────────────┘  │
│            │                                                                │
│            │  ┌─ 2 ─────────────────────────────────────────────────────┐  │
│            │  │  Connect GitHub                              Required   │  │
│            │  │  I need repo access to read your code and open PRs.     │  │
│            │  │                                          [Connect →]    │  │
│            │  └──────────────────────────────────────────────────────────┘  │
│            │                                                                │
│            │  ┌─ 3 ─────────────────────────────────────────────────────┐  │
│            │  │  Connect issue sources                       Optional   │  │
│            │  │  More signal = better recommendations.                   │  │
│            │  │                                                          │  │
│            │  │  ○ Sentry  "I'll catch errors before your users do"     │  │
│            │  │  ○ Linear  "I'll read your backlog and priorities"      │  │
│            │  │  ○ Slack   "I'll pick up on what your team discusses"   │  │
│            │  └──────────────────────────────────────────────────────────┘  │
│            │                                                                │
│            │  ═══════════════════════════════════════════════════════════   │
│            │  Your Direction                                                │
│            │  ═══════════════════════════════════════════════════════════   │
│            │                                                                │
│            │  ┌─ Product Context ─────────────────────────────────────────┐│
│            │  │                                                          ││
│            │  │  Once connected, tell me what matters to your team.       ││
│            │  │                                                          ││
│            │  │  Philosophy   "What's your engineering philosophy?"       ││
│            │  │               e.g. "Ship reliability first, iterate fast" ││
│            │  │               [Add →]                                     ││
│            │  │                                                          ││
│            │  │  Direction    "What's the current focus?"                 ││
│            │  │               e.g. "Payments hardening for Q1 launch"    ││
│            │  │               [Add →]                                     ││
│            │  │                                                          ││
│            │  │  Focus areas  "Which areas should I prioritize?"         ││
│            │  │               e.g. slo, incident-prevention, billing     ││
│            │  │               [Add →]                                     ││
│            │  └──────────────────────────────────────────────────────────┘│
│            │                                                                │
└────────────┴────────────────────────────────────────────────────────────────┘
```

This reframes onboarding as serving the PM's intelligence. "Connect GitHub" isn't an admin checkbox — it's "I need repo access to read your code." The user is setting up a collaborator, not configuring a tool. Each integration explains *why the PM needs it* in one sentence.

The "Your Direction" zone is visible even during setup. Users can start entering product context before the PM has ever run. The placeholder examples ("Ship reliability first, iterate fast") show what good input looks like without requiring it.

#### State 2: Post-onboarding, pre-first-analysis (connected but no PM run yet)

The setup steps collapse. The page now shows the same zone structure as the full workspace, but with empty-state content in each zone. This is critical — the user should see the *shape* of the product before data fills it in, so the first analysis feels like the page coming alive, not a different page loading.

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  143.dev                                                                    │
├────────────┬─────────────────────────────────────────────────────────────────┤
│            │                                                                │
│ ▎Autopilot │  Autopilot                                  [✦ First Analysis] │
│            │  ────────────────────────────────────────────────────────────  │
│  Sessions  │  Suggest · No data yet · ✓ GitHub · ✓ Sentry                   │
│            │                                                                │
│  Projects  │  ┌─ Current Recommendation ─────────────────────────────────┐ │
│            │  │                                                          │ │
│            │  │  I can see 23 open issues across 2 repos.                │ │
│            │  │                                                          │ │
│            │  │  Run my first analysis and I'll tell you which ones      │ │
│            │  │  matter most, which ones cluster together, and what      │ │
│            │  │  your agents should work on first.                        │ │
│            │  │                                                          │ │
│            │  │  This takes about 2 minutes.                             │ │
│            │  │                                                          │ │
│            │  │  [✦ Run First Analysis]                                  │ │
│            │  │                                                          │ │
│            │  └──────────────────────────────────────────────────────────┘ │
│            │                                                                │
│            │  ┌─ Decisions ──────────────┐  ┌─ Performance ──────────────┐ │
│            │  │                          │  │                            │ │
│            │  │  No decisions yet.        │  │  No data yet.              │ │
│            │  │                          │  │                            │ │
│            │  │  After the first          │  │  After a few cycles,      │ │
│            │  │  analysis, you'll see     │  │  you'll see success       │ │
│            │  │  what I chose and why.    │  │  rates by domain.         │ │
│            │  │                          │  │                            │ │
│            │  └──────────────────────────┘  └────────────────────────────┘ │
│            │                                                                │
│            │  ═══════════════════════════════════════════════════════════   │
│            │  Your Direction                                                │
│            │  ═══════════════════════════════════════════════════════════   │
│            │                                                                │
│            │  ┌─ Product Context ─────────────────────────────────────────┐│
│            │  │                                                          ││
│            │  │  Philosophy                    Direction                  ││
│            │  │  "Ship reliability first"      Not set yet               ││
│            │  │  [edit]                        [add →]                    ││
│            │  │                                                          ││
│            │  │  Focus areas                   Avoid areas               ││
│            │  │  slo, incident-prevention      Not set yet               ││
│            │  │  [edit]                        [add →]                    ││
│            │  │                                                          ││
│            │  │  Priority weights              Using defaults            ││
│            │  │  Impact 0.30 · Severity 0.25 · Recency 0.20 · ...      ││
│            │  │  [customize]                                             ││
│            │  └──────────────────────────────────────────────────────────┘│
│            │                                                                │
│            │  ┌─ Autonomy ────────────────┐  ┌─ Documents ───────────────┐│
│            │  │                            │  │                            ││
│            │  │  ●─────○─────○            │  │  No documents yet.         ││
│            │  │  Suggest  Act  Operate    │  │                            ││
│            │  │  ▲                        │  │  Upload a roadmap,         ││
│            │  │                            │  │  architecture doc, or      ││
│            │  │  Starting in Suggest       │  │  strategy doc to give me   ││
│            │  │  mode. I'll recommend,     │  │  more context.             ││
│            │  │  you decide.               │  │                            ││
│            │  │                            │  │  [+ Add document]         ││
│            │  └────────────────────────────┘  └────────────────────────────┘│
│            │                                                                │
└────────────┴────────────────────────────────────────────────────────────────┘
```

Key design choices for this state:

- **The zone structure matches the populated workspace exactly.** The user sees Recommendation, Decisions, Performance, Your Direction in the same positions. When the first analysis completes, content fills into these same zones — the page *comes alive* rather than changing shape.
- **Empty zones explain what they'll show, not that they're empty.** "After the first analysis, you'll see what I chose and why" is better than "No data." Each zone previews its future value.
- **The control strip already works.** It shows the connected integrations, the current autonomy level, and the primary action (First Analysis). The user can see the product is ready.
- **"Your Direction" shows what the user already configured** during State 1, plus defaults for anything they skipped. The "Not set yet" + `[add →]` pattern nudges without blocking.
- **Autonomy defaults to Suggest** with a first-person explanation: "I'll recommend, you decide." This is the PM introducing itself as a collaborator, not a setting being described.

#### State 3: Post-first-analysis (full workspace)

The full split-view workspace as designed in the layout above. The recommendation zone fills in with the PM's analysis. The decisions card populates. The activity feed shows its first entry ("Today: Analyzed 23 issues · 3 delegated · 4 skipped"). The page the user was already on *became* intelligent — no navigation required. The zone structure didn't change — only the content within each zone.

This three-state progression is the entire onboarding and product experience on a single URL. The page grows in place.

---

## 6. Trust, Control, and Developer Experience

### Source of truth and boundaries

The PM page should clearly communicate the boundary between:

- **human-authored steering**
  - product philosophy
  - direction
  - focus/avoid areas
  - reference documents
  - repo overrides
- **PM-generated reasoning**
  - recommendations and prioritization
  - clusters
  - skip decisions
  - approach guidance for delegated sessions
- **system-observed outcomes**
  - success/failure rates
  - merge outcomes
  - post-ship impact
  - decision accuracy trends

This needs to be legible in both the UI and the doc. Teams need to know what they can edit, what the PM inferred, and what the system measured.

### Approval model

The PM should be allowed to recommend broadly, but actions that materially reshape work should have explicit approval boundaries.

Suggested default model:

- PM can automatically analyze and produce recommendations
- PM can automatically create sessions only within configured autonomy and confidence constraints
- PM can suggest project creation, issue closure, or issue relabeling, but those actions should require explicit human approval unless the org opts into automation later

### Autonomy should be a product primitive, not a brittle setting

The system should have a single, understandable autonomy model that applies across PM actions, rather than a patchwork of unrelated toggles.

Users should be able to answer:

- what can the PM do at the current level?
- what becomes automatic at the next level?
- why is the PM still gated on certain actions?
- what signals suggest it is safe to move up?

### Recommended autonomy model

Represent autonomy as a simple 3-level slider with a capability map behind it.

#### Capability summary table

| Capability | Suggest | Act on low-risk work | Operate broadly |
|-----------|---------|----------------------|-----------------|
| Analyze and produce recommendations | Yes | Yes | Yes |
| Recommend sessions | Yes | Yes | Yes |
| Auto-create sessions | No | Yes, for bounded policy-compliant work | Yes, for most policy-compliant work |
| Auto-create sessions for project work | No | Yes | Yes |
| Recommend projects | Yes | Yes | Yes |
| Auto-create simple projects | No | Yes | Yes |
| Auto-create more complex projects | No | No | Yes, when within approved policy |
| Auto-label issues | No | Yes | Yes |
| Auto-dedupe issues | No | Yes | Yes |
| Auto-update issue priority | No | Yes | Yes |
| Auto-assign issues | No | Yes | Yes |
| Auto-close issues | No | No | No |
| Re-prioritize capacity across reactive and project work | Recommend only | Limited, within policy | Yes, within policy |
| Act across multiple repos or workstreams | Recommend only | Limited | Yes, within policy |
| High-risk or ambiguous actions | Recommend only | Escalate | Escalate |

Primary slider:

1. **Suggest**
   - PM creates plans, clusters, and recommendations
   - PM suggests sessions, projects, and issue actions
   - nothing operational happens automatically
2. **Act on low-risk work**
   - PM can autonomously create sessions, including for project work
   - PM can autonomously create simple projects
   - PM can automate low-risk issue actions
   - high-risk or ambiguous work still requires approval
3. **Operate broadly**
   - PM can act automatically across most approved work classes
   - PM can create sessions for both reactive and project work, including more complex but still policy-compliant work
   - PM can create projects beyond the "simple project" definition when they remain within configured scope and autonomy policy
   - PM can automate allowed issue actions broadly across repositories and workflows
   - only high-risk, low-confidence, strategically ambiguous, or explicitly restricted actions escalate

This should be one slider in the UI, but the design should acknowledge that the underlying implementation is a capability matrix with per-action controls behind it.

### Why this model

This is intentionally simpler than a multi-step ladder with weak user-facing distinctions.

- `Suggest` is meaningfully different from automation
- `Act on low-risk work` is the first real trust unlock
- `Operate broadly` is the advanced mode for teams that want the PM to run more of the workflow

The product should avoid autonomy levels that feel different internally but not meaningfully different to users.

### Easy path into autonomy

The product goal should be to make it easy to move into autonomous mode, not to trap users in manual review forever.

That implies:

- new orgs default to `Suggest`
- the UI regularly shows what higher autonomy would unlock
- the PM page highlights readiness signals like sustained success rate, stable context quality, and low override frequency
- moving up a level should be one clear action with a crisp explanation of what changes
- users should be able to step back down just as easily

### Advanced capability controls

The main UX should stay simple with a single autonomy slider. Under the hood, the system should support per-action capability flags so advanced teams can tune behavior without making the primary model harder to understand.

Examples:

- auto-create sessions
- auto-create simple projects
- auto-label issues
- auto-dedupe issues
- auto-update issue priority
- auto-assign issues

These should live in an advanced settings area, not in the main autonomy control.

### Unlock messaging

The PM page should make autonomy advancement feel earned and obvious:

```
Autonomy: Suggest

The PM has completed 9 analysis cycles with 87% accepted recommendations.
Context coverage is healthy and manual overrides are low.

Recommended next step: move to Act on low-risk work
This would let the PM auto-create sessions, create simple projects, and handle routine issue actions automatically.
```

### Guardrails

Higher autonomy should still respect:

- confidence thresholds
- action-type restrictions
- repo-specific overrides
- explicit avoid areas
- human approval for high-risk actions unless the org opts in

The slider should feel simple, but the safety model behind it should remain explicit.

### What "Operate broadly" does and does not mean

`Operate broadly` should mean the PM is trusted to run a much larger share of the execution loop without waiting for approval in routine cases. It should **not** mean unrestricted autonomy.

At this level, the PM may:

- create sessions broadly across eligible work
- create projects that are more complex than the middle tier allows
- apply allowed issue-management actions at scale
- allocate capacity across reactive and project work without waiting for per-action review
- keep work moving across multiple repos or workstreams when that falls within explicit policy

At this level, the PM still should not automatically act when work is:

- low confidence
- high severity with uncertain remediation
- in an explicit avoid area
- likely to create significant product or roadmap tradeoffs
- likely to require cross-team coordination or stakeholder alignment
- likely to require schema migrations, major architecture changes, or risky operational changes unless the org has explicitly opted into that class of autonomy
- blocked on missing context, conflicting signals, or unclear ownership

In these cases, the PM should escalate with a recommendation, rationale, and a clear description of what decision is needed from the human operator.

### Policy-first interpretation

The right mental model is:

- `Act on low-risk work` = automate bounded, routine work
- `Operate broadly` = automate most allowed work, with policy-based exceptions

That distinction is important. The top tier should expand the PM's working surface area, but it should still be constrained by explicit organizational policy rather than by vague trust alone.

### Recommended implementation boundary

Internally, `Operate broadly` should still evaluate each candidate action against a capability matrix such as:

- action type
- confidence level
- repository or project scope
- complexity tier
- strategic sensitivity
- infra / schema / migration risk
- whether the action falls into a human-required approval class

If any of those checks fail, the PM should downgrade from autonomous action to recommendation.

### Recommended UI language

The UI should avoid copy that implies unlimited delegation. Prefer language like:

- "Operate broadly within policy"
- "Most work proceeds automatically; sensitive work still escalates"
- "High-risk or ambiguous work still requires your input"

This makes the autonomy promise strong without sounding reckless.

### Low-risk actions at the middle tier

`Act on low-risk work` should include:

- auto-creating sessions, including sessions attached to project work
- auto-creating simple projects
- labeling
- deduping
- priority changes
- assignment

The key requirement is not that these actions are universally harmless. The key is that they are automatable when they fall within explicit policy, confidence, and scope constraints.

### Closure policy

To keep the model simpler and avoid one of the most trust-sensitive issue actions, the PM should **not** auto-close issues at any autonomy level in the initial design.

The PM may still recommend closure with explicit reasoning and evidence, but closure remains a human-approved action.

### Defining "simple projects"

The PM may auto-create a project at the middle tier only when the project is simple and bounded. A simple project should generally be:

- derived from a clear issue cluster or repeated pattern
- limited in scope
- high confidence
- within one repository or tightly related set of files
- not dependent on major product tradeoffs
- not dependent on schema migrations, broad architectural changes, or cross-team coordination

If the project is broader, more ambiguous, or strategically sensitive, it should remain a recommendation until a human approves it.

### Debuggability requirement

Every major PM decision should support a clear audit path:

- decision summary
- top factors that influenced the decision
- evidence used
- what alternatives were considered or skipped
- what happened after the decision

If a user disagrees with the PM, they should know whether to:

- edit context
- change weights
- add a document
- override the decision directly
- reduce PM autonomy

### Developer experience requirement

For the team building this feature, the PM surface should have stable conceptual primitives. At minimum the design should use explicit entities such as:

- `Recommendation` (the PM's current output — what to work on, what to skip, why)
- `Decision` (a single PM choice: delegate, skip, or cluster)
- `DecisionOutcome` (what happened after the decision: succeeded, failed, still open)
- `AnalysisCycle` (internal record of a PM run — not user-facing)
- `ContextSource` (where steering input comes from)
- `ContextOverride` (per-repo overrides of org-level context)

Without named primitives, implementation will drift into loosely structured JSON blobs and ad hoc UI copy.

---

## 7. Making the PM Visibly Different — Concrete Strategies

### 7a. Thread PM reasoning into Sessions

Every session spawned by the PM already has `pm_approach` and `pm_reasoning` fields. Surface these prominently on session detail:

```
┌─────────────────────────────────────────────────────────┐
│  PM reasoning                                           │
│                                                         │
│  "3 customers affected by token refresh failures.       │
│   Clusters with AUTH-142 and AUTH-156 (shared root      │
│   cause in middleware). Aligns with Q1 reliability       │
│   focus. Previous attempt on AUTH-142 succeeded —        │
│   same approach should work here."                      │
│                                                         │
│  Confidence: high · Complexity: moderate                │
│  Priority weights: customer_impact (0.35) drove rank    │
└─────────────────────────────────────────────────────────┘
```

This makes the PM's intelligence tangible on every session, even for users who never visit the PM page.

### 7b. Show what the PM chose NOT to do

The skip list is one of the PM's most valuable outputs. A human PM builds trust by showing they considered everything and made deliberate tradeoffs. In the split-view layout, skipped issues live inside the Current Recommendation zone as a collapsed section. Surface skipped issues with reasons, but keep the default view concise:

```
Deprioritized (4 issues):
  PAY-7b1c  "Duplicate of PAY-3a2d (already in-flight)"
  UI-9d4e   "In avoid area: legacy-auth"
  API-2e5f  "Needs human decision: unclear if feature or bug"
  DB-1f3g   "Too complex: requires schema migration (PM confidence: low)"
```

### 7c. Surface institutional learning

The decision log with outcomes is the PM's moat. Show it as a narrative, but anchor it to actions:

```
PM track record (last 30 days):
  45 tasks delegated → 38 succeeded (84% success rate)
  12 issues skipped → 9 still open, 3 resolved by other means
  3 clusters identified → 2 resolved as batch, 1 in progress

  Insight: Auth-related tasks have 92% success rate.
  Payment tasks have 67% — PM now requires high confidence for payment.
```

### 7d. Make product context feel alive

Instead of a static settings form, show how product context influenced recent decisions:

```
Product context health:
  ✓ Philosophy referenced in 8 of last 10 analysis cycles
  ✓ "Q1 reliability focus" matched 12 prioritized tasks
  ⚠ Direction last updated 45 days ago — consider refreshing
  ✓ Avoid area "legacy-auth" correctly filtered 3 issues
```

In the split-view layout, these indicators live in the "Your Direction" zone, adjacent to the configuration controls they describe. The user sees the health signal right next to the `[edit]` button — so the nudge and the action are co-located.

### 7e. Company-specific vs. general — visual layering

Show the context inheritance clearly:

```
Effective PM context for [repo-name]:
  ┌─ Organization defaults ────────────────────────┐
  │  Philosophy: "Prefer minimal diffs..."          │
  │  Direction: "Harden billing"                    │
  │  Weights: impact 0.35 · severity 0.25 · ...    │
  └────────────────────────────────────────────────┘
        ↓ overridden by
  ┌─ Repository: payments-api ─────────────────────┐
  │  Focus areas: ["stripe-integration", "refunds"] │
  │  Min priority threshold: 40 (org default: 20)   │
  └────────────────────────────────────────────────┘
        = Effective config for this repo's PM cycle
```

### 7f. Use progressive disclosure aggressively

The PM should feel intelligent, not verbose. Default UI patterns should be:

- one-sentence rationale summaries
- expandable evidence panels
- structured explanation fields instead of freeform paragraphs everywhere
- a small number of headline metrics tied to clear actions

The system should not require users to read long prose to understand the recommendation.

---

## 8. Day 1 and Empty-State Experience

Since the PM Agent page IS the landing page, the empty state experience is the first impression of the entire product. This is covered in detail in Section 5 under "Why this works for the empty state" — the page progresses through three states (pre-onboarding → post-onboarding → full workspace) on a single URL.

### Day 1 behavior

If an org has little or no context yet, the PM should still produce a usable first recommendation using:

- issue severity and recency
- customer impact signals available from integrations
- codebase context from the repo
- conservative defaults for autonomy

### Empty-state guidance

Since the "Your Direction" zone is visible even during onboarding (State 1), context nudges are part of the first experience:

- missing philosophy → "What's your team's engineering philosophy?"
- no focus areas → "Which areas should I prioritize?"
- no documents → "Attach roadmap, architecture, or strategy docs"
- no decision history → "I'll get better after a few completed cycles"

These are framed as the PM asking for help, not as form labels. The PM is a collaborator being onboarded, not a tool being configured.

### Initial trust posture

For new orgs, the PM should start conservative:

- stronger emphasis on review over automation
- recommendations before mutations
- visible confidence and explanation on every decision
- a default autonomy level of `Suggest`

This is especially important for the PM/engineer hybrid persona, who will often be simultaneously evaluating the product and using it to run real work.

---

## 9. Success Criteria

Promoting the PM to top-level nav should be treated as a product bet with measurable outcomes.

Suggested success metrics:

- users can identify the PM's top recommendation and rationale in under 1 minute
- meaningful repeat usage of the PM page after onboarding
- increased review and engagement with PM recommendations
- improved session acceptance or merge rates for PM-selected work
- reduced confusion about where PM settings and outputs live (everything is on one page)
- more explicit user corrections through context updates rather than ad hoc workarounds
- reduced user-facing concept count: users think in Sessions + Projects, not Sessions + Plans + Projects

If these do not improve, the PM likely does not deserve top-level prominence yet.

---

## 10. How This Makes 143 Different

### The pitch

Most AI coding tools: "Give us a ticket, we'll write code."

143 with a visible PM: "We have an AI PM that reads your product strategy, analyzes your issue backlog, learns from what worked, and decides what your agents should build next. You set the direction. The PM handles prioritization and coordination. The coding agents execute. You manage Sessions and Projects — the PM makes both smarter."

### The information hierarchy tells the story

```
Autopilot  → "Here's what we should do and why"  ← THE DIFFERENTIATOR (landing page)
Sessions   → "Here's the work being done"
Projects   → "Here's the long-term goals being pursued"
```

Three concepts. Autopilot is first — the strategic entry point. Sessions and Projects are the execution layers beneath it. The hierarchy reads: intelligence → execution → strategy. Without the PM as the entry point, 143 looks like "a dashboard for agent runs." With Autopilot as the landing page, 143 looks like "an AI engineering team with a PM."

The previous design had Overview → PM Agent → Sessions → Projects (4 items). Overview answered "here's the state of the world" — but that's exactly what Autopilot's recommendation zone already does, with intelligence behind it. Merging them removes a concept without losing any capability.

---

## 11. Adaptive Context Limits

The PM's context gathering now scales automatically by org size. This replaces the previous hardcoded magic numbers (100 issues, 50 runs, 20 PRs, etc.) with tiered limits based on total issue count.

### Tiers

| Tier | Total issues | Issues/status | Decisions | Outcomes | PRs | In-flight | Cycles |
|------|-------------|---------------|-----------|----------|-----|-----------|--------|
| Small | 0-50 | 30 | 20 | 10 | 10 | 10 | 2 |
| Medium | 51-500 | 75 | 50 | 20 | 20 | 30 | 3 |
| Large | 500+ | 150 | 75 | 30 | 30 | 50 | 5 |

### Why adaptive?

- **Small repos** (solo project, early startup): Don't need 100-issue context windows. Smaller context = faster PM cycles, lower token cost, less noise.
- **Large repos** (enterprise, mature product): Need more signal. 30 issues would miss important patterns. More past decisions = better institutional memory.
- **Token budget**: Larger context limits for large orgs are offset by shorter description/stack trace truncation (400/600 chars vs 500/800), keeping total token usage bounded.

### Implementation

- `contextLimitsForOrgSize(totalIssues int) contextLimits` in `internal/services/pm/constants.go`
- `issueStore.CountByOrg()` called once at the start of `gatherContext()` to determine tier
- All limits flow through the `contextLimits` struct — no more inline magic numbers

---

## 12. Implementation Sketch

### Phase 1: PM Agent as landing page (high clarity, moderate effort)
1. Replace "Overview" nav item with "PM Agent" (`Brain` or `Lightbulb` icon), make it the first nav item and default route
2. Create `/pm` page with three-state layout: pre-onboarding (setup wizard), post-onboarding (ready state), full workspace (split-view)
3. Move setup wizard content from current Overview into the PM page's pre-onboarding state, reframed as "help the PM get started"
4. Build "Current Recommendation" zone from existing PM analysis output (reformat internal plan data into recommendation presentation)
5. Move `/prioritization` content → "Your Direction" zone (product context, autonomy, documents)
6. Build Decisions + Performance cards from existing `pm_decision_log` data
7. Build Recent Activity section from pm_plans + decision log history
8. Remove `/plans` page and `/overview` page from frontend
9. Remove "Prioritization" from dropdown menu
10. Remove PM status dot from Sessions (no longer needed — PM page is primary)
11. Create new `/api/v1/pm/current` endpoint that returns the latest recommendation in a presentation-friendly format (no raw plan structure exposed)
12. Deprecate `/api/v1/pm/plans`, `/api/v1/pm/plans/{id}`, `/api/v1/pm/plans/latest` from frontend usage (keep for internal/debug)

### Phase 2: Thread PM intelligence (medium effort, high impact)
13. Add PM reasoning card to session detail (for PM-spawned sessions)
14. Add "Deprioritized" collapsed section to recommendation zone with skip reasoning
15. Add context stats to recommendation zone (issues reviewed, decisions learned from)
16. Add "PM spawned this session" attribution badge on session cards

### Phase 3: Show learning (higher effort, strongest differentiator)
17. Build full decision history view with success rate, trends, filtering (linked from "View all decisions →")
18. Add "context health" indicators to "Your Direction" zone showing how product context influences decisions
19. Show effective context inheritance (org → repo) on per-repo settings
20. Add autonomy readiness signals ("PM has 87% acceptance over 9 cycles — ready to advance?")
