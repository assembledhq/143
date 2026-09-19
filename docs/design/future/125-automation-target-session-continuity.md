# Design: Automation Target Session Continuity

> **Status:** Not Started | **Last reviewed:** 2026-09-19

> **Depends on:** [overall.md](../overall.md), [48-automations-separation.md](../implemented/48-automations-separation.md), [119-github-automation-trigger-context.md](../implemented/119-github-automation-trigger-context.md), [76-pr-repair-session-continuity.md](../implemented/76-pr-repair-session-continuity.md), [82-durable-session-executors.md](../implemented/82-durable-session-executors.md), [88-shared-sandbox-thread-runtimes.md](../implemented/88-shared-sandbox-thread-runtimes.md), [54-s3-session-snapshots.md](../implemented/54-s3-session-snapshots.md)
>
> **Related:** [code-review-scheduling-and-reuse.md](../code-review-scheduling-and-reuse.md) (result reuse for the built-in reviewer, not session reuse), [116-automatic-pr-feedback-follow-through.md](116-automatic-pr-feedback-follow-through.md) (canonical-session continuation for 143-generated PRs)

## Summary

A GitHub-triggered automation that fires on every push to a labelled pull request should not pay for a fresh container, a fresh clone, and a cold agent on each push. It should continue one conversation per pull request: the same session, the same restored workspace, and, where the provider supports it, the same native agent context, so each new turn reviews what changed since the last turn instead of starting over.

This design adds an opt-in **per-target session continuity** mode to automations. The target is the pull request. The first trigger creates a session as today. Later triggers for the same PR continue that session through the existing `continue_session` path, after moving the restored workspace to the triggering head SHA. Runs still get their own `automation_runs` row and history; they just execute inside a shared session.

Default behavior is unchanged: automations keep creating one session per run unless continuity is enabled.

The savings this design commits to come from Stage 1: no clone, no dependency install, no cold read of the repository, and a delta review instead of a whole-PR review. Keeping containers warm between pushes (Stage 3) is not part of that commitment. It is a conditional stage that is built only if Stage 1 measurements show restore time is a meaningful share of turn time, and it ships with hard budgets because an idle warm container occupies the same node slot as a running turn.

## Problem

Today `triggerAutomation` creates an `automation_runs` row per delivery and `newAutomationRunHandler` always creates a new session and enqueues `run_agent` ([github_events.go](../../../internal/services/automations/github_events.go), [handlers.go](../../../internal/worker/handlers.go)). Nothing links a run to a prior session for the same PR. Every push therefore repeats:

- sandbox creation, repository clone, dependency install, and auth bootstrap
- a cold read of the repository and the design principles the goal references
- a review of the whole PR rather than the delta since the last review

The primitives to avoid this already exist at the session layer:

- `continue_session` claims a resumable session, restores its snapshot (workspace and agent state directories), and for Claude Code, Codex, and OpenCode resumes the provider CLI with its captured agent session ID (`CheckpointCapabilityFullResume` in [runtime.go](../../../internal/services/agent/runtime.go)). Amp and Pi restore the filesystem and receive embedded transcript context instead.
- A live container is reused when the session still owns one and a holder keeps it alive; otherwise the turn ends with a snapshot and the container is garbage-collected after `SANDBOX_GC_GRACE`.
- `session_sandbox_holders` lets any holder kind keep a sandbox alive under a lease.
- PR repair (doc 76) already continues one canonical session per PR with two workspace sources: snapshot continuation or PR-head reconstruction, verifying that checked-out `HEAD` equals the expected SHA.

Doc 116 notes the gap directly: "Generic automations create separate sessions and do not preserve complete multi-comment review or reply-thread state." This design closes that gap for automations without turning them into the built-in code reviewer.

## Principle

Borrowed from doc 76: **session continuity and workspace correctness are separate concerns.**

- The automation target session owns the conversation: the goal prompts, assistant replies, logs, and per-turn results for one PR.
- The workspace must match the head SHA the triggering run is about, regardless of whether the backend reused a live container, restored a snapshot, or rebuilt the checkout.

The run remains the unit of "what happened at this trigger." The session is the unit of memory.

