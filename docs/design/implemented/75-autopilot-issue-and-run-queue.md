# Design: Autopilot Issue And Run Queue

> **Status:** Implemented | **Last reviewed:** 2026-05-13

## Problem

The current `Autopilot` page is optimized around a PM-style recommendation brief:
one headline, a short explanation, lightweight evidence, and links into deeper
workflows. That shape no longer matches the operator job described in
`VIR-55 Adjust the Autopilot interface`.

Operators now need `Autopilot` to answer a more concrete question:

**Which issues should the system work on next, and what is the execution state for each one?**

Three gaps drive the redesign:

1. The page does not show the full issue universe in one place. Operators must
   jump across separate surfaces to understand the current backlog.
2. The current recommendation-first layout does not make "low hanging fruit"
   obvious or consistently actionable.
3. Session/run status is disconnected from issue selection. A user can know an
   issue is important without seeing whether it already autoran, is currently
   running, needs review, or is still waiting to be kicked off.

## Design Goal

`Autopilot` should become the supporting background automation queue for
agent-driven issue execution:

- show **all actionable issues** across sources (`Linear`, `Sentry`, `Canny`,
  support-derived records, and internal issues) in one table
- sort the table so the **best low-hanging-fruit candidates rise to the top**
- show the **latest session/run state inline for every issue**
- make it obvious whether the system **already autoran**, **is currently acting**,
  **needs a human**, or **can be started now**

This is not a return to a noisy dashboard. The page should stay operationally
calm, but the primary artifact is now a queue table instead of a briefing hero.
`Sessions` remains the main operating surface for active execution, review, and
human guidance.

## Non-Goals

- Replacing `Projects` as the long-term home for PM-created strategic work
- Turning `Autopilot` into a full issue detail page
- Hiding source-specific detail pages and session detail pages
- Designing the final scoring model implementation for PM prioritization

## Product Decision

`Autopilot` becomes a supporting **unified issue-and-run queue** with three
layers:

1. A compact summary strip for system posture
2. A ranked issue table as the primary surface
3. Lightweight drawers/sheets for issue detail and run history

The existing recommendation hero becomes a compact summary module above the
table instead of the dominant page element.

For the first implementation, the queue is intentionally issue-shaped:

- one table row represents one canonical internal `issues` row
- issue clusters can appear as metadata, rank explanations, and drawer content,
  but they are not first-class queue rows in v1
- the default queue includes actionable issue states only: `open`, `triaged`,
  `in_progress`, and any issue with an active linked execution session
- terminal or non-actionable issue states such as `duplicate`, `wont_fix`,
  archived, and source-closed records stay out of the default queue
- setup remains outside `/autopilot`; if coding-agent auth or GitHub setup is
  missing, route to `/onboarding` rather than rendering the queue with inline
  setup cards

## Core Principles

### 1. Queue first

The first screenful should answer "what should run next?" without requiring a
scroll into another page.

### 2. One row per issue

The operator should not have to mentally join issue data, PM scoring, and run
state from multiple places. Every row should carry source, impact, effort
signal, rank, and latest execution state.

### 3. Low-hanging fruit is explicit

The ordering should intentionally favor issues with high expected impact and
straightforward implementation, not just raw severity or freshness.

### 4. Automation state is visible, not implied

Rows must say whether an issue:

- already autoran
- is queued/running
- produced a PR
- is blocked on human input/review
- has never been attempted and can be launched manually

### 5. Escalate depth progressively

The main table stays dense and scannable. Deep issue context, related issues,
and run timelines open in drawers rather than inflating the row height by
default.

## User Stories

- As an operator, I can see all candidate issues in one place even when they
  came from different systems.
- As an operator, I can immediately spot low-hanging-fruit issues near the top.
- As an operator, I can tell whether Autopilot already took action on an issue.
- As an operator, I can start a session from the row when an issue has not
  autorun.
- As an operator, I can jump from a row to the active or most recent session for
  that issue.
- As an operator, I can distinguish strategic proposals from tactical fixable
  issues without losing the issue queue.

