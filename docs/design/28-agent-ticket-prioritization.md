# Design: AI Product Manager Agent

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
                              │  PM AGENT  (the "brain" — runs as an LLM call)      │
                              │                                                     │
                              │  Reads:                                             │
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

## Detailed Design

### 1. Batch Schedule: Simple Cron

The PM agent runs on a straightforward cron schedule. No complex trigger logic.

```
┌─────────────────────────────────────────────────────┐
│  Scheduler (existing scheduler_lock.go)             │
│                                                     │
│  Every N hours (configurable per org, default: 4h): │
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

**Org setting:** `pm_schedule_hours` (default: `4`). The scheduler checks `last_pm_run_at + pm_schedule_hours < now()` for each org.

### 2. The PM Agent

The PM agent is a single LLM call (not a sandbox execution — it doesn't write code). It receives a context package and returns a structured JSON plan.

#### New Service: `internal/services/pm/service.go`

```go
package pm

// Service is the AI Product Manager. It analyzes accumulated issues,
// reasons about priorities and patterns, and produces a work plan
// that gets delegated to coding agents.
type Service struct {
    issues       issueStore         // fetch open/triaged issues
    agentRuns    agentRunStore      // fetch in-flight and recent runs
    pullRequests prStore            // fetch recent PR outcomes
    orgs         orgStore           // fetch org settings + product direction
    jobs         jobStore           // enqueue run_agent jobs
    plans        planStore          // persist PM plans
    llm          llm.Client         // the LLM backing the PM agent
    logger       zerolog.Logger
}

// Analyze is the main entry point. It:
//  1. Gathers full context for the org
//  2. Calls the LLM with a PM-framed prompt
//  3. Parses the structured plan
//  4. Persists the plan
//  5. Delegates work items to coding agents
func (s *Service) Analyze(ctx context.Context, orgID uuid.UUID, trigger string) (*Plan, error) {
    // Step 1: Gather context
    pmCtx, err := s.gatherContext(ctx, orgID)
    if err != nil {
        return nil, fmt.Errorf("gather context: %w", err)
    }

    // Step 2: Call LLM
    plan, err := s.callPMAgent(ctx, pmCtx)
    if err != nil {
        return nil, fmt.Errorf("pm agent call: %w", err)
    }

    // Step 3: Persist plan
    plan.OrgID = orgID
    plan.TriggeredBy = trigger
    if err := s.plans.Create(ctx, plan); err != nil {
        return nil, fmt.Errorf("persist plan: %w", err)
    }

    // Step 4: Delegate to coding agents
    if err := s.executePlan(ctx, orgID, plan); err != nil {
        return nil, fmt.Errorf("execute plan: %w", err)
    }

    return plan, nil
}
```

#### Context Gathering

The PM agent sees everything relevant to making decisions:

```go
// PMContext is the full picture the PM agent reasons over.
type PMContext struct {
    // What needs attention
    OpenIssues       []IssueSummary    // all issues with status "open" or "triaged"

    // What's already happening
    InFlightRuns     []RunSummary      // agent runs in "pending" or "running" status

    // What happened recently (learning from outcomes)
    RecentOutcomes   []OutcomeSummary  // last ~20 completed runs: did the fix work?
    RecentFailures   []FailureSummary  // last ~10 failures: why did the agent fail?
    RecentPRs        []PRSummary       // last ~20 PRs: merged? rejected? reverted?

    // Strategic context
    ProductDirection string            // admin-set product direction statement
    OrgSettings      OrgSettingsSummary

    // System constraints
    MaxConcurrentRuns int
    CurrentRunCount   int              // how many slots are available right now
}

