# 35 - PM Agent: Top-Level Concept Review

> Should the PM agent be promoted to a first-class concept alongside Sessions and Projects? How does it differ from agents.md? How do we make it clearly differentiated?

**Status**: Design review (decision pending)

## Decision Summary

**Recommendation**: Promote the PM Agent to a top-level nav item, but design it as an operator workspace for a PM/engineer hybrid, not as a generic settings page.

The goal is not "make the PM visible" in the abstract. The goal is to give one cross-functional owner a single place to:

1. understand what the system thinks should happen next
2. inspect why the PM made those choices
3. tune direction and constraints when the PM is off
4. build trust over time through visible outcomes and learning

This page should earn its place in the nav by supporting a repeated operating loop, not by housing PM-related configuration.

**Additional recommendation**: Make autonomy a first-class part of the PM model. New orgs should start in suggestion mode, but the path into higher-autonomy operation should be explicit, legible, and easy to adopt as trust grows.

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

- What changed since the last plan?
- What is the PM recommending now?
- Why is it recommending that?
- What, if anything, should I change?
- Is the PM ready for more autonomy?

---

## 2. Current State Assessment

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

2. **Reason across 200 issues simultaneously** — agents.md gives context to one agent working on one issue. The PM sees the full portfolio and makes cross-cutting decisions (clustering, prioritization, skip reasoning).

3. **Learn from its own mistakes** — the PM reads `PreviousDecisions` (last 50 decisions with outcomes) and adjusts. A coding agent reading agents.md has no feedback loop.

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

### Arguments against promoting

1. **Cruft risk.** Four nav items (Overview, Sessions, Projects, PM) starts to feel heavy. Each new top-level item adds cognitive load.

2. **The PM is infrastructure, not a destination.** Users don't "visit" the PM — they see its effects in Sessions and Projects. Making it a page might create a place nobody goes.

3. **Settings vs. entity confusion.** Unlike Sessions and Projects (which are lists of things), the PM is a mix of config and output. It doesn't fit the "list → detail" pattern of the other nav items.

4. **Design doc 30's original instinct was right.** "Enhance, don't add" is a good principle. The PM's value is best shown through its effects on Sessions and Projects, not on its own page.

### Recommendation: Promote, but only if it supports a clear operating loop

The PM should be top-level if we are committing to it as an **active operating surface** for the PM/engineer hybrid. If it is mostly settings plus historical output, it should stay secondary.

The standard to clear is:

- users visit it repeatedly, not just during setup
- users can take a clear action from it
- it improves trust and control, rather than just exposing more internals
- it gives users a clear path from review-heavy usage into autonomous operation

### Recommendation: Promote, but as a **different kind of page**

The PM isn't a list view like Sessions or Projects. It's an **operator workspace**: part dashboard, part decision review, part steering surface. That's okay. Not every nav item needs to be a CRUD list.

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

## 5. PM Agent Information Architecture

### Design principle

Do not make the PM page a dumping ground for all PM-related data. It should be organized around the user's operating loop:

1. Review the latest plan
2. Inspect the reasoning and tradeoffs
3. Adjust context or constraints if needed
4. Verify whether the PM is getting better

### Recommended page model

Use a **summary-first workspace** with tabs beneath it, not tabs alone.

```
PM Agent
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Plan status       Last run       Recommended next step
Trust signal      Capacity split Requiring attention
Autonomy level    Ready to unlock

┌─────────┬────────────┬──────────┬─────────────┐
│  Plan   │  Decisions │  Context │  Documents  │
└─────────┴────────────┴──────────┴─────────────┘
```

The top summary block is important. It makes the page legible for repeat use and prevents users from landing in a content-heavy tab with no orientation.

### Tab-based layout

```
PM Agent
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

┌─────────┬────────────┬──────────┬─────────────┐
│  Plan   │  Decisions │  Context │  Documents  │
└─────────┴────────────┴──────────┴─────────────┘
```

#### Plan tab (default) — formerly `/plans`
- The main answer: what the PM recommends now
- Latest PM plan with prioritized tasks, clusters, skips, and capacity allocation
- "Analyze now" button
- Change summary since previous plan
- Small set of context stats: issues reviewed, decisions learned from, PRs checked
- Plan history in secondary position

The Plan tab should be the default because it maps to the user's primary question: "What should happen next?"