## Scope

In scope:

- GitHub triggers whose event identifies a pull request: `github.pull_request.opened`, `github.pull_request.updated`, `github.pull_request.ready_for_review`, `github.check_suite.completed`, `github.check_run.completed`, `github.issue_comment.created`, `github.pull_request_review.submitted`, `github.pull_request_review_comment.created`.
- Automations whose `publish_policy` is `none`. Continuity in this version is for read-and-report automations such as design-principle reviews. Sessions that publish PRs accumulate branch state across turns and need the changeset model; that is deferred.
- Claude Code, Codex, OpenCode (full resume) and Amp, Pi (filesystem resume plus embedded history).

Out of scope for this version:

- Schedule, manual, Linear, PagerDuty, and Slack triggers. They have no per-target identity today; a later revision may key Linear triggers on the issue.
- Continuity across pull requests or across automations.
- Replacing or changing the built-in code reviewer and its scheduler.
- Merging transcripts, forking sessions, or exposing continuity to the external API.

## Product Behavior

### Settings

Automation settings gain a **Conversation** section:

- **Session continuity**: `New session per run` (default) or `Continue one session per pull request`.
- **Keep sandbox warm for** (Stage 3, only if built): `Off` (default), 15, 30, 60, or 120 minutes. Shown only when continuity is enabled. Explains that warm time counts toward container usage, is best-effort, and is capped per automation, per organization, and per worker node.
- **Warm targets** (Stage 3, only if built): how many pull requests this automation may keep warm at once. Default 3.

Enabling continuity requires at least one PR-scoped GitHub event trigger and `publish_policy = none`. The form explains both requirements inline and the API rejects violations.

### First trigger for a PR

Identical to today from the user's perspective: a run is created, a session starts, the run row shows the session. The backend also records the session as the active target session for `(automation, repository, PR)`.

### Later triggers for the same PR

- The run row appears with a **Continued** badge and shows `previous head → head`.
- The session transcript gains one new user turn containing the automation goal, the GitHub event context, and a continuation block describing what changed since the last turn.
- Logs, result summary, and token usage for the turn attach to the same session and to the new run.
- If the stored session cannot be continued safely, the run shows **Fresh** (a new session started and became the target session) or **Reconstructed** (same session, rebuilt workspace). The reason is visible in the run details.

### Pushes while a turn is running

At most one turn runs per target. A run that arrives while the target is busy waits with reason **Waiting for the current turn**. When the turn finishes, only the newest waiting run for that target dispatches; older waiting runs are marked skipped with **Superseded by a newer push**. Ten pushes during one review produce one follow-up turn at the final head.

### Reset

The automation page lists active target conversations (repository, PR, turn count, last head, last activity). Members and admins can **Reset conversation** for a target, which retires the session so the next trigger starts fresh. Resetting does not delete the session or its history.

## Continuation Decision

The run handler evaluates the stored target session under the target row lock. Continue when all of the following hold:

| Check | Source | Failure action |
|-------|--------|----------------|
| Automation has `session_continuity = per_target` and the run carries a PR target | run `config_snapshot.github` | fresh session, no target row |
| Active target row exists and `status = active` | `automation_target_sessions` | fresh session, new target row |
| Session not archived or deleted | `sessions.archived_at`, `deleted_at` | retire (`session_unavailable`), fresh |
| Session status is idle or in `ResumableSessionStatuses` | `sessions.status` | running: wait (see Concurrency); other: retire (`not_resumable`), fresh |
| `sandbox_state != destroyed` or a usable snapshot exists | `sessions.sandbox_state`, `snapshot_key` | reconstruct in the same session |
| `pending_snapshot_key` is empty | `sessions.pending_snapshot_key` | short retry, then reconstruct |
| Agent type, model override, and reasoning effort match the automation's current values | run `config_snapshot` vs session | retire (`agent_config_changed`), fresh |
| Base branch unchanged since the last turn | `config_snapshot.github.base_branch` vs `last_base_ref` | retire (`base_retargeted`), fresh |
| `turn_count < max_turns` (constant, initial value 25) | target row | retire (`turn_limit`), fresh |
| Snapshot younger than `SESSION_MAX_SNAPSHOT_AGE` | session snapshot metadata | retire (`snapshot_expired`), fresh |
| Continuity kill switch not set | worker env | fresh session, target row left untouched |

