# 35 - PM Agent: Top-Level Concept Review

> Should the PM agent be promoted to a first-class concept alongside Sessions and Projects? How does it differ from agents.md? How do we make it clearly differentiated?

**Status**: Design review (decision pending)

---

## 1. Current State Assessment

### Where PM lives today

The PM agent is fragmented across **four locations** with no clear home:

| Surface | What it shows | Discoverability |
|---------|--------------|-----------------|
| `/prioritization` (settings dropdown) | PM config: schedule, model, product context, docs, weights | Buried 2 clicks deep |
| `/plans` | PM plan output: analysis, tasks, clusters, skips | **No nav link at all** — orphaned page |
| Sessions list status banner | PM running/completed indicator | Visible but confusing (PM dot on Sessions) |
| Session detail | Tasks, clusters, skipped — the PM's decisions | Shown but not attributed to the PM |

The PM agent does more strategic work than anything else in the system, but it's the least visible concept in the UI.

### What the PM agent actually does (the full picture)

The PM agent is not a settings page. It's a **continuously learning strategic planner** that:

1. **Reads** the codebase (AGENTS.md, README, git history, stack traces at specific file:line locations)
2. **Gathers** 200+ issues, 50 past decisions, 20 recent PRs, in-flight runs, active projects, Slack threads, reference documents
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

## 2. PM Agent vs. agents.md — The Core Distinction

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

2. **Reason across 200 issues simultaneously** — agents.md gives context to one agent working on one issue. The PM sees the full portfolio and makes cross-cutting decisions (clustering, prioritization, skip reasoning).

3. **Learn from its own mistakes** — the PM reads `PreviousDecisions` (last 50 decisions with outcomes) and adjusts. A coding agent reading agents.md has no feedback loop.

4. **Manage capacity** — the PM does slot allocation (`SlotAllocation.Reactive` vs `SlotAllocation.Projects`) and decides what fits in available capacity. agents.md has no concept of resource constraints.

5. **Evolve project strategy** — the PM's `ProjectCycle` model tracks cycle-by-cycle analysis, lessons learned, and approach history. It adjusts strategy based on what worked. agents.md can't iterate on a multi-week goal.

**The one-line distinction**: agents.md is institutional knowledge (static). The PM agent is institutional intelligence (dynamic, learning, strategic).

---

## 3. The Promotion Question: Should PM Be Top-Level?

### Arguments for promoting

1. **It's already the most important thing in the system.** The PM decides what every coding agent works on. It's the orchestrator above Sessions and Projects. Hiding it in settings undersells the entire product.

2. **It's the key differentiator.** Every AI coding tool has "agents that write code." Almost none have a strategic PM layer that learns, prioritizes, and coordinates. This is the wedge. Burying it communicates it's a setting, not a feature.

3. **The current split is confusing.** Config in `/prioritization`, output in `/plans`, status on the Sessions nav item. Users can't build a mental model of "the PM" as a coherent concept when it's scattered.

4. **Projects already got promoted.** Design doc 30 explicitly said "No new nav items" and "Enhance, don't add." But Projects got its own top-level nav item anyway because it deserved it. The PM has a stronger case — it's what makes Projects smart.

5. **Company-specific context deserves visibility.** Product philosophy, direction, focus areas, reference documents, and priority weights are the soul of the PM. They shouldn't be buried in a settings dropdown.

### Arguments against promoting

1. **Cruft risk.** Four nav items (Overview, Sessions, Projects, PM) starts to feel heavy. Each new top-level item adds cognitive load.

2. **The PM is infrastructure, not a destination.** Users don't "visit" the PM — they see its effects in Sessions and Projects. Making it a page might create a place nobody goes.

3. **Settings vs. entity confusion.** Unlike Sessions and Projects (which are lists of things), the PM is a mix of config and output. It doesn't fit the "list → detail" pattern of the other nav items.

4. **Design doc 30's original instinct was right.** "Enhance, don't add" is a good principle. The PM's value is best shown through its effects on Sessions and Projects, not on its own page.

### Recommendation: Promote, but as a **different kind of page**

The PM isn't a list view like Sessions or Projects. It's a **command center** — part dashboard, part config, part history. That's okay. Not every nav item needs to be a CRUD list.

```
┌──────────────────────────────┐
│  Overview                    │
│  Sessions  ●                 │
│  Projects                    │
│  PM Agent                    │  ← NEW: consolidated PM home
└──────────────────────────────┘
```

Remove "Prioritization" from the dropdown. Remove the orphaned `/plans` route. Consolidate everything PM into one place.

---

## 4. PM Agent Page Structure

### Tab-based layout

```
PM Agent
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

┌─────────┬────────────┬──────────┬─────────────┐
│  Plan   │  Decisions │  Context │  Documents  │
└─────────┴────────────┴──────────┴─────────────┘
```

#### Plan tab (default) — formerly `/plans`
- Latest PM plan with analysis, tasks, clusters, skips
- "Analyze now" button
- Context stats (issues reviewed, decisions learned from, PRs checked)
- Plan history accordion

#### Decisions tab — currently not exposed
- Decision log table with success rate headline
- Filter by project, decision type, outcome
- Trend line: PM success rate over time
- This is the **learning loop made visible** — the PM's track record

