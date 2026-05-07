# 48 — Automations: First-Class, Team-Owned Recurring Agents

> **Status:** Implemented | **Last reviewed:** 2026-04-21
>
> **Implementation notes:** Core automations and `automation_runs` are implemented. Notification delivery and repeated-failure alerting were explicitly split out into [doc 49](../future/49-automation-notifications.md).
>
> **Supersedes:** [31-automations-tab.md](31-automations-tab.md) (client-side MVP), [32-project-cadence-and-lifecycle.md](../backlog/32-project-cadence-and-lifecycle.md) (evergreen projects)
>
> **Depends on:** [29-projects.md](29-projects.md)

---

## 1. Problem

Scheduling is currently bolted onto projects as a toggle + interval. This creates three problems:

1. **Identity crisis.** A project has a lifecycle (draft → active → completed). A recurring job never "completes." We already hide completion criteria when scheduling is enabled — a sign the model doesn't fit.
2. **No team ownership.** Schedule config is per-project, set by whoever created it. There's no shared view of "what automations are running for our team?" Competitors (Cursor, Codex, Devin) all treat automations as user-owned. This is our opening.
3. **No run history.** When a scheduled project fires, it creates tasks inside the same project. There's no clean way to see "what happened in last Tuesday's run vs. this Tuesday's run."

## 2. Competitive Context

| Platform | Recurring | Team-visible | Team-owned | Run history |
|----------|-----------|-------------|------------|-------------|
| Cursor Automations | Cron + event triggers | Dashboard (read-only) | No (user-owned, no co-editing) | Per-automation |
| OpenAI Codex | Basic cadence | Share links only | No | Per-thread |
| Devin | Schedule Devins | Slack integration | No | Stateful but opaque |
| Factory | Droids + skills | Shared queue | Partial (org-level) | Yes |
| **143 (proposed)** | Interval + cron | Full dashboard | **Yes — org-owned by default** | **Timeline with diffs** |

**Our differentiation:** 143 is the only platform where automations are a team resource, not a personal tool.

## 3. Core Decision

**Separate automations from projects entirely.**

- **Projects** = finite, goal-oriented work. Draft → active → completed. Has completion criteria, tasks, PRs. Remove all `schedule_*` fields.
- **Automations** = recurring processes. Enabled or paused. Has a goal/prompt, a schedule, run history. Never "completes."

This is a reversal of the decision in doc 32 ("keep automation behavior inside projects"). The reasoning: after building scheduled projects, it's clear that the two concepts want different lifecycles, different UX, and different mental models. Forcing them together adds complexity in both.

## 4. Data Model

### 4.1 `automations` table

```sql
CREATE TABLE automations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id),
    repository_id   UUID REFERENCES repositories(id),

    -- What to do
    name            TEXT NOT NULL,
    goal            TEXT NOT NULL,                    -- prompt/instructions for the agent
    scope           TEXT,                             -- optional file/area scope
    agent_type      TEXT,                             -- codex, claude, etc.
    model_override  TEXT,                             -- optional model override
    execution_mode  TEXT NOT NULL DEFAULT 'sequential',
    max_concurrent  INT NOT NULL DEFAULT 1,
    base_branch     TEXT NOT NULL DEFAULT 'main',

    -- When to do it
    schedule_type   TEXT NOT NULL DEFAULT 'interval', -- 'interval' or 'cron'
    interval_value  INT,                              -- e.g. 3
    interval_unit   TEXT,                             -- 'hours', 'days', 'weeks'
    cron_expression TEXT,                             -- e.g. '0 9 * * 1' (Monday 9am)
    timezone        TEXT NOT NULL DEFAULT 'UTC',
    next_run_at     TIMESTAMPTZ,
    last_run_at     TIMESTAMPTZ,

    -- State
    enabled         BOOLEAN NOT NULL DEFAULT true,
    created_by      UUID REFERENCES users(id),
    paused_by       UUID REFERENCES users(id),
    paused_at       TIMESTAMPTZ,
    priority        INT NOT NULL DEFAULT 50,       -- queue ordering: higher = sooner when multiple automations are due simultaneously

    -- Timestamps
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX idx_automations_due
    ON automations (next_run_at)
    WHERE enabled = true AND deleted_at IS NULL AND next_run_at IS NOT NULL;
```

### 4.2 `automation_runs` table