PR closed or merged events retire the target row (`pr_closed`) without starting a session unless the automation is itself subscribed to `github.pull_request.merged`, in which case the merged run executes as a final continued turn and then retires the row.

Retirement never deletes anything. It sets `status = retired`, `retired_reason`, and `retired_at`, and the next fresh session inserts a new active row with `generation + 1`.

## Workspace Preparation

Continuation reuses `ContinueSession` with a new `AutomationRerunContinueOptions` value alongside `PRRepair` and `PRFeedback`. Complexity stays in workspace preparation, as in doc 76.

### Snapshot continuation or live container

Preferred path. After the existing restore (or live-container reuse), the orchestrator:

1. Fetches the PR head through the session auth socket: `git fetch origin refs/pull/<n>/head`.
2. Requires a clean tree. If `git status --porcelain` is non-empty, it discards local changes (`git reset --hard && git clean -fd`) because per-target automations cannot publish and have no legitimate uncommitted work. The discard is logged with the file list.
3. Checks out the run's head: `git checkout --detach <head_sha>` on the session working branch, then verifies `git rev-parse HEAD == head_sha`.
4. Computes the continuation delta: `git diff --stat <previous_head_sha>..<head_sha>` and the changed file list, bounded to 200 files and 4 KB of stat output. When `previous_head_sha` is unknown or unreachable (force-push that dropped history), the delta says so and the prompt asks for a full review.

If the fetch shows the PR head has already moved past `head_sha`, the turn still reviews `head_sha`; the newer push has its own run and the busy-target coalescing handles ordering.

### PR-head reconstruction

Used when the snapshot is missing, destroyed, or fails to restore. The orchestrator creates a fresh sandbox, clones, fetches the PR head, verifies the SHA, and runs the turn in the same session with bounded transcript context (initial goal, latest assistant summary, last two turns). This reuses `checkoutPullRequestHead` and the bounded-context builder from PR repair. The turn's final snapshot attaches to the session so the next trigger can use snapshot continuation again. Provider-native context is lost on this path; the prompt says so.

### Warm sandbox (Stage 3, conditional)

Today the end of a `run_agent` or `continue_session` turn destroys the container as soon as the turn hold is released unless a preview or another active `session_sandbox_holders` row holds it. There is no idle grace; `SANDBOX_GC_GRACE` only applies to orphaned containers that lost their database reference. Automation sessions never have a preview, so their containers are always destroyed at turn end.

If Stage 3 is built, the end-of-turn path still publishes the snapshot first, then attempts a **best-effort warm hold**: a `session_sandbox_holders` row with `holder_kind = automation_warm`, `holder_id = <target row id>`, and `expires_at = now + warm_sandbox_minutes`. The existing destroy decision already consults active holders, so the container survives while the holder is active. The next continuation takes the reused-container path when the job lands on the owning node and the container is alive; otherwise it falls back to snapshot restore, which is always correct because the snapshot was published first.

**Budgets.** A warm hold is admitted only when all three budgets have room. When any is exhausted the hold is skipped, the container is destroyed as today, and the run records `warm_skipped_reason`. Skipping never affects correctness.

| Budget | Where | Default | Rationale |
|--------|-------|---------|-----------|
| Per worker node | `WORKER_MAX_WARM_SANDBOXES` env | 25% of the effective `WORKER_MAX_ACTIVE_SANDBOXES`, minimum 1 | Warm containers count against the live-container admission gate, so turns must keep guaranteed headroom |
| Per organization | versioned org setting `automation_warm_sandbox_limit` | 5 | One tenant cannot fill a node with idle containers |
| Per automation | `automations.max_warm_targets` | 3 | A busy automation with many open PRs does not keep one container per PR |

