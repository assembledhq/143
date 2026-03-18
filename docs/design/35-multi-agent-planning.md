# Design: Multi-Agent Planning & Review Orchestration

## Problem

Today, using multiple coding agents (Claude Code, Codex, Gemini CLI, etc.) on the same codebase is manual and friction-heavy. Engineers want to:

1. **Write one plan, fan out to many agents** — a structured planning doc that any agent can pick up and execute independently
2. **Run parallel reviews** — kick off Claude Code + Codex reviews on the same PR simultaneously and get synthesized feedback
3. **Divide-and-conquer large tasks** — split work across agents by domain (frontend/backend) or by task, with each agent working in isolation

The goal: make multi-agent collaboration feel as natural as assigning tasks to team members.

---

## Design Principles

1. **Agent-agnostic planning format** — Plans are structured markdown that any agent can parse. No vendor lock-in.
2. **Container isolation** — Each agent already runs in its own Docker sandbox container. No worktrees needed — containers are the isolation boundary. We only need branch naming conventions.
3. **Human stays in the loop** — The engineer writes the plan, approves the merge. Agents execute.
4. **Composition over orchestration** — Simple scripts that chain together, not a monolithic orchestrator.
5. **Progressive complexity** — Start with a plan file and a shell script. Graduate to CI integration when ready.

---

## Part 1: The Planning Doc Format (PLAN.md)

### Why a shared format matters

Different agents parse instructions differently, but they all handle structured markdown well. The planning doc is the **contract** between the engineer and the agents. It needs to be:

- Human-readable (engineers review and approve it)
- Machine-parseable (agents extract their assigned tasks)
- Self-contained (includes enough context that an agent can work without asking questions)

### Format specification

```markdown
# Plan: <title>

## Context
<Background on what we're building and why. 2-3 sentences max.>

## References
- `docs/design/XX-relevant-doc.md` — <why it's relevant>
- `src/components/Foo.tsx` — <what to look at>

## Tasks

### Task 1: <imperative description>
- **Agent**: claude-code | codex | any
- **Domain**: backend | frontend | infra | docs
- **Files**: `internal/services/foo.go`, `internal/db/foo.go`
- **Depends on**: (none | Task N)
- **Acceptance criteria**:
  - [ ] Unit tests pass
  - [ ] No new lint errors
  - [ ] <specific behavioral criteria>
- **Notes**: <additional context, constraints, or approach hints>

### Task 2: <imperative description>
- **Agent**: codex
- **Domain**: frontend
- **Files**: `frontend/src/components/Bar.tsx`, `frontend/src/hooks/useBar.ts`
- **Depends on**: Task 1
- **Acceptance criteria**:
  - [ ] Component renders correct state
  - [ ] Matches existing design patterns
- **Notes**: Use the RadioCardGroup pattern from settings pages.

## Constraints
- <Global constraints that apply to all tasks>
- Do not modify the database schema
- All changes must pass CI

## Out of Scope
- <Things agents should NOT do>
```

### Key design decisions

**Why files are explicit**: Listing files per task gives agents a clear scope boundary. Two agents won't touch the same file. If they must, the dependency chain (`Depends on`) forces sequential execution.

**Why acceptance criteria are checkboxes**: Agents can self-validate. Claude Code runs tests and checks boxes. If an agent can't check all boxes, it flags the task as incomplete rather than silently shipping broken code.

**Why "Agent" is a field, not a folder**: We considered separate files per agent, but a single PLAN.md lets the engineer see the full picture. The orchestrator script filters tasks by agent assignment.

---

## Part 2: Parallel Execution via Container Isolation

### Why not git worktrees?

143.dev already runs each agent in its own **sandboxed Docker container** with an isolated clone of the repository. The container *is* the isolation boundary. We don't need worktrees — we just need:

1. **Branch naming conventions** so each agent's work lands on a predictable branch
2. **A plan entity** that ties the branches together

### Branch naming convention

```
plan/<plan-id>/<task-number>
```

Example:
```
plan/a1b2c3/task-1    ← Claude Code working on backend
plan/a1b2c3/task-2    ← Codex working on frontend
```