Each time an automation fires (or is manually triggered), a lightweight `automation_runs` row is created. One run may produce one or more sessions (e.g., a multi-repo fan-out in a future version, or a retry). The run is the unit of "what happened at this scheduled time."

```sql
CREATE TABLE automation_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    automation_id   UUID NOT NULL REFERENCES automations(id),

    -- Trigger context
    triggered_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    triggered_by    TEXT NOT NULL DEFAULT 'schedule',  -- 'schedule' or 'manual'
    triggered_by_user_id UUID REFERENCES users(id),    -- set when triggered_by = 'manual'
    scheduled_time  TIMESTAMPTZ,                       -- the next_run_at value that caused this run (for idempotency)

    -- Snapshot of automation config at trigger time (for debuggability)
    goal_snapshot   TEXT NOT NULL,                      -- copy of automations.goal at trigger time
    config_snapshot JSONB,                              -- optional: agent_type, model_override, scope, etc.

    -- State
    status          TEXT NOT NULL DEFAULT 'pending',    -- pending, running, completed, completed_noop, failed
    completed_at    TIMESTAMPTZ,
    result_summary  TEXT,                               -- high-level summary (e.g. "3 flaky tests found")

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_automation_runs_automation
    ON automation_runs (automation_id, triggered_at DESC);

-- Idempotency: prevent double-firing for the same scheduled time
CREATE UNIQUE INDEX idx_automation_runs_idempotency
    ON automation_runs (automation_id, scheduled_time)
    WHERE scheduled_time IS NOT NULL;
```

Sessions link to runs (not directly to automations):

```sql
ALTER TABLE sessions ADD COLUMN automation_run_id UUID REFERENCES automation_runs(id);
CREATE INDEX idx_sessions_automation_run ON sessions (automation_run_id)
    WHERE automation_run_id IS NOT NULL;
```

When an automation fires, the scheduler creates a run, then creates a normal session linked to that run. This means:

- **Run history** = `SELECT * FROM automation_runs WHERE automation_id = ? ORDER BY triggered_at DESC`
- **Sessions per run** = `SELECT * FROM sessions WHERE automation_run_id = ?`
- **All existing session infrastructure works:** container orchestration, diff tracking, token usage, failure handling, result summaries, PR detection.
- **Sessions page filters them out:** The main `/sessions` page adds `WHERE automation_run_id IS NULL` so automation-spawned sessions don't clutter the user's manual work. (Or: show them with a subtle "automation" badge and let the user toggle visibility.)
- **Automation detail page shows runs:** `/automations/:id` queries `automation_runs` and displays them as the run timeline.
- **Manual "Run now"** creates a run with `triggered_by = 'manual'` + `triggered_by_user_id` set, so it shows up in the automation's history.
- **Config snapshots** ensure you can always see what goal/config produced a given run, even if the automation has been edited since.
- **Model selection is optional per automation.** The UI exposes an `Auto` default plus curated coding-model choices; when a model is chosen the backend infers and persists the matching `agent_type` so spawned sessions run under the correct adapter.
- **Trend aggregation** queries `automation_runs` directly — each run has a clear identity.

### 4.3 Migration: existing scheduled projects

The migration is split into four stages to reduce blast radius. Each stage is a separate migration file. Stage 4 is destructive and should only run after verifying no code references the dropped columns.

**Stage 1 — Add new tables (backward compatible, no downtime):**

```sql
-- Migration 001: Create automations + automation_runs tables, add FK to sessions
CREATE TABLE automations ( ... );   -- full DDL from §4.1
CREATE TABLE automation_runs ( ... ); -- full DDL from §4.2
ALTER TABLE sessions ADD COLUMN automation_run_id UUID REFERENCES automation_runs(id);
CREATE INDEX idx_sessions_automation_run ON sessions (automation_run_id)
    WHERE automation_run_id IS NOT NULL;
```

**Stage 2 — Backfill automations from existing scheduled projects:**

```sql
-- Migration 002: Backfill
INSERT INTO automations (org_id, repository_id, name, goal, scope, agent_type,
    model_override, execution_mode, max_concurrent, base_branch,
    schedule_type, interval_value, interval_unit, next_run_at, enabled,
    created_by, priority, created_at, updated_at)
SELECT org_id, repository_id, title, goal, scope, agent_type,
    model_override, execution_mode, max_concurrent, base_branch,
    'interval', schedule_interval, schedule_unit, next_run_at, schedule_enabled,
    created_by, priority, created_at, updated_at
FROM projects
WHERE schedule_enabled = true AND deleted_at IS NULL;
```