**Eviction.** Warm holders are the lowest-priority holders. The capacity gate's pressure cleaner gains a holder-aware path: when admission for a real turn would fail, it expires the oldest `automation_warm` holder on the node by `last_turn_at`, runs `FinalizeContainerDestroy`, destroys the container, and then admits. This is new behavior; `ReapForCapacity` today only destroys unreferenced containers and cannot reclaim a warm-held one, which still has `sessions.container_id` set. The thread reaper expires warm holders on their deadline as it does other holders. A container with an active turn is never evicted.

**Billing.** The turn's container usage event closes at turn end as today. A warm hold opens a separate usage event with purpose `automation_warm` that closes on release, expiry, or eviction, so warm time is visible on its own in usage and can be priced or excluded without mixing into turn time.

## Prompt

Add `internal/prompts/templates/automation_rerun.template` with an exported render function. The visible user message on the session thread is:

1. The run's `goal_snapshot` (goal plus GitHub event context, unchanged from today).
2. A **Continuation context** block: turn number, previous head SHA, current head SHA, base branch, the bounded diff stat and changed-file list since the previous head, and whether native agent context was preserved.
3. Instructions: review the changes since the previous head against the goal; do not repeat findings for unchanged code unless the change invalidates them; state which earlier findings the new push resolved; when history is unavailable, review the full PR.

`automationRunPromptSeed` continues to append trigger timestamps. For continued turns the "previous automation run" line refers to the previous run for this target, not the automation's last run overall.

## Concurrency

- **Target lock.** The run handler locks the target row (`SELECT ... FOR UPDATE`, inserting a placeholder when absent) before deciding fresh, continue, wait, or skip. Two workers claiming runs for the same PR serialize on it.
- **One active turn.** Continuation uses `ClaimForResume` plus `ClaimForResumeInSession` on the session's primary thread. A busy session leaves the run `pending` with `wait_reason = target_busy`.
- **Coalescing.** On session turn completion, the automation hook loads waiting runs for the target ordered by `triggered_at`. The newest dispatches; older ones transition to `skipped` with `superseded_by_run_id`. A waiting run older than two hours fails with `wait_timeout` so the stuck-run reaper does not sweep it silently.
- **Dedupe.** `continue_session` uses the thread-scoped dedupe key. Because the handler only enqueues after winning the target lock and the resume claim, a duplicate enqueue is a no-op.
- **Retry.** A `continue_session` retry for a continued run re-enters the same decision; the run keeps its `session_id` and `continuation_mode`.
- **Executors.** Continued turns hand off to session executors exactly like other `continue_session` jobs; no executor changes.

## Database Contract

Migration adds the following. All tables carry `org_id uuid NOT NULL REFERENCES organizations(id)` and every store method filters by it.

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

All three values are captured in each run's `config_snapshot` so an in-flight run keeps the policy it started with. `warm_sandbox_minutes` and `max_warm_targets` are Stage 3 columns; Stage 1 may ship without them.

### `automation_target_sessions`

```sql
CREATE TABLE automation_target_sessions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid NOT NULL REFERENCES organizations(id),
    automation_id   uuid NOT NULL REFERENCES automations(id),
    repository_id   uuid NOT NULL REFERENCES repositories(id),
    target_kind     text NOT NULL CHECK (target_kind IN ('github_pull_request')),
    target_key      text NOT NULL,                 -- PR number as text
    session_id      uuid NOT NULL REFERENCES sessions(id),
    generation      integer NOT NULL DEFAULT 1,
    status          text NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'retired')),
    retired_reason  text,
    retired_at      timestamptz,
    turn_count      integer NOT NULL DEFAULT 0,
    last_head_sha   text,
    last_base_ref   text,
    last_run_id     uuid REFERENCES automation_runs(id),
    last_turn_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_automation_target_sessions_active
    ON automation_target_sessions (org_id, automation_id, repository_id, target_kind, target_key)
    WHERE status = 'active';

CREATE INDEX idx_automation_target_sessions_session
    ON automation_target_sessions (org_id, session_id);

CREATE INDEX idx_automation_target_sessions_automation
    ON automation_target_sessions (org_id, automation_id, updated_at DESC);
```

`retired_reason` values: `manual_reset`, `pr_closed`, `session_unavailable`, `not_resumable`, `agent_config_changed`, `base_retargeted`, `turn_limit`, `snapshot_expired`, `continuity_disabled`.

