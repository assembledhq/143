# 31 - Automations Tab (On-Demand Workflows)

> **Status:** Implemented | **Last reviewed:** 2026-05-06
>
> **Implementation notes:** Superseded by [48-automations-separation.md](48-automations-separation.md). The server-backed automations model, API, scheduler integration, and frontend surfaces now exist; this doc remains as the earlier MVP framing.
>
> **2026-05-14 update:** Automations now include `icon_type`/`icon_value` identity fields. The supported type is `emoji`; the split is deliberate so image-backed icons can be added later without changing list/detail API consumers.

> Add an `Automations` surface that lets teams define reusable workflows and run them on command, while staying aligned with the existing PM + jobs architecture.

## Goal

Give users a simple way to create and execute repeatable automations (for example: flaky-test cleanup, security sweeps, backlog triage) without introducing a second orchestration system.

## Current Framework Fit

This is reasonable within the current framework, because core primitives already exist:

- **Execution primitive**: manual sessions (`POST /api/v1/sessions/manual`) can run arbitrary code-change workflows from instructions.
- **Planning primitive**: PM analysis (`POST /api/v1/pm/analyze`) already handles strategic triage and delegation.
- **Queue primitive**: `jobs` table + worker handlers already executes asynchronous work reliably.
- **Cadence primitive**: PM scheduler already runs on `pm_schedule_hours` and can remain the default background automation.

The main risk is adding a parallel automation engine. The design should instead make automations a thin orchestration layer over these existing primitives.

## MVP Implemented

- Added a new dashboard navigation tab: `/automations`.
- Added a page to:
  - create automations (name, instructions, execution mode),
  - bootstrap from suggested templates (flaky tests, security fixes, codebase improvements, Linear triage),
  - run automations on command (`Run Now`).
- `Run Now` reuses existing APIs:
  - `manual_session` mode -> `POST /api/v1/sessions/manual`
  - `pm_analysis` mode -> `POST /api/v1/pm/analyze`
- Saved automations are currently client-side persisted (local storage), intentionally minimizing backend complexity for iteration speed.

## PM Assessment: Should Framework Change?

Short answer: **yes, but incrementally**.

The concept fits the current architecture now, but long-term flexibility needs light backend formalization:

1. **Server-side automation persistence**
   - Add `automations` table (`org_id`, `name`, `instructions`, `executor_type`, `active`, timestamps).
   - Keeps automations shareable across team members and environments.

2. **Execution tracking**
   - Add `automation_runs` table (`automation_id`, `trigger`, `status`, `started_at`, `completed_at`, `linked_session_id`, `linked_pm_plan_id`, error fields).
   - Enables reliability reporting and auditability without reinventing run orchestration.

3. **Trigger model**
   - Keep PM cron unchanged as one built-in automation.
   - Add optional per-automation triggers over time:
     - on-demand (now),
     - schedule,
     - event-driven (GitHub comments/webhooks) later.

4. **Single executor abstraction**
   - Keep executors constrained to known internal actions (`manual_session`, `pm_analysis`, future `project_cycle`).
   - Avoid arbitrary plugin execution in early versions to keep operational risk low.

## Recommended Next Step

If adoption is strong, implement server-backed `automations` + `automation_runs` first, while continuing to call the same underlying endpoints/jobs. This preserves architectural simplicity and gives flexibility without a separate orchestration stack.
