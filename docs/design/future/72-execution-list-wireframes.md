# 72 - Execution List Wireframes

> **Status:** Proposed | **Last reviewed:** 2026-05-06

## Goal

Show how the automations page should adopt a cleaner execution-list layout:

- automation runs use a strong execution-row anatomy,
- quiet-run grouping remains intact,
- the scope is limited to `/automations/:id` for now.

These are intentionally low-fidelity wireframes to validate structure, not final visual design.

## Rollout note

These wireframes are intentionally scoped to the automations page. `/sessions` stays unchanged. The automation detail page adopts Wireframe A to test the row anatomy on a lower-risk surface before any broader execution-list changes are considered.

## Shared row anatomy

Every execution item should answer the same scan questions in the same order:

1. status
2. title or result summary
3. actor or trigger
4. recency
5. optional secondary detail (confidence, failure snippet, repo, PR, quiet/no-op context)

Base row structure:

```text
[status]  Primary title / summary                      [time]
          Secondary metadata rail
          Optional snippet / result / error
```

---

## Wireframe A - Balanced default

This is the recommended starting point.

### Sessions page

```text
+----------------------------------------------------------------------------------+
| Sessions                                                         [New session]   |
| Each agent execution creates a session.                                         |
+----------------------------------------------------------------------------------+
| [All 128] [Active 6] [Decisions]                              [Mine v] [Repo v] |
+----------------------------------------------------------------------------------+
| ● Running  Fix Slack OAuth callback timeout                         2m ago        |
|            Codex · triggered by Alice · repo: api                                  |
|            Waiting on sandbox command…                                            |
+----------------------------------------------------------------------------------+
| ● Needs guidance  Add org filter to audit export                  12m ago        |
|                   Codex · triggered by Bob · confidence 61%                        |
|                   Needs decision on CSV format for enterprise exports              |
+----------------------------------------------------------------------------------+
| ● Failed   PM weekly planning pass                                 1h ago         |
|            PM agent · automatic · repo: web                                        |
|            Context package missing for frontend routing docs                       |
+----------------------------------------------------------------------------------+
| Showing 50 of 128                                                     [Show more] |
+----------------------------------------------------------------------------------+
```

### Automation runs page

```text
+----------------------------------------------------------------------------------+
| Flaky test cleanup                                                [Pause] [Run]  |
| every 1 day at 09:00 PDT · Next: tomorrow 9:00 AM                               |
+----------------------------------------------------------------------------------+
| [Runs] [Settings]                                                                 |
+----------------------------------------------------------------------------------+
| ● Completed  Removed 2 stale retries from payments spec            8m ago        |
|              Scheduled run · linked session · PR #412                               |
|              Confidence 88% · 3 files changed                                      |
+----------------------------------------------------------------------------------+
| ▸ 5 quiet runs                                                     last one 2d ago|
+----------------------------------------------------------------------------------+
| ● Failed     Security sweep found dependency issue                 4d ago         |
|              Manual run by Alice · linked session                                   |
|              pnpm audit failed because lockfile was out of date                    |
+----------------------------------------------------------------------------------+
| Showing 7 runs                                                          [Load more]|
+----------------------------------------------------------------------------------+
```

### Why this works

- Sessions and runs clearly belong to the same family.
- Sessions still feel like an inbox.
- Automations still feel scoped and history-oriented.
- Quiet runs remain a special grouped row, not a separate UI system.

### Tradeoff

- Slightly less dense than the current sessions table.

---

## Wireframe B - Dense ops variant

This version keeps more of the table-like efficiency while using stacked rows instead of a full table.

### Shared row shape

```text
+----------------------------------------------------------------------------------+
| ● Running   Fix Slack OAuth callback timeout      Alice      82%      api   2m ago|
|   Waiting on sandbox command…                                                   |
+----------------------------------------------------------------------------------+
```

### Sessions page

```text
+----------------------------------------------------------------------------------+
| Sessions                                                         [New session]   |
+----------------------------------------------------------------------------------+
| [All 128] [Active 6] [Decisions]                              [Mine v] [Repo v] |
+----------------------------------------------------------------------------------+
| ● Running   Fix Slack OAuth callback timeout      Alice      82%      api   2m ago|
|   Waiting on sandbox command…                                                   |
+----------------------------------------------------------------------------------+
| ● Failed    PM weekly planning pass               Auto        --      web   1h ago |
|   Context package missing for frontend routing docs                               |
+----------------------------------------------------------------------------------+
```

