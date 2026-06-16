# 76 - PR Overview Command Continuity

> **Status:** Implemented
>
> **Last reviewed:** 2026-05-18
>
> **Depends on:** [../overall.md](../overall.md), [../implemented/61-pr-state-sync-and-repair-actions.md](../implemented/61-pr-state-sync-and-repair-actions.md), [../implemented/74-pr-repair-in-progress-ux.md](../implemented/74-pr-repair-in-progress-ux.md)

## Summary

PR Overview actions should keep one canonical conversation: the session that opened the PR.

For agent-backed actions such as `Fix tests` and `Resolve conflicts`, the product behavior should be simple:

1. continue the original PR session
2. append a short user-visible command prompt
3. pass structured command context, such as failing test logs or conflict metadata
4. run the agent against a workspace that matches the current PR head

The complexity should stay inside workspace preparation. The user should not need to understand whether the backend reused a live container, hydrated a snapshot, or reconstructed a fresh workspace from the PR head branch.

## Problem

Today, `Fix tests` and `Resolve conflicts` resume the original PR session only when its persisted sandbox snapshot is directly usable. When it is not, the backend creates a child revision session. That child-session fallback protects workspace correctness, but it splits the transcript.

That creates user-facing ambiguity:

- the PR was opened from session `S`
- repair work may happen in child session `R`
- later follow-ups like `please fix these issues` often happen back in `S`
- the agent in `S` does not have the repair transcript from `R`

The fix is not to preserve child sessions more carefully. The fix is to stop creating child sessions for routine Overview commands.

## Principle

Session continuity and workspace correctness are separate concerns.

- **Session continuity:** command prompts, assistant replies, logs, diffs, and follow-ups belong to the original PR session.
- **Workspace correctness:** the agent must edit the exact code state the command is about.

The original PR session owns the conversation. The workspace source can vary per command and per runtime condition.

## Overview Commands

The PR health/Overview surface currently exposes these command classes:

- `Fix tests`: agent-backed repair command
- `Resolve conflicts`: agent-backed repair command
- `Push changes`: workspace publishing command
- `Merge`: direct GitHub mutation

Only agent-backed commands need prompt/context injection and agent execution. The non-agent commands should still follow the same ownership rule: they operate on the original PR session and never create child repair sessions.

Future Overview commands should follow the same classification:

- if the command asks the agent to change code, run it as a command-aware continuation of the original session
- if the command only publishes or mutates GitHub state, keep it session-bound but do not involve the agent

## Target Behavior

### Fix tests

When a user clicks the primary `Fix tests` action:

1. The original PR session receives a user message asking the agent to fix the failing tests and push changes to the PR branch.
2. The backend creates or reuses an active `pull_request_repair_runs` row for the current PR head SHA, while keeping the current health version as launch provenance.
3. The worker passes failing-test details through structured command context.
4. The worker prepares a workspace using either snapshot continuation or PR-head reconstruction.
5. The agent runs in the original session context.
6. Assistant output streams into the original session transcript.
7. The final diff, summary, token usage, and snapshot are persisted on the original session.

The dropdown alternative is labeled `Fix without pushing changes`; it uses the same repair flow but asks the agent to stop before pushing.

### Resolve conflicts

The primary `Resolve conflicts` action follows the same flow, with a different visible prompt and command context:

- visible prompt: ask the agent to resolve the conflicts and push changes to the PR branch
- context: PR number, repository, base SHA, head SHA, merge state, conflict flags, and conflict-resolution guardrails
- workspace requirement: checkout must match the current PR head before the agent starts

The dropdown alternative is labeled `Resolve without pushing changes`; it uses the same repair flow but asks the agent to stop before pushing.

Conflict repair should still suppress `Fix tests` and `Merge` for the same PR head SHA while active, matching the current in-progress behavior.

### Push changes

`Push changes` is not an agent command.

It should:

- push the original session's current workspace snapshot to the PR branch
- require a usable snapshot or live workspace
- keep all state attached to the original session
- publish or promote the post-push snapshot back onto the original session
- never create a child repair session

If no usable snapshot exists, the action should stay disabled or return the existing snapshot-not-captured failure. It should not silently reconstruct from PR head, because pushing is meant to publish the session's current work.

### Merge

`Merge` is not an agent command.

It should:

- call GitHub directly after PR-health merge gates pass
- use the existing merge auth and branch-protection handling
- avoid creating or resuming any agent session
- avoid writing conversational transcript messages unless a separate audit/user-visible event policy explicitly calls for it