**Stage 3 — Dual-write period:** Update the scheduler to read from `automations` for new runs while still honoring `projects.schedule_enabled` as a fallback. New scheduled creations go exclusively to `automations`. This stage lasts until all clients have been deployed and verified.

**Stage 4 — Drop legacy columns (destructive, only after verification):**

```sql
-- Migration 003: Drop schedule fields from projects (run only after Stage 3 is verified)
ALTER TABLE projects
    DROP COLUMN schedule_enabled,
    DROP COLUMN schedule_interval,
    DROP COLUMN schedule_unit,
    DROP COLUMN next_run_at;
```

> **Why incremental?** If Stage 2 fails mid-way, automations are half-created but the old scheduler still works via project fields. If we combined all steps into one migration, a failure after dropping columns would leave both paths broken.

### 4.4 Go model

```go
type Automation struct {
    ID             uuid.UUID  `db:"id"              json:"id"`
    OrgID          uuid.UUID  `db:"org_id"          json:"org_id"`
    RepositoryID   *uuid.UUID `db:"repository_id"   json:"repository_id,omitempty"`
    Name           string     `db:"name"            json:"name"`
    Goal           string     `db:"goal"            json:"goal"`
    Scope          *string    `db:"scope"           json:"scope,omitempty"`
    AgentType      *string    `db:"agent_type"      json:"agent_type,omitempty"`
    ModelOverride  *string    `db:"model_override"  json:"model_override,omitempty"`
    ExecutionMode  string     `db:"execution_mode"  json:"execution_mode"`
    MaxConcurrent  int        `db:"max_concurrent"  json:"max_concurrent"`
    BaseBranch     string     `db:"base_branch"     json:"base_branch"`
    ScheduleType   string     `db:"schedule_type"   json:"schedule_type"`
    IntervalValue  *int       `db:"interval_value"  json:"interval_value,omitempty"`
    IntervalUnit   *string    `db:"interval_unit"   json:"interval_unit,omitempty"`
    CronExpression *string    `db:"cron_expression" json:"cron_expression,omitempty"`
    Timezone       string     `db:"timezone"        json:"timezone"`
    NextRunAt      *time.Time `db:"next_run_at"     json:"next_run_at,omitempty"`
    LastRunAt      *time.Time `db:"last_run_at"     json:"last_run_at,omitempty"`
    Enabled        bool       `db:"enabled"         json:"enabled"`
    CreatedBy      *uuid.UUID `db:"created_by"      json:"created_by,omitempty"`
    PausedBy       *uuid.UUID `db:"paused_by"       json:"paused_by,omitempty"`
    PausedAt       *time.Time `db:"paused_at"       json:"paused_at,omitempty"`
    Priority       int        `db:"priority"        json:"priority"`
    CreatedAt      time.Time  `db:"created_at"      json:"created_at"`
    UpdatedAt      time.Time  `db:"updated_at"      json:"updated_at"`
}

type AutomationRun struct {
    ID                uuid.UUID  `db:"id"                    json:"id"`
    AutomationID      uuid.UUID  `db:"automation_id"         json:"automation_id"`
    TriggeredAt       time.Time  `db:"triggered_at"          json:"triggered_at"`
    TriggeredBy       string     `db:"triggered_by"          json:"triggered_by"`          // "schedule" or "manual"
    TriggeredByUserID *uuid.UUID `db:"triggered_by_user_id"  json:"triggered_by_user_id,omitempty"`
    ScheduledTime     *time.Time `db:"scheduled_time"        json:"scheduled_time,omitempty"`
    GoalSnapshot      string     `db:"goal_snapshot"         json:"goal_snapshot"`
    ConfigSnapshot    *string    `db:"config_snapshot"       json:"config_snapshot,omitempty"` // JSON
    Status            string     `db:"status"                json:"status"`
    CompletedAt       *time.Time `db:"completed_at"          json:"completed_at,omitempty"`
    ResultSummary     *string    `db:"result_summary"        json:"result_summary,omitempty"`
    CreatedAt         time.Time  `db:"created_at"            json:"created_at"`
    UpdatedAt         time.Time  `db:"updated_at"            json:"updated_at"`
}

// Session gets one new field:
//   AutomationRunID *uuid.UUID `db:"automation_run_id" json:"automation_run_id,omitempty"`
```

