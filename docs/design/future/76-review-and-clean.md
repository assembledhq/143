# Design: Review and Clean Action

> **Status:** Not Started | **Last reviewed:** 2026-05-08
>
> **Related docs:** [../overall.md](../overall.md), [../implemented/68-sandbox-agent-tabs-and-threads.md](../implemented/68-sandbox-agent-tabs-and-threads.md), [../implemented/40-pr-creation-revamp.md](../implemented/40-pr-creation-revamp.md), [../backlog/11-review-feedback-loop.md](../backlog/11-review-feedback-loop.md)

## Summary

143 should ship a first-class `Review and clean` action inside session detail.

When invoked, it runs a bounded multi-step workflow over the existing session:

1. create or reuse a dedicated reviewer thread
2. send `/review` into that thread
3. wait for the review to complete
4. if the reviewer found issues, send `Please fix the issues you identified.` to the implementation thread
5. optionally repeat review -> fix for a small number of passes
6. run a final cleanup pass that simplifies the code without changing behavior

This should ship as a **pre-built product action with strong defaults**, not as a raw prompt macro. Advanced teams can customize prompts and limits through repo config, but the default experience must work without setup.

## Problem

Today users can create extra threads manually and type follow-up prompts themselves, but the workflow is tedious and inconsistent:

- users must remember the right sequence of review, fix, and cleanup prompts
- they must manually create and switch to a separate reviewer tab
- they must manually decide when review is complete enough to continue
- repeated review/fix loops are awkward and easy to abandon halfway through
- there is no product-owned progress model, cancellation model, or success state

This is especially painful for builders and lighter-weight users, who often want a trustworthy "make this PR-ready" action rather than direct low-level control over every agent message.

## Goals

- Provide a single high-trust `Review and clean` action for interactive coding sessions
- Make code review and cleanup feel like one coherent operation, not three separate prompts
- Preserve the user's existing implementation thread as the source of truth for actual edits
- Use a separate reviewer thread so review remains legible and does not pollute the implementation transcript
- Keep the workflow bounded, inspectable, cancellable, and safe
- Support repo-level customization without requiring config for basic use

## Non-Goals

- This is not a generic workflow builder in v1
- This is not a replacement for GitHub PR review
- This is not the same system as post-PR review feedback ingestion from GitHub
- This does not guarantee code is perfect; it improves readiness and cleanliness before PR creation
- This should not expose arbitrary tool automation or arbitrary shell execution through config

## Product Decision

The product strategy should be:

- **Ship a pre-built `Review and clean` action**
- **Back it with a small workflow engine**
- **Allow repo-level customization of prompts and limits**
- **Defer fully user-defined custom buttons until the execution model is proven**

This is intentionally opinionated.

The user problem is orchestration, not text expansion. A plain configurable prompt button is too weak because it cannot reliably represent:

- spawning or targeting a separate thread
- waiting for agent completion
- passing findings from one thread to another
- bounded loops
- product-owned progress and cancellation state

## Primary UX

### Placement

Add `Review and clean` as a session-level action in the same area that already owns actions like PR creation and repair flows. It should feel like a first-class step on the path to a clean branch and PR, not like an advanced thread management tool.

### Default behavior

When the user clicks `Review and clean`:

1. the product starts a workflow run attached to the session
2. the UI immediately shows stage-based progress
3. a reviewer thread is created if one does not already exist
4. the reviewer thread receives `/review`
5. the implementation thread stays visible by default, but the user can jump to the reviewer thread at any time

Recommended stage labels:

- `Starting review`
- `Reviewing code`
- `Fixing review findings`
- `Re-reviewing`
- `Cleaning up code`
- `Done`

### User-visible controls

The workflow surface should expose:

- current stage
- current pass count
- link to open the reviewer thread
- link to open the implementation thread
- cancel action while the workflow is active
- final outcome summary

It should not expose prompt internals in the main happy path.

### Final completion state

On success, show a compact summary such as:

- review passes completed
- whether findings were fixed
- whether cleanup ran
- suggested next action: `Create PR`

If the workflow stops early because review failed, fix application failed, or the user canceled, preserve partial progress and make the stopping reason explicit.

## Workflow Model

The action should be implemented as a bounded workflow with persisted state, not a one-shot macro.

### Actors

- **Implementation thread:** the thread that owns the real code changes
- **Reviewer thread:** a separate thread used only for inspection and critique
- **Workflow run:** a session-scoped orchestration record that tracks state across page refreshes

### Baseline workflow

```text
Start
  ->
Create/reuse reviewer thread
  ->
Send /review to reviewer thread
  ->
Wait for reviewer completion
  ->
Did reviewer identify actionable issues?
  | yes
  v
Send fix prompt to implementation thread
  ->
Wait for implementation completion
  ->
Pass limit reached?
  | no
  v
Send /review again
  ->
Wait for reviewer completion
  ->
No actionable issues
  ->
Send cleanup prompt to implementation thread
  ->
Wait for implementation completion
  ->
Complete
```

