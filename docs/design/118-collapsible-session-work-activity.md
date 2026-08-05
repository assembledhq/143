# Design: Collapsible Session Work Activity

> **Status:** Partially Implemented | **Last reviewed:** 2026-08-04

Implementation is complete in the working session changeset. The durable schema, typed
lifecycle stores, ownership validation, reconciliation, orchestrator phase
recording, transcript-window/SSE contracts, user preference, rendering kill
switch, phase-preserving frontend model, activity capsule UI, public guide, and
deterministic browser CI fixture are implemented. Final responses,
human-input/approval requests, plans, steering acknowledgements, and terminal
failure/cancel/stop state use the required transactional boundaries, with live
notifications emitted only after commit. The final reconciliation pass also
closed viewport timer rearming, durable interruption/recovery visibility,
missing-association and primary success-measure telemetry, and duplicate,
missed, and out-of-order lifecycle reconciliation coverage. Remaining
launch-gate work is operational verification: materialize and review the PR
stack, exercise an authenticated preview, run the available-provider smoke
matrix, complete the coverage gate on a CI-class runner, and run the
Chromium/WebKit pre-launch browser matrix in an environment with browser system
dependencies.

## Product Specification

### Summary

Session threads should read like conversations after work completes without
becoming opaque while work is active. During execution, 143 should show the
agent's reasoning/status updates, tool calls, results, and other activity in
chronological order. After a contiguous period of execution ends, that activity
should become one compact, expandable **activity capsule**.

A single agent turn may contain multiple activity capsules when execution is
interrupted by a conversation or control-flow boundary:

```text
User: Fix the session transcript scrolling issue.

> Worked for 1m 12s · 6 tool calls

Agent: Should restored anchors expand their parent activity?
User: Yes, expand it before scrolling.

> Worked for 3m 48s · 14 tool calls

Agent: Implemented phase-level collapsing and restored-anchor handling.
```

Selecting a capsule expands the original activity inline. The full durable
transcript remains available; this feature changes its default presentation,
not what 143 records.

### Problem

Coding-agent sessions can contain hundreds of reasoning updates, file reads,
searches, commands, tool results, test outputs, and retries. Individual tool
rows are already collapsible, but every tool still occupies space in the main
timeline. In long threads, execution mechanics overwhelm the actual
conversation: what the user requested, where judgment was needed, and what the
agent ultimately delivered.

This creates four product problems:

1. Returning users must scroll through old implementation detail to find
   decisions and outcomes.
2. Long sessions are difficult to scan and compare across execution periods.
3. Important user and agent messages lose hierarchy among low-level activity.
4. Rendering many visible timeline rows increases browser work and complicates
   scroll restoration, search, and future virtualization.

The solution must not hide active work, failures that need attention, approval
requests, or the evidence required to audit an agent's actions.

### Product Decision

Use a **contiguous activity phase**, not an entire turn, as the primary unit of
progressive disclosure.

- A phase begins when a coding agent actually begins or resumes execution.
- A phase ends when execution pauses for conversation or approval, is steered,
  is interrupted, fails, is cancelled or stopped, or persists its final
  response.
- Active phases are expanded by default.
- Completed and terminal phases are collapsed by default.
- Each phase has its own independently expandable capsule.
- User messages, human-input requests, plans awaiting approval, interruption
  notices, terminal failures, and final assistant responses remain outside
  collapsed activity.
- Individual tool rows retain their existing detail controls inside an expanded
  capsule.

This combines full-fidelity live execution with a concise durable reading
experience while preserving exact chronology across multi-step interactions.

### Goals

1. Substantially reduce the default vertical height of completed session
   threads.
2. Preserve transparency while an agent is working and full inspectability
   afterward.
3. Make requests, decisions, questions, failures, and final outcomes the
   dominant information hierarchy.
4. Represent actual uninterrupted execution periods accurately, including
   pauses and resumptions within one turn.
5. Keep transcript anchors, pagination, scroll restoration, and live updates
   correct.
6. Establish a phase-level rendering boundary suitable for later transcript
   virtualization and search.
7. Provide Compact and Detailed modes with a user-level preference that follows
   the user across threads, browsers, and devices.
8. Launch to all users with a high-confidence automated and manual test plan.

### Non-Goals

1. Deleting, compacting, or changing the agent's model context.
2. Removing tool calls, logs, reasoning/status updates, or results from the
   durable transcript.
3. Making a separate LLM call for every activity label or completed capsule.
4. Including file-change counts, test outcomes, or generated work summaries in
   the first release.
5. Moving all work activity into a separate drawer or page.
6. Shipping transcript virtualization in the first release.
7. Redesigning session tabs, the composer, diff review, or session navigation.
8. Guessing historical execution duration from transcript-entry timestamps.
9. Running live external coding-agent providers in required CI.

### Product Principles

#### Conversation first, evidence available

The default completed state should optimize for reading the collaboration. The
execution trace remains one interaction away and must never be discarded.

#### Live work should be legible

Users watching an active phase should be able to understand what the agent is
doing and intervene. Activity must not collapse merely because it has grown
long or moved outside the viewport.

#### Attention must not be collapsed away

Anything that blocks progress or requires user judgment remains visible.
Compression is appropriate for execution mechanics, not unresolved decisions.

#### Timing must be truthful

“Worked for” means actual coding-agent execution time. Queueing, capacity waits,
sandbox startup, dependency setup, maintenance waits, and post-response cleanup
must not inflate it. If authoritative lifecycle data is unavailable, omit
duration.

#### Deterministic facts before generated claims

Phase state, duration, and logical tool-call count come from durable structured
records. Activity labels may use the coding agent's own status text or actual
operation, but completed summaries must not infer file or test outcomes from
unstructured output.

#### One hierarchy, multiple levels of detail

The phase capsule is the outer disclosure level. Within an expanded capsule,
individual tools retain concise labels and independently expandable input and
output. Opening a capsule does not automatically open every tool result.

### Activity Phase Definition

An activity phase is one uninterrupted period in which a coding agent is
actively processing an acknowledged instruction batch.

#### Phase starts

A phase begins when:

- the coding-agent execution process begins handling the initial turn;
- the runtime acknowledges a steering-message delivery batch and resumes under
  the new instructions;
- the runtime acknowledges a human-input answer or approval and resumes;
- an interrupted or paused runtime actually resumes execution.

The timer does not start when:

- the user submits a prompt;
- a message waits in the thread inbox;
- the job waits for capacity;
- the sandbox or repository environment is being prepared;
- dependencies are installed;
- a runtime is waiting for maintenance or recovery;
- post-response cleanup or snapshotting runs.

Existing setup, capacity, and recovery notices remain outside capsules.

#### Phase ends

A phase ends when one of these boundaries is durably confirmed:

- a human-input, permission, or approval request is persisted;
- a plan awaiting approval is persisted;
- a queued steering message is acknowledged and applied;
- a confirmed interruption, maintenance pause, lost runtime, or capacity
  suspension stops execution;
- execution fails, is cancelled, or is stopped;
- the final assistant response is durably persisted.

Recovered tool errors and ordinary retries do not end a phase. Intermediate
reasoning, progress commentary, commands, tool results, and test output remain
inside the current capsule.

#### Acknowledged delivery batches

User steering and human-input answers create boundaries when the runtime
acknowledges and applies them, not when the user submits them.

If several messages are acknowledged together before execution resumes, they
start one new phase. If the runtime acknowledges them separately with execution
between them, each acknowledgment starts a separate phase.

While a steering message is unacknowledged:

- keep it out of the rendered transcript;
- continue appending prior-phase activity inside the capsule above it;
- do not attribute work to the new instruction yet;
- on acknowledgment, close the prior phase while the message remains hidden;
- only when execution resumes, atomically add the message at its normal applied
  position and start the new capsule below it;
- on delivery failure or cancellation, retain the existing visible inbox
  failure state outside the transcript.

An explicit interrupt ends the phase when interruption is confirmed.

#### Infrastructure interruption and recovery

