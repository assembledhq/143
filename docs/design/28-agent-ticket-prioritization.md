# Design: Agent-Driven Ticket Prioritization ("AI Product Manager")

This document describes how to restructure 143.dev so that an LLM-powered **triage agent** acts as an autonomous product manager — analyzing all incoming Sentry, Linear, and GitHub issues in aggregate, reasoning about patterns, dependencies, and business impact, and producing a prioritized work plan that the coding agents then execute.

## Problem Statement

The current system processes tickets **individually and reactively**: each ingested issue gets a numeric priority score, a complexity estimate, and — if it clears the auto-trigger gates — an agent run. This works, but it has three limitations:

1. **No cross-ticket reasoning.** A cluster of 5 Sentry errors sharing the same root cause gets scored 5 separate times. The system doesn't see that fixing one underlying service timeout would resolve all five.
2. **No strategic context.** The numeric score (customer impact × severity × recency + direction alignment) cannot reason about things like "we shipped a refactor to the payments module last week and 3 of these 8 tickets are regression artifacts from that deploy."
3. **No workload planning.** The system makes per-ticket go/no-go decisions but never produces a plan like "these 3 issues are related, tackle them together in this order, and skip this one because it's a known flaky test."

## Proposed Architecture

### Current Flow (per-ticket, reactive)

```
Webhook/Poll → Ingest → Normalize → Upsert Issue → [per issue] Prioritize → Estimate Complexity → Auto-Trigger → Run Agent
```

### New Flow (batch-aware, agent-driven)

```
                                                    ┌─────────────────────────────────────┐
Webhook/Poll → Ingest → Normalize → Upsert Issue ──┤                                     │
                                                    │  Triage Queue (accumulator)          │
                                                    │  (issues table, status = "open")     │
                                                    └──────────────┬──────────────────────┘
                                                                   │
                                                         trigger: timer / batch threshold
                                                                   │
                                                                   ▼
                                                    ┌──────────────────────────────────────┐
                                                    │  TRIAGE AGENT (AI Product Manager)   │
                                                    │                                      │
                                                    │  Inputs:                             │
                                                    │   • All open/triaged issues          │
                                                    │   • Recent agent run outcomes         │
                                                    │   • PR merge/rejection history        │
                                                    │   • Org settings + product direction  │
                                                    │   • Repo context quality scores       │
                                                    │   • Current in-flight agent runs      │
                                                    │   • Failure patterns & learnings      │
                                                    │                                      │
                                                    │  Outputs:                            │
                                                    │   • Prioritized work plan (ordered)   │
                                                    │   • Issue clusters (related tickets)  │
                                                    │   • Skip recommendations + reasoning  │
                                                    │   • Risk assessment per issue          │
                                                    │   • Suggested approach per issue       │
                                                    └──────────────┬───────────────────────┘
                                                                   │
                                                                   ▼
                                                    ┌──────────────────────────────────────┐
                                                    │  PLAN EXECUTION                      │
                                                    │                                      │
                                                    │  For each item in plan (in order):   │
                                                    │   1. Update issue status → triaged    │
                                                    │   2. Store triage reasoning            │
                                                    │   3. Check gates (autonomy,           │
                                                    │      aggressiveness, concurrency)     │
                                                    │   4. Enqueue run_agent with approach   │
                                                    │      context from triage agent         │
                                                    └──────────────────────────────────────┘
```

### Key Design Principle: Webhooks Stay, the Main Loop Changes

**Ingestion is unchanged.** Sentry/Linear/GitHub webhooks still fire, normalize, and upsert issues exactly as today. The change is what happens *after* ingestion:

- **Before:** Each ingested issue immediately enqueues its own `prioritize` job.
- **After:** Ingested issues land in the issues table with `status = "open"` as usual, but instead of a per-issue `prioritize` job, the system accumulates issues and periodically runs a **batch triage** via an LLM agent that sees the full picture.

## Detailed Design

### 1. Triage Trigger Strategy

The triage agent runs when **any** of these conditions are met:

| Trigger | Condition | Rationale |
|---------|-----------|-----------|
| **Timer** | Every 15 minutes (configurable via `triage_interval` in org settings) | Ensures regular cadence even during quiet periods |
| **Batch threshold** | 5+ new issues since last triage (configurable via `triage_batch_size`) | Don't wait 15 min if a flood of errors arrives |
| **Manual** | Admin clicks "Run Triage" in the UI or calls `POST /api/v1/triage/run` | On-demand when the admin wants a fresh plan |
| **Settings change** | Admin updates product direction, weights, or autonomy level | Re-triage with new strategic context |

Implementation: A new `triage` job type with a scheduler rule. The ingestion service increments a per-org counter in the `triage_state` table; a scheduler check compares the counter against the batch threshold.

### 2. Triage Agent: The AI Product Manager

#### New Service: `internal/services/triage/service.go`

```go
package triage

// TriageService orchestrates the AI product manager triage cycle.
type TriageService struct {
    issues           issueStore
    priorities       priorityScoreStore
    complexity       complexityEstimateStore
    agentRuns        agentRunStore
    pullRequests     pullRequestStore
    orgs             orgStore
    repos            repositoryStore
    jobs             jobStore
    triageState      triageStateStore
    llm              llm.Client
    logger           zerolog.Logger
}

// RunTriage is the main entry point. It:
//  1. Gathers all context (open issues, recent outcomes, org settings)
//  2. Calls the LLM with a structured prompt
//  3. Parses the triage plan
//  4. Persists the plan and updates issue statuses
//  5. Enqueues agent runs for top-priority items
func (s *TriageService) RunTriage(ctx context.Context, orgID uuid.UUID) (*TriagePlan, error) {
    // ... see detailed flow below
}
```

#### Context Gathering (Step 1)

The triage agent receives a **context package** assembled from multiple sources:

```go
type TriageContext struct {
    // All open issues that haven't been resolved or dismissed.
    OpenIssues           []models.Issue           // status in ("open", "triaged")

    // Issues currently being worked on by coding agents.
    InFlightRuns         []models.AgentRun        // status in ("pending", "running")

    // Recent outcomes to learn from.
    RecentCompletedRuns  []AgentRunSummary        // last 20 completed runs with outcome
    RecentFailedRuns     []AgentRunSummary        // last 10 failed runs with failure category

    // PR outcomes — what got merged, what got rejected.
    RecentPROutcomes     []PROutcomeSummary       // last 20 PRs with merge/close status

    // Org-level strategic context.
    ProductDirection     string                   // from org settings
    AutonomyLevel        string                   // manual / auto_simple / auto_all
    Aggressiveness       int                      // 1-4

    // Existing priority scores (for comparison / delta reasoning).
    ExistingScores       map[uuid.UUID]float64    // issue_id -> current score

    // Failure learnings — patterns of what the agent struggles with.
    FailurePatterns      []FailurePatternSummary  // aggregated failure categories

    // Repo context quality — so the agent knows where it has good context.
    RepoContextQuality   map[uuid.UUID]float64    // repo_id -> quality score
}
```

#### LLM Prompt (Step 2)

The triage agent uses a structured system prompt that frames it as a product manager:

```
You are an AI Product Manager for a software engineering team. Your job is to
analyze all incoming tickets (Sentry errors, Linear issues, customer reports),
understand the current state of the codebase and recent engineering outcomes, and
produce a prioritized work plan.

You are NOT writing code. You are deciding WHAT should be worked on, in WHAT
ORDER, and WHY.

## Your Decision Framework

1. **Cluster related issues.** Multiple tickets may share a root cause. Group
   them and treat the cluster as one work item.

2. **Assess fixability.** Given recent agent outcomes (successes, failures, PR
   rejections), estimate whether the agent can realistically fix this issue.
   Consider:
   - Complexity (single file vs. architectural change)
   - Repo context quality (well-documented repos succeed more)
   - Similar past attempts (did a similar fix fail before?)

3. **Prioritize by impact.** Consider:
   - Customer impact (affected users × severity)
   - Business alignment with product direction
   - Recency and momentum (is this getting worse?)
   - Dependencies (does fixing A unblock B?)

4. **Respect constraints.** The team has a concurrency limit of {max_concurrent}
   parallel agent runs. Plan within that budget.

5. **Recommend an approach.** For each work item, provide:
   - Which issue(s) it addresses
   - Suggested approach (where to look, what to change)
   - Risk assessment (what could go wrong)
   - Confidence level (high/medium/low that the agent can handle this)

6. **Flag items to skip.** Not everything should be auto-fixed. Flag issues that:
   - Need human product decisions
   - Are duplicates of in-flight work
   - Are too complex for the current agent capabilities
   - Are misaligned with product direction

Respond with a JSON object following this exact schema: ...
```