// IssueSummary is a compact representation of an issue for the PM prompt.
// Full raw data is too large — we summarize to ~200 chars per issue.
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
    CurrentScore          *float64  `json:"current_priority_score,omitempty"`
    HasStackTrace         bool      `json:"has_stack_trace"` // Sentry issues with stack traces are easier to fix
}
```

#### LLM Prompt

The PM agent prompt frames it as a product manager running a planning session:

```go
const pmSystemPrompt = `You are an AI Product Manager running a planning session for an autonomous
software engineering team. Your team consists of AI coding agents that can fix
bugs and implement small features.

Your job is to look at all the incoming work (Sentry errors, Linear tickets),
understand what's happening in the codebase right now, and decide:
  1. What should be worked on next, and in what order
  2. Which issues are related and should be tackled together
  3. What approach each coding agent should take
  4. What should be skipped or deferred, and why

## How to Think

Think like a senior PM running a sprint planning meeting:

- START by analyzing the situation. What patterns do you see? Are there clusters
  of related errors? Is something getting worse? Did a recent deploy break things?

- CLUSTER related issues. If 5 Sentry errors all point to the same service or
  the same code path, that's one work item, not five. Identify the root cause
  and fix it once.

- PRIORITIZE by real impact. Consider:
  - How many customers are affected (and how badly)
  - Whether it aligns with the product direction
  - Whether it's getting worse (trending up in occurrences)
  - Whether the coding agent can realistically fix it (based on past outcomes)
  - Dependencies (does fixing X unblock Y?)

- GIVE APPROACH HINTS. For each work item, tell the coding agent where to look
  and what to be careful about. You're the PM briefing your engineer. Example:
  "The stack trace points to a nil pointer in handlers/payment.go:142. This is
  likely a missing nil check on the user's payment method. Be careful not to
  change the payment flow logic — just add the guard."

- SKIP things that shouldn't be auto-fixed:
  - Issues that need a human product decision
  - Duplicates of in-flight work
  - Issues too complex for an AI agent (based on past failure patterns)
  - Issues misaligned with product direction

- RESPECT CONSTRAINTS. You have {available_slots} available agent slots
  (out of {max_concurrent} total). Don't plan more work than you have capacity for.

## Output Format

Respond with a JSON object:
{
  "analysis": "<2-3 paragraph situation analysis: what patterns you see, what's urgent, what's trending>",
  "tasks": [
    {
      "rank": 1,
      "issue_ids": ["<uuid>", ...],
      "title": "<your summary of the work item>",
      "reasoning": "<why this is priority #1>",
      "approach": "<specific guidance for the coding agent: where to look, what to change, what to watch out for>",
      "risk": "<what could go wrong>",
      "complexity": "<trivial|simple|moderate|complex>",
      "confidence": "<high|medium|low — can the agent handle this?>"
    }
  ],
  "clusters": [
    {
      "issue_ids": ["<uuid>", ...],
      "root_cause": "<your hypothesis about the shared root cause>",
      "strategy": "<fix root cause in issue X, others will resolve>"
    }
  ],
  "skip": [
    {
      "issue_id": "<uuid>",
      "reason": "<duplicate|needs_human_decision|too_complex|misaligned|already_in_flight>",
      "detail": "<explanation>"
    }
  ]
}`
```

The user prompt contains the serialized `PMContext` as JSON.

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
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           UUID NOT NULL REFERENCES organizations(id),
    status           TEXT NOT NULL DEFAULT 'executing',   -- executing, completed, failed
    analysis         TEXT,                                -- PM's situation analysis
    tasks            JSONB NOT NULL DEFAULT '[]',         -- ordered task list
    clusters         JSONB NOT NULL DEFAULT '[]',         -- issue clusters
    skipped_issues   JSONB NOT NULL DEFAULT '[]',         -- skip list
    issues_reviewed  INT NOT NULL DEFAULT 0,
    token_usage      JSONB,
    triggered_by     TEXT NOT NULL DEFAULT 'cron',        -- "cron" or "manual"
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at     TIMESTAMPTZ
);

CREATE INDEX idx_pm_plans_org_created ON pm_plans(org_id, created_at DESC);
```

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
PMScheduleHours  int    `json:"pm_schedule_hours"`  // hours between PM runs (default: 4)
PMModel          string `json:"pm_model"`           // LLM model for PM agent (default: "sonnet")
```

### 9. Frontend

#### New: PM Plan View (`/pm` or `/plans`)

A page showing the PM agent's latest analysis and work plan:

- **Analysis section** — the PM's 2-3 paragraph situation summary
- **Task list** — ordered cards showing rank, title, reasoning, approach, confidence badge
  - Each card shows which coding agent run it spawned (linked)
  - Status: delegated / skipped (capacity) / completed / failed
- **Clusters section** — visual grouping of related issues with root cause
- **Skip list** — collapsible section showing what was skipped and why
- **History** — previous plans for comparison

#### Modified: Runs Page

Each agent run card shows a "PM Context" expandable section if `pm_plan_id` is set, showing the PM's reasoning and approach guidance.

#### Modified: Settings Page

Add PM configuration section: schedule interval, model selection.

## File Changes Summary

### New Files

| File | Description |
|------|-------------|
| `internal/services/pm/service.go` | PM agent service: context gathering, LLM call, plan parsing, delegation |
| `internal/services/pm/service_test.go` | Tests |
| `internal/services/pm/context.go` | Context assembly (queries issues, runs, PRs, settings) |
| `internal/services/pm/prompt.go` | System/user prompt construction |
| `internal/services/pm/types.go` | Plan, Task, Cluster, SkipEntry types |
| `internal/db/pm_plans.go` | DB store for pm_plans table |
| `internal/db/pm_plans_test.go` | Store tests |
| `internal/api/handlers/pm.go` | API handlers for PM endpoints |
| `migrations/000004_pm_agent.up.sql` | New table + agent_runs columns |
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
| `internal/models/org_settings.go` | Add `PMScheduleHours`, `PMModel` settings |
| `cmd/server/main.go` | Wire up `pm.Service` |

## Migration Path

### Step 1: Add PM agent alongside existing system (no behavior change)

- Add `pm_plans` table, `pm.Service`, API endpoints
- PM agent can be triggered manually via `POST /pm/analyze`
- Plans are stored and viewable but don't create any agent runs
- Existing per-issue prioritize → auto-trigger flow continues unchanged

### Step 2: Enable PM delegation

- PM agent's `executePlan()` starts creating agent runs
- Remove `CheckAutoTrigger()` from the `prioritize` job handler
- Add PM cron to scheduler
- Now: PM agent is the only path to automated coding runs. Manual "Fix This" still works via direct agent run creation.

### Step 3: Polish

- Frontend plan view page
- PM context display on run detail pages
- Settings UI for schedule interval

## Open Questions

1. **Model choice.** The PM agent benefits from strong reasoning (it's making strategic decisions, not just classifying). Recommendation: default to Sonnet-class. The cost is bounded — one LLM call per PM cycle, not per issue.

2. **Issue volume.** With 100+ open issues, the context package could exceed model limits. Recommendation: summarize each issue to ~200 chars, include full detail only for the top 20 by numeric score. If still too large, paginate and have the PM focus on "new since last run" plus "still unresolved from previous plans."

3. **Plan staleness.** If a critical Sentry error arrives 5 minutes after the PM ran, it waits up to N hours. Recommendation: keep the manual `POST /pm/analyze` trigger. Admins can also still use the existing "Fix This" button to manually start an agent run for urgent issues outside the PM cycle.
