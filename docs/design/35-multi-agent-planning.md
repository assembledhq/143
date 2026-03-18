# Design: Multi-Agent Sessions

## Problem

Today, each session runs exactly one coding agent. But many tasks benefit from multiple agents working simultaneously — Claude Code on the backend while Codex handles the frontend, or two agents tackling independent subtasks in parallel, each with their own chat thread the user can interact with.

The goal: **a single session can have multiple coding agents and multiple chats running at the same time**, similar to how conductor.build lets you manage parallel agents from one interface, but without leaving 143.dev.

### What this is NOT

- **Not separate sessions with a parent.** We considered a "plan session spawns child sessions" model, but that fragments the experience. You'd have to click between session detail pages. The user wants everything in one place.
- **Not conductor.build's model.** Conductor uses fully isolated git worktrees — each agent is a completely independent workspace. We want agents working within the same session context, on the same branch, with a unified view.

---

## Design Principles

1. **One session, many agents** — A session is a workspace. You can spin up multiple agent threads within it, each with its own chat and container.
2. **Same branch, container isolation** — All agents in a session target the same branch. Each agent runs in its own container (for filesystem isolation), but commits go to the shared branch.
3. **Agents as threads, not sessions** — Each agent within a session is a "thread" — a parallel lane of work with its own conversation, its own container, its own diff. The session ties them together.
4. **PM plans remain separate** — PM plans are an orchestration/prioritization layer that creates sessions. Multi-agent is about what happens *within* a session once it's created.

---

## Data Model

### New: `session_threads` table

A session can have one or more threads. Each thread is one agent doing one piece of work.

```sql
CREATE TABLE session_threads (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES sessions(id),
    org_id          UUID NOT NULL REFERENCES organizations(id),

    -- Agent identity
    agent_type      TEXT NOT NULL,             -- claude_code, codex, gemini_cli, custom
    model_override  TEXT,                      -- optional model override

    -- Thread metadata
    label           TEXT NOT NULL,             -- "Backend API", "Frontend UI", "Tests"
    instructions    TEXT,                      -- what this thread should do
    file_scope      TEXT[],                    -- files this thread owns (advisory, not enforced)

    -- Execution state
    status          TEXT NOT NULL DEFAULT 'pending',
    -- pending, running, idle, awaiting_input, completed, failed, cancelled
    container_id    TEXT,
    agent_session_id TEXT,                     -- external agent session ID
    sandbox_state   TEXT NOT NULL DEFAULT 'none',
    snapshot_key    TEXT,
    current_turn    INT NOT NULL DEFAULT 0,
    last_activity_at TIMESTAMPTZ,

    -- Results
    confidence_score    FLOAT,
    result_summary      TEXT,
    diff                TEXT,
    failure_explanation TEXT,
    failure_category    TEXT,

    -- Timestamps
    started_at     TIMESTAMPTZ,
    completed_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT fk_session FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

CREATE INDEX idx_session_threads_session ON session_threads(session_id);
CREATE INDEX idx_session_threads_org_status ON session_threads(org_id, status);
```

### Modified: `session_messages` table

Add `thread_id` so messages belong to a specific thread's conversation.

```sql
ALTER TABLE session_messages ADD COLUMN thread_id UUID REFERENCES session_threads(id);
CREATE INDEX idx_session_messages_thread ON session_messages(thread_id);
```

Messages with `thread_id = NULL` are session-level messages (e.g., the initial instructions).

### Modified: `sessions` table

Sessions get lighter. Per-agent fields (`agent_session_id`, `sandbox_state`, `snapshot_key`, `container_id`) move to threads. The session keeps:

- Identity: `id`, `org_id`, `issue_id`
- Lifecycle: `status` (derived from thread statuses — active if any thread is active)
- Metadata: `triggered_by_user_id`, `pm_plan_id`, `project_task_id`
- The existing `agent_type` field becomes the "default" agent type (used for single-thread sessions for backwards compatibility)

### Backwards compatibility

Single-agent sessions still work. When a session has exactly one thread, the UX is identical to today. The thread is implicit — the API can auto-create it. Existing sessions are migrated to have one thread each.

---

## Session Detail Page UX

### Current: Single-agent session detail

