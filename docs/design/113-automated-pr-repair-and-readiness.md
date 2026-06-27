# Design: Automated PR Repair and Readiness Follow-Through

> **Status:** Partially Implemented | **Last reviewed:** 2026-06-27
>
> **Depends on:** [implemented/61-pr-state-sync-and-repair-actions.md](implemented/61-pr-state-sync-and-repair-actions.md), [implemented/78-review-agent-loops.md](implemented/78-review-agent-loops.md), [implemented/107-pr-readiness-checks.md](implemented/107-pr-readiness-checks.md), [implemented/88-shared-sandbox-thread-runtimes.md](implemented/88-shared-sandbox-thread-runtimes.md)

## Summary

143 should automatically take the next obvious step when a session is idle and a linked open PR has conflicts, broken tests, or a completed review loop waiting on readiness. Users should not have to watch the product and press routine buttons.

This is not a new agent capability. It is product follow-through on existing primitives:

- PR health already reports conflicts, failing checks, active repairs, mergeability, and repair context.
- `Resolve conflicts` and `Fix tests` already append structured repair prompts and enqueue `continue_session`.
- Review loops already persist terminal states.
- PR readiness already persists fresh, revision-bound checks and can wait for running review loops.

The goal is a calmer session Overview: show what 143 is doing, keep manual overrides available, and remove low-judgment sequencing work from the user.

## Problem

The current flow makes users act as schedulers:

1. Wait for the session to finish.
2. Notice `Resolve conflicts` or `Fix tests`.
3. Click the obvious repair button.
4. Wait again.
5. Notice a review loop completed.
6. Click `Check readiness`.

That adds friction without adding judgment. It also makes the Overview compete with too many loud calls to action: PR health, readiness, review, push, merge, branch, and repair controls all want attention.

## Product Decision

Add policy-controlled backend follow-through for two cases:

1. **Auto-readiness after review loops:** when a review loop reaches a configured terminal state, enqueue PR readiness for the current workspace.
2. **Auto-repair PR blockers:** when an idle/resumable session has a linked open PR with conflicts or failing tests, start the same repair action the button would start.

Auto-readiness ships first because it is read-only and already supports platform-triggered runs. Auto-repair ships later because it writes to the user's PR branch and requires a durable system-actor model.

Defaults are off for existing and new orgs until internal rollout proves this is reliable.

## Implementation Status

Phases 1-2 are implemented: the org and user settings contracts exist, validation covers the new enum/settings shapes, frontend types mirror the backend, the Organization settings page has a compact **Session automation** section, Account settings has personal inherit/on/off controls that display the current organization default, and PR readiness enqueueing now lives in a reusable backend service without changing runtime behavior.

Phases 3-8 remain planned. The next implementation chunk should add clean-loop auto-readiness behind the existing session automation policy, reusing the extracted readiness service.

## Principles and Boundaries

1. **Automate deterministic next steps, not judgment.** Conflicts, failed checks, and completed clean review loops are concrete signals. Bypasses, merges, and scope changes remain explicit decisions.
2. **Backend owned.** A browser tab should not need to be open.
3. **One visible workflow.** Automatic repair should look like 143 handled the existing product action, not like a separate background system.
4. **No loops without a budget.** Every automatic repair is bounded by `(PR head, action type, policy)`.
5. **Quiet UI, clear override.** Show the current automatic action and a stop/manual path; do not show duplicate primary buttons.
6. **Truth before optimism.** Durable state and dedupe records must land before UI implies work is running.

Non-goals: autonomous merge, readiness bypasses, concurrent repairs for one PR head, competing sessions/PRs, hidden automation, or automatic scope expansion when repairs fail.

## UX

### Session Overview

Use existing cards. Do not add a new tab or dashboard.

Manual state keeps the existing primary action: `PR health -> 2 failing checks -> Fix tests`.

Automatic state replaces that button with progress and a quiet escape hatch: `Fixing tests automatically... Stop auto-repair for this PR`. Conflicts use the same pattern: `Resolving conflicts automatically...`.

