# 74 - PR Repair In-Progress UX

> **Status:** Implemented | **Last reviewed:** 2026-05-07

## Goal

When an operator clicks `Fix tests` or `Resolve conflicts`, the PR health row should stop presenting that same action as available while the repair session for the current PR state is still running.

This is not just a button-loading problem. The product needs a durable PR-level notion of "a repair action is already in progress for this exact snapshot" so the UI remains correct after:

- the initial mutation completes
- a page refresh
- navigation into or out of the repair session
- a second viewer opening the same PR

## Problem

Today the UI only holds a short-lived `pendingAction` value in local mutation state. That covers the round-trip while the repair API call is in flight, but not the steady state afterward.

The backend already persists repair runs in `pull_request_repair_runs` and already dedupes repeated launches through `reused_in_flight`. The gap is that `GET /api/v1/pull-requests/:id/health` does not expose that durable in-progress state back to the client.

Result: the UI briefly shows `Opening repair session…`, then returns to a clickable `Fix tests` or `Resolve conflicts` button even though a repair session is already running.

## Product behavior

### Core rule

For the PR's current head SHA, if there is a non-terminal active repair run for an action, that action must not render as clickable in the PR health row. The repair row still records `health_version` as launch provenance, but same-head health-version churn must not re-enable the repair CTA while the repair is still running.

### Desired steady-state UX

If a `fix_tests` repair run is active for the current PR head SHA:

- do not render the `Fix tests` button
- render a non-clickable status surface: `Fix tests running`
- if the active session is not the session currently being viewed, render `Open repair session`

If a `resolve_conflicts` repair run is active for the current PR head SHA:

- do not render `Resolve conflicts`
- do not render `Fix tests`
- render a non-clickable status surface: `Resolve conflicts running`
- if the active session is not the session currently being viewed, render `Open repair session`

If there are no active repair runs for the current PR head SHA:

- render the normal eligible actions based on PR health (`can_fix_tests`, `can_resolve_conflicts`, `can_merge`)

If the PR advances to a newer head SHA:

- older active repair runs no longer suppress current actions
- the existing stale-context signal (`newer repair context available`) remains responsible for telling the user an older repair session exists against outdated PR state
- a newer `health_version` on the same head keeps the running repair visible

### Why `resolve_conflicts` suppresses both repair CTAs

Conflict resolution is the more fundamental repair action. The existing PR-health design already prioritizes `Resolve conflicts` over `Fix tests`, because resolving conflicts can invalidate the prior CI result. Once a conflict-repair session is running, keeping `Fix tests` visible for the same PR state creates misleading parallelism.

### Merge behavior

While any repair run is active for the current PR head SHA, the PR health row should not offer `Merge`.

The goal is to keep the row internally consistent: a PR should not simultaneously present "repair is already running for this state" and "merge now" as peer actions.

## State model

The source of truth should stay on the PR-health API response.

Suggested response shape:

```json
{
  "pull_request_id": "uuid",
  "health_version": 12,
  "active_repairs": [
    {
      "action_type": "fix_tests",
      "session_id": "uuid",
      "session_status": "running",
      "health_version": 12
    }
  ]
}
```

Suggested server model:

```go
type PullRequestActiveRepair struct {
    ActionType    PullRequestRepairActionType `json:"action_type"`
    SessionID     uuid.UUID                   `json:"session_id"`
    SessionStatus string                      `json:"session_status"`
    HealthVersion int64                       `json:"health_version"`
}
```

Suggested response field:

```go
ActiveRepairs []PullRequestActiveRepair `json:"active_repairs,omitempty"`
```

## Request and render flow

```text
User clicks Fix tests
        |
        v
POST /pull-requests/:id/repair/fix-tests
        |
        +--> backend reuses existing active run OR creates a new one
        |
        v
frontend shows optimistic "Opening repair session…"
        |
        v
frontend refetches GET /pull-requests/:id/health
        |
        v
response includes active_repairs for current head SHA
        |
        v
banner replaces CTA with "Fix tests running" + optional "Open repair session"
```

This is the key handoff:

- mutation-local loading handles the click itself
- API-backed `active_repairs` handles the steady state afterward

## How the UI leaves the in-progress state

The preferred path is event-driven, not client polling.

When the repair session pushes code and CI reruns, the PR health row should update through the existing GitHub-sync pipeline:

```text
repair session pushes changes to PR branch
        |
        v
GitHub emits pull_request.synchronize and later check_run / check_suite events
        |
        v
143 enqueues sync_pull_request_state for that PR
        |
        v
backend writes the new health snapshot and recomputes active_repairs
        |
        v
backend publishes pull_request.updated over the org-scoped SSE stream
        |
        v
open clients invalidate/refetch that PR health query
        |
        v
banner exits "Fix tests running" when the current health response no longer has
the matching active repair or no longer needs repair for that head SHA
```

This keeps the steady-state client cheap:

- no tight polling loop from the browser
- no repeated DB reads just to discover nothing changed
- updates arrive when GitHub tells us the PR actually changed

We should still keep the existing backend reconciliation job for missed webhooks or delivery gaps, but that is a low-frequency safety net, not the primary UX path.

## Backend specification

### Data source

Use `pull_request_repair_runs` as the durable record of launched PR repair work.

The backend should treat a repair run as active for banner purposes only when all of the following are true:

1. `pull_request_id` matches the current PR
2. `head_sha` matches the current PR health response
3. `active = true`
4. the linked session is non-terminal

Terminal session statuses should not suppress the CTA. The banner should return to normal eligibility as soon as the repair session is no longer running.

### Query shape

Add a store method that lists active repair runs for a PR and head SHA. The method should be org-scoped and return all active rows, not just one action type.

Suggested shape:

```go
func (s *PullRequestStore) ListActiveRepairRunsByHead(
    ctx context.Context,
    orgID uuid.UUID,
    pullRequestID uuid.UUID,
    headSHA string,
) ([]models.PullRequestRepairRun, error)
```

`buildPullRequestHealthResponse` should then:

1. build the normal PR health response
2. load active repair runs for `pr.ID` and the response `HeadSHA`
3. load each linked session status
4. discard runs whose sessions are terminal
5. populate `ActiveRepairs`
6. derive `CanMerge` after active repairs are known, so merge is suppressed when needed

### Event-driven refresh after CI changes

We should not add a new frontend polling loop just to clear `Fix tests running`.

Instead, after a repair session changes the PR branch:

1. a successful repair turn marks its repair row inactive and emits `pull_request.updated` on the existing org-scoped SSE channel, so clients can immediately refetch even before GitHub emits a webhook
2. GitHub webhook events (`pull_request.synchronize`, `check_run`, and `check_suite`) enqueue the normal PR-health sync job
3. the sync job writes the latest health snapshot and recomputes `ActiveRepairs`
4. the backend emits the normalized `pull_request.updated` event on the same org-scoped SSE channel
5. subscribed clients refetch only the affected PR health query

That means the "tests finished" transition is driven by the same event pipeline that already updates mergeability, failing checks, and repair eligibility.

`ActiveRepairs` should be recomputed from the latest DB state on every PR-health sync write. In practice this means:

- when the repair turn completes, the backend deactivates the linked repair row and publishes `pull_request.updated`
- when a new push creates a newer head SHA, older repair runs stop suppressing current actions
- when checks turn green, the banner naturally falls back from `Fix tests running` to the next healthy or mergeable state

### Session terminality

Use the same terminal-session rules already relied on by repair-launch dedupe logic. This should not invent a second definition of "in progress."

### Obsolete runs

Keep `ObsoleteActiveRepairSessions` separate from `ActiveRepairs`.

They answer different questions:

- `ActiveRepairs`: is a repair currently running for this PR head SHA?
- `ObsoleteActiveRepairSessions`: is there an older repair session that no longer matches the current PR state?

## Frontend specification

### API types

Add `active_repairs` to `PullRequestHealthResponse` in `frontend/src/lib/types.ts`.

### Banner rendering

`PRHealthBanner` should derive display state in this order:

1. transient mutation state (`pendingAction`)
2. durable API state (`active_repairs`)
3. base eligibility (`can_fix_tests`, `can_resolve_conflicts`, `can_merge`)

That ordering prevents flicker:

- the click immediately shows progress
- the subsequent health refetch preserves the non-clickable state
- the CTA only returns if the repair truly ended or the PR state changed

### Transport

The session detail page should stay subscribed to the existing org-scoped SSE stream for PR updates.

On a matching `pull_request.updated` event for the currently displayed PR:

- invalidate/refetch `["pull-request", pullRequestId, "health"]`
- do not start a dedicated timer-based polling loop for this feature

This keeps the browser work proportional to actual PR events rather than wall-clock time.

### UI structure

Recommended action-row rendering:

```text
[Fix tests running] [Open repair session]
```

or

```text
[Resolve conflicts running] [Open repair session]
```

The first element is status, not a button. It can be a muted badge-like surface or disabled button styling, but it must not be clickable and must read as "work already started."

### Same-session behavior

If the active repair session matches the currently viewed session:

- do not render `Open repair session`
- keep the in-progress status text only

If the active repair session differs:

- render `Open repair session`
- route to `/sessions/:id`

## Copy

Preferred visible labels:

- `Fix tests running`
- `Resolve conflicts running`
- `Open repair session`

Optional supporting copy when space permits:

- `A Fix tests session is already running for this PR state.`
- `A Resolve conflicts session is already running for this PR state.`

## Testing

### Backend

Add table-driven tests for:

- same-head active `fix_tests` run is returned in `active_repairs`
- same-head active `resolve_conflicts` run is returned in `active_repairs`
- terminal linked sessions are excluded
- active runs for older health versions on the same head are included
- active runs for older head SHAs are excluded
- merge is suppressed when `active_repairs` is non-empty
- repair completion and webhook-driven health recomputation clear `active_repairs` after the linked repair row is inactive or the PR advances to a newer head SHA

### Frontend

Add tests for:

- `Fix tests` button is replaced by `Fix tests running`
- `Resolve conflicts` active state suppresses both repair CTAs
- matching `pull_request.updated` SSE events trigger the PR health refetch instead of relying on timer polling
- `Open repair session` appears only when the active session differs from the current session
- optimistic `Opening repair session…` transitions to durable in-progress state after the health refetch

## Rollout

This should ship as a focused PR-health enhancement:

- no new page
- no new operator workflow
- no client-only persistence layer

The implementation is complete when the PR health row reflects the same in-progress truth the backend already uses to dedupe repair launches.