### Bounded loop

Default to `max_review_passes = 2`.

The cleanup step should run only after the latest reviewer outcome is `clean`.

Rationale:

- one pass catches the obvious issues
- a second pass handles cases where fixes introduce follow-up review comments
- more than two passes risks feeling slow, expensive, and indecisive

If the pass limit is reached and the reviewer still finds issues, end in a non-success terminal state such as `needs_human_judgment` rather than looping indefinitely.

## Review Findings Contract

The workflow needs a product-owned way to decide whether a review found actionable issues.

V1 recommendation:

- require the reviewer prompt or `/review` flow to emit structured end-state metadata
- store a normalized `review_outcome` on the reviewer thread turn or workflow step

Preferred states:

- `clean`
- `issues_found`
- `inconclusive`

Why this matters:

- scraping free-form prose for "looks good" is fragile
- the orchestration layer should not depend on prompt wording quirks
- future agents and skills need the same contract

If existing `/review` support does not yet emit structured outcomes, add that contract before or during implementation of this feature.

## Configuration Model

The default experience should require no setup, but repos may override selected behavior through `.143/config.json`.

Illustrative shape:

```json
{
  "buttons": {
    "review_and_clean": {
      "enabled": true,
      "review_prompt": "/review",
      "fix_prompt": "Please fix the issues you identified.",
      "cleanup_prompt": "Please simplify the code, remove unnecessary complexity, and make it as clean as possible without changing behavior.",
      "max_review_passes": 2,
      "reuse_existing_reviewer_thread": true
    }
  }
}
```

### Configuration principles

- the button exists even without config
- config only overrides safe parameters
- prompts remain text-based and scoped to this workflow
- config must not allow arbitrary branching logic in v1

This deliberately stops short of a fully generic user-defined button system. The broader configurable-action framework can come later after this workflow proves out.

## Thread Strategy

### Separate reviewer thread

The review should happen in a separate thread by default.

Benefits:

- review commentary stays readable and isolated
- implementation transcript stays focused on making changes
- users can inspect the reviewer independently
- the same reviewer lane can be reused across multiple passes

### Thread creation behavior

If no reviewer thread exists, create one with a stable label like `Review`.

If one already exists and is clearly associated with this implementation thread, reuse it by default. Reuse reduces thread sprawl and makes the conversation history coherent.

## Failure and Cancellation Semantics

The workflow must survive refreshes and partial failures.

Terminal states should include:

- `completed`
- `completed_with_warnings`
- `needs_human_judgment`
- `failed`
- `canceled`

Examples:

- reviewer thread creation fails -> `failed`
- reviewer reports `inconclusive` -> `needs_human_judgment`
- fix pass errors out -> `failed`
- loop cap reached with remaining issues -> `needs_human_judgment`
- cleanup prompt fails after review/fix succeeded -> `completed_with_warnings`

Cancellation should stop the active step but should not delete created threads or erase prior messages.

## Guardrails

To keep the product safe and predictable:

- only one active `Review and clean` workflow may run per session at a time
- the workflow must target explicit thread IDs, never "whichever thread is active right now"
- loops must be capped
- the cleanup prompt must explicitly avoid behavioral changes
- the product should surface that cleanup may still modify code and should be reviewed like any other agent change

If the session already has another exclusive workflow running, disable the button and explain why.

## Analytics and Success Metrics

Track at least:

- button invocation rate
- workflow completion rate
- cancellation rate
- average number of review passes
- percent of runs ending `clean` vs `needs_human_judgment`
- downstream PR creation rate after completion
- merge rate for sessions that used the action vs similar sessions that did not

Qualitative success signals:

- fewer manual reviewer-thread creations for the same session
- higher builder confidence in shipping code
- better PR readiness with less prompt-writing burden

## Rollout Plan

### Phase 1: Pre-built action only

- ship the button with hardcoded defaults
- add workflow state, progress UI, and cancellation
- use one reviewer thread and one cleanup pass

### Phase 2: Repo-level customization

- allow `.143/config.json` overrides for prompts and loop count
- allow repo config to control whether builders see the action more prominently than other roles
- add validation and sensible defaults

## Future Directions

- reuse the same workflow primitives for other pre-built actions
- only then consider limited custom buttons or skill-backed actions

## Open Questions

1. Should the reviewer always be the same agent/provider as the implementation thread?
   Recommendation: default to the same agent class first; add explicit reviewer-agent overrides later.

2. Should builders get this action more prominently than members/admins?
   Recommendation: yes in the default product experience, with Phase 2 repo config able to tune that visibility.

3. Should the system create a PR automatically after successful completion?
   Recommendation: no. Keep `Create PR` as a separate explicit user action.

## Decision

143 should implement `Review and clean` as a first-class session action backed by a persisted workflow. The shipped UX should be opinionated and easy to trust, while the underlying design should leave room for repo-level customization and future reusable action primitives.
