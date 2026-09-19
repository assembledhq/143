# Design: Automation Target Session Continuity

> **Status:** Not Started | **Last reviewed:** 2026-09-19

> **Depends on:** [overall.md](../overall.md), [48-automations-separation.md](../implemented/48-automations-separation.md), [119-github-automation-trigger-context.md](../implemented/119-github-automation-trigger-context.md), [76-pr-repair-session-continuity.md](../implemented/76-pr-repair-session-continuity.md), [82-durable-session-executors.md](../implemented/82-durable-session-executors.md), [88-shared-sandbox-thread-runtimes.md](../implemented/88-shared-sandbox-thread-runtimes.md), [54-s3-session-snapshots.md](../implemented/54-s3-session-snapshots.md), [102-agent-run-capabilities.md](../implemented/102-agent-run-capabilities.md)
>
> **Related:** [code-review-scheduling-and-reuse.md](../code-review-scheduling-and-reuse.md) (result reuse for the built-in reviewer, not session reuse), [116-automatic-pr-feedback-follow-through.md](116-automatic-pr-feedback-follow-through.md) (canonical-session continuation for 143-generated PRs)

## Summary

A GitHub-triggered automation that fires on every push to a labelled pull request should not pay for a fresh container, a fresh clone, and a cold agent on each push. It should continue one conversation per pull request: the same session, the same restored workspace, and, where the provider's native state survives, the same agent context, so each new turn reviews what changed since the last turn instead of starting over.

This design adds an opt-in **per-target session continuity** mode to automations. The target is the pull request. The first trigger creates a session. Later triggers for the same PR continue that session through the existing `continue_session` path, after moving the restored workspace to the exact head SHA the run is about. Runs still get their own `automation_runs` row, history, outcome, and usage; they execute inside a shared session.

Default behavior is unchanged: automations keep creating one session per run unless continuity is enabled.

The savings this design commits to come from Stage 1: no clone, no tool bootstrap, no cold read of the repository, and a delta review instead of a whole-PR review, in the typical case where dependency inputs are unchanged between pushes. Keeping containers warm between pushes (Stage 3) is not part of that commitment. It is a conditional stage that is built only if Stage 1 measurements show restore time is a meaningful share of turn time, and it ships with hard budgets because an idle warm container occupies the same node slot as a running turn.

## Problem

Today `triggerAutomation` creates an `automation_runs` row per accepted delivery and `newAutomationRunHandler` always creates a new session and enqueues `run_agent` ([github_events.go](../../../internal/services/automations/github_events.go), [handlers.go](../../../internal/worker/handlers.go)). Nothing links a run to a prior session for the same PR. Every push therefore repeats:

- sandbox creation, repository clone, tool bootstrap, and auth bootstrap
- a cold read of the repository and the design principles the goal references
- a review of the whole PR rather than the delta since the last review

The primitives to avoid this already exist at the session layer:

- `continue_session` claims a resumable session, restores its snapshot (workspace and agent state directories), and resumes the provider CLI natively when the captured agent session ID is present. Every adapter with `ResumeBySessionID` resume mode does this: Claude Code `--resume`, Codex `exec resume`, OpenCode `--session`, Amp `threads continue`, Pi `--session`. When the ID is missing, the orchestrator embeds bounded transcript history instead.
- Separately, `CheckpointCapability` ([runtime.go](../../../internal/services/agent/runtime.go)) records whether a restored snapshot is expected to carry native provider state: `full_resume` for Codex, Claude Code, and OpenCode; `filesystem_resume` for Amp and Pi. It does not select the resume path; it describes what a durable checkpoint guarantees after container loss.
- At the end of a turn the container is destroyed as soon as the turn hold is released, unless a preview or an active `session_sandbox_holders` row holds it, and cleanup succeeds. There is no idle grace. `SANDBOX_GC_GRACE` applies only to orphaned containers that lost their database reference. Automation sessions never have a preview, so their containers are destroyed at turn end.
- `session_sandbox_holders` lets any holder kind keep a sandbox alive under a lease.
- PR repair (doc 76) already continues one canonical session per PR with two workspace sources: snapshot continuation or PR-head reconstruction, verifying that checked-out `HEAD` equals the expected SHA.

Doc 116 notes the gap directly: "Generic automations create separate sessions and do not preserve complete multi-comment review or reply-thread state." This design closes that gap for automations without turning them into the built-in code reviewer.

## Principle

Borrowed from doc 76: **session continuity and workspace correctness are separate concerns.**

- The automation target session owns the conversation: the goal prompts, assistant replies, logs, and per-turn results for one PR.
- The workspace must match the head SHA the triggering run is about, on every turn including the first, regardless of whether the backend reused a live container, restored a snapshot, or rebuilt the checkout.

The run remains the unit of "what happened at this trigger" and carries its own outcome, reason, timing, and usage. The session is the unit of memory.

## Scope

In scope:

- GitHub triggers whose event identifies a pull request: `github.pull_request.opened`, `github.pull_request.updated`, `github.pull_request.ready_for_review`, `github.pull_request.merged`, `github.check_suite.completed`, `github.check_run.completed`, `github.issue_comment.created`, `github.pull_request_review.submitted`, `github.pull_request_review_comment.created`.
- Automations whose `publish_policy` is `none`. Continuity in this version is for read-and-report automations such as design-principle reviews. `publish_policy = none` disables 143's automatic PR creation; it is not a read-only sandbox. The agent can still edit files and run commands. Per-target sessions therefore also run with a capability snapshot that omits branch publication and PR tools, and every turn resets the workspace to the run's head SHA, discarding prior writes. Sessions that publish PRs accumulate branch state across turns and need the changeset model; that is deferred.
- All coding adapters. Native resume is attempted for every adapter with a captured agent session ID; the run records whether native context was preserved.

Out of scope for this version:

- Schedule, manual, Linear, PagerDuty, and Slack triggers. They have no per-target identity today; a later revision may key Linear triggers on the issue.
- Continuity across pull requests or across automations.
- Replacing or changing the built-in code reviewer and its scheduler.
- Merging transcripts, forking sessions, or exposing continuity to the external API.
- Repairing missed push webhooks. A missed push is reviewed by the next push's run.

## Product Behavior

### Settings

Automation settings gain a **Conversation** section:

- **Session continuity**: `New session per run` (default) or `Continue one session per pull request`.
- **Keep sandbox warm for** (Stage 3, only if built): `Off` (default), 15, 30, 60, or 120 minutes. Shown only when continuity is enabled. Explains that warm time counts toward container usage, is best-effort, and is capped per automation, per organization, and per worker node.
- **Warm targets** (Stage 3, only if built): how many pull requests this automation may keep warm at once. Default 3.

Enabling continuity requires at least one PR-scoped GitHub event trigger and `publish_policy = none`. The form explains both requirements inline and the API rejects violations.

### First trigger for a PR

A run is created and a session starts, as today, with one difference: the workspace is checked out at the run's head SHA (see Workspace Preparation) instead of the automation's base branch. The backend records the session as the active generation for the target `(automation, repository, PR)`.

### Later triggers for the same PR

