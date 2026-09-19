# Design: Automation Target Session Continuity

> **Status:** Partially Implemented | **Last reviewed:** 2026-09-19

> **Depends on:** [overall.md](overall.md), [48-automations-separation.md](implemented/48-automations-separation.md), [119-github-automation-trigger-context.md](implemented/119-github-automation-trigger-context.md), [76-pr-repair-session-continuity.md](implemented/76-pr-repair-session-continuity.md), [82-durable-session-executors.md](implemented/82-durable-session-executors.md), [88-shared-sandbox-thread-runtimes.md](implemented/88-shared-sandbox-thread-runtimes.md), [54-s3-session-snapshots.md](implemented/54-s3-session-snapshots.md), [102-agent-run-capabilities.md](implemented/102-agent-run-capabilities.md)
>
> **Related:** [code-review-scheduling-and-reuse.md](code-review-scheduling-and-reuse.md) (result reuse for the built-in reviewer, not session reuse), [116-automatic-pr-feedback-follow-through.md](future/116-automatic-pr-feedback-follow-through.md) (canonical-session continuation for 143-generated PRs)

## Summary

A GitHub-triggered automation that fires on every push to a labelled pull request should not pay for a fresh container, a fresh clone, and a cold agent on each push. It should continue one conversation per pull request: the same session, the same restored workspace, and, where the provider's native state survives, the same agent context, so each new turn reviews what changed since the last turn instead of starting over.

This design adds an opt-in **per-target session continuity** mode to automations. The target is the pull request. The first trigger creates a session. Later triggers for the same PR continue that session through the existing `continue_session` path, after moving the restored workspace to the exact head SHA the run is about. Runs still get their own `automation_runs` row, history, outcome, and usage; they execute inside a shared session that 143 owns for as long as the target is active.

Default behavior is unchanged: automations keep creating one session per run unless continuity is enabled.

The savings this design commits to come from Stage 1: no clone, no tool bootstrap, no cold read of the repository, and a delta review instead of a whole-PR review, in the typical case where dependency inputs are unchanged between pushes. Keeping containers warm between pushes (Stage 3) is not part of that commitment. It is a conditional stage that is built only if Stage 1 measurements show restore time is a meaningful share of turn time, and it ships with hard budgets because an idle warm container occupies the same node slot as a running turn.

## Problem

Today `triggerAutomation` creates an `automation_runs` row per accepted delivery and `newAutomationRunHandler` always creates a new session and enqueues `run_agent` ([github_events.go](../../../internal/services/automations/github_events.go), [handlers.go](../../../internal/worker/handlers.go)). Nothing links a run to a prior session for the same PR. Every push therefore repeats:

- sandbox creation, repository clone, tool bootstrap, and auth bootstrap
- a cold read of the repository and the design principles the goal references
- a review of the whole PR rather than the delta since the last review

The primitives to avoid this already exist at the session layer:

- `continue_session` claims a resumable session, restores its snapshot (workspace and agent state directories), and resumes the provider CLI natively when the captured agent session ID is present. Every adapter with `ResumeBySessionID` resume mode does this: Claude Code `--resume`, Codex `exec resume`, OpenCode `--session`, Amp `threads continue`, Pi `--session`. When the ID is missing, the orchestrator embeds bounded transcript history instead. When a native resume attempt fails, the orchestrator retries from the snapshot with embedded history when `shouldRetryResumeFromSnapshot` accepts the failure: a continuation with exit code 1, an empty summary, and no returned agent session ID, and then a provider-specific check. Codex and OpenCode match a missing-session error signature; Claude Code currently accepts any failure that passes the generic guards, so a generic startup or authentication failure also falls back.
- Separately, `CheckpointCapability` ([runtime.go](../../../internal/services/agent/runtime.go)) records whether a restored snapshot is expected to carry native provider state: `full_resume` for Codex, Claude Code, and OpenCode; `filesystem_resume` for Amp and Pi. It does not select the resume path; it describes what a durable checkpoint guarantees after container loss.
- At the end of a turn the container is destroyed as soon as the turn hold is released, unless a preview or an active `session_sandbox_holders` row holds it, and cleanup succeeds. There is no idle grace. `SANDBOX_GC_GRACE` applies only to orphaned containers that lost their database reference. Automation sessions never have a preview, so their containers are destroyed at turn end.
- On snapshot failure after a successful turn, the turn still completes and the session keeps its previous snapshot key ([orchestrator.go](../../../internal/services/agent/orchestrator.go), continuation step 9). Any design that stores per-turn head state must account for a completed turn whose checkpoint is the previous turn's.
- `session_sandbox_holders` lets any holder kind keep a sandbox alive under a lease.
- PR repair (doc 76) already continues one canonical session per PR with two workspace sources: snapshot continuation or PR-head reconstruction, verifying that checked-out `HEAD` equals the expected SHA.

Doc 116 notes the gap directly: "Generic automations create separate sessions and do not preserve complete multi-comment review or reply-thread state." This design closes that gap for automations without turning them into the built-in code reviewer.

## Principle

Borrowed from doc 76: **session continuity and workspace correctness are separate concerns.**

- The automation target session owns the conversation: the goal prompts, assistant replies, logs, and per-turn results for one PR.
- The workspace must match the head SHA the triggering run is about, on every turn including the first, regardless of whether the backend reused a live container, restored a snapshot, or rebuilt the checkout.

Two further rules keep the replacement protocols honest:

- **Durable evidence, not inference.** A run is complete only when a run-keyed result marker exists. Session status and timestamps are never used to infer that a run finished.
- **At-least-once execution with no 143-mediated writes.** The turn's tool allowlist contains no 143 tool that writes outside the sandbox, so re-executing a crashed attempt cannot replay a comment, notification, policy change, or publication. Shell and network effects inside the sandbox remain at-least-once; this design does not claim otherwise.

The run remains the unit of "what happened at this trigger" and carries its own outcome, reason, timing, and usage. The session is the unit of memory.

## Scope

In scope:

- GitHub triggers whose event identifies a pull request: `github.pull_request.opened`, `github.pull_request.updated`, `github.pull_request.ready_for_review`, `github.pull_request.merged`, `github.check_suite.completed`, `github.check_run.completed`, `github.issue_comment.created`, `github.pull_request_review.submitted`, `github.pull_request_review_comment.created`. `github.pull_request.updated` is emitted for the GitHub actions `synchronize`, `edited`, `converted_to_draft`, `reopened`, and `ready_for_review`; the underlying action is preserved on the run because only `synchronize` is a push.
- Automations whose `publish_policy` is `none`. Continuity in this version is for read-and-report automations such as design-principle reviews. `publish_policy = none` disables 143's automatic PR creation; it is not a read-only sandbox. Per-target turns therefore run with a **positive tool allowlist** rather than a subtractive one, because the capability filter has grants and bypasses that a subtractive list misses (`code_review_policy` grants `update_policy`; the `capability`, `automation-goal-improvement complete`, and `preview` namespaces bypass the allowlist entirely). The allowlist for per-target turns is exactly: `session-history search|get|messages`, `code-review-history list|get|policy`, `pr-history` reads, `linear list_tasks|get_task|find_related_tasks`, `pagerduty list_incidents|get_incident|list_notes|list_log_entries|get_service|list_oncalls|find_related_incidents`, `notion search_documents|get_document`, `slack search_messages|get_thread`, `logs query|context|fields|stats`, and `capability` self-inspection. `update_policy`, every `automation` action, `eval add`, `pr create`, `slack send`, `linear update_task|create_task`, `pagerduty add_note|create_status_update`, `automation-goal-improvement complete`, and all `preview` actions are denied. The filter is applied when the session's capability snapshot is built and enforced server-side by the internal API on every call; the preview capability is also omitted from the session-scoped token so the preview bypass is closed at the endpoint. The agent keeps shell and, subject to the organization's sandbox network settings, network access inside the sandbox. Sessions that publish PRs accumulate branch state across turns and need the changeset model; that is deferred.
- All coding adapters. Native resume is attempted for every adapter with a captured agent session ID; the run records whether native context was preserved.

Out of scope for this version:

- Schedule, manual, Linear, PagerDuty, and Slack triggers. They have no per-target identity today; a later revision may key Linear triggers on the issue.
- Human participation in an active generation's session. Per-target sessions are 143-owned while the generation is active (see Session Ownership). After retirement the session is an ordinary session again.
- Continuity across pull requests or across automations.
- Replacing or changing the built-in code reviewer and its scheduler.
- Merging transcripts, forking sessions, or exposing continuity to the external API.

## Product Behavior

### Settings

Automation settings gain a **Conversation** section:

- **Session continuity**: `New session per run` (default) or `Continue one session per pull request`.
- **Keep sandbox warm for** (Stage 3, only if built): `Off` (default), 15, 30, 60, or 120 minutes. Shown only when continuity is enabled. Explains that warm time counts toward container usage, is best-effort, and is capped per automation, per organization, and per worker node.
- **Warm targets** (Stage 3, only if built): how many pull requests this automation may keep warm at once. Default 3.

Enabling continuity requires at least one PR-scoped GitHub event trigger and `publish_policy = none`. The form explains both requirements inline and the API rejects violations. The settings page also states that the resulting sessions cannot be messaged by people while the PR conversation is active, and that the agent runs without external-write tools.

### First trigger for a PR

A run is created and a session starts, as today, with two differences: the workspace is checked out at the run's head SHA (see Workspace Preparation) instead of the automation's base branch, and the session is marked 143-owned. The backend records the session as the active generation for the target `(automation, repository, PR)`.

### Later triggers for the same PR

- The run row appears with a **Continued** badge and shows `previous head → head`.
- The session transcript gains one new user turn containing the automation goal, the GitHub event context, and a continuation block describing what changed since the baseline the agent's context actually covers.
- Logs, result summary, outcome, and token usage for the turn attach to the run; the transcript stays in the session.
- If the stored session cannot be continued safely, the run shows **Fresh** (a new generation started) or **Reconstructed** (same session, rebuilt workspace, native context lost). The cause is stored on the run as `continuation_reason`; the terminal result is stored separately as `outcome_reason`.

### Pushes and other events while a turn is running

At most one turn executes per target at a time, across generations. A run that arrives while the target is busy waits with reason **Waiting for the current turn**.

Waiting runs are not interchangeable:

- Only **push** runs (`github.pull_request.updated` with action `synchronize`) coalesce. A new push run supersedes any older waiting push run for the same target at arrival. At dispatch, a push run reviews the PR's **current** head as reported by GitHub, not the head it was delivered with, so a delayed delivery for an older head never replaces newer work (see Head Authority).
- **Every other event** (comments, reviews, checks, `edited`, `converted_to_draft`, `reopened`, `ready_for_review`, `labeled`, `merged`) queues in arrival order and each executes. A same-head run of these kinds receives an empty code delta plus the event text.
- At most 10 runs wait per target. Beyond that, a new run fails immediately with `wait_overflow`.

### Reset

The automation page lists target conversations (repository, PR, generation, turn count, last reviewed head, last activity). Members and admins can **Reset conversation** for a target, which retires the active generation so the next trigger starts a fresh session. Resetting does not delete the session or its history. A running turn finishes on the old generation; its completion is fenced by generation and does not touch the target's waiters. Reset itself requests a target wake, so waiting runs are re-evaluated against the new generation once the old turn releases the target.

## Session Ownership

While a generation is active, its session is **automation-owned**:

- `sessions.automation_owner_generation_id` is set. The thread service rejects human sends, new threads, and follow-up commands on the session with 409 `SESSION_AUTOMATION_OWNED` and a message pointing at the target's Reset action. Today human sends deliberately proceed when the session is already running and permit sibling threads; that policy is unchanged for ordinary sessions and simply does not apply to owned sessions.
- Session-level actions that mutate the workspace (previews, PR worktree materialization, split verification) are rejected the same way. Read-only views work.
- Generation retirement and workspace ownership release are separate. When no run is executing for the generation and the session holds no turn hold, retirement clears the owner marker in the same transaction. When a run is executing, retirement sets `ownership_release_pending = true` on the generation and leaves the marker in place; the executing run's completion (or its preflight skip) clears the marker after the turn hold has been released and the container is destroyed or held. Until then the session still rejects human mutation with a message that the current turn is finishing. Reset, disable, and close during execution all follow this path.
- If a per-target turn ends `awaiting_input` or `needs_human_guidance`, the turn has ended, so completion retires the generation with `awaiting_input` and clears the marker immediately; the run fails with `outcome_reason = awaiting_input` and a person can answer in the now-ordinary session. The next trigger starts a new generation.

Because no human turn can run on an owned session, the automation's session claim is the only claim that can hold the session, and the target lock plus the session claim together are an exclusive workspace reservation for the whole attempt, including cleanup.

## Continuation Decision

The run handler evaluates the target inside the ownership transaction described in Turn Ownership and Dispatch. The decision has three parts: readiness of the stored session, compatibility of the run with the session, and the resulting action.

### Readiness

Readiness is computed from the session row, its runtime fields, and the generation row, not from `sandbox_state` alone, because `sandbox_state = none` is a valid non-destroyed value that proves nothing.

| Readiness | Condition | Workspace source |
|-----------|-----------|------------------|
| `live` | `container_id` set, `worker_node_id` set, an active `automation_warm` or other holder for that container, no `pending_snapshot_key` | Reused live container on the owning node |
| `checkpoint` | `snapshot_key` set, `pending_snapshot_key` empty, last checkpoint kind is `turn_complete`, `graceful_stop`, or `bootstrap`, snapshot age within `SESSION_MAX_SNAPSHOT_AGE`, snapshot bytes within the checkpoint size limit | Snapshot restore |
| `pending` | `pending_snapshot_key` set | Retry the job with a 15 s delay for up to 3 minutes; then treat as `checkpoint` if a key was published, else `rebuild` only after the existing stranded-pending-key clearing has run (never race a live publisher) |
| `rebuild` | No usable checkpoint: missing or reaped snapshot, `sandbox_state = destroyed` with no snapshot, restore failure | PR-head reconstruction in the same session |

Native context is separate. `agent_session_id` present on the primary thread means native resume is attempted; absent means embedded history. If the native attempt fails and `shouldRetryResumeFromSnapshot` accepts the failure, the orchestrator's existing snapshot-retry path runs the turn with embedded history. Stage 1 tightens the Claude Code branch of that predicate to a missing-session signature so that generic startup or authentication failures fail the attempt instead of silently degrading context. Any other failure fails the attempt (`agent_failed`); the retry starts a new attempt, which cannot replay 143-mediated writes. The run records `native_context = false` whenever embedded history was used.

**Checkpoint coherence.** Provenance follows the checkpoint, not the review. For an owned session, `sessions.snapshot_key` may be replaced by exactly one writer, `PublishCheckpointWithProvenance`, which in one statement installs the snapshot key and checkpoint metadata on the session and writes `checkpoint_snapshot_key`, `checkpoint_head_sha`, `checkpoint_dependency_fingerprint`, and `checkpoint_review_complete` (false for any interrupted turn) on the generation. Every publication kind goes through it: `turn_complete`, `graceful_stop` on cancel, `worker_drain`, and `bootstrap`.

Today three other paths replace `snapshot_key` and would bypass that boundary: `UpdateTurnComplete` receives the new key even when checkpoint publication failed, the drain path calls `UpdateWorkspaceSnapshot` after publication, and pending-key promotion installs an uploaded key directly. For owned sessions these change: `UpdateTurnComplete` and `UpdateWorkspaceSnapshot` leave `snapshot_key` untouched (a SQL guard on `automation_owner_generation_id`), and the orchestrator's owned-session paths call the provenance writer first and pass its result, or the previous key on failure, to the status write. Pending-key promotion for owned sessions runs the provenance writer with the provenance captured at upload time, or, when none was captured, sets the generation's provenance to null in the same statement. A provenance write failure therefore leaves the previous key installed and the new blob orphaned for the snapshot reaper.

Provenance is bound to a key: readiness compares `sessions.snapshot_key` with the generation's `checkpoint_snapshot_key`, and a mismatch is treated exactly like null provenance. `last_reviewed_head_sha` is separate and advances only on a completed review.

Consequences for the next turn: the delta baseline for a native-resume turn is `checkpoint_head_sha`, because that is what the restored native context saw; when `checkpoint_review_complete` is false the continuation block says the turn at that head was interrupted and its review is incomplete; the summaries of completed runs between `checkpoint_head_sha` and `last_reviewed_head_sha` are embedded as data. For embedded-history turns and reconstruction the baseline is `last_reviewed_head_sha` with the same summaries embedded. A generation whose provenance is null or whose `checkpoint_snapshot_key` does not match the session's `snapshot_key` (a checkpoint published before this design, a failed provenance write, or a promotion without provenance) is treated as reconstructed: fingerprint absent, full-context note, delta from `last_reviewed_head_sha`.

### Compatibility

| Check | Source | Failure action |
|-------|--------|----------------|
| Automation has `session_continuity = per_target` and the run carries a PR target | run `config_snapshot.github` | fresh per-run session, no target row |
| Continuity kill switch not set on the worker | worker env | fresh per-run session; target rows untouched |
| Target lifecycle is `open`, or the event is `reopened`, or the event is `merged` while lifecycle is `merged` | `automation_targets.lifecycle_state` | skip (`pr_closed`) |
| Active generation exists | `automation_target_sessions` | fresh session, new generation |
| Session not archived or deleted | `sessions.archived_at`, `deleted_at` | retire (`session_unavailable`), fresh |
| Session status is `idle` or in `ResumableSessionStatuses` | `sessions.status` | `running`: wait; other: retire (`not_resumable`), fresh |
| Agent type, model override, and reasoning effort match the automation's current values | automation row vs session | retire (`agent_config_changed`), fresh |
| Identity scope and executing user unchanged | automation `identity_scope`, `created_by` vs session `triggered_by_user_id` | retire (`identity_changed`), fresh |
| Repository installation still active and the automation still authorized for the repository | repository and installation rows | run fails (`repository_unavailable`), no dispatch |
| Base branch unchanged since the last turn | run `config_snapshot.github.base_branch` vs `last_base_ref` | retire (`base_retargeted`), fresh |
| `turn_count < 25` | generation row | retire (`turn_limit`), fresh |
| Last checkpoint within the size limit (default 2 GB) | session snapshot bytes | retire (`snapshot_too_large`), fresh |

The capability snapshot is resolved per run through `ResolveForSession` and then filtered to the restricted per-target set. The continued turn executes with the run's snapshot; the session row's snapshot is updated to match inside the ownership transaction so credential and tool resolution during the turn sees the current grant. A narrowed grant therefore applies on the next turn without retiring the session.

Continuity mode is read from the automation row at dispatch time, not from the run's `config_snapshot`. The snapshot keeps the value for audit. The worker kill switch overrides both.

### Actions

- **continue**: readiness `live` or `checkpoint`; `continuation_mode = continued`.
- **reconstruct**: readiness `rebuild`; `continuation_mode = reconstructed`; `continuation_reason` records why (`snapshot_missing`, `restore_failed`, `sandbox_destroyed`).
- **fresh**: no active generation or a retire decision; `continuation_mode = fresh`; `continuation_reason` records why (`no_generation` or the retire reason); a new generation row is inserted with `generation + 1`.
- **wait**: another run is executing for the target, or the session claim fails; see Waiting and Head Authority.

Retirement never deletes anything. It sets `status = retired`, `retired_reason`, and `retired_at`, and releases session ownership now or marks it pending as defined in Session Ownership.

## Turn Ownership and Dispatch

This section defines the atomic unit that turns a pending run into an executing turn.

### Identity rows

- `automation_targets` is the identity, lifecycle, and lock row for `(org, automation, repository, target_kind, target_key)`. It exists whether or not a session exists yet, so an absent target can be created and locked without a placeholder session. It also carries `lifecycle_state` and the `wake_requested_at` outbox marker.
- `automation_target_sessions` holds one row per generation with a non-null `session_id`.

### Ownership transaction

One database transaction, lock order fixed:

1. Take the automation-scoped transaction advisory lock (`pg_advisory_xact_lock` keyed by org and automation), then upsert and `SELECT ... FOR UPDATE` the `automation_targets` row. The advisory lock exists because a target row inserted by an uncommitted transaction is invisible to row scans: the continuity switch to `per_run` takes the same lock before locking every target of the automation, so it either waits for an in-flight target creation to commit or finishes before that creation reads the automation row. Lock order is always advisory lock, then target rows.
2. Read the active generation, if any, and evaluate Compatibility. For push runs, resolve the current head (see Head Authority).
3. If another run is executing for the target (`automation_runs.dispatch_state = executing` for this `target_id`, enforced by a partial unique index), record the run as waiting (`dispatch_state = waiting`, `wait_reason = target_busy`, `wait_started_at = now`) and commit. Push runs supersede older waiting push runs here.
4. Create the session if the action is fresh. Claim the session and its primary thread with a new store method `ClaimForAutomationTurn(ctx, orgID, sessionID, threadID, allowDestroyed)`. It accepts `idle` plus `ResumableSessionStatuses`, applies the same runtime reset assignments as `ClaimForResume`, permits `sandbox_state = destroyed` only when the action is reconstruct, and claims the primary thread through the thread store's resume or fresh claim as appropriate. Existing `ClaimForResume` is not reused because it excludes `idle` and rejects destroyed sandboxes. On an owned session this claim cannot contend with a human turn.
5. Insert the visible user message on the primary thread.
6. Enqueue `continue_session` (or `run_agent` for fresh) with dedupe key `automation_turn:<run_id>` and obtain its job ID. If the enqueue reports a conflict, look up the existing pending or running job by that dedupe key in the same transaction; accept only if its payload carries this `run_id`, and use that job's ID. Otherwise fail the transaction.
7. Reserve the run in one `UPDATE`: `target_id`, `target_generation`, `session_id`, `job_id`, `continuation_mode`, `continuation_reason`, `previous_head_sha` (the delta baseline), `head_epoch`, `dispatch_state = executing`, `execution_started_at = now`. The executing CHECK requires `session_id`, `job_id`, and `execution_started_at` in the same statement, which is why the job is enqueued before the reservation; PostgreSQL evaluates CHECK constraints per statement, not at commit.
8. Commit. The job notify fires after commit.

Any failure rolls back every step, leaving the run `pending` for retry. Because the session is automation-owned, the target lock is the only ordering needed between turns.

### Dispatch identity and attempts

The dedupe key is run-scoped, not thread-scoped. The existing thread key `continue_session:<thread_id>` cannot be used: `EnqueueInTx` returns `uuid.Nil` on conflict with a pending or running job carrying the same key, and the previous turn's job is still running while its completion logic executes, so the next run's enqueue would be silently dropped.

An **attempt** is one execution of the job under one job lease. The worker issues a fresh UUID `lock_token` on every job claim; the claim installs it on the job row, and ownership loss resets the job to `pending` with the token cleared. Token inequality alone therefore proves nothing: a paused worker with an old token would still differ from a newer worker's token. The attempt claim validates ownership against the authoritative job row instead, in one transaction that serializes with reclaim because both lock the job row:

```sql
BEGIN;
SELECT 1 FROM jobs
 WHERE id = @job_id AND org_id = @org_id AND status = 'running'
   AND lock_token = @lock_token AND lease_expires_at > now()
 FOR UPDATE;                                  -- zero rows: not the owner; abort
UPDATE automation_runs
   SET attempt = attempt + 1, attempt_lock_token = @lock_token, attempt_started_at = now()
 WHERE id = @run_id AND org_id = @org_id AND dispatch_state = 'executing' AND job_id = @job_id;
COMMIT;
```

The lock token comes from the job context (`jobctx.LockTokenFromContext`). A worker whose job was reclaimed finds the job `pending` with no token, or `running` under another token, and cannot claim, even in the window before the next worker claims. Every later write for the attempt (result marker, preflight outcome, completion) is fenced twice: `automation_runs.attempt_lock_token = @lock_token`, and an `EXISTS` on the `jobs` row with `status = 'running' AND lock_token = @lock_token AND id = @job_id`, the same pattern `PublishCheckpoint` uses today. A worker that lost its lease can neither claim a new attempt nor write for an old one.

Recovery is authorized separately. A later attempt that finds a result marker for the run's current `attempt` may complete from it under its own live lease without claiming a new attempt. The scheduler's dead-letter path, which holds no lease, may record `retries_exhausted` only when the job row is terminal (`failed` or dead-lettered) and no marker exists.

### Invariants

Database constraints:

- At most one run with `dispatch_state = executing` per target across generations (partial unique index).
- `dispatch_state = executing` implies non-null `session_id`, `job_id`, and `execution_started_at` (CHECK, satisfied by the single reservation statement).
- A generation row is either active with null retirement fields or retired with both set (CHECK).

Transactional guarantees:

- A session that is the active generation for a target is claimed only through the ownership transaction while the generation is active.
- `last_reviewed_head_sha` advances only in a completion fenced by attempt token and generation, and only by a run with a non-null `head_epoch` at or above `last_reviewed_epoch`.

### Retry and recovery

The `continue_session` payload carries `automation_run_id` and `target_generation`. On any retry the handler re-reads the run:

- `dispatch_state = executing` with the same `job_id` and no result marker for the current attempt: perform a new attempt claim (validated against the job row) and re-enter the turn without re-running the ownership transaction. Session status is restored by the existing continuation recovery paths (`idle` and `snapshotted` on startup failure, drain requeue on worker drain). Re-execution replays no 143-mediated writes; shell and network effects are at-least-once.
- A result marker exists for the run's current attempt: the turn ended but completion was not recorded; complete from the marker under the retry's own live lease without claiming a new attempt. Completion is idempotent.
- Job dead-lettered: the scheduler records `failed` with `outcome_reason = retries_exhausted` only when the job row is terminal and no marker exists, releases the target, and requests a wake.

A retry never waits on its own reservation and never creates another session for the same run.

### Clocks and the stuck-run reaper

The scheduler's stuck-run reaper marks pending or running runs failed one hour after `triggered_at` (`stuckAutomationRunThreshold`). That is wrong for a run that waited legitimately. The reaper query changes to:

- exempt runs with `dispatch_state = waiting`;
- measure executing runs from `attempt_started_at`, and skip a run whose `job_id` has a live lease (running job with unexpired lease, or an active session executor row).

A separate wait timeout, `FailTimedOutWaits`, fails waiting runs two hours after `wait_started_at` with `outcome_reason = wait_timeout`. Its predicate excludes runs with `head_resolution = ambiguous`; those have exactly one deadline, `ResolveAmbiguityDeadline`, which restarts their wait clock when it converts them to `unresolved`. The two sweeps therefore never act on the same row: ambiguous rows belong to the ambiguity deadline, and every other waiting row, including unresolved candidates draining afterwards, belongs to the wait timeout measured from its restarted `wait_started_at`.

## Head Authority

Delivery order and `triggered_at` are unsafe for ordering pushes: a delayed redelivery for an older head can arrive after a newer push. GitHub's `pull_request.updated_at` is a server-side timestamp that advances on every push, so a **strictly** newer value orders deliveries for one PR regardless of arrival. It has one-second precision, so two pushes can share a value, and some payloads omit it; those cases are ambiguous and are never resolved by guessing. `PRService` forwards the timestamp on the trigger request as `PullRequestUpdatedAt`, the run stores it, and Stage 1 extends `PullRequestHead` and `GetPullRequestHead` to return `updated_at` so the dispatch-time lookup can resolve ties.

Head observation and event acceptance are separate. Observation decides what the target believes the current head is; acceptance decides whether a run executes. Only `synchronize` runs are ever skipped or superseded on head grounds; every other event executes.

- **Observation.** `automation_targets` keeps `observed_head_sha`, `observed_head_updated_at`, `head_epoch`, `head_resolution_pending`, and `head_resolution_deadline_at`. An epoch is assigned only to an **authoritative** head: a delivery whose `updated_at` is strictly newer than `observed_head_updated_at`, or a successful dispatch-time lookup. Any delivery that carries the PR head and its timestamp (`synchronize`, `opened`, `reopened`, `ready_for_review`) contributes to observation inside the target-locked arrival transaction: a strictly newer timestamp makes the target adopt the delivered head and increment `head_epoch`, and the arriving run is stamped `head_resolution = authoritative` with that epoch. A same-head delivery joins the observed epoch. Divergent force-pushed heads are ordered the same way, because the epoch, not ancestry, decides which is newer.
- **Acceptance for push runs** (`synchronize` only), in the same transaction:
  - authoritative (strictly newer): any older waiting push run is superseded;
  - strictly older timestamp with a different head: skipped at arrival as `stale_head`; it can never supersede newer work;
  - equal timestamp with a different head, or a missing timestamp: **ambiguous**. The run waits with `head_resolution = ambiguous`, a null epoch, and neither supersedes nor is superseded; the target sets `head_resolution_pending = true` and, if unset, `head_resolution_deadline_at = now + 30 minutes`.
- **Acceptance for every other run** (`opened`, `reopened`, `ready_for_review`, `edited`, `converted_to_draft`, `labeled`, `merged`, comments, reviews, checks): the run always queues FIFO and executes at its delivered head. Its epoch is the observed epoch when its head equals `observed_head_sha`, and null otherwise; a lifecycle delivery that was itself authoritative carries the epoch it created. A run delivered without a head is enriched at dispatch by the lookup, reviews the current head, and receives its epoch. Null epochs never advance the baseline, so a delayed `ready_for_review` at H1 after H2 was observed still executes but cannot move the baseline back, and neither can a comment on an older head that waits behind a newer push.
- **At dispatch** of a push run, the handler looks up the PR's current head and `updated_at` from GitHub with the installation token, as `PRService` already does for `issue_comment` enrichment. On success: a head newer than observed (a missed webhook) is adopted with a new epoch and the run reviews it; if ambiguous candidates exist, the candidate whose head equals the current head is stamped authoritative with the epoch and the other ambiguous push runs are superseded, and `ResolveAmbiguousHeads` clears both `head_resolution_pending` and `head_resolution_deadline_at` in the same target-locked statement, as does any transition that leaves no ambiguous candidate on the target, so a later tie always receives a fresh 30-minute window. On failure with no ambiguity pending: the run reviews its delivered head, which is safe because the surviving push run carries the newest strictly-ordered head, and the run sets `head_lookup_degraded`. On failure with ambiguity pending: the run is not executed; the job retries with backoff (30 s doubling to 10 min), and `ReconcileTargetWakes` retries the lookup for targets with `head_resolution_pending`.
- **Ambiguity deadline.** Ambiguous candidates are never failed by the ordinary wait sweep. When `head_resolution_deadline_at` passes unresolved, one target-locked transition, `ResolveAmbiguityDeadline`, marks every ambiguous candidate `head_resolution = unresolved` with a null epoch, restarts each candidate's `wait_started_at`, clears `head_resolution_pending` and the deadline, and writes the wake outbox. From then on the candidates are ordinary dispatchable runs: an authoritative push run that was blocked behind the ambiguity dispatches first, then the unresolved candidates in arrival order, each at its own delivered head. Nothing is discarded; the cost is at most one redundant review per tie. A lookup that succeeds before the deadline resolves the tie instead and no candidate becomes unresolved.
- **Baseline monotonicity.** Completion advances `last_reviewed_head_sha` and `last_reviewed_epoch` only when the run's `head_epoch` is non-null and greater than or equal to `last_reviewed_epoch`. A run's head is by construction the head of its epoch. A stale delivery, an unresolved tie, or an off-head comment can never advance the baseline; an authoritative force-push review always can, even when the heads share no ancestry.
- **Delta across a force-push.** When the baseline is not an ancestor of the new head, the delta is computed from `base_sha` and the continuation block states that history was rewritten since the baseline, embedding the previous run summaries as data.
- **Non-push runs** keep their delivered head. A same-head non-push run executes with an empty code delta.
- **Duplicates.** A push run whose head equals `last_reviewed_head_sha` is skipped as `duplicate_head`. No other event kind is deduplicated by head.
- **Base retarget** is detected at dispatch from the resolved base ref, not from the `edited` action, and retires the generation.

## Workspace Preparation

Continuation reuses `ContinueSession` with a new `AutomationTurnContinueOptions` value alongside `PRRepair` and `PRFeedback`. The same preparation applies to fresh first turns, which today clone the base branch and create a working branch without any PR-head checkout.

### Checkout at the exact head

For every automation turn in per-target mode:

1. Fetch the run's head commit by SHA: `git fetch origin <head_sha>` (GitHub serves reachable commits by SHA), falling back to `refs/pull/<n>/head` only to populate the local ref. `head_sha` is validated as 40 lowercase hex characters and the PR number as an integer before either is passed to git; nothing from GitHub is interpolated raw.
2. If the SHA is unreachable, the run is skipped with `outcome_reason = stale_head` through the executing-preflight path in Completion, because the reservation has already committed. For a push run this means the PR was force-pushed past the newest delivered head before dispatch; the newer push has its own run.
3. Fetch the base ref and record `base_sha = git merge-base origin/<base_ref> <head_sha>` on the run. This is the baseline for a full review when no previous head is usable.
4. Check out detached: `git checkout --detach <head_sha>`. Per-target sessions do not maintain a working branch; `sessions.working_branch` is set to `pr/<n>` for display only, and diff collection is disabled for these sessions because `publish_policy = none` makes a session diff meaningless.
5. Verify `git rev-parse HEAD == head_sha`; a mismatch fails the turn before the agent starts.

### Clean tree and dependency inputs

Before checkout: `git reset --hard && git clean -fd` (not `-x`, so ignored dependency and build caches survive). The discarded path list is bounded to 50 entries, sanitized, and logged. When `.gitmodules` exists, `git submodule update --init --recursive` runs after checkout. Nested repositories and Git LFS are unsupported in this version; a target whose checkout contains either is retired with `unsupported_workspace`.

A clean tracked tree does not make the workspace runnable. The turn computes a **dependency input fingerprint**, a heuristic hash over manifests and lockfiles present at the head (`package.json`, `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`, `go.mod`, `go.sum`, `pyproject.toml`, `requirements*.txt`, `Gemfile`, `Gemfile.lock`, `Cargo.toml`, `Cargo.lock`), toolchain files (`.tool-versions`, `.nvmrc`, `mise.toml`), the repo-config sandbox dependency declarations, and the sandbox image digest. The fingerprint is stored on the generation **together with the checkpoint it applies to** (`checkpoint_dependency_fingerprint`) and is meaningful only for a workspace restored from that checkpoint or reused live:

- Restored or live workspace with an equal fingerprint: no tool bootstrap; the prompt says dependency inputs are unchanged since the last checkpoint.
- Restored or live workspace with a different fingerprint, or any reconstructed workspace (fingerprint treated as absent): `sandboxdeps.Apply` re-runs for declared tools and the prompt says the environment may be cold and names the changed inputs. Project dependency installation remains the agent's responsibility, as it is for fresh sessions today.

The "no install" saving in the Summary is therefore typical, not guaranteed.

### Delta

Compute the continuation delta from the baseline chosen in Checkpoint coherence to `head_sha`: `git diff --stat` and the changed-file list, bounded to 200 files and 4 KB of stat output. Embed the result summaries of successful runs between the baseline and `last_reviewed_head_sha` as data. When the baseline is null or unreachable, compute from `base_sha` and ask for a full review.

### Snapshot continuation or live container

Readiness `checkpoint` restores the snapshot into a new container (or `live` reuses the held container on the owning node), then runs the checkout steps above. The container restore path is unchanged.

### PR-head reconstruction

Readiness `rebuild` creates a fresh sandbox, clones, runs the checkout steps, and runs the turn in the same session with bounded transcript context (initial goal, latest assistant summary, last two turns) because native state is gone. This reuses the PR repair reconstruction helper and the bounded-context builder, with the SHA-by-fetch change above. The turn's final snapshot attaches to the session so the next trigger can use snapshot continuation. Reconstruction is delivered in Stage 1 because the decision table needs it.

## Prompt

Add `internal/prompts/templates/automation_turn.template` with an exported render function. The visible user message on the primary thread is:

1. The run's `goal_snapshot` (goal plus GitHub event context, as today).
2. A **Continuation context** block: turn number, baseline SHA or "none", `head_sha`, `base_sha`, base branch, the bounded diff stat and changed-file list, embedded summaries of intervening successful runs, whether native agent context was preserved, and whether dependency inputs changed or the environment is cold.
3. For non-push events at the same head: the event text and an instruction to respond to the event without re-reviewing unchanged code.
4. Instructions: review the changes since the baseline against the goal; do not repeat findings for unchanged code unless the change invalidates them; state which earlier findings the new push resolved; when history is unavailable, review the full PR from `base_sha`.

PR-derived text (title, body, comment bodies, review text, file paths, diff content) and prior run summaries are placed in delimited data blocks that the template marks as untrusted content to be analyzed, never followed. Each block is size-bounded. Native provider context may already contain such text from earlier turns; the per-turn template repeats the boundary instruction so it is present in every turn's instructions.

## Waiting and Coalescing

- **Push runs** (`synchronize`): a push run with a strictly newer epoch supersedes any older waiting push run for the same target at arrival, inside the arrival transaction: the older run transitions to `skipped` with `outcome_reason = superseded` and `superseded_by_run_id` set. A push run with an older epoch is skipped at arrival as `stale_head`. Ambiguous push runs neither supersede nor are superseded until dispatch resolves them. The surviving authoritative push run therefore always carries the newest strictly-ordered head.
- **All other runs**, including `opened`, `reopened`, and `ready_for_review`, queue in arrival order and are never superseded or skipped on head grounds.
- **Dispatch order** when the target frees: the waiting push run first, then the others in arrival order. Each dispatch runs the ownership transaction.
- **Waiting cap.** At most 10 waiting runs per target; further arrivals fail with `outcome_reason = wait_overflow`.
- **Terminalizing a waiting run** (superseded, `wait_timeout`, `wait_overflow`, `pr_closed`, `duplicate_head`, `stale_head`) uses a separate CAS, `TerminalizeUnstarted`, whose predicate is `status = 'pending' AND (dispatch_state = 'waiting' OR dispatch_state IS NULL)`. It sets `dispatch_state = done`. The executing-run completer never touches these rows.

## Target Wake-up

Dispatching the next waiter is not left to whichever process happens to finish a turn.

- **Outbox.** Every transaction that frees or changes a target (turn completion, run terminalization, reset, disable, close or merge, dead-lettering) sets `automation_targets.wake_requested_at = now()` and, in the same transaction, enqueues an `automation_target_wake` job with dedupe key `automation_target_wake:<target_id>`. The job handler runs the ownership transaction for the next waiter and clears `wake_requested_at` only if no newer request arrived.
- **Reconciliation.** A per-org scheduler sweep, every minute, enqueues a wake for any target that has waiting runs, no executing run, and either `wake_requested_at` older than two minutes or no pending wake job. This covers a crash between completion and wake, a lost job, and a wake consumed while the target was still busy.
- **Human turns** cannot block a waiter because owned sessions accept none.

## Completion

Successful `ContinueSession` turns end with the session `idle` and do not call `AutomationHooks.OnSessionComplete`, which only reacts to terminal session statuses. Continued runs therefore need their own completion path. The legacy hook also marks a fresh no-diff run `completed_noop`; it returns early for any session that carries an automation owner marker or a generation row, so per-target runs are completed only by the path below.

### Result marker

The orchestrator writes a run-keyed result marker for **every attempt end** (completed, failed, cancelled, awaiting input) in the **same database transaction** as the session status write for that end (`UpdateTurnComplete`, the cancelled-session turn completion, or the failure status write), fenced by the attempt lock token and the job-row `EXISTS`:

`automation_run_results (run_id PK, org_id, attempt, attempt_lock_token, thread_id, turn_number, outcome, review_complete, checkpoint_key, checkpoint_published, checkpoint_head_sha, native_context, dependency_fingerprint, agent_session_id, recorded_at)`

`checkpoint_published` is true only when `PublishCheckpoint` for a snapshot taken at this attempt's end succeeded, including the `graceful_stop` checkpoint the cancel path publishes; a snapshot or publication failure leaves it false and `checkpoint_key` null while `outcome` can still be `turn_completed`. `review_complete` is true only for `turn_completed`. Warm admission (Stage 3) requires `checkpoint_published = true`.

### Executing preflight outcomes

Some outcomes are discovered after the reservation committed but before the agent starts: an unreachable head (`stale_head`), a lifecycle change since reservation (`pr_closed`), or a failed repository authorization (`repository_unavailable`). These runs are `executing`, so `TerminalizeUnstarted` must not touch them. A fenced `CompleteExecutingPreflight(run_id, attempt_lock_token, outcome)` sets `status` (`skipped` for `stale_head` and `pr_closed`, `failed` for `repository_unavailable`), `dispatch_state = done`, releases the session and thread claims back to `idle` without advancing turn counters, deletes the user message inserted at reservation because no assistant turn happened, writes the wake outbox, and applies any pending ownership release. No result marker is written for a preflight outcome.

### Automation turn completer

`AutomationTurnCompleter.Complete(run_id, attempt_lock_token)` is called by the worker handlers after the orchestrator returns, and by the retry path when a marker exists. It reads the marker and applies:

| Marker outcome | Run status | `outcome_reason` | Advances `last_reviewed_head_sha` | Checkpoint provenance |
|----------------|------------|------------------|------------------------------------|-----------------------|
| turn completed | `completed` | `turn_completed` | yes, subject to epoch monotonicity | already written at publication |
| agent failed or exited non-zero | `failed` | `agent_failed` | no | already written at publication, if any |
| cancelled | `failed` | `cancelled` | no | already written at publication (`review_complete = false`) |
| awaiting human input | `failed` | `awaiting_input`; generation retired and ownership released | no | already written at publication |
| retries exhausted (no marker, job dead-lettered) | `failed` | `retries_exhausted` | no | unchanged |

Checkpoint provenance is never written by the completer. It is written by the `PublishCheckpoint` wrapper at publication time (see Checkpoint coherence), so a cancelled or drained turn that replaced the snapshot has already replaced the provenance by the time the completer runs.

For per-target runs, `completed_noop` is not derived from the session diff. A report-only review that finished is `completed`.

### Fencing and idempotency

Completion updates the run only where `id = run_id AND dispatch_state = executing AND attempt_lock_token = @token`, with the job-row `EXISTS` fence, and updates the generation only where `generation = run.target_generation`; baseline fields are updated only when the generation is still active, while `turn_count` and `last_run_id` are recorded for a retired generation too. A late callback from an earlier attempt or generation matches zero rows and is logged. Calling the completer twice for the same attempt is a no-op after the first success. The completion transaction also sets `dispatch_state = done`, clears the session owner marker when `ownership_release_pending` is set, writes the wake outbox, and enqueues the wake job.

### Order of operations at turn end

1. Orchestrator attempts the end-of-attempt snapshot and publishes it through `PublishCheckpointWithProvenance`, which installs the key and the provenance together; on failure the previous key stays installed.
2. Orchestrator writes the result marker in the same transaction as the session status write for the attempt end.
3. Stage 3 only: warm hold admission and the usage-event swap, inside the orchestrator's end-of-turn path via `AutomationTurnHooks.BeforeTurnHoldRelease`, only if `checkpoint_published`.
4. Orchestrator releases the turn hold and destroys or keeps the container (existing).
5. Worker handler calls the completer: run status, `outcome_reason`, durations, `native_context`, generation counters (`turn_count`, `last_attempted_head_sha`, `last_reviewed_head_sha`, `last_reviewed_epoch`, `last_run_id`, `last_turn_at`), pending ownership release, wake outbox and job.

### Run-local history

Run list and detail responses read status, outcome, timing, and usage from the run row, never from the shared session's current state. Continued runs store `thread_id` and `turn_number` so messages and logs for the turn can be addressed. Per-turn token usage lives on `session_messages.token_usage`; the assistant message for a per-target turn gains `session_messages.automation_run_id`, and usage rollups attribute by it. Historical per-run rows keep reading through the origin link.

`session_automation_links` keeps its meaning (the run that created the session); `automation_runs.session_id` is the executing session. Slack session notifications and the PagerDuty writeback receive the executing run's ID. `GoalImprovementService.OnSessionComplete` handles dedicated goal-improvement sessions and is unaffected. Migration 000248 is historical repair SQL and is left unchanged.

## Lifecycle Transitions

`automation_targets.lifecycle_state` is `open`, `closed`, or `merged`, set by an internal notification `OnPullRequestClosed(repository, number, merged)` that `PRService` emits from its `closed` branch regardless of automation subscriptions (an unmerged close produces no automation event today), and reset to `open` by a `reopened` delivery. Before creating a new generation for a target whose state is unknown or stale (older than one hour), dispatch revalidates PR openness through the GitHub API; a closed PR results in `pr_closed`.

| Event | Executing turn | Waiting runs | Warm hold (Stage 3) | Generation |
|-------|----------------|--------------|---------------------|------------|
| Reset (manual) | finishes on old generation; completion fenced; wake requested | re-evaluated against the new generation on wake; execute fresh | released | retired `manual_reset`; ownership released now if idle, else pending until the turn's completion; next dispatch creates `generation + 1` |
| Continuity set to `per_run` | finishes | dispatched as ordinary per-run sessions | released | all active generations retired `continuity_disabled`; ownership released now or pending as above |
| Worker kill switch | finishes | dispatched as ordinary per-run sessions | not created; existing holds expire | untouched, so re-enabling continues where it left off |
| PR merged | finishes; a subscribed `merged` run is dispatched next and executes as the final turn; other waiters skipped `pr_closed` | skipped `pr_closed` except the `merged` run | released after the final turn | retired `pr_merged` after the final turn, or immediately when no `merged` run exists |
| PR closed without merge | finishes | skipped `pr_closed` | released | retired `pr_closed`; ownership released now or pending as above |
| PR reopened | n/a | n/a | n/a | lifecycle back to `open`; next trigger creates a new generation; the retired row stays |
| Late event after close (not `reopened`) | n/a | skipped `pr_closed` at arrival | n/a | no new generation |
| Awaiting input | ends the turn | wake requested; execute fresh | released | retired `awaiting_input`; ownership released |
| Agent, model, effort, identity change | finishes | first dispatch retires and goes fresh | released on retire | retired with the matching reason |
| Base retarget | finishes | first dispatch retires and goes fresh | released on retire | retired `base_retargeted` |