```
┌─────────────────────────────────────────────────────────┐
│  Session: Fix null pointer in parser                    │
│  Status: ● running    Agent: claude    5m ago           │
│                                                         │
│  [Chat] [Overview] [Logs] [Changes] [Validation]        │
│  ┌─────────────────────────────────────────────────────┐│
│  │ User: Fix the null pointer in parser.go line 42     ││
│  │                                                     ││
│  │ Claude: I'll look at parser.go...                   ││
│  │                                                     ││
│  │ Claude: Fixed. Added nil check before...            ││
│  │                                                     ││
│  │ [Send message...]                                   ││
│  └─────────────────────────────────────────────────────┘│
└─────────────────────────────────────────────────────────┘
```

### Multi-agent: Split-pane layout

The session detail page splits into columns when multiple threads are active. Each column is a self-contained chat with its own status, input, and scroll. A persistent `[+]` button in the thread header bar lets users add threads at any time.

#### Single-thread (default — identical to today)

Every session starts with one thread. The layout is unchanged from the current single-agent experience. The only addition is a subtle `[+]` affordance in the header that signals "you can add another agent here."

```
┌────────────────────────────────────────────────────────────────┐
│  Session: Fix null pointer in parser                            │
│  Status: ● running    Branch: fix/null-pointer                  │
│  [Overview] [Changes] [Validation]                               │
│                                                                  │
│  ┌────────────────────────────────────────────────────────┐ [+] │
│  │ claude · running                                       │     │
│  │──────────────────────────────────────────────────────── │     │
│  │                                                        │     │
│  │ User: Fix the null pointer in parser.go line 42        │     │
│  │                                                        │     │
│  │ Claude: I'll look at parser.go...                      │     │
│  │                                                        │     │
│  │ Claude: Fixed. Added nil check before dereference.     │     │
│  │                                                        │     │
│  │ [Send message...]                                      │     │
│  └────────────────────────────────────────────────────────┘     │
└────────────────────────────────────────────────────────────────┘
```

The `[+]` button sits at the far right of the thread header row — visually quiet but always discoverable. Clicking it opens the "Add thread" popover (see below).

#### Multi-thread (2-3 threads, wide screen)

When the user adds a thread, the layout splits into side-by-side columns. Each column scrolls independently.

```
┌────────────────────────────────────────────────────────────────────────────┐
│  Session: Add audit logging                                                │
│  Status: ● 2 running    Branch: feat/audit-logs                            │
│  [Overview] [Changes] [Validation]                                          │
│                                                                             │
│  ┌──────────────────────────┐ ┌──────────────────────────┐ ┌──────┐        │
│  │ Backend API              │ │ Frontend UI              │ │ [+]  │        │
│  │ ● claude · running       │ │ ● codex · running        │ │      │        │
│  │──────────────────────────│ │──────────────────────────│ │      │        │
│  │                          │ │                          │ │      │        │
│  │ User: Build the audit    │ │ User: Build the audit    │ │      │        │
│  │ log API endpoints.       │ │ log viewer page.         │ │      │        │
│  │                          │ │                          │ │      │        │
│  │ Claude: Looking at       │ │ Codex: I'll create the   │ │      │        │
│  │ handlers/sessions.go     │ │ page with DataTable...   │ │      │        │
│  │ for patterns...          │ │                          │ │      │        │
│  │                          │ │ Codex: Working on the    │ │      │        │
│  │ Claude: Created 3 files: │ │ filter tabs now...       │ │      │        │
│  │ audit.go, audit_store.go │ │                          │ │      │        │
│  │ audit_model.go           │ │                          │ │      │        │
│  │                          │ │                          │ │      │        │
│  │ [Send message...]        │ │ [Send message...]        │ │      │        │
│  └──────────────────────────┘ └──────────────────────────┘ └──────┘        │
└────────────────────────────────────────────────────────────────────────────┘
```

The `[+]` button becomes a narrow column at the right edge — always visible, always in the same position regardless of thread count. It visually rhymes with the thread columns but is clearly a different element (muted background, dashed border, centered `+` icon). This follows the same pattern as Trello's "+ Add another list" column or Notion's "+ New column" — users intuitively understand it means "add another one of these."

#### The "Add thread" popover

Clicking `[+]` opens a compact popover anchored to the button:

```
┌──────────────────────────────────┐
│  Add agent thread                │
│                                  │
│  Agent:  [Claude Code ▾]        │
│                                  │
│  Label:  [                    ]  │
│          e.g. "Backend", "Tests" │
│                                  │
│  Instructions (optional):        │
│  ┌──────────────────────────┐    │
│  │                          │    │
│  │                          │    │
│  └──────────────────────────┘    │
│                                  │
│  [Cancel]            [Add ↵]    │
└──────────────────────────────────┘
```

