# Design: Goal-First Automation UX

> **Status:** Implemented | **Last reviewed:** 2026-05-22

## Problem

The current automation creation and detail views expose automations primarily as settings forms. That makes recurring agent work feel like configuration, even though the most important artifact is the goal: the instruction the agent will repeatedly execute.

For an AI-native product, users should feel like they are defining a durable teammate or recurring job by writing its mission, then choosing a few execution defaults. The details page should make the goal readable, inspectable, and easy to improve. Metadata, schedule, model, and run history remain important, but they should support the goal rather than compete with it.

## Goals

- Make the automation goal the dominant surface on both creation and detail.
- Keep creation close to the manual session composer pattern: a large prompt box, compact inline settings, and a single primary create action.
- Move low-frequency settings into an advanced surface so the default flow is fast.
- Make automation detail feel closer to session detail: main content on the left, supporting metadata and runs on the right.
- Preserve existing automation capabilities: repository, schedule, identity scope, model, reasoning, base branch, priority, pre-PR review, templates, pause/resume, run now, and run history.

## Non-Goals

- Do not redesign the automations list page in this pass.
- Do not change the backend automation execution model.
- Do not remove templates; change how they are presented so they assist prompt creation without dominating the page.

## Direction: Composer-First Creation, Reading-First Detail

Use a composer-first creation flow and a reading-first detail view. Keep the existing automation data model and execution semantics, but change the UI hierarchy so the goal is the central object and settings are supporting controls.

Borrow one element from the existing template system: a compact template picker. Templates should insert high-quality structured goal starters from the existing frontend template catalog, not become a large gallery above the composer.

### Creation

The default `/automations/new` surface becomes a centered composer similar to `/sessions/new`, with the goal as the main input.

Primary anatomy:

- A large goal editor with placeholder text like "Describe what this automation should do every run..."
- A seamless title row inside the composer sheet with the automation emoji picker and name. The emoji starts as the existing gear default and can be changed before creation.
- Compact inline controls in the composer footer:
  - repository,
  - triggers, including schedule cadence and PR event entry points,
  - run identity,
  - model,
  - reasoning when supported,
  - template picker.
- A secondary `Advanced` button opens a sheet for lower-frequency fields:
  - base branch,
  - priority,
  - pre-PR review passes,
  - timezone details,
  - optional scope.
- The create button remains in the composer footer, aligned with the session composer pattern.

Templates should feel like prompt starters, not cards competing with the form. Use a compact template menu or command-style picker from the composer footer. Selecting a template fills the goal and suggested settings, then returns focus to the goal editor.

Desktop wireframe:

```
+--------------------------------------------------------------+
| New automation                                               |
| Create a recurring agent for this team.                      |
|                                                              |
|        +----------------------------------------------+      |
|        | [gear]  High severity bug fixes              |      |
|        |                                              |      |
|        | Describe what this automation should do      |      |
|        | every run...                                 |      |
|        |                                              |      |
|        | ## Goal                                      |      |
|        | Inspect recent commits and identify...       |      |
|        |                                              |      |
|        +----------------------------------------------+      |
|        | Repo v  Daily 9:00 v  Codex v  Medium v      |      |
|        | Templates v          Advanced v      Create  |      |
|        +----------------------------------------------+      |
|                                                              |
+--------------------------------------------------------------+
```

Advanced settings sheet:

```
+----------------------------+
| Advanced settings          |
+----------------------------+
| Scope                      |
| Run as                     |
| Base branch                |
| Pre-PR review passes       |
| Priority                   |
| Timezone                   |
|                            |
|              Cancel  Apply |
+----------------------------+
```

### Detail

The default `/automations/:id` view becomes a goal reading surface.

Desktop anatomy:

- Left main column:
  - automation title and compact action bar,
  - goal rendered as readable markdown,
  - optional inline edit mode for the goal,
  - latest run outcome summary below the goal,
  - longer run history after that if the right rail is too constrained.
- Right rail:
  - status, next run, last run,
  - repository, schedule, run identity, model, reasoning,
  - run now / pause / resume actions,
  - recent runs using the existing execution-row pattern from `72-execution-list-wireframes.md`.

Mobile anatomy:

- Goal appears first.
- Metadata collapses into a details sheet or accordion reachable from the top action row.
- Runs appear below the goal as a compact list.

Desktop wireframe:

```
+-------------------------------------------------------------------------+
| Automations / High severity bug fixes                    Run now  Pause |
+-----------------------------------------------+-------------------------+
| High severity bug fixes                       | Status                  |
|                                               |   Paused                |
| You are a deep bug-finding automation...      | Next run                |
|                                               |   -                     |
| ## Goal                                      | Last ran                |
| Inspect recent commits and identify...        |   Apr 21, 2026 9:01 AM |
|                                               |                         |
| ## Investigation strategy                     | Details                 |
| - Focus on behavioral changes...              |   Repo: assembled       |
| - Look for data corruption...                 |   Runs in: Worktree     |
|                                               |   Schedule: Weekdays... |
| ## Fix strategy                               |   Model: GPT-5.3-Codex  |
| - If you find a critical bug...               |   Reasoning: Medium     |
|                                               |                         |
| Latest run                                    | Previous runs           |
| Completed, opened PR #412                     |   ! Failed  1mo         |
|                                               |   - No-op   1mo         |
| View all runs                                 |   ! Failed  1mo         |
+-----------------------------------------------+-------------------------+
```

