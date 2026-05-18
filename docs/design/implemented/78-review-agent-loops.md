# Design: Review Agent Loops

> **Status:** Implemented | **Last reviewed:** 2026-05-15
>
> **Depends on:** [implemented/48-automations-separation.md](../implemented/48-automations-separation.md), [backlog/11-review-feedback-loop.md](../backlog/11-review-feedback-loop.md), [implemented/40-pr-creation-revamp.md](../implemented/40-pr-creation-revamp.md), [implemented/68-sandbox-agent-tabs-and-threads.md](../implemented/68-sandbox-agent-tabs-and-threads.md), [implemented/64-session-composer-slash-commands.md](../implemented/64-session-composer-slash-commands.md)

## Implementation Summary

Implemented as a shared backend review-loop primitive with persisted
`session_review_loops` and `session_review_loop_passes` records, native
structured `/review` messages for Codex and Claude Code, one review thread per
loop, pass-limit enforcement, and automatic continuation from review to
decision to fix to confirmation review.

Manual session detail now exposes a `Review` action that starts a two-pass loop
in the current sandbox. Automations persist `pre_pr_review_loops`; new
automations default to one pass, existing rows backfill to zero, and the
`open_pr` worker gate starts or waits for the automation review loop before
publication. Clean automation loops enqueue PR creation again; loops that hit
the pass limit block PR creation for human decision.

## Problem

143 can produce a diff, show it in a strong review surface, validate it, and
open a PR. The missing quality loop is the back-and-forth developers already
perform manually:

1. ask the coding agent to review the current diff
2. let that same agent fix the issues it found
3. review again
4. repeat until the agent says the work is clean or the pass limit is reached

This should become a first-class capability for both manual sessions and
automations. The loop should not hide Codex and Claude Code behind a fake
generic review abstraction. It should run the selected agent's native review
command, especially `/review`, inside the same sandbox that will be validated
and shipped.

## Product Decision

Build a shared **review agent loop** capability.

For manual sessions, the session detail `Review` button starts the loop:

```text
User clicks Review
  ->
Create one review-loop tab in the current sandbox
  ->
The selected agent runs its native /review command
  ->
The same agent fixes the issues it found
  ->
The same agent runs /review again
  ->
Repeat until the agent reports the review is clean or max passes is reached
```

For automations, the same loop runs before PR creation when the automation's
pre-PR review setting is enabled.

The invariant is the same in both paths: this is one session, one sandbox, one
agent loop, one branch, one diff, and one eventual PR. Review loops improve the
current branch; they do not create child sessions, competing PRs, or separate
reviewer/fixer lanes.

## Goals

- Make pre-PR quality review a first-class manual session action.
- Let automations polish their own work before opening PRs.
- Use each selected agent's native review command instead of inventing a
  vendor-neutral command vocabulary.
- Run review and fix passes inside the current session sandbox so the agent
  sees the live filesystem, branch, credentials, preview state, and uncommitted
  changes.
- Create one visible review-loop tab for manual sessions so users can inspect
  what the agent reviewed, fixed, and re-reviewed.
- Support multiple back-and-forth passes with explicit cost/latency tradeoffs.
- Stop early when the agent reports the review is clean.
- Persist loop and pass records for UI, recovery, run history, settings
  enforcement, and analytics.
- Keep the final validation and PR path branch-level.

## Non-Goals

- Replacing human PR review.
- Running GitHub review webhooks through this loop. That remains the
  post-PR feedback loop in [backlog/11-review-feedback-loop.md](../backlog/11-review-feedback-loop.md).
- Running isolated agent bakeoffs. If the user wants independent attempts, they
  should create separate sessions.
- Letting review-loop tabs open separate PRs.
- Running review and fix concurrently. Review loops are always sequential.
- Normalizing each agent's review findings into a shared severity taxonomy. The
  product should preserve the selected agent's native review output.

## User Experience

### Manual session entry point

Session detail exposes `Review this work` in the Overview readiness area rather
than in the persistent header action cluster. It belongs with publish readiness
controls because the user is asking, "is this ready to ship?", but it remains a
secondary action so `Create PR` can stay the single primary shipping action.