A confirmed worker drain, maintenance pause, lost runtime, or other execution
suspension closes the active phase. The interruption notice remains visible.
Recovery starts a new phase only when the agent actually resumes. Waiting time
is excluded from both phase durations.

#### Final response boundary

The user-visible phase ends when the final assistant response is durably
persisted, not when the underlying process later exits. Adapter cleanup, usage
accounting, snapshots, and container bookkeeping do not contribute to
“Worked for.” A cleanup failure is a system/runtime issue and must not
retroactively change the completed phase.

### Core User Experience

#### Active expanded capsule

In Compact mode, an active phase is expanded by default:

```text
Working for 2m 41s · 8 tool calls

  Searched session transcript components
  Read chat-timeline.tsx
  Running `npm test -- session-detail`...
```

New entries append in chronological order through the existing live-update
path. Existing scroll-follow behavior remains authoritative; activity must not
force the user back to the live edge after they intentionally scroll away.

#### Manually collapsed active capsule

Users may collapse an active capsule. Its summary updates live:

```text
● Working for 2m 41s · 8 tool calls · Running `npm test -- session-detail`...
```

The latest activity label should prefer:

1. the coding agent's own human-readable activity/reasoning summary;
2. otherwise, a concise representation of the actual tool, command, file,
   search, or operation;
3. otherwise, a generic tool/activity label.

Do not make a secondary LLM call merely to generate this label. Apply baseline
security sanitation: redact known credentials and secrets, strip terminal
escape/control characters, remove URL credentials and sensitive query
parameters, and truncate to a bounded single line. Do not place raw tool results
in the capsule label. The label is status text, not an authoritative audit
statement; the full original activity remains inside the expanded capsule.

#### Completed and terminal capsules

When a phase ends, its summary uses phase state, actual execution duration, and
backend-derived logical tool-call count:

```text
Worked for 3m 48s · 14 tool calls
Failed after 52s · 4 tool calls
Cancelled after 1m 12s · 7 tool calls
Interrupted after 2m 03s · 9 tool calls
Worked for 18s
```

Every authoritative phase renders a capsule, including phases with zero tool
calls. Do not show `0 tool calls`.

Version one does not include:

- files changed;
- tests passed or failed;
- recovered-error counts;
- LLM-generated completed-work summaries.

Those facts may be added later only from authoritative phase-scoped structured
data.

#### Interaction-aware automatic collapse

In Compact mode, a completed phase collapses automatically only when:

- the user is at the live edge;
- the user has not manually interacted with that capsule; and
- the following visible boundary event or final response has rendered.

Keep the phase expanded when the user:

- explicitly expanded or pinned it;
- opened an individual tool detail;
- selected text within it; or
- scrolled into its activity to inspect it.

Do not collapse between the last tool result and a slightly later question,
failure, or final response. Do not force scroll-to-bottom as a side effect.
Detailed mode never automatically collapses a phase. On a fresh thread load,
completed phases follow the saved Compact/Detailed user preference; transient
inspection state is not restored.

#### Expanded completed capsule

Selecting a capsule expands its original activity inline. The capsule header
remains visible in both Compact and Detailed modes:

```text
⌄ Worked for 3m 48s · 14 tool calls
  Searched...
  Read...
  Ran tests...
```

The header remains the control used to collapse that phase. Expanding one phase
does not collapse another. Entries retain chronological order, timestamps,
truncation behavior, and individual tool disclosure.

#### Visible boundary events

The following remain visible outside collapsed activity:

- user prompts and applied steering messages;
- unanswered human-input requests;
- permission and approval requests;
- plans awaiting approval or adjustment;
- user answers and approvals;
- confirmed interruption, recovery, maintenance, and waiting notices;
- terminal failures, cancellations, and stops;
- final assistant responses;
- visible system-authored prompts that define execution.

Recovered errors may remain inside the activity capsule. A terminal error must
also have a visible failure boundary outside it.

### Compact and Detailed Modes

Version one ships both modes:

- **Compact** is the default for new users. Active phases are expanded and
  completed phases are collapsed unless interaction-aware rules preserve them.
- **Detailed** expands every phase, including phases loaded later through
  transcript pagination.

The control label is **Activity detail: Compact / Detailed**.

The preference is user-level, not thread-level:

- changing it in any thread changes the user's default everywhere;
- it applies to the currently open thread immediately;
- it applies to every subsequently opened thread;
- it persists across browsers and devices in the existing typed
  `users.settings` JSONB;
- it is available both in the transcript overflow menu and on the Account
  settings page.

Individual capsule expansion/collapse is a transient local override. It does
not change the global preference and is not persisted across a fresh thread
load.

Both preference controls use the same TanStack mutation and optimistically
update the cached authenticated user. On failure, revert the optimistic update
and surface an error.

### Historical Sessions

Sessions created before authoritative phase records exist remain inspectable:

- infer contiguous activity capsules using visible conversation/control
  boundaries;
- show a deterministic summary such as `Activity · 14 tool calls`;
- omit duration and “Worked for”;
- do not backfill or guess phase timing from first/last entry timestamps;
- preserve all visible messages and ambiguous assistant output;
- fall back to the existing flat renderer for malformed transcript data rather
  than dropping entries.

Historical compatibility remains covered indefinitely; it is not a one-time
migration state.

### Summary Content Rules

Required for authoritative phases:

- active or terminal state label;
- actual execution duration when lifecycle timestamps are valid;
- logical tool-call count when greater than zero.

Required for inferred historical capsules:

- neutral `Activity` label;
- logical tool-call count when greater than zero.

State copy:

| Phase state | Boundary qualification | Capsule copy |
| --- | --- | --- |
| `running` | any | `Working for {duration}` |
| `completed` | any normal boundary | `Worked for {duration}` |
| `failed` | `error` | `Failed after {duration}` |
| `cancelled` | `stopped` | `Stopped after {duration}` |
| `cancelled` | `cancelled` | `Cancelled after {duration}` |
| `interrupted` | maintenance/runtime loss | `Interrupted after {duration}` |
| inferred historical | no phase record | `Activity` |

Formatting:

- use compact elapsed time such as `42s`, `2m 41s`, or `1h 08m`;
- do not show milliseconds;
- pluralize `tool call` correctly;
- omit unavailable or zero-value facts;
- keep the summary to one line on desktop when practical;
- allow natural wrapping on narrow screens without horizontal scrolling;
- keep detailed absolute timestamps available inside expanded activity.

### Anchors and Restored Positions

Opening a URL, restored reading position, search result, or navigation target
whose entry is inside a collapsed capsule must:

1. load the transcript window containing the target;
2. expand the containing capsule;
3. wait for its contents to mount and measure;
4. scroll to the exact stable entry ID; and
5. apply the existing highlight/focus treatment.

Automatic anchor expansion is transient and does not change the saved
Compact/Detailed preference.

### Accessibility and Responsive Behavior

- Use a shadcn/Radix disclosure primitive, not a raw HTML button.
- Expose `aria-expanded` and an accessible name containing phase state,
  duration when available, and tool count when nonzero.
- Support keyboard activation and a visible focus indicator.
- Keep screen-reader and visual chronological order identical.
- Keep visible boundary events discoverable without opening a capsule.
- Respect `prefers-reduced-motion`.
- Avoid animating the full height of large output regions.
- On mobile, preserve state and duration before lower-priority live-label text.
- Expanded headers remain visible and operable at supported mobile widths.

### Success Measures

Primary product measures:

- median rendered height of a completed phase;
- median top-level row count per loaded transcript window;
- time or scroll distance from thread open to the latest final response;
- Compact/Detailed preference distribution;
- individual capsule expansion and re-collapse rate.

Correctness and safety guardrails:

- human-input response rate and time to response;
- anchor restoration failures;
- large unexpected scroll deltas;
- phase lifecycle reconciliation failures;
- transcript entries missing an expected phase association;
- stranded running phases;
- frontend exceptions and long-thread rendering regressions;
- support reports that agent actions or final responses are hidden.

