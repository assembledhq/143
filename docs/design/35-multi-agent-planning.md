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

### Multi-agent: Three approaches for the session detail page

---

#### Approach A: Thread tabs (horizontal tabs per agent)

Each thread gets a tab within the Chat panel. You switch between agent conversations like browser tabs.

```
┌────────────────────────────────────────────────────────────────┐
│  Session: Add audit logging                                    │
│  Status: ● 2/3 threads active    Branch: feat/audit-logs      │
│                                                                │
│  [Chat] [Overview] [Logs] [Changes] [Validation]               │
│                                                                │
│  Chat:                                                         │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │ [Backend API ●] [Frontend UI ●] [Tests ○]               │  │
│  │                                                          │  │
│  │  User: Build the audit log API endpoints.                │  │
│  │        Follow the patterns in handlers/sessions.go.      │  │
│  │                                                          │  │
│  │  Claude: I'll create the handler, store, and model...    │  │
│  │                                                          │  │
│  │  Claude: Done. Created:                                  │  │
│  │    - internal/api/handlers/audit.go                      │  │
│  │    - internal/db/audit_store.go                          │  │
│  │    - internal/models/audit.go                            │  │
│  │                                                          │  │
│  │  [Send message...]                                       │  │
│  └──────────────────────────────────────────────────────────┘  │
│                                                                │
│  Changes (combined):                                           │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │ + internal/api/handlers/audit.go  (Backend API, claude)  │  │
│  │ + internal/db/audit_store.go      (Backend API, claude)  │  │
│  │ ~ frontend/src/app/audit/page.tsx (Frontend UI, codex)   │  │
│  │                                                          │  │
│  │ Filter: [All threads ▾]                                  │  │
│  └──────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────┘
```

**Pros:**
- Familiar tab pattern. Each thread has a focused conversation.
- Easy to send a message to a specific agent — you're already on their tab.
- Changes tab can show combined diff with per-thread attribution.
- Minimal visual complexity — only one chat visible at a time.

**Cons:**
- You can't see what multiple agents are doing at the same time — you have to click between tabs.
- For 2-3 threads this is fine, but doesn't scale to 5+ (tab bar overflows).
- Doesn't give the "command center" feeling of monitoring parallel work.

---

#### Approach B: Split-pane columns (side-by-side agent chats)

The session detail page splits into columns, one per active thread. Each column is a self-contained chat + status view.

```
┌────────────────────────────────────────────────────────────────────────┐
│  Session: Add audit logging                                            │
│  Status: ● 2/3 threads active    Branch: feat/audit-logs              │
│  [Overview] [Changes] [Validation]                                     │
│                                                                        │
│  ┌──────────────────────┐ ┌──────────────────────┐ ┌─────────────────┐│
│  │ Backend API          │ │ Frontend UI          │ │ Tests           ││
│  │ ● claude · running   │ │ ● codex · running    │ │ ○ claude · pend ││
│  │─────────────────────│ │─────────────────────│ │                 ││
│  │                      │ │                      │ │  Waiting for    ││
│  │ User: Build the      │ │ User: Build the      │ │  Backend API    ││
│  │ audit log API...     │ │ audit log viewer...  │ │  to complete    ││
│  │                      │ │                      │ │                 ││
│  │ Claude: Looking at   │ │ Codex: I'll create   │ │                 ││
│  │ the patterns in      │ │ the page component   │ │                 ││
│  │ handlers/sessions... │ │ with the data table  │ │                 ││
│  │                      │ │ pattern...           │ │                 ││
│  │ Claude: Created      │ │                      │ │                 ││
│  │ 3 files...           │ │ Codex: Working on    │ │                 ││
│  │                      │ │ the filter tabs...   │ │                 ││
│  │                      │ │                      │ │                 ││
│  │ [Send message...]    │ │ [Send message...]    │ │                 ││
│  └──────────────────────┘ └──────────────────────┘ └─────────────────┘│
│                                                                        │
└────────────────────────────────────────────────────────────────────────┘
```

**Pros:**
- You see all agents working simultaneously — the "command center" feel.
- Each thread has its own chat input — you can message any agent without switching.
- Visually clear which agent is doing what, which is done, which is blocked.
- The "wow" factor — this is what makes multi-agent feel real.