Clicking `Review this work` opens a focused setup dialog with one real decision:
how many back-and-forth passes to allow. The dialog is used on mobile and desktop
so the setup controls do not depend on a popover inside the mobile details sheet.

```text
Review this work

Agent
Claude Code
Runs /review and fixes issues in this session's sandbox.

Review passes
[ - ] 2 [ + ]        Recommended
1 quick pass · 2 standard · 3+ deeper polish

[ Start review ]
```

Design choices:

- There is no separate reviewer/fixer selector. The selected agent owns both
  review and fix turns in the same review-loop tab.
- Default to `2` passes for manual sessions: review, fix, confirm.
- The pass stepper allows `1..5`. `2` is visually marked as recommended.
- The agent row is informational by default. It uses the current session agent
  unless the user explicitly changes the model through the normal session/model
  controls before starting the loop.
- The setup dialog must clearly state that the loop runs in the current sandbox.

### Manual session timeline

After starting, the Overview area shows a compact timeline:

```text
Review loop
Pass 1: reviewing in Claude Review
Pass 1: fixing issues
Pass 2: confirming
```

Completed example:

```text
Review loop
Pass 1 found issues and applied fixes
Pass 2 found no remaining issues
Ready for validation
```

Stopped example:

```text
Review loop
Pass 3 still reported issues
Pass limit reached
Needs human decision
```

Each row should jump to the relevant message in the review-loop tab.

### Review-loop tab

Starting a manual loop creates one tab such as `Claude Review` or `Codex
Review`.

The tab header should make the runtime location explicit:

```text
Claude Review • running • current sandbox
```

The first review message includes the native command:

```text
/review the current workspace diff. If you find issues, report them in your
normal review format. Then, when asked to fix, address the issues you found and
run relevant verification.
```

The command token is persisted through the existing structured slash-command
model. The adapter decides how to serialize it for the selected agent.

### Fix pass

The fix pass always runs in the same review-loop tab as the review pass. There
is no product option to choose another fixer.

The fix instruction should reference the agent's own previous review output:

```text
Fix the issues you identified in the previous review pass. Preserve the scope
of the current change. Add or update tests when appropriate. Run relevant
verification and report anything you could not verify.
```

The product does not need to parse every finding into a platform-owned schema
before sending the fix instruction. The selected agent already has its review
context in the same tab and should use its own findings as the source of truth.

### Automation settings

Automation create/edit uses the same pass-count UI language as manual sessions:

```text
Pre-PR review
Run the coding agent's review/fix loop before opening a PR.

Review passes
[ Off ]  [ - ] 1 [ + ]
1 quick pass · 2 standard · 3+ deeper polish
```

Recommended automation defaults:

- New automations default to `1`.
- Existing automations backfill to `0` to preserve behavior.
- High-risk templates can recommend `2` or `3`.
- The same hard cap of `5` applies.

### Automation run history

Automation run detail shows review loops in the run timeline:

```text
Implementation completed
Pass 1 reviewed and fixed issues
Pass 2 found no remaining issues
PR opened
```

If the run stops without a PR:

```text
Pass 3 still reported issues
Pass limit reached
Needs human review before PR creation
```

## Pass Count Options

The pass count is a product policy knob, not only a retry count. It controls
cost, latency, and how much polish the system should attempt before asking a
human to decide.

| Passes | Best for | Tradeoff |
| --- | --- | --- |
| `0` | Disabled for automations | No automated quality improvement |
| `1` | Fast sanity check before PR | Catches obvious issues but cannot confirm fixes |
| `2` | Default manual session review | Gives one review, one fix, and one confirmation review |
| `3` | Riskier changes or test-heavy work | More likely to converge, costs more time/tokens |
| `4-5` | Large refactors, security-sensitive changes | Useful but can churn; needs visible cost/time expectations |

Recommended defaults:

- Manual sessions: `2`
- Automations: `1`
- Hard cap: `5`

The loop always stops early when the agent reports the review is clean.

## Review Completion

Do not normalize review findings into platform severities. Codex, Claude Code,
and other supported agents have their own review language and may change their
finding formats over time. The product should preserve and display the native
review output.

### Nits and polish policy

Even though the product should not normalize findings into platform severities,
the loop must still be explicit about how to handle nits. The product rule is:
**fix low-risk local nits only**.