Expansion rate is diagnostic, not a target to minimize. High expansion on
failed phases is expected.

### Acceptance Criteria

1. Each uninterrupted execution period has one authoritative phase record and
   one independently expandable capsule.
2. A turn interrupted by human input, steering, approval, or recovery renders
   separate capsules around the visible boundary.
3. Active phases are expanded by default in Compact mode and update live.
4. Completed phases are collapsed by default in Compact mode unless the user
   was actively inspecting them.
5. Detailed mode expands all current and later-loaded phases by default; a
   later explicit per-capsule action may override that phase for the current
   view.
6. The user's Compact/Detailed preference persists in database-backed user
   settings and applies across threads and devices.
7. Every authoritative phase renders, including zero-tool phases.
8. Duration measures actual execution only and excludes queueing, setup,
   waiting, recovery, and post-response cleanup.
9. Historical inferred capsules never display guessed duration.
10. Queued steering stays out of the transcript until the runtime applies it,
    then appears as a normal user message.
11. One acknowledged delivery batch has durable identity and creates exactly
    one new phase only when execution actually resumes.
12. Visible boundary events and final assistant responses are never hidden
    solely inside collapsed activity.
13. Selecting a capsule reveals all phase activity in chronological order.
14. Individual tool detail and full-output loading retain existing behavior.
15. Manual capsule state survives live query refresh and pagination during the
    current view without changing the saved user preference.
16. Deep links and restored anchors expand the containing capsule before
    scrolling.
17. Prepending older transcript windows does not cause an unexpected visible
    scroll jump.
18. The experience is keyboard accessible and works at desktop and mobile
    widths.
19. The emergency kill switch restores the existing flat timeline without
    stopping phase recording.
20. No transcript event is deleted or made inaccessible by this feature.

## Engineering Specification

### Existing Architecture

The transcript backend already provides:

- `GET /api/v1/sessions/{session_id}/threads/{thread_id}/transcript`;
- turn-grouped, bounded windows;
- stable opaque transcript entry IDs;
- embedded message, log, and human-input records;
- content truncation with on-demand full-log loading;
- older, newer, around-anchor, and latest pagination;
- thread/runtime status in transcript metadata;
- persisted windows merged with live SSE buffers in session detail.

The current frontend flattens `SessionTranscriptTurn[]` into global message,
log, and human-input arrays through `flattenTranscriptWindows`, then rebuilds
one flat `TimelineEntry[]`. `ChatTimeline` renders each tool group as a
top-level row.

The current transcript `started_at` and `ended_at` are entry-boundary
timestamps derived from the earliest and latest entry in a turn. `ended_at` may
be present while a turn is active and must not be used as lifecycle state or
execution duration.

Existing `thread_runtimes` also cannot provide phase timing: a runtime may span
multiple turns, pauses, and resumptions; it has no phase identity and does not
record every pause/resume interval.

### Durable Activity Phase Model

Add a tenant-scoped operational table such as `session_activity_phases`:

```sql
CREATE TABLE session_activity_phases (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid        NOT NULL REFERENCES organizations(id),
    session_id      uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    thread_id       uuid        NOT NULL REFERENCES session_threads(id) ON DELETE CASCADE,
    turn_number     int         NOT NULL,
    phase_number    int         NOT NULL,
    status          text        NOT NULL,
    boundary_reason text,
    started_at      timestamptz NOT NULL,
    completed_at    timestamptz,
    runtime_id      uuid        REFERENCES thread_runtimes(id) ON DELETE SET NULL,
    trigger_kind    text        NOT NULL,
    trigger_batch_id uuid,
    trigger_sequence_start bigint,
    trigger_sequence_end   bigint,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_session_activity_phases_turn_nonnegative
        CHECK (turn_number >= 0),
    CONSTRAINT chk_session_activity_phases_phase_positive
        CHECK (phase_number > 0),
    CONSTRAINT chk_session_activity_phases_status
        CHECK (status IN (
            'running', 'completed', 'failed', 'cancelled', 'interrupted'
        )),
    CONSTRAINT chk_session_activity_phases_trigger_kind
        CHECK (trigger_kind IN ('initial', 'inbox_batch', 'recovery')),
    CONSTRAINT chk_session_activity_phases_lifecycle
        CHECK (
            (status = 'running' AND completed_at IS NULL AND boundary_reason IS NULL)
            OR
            (status <> 'running' AND completed_at IS NOT NULL AND boundary_reason IS NOT NULL)
        ),
    CONSTRAINT chk_session_activity_phases_time_order
        CHECK (completed_at IS NULL OR completed_at >= started_at),
    CONSTRAINT chk_session_activity_phases_status_reason
        CHECK (
            (status = 'running' AND boundary_reason IS NULL)
            OR
            (status = 'completed' AND boundary_reason IN (
                'final_response', 'human_input', 'approval',
                'plan_approval', 'steered'
            ))
            OR
            (status = 'failed' AND boundary_reason = 'error')
            OR
            (status = 'cancelled' AND boundary_reason IN (
                'stopped', 'cancelled'
            ))
            OR
            (status = 'interrupted' AND boundary_reason IN (
                'maintenance', 'runtime_lost',
                'capacity_suspended', 'interrupted'
            ))
        ),
    CONSTRAINT chk_session_activity_phases_trigger_range
        CHECK (
            (
                trigger_kind = 'inbox_batch'
                AND trigger_batch_id IS NOT NULL
                AND trigger_sequence_start IS NOT NULL
                AND trigger_sequence_end IS NOT NULL
                AND trigger_sequence_start > 0
                AND trigger_sequence_end >= trigger_sequence_start
            )
            OR
            (
                trigger_kind <> 'inbox_batch'
                AND trigger_batch_id IS NULL
                AND trigger_sequence_start IS NULL
                AND trigger_sequence_end IS NULL
            )
        ),
    CONSTRAINT uq_session_activity_phase_number
        UNIQUE (org_id, thread_id, turn_number, phase_number)
);

CREATE UNIQUE INDEX idx_session_activity_phases_one_running
    ON session_activity_phases (org_id, thread_id)
    WHERE status = 'running';

CREATE UNIQUE INDEX idx_session_activity_phases_trigger_batch
    ON session_activity_phases (org_id, trigger_batch_id)
    WHERE trigger_batch_id IS NOT NULL;

CREATE TABLE thread_inbox_delivery_batches (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid        NOT NULL REFERENCES organizations(id),
    session_id      uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    thread_id       uuid        NOT NULL REFERENCES session_threads(id) ON DELETE CASCADE,
    runtime_id      uuid        NOT NULL REFERENCES thread_runtimes(id) ON DELETE CASCADE,
    sequence_start  bigint      NOT NULL,
    sequence_end    bigint      NOT NULL,
    status          text        NOT NULL,
    acknowledged_at timestamptz NOT NULL,
    started_at      timestamptz,
    abandoned_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_thread_inbox_delivery_batches_range
        CHECK (
            sequence_start > 0
            AND sequence_end >= sequence_start
        ),
    CONSTRAINT chk_thread_inbox_delivery_batches_status
        CHECK (status IN ('acknowledged', 'started', 'abandoned')),
    CONSTRAINT chk_thread_inbox_delivery_batches_lifecycle
        CHECK (
            (status = 'acknowledged' AND started_at IS NULL AND abandoned_at IS NULL)
            OR
            (status = 'started' AND started_at IS NOT NULL AND abandoned_at IS NULL)
            OR
            (status = 'abandoned' AND started_at IS NULL AND abandoned_at IS NOT NULL)
        ),
    CONSTRAINT uq_thread_inbox_delivery_batches_range
        UNIQUE (org_id, thread_id, sequence_start, sequence_end)
);

ALTER TABLE session_activity_phases
    ADD CONSTRAINT fk_session_activity_phases_trigger_batch
    FOREIGN KEY (trigger_batch_id)
    REFERENCES thread_inbox_delivery_batches(id);
```

Exact names may change, but the following contract is required:

- one row per contiguous execution phase;
- monotonically increasing `phase_number` within a thread turn;
- at most one running phase per thread;
- explicit `org_id`, `session_id`, `thread_id`, and `turn_number`;
- actual agent-execution `started_at`;
- terminal `completed_at`;
- database checks that running rows have no completion/reason and terminal rows
  have both;
- typed status and boundary reason;
- optional association to the runtime responsible for the phase;
- typed trigger kind and, for acknowledged inbox work, the exact inclusive
  sequence range and durable batch ID that formed the delivery batch;
- org/thread/turn indexes for transcript-window reads.

This is operational lifecycle state, not versioned settings. Insert a row when
execution starts and update that same row exactly once to a terminal state.
Terminal rows are immutable through service/store guards.

#### Status

Define a dedicated typed string with named constants and `Validate() error`:

- `running`;
- `completed`;
- `failed`;
- `cancelled`;
- `interrupted`.

#### Boundary reason

Define a separate typed string with named constants and validation. Initial
values should cover:

- `final_response`;
- `human_input`;
- `approval`;
- `plan_approval`;
- `steered`;
- `maintenance`;
- `runtime_lost`;
- `capacity_suspended`;
- `interrupted`;
- `stopped`;
- `cancelled`;
- `error`.

Status describes how the phase ended; boundary reason describes why the
boundary exists. For example, a phase ending normally for human input is
`completed` with `boundary_reason="human_input"`, while a lost runtime is
`interrupted` with `boundary_reason="runtime_lost"`.

#### Trigger kind and acknowledged batch identity

Define a typed trigger kind:

- `initial`;
- `inbox_batch`;
- `recovery`.

For `inbox_batch`, persist the exact inclusive thread inbox sequence range
acknowledged together. The range is `(previous_last_acked_sequence,
new_last_acked_sequence]`, represented as
`trigger_sequence_start=previous+1` and
`trigger_sequence_end=new`. Every inbox entry in the range must belong to the
same org/session/thread, be durably acknowledged by the same runtime, and be
part of the instruction batch applied before the phase begins.

`thread_inbox_delivery_batches` is the durable identity for that acknowledged
delivery. A service transaction locks the thread runtime, verifies that
`sequence_start` is exactly the prior watermark plus one, advances the
watermark, and inserts one batch row. Replayed acknowledgment is idempotent;
overlapping or discontinuous ranges are rejected. When execution resumes, a
second transaction changes the batch from `acknowledged` to `started` and
creates exactly one phase referencing it. A delivery that is superseded or
cannot resume is explicitly changed to `abandoned`; it must never silently
disappear.

The batch and range form the durable association between one phase and the user
messages, human-input answers, or control entries that triggered it. Initial
and recovery phases have no inbox batch or sequence range. Do not infer batches
later from timestamps or message adjacency.

All enum validation must have table-driven parallel tests with exact expected
values and descriptive `require` messages.

### Transcript Source Associations

Add nullable `activity_phase_id uuid` associations to:

- `session_logs`;
- `session_messages`;
- `session_human_input_requests`.

New agent-produced activity carries the active phase ID. The visible event that
closes a phase also carries that phase ID when platform-owned. User messages
submitted outside execution may remain null until the associated delivery is
acknowledged; the new phase is linked through the acknowledged delivery batch.

`activity_phase_id` records causal/lifecycle association, not disclosure
placement. Human-input requests, plans, terminal failures, and final assistant
messages remain visible timeline nodes even when they carry the ID of the phase
they closed. Only activity-classified entries become children of the capsule.

Historical rows remain null.

Add indexes that support phase-scoped transcript reads and completed tool-count
queries:

- `session_logs (org_id, activity_phase_id, timestamp)` where phase ID is not
  null;
- `session_messages (org_id, activity_phase_id, created_at)` where phase ID is
  not null;
- `session_human_input_requests (org_id, activity_phase_id, created_at)` where
  phase ID is not null.

For partitioned tables, create parent indexes so PostgreSQL propagates them to
current and future partitions through the repository's existing partition
management path.

Because session logs and messages are partitioned and high-write, migration and
FK design require explicit review. The implementation must either:

- add a safe database-backed FK supported by the partition layout; or
- use the reviewed hot-table no-FK exception with
  `-- lint:allow-hot-table-no-fk reason="..."` and validate ownership in every
  write path.

Regardless of physical FK choice, writes must validate that the phase belongs
to the same org, session, thread, and turn. Missing an org constraint is a P0
data-isolation bug.

### Lifecycle Ownership

Phase lifecycle semantics belong in the shared orchestrator/runtime-control
layer, not individual provider adapters.

The shared layer:

- opens and closes phases;
- owns status and boundary-reason transitions;
- coordinates steering acknowledgment and delivery batches;
- coordinates human-input and approval pauses;
- handles recovery and reaper transitions;
- supplies the active phase ID to transcript writers;
- publishes live phase state.

Codex, Claude Code, OpenCode, Amp, and Pi adapters:

- emit their normal activity and lifecycle signals;
- propagate the supplied active phase ID;
- do not implement provider-specific phase semantics.

Work performed within the same thread/runtime, including subagent tool calls,
belongs to the active thread phase and contributes to its tool-call count.
A separately created 143 tab/thread owns its own phases and capsules. Parent
capsules may show a status such as `Waiting for 2 delegated tasks...` but do not
inline another thread's transcript.

### Phase Lifecycle Operations

Create a service/store boundary for phase transitions. Representative methods:

```go
StartPhase(
    ctx context.Context,
    orgID uuid.UUID,
    sessionID uuid.UUID,
    threadID uuid.UUID,
    turnNumber int,
    runtimeID *uuid.UUID,
    trigger models.ActivityPhaseTrigger,
) (models.SessionActivityPhase, error)

CompletePhase(
    ctx context.Context,
    orgID uuid.UUID,
    phaseID uuid.UUID,
    status models.ActivityPhaseStatus,
    reason models.ActivityPhaseBoundaryReason,
    completedAt time.Time,
) (models.SessionActivityPhase, error)
```

Requirements:

- `orgID` is the first argument after context on every exported store method;
- every query filters by `org_id`;
- start locks the owning thread row (or an equivalent stable allocation row)
  and allocates `COALESCE(MAX(phase_number), 0) + 1` for that thread/turn inside
  the transaction;
- phase creation occurs at actual adapter execution start/resume, before the
  first phase-associated transcript entry is persisted;
- an inbox-triggered start locks and atomically changes its durable delivery
  batch from `acknowledged` to `started`; the phase copies the batch's exact
  sequence range and references its ID;
- one unique batch can start at most one phase;
- the partial unique index prevents two active phases in one thread;
- completion uses expected-status semantics (`WHERE status='running'`);
- a second terminal transition is rejected or idempotently returns the exact
  existing terminal row when all terminal values match;
- a stale worker cannot mutate a replacement phase;
- no error is discarded.

Set `started_at` immediately before invoking/resuming the provider adapter after
queueing, capacity, sandbox, repository, and dependency setup have completed.
If adapter invocation fails before useful work begins, terminally fail the
created phase rather than leaving it running.

### Atomic Boundaries

Boundary-event persistence and phase transitions must be atomic whenever both
are platform-owned database writes.

Required transactional operations include:

- persist a human-input request and close the active phase;
- persist a plan/approval request and close the active phase;
- persist a final assistant message and complete the phase;
- persist a terminal failure/cancellation/stop and terminate the phase;
- durably process a steering acknowledgment, close the old phase, and promote
  the acknowledged delivery batch;
- durably process a human-input answer acknowledgment and associate its
  acknowledged batch.

Use `db.TxStarter.Begin()` and the existing `DBTX` store pattern. A transaction
must leave either the previous consistent state or the complete new state.

External-provider acknowledgment may arrive outside a database transaction,
but its durable handler performs all related platform writes atomically.

Acknowledgment and execution resumption are two distinct lifecycle transitions:

1. **Acknowledgment transaction:** advance the runtime/inbox acknowledgment
   watermark, insert the durable delivery batch with the exact acknowledged
   sequence range, promote the queued messages to `acknowledged`, and close the
   prior phase when steering changes its instructions.
