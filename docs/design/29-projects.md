# Design: Projects — Autonomous Multi-Task Agent Orchestration

This document explores how 143 can introduce a **Projects** concept — a higher-level abstraction above individual issues and agent runs that lets users define a body of work, break it into tasks, and have agents execute them autonomously without micromanaging each PR.

## Problem

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

## Design Options

### Option A: Projects as a Thin Orchestration Layer (Recommended)

A project is a lightweight container that holds a goal description, a set of tasks, and orchestration rules. The PM agent breaks the goal into tasks, and the existing agent run infrastructure executes them. The project tracks overall progress and handles inter-task dependencies.

**Core idea**: Projects are to PM plans what epics are to stories. The PM plan is a single analysis cycle; a project is a persistent, user-defined scope that may span multiple PM cycles.

#### Data Model

```sql
CREATE TABLE projects (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id),
    repository_id   UUID NOT NULL REFERENCES repositories(id),
    title           TEXT NOT NULL,
    description     TEXT,             -- user's high-level goal
    status          TEXT NOT NULL DEFAULT 'draft',
        -- draft | planning | executing | paused | completed | failed
    execution_mode  TEXT NOT NULL DEFAULT 'sequential',
        -- sequential | parallel | dependency_graph
    max_concurrent  INT NOT NULL DEFAULT 3,
    auto_merge      BOOLEAN NOT NULL DEFAULT false,
    base_branch     TEXT NOT NULL DEFAULT 'main',
    created_by      UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);

CREATE TABLE project_tasks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      UUID NOT NULL REFERENCES projects(id),
    org_id          UUID NOT NULL REFERENCES organizations(id),
    title           TEXT NOT NULL,
    description     TEXT,             -- detailed task spec for the coding agent
    approach        TEXT,             -- PM's suggested approach
    status          TEXT NOT NULL DEFAULT 'pending',
        -- pending | blocked | queued | running | completed | failed | skipped
    sort_order      INT NOT NULL DEFAULT 0,
    depends_on      UUID[],           -- other task IDs that must complete first
    issue_id        UUID REFERENCES issues(id),  -- optional link to source issue
    agent_run_id    UUID REFERENCES agent_runs(id),  -- the run executing this task
    branch_name     TEXT,             -- the branch this task's agent works on
    pr_url          TEXT,             -- resulting PR URL
    retry_count     INT NOT NULL DEFAULT 0,
    max_retries     INT NOT NULL DEFAULT 1,
    complexity      TEXT,             -- trivial | simple | moderate | complex
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX idx_projects_org_status ON projects(org_id, status);
CREATE INDEX idx_project_tasks_project ON project_tasks(project_id, sort_order);
CREATE INDEX idx_project_tasks_status ON project_tasks(project_id, status);
```

#### Lifecycle

```
User creates project (title + description)
    ↓
PM agent breaks it into tasks (planning phase)
    ↓
User reviews/edits task breakdown → approves
    ↓
Orchestrator starts executing tasks per execution_mode
    ↓
Each task → agent_run → validation → PR
    ↓
Project tracks overall progress, handles failures/retries
    ↓
All tasks done → project completed
```

#### Execution Modes

**Sequential**: Tasks run one at a time in `sort_order`. Each task's PR is merged (or approved) before the next starts. The next agent sees the cumulative codebase changes. Best for refactoring chains.

**Parallel**: Up to `max_concurrent` tasks run simultaneously, each on its own branch forked from `base_branch`. Creates independent PRs. Best for unrelated bug fixes or feature additions. Merge conflicts are detected and flagged.

**Dependency Graph**: Tasks declare `depends_on` relationships. The orchestrator runs tasks whose dependencies are all completed, up to `max_concurrent`. A topological sort determines the execution order. Best for complex features with mixed independent and dependent work.

#### Branch Strategy

Each task gets its own branch: `143/project-{short-id}/{task-number}-{slug}`.

For sequential mode, each subsequent task branches from the previous task's branch (stacked PRs). For parallel mode, all tasks branch from `base_branch`. For dependency graph mode, a task branches from the merge of its dependency branches.

#### Key Behaviors

- **Failure handling**: When a task fails, the project pauses and notifies the user. The user can retry the task, skip it, or provide guidance. Other independent tasks (in parallel/graph mode) continue.
- **User intervention**: Users can add/remove/reorder tasks while the project is executing. New tasks enter as `pending`. Running tasks aren't interrupted.
- **Progress tracking**: The project shows a board-like view with task cards in columns (pending | running | completed | failed). Each card links to its agent run and PR.
- **Context passing**: Each task's agent prompt includes the project description as context, plus summaries of completed sibling tasks. This gives the agent awareness of the broader goal.

#### API Surface