If automation is disabled, keep the current manual buttons. If automation tried the current head and hit its cap, return to a compact manual action: `Tests still failing after automatic repair -> Fix tests again`.

Never show a spinning automatic state next to the same enabled primary button. Users should not have to decide whether another click helps.

### Readiness After Review

When auto-readiness is enabled, the review/readiness card should progress naturally from `Review loop completed` to `Checking readiness...`, then to the final readiness state. Add a quiet reason line when useful: `Started after review loop completed`.

### Transcript and Activity

Automatic actions should be visible and clearly platform-authored:

- `143 started Fix tests because GitHub reported 2 failing checks.`
- `143 started Resolve conflicts because GitHub reported merge conflicts.`
- `143 started readiness checks after the review loop completed.`

Do not imply a human clicked the button.

### Settings

Organization defaults should live on the Organization settings page (`/settings`) in a new **Session automation** section, not under PR readiness, Agent, Runtime, or Integrations settings. This is session behavior: the trigger is "when this session becomes idle, decide the next automatic session action." PR health and readiness are inputs, but the policy is about session follow-through.

Place the section near other organization-level workflow defaults, ideally above the existing `Pull requests` section so admins see it as "what sessions do next" before "how PRs are created." The section should be compact: one read-only readiness default, then a visually separate branch-writing repair default group. Repair actions commit to and push the user's PR branch, so the first enablement for either write action needs a one-time confirmation.

Users also need a personal setting on the Account/Personal settings page. Each automatic action should offer `Use organization default`, `On`, and `Off`. `Use organization default` is the default choice and should display the effective org/repo value inline so users understand what will happen without opening organization settings.

Do not expose job types, dedupe keys, health versions, workspace modes, or attempt-count tuning in the main UI.

## Trigger Semantics

### Auto-Readiness

Trigger when a review loop reaches a configured terminal state for the current workspace. V1 defaults to `clean` only. `needs_human_decision`, `failed`, and `cancelled` are off: those states need visible human attention, and automatic motion risks signalling that the issue is handled.

Readiness evaluates the current session revision/snapshot. If a new turn changes the workspace before the job runs, existing freshness checks should mark the result stale or a new run should be created for the latest revision.

### Auto-Repair

Evaluate after a successful `continue_session` completion when the session has a linked open PR. This is the highest-signal v1 trigger: the session just became available for a continuation. PR-health-sync-triggered repair can be added later.

Because this hook runs on the hot path of session completion, do cheap local checks before calling `GetPullRequestHealth` or GitHub: policy enabled, linked open PR present, automatic budget available, and session idle/resumable. Only then read fresh enough PR health.

The decision must be bound to the current `head_sha`; if the PR head changes between health read and repair start, abort. After health read, require unblocked PR health, no active repair for current head/action, a still-present blocker, and a valid system actor path.

Action ordering is conflicts first, then tests. Do not start both. Conflicts can invalidate check results; let the next idle evaluation decide whether tests still need repair.

## Backend Design

### Readiness Runner

Move `SessionHandler.enqueuePRReadinessRun` into a reusable service used by HTTP, review-loop terminal transitions, workers, and future CLI/API triggers.

It should preserve current behavior: return an existing queued/running run, stamp workspace revision and snapshot key, create `pr_readiness_runs`, enqueue `run_pr_readiness` with dedupe key `pr_readiness:{session_id}`, and accept nullable platform attribution.

Review-loop terminal transitions need atomicity: if policy requires auto-readiness, marking the loop terminal and enqueueing readiness must succeed or fail together. Reuse the existing pattern behind `MarkPassCleanAndEnqueueOpenPR` rather than inventing a parallel mechanism.

### Auto-Repair Coordinator

Add a small coordinator around existing PR service behavior:

```go
type PRAutoRepairCoordinator interface {
    MaybeStart(ctx context.Context, orgID uuid.UUID, sessionID uuid.UUID, reason string) (*AutoRepairDecision, error)
}
```

Responsibilities: load session and linked PR, run cheap policy/budget checks before GitHub health reads, read head-consistent PR health, choose one action, call `StartPullRequestRepair`, record decision/audit state, and treat `ErrRepairAlreadyInProgress` / `ErrRepairSessionBusy` as handled no-ops.

