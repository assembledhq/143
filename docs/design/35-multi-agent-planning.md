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
2. **Isolation by default** — Each agent works in a git worktree. No merge conflicts during execution.
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

## Part 2: Parallel Execution with Git Worktrees

### The isolation problem

Two agents editing the same repo will create merge conflicts. The solution: **git worktrees**. Each agent gets its own checkout of the repo, works on its own branch, and changes are merged back.

```
repo/                          ← main checkout (engineer's workspace)
.worktrees/
  agent-claude-task-1/         ← Claude Code works here
  agent-codex-task-2/          ← Codex works here
```

### Branch naming convention

```
plan/<plan-name>/<agent>/<task-number>
```

Example:
```
plan/add-audit-logs/claude/task-1
plan/add-audit-logs/codex/task-2
```

### Orchestration flow

```
┌─────────────┐
│  Engineer    │
│  writes      │
│  PLAN.md     │
└──────┬──────┘
       │
       ▼
┌─────────────┐     ┌──────────────┐     ┌──────────────┐
│  Orchestrator│────▶│  Worktree 1  │     │  Worktree 2  │
│  (shell)     │────▶│  Claude Code │     │  Codex       │
│              │     │  Task 1      │     │  Task 2      │
└──────┬──────┘     └──────┬───────┘     └──────┬───────┘
       │                   │                     │
       │              branch:              branch:
       │              plan/.../claude/1    plan/.../codex/2
       │                   │                     │
       ▼                   ▼                     ▼
┌─────────────────────────────────────────────────────┐
│  Engineer reviews branches, merges into feature     │
│  branch, opens PR                                   │
└─────────────────────────────────────────────────────┘
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
./scripts/multi-agent-review.sh <pr-number>
```

This:
1. Fetches the PR diff
2. Kicks off Claude Code review and Codex review in parallel (background processes)
3. Each agent posts its review as a separate PR comment with a clear header
4. A final summary comment synthesizes both reviews

### Review comment format

Each agent's review is posted with a header badge so it's instantly recognizable:

```markdown
## 🔍 Review: Claude Code

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

## Part 4: Integration with 143.dev

### How this fits the product

143.dev already orchestrates coding agents for automated fixes. Multi-agent planning extends this:

1. **PM agent writes the PLAN.md** — The existing PM agent (design doc 30) could output plans in this format, assigning tasks to different coding agents based on their strengths.
2. **Agent orchestrator reads PLAN.md** — The existing agent orchestrator (design doc 06) can be extended to support multi-agent task distribution.
3. **Validation runs on each branch** — The existing validation pipeline (design doc 07) validates each agent's output independently before merge.

### Future: Agent-to-agent handoffs

In the future, agents could hand off partially-completed work:
- Claude Code generates the backend API → pushes branch
- Codex picks up the branch, reads the API, builds the frontend
- Both branches merge into the feature branch

This is the `Depends on` field in action, just automated.

---

## Part 5: UX & Engineering Experience

### For the engineer writing plans

**The PLAN.md should feel like writing a tech spec**, not configuring a build system. Keep it markdown. Keep it readable. The structured fields (Agent, Files, Acceptance criteria) are lightweight metadata, not YAML config.

**Auto-generation helps**: An agent can generate a draft PLAN.md from a one-liner description. The engineer reviews, adjusts, and approves. This is faster than writing from scratch but keeps the human in control.

**IDE integration**: PLAN.md files get syntax highlighting and validation via a simple schema. Editors can show which tasks are assigned to which agent, which are complete, which are blocked.

### For the engineer reviewing results

**One PR per plan, multiple commits**: Each agent's work is a separate commit (or series of commits) on the feature branch. The engineer reviews the full PR, not individual agent outputs.

**Diff by agent**: The PR description includes a breakdown of which agent did what, linking to the specific commits. This makes review faster — you know which "teammate" wrote which code.

**Conflict resolution is manual**: If two agents touched overlapping code (shouldn't happen with good planning, but it will), the engineer resolves conflicts manually. The tooling shows where conflicts are and which tasks caused them.

### For the team adopting this

**Start small**: Use PLAN.md for one feature. Run one agent. Then try two agents on a split task. Then try parallel reviews. Build confidence incrementally.

**Measure what matters**:
- Time from plan to merged PR
- Number of human interventions needed
- Review coverage (issues caught by agent A that agent B missed)
- Conflict rate (how often do agents step on each other)

---

## Implementation: Scripts

Three scripts in `scripts/multi-agent/`:

1. **`plan-execute.sh`** — Reads PLAN.md, creates worktrees, assigns tasks to agents
2. **`multi-review.sh`** — Kicks off parallel PR reviews from multiple agents
3. **`plan-status.sh`** — Shows status of all tasks in a plan (which branches exist, CI status)

These are intentionally shell scripts, not a Go service. They're developer tools, not product infrastructure. They should be simple enough to understand, modify, and debug.

---

## Open Questions

1. **How do we handle agent-specific context?** Claude Code reads CLAUDE.md, Codex reads its own config. Should PLAN.md reference these, or should each agent's context file reference the plan?
2. **Cost management**: Running 2-3 agents in parallel multiplies API costs. Should we add a cost estimation step before execution?
3. **State persistence**: If an agent fails mid-task, how do we resume? The worktree and branch are still there, but the agent's context is lost.
4. **Codex vs Claude Code strengths**: Should the orchestrator auto-assign agents based on task domain, or should engineers always choose?
