# 32 - Project Cadence and Lifecycle (Evergreen + Finite)

> **Status:** Backlog | **Last reviewed:** 2026-05-06
>
> **Implementation notes:** Scheduling fields exist (schedule_enabled, cadence_type, cadence_value, timezone). Missing: project type (finite/evergreen) distinction, run conditions, no-op cycle recording, quick actions tab.

**Depends on**: [29-projects.md](../implemented/29-projects.md), [30-pm-agent-ux-elevation.md](30-pm-agent-ux-elevation.md), [31-automations-tab.md](../implemented/31-automations-tab.md)

## 1. Decision Summary

The simplest long-term product model is:

1. Keep `Projects` as the primary planning and execution surface.
2. Keep automation behavior inside projects as reusable `Quick Actions` and optional scheduled runs.
3. Avoid introducing a separate top-level Workflows product area for now.

This keeps the mental model clean: users think in outcomes, and projects are outcomes.

## 2. Product Model

### 2.1 Project types

Each project is explicitly one of two lifecycle types:

1. `finite`
2. `evergreen`

`finite` projects are scoped deliverables that complete.
`evergreen` projects are ongoing quality/reliability tracks that continuously run.

### 2.2 Shared scheduling policy

Both project types use one scheduling policy shape:

- `schedule_enabled` boolean
- `cadence_type` (`every_n_days` or cron)
- `cadence_value` (e.g., `3` days or `0 4 * * *`)
- `timezone`
- `next_run_at`
- `last_run_at`
- `max_concurrent_cycles` (default `1`)
- `run_conditions` (optional gates: new issues, new commits, unresolved task count, etc.)

## 3. Visual / Mental Model

### 3.1 Navigation

Keep navigation simple:

- Overview
- Sessions
- Projects

No separate top-level Workflows nav.

### 3.2 Project detail tabs

Project detail contains:

1. `Plan`
2. `Tasks`
3. `Quick Actions`
4. `History`

`Quick Actions` are project-scoped automation templates (flaky tests, security sweep, backlog triage, etc.).

## 4. Execution Diagrams

### 4.1 Finite project run flow

```text
project active + schedule due
  -> evaluate gates
  -> create project cycle
  -> PM analyzes/plans
  -> delegate runs
  -> update progress
  -> check completion criteria
     -> met: mark completed, disable schedule
     -> not met: compute next_run_at
```

### 4.2 Evergreen project run flow

```text
project active + schedule due
  -> evaluate gates
  -> create maintenance cycle
  -> PM analyzes/plans
  -> delegate runs (if needed)
  -> record outcome
     -> meaningful changes: normal cycle
     -> no meaningful changes: no-op cycle
  -> compute next_run_at
```

No automatic `completed` transition for evergreen projects.

## 5. Scheduler Semantics

### 5.1 Due selection

Project is due when all are true:

1. `status = active`
2. `schedule_enabled = true`
3. `next_run_at <= now()`
4. no in-progress cycle for the same project (unless explicitly allowed)

### 5.2 Run gates

Before creating a cycle:

1. budget/capacity gate
2. org concurrency gate
3. project concurrency gate
4. optional run conditions gate

If gates fail, skip run and advance `next_run_at` with reason recorded.

## 6. Evergreen vs Finite Product Expectations

### 6.1 Evergreen projects should optimize for health, not completion

Track:

1. backlog size trend
2. average issue age
3. flaky test count
4. unresolved security findings
5. no-op streak

Add optional archive/sunset rules:

1. archive after N no-op cycles
2. archive after explicit owner confirmation

### 6.2 Finite projects should optimize for closure

Require:

1. explicit completion criteria
2. clear scope
3. success checkpoints

On completion:

1. mark `completed`
2. stop schedule
3. optionally create a low-frequency monitor schedule

## 7. Data Model Direction

Keep current `projects`, `project_tasks`, `project_cycles`, and add minimal fields:

1. `projects.type` (`finite` | `evergreen`)
2. `projects.schedule_enabled`
3. `projects.cadence_type`
4. `projects.cadence_value`
5. `projects.timezone`
6. `projects.next_run_at`
7. `projects.run_conditions` (jsonb)

No separate playbook tables required in this phase.

## 8. API Direction

Project-centric endpoints only:

1. `PATCH /api/v1/projects/{id}` for schedule/lifecycle config
2. `POST /api/v1/projects/{id}/run` manual run
3. `GET /api/v1/projects/{id}/cycles` run history
4. `GET /api/v1/projects/{id}` includes scheduling and lifecycle metadata

Quick actions can map to existing executors internally.

## 9. Implementation Phases

### Phase 1: Lifecycle and scheduling primitives

1. add project `type` + schedule fields
2. add scheduler support for due projects
3. add cycle no-op recording

### Phase 2: Project UX refinement

1. add schedule editor in project settings
2. add finite vs evergreen creation choice
3. add quick actions panel

### Phase 3: Policy and reliability

1. add run conditions
2. add run skip reasons and timeline events
3. add archive/sunset controls for evergreen

## 10. Why this is the best fit now

1. Very clear user model: "all strategic work lives in Projects."
2. Flexible enough for both ongoing maintenance and scoped initiatives.
3. Uses existing PM + jobs infrastructure with minimal conceptual overhead.
4. Leaves room to extract reusable org-level automation later if truly needed.