The review-loop prompt tells the agent:

```text
Fix nits when they are local, low-risk, and in files already touched by this
change. Do not expand the scope of the diff just to satisfy subjective style
preferences. If a nit is broad, risky, or unrelated to the current change,
leave it for later and mention it in your summary.
```

This keeps the default loop useful for polish without turning every review into
an unbounded cleanup pass. It is not exposed as a setup option in v1.

The only structured decision the platform needs is whether the loop should
continue. That decision should come from the coding agent, not from a generic
finding parser.

After each review pass, the loop asks the same agent for a bounded continuation
decision:

```text
Based on your latest review, are there remaining issues you should fix in this
sandbox before this work is considered clean? Apply the nit policy above when
deciding whether remaining nits require another fix pass. Answer with one of:

- REVIEW_CLEAN
- NEEDS_FIX_PASS
```

The UI can display the raw review output, the raw fix summary, and this compact
decision. It should not reclassify findings into `blocking`, `major`, `minor`,
or any other platform-wide severity scheme.

## Execution Model

For `passIndex` from `1..max_review_passes`:

1. Ensure the session has a live or hydrated sandbox.
2. Capture a loop checkpoint before the first review pass.
3. Create or reuse the review-loop thread.
4. Run the selected agent's native review command in that thread.
5. Ask the same agent whether the latest review is clean or needs a fix pass.
6. If the agent reports `REVIEW_CLEAN`, mark the loop `clean` and stop.
7. If the agent reports `NEEDS_FIX_PASS`, send the fix instruction in the same
   thread.
8. Wait for the fix pass to complete.
9. Capture a checkpoint and continue to the next review pass.

After the final allowed pass:

- if clean: mark `clean`
- if the agent still reports issues: mark `needs_human_decision`
- if a pass fails: mark `failed` with the failing pass and reason
- if canceled: mark `cancelled`

### Always sequential

Review loops must always be sequential. A loop never reviews and fixes at the
same time, and it never runs separate reviewer/fixer agents in parallel.

The sequential contract is part of the product model:

- `/review` inspects a stable workspace state
- fix turns address the immediately preceding review output
- confirmation review inspects the post-fix workspace state
- the transcript remains understandable because one agent owns the loop

### Checkpoints

The implemented loop records the session snapshot key available at loop start
as `loop_start_checkpoint_key` and `latest_checkpoint_key`. Per-pass checkpoint
capture remains a follow-up: the current loop relies on the existing
thread/session snapshot behavior after each turn.

## Running After PR Creation

Review loops are allowed at any point in the session lifecycle, including after
a PR already exists.

If a review loop runs after PR creation, it still modifies the session sandbox
and working branch. The app should treat those changes the same way it treats
other post-PR session edits:

- show the updated local diff
- keep the existing PR as the linked publish target
- require the user to use the existing `Push changes` action to update the
  remote PR branch
- do not auto-push review-loop changes directly to GitHub

This keeps review loops consistent with the current PR flow and avoids a hidden
side effect where a review button silently updates a remote branch.

## Future Review Policy Settings

The implemented automation gate requires a clean review loop when
`pre_pr_review_loops` is enabled for an automation. Broader org-level policy
settings remain future work: organizations should be able to require a clean
review loop before PR creation or merge-related actions, with role-specific
policy.

Recommended setting:

```text
Require review loop before PR

Builders        [ On  ]  Default on
Members         [ Off ]  Optional
Admins          Always allowed to bypass
```

Policy intent:

- **Builders** means 143-initiated builder/agent work. Default to requiring a
  clean review loop before PR creation.
- **Members / engineers** means write-capable human users. Default off, but
  org admins can require it if they want the same quality gate for human-started
  sessions.
- **Admins** can always bypass or accept the risk. Admins need an escape hatch
  for urgent fixes and broken review-loop infrastructure.

The UI should surface this as a clear gate near `Create PR` or `Push changes`:

```text
Review required
This workspace must complete a review loop before creating a PR.

[ Run review ]
```

For admins:

```text
Review required for this role
Admins may bypass this requirement.

[ Run review ] [ Bypass ]
```

The bypass should be audited with `org_id`, `session_id`, user ID, role, and a
short reason when provided.