#### Triage Plan Output (Step 3)

```go
// TriagePlan is the structured output from the triage agent.
type TriagePlan struct {
    ID                uuid.UUID           `json:"id" db:"id"`
    OrgID             uuid.UUID           `json:"org_id" db:"org_id"`
    Status            string              `json:"status" db:"status"` // "draft", "approved", "executing", "completed"

    // The ordered work items the agent recommends tackling.
    WorkItems         []TriageWorkItem    `json:"work_items"`

    // Issues the agent recommends skipping with reasoning.
    SkippedIssues     []TriageSkipEntry   `json:"skipped_issues"`

    // High-level analysis and patterns the agent noticed.
    AnalysisSummary   string              `json:"analysis_summary"`

    // Clusters of related issues the agent identified.
    IssueClusters     []IssueCluster      `json:"issue_clusters"`

    // Metadata.
    IssuesAnalyzed    int                 `json:"issues_analyzed"`
    TokenUsage        json.RawMessage     `json:"token_usage"`
    ModelUsed         string              `json:"model_used"`
    CreatedAt         time.Time           `json:"created_at" db:"created_at"`
}

type TriageWorkItem struct {
    Rank              int                 `json:"rank"`         // 1 = highest priority
    IssueIDs          []uuid.UUID         `json:"issue_ids"`    // may be multiple if clustered
    PrimaryIssueID    uuid.UUID           `json:"primary_issue_id"`
    Title             string              `json:"title"`        // agent's summary
    Reasoning         string              `json:"reasoning"`    // why this is prioritized here
    SuggestedApproach string              `json:"suggested_approach"` // hints for the coding agent
    RiskAssessment    string              `json:"risk_assessment"`
    EstimatedTier     int                 `json:"estimated_tier"`     // 1-5 complexity
    Confidence        string              `json:"confidence"`         // high / medium / low
    Action            string              `json:"action"`             // "auto_fix", "fix_with_review", "needs_human"
}

type TriageSkipEntry struct {
    IssueID    uuid.UUID `json:"issue_id"`
    Reason     string    `json:"reason"`     // "duplicate", "needs_product_decision", "too_complex", "misaligned", "already_in_flight"
    Detail     string    `json:"detail"`     // human-readable explanation
}

type IssueCluster struct {
    ClusterID    string       `json:"cluster_id"`
    RootCause    string       `json:"root_cause"`    // agent's hypothesis
    IssueIDs     []uuid.UUID  `json:"issue_ids"`
    FixStrategy  string       `json:"fix_strategy"`  // "fix root cause first, others resolve automatically"
}
```

### 3. Plan Persistence and Execution

#### New DB Table: `triage_plans`

```sql
CREATE TABLE triage_plans (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            UUID NOT NULL REFERENCES organizations(id),
    status            TEXT NOT NULL DEFAULT 'draft',    -- draft, approved, executing, completed, superseded
    work_items        JSONB NOT NULL DEFAULT '[]',
    skipped_issues    JSONB NOT NULL DEFAULT '[]',
    issue_clusters    JSONB NOT NULL DEFAULT '[]',
    analysis_summary  TEXT,
    issues_analyzed   INT NOT NULL DEFAULT 0,
    token_usage       JSONB,
    model_used        TEXT,
    triggered_by      TEXT NOT NULL,                    -- "timer", "batch_threshold", "manual", "settings_change"
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at      TIMESTAMPTZ
);

CREATE INDEX idx_triage_plans_org_status ON triage_plans(org_id, status);
```