2. **Execution-resumed transaction:** immediately before provider execution
   resumes, atomically mark the delivery batch `started`, mark its messages
   `applied`, and start the new phase with `trigger_kind="inbox_batch"` and the
   previously persisted batch ID and range.

If a provider reports acknowledgment and resume in one callback, the shared
orchestrator may execute both transitions back-to-back, but it must retain the
two state boundaries. Time between acknowledgment and actual resume never
contributes to the new phase duration.

### Queued Steering and Delivery Acknowledgment

The existing optimistic/queued message path must preserve delivery state.

Before acknowledgment:

- the message stays out of the rendered transcript;
- the active phase remains open;
- subsequent old-phase entries retain the old phase ID.

On acknowledgment:

- atomically close the active phase with `boundary_reason="steered"`;
- mark every inbox entry/message in the range acknowledged;
- persist the inclusive acknowledged sequence range for the pending resume;
- do not start or time the new phase yet;
- when execution actually resumes, atomically mark the batch/messages applied
  and start one new phase for that exact range;
- tag subsequent entries with the new phase ID.

On failed or cancelled delivery:

- do not start a phase;
- keep the unapplied message out of the transcript and surface recovery through
  the existing inbox failure notice;
- retain visible failure/cancellation state;
- preserve the old phase until its actual execution state changes.

Explicit interruption follows confirmation from runtime control rather than
submission time.

### Runtime Loss and Reconciliation

The durable runtime reaper/recovery path owns abandoned phase cleanup.

When a runtime lease is lost:

- close the active phase as `interrupted`;
- use `boundary_reason="runtime_lost"` or the more specific typed reason;
- guard the write with lease/runtime identity or expected phase status;
- publish the phase transition;
- start a new phase only when recovery actually resumes execution.

Add a bounded reconciliation path for stranded historical `running` phases.
The frontend must not infer terminal state from missing heartbeat or entry
timestamps.

Operational checks:

- alert when a running phase has no valid runtime lease beyond the recovery
  window;
- record reconciliation failures with org/session/thread/phase identifiers but
  no transcript content;
- ensure a stale worker cannot append new entries to a superseded phase.

### Tool-Call Count

Completed phase metadata returned by the transcript API includes an
authoritative `tool_call_count` computed from persisted `tool_use` records with
the same phase ID.

- Do not store a mutable counter on the phase row.
- Count one logical invocation regardless of result presence or result count.
- Pairing/deduplication must prevent persisted/live duplicates from inflating
  the count.
- While active, the frontend may show a provisional count from deduplicated
  live tool-use events.
- Persisted backend count replaces the provisional count after refresh.

### Activity Label

The latest active label should use existing agent/tool event data:

1. agent-emitted human-readable activity/reasoning summary;
2. concise actual tool/command/file/search operation;
3. generic fallback.

Do not invoke a secondary model solely for label generation.

Create one shared sanitization contract:

- redact known credential, token, password, and sensitive environment patterns;
- remove URL userinfo and sensitive query values;
- strip ANSI escapes and control characters;
- normalize whitespace to one line;
- cap displayed length;
- never use raw tool-result content as the label.

Prefer a server-provided sanitized summary so clients agree, but treat it as
untrusted display text and escape it normally. Preserve original transcript
activity for the expanded audit view.

### Transcript Window API

Embed phase metadata in the existing transcript-window response. Do not add a
second phase request.

Example:

```json
{
  "data": [
    {
      "turn_number": 4,
      "started_at": "2026-07-26T12:00:00Z",
      "ended_at": "2026-07-26T12:08:10Z",
      "phases": [
        {
          "id": "phase-uuid-1",
          "phase_number": 1,
          "status": "completed",
          "boundary_reason": "human_input",
          "started_at": "2026-07-26T12:00:08Z",
          "completed_at": "2026-07-26T12:01:20Z",
          "tool_call_count": 6
        },
        {
          "id": "phase-uuid-2",
          "phase_number": 2,
          "status": "completed",
          "boundary_reason": "final_response",
          "started_at": "2026-07-26T12:04:22Z",
          "completed_at": "2026-07-26T12:08:10Z",
          "tool_call_count": 14
        }
      ],
      "entries": [
        {
          "id": "tuse_123",
          "kind": "tool_use",
          "activity_phase_id": "phase-uuid-1"
        },
        {
          "id": "hiq_456",
          "kind": "human_input",
          "activity_phase_id": "phase-uuid-1"
        },
        {
          "id": "tuse_789",
          "kind": "tool_use",
          "activity_phase_id": "phase-uuid-2"
        }
      ]
    }
  ],
  "meta": {
    "position": "latest",
    "has_older": true,
    "has_newer": false,
    "thread_status": "idle"
  }
}
```

Requirements:

- phases are scoped to the same org/session/thread as the window;
- phase membership is explicit through `activity_phase_id`;
- the API returns every phase needed to render returned entries;
- turn windows remain the pagination boundary, but candidate turns are the
  union of turns present in messages, logs, human-input records, and durable
  phases;
- a turn containing a durable phase and no transcript entries is returned with
  an empty `entries` array, so zero-tool and interrupted-before-output phases
  cannot disappear from pagination;
- phase-only turns participate in older/newer cursor calculation;
- every phase has an opaque anchor ID such as `aph_<opaque-id>`; around-anchor
  loading accepts both transcript-entry and phase anchors without clients
  parsing either format;
- phase data and entries come from one coherent database read/transaction
  snapshot where practical;
- legacy turns may return an empty or omitted `phases`;
- existing transcript entry IDs and cursor semantics remain stable;
- current turn-level `started_at`/`ended_at` retain their documented
  entry-boundary meaning and are not reused as execution lifecycle fields.

Add typed frontend and Go models with validation tests.

### Frontend Render Model

Preserve phase structure through the transcript adapter instead of flattening it
away.

Representative model:

```ts
interface TimelineActivityPhase {
  id: string;
  turnNumber: number;
  phaseNumber: number;
  status: "running" | "completed" | "failed" | "cancelled" | "interrupted";
  boundaryReason?: string;
  startedAt: string;
  completedAt?: string;
  toolCallCount: number;
  provisionalToolCallCount?: number;
  latestActivityLabel?: string;
  entries: TimelineEntry[];
  inferredHistorical: false;
}

interface InferredHistoricalActivity {
  id: string;
  turnNumber: number;
  entries: TimelineEntry[];
  toolCallCount: number;
  inferredHistorical: true;
}

type TimelineNode =
  | { kind: "visible"; entry: TimelineEntry }
  | { kind: "phase"; phase: TimelineActivityPhase }
  | { kind: "historical_activity"; activity: InferredHistoricalActivity };
```

The final rendered timeline is one ordered `TimelineNode[]`. Visible boundary
events split inferred historical activity. Authoritative phase IDs determine
new-session membership; the frontend must not reconstruct authoritative phase
membership from timestamps. Unapplied inbox messages are excluded before node
construction and enter the visible timeline as normal user messages only after
the runtime applies them.

Queued or acknowledged-but-not-applied user messages are excluded from ordinary
timestamp ordering and from the rendered timeline. The transcript API supplies
stable inbox sequence, delivery state, and created/acknowledged/applied
timestamps. Once the runtime starts the delivery batch, `applied_at` becomes
its presentation boundary: the message appears atomically between the closed
prior phase and the newly running phase. `created_at` remains the audit
timestamp and must not pull the applied message back into older activity.
Abandoned delivery remains actionable through the existing inbox failure
behavior outside the transcript.

Persisted assistant-role messages are authoritative final responses under the
current message contract. `assistant_final` metadata remains authoritative for
output logs and persisted-message duplicate suppression. Ambiguous legacy
assistant output remains visible rather than being hidden.

### User Settings

Add a typed user setting such as:

```go
type SessionActivityDetail string

const (
    SessionActivityDetailCompact  SessionActivityDetail = "compact"
    SessionActivityDetailDetailed SessionActivityDetail = "detailed"
)

type UserSettings struct {
    // existing fields...
    SessionActivityDetail SessionActivityDetail `json:"session_activity_detail,omitempty"`
}
```

Requirements:

- missing/empty resolves to Compact;
- validation accepts only empty, `compact`, and `detailed`;
- use the existing RFC 7386 merge-patch endpoint;
- merge server-side in a transaction under row lock;
- do not replace the entire settings document;
- `/auth/me` returns the typed effective preference;
- Account settings and transcript controls share one mutation path;
- optimistic cache update rolls back on failure;
- preference changes affect the current thread immediately and all future
  thread opens;
- changing Compact/Detailed clears all transient phase overrides in the current
  thread before applying the new preference;
- individual capsule overrides never write user settings.

Add table-driven model tests, merge-patch tests, handler/store tests, API client
tests, and UI mutation/error tests.

### Disclosure State

Effective capsule disclosure:

1. transient per-phase override, when present;
2. Detailed preference: expanded;
3. Compact preference and running phase: expanded;
4. Compact preference and terminal phase: collapsed;
5. inferred historical activity follows the same Compact/Detailed rule.

The precedence above applies after the latest preference change. Switching the
preference first clears the transient override map: Detailed therefore expands
every loaded phase immediately, and Compact applies its active/terminal
defaults immediately. A later explicit capsule toggle may again create a local
override until the next preference change or fresh thread open.

Transient overrides use stable phase IDs. Historical inferred groups use a
deterministic client key derived from thread, turn, and boundary entry IDs.
Keep overrides outside row components so query replacement, live reconciliation,
and pagination do not reset the current view.

Interaction-aware preservation is a deterministic per-phase state machine:

- `untouched`: eligible for terminal auto-collapse;
- `manual_expanded`: set by an explicit expand action;
- `manual_collapsed`: set by an explicit collapse action;
- `child_open`: set when any nested tool disclosure is opened;
- `text_selecting`: set when a non-empty selection range is inside the capsule;
- `viewport_inspecting`: set after a user-initiated scroll leaves the live edge
  and at least 48 CSS pixels of the phase body remain visible for 250 ms.

Only `untouched` may auto-collapse on a terminal transition. Any other state is
protected for the lifetime of the rendered thread. A programmatic scroll,
prepend compensation, initial render, or momentary pass through the viewport
does not set `viewport_inspecting`. `manual_collapsed` stays collapsed when
labels or lifecycle state update. These states reset only on a fresh thread
open or when a preference change clears transient state.

This state is local to the current rendered thread. It is not written to the
backend and resets on a fresh open.

### Component Structure

Add an application-level component such as:

```tsx
<ActivityCapsule
  phase={phase}
  detailPreference={activityDetail}
  expanded={expanded}
  inspectionState={inspectionState}
  onExpandedChange={setExpanded}
>
  {renderTimelineEntries(phase.entries)}
</ActivityCapsule>
```

Requirements:

- header remains mounted and visible in expanded and collapsed states;
- collapsed children are not mounted;
- active elapsed timer is isolated so it does not rerender the full timeline
  every second;
- `ToolGroupEntry`, `HiddenLogsGroup`, markdown rendering, and on-demand
  full-output loading remain reusable inside;
- do not duplicate timeline entry rendering between phase and legacy paths;
- preserve entry container props/anchors for children;
- preserve day separators at conversation boundaries without duplicates;
- use semantic design tokens and shadcn/Radix primitives.

Likely primary frontend files:

- `frontend/src/lib/timeline.ts`;
- `frontend/src/lib/types.ts`;
- `frontend/src/lib/api.ts`;
- `frontend/src/components/chat-timeline.tsx`;
- `frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx`;
- `frontend/src/app/(dashboard)/settings/account/page.tsx`;
- focused tests adjacent to each changed module.

### Live Updates

Phase lifecycle information is a required part of the existing SSE/live-update
contract. Publish these typed events after their database transaction commits:

- `session_activity_phase.started`;
- `session_activity_phase.terminal`;
- `thread_inbox_delivery.acknowledged`;
- `thread_inbox_delivery.started`;
- `thread_inbox_delivery.abandoned`.

Every event includes a stable event ID, org/session/thread identifiers, the
durable phase or batch ID, the resulting typed state, and `emitted_at`. Phase
events include the full phase summary model. Delivery events include the
inclusive sequence range and delivery timestamps. Payloads contain no message,
label, command, reasoning, or tool-result content.

SSE is an invalidation and low-latency presentation channel, not the source of
truth. On a phase or delivery transition, the client refetches the affected
transcript window and replaces boundary state from one coherent response.
Persisted records win over optimistic/live records by stable ID. The client
deduplicates repeated event IDs and tolerates out-of-order events; reconnect,
window focus, and normal query revalidation recover missed events. The old
phase closure, applied user message, and new phase appear in one render commit
after the authoritative refetch, even when their SSE notifications arrive
separately.

Live requirements:

- phase-open creates/updates an active capsule;
- live entries carry the active phase ID;
- tool count is provisional until backend reconciliation;
- latest activity label updates without reopening a manually collapsed phase;
- boundary transition and visible boundary event appear atomically from the
  user's perspective;
- persisted data deduplicates live data using stable IDs;
- no transition forces the user to the live edge;
- active-to-terminal automatic collapse follows inspection-aware rules.

### Anchoring and Scroll Restoration

The session-detail controller remains the single owner of transcript windows,
entry anchors, prepend compensation, restored positions, and jump-to-latest.

For an entry inside a collapsed capsule:

1. load the containing transcript window;
2. resolve `activity_phase_id` or historical inferred group;
3. create a transient expansion override;
4. wait for the child entry to mount;
5. measure and restore the target position;
6. highlight/focus the target.

Older-window prepends mount new history according to the saved user preference,
then apply height-difference compensation. Detailed mode must remain correct
even when prepending multiple expanded phases. Compact mode must not expand
older phases merely because they loaded.

### Performance

- Do not mount collapsed activity children.
- Keep grouping linear in the number of loaded entries and phases.
- Compute completed counts on the backend and active provisional counts
  incrementally.
- Memoize phase nodes from stable query/live inputs.
- Isolate the active timer and live label from the rest of the timeline.
- Fetch full truncated tool output only when the individual tool is opened.
- Preserve bounded transcript-window loading.
- Do not fetch all history when the user switches Compact/Detailed mode.

Phase nodes are the intended future virtualization unit. Virtualization is not
part of version one.

### Emergency Kill Switch

Add a server-controlled feature flag that restores the existing flat timeline
for all users without a frontend redeploy.

The kill switch:

- affects rendering only;
- does not disable phase creation, association, reconciliation, or telemetry;
- does not delete phase records or user settings;
- can be activated quickly for hidden final responses, broken anchors, severe
  scrolling regressions, or other usability failures;
- has a test proving that the same transcript renders through the legacy path.

Expose the switch through the authenticated application-config response:

```json
{
  "session_activity_capsules_enabled": true,
  "revision": "opaque-revision",
  "updated_at": "2026-07-26T12:00:00Z"
}
```

The frontend loads it before selecting the transcript renderer, caches the last
known good value, refetches at least every 30 seconds while a session is open,
and refetches on window focus. A new page load observes the current value
immediately; an already-open page observes a change within 30 seconds. If the
initial config request fails and there is no cached value, use the legacy flat
renderer. A later failure retains the last known good value to avoid UI
flapping. Server-side activation/deactivation is audited with actor, revision,
timestamp, and reason, without transcript content.

The normal launch is immediate for all users; the flag is an emergency rollback
mechanism, not a cohort rollout.

### Analytics and Observability

Record privacy-safe product and correctness signals:

- Compact/Detailed preference changes;
- individual capsule expansion/collapse;
- automatic collapse suppressed due to inspection;
- anchor-triggered automatic expansion;
- phase start, terminal transition, and reconciliation failure counts;
- transcript entries missing an expected phase ID;
- stranded running phases;
- scroll-restoration failures and large unexpected scroll deltas;
- kill-switch activation.