## Data Model

### Automation config

Add:

```sql
ALTER TABLE automations
ADD COLUMN pre_pr_review_loops INT NOT NULL DEFAULT 0;
```

### Review requirement settings

Store role-specific policy in org settings or a dedicated settings table,
depending on the existing settings implementation at the time of build:

```json
{
  "review_loop_requirements": {
    "builders": true,
    "members": false,
    "admins_can_bypass": true
  }
}
```

### Review loop record

```sql
CREATE TABLE session_review_loops (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    automation_run_id uuid REFERENCES automation_runs(id),
    thread_id uuid REFERENCES session_threads(id),

    status text NOT NULL,
    source text NOT NULL, -- manual, automation
    agent_type text NOT NULL,
    max_passes integer NOT NULL,
    completed_passes integer NOT NULL DEFAULT 0,
    review_required boolean NOT NULL DEFAULT false,
    bypassed_by_user_id uuid REFERENCES users(id),
    bypass_reason text,

    loop_start_checkpoint_key text,
    latest_checkpoint_key text,
    latest_summary text,
    started_by_user_id uuid REFERENCES users(id),
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);
```

`automation_run_id` is null for manually started loops and set for automation
pre-PR loops.

### Pass records

```sql
CREATE TABLE session_review_loop_passes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES organizations(id),
    loop_id uuid NOT NULL REFERENCES session_review_loops(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    pass_index integer NOT NULL,

    review_message_id uuid REFERENCES session_messages(id),
    decision_message_id uuid REFERENCES session_messages(id),
    fix_message_id uuid REFERENCES session_messages(id),
    status text NOT NULL,
    agent_decision text,
    review_output text,
    fix_summary text,
    review_started_at timestamptz,
    review_completed_at timestamptz,
    fix_started_at timestamptz,
    fix_completed_at timestamptz,
    summary text
);
```

There is intentionally no platform-owned `session_review_findings` table in v1.
The agent's native review output is stored on the pass and transcript messages
as-is.

All tables include `org_id`, and every store method must filter by `org_id`.

## Service Architecture

Introduce a reusable service:

```go
type ReviewLoopService interface {
    Start(ctx context.Context, orgID uuid.UUID, sessionID uuid.UUID, req StartReviewLoopRequest) (*ReviewLoop, error)
    Continue(ctx context.Context, orgID uuid.UUID, loopID uuid.UUID) error
    Cancel(ctx context.Context, orgID uuid.UUID, loopID uuid.UUID) error
    BypassRequirement(ctx context.Context, orgID uuid.UUID, sessionID uuid.UUID, userID uuid.UUID, reason string) error
}
```

Responsibilities:

- validate the session is reviewable and has a restorable sandbox
- create or reuse the review-loop thread
- enqueue review, decision, and fix jobs sequentially
- persist loop/pass state
- enforce pass limits and role-based review requirements
- emit session events for manual timelines, tab transcripts, and automation run
  history

The service should not live inside HTTP handlers or the agent adapter. The
adapter knows how to run `/review`; the review-loop service knows when and why
to run it.

## API Shape

Manual session endpoints:

```text
POST   /api/v1/sessions/{session_id}/review-loops
GET    /api/v1/sessions/{session_id}/review-loops
GET    /api/v1/sessions/{session_id}/review-loops/{loop_id}
POST   /api/v1/sessions/{session_id}/review-loops/{loop_id}/cancel
```

Automation create/update/get payloads expose:

```json
{
  "pre_pr_review_loops": 1
}
```

Start request:

```json
{
  "agent_type": "claude_code",
  "model": "claude-sonnet-4-5",
  "max_passes": 2
}
```

## Prompt And Command Handling

The review pass is a normal user message in the review-loop thread with a
structured command entry:

```json
{
  "message": "/review the current workspace diff...",
  "commands": [
    {
      "kind": "command",
      "agent_type": "claude_code",
      "name": "review",
      "token": "/review",
      "display": "/review",
      "arguments": "the current workspace diff..."
    }
  ]
}
```

Prompt templates for review framing, fix instruction, and review completion
decision must live in `internal/prompts/templates/`.

Required templates:

- `review_loop_review.template`
- `review_loop_decision.template`
- `review_loop_fix.template`