```
POST   /api/v1/projects                    -- create a new project
GET    /api/v1/projects                    -- list projects
GET    /api/v1/projects/{id}               -- get project with tasks
PATCH  /api/v1/projects/{id}               -- update project settings
POST   /api/v1/projects/{id}/plan          -- trigger PM to break into tasks
POST   /api/v1/projects/{id}/start         -- begin execution
POST   /api/v1/projects/{id}/pause         -- pause execution
POST   /api/v1/projects/{id}/resume        -- resume execution
POST   /api/v1/projects/{id}/tasks         -- add a task manually
PATCH  /api/v1/projects/{id}/tasks/{tid}   -- update a task
DELETE /api/v1/projects/{id}/tasks/{tid}   -- remove a task
POST   /api/v1/projects/{id}/tasks/{tid}/retry  -- retry a failed task
```

#### How Planning Works

When the user hits "Plan" on a project, it enqueues a `project_plan` job. The PM agent:

1. Reads the project description and repository context
2. Analyzes the codebase to understand scope
3. Breaks the goal into discrete, agent-executable tasks
4. Estimates complexity for each task
5. Suggests an execution order and dependency graph
6. Returns the task breakdown for user review

The user can then edit task descriptions, reorder, add dependencies, or remove tasks before starting execution.

#### How Execution Works

The **project orchestrator** is a new worker job handler (`project_execute`) that:

1. Loads the project and its tasks
2. Determines which tasks are ready to run (dependencies met, under concurrency limit)
3. For each ready task:
   a. Creates a branch from the appropriate base
   b. Creates an `agent_run` linked to the task
   c. Enqueues the agent run job with the task description + project context
4. Monitors running tasks via polling
5. When a task completes:
   a. If validation passes → creates PR
   b. Marks the task as completed
   c. Checks if new tasks are now unblocked
   d. Enqueues the next batch
6. When all tasks are done → marks the project as completed

This reuses the existing `agent_run` → `validation` → `PR` pipeline entirely. The project orchestrator is just a higher-level scheduler on top.

---

### Option B: Projects as PM Plan Extensions

Instead of a new entity, extend PM plans to be persistent and editable. A "project" is just a named PM plan that the user can curate.

**Pros**: Less new infrastructure, reuses PM plan tables.
**Cons**: PM plans are designed as snapshots of a single analysis cycle. Making them mutable and long-lived fights the current design. The `tasks` JSONB field would need to become a real table anyway. Doesn't cleanly support user-created projects that weren't generated by PM analysis.

#### Rough Shape

- Add `name`, `is_project`, `execution_mode` columns to `pm_plans`
- Promote the `tasks` JSONB to a `pm_plan_tasks` table with status tracking
- Add a `project_execute` job handler that works off `pm_plan_tasks`

This gets ~80% of Option A with less new code, but creates an awkward dual-purpose table.

---

### Option C: Projects as Issue Groups with Auto-Execution

Model projects as tagged groups of issues. Create a `project_id` column on `issues`, and a thin `projects` table. The PM agent auto-executes all issues in a project.

**Pros**: Minimal new tables, works with existing issue-centric flow.
**Cons**: Doesn't support user-defined tasks that don't originate as issues. Loses the "plan then execute" workflow. No dependency graph. Feels like a tag rather than a first-class concept.

---

## Recommendation

**Option A** is the strongest design. It introduces projects as a first-class concept that cleanly layers on top of the existing infrastructure:

- **Reuses everything**: Agent runs, validation pipeline, PR creation, job queue, sandbox orchestration — all unchanged.
- **Adds one new concern**: Multi-task scheduling with dependency awareness.
- **Clean separation**: A project is a container for tasks. Each task maps to an agent run. The project orchestrator is just a scheduler.
- **User-friendly**: The board-like UI is familiar to anyone who's used Linear/Jira/Trello.

The key insight is that 143 already has all the pieces for executing individual tasks. Projects just need an orchestration layer that decides **which tasks to run when** and **passes context between them**.

## Implementation Phases

### Phase 1: Core Data Model + CRUD (1-2 days)
- Migration: `projects` and `project_tasks` tables
- Models: `Project`, `ProjectTask` structs + enums
- Stores: `ProjectStore`, `ProjectTaskStore` with CRUD operations
- Handlers: Project and task CRUD endpoints
- Frontend: Basic project list/create/detail pages

### Phase 2: Planning Integration (1-2 days)
- `project_plan` job handler that uses the PM agent to break a project into tasks
- Task review/edit UI before execution
- Complexity estimation per task

### Phase 3: Execution Orchestrator (2-3 days)
- `project_execute` job handler with sequential/parallel/dependency modes
- Branch management (create/track per-task branches)
- Context passing between tasks (project description + sibling summaries)
- Task status updates as agent runs complete
- Failure handling + retry logic

### Phase 4: Frontend Board UI (2-3 days)
- Kanban-style board showing tasks by status
- Real-time updates via SSE
- Task detail panel with agent run logs, PR link, etc.
- Drag-and-drop reordering
- Dependency visualization (simple arrows between cards)

### Phase 5: Polish + Advanced Features (ongoing)
- Auto-merge mode (merge PRs automatically when validation passes)
- Stacked PR support for sequential mode
- Project templates (common project types pre-configured)
- PM agent learning from project outcomes
- Slack/email notifications for project milestones
