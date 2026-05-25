# Design: AI Product Manager Agent

> **Status:** Implemented | **Last reviewed:** 2026-03-25

This document describes how to restructure 143.dev so that the **main loop** is an LLM-powered Product Manager (PM) agent that runs on a batch schedule, analyzes all accumulated Sentry errors and Linear tickets, reasons about what matters most, and delegates work to coding agents.

## Problem Statement

The current system is **reactive and per-ticket**: each ingested issue immediately gets a numeric priority score, a complexity estimate, and — if it passes the auto-trigger gates — spawns a coding agent run. This has three problems:

1. **No cross-ticket reasoning.** Five Sentry errors sharing the same root cause get five separate scores and five separate agent runs instead of one coordinated fix.
2. **No strategic thinking.** A numeric formula (`customer_impact × severity × recency`) can't reason about things like "3 of these 8 tickets are regressions from last week's payments refactor — fix the root cause, not the symptoms."
3. **No workload planning.** The system never says "do A first because it unblocks B, skip C because it's a known flaky test, and cluster D+E+F into one fix."

## Current vs. Proposed Architecture

### Current Flow (per-ticket, reactive)

```
Webhook → Ingest → Upsert Issue → [immediately, per-issue] Prioritize → Auto-Trigger → Run Coding Agent
```

Every ingested issue enqueues its own `prioritize` job, which scores it, estimates complexity, and possibly spawns a coding agent. Each issue is evaluated in isolation.

### Implementation Status (current codebase)

The codebase now matches the **PM-driven batch flow**:

- `prioritize` jobs recompute score + complexity for display only (no auto-trigger).
- `ProductContext` + PM settings are available in org settings and mirrored into `product_direction` for compatibility.
- PM tables (`pm_plans`, `pm_decision_log`) and PM service + `pm_analyze` job exist.
- PM UI (plans view + settings) and run-level PM context display are implemented.

### Implementation Progress (2026-03-02)

- [x] Add `pm_plans` + `pm_decision_log` tables and `agent_runs` PM columns
- [x] Add `ProductContext`, `pm_schedule_hours`, and `pm_model` to org settings (with migration from `product_direction`)
- [x] Implement PM stores, plan parsing, and decision log helpers
- [x] Implement PM service (context gathering, sandbox run, plan execution)
- [x] Add `pm_analyze` worker handler + scheduler cron enqueue
- [x] Add PM API endpoints (`/pm/analyze`, `/pm/plans`, `/pm/plans/latest`)
- [x] Inject PM context into agent prompts and store PM fields on runs
- [x] Remove auto-trigger from `prioritize` job handler
- [x] Frontend PM plan view + PM context on runs + Product Context settings UI
- [x] Add tests and run `go vet`, `go build`, `go test`, `npm run typecheck`, `npm run lint`, `npm run build`

This document therefore describes a **planned transition** from the existing flow into the PM-driven loop below.

### Proposed Flow (batch, PM-agent-driven)

```
                                     Webhooks still fire in real-time
                                     ┌─────────────────────────────────────────────┐
  Sentry webhook ──────────────────▶ │                                             │
  Linear webhook ──────────────────▶ │  Ingestion (unchanged)                      │
  GitHub webhook ──────────────────▶ │  Normalize → Upsert into issues table       │
  Polling workers ─────────────────▶ │  (status = "open")                          │
                                     └──────────────────────┬──────────────────────┘
                                                            │
                                              Issues accumulate in DB
                                                            │
                                     ┌──────────────────────┴──────────────────────┐
                                     │                                             │
                                     │  BATCH CRON: every N hours (e.g. every 4h)  │
                                     │  Enqueues a "pm_analyze" job per org         │
                                     │                                             │
                                     └──────────────────────┬──────────────────────┘
                                                            │
                                                            ▼
                              ┌─────────────────────────────────────────────────────┐
                              │                                                     │
                              │  PM AGENT  (the "brain" — runs in a sandbox     │
                              │  with full repo access, like a coding agent)     │
                              │                                                     │
                              │  Has access to:                                     │
                              │   • Full GitHub repo (cloned into sandbox)          │
                              │   • All open/triaged issues (Sentry + Linear)       │
                              │   • In-flight agent runs (what's already being      │
                              │     worked on)                                      │
                              │   • Recent agent run outcomes (successes, failures)  │
                              │   • Recent PR outcomes (merged, rejected, reverted) │
                              │   • Org product direction + settings                │
                              │   • Failure patterns (what the agent struggles with) │
                              │                                                     │
                              │  Produces:                                          │
                              │   • Situation analysis ("here's what's going on")   │
                              │   • Prioritized task list with reasoning            │
                              │   • Issue clusters (shared root cause)              │
                              │   • Approach hints per task (for coding agents)     │
                              │   • Skip list with reasons                          │
                              │   • Risk flags                                      │
                              │                                                     │
                              └───────────────────────┬─────────────────────────────┘
                                                      │
                                                      ▼
                              ┌─────────────────────────────────────────────────────┐
                              │                                                     │
                              │  DELEGATION: PM agent's plan → coding agent runs    │
                              │                                                     │
                              │  For each task in the plan (respecting concurrency  │
                              │  limits):                                           │
                              │   1. Mark issues as "triaged"                       │
                              │   2. Create AgentRun with PM's approach hints       │
                              │   3. Enqueue run_agent job                          │
                              │                                                     │
                              │  Skip entries → log reasoning, leave as "open"      │
                              │  or mark "wont_fix" per PM recommendation           │
                              │                                                     │
                              └─────────────────────────────────────────────────────┘
```

### What Stays the Same

- **All webhook handling.** Sentry, Linear, and GitHub webhooks still fire and ingest issues in real-time. The ingestion pipeline (`service.go`, adapters, normalization, dedup) is untouched.
- **The coding agent pipeline.** Once a `run_agent` job is enqueued, everything downstream is identical: orchestrator → sandbox → execute → validate → open PR.
- **The existing prioritization service.** Kept for manual "Reprioritize" button and dashboard score badges. It just no longer auto-triggers coding runs.

### What Changes

- **The `prioritize` job no longer auto-triggers `run_agent`.** Ingestion still enqueues `prioritize` for scoring/display, but `CheckAutoTrigger()` is removed from the automated flow. The PM agent owns that decision now.
- **A new `pm_analyze` cron job** runs every N hours and is the main entry point for all work planning.
- **Coding agents receive richer context** — the PM agent's suggested approach, cluster info, and risk assessment are injected into their prompts.

## Product Context: What the PM Agent Needs to Know About You

The PM agent's quality is directly proportional to how well it understands the org's goals, philosophy, and constraints. Today, `OrgSettings.ProductDirection` is a single free-text string — too vague for an agent making strategic decisions.

Replace it with a structured `ProductContext` that captures the full picture an admin would give a new PM on day one:

### `ProductContext` struct (replaces `ProductDirection` string)

```go
// ProductContext is the structured input that tells the PM agent who you are,
// what you care about, and how you want work done. This is the single most
// important configuration for PM agent quality — a PM without product context
// is just a scoring function with extra steps.
type ProductContext struct {
    // Philosophy: how should the PM agent think about tradeoffs?
    // This is the "personality" of the PM — it shapes every decision.
    // Include your preferences on fix style (minimal vs. thorough),
    // risk appetite, code quality standards, etc.
    //
    // Examples:
    //   "We value simplicity above all else. The codebase should be small and
    //    readable. Prefer deleting code over adding abstractions. If a fix
    //    requires more than 50 lines, question whether the approach is right."
    //
    //   "We're building for enterprise — configurability and extensibility
    //    matter more than minimalism. Every behavior should be overridable.
    //    Edge cases matter because our customers hit all of them."
    //
    //   "We're a 3-person startup moving fast. Prefer working code over
    //    perfect code. Skip edge cases that affect < 1% of users. Don't
    //    gold-plate — ship and iterate."
    Philosophy    string   `json:"philosophy"`

    // Direction: what is the team focused on right now?
    // This changes more frequently than philosophy (quarterly or monthly).
    //
    // Examples:
    //   "Preparing for SOC2 audit — prioritize security and reliability."
    //   "Launching payments v2 — billing module stability is critical."
    //   "Reducing churn — focus on issues reported by paying customers."
    Direction     string   `json:"direction"`

    // FocusAreas: which parts of the codebase matter most right now?
    // Issues in these areas get prioritized; the PM can also use this
    // to give better approach hints (it knows where attention should go).
    //
    // Examples: ["payments", "onboarding", "API stability", "mobile app"]
    FocusAreas    []string `json:"focus_areas,omitempty"`

    // AvoidAreas: what should the agent NOT touch?
    // Areas that are being rewritten, deprecated, or too risky for automation.
    //
    // Examples: ["legacy-auth (being rewritten)", "experimental-features"]
    AvoidAreas    []string `json:"avoid_areas,omitempty"`
}
```