Do not add a finding-normalization template. The platform stores and displays
the agent's native review output.

## Agent-Specific Behavior

Codex and Claude Code both expose `/review`, but they should not be treated as
semantically identical.

Adapter responsibilities:

- confirm whether the selected agent supports a native review command
- serialize the command in the form the CLI expects
- preserve the visible `/review` transcript
- capture raw review output, the agent's clean/needs-fix decision, and fix
  summaries for user inspection

If an agent does not support `/review`, hide it from the review-loop selector
in v1. A later version can expose `generic prompt review` if users ask for it.

## Validation And PR Readiness

A clean review loop does not replace platform validation. It advances the
session toward validation:

```text
Review loop clean
  ->
Run normal validation
  ->
Create PR / Push changes / repair failures / ask for human guidance
```

If policy requires a review loop and the latest loop is not clean, `Create PR`
or `Push changes` should be gated unless the user is an admin and chooses to
bypass. If no policy requires review, an unfinished review loop is advisory.

The PR health row should show the latest review-loop outcome near CI state so
the user can distinguish "tests are green" from "agent review still found work
to do."

## Options And Tradeoffs

### Agent selection

- **Current session agent:** simplest mental model and default.
- **Explicit model switch before starting review:** available through the
  existing session/model controls when a user wants another model to own the
  whole loop.
- **Separate reviewer/fixer agents:** intentionally not supported. It makes the
  product harder to understand and creates unclear ownership.

Recommended: default to the current session agent and keep one agent responsible
for both review and fixes.

### Auto-send versus prefill

- **Auto-send:** best button UX and the target behavior.
- **Prefill then user sends:** rejected for the implemented loop because it
  leaves automation runs without a durable continuation point.

Implemented behavior: auto-send the native review, decision, fix, and
confirmation prompts through the durable thread/message path.

## Rollout Status

### Phase 1: Visible manual review loop

- Rename/link the design as review loops, not automation-only review.
- Add `Review` button.
- Create one review-loop tab in the same sandbox.
- Send native `/review` as a structured slash command.
- Persist loop/pass records.
- Run one review pass and stop with the native output.

### Phase 2: Sequential fix and confirmation

- Add the agent decision step: `REVIEW_CLEAN` or `NEEDS_FIX_PASS`.
- Send fix instructions in the same review-loop tab.
- Run a second review pass after fixes.
- Stop early when clean.
- Support `max_passes` from `1..5`.

### Phase 3: Automation pre-PR review

- Add `pre_pr_review_loops` to automations.
- Run the same loop before automation PR creation.
- Persist review/fix passes for automation run history surfaces.
- Stop before PR creation when the agent still reports issues.

### Deferred follow-up: Policy and recovery

- Add role-specific review-loop requirement settings.
- Add admin bypass with audit logging.
- Capture and expose loop checkpoints.
- Add stop-after-current-pass and resume.
- Add org/project defaults for pass count.

## Testing Coverage

Backend:

- store tests for loop/pass queries with required `org_id` filtering
- handler/API coverage through route wiring and store-backed operations
- service tests for starting loops, dirty-then-clean, and clean automation
  loop PR resumption
- worker tests proving automation PR creation starts review before pushing
- prompt rendering tests for review, decision, and fix templates

Frontend:

- type coverage for review loop API/types
- session detail Review action wiring
- automation create/edit pass-count controls
- MSW coverage for review-loop API requests used by session detail tests

Verification follows the normal repo rules: `go vet ./...`, `go build ./...`,
`go test ./...` for backend changes, and frontend typecheck/lint/build for UI
changes.

## Deferred Follow-Ups

1. How much raw review output should be retained long term versus summarized
   after the session snapshot expires?
2. What is the right cost preview for 3-5 pass loops on large diffs?
3. Should manual review keep the full pass-count setup dialog, or add a
   fast-start default of two passes?
4. Should the review-loop tab be reusable for future review loops in the same
   session, or should each click create a fresh tab?

## Decision

Make review loops the shared manual-session and automation quality primitive:
one selected agent runs native `/review`, fixes its own findings in the same
tab, and repeats sequentially in the same sandbox until it reports clean or
policy says to stop.