Do not duplicate repair prompt construction, health enrichment, or `continue_session` enqueueing. Those stay in `StartPullRequestRepair`.

### Attribution Prerequisite

Auto-repair cannot ship until session messages support platform/system authorship.

Today `StartPullRequestRepair(ctx, orgID, pullRequestID, userID uuid.UUID, opts)` requires a real user and writes the repair prompt as a user message. Auto-repair must not falsely attribute branch-writing actions to the last viewer or a random owner.

Product default is `143 started Fix tests automatically`, not `Alex started Fix tests`.

Add the smallest durable actor model needed for system-authored session messages and audit details before building the auto-repair coordinator. Auto-readiness is not blocked because readiness runs already allow nullable `triggeredByUserID`.

### Attempt Limits

V1 invariant:

```text
At most 1 automatic attempt per (org_id, pull_request_id, head_sha, action_type)
```

This is a fixed safety rail, not a v1 setting. Manual clicks remain available after the automatic budget is consumed.

Extend `pull_request_repair_runs` rather than adding a new table. It already carries `head_sha` and `action_type`; add `triggered_by`, `trigger_reason`, and `auto_attempt`.

The `N = 1` case can rely on existing active-repair guards plus historical attempt lookup. Before raising `N`, add a race-safe budget constraint or locked budget read.

## Policy Model

Policy resolves in two layers:

1. **Organization defaults** are admin-owned and stored with organization settings under a session automation/follow-through settings object.
2. **User preferences** live in personal settings and can either inherit the organization default or explicitly force an action on/off for sessions and PRs the user is allowed to manage.

Do not put this inside the PR readiness policy sheet in v1. PR readiness defines evidence and enforcement before PR creation; automatic follow-through defines what an idle session should do next. Keeping those separate avoids making a session automation behavior feel like a PR-only quality gate. A future repo-specific override can live on repository settings if teams need per-repo behavior, but v1 should start with one organization default plus user inheritance/override.

Suggested organization settings shape:

```json
{
  "session_automation": {
    "automatic_follow_through": {
      "readiness_after_review_loop": false,
      "readiness_after_review_loop_states": ["clean"],
      "resolve_conflicts_when_idle": false,
      "fix_tests_when_idle": false
    }
  }
}
```

Suggested user preference shape:

```json
{
  "automatic_pr_follow_through": {
    "readiness_after_review_loop": "inherit",
    "resolve_conflicts_when_idle": "inherit",
    "fix_tests_when_idle": "inherit"
  }
}
```

Allowed user values are `inherit`, `on`, and `off`. The effective value is user preference over organization default. A user override cannot bypass role permissions, repository access, branch protection, attempt caps, or the system-actor prerequisite.

Defaults:

| Setting | Existing orgs | New orgs |
|---|---:|---:|
| Run readiness after clean review loop | off | off initially |
| Resolve conflicts when idle | off | off initially |
| Fix tests when idle | off | off initially |
| Max automatic attempts per head/action | 1 | 1 |

User defaults are `inherit`, so organization defaults remain the team baseline until a user deliberately chooses a personal preference.

## Audit, Notifications, and Metrics

Every automatic action needs an audit/event trail with org, repo, session, PR, head SHA, action type, trigger reason, policy source, actor, and outcome.

Notify on outcomes that need attention, not every start:

- automatic repair failed
- attempt budget exhausted
- readiness blocked after auto-run
- repair succeeded but a different blocker remains

Success metrics:

- fewer manual clicks from PR creation to reviewable state
- lower time from blocker detection to repair start
- fewer idle sessions with actionable PR blockers
- low duplicate-repair rate
- low stop/disable rate
- more PRs with current readiness evidence before human review

Counter-metric before customer rollout: **repair regret**, measured by humans reverting, force-pushing over, or redoing automatic repair work. Efficiency can improve while the feature quietly does the wrong thing; regret catches that.

## Rollout

Track A: auto-readiness, read-only, ships first.