### How `ProductContext` flows into the PM agent via AGENTS.md

The PM agent receives `ProductContext` through the repo's `AGENTS.md` file — the same file that coding agents already read for codebase instructions. Before the PM agent runs, the service **appends** a `## Product Context` section to the existing `AGENTS.md` in the sandbox:

```go
// writeProductContextToAgentsMD appends the org's ProductContext to the repo's
// AGENTS.md (or creates it if it doesn't exist). This means the PM agent discovers
// product context naturally through its standard codebase exploration flow —
// it reads AGENTS.md like any agent would, and finds the product context there.
func (s *Service) writeProductContextToAgentsMD(ctx context.Context, sb *agent.Sandbox, pc *ProductContext) error {
    // Read existing AGENTS.md (may not exist)
    existing, _ := s.sandbox.ReadFile(ctx, sb, "/workspace/AGENTS.md")

    section := fmt.Sprintf(`

## Product Context

**Philosophy:** %s

**Current direction:** %s

**Focus areas:** %s

**Avoid areas:** %s
`, pc.Philosophy, pc.Direction, strings.Join(pc.FocusAreas, ", "), strings.Join(pc.AvoidAreas, ", "))

    return s.sandbox.WriteFile(ctx, sb, "/workspace/AGENTS.md", append(existing, []byte(section)...))
}
```

**Why AGENTS.md instead of a separate context file:**
- The PM agent already reads `AGENTS.md` as part of its standard codebase exploration (step 2 of the workflow). Putting product context there means the agent finds it naturally — no need for a special instruction to read a custom file.
- It mirrors how a human PM would work: they'd read the team's AGENTS.md/README to understand the project, and the product context is right there alongside the codebase instructions.
- Coding agents also read `AGENTS.md`, so if the PM's approach hints reference product philosophy, the coding agent has the same shared context.

