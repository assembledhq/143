# Design: Multi-Agent Sessions

## Problem

Today, each session runs exactly one coding agent. But users sometimes want to compare how different agents approach the same problem, or split work across agents (e.g., Claude on the backend, Codex on the frontend) — all within the same session, on the same branch, producing one PR.

The goal: **a single session can have multiple coding agents running at the same time**, each with their own chat thread the user can interact with side-by-side.

### What this is NOT

- **Not separate sessions with a parent.** That fragments the experience — you'd have to click between session detail pages. The user wants everything in one place.
- **Not isolated containers or worktrees.** Some tools give each agent its own filesystem copy. We don't. Our agents share a single container, a single filesystem, and a single branch — like two terminal tabs on the same machine. This is simpler, cheaper, and matches how the primary use cases actually work (comparing approaches, or working on different files).

---

## Design Principles

1. **Single-agent by default** — Every session starts with one agent. This is identical to today. Multi-agent is opt-in, never automatic.
2. **Shared container, shared filesystem** — All agents in a session run inside the same Docker container, on the same branch, with the same filesystem. Think of it like multiple tmux panes in the same terminal — each agent is an independent process, but they all see the same files and the same git state.
3. **Threads as the unit** — Each agent within a session is a "thread" — a parallel lane of work with its own conversation and agent process. The session ties them together. Threads share the container and filesystem.
4. **User-initiated, never automated** — The PM and automated systems always create single-thread sessions. Only the user adds threads, primarily to compare agents or split work.

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
    agent_session_id TEXT,                     -- external agent session ID (process within shared container)
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

Sessions get lighter. The `agent_session_id` moves to threads. Container-level fields (`container_id`, `sandbox_state`, `snapshot_key`) stay on the session since all threads share the same container. The session keeps:

- Identity: `id`, `org_id`, `issue_id`
- Container: `container_id`, `sandbox_state`, `snapshot_key` (shared by all threads)
- Lifecycle: `status` (derived from thread statuses — active if any thread is active)
- Metadata: `triggered_by_user_id`, `pm_plan_id`, `project_task_id`
- The existing `agent_type` field becomes the "default" agent type (used for single-thread sessions for backwards compatibility)

### Backwards compatibility

Single-agent sessions still work. When a session has exactly one thread, the UX is identical to today. The thread is implicit — the API can auto-create it. Existing sessions are migrated to have one thread each.

---

## Session Detail Page

### Single-thread (default — identical to today)

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

### Multi-thread: Split-pane layout

When the user adds a thread, the layout splits into side-by-side columns. Each column is a self-contained chat with its own status, input, and independent scroll.

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

---

## Adding a Thread

### The `[+]` button

The `[+]` is the only entry point for creating threads. It's always visible:
- **Single-thread sessions**: sits at the far right of the thread header row
- **Multi-thread sessions**: becomes the rightmost narrow column

Keyboard shortcut: `Cmd+Shift+T` opens the popover from anywhere on the session page.

### The "Add thread" popover

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
- **Label** is required — it names the column. Placeholder text gives examples ("Backend", "Tests", "Codex attempt").
- **Instructions** are optional. If blank, the thread inherits the session's original instructions (the most common case for "compare agents" — same problem, different agent). If provided, the thread gets those specific instructions.
- The popover closes on `Esc` or clicking outside.

### What happens after clicking "Add"

1. A new agent process starts inside the session's existing container — same filesystem, same branch, same working directory. No new container is created.
2. If this is the first additional thread, the layout transitions from single-pane to split-pane (animation: the existing chat slides left, new column slides in from right)
3. The new thread appears as the rightmost column, with the `[+]` button shifting further right
4. The new agent begins working immediately — it can see the current state of the repo, including any uncommitted changes from the first agent

---

## Thread Column Design

### Column header

Each thread column has a compact header showing status and controls:

```
┌──────────────────────────────────────┐
│ Backend API                      [⋯] │
│ ● claude · running · 3m              │
│──────────────────────────────────────│
```