Mobile wireframe:

```
+------------------------------+
| High severity bug fixes  ... |
| Paused - assembled           |
+------------------------------+
| Goal                         |
| ## Goal                      |
| Inspect recent commits...    |
|                              |
| ## Investigation strategy    |
| - Focus on behavior...       |
+------------------------------+
| Latest run                   |
| Completed, opened PR #412    |
+------------------------------+
| Previous runs                |
| ! Failed  1mo                |
| - No-op   1mo                |
+------------------------------+
```

## Template Picker

Use the existing `automationTemplates` catalog as the source of truth. The picker is a compact command/menu surface opened from the composer footer:

- Show featured templates first.
- Include search over template name, summary, and tags.
- Selecting a template applies:
  - `name`,
  - `goal`,
  - `defaultInterval`,
  - `defaultUnit`.
- Preserve user-selected repository, model, reasoning, identity scope, and advanced settings unless the user explicitly resets them.
- Keep the dedicated `/automations/templates` page for deeper browsing and prompt preview.

The template picker should not require new backend schema. Templates remain frontend-authored prompt starters.

## Backend Impact

This should not require backend schema changes. Existing automation fields already support the new UI:

- `icon_type` and `icon_value` map to the composer title-row emoji picker.
- `name` maps to the composer title field.
- `goal` maps to the primary composer and detail reading surface.
- `repository_id`, `interval_value`, `interval_unit`, `interval_run_at`, `timezone`, `model_override`, `reasoning_effort`, `identity_scope`, `base_branch`, `priority`, `scope`, and `pre_pr_review_loops` map to compact or advanced controls.
- `automation_runs.goal_snapshot` continues to preserve the exact goal used by each historical run.

This likely does not require API changes either. The first pass should keep the existing automation API shape:

```http
POST /api/v1/automations
PATCH /api/v1/automations/:id
GET /api/v1/automations/:id
GET /api/v1/automations/:id/runs
POST /api/v1/automations/:id/run
POST /api/v1/automations/:id/pause
POST /api/v1/automations/:id/resume
```

Detail can use the existing `GET /api/v1/automations/:id` plus `GET /api/v1/automations/:id/runs`. If the right rail needs only recent runs, request the first page with a small limit. If the current API does not expose `limit`, add `?limit=10` to the runs endpoint instead of adding a new route.

Settings edits should continue to use `PATCH /api/v1/automations/:id`. The UI can group advanced-sheet changes into one save, while simple toggles like pause/resume and run-now keep their dedicated endpoints.

## Implementation Plan

1. Create a shared automation composer component.
   - Use `AutomationGoalEditor` as the dominant input.
   - Put the emoji picker and name in the composer title row, defaulting the emoji to the current gear icon.
   - Put repo, schedule, identity, model, and reasoning in a compact footer.
   - Move scope, base branch, priority, and pre-PR review into an advanced sheet.

2. Redesign `/automations/new`.
   - Replace the current stacked template cards and form sections with the composer-first layout.
   - Keep templates accessible through a compact footer picker and `/automations/templates`.
   - Keep `NoReposWarning` and permission behavior unchanged.

3. Redesign `/automations/:id`.
   - Replace the tab-first structure with a two-column layout on desktop.
   - Render the goal as the primary main-column content.
   - Move status, schedule, repo, model, reasoning, and run actions into a right rail.
   - Keep settings editable through inline edit affordances or an advanced settings sheet.

4. Reposition run history.
   - Show a small recent-runs list in the right rail.
   - Keep the fuller execution list below the goal or behind a `View all runs` section.
   - Continue using the execution-row direction from `future/72-execution-list-wireframes.md`.

5. Validate responsive behavior.
   - On mobile, show goal first, details in a sheet, and runs below.
   - Ensure composer controls wrap without text overlap.

6. Tests and verification.
   - Add or update React tests for creation, template insertion, advanced sheet behavior, detail goal rendering, and right-rail metadata.
   - Run `npm run typecheck`, `npm run lint`, and `npm run build` from `frontend/`.

## Open Questions

- Should automation names be required, optional, or generated from the goal?
- Should the default schedule stay visible in the composer footer or open as a popover from a single cadence chip?
- Should settings edits on detail auto-save individually, or require an explicit save in the advanced sheet?
- Should run history remain partly visible in the right rail when there are many failures, or should failures promote into a main-column banner?
