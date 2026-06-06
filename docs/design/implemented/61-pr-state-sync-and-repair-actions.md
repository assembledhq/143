# 61 - PR State Sync and Repair Actions

> **Status:** Implemented
>
> **Last reviewed:** 2026-04-23
>
> **Depends on:** [../overall.md](../overall.md), [57-coding-agent-settings-rethink.md](57-coding-agent-settings-rethink.md)

## Summary

Add a compact **PR health** row to the **session detail Overview** surface for the current session. When the PR linked to that session has actionable GitHub state, show one or both repair actions:

- `Resolve conflicts`
- `Fix tests`

The row should live **at the very top of the Overview tab content**, in the same vicinity as the session's existing PR/session error notice like `PR session expired`. If that notice is present, the PR health row should render directly below it. It should be one of the first things the user sees when they open Overview on a session whose linked PR needs attention.

The buttons should stay simple from the operator's point of view, but they should not send only a bare natural-language prompt. Each click should resume the linked coding session, or create a revision session when resume is not possible, with a structured GitHub-derived payload attached behind a short visible prompt:

- `Please resolve the conflicts.`
- `Please fix these tests.`

The key requirement is that these actions appear quickly after GitHub state changes and remain tied to the current session's PR context. That means 143 needs a first-class PR-state sync path, not just today's coarse `status` / `review_status` / `ci_status` fields.

## Problem

Today 143 knows only a thin slice of PR health:

- `pull_requests` persists `status`, `review_status`, and `ci_status`
- GitHub webhook handling updates those fields from `pull_request`, `pull_request_review`, `pull_request_review_comment`, and `check_suite`
- the `check_suite` path only collapses outcomes to `success` or `failure`

That is enough for badges, but not enough for repair actions.

Specifically, we do **not** currently persist:

- whether a PR is mergeable
- whether the branch is conflicted or behind base
- which checks failed
- which failed checks were actually test failures versus lint/build/deploy failures
- annotations or log excerpts that would help the agent fix the problem

Without that richer state, the frontend cannot confidently decide when to show the buttons, and the agent cannot be given the right context when the user clicks one.

## What Comparable Tools Suggest

### Conductor

Conductor is the closest product reference for the UX. On January 16, 2026, Conductor introduced its **Checks** tab as a single place to see what blocks merging. Their public write-up says it includes:

- git status
- deployments
- GitHub Actions
- comments
- human todos

It also says each item has a recommended action, including sending failing CI logs to the AI agent or forwarding comments to the AI. Importantly, Conductor says this is built by querying the current branch state with the `gh` CLI.

That is the product pattern to copy: **surface current merge blockers as explicit, actionable repair work**.