**Cons:**
- Horizontal space is limited. 3 columns is fine on a wide screen, 4+ gets cramped.
- Each column has less room for chat content than a full-width view.
- Mobile/narrow screens need a fallback (probably Approach A's tab pattern).
- More complex to implement — responsive layout, scroll sync, etc.

---

#### Approach C: Unified timeline (interleaved chat with agent badges)

All threads share one chat timeline. Messages are tagged with which agent/thread they belong to. The user can @-mention a specific thread or broadcast to all.

```
┌────────────────────────────────────────────────────────────────┐
│  Session: Add audit logging                                    │
│  Status: ● 2/3 threads active    Branch: feat/audit-logs      │
│                                                                │
│  [Chat] [Overview] [Changes] [Validation]                      │
│                                                                │
│  Threads: [Backend API ●] [Frontend UI ●] [Tests ○]           │
│                                                                │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │                                                          │  │
│  │  You (to all):                                           │  │
│  │  Build audit logging. Backend: API endpoints following   │  │
│  │  existing patterns. Frontend: viewer page with filters.  │  │
│  │  Tests: after both are done.                             │  │
│  │                                                          │  │
│  │  ┌─ Backend API · claude ─────────────────────────────┐  │  │
│  │  │ Looking at handlers/sessions.go for patterns...    │  │  │
│  │  └───────────────────────────────────────────────────┘  │  │
│  │                                                          │  │
│  │  ┌─ Frontend UI · codex ──────────────────────────────┐  │  │
│  │  │ Creating audit log viewer with DataTable pattern.  │  │  │
│  │  └───────────────────────────────────────────────────┘  │  │
│  │                                                          │  │
│  │  ┌─ Backend API · claude ─────────────────────────────┐  │  │
│  │  │ Created 3 files. Endpoints: GET /audit-logs,       │  │  │
│  │  │ GET /audit-logs/:id. Running tests...              │  │  │
│  │  └───────────────────────────────────────────────────┘  │  │
│  │                                                          │  │
│  │  You (to Frontend UI):                                   │  │
│  │  Use the same filter tab pattern from the sessions page. │  │
│  │                                                          │  │
│  │  ┌─ Frontend UI · codex ──────────────────────────────┐  │  │
│  │  │ Got it, switching to FilterTabs component...       │  │  │
│  │  └───────────────────────────────────────────────────┘  │  │
│  │                                                          │  │
│  │  [Send to: All threads ▾] [Message...]          [Send]  │  │
│  └──────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────┘
```

**Pros:**
- Single scrolling timeline — you see the full story of what happened, in order.
- Natural for monitoring: you just scroll and see what all agents are doing.
- Easy to broadcast instructions to all threads or target one.
- Works well on any screen width — no column layout needed.
- Feels like a team group chat where each agent is a team member.

**Cons:**
- Gets noisy with many active threads — messages interleave in confusing ways.
- Harder to follow one agent's train of thought — it's broken up by other agents' messages.
- The "Send to" dropdown adds friction vs. just typing in a thread's input box.
- Need clear visual differentiation per thread (colors, borders) or it becomes unreadable.

---

### Recommendation

**Approach B (split-pane) as the primary view, with Approach A (tabs) as the narrow-screen fallback.**

Rationale:

1. **The core value of multi-agent is seeing agents work in parallel.** If you can only see one at a time (Approach A), it doesn't feel like multi-agent — it feels like switching between sessions, which is what we already have.

2. **Split-pane is the conductor.build insight.** What makes conductor feel powerful is seeing multiple agents side-by-side. We can do this within a session detail page instead of requiring a separate app.

3. **Approach C (unified timeline) gets unreadable fast.** Two agents posting messages simultaneously creates a messy interleaved timeline. It works for Slack because humans have natural pauses. Agents don't — they'll flood the timeline.

4. **Responsive fallback to tabs.** On screens narrower than ~1200px, collapse columns into tabs. Same data, different layout. The tab pattern (Approach A) is the mobile/narrow fallback, not the primary experience.

### Layout breakpoints

- **≥ 1200px**: Side-by-side columns (Approach B)
- **< 1200px**: Thread tabs (Approach A)
- **Session with 1 thread**: Current single-agent layout (no change)

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

Threads are created when the session starts. The user specifies the split:

**Option 1: Chat-based creation (interactive)**
```
User: "I need to add audit logging. Use Claude for the backend
       and Codex for the frontend."

System creates session with 2 threads:
  - Thread "Backend" (claude_code)
  - Thread "Frontend" (codex)
```

**Option 2: Explicit thread creation in the new-session form**
The `/sessions/new` page gets a "Add thread" button:
```
┌──────────────────────────────────────────────────┐
│  New Session                                      │
│                                                   │
│  Thread 1:                                        │
│  Label: [Backend API        ]                     │
│  Agent: [Claude Code ▾]                           │
│  Instructions: [Build audit log endpoints...  ]   │
│                                                   │
│  Thread 2:                                        │
│  Label: [Frontend UI         ]                    │
│  Agent: [Codex ▾]                                 │
│  Instructions: [Build audit log viewer page...]   │
│                                                   │
│  [+ Add thread]                                   │
│                                                   │
│  [Start session]                                  │
└──────────────────────────────────────────────────┘
```

**Option 3: Add threads to a running session**
While a session is active (or idle), the user can add a new thread:
```
[+ Add agent thread]  →  picks agent type, writes instructions, starts it
```

This is the most flexible: start with one agent, add more as needed.

### Recommendation: Support all three, start with Option 3

Option 3 is the most natural and lowest-friction. You start a normal session. If you realize you want a second agent working in parallel, you add a thread. No upfront planning required.

Option 2 is the power-user path for when you know upfront you want multiple agents.

Option 1 (chat-based) is a future enhancement — the system parses your intent and auto-creates threads.

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

## Integration with PM Plans

PM plans continue to create sessions. For multi-agent plans, the PM agent can specify threads in the session creation:

```go
session := &models.Session{
    IssueID: issueID,
    OrgID:   orgID,
    Status:  "pending",
    PMPlanID: &plan.ID,
}

// PM agent decided this issue needs two agents
threads := []models.SessionThread{
    {Label: "Backend fix", AgentType: "claude_code", Instructions: "..."},
    {Label: "Test coverage", AgentType: "codex", Instructions: "..."},
}
```

This keeps PM plans as the orchestration layer while sessions handle the multi-agent execution.

---

## Open Questions

1. **File scope enforcement**: Should we enforce that threads only modify files in their `file_scope`, or keep it advisory? Enforcement prevents conflicts but limits flexibility.
2. **Thread-to-thread communication**: Can thread A read what thread B has done? Currently no (separate containers). Should we add a "sync" step where a thread pulls the latest from the branch?
3. **Cost controls**: Multiple threads = multiple agent runs = higher costs. Should we show estimated cost before starting? Should there be a max threads-per-session limit?
4. **Thread dependencies**: Should threads support "wait for thread X to complete before starting"? The PLAN.md format had `Depends on` — do we carry that into threads?
5. **Validation**: Run validation per-thread (each thread's diff independently) or once on the combined result? Per-thread catches issues earlier; combined catches integration issues.
