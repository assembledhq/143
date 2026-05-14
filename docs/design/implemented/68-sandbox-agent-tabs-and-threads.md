# Design: Sandbox Agent Tabs and Threads

> **Status:** Implemented | **Last reviewed:** 2026-05-08
>
> Phases 0-4 are implemented. Phase 0 data model compatibility and Phase 1
> single-running-thread tab UX shipped previously. Phase 2 added concurrent
> threads in one sandbox (relaxed `ClaimIdleForSession` admission with a
> per-session running cap of 3, thread-start checkpoint stamping via
> `base_snapshot_key`, file-touch attribution via `session_thread_file_events`,
> and overlap badges in the tab strip). Phase 3 added per-tab cost
> accounting (`cost_cents`), the `Touched by tab` /
> `Overlap` filter in the Changes view, and a queued-message counter
> (`pending_message_count`). Phase 4 added "Summarize all tabs" (a side panel
> that rolls up status + result_summary + touched files + overlap) and "Revert
> this tab's changes" (enqueues `revert_session_thread`). The legacy
> single-thread API stays as a compatibility alias; the new endpoints are
> additive.

Desktop tab-strip polish now also ships as part of the implemented surface:
the selected tab uses the shared purple underline treatment, tabs expose
per-tab archive `X` controls instead of a single strip-level close button,
and the blue dot is reserved for running/pending work or tabs the user has
not opened yet.

## Summary

143 should support **multiple independent agent tabs inside one sandbox**.
The user experience should feel like Conductor's workspace tabs: one copied
repo, one branch, one running environment, and several agent tabs that can
work, review, test, or challenge each other at the same time.

The product contract:

1. A **session** remains the durable unit of work, review, validation, and PR.
2. A session owns one **sandbox**: repository checkout, branch, credentials,
   preview, dev processes, and snapshot lifecycle.
3. A sandbox can contain multiple **agent tabs**. Each tab has its own
   model/provider, transcript, runtime process, status, cost accounting, and
   checkpoint boundary.
4. Tabs share the same filesystem and branch by default. That is the point:
   users choose this mode when agents need the same code state and should land
   one combined result.
5. The system makes collaboration legible through ownership hints, write
   attribution, conflict warnings, and combined review rather than pretending
   concurrent agents are isolated.
6. Adding a tab creates a fresh, blank tab in the same sandbox. It does not
   copy the prior transcript or automatically replay the original prompt.

In the UI we call these **tabs**. In backend code we call them **threads**
(the `session_threads` table backs them). Both names refer to the same object;
choose the term that matches the audience.

## Research Snapshot

Current multi-agent coding products split into two patterns:

- **Workspace isolation for independent work.** Conductor documents the core
  decision well: use separate workspaces when work has its own branch, app
  state, and PR path; use multiple agents in one workspace when they share code
  state and context. See
  [Conductor parallel agents](https://www.conductor.build/docs/concepts/parallel-agents)
  and
  [Conductor isolated workspaces](https://www.conductor.build/docs/concepts/workspaces-and-branches).
- **Terminal or pane multiplexing.** GridTerm, AgentPane, Remocode, and
  Superconductor all emphasize side-by-side agent panes/tabs, session restore,
  provider flexibility, and low-friction switching:
  [GridTerm](https://gridterm.com/),
  [AgentPane](https://agentpane.dev/),
  [Remocode](https://www.remocode.org/),
  [Superconductor](https://super.engineering/).
- **Orchestrated task fanout.** CallCode and webmux focus more on dispatching
  parallel tasks and tracking progress from one dashboard, often with isolated
  worktrees and integrated PR/CI status:
  [CallCode](https://callcode.dev/),
  [webmux](https://webmux.dev/).
- **Research platforms.** OpenHands frames agents as software developers that
  use terminals, file systems, browsers, and sandboxed execution environments,
  with explicit support for safe sandbox interaction and coordination:
  [OpenHands paper](https://arxiv.org/abs/2407.16741).

The lesson for 143: expose both modes, but do not blur them.

| User intent | Correct unit | Why |
| --- | --- | --- |
| Two issues can ship independently | Two sessions / sandboxes | Separate branches, validation, PRs, and rollback paths |
| One feature needs implementation plus test repair | One session, multiple tabs | Agents need the same branch and current diff |
| Ask another agent for a second opinion on one fix | One session, multiple tabs | The user wants one final chosen diff |
| Frontend and backend must land together | One session, multiple tabs | Shared types and integration tests matter |
| Risky alternative architecture | Separate sessions / sandboxes | User may discard one whole branch |

## Problem

Today each interactive coding session effectively has one active agent. Users
can queue follow-up instructions to that agent, but they cannot:

- launch a second Codex tab in the same sandbox to review or test the first
  agent's work
- ask Claude, Codex, Gemini, Amp, or a custom CLI agent to inspect the same
  branch and current filesystem state
- split one feature across specialist lanes while preserving one branch and PR
- keep a visible "reviewer" or "test fixer" tab beside an implementation tab
- ask one agent to inspect another agent's diff without manually copying state
  between sessions

The current single-thread assumption also leaks into runtime architecture:

- agent process identity is coupled too closely to the session
- snapshots are session-level, but in-flight mutations are not attributed well
  enough to a thread
- cancellation, status, runtime budgets, and cost accounting are not naturally
  per-thread
- PR readiness is combined, while per-tab state is singular

## Product Principles

### 1. Start simple, expand in place

Every session starts with one tab. The user can add another tab only after the
sandbox exists. Creation flows should not force users to pre-plan a whole agent
team.

### 2. One sandbox means one final artifact

Multiple tabs in one sandbox produce one branch, one validation result, and one
PR path. If the user wants separate PRs, the product should guide them to
separate sessions.

The strongest product promise is: this is still **one change**. Multi-tab mode
does not create multiple tasks, branches, or PRs. It gives the user more than
one agent tab attached to the same change.

### 3. Tabs are agent lanes, not browser tabs

An agent tab is a durable lane with its own transcript and process state. It
may be viewed as a tab, split pane, or pop-out, but the backing object (a
thread, in the backend) is the same.

### 4. Make parallel work inspectable

The user should be able to answer:

- which agents are running
- what each is trying to do
- what files each has touched
- whether two agents are editing the same file
- who is blocked, who needs input, and who is burning time
- what the combined branch looks like right now

### 5. Prefer advisory coordination before hard locking

Hard file locks are tempting, but they will often make agents worse by blocking
legitimate refactors. Start with ownership hints, collision detection, and clear
review affordances. Add enforcement only for proven failure modes.

## Primary UX

### Main Tab Shell

The agent tab strip belongs directly above the transcript and shared composer.
It should not live in the side panel that shows session details, PR health,
linked issues, or metadata.

Tab titles should stay plain and stable. The visible label is just the tab's
name, such as `Main` or `Claude 2`; status belongs in the separate dot/badges,
not appended to the title text. Avoid labels like `Main -- idle` or
`Claude • running` in the title itself.

When there is only one tab, the default UI should continue to feel like
today's single-agent session. The tab system should be discoverable but quiet:

- show the current agent/tab label in the tab header
- include a small `+` affordance near that label
- do not render a heavy multi-tab strip until there is more than one tab

The wireframes below show only the main agent surface — the area that
contains the transcript and the shared composer. Session-level chrome
(title, branch, PR status, and the Overview/Changes/Validation subnav)
lives above or beside this area, not inside it.

Single tab — today's experience plus a quiet `+` affordance in the header:

```text
Codex • running                                                   [ + ]
──────────────────────────────────────────────────────────────────────

  > User: Add an audit log export endpoint.
  Codex: Looking at internal/api/handlers/audit.go ...
  Codex: Created export handler and wired it into the router.

──────────────────────────────────────────────────────────────────────
[ Send a message to Codex... ]                                      ↩
```

Multi-tab — after the user adds a second tab, the strip expands into a
Conductor-like row directly above the transcript. The active tab's
transcript and composer fill the area below:

```text
[ Codex • running ]   ▸ Claude • running ◂   [ Codex 2 • idle ]   [ + ]
──────────────────────────────────────────────────────────────────────

  > User → Claude: Review the diff Codex produced. Flag missed cases.
  Claude: Reading the new export handler and its tests ...
  Claude: Two issues — response shape diverges from OpenAPI; missing
          pagination cap.

──────────────────────────────────────────────────────────────────────
[ Send a message to Claude... ]                                     ↩
```

The side details panel can summarize active tabs, but it should not be the
primary navigation surface for switching between them.

The multi-tab strip should visually match the rest of the session chrome
instead of introducing a separate tray treatment. Use the shared line-tab
pattern already used by adjacent session sub-navigation: transparent strip
background, a clear active state, and the product's primary underline/accent
language rather than a detached gray pill bar.

### Add Tab

The v1 `+` action should be deliberately small. A new tab starts as an empty
tab attached to the same sandbox:

- agent/provider: Codex, Claude Code, Gemini, Amp, Pi, custom CLI
- model/reasoning level: seeded from the user's defaults for that provider
- optional label, auto-generated if blank

No role presets, scope pickers, autonomy modes, or specialized templates in v1.
The new tab has:

- repository context package
- current branch, current filesystem, and current uncommitted changes
- the same sandbox credential and preview context

It does **not** start with prior chat messages, copied attachments, or the
original user prompt in the visible transcript. The agent sees the same working
tree once the user sends the first message, but the tab itself is blank.

Creating the tab should not start agent execution or spend tokens by itself.
The first run starts only after the user sends a message in that tab.

Before that first run starts, the platform should capture a checkpoint of the
shared sandbox. That checkpoint is the user's recovery point if the new tab
makes the branch worse.

### Quick Actions

The bare `+` opens a blank tab. That is correct for ad-hoc work, but 143
already exposes a one-click repair action that should plug straight into the
multi-tab model when it lands.

- **Fix tests** — already implemented as a PR-health repair action that
  spawns a session-level fix when checks are failing (see
  [implemented/61-pr-state-sync-and-repair-actions.md](../implemented/61-pr-state-sync-and-repair-actions.md)).
  When multi-tab is available, the action should reuse the existing repair
  codepath and prompt template, but route the launch into a new tab inside
  the current sandbox rather than spawning a separate session operation.
  Same checkpoint capture as `Add Tab`; the pre-seeded message is placed in
  the new tab's composer for the user to review and send.

- **Review** — starts a bounded review/fix loop in the current sandbox. The
  action creates or reuses one review-loop tab, runs the selected agent's native
  `/review` command against the live working tree, lets that same agent fix its
  own findings, and repeats sequentially until the agent reports clean or the
  configured pass limit is reached. This is the primary orchestrated tab helper
  we are targeting, not just a blank tab preset. The detailed behavior lives in
  [78-review-agent-loops.md](../future/78-review-agent-loops.md).

### Layout Modes

A tab can be rendered in any of three layouts:

- **Tabs:** default; one active transcript at a time.
- **Split view:** two to four tabs side by side for active supervision.
- **Focus/pop-out:** one tab in a larger view while other tabs continue
  running.

The screenshot mental model maps naturally to tabs: a workspace-level strip
with one or more agent tabs inside the same copied repository.

### Live Coordination Signals

Each tab header should show:

- status dot: `pending`, `running`, `idle`, `awaiting_input`, `completed`,
  `failed`, or `cancelled`
- agent/provider icon and label
- attention badge when the tab needs input
- changed-file count since the tab started
- conflict badge when another active tab touched overlapping files
- optional hover card with plain-language status, queued follow-up work, and
  overlap/conflict signals; avoid low-signal operational metrics like per-tab
  cost in the primary tooltip

### Combined Review

`Changes` remains branch-level by default. It should add tab filters:

- All changes
- Since tab started
- Touched by tab
- Overlap with another tab
- Unattributed workspace changes

Tab attribution is advisory, not a substitute for the final branch diff. The
PR should always be created from the actual sandbox filesystem.

### Simple User Model

For both engineers and less technical users, the UI should keep the core action
simple:

- one session title
- one current branch
- one set of changes
- one `Review changes` path
- one `Create PR` path

Tabs are secondary. They help the user ask another agent to look at the same
work, but they should not make the session feel like a project board.

### Status Vocabulary

Do not introduce new tab status names. Reuse the existing `models.ThreadStatus`
values from `internal/models/session_enums.go`:

- `pending`
- `running`
- `idle`
- `awaiting_input`
- `completed`
- `failed`
- `cancelled`

Session-level rollups should continue to use the existing `models.SessionStatus`
values. UI labels can apply normal title casing, but API values, filters,
analytics, and state machines should use these exact constants.

Recommended thread lifecycle:

```text
idle -> pending -> running -> idle
                         \-> awaiting_input -> pending -> running
                         \-> completed
                         \-> failed
                         \-> cancelled
```

`idle` covers a blank tab waiting for the user's first message and a completed
turn waiting for another message. `pending` means the user has sent work and the
job is queued but not yet running.

## Senior PM View: Best Developer Experience

### The product should feel like managing a small engineering pod

The key shift is from "chat with an assistant" to "direct several agents in one
workspace." That requires an operating surface with:

- clear lanes of ownership
- fast status scanning
- low-friction reassignment
- easy handoff between agents
- strong final review

### Killer workflows

#### 1. Implementer plus reviewer

1. User starts Codex on a feature.
2. When Codex has a draft, user clicks `+` and adds a Claude tab.
3. User asks Claude to review the current diff and leave findings in its own
   tab.
4. User sends the findings back to Codex or lets Claude patch small fixes.
5. Combined branch goes through normal validation and PR.

This should become the default "raise quality" workflow. Promoting it to a
one-click **Review this work** quick action is a candidate for a later phase
once usage confirms the pattern is common enough to deserve dedicated UI.

#### 2. Second opinion on the same branch

1. User starts Codex.
2. User adds a Claude tab.
3. The Claude tab opens blank, but attached to the same branch and filesystem.
4. User asks Claude to inspect the current work, propose an alternative, or
   continue from the visible code state.
5. User compares summaries, diffs, tests, and confidence.

The product must make this honest: because the sandbox is shared, this is not a
clean isolated bakeoff. It is a second tab looking at the same branch. If the
user wants two independent attempts that can be compared without shared writes,
the product should guide them to separate sessions.

#### 3. Specialist split

1. User starts one tab and asks it to work on backend changes.
2. User adds another tab and asks it to work on frontend changes.
3. Agents work against the same branch, so shared generated types and API
   contracts are visible immediately.
4. A third tab can run integration checks and patch breakage.

This is where shared sandbox beats isolated worktrees.

#### 4. Human as tech lead

The user can queue instructions into multiple tabs while agents run. Each tab
keeps its own backlog, so the user can say:

- "Codex, finish the migration."
- "Claude, review only files touched by Codex."
- "Tests, run the frontend checks after both of them go idle."

### Guardrails that protect the experience

- Make "separate sandbox" a visible recommendation when the task sounds like an
  independent PR.
- Capture a checkpoint before a second tab is allowed to run.
- Show file overlap warnings while agents are active.
- Allow users to stop or cancel one tab's current turn without stopping the
  sandbox.
- Provide "summarize all tabs" and "ask reviewer to inspect implementer"
  actions.
- Keep the final PR path branch-level; do not ask users to reconcile tab-level
  PRs.

## Senior Engineering View: Scalable Architecture

### Domain Model

#### Session

The existing session remains the parent execution record:

- `org_id`
- `repository_id`
- branch and base commit
- sandbox lifecycle
- preview lifecycle
- session-level status, derived from threads
- snapshot and validation state
- PR state

#### Sandbox

The sandbox is the runtime environment for a session:

- container identity
- mounted repository path
- credential socket
- preview routing
- dev process registry
- shared filesystem mutation stream
- runtime resource budgets

If today's implementation stores most of this on `sessions`, that can remain
physically true at first. The design boundary should still be explicit because
threads attach to the sandbox, not directly to a container implementation.

#### AgentThread

Create a durable `session_threads` concept:

```sql
CREATE TABLE session_threads (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,

    label text NOT NULL,
    agent_type text NOT NULL,
    model text,
    reasoning_level text,

    status text NOT NULL DEFAULT 'idle',
    agent_process_id text,
    external_agent_session_id text,
    current_turn integer NOT NULL DEFAULT 0,
    started_at timestamptz,
    last_activity_at timestamptz,
    completed_at timestamptz,

    base_snapshot_key text,
    last_checkpoint_key text,
    last_diff_summary text,
    cost_cents numeric(12, 4) NOT NULL DEFAULT 0,

    created_by_user_id uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_session_threads_session
ON session_threads (org_id, session_id, created_at);

CREATE INDEX idx_session_threads_active
ON session_threads (org_id, status)
WHERE status IN ('pending', 'running', 'awaiting_input');
```

Use typed string enums in `internal/models` for `status` with `Validate() error`
tests.

`base_snapshot_key` records the checkpoint captured before the tab's first
agent run. The thread row should not duplicate prior transcript content or
large context blobs. The shared session, linked issue snapshots, attachments,
repository context, and sandbox state remain referenced through their existing
tables and session-level records.

#### Thread Messages

Extend `session_messages` so every message can be either session-level or
thread-level:

- `thread_id nullable`
- `visibility`: `session`, `thread`
- `input_references`: existing structured references
- `created_by`: user, agent, system

Session-level messages are broadcast instructions or summaries. Thread-level
messages are the normal transcript for a tab.

#### Thread Events

Add append-only events for streaming and auditability:

- `thread_started`
- `thread_output`
- `thread_tool_call`
- `thread_file_write_observed`
- `thread_checkpointed`
- `thread_awaiting_input`
- `thread_failed`
- `thread_completed`

The UI should subscribe once per session and receive events for all threads over
the existing SSE shape or a session-scoped event stream.

#### File Mutation Attribution

Add a lightweight attribution table:

```sql
CREATE TABLE session_thread_file_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    thread_id uuid REFERENCES session_threads(id) ON DELETE SET NULL,
    path text NOT NULL,
    event_type text NOT NULL,
    before_hash text,
    after_hash text,
    observed_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_thread_file_events_session_path
ON session_thread_file_events (org_id, session_id, path, observed_at);
```

Attribution can be produced by:

1. wrapping tool/file operations when the agent adapter supports it
2. sampling `git status --porcelain=v2` before and after each thread turn
3. using in-sandbox file watchers as a best-effort enhancement

This is not security attribution. It is operational evidence for UI warnings and
review filters.

### Runtime Model

#### Process Supervision

Each thread gets its own supervised agent process inside the sandbox:

- independent stdin/stdout/stderr
- independent cancellation signal
- independent runtime deadline
- independent queued input
- shared environment and working directory
- shared credential socket with per-thread audit labels

The sandbox supervisor owns a process group per thread. Killing one thread must
not kill the container or sibling threads unless the user stops the whole
session.

#### Terminal Multiplexing

We should not need to install `tmux` or another terminal multiplexer in the
sandbox for v1.

The correct mental model is "multiple terminals on one computer," but the
platform should implement that directly:

- keep one sandbox container for the session
- start one agent process per active tab/turn inside that container
- attach separate stdin/stdout/stderr streams to each process
- allocate a PTY per process only when the agent CLI requires terminal behavior
- track each process by `thread_id`, PID/exec ID, status, and process group
- send cancellation to one process group without stopping sibling tabs

For Docker-backed sandboxes, this can be implemented as multiple concurrent
`docker exec` sessions against the same running container, or as a small
in-container supervisor that spawns child processes on request. Both approaches
avoid adding user-facing terminal infrastructure.

`tmux` is useful when a human needs to attach to persistent terminal panes. It
is less attractive as the product primitive because it adds another stateful
layer to snapshot, inspect, secure, and recover. It also makes structured
process ownership harder: the platform needs per-tab logs, cancellation,
budgets, costs, and health, not just terminal panes.

Use `tmux` only as a fallback for a specific agent CLI that cannot run correctly
with normal pipes or a per-process PTY. The default architecture should be
direct process supervision.

#### Concurrency Limits

Set conservative defaults:

- max threads per session: 4
- max running threads per session: 3
- max running threads per org: derived from plan and worker capacity

Admission should be centralized through Postgres row locks or the existing job
queue so two browser tabs cannot over-admit threads concurrently.

#### Resource Controls

Shared sandbox resources need both session-level and thread-level budgets:

- CPU and memory remain container-level initially.
- Runtime duration, token/cost budget, and output volume are thread-level.
- Preview ports and dev servers are session-level.
- Background commands spawned by a thread should be attached to that thread's
  process group where possible.

If one thread causes runaway CPU or disk usage, the platform may need to stop
the whole sandbox. The UI should explain that as a sandbox-level failure, not a
thread-only failure.

#### Checkpoints

Use three checkpoint scopes:

| Scope | When captured | Purpose |
| --- | --- | --- |
| Session base | before first thread starts | restore the whole sandbox |
| Thread start | before a new thread's first agent run | compare/revert that thread's contribution |
| Turn checkpoint | before each user message to a thread | resume and rollback within that thread |

Checkpoint restore is harder with multiple active threads. Rules:

1. Restoring a session-level checkpoint requires stopping all active threads.
2. Restoring a thread-start checkpoint requires stopping that thread and warning
   if later sibling-thread changes overlap the same files.
3. Per-thread rollback is allowed only when the system can compute a clean
   reverse patch against the current workspace. Otherwise it becomes a guided
   "ask another agent to revert this" flow.

This keeps rollback honest in a shared filesystem.

#### Git and PR Invariants

- The session branch remains the guarded branch.
- Agents must not switch branches.
- PR creation uses the actual sandbox working tree, not a synthesized per-thread
  diff.
- Commit attribution may use trailer metadata such as
  `143-Thread-ID: <uuid>`, but the system must not depend on perfect commit
  hygiene for correctness.
- Validation runs against the combined branch.

#### Credentials

The existing per-session host credential socket should become thread-aware:

- same socket lifetime as the sandbox
- per-request metadata includes `session_id`, `thread_id`, `agent_type`, and
  user/org context
- audit logs distinguish "Codex reviewer read PR checks" from "Claude
  implementer pushed branch"

No provider token should be copied into the sandbox solely to support
multi-threading.

## API Shape

Recommended endpoints:

```text
GET    /api/v1/sessions/{session_id}/threads
POST   /api/v1/sessions/{session_id}/threads
PATCH  /api/v1/sessions/{session_id}/threads/{thread_id}
POST   /api/v1/sessions/{session_id}/threads/{thread_id}/messages
POST   /api/v1/sessions/{session_id}/threads/{thread_id}/cancel
POST   /api/v1/sessions/{session_id}/threads/{thread_id}/complete
GET    /api/v1/sessions/{session_id}/threads/{thread_id}/changes
GET    /api/v1/sessions/{session_id}/thread-events
```

`POST /api/v1/sessions/{session_id}/threads` creates an `idle` blank tab. It
does not enqueue an agent job. The first
`POST /threads/{thread_id}/messages` call captures the thread-start checkpoint
when needed, then enqueues the thread run.

Existing `POST /api/v1/sessions/{id}/messages` should remain as a compatibility
alias for the default thread when the session has exactly one thread. Once a
session has multiple threads, the generic endpoint should return a validation
error that asks the client to specify a thread.

## Worker Execution

### Jobs

Introduce thread-scoped job kinds:

- `start_session_thread`
- `continue_session_thread`
- `cancel_session_thread`
- `checkpoint_session_thread`

Jobs must include both `session_id` and `thread_id`. The job handler should:

1. lock the session row enough to verify sandbox state
2. lock the thread row for status transition
3. hydrate or attach to the sandbox
4. start or resume the thread process
5. stream thread events
6. checkpoint at the configured boundary
7. update derived session status

### Leases

Threads need leases separate from session leases:

- a worker can own thread A and thread B for the same session only if it owns or
  can attach to the sandbox host
- in the first implementation, all active threads for one sandbox should be
  pinned to the same worker node
- later, distributed attachment can exist only if the sandbox runtime exposes a
  safe remote process API

This is the key scalability constraint: multiple threads are cheaper than
multiple sandboxes, but they are not freely schedulable across the fleet.

### Recovery

If a worker dies:

- mark active thread leases stale
- stop assuming in-memory agent process state survived
- hydrate the latest session snapshot on a new worker
- resume each thread from its latest durable conversation/checkpoint capability
- if an agent cannot resume conversationally, surface `filesystem restored,
  conversation restart required` at the thread level

Recovery should not create N independent sandboxes for N threads.

## Conflict Handling

### Detection

Detect overlap at three levels:

- file overlap: two active threads touched the same path
- hunk overlap: two threads touched nearby line ranges in the same file
- command overlap: two threads are running incompatible commands, such as two
  package installs or migrations at once

Start with file overlap because it is cheap and good enough for v1.

### UX

When overlap occurs:

- show a warning badge on both tabs
- show the path list in the tab hover card
- offer "stop other turn" and "ask another tab to reconcile"
- keep running by default unless a destructive operation is detected

### Enforcement

Do not implement hard locks in v1. Consider file-scope enforcement later if
telemetry shows high conflict rates or if enterprise customers need stricter
auditability.

## Validation and Quality

Validation is branch-level:

- run tests/lints/builds once against the combined sandbox
- record which threads were active when validation began
- if validation fails, the user can assign the failure to a chosen thread
- a user can dedicate a tab to running checks early, but that is advisory until
  final branch validation passes

The review agent workflow should become a first-class quality pattern, not a
separate validation stage replacement.

## Metrics

Track:

- threads per session
- concurrent running threads per session/org/worker
- time to first useful diff
- conflict warnings per session
- thread cancellation rate
- validation pass rate for single-thread vs multi-thread sessions
- PR merge rate and review changes requested
- per-provider cost and latency by tab
- recovery success rate for multi-thread sessions

Product success is not "more threads." It is higher accepted PR quality and
shorter human supervision time for sessions where parallelism is appropriate.

## Rollout Plan

### Phase 0: Data model and compatibility — Completed

- Added `session_threads`.
- Added `thread_id` to session messages and logs.
- Kept existing single-thread APIs working.
- Added a primary-thread invariant: every non-deleted session has a seeded
  `Main` thread. Legacy NULL-thread messages/logs are attributed to that row
  when the mapping is unambiguous, and new first-turn execution is attributed
  to the seeded primary thread.

### Phase 1: Multiple blank tabs, one running thread at a time — Completed

- Users can create multiple blank tabs.
- The `Add agent tab` action creates a new blank tab immediately with current
  tab/session defaults (agent, model when applicable, autogenerated label)
  instead of opening a setup modal first.
- Creating a tab does not start execution.
- First messages are routed through the selected thread.
- Thread transcript and log views are scoped by `thread_id`.
- The selected tab's agent/provider and model are used for execution.
- Only one thread may be actively running.
- This validates the UX, data model, message routing, and review filters without
  concurrent filesystem writes.

> **Composer behavior:** the shared composer remains sendable while a selected
> thread is already `pending` or `running`. Follow-up messages are appended to
> the thread-scoped pending queue and drain after the in-flight turn
> completes, including races where a resumable thread flips back to `running`
> between inspection and resume-claim. Comment-resolution sends still require
> an immediately claimable thread because that path must commit the message and
> resolution atomically against the same turn.

### Phase 2: Concurrent tabs in one sandbox — Planned

- Allow multiple tabs to run in one sandbox.
- Capture a thread-start checkpoint before each tab's first run.
- Add file-touch attribution and overlap badges.
- Add branch-level combined review filters.

### Phase 3: Recovery and review polish — Planned

- Enforce per-session running-thread limits.
- Add thread-scoped cancellation, cost budgets, and recovery UX.
- Add split view and attribution filters.

### Phase 4: Orchestration helpers — Planned

- Add the session `Review` loop: one review-loop tab runs native `/review`,
  fixes its own findings, and repeats sequentially under a pass budget.
- Add "summarize all tabs."
- Add "fork this tab into a separate sandbox" for risky divergent work.
- Add "revert this tab's changes" where reverse patches are clean.
- Add optional supervisor policies for safe approvals and escalation.

## Open Questions

1. Which agents can support real conversational resume per thread, and which
   need filesystem-only restart semantics?
2. How much write attribution is enough for useful review filters without
   adding brittle filesystem watchers?
3. How should billing expose tab-level cost without making the UI feel like a
   meter?

## Decision

Build toward **session-owned sandbox, sandbox-owned agent threads**.

Do not model this as child sessions, per-thread containers, or separate PRs.
Those are useful for independent work, but they are the wrong abstraction for
the desired Conductor-like tab experience. The scalable version is a shared
sandbox with explicit thread processes, per-thread durable state, clear
coordination signals, and branch-level validation/PR ownership.