#### Context tab — formerly `/prioritization`
- Product philosophy, direction, focus/avoid areas
- PM schedule and model selection
- Priority weights with sum validation
- Per-repo overrides list

#### Documents tab — currently nested in prioritization
- Reference documents (roadmaps, strategy docs, architecture)
- Add/edit/delete with type badges
- Shows "last read by PM" timestamp to prove documents are being used

### Why tabs, not separate pages

The PM is a single concept with multiple facets. Tabs keep it unified. Users should think "I'm configuring and monitoring the PM" — not "I'm on a different page."

---

## 5. Making the PM Visibly Different — Concrete Strategies

### 5a. Thread PM reasoning into Sessions

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

### 5b. Show what the PM chose NOT to do

The skip list is one of the PM's most valuable outputs. A human PM builds trust by showing they considered everything and made deliberate tradeoffs. Surface skipped issues with reasons:

```
Deprioritized (4 issues):
  PAY-7b1c  "Duplicate of PAY-3a2d (already in-flight)"
  UI-9d4e   "In avoid area: legacy-auth"
  API-2e5f  "Needs human decision: unclear if feature or bug"
  DB-1f3g   "Too complex: requires schema migration (PM confidence: low)"
```

### 5c. Surface institutional learning

The decision log with outcomes is the PM's moat. Show it as a narrative:

```
PM track record (last 30 days):
  45 tasks delegated → 38 succeeded (84% success rate)
  12 issues skipped → 9 still open, 3 resolved by other means
  3 clusters identified → 2 resolved as batch, 1 in progress

  Insight: Auth-related tasks have 92% success rate.
  Payment tasks have 67% — PM now requires high confidence for payment.
```

### 5d. Make product context feel alive

Instead of a static settings form, show how product context influenced recent decisions:

```
Product context health:
  ✓ Philosophy referenced in 8 of last 10 plans
  ✓ "Q1 reliability focus" matched 12 prioritized tasks
  ⚠ Direction last updated 45 days ago — consider refreshing
  ✓ Avoid area "legacy-auth" correctly filtered 3 issues
```

### 5e. Company-specific vs. general — visual layering

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

---

## 6. How This Makes 143 Different

### The pitch

Most AI coding tools: "Give us a ticket, we'll write code."

143 with a visible PM: "We have an AI PM that reads your product strategy, analyzes your issue backlog, learns from what worked, and decides what your agents should build next. You set the direction. The PM handles prioritization, planning, and coordination. The coding agents execute."

### The information hierarchy tells the story

```
Overview  → "Here's the state of the world"
PM Agent  → "Here's what we should do about it and why"  ← THE DIFFERENTIATOR
Sessions  → "Here's the work being done"
Projects  → "Here's the long-term goals being pursued"
```

Each level answers a different question. The PM is the strategic layer between "what exists" (Overview) and "what's happening" (Sessions/Projects). Without it visible, 143 looks like "a dashboard for agent runs." With it visible, 143 looks like "an AI engineering team with a PM."

---

## 7. Implementation Sketch

### Phase 1: Consolidate (low effort, high clarity)
1. Add "PM Agent" to sidebar nav with `Brain` or `Lightbulb` icon
2. Create `/pm` page with tabs: Plan, Decisions, Context, Documents
3. Move `/plans` content → Plan tab
4. Move `/prioritization` content → Context tab + Documents tab
5. Build Decisions tab from existing `pm_decision_log` data
6. Remove "Prioritization" from dropdown menu
7. Move PM status dot from Sessions to PM Agent nav item

### Phase 2: Thread PM intelligence (medium effort, high impact)
8. Add PM reasoning card to session detail (for PM-spawned sessions)
9. Add "Deprioritized" section to plan view with skip reasoning
10. Add context stats to plan view (issues reviewed, decisions learned from)
11. Add "PM spawned this session" attribution badge on session cards

### Phase 3: Show learning (higher effort, strongest differentiator)
12. Build decision history table with success rate, trends, filtering
13. Add "context health" indicators showing how product context influences decisions
14. Add "PM insights" card to Overview showing patterns and suggestions
15. Show effective context inheritance (org → repo) on per-repo settings

---

## 8. Open Questions

1. **Icon choice**: Brain? Lightbulb? Compass? Target (currently used for Prioritization)?
2. **Name**: "PM Agent" vs "PM" vs "Planner" vs "Strategy"? "PM Agent" is the most honest about what it is.
3. **Should the PM status dot move to the PM nav item or stay on Sessions?** Recommendation: move it — the dot represents PM activity, not session activity.
4. **Should Overview become PM-powered?** The Overview could show the PM's latest analysis as its hero content, making the PM feel like the product's intelligence layer rather than a separate page. This is a bigger bet but a stronger story.
5. **Do we need both a PM page AND PM reasoning threaded into Sessions/Projects?** Yes — the page is the "configure and monitor" home, the threading is "see the effects everywhere." They serve different purposes.

---

## 9. What This Is NOT

- **Not a new agent type.** The PM agent already exists. We're giving it a home.
- **Not more settings pages.** We're consolidating two existing pages (prioritization + plans) into one coherent place.
- **Not a feature for power users.** The PM page should be the first place a new user visits after setup to understand what 143 is doing on their behalf.
- **Not competing with agents.md.** agents.md tells coding agents HOW to work. The PM tells the system WHAT to work on. They're complementary, not alternatives.