### `automation_runs`

```sql
ALTER TABLE automation_runs
    ADD COLUMN session_id uuid REFERENCES sessions(id),
    ADD COLUMN continuation_mode text
        CHECK (continuation_mode IN ('fresh', 'continued', 'reconstructed')),
    ADD COLUMN previous_head_sha text,
    ADD COLUMN wait_reason text
        CHECK (wait_reason IN ('target_busy')),
    ADD COLUMN superseded_by_run_id uuid REFERENCES automation_runs(id),
    ADD COLUMN target_session_id uuid REFERENCES automation_target_sessions(id),
    ADD COLUMN restore_duration_ms integer,
    ADD COLUMN turn_duration_ms integer,
    ADD COLUMN warm_skipped_reason text
        CHECK (warm_skipped_reason IN (
            'node_budget', 'org_budget', 'automation_budget', 'disabled', 'snapshot_failed'
        ));

CREATE INDEX idx_automation_runs_session
    ON automation_runs (org_id, session_id, triggered_at DESC)
    WHERE session_id IS NOT NULL;

CREATE INDEX idx_automation_runs_waiting_target
    ON automation_runs (org_id, target_session_id, triggered_at DESC)
    WHERE status = 'pending' AND wait_reason IS NOT NULL;
```

`automation_runs.session_id` is the session that executed the run. `session_automation_links` keeps its current meaning: the run that created the session (origin ownership for session lists). For a fresh run both point at the same pair; for a continued run only `automation_runs.session_id` is set. The `skipped` run status already exists and gains the `superseded_by_run_id` provenance.

`restore_duration_ms` (container create plus snapshot restore, or clone plus bootstrap for fresh runs) and `turn_duration_ms` (agent start to agent exit) are Stage 1 columns. They are the inputs to the Stage 3 gate and are cheap to record from timestamps the orchestrator already has. `warm_skipped_reason` is a Stage 3 column.

### `session_sandbox_holders`

```sql
ALTER TABLE session_sandbox_holders
    DROP CONSTRAINT chk_session_sandbox_holders_holder_kind,
    ADD CONSTRAINT chk_session_sandbox_holders_holder_kind CHECK (holder_kind IN (
        'thread_runtime', 'preview', 'snapshot', 'operator', 'automation_warm'
    ));
```

`models.SessionSandboxHolderKind` gains `automation_warm`. This constraint change is Stage 3 only.

### Warm budget settings (Stage 3)

- Org settings JSON gains `automation_warm_sandbox_limit` (integer, default 5, range 0 to 100), managed through the existing versioned org settings path.
- Worker config gains `WORKER_MAX_WARM_SANDBOXES` (integer, default 0 meaning 25% of the effective `WORKER_MAX_ACTIVE_SANDBOXES`, minimum 1). Documented in the environment variables reference when Stage 3 ships.
- `container_usage_events` gains `purpose text NOT NULL DEFAULT 'turn' CHECK (purpose IN ('turn', 'automation_warm'))`. Historical rows default to `turn`. Usage rollups group by it so warm minutes are reported separately from turn minutes.

No other schema changes. `implemented/01-database-schema.md` is updated when each migration lands.

### Completion hook

`AutomationHooks.OnSessionComplete` currently resolves the run through the session's origin link. It changes to resolve the running run by `automation_runs.session_id = session.id AND status = 'running'`, falling back to the origin link for historical rows. It then updates the target row (`turn_count`, `last_head_sha`, `last_base_ref`, `last_run_id`, `last_turn_at`), creates the warm holder when configured, and dispatches the newest waiting run for the target. Other consumers of the session-to-run link that assume 1:1 are audited in the delivery plan.

## API Contract

### Automation create and update

`POST /api/v1/automations` and `PATCH /api/v1/automations/{id}` accept:

```json
{
  "session_continuity": "per_run" | "per_target",
  "warm_sandbox_minutes": 0,
  "max_warm_targets": 3
}
```

Validation (400, existing automation validation error code, `details.field` set):