This shapes every decision the PM makes:
- **Philosophy** determines *how* the PM thinks — values, tradeoff preferences, fix style, risk appetite. It's the single most expressive field.
- **Direction** determines *what* gets prioritized (security issues rise when preparing for SOC2)
- **Focus/avoid areas** determine *where* the PM sends coding agents (and where it doesn't)

### Why structured input, not auto-bootstrapped

The PM agent can infer *what's broken* from the data (issue stats, failure patterns, occurrence trends). But it cannot infer *what the team cares about*. Product philosophy and strategic direction are fundamentally human inputs — they come from the founder's head, not from Sentry stack traces.

The structured fields (with examples in the UI) make this fast to fill out — 5 minutes of admin time that dramatically improves every PM cycle.

### Auto-bootstrapped context (supplements, doesn't replace)

The PM agent also receives **raw outcome data** that it can reason over directly — no pre-computed aggregates. Since the PM runs as a full agent with codebase access, it's capable of drawing its own conclusions from raw data:

- **Recent run outcomes** (last ~20): success/failure, failure category, and issue. The PM can spot patterns itself ("3 of the last 5 failures were in repo X").
- **Recent PR outcomes** (last ~20): merged/rejected/reverted, reviewer comments. The PM can see what reviewers are pushing back on.
- **In-flight runs**: what's currently being worked on, so the PM doesn't duplicate effort.
- **Issue timestamps**: `first_seen`, `last_seen`, and `occurrence_count` on each issue let the PM see trends directly.

These raw signals make the PM useful from day one even with minimal `ProductContext` input. But the PM gets dramatically better once the admin fills in philosophy + direction.

### Settings UI for `ProductContext`

The Settings page gains a "Product Context" section:

- **Philosophy** — large textarea with placeholder examples ("Tell the PM how you think about tradeoffs")
- **Current Direction** — textarea with placeholder ("What is the team focused on this quarter?")
- **Focus Areas** — tag input (type and press Enter)
- **Avoid Areas** — tag input with warning styling

### Migration from `ProductDirection` string

The existing `product_direction` string field maps to `ProductContext.Direction`. On first load, if `product_context` is null but `product_direction` is set, the system migrates it:

```go
if settings.ProductContext == nil && settings.ProductDirection != "" {
    settings.ProductContext = &ProductContext{
        Direction: settings.ProductDirection,
    }
}
```

## Detailed Design

### 1. Batch Schedule: Simple Cron

The PM agent runs on a straightforward cron schedule. No complex trigger logic.

```
┌─────────────────────────────────────────────────────┐
│  Scheduler (existing scheduler_lock.go)             │
│                                                     │
│  Every N hours (configurable per org, default: 24h): │
│    For each org with active integrations:            │
│      Enqueue "pm_analyze" job                       │
│                                                     │
│  Also runs on-demand via:                           │
│    POST /api/v1/pm/analyze (admin-only)             │
│                                                     │
└─────────────────────────────────────────────────────┘
```

**Why a simple cron instead of threshold-based triggers:**
- Easier to reason about and debug ("the PM runs at 8am, 12pm, 4pm, 8pm")
- Batching over hours gives the PM agent more data to reason about (patterns emerge from clusters, not single tickets)
- Avoids the complexity of counter-based triggers, race conditions, and "how often is too often" tuning
- Admins can always trigger manually when something urgent comes in

**Org setting:** `pm_schedule_hours` (default: `24`). The scheduler checks `last_pm_run_at + pm_schedule_hours < now()` for each org.

### 2. The PM Agent

The PM agent **runs in a sandbox with the full GitHub repo cloned**, just like a coding agent — but instead of writing code, it reads the codebase, analyzes issues in context, and produces a prioritized work plan. This is the critical difference from a blind LLM call: the PM can browse files, read architecture docs, trace stack traces to actual code, and give approach hints grounded in real code.

The PM agent uses the existing `AgentAdapter` and `SandboxProvider` infrastructure. It's a new adapter type (`pm_agent`) alongside `claude_code` and `codex`, but its prompt instructs it to analyze and plan rather than write diffs.

#### Why sandbox execution, not a single LLM call

A single LLM call would see only issue metadata and truncated descriptions. The PM would have to guess about code structure, file locations, and architectural context. With sandbox access:

- **Approach hints are grounded.** The PM can `cat handlers/payment.go` and see the actual nil pointer at line 142 before telling a coding agent to fix it.
- **Complexity estimates are real.** The PM can see how many files a change touches, whether there are tests, and how tangled the dependency graph is — not just guess from a bug title.
- **Clustering is smarter.** The PM can trace two different Sentry errors to the same function and confirm they share a root cause by reading the code.
- **Architecture awareness.** The PM reads CLAUDE.md, AGENTS.md, directory-level README files, and understands the codebase layout before making decisions.

#### New Service: `internal/services/pm/service.go`

```go
package pm

// Service is the AI Product Manager. It spins up a sandbox with the repo
// cloned, runs an agent that analyzes issues against the actual codebase,
// and produces a work plan that gets delegated to coding agents.
type Service struct {
    issues       issueStore         // fetch open/triaged issues
    agentRuns    agentRunStore      // fetch in-flight and recent runs
    pullRequests prStore            // fetch recent PR outcomes
    orgs         orgStore           // fetch org settings + product direction
    repos        repoStore          // fetch repo URL, branch, credentials
    jobs         jobStore           // enqueue run_agent jobs
    plans        planStore          // persist PM plans
    decisionLog  decisionLogStore   // persist + query PM decision history
    sandbox      agent.SandboxProvider  // reuse existing sandbox infrastructure
    adapter      agent.AgentAdapter     // PM agent adapter (claude_code or codex in PM mode)
    logger       zerolog.Logger
}

// Analyze is the main entry point. It:
//  1. Gathers full context (issues, outcomes, previous decisions)
//  2. Spins up a sandbox, clones the repo
//  3. Writes ProductContext into AGENTS.md in the sandbox
//  4. Writes issue context + decision log to .pm-context.json
//  5. Runs the PM agent (explores codebase, reads AGENTS.md, produces plan)
//  6. Parses the structured plan from the agent's output
//  7. Persists the plan + writes decisions to the decision log
//  8. Delegates work items to coding agents
func (s *Service) Analyze(ctx context.Context, orgID uuid.UUID, trigger string) (*Plan, error) {
    // Step 1: Gather issue/outcome context from DB
    pmCtx, err := s.gatherContext(ctx, orgID)
    if err != nil {
        return nil, fmt.Errorf("gather context: %w", err)
    }

    // Step 2: For each repo in the org, run the PM agent in a sandbox
    // (For orgs with multiple repos, run once per repo or once with the primary repo)
    repo, err := s.repos.GetPrimaryByOrg(ctx, orgID)
    if err != nil {
        return nil, fmt.Errorf("get primary repo: %w", err)
    }

    // Step 3: Create sandbox and clone repo
    sbCfg := agent.DefaultSandboxConfig()
    sbCfg.Timeout = 10 * time.Minute // PM needs more time to explore
    sb, err := s.sandbox.Create(ctx, sbCfg)
    if err != nil {
        return nil, fmt.Errorf("create sandbox: %w", err)
    }
    defer s.sandbox.Destroy(ctx, sb)

    if err := s.sandbox.CloneRepo(ctx, sb, repo.URL, repo.DefaultBranch, repo.Token); err != nil {
        return nil, fmt.Errorf("clone repo: %w", err)
    }

    // Step 4: Write ProductContext into AGENTS.md in the sandbox.
    // The PM agent reads AGENTS.md naturally during codebase exploration.
    if pmCtx.productContext != nil {
        if err := s.writeProductContextToAgentsMD(ctx, sb, pmCtx.productContext); err != nil {
            s.logger.Warn().Err(err).Msg("failed to write product context to AGENTS.md")
        }
    }

    // Step 5: Write the issue context + decision log as a file in the sandbox
    contextJSON, _ := json.Marshal(pmCtx)
    if err := s.sandbox.WriteFile(ctx, sb, "/workspace/.pm-context.json", contextJSON); err != nil {
        return nil, fmt.Errorf("write context: %w", err)
    }

    // Step 6: Run the PM agent
    prompt := s.preparePMPrompt(pmCtx)
    logCh := make(chan agent.LogEntry, 100)
    go func() { for range logCh {} }() // drain logs (or store them)
    result, err := s.adapter.Execute(ctx, sb, prompt, logCh)
    if err != nil {
        return nil, fmt.Errorf("pm agent execution: %w", err)
    }

    // Step 7: Parse the plan from the agent's output
    plan, err := parsePlan(result.Summary)
    if err != nil {
        return nil, fmt.Errorf("parse plan: %w", err)
    }

    // Step 8: Persist plan
    plan.OrgID = orgID
    plan.TriggeredBy = trigger
    plan.TokenUsage, _ = json.Marshal(result.TokenUsage)
    if err := s.plans.Create(ctx, plan); err != nil {
        return nil, fmt.Errorf("persist plan: %w", err)
    }

    // Step 9: Write decisions to the decision log (institutional memory)
    entries := planToDecisionLog(plan)
    for _, entry := range entries {
        entry.OrgID = orgID
        if err := s.decisionLog.Create(ctx, &entry); err != nil {
            s.logger.Error().Err(err).Msg("failed to write decision log entry")
        }
    }

    // Step 10: Delegate to coding agents
    if err := s.executePlan(ctx, orgID, plan); err != nil {
        return nil, fmt.Errorf("execute plan: %w", err)
    }

    return plan, nil
}
```

#### PM Agent Adapter

The PM agent reuses the existing `AgentAdapter` interface. A new adapter (or a mode flag on the existing Claude Code adapter) configures the agent for analysis instead of code writing:

```go
// PMAdapter wraps an existing AgentAdapter (e.g., Claude Code) and overrides
// PreparePrompt to use the PM system prompt instead of the coding prompt.
// It reuses the same Execute flow — the agent runs in a sandbox with full
// CLI tool access (file reading, grep, etc.) but is instructed not to write code.
type PMAdapter struct {
    inner agent.AgentAdapter // the underlying claude_code or codex adapter
}

func (a *PMAdapter) Name() string { return "pm_agent" }

func (a *PMAdapter) PreparePrompt(ctx context.Context, input *agent.AgentInput) (*agent.AgentPrompt, error) {
    // Use the PM-specific system prompt (see LLM Prompt section below)
    // instead of the coding agent's fix-the-bug prompt
    return &agent.AgentPrompt{
        SystemPrompt: pmSystemPrompt,
        UserPrompt:   input.PMContext.ContextJSON, // serialized PMContext
        MaxTokens:    32000, // PM produces a structured plan, not a diff
    }, nil
}

func (a *PMAdapter) Execute(ctx context.Context, sandbox *agent.Sandbox, prompt *agent.AgentPrompt, logCh chan<- agent.LogEntry) (*agent.AgentResult, error) {
    // Delegate to the underlying adapter's Execute — same sandbox, same CLI tools
    return a.inner.Execute(ctx, sandbox, prompt, logCh)
}
```

#### Sandbox Configuration for PM

The PM agent uses a lighter sandbox config than coding agents:

```go
func pmSandboxConfig() agent.SandboxConfig {
    cfg := agent.DefaultSandboxConfig()
    cfg.Timeout = 10 * time.Minute  // more time to explore the codebase
    cfg.CPULimit = 1                // doesn't need heavy compute — mostly reading
    cfg.MemoryLimitMB = 2048       // less memory than coding agents
    cfg.NetworkPolicy = "restricted" // same restricted network as coding agents
    return cfg
}
```

The PM agent has **read-only** intent — it explores the codebase but doesn't modify it. The sandbox is destroyed after the plan is produced. (The agent CLI technically has write access inside the sandbox, but the PM prompt instructs it to only read. Any files it creates are discarded when the sandbox is destroyed.)

#### Context Gathering

The PM agent sees everything relevant to making decisions:

```go
// PMContext is the full picture the PM agent reasons over.
// Kept deliberately lean — the PM agent has codebase access and can
// derive patterns itself. We provide raw data, not pre-computed derivatives.
type PMContext struct {
    // What needs attention
    OpenIssues       []IssueSummary    // all issues with status "open" or "triaged"

    // What's already happening
    InFlightRuns     []RunSummary      // agent runs in "pending" or "running" status

    // What happened recently (learning from outcomes)
    RecentOutcomes   []OutcomeSummary  // last ~20 completed runs: success/failure + reasoning
    RecentPRs        []PRSummary       // last ~20 PRs: merged? rejected? reverted?

    // Institutional memory: what the PM decided in previous cycles
    // (see "Decision Log" section below)
    PreviousDecisions []DecisionLogEntry // last ~50 entries with outcomes backfilled

    // Strategic context is NOT included here — it's written into AGENTS.md
    // in the sandbox so the PM discovers it naturally (see ProductContext section)

    // System constraints
    MaxConcurrentRuns int
    CurrentRunCount   int              // how many slots are available right now
}

// IssueSummary is a compact representation of an issue for the PM prompt.
type IssueSummary struct {
    ID                    string    `json:"id"`
    Source                string    `json:"source"`     // "sentry" or "linear"
    Title                 string    `json:"title"`
    Description           string    `json:"description"` // truncated to 200 chars
    Severity              string    `json:"severity"`
    OccurrenceCount       int       `json:"occurrences"`
    AffectedCustomerCount int       `json:"affected_customers"`
    FirstSeenAt           string    `json:"first_seen"`
    LastSeenAt            string    `json:"last_seen"`
    Tags                  []string  `json:"tags,omitempty"`
    HasStackTrace         bool      `json:"has_stack_trace"`
}
```

#### LLM Prompt

The PM agent prompt frames it as a product manager running a planning session:

```go
const pmSystemPrompt = `You are an AI Product Manager running a planning session for an autonomous
software engineering team. Your team consists of AI coding agents that can fix
bugs and implement small features.

You have FULL ACCESS to the codebase — it is cloned into /workspace. Use it.
Browse files, read code, trace stack traces to actual source lines, check for
tests, understand the architecture. Your approach hints should be grounded in
real code, not guesses.

Your job is to:
  1. Understand the codebase architecture and current state
  2. Analyze all incoming work (Sentry errors, Linear tickets)
  3. Decide what should be worked on next, and in what order
  4. Give each coding agent a specific, grounded briefing
  5. Skip or defer work that shouldn't be auto-fixed, and explain why

## Your Workflow

1. START by reading AGENTS.md in /workspace. This contains the codebase
   instructions AND your Product Context (philosophy, direction, focus areas,
   avoid areas). This is your most important input — internalize it before
   reading anything else.

2. READ the issue context from /workspace/.pm-context.json. This contains:
   - All open issues (Sentry errors, Linear tickets)
   - In-flight agent runs (what's already being worked on)
   - Recent outcomes (what succeeded, what failed, and why)
   - Your PREVIOUS DECISIONS from past cycles (with outcomes).
     Review these carefully — maintain consistency with past decisions,
     learn from failures, and don't re-evaluate issues you already skipped
     unless circumstances have changed.

3. EXPLORE THE CODEBASE. Before analyzing issues:
   - Read CLAUDE.md and README.md for additional architecture context
   - Understand the directory structure (ls the top-level dirs)
   - Note the test infrastructure (are there tests? what framework?)
   - Check recent git commits (git log --oneline -20) to understand recent changes

3. FOR EACH ISSUE you're considering, investigate in the codebase:
   - If it has a stack trace, go read the actual code at those locations
   - Trace the call chain to understand the root cause
   - Check if there are existing tests that cover this path
   - Look for related code that might have the same bug pattern
   - Assess how many files a fix would touch

4. CLUSTER related issues after investigating. Now that you've read the actual
   code, you can confirm (not just guess) whether issues share a root cause.

5. PRODUCE YOUR PLAN with approach hints that reference real files, real
   functions, and real code patterns you observed. The coding agent will get
   your approach as part of its prompt — make it specific enough to be useful.

## Product Context (in AGENTS.md)

You'll find a "Product Context" section in the AGENTS.md file you read in
step 1. This is the team's philosophy, strategic direction, focus areas,
and avoid areas. It's your most important input — it tells you how to think,
not just what to look at.

- **Philosophy** tells you the team's values (simplicity vs. configurability,
  speed vs. correctness, etc.). It may include preferences on fix style,
  risk appetite, and code quality standards. Let it guide your approach hints.
  If the philosophy says "prefer deleting code over adding abstractions",
  don't recommend adding a new helper function.
- **Direction** tells you what matters THIS quarter. Align your priorities.

## Previous Decisions (in .pm-context.json)

The "previous_decisions" field in the context file contains your decisions
from past cycles, with outcomes filled in where available. Use this to:

- **Maintain consistency.** If you skipped an issue last cycle, don't suddenly
  delegate it unless something changed (new occurrences, severity increase,
  direction shift).
- **Learn from failures.** If you delegated an issue with "high" confidence
  and it failed, adjust your approach or confidence for similar issues.
- **Avoid redundant work.** If an issue was already delegated and is still
  in-flight, don't delegate it again.
- **Build on observations.** Your past "observe" entries (e.g., "billing
  module has no tests") should inform current approach hints and confidence
  levels.
- **Focus areas** are where the team wants coding agents active. Prioritize
  issues in these areas. Deprioritize issues outside them unless they're
  critical.
- **Avoid areas** are off-limits. Skip issues in these areas with a clear
  reason, even if they have high severity.

If product context is sparse or missing, fall back to data-driven defaults:
prioritize by customer impact and severity, prefer high-confidence tasks.

## Decision Framework

Think like a senior PM running a sprint planning meeting, but one who has
actually read the code:

- PRIORITIZE by real impact, filtered through the product context. Consider:
  - How many customers are affected (and how badly)
  - Whether it aligns with the current direction and focus areas
  - Whether it's getting worse (check the trending_issues data)
  - Whether the coding agent can realistically fix it — you've seen the code,
    so you can assess complexity directly, not just from the bug title
  - Dependencies (does fixing X unblock Y?)

- GIVE GROUNDED APPROACH HINTS. You've read the code. Tell the coding agent
  exactly where the problem is. Example:
  "The nil pointer is at handlers/payment.go:142 — the PaymentMethod field on
  the User struct can be nil when the user hasn't set up billing yet. The fix
  is a nil check before accessing user.PaymentMethod.ID. There's an existing
  test in handlers/payment_test.go that covers the happy path but doesn't test
  the nil case — add a test case for a user without a payment method."

- LEARN FROM HISTORY. You'll see recent outcomes (successes, failures, PR
  rejections). Use them:
  - If the agent failed on a similar issue recently, lower your confidence
  - If PRs from the agent keep getting rejected for the same reason, adjust
    your approach hints to address that pattern

- SKIP things that shouldn't be auto-fixed:
  - Issues in avoid_areas
  - Issues that need a human product decision
  - Duplicates of in-flight work
  - Issues too complex for the agent (based on failure patterns and your
    assessment of the code)
  - Issues misaligned with product direction

- RESPECT CONSTRAINTS. You have {available_slots} available agent slots
  (out of {max_concurrent} total). Don't plan more work than you have capacity for.

## IMPORTANT: Do NOT modify any code

You are analyzing and planning only. Do not write code, create branches, or
modify files. Your output is a structured plan, not a diff.

## Output Format

When you have completed your analysis, output your plan as a JSON object
between <pm-plan> tags:

<pm-plan>
{
  "analysis": "<2-3 paragraph situation analysis: what you found in the codebase, what patterns you see, what's urgent>",
  "tasks": [
    {
      "rank": 1,
      "issue_ids": ["<uuid>", ...],
      "title": "<your summary of the work item>",
      "reasoning": "<why this is priority #1>",
      "approach": "<specific, code-grounded guidance: exact files, functions, line numbers, what to change, what tests to add>",
      "risk": "<what could go wrong, based on what you saw in the code>",
      "complexity": "<trivial|simple|moderate|complex>",
      "confidence": "<high|medium|low — can the agent handle this?>"
    }
  ],
  "clusters": [
    {
      "issue_ids": ["<uuid>", ...],
      "root_cause": "<confirmed root cause based on your code investigation>",
      "strategy": "<fix root cause in issue X, others will resolve>"
    }
  ],
  "skip": [
    {
      "issue_id": "<uuid>",
      "reason": "<duplicate|needs_human_decision|too_complex|misaligned|in_avoid_area|already_in_flight>",
      "detail": "<explanation>"
    }
  ]
}
</pm-plan>`
```

The PM context (issues, outcomes, product context, auto-derived signals) is written to `/workspace/.pm-context.json` for the agent to read. The agent then explores the codebase using its CLI tools (file reading, grep, git log, etc.) before producing its plan.

#### Plan Output Types

```go
// Plan is the PM agent's output — a prioritized, reasoned work plan.
type Plan struct {
    ID            uuid.UUID       `json:"id" db:"id"`
    OrgID         uuid.UUID       `json:"org_id" db:"org_id"`
    Status        string          `json:"status" db:"status"` // "executing", "completed", "failed"
    Analysis      string          `json:"analysis" db:"analysis"`
    Tasks         []Task          `json:"tasks"`
    Clusters      []Cluster       `json:"clusters"`
    SkippedIssues []SkipEntry     `json:"skipped_issues"`
    IssuesReviewed int            `json:"issues_reviewed" db:"issues_reviewed"`
    TokenUsage    json.RawMessage `json:"token_usage,omitempty" db:"token_usage"`
    TriggeredBy   string          `json:"triggered_by" db:"triggered_by"` // "cron" or "manual"
    CreatedAt     time.Time       `json:"created_at" db:"created_at"`
    CompletedAt   *time.Time      `json:"completed_at,omitempty" db:"completed_at"`
}

// Task is a single work item the PM agent wants a coding agent to tackle.
type Task struct {
    Rank       int         `json:"rank"`
    IssueIDs   []uuid.UUID `json:"issue_ids"`   // may be multiple if clustered
    Title      string      `json:"title"`        // PM's summary
    Reasoning  string      `json:"reasoning"`    // why it's at this rank
    Approach   string      `json:"approach"`     // guidance for the coding agent
    Risk       string      `json:"risk"`
    Complexity string      `json:"complexity"`   // trivial, simple, moderate, complex
    Confidence string      `json:"confidence"`   // high, medium, low

    // Set during execution
    AgentRunID *uuid.UUID  `json:"agent_run_id,omitempty"` // linked once delegated
    Status     string      `json:"status"`                 // "pending", "delegated", "skipped_capacity"
}

// Cluster groups related issues the PM agent identified as sharing a root cause.
type Cluster struct {
    IssueIDs  []uuid.UUID `json:"issue_ids"`
    RootCause string      `json:"root_cause"`
    Strategy  string      `json:"strategy"`
}

// SkipEntry is an issue the PM agent recommends not working on, with reasoning.
type SkipEntry struct {
    IssueID uuid.UUID `json:"issue_id"`
    Reason  string    `json:"reason"`
    Detail  string    `json:"detail"`
}
```

### 3. Delegation: PM Plan → Coding Agent Runs

After the PM agent produces a plan, the service executes it by creating coding agent runs:

```go
// executePlan takes the PM's task list and delegates to coding agents.
func (s *Service) executePlan(ctx context.Context, orgID uuid.UUID, plan *Plan) error {
    org, _ := s.orgs.GetByID(ctx, orgID)
    settings := models.ParseOrgSettings(org.Settings)

    maxConcurrent := settings.MaxConcurrentRuns
    if maxConcurrent <= 0 {
        maxConcurrent = 3
    }

    running, _ := s.agentRuns.CountRunningByOrg(ctx, orgID)
    available := maxConcurrent - running

    delegated := 0
    for i := range plan.Tasks {
        task := &plan.Tasks[i]

        if delegated >= available {
            task.Status = "skipped_capacity"
            continue
        }

        // Skip low-confidence tasks unless autonomy is "auto_all"
        if task.Confidence == "low" && settings.AutonomyLevel != "auto_all" {
            task.Status = "skipped_capacity"
            continue
        }

        // Create the agent run with PM context injected
        primaryIssueID := task.IssueIDs[0]
        run := &models.AgentRun{
            IssueID:       primaryIssueID,
            OrgID:         orgID,
            AgentType:     settings.DefaultAgentType,
            Status:        "pending",
            AutonomyLevel: settings.AutonomyLevel,
            TokenMode:     tokenModeFromComplexity(task.Complexity),
            PMPlanID:      &plan.ID,
            PMApproach:    &task.Approach,
            PMReasoning:   &task.Reasoning,
        }
        if err := s.agentRuns.Create(ctx, run); err != nil {
            s.logger.Error().Err(err).Msg("failed to create agent run from PM plan")
            continue
        }

        // Mark issues as triaged
        for _, issueID := range task.IssueIDs {
            _ = s.issues.UpdateStatus(ctx, orgID, issueID, "triaged")
        }

        // Enqueue the coding agent job
        s.enqueueRunAgent(ctx, orgID, run.ID)
        task.AgentRunID = &run.ID
        task.Status = "delegated"
        delegated++
    }

    // Update the plan with delegation results
    return s.plans.Update(ctx, plan)
}
```

### 4. Injecting PM Context into Coding Agents

When the orchestrator runs a coding agent, it checks if the run has PM context and injects it into the prompt.

#### Change to `AgentInput` (`internal/services/agent/adapter.go`)

```go
type AgentInput struct {
    Issue              *models.Issue
    RepoURL            string
    RepoBranch         string
    OrgSettings        json.RawMessage
    TokenMode          string
    ComplexityEstimate *ComplexityEstimate
    ContextDocs        []string
    RevisionContext    *RevisionContext
    // NEW: PM agent's guidance for this specific task
    PMContext          *PMTaskContext
}

// PMTaskContext carries the PM agent's analysis into the coding agent's prompt.
type PMTaskContext struct {
    Approach       string      // "The stack trace points to handlers/payment.go:142..."
    Risk           string      // "Be careful not to change the payment flow logic"
    Reasoning      string      // "This is P1 because it affects 2000 customers/day"
    RelatedIssues  []string    // titles of other issues in the same cluster
    RootCause      string      // "All 3 errors stem from a missing nil check on..."
}
```

The Claude Code adapter's `PreparePrompt()` injects this as an additional section when present:

```
## Product Manager Analysis

The PM agent has analyzed this issue and recommends the following approach:

**Why this is a priority:** {reasoning}

**Suggested approach:** {approach}

**Risk to watch for:** {risk}

**Related issues (same root cause):**
{related_issues}

**Root cause hypothesis:** {root_cause}
```

### 5. Changes to Existing Components

#### Ingestion Service (`internal/services/ingestion/service.go`)

**Minimal change.** Ingestion still enqueues `prioritize` jobs for scoring/display (the dashboard badges still work). The only difference: `prioritize` no longer calls `CheckAutoTrigger()`.

```go
// In handlers.go — the prioritize handler becomes score-only:
func newPrioritizeHandler(...) JobHandler {
    return func(ctx context.Context, jobType string, payload json.RawMessage) error {
        // ... parse payload ...
        score, err := services.Prioritization.ComputeScore(ctx, orgID, issueID)
        // ... estimate complexity ...
        // REMOVED: services.Prioritization.CheckAutoTrigger(...)
        // The PM agent now owns the decision to start coding runs.
        return nil
    }
}
```

#### Prioritization Service (`internal/services/prioritization/service.go`)

`CheckAutoTrigger()` is kept but **only called from manual triggers** (admin clicks "Fix This" on a specific issue). The automated path goes through the PM agent.

#### Scheduler (`internal/cluster/scheduler_lock.go`)

Add a new scheduled task alongside the existing ones:

```go
// In the scheduler's tick loop, add:
case "pm_analyze":
    // Every pm_schedule_hours, enqueue a PM analysis job for each org.
    orgs := s.listOrgsWithActiveIntegrations(ctx)
    for _, orgID := range orgs {
        dedupeKey := fmt.Sprintf("pm_analyze:%s", orgID.String())
        s.jobs.Enqueue(ctx, orgID, "default", "pm_analyze", map[string]string{
            "org_id":  orgID.String(),
            "trigger": "cron",
        }, 5, &dedupeKey)
    }
```

#### Worker Handlers (`internal/worker/handlers.go`)

Add a new handler:

```go
w.Register("pm_analyze", newPMAnalyzeHandler(stores, services, logger))
```

#### Agent Run Model (`internal/models/models.go`)

Add PM-linkage fields to `AgentRun`:

```go
type AgentRun struct {
    // ... existing fields ...

    // PM agent context (set when the run was created by the PM agent)
    PMPlanID    *uuid.UUID `json:"pm_plan_id,omitempty" db:"pm_plan_id"`
    PMApproach  *string    `json:"pm_approach,omitempty" db:"pm_approach"`
    PMReasoning *string    `json:"pm_reasoning,omitempty" db:"pm_reasoning"`
}
```

### 6. Database Changes

#### New Table: `pm_plans`

```sql
CREATE TABLE pm_plans (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                   UUID NOT NULL REFERENCES organizations(id),
    status                   TEXT NOT NULL DEFAULT 'executing',   -- executing, completed, failed
    analysis                 TEXT,                                -- PM's situation analysis
    tasks                    JSONB NOT NULL DEFAULT '[]',         -- ordered task list
    clusters                 JSONB NOT NULL DEFAULT '[]',         -- issue clusters
    skipped_issues           JSONB NOT NULL DEFAULT '[]',         -- skip list
    issues_reviewed          INT NOT NULL DEFAULT 0,
    product_context_snapshot JSONB,                               -- snapshot of ProductContext at plan time
    token_usage              JSONB,
    triggered_by             TEXT NOT NULL DEFAULT 'cron',        -- "cron" or "manual"
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at             TIMESTAMPTZ
);

CREATE INDEX idx_pm_plans_org_created ON pm_plans(org_id, created_at DESC);
```

The `product_context_snapshot` column captures the `ProductContext` that was active when the plan was generated. This makes plans self-contained and auditable — you can always see what product context led to a given set of decisions, even if the admin changes direction later.

#### New Table: `pm_decision_log`

See the "Decision Log: Institutional Memory" section below for the full table definition and usage. The migration creates both `pm_plans` and `pm_decision_log` together.

#### Alter Table: `agent_runs`

```sql
ALTER TABLE agent_runs
    ADD COLUMN pm_plan_id  UUID REFERENCES pm_plans(id),
    ADD COLUMN pm_approach TEXT,
    ADD COLUMN pm_reasoning TEXT;
```

### 7. API Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/api/v1/pm/analyze` | admin | Manually trigger a PM analysis run |
| `GET` | `/api/v1/pm/plans` | viewer+ | List PM plans (paginated, newest first) |
| `GET` | `/api/v1/pm/plans/{id}` | viewer+ | Get a specific plan with tasks, clusters, skips |
| `GET` | `/api/v1/pm/plans/latest` | viewer+ | Get the most recent plan |

Existing endpoints are unchanged. The `GET /api/v1/runs/{id}` response gains the `pm_approach` and `pm_reasoning` fields.

### 8. Org Settings

```go
// New fields in OrgSettings:
PMScheduleHours  int              `json:"pm_schedule_hours"`  // hours between PM runs (default: 24)
PMModel          string           `json:"pm_model"`           // LLM model for PM agent (default: "sonnet")
ProductContext   *ProductContext  `json:"product_context,omitempty"` // replaces product_direction string
```

The existing `ProductDirection string` field is kept for backward compatibility but deprecated. `ParseOrgSettings()` migrates it into `ProductContext.Direction` when `ProductContext` is nil.

### 9. Frontend

#### New: PM Plan View (`/pm` or `/plans`)

A page showing the PM agent's latest analysis and work plan:

- **Analysis section** — the PM's 2-3 paragraph situation summary
- **Task list** — ordered cards showing rank, title, reasoning, and approach
  - Each card shows which coding agent run it spawned (linked)
  - Status: delegated / skipped (capacity) / completed / failed
- **Clusters section** — visual grouping of related issues with root cause
- **Skip list** — collapsible section showing what was skipped and why
- **History** — previous plans for comparison

#### Modified: Runs Page

Each agent run card shows a "PM Context" expandable section if `pm_plan_id` is set, showing the PM's reasoning and approach guidance.

#### Modified: Settings Page

Add PM configuration section: schedule interval, model selection.

## OpenClaw-Style Autonomy Enhancements (Recommended)

To behave like an **OpenClaw-style autonomous PM**, the agent should run as a
continuous planning loop with memory, adaptive scheduling, and self-correction.
These additions build on the base design without requiring new infrastructure.

### A. Persistent PM State (Memory + Loop Control)

Add a small `pm_state` table keyed by `(org_id, repo_id)`:

- `last_plan_id`, `last_run_at`, `next_run_at`
- `backlog_summary` (short natural-language memory)
- `recent_outcome_summary` (last N plan outcomes)
- `last_error` / `last_error_at` (for health + retry decisions)

This gives the PM a **stable memory anchor** and allows the system to run
autonomously without requiring admins to re-trigger analysis.

### B. Adaptive Scheduling (Cron + Wakeups)

Keep the base cron, but add **wakeups** for critical events:

- new Sev-1 Sentry spikes
- regression outcomes
- Linear tickets tagged “blocking”

Implementation: enqueue `pm_analyze` with a dedupe key, but **do not** bypass
the `pm_state.next_run_at` guard unless severity is critical. This prevents
thrashing while still enabling fast response.

### C. Carryover + Replan Stability

Plans should be **iterative** instead of re-solving the world each cycle:

- Carry over unfinished tasks from the previous plan.
- Only re-evaluate issues whose “fingerprint” changed (e.g. severity, last_seen,
  occurrence_count).
- Persist `carryover_from_plan_id` and `replan_reason` on the new plan.

This prevents flip-flopping and encourages consistent behavior across cycles.

### D. Guardrails that Reuse Existing Org Settings

The PM should reuse the current settings model instead of inventing new knobs:

- `autonomy_level` gates delegation vs. “plan-only”.
- `execution_aggressiveness` caps delegated complexity tiers.
- `max_concurrent_runs` caps the number of delegated tasks per plan.

Add PM-specific **budgets** (soft caps) rather than new gate logic:
`pm_max_tasks`, `pm_max_tokens`, and `pm_max_issue_count`.

### E. Use Existing Scores as Inputs (Not Replacements)

The current prioritization score + complexity estimate should be **inputs** to
the PM, not replaced. This allows a smooth migration and avoids wasted LLM
tokens recalculating the same data.

### F. Outcome-Driven Self-Correction

Pull existing data into the PM context to improve decision quality:

- `agent_runs` failure categories + explanations
- validation results
- review feedback (reject / rework patterns)

This should update the PM’s “confidence” and influence what it delegates next.

### G. Multi-Repo Awareness

Store `repo_id` on `pm_plans` + `pm_decision_log`, and run the PM per repo.
The PM can still cluster issues across repos, but **delegation stays per-repo**
to avoid invalid cross-repo assumptions.

### H. Operator Visibility + Overrides

Add UI controls to “approve plan”, “skip task”, or “retry as manual”, and log
overrides in the decision log. Autonomy should be **observable and interruptible**.

## Alignment With Current Implementation

To transition cleanly from the current system:

1. **Keep existing prioritize jobs** for scoring/complexity.
2. **Stop auto-triggering** in `prioritize` once PM delegation is enabled.
3. **Add `pm_analyze` as a job type** in the worker and enqueue via cron or
   an external scheduler hitting `/api/v1/pm/analyze`.
4. **Continue using `product_direction`** until `ProductContext` is added.
5. **Use the existing job queue** for PM delegation (`run_agent`) so no new
   execution pipeline is required.

## File Changes Summary

### New Files

| File | Description |
|------|-------------|
| `internal/services/pm/service.go` | PM agent service: context gathering, sandbox execution, plan parsing, delegation |
| `internal/services/pm/service_test.go` | Tests |
| `internal/services/pm/adapter.go` | PMAdapter: wraps existing AgentAdapter for PM-mode execution |
| `internal/services/pm/context.go` | Context assembly (queries issues, runs, PRs, settings) |
| `internal/services/pm/prompt.go` | System/user prompt construction |
| `internal/services/pm/types.go` | Plan, Task, Cluster, SkipEntry, DecisionLogEntry types |
| `internal/services/pm/decision_log.go` | Decision log extraction from plans, outcome backfill |
| `internal/db/pm_plans.go` | DB store for pm_plans table |
| `internal/db/pm_decision_log.go` | DB store for pm_decision_log table |
| `internal/db/pm_plans_test.go` | Store tests |
| `internal/db/pm_decision_log_test.go` | Store tests |
| `internal/api/handlers/pm.go` | API handlers for PM endpoints |
| `migrations/000004_pm_agent.up.sql` | New tables (pm_plans, pm_decision_log) + agent_runs columns |
| `migrations/000004_pm_agent.down.sql` | Rollback |
| `frontend/src/app/(dashboard)/plans/page.tsx` | PM plan view page |
| `frontend/src/components/pm/plan-view.tsx` | Plan view component |
| `frontend/src/components/pm/task-card.tsx` | Task card component |

### Modified Files

| File | Change |
|------|--------|
| `internal/worker/handlers.go` | Add `pm_analyze` handler; remove `CheckAutoTrigger` call from `prioritize` handler |
| `internal/services/agent/adapter.go` | Add `PMTaskContext` type to `AgentInput` |
| `internal/services/agent/adapters/claude_code.go` | Inject PM context into prompts |
| `internal/services/agent/orchestrator.go` | Read `PMApproach`/`PMReasoning` from run, populate `AgentInput.PMContext` |
| `internal/models/models.go` | Add `PMPlanID`, `PMApproach`, `PMReasoning` fields to `AgentRun` |
| `internal/cluster/scheduler_lock.go` | Add `pm_analyze` cron task |
| `internal/api/router.go` | Add PM routes |
| `internal/models/org_settings.go` | Add `ProductContext` struct, `PMScheduleHours`, `PMModel` settings; migration from `ProductDirection` |
| `cmd/server/main.go` | Wire up `pm.Service` |

## PM Agent Quality: What Makes It Perform Well

The PM agent is only as good as its inputs. This section addresses the key risks to PM quality and how to mitigate them.

### Risk 1: PM agent explores too much / runs too long

The PM has full repo access and could spend its entire 10-minute timeout reading every file instead of producing a plan. Unlike a coding agent that focuses on a single issue, the PM is analyzing potentially dozens of issues across the whole codebase.

**Mitigation:**
- The PM prompt's workflow section gives it a structured sequence: read architecture docs first, then investigate issues in priority order, then produce the plan.
- The sandbox timeout (10 minutes) acts as a hard cap. The prompt instructs the PM to produce a partial plan if it runs out of time: "If you're running low on time, produce a plan for the issues you've investigated so far. A partial plan is better than no plan."
- For Sentry issues with stack traces, the PM should go directly to the referenced code. For Linear tickets without stack traces, the PM uses grep/search to find relevant code — but should limit exploration to 2-3 minutes per issue.
- Token mode is set to "high" for the PM agent (it needs to explore and reason extensively).

### Risk 2: Stale plans

If the PM runs every 24 hours and a P0 Sentry error arrives at hour 0:05, it waits ~24 hours. Meanwhile, the admin sees it in the dashboard but there's no automated response.

**Mitigation:**
- The existing "Fix This" button on individual issues still works — it bypasses the PM and creates a direct agent run via `CheckAutoTrigger()`.
- The manual `POST /pm/analyze` endpoint lets admins trigger an immediate PM cycle.
- Consider adding a severity-based fast path in the future: if a `critical` severity issue arrives and no PM cycle is running, auto-trigger a mini PM cycle for just that issue. This is **not in v1** to keep complexity low.

### Risk 3: Context window overflow

With 100+ open issues, the PM context can get large. However, since the PM runs as a full agent with file access, the context is written to `/workspace/.pm-context.json` rather than being crammed into the system prompt. The PM agent reads this file and can selectively focus on subsets.

**Mitigation:**
- **File-based context.** The full issue context is written to a JSON file in the sandbox. The system prompt stays small (instructions only). The agent reads the file and can process it in chunks.
- **Two-tier summarization.** Top 30 issues by numeric score get full `IssueSummary` (200 chars). Remaining issues get a one-line summary (title + severity + occurrence count only, ~50 chars each).
- **Recency windowing.** Only include outcomes/failures/PRs from the last 30 days. Older history is irrelevant to current planning.
- **For very large orgs (500+ open issues)**, split into per-repo PM cycles — one sandbox per repo with only that repo's issues.

### Risk 4: Cold start (no historical outcomes)

On day one, the PM has no completed runs, no failure patterns, no PR history. Its plans will be based purely on issue metadata + product context.

**Mitigation:**
- This is acceptable. On day one, the PM is essentially doing what the numeric formula does (prioritize by severity × customer impact), but with the added ability to cluster related issues and skip obviously misaligned ones.
- The PM prompt includes: "If you have no historical outcome data, that's fine. Prioritize by customer impact and severity. Set confidence to 'medium' for all tasks until you have outcome data to calibrate against."
- Quality improves rapidly: after ~10 agent runs (a few PM cycles), the PM has enough outcome data to start learning what works.

### Risk 5: PM plans that are all skips

If the product context is too restrictive (narrow focus areas, many avoid areas, low risk tolerance), the PM might skip everything and delegate nothing.

**Mitigation:**
- The PM prompt includes: "You must delegate at least 1 task per cycle if there are any open issues, even if confidence is medium. If all issues are in avoid areas or too complex, say so in your analysis and flag it for admin attention — but still recommend the single safest option."
- The `Plan` tracks `issues_reviewed` vs. tasks delegated. If the ratio is consistently poor (>90% skip rate), surface a warning in the UI: "The PM agent is skipping most issues. Consider adjusting your product context."

### Risk 6: Inconsistency between PM cycles

The PM is non-deterministic. Issue A might be ranked #1 in one cycle and #3 in the next, with no underlying change. This is confusing for admins.

**Mitigation:**
- The **decision log** (see below) gives the PM memory of its past decisions. The PM prompt instructs it to maintain consistency: "Review your previous decisions. Don't re-evaluate settled issues unless circumstances changed."
- Store the `product_context_snapshot` on each plan so admins can see the inputs were the same.
- In the frontend, show a plan diff: highlight issues that moved up, down, or were newly added/removed between consecutive plans.

## Plan Outcome Tracking (Feedback Loop)

For the PM to improve over time, it needs to learn from what happened to its previous plans. This is the flywheel.

### What to track

After each PM cycle completes (all delegated tasks reach a terminal state), compute:

```go
type PlanOutcome struct {
    PlanID           uuid.UUID
    TasksTotal       int                 // how many tasks were in the plan
    TasksDelegated   int                 // how many were delegated to agents
    TasksSucceeded   int                 // agent run completed + validation passed + PR merged
    TasksFailed      int                 // agent run failed or validation failed
    TasksPRRejected  int                 // PR created but closed/rejected by reviewer
    AvgConfidenceHit float64             // how often PM's confidence matched actual outcome
    SkipAccuracy     float64             // % of skipped issues that are still open (skip was correct)
}
```

### How to feed it back

The next PM cycle receives the last 5 `PlanOutcome` summaries in its context:

```
## Your Recent Track Record

Plan #42 (2 days ago): 3 tasks delegated, 2 succeeded, 1 failed (timeout on complex issue).
  Your confidence was "high" for the failure — you overestimated.

Plan #41 (3 days ago): 2 tasks delegated, 2 succeeded. You skipped issue X as "too complex"
  but an admin manually fixed it in 1 line — the skip was wrong.

Overall: 70% success rate on delegated tasks. Your "high confidence" predictions succeed 85%
of the time. Your "medium confidence" predictions succeed 40%.
```

This self-calibration loop is the main mechanism for PM improvement. It's not needed in v1 but should be added early in v2.

## Decision Log: Institutional Memory Across PM Cycles

Each PM agent run is ephemeral — the sandbox is destroyed after the plan is produced. Without a persistent record of past decisions, the next PM cycle starts from scratch and may re-evaluate the same issues, make contradictory decisions, or repeat mistakes. The **decision log** gives the PM agent institutional memory.

### What gets logged

Every PM cycle writes its key decisions to a `pm_decision_log` table. Each entry captures one decision the PM made about a specific issue or cluster:

```go
// DecisionLogEntry is a single decision the PM agent made during a planning cycle.
// These accumulate over time and form the PM's "institutional memory."
type DecisionLogEntry struct {
    ID         uuid.UUID  `json:"id" db:"id"`
    OrgID      uuid.UUID  `json:"org_id" db:"org_id"`
    PlanID     uuid.UUID  `json:"plan_id" db:"plan_id"`          // which PM cycle produced this
    IssueID    *uuid.UUID `json:"issue_id,omitempty" db:"issue_id"` // null for general observations
    Decision   string     `json:"decision" db:"decision"`        // "delegate", "skip", "cluster", "defer", "observe"
    Reasoning  string     `json:"reasoning" db:"reasoning"`      // PM's explanation (1-2 sentences)
    Outcome    *string    `json:"outcome,omitempty" db:"outcome"` // filled in later: "succeeded", "failed", "pr_rejected", "still_open"
    CreatedAt  time.Time  `json:"created_at" db:"created_at"`
}
```

Decision types:
- **`delegate`** — PM assigned this issue to a coding agent. Reasoning explains why it was prioritized.
- **`skip`** — PM decided not to work on this issue. Reasoning explains why (too complex, wrong area, needs human input, etc.).
- **`cluster`** — PM grouped this issue with others under a shared root cause. Reasoning identifies the root cause.
- **`defer`** — PM recognized the issue but deliberately postponed it. Reasoning explains what would need to change.
- **`observe`** — A general observation about the codebase or patterns that doesn't tie to a single issue (e.g., "the payments module has no tests — all payment issues are high risk until tests exist").

### How decisions are extracted from the plan

After `parsePlan()` returns a `Plan`, the service converts it into decision log entries:

```go
func planToDecisionLog(plan *Plan) []DecisionLogEntry {
    var entries []DecisionLogEntry

    for _, task := range plan.Tasks {
        for _, issueID := range task.IssueIDs {
            entries = append(entries, DecisionLogEntry{
                PlanID:    plan.ID,
                OrgID:     plan.OrgID,
                IssueID:   &issueID,
                Decision:  "delegate",
                Reasoning: task.Reasoning,
            })
        }
    }

    for _, skip := range plan.SkippedIssues {
        entries = append(entries, DecisionLogEntry{
            PlanID:    plan.ID,
            OrgID:     plan.OrgID,
            IssueID:   &skip.IssueID,
            Decision:  "skip",
            Reasoning: skip.Detail,
        })
    }

    for _, cluster := range plan.Clusters {
        for _, issueID := range cluster.IssueIDs {
            entries = append(entries, DecisionLogEntry{
                PlanID:    plan.ID,
                OrgID:     plan.OrgID,
                IssueID:   &issueID,
                Decision:  "cluster",
                Reasoning: cluster.RootCause + " — " + cluster.Strategy,
            })
        }
    }

    return entries
}
```

### How the decision log is fed to the next PM cycle

The PM service fetches the last N decision log entries (default: last 50, or last 30 days, whichever is smaller) and includes them in the PM's context. The agent sees them as a structured history:

```
## Previous Decisions

These are your decisions from recent planning cycles. Use them to maintain
consistency, avoid re-evaluating settled issues, and learn from outcomes.

### Cycle #45 (4 hours ago)
- DELEGATED issue "NilPointer in payment handler" → succeeded (PR merged)
- DELEGATED issue "Timeout in webhook processing" → failed (agent timed out)
- SKIPPED issue "Refactor auth module" → reason: "Too complex for automated fix,
  spans 12 files with no test coverage"
- CLUSTERED issues "500 on /api/users", "500 on /api/teams" → root cause:
  "Both hit the same malformed JSON decoder in pkg/api/decode.go"

### Cycle #44 (8 hours ago)
- DELEGATED issue "Missing CORS header" → succeeded (PR merged)
- DEFERRED issue "Slow dashboard query" → reason: "Needs schema migration,
  waiting for next deploy window"
- OBSERVED: "The billing module has 0% test coverage — all billing issues
  should be flagged as medium confidence until tests exist"
```

The decision log entries include their **outcomes** (filled in asynchronously as agent runs complete, PRs are merged/rejected, etc.). This gives the PM direct feedback on its past decisions — it can see which delegations succeeded, which skips were correct, and which deferrals are still waiting.

### Decision log update flow

Outcomes are backfilled by an async process (or the same plan-outcome tracker from the feedback loop section):

```go
// updateDecisionOutcomes is called when an agent run reaches a terminal state.
// It finds the decision log entry for that run's issue and fills in the outcome.
func (s *Service) updateDecisionOutcomes(ctx context.Context, run *models.AgentRun) error {
    if run.PMPlanID == nil {
        return nil // not a PM-delegated run
    }

    outcome := outcomeFromRunStatus(run.Status, run.ValidationResult)
    return s.decisionLog.UpdateOutcome(ctx, *run.PMPlanID, run.IssueID, outcome)
}
```

### Database table: `pm_decision_log`

```sql
CREATE TABLE pm_decision_log (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id      UUID NOT NULL REFERENCES organizations(id),
    plan_id     UUID NOT NULL REFERENCES pm_plans(id),
    issue_id    UUID REFERENCES issues(id),            -- null for "observe" entries
    decision    TEXT NOT NULL,                          -- delegate, skip, cluster, defer, observe
    reasoning   TEXT NOT NULL,
    outcome     TEXT,                                   -- succeeded, failed, pr_rejected, still_open (filled async)
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_pm_decision_log_org_created ON pm_decision_log(org_id, created_at DESC);
CREATE INDEX idx_pm_decision_log_plan ON pm_decision_log(plan_id);
CREATE INDEX idx_pm_decision_log_issue ON pm_decision_log(issue_id);
```

## Migration Path

### Step 1: Add PM agent alongside existing system (no behavior change)

- Add `pm_plans` and `pm_decision_log` tables, `pm.Service`, API endpoints, `ProductContext` to org settings
- PM agent can be triggered manually via `POST /pm/analyze`
- Plans are stored and viewable but don't create any agent runs
- Decision log is written on each PM cycle
- Existing per-issue prioritize → auto-trigger flow continues unchanged
- Settings UI gains the Product Context section

### Step 2: Enable PM delegation

- PM agent's `executePlan()` starts creating agent runs
- Remove `CheckAutoTrigger()` from the `prioritize` job handler
- Add PM cron to scheduler
- Decision log outcomes are backfilled as agent runs complete
- ProductContext is written to AGENTS.md in the sandbox
- Now: PM agent is the only path to automated coding runs. Manual "Fix This" still works via direct agent run creation.

### Step 3: Polish and feedback loop

- Frontend plan view page with plan diffs between cycles
- PM context display on run detail pages
- Plan outcome tracking and feedback injection
- Decision log viewer in the frontend (see past decisions + outcomes)
- Settings UI for schedule interval and model selection

## Resolved Decisions

1. **Model choice.** Default to Sonnet-class. The PM makes strategic decisions that benefit from strong reasoning. Since the PM runs as a full agent with codebase access (not a single LLM call), token usage is higher — expect 50-100K tokens per PM cycle as it explores the repo and reasons over issues. At 6 cycles/day, that's ~$5-15/day at Sonnet pricing. Still modest compared to the cost of the coding agent runs it orchestrates.

2. **Issue volume strategy.** Two-tier summarization: top 30 get full summaries (200 chars), remaining get one-liners. Recency window of 30 days for outcomes/PRs. Truncate if exceeding 80% of context window.

3. **Plan staleness.** Admins keep the manual trigger and the per-issue "Fix This" button. No automatic fast-path for critical issues in v1 — that's a future optimization that adds complexity without being proven necessary.

4. **ProductContext vs. auto-bootstrap.** ProductContext (philosophy, direction, focus/avoid areas) is user input. Raw outcome data (recent runs, PRs, in-flight work) is auto-included for the PM to reason over. No pre-computed aggregates — the PM agent is smart enough to spot patterns in raw data, and pre-computing biases its reasoning.

5. **Keep ProductContext lean.** Four fields: philosophy, direction, focus areas, avoid areas. Fix style and risk tolerance are subsumed by philosophy (e.g., "we prefer minimal diffs" or "we move fast and accept revisions"). Fewer knobs = less admin friction = more likely to actually get filled in.

6. **ProductContext delivery via AGENTS.md.** Rather than injecting product context as a separate file or system prompt section, we append it to the repo's `AGENTS.md` in the sandbox. This means the PM discovers it naturally during its standard codebase exploration flow — the same way a human PM would read the team's docs before planning. It also means coding agents reading AGENTS.md see the same product context, ensuring shared alignment.

7. **Decision log for institutional memory.** Each PM cycle writes its decisions (delegate, skip, cluster, defer, observe) to a `pm_decision_log` table with outcomes backfilled asynchronously. The last ~50 entries are provided to the next PM cycle as context, giving the agent continuity across runs. This prevents the PM from re-evaluating settled issues, enables learning from past failures, and makes the PM's behavior more consistent over time.