### Automation runs page

```text
+----------------------------------------------------------------------------------+
| Flaky test cleanup                                                [Pause] [Run]  |
+----------------------------------------------------------------------------------+
| [Runs] [Settings]                                                                 |
+----------------------------------------------------------------------------------+
| ● Completed Removed 2 stale retries            schedule    88%   PR #412  8m ago |
|   linked session · 3 files changed                                                |
+----------------------------------------------------------------------------------+
| ▸ 5 quiet runs                                            no changes     2d ago   |
+----------------------------------------------------------------------------------+
| ● Failed    Security sweep found issue         Alice       --    no PR    4d ago  |
|   pnpm audit failed because lockfile was out of date                              |
+----------------------------------------------------------------------------------+
```

### Why this works

- Keeps fast scanning for power users.
- Still uses one row anatomy across both pages.
- Easier migration path from the current sessions table.

### Tradeoff

- More utilitarian, less room for result summaries and nuance.

---

## Wireframe C - Narrative variant

This version leans more toward a card-feed while preserving a common anatomy.

### Sessions page

```text
+----------------------------------------------------------------------------------+
| Sessions                                                         [New session]   |
| Each agent execution creates a session.                                         |
+----------------------------------------------------------------------------------+
| [All 128] [Active 6] [Decisions]                              [Mine v] [Repo v] |
+----------------------------------------------------------------------------------+
| ● Running                                                                      2m ago|
| Fix Slack OAuth callback timeout                                                    |
| Codex · triggered by Alice · repo: api                                              |
| Waiting on sandbox command…                                                         |
| [Open session]                                                                      |
+----------------------------------------------------------------------------------+
| ● Needs guidance                                                               12m ago|
| Add org filter to audit export                                                    |
| Codex · triggered by Bob · confidence 61%                                          |
| Needs decision on CSV format for enterprise exports                                |
| [Open session]                                                                      |
+----------------------------------------------------------------------------------+
```

### Automation runs page

```text
+----------------------------------------------------------------------------------+
| Flaky test cleanup                                                [Pause] [Run]  |
| every 1 day at 09:00 PDT · Next: tomorrow 9:00 AM                               |
+----------------------------------------------------------------------------------+
| [Runs] [Settings]                                                                 |
+----------------------------------------------------------------------------------+
| ● Completed                                                                    8m ago|
| Removed 2 stale retries from payments spec                                       |
| Scheduled run · linked session · PR #412                                         |
| Confidence 88% · 3 files changed                                                 |
| [Open run]                                                                        |
+----------------------------------------------------------------------------------+
| ▸ 5 quiet runs · all no-op’d                                        last one 2d ago|
+----------------------------------------------------------------------------------+
| ● Failed                                                                       4d ago|
| Security sweep found dependency issue                                            |
| Manual run by Alice · linked session                                             |
| pnpm audit failed because lockfile was out of date                               |
| [Open run]                                                                        |
+----------------------------------------------------------------------------------+
```

### Why this works

- Easiest to read for newer users.
- Gives runs and sessions more breathing room.
- Strong fit if result summaries are important product value.

### Tradeoff

- Least dense; long histories take more vertical space.

---

## Recommendation

Start with **Wireframe A** on the automation detail page only.

It has the best balance of:

- consistency across sessions and runs,
- enough density for repeated operational use,
- enough room for failure and result snippets,
- a clean mobile path.

For the pilot:

- leave `/sessions` unchanged,
- apply this row pattern only to `/automations/:id` runs,
- keep quiet-run grouping,
- reuse the row anatomy later only if the pilot proves out.

If the team later wants to preserve more of the current sessions power-user feel, move slightly toward **Wireframe B**. If storytelling and result summaries matter more than scan density, move toward **Wireframe C**.

## Mobile sketch

All three options should collapse to the same mobile card shape:

```text
+-------------------------------------------+
| ● Running                         2m ago  |
| Fix Slack OAuth callback timeout          |
| Codex · Alice · api                       |
| Waiting on sandbox command…               |
+-------------------------------------------+
```

Quiet-group row:

```text
+-------------------------------------------+
| ▸ 5 quiet runs               last one 2d  |
+-------------------------------------------+
```

## Implementation note

The cleanest component path is likely:

- `ExecutionList`
- `ExecutionRow`
- `ExecutionGroupRow`
- shared status token component
- shared list footer for counts + pagination

Then sessions and automation runs can compose the same primitives with different metadata fields and optional grouping.