- The run row appears with a **Continued** badge and shows `previous head → head`.
- The session transcript gains one new user turn containing the automation goal, the GitHub event context, and a continuation block describing what changed since the last successfully reviewed head.
- Logs, result summary, outcome, and token usage for the turn attach to the run; the transcript stays in the session.
- If the stored session cannot be continued safely, the run shows **Fresh** (a new generation started) or **Reconstructed** (same session, rebuilt workspace, native context lost). The reason is stored on the run as `outcome_reason` and shown in run details.

### Pushes and other events while a turn is running

At most one turn executes per target at a time, across generations. A run that arrives while the target is busy waits with reason **Waiting for the current turn**.

Waiting runs are not interchangeable:

- A new **push** run (`github.pull_request.updated`) immediately supersedes any older waiting push run for the same target. Ten pushes during one review produce one follow-up turn at the newest head.
- **Comment, review, and check** runs queue in arrival order and each executes. They are never coalesced with each other or with pushes, because each carries a distinct request. A same-head comment run receives an empty code delta plus the event text.
- At most 10 runs wait per target. Beyond that, a new run fails immediately with `wait_overflow`.

### Reset

The automation page lists target conversations (repository, PR, generation, turn count, last reviewed head, last activity). Members and admins can **Reset conversation** for a target, which retires the active generation so the next trigger starts a fresh session. Resetting does not delete the session or its history. A running turn finishes on the old generation; its completion does not dispatch waiters into the old generation. Waiting runs are re-evaluated against the new generation when the old turn releases the target.

## Continuation Decision

The run handler evaluates the target inside the ownership transaction described in Turn Ownership and Dispatch. The decision has three parts: readiness of the stored session, compatibility of the run with the session, and the resulting action.

### Readiness

Readiness is computed from the session row and its runtime fields, not from `sandbox_state` alone, because `sandbox_state = none` is a valid non-destroyed value that proves nothing.

| Readiness | Condition | Workspace source |
|-----------|-----------|------------------|
| `live` | `container_id` set, `worker_node_id` set, no `pending_snapshot_key` | Reused live container on the owning node (Stage 3 warm hold, or a preview) |
| `checkpoint` | `snapshot_key` set, `pending_snapshot_key` empty, last checkpoint kind is `turn_complete`, `graceful_stop`, or `bootstrap`, snapshot age within `SESSION_MAX_SNAPSHOT_AGE`, snapshot bytes within the checkpoint size limit | Snapshot restore |
| `pending` | `pending_snapshot_key` set | Retry the job with a 15 s delay for up to 3 minutes; then treat as `checkpoint` if a key was published, else `rebuild` only after the existing stranded-pending-key clearing has run (never race a live publisher) |
| `rebuild` | No usable checkpoint: missing or reaped snapshot, `sandbox_state = destroyed` with no snapshot, restore failure | PR-head reconstruction in the same session |

Native context availability is separate: `agent_session_id` present on the primary thread means native resume is attempted; absent means embedded history. A native resume attempt that fails (provider state missing, CLI incompatibility) falls back to embedded history within the same turn. Because per-target turns are review-only and the workspace is reset every turn, a fallback never replays side effects. The run records `native_context = false` in either case.

### Compatibility

| Check | Source | Failure action |
|-------|--------|----------------|
| Automation has `session_continuity = per_target` and the run carries a PR target | run `config_snapshot.github` | fresh per-run session, no target row |
| Continuity kill switch not set on the worker | worker env | fresh per-run session; target rows untouched |
| Active generation exists | `automation_target_sessions` | fresh session, new generation |
| Session not archived or deleted | `sessions.archived_at`, `deleted_at` | retire (`session_unavailable`), fresh |
| Session status is `idle` or in `ResumableSessionStatuses` | `sessions.status` | `running`: wait; other: retire (`not_resumable`), fresh |
| Agent type, model override, and reasoning effort match the automation's current values | automation row vs session | retire (`agent_config_changed`), fresh |
| Identity scope and executing user unchanged | automation `identity_scope`, `created_by` vs session `triggered_by_user_id` | retire (`identity_changed`), fresh |
| Repository installation still active and the automation still authorized for the repository | repository and installation rows | run fails (`repository_unavailable`), no dispatch |
| Base branch unchanged since the last turn | run `config_snapshot.github.base_branch` vs `last_base_ref` | retire (`base_retargeted`), fresh |
| `turn_count < 25` | target generation row | retire (`turn_limit`), fresh |
| Last checkpoint within the size limit (default 2 GB) | session snapshot bytes | retire (`snapshot_too_large`), fresh |

The current capability snapshot is resolved per run through `ResolveForSession`, as fresh runs do today. The continued turn executes with the run's snapshot; the session row's snapshot is updated to match inside the ownership transaction so credential and tool resolution during the turn sees the current grant. A narrowed grant therefore applies on the next turn without retiring the session.

Continuity mode is read from the automation row at dispatch time, not from the run's `config_snapshot`. The snapshot keeps the value for audit. The worker kill switch overrides both.

### Actions

- **continue**: readiness `live` or `checkpoint`; `continuation_mode = continued`.
- **reconstruct**: readiness `rebuild`; `continuation_mode = reconstructed`; delivered in Stage 1.
- **fresh**: no active generation or a retire decision; `continuation_mode = fresh`; a new generation row is inserted with `generation + 1`.
- **wait**: session claim fails because the session is running (an automation turn or a human turn); see Waiting and Coalescing.

Retirement never deletes anything. It sets `status = retired`, `retired_reason`, and `retired_at`. `retired_reason` values: `manual_reset`, `pr_closed`, `pr_merged`, `session_unavailable`, `not_resumable`, `agent_config_changed`, `identity_changed`, `base_retargeted`, `turn_limit`, `snapshot_too_large`, `continuity_disabled`.

## Turn Ownership and Dispatch

This section defines the atomic unit that turns a pending run into an executing turn. It replaces the separate "target lock, then claim, then enqueue" sequence, which could leave a half-claimed session.

### Identity rows

Two tables separate the logical target from its sessions:

- `automation_targets` is the identity and lock row for `(org, automation, repository, target_kind, target_key)`. It exists whether or not a session exists yet, so an absent target can be created and locked without a placeholder session.
- `automation_target_sessions` holds one row per generation with a non-null `session_id`.

### Ownership transaction

One database transaction, lock order fixed:

1. Upsert and `SELECT ... FOR UPDATE` the `automation_targets` row.
2. Read the active generation, if any, and evaluate Compatibility.
3. If another run is executing for the target (`automation_runs.dispatch_state = executing` for this `target_id`, enforced by a partial unique index), or the session claim in step 5 fails because the session is running, record the run as waiting (`dispatch_state = waiting`, `wait_reason = target_busy`, `wait_started_at = now`) and commit. Push runs supersede older waiting push runs here.
4. Reserve the run: set `target_id`, `target_generation`, `session_id` (existing or newly created), `continuation_mode`, `previous_head_sha` (the generation's `last_reviewed_head_sha`), `dispatch_state = executing`, `execution_started_at = now`, `attempt = 1`.
5. Claim the session and its primary thread with a new store method `ClaimForAutomationTurn(ctx, orgID, sessionID, threadID, allowDestroyed)`. It accepts `idle` plus `ResumableSessionStatuses`, applies the same runtime reset assignments as `ClaimForResume`, permits `sandbox_state = destroyed` only when the action is reconstruct, and claims the primary thread through the thread store's resume or fresh claim as appropriate. Existing `ClaimForResume` is not reused because it excludes `idle` and rejects destroyed sandboxes.
6. Insert the visible user message on the primary thread.
7. Enqueue `continue_session` (or `run_agent` for fresh) with dedupe key `automation_turn:<run_id>` and store the returned `job_id` on the run.
8. Commit. The job notify fires after commit.

Any failure rolls back every step, leaving the run `pending` for retry. The target lock orders automation runs among themselves; the session claim is the arbiter between automation turns and human turns on the same session.

### Dispatch identity

The dedupe key is run-scoped, not thread-scoped. The existing thread key `continue_session:<thread_id>` cannot be used: `EnqueueInTx` returns `uuid.Nil` on conflict with a pending or running job carrying the same key, and the previous turn's job is still running while its completion logic executes, so the next run's enqueue would be silently dropped. Serialization is provided by the ownership transaction, not by the dedupe key. An enqueue that returns `uuid.Nil` for an `automation_turn:<run_id>` key is accepted only if the existing job's payload carries the same `run_id`; otherwise the transaction fails.

### Invariants

- At most one run with `dispatch_state = executing` per target across generations (partial unique index).
- A run in `executing` has a non-null `session_id`, `job_id`, and `execution_started_at`.
- A session that is the active generation for a target is never claimed by another automation while a run is executing for that target.

### Retry and recovery

The `continue_session` payload carries `automation_run_id`, `target_generation`, and `attempt`. On any retry the handler re-reads the run:

- `dispatch_state = executing` with the same `job_id`: the run recognizes its own reservation and re-enters the turn without re-running the ownership transaction. Session status is restored by the existing continuation recovery paths (`idle` and `snapshotted` on startup failure, drain requeue on worker drain).
- Turn completed but completion not recorded (worker crash after the orchestrator returned): the handler observes the session `idle` with a snapshot published after `execution_started_at` and calls the completer with outcome derived from the thread's last turn; completion is idempotent (see Completion).
- Job dead-lettered: the completer records `failed` with `outcome_reason = retries_exhausted`, releases the target, and dispatches the next waiter.

A retry never waits on its own reservation and never creates another session for the same run.

### Clocks and the stuck-run reaper

The scheduler's stuck-run reaper marks pending or running runs failed one hour after `triggered_at` (`stuckAutomationRunThreshold`). That is wrong for a run that waited legitimately. The reaper query changes to:

- exempt runs with `dispatch_state = waiting`;
- measure executing runs from `execution_started_at`, and skip a run whose `job_id` has a live lease (running job with unexpired `lock_token` lease, or an active session executor row).

A separate wait timeout fails waiting runs two hours after `wait_started_at` with `outcome_reason = wait_timeout`.

## Workspace Preparation

Continuation reuses `ContinueSession` with a new `AutomationTurnContinueOptions` value alongside `PRRepair` and `PRFeedback`. The same preparation applies to fresh first turns, which today clone the base branch and create a working branch without any PR-head checkout.

### Checkout at the exact head

For every automation turn in per-target mode:

1. Fetch the run's head commit by SHA: `git fetch origin <head_sha>` (GitHub serves reachable commits by SHA), falling back to `refs/pull/<n>/head` only to populate the local ref. `head_sha` is validated as 40 lowercase hex characters and the PR number as an integer before either is passed to git; nothing from GitHub is interpolated raw.
2. If the SHA is unreachable (history rewritten, PR force-pushed past it), the run is skipped with `outcome_reason = stale_head`. The newer push has its own run.
3. Fetch the base ref and record `base_sha = git merge-base origin/<base_ref> <head_sha>` on the run. This is the baseline for a full review when no previous head is usable.
4. Check out detached: `git checkout --detach <head_sha>`. Per-target sessions do not maintain a working branch; `sessions.working_branch` is set to `pr/<n>` for display only, and diff collection is disabled for these sessions because `publish_policy = none` makes a session diff meaningless.
5. Verify `git rev-parse HEAD == head_sha`; a mismatch fails the turn before the agent starts.

### Clean tree and dependency invalidation

Before checkout: `git reset --hard && git clean -fd` (not `-x`, so ignored dependency and build caches survive). The discarded path list is bounded to 50 entries, sanitized, and logged. When `.gitmodules` exists, `git submodule update --init --recursive` runs after checkout. Nested repositories and Git LFS are unsupported in this version; a target whose checkout contains either is retired with `unsupported_workspace`.

A clean tracked tree does not make the workspace runnable when dependency inputs changed. The turn computes a **dependency fingerprint**: a hash over the lockfiles present at the head (`package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`, `go.sum`, `requirements*.txt`, `Gemfile.lock`, `Cargo.lock`), the repo-config sandbox dependency declarations, and the sandbox image digest. It is compared with `dependency_fingerprint` on the generation row:

- Unchanged: no install step; the prompt says caches are current.
- Changed: `sandboxdeps.Apply` re-runs for declared tools, and the prompt tells the agent that project dependencies may be stale and names the changed manifests. Project dependency installation remains the agent's responsibility, as it is for fresh sessions today.

The fingerprint is stored on success. The "no install" saving in the Summary is therefore typical, not guaranteed.

### Delta

Compute the continuation delta from the generation's `last_reviewed_head_sha` (the head of the last **successful** turn, never advanced by failed turns) to `head_sha`: `git diff --stat` and the changed-file list, bounded to 200 files and 4 KB of stat output. When `last_reviewed_head_sha` is null or unreachable, the delta is computed from `base_sha` and the prompt asks for a full review.

### Snapshot continuation or live container

Readiness `checkpoint` restores the snapshot into a new container (or `live` reuses the held container on the owning node), then runs the checkout steps above. The container restore path is unchanged.

### PR-head reconstruction

Readiness `rebuild` creates a fresh sandbox, clones, runs the checkout steps, and runs the turn in the same session with bounded transcript context (initial goal, latest assistant summary, last two turns) because native state is gone. This reuses the PR repair reconstruction helper and the bounded-context builder, with the SHA-by-fetch change above. The turn's final snapshot attaches to the session so the next trigger can use snapshot continuation. Reconstruction is delivered in Stage 1 because the decision table needs it.

## Prompt

Add `internal/prompts/templates/automation_turn.template` with an exported render function. The visible user message on the primary thread is:

1. The run's `goal_snapshot` (goal plus GitHub event context, as today).
2. A **Continuation context** block: turn number, `previous_head_sha` or "none", `head_sha`, `base_sha`, base branch, the bounded diff stat and changed-file list, whether native agent context was preserved, and whether dependency inputs changed.
3. For non-push events at the same head: the event text and an instruction to respond to the event without re-reviewing unchanged code.
4. Instructions: review the changes since the previous head against the goal; do not repeat findings for unchanged code unless the change invalidates them; state which earlier findings the new push resolved; when history is unavailable, review the full PR from `base_sha`.

PR-derived text (title, body, comment bodies, review text, file paths, diff content) and prior assistant summaries used in reconstruction are placed in delimited data blocks that the template marks as untrusted content to be analyzed, never followed. Each block is size-bounded. Native provider context may already contain such text from earlier turns; the per-turn template repeats the boundary instruction so it is present in every turn's instructions.

## Waiting and Coalescing

- **Push runs.** When a `github.pull_request.updated` run arrives and finds a waiting push run for the same target, the older run transitions to `skipped` with `superseded_by_run_id` set, immediately, inside the arrival transaction. Only the newest push waits.
- **Non-push runs** queue in arrival order. They are not superseded by pushes or by each other.
- **Dispatch order** when the target frees: the waiting push run first (it moves the reviewed head forward), then non-push runs in arrival order. Each dispatch runs the ownership transaction.
- **Head authority.** A run reviews its own `head_sha`. Arrival order and `triggered_at` are not used to decide which head is newest; reachability at fetch time is. A delayed delivery for an older head that is no longer reachable is skipped as `stale_head`. A delayed delivery for an older head that is still reachable executes, and the newer push's run follows; the generation's `last_reviewed_head_sha` only moves to a head that is an ancestor-or-equal of the newest known head, so an out-of-order older turn does not move the baseline backwards.
- **Same-head duplicates.** A run whose `head_sha` equals the generation's `last_reviewed_head_sha` and whose event is a push is skipped as `duplicate_head`. Same-head non-push runs execute.
- **Waiting cap.** At most 10 waiting runs per target; further arrivals fail with `wait_overflow`.

## Completion

Successful `ContinueSession` turns end with the session `idle` and do not call `AutomationHooks.OnSessionComplete`, which only reacts to terminal session statuses. Continued runs therefore need their own completion path.

### Automation turn completer

A new `AutomationTurnCompleter` is called by the worker handlers for `continue_session` and `run_agent` after the orchestrator returns, with `(automation_run_id, target_generation, attempt, outcome)`. Outcomes:

| Outcome | Run status | Run `outcome_reason` | Advances `last_reviewed_head_sha` |
|---------|------------|----------------------|------------------------------------|
| turn completed | `completed` | `turn_completed` | yes |
| agent failed or exited non-zero | `failed` | `agent_failed` | no |
| cancelled | `failed` | `cancelled` | no |
| awaiting human input | `failed` | `awaiting_input` (the session stays resumable for a human) | no |
| retries exhausted | `failed` | `retries_exhausted` | no |
| skipped before execution | `skipped` | `stale_head`, `duplicate_head`, `superseded`, `wait_timeout`, `wait_overflow`, `pr_closed` | no |

For per-target runs, `completed_noop` is not derived from the session diff. A report-only review that finished is `completed`.

### Fencing and idempotency

Completion updates the run only where `id = run_id AND dispatch_state = executing AND attempt = payload.attempt`, and updates the generation row only where `generation = payload.target_generation AND status = active`. A late callback from an earlier run or generation matches zero rows and is logged. Calling the completer twice for the same attempt is a no-op after the first success.

### Order of operations at turn end

1. Orchestrator publishes the turn-complete snapshot (existing).
2. Stage 3 only: warm hold admission and the usage-event swap happen here, inside the orchestrator's end-of-turn path, before `ReleaseTurnHold`, via an `AutomationTurnHooks.BeforeTurnHoldRelease` callback.
3. Orchestrator releases the turn hold and destroys or keeps the container (existing).
4. Worker handler calls the completer: run status, `outcome_reason`, durations, `native_context`, generation counters (`turn_count`, `last_reviewed_head_sha`, `last_attempted_head_sha`, `checkpoint_head_sha`, `dependency_fingerprint`, `last_run_id`, `last_turn_at`).
5. Completer dispatches the next waiting run for the target through the ownership transaction. The previous job row may still be `running`; the run-scoped dedupe key makes that irrelevant.

### Run-local history

Run list and detail responses read status, outcome, timing, and usage from the run row, never from the shared session's current state. Continued runs also store `thread_id` and `turn_number` so messages and logs for the turn can be addressed. Per-turn token usage rows written by the orchestrator gain an `automation_run_id` attribution for per-target turns. Historical per-run rows keep reading through the origin link.

`session_automation_links` keeps its meaning (the run that created the session); `automation_runs.session_id` is the executing session. Slack session notifications and the PagerDuty writeback receive the executing run's ID. `GoalImprovementService.OnSessionComplete` handles dedicated goal-improvement sessions and is unaffected. Migration 000248 is historical repair SQL and is left unchanged.

## Lifecycle Transitions

| Event | Running turn | Waiting runs | Warm hold (Stage 3) | Generation |
|-------|--------------|--------------|---------------------|------------|
| Reset (manual) | finishes on old generation; its completion does not dispatch | re-evaluated against the new generation when the target frees; execute fresh | released | retired `manual_reset`; next dispatch creates `generation + 1` |
| Continuity set to `per_run` | finishes | dispatched as ordinary per-run sessions | released | all active generations retired `continuity_disabled` |
| Worker kill switch | finishes | dispatched as ordinary per-run sessions | not created; existing holds expire | untouched, so re-enabling continues where it left off |
| PR merged | finishes; a subscribed `merged` run executes as a final turn first | skipped `pr_closed` | released | retired `pr_merged` |
| PR closed without merge | finishes | skipped `pr_closed` | released | retired `pr_closed` |
| PR reopened | n/a | n/a | n/a | next trigger creates a new generation; the retired row stays |
| Agent, model, effort, identity change | finishes | first dispatch retires and goes fresh | released on retire | retired with the matching reason |
| Base retarget | finishes | first dispatch retires and goes fresh | released on retire | retired `base_retargeted` |

Close and merge retirement do not depend on the automation subscribing to `github.pull_request.merged`. An unmerged close produces no automation event today, so `PRService` gains an internal lifecycle notification, `OnPullRequestClosed(repository, number, merged)`, that the trigger service uses to retire generations for every per-target automation in the org with a matching active target. Callbacks from a retired generation are fenced by `target_generation`.

## Warm Sandbox (Stage 3, conditional)

If Stage 3 is built, the end-of-turn path publishes the snapshot first, then attempts a **best-effort warm hold**: a `session_sandbox_holders` row with `holder_kind = automation_warm`, `holder_id = <generation row id>`, `expires_at = now + warm_sandbox_minutes`, and `usage_event_id` set to the warm usage event opened in the same step. The existing destroy decision consults active holders, so the container survives while the holder is active. Skipping the hold never affects correctness because the snapshot is already published.

### Budgets and atomic admission

| Budget | Where | Default | Rationale |
|--------|-------|---------|-----------|
| Per worker node | `WORKER_MAX_WARM_SANDBOXES` env | unset: `min(25% of effective WORKER_MAX_ACTIVE_SANDBOXES, WORKER_MAX_ACTIVE_SANDBOXES - 1)`; `0`: warm disabled on the node | Warm containers count against the live-container admission gate, so turns must keep guaranteed headroom; single-slot nodes get 0 |
| Per organization | org setting `automation_warm_sandbox_limit` | 5 | One tenant cannot fill a node with idle containers |
| Per automation | `automations.max_warm_targets` | 3 | A busy automation with many open PRs does not keep one container per PR |

Admission is atomic: one statement inserts the holder only if the org and automation counts of active `automation_warm` holders are below their limits, evaluated with the generation row locked; the node count is checked under the capacity gate's mutex on the owning worker before that statement. Rejection order is node, org, automation, and the reason is recorded as `warm_skipped_reason`. Reset, disable, and retirement release holds.

### Expiry sweep

No existing sweep expires generic holders and destroys containers; runtime reclaim only expires `thread_runtime` holders of lost runtimes. Stage 3 adds a worker-local sweep on each node, run with the sandbox GC interval, that for each expired `automation_warm` holder whose container this node owns: CAS the holder to `expired` with its lease token, run `FinalizeContainerDestroy`, destroy the container, close the warm usage event at the destruction time, and clear `container_id`. If the owning node is lost, the holder expires by time, the existing dead-node continuation path clears the stale container reference on the next turn, and the container is reclaimed by that node's GC when it returns or by the hard-max sweep.

### Eviction

Pressure GC today reclaims unreferenced containers and, past the 24-hour hard max, referenced containers with no active holder. It cannot preempt a valid warm hold, and the capacity gate performs one bounded cleanup pass and recounts rather than guaranteeing admission. Stage 3 adds a holder-aware path used only when admission for a real turn would fail: select the oldest `automation_warm` holder on the node by `last_turn_at` whose container has no other active holder, expire it as in the sweep, require finalize and destroy to succeed, recount, and repeat up to the pressure destroy limit. If admission still fails, the turn takes the ordinary admission-failure retry. A container with an active turn is never evicted.

### Node affinity

A warm continuation is enqueued with `jobs.target_node_id` set to the owning node. Wrong-node continuation today defers to the owner rather than restoring beside a live container, so affinity is kept rather than transferred. If the owner is dead, the dead-node path clears the container reference and the run restores from snapshot elsewhere, recording `warm_hit = false`.

### Billing

Interval transitions share one timestamp and are idempotent:

- Turn end with warm admitted: close the turn usage event at `T` and open the warm event at `T` for the same container, in the `BeforeTurnHoldRelease` step, before the deferred usage stop runs. The deferred stop then finds the turn event already closed and does nothing (`stopped_at IS NULL` guard added to `RecordStop`, which today can overwrite a closed interval).
- Reuse: at continuation start, close the warm event at `T2` and open the turn event at `T2`; the holder transitions to `released`.
- Expiry or eviction: the sweep closes the warm event at actual destruction time. If the node is lost, close at the last holder heartbeat. Billing ends at destruction, not lease expiry, because the container consumed resources until then.

The usage sampler tracks one active event per container; transitions swap the active event atomically. Rollups group by `purpose`, the usage API reports `warm_container_minutes` separately, and peak concurrency counts containers, not events.

## Database Contract

Migrations are additive. All tenant tables carry `org_id uuid NOT NULL REFERENCES organizations(id)`; every store method takes `orgID` and filters by it; inserts validate that the referenced automation, repository, session, and run belong to the same `org_id` before writing, because UUID foreign keys alone do not enforce same-org relationships. `updated_at` is maintained by the store on every update, as elsewhere in the repo.

### `automations`

```sql
ALTER TABLE automations
    ADD COLUMN session_continuity text NOT NULL DEFAULT 'per_run'
        CHECK (session_continuity IN ('per_run', 'per_target')),
    ADD COLUMN warm_sandbox_minutes integer NOT NULL DEFAULT 0
        CHECK (warm_sandbox_minutes >= 0 AND warm_sandbox_minutes <= 240),
    ADD COLUMN max_warm_targets integer NOT NULL DEFAULT 3
        CHECK (max_warm_targets >= 0 AND max_warm_targets <= 50);
```

All three values are captured in each run's `config_snapshot` for audit. `warm_sandbox_minutes` and `max_warm_targets` are Stage 3 columns; Stage 1 may ship without them.

### `automation_targets`

```sql
CREATE TABLE automation_targets (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid NOT NULL REFERENCES organizations(id),
    automation_id   uuid NOT NULL REFERENCES automations(id),
    repository_id   uuid NOT NULL REFERENCES repositories(id),
    target_kind     text NOT NULL CHECK (target_kind IN ('github_pull_request')),
    target_key      text NOT NULL CHECK (length(target_key) BETWEEN 1 AND 64),
    active_generation integer NOT NULL DEFAULT 0 CHECK (active_generation >= 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, automation_id, repository_id, target_kind, target_key)
);
```

This is the lock row. `active_generation = 0` means no session exists yet.

### `automation_target_sessions`

```sql
CREATE TABLE automation_target_sessions (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                  uuid NOT NULL REFERENCES organizations(id),
    target_id               uuid NOT NULL REFERENCES automation_targets(id),
    generation              integer NOT NULL CHECK (generation >= 1),
    session_id              uuid NOT NULL REFERENCES sessions(id),
    status                  text NOT NULL DEFAULT 'active'
                            CHECK (status IN ('active', 'retired')),
    retired_reason          text CHECK (retired_reason IN (
                                'manual_reset', 'pr_closed', 'pr_merged', 'session_unavailable',
                                'not_resumable', 'agent_config_changed', 'identity_changed',
                                'base_retargeted', 'turn_limit', 'snapshot_too_large',
                                'unsupported_workspace', 'continuity_disabled')),
    retired_at              timestamptz,
    turn_count              integer NOT NULL DEFAULT 0 CHECK (turn_count >= 0),
    last_attempted_head_sha text,
    last_reviewed_head_sha  text,
    checkpoint_head_sha     text,
    last_base_ref           text,
    dependency_fingerprint  text,
    last_run_id             uuid REFERENCES automation_runs(id),
    last_turn_at            timestamptz,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_automation_target_sessions_retired
        CHECK ((status = 'retired') = (retired_at IS NOT NULL AND retired_reason IS NOT NULL)),
    UNIQUE (org_id, target_id, generation)
);

CREATE UNIQUE INDEX idx_automation_target_sessions_active
    ON automation_target_sessions (org_id, target_id)
    WHERE status = 'active';

CREATE INDEX idx_automation_target_sessions_session
    ON automation_target_sessions (org_id, session_id);
```

### `automation_runs`

```sql
ALTER TABLE automation_runs
    ADD COLUMN target_id uuid REFERENCES automation_targets(id),
    ADD COLUMN target_generation integer,
    ADD COLUMN session_id uuid REFERENCES sessions(id),
    ADD COLUMN thread_id uuid,
    ADD COLUMN turn_number integer,
    ADD COLUMN continuation_mode text
        CHECK (continuation_mode IN ('fresh', 'continued', 'reconstructed')),
    ADD COLUMN native_context boolean,
    ADD COLUMN previous_head_sha text,
    ADD COLUMN base_sha text,
    ADD COLUMN dispatch_state text
        CHECK (dispatch_state IN ('waiting', 'executing', 'done')),
    ADD COLUMN wait_reason text CHECK (wait_reason IN ('target_busy')),
    ADD COLUMN wait_started_at timestamptz,
    ADD COLUMN execution_started_at timestamptz,
    ADD COLUMN job_id uuid,
    ADD COLUMN attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    ADD COLUMN superseded_by_run_id uuid REFERENCES automation_runs(id),
    ADD COLUMN outcome_reason text CHECK (outcome_reason IN (
        'turn_completed', 'agent_failed', 'cancelled', 'awaiting_input', 'retries_exhausted',
        'stale_head', 'duplicate_head', 'superseded', 'wait_timeout', 'wait_overflow',
        'pr_closed', 'repository_unavailable')),
    ADD COLUMN worker_node_id text,
    ADD COLUMN restore_snapshot_bytes bigint,
    ADD COLUMN restore_duration_ms integer,
    ADD COLUMN turn_duration_ms integer,
    ADD COLUMN warm_hit boolean,
    ADD COLUMN warm_skipped_reason text CHECK (warm_skipped_reason IN (
        'node_budget', 'org_budget', 'automation_budget', 'disabled', 'snapshot_failed'));

CREATE UNIQUE INDEX idx_automation_runs_one_executing_per_target
    ON automation_runs (org_id, target_id)
    WHERE dispatch_state = 'executing';

CREATE INDEX idx_automation_runs_session
    ON automation_runs (org_id, session_id, triggered_at DESC)
    WHERE session_id IS NOT NULL;

CREATE INDEX idx_automation_runs_waiting_target
    ON automation_runs (org_id, target_id, triggered_at)
    WHERE dispatch_state = 'waiting';
```

`restore_snapshot_bytes`, `restore_duration_ms` (container create plus restore, or clone plus bootstrap for fresh runs), `turn_duration_ms` (agent start to agent exit), and `worker_node_id` are recorded for the first attempt of every run; retries overwrite them and increment `attempt`, and the Stage 3 gate uses first-attempt values only. `warm_hit` and `warm_skipped_reason` are Stage 3 columns. The `skipped` run status exists today and gains `outcome_reason` and `superseded_by_run_id` provenance.

Per-turn token usage rows gain `automation_run_id uuid` (nullable, indexed) for continued turns.

### Stuck-run reaper

`AutomationRunStore.ReapStuckRuns` changes to exclude `dispatch_state = 'waiting'`, to measure `executing` runs from `execution_started_at`, and to skip runs whose `job_id` is a running job with an unexpired lease or an active session executor. A separate `FailTimedOutWaits` sweep fails waiting runs older than two hours. Both remain per-org sweeps invoked by the scheduler.

### `session_sandbox_holders` (Stage 3)

```sql
ALTER TABLE session_sandbox_holders
    DROP CONSTRAINT chk_session_sandbox_holders_holder_kind,
    ADD CONSTRAINT chk_session_sandbox_holders_holder_kind CHECK (holder_kind IN (
        'thread_runtime', 'preview', 'snapshot', 'operator', 'automation_warm'
    )),
    ADD COLUMN usage_event_id uuid;
```

`models.SessionSandboxHolderKind` gains `automation_warm`.

### `container_usage_events` (Stage 3)

```sql
ALTER TABLE container_usage_events
    ADD COLUMN purpose text NOT NULL DEFAULT 'turn'
        CHECK (purpose IN ('turn', 'automation_warm'));
```

Historical rows default to `turn`. `usage_hourly_execution` rollups add `purpose` to their grouping. `RecordStop` gains a `stopped_at IS NULL` guard.

### Settings and configuration (Stage 3)

- Org settings JSON gains `automation_warm_sandbox_limit` (integer, default 5, range 0 to 100), updated through the existing `PATCH /api/v1/settings` route, which merges the nested `settings` object and performs an in-place update. There is no version conflict check on org settings today and this design does not add one.
- Worker config gains `WORKER_MAX_WARM_SANDBOXES` (integer; unset means the derived default above, `0` disables). Documented in the environment variables reference when Stage 3 ships.

### Store surface

New store methods, all taking `orgID` first: `AutomationTargetStore.LockOrCreate`, `GetActiveGeneration`, `RetireGeneration`, `InsertGeneration`, `AdvanceGeneration`; `AutomationRunStore.ReserveForExecution`, `MarkWaiting`, `SupersedeWaitingPush`, `CompleteTurn`, `NextWaiting`, `FailTimedOutWaits`; `SessionStore.ClaimForAutomationTurn`. The only cross-org method is the scheduler-invoked per-node warm sweep candidate query in Stage 3, which is host-local like the sandbox GC reference listing and carries `lint:allow-no-orgid reason="host-local container sweep"`.

`implemented/01-database-schema.md` is updated when each migration lands.

## API Contract

All routes are under `/api/v1/`, use the existing `{data, meta}` and `{error: {code, message, details}}` shapes, and follow `PaginationMeta`: `next_cursor` is omitted when there is no further page.

### Automation create and update

`POST /api/v1/automations` and `PATCH /api/v1/automations/{id}` accept:

```json
{
  "session_continuity": "per_run" | "per_target",
  "warm_sandbox_minutes": 0,
  "max_warm_targets": 3
}
```

Validation, returning 400 with code `INVALID_SESSION_CONTINUITY` and `details.field`:

- `per_target` requires at least one PR-scoped GitHub event trigger.
- `per_target` requires `publish_policy = "none"`.
- `warm_sandbox_minutes` requires `per_target` and must be 0 to 240 (Stage 3).
- `max_warm_targets` must be 0 to 50 (Stage 3).

The event-trigger mutation endpoints re-run the first rule when triggers change and return the same code. Switching to `per_run` retires every active generation for the automation with `continuity_disabled`. RBAC matches existing automation mutation rules. Responses include every field. Before Stage 3 ships, the warm fields are absent from requests and responses rather than accepted and ignored. Session automation tools (doc 111) do not accept the continuity fields in this version and preserve them on update.

### Organization settings (Stage 3)

`PATCH /api/v1/settings` accepts `settings.automation_warm_sandbox_limit` (integer, 0 to 100). Admin only; existing merge and in-place update semantics; 400 `INVALID_SETTINGS` on range violations.

### Run list and detail

`GET /api/v1/automations/{id}/runs` rows and the run detail response gain additive optional fields:

```json
{
  "session_id": "uuid",
  "thread_id": "uuid",
  "target_id": "uuid",
  "target_generation": 2,
  "continuation_mode": "fresh" | "continued" | "reconstructed",
  "native_context": true,
  "previous_head_sha": "abc123",
  "base_sha": "def456",
  "dispatch_state": "waiting" | "executing" | "done",
  "wait_reason": "target_busy",
  "outcome_reason": "turn_completed",
  "superseded_by_run_id": "uuid",
  "restore_duration_ms": 18400,
  "turn_duration_ms": 412000,
  "warm_hit": false,
  "warm_skipped_reason": "org_budget"
}
```

Status, outcome, timing, and usage come from the run row for per-target runs. `trigger_target` and `trigger_details` are unchanged. Warm fields appear only once Stage 3 ships.

### Target conversations

`GET /api/v1/automations/{id}/targets` (viewer or above). Query: `status` (`active` default, `retired`, `all`), `limit` (default 25, max 100), `cursor`. Ordered by `updated_at DESC, id DESC`; the cursor is opaque and encodes both.

```json
{
  "data": [{
    "id": "uuid",
    "target_id": "uuid",
    "repository_id": "uuid",
    "repository_full_name": "org/repo",
    "target_kind": "github_pull_request",
    "target_key": "1234",
    "pull_request_url": "https://github.com/org/repo/pull/1234",
    "session_id": "uuid",
    "generation": 2,
    "status": "active",
    "turn_count": 4,
    "last_reviewed_head_sha": "abc123",
    "last_turn_at": "2026-09-19T10:00:00Z",
    "retired_reason": null,
    "retired_at": null
  }],
  "meta": {}
}
```

`POST /api/v1/automations/{id}/targets/{target_id}/reset` (member or admin), empty body. Retires the active generation with `manual_reset`, releases any warm hold, writes an audit event, and returns 200 with `{data: <generation row as above>}`. 404 `NOT_FOUND` for an unknown target in the org; 409 `AUTOMATION_TARGET_NOT_ACTIVE` when no active generation exists. A running turn is not cancelled.

### Session detail

Session responses gain an optional `automation_target` object (`automation_id`, `automation_name`, `target_kind`, `target_key`, `pull_request_url`, `generation`, `turn_count`, `status`) when the session is a generation of a target. No SSE changes; existing run polling and session streams carry the new state.

### Internal and external APIs

No change to the external API in this version.

## Delivery Plan

### Stage 1 — Continuity with snapshot continuation and reconstruction

- Migrations, models, stores, and validation for the Stage 1 schema above.
- Ownership transaction, `ClaimForAutomationTurn`, run-scoped dispatch identity, waiting and superseding, executing invariant, retry recognition.
- Orchestrator `AutomationTurnContinueOptions`: fetch by SHA, base SHA, detached checkout, verification, clean tree, dependency fingerprint, delta, reconstruction, timing and node recording.
- `AutomationTurnCompleter` with fencing and idempotency; completion wiring in both worker handlers; next-waiter dispatch.
- Reaper changes: waiting exemption, execution clock, lease-aware skip, wait timeout.
- `OnPullRequestClosed` lifecycle notification and generation retirement.
- Prompt template with untrusted-data boundaries.
- Audit and adapt session-to-run consumers: run list and detail queries, Slack session notifications, PagerDuty writeback, session list ownership queries. `GoalImprovementService` and migration 000248 are unchanged.
- Settings UI fields, run badges, and `outcome_reason` in run details.
- Worker env kill switch `AUTOMATION_SESSION_CONTINUITY_DISABLED=1`.

Exit: a labelled PR with five pushes produces one session and five runs; the second and later runs skip clone and tool bootstrap; Claude Code and Codex turns resume with native context and record `native_context = true`; a destroyed snapshot reconstructs in the same session; an agent type change goes fresh; concurrent pushes coalesce to one follow-up turn; a follow-up dispatched while the previous job is still running is not lost; a run that waited 55 minutes is not reaped 5 minutes into execution.

### Stage 2 — Conversation surface

- Targets endpoints, conversation list on the automation page, reset action, session-page attribution.
- Metrics: continuation rate by mode, native-context rate, retire reasons, time to first agent event and tokens per run for fresh versus continued.

Exit: no automation with continuity enabled leaves a run without a session or a session without a generation row; reset works while a turn runs and the old turn's completion does not dispatch into the retired generation.

### Stage 3 gate — Is warm worth building?

Stage 3 is conditional. It is not scheduled until Stage 1 has run in production for at least two weeks on real labelled PRs and the numbers below justify it.

What Stage 3 can save is only the container create plus the snapshot download and untar. It cannot save clone, dependency install, the agent's cold read, or any tokens; Stage 1 already removes those. What Stage 3 costs is a live-container slot, host memory, and disk for the whole warm window on one node, plus node affinity.

Measurement contract, from Stage 1 columns:

- Cohort: runs with `continuation_mode = continued`, `attempt = 1`, `outcome_reason = turn_completed`, at least 200 runs across at least 5 automations.
- Restore share: median of `restore_duration_ms / turn_duration_ms` over the cohort.
- Checkpoint size: `restore_snapshot_bytes` distribution over the cohort (the session row's snapshot size is overwritten on each publication, so the run column is the durable record).
- Idle gap: for consecutive runs on the same generation, `completed_at` of turn N to `triggered_at` of run N+1. Warmth starts at completion, so this, not trigger-to-trigger, is the TTL simulation input. Compute the hit rate a 15, 30, and 60-minute TTL would have achieved.
- Placement: fraction of consecutive continued runs on the same generation with equal `worker_node_id`.

Build Stage 3 only if restore share exceeds 20%, the simulated 15-minute hit rate exceeds one third of continued runs, and same-node placement exceeds 50%. If restore is slow mainly because checkpoints are large, reduce checkpoint size first and re-measure. If the gate fails, Stage 3 stays parked here with the measured values recorded in the decision log. Token and cold-read savings are hypotheses until the Stage 2 fresh-versus-continued comparison confirms them, particularly for turns where native resume fell back.

### Stage 3 — Warm sandboxes (conditional on the gate)

- Holder kind, `usage_event_id`, atomic budget admission, `warm_skipped_reason`, `warm_hit`.
- Expiry sweep, holder-aware eviction, node affinity via `target_node_id`.
- Usage purpose column, interval transitions, `RecordStop` guard, rollup and usage API changes.
- Settings UI for warm minutes, warm targets, and the org limit.

Exit: a push within a 15-minute window reuses the container on the owning node and records `warm_hit = true`; a push after expiry restores from snapshot; each budget skips the hold with the right reason; eviction reclaims the oldest warm container before refusing a real turn and never destroys a container with an active turn; warm and turn usage intervals never overlap and each closes exactly once across reuse, expiry, eviction, and node loss.

## Validation and Acceptance

| Area | Tests |
|------|-------|
| Readiness and compatibility | Table-driven unit tests for every readiness state and compatibility row, including kill switch, `sandbox_state = none`, pending snapshot wait, and size limit |
| Ownership transaction | Real PostgreSQL: two workers reserve runs for one target; second waits; failure after claim rolls back the claim and message; executing invariant rejects a second reservation; retry recognizes its own reservation |
| Dispatch identity | Follow-up enqueue succeeds while the previous `continue_session` job is still `running`; a conflicting key with a different run fails the transaction |
| Waiting | Push supersedes older waiting push at arrival; non-push runs queue and each executes; waiting cap; wait timeout; stuck reaper ignores waiting runs and measures from execution start; lease-aware skip |
| Workspace | Fetch by SHA; unreachable SHA skips as `stale_head`; base SHA recorded; detached checkout verified; dirty tree discarded with bounded log; submodules synced; dependency fingerprint change re-runs tool bootstrap and flags the prompt; delta from last reviewed head; full review from base when unavailable |
| Reconstruction | Missing and destroyed snapshots rebuild in the same session; a live pending publisher is never raced; the reconstructed turn publishes a snapshot back onto the session |
| Completion | Success, failure, cancel, awaiting input, retries exhausted, and every skip reason produce the specified run status and reason; late callbacks from an earlier run or generation match zero rows; double completion is a no-op; `last_reviewed_head_sha` never moves backward or on failure; next waiter dispatched |
| Lifecycle | Reset during a running turn; disable; kill switch; merged with and without subscription; unmerged close via `OnPullRequestClosed`; reopen; agent config and identity changes |
| Prompt | Rendered snapshot tests for continued, reconstructed, no-history, same-head event, and dependency-changed cases; PR text appears only inside delimited untrusted blocks; SHA and PR number validation rejects malformed input |
| Run history | Run list and detail read run-local fields; historical rows read the origin link; token usage attributed by `automation_run_id` |
| Warm (Stage 3) | Atomic admission under concurrency; each budget's reason; `0` disables and unset derives; single-slot node gets 0; expiry sweep; eviction order and refusal to evict active turns; node-loss recovery; usage intervals disjoint and closed once |
| Frontend | Settings validation messages, run badges, targets list and pagination, reset confirmation, mobile layout |
| Tenancy | `lint-stores` and `lint-schema` pass; same-org validation rejects cross-org parents; cross-org target lookups return nothing |

Acceptance measurements before enabling by default for any template: median time from trigger to first agent event for continued runs versus fresh runs, tokens per continued run versus fresh run on the same PRs, native-context rate, and a sampled quality comparison of continued reviews against independent fresh reviews on the same head. The Stage 3 gate measurements are collected in the same window.

## Rollout, Migration, and Recovery

- Deploy the migration first; it is additive. Old workers ignore the new columns and keep creating fresh sessions because `session_continuity` defaults to `per_run`.
- Enable continuity per automation. There is no global default flip in this design.
- Rollback: set automations back to `per_run` (retires generations; waiters run as per-run sessions) or set the worker kill switch (forces fresh sessions; generations untouched). Keep the schema; the down migration is a disposable compatibility check only.
- Precedence: worker kill switch, then the automation row's current `session_continuity`, then the run's `config_snapshot` for audit only.
- A session executor or worker loss mid-turn recovers through the existing checkpoint path and the retry rules above.
- Stage 3, if built, rolls out with `WORKER_MAX_WARM_SANDBOXES` set explicitly per node and the org limit at its default. Setting the node value to `0` disables warm holds on that node without touching automations; existing warm containers drain through the expiry sweep.

## Risks

- **Anchoring on stale context.** A resumed agent may repeat or over-trust earlier findings. The continuation block frames the turn as a delta review and asks for explicit resolution of earlier findings. The turn limit and reset bound drift.
- **Instructions carried in PR content.** PR text and diffs are untrusted and persist in native provider context across turns. The template marks them as data on every turn, bounds their size, and validates git arguments. This reduces, not removes, the surface; per-target sessions carry no publication capability so the blast radius is a bad review.
- **Hidden 1:1 assumptions.** Several paths treat a session and its automation run as one pair. The Stage 1 audit list is the mitigation; tests cover each adapted consumer.
- **Workspace validity across turns.** A clean tracked tree with stale dependency caches can produce misleading tool output. The dependency fingerprint and the `unsupported_workspace` retirement bound this; project dependency installation stays with the agent, as today.
- **Warm containers crowd out turns.** An idle warm container occupies the same admission slot as a running turn and cannot be reclaimed by today's pressure GC. The per-node budget, the holder-aware eviction path, and the rule that a real turn always wins are the mitigations; Stage 3 does not ship without all three.
- **Node affinity for warm containers.** Continuation on the wrong node loses the warm benefit while still paying for it. The gate requires evidence of same-node placement before building, and `warm_hit` measures the benefit afterwards.
- **Checkpoint growth.** Session checkpoints keep `.git` and build caches. The size limit retires oversized generations; age and turn limits bound duration.

## Non-Goals

- Sharing a session across multiple pull requests or multiple automations.
- Continuity for non-PR triggers in this version.
- Cancelling an in-flight turn when a newer push arrives. Waiting and superseding are sufficient for review workloads; cancellation can follow the built-in reviewer's classification rules later.
- Reusing prior review results without running an agent. That is the built-in reviewer's scheduling design.
- Publishing pull requests from a per-target session.
- Repairing missed push webhooks.

## Decision Log

- **2026-09-19 — Session per target, run per trigger.** Runs keep their own rows, outcome, timing, and usage so the automation page stays truthful about what fired; the session is the memory. Rejected: one run per PR with turns nested inside it, because it breaks run idempotency, dedupe, and analytics keyed on runs.
- **2026-09-19 — Wait and supersede instead of cancel.** Review turns are short relative to push bursts, and cancellation requires the equivalence classification the built-in reviewer is still building.
- **2026-09-19 — Restrict to `publish_policy = none` and strip publication capabilities.** Keeps workspace correctness a one-line rule (reset to head) instead of a changeset-aware merge of prior turns with new pushes, and bounds what a prompt-injected turn can do.
- **2026-09-19 — Warm sandbox is a separate, conditional stage.** Snapshot restore already removes the clone, bootstrap, and cold-read cost, and warm saves no tokens. Stage 3 is gated on Stage 1 data rather than scheduled.
- **2026-09-19 — Warm holds are best-effort and budgeted.** Skipping a hold is free of correctness risk. Three budgets plus oldest-first eviction keep idle containers from displacing real turns. Rejected: an unbounded pool with TTL only, because the current pressure GC cannot reclaim held containers and a busy tenant would starve every other tenant on the node.
- **2026-09-19 — Run-scoped dispatch identity and an explicit completer.** Review round 1 showed the thread-scoped dedupe key silently drops a follow-up enqueued while the previous job is still running, and that successful continuations never reach the terminal-status hook. Both are now explicit contracts.
- **2026-09-19 — Separate target identity from generations.** Review round 1 showed a placeholder row cannot exist with a non-null `session_id`. `automation_targets` is the lock row; generations hang off it.
- **2026-09-19 — Push-only coalescing with head authority by reachability.** Review round 1 showed arrival order can supersede newer work and that comment and review events are distinct requests.

## Review History

- **Round 1 (2026-09-19, Codex gpt-6-astra, high effort).** Verdict: request changes. Five factual corrections (teardown wording, pressure GC scope, Amp and Pi native resume, no generic holder expiry sweep, org settings not versioned) and fifteen design findings (dispatch dedupe, atomic ownership, completion identity, reaper clocks, retirement transitions, event-specific coalescing, exact-SHA correctness on fresh runs, workspace validity, readiness state machine, run-local history, per-turn authority and injection surface, contract precision, warm admission and affinity, billing overlap, measurement contract). All folded into this revision.