Design details:
- **Agent selector** defaults to the same agent type as the existing thread. If the user wants to compare agents, they pick a different one.
- **Label** is required — it names the column. Placeholder text gives examples.
- **Instructions** are optional. If blank, the thread inherits the session's original instructions. If provided, the thread gets those specific instructions.
- **Keyboard shortcut**: `Cmd+Shift+T` opens the popover from anywhere on the session page.
- The popover closes on `Esc` or clicking outside.

#### Thread column header details

Each thread column has a compact header showing status and controls:

```
┌──────────────────────────────────────┐
│ Backend API                      [⋯] │
│ ● claude · running · 3m              │
│──────────────────────────────────────│
```

The `[⋯]` menu contains:
- **End thread** — stops the agent, marks thread as completed
- **Cancel thread** — aborts immediately, marks as cancelled
- **View diff** — shows only this thread's changes

No rename, no reorder, no drag-and-drop — keep it simple. Thread order is creation order.

#### Responsive behavior

- **≥ 1200px**: Side-by-side columns. Up to 3 threads fit comfortably; 4 threads work but columns get narrow.
- **< 1200px**: Columns collapse to horizontal tabs at the top of the chat area. Each tab shows the thread label and a status dot. The `[+]` becomes a `[+]` tab at the end.
- **Session with 1 thread**: Full-width single-agent layout. No tabs, no columns. Just the `[+]` button in the header.

```
Narrow screen (< 1200px):
┌────────────────────────────────────────────────────┐
│  Session: Add audit logging                         │
│  [Overview] [Changes] [Validation]                   │
│                                                      │
│  [Backend API ●] [Frontend UI ●] [+]                │
│  ┌──────────────────────────────────────────────┐   │
│  │ ● claude · running                           │   │
│  │                                              │   │
│  │ User: Build the audit log API endpoints.     │   │
│  │                                              │   │
│  │ Claude: Looking at handlers/sessions.go...   │   │
│  │                                              │   │
│  │ [Send message...]                            │   │
│  └──────────────────────────────────────────────┘   │
└────────────────────────────────────────────────────┘
```

---

## Session List Page

The sessions list page doesn't change much. Multi-agent sessions show differently in two ways:

1. **Agent column**: Shows "2 agents" or "claude + codex" instead of a single agent name
2. **Status**: Shows aggregate "2/3 running" instead of a single status

```
┌─────────┬─────────────────────────┬──────────────┬──────────┐
│ Status  │ Title                   │ Agent        │ Modified │
├─────────┼─────────────────────────┼──────────────┼──────────┤
│ ● 2/3   │ Add audit logging       │ claude+codex │ 2m ago   │
│ ●       │ Fix null pointer        │ codex        │ 10m ago  │
│ ● 1/1   │ Update dependencies     │ claude       │ 15m ago  │
└─────────┴─────────────────────────┴──────────────┴──────────┘
```

Single-thread sessions look identical to today. No visual noise for users who don't use multi-agent.

---

## Thread Lifecycle

### Creating threads

Every session starts with exactly one thread — the default agent working on the issue. This is identical to today's behavior. The session creation flow doesn't change at all.

Users add threads to a **running or idle session** via the `[+]` button described in the UX section above. The flow:

1. User starts a normal single-agent session (no change from today)
2. While watching the agent work, user decides they want a second agent — maybe to compare approaches, or to work on a different part of the problem
3. User clicks `[+]`, picks an agent type, writes a label and optional instructions
4. New thread starts immediately in its own container, on the same branch
5. The layout splits into side-by-side columns

This is intentionally the *only* way to create multi-thread sessions. We don't add multi-thread options to the session creation form, and we don't have the system auto-create threads. The mental model is simple: **start with one agent, add more if you want.**

#### Why this matters

The most common use case for multiple threads is **comparing agents** — "let me see how Claude and Codex each approach this." That's a decision the user makes *after* seeing the first agent's approach, not upfront. Forcing users to plan threads before they've even started watching defeats the purpose.

Less common but also supported: adding a second thread to handle a different part of the work (backend + frontend), or adding a "verification" thread that independently checks the first agent's output.

### Thread status transitions

```
pending → running → idle ←→ running → completed
                      ↓              → failed
                      → completed    → cancelled
                      → cancelled
```

Same as session statuses today, but per-thread. The session's overall status is derived:
- **running**: any thread is running
- **idle**: all threads are idle (waiting for user input)
- **completed**: all threads are completed
- **failed**: any thread failed (others may still be running)
- **partial**: some threads completed, some failed

---