## 5. API

```
POST   /api/v1/automations              Create automation
GET    /api/v1/automations              List automations (org-wide)
GET    /api/v1/automations/:id          Get automation detail
PATCH  /api/v1/automations/:id          Update automation (any team member)
DELETE /api/v1/automations/:id          Soft-delete
POST   /api/v1/automations/:id/run      Trigger manual run (creates a run + session)
POST   /api/v1/automations/:id/pause    Pause (records paused_by/paused_at)
POST   /api/v1/automations/:id/resume   Resume
POST   /api/v1/automations/bulk         Bulk pause/resume/delete (for deploy freezes, incidents)
GET    /api/v1/automations/:id/runs     List runs for this automation (paginated)
GET    /api/v1/automations/:id/runs/:rid Get run detail + its sessions
```

All endpoints are org-scoped. Any authenticated org member can CRUD any automation. This is the team-ownership model — no per-user isolation.

### Pagination

All list endpoints use cursor-based pagination:

```
GET /api/v1/automations/:id/runs?cursor=<run_id>&limit=25
```

Response includes `next_cursor` (null when no more results). Default `limit=25`, max `limit=100`. This applies to:
- `GET /api/v1/automations` (cursor by automation ID)
- `GET /api/v1/automations/:id/runs` (cursor by run ID, ordered by `triggered_at DESC`)

Individual session detail uses the existing `GET /api/v1/sessions/:id` endpoint — no new endpoint needed.

### Bulk operations

`POST /api/v1/automations/bulk` accepts:

```json
{
  "action": "pause" | "resume" | "delete",
  "automation_ids": ["uuid1", "uuid2"],  // optional: omit to apply to all
  "filter": { "enabled": true }          // optional: apply to matching automations
}
```

This is useful for org-wide operations like deploy freezes ("pause all automations") or incident response.

## 6. Scheduler Changes

The existing `scheduleProjectCycles()` in `cluster/scheduler.go` becomes `scheduleAutomationRuns()`.

### 6.1 Claim-and-fire loop

```sql
-- Atomically claim due automations (prevents double-firing across replicas/restarts)
WITH due AS (
    SELECT id, next_run_at
    FROM automations
    WHERE enabled = true
      AND deleted_at IS NULL
      AND next_run_at <= now()
    ORDER BY priority DESC, next_run_at ASC
    FOR UPDATE SKIP LOCKED
)
UPDATE automations a
SET last_run_at = now(),
    next_run_at = <computed_next>   -- see §6.2
FROM due
WHERE a.id = due.id
RETURNING a.id, a.goal, a.agent_type, a.model_override, a.scope, due.next_run_at AS scheduled_time;
```

For each returned row:

1. **Insert an `automation_runs` row** with `scheduled_time` set to the `next_run_at` value that was claimed. The unique index on `(automation_id, scheduled_time)` guarantees idempotency — if a duplicate fires (crash-restart, clock skew), the insert fails and the scheduler skips it.
2. **Snapshot the automation's current goal and config** into `goal_snapshot` / `config_snapshot` on the run.
3. **Check `max_concurrent`:** count in-flight runs (`status IN ('pending', 'running')`) for this automation. If `>= max_concurrent`, mark the run as `status = 'skipped'` and skip session creation.
4. **Create a session** with `automation_run_id` set (reuses the existing session creation path).
5. **Enqueue the session** for execution (same as manual session dispatch).

The `SELECT ... FOR UPDATE SKIP LOCKED` pattern ensures that even with multiple scheduler replicas, each due automation is claimed by exactly one replica. No advisory locks needed.

### 6.2 Next-run calculation and cron

For `schedule_type = 'interval'`: `next_run_at = now() + interval_value * interval_unit`.