Allowed properties include scoped identifiers already permitted by policy,
phase status/reason, tool-count bucket, duration bucket, interaction trigger,
and viewport class.

Never send:

- prompt or response text;
- commands;
- activity-label text;
- reasoning;
- tool input/output;
- file content or paths;
- secrets.

Add alerts for phase lifecycle integrity and frontend anchor failures. Product
preference and expansion behavior belongs in dashboards without a launch
success threshold.

### Documentation

Because this is a significant user-facing session behavior and Account setting,
implementation must update the relevant public Fumadocs session guide to
explain:

- why completed work is compacted;
- Compact versus Detailed behavior;
- how to expand one activity capsule;
- where to change the persistent preference;
- that expanded activity remains the complete inspectable execution trace.

Do not expose internal phase-table, runtime, kill-switch, or reconciliation
details in public docs. Update screenshots only after the final UI is visually
verified.

### Testing Strategy

The immediate all-user launch is blocked on the following test layers.

#### Backend model and store tests

Use table-driven parallel Go tests with `require` and exact expected values:

- phase status and boundary-reason validation;
- user preference validation and defaulting;
- phase start and monotonically increasing numbering;
- at-most-one-running-phase enforcement;
- terminal-update-once behavior;
- idempotent identical terminal transition;
- stale-worker/lease rejection;
- org/session/thread/turn ownership validation;
- authoritative tool-call count;
- phase listing within transcript windows;
- nullable historical associations;
- every store query filters by org ID;
- migration up/down behavior and tenancy lint.

Run focused package tests and `go vet` for touched packages. Because the change
crosses models, stores, services, handlers, orchestrator, migrations, and API
contracts, pre-PR verification should include full `go test ./...`,
`go vet ./...`, tenancy lints, and migration checks with large temp/cache paths
redirected under `/home/sandbox`.

#### Lifecycle service and orchestrator tests

Build a deterministic provider-independent harness covering:

- initial execution start;
- final-response atomic completion;
- human-input atomic close;
- approval and plan boundaries;
- queued steering before acknowledgment;
- acknowledged single-message and multi-message batches;
- failed/cancelled steering delivery;
- explicit interruption confirmation;
- maintenance pause and resume;
- runtime loss, reaper completion, and recovery;
- error, cancellation, and stop;
- recovered tool errors that do not close a phase;
- zero-tool phase;
- delegated same-thread activity;
- separate-thread isolation;
- stale worker after replacement phase;
- boundary-event transaction rollback.

Adapter contract tests for Codex, Claude Code, OpenCode, Amp, and Pi verify that
the supplied phase ID and lifecycle signals propagate. Required CI must not call
live providers.

#### Transcript API tests

Verify exact phase/entry response values for:

- multiple phases in one turn;
- a phase-only turn with an empty entry array in latest, older, and newer
  pagination;
- around-anchor loading from an opaque phase anchor;
- entries explicitly associated with each phase;
- visible boundary entries carrying the closing phase ID;
- authoritative tool counts;
- running phase;
- historical turn without phases;
- around-anchor window;
- older/newer pagination;
- phase needed by a returned entry is always present;
- org/session/thread scoping and cross-tenant rejection;
- stable existing cursor and entry IDs.

Verify delivery-batch persistence and lifecycle for:

- exact contiguous sequence-range allocation under the locked runtime
  acknowledgment watermark;
- acknowledgment replay idempotency;
- rejection of overlapping and discontinuous ranges;
- acknowledgment without execution time;
- atomic batch `started` transition and one phase creation on execution resume;
- one phase per acknowledged batch under concurrent retries;
- explicit abandonment when an acknowledged batch never resumes.

Verify every status/reason pair accepted by the database and typed model, and
reject every invalid pair, including running rows with terminal data and
terminal rows without completion data.

#### Pure frontend transformation tests

Use table-driven Vitest cases for:

- authoritative phase grouping;
- multiple capsules within one turn;
- visible boundary ordering;
- inferred historical grouping without duration;
- assistant final-message and duplicate-log handling;
- provisional/live deduplication;
- active and terminal summary formatting;
- zero-tool phases;
- invalid/missing timestamps;
- activity-label sanitation;
- Compact/Detailed effective state;
- transient override precedence;
- queued steering omission before acknowledgment and applied-boundary ordering
  after execution resumes;
- malformed legacy fallback.

Compare complete expected structures rather than partial lengths where
practical.

#### Component tests

Use Testing Library and user-event to verify:

- Compact defaults;
- Detailed mode;
- active and terminal disclosure;
- expanded headers remain visible;
- summary copy and pluralization;
- active label updates;
- keyboard/pointer behavior and `aria-expanded`;
- collapsed children are not mounted;
- individual tool detail still expands and loads full output;
- visible boundary events and final responses remain visible;
- interaction suppresses automatic collapse;
- all inspection-state transitions, including the 48-pixel/250-ms viewport
  threshold and exclusion of programmatic scrolling;
- a preference switch clears overrides before applying Detailed or Compact,
  while a later manual toggle creates a fresh local override;
- preference mutation optimistic update, rollback, and error state;
- reduced-motion behavior where observable;
- kill-switch legacy rendering.

#### Session-detail integration tests

Verify:

- active phase streaming to atomic terminal boundary;
- duplicated, missed, and out-of-order lifecycle SSE notifications reconcile
  to the durable transcript state;
- two phases around human input in one turn;
- queued steer omitted until the acknowledged new phase starts;
- multiple messages in one acknowledged batch;
- interruption and recovery capsules;
- query refresh preserves transient state;
- additional windows obey saved Compact/Detailed preference;
- anchor expansion occurs before scrolling;
- older-window prepend compensation;
- jump-to-latest in both modes;
- thread switching does not leak phase overrides;
- historical and authoritative phases coexist.

#### Playwright browser tests

Add a separate required `session-transcript-e2e` CI job. It runs in parallel with
frontend lint/build and Vitest.

Required PR coverage:

- Chromium desktop;
- Chromium mobile viewport;
- focused transcript scenarios only;
- while this feature owns the transcript renderer, run for every change under
  `frontend/**`, `internal/**`, `migrations/**`, or `.github/workflows/**`;
  do not use a narrower hand-maintained list of “relevant” files;
- documentation-only and unrelated repository metadata changes may skip it
  through one centrally maintained CI path classifier;
- the required-check aggregator must require the job whether it runs or is
  explicitly skipped by that classifier, so branch protection never waits on a
  missing check.

Scheduled and pre-launch coverage:

- Chromium desktop and mobile;
- WebKit desktop and mobile;
- Firefox is out of scope initially.

The browser suite must cover:

- active streaming and completion collapse;
- inspection-aware non-collapse;
- human-input and steering boundaries;
- queued steering omission before acknowledgment and atomic appearance after
  application;
- older-window prepend without scroll jumps;
- deep-link expansion into collapsed activity;
- Compact/Detailed persistence after reload and a second browser context;
- historical fallback without duration;
- runtime interruption and resumed-phase separation;
- zero-tool phase;
- kill-switch fallback;
- kill-switch refresh on initial load, focus, and the 30-second freshness bound;
- light/dark theme and responsive wrapping;
- keyboard disclosure behavior.

CI infrastructure:

- install/cache Chromium in the required PR job;
- use deterministic API/SSE fixtures, not live coding agents;
- start only the minimum deterministic web/API fixture;
- upload trace, screenshot, and video outputs on failure;
- target approximately 2–5 minutes for the focused cached job;
- add the job to required checks.

#### Manual real-provider smoke matrix

Before the all-user launch, run documented sandbox smoke tests for every
configured provider:

- Codex;
- Claude Code;
- OpenCode;
- Amp;
- Pi.

For each provider verify at minimum:

- initial active phase;
- tool activity association;
- final response completion;
- human-input or steering boundary when supported;
- capsule summary and expansion;
- no missing final response;
- no browser console errors.

Also run the full Chromium/WebKit desktop/mobile Playwright matrix manually.