## Branch & Merge Strategy

All threads in a session work on the **same branch**. Each thread's container starts from the same commit. When a thread finishes:

1. Thread commits its changes to the branch
2. If another thread already committed, the thread rebases/merges before pushing
3. Conflicts are flagged to the user (thread goes to `awaiting_input`)

This is the simplest model. If conflicts are common, we can add file-scope enforcement (thread A can only touch files in `internal/`, thread B only in `frontend/`), but start without it.

---

## API Changes

### New endpoints

```
POST   /api/v1/sessions/{id}/threads              -- Create a new thread
GET    /api/v1/sessions/{id}/threads              -- List threads for a session
GET    /api/v1/sessions/{id}/threads/{tid}        -- Get thread detail
POST   /api/v1/sessions/{id}/threads/{tid}/messages  -- Send message to a thread
GET    /api/v1/sessions/{id}/threads/{tid}/messages  -- Get thread messages
POST   /api/v1/sessions/{id}/threads/{tid}/end    -- End a specific thread
GET    /api/v1/sessions/{id}/threads/{tid}/logs   -- Get thread logs
```

### Modified endpoints

```
GET    /api/v1/sessions/{id}                      -- Now includes threads[] in response
POST   /api/v1/sessions/manual                    -- Accepts optional threads[] array
GET    /api/v1/sessions/{id}/changes              -- Combined diff across all threads
```

### Backwards compatibility

Existing single-agent sessions continue to work. The API auto-creates a single thread when none is specified. Existing `POST /sessions/{id}/messages` sends to the default (first) thread.

---

## How PM Plans, Projects, and Threads Intersect

### Current architecture (without threads)

```
Projects (persistent)              PM Plans (ephemeral, per-cycle)
────────────────────               ─────────────────────────────
User creates project    ──────▶   PM agent sees active projects
  "Migrate to GraphQL"            during analysis cycle
                                         │
                                         ▼
                                  Plan output contains:
                                  ├─ Tasks[]        (reactive: fix issues)
                                  ├─ ProjectPlans[] (proactive: advance projects)
                                  ├─ Clusters[]     (root cause groupings)
                                  └─ NewProjects[]  (suggestions for user)
                                         │
                          ┌──────────────┴──────────────┐
                          ▼                              ▼
                    Reactive Tasks               Project Tasks
                    1 Task → 1 Session           1 ProjectTask → 1 Session
                    (session.pm_plan_id)         (session.project_task_id)
                                                 (session.pm_plan_id)
```

**The binding is always: 1 task → 1 session → 1 agent.**

### With threads: what changes, what stays the same

**PM Plans don't change at all.** They remain the analytical layer — the PM agent decides *what* to work on and *how many* tasks to create. The PM always creates single-threaded sessions. No `Threads` field on `Task`, no multi-agent logic in the PM prompt.

**Projects don't change.** They remain persistent goal containers with execution_mode and max_concurrent. ProjectTasks are still created per PM cycle, tracked across cycles.

**The only change is at the session layer.** Sessions now *support* multiple threads, but they're always created with one. The user adds threads manually via the `[+]` button if they want to compare agents or parallelize work.

```
PM/Project creates:   1 task → 1 session → 1 thread (always)
User can then add:    same session → 2nd thread, 3rd thread, etc.
```

### Why the PM doesn't create multi-threaded sessions

Multi-thread sessions are a **user-initiated comparison and exploration tool**, not an automated optimization. The primary use case is "let me see how Claude and Codex each approach this problem" — that's an inherently human decision that requires watching the first agent and making a judgment call.

Reasons to keep PM single-threaded:
1. **Simplicity** — the PM's job is hard enough (triage, prioritize, plan). Adding thread-planning complexity increases failure modes without clear ROI.
2. **Cost predictability** — automated multi-threading could silently double or triple agent costs. Users should opt in to that.
3. **The PM already parallelizes at the session level.** If the PM wants 3 things done in parallel, it creates 3 tasks → 3 sessions. That's the right abstraction for automated parallelism.
4. **Threads are for human judgment calls** — comparing agents, adding a verification pass, splitting work mid-session. These are reactive decisions, not plannable ones.

### How projects interact with threads

Projects control **how many sessions run in parallel** via `execution_mode` and `max_concurrent`. Threads are *within* a session, so they don't affect the project's concurrency model at all.