- `per_target` requires at least one PR-scoped GitHub event trigger.
- `per_target` requires `publish_policy = "none"`.
- `warm_sandbox_minutes` requires `per_target` and must be 0 to 240 (Stage 3).
- `max_warm_targets` must be 0 to 50 (Stage 3).

Switching back to `per_run` retires every active target row for the automation with `continuity_disabled`. RBAC matches existing automation mutation rules. Responses include every field. Before Stage 3 ships, the warm fields are absent from requests and responses rather than accepted and ignored.

### Organization settings (Stage 3)

The existing org settings update route accepts `automation_warm_sandbox_limit` (integer, 0 to 100). Admin only, same versioning and conflict behavior as other org settings.

### Run list

`GET /api/v1/automations/{id}/runs` rows gain additive optional fields:

```json
{
  "session_id": "uuid",
  "continuation_mode": "fresh" | "continued" | "reconstructed",
  "previous_head_sha": "abc123",
  "wait_reason": "target_busy",
  "superseded_by_run_id": "uuid",
  "target_session_id": "uuid",
  "restore_duration_ms": 18400,
  "turn_duration_ms": 412000,
  "warm_skipped_reason": "org_budget"
}
```

`trigger_target` and `trigger_details` are unchanged. `warm_skipped_reason` appears only once Stage 3 ships.

### Target conversations

`GET /api/v1/automations/{id}/targets?status=active|retired&cursor=` (viewer or above):

```json
{
  "data": [{
    "id": "uuid",
    "repository_id": "uuid",
    "repository_full_name": "org/repo",
    "target_kind": "github_pull_request",
    "target_key": "1234",
    "pull_request_url": "https://github.com/org/repo/pull/1234",
    "session_id": "uuid",
    "generation": 2,
    "status": "active",
    "turn_count": 4,
    "last_head_sha": "abc123",
    "last_turn_at": "2026-09-19T10:00:00Z",
    "retired_reason": null
  }],
  "meta": {"next_cursor": null}
}
```

`POST /api/v1/automations/{id}/targets/{target_id}/reset` (member or admin): retires the active row with `manual_reset`, audits the action, returns the updated row with 200. 404 for unknown target, 409 `AUTOMATION_TARGET_NOT_ACTIVE` if already retired. A running turn is not cancelled; it finishes on the old session and no waiting run dispatches to it.

### Session detail

Session responses gain an optional `automation_target` object (`automation_id`, `automation_name`, `target_kind`, `target_key`, `pull_request_url`, `generation`, `turn_count`) when the session is an active or retired target session. No SSE changes; existing run polling and session streams carry the new state.

### Internal and external APIs

No change to session automation tools (doc 111) or the external API in this version.

## Delivery Plan

### Stage 1 — Continuity with snapshot continuation

- Migration, models, stores, and validation for the schema above.
- Run handler decision table, target locking, `continue_session` enqueue with `command_type = automation_rerun`, `automation_run_id`, `head_sha`, `previous_head_sha`, `pull_request_number`.
- Orchestrator `AutomationRerunContinueOptions`: fetch, clean-tree enforcement, checkout, SHA verification, delta computation, prompt rendering.
- Completion hook rewrite, waiting-run dispatch, supersede, wait timeout.
- Audit and adapt 1:1 session-to-run assumptions: `AutomationHooks`, `GoalImprovementService.OnSessionComplete`, PagerDuty writeback, Slack session notifications keyed by `AutomationRunID`, migration 248 no-change noop classification, session list ownership queries.
- Settings UI fields, run list badge, and run details reason.
- Worker env kill switch `AUTOMATION_SESSION_CONTINUITY_DISABLED=1` forcing fresh sessions without erroring.

Exit: a labelled PR with five pushes produces one session and five runs; the second and later runs skip clone and dependency install; Claude Code and Codex turns resume with native context; a destroyed snapshot or changed agent type produces the documented fallback; concurrent pushes coalesce.

### Stage 2 — Reconstruction and conversation surface

- PR-head reconstruction inside the same session with bounded transcript context.
- Targets endpoints, conversation list on the automation page, reset action, session-page attribution.
- Metrics: continuation rate by mode, time to first agent event fresh versus continued, tokens per run fresh versus continued, retire reasons.