#### Full frontend launch gate

Before launch run:

- focused and full Vitest suites;
- frontend coverage gate;
- `npm run typecheck`;
- `npm run lint`;
- `npm run build`;
- required Chromium Playwright job;
- scheduled/pre-launch Chromium/WebKit matrix;
- preview update, screenshots, interactions, and console-error inspection.

Use rootfs-backed temp and cache directories under `/home/sandbox` for large
build/test outputs.

### Rollout Plan

#### Implementation ordering

1. Add typed activity phase models, migration, store, lifecycle service, and
   reconciliation.
2. Add explicit phase associations to transcript sources and shared writer
   context.
3. Instrument provider-independent orchestration and adapter propagation.
4. Embed phases in transcript windows and live updates.
5. Add the typed database-backed user preference.
6. Build the phase-preserving frontend model and capsule UI.
7. Integrate anchors, pagination, scroll restoration, queued-steering omission,
   and inspection-aware collapse.
8. Add unit, integration, Playwright, and manual smoke coverage.
9. Update public session documentation and final screenshots.
10. Complete the full launch gate.
11. Enable Compact for all users.

Phase recording should be deployed before or with the UI so new sessions have
authoritative timing immediately. Do not delay the UI to fabricate historical
timing.

#### Deployment compatibility

Use an expand-contract deployment:

1. Apply the additive migration with nullable transcript associations.
2. Deploy backend models, phase recording, transcript response fields, user
   setting support, reconciliation, and kill-switch control.
3. Verify phase integrity metrics while the existing frontend continues to
   ignore the additive response fields.
4. Deploy the capsule frontend and Account setting.

An older frontend must ignore new response fields safely. A newer frontend
receiving a turn without phase metadata must use the documented historical
fallback, so a partial backend rollout cannot hide transcript entries. Deploy
user-setting backend support before exposing the mutation control; the existing
settings merge validator correctly rejects unknown fields on old API nodes.

#### Launch

- No cohort rollout.
- Compact is the default for every user without a saved preference.
- Detailed is available inline and in Account settings from launch.
- Emergency kill switch is tested and ready.
- Phase recording remains enabled if the UI kill switch is activated.

### Risks and Mitigations

| Risk | Mitigation |
| --- | --- |
| A final answer is hidden in activity. | Persisted assistant messages are visible boundaries; final response and phase completion are atomic; conservative legacy fallback remains visible. |
| Activity is assigned to the wrong instruction after steering. | Keep the old phase active until runtime acknowledgment; omit queued messages until application; use one phase per acknowledged batch. |
| Duration includes waiting or setup. | Start at actual execution and close on confirmed pause/final persistence; store authoritative phase timestamps. |
| A crash leaves a phase running forever. | Reaper closes lost-runtime phases, guarded by lease/status; alert and reconcile stranded rows. |
| A stale worker writes to a replacement phase. | Propagate explicit phase ID and runtime/lease identity; reject stale terminal and append operations. |
| Boundary event and phase state disagree. | Persist platform-owned boundary events and phase transitions atomically. |
| Historical sessions display false duration. | Never backfill from transcript timestamps; use neutral inferred capsules without duration. |
| Final tool counts drift. | Backend derives completed count from persisted logical tool-use records; live count is provisional only. |
| Activity labels expose secrets. | Apply baseline credential/URL/control-character redaction and bounded one-line formatting; never use raw results. |
| Auto-collapse disrupts someone reading. | Suppress collapse when the user interacted, selected, opened, or inspected the capsule. |
| Loading older history resets detail behavior. | Database-backed user preference applies to every loaded window; transient overrides use stable phase IDs. |
| An anchor points into unmounted content. | Expand the containing phase before measuring and scrolling; cover in Chromium/WebKit browser tests. |
| Collapsed content still hurts performance. | Do not mount children; retain bounded windows and on-demand full output. |
| Immediate rollout has an escaped regression. | Comprehensive launch gate plus server-controlled rendering kill switch that preserves phase recording. |
| Provider behavior diverges. | Central lifecycle ownership, deterministic shared harness, adapter contract tests, and manual provider smoke matrix. |

### Resolved Decisions

- Activity phases, not turns, are the disclosure unit.
- Human input, approvals, applied steering, interruption, and final response
  create phase boundaries.
- Runtime acknowledgment, not submission time, controls steering boundaries.
- One acknowledged delivery batch creates one phase.
- Acknowledged delivery batches have durable identity; acknowledgment and
  execution resume are separate transitions.
- Actual execution time is authoritative; waiting/setup/cleanup are excluded.
- A durable phase table is required.
- Phase rows are inserted running and terminally updated once.
- Transcript source rows carry nullable explicit phase associations.
- Boundary-event writes and phase transitions are atomic.
- Lifecycle ownership is provider-independent and centralized.
- Lost runtimes close phases through recovery/reaper paths.
- Delegated work is owned by its thread.
- Every authoritative phase renders, including zero-tool phases.
- Phase-only turns participate in transcript pagination and anchoring.
- Completed tool counts are backend-derived.
- V1 omits files changed and test outcomes.
- Activity labels use agent/actual-operation text with baseline sanitation and
  no secondary model call.
- Compact and Detailed ship together.
- Compact/Detailed is a database-backed user preference exposed inline and in
  Account settings.
- Individual phase disclosure overrides are transient.
- Preference changes clear transient disclosure overrides before applying the
  new mode; later manual toggles may create fresh overrides.
- Completed phases collapse only when the user is not inspecting them.
- Inspection-aware collapse follows a deterministic client state machine.
- Expanded headers remain visible.
- Historical sessions use inferred capsules without guessed duration.
- Phases are embedded in transcript-window responses.
- Queued messages stay out of the transcript and appear at their applied
  boundary only when execution begins.
- Durable lifecycle SSE events invalidate and reconcile to transcript state.
- Launch is immediate to all users with an emergency rendering kill switch.
- Open pages refresh kill-switch configuration within 30 seconds.
- Required CI is deterministic; live providers are manual smoke tests.
- Playwright runs as a separate required Chromium job for all frontend,
  backend, migration, and workflow changes, with WebKit in scheduled and
  pre-launch coverage.
- Telemetry contains no transcript or activity-label content.

### Implementation Resolutions

1. Durable records use `session_activity_phases` and
   `thread_inbox_delivery_batches`; transcript sources use
   `activity_phase_id`.
2. Durable session messages and human-input requests use ownership-validated
   phase foreign keys. Partitioned session logs carry the nullable association
   without a phase FK and validate ownership in their write path.
3. Compact/Detailed lives in the existing transcript toolbar overflow menu and
   persists through database-backed user settings.
4. The emergency switch is server-controlled application config, refreshed on
   initial load, focus, and a 30-second interval. Privacy-safe UI events use the
   existing authenticated API and OpenTelemetry metrics path without transcript
   or label content.
5. Playwright uses a dedicated session activity fixture page with deterministic
   API/state controls; required PR coverage runs Chromium desktop and mobile,
   with WebKit reserved for scheduled and pre-launch verification.

### Decision Log

- **2026-07-14:** Proposed turn-level progressive disclosure with completed
  work collapsed by default.
- **2026-07-25:** Corrected the initial design to require authoritative
  lifecycle timing and preserve visible-event chronology.
- **2026-07-26:** Replaced turn-level grouping with separate contiguous
  activity phases; added durable lifecycle records, explicit transcript
  associations, acknowledgment-based boundaries, atomic transitions,
  database-backed Compact/Detailed preference, immediate all-user rollout,
  emergency kill switch, and the full deterministic/manual/browser testing
  strategy.
- **2026-07-26:** Closed the full-pass consistency gaps by adding phase-only
  pagination and anchors, durable acknowledged-batch identity, unapplied-message
  omission, separate acknowledgment/resume transactions, preference-reset
  semantics, a complete status/reason matrix, required lifecycle SSE events,
  kill-switch freshness/failure behavior, broad required Playwright path
  coverage, and a deterministic inspection state machine.