Source: [The Checks tab](https://www.conductor.build/blog/checks-tab)

### Claude Code

Claude Code's GitHub integration is primarily **event-triggered**. Anthropic documents `@claude` mentions on PRs and issues, plus GitHub Actions workflows that run on PR events such as `opened` and `synchronize`. The GitHub App permissions called out in the docs are `Contents`, `Issues`, and `Pull requests`.

This is useful as an architecture reference: Claude keeps GitHub as the source of truth and reacts to GitHub events. But the public docs do not describe a first-class "checks hub" UI comparable to Conductor's.

Source: [Claude Code GitHub Actions](https://code.claude.com/docs/en/github-actions)

### Codex

Codex also has an event-driven GitHub story. OpenAI documents:

- `@codex review` in pull requests
- a Codex GitHub Action that runs on GitHub events
- direct GitHub connectivity for repo access and PR creation

The public docs position Codex as something you can trigger from GitHub or CI/CD, not as a persistent PR-health dashboard. Like Claude Code, this points toward **GitHub events plus backend sync** rather than frontend-only polling against GitHub.

Sources:

- [Use Codex in GitHub](https://developers.openai.com/codex/integrations/github)
- [Codex GitHub Action](https://developers.openai.com/codex/github-action)

## Recommendation

Take the **Conductor-style actionability** and pair it with a **server-synced GitHub state model**.

Do **not** copy Conductor's exact implementation detail of reading local workspace state with `gh`, because our target surface is a product session-detail page that may not have a live local checkout. For 143, GitHub should remain the remote source of truth and the backend should materialize a normalized PR-health record for the UI.

Recommended product behavior:

1. The backend syncs detailed PR health from GitHub whenever relevant PR/check state changes.
2. The backend pushes normalized PR-health updates to subscribed clients over SSE as soon as the sync completes.
3. When conflicts or failing tests are present, the page renders one or both repair buttons.
4. Clicking a button resumes or creates a revision session with a short prompt plus structured GitHub context.

## Current 143 Baseline

As of 2026-04-23, the relevant code paths are:

- PR state storage: [internal/db/pull_requests.go](../../../internal/db/pull_requests.go)
- PR model: [internal/models/models.go](../../../internal/models/models.go)
- GitHub webhook handling: [internal/services/github/pr.go](../../../internal/services/github/pr.go)
- PR authorship/user GitHub status flow: [internal/api/handlers/github_status.go](../../../internal/api/handlers/github_status.go)
- Current session detail UX: [frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx](../../../frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx)

The important gap is that the current webhook processing stores only summary flags. It does not materialize repair-ready GitHub payloads.

## UX Design

### Product boundary

PR health should be treated as a **product-level domain primitive**, not as a settings-page-only feature.

The session-detail Overview banner is the first consumer, not the entire abstraction boundary.

The same normalized PR health model should be reusable from:

- repo detail surfaces
- session detail surfaces
- project/task surfaces
- future inbox or notifications surfaces

The frontend should therefore expose shared UI building blocks such as:

- `PRHealthBanner` for compact action-first rendering
- `PRHealthSummary` for list/detail metadata
- shared button eligibility logic derived from the canonical API payload

For transport, clients should not open one SSE stream per PR or per card.

Recommended subscription topology:

- one org-scoped PR-health stream per active app shell
- lightweight `pull_request.updated` events on that shared stream
- client-side filtering plus bounded cache invalidation for the specific PRs visible in the current surface
- event coalescing so repeated writes for the same PR/version do not fan out redundant client work
- bounded client-side PR-health caches so one noisy org cannot force unbounded invalidation state into the browser

This avoids a one-EventSource-per-item anti-pattern once PR health appears in repo lists, inbox views, and project surfaces.

### Target surface

This doc assumes the buttons live in the existing **session detail Overview** surface for the active session.

That Overview surface should gain a session-scoped PR-health row:

- `PR health`

The banner should only render when the session has PR context in scope, such as:

- a linked open PR
- a PR-creation failure state that can be repaired from this session
- a session-specific PR workflow that is waiting on GitHub state

It should appear **above the top of the Overview page content**, adjacent to the current session-level PR error notice area.

### Placement

Inside the session detail panel:

1. tab strip
2. existing session-level PR/error notice, if any
3. separate session-level `PR health` row immediately below that notice, or at the top if no notice is present
4. existing Overview content such as result cards

This makes the PR repair actions feel like first-class next steps rather than passive metadata.

### Row contents

When the PR is healthy:

- show current branch / PR number / last sync time
- show a quiet healthy state
- do not show repair buttons

When the PR needs attention:

- show a short summary of the blocker
- show the relevant repair button(s)
- show the most recent GitHub sync timestamp as plain inline text, not a pill
- keep the sync timestamp live relative to the current time (`Synced 15s ago`, `Synced 3m ago`) with an adaptive refresh cadence
- keep the row visually compact so it reads like a lightweight action strip, not a large content block

If the session is already showing a PR-related error notice, keep that notice as-is and render the PR health row directly below it as a separate element.

### Button rules

Show `Resolve conflicts` when:

- `has_conflicts = true`, or
- normalized `merge_state` is a conflicting state

Show `Fix tests` when:

- one or more latest checks failed. Test-classified failures still drive the `failing_test_count` summary and copy, but failed lint/build/unknown checks are also repairable because GitHub often names test jobs generically.

If both are true:

- show both buttons
- visually promote `Resolve conflicts` first
- include a note that CI may need to rerun after conflict resolution

Reasoning: a conflicted branch often invalidates existing test results, so conflict repair is the more fundamental blocker.

### UI wireframe

```text
┌──────────────────────────────────────────────────────────────────────┐
│ Tabs: [Overview] [Changes] [Preview]                                │
├──────────────────────────────────────────────────────────────────────┤
│ PR session expired                                                  │
│ Session state expired — re-run to create a PR.                      │
├──────────────────────────────────────────────────────────────────────┤
│ PR health                                                    Synced │
│ PR #184 is blocked by conflicts and 2 failing test jobs.     22s ago│
│ [Resolve conflicts] [Fix tests]                                     │
├──────────────────────────────────────────────────────────────────────┤
│ Result                                                               │
│ ... existing session result / timeline content ...                  │
└──────────────────────────────────────────────────────────────────────┘
```

Healthy-state variant:

```text
┌──────────────────────────────────────────────────────────────────────┐
│ PR health                                                    Synced │
│ PR #184 is mergeable and all required test checks are passing. 12s ago│
└──────────────────────────────────────────────────────────────────────┘
```

### Button lifecycle

On click:

1. Optimistically disable the clicked button.
2. Start the repair session.
3. Replace the button with a spinner plus `Opening repair session…`.
4. If we resumed the current session, stay on the same session detail view and stream the resumed work in place.
5. If we created a new revision session, route the user into that new linked session detail.
6. On failure, restore the button and show a scoped error notice inline with the PR health row.

These are operational actions, not settings edits, so they should use explicit buttons rather than autosave.

### Stale and unknown-state UX

The UI should avoid offering repair actions when PR health is too stale or too ambiguous to trust.

Recommended behavior:

- if a fresh summary sync is in progress, show a lightweight syncing state and suppress repair buttons until eligibility is known
- if mergeability remains `mergeability_pending` after retry, do not show `Resolve conflicts` yet; keep the Merge action visible but disabled in a neutral checking state
- if failed checks are present but cannot be confidently classified, still show `Fix tests` and pass the failed check names, provider, URL, summary, annotations, and log excerpt when available
- if the health snapshot is older than a defined freshness threshold, show the row as stale and trigger a refresh rather than reusing old button eligibility

The system should prefer temporarily hiding a button over showing an incorrect repair action against stale state.

## Prompt and Context Contract

The operator-facing prompt should stay intentionally small:

- `Please resolve the conflicts.`
- `Please fix these tests.`

The real context should come from a structured attachment added by the backend.

### Merge conflict payload

Recommended fields:

- PR id / repo / number / URL
- base branch and head branch
- base SHA and head SHA
- merge state
- list of conflicted files if GitHub exposes them, otherwise best-known summary
- whether the branch is also behind base

### Failing test payload

Recommended fields:

- PR id / repo / number / URL
- head SHA
- failing checks summary
- for each failed test-like check:
  - check name
  - provider (`github_actions`, `check_run`, etc.)
  - workflow name / job name
  - failing step names
  - annotations
  - a bounded log excerpt
  - link back to GitHub

### Why the prompt should stay short

The prompt should not repeat the logs or the PR metadata. That information belongs in structured machine context so the agent can use it precisely and the UI remains legible.

## Backend Sync Design

### New sync job

Add a job such as:

- `sync_pull_request_state`

This job is the single place that talks to GitHub to refresh repair-relevant state.
It should be optimized for fast summary refresh, not for always building full repair payloads inline.

### Triggers

Enqueue the sync job on:

- `pull_request` events for `opened`, `reopened`, `synchronize`, and `closed`
- `check_suite.completed` as a coarse wake-up signal
- `check_run.completed` as the canonical per-job repairability signal
- optional manual refresh from the UI

Do not dedupe by a naive time bucket.

Instead, use a **per-PR singleflight sync queue**:

- one active summary sync per PR
- keyed by `pull_request_id` with the observed `head_sha` and `base_sha`
- if a new event arrives while sync is running, mark the PR dirty and immediately reschedule another sync when the current run finishes

This scales better under sustained webhook bursts and avoids arbitrary behavior around time windows.

Current implementation note: ordinary PR lifecycle webhooks use the stable per-PR dedupe key `sync_pull_request_state:<pull_request_id>`. Completed `check_suite` and `check_run` webhooks use separate completion-scoped dedupe keys so a check-completion wake-up is not swallowed by a generic sync that was already pending or running before GitHub finished CI. This is a pragmatic approximation of the dirty-reschedule behavior above and keeps the post-completion sync low-latency without flooding the queue.

### Reconciliation path

Event-driven sync is necessary for freshness, but it is not sufficient for correctness.

Add a low-frequency reconciliation job such as:

- `reconcile_pull_request_state`

This job should periodically resync:

- open PRs whose `github_state_synced_at` is stale
- PRs recently changed by webhook but missing a successful sync
- PRs whose outbox/event publish path failed

Recommended role:

- webhooks give low-latency freshness
- reconciliation repairs drift caused by missed webhooks, GitHub incidents, worker outages, or publish gaps

Recommended sweep strategy:

- select only indexed stale candidates rather than scanning all PRs
- query primarily on `(org_id, status, github_state_synced_at)` with `status = 'open'`
- order by oldest `github_state_synced_at` first
- process bounded batches per run
- enforce per-org reconciliation budgets so one degraded org cannot monopolize the sweeper

Without this path, some PR health rows will eventually become silently wrong and stop offering the right repair actions.

### Two-stage sync

The sync path should be explicitly split into a cheap summary phase and a deferred enrichment phase.

#### Stage 1: summary sync

This stage runs on every relevant webhook burst and is optimized for freshness, low GitHub API cost, and fast SSE fan-out.

It should fetch only what is needed to drive UI summaries and button eligibility:

1. PR details
   - mergeability
   - merge state
   - base/head refs and SHAs
2. check status for the PR head SHA
3. normalized failed-check counts by category

The summary sync should be what produces the next `pull_request.updated` event.

#### Stage 2: deferred enrichment

This stage runs only when needed:

- when Stage 1 detects failing test-like checks
- when the health surface is opened and repair context is stale or missing
- when the user clicks `Fix tests`
- when conflict detail needs refreshing for `Resolve conflicts`

This is where the system fetches expensive artifacts such as:

- annotations
- failing step names
- bounded log excerpts

This keeps the steady-state webhook path cheap and prevents noisy CI from turning every event into an expensive GitHub crawl.

### Worker fairness and budgets

Sync and enrichment need explicit fairness controls so one noisy repo or org cannot starve the rest of the system.

Recommended controls:

- per-org concurrency caps for summary sync jobs
- per-org concurrency caps for enrichment jobs
- separate worker lanes or queue priorities for summary sync versus enrichment
- per-org GitHub API budgets or token buckets
- preference for summary freshness work over enrichment when the system is saturated

The practical goal is simple: PR eligibility and top-level health should stay fresh even when enrichment work is backlogged.

### Enrichment cache and state machine

Deferred enrichment must be shared across viewers.

Define at most one enrichment job per:

- `(pull_request_id, health_version)`

Recommended states:

- `not_requested`
- `pending`
- `ready`
- `failed`
- `stale`

Recommended behavior:

1. opening the health surface checks whether enrichment for the current `health_version` is already `ready` or `pending`
2. if `ready`, reuse the stored payload
3. if `pending`, reuse the existing in-flight job rather than starting another
4. only if no enrichment exists for the current `health_version`, enqueue one new enrichment job
5. when `health_version` changes, prior enrichment becomes `stale`

This prevents thundering-herd enrichment fetches when multiple users view the same failing PR.

### Enrichment policy decision

Deferred enrichment should **not** run eagerly for every PR with `failing_test_count > 0`.

Recommended default:

- always run cheap summary sync
- run expensive enrichment only when a human is likely to act soon

Concretely, enrichment should happen when:

- the session detail PR-health banner is viewed and the current `health_version` lacks enrichment
- a repair action is requested
- an explicit high-signal workflow says this PR is actively being worked

This keeps GitHub API usage bounded and avoids spending enrichment work on failing PRs nobody opens.

### GitHub reads

Long-term, `check_run` or workflow-job data should be the canonical source for repairability because it supports:

- job-level attribution
- failing-step extraction
- annotations
- repair-ready payload construction

`check_suite` can remain a wake-up signal, but it should not be the source of truth for repair payloads.

### Mergeability retry

GitHub mergeability is often computed asynchronously. If GitHub returns an indeterminate value, retry a few times with short exponential backoff before persisting `mergeability_pending`; worker-driven syncs then keep retrying through the queue's exponential backoff until GitHub reports a definitive mergeability state or the retry window expires.

### Classification

Normalize GitHub data into product states:

- `clean`
- `conflicted`
- `behind`
- `unknown`

and classify failed checks into:

- `test`
- `lint`
- `build`
- `deploy`
- `unknown`

Any failed check can drive the `Fix tests` repair action. The category is still stored so summary copy and future routing can distinguish test, lint, build, deploy, and unknown failures.

## Data Model

Keep the querying fields explicit and the heavy payloads structured.

### `pull_requests` additions

Keep only narrow hot-path summary fields on `pull_requests`:

- `head_sha text`
- `base_sha text`
- `merge_state text NOT NULL DEFAULT 'unknown'`
- `has_conflicts boolean NOT NULL DEFAULT false`
- `failing_test_count integer NOT NULL DEFAULT 0`
- `needs_agent_action boolean NOT NULL DEFAULT false`
- `github_state_synced_at timestamptz`
- `health_version bigint NOT NULL DEFAULT 0`

Enum-like fields should follow the normal typed-string pattern in `internal/models`.

These fields should stay small enough that frequent sync updates do not cause row bloat or couple lightweight reads to heavyweight JSON payload rewrites.

### New current snapshot table

Keep immutable snapshots for provenance, replay, and obsolete repair sessions.

Recommended immutable table:

```sql
CREATE TABLE pull_request_health_snapshots (
    pull_request_id        uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
    org_id                 uuid NOT NULL REFERENCES organizations(id),
    version                bigint NOT NULL,
    head_sha               text NOT NULL,
    base_sha               text NOT NULL,
    summary_json           jsonb NOT NULL,
    conflict_payload       jsonb,
    failing_tests_payload  jsonb,
    payload_size_bytes     integer NOT NULL DEFAULT 0,
    enrichment_status      text NOT NULL DEFAULT 'not_requested',
    enriched_at            timestamptz,
    created_at             timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (pull_request_id, version)
);
```

Then keep a separate hot pointer/current cache:

```sql
CREATE TABLE pull_request_health_current (
    pull_request_id        uuid PRIMARY KEY REFERENCES pull_requests(id) ON DELETE CASCADE,
    org_id                 uuid NOT NULL REFERENCES organizations(id),
    version                bigint NOT NULL,
    head_sha               text NOT NULL,
    base_sha               text NOT NULL,
    summary_json           jsonb NOT NULL,
    summary_preview_json   jsonb,
    enrichment_status      text NOT NULL DEFAULT 'not_requested',
    enriched_at            timestamptz,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);
```

Recommended properties:

- immutable snapshot retention for any `health_version` referenced by:
  - a repair session
  - an outbox event
  - an audit trail
- one current row per PR
- monotonic `version` matching the hot summary row
- exact git identity includes both `head_sha` and `base_sha`
- explicit payload size tracking and enforced caps on immutable snapshots
- separation between summary freshness and enrichment freshness
- full repair payloads live in immutable snapshots, not in the mutable current row

Recommended current-table role:

- fast read path for “what is the current PR health?”
- mutable pointer/cache for the latest snapshot
- summary-first cache, not a second full payload store
- never the only source of truth for an already-referenced `health_version`

This prevents older or obsolete repair sessions from losing the exact snapshot they were based on after a later sync overwrites the current row.

### Snapshot retention and garbage collection

Immutable snapshots need an explicit lifecycle policy.

Recommended retention rules:

- pin any snapshot referenced by an active repair session
- pin any snapshot still needed by the outbox replay or reconnect window
- pin any snapshot referenced by an audit or debugging record
- keep a bounded recent-history window per PR even when snapshots are not pinned
- garbage-collect only snapshots that are both unpinned and older than a defined TTL

This preserves provenance without allowing snapshot storage to grow without bound.

### Query and index strategy

Because PR health is now a cross-surface product primitive, define the canonical query path explicitly.

Cross-surface list views should query indexed summary predicates from `pull_requests`, not inspect JSON payloads from the snapshot tables.

Recommended canonical summary predicates:

- `status = 'open'`
- `has_conflicts = true`
- `failing_test_count > 0`
- `needs_agent_action = true`
- `github_state_synced_at`
- `health_version`

Recommended derived summary field:

- `needs_agent_action boolean NOT NULL DEFAULT false`

This should be computed during summary sync so cross-surface queries do not need to restate “conflicts or failing tests” logic everywhere.

Recommended indexes:

- `(org_id, status, has_conflicts, github_state_synced_at DESC)`
- `(org_id, status, failing_test_count, github_state_synced_at DESC)`
- `(org_id, status, needs_agent_action, github_state_synced_at DESC)`
- `(org_id, github_repo, status, github_state_synced_at DESC)`

Use `pull_request_health_current` for the current summary/preview read path, and `pull_request_health_snapshots` when a surface needs detailed repair context for a specific PR and version.

## Repair Action Flow

### Preferred path: resume linked session

If the PR is linked to a session and that session still has a usable snapshot:

1. append a new user message with the short prompt
2. attach the structured GitHub payload to that message
3. resume the session

This preserves branch context, transcript continuity, and existing review state.

### Fallback path: create revision session

If the linked session cannot be resumed:

1. create a new revision session against the PR head branch
2. link it back to the PR
3. seed it with the short prompt plus structured GitHub payload

This should use the same conceptual `revision` flow we already use for PR-follow-up work.

### Idempotency and concurrency

Repair actions must be idempotent.

Define a single active repair run per:

- `(pull_request_id, action_type, health_version)`

where `action_type` is one of:

- `fix_tests`
- `resolve_conflicts`

Recommended behavior:

1. compute a stable dedupe key from `(pull_request_id, action_type, health_version)`
2. if a matching in-flight repair session already exists, return that existing session id
3. otherwise create a new repair session and persist the dedupe key
4. allow a new repair run when `health_version` changes, because the normalized PR state materially changed
5. the current `health_version` is derived from the exact snapshot identity, which includes both `head_sha` and `base_sha`

This prevents duplicate revision sessions from repeated clicks, retries, or concurrent operators.

### Repair identity decision

Repair identity should be tied to **`health_version`**, not raw git SHAs alone.

Reasoning:

- the operator is acting on a normalized PR-health snapshot, not directly on a bare commit
- the exact same `head_sha` can become materially different repair work when `base_sha` or mergeability changes
- using `health_version` gives sync, SSE, and repair flows one canonical identity

The snapshot can still be derived from `head_sha + base_sha + normalized check state`, but the repair API should treat `health_version` as the action identity.

### Superseded repair sessions

When a newer `health_version` supersedes an in-flight repair session, the default behavior should be to **mark the older repair session obsolete, not auto-cancel it**.

Recommended behavior:

- keep the in-flight repair session running by default
- mark it as obsolete for the latest health snapshot
- do not reuse it for future repair actions
- let the user decide whether to continue with it or start from the newer snapshot

Automatic cancellation is riskier and can kill useful work mid-flight. The safer long-term default is obsolescence tracking rather than hard cancellation.

## API Shape

Recommended endpoints:

- `GET /api/v1/pull-requests/:id/health`
- `POST /api/v1/pull-requests/:id/repair/fix-tests`
- `POST /api/v1/pull-requests/:id/repair/resolve-conflicts`

The `health` response should be presentation-friendly and include:

- summary flags
- button eligibility
- last synced time
- concise human-readable summaries

The `repair/*` endpoints should return:

- linked session id
- whether we resumed an existing session or created a revision session
- whether the response reused an existing in-flight repair session

Recommended request/response contract additions:

- optional client idempotency key
- server-side dedupe regardless of client behavior
- returned `head_sha`, `base_sha`, and `health_version` so the client can reason about whether it acted on stale state

## Frontend Refresh Model

### Recommendation

Use **SSE immediately** for this surface.

This is the better long-term infrastructure choice if the goal is a scalable product-wide pattern for integration-backed status surfaces. PR health is an event-driven domain already:

- GitHub emits webhook events
- the backend already processes those events
- the UI benefits from fast fan-out of normalized state

Polling would work, but it would create unnecessary steady-state read load and teach the frontend the wrong integration pattern for a feature that is naturally push-driven.

Recommended model:

- GitHub webhook arrives
- backend enqueues `sync_pull_request_state`
- sync job normalizes the latest PR health snapshot
- backend writes a versioned outbox event
- SSE fan-out emits `pull_request.updated` to interested clients
- frontend updates the `PR health` row in place

Recommended transport shape:

- one org-scoped SSE stream per active client shell
- one lightweight event type for PR health changes
- client-side filtering to update only visible or cached PRs
- server-side coalescing for repeated updates to the same PR/version
- bounded client cache invalidation rather than broad list refetches
- no per-PR EventSource connections

### Why not polling first

Polling has real downsides:

- redundant reads for unchanged PRs
- slower user-visible updates unless the poll interval is aggressive
- more frontend timers and tab-visibility edge cases
- a weaker foundation for future push-driven surfaces like review comments, deployment blockers, and mergeability transitions

### Downsides of SSE

SSE is still the right choice here, but it has implementation costs:

- we need a product-level event model for `pull_request.updated`
- we need per-user/per-org authorization on the stream
- clients must handle reconnect, missed events, and stale snapshots
- event ordering must be idempotent because GitHub webhooks can arrive in bursts or out of order
- long-lived connections increase operational complexity on API nodes and any proxy layer

These are real costs, but they are **one-time infrastructure costs** that pay off across the rest of the product. They do not remove the need for backend sync; they only replace wasteful client polling with push delivery of our normalized state.

### Implementation notes

SSE should deliver a **normalized 143 PR-health event envelope**, not raw GitHub webhook bodies and not full repair payloads.

That keeps the contract stable, lightweight, and safe to fan out broadly. Reconnect logic can always recover by refetching the canonical `GET /api/v1/pull-requests/:id/health` snapshot.

The event contract should be explicit and versioned. Each `pull_request.updated` event should include at least:

- `pull_request_id`
- `version`
- `head_sha`
- `base_sha`
- `synced_at`

Recommended event semantics:

- the event is an invalidation/update hint, not a full repair payload
- multiple writes for the same `(pull_request_id, version)` should coalesce before fan-out
- clients should invalidate only visible or cached PR-health queries for that PR
- reconnect should always be repaired by refetching the canonical health endpoint

The client should ignore any event whose `version` is older than the currently cached `health_version`.

The backend should emit these events from an outbox-backed publisher so retries, reconnects, and worker restarts do not create silent gaps or stale-overwrite bugs.

Recommended frontend behavior:

- subscribe to `pull_request.updated` while the session detail view is open and the PR is open
- ignore stale events using `version`
- refetch the canonical health query on reconnect
- refetch the canonical health query after a repair action starts
- stop listening when the PR is merged or closed

## Safety and Payload Limits

Do not pass raw unlimited GitHub logs into the agent.

Apply these limits before storing or sending repair context:

- keep only failed checks, not all checks
- cap the number of failed jobs included
- cap log excerpt size per job
- prefer annotations and failing-step summaries over raw logs
- redact obvious secret material
- enforce maximum snapshot payload size at write time
- allow deferred enrichment to truncate rather than fail the whole sync when GitHub returns too much data

The goal is to give the agent the smallest context bundle that still lets it act.

## Observability and Success Metrics

This feature adds a non-trivial sync, storage, queueing, and fan-out path. It should ship with explicit operating signals.

Recommended metrics:

- webhook-to-summary-sync latency
- summary-sync success rate and retry rate
- enrichment job success rate, latency, and cache-hit/reuse rate
- reconciliation scan volume, catch-up rate, and number of drifted PRs repaired
- SSE connection count, reconnect rate, event fan-out rate, and stale-event drop count
- repair-action dedupe hit rate and obsolete-session rate

Recommended product-level signals:

- time from GitHub state change to button visibility update
- percentage of `Fix tests` clicks that had ready enrichment versus on-demand enrichment
- percentage of repair actions launched from stale snapshots
- percentage of PRs with actionable blockers but no rendered CTA

Recommended alerts:

- sustained lag in summary sync or reconciliation
- rising GitHub API budget exhaustion
- SSE publish backlog growth
- repeated failures to classify checks for the same org or repo

Without this instrumentation, the architecture may look correct on paper while quietly drifting in production.

## Rollout Plan

### Phase 1: richer sync

- add PR summary fields and a separate current-snapshot table
- add immutable `pull_request_health_snapshots`
- add `sync_pull_request_state`
- make `check_run` canonical for failed-check classification
- implement summary-first sync on webhook events
- add `reconcile_pull_request_state` for stale/open PRs
- add indexed stale-PR selection plus bounded reconciliation batches
- add per-org sync fairness controls
- expose `GET /pull-requests/:id/health`

### Phase 2: SSE and enrichment

- add versioned `pull_request.updated` outbox events
- add SSE fan-out for PR health updates
- make the stream org-scoped rather than per-PR
- include `base_sha` and `health_version` in the event contract
- coalesce repeated PR/version events before fan-out
- implement deferred enrichment for failed test jobs
- enforce one enrichment job per `(pull_request_id, health_version)`
- add separate worker lanes or priorities for enrichment
- enforce payload caps and truncation rules

### Phase 3: UI card

- add the session-detail `PR health` row near the existing top-of-Overview PR/error notice area
- render conflict/test summaries
- render the two repair buttons when eligible

### Phase 4: repair actions

- wire `repair/fix-tests`
- wire `repair/resolve-conflicts`
- enforce server-side repair dedupe on `(pull_request_id, action_type, health_version)`
- resume existing session when possible
- otherwise create revision session

### Phase 5: retention and cleanup

- add snapshot pinning rules for repair, outbox, and audit references
- add TTL-based garbage collection for unpinned snapshots
- validate that current-row reads never depend on mutable copies of full repair payloads

## Decision

Build this as a **server-synced PR health surface with explicit repair actions**, not as:

- a frontend-only GitHub poller
- a comment-trigger-only workflow like `@claude` or `@codex`
- a local-CLI-dependent design like Conductor's `gh`-backed Checks tab

The product principle is:

- **GitHub is the source of truth**
- **143 materializes a small summary state plus a separate versioned repair snapshot**
- **143 retains immutable snapshots for referenced health versions**
- **143 uses `check_run` as the canonical repairability signal**
- **PR health is a shared domain primitive, not a page-local implementation**
- **143 pushes versioned normalized PR-health updates over SSE**
- **repair actions are idempotent per PR action and health snapshot version**
- **the UI turns that state into one-click actions at the top of Overview**

That gets the Conductor-style experience the user wants, while fitting 143's existing session, PR, and GitHub webhook architecture.