#### Decisions tab — currently not exposed
- The audit and trust surface
- Decision log with outcome, confidence, and rationale summary
- Filter by repo, project, decision type, outcome
- Trend line plus segmented performance by issue type or domain
- Clear links from decisions to resulting sessions/projects

This is the **learning loop made visible** and should answer: "Should I trust this system more or constrain it more?"

#### Context tab — formerly `/prioritization`
- The steering surface
- Product philosophy, direction, focus/avoid areas
- PM schedule and model selection
- Priority weights with sum validation
- Organization autonomy slider with clear capability mapping
- Per-repo overrides list
- Preview of the effective context the PM will actually use

This tab should focus on steering, not on proving intelligence.

#### Documents tab — currently nested in prioritization
- Reference documents (roadmaps, strategy docs, architecture)
- Add/edit/delete with type badges
- Shows "last read by PM" timestamp to prove documents are being used

### Why tabs, not separate pages

The PM is a single concept with multiple facets. Tabs keep it unified. Users should think "I'm operating the PM" — not "I'm on a settings page."

### Interaction modes and ownership

The tabs should not imply the same user intent:

- `Plan` and `Decisions` are operational and likely visited frequently
- `Context` and `Documents` are steering/setup surfaces and likely visited less often

This distinction matters for UX. The operational tabs should prioritize scanability and decision-making. The steering tabs can be more form-oriented.

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
  - plans
  - prioritization
  - clusters
  - skip decisions
  - recommendations
- **system-observed outcomes**
  - success/failure rates
  - merge outcomes
  - post-ship impact
  - decision accuracy trends

This needs to be legible in both the UI and the doc. Teams need to know what they can edit, what the PM inferred, and what the system measured.

### Approval model

The PM should be allowed to recommend broadly, but actions that materially reshape work should have explicit approval boundaries.

Suggested default model:

- PM can automatically produce plans and recommendations
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
| Generate plans and recommendations | Yes | Yes | Yes |
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

The PM has produced 9 reviewed plans with 87% accepted recommendations.
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

- `Plan`
- `Decision`
- `DecisionOutcome`
- `ContextSource`
- `ContextOverride`
- `Recommendation`

Without named primitives, implementation will drift into loosely structured JSON blobs and ad hoc UI copy.

---

## 7. Making the PM Visibly Different — Concrete Strategies

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

The skip list is one of the PM's most valuable outputs. A human PM builds trust by showing they considered everything and made deliberate tradeoffs. Surface skipped issues with reasons, but keep the default view concise:

```
Deprioritized (4 issues):
  PAY-7b1c  "Duplicate of PAY-3a2d (already in-flight)"
  UI-9d4e   "In avoid area: legacy-auth"
  API-2e5f  "Needs human decision: unclear if feature or bug"
  DB-1f3g   "Too complex: requires schema migration (PM confidence: low)"
```

### 5c. Surface institutional learning

The decision log with outcomes is the PM's moat. Show it as a narrative, but anchor it to actions:

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

### 5f. Use progressive disclosure aggressively

The PM should feel intelligent, not verbose. Default UI patterns should be:

- one-sentence rationale summaries
- expandable evidence panels
- structured explanation fields instead of freeform paragraphs everywhere
- a small number of headline metrics tied to clear actions

The system should not require users to read long prose to understand the recommendation.

---

## 8. Day 1 and Empty-State Experience

The PM must work before the team has a rich body of PM context.

### Day 1 behavior

If an org has little or no context yet, the PM should still produce a usable first plan using:

- issue severity and recency
- customer impact signals available from integrations
- codebase context from the repo
- conservative defaults for autonomy

### Empty-state guidance

The PM page should guide the user to improve plan quality over time:

- missing philosophy → "Add a short product philosophy to improve tradeoff quality"
- no focus areas → "Define one or two current focus areas"
- no documents → "Attach roadmap, architecture, or strategy docs"
- no decision history → "The PM will get better after a few completed cycles"

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
- increased manual review and approval of PM plans
- improved session acceptance or merge rates for PM-selected work
- reduced confusion about where PM settings and outputs live
- more explicit user corrections through context updates rather than ad hoc workarounds

If these do not improve, the PM likely does not deserve top-level prominence yet.

---

## 10. How This Makes 143 Different

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
