# Design: Projects — Iterative Multi-Task Agent Orchestration

> **Status:** Implemented | **Last reviewed:** 2026-05-06

**Depends on**: [06-agent-orchestrator.md](06-agent-orchestrator.md)
**Inspired by**: [OpenAI Symphony](https://github.com/openai/symphony)

---

## 1. Problem Statement

Today, 143 operates at the granularity of individual issues:

1. Issues arrive from Sentry/Linear/support tickets
2. The PM agent analyzes them, clusters, and prioritizes
3. Coding agents fix issues **one at a time**, each producing a single PR
4. Users must monitor each agent run and review each PR individually

This works well for reactive bug fixing, but breaks down for **proactive, multi-task work** — the kind of thing a team would put on a Linear board or a Jira epic:

- "Migrate all API endpoints from REST to GraphQL"
- "Add comprehensive error handling across the payments module"
- "Upgrade from React 17 to React 18 across the codebase"
- "Implement the new notification system (backend + frontend + tests)"

These are **projects** — scoped bodies of work with multiple interdependent tasks that should be completed as a cohesive unit. Users want to define the goal, let 143 break it down, and come back to a set of completed PRs ready for review — not babysit 15 individual agent runs.

### What the PM agent lacks today

The PM agent runs a single function: `Analyze(orgID, trigger) → Plan`. Each Plan is:

- **Ephemeral**: born and completed in one cycle, no continuity
- **Flat**: all open issues treated as a uniform pool
- **Reactive**: driven by what's broken, not what we're building toward
- **Memoryless across objectives**: the decision log captures per-issue history, but there's no concept of "we're 60% through a migration"

Users want to say: *"Migrate all API endpoints from REST to GraphQL"* and have 143:

1. Break it into tasks
2. Execute the first batch
3. Review what worked and what didn't
4. Plan the next batch informed by results
5. Repeat until the project goal is met
6. Track progress the whole time

This requires the PM to maintain **project state across cycles** — knowing what's been done, what failed, what's next, and when to declare victory.

---

## 2. Core Concepts

### What is a project?

A project is a **persistent, goal-oriented container** that spans multiple PM cycles. It has:

- A **goal** (what success looks like)
- A **scope** (what's in and out of bounds)
- **Completion criteria** (how the PM knows when to stop)
- **Tasks** (created incrementally by the PM, not all upfront)
- **Memory** (lessons learned, approach history, cycle-by-cycle progress)

Projects are to PM plans what epics are to stories. A PM plan is a snapshot of one analysis cycle; a project is a persistent scope that may span dozens of cycles.

### How projects differ from reactive issue triage

| Aspect | Reactive triage | Projects |
|--------|----------------|----------|
| Source | External (Sentry, Linear, support) | User-defined goal |
| Planning | One-shot per cycle | Iterative, across cycles |
| Task creation | PM analyzes existing issues | PM decomposes goal into tasks |
| Memory | Decision log (per-issue) | Project-level lessons + approach history |
| Completion | Issue marked fixed | PM evaluates goal criteria against codebase |
| Scope | Whatever issues arrived | Bounded by user-defined scope |

### Iterative planning (why not plan everything upfront?)

The PM does **not** decompose the entire project into tasks on day one. Instead, it plans one batch at a time:

```
Project created (with goal + scope + completion criteria)
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
    │         └─ may revise approach based on what it found in the code
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

Why iterative? Because real projects change as you learn:

- The scope is often exploratory ("improve performance across the payments module")
- Early tasks change the landscape for later ones (a refactor moves files)
- Failures require replanning, not just retrying
- The project evolves as the PM reads actual code and discovers reality

An upfront plan is wrong by step 3. Iterative planning lets the PM adapt.

---

## 3. Data Model

### `projects` table

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
        -- proposed | draft | planning | active | paused | completed | cancelled
    priority            INT NOT NULL DEFAULT 50, -- 0=highest, 100=lowest

    -- Execution config
    execution_mode      TEXT NOT NULL DEFAULT 'sequential',
        -- sequential | parallel | dependency_graph
    max_concurrent      INT NOT NULL DEFAULT 2,
    auto_merge          BOOLEAN NOT NULL DEFAULT false,
    base_branch         TEXT NOT NULL DEFAULT 'main',

    -- PM memory (accumulated across cycles)
    current_phase       TEXT,                    -- PM's description of where we are
    lessons_learned     JSONB DEFAULT '[]',      -- structured lessons from past cycles
    approach_history    JSONB DEFAULT '[]',      -- what approaches were tried and outcomes

    -- Progress (denormalized for fast reads)
    total_tasks         INT NOT NULL DEFAULT 0,
    completed_tasks     INT NOT NULL DEFAULT 0,
    failed_tasks        INT NOT NULL DEFAULT 0,

    -- Provenance (for PM-proposed projects)
    proposed_by_pm      BOOLEAN NOT NULL DEFAULT false,
    source_issue_ids    UUID[],               -- issues that motivated a PM proposal
    proposal_reasoning  TEXT,                 -- PM's justification for the proposal

    -- Ownership
    created_by          UUID REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ
);

CREATE INDEX idx_projects_org_status ON projects(org_id, status);
CREATE INDEX idx_projects_org_priority ON projects(org_id, priority);
```

### `project_tasks` table

Tasks are created **incrementally** by the PM each cycle — not all at once. Each task maps to one agent run.

```sql
CREATE TABLE project_tasks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id),
    org_id          UUID NOT NULL REFERENCES organizations(id),

    -- Task definition (written by PM each cycle)
    title           TEXT NOT NULL,
    description     TEXT,             -- detailed task spec for the coding agent
    approach        TEXT,             -- code-grounded guidance with file paths
    reasoning       TEXT,             -- why this task, why now

    -- Ordering and dependencies
    sort_order      INT NOT NULL DEFAULT 0,
    depends_on      UUID[],           -- task IDs that must complete first
    batch_number    INT NOT NULL,     -- which PM cycle created this task

    -- Status
    status          TEXT NOT NULL DEFAULT 'pending',
        -- pending | blocked | delegated | running | completed | failed | skipped | cancelled
        -- Maps to existing statuses: agent_runs use pending/running/completed/failed/cancelled/skipped;
        -- PM tasks use pending/delegated/skipped_capacity. We add 'blocked' (dependencies unmet).
    complexity      TEXT,             -- trivial | simple | moderate | complex  (matches PMTaskComplexity)
    confidence      TEXT,             -- high | medium | low  (matches PMTaskConfidence)

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

### `session_pm_context` table

Project and PM execution context is linked to normal sessions without widening the core `sessions` row. The session API still exposes `pm_plan_id`, `pm_approach`, `pm_reasoning`, and `project_task_id` for compatibility, but the store hydrates them from `session_pm_context`.

```sql
CREATE TABLE session_pm_context (
    session_id      UUID PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    org_id          UUID NOT NULL REFERENCES organizations(id),
    pm_plan_id      UUID REFERENCES pm_plans(id),
    pm_approach     TEXT,
    pm_reasoning    TEXT,
    project_task_id UUID REFERENCES project_tasks(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_session_pm_context_pm_plan
    ON session_pm_context (org_id, pm_plan_id)
    WHERE pm_plan_id IS NOT NULL;

CREATE INDEX idx_session_pm_context_project_task
    ON session_pm_context (org_id, project_task_id)
    WHERE project_task_id IS NOT NULL;
```

### `project_cycles` table

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

---

## 4. API Surface

```
POST   /api/v1/projects                         -- create a new project
GET    /api/v1/projects                         -- list projects (filterable by status)
GET    /api/v1/projects/{id}                    -- get project with tasks + cycles
PATCH  /api/v1/projects/{id}                    -- update project settings/scope
DELETE /api/v1/projects/{id}                    -- cancel a project
POST   /api/v1/projects/{id}/approve            -- approve a PM-proposed project (proposed → draft)
POST   /api/v1/projects/{id}/dismiss            -- dismiss a PM-proposed project (proposed → cancelled)
POST   /api/v1/projects/{id}/start             -- transition draft → active (first PM cycle)
POST   /api/v1/projects/{id}/pause             -- pause execution
POST   /api/v1/projects/{id}/resume            -- resume execution

POST   /api/v1/projects/{id}/tasks             -- add a task manually
PATCH  /api/v1/projects/{id}/tasks/{tid}       -- update a task
DELETE /api/v1/projects/{id}/tasks/{tid}       -- remove a task
POST   /api/v1/projects/{id}/tasks/{tid}/retry -- retry a failed task

GET    /api/v1/projects/{id}/cycles            -- list PM cycles for a project
GET    /api/v1/projects/{id}/cycles/{cid}      -- cycle detail (analysis + decisions)
```

### Creating a project

```json
POST /api/v1/projects
{
  "title": "Migrate REST to GraphQL",
  "goal": "All API endpoints use GraphQL. REST handlers removed. Tests pass.",
  "scope": "Only internal/api/handlers/ — external integrations are out of scope.",
  "completion_criteria": "No files matching internal/api/handlers/rest_*.go exist. All endpoint tests use GraphQL queries.",
  "execution_mode": "dependency_graph",
  "max_concurrent": 2,
  "base_branch": "main",
  "priority": 10
}
```

The project starts in `draft` status. Calling `POST /projects/{id}/start` transitions it to `active`, which signals the PM agent to begin planning for it on the next cycle.

---

## 5. PM Integration — Iterative Planning

This is the heart of the design: how the PM agent gains project awareness and plans iteratively across cycles.

### How the PM sees projects

The `PMContext` struct gains an `ActiveProjects` field. Each active project is represented as a summary containing its goal, current state, recent history, and actionable information:

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

    // Project context
    ActiveProjects    []ProjectSummary          `json:"active_projects,omitempty"`
}
```

Each `ProjectSummary` provides the PM with enough context to plan the next batch:

```go
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

### Example: what the PM sees for one project

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

### PM system prompt additions

The PM system prompt gains a new section for project work. Added after the existing reactive workflow:

```
## Project Planning

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

The system already enforces a `max_concurrent_runs` limit per org (default: 3,
configured in OrgSettings, editable on the Agent settings page at `/agent`).
Each agent run creates a Docker + gVisor sandbox, so this limit exists for
practical resource reasons. The PM is already told how many slots are available
(`available_slots`) and caps task delegation accordingly (see `pm/execute.go`).

Users who adopt projects may want to increase `max_concurrent_runs` (currently
capped at 10 in the UI) to give the PM more room to run project tasks alongside
reactive work. The existing Agent settings page is the right place for this —
no new settings surface needed.

With projects, the PM must now split those same available slots between
reactive issue triage and project work:
- If there are critical/high-severity issues → reserve at least 1 slot
- Distribute remaining slots across active projects by priority
- If no urgent issues and no active projects → use slots for reactive triage
- Never starve projects entirely — if a project has been stalled for 3+ cycles
  with no progress, flag it
```

### PM output format

The PM output gains `slot_allocation` and `project_plans` sections:

```json
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

  "tasks": [ ... ],           // reactive issue tasks (existing format, unchanged)
  "clusters": [ ... ],        // existing, unchanged
  "skip": [ ... ],            // existing, unchanged

  "proposed_projects": [           // NEW: PM summarizes projects it proposed during the run
    {
      "repository_id": "<uuid>",
      "title": "Standardize error handling in payments",
      "goal": "All payment endpoints use consistent error types and HTTP codes",
      "scope": "internal/api/handlers/payment*.go",
      "completion_criteria": "All payment handlers return PaymentError types. No raw http.Error calls.",
      "reasoning": "Issues #42, #47, #51 all stem from inconsistent error handling in payments",
      "source_issue_ids": ["<uuid>", "<uuid>", "<uuid>"],
      "suggested_priority": 30
    }
  ],

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
```

### Slot allocation strategy

Today, `pm/execute.go` computes `available = settings.MaxConcurrentRuns - running` and delegates tasks until `available` is exhausted. With projects, the PM must reason about how to split those same available slots between reactive work and project work. The prompt instructs it to reason about this, and we enforce guardrails in code:

```go
func (s *Service) enforceSlotAllocation(plan *Plan, settings OrgSettings, hasActiveProjects bool) {
    available := settings.MaxConcurrentRuns - currentRunning

    // Hard rules (code-enforced, not PM-discretion):
    // 1. Critical issues always get at least 1 slot (if any critical issues exist)
    // 2. Active projects always get at least 1 slot total (if any active)
    // 3. No single project can claim more than its max_concurrent setting

    // The PM's slot_allocation in the plan is a recommendation.
    // We validate and adjust if it violates these rules.
}
```

---

## 6. Execution Pipeline

### Modified `pm.Service.Analyze`

The existing Analyze method gains project awareness:

```go
func (s *Service) Analyze(ctx context.Context, orgID uuid.UUID, trigger PMTrigger) (*Plan, error) {
    // 1. Gather existing context (issues, runs, outcomes, decisions)
    ctxBundle, err := s.gatherContext(ctx, orgID)

    // 2. Gather project context
    activeProjects, err := s.projects.ListByOrg(ctx, orgID, ProjectFilters{Status: "active"})
    for _, project := range activeProjects {
        summary := s.buildProjectSummary(ctx, project)
        ctxBundle.pmContext.ActiveProjects = append(ctxBundle.pmContext.ActiveProjects, summary)
    }

    // 3. Run PM agent (existing sandbox + prompt flow)
    plan, err := s.runPMAgent(ctx, ctxBundle)

    // 4. Execute reactive tasks (existing)
    s.executePlan(ctx, orgID, plan, ...)

    // 5. Execute project plans
    for _, projectPlan := range plan.ProjectPlans {
        s.executeProjectPlan(ctx, orgID, projectPlan, ctxBundle.settings)
    }

    // 6. PM-proposed projects are created during the run via an internal CLI
    // tool. The final plan may still summarize those proposals for auditability,
    // but is not the canonical mutation path.

    return plan, nil
}
```

### Project plan execution

```go
func (s *Service) executeProjectPlan(ctx context.Context, orgID uuid.UUID, pp ProjectPlan, settings OrgSettings) error {
    project, err := s.projects.GetByID(ctx, pp.ProjectID)

    // Update project memory
    project.CurrentPhase = pp.CurrentPhase
    project.LessonsLearned = appendLessons(project.LessonsLearned, pp.LessonsLearned)

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

### Execution modes

**Sequential**: Tasks run one at a time in `sort_order`. Each task's PR is merged (or approved) before the next starts. The next agent sees the cumulative codebase changes. Best for refactoring chains.

**Parallel**: Up to `max_concurrent` tasks run simultaneously, each on its own branch forked from `base_branch`. Creates independent PRs. Best for unrelated work. Merge conflicts are detected and flagged.

**Dependency Graph**: Tasks declare `depends_on` relationships. The orchestrator runs tasks whose dependencies are all completed, up to `max_concurrent`. A topological sort determines the execution order. Best for complex features with mixed independent and dependent work.

### Branch strategy

Each task gets its own branch: `143/project-{short-id}/{task-number}-{slug}`.

- **Sequential mode**: each subsequent task branches from the previous task's branch (stacked PRs)
- **Parallel mode**: all tasks branch from `base_branch`
- **Dependency graph mode**: a task branches from the merge of its dependency branches

### How task results flow back to the PM

When an agent run completes (success or failure), the existing post-run handler updates the `agent_run` record. For project-linked runs, we add:

```go
func (s *Service) onAgentRunComplete(ctx context.Context, run *AgentRun) error {
    // Existing: update agent_run status, diff, etc.

    // If this run is linked to a project task, update the task
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

The next PM cycle picks up these results via `buildProjectSummary`, which reads the latest task statuses. The PM sees: "Task 3 (migrate /orders endpoint) failed because the test fixture assumed REST response format — need to update test helpers first."

### Failure handling

- **Task failure**: The task is marked `failed` with `outcome_notes` explaining what happened. The PM sees this on the next cycle and can either retry with a different approach or create a prerequisite task.
- **Repeated failure**: If a task has been retried `max_retries` times, it's marked `skipped`. The PM must work around it or flag for human review.
- **Project stall**: If a project has 3+ consecutive cycles with no progress (no tasks completed), the PM flags it with `status_recommendation: "needs_human_review"`.

### Context passing between tasks

Each task's agent prompt includes:

- The project goal and scope (so the agent understands the bigger picture)
- The task's specific description and approach
- Summaries of completed sibling tasks (so the agent knows what's already been done)
- Summaries of failed sibling tasks (so the agent avoids the same mistakes)

This gives each coding agent awareness of the broader project without requiring it to re-read the entire history.

---

## 7. Completion Detection

The PM evaluates completion criteria each cycle. This is not just "all tasks done" — it's "the goal is actually met":

1. **PM reads the completion criteria**: e.g., "No files matching `internal/api/handlers/rest_*.go` exist"
2. **PM explores the codebase**: Runs `ls`, reads files, checks test output
3. **PM evaluates**: Are the criteria met?
   - If yes → `status_recommendation: "completed"`
   - If partially → update `progress_pct` and `current_phase`
   - If unreachable → `status_recommendation: "needs_human_review"` with explanation

This means a project can complete even if not all originally-envisioned tasks were needed (the PM found a shortcut), or can remain active even after all current tasks pass (the PM discovers more work is needed).

---

## 8. PM-Proposed Projects

The PM can propose projects when it spots patterns in the issue stream that suggest a cohesive body of work. For example:

- 5 issues all about inconsistent error handling in the payments module → PM proposes "Standardize error handling in payments"
- 3 issues about REST endpoint bugs + a feature request for new endpoints → PM proposes "Migrate payments API to GraphQL"
- Repeated test failures in the same subsystem → PM proposes "Fix flaky tests in notification service"

### How it works

The PM system prompt includes instructions for project proposal:

```
## Project Proposals

When you see a cluster of related issues that share a root cause or would
benefit from a coordinated fix rather than individual patches, you may
propose a project. Include in your plan output:

"proposed_projects": [
  {
    "title": "...",
    "goal": "...",
    "scope": "...",
    "completion_criteria": "...",
    "reasoning": "Why these issues are better addressed as a project than individually",
    "source_issue_ids": ["..."],  // the issues that motivated this proposal
    "suggested_priority": 30
  }
]

Only propose a project when:
- 3+ related issues would benefit from coordinated work
- The work requires a shared approach or has ordering dependencies
- Individual fixes would create inconsistency or duplicate effort
```

### Lifecycle

```
PM proposes project (status: proposed)
    ↓
User sees proposal in Projects page with "Proposed" badge
    ↓
User reviews: Approve → status becomes draft (user can edit goal/scope)
         or: Dismiss → status becomes cancelled
    ↓
User starts project → status becomes active → normal project lifecycle
```

### Key rules

- **No work happens on proposed projects.** Status `proposed` means the PM will not plan tasks for it or allocate slots to it. It's purely a suggestion.
- **The PM can reference proposed projects.** If the PM sees a new issue that relates to a proposed project, it can note "this issue is related to proposed project X" in its analysis, reinforcing the case for approval.
- **Proposed projects appear in the Projects list** with a distinct visual treatment (e.g., dashed border, "Proposed by PM" label, Approve/Dismiss buttons).
- **The PM should not spam proposals.** The prompt instructs it to propose only when the signal is strong (3+ related issues, clear shared root cause). We can add a rate limit in code if needed (e.g., max 2 proposals per PM cycle).

### Data model

The `proposed` status is already in the `projects.status` enum. The `proposed_by_pm`, `source_issue_ids`, and `proposal_reasoning` columns are included in the main `projects` table definition (section 3).

---

## 9. Frontend

### How projects relate to sessions

Today, the UI has three top-level nav items: **Overview**, **Sessions**, and **Issues**. Sessions are the unified view that merges PM plan sessions (type: `"plan"`) and manual ad-hoc runs (type: `"manual"`) into a single timeline. Each session maps to either a PM analysis cycle or a single agent run.

Projects introduce a new concept that must coexist without making the UI confusing. The key insight: **project tasks produce agent runs, and those agent runs already appear in sessions**. So sessions remain the "what's happening right now" view, while projects are the "what are we working toward" view.

#### Navigation

```
Sidebar:
├── Overview      → /overview        (unchanged)
├── Sessions      → /sessions        (unchanged — still the activity feed)
├── Projects      → /projects        (NEW — goal-oriented view)
└── Issues        → /issues          (unchanged)
```

Projects gets its own top-level nav item rather than being nested under Sessions, because projects are a distinct concept (persistent goals) not a type of session (ephemeral execution).

#### How they connect

- A **project task** creates an **agent run** when dispatched
- That agent run appears in the **Sessions** page as part of a PM plan session (since the PM delegated it)
- The **Project detail** page links to each task's agent run, which links to the session
- The **Session detail** page for a PM plan shows which tasks were project-sourced vs reactive-issue-sourced

```
Projects page          Sessions page          Issues page
┌──────────────┐      ┌──────────────┐      ┌──────────────┐
│ REST→GraphQL │      │ PM Session 5 │      │ Issue #42    │
│  ├─ Task 1 ──┼──────┼─→ Run abc ───┼──────┼─→ (source)  │
│  ├─ Task 2 ──┼──────┼─→ Run def    │      │ Issue #43    │
│  └─ Task 3   │      │   Run ghi ───┼──────┼─→ (reactive) │
└──────────────┘      └──────────────┘      └──────────────┘
```

Users who don't use projects see no change — Sessions and Issues work exactly as before. Users who create projects get the Projects page as a goal-tracking dashboard, with Sessions remaining the real-time activity feed.

#### Session detail: project attribution

When a session (PM plan) includes project-sourced tasks, the task cards show a small project badge linking back to the project. This is the only change to the existing session UI:

```
PM Session #5 — 3 tasks
├─ [REST→GraphQL] Migrate /users GET to GraphQL   ← project badge
├─ [REST→GraphQL] Migrate /products GET to GraphQL ← project badge
└─ Fix null pointer in auth middleware              ← no badge (reactive)
```

### Project list page (`/projects`)

Shows all projects with status, priority, progress bar, and last activity. Filterable by status (proposed | active | completed | etc). Sortable by priority or updated_at.

PM-proposed projects appear with a "Proposed" badge and a prominent "Approve" / "Dismiss" action (see section on PM-proposed projects below).

### Project detail page (`/projects/{id}`)

#### Board view (default)
Kanban-style board with task cards in columns: Pending | Running | Completed | Failed. Each card shows title, complexity badge, and links to agent run / PR.

#### Timeline view
Cycle-by-cycle history showing what the PM decided each cycle, tasks created, and outcomes. Useful for understanding how the project evolved.

#### Settings panel
Edit goal, scope, completion criteria, execution mode, max_concurrent, priority. Pause/resume/cancel controls.

### Key behaviors

- **User intervention**: Users can add/remove/reorder tasks while the project is active. New tasks enter as `pending`. Running tasks aren't interrupted.
- **Manual task addition**: Users can add tasks manually (via API or UI) if they want to inject specific work the PM wouldn't create on its own.
- **Real-time updates**: Task status changes stream to the UI via SSE (same mechanism as agent run logs).

---

## 10. Scaling: Context Compression

### When this becomes necessary

The iterative planning design works cleanly when:
- 1-5 active projects
- <50 historical tasks per project
- <100 open issues
- Single repository per org (or dominant repo)

It hits limits when:
- 5-20 active projects with deep history
- PM context exceeds ~60K tokens (leaving insufficient room for codebase exploration)
- Project summaries crowd out reactive issue context

**The transition signal is measurable**: track PM context size in tokens each cycle. When it consistently exceeds 60K tokens, it's time for compression.

### How compression works

A compression layer sits between raw project state and the PM context. Instead of passing full task histories, cycle analyses, and approach records, it produces **bounded digests** — fixed-size summaries that capture essential information.

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

### Digest structure

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

    // Only actionable items, not full history
    BlockedTasks       []string `json:"blocked_tasks,omitempty"`
    NextCandidateTasks []string `json:"next_candidate_tasks,omitempty"`

    // Flags for PM attention
    NeedsAttention     bool     `json:"needs_attention,omitempty"`
    AttentionReason    string   `json:"attention_reason,omitempty"`

    CyclesSinceLastProgress int `json:"cycles_since_last_progress"`
}
```

### Digest generation: algorithmic first, LLM later

**Phase 1: Algorithmic compression (recommended start)**

Deterministic Go code that reads raw project state and produces a digest:

```go
func (s *Service) buildProjectDigest(ctx context.Context, project *Project) ProjectDigest {
    tasks, _ := s.projectTasks.ListByProject(ctx, project.ID)
    cycles, _ := s.projectCycles.ListByProject(ctx, project.ID, CycleFilters{Limit: 5})

    completed := countByStatus(tasks, "completed")
    failed := countByStatus(tasks, "failed")

    lastCycle := cycles[0]
    progressSummary := fmt.Sprintf(
        "%d%% complete. %d of %d tasks done. Last cycle: %d succeeded, %d failed. %s",
        project.ProgressPct, completed, len(tasks),
        lastCycle.TasksCompleted, lastCycle.TasksFailed,
        summarizeBlockers(tasks, cycles),
    )

    lessons := deduplicateLessons(project.LessonsLearned)
    if len(lessons) > 5 {
        lessons = lessons[:5]
    }

    return ProjectDigest{
        // ... fill from above
    }
}
```

Pros: Cheap, fast, deterministic, no LLM call.
Cons: Template-based summaries lack nuance. Can't synthesize patterns.

**Phase 2: LLM-generated digests (when needed)**

Use a cheap, fast model (Haiku / GPT-4o-mini) to summarize each project's raw state into a digest. Only pursue this if users report the PM making decisions that show poor understanding of project state, AND algorithmic digests are identified as the bottleneck.

### Context budget management

Explicit token budgets prevent context blowout:

```go
const (
    PMContextBudgetTotal     = 80_000  // tokens reserved for context
    PMContextBudgetIssues    = 25_000  // reactive issues
    PMContextBudgetProjects  = 40_000  // all project digests combined
    PMContextBudgetHistory   = 10_000  // decision log, recent outcomes
    PMContextBudgetReserve   = 5_000   // buffer
)
```

Projects are packed into the budget by priority. If a project overflows the budget, it gets an ultra-compressed representation (just ID + title + progress percentage). The PM still knows it exists but can't plan for it this cycle.

### What gets cut when context overflows

Priority order (highest priority first):

1. **Active project digests** (sorted by project priority)
2. **Critical/high-severity issues** — production fires
3. **In-flight runs** — avoid duplicate work
4. **Recent failures** — avoid repeating mistakes
5. **Medium/low-severity issues** — reactive triage
6. **Decision history** — institutional memory (first to compress)

Overflow is surfaced to the PM:

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

### Tradeoffs of compression

| Dimension | Full context (sections 1-8) | With compression (this section) |
|-----------|---------------------------|--------------------------------|
| Max projects | ~5 comfortably, ~10 with strain | 20+ comfortably |
| Context per project | Full history (~5-15K tokens) | Digest (~1-2K tokens) |
| PM cycle time | Grows with project count/history | Bounded by token budget |
| Information fidelity | Full — PM sees everything | Lossy — PM sees summaries |
| Implementation effort | Core project work | + compression pipeline + budget management |
| Debugging | Straightforward — check PM context | Two-layer — check digest + raw state |
| Failure modes | PM overwhelmed by large context | PM misled by lossy digest |

---

## 11. Multi-Repo Considerations

### Short-term (Phase 1-2)

Each project is scoped to one repository (enforced by `repository_id` FK). Multi-repo initiatives are modeled as separate projects per repo. The PM sees all projects and can coordinate across them at the planning level, even if it can't explore both codebases in one cycle.

### Medium-term (Phase 3+)

The PM could clone multiple repos into the same sandbox (one per `/workspace/<repo-name>/`). Adds complexity but is mechanically straightforward. Token budget becomes more important since exploring multiple repos is expensive.

### Long-term

Per-repo PM agents with a meta-scheduler. Only pursue if multi-repo projects become a primary use case AND the single PM is the bottleneck. (This is the "Model C" architecture — autonomous sub-PMs per project/repo — which we deliberately defer.)

---

## 12. Risk Analysis

### PM produces incoherent project plans

**Likelihood**: Medium. **Impact**: High — agents execute bad tasks, waste slots.

Mitigation:
- Human approval gate on first cycle of each project (required)
- Optional auto-approve for subsequent cycles if success rate > 80%
- Confidence scoring on project tasks (same as reactive tasks)
- Circuit breaker: if a project has 3 consecutive failed cycles, auto-pause and notify

### Slot starvation for reactive work

**Likelihood**: Medium — aggressive projects consume all slots. **Impact**: Medium — critical production issues wait.

Mitigation:
- Hard reserve: at least 1 slot for critical/high issues (code-enforced, not PM-discretion)
- PM prompt explicitly prioritizes production fires over project work
- Alert if reactive queue depth exceeds threshold while projects consume all slots

### Project scope creep via PM

**Likelihood**: Medium — PM might expand scope beyond user intent. **Impact**: Low-medium — wasted agent runs.

Mitigation:
- Scope field is user-defined and read-only by PM
- PM can *recommend* scope changes but cannot modify the project
- UI shows "PM suggests expanding scope to include X" — user must approve

### Context window overflow

**Likelihood**: High for orgs with many projects. **Impact**: Medium — PM context gets truncated, decisions degrade.

Mitigation:
- This is exactly what the compression layer (section 9) solves
- Before compression: cap active projects at 5 per org (warn, don't block)
- Monitor context size per cycle, alert when approaching limits

### Compression loses critical information

**Likelihood**: Medium. **Impact**: Medium — PM misses patterns, repeats mistakes.

Mitigation:
- `NeedsAttention` flag in digests for anomalous patterns
- Full project state always available in DB for human review
- Gradual rollout: start with full context, add compression only when needed

---

## 13. Symphony Comparison

This design is inspired by [OpenAI's Symphony](https://github.com/openai/symphony), an open-source orchestration framework that turns Linear board issues into autonomous agent runs.

| Concept | Symphony | 143 Projects |
|---------|----------|--------------|
| Project definition | Implicit (Linear board) | Explicit (goal + scope + criteria) |
| Task source | Issues on the board | PM-generated iteratively each cycle |
| Orchestrator | Polling daemon per board | Single PM agent, multi-project |
| Planning | None — issues arrive pre-planned | PM plans iteratively each cycle |
| Per-task isolation | Filesystem workspace | Docker + gVisor sandbox |
| Workflow config | `WORKFLOW.md` in repo | Org settings + `AGENTS.md` |
| Progress tracking | Issue state on board | project_cycles + task statuses |
| Failure recovery | Exponential backoff retry | PM replans with new approach |
| Cross-project awareness | None (separate instances) | Full (single PM sees all) |
| Completion | Issue moved to terminal state | PM evaluates criteria against codebase |
| Memory/learning | None | lessons_learned, approach_history, decision_log |

The key philosophical difference: Symphony trusts the human to manage the project board and just automates execution. 143 Projects trusts the PM agent to manage the project plan and automates both planning and execution, with human oversight at project boundaries (creation, approval, completion review).

---

## 14. Implementation Phases

### Phase 1: Core Data Model + CRUD

- Migration: `projects`, `project_tasks`, `project_cycles` tables
- Models: `Project`, `ProjectTask`, `ProjectCycle` structs + enums
- Stores: CRUD operations for each entity
- Handlers: Project and task CRUD endpoints
- Frontend: Basic project list/create/detail pages

### Phase 2: PM Planning Integration

- Add `ActiveProjects` to `PMContext`, implement `buildProjectSummary`
- Extend PM system prompt with project planning + project proposal sections
- Parse `project_plans` from PM output and treat `proposed_projects` as summary metadata
- `executeProjectPlan` creates tasks, dispatches to agents
- Add an internal PM tool for creating repo-scoped projects with status `proposed`
- Agent run completion updates project task status + project progress
- Record `project_cycles` for each PM cycle that touches a project
- Approve/dismiss endpoints for PM-proposed projects

### Phase 3: Execution Orchestrator

- Branch management (create/track per-task branches)
- Sequential / parallel / dependency graph execution modes
- Context passing between tasks (project goal + sibling summaries)
- Failure handling + retry logic with PM replanning
- Slot allocation enforcement (guardrails around PM's recommendations)

### Phase 4: Frontend Board UI

- Kanban-style board showing tasks by status
- Real-time updates via SSE
- Task detail panel with agent run logs, PR link
- Cycle timeline view (PM decisions over time)
- Project settings panel

### Phase 5: Completion + Memory

- PM evaluates completion criteria against codebase
- `lessons_learned` and `approach_history` accumulate and feed back
- Stall detection (3+ cycles with no progress → auto-flag)
- Project completion notifications

### Phase 6: Context Compression (when needed)

**Signal to start**: PM context consistently exceeds 60K tokens OR org has 5+ active projects with 30+ tasks each.

- Context budget system (token estimation + budgets per category)
- Algorithmic digest generator for projects
- Overflow handling (ultra-compressed fallback for low-priority projects)
- Monitoring: track context size, compression ratios

---

## Open Questions

1. **Should projects auto-pause after N consecutive failures?** Proposed: yes, at 3. Configurable per project?

2. ~~**Can the PM create projects on its own?**~~ **Yes.** The PM can propose projects (status: `proposed`), but no work begins until a human confirms. See "PM-Proposed Projects" section.

3. **How do projects interact with Linear/Sentry sync?** If a project task maps to a Linear issue, should task completion update Linear? Probably yes — bidirectional sync. Separate integration concern.

4. **Should project tasks have their own review gates?** Today, project tasks follow the same execution and PR policies as reactive tasks. Should project tasks use different review requirements because they have richer PM context?

5. **Auto-merge for project PRs?** The `auto_merge` flag exists in the schema. When should this be safe to enable? Proposed: only when all validation checks pass AND the task is `complexity: trivial|simple`.