## Command-Aware Continuation

Agent-backed Overview commands should use the existing continuation model with command metadata.

The repair launch request may include the caller's active `thread_id`. When present, the backend validates that the thread belongs to the canonical PR session and uses that thread for the visible user command, active repair attribution, and worker continuation payload. Older clients that omit `thread_id` fall back to the first-created session thread for compatibility, but product UI should always send the current active thread so the clicked tab becomes the go-forward conversation lane.

The continuation payload should carry enough information for the worker to select the correct workspace and build the correct prompt:

```json
{
  "job_type": "continue_session",
  "payload": {
    "org_id": "org-id",
    "session_id": "origin-session-id",
    "pull_request_id": "pr-id",
    "repair_run_id": "repair-run-id",
    "command_type": "fix_tests",
    "health_version": 12,
    "head_sha": "expected-head-sha",
    "thread_id": "active-thread-id"
  }
}
```

This keeps the abstraction small:

- `continue_session` remains the session turn executor
- command metadata tells it how to prepare the workspace and prompt
- thread metadata tells it which session tab owns streamed logs, final result metadata, and sibling-tab attribution
- repair-run state remains the durable PR-level in-progress marker

A separate `repair_pull_request` job is not required unless the implementation becomes cleaner with a thin wrapper. If a wrapper is introduced, it should delegate to shared continuation helpers rather than duplicate session execution.

## Workspace Selection

The worker should choose the simplest correct workspace source.

### Snapshot continuation

Use normal continuation when:

- `pending_snapshot_key` is empty
- `snapshot_key` is present
- `sandbox_state` is not `destroyed`
- the session status is resumable

This is the preferred path. It preserves the workspace and may preserve adapter-native runtime context.

### PR-head reconstruction

Use PR-head reconstruction when normal continuation is unavailable or untrusted.

This path should:

- create a fresh sandbox
- clone or fetch the PR head ref
- verify checked-out `HEAD` equals the PR health `head_sha`
- fail with a retryable stale-health result if the PR advanced
- run the agent with the visible command prompt and structured command context
- include bounded transcript context because adapter-native runtime resume is not available
- collect diff using the same base/target metadata expected by the Changes tab
- snapshot the final workspace and attach it to the original session

This is the only special behavior needed to make the clean model correct. The system continues the original session, but it does not blindly trust the original session's old workspace.

## Command Context

Visible prompts should stay short:

- `Please fix these tests.`
- `Please resolve the conflicts.`

Detailed context should be structured and generated by the backend:

- command type
- PR number and repository
- base SHA and expected head SHA
- merge state and conflict metadata
- failing check names, categories, annotations, log excerpts, and details URLs
- relevant command references

For PR-head reconstruction, add bounded transcript context:

- initial user request or issue summary
- latest assistant result summary
- recent command-related turns
- current PR health summary

Do not dump the full transcript by default. Keep the context deterministic and scoped to the command.

## Data Model

No new session relationship is required.

Recommended changes:

- Add `workspace_mode` to `pull_request_repair_runs`.
- Valid values: `snapshot_continuation`, `pr_head_reconstruction`.
- Keep `pull_request_repair_runs.session_id` pointing at the original PR session.
- Keep `pull_requests.session_id` as the canonical PR session.

Optional audit fields:

- `workspace_head_sha`
- `workspace_base_sha`
- `workspace_mode_details jsonb`

Do not add `current_repair_session_id` for this model. The current repair session is always the PR session.

## API Behavior

Repair launch responses should keep returning the effective session ID, but it should be the original PR session for routine Overview repairs.

Recommended `PullRequestRepairResponse.mode` values:

- `resumed`: snapshot/live continuation
- `reconstructed`: PR-head reconstruction inside the original session
- `existing`: active repair run already exists

`GET /pull-requests/:id/health` should continue exposing `active_repairs`. Those active repairs should point to the original PR session, so `Open repair session` consistently opens the PR conversation.

## State Transitions

On `Fix tests` / `Resolve conflicts` launch:

- create or reuse a `pull_request_repair_runs` row
- append the visible command prompt to the original session
- set the original session to `running` if a new command turn starts immediately
- enqueue command-aware `continue_session`

On success:

- append assistant final output to the original session
- persist result summary, diff, token usage, and snapshot on the original session
- request PR-health sync so CI/mergeability can clear active repair state