The plan ID comes from the database (UUID), not the plan name. This avoids collisions and keeps branch names short.

### Orchestration flow

```
┌─────────────┐
│  Engineer    │
│  writes plan │
│  (UI or MD)  │
└──────┬──────┘
       │
       ▼
┌──────────────┐     ┌──────────────────┐     ┌──────────────────┐
│  Orchestrator │────▶│  Container 1     │     │  Container 2     │
│  (Go service) │────▶│  Claude Code     │     │  Codex           │
│               │     │  Task 1 (backend)│     │  Task 2 (frontend)│
└──────┬───────┘     └──────┬───────────┘     └──────┬───────────┘
       │                    │                         │
       │               branch:                   branch:
       │               plan/<id>/task-1          plan/<id>/task-2
       │                    │                         │
       ▼                    ▼                         ▼
┌─────────────────────────────────────────────────────────────┐
│  System merges task branches → feature branch → opens PR   │
│  Engineer reviews the combined PR                          │
└─────────────────────────────────────────────────────────────┘
```

---

## Part 3: Multi-Agent Review

### The review problem

PR reviews from a single agent miss things. Different agents have different strengths:
- **Claude Code**: deep architectural reasoning, security analysis, pattern consistency
- **Codex**: fast iteration, broad coverage, quick fixes

Running both in parallel gives you better coverage than either alone.

### Review orchestration

A single command kicks off reviews from multiple agents and posts results as PR comments:

```bash
./scripts/multi-agent/multi-review.sh <pr-number>
```

This:
1. Fetches the PR diff
2. Kicks off Claude Code review and Codex review in parallel (background processes)
3. Each agent posts its review as a separate PR comment with a clear header
4. A final summary comment synthesizes both reviews

### Review comment format

Each agent's review is posted with a header so it's instantly recognizable:

```markdown
## Review: Claude Code

**Focus**: Architecture, security, patterns

### Issues Found
- ...

### Suggestions
- ...

---
*Automated review via multi-agent orchestration*
```

### When NOT to use multi-agent review

- **Small changes** (< 50 lines): One agent is sufficient. Don't waste API costs.
- **Documentation-only PRs**: A single agent can handle these.
- **Urgent hotfixes**: Speed matters more than coverage. Use one agent.

---

## Part 4: Sessions Page UI — Approaches

This is the core UX question: how do multi-agent plans appear in the sessions page?

### Current state

The sessions page is a **flat table** with columns: Status, Title, Agent Type, Triggered By, Confidence, Last Modified. Sessions have tab filters (All, Active, Needs Guidance, Failed, Done, Decisions).

Existing grouping primitives in the data model:
- `parent_session_id` — self-referential FK for revision chains
- `pm_plan_id` — links session to a PM agent plan
- `project_task_id` — links session to a project task
- Projects already have `execution_mode` (sequential, parallel, dependency_graph) and `max_concurrent`

### Approach A: Grouped rows (expand/collapse in the sessions table)

Plans appear as **collapsible parent rows** in the existing sessions table. A plan row shows aggregate info; expanding it reveals the individual task sessions.

```
┌─────────┬──────────────────────────────┬───────────┬──────────┐
│ Status  │ Title                        │ Agent     │ Modified │
├─────────┼──────────────────────────────┼───────────┼──────────┤
│ ● 2/3   │ ▶ Plan: Add audit logging    │ 3 agents  │ 2m ago   │
├─────────┼──────────────────────────────┼───────────┼──────────┤
│ ●       │   Task 1: Add DB store       │ claude    │ 5m ago   │
│ ●       │   Task 2: Add API handler    │ claude    │ 3m ago   │
│ ○       │   Task 3: Add UI components  │ codex     │ 2m ago   │
├─────────┼──────────────────────────────┼───────────┼──────────┤
│ ●       │ Fix null pointer in parser   │ codex     │ 10m ago  │
├─────────┼──────────────────────────────┼───────────┼──────────┤
│ ● 1/2   │ ▶ Plan: Notification system  │ 2 agents  │ 15m ago  │
└─────────┴──────────────────────────────┴───────────┴──────────┘
```