#### New DB Table: `triage_state`

Tracks per-org triage state for trigger logic:

```sql
CREATE TABLE triage_state (
    org_id              UUID PRIMARY KEY REFERENCES organizations(id),
    last_triage_at      TIMESTAMPTZ,
    last_triage_plan_id UUID REFERENCES triage_plans(id),
    issues_since_triage INT NOT NULL DEFAULT 0,        -- counter reset after each triage
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

#### Execution Flow

After the triage agent produces a plan:

1. **Auto-execute mode** (default for `auto_all` autonomy): The plan is immediately executed.
   - For each work item in rank order (up to concurrency limit):
     - Update issue status to `triaged`
     - Store the triage reasoning on the issue (new `triage_reasoning` column or in a separate table)
     - Create an `AgentRun` with the `suggested_approach` injected into the prompt context
     - Enqueue `run_agent` job
   - Skip entries: update issues with skip reason, optionally mark `wont_fix`

2. **Review mode** (for `manual` or `auto_simple` autonomy): The plan is stored as `draft` and surfaces in the UI for admin approval.
   - Admin can:
     - Approve the full plan → executes as above
     - Approve individual items → cherry-pick which to run
     - Edit items → modify approach, reorder
     - Dismiss items → skip with reason
   - UI shows the plan as a draggable, interactive list

3. **Superseding**: When a new triage runs, any previous `draft` or `executing` plan for the org is marked `superseded`. Only one active plan per org at a time.

### 4. Changes to Existing Components

#### Ingestion Service (`internal/services/ingestion/service.go`)

**Change**: After upserting an issue, instead of enqueuing a `prioritize` job, increment the triage counter.

```go
// Before:
dedupeKey := fmt.Sprintf("prioritize:%s", issue.ID.String())
_, _ = s.jobStore.Enqueue(ctx, orgID, "default", "prioritize", ...)

// After:
if err := s.triageState.IncrementIssueCounter(ctx, orgID); err != nil {
    s.logger.Warn().Err(err).Msg("failed to increment triage counter")
}
// The triage scheduler checks this counter and triggers when threshold is met.
```

**Note**: The per-issue `prioritize` job handler is **kept** as a fallback and for manual reprioritize requests via the API. The triage agent's work plan subsumes its role for the automated flow.

#### Worker Handlers (`internal/worker/handlers.go`)

**Add**: New `triage` job handler that calls `TriageService.RunTriage()`.

```go
w.Register("triage", newTriageHandler(stores, services, logger))
```

**Add**: New `execute_triage_plan` job handler that processes an approved plan.

```go
w.Register("execute_triage_plan", newExecuteTriagePlanHandler(stores, services, logger))
```

#### Agent Orchestrator (`internal/services/agent/orchestrator.go`)

**Change**: `AgentInput` gains a `TriageContext` field so the coding agent receives the triage agent's suggested approach and cluster context.

```go
type AgentInput struct {
    Issue              *models.Issue
    RepoURL            string
    RepoBranch         string
    OrgSettings        *models.OrgSettings
    TokenMode          string
    ComplexityEstimate *ComplexityEstimate
    RevisionContext    *RevisionContext
    // NEW: Context from the triage agent's analysis
    TriageContext      *TriageWorkItemContext
}