Exit: no automation with continuity enabled leaves a run without a session or a session without a target row; reset works while a turn runs.

### Stage 3 gate — Is warm worth building?

Stage 3 is conditional. It is not scheduled until Stage 1 has run in production for at least two weeks on real labelled PRs and the numbers below justify it.

What Stage 3 can save is only the container create plus the snapshot download and untar. It cannot save clone, dependency install, the agent's cold read, or any tokens; Stage 1 already removes those. What Stage 3 costs is a live-container slot, host memory, and disk for the whole warm window on one node, plus node affinity: a continuation that lands on a different node pays the warm cost and still restores from snapshot.

Inputs, recorded per continued run in Stage 1:

- `restore_duration_ms` and `turn_duration_ms` on `automation_runs`.
- Snapshot size per session (already known at publish time).
- Gap between consecutive pushes per target, derivable from `triggered_at` on runs sharing a `target_session_id`.
- Node distribution of consecutive `continue_session` jobs for the same session, from existing job and executor records.

Build Stage 3 only if, over the measurement window:

- median `restore_duration_ms / turn_duration_ms` for continued runs exceeds 20%, **and**
- the median gap between pushes on the same target is under the smallest offered warm window (15 minutes) for at least a third of targets, **and**
- consecutive continuations for the same session land on the same node often enough that a warm hit rate above 50% is plausible.

If restore is slow mainly because snapshots are large, reduce snapshot size first (tighter excludes, dependency caches outside the checkpoint) and re-measure. That is cheaper than warm containers and benefits every session. If the gate fails, Stage 3 stays parked in this document with the measured values recorded in the decision log.

### Stage 3 — Warm sandboxes (conditional on the gate)

- `automation_warm` holder creation and expiry, best-effort admission against the three budgets, `warm_skipped_reason` recording.
- Holder-aware pressure eviction in the capacity gate: oldest warm holder first, `FinalizeContainerDestroy`, then admit the real turn.
- Separate `automation_warm` usage events and their appearance in the usage dashboard.
- Node affinity check: a continuation whose live container lives on another node falls back to snapshot restore rather than waiting.
- Settings UI for warm minutes, warm targets, and the org limit.

Exit: with a 15-minute warm window, a push within the window reuses the container on the owning node; a push after expiry restores from snapshot; a node, org, or automation at its budget skips the hold with the right reason; capacity pressure evicts the oldest warm container before refusing a real turn and never destroys a container with an active turn; warm time appears separately in usage.

## Validation and Acceptance

| Area | Tests |
|------|-------|
| Decision table | Table-driven unit tests for every row and failure action, including kill switch |
| Handler | First trigger creates session and target row; second trigger enqueues `continue_session` with the expected payload and sets `session_id`, `continuation_mode`, `previous_head_sha`; agent config change, base retarget, turn limit, and merged PR retire and go fresh |
| Concurrency | Real PostgreSQL test with two workers claiming runs for one target; busy target waits; newest waiting run dispatches; older runs marked superseded; wait timeout fails the run |
| Orchestrator | Fetch and checkout to head SHA; SHA mismatch fails the turn; dirty tree is discarded and logged; delta bounded; unreachable previous head yields full-review prompt; reconstruction path publishes a snapshot back onto the session |
| Hook | Completion resolves the run by executing session, updates the target row, records restore and turn durations |
| Gate inputs | `restore_duration_ms` and `turn_duration_ms` are populated for fresh, continued, and reconstructed runs |
| Warm budgets (Stage 3) | Node, org, and automation budgets each skip the hold with the matching `warm_skipped_reason`; a hold is never created before the snapshot is published |
| Warm eviction (Stage 3) | Pressure path evicts the oldest warm holder, finalizes and destroys it, then admits; a container with an active turn is never evicted; reaper expires holders on deadline |
| Warm billing (Stage 3) | Turn and warm usage events are distinct; warm event closes on release, expiry, and eviction |
| Prompt | Rendered template snapshot tests for continued, reconstructed, and no-history cases |
| Frontend | Settings validation messages, run badges, targets list, reset confirmation, mobile layout |
| Tenancy | `lint-stores` and `lint-schema` pass; cross-org target lookups return nothing |