```
Project (execution_mode: parallel, max_concurrent: 3)
│
├─ ProjectTask A → Session 1 (user added 2nd thread)  ← still counts as 1 session
├─ ProjectTask B → Session 2 (single thread)           ← counts as 1 session
├─ ProjectTask C → Session 3 (single thread)           ← counts as 1 session
│
└─ ProjectTask D → waiting (max_concurrent reached)
```

**Threads don't consume concurrency slots.** A session with 3 threads counts as 1 session toward `max_concurrent`. This is intentional — threads are user-initiated exploration within a unit of work, not additional units of work.

To prevent cost sprawl, we cap threads per session at the org level:

```go
type Organization struct {
    // ...existing fields...
    MaxThreadsPerSession int `json:"max_threads_per_session"` // default: 4
}
```

### The clean layering

```
┌─────────────────────────────────────────────────┐
│  Projects                                        │
│  "What are we building long-term?"               │
│  Controls: execution_mode, max_concurrent,       │
│            lifecycle (draft → active → done)     │
├─────────────────────────────────────────────────┤
│  PM Plans                                        │
│  "What should we work on right now?"             │
│  Controls: which issues, which projects get      │
│            slots, task decomposition, agent       │
│            selection                             │
├─────────────────────────────────────────────────┤
│  Sessions                                        │
│  "A workspace for producing one PR."             │
│  Contains: threads, branch, combined diff,       │
│            validation results                    │
├─────────────────────────────────────────────────┤
│  Threads                                         │
│  "One agent doing one piece of the work."        │
│  User-initiated. Contains: chat, container,      │
│  agent state, individual diff                    │
└─────────────────────────────────────────────────┘
```

Each layer has a clear responsibility:

| Layer | Decides | Persists? | Scope |
|-------|---------|-----------|-------|
| **Project** | What goals to pursue, how many sessions can run in parallel | Yes (across cycles) | Long-lived |
| **PM Plan** | What to work on this cycle, which agent, task decomposition | No (ephemeral JSON) | One cycle |
| **Session** | Nothing — it's the execution container, starts with 1 thread | Yes | One PR |
| **Thread** | Nothing — user-created, executes instructions | Yes | One agent's work |

### What doesn't change

- **PM plan creation flow** (`pm/service.go` → `Analyze()`) — unchanged, no thread awareness needed
- **PM plan execution flow** (`pm/execute.go` → `executePlan()`) — unchanged, creates single-thread sessions as before
- **Project task dispatch** (`pm/project_execute.go`) — unchanged, creates single-thread sessions as before
- **Project execution_mode** — still controls session-level parallelism
- **Decision log** — still tracks PM decisions at the task level
- **Slot allocation** — still counts sessions, not threads
- **The PM agent prompt** — no changes needed
- **The Task struct** — no changes needed

### Example: user adds a thread to a PM-created session

```
1. PM creates a reactive task:
   Task: "Fix auth token expiry" → Session (claude_code, single thread)

2. User opens the session, watches Claude working on the fix

3. User thinks: "I wonder if Codex would approach this differently"
   → Clicks [+], selects Codex, labels it "Codex approach"
   → New thread starts in its own container, same branch

4. Both agents work in parallel. User compares approaches
   in the split-pane view.

5. User decides Claude's approach is better:
   → Ends the Codex thread (changes discarded)
   → Claude's thread continues → PR

6. Alternatively, user could end Claude's thread and keep Codex's.
```

This keeps the PM simple and predictable while giving users the power to explore when they want to.

---

## Open Questions

1. **File scope enforcement**: Should we enforce that threads only modify files in their `file_scope`, or keep it advisory? Enforcement prevents conflicts but limits flexibility.
2. **Thread-to-thread communication**: Can thread A read what thread B has done? Currently no (separate containers). Should we add a "sync" step where a thread pulls the latest from the branch?
3. **Cost visibility**: Multiple threads = multiple agent runs = higher costs. Should we show per-thread cost in the column header? Should the `[+]` popover show an estimated cost warning?
4. **Discarding a thread's changes**: When the user ends a thread whose changes they don't want (the "compare and pick" flow), how do we cleanly discard that thread's commits from the branch? Revert commits? Separate branches per thread?
5. **Validation**: Run validation per-thread (each thread's diff independently) or once on the combined result? Per-thread catches issues earlier; combined catches integration issues.
6. **Thread limit**: Default `max_threads_per_session` is 4. Is that right? Too low limits the compare-3-agents use case. Too high risks cost surprise.
7. **Future: PM-initiated threads**: If we later decide the PM should create multi-threaded sessions (e.g., for cross-domain features), the data model supports it — we just add a `Threads` field to `Task`. But we're explicitly deferring this.
