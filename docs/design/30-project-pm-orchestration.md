# Design: Project-Aware PM Orchestration

**Status**: Proposal
**Authors**: PM Design
**Depends on**: [29-projects.md](29-projects.md), [06-agent-orchestrator.md](06-agent-orchestrator.md)
**Inspired by**: [OpenAI Symphony](https://github.com/openai/symphony)

---

## Executive Summary

Today, 143's PM agent operates as a **reactive triage engine** — it wakes up, looks at all open issues, creates a one-shot plan, delegates tasks, and goes back to sleep. This works for bug fixing but cannot drive **sustained, goal-oriented work** like feature development, migrations, or architectural improvements.

This document proposes two evolutionary stages for project-aware PM orchestration:

- **Model B (Iterative Planning)**: A single PM agent gains project awareness — it plans one batch of work per project per cycle, reviews results, and plans the next batch. Projects are persistent entities with goals, progress tracking, and institutional memory.

- **Model B+ (Compressed Context)**: When the number of active projects or accumulated history exceeds what fits in a single PM context window, we introduce a compression layer that summarizes project state into bounded digests, allowing the single PM to scale to 10-20+ concurrent projects.

Both models preserve the single-PM architecture (one brain, global visibility, coherent prioritization) while adding the ability to pursue multi-cycle objectives.

---

## Problem Statement

### What we have

The PM agent today runs a single function: `Analyze(orgID, trigger) → Plan`. Each Plan is:
- **Ephemeral**: born and completed in one cycle, no continuity
- **Flat**: all open issues treated as a uniform pool
- **Reactive**: driven by what's broken, not what we're building toward
- **Memoryless across projects**: the decision log captures per-issue history, but there's no concept of "we're 60% through a migration"

### What we need

Users want to say: *"Migrate all API endpoints from REST to GraphQL"* and have 143:
1. Break it into tasks
2. Execute the first batch
3. Review what worked and what didn't
4. Plan the next batch informed by results
5. Repeat until the project goal is met
6. Track progress the whole time

This requires the PM to maintain **project state across cycles** — knowing what's been done, what failed, what's next, and when to declare victory.

### Why not just use the existing projects design (doc 29)?

Doc 29 defines projects as a task container with an upfront decomposition step. The user creates a project, the PM breaks it into tasks, the user approves, and execution proceeds. This is **Model A** — plan once, execute linearly.

Model A works for well-understood work where the full task graph is knowable upfront. It breaks down when:
- The scope is exploratory ("improve performance across the payments module")
- Early tasks change the landscape for later ones
- Failures require replanning, not just retrying
- The project evolves as you learn from the codebase

Models B and B+ address this by making planning **iterative and adaptive**.

---

## Model B: Iterative Planning

### Core Concept

The PM agent runs on a regular cadence (cron or webhook-triggered). Each cycle, it:

1. **Loads all active projects** alongside open issues
2. **Reviews project progress** — what tasks completed, what failed, what changed
3. **Decides how to allocate slots** — reactive triage vs. project advancement
4. **Plans the next batch** for each active project, informed by what happened last cycle
5. **Delegates work** through the existing agent run pipeline

The key insight: **planning and execution are interleaved, not sequential**. The PM doesn't plan the whole project upfront — it plans one cycle's worth of work, observes results, and adapts.

### Lifecycle

```
User creates project (goal + scope + completion criteria)
    │
    ▼
PM cycle 1: PM reads project → explores codebase → plans first batch
    │         └─ creates project_tasks → delegates to agents
    ▼
Agents execute → produce PRs (or fail)
    │
    ▼
PM cycle 2: PM reviews results → "task 1 succeeded, task 2 failed because X"
    │         └─ plans next batch (adjusting for failures + new learnings)
    │         └─ may revise project scope or approach
    ▼
Agents execute → produce PRs (or fail)
    │
    ▼
PM cycle N: PM evaluates completion criteria
    │         └─ "all GraphQL endpoints migrated, old REST handlers removed"
    │         └─ marks project as completed
    ▼
Project done
```

### How this differs from Model A (doc 29)

| Aspect | Model A (doc 29) | Model B (this doc) |
|--------|------------------|-------------------|
| Planning | Once, upfront | Every cycle, iterative |
| Task graph | Fixed at plan time | Evolves each cycle |
| Failure handling | Retry or skip | Replan with new approach |
| PM involvement | Plan phase only | Every cycle |
| Completion | All tasks done | PM evaluates goal criteria |
| Learning | None across tasks | Results inform next batch |
| Scope changes | Manual user edits | PM can propose scope changes |

### Data Model Changes

#### New: `projects` table

Extends the schema from doc 29 with fields for iterative PM orchestration:

```sql
CREATE TABLE projects (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID NOT NULL REFERENCES organizations(id),
    repository_id       UUID NOT NULL REFERENCES repositories(id),

    -- User-defined
    title               TEXT NOT NULL,
    goal                TEXT NOT NULL,           -- what success looks like
    scope               TEXT,                    -- what's in/out of scope
    completion_criteria TEXT,                    -- how PM knows we're done

    -- Lifecycle
    status              TEXT NOT NULL DEFAULT 'draft',
        -- draft | planning | active | paused | completed | abandoned
    priority            INT NOT NULL DEFAULT 50, -- 0=highest, 100=lowest

    -- Execution config
    execution_mode      TEXT NOT NULL DEFAULT 'sequential',
        -- sequential | parallel | dependency_graph
    max_concurrent      INT NOT NULL DEFAULT 2,
    base_branch         TEXT NOT NULL DEFAULT 'main',

    -- PM memory (the critical addition for Model B)
    current_phase       TEXT,                    -- PM's description of where we are
    lessons_learned     JSONB DEFAULT '[]',      -- structured lessons from past cycles
    approach_history    JSONB DEFAULT '[]',      -- what approaches were tried and outcomes

    -- Progress (denormalized for fast reads)
    total_tasks         INT NOT NULL DEFAULT 0,
    completed_tasks     INT NOT NULL DEFAULT 0,
    failed_tasks        INT NOT NULL DEFAULT 0,

    -- Ownership
    created_by          UUID REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ
);

CREATE INDEX idx_projects_org_status ON projects(org_id, status);
CREATE INDEX idx_projects_org_priority ON projects(org_id, priority);
```

#### New: `project_tasks` table

Each task belongs to a project and maps to an agent run. Tasks are created incrementally by the PM each cycle — NOT all at once.

```sql
CREATE TABLE project_tasks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id),
    org_id          UUID NOT NULL REFERENCES organizations(id),

    -- Task definition (written by PM each cycle)
    title           TEXT NOT NULL,
    description     TEXT,
    approach        TEXT,             -- code-grounded guidance
    reasoning       TEXT,             -- why this task, why now

    -- Ordering and dependencies
    sort_order      INT NOT NULL DEFAULT 0,
    depends_on      UUID[],           -- task IDs that must complete first
    batch_number    INT NOT NULL,     -- which PM cycle created this task

    -- Status
    status          TEXT NOT NULL DEFAULT 'pending',
        -- pending | blocked | queued | running | completed | failed | skipped
    complexity      TEXT,             -- trivial | simple | moderate | complex
    confidence      TEXT,             -- high | medium | low

    -- Execution links
    agent_run_id    UUID REFERENCES agent_runs(id),
    issue_id        UUID REFERENCES issues(id),  -- optional: task may address an issue
    branch_name     TEXT,
    pr_url          TEXT,

    -- PM reflection (filled after agent run completes)
    outcome_notes   TEXT,             -- PM's assessment of what happened
    retry_count     INT NOT NULL DEFAULT 0,
    max_retries     INT NOT NULL DEFAULT 2,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX idx_project_tasks_project ON project_tasks(project_id, sort_order);
CREATE INDEX idx_project_tasks_status ON project_tasks(project_id, status);
CREATE INDEX idx_project_tasks_batch ON project_tasks(project_id, batch_number);
```

#### New: `project_cycles` table

Records each PM planning cycle for a project. This is the audit trail — what the PM decided each cycle and why.

```sql
CREATE TABLE project_cycles (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id),
    org_id          UUID NOT NULL REFERENCES organizations(id),
    pm_plan_id      UUID REFERENCES pm_plans(id),  -- link to the PM plan that included this

    cycle_number    INT NOT NULL,
    analysis        TEXT NOT NULL,     -- PM's assessment of project state this cycle
    decisions       JSONB NOT NULL,    -- what tasks were created/skipped/deferred and why
    progress_pct    INT,              -- PM's estimate of overall completion (0-100)

    -- Context snapshot (for debugging/auditing)
    tasks_completed_this_cycle  INT NOT NULL DEFAULT 0,
    tasks_failed_this_cycle     INT NOT NULL DEFAULT 0,
    tasks_created_this_cycle    INT NOT NULL DEFAULT 0,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_project_cycles_project ON project_cycles(project_id, cycle_number);
```

### PM Context Changes

The `PMContext` struct gains project awareness:

```go
type PMContext struct {
    // Existing reactive context
    OpenIssues        []IssueSummary            `json:"open_issues"`
    InFlightRuns      []RunSummary              `json:"in_flight_runs"`
    RecentOutcomes    []OutcomeSummary          `json:"recent_outcomes"`
    RecentPRs         []PRSummary               `json:"recent_prs"`
    PreviousDecisions []DecisionLogEntrySummary `json:"previous_decisions"`
    MaxConcurrentRuns int                       `json:"max_concurrent_runs"`
    CurrentRunCount   int                       `json:"current_run_count"`

    // New: project context
    ActiveProjects    []ProjectSummary          `json:"active_projects,omitempty"`
}

type ProjectSummary struct {
    ID                  string            `json:"id"`
    Title               string            `json:"title"`
    Goal                string            `json:"goal"`
    Scope               string            `json:"scope,omitempty"`
    CompletionCriteria  string            `json:"completion_criteria,omitempty"`
    Priority            int               `json:"priority"`
    Status              string            `json:"status"`
    ExecutionMode       string            `json:"execution_mode"`
    MaxConcurrent       int               `json:"max_concurrent"`

    // Progress
    CurrentPhase        string            `json:"current_phase,omitempty"`
    TotalTasks          int               `json:"total_tasks"`
    CompletedTasks      int               `json:"completed_tasks"`
    FailedTasks         int               `json:"failed_tasks"`
    ProgressPct         int               `json:"progress_pct,omitempty"`

    // Recent history (last 2-3 cycles)
    RecentCycles        []CycleSummary    `json:"recent_cycles,omitempty"`

    // Current state
    PendingTasks        []TaskSummary     `json:"pending_tasks,omitempty"`
    RunningTasks        []TaskSummary     `json:"running_tasks,omitempty"`
    RecentlyCompleted   []TaskSummary     `json:"recently_completed,omitempty"`
    RecentlyFailed      []TaskSummary     `json:"recently_failed,omitempty"`

    // Memory
    LessonsLearned      []string          `json:"lessons_learned,omitempty"`
    ApproachHistory     []ApproachRecord  `json:"approach_history,omitempty"`
}

type CycleSummary struct {
    CycleNumber         int               `json:"cycle_number"`
    Analysis            string            `json:"analysis"`
    TasksCreated        int               `json:"tasks_created"`
    TasksCompleted      int               `json:"tasks_completed"`
    TasksFailed         int               `json:"tasks_failed"`
    CreatedAt           string            `json:"created_at"`
}

type TaskSummary struct {
    ID                  string            `json:"id"`
    Title               string            `json:"title"`
    Status              string            `json:"status"`
    Approach            string            `json:"approach,omitempty"`
    OutcomeNotes        string            `json:"outcome_notes,omitempty"`
    Complexity          string            `json:"complexity,omitempty"`
    Confidence          string            `json:"confidence,omitempty"`
    BatchNumber         int               `json:"batch_number"`
}

type ApproachRecord struct {
    TaskTitle           string            `json:"task_title"`
    Approach            string            `json:"approach"`
    Outcome             string            `json:"outcome"` // succeeded | failed
    LessonLearned       string            `json:"lesson,omitempty"`
}
```

### PM System Prompt Changes

The PM system prompt gains a new section for project work. The full prompt structure becomes:

```
You are an AI Product Manager running a planning session...

## Your Workflow
1. Read AGENTS.md (existing)
2. Read .pm-context.json (existing — now includes ActiveProjects)
3. Explore the codebase (existing)
4. Investigate issues (existing)

## NEW: Project Planning

For each active project in your context:

1. REVIEW PROGRESS. Read the project's recent cycles, completed tasks, and
   failed tasks. Understand where the project stands.

2. ASSESS FAILURES. If tasks failed in the previous cycle:
   - Read the failure details and outcome notes
   - Determine if the approach was wrong or if it was a transient issue
   - Check approach_history — do NOT repeat failed approaches
   - Decide: retry with different approach, skip, or escalate

3. DETERMINE NEXT TASKS. Based on the project goal, current phase, and
   what's been completed:
   - What are the logical next steps?
   - Are there dependencies on completed work?
   - Has completed work changed the landscape? (e.g., a refactor moved files)
   - Explore the codebase to verify assumptions — don't plan against stale state

4. EVALUATE COMPLETION. Check the project's completion_criteria against the
   actual codebase state:
   - If criteria are met → recommend marking project as completed
   - If criteria are partially met → update current_phase and progress estimate
   - If criteria seem unreachable → flag for human review

5. RESPECT PROJECT PRIORITY. Higher-priority projects get slots first.
   If you have 3 available slots and 2 active projects (priority 10 and 50),
   the priority-10 project gets first pick.

## Slot Allocation

You must balance reactive triage (open issues) against project work.
Allocation strategy:
- If there are critical/high-severity issues → reserve at least 1 slot
- Distribute remaining slots across active projects by priority
- If no urgent issues and no active projects → use slots for reactive triage
- Never starve projects entirely — if a project has been stalled for 3+ cycles
  with no progress, flag it

## Output Format

Your output now includes a project_plans section:

<pm-plan>
{
  "analysis": "<situation analysis covering both issues and projects>",

  "slot_allocation": {
    "reactive": 1,
    "projects": {
      "<project_id>": 2,
      "<project_id>": 1
    },
    "reasoning": "<why this allocation>"
  },

  "tasks": [ ... ],           // reactive issue tasks (existing format)
  "clusters": [ ... ],        // existing
  "skip": [ ... ],            // existing

  "project_plans": [
    {
      "project_id": "<uuid>",
      "cycle_analysis": "<what happened since last cycle, what we learned>",
      "progress_pct": 45,
      "current_phase": "<updated phase description>",
      "status_recommendation": "active",  // or "completed" or "needs_human_review"
      "lessons_learned": ["<new lesson from this cycle>"],
      "new_tasks": [
        {
          "title": "<task title>",
          "description": "<detailed spec>",
          "approach": "<code-grounded guidance with file paths>",
          "reasoning": "<why this task now>",
          "depends_on": ["<task_id or null>"],
          "complexity": "moderate",
          "confidence": "high"
        }
      ],
      "skipped_tasks": [
        {
          "description": "<what we considered but didn't queue>",
          "reason": "<why not yet>"
        }
      ]
    }
  ]
}
</pm-plan>
```

### Execution Flow

#### Modified `pm.Service.Analyze`

```go
func (s *Service) Analyze(ctx context.Context, orgID uuid.UUID, trigger PMTrigger) (*Plan, error) {
    // 1. Gather existing context (issues, runs, outcomes, decisions)
    ctxBundle, err := s.gatherContext(ctx, orgID)

    // 2. NEW: Gather project context
    activeProjects, err := s.projects.ListByOrg(ctx, orgID, ProjectFilters{Status: "active"})
    for _, project := range activeProjects {
        summary := s.buildProjectSummary(ctx, project)
        ctxBundle.pmContext.ActiveProjects = append(ctxBundle.pmContext.ActiveProjects, summary)
    }

    // 3. Run PM agent (existing sandbox + prompt flow)
    plan, err := s.runPMAgent(ctx, ctxBundle)

    // 4. Execute reactive tasks (existing)
    s.executePlan(ctx, orgID, plan, ...)

    // 5. NEW: Execute project plans
    for _, projectPlan := range plan.ProjectPlans {
        s.executeProjectPlan(ctx, orgID, projectPlan, ctxBundle.settings)
    }

    return plan, nil
}

func (s *Service) executeProjectPlan(ctx context.Context, orgID uuid.UUID, pp ProjectPlan, settings OrgSettings) error {
    project, err := s.projects.GetByID(ctx, pp.ProjectID)

    // Update project memory
    project.CurrentPhase = pp.CurrentPhase
    project.LessonsLearned = appendLessons(project.LessonsLearned, pp.LessonsLearned)
    // ... update approach_history from completed task outcomes

    // Handle status recommendation
    if pp.StatusRecommendation == "completed" {
        project.Status = "completed"
        project.CompletedAt = timePtr(time.Now())
        return s.projects.Update(ctx, project)
    }

    // Record the cycle
    cycle := &ProjectCycle{
        ProjectID:   project.ID,
        OrgID:       orgID,
        CycleNumber: project.nextCycleNumber(),
        Analysis:    pp.CycleAnalysis,
        ProgressPct: pp.ProgressPct,
    }

    // Create new tasks from PM output
    for _, taskSpec := range pp.NewTasks {
        task := &ProjectTask{
            ProjectID:   project.ID,
            OrgID:       orgID,
            Title:       taskSpec.Title,
            Description: taskSpec.Description,
            Approach:    taskSpec.Approach,
            Reasoning:   taskSpec.Reasoning,
            BatchNumber: cycle.CycleNumber,
            Complexity:  taskSpec.Complexity,
            Confidence:  taskSpec.Confidence,
            Status:      "pending",
        }
        s.projectTasks.Create(ctx, task)

        // Dispatch if within slot allocation
        s.dispatchProjectTask(ctx, orgID, project, task, settings)
    }

    // Update progress counters
    s.projects.UpdateProgress(ctx, project)
    return nil
}
```

#### How task results flow back to the PM

When an agent run completes (success or failure), the existing `analyze_failure` / post-run handler updates the `agent_run` record. For project-linked runs, we add:

```go
func (s *Service) onAgentRunComplete(ctx context.Context, run *AgentRun) error {
    // Existing: update agent_run status, diff, confidence, etc.

    // NEW: if this run is linked to a project task, update the task
    if run.ProjectTaskID != nil {
        task, _ := s.projectTasks.GetByID(ctx, *run.ProjectTaskID)
        if run.Status == "completed" {
            task.Status = "completed"
            task.CompletedAt = timePtr(time.Now())
        } else {
            task.Status = "failed"
            task.OutcomeNotes = formatFailure(run)
        }
        s.projectTasks.Update(ctx, task)

        // Update project progress counters
        s.projects.UpdateProgress(ctx, task.ProjectID)
    }

    return nil
}
```

The next PM cycle picks up these results via `buildProjectSummary`, which reads the latest task statuses. The PM sees: "Task 3 (migrate /users endpoint) failed because the test fixture assumed REST response format — need to update test helpers first."

### Slot Allocation Strategy

The PM must decide how to split available agent slots between reactive work and projects. The prompt instructs it to reason about this, but we also enforce guardrails in code:

```go
func (s *Service) enforceSlotAllocation(plan *Plan, settings OrgSettings) {
    available := settings.MaxConcurrentRuns - currentRunning

    // Hard rules:
    // 1. Critical issues always get at least 1 slot (if any critical issues exist)
    // 2. Active projects always get at least 1 slot total (if any active)
    // 3. No single project can claim more than its max_concurrent setting

    // The PM's slot_allocation in the plan is a recommendation.
    // We validate and adjust if it violates these rules.
}
```

### What the PM "sees" each cycle for a project

This is the critical part — the PM needs enough context to make good decisions without being overwhelmed. For Model B, each active project includes in the context:

```json
{
  "id": "abc-123",
  "title": "Migrate REST to GraphQL",
  "goal": "All API endpoints use GraphQL. REST handlers removed. Tests pass.",
  "completion_criteria": "No files matching internal/api/handlers/rest_*.go exist. All endpoint tests use GraphQL queries.",
  "priority": 10,
  "status": "active",
  "current_phase": "Migrating read endpoints (mutations not started)",
  "total_tasks": 8,
  "completed_tasks": 5,
  "failed_tasks": 1,
  "progress_pct": 55,

  "recent_cycles": [
    {
      "cycle_number": 3,
      "analysis": "Completed users and products read endpoints. The orders endpoint failed because it depends on a custom middleware that assumes REST path params.",
      "tasks_created": 2,
      "tasks_completed": 2,
      "tasks_failed": 1,
      "created_at": "2026-03-04T10:00:00Z"
    }
  ],

  "recently_completed": [
    {"title": "Migrate /users GET to GraphQL query", "approach": "...", "outcome_notes": "Succeeded. Updated 3 files."},
    {"title": "Migrate /products GET to GraphQL query", "approach": "...", "outcome_notes": "Succeeded. Found and updated 2 downstream consumers."}
  ],

  "recently_failed": [
    {"title": "Migrate /orders GET to GraphQL query", "approach": "Replace handler in rest_orders.go:45...", "outcome_notes": "Failed: pathParamMiddleware in middleware/rest.go:23 injects params assuming chi URL params. Need to either replace middleware first or create GraphQL-compatible adapter."}
  ],

  "pending_tasks": [],

  "lessons_learned": [
    "REST path param middleware must be migrated before endpoints that use it",
    "Downstream consumers of API responses need updating too — check for internal HTTP clients"
  ],

  "approach_history": [
    {"task_title": "Migrate /orders GET", "approach": "Direct handler replacement", "outcome": "failed", "lesson": "Middleware dependency blocked it"}
  ]
}
```

### Pros of Model B

1. **Adaptive**: The PM learns from each cycle. Failed approaches inform future planning. The task graph evolves based on real results, not upfront guesses.

2. **Single PM, global view**: One PM sees all projects and all reactive issues simultaneously. It can spot cross-project dependencies, avoid conflicts, and make coherent allocation decisions.

3. **Incremental complexity**: Builds on the existing PM infrastructure. The PM agent already runs in a sandbox, reads the codebase, and produces structured plans. We're extending its context and output format, not rebuilding it.

4. **Human-compatible**: Project progress is visible and interpretable. Users see cycle-by-cycle analysis, understand why the PM made each decision, and can intervene (pause, adjust scope, add tasks) at any point.

5. **Natural failure recovery**: When a task fails, the PM doesn't just retry — it reconsiders. Maybe the approach was wrong. Maybe a prerequisite is missing. This mirrors how a human PM would react to a blocked sprint item.

### Cons of Model B

1. **PM cycle time scales with projects**: Each PM cycle must now reason about all active projects in addition to reactive issues. With 5 active projects, each with 10+ historical tasks, the PM context gets large. A single cycle might take 3-5 minutes. This is manageable but noticeable.

2. **PM context window pressure**: The PM agent runs inside a coding agent (Claude/Codex) with a finite context window. With 5 projects × (goal + scope + 10 tasks + 3 cycles of history + lessons) + 50 open issues + 20 recent outcomes, we're pushing toward 50-80K tokens of context. Viable for current models (200K windows) but not infinitely scalable.

3. **Cadence mismatch**: All projects share the same PM cadence. If Project A needs fast iteration (quick bug fixes) and Project B needs slow deliberation (architecture migration), they're both tied to the same polling interval. You can't run Project A's PM cycle every 5 minutes and Project B's every hour.

4. **Single point of failure**: If the PM agent produces a bad plan (hallucinates a task, misreads progress), it affects all projects simultaneously. One confused cycle can misallocate all slots.

5. **No parallelism in planning**: The PM reasons sequentially about each project. With 5 projects, it must think about all 5 in one pass. It can't spend more time thinking about the hard project and less on the easy one.

6. **Codebase exploration is single-repo**: The PM clones one repo. If Project A targets repo X and Project B targets repo Y, the PM can only explore one per cycle (or needs multiple sandboxes, which complicates the implementation).

---

## Model B+: Compressed Context for Scale

### When to transition from B to B+

Model B works cleanly when:
- 1-5 active projects
- <50 historical tasks per project
- <100 open issues
- Single repository per org (or dominant repo)

Model B+ becomes necessary when:
- 5-20 active projects
- Project history grows deep (50+ completed tasks, 10+ cycles)
- PM context would exceed ~60K tokens (leaving room for codebase exploration)
- Multiple repositories need project-level tracking

The transition signal is measurable: **track PM context size in tokens each cycle**. When it consistently exceeds 60K tokens, it's time for B+.

### Core Concept

Model B+ adds a **compression layer** between raw project state and the PM context. Instead of passing full task histories, cycle analyses, and approach records, we produce **bounded digests** that capture the essential information in a fixed token budget.

The PM still sees all projects. It still makes global decisions. But the per-project context is summarized rather than exhaustive.

### The Compression Pipeline

```
Raw project state (unbounded)
    │
    ▼
Digest generator (per project)
    │   - Summarizes N cycles of history into 1 paragraph
    │   - Distills task outcomes into key lessons
    │   - Compresses approach history into patterns
    │   - Preserves only actionable pending/failed tasks
    ▼
Project digest (bounded: ~2K tokens per project)
    │
    ▼
PM context (bounded: ~40K tokens for 20 projects + issues)
```

### Digest Structure

```go
type ProjectDigest struct {
    ID                 string   `json:"id"`
    Title              string   `json:"title"`
    Goal               string   `json:"goal"`
    Priority           int      `json:"priority"`
    Status             string   `json:"status"`
    ProgressPct        int      `json:"progress_pct"`

    // Compressed: single paragraph, not per-cycle breakdowns
    ProgressSummary    string   `json:"progress_summary"`
    // e.g.: "55% complete. 5 of 8 endpoints migrated to GraphQL.
    //        Last cycle: 2 succeeded, 1 failed (middleware dependency).
    //        Current blocker: REST path param middleware needs migration first."

    // Compressed: top lessons only, not full approach_history
    KeyLessons         []string `json:"key_lessons"`
    // e.g.: ["Middleware must be migrated before dependent endpoints",
    //        "Internal HTTP clients need response format updates"]

    // Only actionable items, not full history
    BlockedTasks       []string `json:"blocked_tasks,omitempty"`
    NextCandidateTasks []string `json:"next_candidate_tasks,omitempty"`

    // Flags for PM attention
    NeedsAttention     bool     `json:"needs_attention,omitempty"`
    AttentionReason    string   `json:"attention_reason,omitempty"`
    // e.g.: "Stalled for 3 cycles — same task keeps failing"

    CyclesSinceLastProgress int `json:"cycles_since_last_progress"`
}
```

### How Digests Are Generated

Two approaches, with a clear recommendation:

#### Option 1: Algorithmic compression (recommended for v1)

Deterministic Go code that reads raw project state and produces a digest:

```go
func (s *Service) buildProjectDigest(ctx context.Context, project *Project) ProjectDigest {
    tasks, _ := s.projectTasks.ListByProject(ctx, project.ID)
    cycles, _ := s.projectCycles.ListByProject(ctx, project.ID, CycleFilters{Limit: 5})

    // Progress summary: template-based
    completed := countByStatus(tasks, "completed")
    failed := countByStatus(tasks, "failed")
    total := len(tasks)

    lastCycle := cycles[0] // most recent
    progressSummary := fmt.Sprintf(
        "%d%% complete. %d of %d tasks done. Last cycle: %d succeeded, %d failed. %s",
        project.ProgressPct, completed, total,
        lastCycle.TasksCompleted, lastCycle.TasksFailed,
        summarizeBlockers(tasks, cycles),
    )

    // Key lessons: deduplicate and take top 5
    lessons := deduplicateLessons(project.LessonsLearned)
    if len(lessons) > 5 {
        lessons = lessons[:5]
    }

    // Next candidates: pending tasks not blocked
    candidates := filterPending(tasks)

    return ProjectDigest{
        // ... fill fields
    }
}
```

**Pros**: Cheap, fast, deterministic, no LLM call.
**Cons**: Template-based summaries lack nuance. Can't synthesize patterns across tasks the way an LLM can.

#### Option 2: LLM-generated digests (consider for v2)

Use a small, fast model to summarize each project's raw state into a digest:

```go
func (s *Service) buildProjectDigestLLM(ctx context.Context, project *Project) ProjectDigest {
    rawState := s.gatherFullProjectState(ctx, project)
    prompt := fmt.Sprintf(digestPromptTemplate, rawState)

    // Use a cheap model (Haiku / GPT-4o-mini) — this is compression, not planning
    result, _ := s.cheapModel.Complete(ctx, prompt)
    return parseDigest(result)
}
```

**Pros**: Better summaries, can identify non-obvious patterns, adapts language to what matters.
**Cons**: Adds latency and cost per project per cycle. Non-deterministic — digest quality varies. Adds a failure mode (model produces bad digest → PM gets bad context).

**Recommendation**: Start with Option 1. It's sufficient for the "compress history into a bounded summary" use case. Graduate to Option 2 if users report that the PM is making decisions that show poor understanding of project state — that's the signal that algorithmic summaries are losing critical nuance.

### Context Budget Management

B+ introduces an explicit token budget for PM context:

```go
const (
    PMContextBudgetTotal     = 80_000  // tokens reserved for context (out of 200K window)
    PMContextBudgetIssues    = 25_000  // reactive issues
    PMContextBudgetProjects  = 40_000  // all project digests combined
    PMContextBudgetHistory   = 10_000  // decision log, recent outcomes
    PMContextBudgetReserve   = 5_000   // buffer
)

func (s *Service) buildBudgetedContext(ctx context.Context, orgID uuid.UUID) (*PMContext, error) {
    // 1. Build all project digests
    digests := s.buildAllDigests(ctx, orgID)

    // 2. Sort by priority (lower number = higher priority)
    sort.Slice(digests, func(i, j int) bool {
        return digests[i].Priority < digests[j].Priority
    })

    // 3. Pack digests within budget
    packed := []ProjectDigest{}
    tokensUsed := 0
    for _, d := range digests {
        size := estimateTokens(d)
        if tokensUsed + size > PMContextBudgetProjects {
            // Overflow projects get ultra-compressed: just ID + title + progress%
            packed = append(packed, ultraCompress(d))
        } else {
            packed = append(packed, d)
            tokensUsed += size
        }
    }

    // 4. Build issue summaries within their budget
    issues := s.buildIssueSummaries(ctx, orgID, PMContextBudgetIssues)

    // 5. Assemble final context
    return &PMContext{
        OpenIssues:     issues,
        ActiveProjects: packed,
        // ...
    }, nil
}
```

### What gets cut when context overflows

Priority order for what stays in context (highest priority first):

1. **Active project digests** (sorted by project priority) — the PM must know about its projects
2. **Critical/high-severity issues** — production fires
3. **In-flight runs** — avoid duplicate work
4. **Recent failures** — avoid repeating mistakes
5. **Medium/low-severity issues** — reactive triage
6. **Decision history** — institutional memory (first to compress)

When something gets cut, it doesn't disappear — it's summarized at a higher level:

```json
{
  "overflow_summary": {
    "additional_projects": 3,
    "additional_projects_note": "3 lower-priority projects not shown (priorities 70-90). All making steady progress.",
    "additional_issues": 47,
    "additional_issues_note": "47 low-severity issues not shown. No critical patterns detected."
  }
}
```

### Pros of Model B+

1. **Scales to many projects**: 20 active projects at ~2K tokens each = 40K tokens. Well within budget. Model B would need 100K+ tokens for the same projects with full history.

2. **Preserves global coherence**: Still one PM, still one brain. The PM can reason about cross-project tradeoffs, even with compressed context.

3. **Predictable performance**: PM cycle time is bounded because context size is bounded. No surprise 10-minute PM runs because a project accumulated 200 tasks.

4. **Graceful degradation**: When projects overflow the budget, they degrade to ultra-compressed summaries rather than being dropped entirely. The PM still knows they exist.

5. **Observable**: Token budgets and compression ratios are measurable. You can monitor "how much context is the PM losing?" and tune thresholds.

### Cons of Model B+

1. **Information loss is real**: Compression means the PM doesn't see every detail. It might miss a subtle pattern in task failures that a full-context PM would catch. The algorithmic summarizer decides what matters — and it might decide wrong.

2. **Tuning complexity**: Token budgets, compression strategies, priority cutoffs — these are all knobs that need tuning. Wrong settings produce either a PM that's flying blind or one that's drowning in context.

3. **Two layers of summarization**: The digest summarizes raw state. The PM then summarizes the digest into a plan. Two levels of lossy compression compound errors. If the digest says "steady progress" but the raw data shows a concerning failure pattern, the PM can't catch it.

4. **Digest staleness**: If digests are generated at the start of a PM cycle, they reflect state at that instant. If an agent run completes mid-cycle, the digest is stale. This is minor (the next cycle picks it up) but worth noting.

5. **Testing difficulty**: How do you test that a compression pipeline preserves the "right" information? Unit tests can check format, but the semantic quality of a summary is hard to assert programmatically.

6. **Harder to debug PM decisions**: When the PM makes a surprising decision, you need to check both the digest and the raw state to understand whether the PM misunderstood or the digest was lossy. Adds a debugging layer.

---

## Comparison: Model B vs. Model B+

| Dimension | Model B | Model B+ |
|-----------|---------|----------|
| **Max projects** | ~5 comfortably, ~10 with strain | 20+ comfortably |
| **Context per project** | Full history (~5-15K tokens) | Digest (~1-2K tokens) |
| **PM cycle time** | Grows with project count/history | Bounded by token budget |
| **Information fidelity** | Full — PM sees everything | Lossy — PM sees summaries |
| **Implementation effort** | Moderate (extend PM context + prompt) | Higher (+ compression pipeline + budget management) |
| **Debugging** | Straightforward — check PM context | Two-layer — check digest + raw state |
| **Failure modes** | PM overwhelmed by large context | PM misled by lossy digest |
| **Cadence flexibility** | All projects same cadence | Same (would need Model C for per-project cadence) |
| **Cross-project reasoning** | Excellent (sees all details) | Good (sees summaries, may miss nuance) |
| **New tables** | projects, project_tasks, project_cycles | Same + digest cache (optional) |
| **Prompt changes** | Add project section to PM prompt | Same + budget overflow handling |

---

## Recommended Implementation Path

### Phase 1: Model B Core (target: first)

**Goal**: Projects exist, PM plans iteratively, tasks execute through existing pipeline.

1. **Migration**: Create `projects`, `project_tasks`, `project_cycles` tables
2. **Models + Stores**: `Project`, `ProjectTask`, `ProjectCycle` with CRUD
3. **API**: Project CRUD endpoints + task management
4. **PM Context**: Add `ActiveProjects` to `PMContext`, implement `buildProjectSummary`
5. **PM Prompt**: Extend system prompt with project planning section
6. **PM Output Parsing**: Parse `project_plans` from PM output
7. **Execution**: `executeProjectPlan` creates tasks, dispatches to agents
8. **Feedback Loop**: Agent run completion updates project task status
9. **Frontend**: Project list page, project detail with task board

**Exit criteria**: A user can create a project, the PM breaks it into tasks across multiple cycles, agents execute tasks, and the PM adapts based on results.

### Phase 2: Model B Polish

**Goal**: Robust slot allocation, dependency handling, progress tracking.

1. **Slot allocation enforcement**: Validate PM's allocation against guardrails
2. **Dependency graph execution**: Respect `depends_on` in task dispatch
3. **Branch management**: Per-task branches with stacking for sequential mode
4. **Completion detection**: PM evaluates completion criteria against codebase
5. **Project memory**: `lessons_learned` and `approach_history` accumulate and feed back
6. **Cycle history**: `project_cycles` table populated and used in context
7. **Frontend**: Progress visualization, cycle timeline, slot allocation dashboard

**Exit criteria**: Projects with inter-task dependencies execute correctly. The PM demonstrably avoids repeating failed approaches. Projects complete when goals are met.

### Phase 3: Model B+ Compression (when needed)

**Signal to start**: PM context consistently exceeds 60K tokens OR org has 5+ active projects with 30+ tasks each.

1. **Context budget system**: Token estimation + budgets per category
2. **Digest generator**: Algorithmic summarizer for projects
3. **Overflow handling**: Ultra-compressed fallback for low-priority projects
4. **Monitoring**: Track context size, compression ratios, digest quality
5. **Budget tuning**: Expose knobs for ops (or auto-tune based on model context window)

**Exit criteria**: PM operates coherently with 15+ active projects. Context size stays within budget. No measurable degradation in PM decision quality vs. Model B (measured by task success rates).

### Phase 4: Model B+ LLM Digests (optional)

**Signal to start**: Users report PM making decisions that show poor understanding of project state, AND algorithmic digests are identified as the bottleneck.

1. **Digest model**: Use cheap model (Haiku/4o-mini) to generate natural-language digests
2. **Digest caching**: Cache digests, invalidate on state change
3. **Quality monitoring**: Compare LLM digest decisions vs. full-context decisions on a sample
4. **Fallback**: If LLM digest fails, fall back to algorithmic

---

## Multi-Repo Considerations

Both Model B and B+ assume the PM clones one repository per cycle. For multi-repo projects:

### Short-term (Phase 1-2)
- Each project is scoped to one repository (enforced by `repository_id` FK)
- Multi-repo initiatives are modeled as separate projects per repo
- The PM sees all projects and can coordinate across them at the planning level (even if it can't explore both codebases in one cycle)

### Medium-term (Phase 3+)
- The PM could clone multiple repos into the same sandbox (one per `/workspace/<repo-name>/`)
- Adds complexity but is mechanically straightforward
- Token budget becomes more important since exploring multiple repos is expensive

### Long-term (Model C territory)
- Per-repo PM agents with a meta-scheduler
- Only pursue if multi-repo projects are a primary use case AND the single PM is the bottleneck

---

## Risk Analysis

### Risk: PM produces incoherent project plans

**Likelihood**: Medium
**Impact**: High — agents execute bad tasks, waste slots
**Mitigation**:
- Human approval gate on first cycle of each project (required)
- Optional auto-approve for subsequent cycles if success rate > 80%
- Confidence scoring on project tasks (same as reactive tasks)
- Circuit breaker: if a project has 3 consecutive failed cycles, auto-pause and notify

### Risk: Slot starvation for reactive work

**Likelihood**: Medium — aggressive projects consume all slots
**Impact**: Medium — critical production issues wait
**Mitigation**:
- Hard reserve: at least 1 slot for critical/high issues (code-enforced, not PM-discretion)
- PM prompt explicitly prioritizes production fires over project work
- Alert if reactive queue depth exceeds threshold while projects consume all slots

### Risk: Project scope creep via PM

**Likelihood**: Medium — PM might expand scope beyond user intent
**Impact**: Low-medium — wasted agent runs
**Mitigation**:
- Scope field is user-defined and read-only by PM
- PM can *recommend* scope changes but cannot modify the project
- UI shows "PM suggests expanding scope to include X" — user must approve

### Risk: Context window overflow (Model B)

**Likelihood**: High for orgs with many projects
**Impact**: Medium — PM context gets truncated, decisions degrade
**Mitigation**:
- This is exactly what Model B+ solves
- For Model B: cap active projects at 5 per org (warn, don't block)
- Monitor context size per cycle, alert when approaching limits

### Risk: Compression loses critical information (Model B+)

**Likelihood**: Medium
**Impact**: Medium — PM misses patterns, repeats mistakes
**Mitigation**:
- `NeedsAttention` flag in digests for anomalous patterns (stalled projects, repeated failures)
- Full project state always available in DB for human review
- A/B testing: run both full-context and digest-context PM analyses, compare decisions
- Gradual rollout: start with algorithmic digests, monitor task success rates

---

## Open Questions

1. **Should projects auto-pause after N consecutive failures?** Proposed: yes, at 3. But should it be configurable per project?

2. **Can the PM create projects?** Today, only users create projects. Should the PM be able to say "I see 5 related issues that should be a project" and auto-create one? Probably yes in the long term, but keep it human-only for v1 to build trust.

3. **How do projects interact with Linear/Sentry sync?** If a project task maps to a Linear issue, should task completion update Linear? Probably yes — bidirectional sync. But that's a separate integration concern.

4. **What's the right PM cycle cadence for projects?** Reactive triage might want 15-minute cycles. Projects might want hourly. For Model B, they share a cadence. Is this a real problem? (Probably not until scale.)

5. **Should project tasks have their own confidence gating?** Today, reactive tasks respect the org's confidence threshold. Should project tasks have a different threshold? (e.g., "I trust the PM more for project work because it has better context")

---

## Appendix: Symphony Comparison

| Concept | Symphony | 143 Model B/B+ |
|---------|----------|-----------------|
| Project definition | Implicit (Linear board) | Explicit (goal + scope + criteria) |
| Task source | Issues on the board | PM-generated per cycle |
| Orchestrator | Polling daemon per board | Single PM agent, multi-project |
| Planning | None — issues arrive pre-planned | PM plans iteratively each cycle |
| Per-issue isolation | Filesystem workspace | Docker + gVisor sandbox |
| Workflow config | `WORKFLOW.md` in repo | Org settings + `AGENTS.md` |
| Progress tracking | Issue state on board | project_cycles + task statuses |
| Failure recovery | Exponential backoff retry | PM replans with new approach |
| Cross-project awareness | None (separate instances) | Full (single PM sees all) |
| Completion | Issue moved to terminal state | PM evaluates criteria against codebase |
| Memory/learning | None | lessons_learned, approach_history, decision_log |

The key philosophical difference: Symphony trusts the human to manage the project board and just automates execution. Model B/B+ trusts the PM agent to manage the project plan and automates both planning and execution, with human oversight at project boundaries (creation, approval, completion review).