- **Label**: the name the user gave when creating the thread
- **Status dot**: green (running), yellow (idle/awaiting input), red (failed), gray (completed/cancelled)
- **Agent + duration**: which agent, how long it's been running
- **`[⋯]` overflow menu**: thread actions (see below)

### Thread overflow menu `[⋯]`

- **End thread** — stops the agent gracefully, marks thread as completed, keeps its changes
- **Cancel thread** — aborts immediately, marks as cancelled, discards changes
- **View diff** — shows only this thread's changes (vs. the combined diff on the Changes tab)

No rename, no reorder, no drag-and-drop. Thread order is creation order. Keep it simple.

### Column content

Each column is a standard chat view:
- Scrollable message history (independent scroll per column)
- Agent messages with the same formatting as today's single-agent chat
- `[Send message...]` input at the bottom of each column
- Users can interact with each agent independently — send a message to one without affecting the other

---

## Responsive Behavior

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

Multi-agent sessions show differently in two ways:

1. **Agent column**: Shows "claude + codex" instead of a single agent name
2. **Status**: Shows "2/3 running" instead of a single status

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

## Changes Tab (Combined Diff)

The existing Changes tab shows a combined diff across all threads. Each file is annotated with which thread produced it:

```
┌──────────────────────────────────────────────────────────────┐
│  Changes (2 threads)                                          │
│                                                               │
│  Filter: [All threads ▾]                                     │
│                                                               │
│  + internal/api/handlers/audit.go      Backend API · claude  │
│  + internal/db/audit_store.go          Backend API · claude  │
│  + internal/models/audit.go            Backend API · claude  │
│  ~ frontend/src/app/audit/page.tsx     Frontend UI · codex   │
│  + frontend/src/app/audit/columns.tsx  Frontend UI · codex   │
│                                                               │
│  [View diff ▾]                                               │
└──────────────────────────────────────────────────────────────┘
```

The filter dropdown lets users view changes from a specific thread only. This is the same data available via "View diff" in the thread's `[⋯]` menu, but accessible from the Changes tab for quick comparison.

---

## Thread Lifecycle

### Creating threads

Every session starts with exactly one thread — the default agent working on the issue. This is identical to today's behavior. The session creation flow doesn't change at all.

Users add threads to a **running or idle session** via the `[+]` button. The flow:

1. User starts a normal single-agent session (no change from today)
2. While watching the agent work, user decides they want a second agent — maybe to compare approaches, or to work on a different part of the problem
3. User clicks `[+]`, picks an agent type, writes a label and optional instructions
4. A new agent process starts inside the same container — no new container, no filesystem copy. It's like opening a second terminal tab.
5. The layout splits into side-by-side columns

This is intentionally the *only* way to create multi-thread sessions. We don't add multi-thread options to the session creation form, and we don't have the system auto-create threads. The mental model is simple: **start with one agent, add more if you want.**

#### Why single-agent default matters

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

### The "compare and pick" flow

The most common multi-thread workflow:

1. Session starts with Claude working on a bug fix
2. User adds a Codex thread with the same instructions
3. Both agents work in parallel in the same container — user watches side-by-side
4. Claude finishes first with a clean fix. Codex is still working.
5. User reviews Claude's diff in the column. Looks good.
6. User ends the Codex thread via `[⋯]` → "End thread" (stops the agent process)
7. Session continues with the changes on the branch → PR

Since both agents share the filesystem, the user can also let both finish and then review the combined result. There's no "discard" step needed — the branch simply has all the commits from both agents, and the user can ask an agent to revert anything they don't want.

The inverse is also fine — user might prefer Codex's approach and end Claude's thread instead.

### The "split work" flow

The other common workflow:

1. Session starts with Claude working on backend API endpoints
2. User adds a Codex thread with instructions to build the frontend UI
3. Both agents work simultaneously — Claude creates `handlers/audit.go`, Codex creates `app/audit/page.tsx`
4. Because they share the filesystem, Codex can see the API types Claude defined and import them
5. Both threads complete. The branch has a coherent set of changes — backend + frontend — ready for PR