Acceptance measurements before enabling by default for any template: median time from trigger to first agent event for continued runs versus fresh runs, tokens per continued run versus fresh run on the same PRs, and a sampled quality comparison of continued reviews against independent fresh reviews on the same head. The Stage 3 gate measurements above are collected in the same window.

## Rollout, Migration, and Recovery

- Deploy the migration first; it is additive. Old workers ignore the new columns and keep creating fresh sessions because `session_continuity` defaults to `per_run`.
- Enable continuity per automation. There is no global default flip in this design.
- Rollback: set automations back to `per_run` or set the worker kill switch. Active target rows are retired; waiting runs dispatch as fresh sessions. Keep the schema; the down migration is a disposable compatibility check only.
- A session executor or worker loss mid-turn recovers through the existing checkpoint path. The run stays `running` until the session reaches a terminal status, exactly as today.
- Stage 3, if built, rolls out with `WORKER_MAX_WARM_SANDBOXES` set explicitly per node and the org limit at its default. Setting the node value to 0 disables warm holds on that node without touching automations; existing warm containers drain through holder expiry.

## Risks

- **Anchoring on stale context.** A resumed agent may repeat or over-trust earlier findings. The continuation block frames the turn as a delta review and asks for explicit resolution of earlier findings. The turn limit and reset bound drift.
- **Hidden 1:1 assumptions.** Several paths treat a session and its automation run as one pair. The Stage 1 audit list is the mitigation; tests cover each adapted consumer.
- **Warm containers crowd out turns.** An idle warm container occupies the same admission slot as a running turn and cannot be reclaimed by today's pressure GC. The per-node budget, the holder-aware eviction path, and the rule that a real turn always wins are the mitigations; Stage 3 does not ship without all three.
- **Node affinity for warm containers.** Continuation on the wrong node silently loses the warm benefit while still paying for it. The Stage 3 gate requires evidence of same-node placement before building, and Stage 3 reports warm hit rate so the benefit is measured rather than assumed.
- **Snapshot growth.** Session checkpoints keep `.git` and build caches. Long-lived target sessions grow; `SESSION_MAX_SNAPSHOT_AGE` and the turn limit bound this.
- **Discarding local changes.** Enforced only for `publish_policy = none` automations, where uncommitted work has no product meaning. Extending continuity to publishing automations must revisit this rule.

## Non-Goals

- Sharing a session across multiple pull requests or multiple automations.
- Continuity for non-PR triggers in this version.
- Cancelling an in-flight turn when a newer push arrives. Waiting and coalescing are sufficient for review workloads; cancellation can follow the built-in reviewer's classification rules later.
- Reusing prior review results without running an agent. That is the built-in reviewer's scheduling design.
- Publishing pull requests from a per-target session.

## Decision Log

- **2026-09-19 — Session per target, run per trigger.** Runs keep their own rows and history so the automation page stays truthful about what fired; the session is the memory. Rejected: one run per PR with turns nested inside it, because it breaks run idempotency, dedupe, and analytics keyed on runs.
- **2026-09-19 — Wait and coalesce instead of cancel.** Review turns are short relative to push bursts, and cancellation requires the equivalence classification the built-in reviewer is still building.
- **2026-09-19 — Restrict to `publish_policy = none`.** Keeps workspace correctness a one-line rule (reset to head) instead of a changeset-aware merge of prior turns with new pushes.
- **2026-09-19 — Warm sandbox is a separate, conditional stage.** Snapshot restore already removes the clone, install, and cold-read cost, and warm saves no tokens. Warm containers add node affinity and capacity accounting that deserve their own measurements, so Stage 3 is gated on Stage 1 data (restore time as a share of turn time, push cadence, same-node placement) rather than scheduled.
- **2026-09-19 — Warm holds are best-effort and budgeted.** Because the snapshot is always published before a hold, skipping a hold is free of correctness risk. Three budgets (node, org, automation) plus oldest-first eviction keep idle containers from displacing real turns. Rejected: an unbounded pool with TTL only, because the current pressure GC cannot reclaim referenced containers and a busy tenant would starve every other tenant on the node.