## Information Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│ Header: Autopilot                                   [Run analysis]  │
│ Status: Autopilot mode · analyzed 18m ago · 6 runs active           │
├──────────────────────────────────────────────────────────────────────┤
│ Summary strip:                                                       │
│ [Auto-runnable now] [Needs review] [Connected work]                 │
├──────────────────────────────────────────────────────────────────────┤
│ Filters: Source | Status | Auto-run state | Repo | Owner | Search   │
├──────────────────────────────────────────────────────────────────────┤
│ Ranked issue table                                                   │
│ Rank | Issue | Source | Low-hanging fruit | Run state | Action       │
│ ------------------------------------------------------------------   │
│ 1    | Auth token expiry... | Sentry | Very high     | Running       │
│ 2    | Missing retry copy   | Canny  | High          | Start run      │
│ 3    | Linear bug VIR-101   | Linear | Medium        | Awaiting review│
│ ...                                                                  │
├──────────────────────────────────────────────────────────────────────┤
│ Optional compact project/proposal summary                            │
└──────────────────────────────────────────────────────────────────────┘
```

## Page Structure

### 1. Header and status line

Keep the existing page title and top-right action, but repurpose the subtitle
for execution posture rather than PM prose.

Examples:

- `Suggest · analyzed 18m ago · 24 issues ranked`
- `Act on low-risk · analyzed 6m ago · 2 runs active`
- `Operate · analysis stale · 5 issues need attention`

### 2. Summary strip

Use a compact row of aggregate cards, not a hero. Do not duplicate the top
ranked issue here; the ranked issue table already carries the opportunity list.

Recommended cards:

- `Auto-runnable now` — count of issues above the autorun threshold with no
  active run
- `Needs review` — issues whose latest run is blocked on approval/guidance
- `Connected work` — active runs plus live PRs from the latest linked sessions

This keeps the page's top summary operational while the ranked table remains the
place to inspect specific opportunities.

### 3. Filters and controls

The main controls row should support:

- source filter: `All`, `Linear`, `Sentry`, `Canny`, `Support`, `Internal`
- run state filter: `Any`, `Not started`, `Queued`, `Running`, `Needs review`,
  `PR open`, `Merged`, `Failed`
- automation filter: `Any`, `Autorun attempted`, `Manual only`, `Ready to run`
- repo filter
- search by issue key/title
- sort override

Default sort remains the system ranking, but operators can temporarily sort by
impact, freshness, or run state.

### 4. Ranked issue table

This is the primary surface.

Recommended columns:

| Column | Purpose |
|---|---|
| `Rank` | Stable queue position based on Autopilot ordering |
| `Issue` | Title, key, repo, and lightweight metadata |
| `Source` | Source badge (`Linear`, `Sentry`, `Canny`, etc.) |
| `Customer impact` | Compact count/severity summary |
| `Implementation ease` | Estimated ease signal |
| `Low-hanging fruit` | Combined score label and explanation tooltip |
| `Run state` | Latest linked session/run state |
| `Action` | Primary next action |

Row density should stay compact. Secondary metadata should sit inside the issue
cell rather than expanding the number of columns indefinitely.

### 5. Row action model

Each row has exactly one dominant action based on current state:

- `Start run` when no session exists and the issue is eligible for manual launch
- `View run` when a session is queued or running
- `Review` when the latest run needs approval/guidance
- `Open PR` when the latest session has an active PR
- `Retry` when the latest run failed and the issue remains eligible
- `Blocked` when prerequisites are missing or policy disallows launch

If the system already autoran, the row should state that explicitly near the run
state, for example `Autoran 12m ago`.

## Ranking Model

The sort order should optimize for low-hanging fruit by combining:

- expected customer/business impact
- implementation straightforwardness
- confidence that the issue maps cleanly to a fixable code path
- freshness/relevance
- de-duplication/cluster context
- automation eligibility

At the UX layer, the operator should not see raw weights by default. Show a
human label:

- `Very high`
- `High`
- `Medium`
- `Low`

Hover or peek detail can explain the label with short reasons such as:
`high customer impact, isolated component, clear repro from Sentry`.

## Cross-Source Issue Model

The table should unify records from multiple sources behind one common issue-row
shape.

Each row needs:

- canonical internal issue ID
- source type and source key
- normalized title
- repo scope
- impact summary
- PM ranking metadata
- latest linked session metadata
- latest PR metadata if present

Autopilot should still support source-native affordances:

- `Linear` rows show issue key and state
- `Sentry` rows show error frequency/regression hints
- `Canny` rows show request/customer signal

The source badge should open source detail in a secondary surface, not replace
the unified row model.

## Run State Model On The Row

Each issue row should render execution state derived from linked sessions,
agent-run records, and PR state. The row state is a read-model projection; it
must not mutate issue lifecycle state by itself.

Recommended normalized states:

- `Not started`
- `Queued`
- `Running`
- `Awaiting input`
- `Needs review`
- `PR open`
- `Merged`
- `Failed`
- `Skipped`

If multiple sessions are linked, the row defaults to the **active session** if
one exists; otherwise it shows the most recent meaningful session. A secondary
"N runs" affordance opens run history for that issue.

Selection order:

1. Prefer a linked session with an active agent run (`pending` or `running`).
2. Otherwise prefer a linked session whose latest run requires human action.
3. Otherwise prefer a linked session with an open PR.
4. Otherwise use the most recently updated linked session.

Display-state precedence:

| Display state | Backend condition | Dominant action |
|---|---|---|
| `Queued` | Latest relevant agent run is `pending` | `View run` |
| `Running` | Latest relevant agent run is `running` | `View run` |
| `Awaiting input` | Session is paused for operator input before more execution can continue | `Review` |
| `Needs review` | Latest run ended in `needs_human_guidance` or equivalent review-gated state | `Review` |
| `PR open` | Latest linked PR is open | `Open PR` |
| `Merged` | Latest linked PR is merged | `Open PR` or `View run` |
| `Failed` | Latest relevant run failed and retry is allowed | `Retry` |
| `Skipped` | Autorun gate rejected execution before session start | `Blocked` |
| `Not started` | No linked session exists for the issue | `Start run` or `Blocked` |

When issue lifecycle and execution state disagree, execution state wins for row
display. For example, an `in_progress` issue with a failed latest run should show
`Failed` so the operator sees the actionable recovery path.

## Drawer Behavior

### Issue preview drawer

Opening a row should show:

- normalized issue summary
- source-specific details
- why it ranked where it did
- related issues/cluster membership
- recent session history

### Run history drawer

From the run-state cell, operators can inspect:

- chronological session/run attempts for this issue
- whether a run was autorun or manually started
- final outcome for each attempt
- direct links to session detail and PR

## Relationship To Projects

`Autopilot` should still surface strategic PM output, but it becomes secondary to
the issue queue.

Recommended treatment:

- keep a compact proposal/project summary below the table or in a right rail on
  wide screens
- do not let project cards displace the issue table from the initial viewport
- preserve `Projects` as the full review and management surface for PM-created
  projects

## Empty And Edge States

### No issues

Show a calm empty state:

- headline: `No ranked issues right now`
- body: explain whether ingestion is empty, analysis has not run, or filters are
  excluding everything
- CTA: `Run analysis` or `Clear filters`

### Analysis stale

Keep the table visible if cached ranking exists, but show a stale banner and
de-emphasize confidence in the ordering.

### Missing integration context

Rows can still appear when only some sources are connected. Source filters
should show unavailable integrations as disabled rather than disappearing.

### Mobile layout

The desktop table collapses to a compact issue list on narrow screens:

- each row/card shows rank, issue title, source badge, low-hanging-fruit label,
  run state, and one primary action
- secondary fields such as customer impact, repo, and implementation ease move
  into a details line or drawer
- filters collapse behind a single controls sheet with active filter chips
  visible above the list
- the primary action remains reachable without horizontal scrolling

## Engineering Spec

### API Shape

The page likely needs a dedicated queue endpoint rather than stitching multiple
queries on the client:

- `GET /api/v1/autopilot/queue`

This is a list endpoint and should follow the existing API list convention:
top-level `data` is the page of rows, and `meta.next_cursor` carries pagination.
Queue-level summary belongs in `meta.summary`.

```json
{
  "data": [
    {
      "id": "iss_123",
      "rank": 1,
      "source": {"type": "sentry", "key": "SENTRY-99"},
      "title": "Auth token expiry causes retry loop",
      "repo": {"id": "repo_123", "name": "api"},
      "issue_status": "triaged",
      "customer_impact": {"label": "High", "count": 42},
      "implementation_ease": "High",
      "low_hanging_fruit": {
        "label": "Very high",
        "reasons": ["high impact", "isolated subsystem", "clear repro"],
        "cluster_size": 3
      },
      "display_run_state": "running",
      "latest_session": {
        "id": "ses_123",
        "title": "Fix auth token expiry retry loop",
        "updated_at": "2026-05-08T12:15:00Z"
      },
      "latest_agent_run": {
        "id": "run_123",
        "status": "running",
        "trigger_mode": "auto",
        "started_at": "2026-05-08T12:10:00Z"
      },
      "latest_pr": null,
      "available_action": "view_run",
      "action_disabled_reason": null
    }
  ],
  "meta": {
    "next_cursor": null,
    "summary": {
      "top_issue_id": "iss_123",
      "autorunnable_count": 12,
      "needs_review_count": 3,
      "open_pr_count": 4,
      "active_run_count": 2,
      "ranked_issue_count": 24,
      "analyzed_at": "2026-05-08T12:00:00Z"
    }
  }
}
```

This should remain cursor-paginated like other list surfaces.

### Query Parameters

Supported query params:

| Param | Meaning |
|---|---|
| `cursor` | Cursor for the next page |
| `limit` | Page size, bounded by the API default maximum |
| `source` | Source type filter |
| `run_state` | Display run-state filter |
| `automation` | `autorun_attempted`, `manual_only`, or `ready_to_run` |
| `repo_id` | Repository filter |
| `q` | Search over source key and normalized title |
| `sort` | `rank`, `impact`, `freshness`, or `run_state` |

Filters must be URL-backed on the frontend via `nuqs`.

### Read Model

The backend should build a dedicated Autopilot queue read model rather than
asking the client to stitch issue, session, run, and PR endpoints together.

- extend the Autopilot read model to emit a unified issue queue response
- normalize cross-source issues into a single row contract
- join issue ranking data with `session_issue_links`, latest session, latest
  agent run, and latest PR state
- expose whether the latest run was `auto` or `manual`
- preserve org scoping on every query path
- return typed string fields for enum-like values such as source type,
  issue status, run state, trigger mode, and available action
- add tests that verify org filters are present on every query and that display
  state precedence is stable

The read model should treat issues and sessions as distinct concepts. Issues are
the queue item; sessions are the execution history attached through
`session_issue_links`.

### Start Run Behavior

`Start run` should open a slim confirmation sheet in v1 rather than launching
immediately. The sheet should show the inferred repo, linked issue context,
selected coding agent, and any blocking prerequisites. Agent or prompt overrides
can be added later, but v1 should at least prevent accidental execution and make
policy blocks legible.

Submitting the sheet creates a session linked to the issue as `primary` and
enqueues the agent run using the same execution path as existing issue-triggered
sessions. The row should optimistically move to `Queued` only after the create
mutation succeeds.

### Ranking Contract

The detailed scoring algorithm can evolve, but the queue needs a stable contract:

- every row has a deterministic `rank`
- ties break by higher impact, then higher implementation ease, then newer
  source activity, then issue ID for stable pagination
- `low_hanging_fruit.label` is derived from the rank inputs, not manually entered
- `low_hanging_fruit.reasons` should contain two to four short explanations
- stale analysis keeps the previous rank but marks `meta.summary.analyzed_at` as
  stale in the UI
- issues missing repo attribution can rank, but they are not `ready_to_run` until
  repo can be inferred or selected by the operator

### Frontend Implementation

- replace the current hero-dominant layout with summary strip + table
- reuse shared dense table, badge, and drawer primitives where possible
- keep keyboard list navigation (`j`/`k`) and command-palette integration
- use URL-backed filters so queue views are shareable
- use shadcn/ui controls for filters, table actions, drawer/sheet content, and
  empty states
- keep the existing `/onboarding` redirect guard for missing required setup

### Rollout

Recommended implementation sequence:

1. Backend queue endpoint with summary, pagination, row state derivation, and
   tests.
2. Frontend queue table with filters, summary strip, empty states, and manual
   start confirmation.
3. Issue preview and run-history drawers.
4. Cluster explanations, richer source-native detail, and any bulk actions.

## Open Questions

1. Should `Autopilot` support bulk actions, or is per-row action enough for the
   first version?
2. How should `Canny` and support issues without strong repo attribution be
   ranked against highly actionable Sentry and Linear issues?
3. Should archived/terminal issues be available behind an explicit filter after
   v1, or should they remain source-detail-only?

## Success Criteria

- Operators can understand backlog priority and run status from one page.
- The highest-ranked low-hanging-fruit issue is visible in the first viewport.
- Users can tell, per row, whether Autopilot already acted.
- Manual run kick-off no longer requires leaving the Autopilot queue.
- The page supports mixed-source issue intake without collapsing into separate
  source silos.