1. Extract readiness enqueue service with no behavior change.
2. Add clean-loop auto-readiness behind a repo/org policy flag.

Track B: auto-repair, write action, gated on attribution.

1. Land durable system-actor support for session messages and audit.
2. Extend repair runs for automatic attempt accounting.
3. Add auto-repair coordinator behind a repo/org policy flag.

Shared:

1. Add quiet session UI states for automatic repair/readiness.
2. Enable internally for selected repos.
3. Expand to opt-in customer repos only after duplicate rate, disable rate, failed-loop recurrence, and repair regret are low.

## Test Plan

Backend:

- auto-readiness enqueues after clean terminal review loop
- auto-readiness does not enqueue after `needs_human_decision`, `failed`, or `cancelled` by default
- readiness enqueue is deduped and atomic with loop terminal transition
- auto-repair short-circuits disabled/no-PR/budget-exhausted cases before PR health reads
- auto-repair chooses conflicts before tests
- auto-repair aborts on stale head
- auto-repair no-ops on blocked health, active repair, or busy canonical session
- auto-repair respects per-head/action attempt cap
- manual repair still works after auto budget is exhausted

Frontend:

- automatic progress replaces duplicative manual buttons
- exhausted auto-repair returns a manual action with clear copy
- readiness card shows auto-start reason quietly
- settings separate read-only readiness from branch-writing repair actions

## Engineering Specification

### API Surface

Use existing settings endpoints. Do not add new public settings endpoints in v1.

- `GET /api/v1/settings` returns organization settings with `settings.session_automation.automatic_follow_through`.
- `PATCH /api/v1/settings` updates the organization default through the existing RFC 7386 merge-patch settings flow. Admin-only, same as other organization settings.
- `GET /api/v1/auth/me` returns user settings with `settings.automatic_pr_follow_through`.
- `PATCH /api/v1/auth/me/settings` updates personal inherit/on/off choices through the existing user settings merge-patch endpoint.
- Existing manual action endpoints remain unchanged: `POST /api/v1/pull-requests/{id}/repair/fix-tests`, `POST /api/v1/pull-requests/{id}/repair/resolve-conflicts`, and `POST /api/v1/sessions/{id}/pr-readiness-runs`.

Optional later API, only if the UI needs server-derived copy: `GET /api/v1/settings/session-automation/effective` returning the resolved org default plus current user's preference. V1 can compute display state client-side from `/settings` and `/auth/me`.

### JSON and Type Changes

Backend `internal/models`:

- Add `SessionAutomationSettings` to `OrgSettings`:
  - `SessionAutomation SessionAutomationSettings json:"session_automation,omitempty"`
- Add `SessionAutomationSettings`:
  - `AutomaticFollowThrough AutomaticFollowThroughOrgSettings json:"automatic_follow_through,omitempty"`
- Add `AutomaticFollowThroughOrgSettings`:
  - `ReadinessAfterReviewLoop bool json:"readiness_after_review_loop,omitempty"`
  - `ReadinessAfterReviewLoopStates []ReviewLoopStatus json:"readiness_after_review_loop_states,omitempty"`
  - `ResolveConflictsWhenIdle bool json:"resolve_conflicts_when_idle,omitempty"`
  - `FixTestsWhenIdle bool json:"fix_tests_when_idle,omitempty"`
- Add `AutomaticFollowThroughPreference` typed string: `inherit`, `on`, `off`, with `Validate()`.
- Add `AutomaticPRFollowThroughUserSettings` to `UserSettings`:
  - `AutomaticPRFollowThrough AutomaticPRFollowThroughUserSettings json:"automatic_pr_follow_through,omitempty"`
- Add `AutomaticPRFollowThroughUserSettings`:
  - `ReadinessAfterReviewLoop AutomaticFollowThroughPreference json:"readiness_after_review_loop,omitempty"`
  - `ResolveConflictsWhenIdle AutomaticFollowThroughPreference json:"resolve_conflicts_when_idle,omitempty"`
  - `FixTestsWhenIdle AutomaticFollowThroughPreference json:"fix_tests_when_idle,omitempty"`