This flow works naturally because the agents see each other's file changes in real time, just like two developers pairing on the same machine.

---

## Shared Container Model

All threads in a session run as **separate agent processes inside the same Docker container**. They share the filesystem, the git repo, and the branch. This is the mental model of two tmux panes pointed at the same directory.

### Why shared container

1. **No merge conflicts by default.** Both agents see the same files in real time. If agent A writes `parser.go` and agent B reads it a moment later, B sees A's changes. This is the natural behavior users expect — it's how two developers working on the same machine would operate.
2. **No branch gymnastics.** No sub-branches, no rebasing, no cherry-picking, no "which thread's commits do we keep." There's one branch, one working tree, one set of commits. The agents commit to it as they work, just like two people would.
3. **Dramatically simpler infrastructure.** No container-per-thread provisioning, no filesystem snapshots, no copy-on-write. Adding a thread is starting a process, not spinning up a VM.
4. **The primary use cases don't conflict.** Users will mostly use multi-thread to (a) get two reviews/approaches on the same problem, or (b) work on different parts of the codebase simultaneously (backend + frontend). Neither case involves two agents editing the same file at the same time.

### What about file conflicts?

In rare cases, two agents might edit the same file simultaneously. This is handled the same way it would be if two developers were pair-programming on the same machine:

- Agents use standard file read/write operations. The last write wins at the filesystem level.
- If an agent's coding tool detects the file changed since it last read it, the agent should re-read and reconcile — this is standard behavior for tools like Claude Code that check file state before editing.
- For the "compare agents" use case (same instructions, different agents), the user typically cancels one thread before it commits anyway. The agents are racing to produce a solution, not collaborating on one.

We explicitly do **not** add file locking, file-scope enforcement, or conflict detection in v1. The shared filesystem model is simple, and the use cases don't demand it. If we see users hitting conflicts in practice, we can add advisory file scopes later.

### Git workflow

Since all agents share the same working tree:

- Agents commit directly to the session's branch as they work
- All commits appear in a single linear history on the branch
- The diff for each thread is tracked by recording which commits belong to which thread (via commit metadata or the `session_threads.diff` field)
- Cancelling a thread doesn't require reverting commits — the user can simply stop the agent. If the cancelled thread already committed changes the user doesn't want, they can ask the remaining agent to revert them, or we can offer a "Revert thread's commits" action in the `[⋯]` menu

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

## Relationship to PM Plans and Projects

**PM plans and projects always create single-thread sessions.** Multi-threading is exclusively user-initiated.

This is intentional:
1. **Simplicity** — the PM's job is hard enough (triage, prioritize, plan). Adding thread-planning increases failure modes without clear ROI.
2. **Cost predictability** — automated multi-threading could silently double or triple agent costs. Users should opt in.
3. **The PM already parallelizes at the session level.** 3 tasks = 3 sessions. That's the right abstraction for automated parallelism.
4. **Threads are for human judgment** — comparing agents, adding verification, splitting work mid-session. These are reactive decisions, not plannable ones.

Threads don't affect project concurrency. A session with 3 threads still counts as 1 session toward `max_concurrent`. Threads are parallel *within* a unit of work (one PR), not parallel units of work.

---

## Open Questions

1. **Cost visibility**: Should we show per-thread cost in the column header? Should the `[+]` popover show an estimated cost warning?
2. **Validation**: Run validation per-thread (each thread's diff independently) or once on the combined result? Per-thread catches issues earlier; combined catches integration issues.
3. **Thread limit**: Default max is 4 threads per session. Too low limits the compare-3-agents use case. Too high risks cost surprise.
4. **Commit attribution**: How do we attribute commits to specific threads? Options: (a) convention-based commit message tags, (b) a mapping table `thread_commits(thread_id, commit_sha)`, (c) record the diff at thread completion time and don't track individual commits.