Callbacks from a retired generation are fenced by `target_generation`. Every row above that frees the target writes the wake outbox.

## Warm Sandbox (Stage 3, conditional)

If Stage 3 is built, the end-of-turn path attempts a **best-effort warm hold** only after the turn's checkpoint publication is confirmed: a `session_sandbox_holders` row with `holder_kind = automation_warm`, `holder_id = <generation row id>`, `expires_at = now + warm_sandbox_minutes`, `usage_event_id` set to the warm usage event opened in the same step, and `cleanup_state = none`. The existing destroy decision consults active holders, so the container survives while the holder is active. Skipping the hold never affects correctness.

### Budgets and atomic admission

| Budget | Where | Default | Rationale |
|--------|-------|---------|-----------|
| Per worker node | `WORKER_MAX_WARM_SANDBOXES` env | unset: `min(25% of effective WORKER_MAX_ACTIVE_SANDBOXES, WORKER_MAX_ACTIVE_SANDBOXES - 1)`; `0`: warm disabled on the node | Warm containers count against the live-container admission gate, so turns must keep guaranteed headroom; single-slot nodes get 0 |
| Per organization | org setting `automation_warm_sandbox_limit` | 5 | One tenant cannot fill a node with idle containers |
| Per automation | `automations.max_warm_targets` | 3 | A busy automation with many open PRs does not keep one container per PR |

Locking a generation row cannot serialize admissions from different generations, and the capacity gate's mutex is process-local (session executors build their own gate instance), so admission is serialized in the database. The admission transaction takes three transaction-scoped advisory locks in fixed order, node then org then automation (`pg_advisory_xact_lock(hashtext('warm:node:' || node_id))` and the org and automation equivalents), counts active `automation_warm` holders by `owner_node_id`, by `org_id`, and by automation (joined through the generation row), and inserts the holder only if all three counts are below their limits. Release, expiry, and eviction take the same locks in the same order. Rejection order is node, org, automation, and the reason is recorded as `warm_skipped_reason`. Reset, disable, and retirement release holds.

### Expiry, eviction, and cleanup recovery

No existing sweep expires generic holders and destroys containers; runtime reclaim only expires `thread_runtime` holders of lost runtimes. Stage 3 adds a per-node warm reconciler in the worker process (not the executor, which exits after the turn), running at the sandbox GC interval:

- **Heartbeat.** The reconciler heartbeats every active `automation_warm` holder it owns every 30 seconds by updating `heartbeat_at`.
- **Expiry.** For each owned holder past `expires_at`: CAS `status = expired` with the lease token, then run the cleanup state machine.
- **Cleanup state machine** on the holder row. The holder's own `container_id` is never cleared; only the session's reference is. Steps, each idempotent and resumed from the persisted state on the next pass:
  1. `destroy_authorized`: one transaction runs the session-reference CAS (`FinalizeContainerDestroy` semantics: clear `sessions.container_id` only if it still equals the holder's container and no other active holder or turn hold exists) **and** sets the holder to `destroy_authorized` with `destroy_authorized_at`. Because both writes commit together, a crash cannot leave the reference cleared without the authorization recorded. A false CAS is disambiguated inside the same transaction: if `sessions.container_id` still equals the holder's container, another holder or turn blocks destruction and the holder returns to `expired` for a later pass; if it no longer equals, the reference was cleared by another actor (a reconciler or an operator); the holder records `cleanup_note = reference_cleared_elsewhere` and continues to the `destroyed` step with its retained container identity, because a missing reference proves neither destruction nor billing closure. An alive container is destroyed by this reconciler (nothing else references it); an absent one records an estimated `destroyed_at = now()`. Usage is then closed at that time, and only then is the holder `done`. `done` is reachable only through `usage_closed`.
  2. `destroyed`: `provider.Destroy` on the holder's container; an already-missing container is success. `destroyed_at` records the provider's acknowledgement time. If the provider returns an ambiguous error, the step retries; after five failures it probes `IsAlive` and, if the container is gone, records `destroyed_at = now()` with `cleanup_note = destruction_time_estimated`.
  3. `usage_closed`: close the warm usage event at `destroyed_at`, accepting an already-closed matching event.
  4. `done`.
- **Eviction.** Pressure GC today reclaims unreferenced containers and, past the 24-hour hard max, referenced containers with no active holder; it cannot preempt a valid warm hold. When admission for a real turn would fail, the capacity gate calls the reconciler's evict path: select the oldest owned `automation_warm` holder by `last_turn_at` whose container has no other active holder, expire it, run the state machine to `destroyed`, recount, and repeat up to the pressure destroy limit. If admission still fails, the turn takes the ordinary admission-failure retry. A container with an active turn is never evicted.
- **Node loss.** The scheduler marks holders whose `heartbeat_at` is older than two minutes and whose node has no live worker heartbeat: holder `status = expired`, `cleanup_state = orphaned`, holder `container_id` retained. It clears the session's reference with the expected-ID guard, closes the warm usage event at the last `heartbeat_at` (an explicit estimation policy, since actual destruction time is unknown), and sets `cleanup_note = node_lost`. `orphaned` is terminal for the scheduler; when the node returns, its reconciler destroys any orphaned holder's container it still finds and moves the holder to `done`. Otherwise the hard-max sweep reaps the container.

### Node affinity

A warm continuation is enqueued with `jobs.target_node_id` set to the owning node. Wrong-node continuation today defers to the owner rather than restoring beside a live container, so affinity is kept rather than transferred. If the owner is dead, the dead-node path clears the container reference and the run restores from snapshot elsewhere, recording `warm_hit = false`.

### Billing

Interval transitions share one timestamp and are idempotent:

- Turn end with warm admitted: close the turn usage event at `T` and open the warm event at `T` for the same container, in the `BeforeTurnHoldRelease` step, before the deferred usage stop runs.
- Deferred `ContainerStopped` then runs. Today it deletes the sampler entry by container ID and calls `RecordStop`, which treats zero updated rows as an error. Both change: the sampler entry is removed only if its event ID matches the event being stopped, and `RecordStop` returns success for an event that is already closed with the same ID (`stopped_at IS NOT NULL`), while still failing for an unknown ID.
- Reuse: at continuation start, close the warm event at `T2` and open the turn event at `T2`; the holder transitions to `released` and its cleanup state to `done` without destruction.
- Expiry or eviction: the state machine closes the warm event at `destroyed_at`, which is the provider's acknowledged destruction time or the recorded estimate. Node loss uses the last heartbeat as described above.

The usage sampler tracks one active event per container; transitions swap the active event atomically under the tracker's lock. Rollups group by `purpose`, the usage API reports `warm_container_minutes` separately, and peak concurrency counts containers, not events.

## Database Contract

Migrations are additive. All tenant tables carry `org_id uuid NOT NULL REFERENCES organizations(id)`; every store method takes `orgID` and filters by it; inserts validate that the referenced automation, repository, session, and run belong to the same `org_id` before writing, because UUID foreign keys alone do not enforce same-org relationships. `updated_at` is maintained by the store on every update, as elsewhere in the repo.

Foreign-key and retention decisions: `session_id` references `sessions(id)` (sessions are soft-deleted; no cascade). `thread_id` and `job_id` are plain UUIDs with no FK because thread rows cascade with sessions and job rows are pruned. `usage_event_id` on holders is a plain UUID because usage events are pruned by retention. `superseded_by_run_id` and `last_run_id` reference `automation_runs(id)`.

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

### `sessions`

```sql
ALTER TABLE sessions
    ADD COLUMN automation_owner_generation_id uuid;
CREATE INDEX idx_sessions_automation_owner
    ON sessions (org_id, automation_owner_generation_id)
    WHERE automation_owner_generation_id IS NOT NULL;
```

No FK, to avoid a cycle with `automation_target_sessions`; the generation row's `session_id` is the authoritative link. The marker is cleared by the retirement transaction when the session is idle, or by the executing run's completion or preflight skip when `ownership_release_pending` is set.

### `automation_targets`

```sql
CREATE TABLE automation_targets (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              uuid NOT NULL REFERENCES organizations(id),
    automation_id       uuid NOT NULL REFERENCES automations(id),
    repository_id       uuid NOT NULL REFERENCES repositories(id),
    target_kind         text NOT NULL CHECK (target_kind IN ('github_pull_request')),
    target_key          text NOT NULL CHECK (length(target_key) BETWEEN 1 AND 64),
    active_generation   integer NOT NULL DEFAULT 0 CHECK (active_generation >= 0),
    lifecycle_state     text NOT NULL DEFAULT 'open'
                        CHECK (lifecycle_state IN ('open', 'closed', 'merged')),
    lifecycle_updated_at timestamptz,
    observed_head_sha   text,
    observed_head_updated_at timestamptz,
    head_epoch          integer NOT NULL DEFAULT 0 CHECK (head_epoch >= 0),
    head_resolution_pending boolean NOT NULL DEFAULT false,
    head_resolution_deadline_at timestamptz,
    wake_requested_at   timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, automation_id, repository_id, target_kind, target_key)
);

CREATE INDEX idx_automation_targets_wake
    ON automation_targets (org_id, wake_requested_at)
    WHERE wake_requested_at IS NOT NULL;
```

This is the lock row. `active_generation = 0` means no session exists yet.

### `automation_target_sessions`

```sql
CREATE TABLE automation_target_sessions (
    id                              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                          uuid NOT NULL REFERENCES organizations(id),
    target_id                       uuid NOT NULL REFERENCES automation_targets(id),
    generation                      integer NOT NULL CHECK (generation >= 1),
    session_id                      uuid NOT NULL REFERENCES sessions(id),
    status                          text NOT NULL DEFAULT 'active'
                                    CHECK (status IN ('active', 'retired')),
    retired_reason                  text CHECK (retired_reason IN (
                                        'manual_reset', 'pr_closed', 'pr_merged', 'session_unavailable',
                                        'not_resumable', 'agent_config_changed', 'identity_changed',
                                        'base_retargeted', 'turn_limit', 'snapshot_too_large',
                                        'unsupported_workspace', 'awaiting_input', 'continuity_disabled')),
    retired_at                      timestamptz,
    turn_count                      integer NOT NULL DEFAULT 0 CHECK (turn_count >= 0),
    ownership_release_pending       boolean NOT NULL DEFAULT false,
    last_attempted_head_sha         text,
    last_reviewed_head_sha          text,
    last_reviewed_epoch             integer NOT NULL DEFAULT 0 CHECK (last_reviewed_epoch >= 0),
    checkpoint_snapshot_key         text,
    checkpoint_head_sha             text,
    checkpoint_dependency_fingerprint text,
    checkpoint_review_complete      boolean,
    last_base_ref                   text,
    last_run_id                     uuid REFERENCES automation_runs(id),
    last_turn_at                    timestamptz,
    created_at                      timestamptz NOT NULL DEFAULT now(),
    updated_at                      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_automation_target_sessions_retirement CHECK (
        (status = 'active'  AND retired_at IS NULL     AND retired_reason IS NULL) OR
        (status = 'retired' AND retired_at IS NOT NULL AND retired_reason IS NOT NULL)),
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
    ADD COLUMN github_action text,
    ADD COLUMN pull_request_updated_at timestamptz,
    ADD COLUMN head_epoch integer,
    ADD COLUMN head_resolution text
        CHECK (head_resolution IN ('authoritative', 'ambiguous', 'unresolved')),
    ADD COLUMN continuation_mode text
        CHECK (continuation_mode IN ('fresh', 'continued', 'reconstructed')),
    ADD COLUMN continuation_reason text CHECK (continuation_reason IN (
        'no_generation', 'kill_switch', 'session_unavailable', 'not_resumable',
        'agent_config_changed', 'identity_changed', 'base_retargeted', 'turn_limit',
        'snapshot_too_large', 'unsupported_workspace', 'awaiting_input',
        'snapshot_missing', 'restore_failed', 'sandbox_destroyed')),
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
    ADD COLUMN attempt_lock_token uuid,
    ADD COLUMN attempt_started_at timestamptz,
    ADD COLUMN superseded_by_run_id uuid REFERENCES automation_runs(id),
    ADD COLUMN outcome_reason text CHECK (outcome_reason IN (
        'turn_completed', 'head_lookup_degraded', 'agent_failed', 'cancelled', 'awaiting_input',
        'retries_exhausted', 'stale_head', 'duplicate_head', 'superseded', 'wait_timeout',
        'wait_overflow', 'pr_closed', 'repository_unavailable')),
    ADD COLUMN head_lookup_degraded boolean NOT NULL DEFAULT false,
    ADD COLUMN worker_node_id text,
    ADD COLUMN restore_snapshot_bytes bigint,
    ADD COLUMN restore_duration_ms integer,
    ADD COLUMN turn_duration_ms integer,
    ADD COLUMN warm_hit boolean,
    ADD COLUMN warm_skipped_reason text CHECK (warm_skipped_reason IN (
        'node_budget', 'org_budget', 'automation_budget', 'disabled', 'checkpoint_unpublished')),
    ADD CONSTRAINT chk_automation_runs_executing CHECK (
        dispatch_state IS DISTINCT FROM 'executing'
        OR (session_id IS NOT NULL AND job_id IS NOT NULL AND execution_started_at IS NOT NULL));

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

Status mapping: `superseded`, `duplicate_head`, `stale_head`, and `pr_closed` set `status = skipped` whether applied by `TerminalizeUnstarted` or by the executing preflight path; `wait_timeout`, `wait_overflow`, and `repository_unavailable` set `status = failed`. `head_lookup_degraded` is a flag, not an outcome, so a degraded turn still records `turn_completed`. This mapping is used consistently by the store, the API, and the tests. `restore_snapshot_bytes`, `restore_duration_ms` (container create plus restore, or clone plus bootstrap for fresh runs), `turn_duration_ms` (agent start to agent exit), and `worker_node_id` are recorded per attempt and overwritten by retries; the Stage 3 gate uses `attempt = 1` rows only. `warm_hit` and `warm_skipped_reason` are Stage 3 columns.

### `automation_run_results`

```sql
CREATE TABLE automation_run_results (
    run_id                uuid PRIMARY KEY REFERENCES automation_runs(id),
    org_id                uuid NOT NULL REFERENCES organizations(id),
    attempt               integer NOT NULL,
    attempt_lock_token    uuid NOT NULL,
    thread_id             uuid NOT NULL,
    turn_number           integer NOT NULL,
    outcome               text NOT NULL CHECK (outcome IN (
                              'turn_completed', 'agent_failed', 'cancelled', 'awaiting_input')),
    review_complete       boolean NOT NULL DEFAULT false,
    checkpoint_key        text,
    checkpoint_published  boolean NOT NULL DEFAULT false,
    checkpoint_head_sha   text,
    native_context        boolean NOT NULL,
    dependency_fingerprint text,
    agent_session_id      text,
    recorded_at           timestamptz NOT NULL DEFAULT now()
);
```

Written by the orchestrator in the same transaction as the turn's status update; one row per run, replaced only by a newer attempt's write carrying a newer token.

### `session_messages`

```sql
ALTER TABLE session_messages ADD COLUMN automation_run_id uuid REFERENCES automation_runs(id);
CREATE INDEX idx_session_messages_automation_run
    ON session_messages (org_id, automation_run_id)
    WHERE automation_run_id IS NOT NULL;
```

Set on the user and assistant messages of a per-target turn. Usage rollups attribute `token_usage` by it.

### Stuck-run reaper and sweeps

`AutomationRunStore.ReapStuckRuns` changes to exclude `dispatch_state = 'waiting'`, to measure `executing` runs from `attempt_started_at`, and to skip runs whose `job_id` is a running job with an unexpired lease or an active session executor. New per-org sweeps invoked by the scheduler: `FailTimedOutWaits` (waiting runs older than two hours, excluding `head_resolution = ambiguous`), `ResolveAmbiguityDeadline` (targets whose `head_resolution_deadline_at` has passed), and `ReconcileTargetWakes` (see Target Wake-up), each taking the target lock so they serialize with arrival and dispatch.

### `session_sandbox_holders` (Stage 3)

```sql
ALTER TABLE session_sandbox_holders
    DROP CONSTRAINT chk_session_sandbox_holders_holder_kind,
    ADD CONSTRAINT chk_session_sandbox_holders_holder_kind CHECK (holder_kind IN (
        'thread_runtime', 'preview', 'snapshot', 'operator', 'automation_warm'
    )),
    ADD COLUMN usage_event_id uuid,
    ADD COLUMN cleanup_state text NOT NULL DEFAULT 'none'
        CHECK (cleanup_state IN ('none', 'destroy_authorized', 'destroyed', 'usage_closed', 'done', 'orphaned')),
    ADD COLUMN destroy_authorized_at timestamptz,
    ADD COLUMN destroyed_at timestamptz,
    ADD COLUMN cleanup_note text
        CHECK (cleanup_note IN ('reference_cleared_elsewhere', 'destruction_time_estimated', 'node_lost'));
```

`models.SessionSandboxHolderKind` gains `automation_warm`. Holder `status` keeps its existing values; `orphaned` is a cleanup state, not a holder status, and the nonempty `container_id` constraint is preserved because holder container IDs are never cleared.

### `container_usage_events` and rollups (Stage 3)

```sql
ALTER TABLE container_usage_events
    ADD COLUMN purpose text NOT NULL DEFAULT 'turn'
        CHECK (purpose IN ('turn', 'automation_warm'));

ALTER TABLE usage_hourly_execution
    ADD COLUMN purpose text NOT NULL DEFAULT 'turn';
-- The rollup's uniqueness key gains purpose; the exact key follows the current
-- definition in migrations and is extended, not replaced.
```

Historical rows default to `turn`. `RecordStop` returns success for an already-closed event with the same ID.

### Settings and configuration (Stage 3)

- Org settings JSON gains `automation_warm_sandbox_limit` (integer, default 5, range 0 to 100), updated through the existing `PATCH /api/v1/settings` route, which merges the nested `settings` object and performs an in-place update. There is no version conflict check on org settings today and this design does not add one.
- Worker config gains `WORKER_MAX_WARM_SANDBOXES` (integer; unset means the derived default above, `0` disables). Documented in the environment variables reference when Stage 3 ships.

### Store surface

New store methods, all taking `orgID` first: `AutomationTargetStore.LockOrCreate`, `GetActiveGeneration`, `RetireGeneration`, `InsertGeneration`, `SetLifecycle`, `RequestWake`, `ClearWake`; `AutomationRunStore.ReserveForExecution`, `MarkWaiting`, `SupersedeWaitingPush`, `ClaimAttempt` (locks the job row), `TerminalizeUnstarted`, `CompleteExecutingPreflight`, `CompleteExecuting`, `NextWaiting`, `FailTimedOutWaits`, `ReconcileTargetWakes`; `AutomationRunResultStore.Write`, `GetByRun`; `SessionStore.ClaimForAutomationTurn`, `SetAutomationOwner`, `ClearAutomationOwner`, `PublishCheckpointWithProvenance` (the only `snapshot_key` writer for owned sessions; `UpdateTurnComplete`, `UpdateWorkspaceSnapshot`, and pending-key promotion gain owned-session guards); `AutomationTargetStore.ResolveAmbiguousHeads`, `ResolveAmbiguityDeadline`; Stage 3 `SessionSandboxHolderStore.AuthorizeWarmDestroy` (session CAS and holder update in one transaction). The only cross-org methods are the Stage 3 per-node warm reconciler's owned-holder listing, which is host-local like the sandbox GC reference listing and carries `lint:allow-no-orgid reason="host-local warm holder sweep"`, and the orphan sweep's dead-node holder listing with the same marker.

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

### Session mutation on owned sessions

Thread message sends, thread creation, follow-up commands, preview start, PR worktree materialization, and split verification on a session with `automation_owner_generation_id` set return 409 `SESSION_AUTOMATION_OWNED` with `details.automation_id`, `details.target_id`, `details.reset_url`, and `details.release_pending` (true when the generation is already retired and the marker will clear when the current turn finishes). Read routes are unaffected.

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
  "github_action": "synchronize",
  "head_epoch": 7,
  "head_resolution": "authoritative",
  "head_lookup_degraded": false,
  "continuation_mode": "fresh" | "continued" | "reconstructed",
  "continuation_reason": "no_generation",
  "native_context": true,
  "previous_head_sha": "abc123",
  "base_sha": "def456",
  "dispatch_state": "waiting" | "executing" | "done",
  "wait_reason": "target_busy",
  "outcome_reason": "turn_completed",
  "superseded_by_run_id": "uuid",
  "attempt": 1,
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
    "lifecycle_state": "open",
    "session_id": "uuid",
    "generation": 2,
    "status": "active",
    "turn_count": 4,
    "last_reviewed_head_sha": "abc123",
    "checkpoint_head_sha": "abc123",
    "last_turn_at": "2026-09-19T10:00:00Z",
    "waiting_runs": 1,
    "retired_reason": null,
    "retired_at": null
  }],
  "meta": {}
}
```

`POST /api/v1/automations/{id}/targets/{target_id}/reset` (member or admin), empty body. Retires the active generation with `manual_reset`, releases ownership immediately when the session is idle or marks it pending when a turn is executing, releases any warm hold, writes the wake outbox, writes an audit event, and returns 200 with `{data: <generation row as above>}` including `ownership_release_pending`. 404 `NOT_FOUND` for an unknown target in the org; 409 `AUTOMATION_TARGET_NOT_ACTIVE` when no active generation exists. A running turn is not cancelled.

### Session detail

Session responses gain an optional `automation_target` object (`automation_id`, `automation_name`, `target_kind`, `target_key`, `pull_request_url`, `generation`, `turn_count`, `status`, `owned`) when the session is a generation of a target. No SSE changes; existing run polling and session streams carry the new state.

### Internal and external APIs

No change to the external API in this version.

## Delivery Plan

### Stage 1 — Continuity with snapshot continuation and reconstruction

- Migrations, models, stores, and validation for the Stage 1 schema above.
- Session ownership marker and the 409 rejections in the thread service and session handlers.
- Ownership transaction, `ClaimForAutomationTurn`, run-scoped dispatch identity with conflict lookup, attempt claim, waiting and superseding, executing invariant, retry recognition.
- Head authority: `PullRequestUpdatedAt` on the trigger request; `PullRequestHead.UpdatedAt` returned by `GetPullRequestHead`; strict-order epoch assignment at arrival for push and lifecycle heads; ambiguity handling with `head_resolution_pending`, lookup retries, and unresolved execution; non-push epoch attribution; dispatch-time head lookup; degraded mode; epoch-monotonic baseline; force-push delta.
- `PublishCheckpointWithProvenance` as the exclusive `snapshot_key` writer for owned sessions, with owned-session guards on `UpdateTurnComplete`, `UpdateWorkspaceSnapshot`, and pending-key promotion, and key-bound provenance checks in readiness.
- Claude Code fallback predicate tightened to a missing-session signature.
- Orchestrator `AutomationTurnContinueOptions`: fetch by SHA, base SHA, detached checkout, verification, clean tree, dependency input fingerprint scoped to the checkpoint, delta with embedded summaries, reconstruction, timing and node recording, result marker write.
- `AutomationTurnCompleter` with double fencing and idempotency; `CompleteExecutingPreflight`; `TerminalizeUnstarted`; pending ownership release; wake outbox and `automation_target_wake` job; reconciliation sweep; legacy hook early return.
- Reaper changes: waiting exemption, attempt clock, lease-aware skip, wait timeout.
- `OnPullRequestClosed` lifecycle notification, `reopened` handling, openness revalidation, generation retirement.
- Positive tool allowlist for per-target turns, applied at snapshot build time, enforced by the internal API, with the preview capability omitted from the session token.
- Prompt template with untrusted-data boundaries.
- Audit and adapt session-to-run consumers: run list and detail queries, Slack session notifications, PagerDuty writeback, session list ownership queries. `GoalImprovementService` and migration 000248 are unchanged.
- Settings UI fields, run badges, `continuation_reason` and `outcome_reason` in run details.
- Worker env kill switch `AUTOMATION_SESSION_CONTINUITY_DISABLED=1`.

Exit: a labelled PR with five pushes produces one session and five runs; the second and later runs skip clone and tool bootstrap; Claude Code and Codex turns resume with native context and record `native_context = true`; a destroyed snapshot reconstructs in the same session; an agent type change goes fresh; concurrent pushes coalesce to one follow-up turn at the current head; a delayed older-head delivery never moves the baseline backwards; a follow-up dispatched while the previous job is still running is not lost; a crash between completion and wake is repaired by reconciliation; a run that waited 55 minutes is not reaped 5 minutes into execution; a human send to an owned session is rejected with 409.

### Stage 2 — Conversation surface

- Targets endpoints, conversation list on the automation page, reset action, session-page attribution and owned-session banner.
- Metrics: continuation rate by mode, native-context rate, retire and continuation reasons, time to first agent event and tokens per run for fresh versus continued.

Exit: no automation with continuity enabled leaves a run without a session or a session without a generation row; reset works while a turn runs and the old turn's completion does not dispatch into the retired generation.

### Stage 3 gate — Is warm worth building?

Stage 3 is conditional. It is not scheduled until Stage 1 has run in production for at least two weeks on real labelled PRs and the numbers below justify it.

What Stage 3 can save is only the container create plus the snapshot download and untar. It cannot save clone, dependency install, the agent's cold read, or any tokens; Stage 1 already removes those. What Stage 3 costs is a live-container slot, host memory, and disk for the whole warm window on one node, plus node affinity.

Measurement contract, from Stage 1 columns:

- Cohort: runs with `continuation_mode = continued`, `attempt = 1`, `outcome_reason = turn_completed`, at least 200 runs across at least 5 automations.
- Restore share: median of `restore_duration_ms / turn_duration_ms` over the cohort.
- Checkpoint size: `restore_snapshot_bytes` distribution over the cohort.
- Idle gap: for consecutive runs on the same generation, `completed_at` of turn N to `triggered_at` of run N+1. A non-positive gap means the next run was already waiting; those are reported separately as queued follow-ups and excluded from the TTL simulation, because waiting already covers them without a warm container.
- TTL simulation: replay the cohort's completion and arrival timestamps in order, per node and org and automation, applying the configured budgets (node default, org 5, automation 3) and oldest-first eviction, for TTLs of 15, 30, and 60 minutes; report the hit rate each would have achieved.
- Placement: fraction of consecutive continued runs on the same generation with equal `worker_node_id`.

Build Stage 3 only if restore share exceeds 20%, the simulated 15-minute hit rate under budgets exceeds one third of continued runs, and same-node placement exceeds 50%. If restore is slow mainly because checkpoints are large, reduce checkpoint size first and re-measure. If the gate fails, Stage 3 stays parked here with the measured values recorded in the decision log. Token and cold-read savings are hypotheses until the Stage 2 fresh-versus-continued comparison confirms them, particularly for turns where native resume fell back.

### Stage 3 — Warm sandboxes (conditional on the gate)

- Holder kind, `usage_event_id`, `cleanup_state`, advisory-lock budget admission, `warm_skipped_reason`, `warm_hit`.
- Per-node warm reconciler: heartbeat, expiry, cleanup state machine, eviction path, orphan sweep.
- Node affinity via `target_node_id`.
- Usage purpose column, rollup key change, interval transitions, sampler event-ID matching, `RecordStop` idempotency, usage API changes.
- Settings UI for warm minutes, warm targets, and the org limit.

Exit: a push within a 15-minute window reuses the container on the owning node and records `warm_hit = true`; a push after expiry restores from snapshot; two generations and two executor processes admitting simultaneously cannot exceed any budget; each budget skips the hold with the right reason; eviction reclaims the oldest warm container before refusing a real turn and never destroys a container with an active turn; a crash at every cleanup step resumes and finishes; warm and turn usage intervals never overlap and each closes exactly once across reuse, expiry, eviction, and node loss.

## Validation and Acceptance

| Area | Tests |
|------|-------|
| Readiness and compatibility | Table-driven unit tests for every readiness state and compatibility row, including kill switch, `sandbox_state = none`, pending snapshot wait, size limit, lifecycle states, and checkpoint-head lag |
| Ownership | Real PostgreSQL: two workers reserve runs for one target; second waits; failure after claim rolls back the claim and message; executing invariant rejects a second reservation; retry recognizes its own reservation; a human send after the automation transaction commits is rejected with 409 |
| Dispatch identity and attempts | Follow-up enqueue succeeds while the previous `continue_session` job is still `running`; conflict lookup accepts the same run and rejects a different one; a paused worker whose job was reclaimed cannot claim an attempt before or after the next worker claims; a stale lock token cannot write a result marker, a preflight outcome, or a completion; first dispatch succeeds with the executing CHECK enabled |
| Completion evidence | No marker plus dead-lettered job yields `retries_exhausted`; marker present on retry completes without re-execution; crash after `UpdateTurnComplete` and before the completer is recovered from the marker; snapshot failure produces `checkpoint_published = false` and leaves provenance unchanged; upload success followed by a provenance-write failure keeps the previous key installed on the owned session and orphans the new blob; drain and pending-key promotion on an owned session never install a key without provenance; a key mismatch between session and generation is treated as null provenance; a cancelled turn that published a `graceful_stop` checkpoint replaces provenance with `review_complete = false`; successful A then interrupted B with different dependency inputs then continuation at C treats the environment as B's and embeds B as interrupted; unreachable SHA after reservation is skipped through the preflight path and releases the claim; legacy hook ignores owned sessions |
| Waiting and wake | Push supersedes older waiting push at arrival; non-push runs queue and each executes; waiting cap; wait timeout; stuck reaper ignores waiting runs and measures from attempt start; lease-aware skip; `TerminalizeUnstarted` never matches executing runs; wake job dispatches the next waiter; reconciliation repairs a lost wake |
| Head authority | H2 waiting then delayed H1 with older `updated_at`: H1 skipped at arrival, H2 reviewed, including with the GitHub lookup failing; H1 and H2 with equal `updated_at` in both arrival orders: neither superseded, lookup resolves to the current head and supersedes the other; lookup failing to the ambiguity deadline converts both to unresolved in one target-locked transition, restarts their wait clocks, dispatches a blocked authoritative push first, then both candidates, while `FailTimedOutWaits` running concurrently at the deadline touches neither; the second candidate waiting while the first executes is not failed; missing timestamp treated as ambiguous; two ambiguity episodes separated by a successful resolution each receive a full deadline window; delayed `opened`, `reopened`, and `ready_for_review` at H1 after H2 is observed execute with null epochs and do not regress the baseline; H1 comment waiting behind an H2 push: comment gets a null epoch and never moves the baseline back; divergent force-push with a newer epoch advances the baseline; a stale delivery never advances it; missed webhook adopted at dispatch; duplicate rule applies only to pushes; `edited` with base change retires |
| Workspace | Fetch by SHA; unreachable SHA skips as `stale_head`; base SHA recorded; detached checkout verified; dirty tree discarded with bounded log; submodules synced; fingerprint change re-runs tool bootstrap; reconstruction treats the fingerprint as absent; delta from the coherent baseline with embedded summaries; full review from base when unavailable |
| Reconstruction | Missing and destroyed snapshots rebuild in the same session; a live pending publisher is never raced; the reconstructed turn publishes a snapshot back onto the session |
| Lifecycle | Reset, disable, and close during a running turn keep rejecting human mutation until that turn's completion clears the marker; idle reset releases immediately; kill switch; merged with and without subscription; unmerged close via `OnPullRequestClosed`; late event after close; reopen; awaiting input releases ownership; agent config and identity changes |
| Capabilities | The allowlist is exact: `update_policy`, automation actions, eval add, PR create, Slack send, Linear and PagerDuty writes, goal-improvement complete, and preview actions are denied server-side even when the org grants them; read tools remain |
| Prompt | Rendered snapshot tests for continued, reconstructed, no-history, same-head event, checkpoint-lag, and dependency-changed cases; PR text appears only inside delimited untrusted blocks; SHA and PR number validation rejects malformed input |
| Run history | Run list and detail read run-local fields; historical rows read the origin link; token usage attributed by `session_messages.automation_run_id` |
| Warm (Stage 3) | Concurrent admission across generations and executor processes respects every budget; each budget's reason; `0` disables and unset derives; single-slot node gets 0; heartbeat, expiry, and each cleanup step resumes after a crash, including a crash between the session CAS and the holder update (impossible to observe because they commit together) and a crash after destruction before usage closure; false CAS with the reference intact defers; false CAS with the reference gone and the container alive destroys it and closes usage; reference gone and container absent records an estimated destruction time and closes usage; destruction success followed by a usage-close crash resumes at `usage_closed`; eviction order and refusal to evict active turns; orphan sweep on node loss keeps holder identity and the returning node finishes; deferred `ContainerStopped` leaves the swapped warm event in place; usage intervals disjoint and closed once |
| Frontend | Settings validation messages, owned-session banner, run badges, targets list and pagination, reset confirmation, mobile layout |
| Tenancy | `lint-stores` and `lint-schema` pass; same-org validation rejects cross-org parents; cross-org target lookups return nothing |

Acceptance measurements before enabling by default for any template: median time from trigger to first agent event for continued runs versus fresh runs, tokens per continued run versus fresh run on the same PRs, native-context rate, and a sampled quality comparison of continued reviews against independent fresh reviews on the same head. The Stage 3 gate measurements are collected in the same window.

## Rollout, Migration, and Recovery

- Deploy the migration first; it is additive. Old workers ignore the new columns and keep creating fresh sessions because `session_continuity` defaults to `per_run`.
- Enable continuity per automation. There is no global default flip in this design.
- Rollback: set automations back to `per_run` (retires generations, releases ownership; waiters run as per-run sessions) or set the worker kill switch (forces fresh sessions; generations untouched). Keep the schema; the down migration is a disposable compatibility check only.
- Precedence: worker kill switch, then the automation row's current `session_continuity`, then the run's `config_snapshot` for audit only.
- A session executor or worker loss mid-turn recovers through the existing checkpoint path, the attempt claim, and the result marker rules above.
- Stage 3, if built, rolls out with `WORKER_MAX_WARM_SANDBOXES` set explicitly per node and the org limit at its default. Setting the node value to `0` disables warm holds on that node without touching automations; existing warm containers drain through the reconciler.

## Risks

- **Anchoring on stale context.** A resumed agent may repeat or over-trust earlier findings. The continuation block frames the turn as a delta review and asks for explicit resolution of earlier findings. The turn limit and reset bound drift.
- **Instructions carried in PR content.** PR text and diffs are untrusted and persist in native provider context across turns. The template marks them as data on every turn, bounds their size, and validates git arguments. Per-target turns carry no 143 tool that writes outside the sandbox, so the remaining authority is shell and whatever network egress the organization's sandbox settings allow, and those effects are at-least-once across retries. Organizations that enable continuity on public repositories should pair it with restricted egress.
- **Owned sessions surprise people.** A person who opens a per-target session cannot message it. The 409 message and the session banner point at Reset, which hands the session back.
- **Hidden 1:1 assumptions.** Several paths treat a session and its automation run as one pair. The Stage 1 audit list is the mitigation; tests cover each adapted consumer.
- **Workspace validity across turns.** A clean tracked tree with stale dependency caches can produce misleading tool output. The checkpoint-scoped fingerprint and the `unsupported_workspace` retirement bound this; project dependency installation stays with the agent, as today.
- **Warm containers crowd out turns.** An idle warm container occupies the same admission slot as a running turn and cannot be reclaimed by today's pressure GC. The per-node budget, the reconciler's eviction path, and the rule that a real turn always wins are the mitigations; Stage 3 does not ship without all three.
- **Node affinity for warm containers.** Continuation on the wrong node loses the warm benefit while still paying for it. The gate requires evidence of same-node placement before building, and `warm_hit` measures the benefit afterwards.
- **Checkpoint growth.** Session checkpoints keep `.git` and build caches. The size limit retires oversized generations; age and turn limits bound duration.

## Non-Goals

- Sharing a session across multiple pull requests or multiple automations.
- Continuity for non-PR triggers in this version.
- Human participation in an active generation's session.
- Cancelling an in-flight turn when a newer push arrives. Waiting and superseding are sufficient for review workloads; cancellation can follow the built-in reviewer's classification rules later.
- Reusing prior review results without running an agent. That is the built-in reviewer's scheduling design.
- Publishing pull requests, comments, or notifications from a per-target turn.
- Repairing missed push webhooks beyond the dispatch-time head lookup.

## Decision Log

- **2026-09-19 — Session per target, run per trigger.** Runs keep their own rows, outcome, timing, and usage so the automation page stays truthful about what fired; the session is the memory. Rejected: one run per PR with turns nested inside it, because it breaks run idempotency, dedupe, and analytics keyed on runs.
- **2026-09-19 — Wait and supersede instead of cancel.** Review turns are short relative to push bursts, and cancellation requires the equivalence classification the built-in reviewer is still building.
- **2026-09-19 — Restrict to `publish_policy = none` and strip every external-write capability.** Keeps workspace correctness a one-line rule (reset to head), makes crashed attempts re-executable without replaying 143-mediated writes (shell and network effects remain at-least-once), and bounds what a prompt-injected turn can do. Review round 2 showed that removing publishing alone left comment, Slack, and automation writes reachable.
- **2026-09-19 — Automation-owned sessions.** Review round 2 showed human sends are admitted on running sessions and sibling threads share the checkout, which no target lock can prevent. Rejected: a reservation checked by every human path, because it spreads the invariant across many handlers for a v1 whose users can reset instead.
- **2026-09-19 — Durable result marker and attempt tokens.** Review round 2 showed idle-plus-timestamp inference cannot distinguish a completed run from the previous turn, and that `job_id` cannot fence a worker that lost its lease. The job lock token already fences executor writes and is reused as the attempt identity.
- **2026-09-19 — Current head at dispatch, not delivery order.** Review round 2 gave a counterexample where a delayed ancestor superseded a newer waiting push. Reviewing the current head as GitHub reports it removes ordering from the protocol; degraded mode keeps the baseline monotonic.
- **2026-09-19 — Outbox plus reconciliation for wake-up.** Review round 2 showed a crash between completion and next-waiter dispatch strands a waiter that the reaper exempts.
- **2026-09-19 — Warm sandbox is a separate, conditional stage.** Snapshot restore already removes the clone, bootstrap, and cold-read cost, and warm saves no tokens. Stage 3 is gated on Stage 1 data rather than scheduled.
- **2026-09-19 — Warm holds are best-effort, budgeted, and serialized in the database.** Skipping a hold is free of correctness risk. Advisory locks in fixed order replace row locks and process-local mutexes that cannot serialize across generations or executor processes.
- **2026-09-19 — Separate target identity from generations.** A placeholder row cannot exist with a non-null `session_id`. `automation_targets` is the lock, lifecycle, and outbox row; generations hang off it.
- **2026-09-19 — Attempt ownership is validated against the job row, not by token inequality.** Review round 3 showed a paused worker with an old token still differs from the new owner's token. Locking and checking the authoritative job row serializes with reclaim and closes the window before the next claim.
- **2026-09-19 — Provenance follows the checkpoint.** Review round 3 showed cancellation publishes a `graceful_stop` checkpoint that would replace the snapshot without replacing its declared head. Writing provenance in the publication wrapper makes the two inseparable.
- **2026-09-19 — Order pushes by GitHub's `updated_at`, not delivery.** Review round 3 showed degraded mode could let a delayed ancestor discharge newer coalesced work. A server-side timestamp orders deliveries without a lookup; epochs replace ancestry for baseline monotonicity so divergent force-pushes advance.
- **2026-09-19 — Positive tool allowlist.** Review round 3 showed a subtractive list left `update_policy` and three bypassed namespaces reachable.
- **2026-09-19 — Ambiguity is preserved, never guessed.** Review round 4 showed `updated_at` has one-second precision, so ties and missing values cannot be ordered. Ambiguous pushes wait for an authoritative lookup; if none arrives before the wait timeout, every candidate is reviewed rather than any discarded.
- **2026-09-19 — Only pushes are ever dropped on head grounds.** Review round 5 showed arrival classification could skip a delayed lifecycle event the product promises to execute. Observation and acceptance are now separate; every non-`synchronize` event executes at its delivered head with an epoch only when that head is the observed one.
- **2026-09-19 — One deadline per waiting row.** Review round 5 showed the ambiguity fallback and the wait-timeout sweep could race on the same candidates. Ambiguous rows have exactly one deadline transition, which converts them and restarts their wait clock before the ordinary sweep can see them.
- **2026-09-19 — One snapshot writer for owned sessions.** Review round 4 showed three existing paths install a snapshot key outside checkpoint publication. Provenance is bound to the key and installed by the same statement, and the other writers are guarded for owned sessions.
- **2026-09-19 — Continuation does not pin a subscription.** A session is not bound to one coding credential: the orchestrator picks a credential per turn from the org's or user's pool and skips rate-limited ones through the health cache and the persistent rate-limit marker, so a continued turn is picked exactly like a fresh session's first turn. Native context lives in the snapshot's local agent state, not in the provider account. When every credential in the pool is limited, the turn fails as `agent_failed` and the job's retry starts a new attempt, the same behaviour a fresh per-run session has today; a pre-dispatch credential availability check would be an improvement for both paths and is out of scope here.
- **2026-09-19 — Dependency fingerprint keys on the image tag.** The sandbox image is resolved by tag (`SANDBOX_IMAGE`) and no digest is read at run time, so the fingerprint hashes the tag; a rebuilt image under the same tag is not detected until the digest is plumbed through `SandboxConfig`. The fingerprint also covers tracked manifests at any depth (top level and nested), bounded to 64 files.
- **2026-09-19 — Preflight releases a pending session too.** A fresh generation's session is `pending` until the agent starts; a preflight outcome (stale head, repository unavailable) returns it to `idle` like a claimed running session, so the next trigger can claim it instead of retiring it as not resumable.
- **2026-09-19 — Turn path split from the tool allowlist.** The orchestrator turn path (workspace preparation, provenance writer, delta prompt, result marker, preflight) ships as its own slice; the positive tool allowlist, token scopes, and internal API enforcement follow as the next slice so each stays reviewable.
- **2026-09-19 — Target creation and the continuity switch share an advisory lock.** Implementation review showed that a target inserted by an uncommitted ownership transaction is invisible to the switch's `FOR UPDATE` scan. `LockOrCreate` and the bulk retirement take an automation-scoped transaction advisory lock before any target row lock.

## Review History

- **Round 1 (2026-09-19, Codex gpt-6-astra, high effort).** Verdict: request changes. Five factual corrections and fifteen design findings. All folded in.
- **Round 6 (2026-09-19, Codex gpt-6-astra, high effort).** Verdict: **accept**. No blockers or majors. One minor: a successful tie resolution did not explicitly clear the ambiguity deadline, so a later tie could enter fallback immediately. Folded in.
- **Round 5 (2026-09-19, Codex gpt-6-astra, high effort).** Verdict: request changes. Two majors, both in the head-resolution protocol added in round 4: the ambiguity fallback raced the wait-timeout sweep on the same rows, and arrival classification could skip delayed lifecycle events. Folded in: a single target-locked `ResolveAmbiguityDeadline` transition with mutually exclusive sweep predicates and restarted wait clocks, and separation of head observation from event acceptance so only `synchronize` runs are ever skipped or superseded.
- **Round 4 (2026-09-19, Codex gpt-6-astra, high effort).** Verdict: request changes. No blockers. Four majors (`updated_at` ties and missing timestamps could discard newer work, three snapshot-key writers bypassed the provenance boundary, the reference-cleared cleanup branch skipped billing closure, non-push runs had no epoch attribution and could move the baseline back), two minors (prose still describing immediate ownership release and ancestry-based monotonicity, lookup helper lacking `updated_at`). All folded in: ambiguous head resolution with durable reconciliation and unresolved execution, exclusive key-bound provenance writer with guards on the other writers, cleanup branch that reaches `done` only through usage closure, epoch attribution for every event kind, consistency edits, and the lookup extension in Stage 1.
- **Round 3 (2026-09-19, Codex gpt-6-astra, high effort).** Verdict: request changes. One blocker (attempt claim accepted any differing token), seven majors (ownership released while a turn ran, degraded head lookup could discharge newer work and the ancestry predicate could not advance across force-pushes, interrupted checkpoints replaced the snapshot without provenance, subtractive capability list missed a policy write and bypassed namespaces, cleanup state machine not restart-safe and orphan transition invalid, executing `stale_head` had no legal terminal transition, reservation order violated the executing CHECK), one minor (fallback predicate described inaccurately). All folded in: job-row-validated attempt claims with double fencing, pending ownership release, `updated_at` epochs with dispatch-time lookup as enrichment only, provenance written at publication for every checkpoint kind, positive allowlist with endpoint enforcement, atomic destroy authorization with retained holder identity and explicit orphan state, executing preflight outcomes, enqueue-before-reserve ordering, accurate fallback description with a Stage 1 tightening.
- **Round 2 (2026-09-19, Codex gpt-6-astra, high effort).** Verdict: request changes. One blocker (completion inferred from session state; attempt fencing), eight majors (human-turn arbitration, waiter wake-up and closed-target state, head ordering, checkpoint coherence, replay safety, shared budget atomicity, warm cleanup recovery, contradictory terminal transitions), four minors (fingerprint scope and inputs, unspecified fields and FK decisions, TTL simulation without budgets, retirement CHECK). All folded in: result marker and attempt tokens, automation-owned sessions, wake outbox with reconciliation and lifecycle state, current-head dispatch, checkpoint-head coherence, restricted capability set, advisory-lock admission, cleanup state machine and orphan sweep, separate unstarted and executing transitions, checkpoint-scoped fingerprint, `continuation_reason`, `session_messages` attribution, FK policy, budget-aware TTL simulation, explicit retirement CHECK.