For `schedule_type = 'cron'`: use the [`gorhill/cronexpr`](https://github.com/gorhill/cronexpr) library (supports standard 5-field cron + seconds extension). Compute `next_run_at = cronexpr.MustParse(cron_expression).Next(now().In(tz))` where `tz` is loaded from the automation's `timezone` field.

**Cron validation:** The API's create/update handler must validate the cron expression at write time:

```go
if _, err := cronexpr.Parse(input.CronExpression); err != nil {
    return apierr.BadRequest("invalid cron expression: %v", err)
}
```

**DST edge cases:** The `timezone` field uses IANA names (e.g., `America/New_York`). When a DST transition causes a scheduled time to not exist (spring forward) or exist twice (fall back), the cron library's `Next()` handles this correctly — it skips nonexistent times and picks the first occurrence of ambiguous times. This behavior should be documented in the API response.

### 6.3 Run completion

No new job type needed. The worker picks up the session like any other session. When it completes, a callback updates the corresponding `automation_runs` row:

```go
// Called by session completion handler
func (s *Scheduler) OnSessionComplete(session *Session) {
    if session.AutomationRunID == nil { return }
    run := s.db.GetAutomationRun(*session.AutomationRunID)
    run.Status = mapSessionStatus(session.Status) // completed, completed_noop, failed
    run.CompletedAt = session.CompletedAt
    run.ResultSummary = session.ResultSummary
    s.db.UpdateAutomationRun(run)
}
```

## 7. UI Design

### 7.1 Navigation

```
Sidebar:
  Overview
  Sessions
  Projects        ← finite work only, no schedule fields
  Automations     ← NEW top-level nav item
  Settings
```

### 7.2 Automations list page (`/automations`)

```
┌─────────────────────────────────────────────────────────────┐
│  Automations                                    [+ New]     │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌─ Enabled ───────────────────────────────────────────┐    │
│  │                                                     │    │
│  │  ⟳ Find flaky tests              every 1 day       │    │
│  │    pangyo • Last run: 2h ago • 3 issues found       │    │
│  │    Next run: tomorrow 9:00 AM                       │    │
│  │                                                     │    │
│  │  ⟳ Security sweep                every 7 days      │    │
│  │    pangyo • Last run: 3d ago • 1 PR opened          │    │
│  │    Next run: Apr 19 9:00 AM                         │    │
│  │                                                     │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                             │
│  ┌─ Paused ────────────────────────────────────────────┐    │
│  │                                                     │    │
│  │  ⏸ Codebase maintenance          every 3 days      │    │
│  │    pangyo • Paused by @john 2d ago                  │    │
│  │                                                     │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

Key design choices:
- Grouped by enabled/paused — not by status lifecycle.
- Each row shows: name, cadence, repo, last run summary, next run time.
- "Paused by @john" — team audit trail built into the UI.
- On narrow mobile viewports, automation cards must stack name, cadence, and timing metadata vertically, with wrapped text instead of truncating the details into overlapping rows. The overflow menu remains pinned as a separate trailing control so long names and schedules cannot collide with actions.

### 7.3 New automation page (`/automations/new`)

```
┌─────────────────────────────────────────────────────────────┐
│  New Automation                                             │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌─ Start from a template ─────────────────────────────┐    │
│  │  [🧪 Flaky tests] [🛡 Security] [🔧 Maintenance]    │    │
│  │  [📋 Backlog triage] [📝 Doc freshness]              │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                             │
│  Name         [________________________]                    │
│  Goal         [________________________]                    │
│               [________________________]                    │
│  Scope        [________________________] (optional)         │
│  Repository   [▾ Select repo          ]                     │
│                                                             │
│  ── Schedule ───────────────────────────────────────────    │
│  Run every  [3] [▾ days ]                                   │
│                                                             │
│  ── Advanced ─────────────────────────── (collapsed) ──     │
│  Agent / Model / Execution mode / Priority / Base branch    │
│                                                             │
│              [Create automation]                             │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

Key design choices:
- Templates are front-and-center (not hidden behind a toggle).
- No schedule toggle needed — this is the automations page, scheduling is the whole point.
- Schedule is always visible, not a collapsible opt-in.
- No completion criteria field (automations don't complete).

### 7.4 Automation detail page (`/automations/:id`)

Two tabs: **Sessions** (run history) and **Settings**.

#### Runs tab (default)

```
┌─────────────────────────────────────────────────────────────┐
│  ⟳ Find flaky tests                  [Enabled ▾] [Run now] │
│  pangyo • every 1 day • Next: tomorrow 9:00 AM             │
├──────────┬──────────────────────────────────────────────────┤
│  Runs    │  Settings                                        │
├──────────┴──────────────────────────────────────────────────┤
│                                                             │
│  ┌─ Apr 15, 9:02 AM ─ Completed ──────────────────────┐    │
│  │  Found 3 flaky tests in payments module.            │    │
│  │  Opened PR #412: fix timing-dependent assertions    │    │
│  │  Duration: 4m 32s • 1 session • 1 PR               │    │
│  │                                                     │    │
│  │  [View PR] [View session]                           │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                             │
│  ┌─ Apr 14, 9:01 AM ─ Completed ──────────────────────┐    │
│  │  Found 5 flaky tests. Opened PR #408, PR #409.     │    │
│  │  Duration: 6m 10s • 2 sessions • 2 PRs             │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                             │
│  ┌─ Apr 13, 9:00 AM ─ Failed ─────── ⚠ ───────────────┐   │
│  │  Session crashed: OOM after scanning 12k files.     │    │
│  │  Duration: 8m 42s • 1 session (failed)              │    │
│  │                                                     │    │
│  │  [View session] [Retry]                             │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                             │
│  ┌─ Apr 12, 9:00 AM ─ Completed (no-op) ──────────────┐   │
│  │  No new flaky tests detected. No action taken.      │    │
│  │  Duration: 1m 05s                                   │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                             │
│  ── Trend ──────────────────────────────────────────────    │
│  Issues found:  12 → 8 → 5 → 3  (last 4 runs)            │
│  ████████████                                               │
│  ████████                                                   │
│  █████                                                      │
│  ███                                                        │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

Key design choices:
- **Run timeline is the default view.** This is where the value compounds — you see the automation getting smarter / the codebase getting healthier over time.
- Each run card shows: summary, PRs opened, duration, links to sessions.
- **Failed runs are prominent:** red/warning styling with the failure reason and a [Retry] button that creates a new manual run.
- **Trend visualization** at the bottom — the "flaky test count going down" chart that makes the ROI visible.
- No-op runs are shown (dimmed) so you know the automation ran but found nothing.
- Runs are paginated (cursor-based, 25 per page) — "Load more" at the bottom.

#### Settings tab

Standard form: name, goal, scope, repo, schedule, agent config. Any team member can edit. Changes are audit-logged.

### 7.5 Project creation page (simplified)

Remove the schedule toggle, template pills, and schedule fields from `/projects/new`. The creation page becomes purely about finite work:

```
┌─────────────────────────────────────────────────────────────┐
│  New Project                                                │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  Describe what you want to build...                         │
│  [________________________] [✨ Generate]                   │
│                                                             │
│  Title                [________________________]            │
│  Goal                 [________________________]            │
│  Scope                [________________________] (optional) │
│  Completion criteria  [________________________] (optional) │
│  Repository           [▾ Select repo          ]             │
│                                                             │
│  ── Advanced ─────────────────────────── (collapsed) ──     │
│                                                             │
│              [Create project]                               │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

The project settings tab also loses the "Recurring schedule" card entirely.

### 7.6 Project sidebar (simplified)

Remove the "Scheduled" filter tab. The sidebar becomes:

```
Filter tabs: [All] [Active 3] [Draft] [Done]
```

No more purple "Scheduled" badge — those are now in `/automations`.

## 8. Team-Ownership Details

This is the key differentiator. Specifics:

1. **All automations are org-scoped.** There is no "my automations" vs "team automations." If you create one, everyone sees it.
2. **Any member can edit/pause/trigger.** No permission gating beyond org membership (initially). The `created_by` and audit log track who did what.
3. **Run history shows who triggered manual runs.** "Triggered by @sarah" vs "Scheduled."
4. **Pause/enable shows who changed state.** "Paused by @john 2d ago" is visible on the list page.
5. **Notifications** (per-automation preferences, Slack/email on completion/failure, repeated-failure alerting) are specified separately in [doc 49](../future/49-automation-notifications.md).

## 9. Templates

### 9.1 Storage and delivery

Templates are **hardcoded in the frontend** for v1 — stored in `frontend/src/lib/automation-templates.ts`. They remain a UX convenience rather than a backend data model, but the client-side shape is now richer than the original MVP. Each template includes:

- `name` + `category`
- short summary text for selection UI
- a fully structured `goal` prompt
- expected outcomes / tags for browse surfaces
- default cadence

The prompt body is intentionally written more like a good engineering issue than a slogan. Each built-in template includes explicit sections such as:

- `What to do`
- `Output requirements`
- `Verification`

This matches how current coding-agent products publicly recommend reusable prompts: concrete scope, expected deliverable, and a way for the agent to check its work.

**Why frontend-only:** Templates are a UX convenience, not a data model concept. Keeping them client-side means we can iterate on wording/selection without migrations. If we later want user-created templates or an org template library, we'd add a `automation_templates` table — but that's a v2 concern.

### 9.2 Built-in templates

The default library now spans multiple categories instead of only five short starters:

| Category | Examples |
|----------|----------|
| Reliability | Find flaky tests, CI failure triage, Performance regression sweep |
| Security | Security sweep, Dependency drift review |
| Maintenance | Codebase maintenance, Dead code cleanup |
| Planning | Backlog triage |
| Documentation | Documentation freshness, API contract audit |

On `/automations/new`, the UI shows a small featured subset for fast starts. A dedicated `/automations/templates` page exposes the broader catalog with category browsing, expected outcomes, and full prompt previews for users who want deeper template selection.

## 10. Implementation Phases

### Phase 1: Data model + API + migration (Stages 1-2) ✅ Completed 2026-04-16

1. ~~Create `automations` table + `automation_runs` table + add `automation_run_id` FK to sessions (Migration Stage 1).~~
2. ~~Backfill existing `schedule_enabled=true` projects to automations (Migration Stage 2).~~
3. ~~Implement CRUD API endpoints with cursor-based pagination.~~
4. ~~Implement bulk pause/resume/delete endpoint.~~
5. ~~Update scheduler with `SELECT ... FOR UPDATE SKIP LOCKED` claim loop, idempotency checks, and `max_concurrent` enforcement (§6.1).~~
6. ~~Add cron expression validation using `gorhill/cronexpr` (§6.2).~~
7. ~~Dual-write period: scheduler reads from both automations and projects (Migration Stage 3).~~

### Phase 2: Frontend ✅ Completed 2026-04-16

1. ~~Add `/automations` nav item and list page.~~
2. ~~Build `/automations/new` with templates (loaded from `automation-templates.ts`).~~
3. ~~Build `/automations/:id` with runs timeline (including failed run states + retry) and settings.~~
4. ~~Remove schedule UI from project creation and project settings.~~
5. ~~Remove "Scheduled" filter from project sidebar.~~
6. ~~Add cursor-based pagination to run list ("Load more").~~

### Phase 3: Team features + trends + cleanup ✅ Completed 2026-04-18

1. ~~Audit log entries for automation changes (who edited/paused/enabled).~~ ✅
2. ~~Run trend visualizations (aggregated from `automation_runs`, not sessions).~~ ✅
3. ~~Drop legacy schedule fields from projects (Migration Stage 4).~~ ✅ (migration 000075)
4. ~~Wire automation execution end-to-end: worker handler creates a session per run and dispatches `run_agent`; orchestrator's completion hook (`internal/services/automations/hooks.go`) maps terminal session status back to the `automation_runs` row.~~ ✅

**Deferred to [doc 49](../future/49-automation-notifications.md):** per-automation notification preferences (Slack/email on completion/failure) and repeated-failure alerting. These depend on the notification infrastructure in [doc 22](../future/22-notifications.md) landing first, so they are scoped out of doc 48.

## 11. What This Doesn't Do (Yet)

- **Event-based triggers** (webhook, PR opened, Slack message). Interval/cron is enough for v1. Can add a `triggers` abstraction later.
- **Multi-repo fan-out.** One automation = one repo for now. The `automation_runs` → sessions relationship is already one-to-many, so multi-repo fan-out (one run, N sessions across repos) is a natural v2 extension.
- **Cost tracking per-automation.** Valuable — sessions already have `token_usage`, so aggregating per-automation via `automation_runs` is straightforward.
- **Alerting on repeated failures.** If an automation fails N times in a row, notify the team. Moved to [doc 49](../future/49-automation-notifications.md) along with per-automation notification preferences.
- **Retry policies.** Currently, failed runs stay failed. Auto-retry with backoff is a v2 feature. Manual retry (via the [Retry] button) is supported in v1.
- **User-created templates / template marketplace.** Templates are hardcoded in the frontend for v1 (§9.1). Org-level custom templates and public sharing are v3 ideas.
- **Automation versioning / changelog.** Config snapshots on runs (§4.2) provide point-in-time debuggability, but there's no explicit version history or diff view for automation config changes. Could add an `automation_versions` table later if needed.