Frontend `frontend/src/lib/types.ts` mirrors those shapes in `OrgSettings`, `UserSettings`, and `UserSettingsUpdateRequest`.

Effective resolution:

```text
effective = user preference if on/off, otherwise organization default
```

For background session-idle evaluation, resolve the user preference from `sessions.triggered_by_user_id`. If absent or unreadable, use the organization default. User overrides never bypass RBAC, repository access, branch protection, attempt caps, or system-actor requirements.

### Database / Schema

No migration is needed for organization or user settings because both live in existing JSONB settings documents.

Add one migration for automatic repair accounting:

```sql
ALTER TABLE pull_request_repair_runs
  ADD COLUMN auto_attempt boolean NOT NULL DEFAULT false,
  ADD COLUMN trigger_reason text NOT NULL DEFAULT '',
  ADD COLUMN triggered_by_source text NOT NULL DEFAULT 'manual',
  ADD COLUMN triggered_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX idx_pull_request_repair_runs_auto_attempts
  ON pull_request_repair_runs (org_id, pull_request_id, head_sha, action_type)
  WHERE auto_attempt = true;
```

Extend `models.PullRequestRepairRun` and `PullRequestStore.CreateRepairRun` to read/write these fields. The v1 cap is enforced by querying existing `auto_attempt = true` rows for the same `(org_id, pull_request_id, head_sha, action_type)`.

For system-authored repair prompts, do not overload a real user ID. Extend `SessionMessageSource` with `system_auto_repair` and allow `SessionMessage.UserID == nil` for that source while keeping `role = user` so the agent receives the repair prompt as user-directed instruction. Update transcript rendering to label it as 143/system authored.

### Services and Workers

Add `PRReadinessRunner` service by extracting `SessionHandler.enqueuePRReadinessRun`. It owns readiness run creation, dedupe, job enqueue, revision/snapshot stamping, and nullable attribution.

Add `PRAutoRepairCoordinator`:

```go
MaybeStart(ctx context.Context, orgID uuid.UUID, sessionID uuid.UUID, reason string) (*AutoRepairDecision, error)
```

Coordinator order:

1. Load session and linked PR.
2. Resolve org setting plus session owner's user preference.
3. Short-circuit disabled/no-PR/budget-exhausted/not-idle cases before PR health reads.
4. Read head-consistent PR health.
5. Choose `resolve_conflicts` before `fix_tests`.
6. Call `StartPullRequestRepair` with system source metadata.
7. Treat already-running/busy states as successful no-op decisions.

Worker hooks:

- After successful `continue_session` completion and repair-run completion bookkeeping, call `MaybeStart(..., "session_idle")`.
- After review-loop terminal `clean`, call `PRReadinessRunner.EnqueueRun(..., "review_loop_clean")` atomically with loop terminal transition.

### Implementation Phases

1. **Implemented - Settings types and UI shell:** added org/user settings types, validation tests, frontend types, Organization -> Session automation section, and Account personal controls. No automation behavior yet.
2. **Planned - Readiness runner extraction:** move readiness enqueue logic into a reusable service with no behavior change; update handler tests.
3. **Planned - Auto-readiness after clean review loop:** wire terminal clean loop -> readiness enqueue behind effective policy; add worker/service tests and quiet readiness UI reason.
4. **Planned - System-authored session messages:** add `system_auto_repair` source support, transcript labeling, and tests proving no real user attribution is required.
5. **Planned - Repair attempt accounting:** add migration/store/model fields and tests for per-head/action automatic attempt caps.
6. **Planned - Auto-repair coordinator:** implement eligibility, cheap short-circuits, head-SHA consistency, action ordering, and service tests. Keep policy default off.
7. **Planned - Session completion hook and UI states:** invoke coordinator after successful continuation, expose active/attempted/exhausted states in PR health UI, and hide duplicate manual buttons while automation is running.
8. **Planned - Internal rollout:** enable for selected internal repos/orgs, measure duplicate rate, disable rate, failed-loop recurrence, and repair regret before customer opt-in.