type TriageWorkItemContext struct {
    SuggestedApproach  string     // "Look at the timeout handling in pkg/http/client.go..."
    RiskAssessment     string     // "This touches the payment flow — be extra careful..."
    RelatedIssueIDs    []uuid.UUID // Other issues in the same cluster
    ClusterRootCause   string     // "All 3 errors stem from a missing nil check in..."
}
```

The Claude Code adapter's `PreparePrompt()` injects this context into the system prompt when present.

#### Scheduler (`internal/cluster/scheduler_lock.go`)

**Add**: A new scheduled job that checks triage trigger conditions:

```go
// Every minute, check if any org needs a triage run:
//  - Timer: last_triage_at + triage_interval < now()
//  - Batch: issues_since_triage >= triage_batch_size
func (s *Scheduler) checkTriageTriggers(ctx context.Context) {
    orgs := s.triageState.ListOrgsNeedingTriage(ctx)
    for _, orgID := range orgs {
        s.jobs.Enqueue(ctx, orgID, "default", "triage", ...)
    }
}
```

### 5. Prioritization Service: Kept but Scoped Down

The existing `prioritization/service.go` is **not deleted**. It serves two purposes in the new architecture:

1. **Numeric scoring for display.** The dashboard still shows priority scores and complexity tiers. The triage agent's analysis augments but doesn't replace the UI badges.

2. **Fallback for manual triggers.** When an admin clicks "Reprioritize" on a single issue, the per-issue scoring still runs.

3. **Input to the triage agent.** The numeric scores are part of the context package the triage agent receives. It can use them as one input among many.

The key change: the `prioritize` job no longer auto-triggers `run_agent`. That responsibility moves to the triage plan execution flow.

### 6. API Endpoints

#### New Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/api/v1/triage/run` | admin | Manually trigger a triage run |
| `GET` | `/api/v1/triage/plans` | viewer+ | List triage plans for the org (with pagination) |
| `GET` | `/api/v1/triage/plans/{id}` | viewer+ | Get a specific triage plan with full details |
| `GET` | `/api/v1/triage/plans/current` | viewer+ | Get the most recent active plan |
| `POST` | `/api/v1/triage/plans/{id}/approve` | admin | Approve a draft plan for execution |
| `POST` | `/api/v1/triage/plans/{id}/approve-items` | admin | Approve specific items from a plan |
| `DELETE` | `/api/v1/triage/plans/{id}` | admin | Dismiss/cancel a draft plan |

#### Modified Endpoints

| Endpoint | Change |
|----------|--------|
| `GET /api/v1/issues` | Add `triage_reasoning` field in response if available |
| `GET /api/v1/issues/{id}` | Include triage context (which plan, rank, cluster info) |
| `PATCH /api/v1/organizations/settings` | Add `triage_interval`, `triage_batch_size` settings |

### 7. Org Settings Additions

```go
type OrgSettings struct {
    // ... existing fields ...

    // Triage agent settings
    TriageInterval       int    `json:"triage_interval"`         // minutes between auto-triage (default: 15)
    TriageBatchThreshold int    `json:"triage_batch_threshold"`  // issue count to trigger early triage (default: 5)
    TriageAutoExecute    bool   `json:"triage_auto_execute"`     // auto-execute plans or require approval (default: follows autonomy_level)
    TriageModel          string `json:"triage_model"`            // LLM model for triage (default: same as prioritization)
}
```

### 8. Frontend Changes

#### New Page: Triage Plan View (`/triage`)

Shows the current triage plan as an interactive work queue:

- **Plan header**: analysis summary, when it ran, how many issues analyzed, model used
- **Work items list**: ordered cards showing rank, title, reasoning, confidence, action
  - Each card expandable to show related issues, suggested approach, risk
  - Drag-to-reorder for manual adjustment
  - Approve/skip/edit actions per item
- **Skipped issues section**: collapsible list of issues the agent recommended skipping with reasons
- **Clusters view**: visual grouping of related issues with root cause hypothesis
- **Plan history**: sidebar showing previous plans for comparison

#### Modified Pages

- **Issues page**: Add "Triage Status" column showing the issue's position in the current plan
- **Settings page**: Add triage configuration section (interval, batch size, auto-execute toggle)
- **Overview/Dashboard**: Show active triage plan summary widget

### 9. Migration Plan (Incremental)

This can be built incrementally without breaking the existing system:

#### Phase A: Foundation (no behavior change)
1. Add `triage_plans` and `triage_state` tables (migration)
2. Add `TriageService` with `RunTriage()` that produces a plan but doesn't execute it
3. Add API endpoints for viewing plans
4. Wire up manual trigger (`POST /triage/run`)
5. Frontend: add read-only plan view page

**At this point**: The system still works exactly as before. The triage agent can be triggered manually for experimentation, but it has no effect on the pipeline.