On stale PR head:

- mark the repair attempt obsolete or retryable for that health version
- request PR-health resync
- do not run the agent against the wrong checkout

On unrecoverable failure:

- surface the failure in the original session
- mark the repair run inactive or failed according to existing repair-run lifecycle rules

## Concurrency

Only one active repair should run for a given PR head SHA and action type.

Existing `pull_request_repair_runs` dedupe should remain the PR-level authority. The original session still needs normal session turn locking:

- do not start a new command turn while the session is already running unless existing queued-message semantics can safely accept it
- if a user sends a normal follow-up during a command turn, preserve it and drain it through existing continuation behavior
- if PR health advances mid-command on the same head SHA, keep suppressing duplicate CTAs; if the PR head SHA changes, let the running turn finish but stop suppressing CTAs for the newer branch state

## Migration Plan

Phase 1: Instrument the current flow.

- Add `workspace_mode` to `pull_request_repair_runs`.
- Record when the existing path uses snapshot continuation versus child revision fallback.
- Log why original-session continuation was rejected.

Phase 2: Add command-aware continuation.

- Add command metadata to `continue_session` payloads.
- Teach continuation to build command context for `fix_tests` and `resolve_conflicts`.
- Keep existing child fallback as a safety valve while this path matures.

Phase 3: Add PR-head reconstruction.

- Implement checkout/fetch of the expected PR head.
- Validate checked-out `HEAD` against PR health `head_sha`.
- Publish reconstructed snapshots back onto the original session.
- Add tests for missing snapshot, destroyed snapshot, pending snapshot, stale head SHA, and successful snapshot publication.

Phase 4: Remove routine child-session fallback.

- Route `Fix tests` and `Resolve conflicts` through original-session command continuation.
- Keep child/fork sessions only for explicit user divergence.
- Update UI copy so `Open repair session` points to the original PR session.
- Confirm `Push changes` and `Merge` continue to use their existing non-child-session paths.

## Test Plan

Backend tests:

- `Fix tests` with a valid snapshot uses `snapshot_continuation`
- `Fix tests` without a usable snapshot uses `pr_head_reconstruction`
- `Resolve conflicts` uses the same original-session command path
- PR-head reconstruction validates expected head SHA
- stale head SHA triggers retry/resync and does not invoke the agent
- command messages and assistant messages are written to the original session
- repair runs point at the original session
- successful reconstruction publishes a new snapshot on the original session
- `Push changes` does not create child sessions
- `Merge` does not enqueue agent jobs

Frontend tests:

- clicking `Fix tests` keeps the user in or routes to the original PR session
- clicking `Resolve conflicts` keeps the user in or routes to the original PR session
- active repair state points to the original session
- `Open repair session` opens the original PR session
- no child-session navigation occurs for routine Overview commands
- `Push changes` and `Merge` do not navigate to child sessions

Integration tests:

- a PR created from a session with no post-PR snapshot can still run `Fix tests` in the original transcript
- repeated repair commands do not create additional sessions
- after PR-head reconstruction succeeds, the next follow-up can use snapshot continuation

## Risks

The main risk is treating PR-head reconstruction as equivalent to adapter-native runtime resume. It is not. The reconstructed path loses opaque agent runtime context, so the prompt must carry bounded transcript and repair context.

Another risk is diff drift. The reconstructed sandbox must stamp the same base and target branch metadata used by session diff collection. Otherwise the Changes tab can show an inflated or empty diff.

The complexity should remain isolated to workspace preparation. If command-aware continuation starts duplicating the entire `continue_session` flow, the design has drifted.

## Non-Goals

- Do not merge child-session transcripts back into the original session.
- Do not remove deliberate fork/revision sessions.
- Do not rely on generic no-snapshot continuation for PR repair.
- Do not run repair against a branch unless the checked-out head SHA matches the PR health snapshot.
- Do not turn direct GitHub actions such as `Merge` into agent commands.

## Recommendation

Adopt command-aware continuation for PR Overview actions.

The target abstraction is:

- one canonical transcript: the original PR session
- one command executor for agent-backed Overview actions: continuation with command context
- two workspace sources for agent-backed commands: snapshot continuation or PR-head reconstruction
- one correctness rule: the agent edits the exact PR head the command refers to

This gives users the simple model they expect while keeping the engineering risk concentrated in a small, testable workspace-preparation layer.