**Pros:**
- Minimal UI change — stays in the sessions page, uses existing table patterns
- Plans and single sessions are visually in the same place — one page to check everything
- Progressive disclosure: collapsed by default, expand when you care
- Tab filters still work (Active plans = any plan with an active task)

**Cons:**
- Table rows with nesting can get visually noisy with many plans
- Sorting becomes ambiguous (sort by plan's last modified, or individual task's?)
- The sessions table is doing two jobs (list + grouping) which can feel cramped
- Hard to show plan-level metadata (context, constraints, progress bar) in a table row

**Implementation complexity:** Low. Add a `plan_id` FK to sessions, group in the query, render with expand/collapse (Radix Collapsible or accordion).

---

### Approach B: Plans as a separate page/tab

Add a **"Plans" tab** to the sessions page navigation (alongside the existing All/Active/Failed/Done tabs), or a dedicated `/plans` page.

```
Sessions page:
┌──────────────────────────────────────────────────────────────┐
│  [All] [Active] [Failed] [Done] [Decisions] [Plans]         │
└──────────────────────────────────────────────────────────────┘

Plans tab:
┌─────────────────────────────────────────────────────────────┐
│                                                             │
│  ┌─────────────────────────────┐  ┌────────────────────────┐│
│  │ Add audit logging           │  │ Notification system    ││
│  │ ━━━━━━━━━━━━━━━━━━━━━ 67%  │  │ ━━━━━━━━━━━━━━━━ 50%  ││
│  │                             │  │                        ││
│  │ Task 1  ● claude  done     │  │ Task 1  ● codex  done  ││
│  │ Task 2  ● claude  running  │  │ Task 2  ○ claude pend  ││
│  │ Task 3  ○ codex   pending  │  │                        ││
│  │                             │  │                        ││
│  │ 2m ago · triggered by Alex  │  │ 15m ago · triggered by ││
│  └─────────────────────────────┘  └────────────────────────┘│
│                                                             │
└─────────────────────────────────────────────────────────────┘

Plan detail page (/plans/:id):
┌─────────────────────────────────────────────────────────────┐
│  ← Back to Plans                                            │
│                                                             │
│  Add audit logging                                          │
│  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 67% (2/3 tasks)       │
│                                                             │
│  Context: Adding audit log infrastructure for SOC2          │
│  compliance. Backend store + API + UI viewer.               │
│                                                             │
│  ┌──────────┐     ┌──────────┐     ┌──────────┐            │
│  │ Task 1   │────▶│ Task 2   │     │ Task 3   │            │
│  │ DB store │     │ API hand │     │ UI comp  │            │
│  │ ● done   │     │ ● running│     │ ○ pending│            │
│  │ claude   │     │ claude   │     │ codex    │            │
│  └──────────┘     └──────────┘     └──────────┘            │
│                        │                                    │
│                   depends on Task 1                         │
│                                                             │
│  [View combined diff]  [Open PR]  [Cancel remaining]        │
│                                                             │
│  ── Task Sessions ──────────────────────────────────────    │
│  (each task links to its session detail page)               │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

**Pros:**
- Plans get a dedicated space with room for rich metadata (context, dependency graph, progress bar)
- Sessions table stays clean and flat — no nesting complexity
- The plan detail page can show the dependency DAG visually (task 2 depends on task 1)
- Natural place to put plan-level actions (cancel all, retry failed, view combined diff)
- Scales well: 20 plans with 5 tasks each = 20 cards, not 100 table rows

**Cons:**
- More navigation: engineer has to check two places (sessions + plans)
- Doesn't answer "what's running right now?" in a single glance — active sessions are on the sessions tab, active plans are on the plans tab
- More frontend work: new page, new components, new API endpoint
- Risk of plans feeling disconnected from the sessions they spawn

**Implementation complexity:** Medium. New `plans` table, new API endpoints, new page components. But the plan detail page is the real value — showing the dependency graph and combined diff.

---

### Approach C: Extend Projects (no new entity)

A "plan" is just a **Project with `execution_mode: parallel`** and multiple task-to-agent assignments. No new entity — plans are projects.

```
Projects already have:
- tasks with batch_number for parallel grouping
- execution_mode: sequential | parallel | dependency_graph
- max_concurrent for parallel limits
- project_task_id on sessions linking tasks to sessions
```

The existing projects page becomes the home for multi-agent plans. A "Quick Plan" button creates a short-lived project from a PLAN.md.

**Pros:**
- Zero new entities — leverages existing schema and UI
- Projects already handle task dependencies, batching, and execution modes
- Sessions already link to project tasks via `project_task_id`
- Less code to build and maintain

**Cons:**
- Projects are designed for long-lived, recurring work (evergreen/finite lifecycle). Plans are ephemeral.
- Mixing short-lived plans with long-lived projects clutters the projects page
- Projects carry a lot of weight (cadences, lifecycle states, automations) that plans don't need
- The mental model is off: "I want to split a feature across two agents" doesn't feel like "creating a project"

**Implementation complexity:** Low for backend, medium for UX. The data model works, but the UX needs careful design to keep plans lightweight when projects are heavyweight.

---

### Approach D: Plan as a session variant (plan session spawns child sessions)

A plan is itself a **session** with `session_type: plan`. When it runs, the orchestrator session spawns child sessions (one per task) linked via `parent_session_id`.

```
Sessions table (flat, but with visual grouping cues):
┌─────────┬──────────────────────────────┬───────────┬──────────┐
│ Status  │ Title                        │ Agent     │ Modified │
├─────────┼──────────────────────────────┼───────────┼──────────┤
│ ● 2/3   │ Plan: Add audit logging      │ multi     │ 2m ago   │  ← plan session
│ ●       │ ├─ Add DB store              │ claude    │ 5m ago   │  ← child session
│ ●       │ ├─ Add API handler           │ claude    │ 3m ago   │  ← child session
│ ○       │ └─ Add UI components         │ codex     │ 2m ago   │  ← child session
├─────────┼──────────────────────────────┼───────────┼──────────┤
│ ●       │ Fix null pointer in parser   │ codex     │ 10m ago  │  ← regular session
└─────────┴──────────────────────────────┴───────────┴──────────┘
```

**Pros:**
- Uses existing `parent_session_id` — already in the schema
- Everything stays on the sessions page — one place to see everything
- The plan session detail page becomes the plan dashboard (shows child sessions, progress, combined diff)
- Clicking a child session goes to the normal session detail page
- Natural mental model: "I started a plan, it spawned sub-tasks" — same as "I started a session, it created revisions"
- Filtering works naturally: Active tab shows plans with active children, Done tab shows completed plans

**Cons:**
- `parent_session_id` currently means "revision of" — reusing it for "task within plan" overloads the meaning
- The sessions table does visual nesting, which can be noisy
- Plan metadata (context, constraints, dependency graph) has to fit within the session model or use a JSONB field
- Not clear where the PLAN.md content lives — in the session's `result_summary`? A new field?

**Implementation complexity:** Low-Medium. Reuses existing parent/child pattern. Needs a `session_type` or `plan_id` field to distinguish plan sessions from regular sessions. The session detail page needs a "plan view" variant.

---

### Recommendation

**Start with Approach D (plan-as-session), graduate to Approach B (dedicated page) if plans become a power feature.**

Rationale:

1. **D is the lowest-friction starting point.** It reuses `parent_session_id`, keeps everything on the sessions page, and doesn't require new entities or pages. Engineers see plans and sessions in one view.

2. **D matches the existing mental model.** Users already understand that sessions can have parent sessions (revisions). "A plan is a session that spawns child sessions" is a small conceptual step.

3. **When plans become common enough to justify their own page, upgrade to B.** The plan-as-session data model still works — you just add a filtered view or dedicated page that shows only plan sessions with richer visualization (dependency graph, combined diff, progress bars).

4. **Avoid C (projects).** The mental model mismatch is real. Plans are "I want to do this thing with 2 agents right now." Projects are "this is an ongoing area of work." Merging them creates confusion.

### Implementation path for Approach D

1. Add `plan_id UUID` nullable FK to sessions (or reuse `parent_session_id` with a `session_type` enum: `standard | plan | plan_task`)
2. When user creates a multi-agent plan, create a parent session of type `plan`
3. Orchestrator creates child sessions (type `plan_task`) linked to parent, one per task
4. Each child session runs in its own container with its assigned agent
5. Sessions page groups children under parents visually (tree lines: `├─`, `└─`)
6. Parent session detail page shows plan overview: task progress, dependency status, combined diff, merge action
7. Child session detail pages are standard session detail pages (logs, chat, diff, validation)

---

## Part 5: Integration with 143.dev

### How this fits the product

143.dev already orchestrates coding agents for automated fixes. Multi-agent planning extends this:

1. **PM agent writes the plan** — The existing PM agent (design doc 30) could output plans in this format, assigning tasks to different coding agents based on their strengths.
2. **Agent orchestrator reads the plan** — The existing agent orchestrator (design doc 06) can be extended to support multi-agent task distribution.
3. **Validation runs on each task branch** — The existing validation pipeline (design doc 07) validates each agent's output independently before merge.

### Future: Agent-to-agent handoffs

In the future, agents could hand off partially-completed work:
- Claude Code generates the backend API → pushes to task branch
- Codex picks up the branch, reads the API, builds the frontend
- Task branches merge into the feature branch

This is the `Depends on` field in action, just automated.

---

## Part 6: UX & Engineering Experience

### For the engineer writing plans

**The PLAN.md should feel like writing a tech spec**, not configuring a build system. Keep it markdown. Keep it readable. The structured fields (Agent, Files, Acceptance criteria) are lightweight metadata, not YAML config.

**Auto-generation helps**: An agent can generate a draft plan from a one-liner description. The engineer reviews, adjusts, and approves. This is faster than writing from scratch but keeps the human in control.

### For the engineer reviewing results

**One PR per plan, multiple commits**: Each agent's work is a separate commit (or series of commits) on the feature branch. The engineer reviews the full PR, not individual agent outputs.

**Diff by agent**: The PR description includes a breakdown of which agent did what, linking to the specific commits. This makes review faster — you know which "teammate" wrote which code.

### For the team adopting this

**Start small**: Use a plan for one feature with one agent. Then try two agents on a split task. Then try parallel reviews. Build confidence incrementally.

**Measure what matters**:
- Time from plan to merged PR
- Number of human interventions needed
- Review coverage (issues caught by agent A that agent B missed)
- Conflict rate (how often do agents step on each other)

---

## Implementation: Scripts (local dev workflow)

Three scripts in `scripts/multi-agent/` for local development (outside of 143.dev's container orchestration):

1. **`plan-execute.sh`** — Reads PLAN.md, creates worktrees (for local dev), assigns tasks to agents
2. **`multi-review.sh`** — Kicks off parallel PR reviews from multiple agents
3. **`plan-status.sh`** — Shows status of all plan branches

These are intentionally shell scripts, not a Go service. They're developer tools for local multi-agent workflows, separate from the product's container-based orchestration.

---

## Open Questions

1. **How do we handle agent-specific context?** Claude Code reads CLAUDE.md, Codex reads its own config. Should the plan reference these, or should each agent's context file reference the plan?
2. **Cost management**: Running 2-3 agents in parallel multiplies API costs. Should we add a cost estimation step before execution?
3. **State persistence**: If an agent fails mid-task, how do we resume? The container and branch are still there, but the agent's context may be lost.
4. **Agent auto-assignment**: Should the orchestrator auto-assign agents based on task domain (backend → Claude, frontend → Codex), or should engineers always choose?
5. **Branch merging strategy**: When all tasks complete, should the system auto-merge task branches into a feature branch, or wait for human approval? Auto-merge is faster but risks conflicts going unnoticed.
6. **Plan creation UX**: Should plans be created via the UI (form/chat), via PLAN.md upload, or via the PM agent? Probably all three, but what's the primary path?