#### Phase B: Parallel Run (shadow mode)
1. Add triage trigger logic to scheduler (timer + batch threshold)
2. Triage runs automatically but plans are always `draft` (never auto-executed)
3. Dashboard shows triage recommendations alongside the existing priority scores
4. Team can compare triage agent recommendations vs. numeric scoring to validate quality

**At this point**: The triage agent runs in parallel. Existing pipeline still drives all actual work.

#### Phase C: Switchover
1. Add `execute_triage_plan` handler
2. Add plan approval flow in frontend
3. For `manual` autonomy orgs: plans require approval before execution
4. For `auto_simple`/`auto_all` orgs: plans auto-execute (configurable)
5. Ingestion stops enqueuing `prioritize` jobs by default (counter-based trigger instead)
6. Per-issue `prioritize` kept as manual action only

**At this point**: The triage agent is the primary decision-maker.

#### Phase D: Optimization
1. Add cluster-aware coding: when the triage agent identifies a cluster, the coding agent receives all related issues
2. Add plan diff: show what changed between consecutive triage plans
3. Add triage feedback loop: track which triage recommendations led to successful PRs
4. Add triage agent self-improvement: inject past plan outcomes into future triage prompts

## File Changes Summary

### New Files
| File | Description |
|------|-------------|
| `internal/services/triage/service.go` | Triage agent service (context gathering, LLM call, plan parsing) |
| `internal/services/triage/service_test.go` | Tests |
| `internal/services/triage/context.go` | Context assembly logic |
| `internal/services/triage/prompt.go` | LLM prompt construction |
| `internal/services/triage/plan.go` | Plan types and execution logic |
| `internal/db/triage_plans.go` | DB store for triage_plans table |
| `internal/db/triage_plans_store_test.go` | Store tests |
| `internal/db/triage_state.go` | DB store for triage_state table |
| `internal/db/triage_state_store_test.go` | Store tests |
| `internal/api/handlers/triage.go` | API handlers for triage endpoints |
| `internal/api/handlers/triage_test.go` | Handler tests |
| `migrations/000004_triage.up.sql` | Schema migration |
| `migrations/000004_triage.down.sql` | Rollback migration |
| `frontend/src/app/(dashboard)/triage/page.tsx` | Triage plan view page |
| `frontend/src/components/triage/plan-view.tsx` | Plan view component |
| `frontend/src/components/triage/work-item-card.tsx` | Work item card component |

### Modified Files
| File | Change |
|------|--------|
| `internal/services/ingestion/service.go` | Add triage counter increment after upsert |
| `internal/worker/handlers.go` | Add `triage` and `execute_triage_plan` handlers |
| `internal/services/agent/orchestrator.go` | Add `TriageContext` to `AgentInput` |
| `internal/services/agent/adapter.go` | Add `TriageWorkItemContext` type |
| `internal/services/agent/adapters/claude_code.go` | Inject triage context into prompts |
| `internal/api/router.go` | Add triage routes |
| `internal/models/models.go` | Add `TriagePlan` and related model types |
| `internal/models/org_settings.go` | Add triage-related settings fields |
| `cmd/server/main.go` | Wire up `TriageService` |

## Open Questions

1. **Token budget for triage.** With 50+ open issues, the context package could be large. Should we summarize issues before sending to the triage agent, or use a model with a large context window? Recommendation: summarize to ~200 chars per issue, include full detail only for top-20 by numeric score.

2. **Triage agent model choice.** Should the triage agent use a more powerful model (Opus-class) than the per-issue complexity estimator (Haiku-class)? Recommendation: yes — the triage agent makes higher-stakes decisions and benefits from better reasoning. The cost per run is bounded (one call per triage cycle, not per issue).

3. **Plan staleness.** What happens if new issues arrive while a plan is being executed? Recommendation: new issues increment the counter; if the batch threshold is hit, a new triage runs and supersedes the current plan. In-flight agent runs from the old plan are not cancelled.

4. **Relationship to numeric scoring.** Should the triage agent's ranking completely replace the numeric priority score, or coexist? Recommendation: coexist. Numeric scores are fast, deterministic, and useful for UI sorting. The triage agent's analysis is richer but slower and non-deterministic. Show both.
