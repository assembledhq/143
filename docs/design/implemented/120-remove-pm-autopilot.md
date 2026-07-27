# 120 - Remove PM Agent And Autopilot

> **Status:** Implemented | **Last reviewed:** 2026-07-26
>
> **Applies to:** Autopilot UI, PM analysis and context jobs, PM-driven issue
> and project creation, prioritization, organization and repository PM
> settings, PM plans and decisions, and shared session context.

## Decision Summary

Remove the PM Agent and Autopilot product from 143.

All four PRs are implemented. The product, background execution paths, feature
APIs, settings, UI, and obsolete backend machinery are removed. Historical PM
records remain readable, while shared eval and session context use neutral
reference-document and execution-context contracts.

The current feature has not proven effective enough to justify its product
surface, background compute, automatic work creation, or backend complexity.
The removal must do more than hide `/autopilot`: PM analysis can be scheduled,
started manually, triggered by integration setup, refreshed periodically, and
can create sessions, issues, and project proposals through several independent
paths.

The removal used four pull requests:

1. **Make PM and Autopilot inert.** Stop every automatic PM job and every
   PM-owned path that can create or dispatch work. Preserve compatibility with
   the existing UI and historical data during this safety deployment.
2. **Make PM and Autopilot absent.** Remove the UI, feature API, settings
   surfaces, documentation, and dedicated Autopilot queue implementation while
   retaining historical records.
3. **Remove obsolete machinery.** Simplify the backend and data model after the
   shutdown has been deployed and observed. Retain standalone priority scoring,
   preserve eval reference-document pins under neutral names, remove PM Project
   planning, and neutralize shared session-context names.
4. **Contract rollout compatibility.** Promote the neutral schema projections
   to base tables, remove legacy dual-write columns and triggers, retire the
   temporary disabled-job barrier, and remove PM-only Project provenance.

This sequencing creates clear rollout and rollback boundaries:

- PR 1 stops side effects and compute.
- PR 2 removes the user-facing product.
- PR 3 performs destructive or cross-cutting cleanup.

## Goals

1. Ensure 143 never starts a coding session merely because PM or Autopilot
   selected work.
2. Stop scheduled and integration-triggered PM sandboxes and context refreshes.
3. Remove Autopilot from navigation, settings, onboarding, public positioning,
   and documentation.
4. Remove the dedicated Autopilot issue-and-run queue and its API.
5. Preserve manual sessions, explicit issue starts, user-authored Automations,
   historical sessions, PRs, projects, audit records, and eval reproducibility.
6. Simplify PM-specific services, settings, stores, models, prompts, and schema
   after no live workflow depends on them.
7. Decouple retained Project behavior from organization-level Autopilot
   autonomy.
8. Preserve standalone priority scoring, non-PM internal issue creation, and
   eval reference-document pinning.

## Non-Goals

- Removing user-authored Automations, scheduled automations, or event-triggered
  automations.
- Removing explicit session starts from Slack, Linear, PagerDuty, GitHub, the
  external API, or the 143 UI.
- Removing PR health synchronization, repair workflows, code review,
  previews, or preview lifecycle automation.
- Removing per-session autonomy. `sessions.autonomy_level` controls behavior
  inside an already-started session and is separate from the organization PM
  policy with the same historical name.
- Deleting historical PM-created sessions, issues, projects, PRs, or audit
  events.
- Rewriting old migrations. New migrations will evolve the current schema.
- Renaming shared session-context fields in the initial shutdown.

## Product Boundary

### Remove

- The Autopilot workspace and decision-history pages.
- Autopilot organization and repository settings.
- Scheduled and manually triggered general PM analysis.
- Automatic PM context bootstrap and refresh.
- PM-driven selection and dispatch of coding sessions.
- PM-created issues that automatically dispatch sessions.
- Automatically generated PM project proposals, unless explicit project
  planning is deliberately retained under a new product boundary.
- PM status, current recommendation, decision history, and plan history as
  product surfaces.
- Autopilot-specific priority eligibility and run-state presentation.

### Retain

- Manual Sessions.
- Explicit "start session" actions for issues.
- User-authored Projects and tasks.
- User-authored Automations and their scheduling/event machinery.
- Runtime concurrency limits.
- Historical rendering of PM sessions and PM-created records.
- Eval input snapshots and reference-document pins until they are migrated to
  a neutral model.
- Shared session execution briefs currently stored under PM-named fields.

### Projects

Projects reuse PM service code but are not equivalent to Autopilot.

Today, a general PM analysis may produce project plans and dispatch project
tasks. A project also has an explicit `Run now` action that enqueues a
`project_cycle` job. Both PM-driven paths will be removed.

The removal will:

- preserve human-authored Projects;
- preserve manual Project and task management;
- prevent general PM analysis from creating or dispatching project work;
- stop organization `autonomy_level` from silently governing Project dispatch;
- remove the Project `Run now` action and `project_cycle` job;
- remove PM-proposed project creation, metadata, proposal summaries,
  deduplication, and rate limits.

Projects remain a user-authored planning and tracking surface. Removing PM
planning must not remove Project CRUD, task management, progress tracking, or
other behavior that does not depend on PM.

## Current Execution Paths

Removing the UI alone would leave the following paths active.

### Scheduled PM Analysis

The cluster scheduler enumerates organizations with active integrations,
resolves `pm_schedule_hours`, and enqueues `pm_analyze` at the organization or
repository level. The default interval is 24 hours.

```text
cluster scheduler
  -> pm_analyze job
  -> PM worker handler
  -> PM sandbox
  -> persisted PM plan and decisions
  -> executePlan
  -> session rows and run_agent jobs
```

The default organization autonomy is `auto_simple`, not manual. A new
organization may therefore dispatch work unless a user changes its settings.

### Manual PM Analysis

`POST /api/v1/pm/analyze` enqueues the same `pm_analyze` job. The Sessions page
also exposes this through the PM status banner's `Run now` action.

Disabling the scheduler without disabling this endpoint would leave the
feature operational.

### Integration-Triggered Context Work

After an integration is connected, `maybeEnqueuePMContext` automatically
enqueues:

- `pm_bootstrap` when no autogenerated PM document exists; or
- `pm_context_refresh` when an autogenerated document already exists.

These are agent-backed background processes even if they do not dispatch a
coding session.

### Periodic Context Refresh

The scheduler checks the age of autogenerated PM documents and enqueues
`pm_context_refresh` according to `context_refresh_interval_days`, defaulting
to 14 days.

### PM Plan Execution

After a PM plan is parsed and persisted, `executePlan`:

1. resolves the organization concurrency limit;
2. checks organization autonomy and task confidence;
3. creates coding sessions;
4. writes an initial delegated message;
5. marks selected issues as triaged;
6. enqueues `run_agent`;
7. records PM plan/session links and task status.

The same analysis then calls `executeProjectPlan` for any project plans in the
PM output.

### Internal Issue Creation And Dispatch

`POST /api/v1/internal/issues` is a separate automatic dispatch path. It:

1. creates an issue with source `pm_agent`;
2. creates a pending coding session;
3. enqueues `run_agent`.

This can happen while the PM sandbox is still running and before
`executePlan`. A guard only at `executePlan` is therefore insufficient.

The current endpoint accepts broadly valid internal sandbox tokens and only
explicitly blocks automation-goal-improvement sessions. The PM service uses a
legacy unscoped internal token without a session origin or tool-scope claim,
so the handler cannot reliably distinguish a PM caller from another legacy
caller.

The internal issue capability is not structurally PM-only and may be used by
ordinary coding-agent workflows. Preserve its non-PM behavior. Remove the PM
service and its access to this endpoint rather than changing the endpoint's
contract globally.

Broad internal-token scope hardening may be valuable, but it is a separate
security change. It is not required for this removal once PM execution and PM
token generation are gone.

### Internal Project Proposals

`POST /api/v1/internal/projects/propose` lets an agent create a draft Project
and seed tasks. It includes PM-specific:

- per-token and per-repository proposal caps;
- project deduplication;
- `proposed_by_pm`;
- proposal reasoning;
- source issue links;
- similar-project metadata.

Remove this PM-only endpoint and its tool integration. Human-authored Projects
continue through the normal Project API.

### Priority Recalculation

Ingestion and the public API can enqueue `prioritize`. The worker computes
priority scores and complexity estimates. It no longer calls
`CheckAutoTrigger`, but the service still contains a complete dead automatic
dispatch implementation and tests.

Priority data is also exposed outside the Autopilot queue:

- `GET /api/v1/issues/{id}/priority`
- `GET /api/v1/issues/{id}/complexity`
- `GET /api/v1/priority-scores`
- `POST /api/v1/issues/{id}/reprioritize`
- issue listing with `sort=priority`

Retain priority scoring as a standalone issue feature initially. Remove its
automatic eligibility and dispatch semantics, including dead
`CheckAutoTrigger`, while preserving priority sorting, complexity estimates,
manual reprioritization, and their existing API contracts. Usage can be
evaluated separately after the Autopilot removal.

## User Interface Removal

Delete these routes:

- `/autopilot`
- `/autopilot/decisions`
- `/settings/autopilot`

Delete the Autopilot component family:

- `frontend/src/components/autopilot/`
- `frontend/src/components/autopilot-proposal-card.tsx`

Remove:

- primary navigation and settings navigation links;
- command-palette navigation and settings commands;
- browser page-title mappings;
- the robots exclusion;
- the Sessions PM status banner and manual analysis action;
- onboarding copy that says integrations are required for Autopilot;
- the Autopilot setup checklist;
- landing-page and homepage Autopilot positioning;
- Autopilot query keys, API client methods, TypeScript models, test mocks, and
  tests.

The PM status banner imports shared-looking components from the Autopilot
directory. Those components should be deleted if they have no non-PM consumer,
not moved merely to preserve dead presentation code.

### Repository Settings

Repository details expose `RepoPMSettings`, including:

- product context;
- PM schedule;
- PM model;
- priority weights;
- minimum priority threshold.

Removing only `/settings/autopilot` would leave a second PM configuration
surface. Remove repository PM scheduling and PM-specific labels. Preserve
repository context and priority policy only where required by retained
standalone priority scoring or another non-PM consumer; move retained controls
to a neutral issue or repository context surface.

## API Removal

### Dedicated Autopilot API

Remove:

```http
GET /api/v1/autopilot/queue
```

This permits deletion of:

- `internal/api/handlers/autopilot.go`;
- `internal/db/autopilot_queue.go`;
- `internal/models/autopilot_queue.go`;
- router construction and registration;
- frontend response types and API methods;
- tests and fixtures.

The queue store is a large Autopilot-only query and projection layer joining
issues, sessions, PRs, and previews. Removing it is one of the highest-value
backend simplifications.

### PM API

After PR 1 makes them inert, remove PM endpoints with no retained consumer:

- analysis, current recommendation, status, plans, and decisions;
- bootstrap and refresh;
- pending context refresh suggestions;
- PM document management if documents are not retained under a neutral model;
- PM document-set pin management only after eval ownership is resolved.

Historical records do not require keeping the feature API indefinitely. If
historical PM analysis must remain inspectable, expose it through ordinary
session history rather than a live PM product contract.

### Priority API

Retain the existing priority and complexity endpoints, worker calculation,
issue priority sorting, stores, models, and tables. Delete automatic
eligibility and dispatch semantics and the dead `CheckAutoTrigger` path. A
later independent usage review may remove standalone scoring, but that is not
part of PM removal.

## Settings Model

### Remove From PM Policy

- organization `autonomy_level`;
- `execution_aggressiveness`;
- `pm_schedule_hours`;
- `context_refresh_interval_days`;
- `min_priority_threshold` if priority scoring is removed;
- `priority_weights` if priority scoring is removed;
- legacy `product_direction`;
- repository PM schedule and PM-specific priority overrides.

### Retain

#### Runtime Concurrency

`max_concurrent_runs` is not an Autopilot-only setting. It is used by:

- manual session creation;
- runtime capacity reporting;
- runtime extension decisions;
- orchestrator concurrency enforcement.

Keep it under Runtime as an organization safety limit. Remove duplicate
Autopilot presentation and PM-specific interpretation.

#### Per-Session Autonomy

Keep the session `autonomy_level` column and `SessionAutonomy` type. It
controls validation and interaction behavior inside a session that has already
been explicitly started. It is separate from the organization
`AutonomyLevel` policy that decides whether PM may start work.

#### Product Context

Preserve stored `product_context` for general agent or repository guidance,
but remove its PM-specific processing and presentation:

- remove legacy mirroring to `product_direction`;
- do not run automatic bootstrap or refresh;
- preserve the data as inert compatibility state until a neutral owning
  surface is established.

Legacy keys in organization and repository JSON settings may remain inert for
one compatibility release. They do not need an immediate destructive rewrite.

## Backend Simplification

### Prioritization Service

Delete the dead `CheckAutoTrigger` path and its tests. It has no production
caller but still depends on organization autonomy, concurrency, session
creation, and job enqueueing.

Narrow the service to score and complexity calculation and use
`models.OrgSettings` rather than the duplicate
`prioritization.OrgSettings`. Preserve the `prioritize` worker, ingestion
enqueue sites, priority APIs, issue sorting, prompts, stores, models, and
tables for the standalone scoring feature.

### PM Service

After shutdown, remove unused portions of `internal/services/pm`, including:

- general analysis and context gathering;
- sandbox setup and PM adapter selection;
- plan parsing and persistence;
- decision logging;
- plan execution;
- bootstrap and refresh;
- project proposal generation if not retained;
- PM-specific prompts and prompt renderers;
- PM job handlers and job constants.

Remove Project planning-cycle logic, `project_cycle`, and PM proposal
generation. Preserve Project CRUD, task management, progress calculation, and
other human-authored Project behavior.

### Session Orchestrator

PM-linked session completion currently updates PM decision outcomes. Retain
this hook through PR 1 so historical in-flight PM sessions can finish
consistently. Remove it in PR 3 after no new PM-linked sessions can be created.

### Internal Agent APIs

Remove the PM-only `internal/projects/propose` endpoint and tool integration.
Preserve the existing non-PM `internal/issues` capability and its behavior.
Removing PM execution also removes PM generation of legacy unscoped internal
tokens. A system-wide internal tool-scope redesign is useful security work but
is outside this feature removal.

## Data Model And Migration Strategy

### Preserve Historical Product Records

Do not delete:

- PM-created Sessions;
- issues with source `pm_agent`;
- PM-proposed or PM-generated Projects;
- associated tasks, branches, PRs, previews, logs, usage, or audits.

Historical PM sessions should render in ordinary session history. The
`pm_agent` agent type and issue source remain valid for historical reads after
new PM writes stop. They are removed from selectable/frontend registries but
not rejected when scanning stored records.

Do not rewrite old enum migrations. A later migration may reject new
`pm_agent` writes if desired, but compatibility tests and historical scans must
continue to accept stored values.

### PM Plans And Decisions

Remove live PM plan and decision APIs, services, mutation stores, and new
writes. Preserve existing `pm_plans` and `pm_decision_log` rows as historical
archive data while historical Sessions or Projects reference them. Retain only
the narrow read projections required to render ordinary Session and Project
history; there is no dedicated PM archive UI.

Remove PM-specific plan and decision application enums, completion hooks,
audit emissions, and project-cycle references when no live application path
uses them. Do not drop archive tables or foreign-keyed rows merely to eliminate
unused code.

### Shared Session Context

Current `session_pm_context` contains:

- `pm_plan_id`;
- `pm_approach`;
- `pm_reasoning`;
- `project_task_id`.

The table cannot be dropped with PM:

- `project_task_id` belongs to Projects;
- `pm_approach` is used as a generic prompt or execution brief by Linear,
  PagerDuty, evals, Automations, internal issues, and other session starts;
- `pm_reasoning` is used for prompt and session presentation.

PR 3 introduces the neutral contract and PR 4 completes the physical rename:

```text
session_pm_context      -> session_execution_context
pm_approach             -> execution_brief
pm_reasoning            -> planning_reasoning
```

Remove only `pm_plan_id` after PM plan history is retired. This rename is a
shared contract migration and must not be bundled into the shutdown.

### Reference Documents And Eval Pins

PM documents are not isolated to Autopilot. `pm_document_set_pins` are
referenced by `eval_tasks`, `eval_runs`, and input manifests to preserve eval
reproducibility.

Therefore:

- do not drop PM document or pin tables in PR 1 or PR 2;
- preserve existing pinned document versions;
- preserve manual document and pin functionality required by evals;
- rename them to neutral reference documents and immutable context-set pins in
  PR 3;
- remove automatic PM bootstrap and refresh behavior;
- preserve eval foreign keys, snapshots, and audit display through the rename.

### Priority Data

Preserve priority scores and complexity estimates as a standalone issue
feature. Remove only Autopilot eligibility and automatic-dispatch semantics.
Any future removal requires a separate usage review and design.

### JSON Settings

Organization and repository settings are JSON. Code should stop supplying
defaults for removed keys and stop writing them. Existing keys may remain
stored but ignored until an optional cleanup migration removes them. This
avoids combining feature shutdown with a broad rewrite of tenant settings.

## Operational Rollout

### Pending Jobs

The jobs table supports `cancelled`, but the current store does not expose a
general bulk cancel-by-type operation.

PR 1 must provide a safe transition for pending:

- `pm_analyze`;
- `pm_bootstrap`;
- `pm_context_refresh`.

Preferred approach:

1. add a narrowly scoped store or operator command that changes only pending
   jobs of these exact types to `cancelled`;
2. keep handlers registered during the compatibility window;
3. make handlers return successfully without performing work while removal is
   active;
4. remove handlers and constants only after old pending jobs have been
   drained or cancelled.

PR 1 implements the transition as a database-enforced rollout barrier:

- migration `000259` refuses to proceed while any PM or Project-cycle job is
  still running, so operators must let those jobs finish or drain their old
  workers before retrying the deployment;
- the same migration installs a temporary `jobs` trigger that rejects new
  `pm_analyze`, `pm_bootstrap`, `pm_context_refresh`, and `project_cycle`
  inserts from old API or scheduler processes during rolling replacement;
- after installing the trigger, the migration marks pending jobs of those
  exact types `cancelled` without deleting their history.

The temporary trigger remains through the compatibility window and should be
removed with the obsolete job types in the follow-up removal.

Do not delete job rows. Preserve them for audit and queue history.

### Running Jobs And Sandboxes

Cancelling pending jobs does not stop a running PM sandbox. A running PM agent
could call `internal/issues` or `internal/projects/propose`, or reach
`executePlan`, during deployment.

PR 1 must use defense in depth:

- guard at PM analysis entry;
- guard before PM sandbox creation;
- guard before plan execution;
- remove or guard the PM-only internal project proposal endpoint;
- guard project-plan execution from a general PM analysis.

The deployment must account for old worker processes during rolling drain.
Compatibility handlers should be harmless before they are removed.

### Production State

The implementation may optionally set legacy organization autonomy to manual
as a belt-and-suspenders measure, but settings changes alone are not a complete
shutdown:

- PM analysis still consumes compute in manual mode;
- internal issue creation dispatches independently;
- project proposals can still be created;
- running old workers may not see new defaults.

Code-level shutdown remains required.

### Observability

After PR 1 deploys, verify:

- no new jobs of the three PM types are enqueued;
- no new sessions have `agent_type = pm_agent`;
- no new sessions acquire `pm_plan_id`;
- no new issues are created by PM analysis;
- no new Projects have `proposed_by_pm = true`;
- no `run_agent` enqueue is causally linked to PM analysis;
- normal manual Sessions and user-authored Automations continue to enqueue and
  run;
- Slack sync remains scheduled.

The PM scheduling code currently interleaves Slack sync with PM scheduling.
Move Slack scheduling outside the removed PM condition; do not delete or
accidentally gate it.

## Documentation And Demo Data

Remove or update:

- `docs/public/guides/autopilot.mdx`;
- public guide indexes and metadata;
- public docs homepage Autopilot links;
- README positioning and setup flow;
- homepage and landing-page Autopilot copy;
- onboarding and setup-checklist copy;
- design docs that describe Autopilot as a current major product surface;
- `docs/design/overall.md`.

Update demo and seed data:

- `.143/seed/30_issues.sql`;
- `.143/seed/40_sessions_base.sql`;
- `.143/seed/70_product_surface.sql`;
- `.143/seed/75_reference_context_projects.sql`;
- related provider/session seeds;
- `internal/demoseed` verification.

Historical implemented design docs remain historical records. They do not need
to be rewritten, but the architecture map should point to this removal design
and stop describing Autopilot as current behavior.

## Three-PR Implementation Plan

### PR 1 - Make PM And Autopilot Inert

**Purpose:** stop compute and side effects with the smallest safe,
backward-compatible deployment.

#### Scheduler And Trigger Changes

- Stop enqueueing scheduled organization and repository `pm_analyze`.
- Move Slack sync scheduling outside the PM scheduling block.
- Stop periodic `pm_context_refresh`.
- Stop integration-connect `pm_bootstrap` and `pm_context_refresh`.
- Disable manual analysis, bootstrap, and refresh mutations.

#### Worker And Service Guards

- Add a hard shutdown guard at PM analysis entry.
- Add a guard before sandbox creation.
- Add a guard immediately before `executePlan`.
- Prevent general PM analysis from invoking `executeProjectPlan`.
- Keep PM handlers registered temporarily but make stale jobs harmless.
- Add a scoped way to cancel pending PM job types.

#### Internal Mutation Guards

- Remove PM execution and its calls to `POST /api/v1/internal/issues`.
- Preserve non-PM internal issue creation and dispatch behavior.
- Remove the PM-only internal project proposal endpoint and tool integration.
- Verify ordinary non-PM internal issue creation still works.

#### Compatibility

- Keep read APIs and UI temporarily.
- Keep PM decision completion updates for already-running historical sessions.
- Keep schema and historical rows unchanged.

#### Required Tests

- Scheduler never enqueues PM job types.
- Integration connect never enqueues PM context jobs.
- Manual PM mutation endpoints cannot enqueue.
- Stale PM jobs do not create sandboxes or sessions.
- PM plans cannot enqueue `run_agent`.
- PM analysis cannot create internal issues.
- The PM-only internal project proposal endpoint is unavailable.
- Non-PM internal issue creation retains its existing behavior.
- Slack sync, manual Sessions, and user-authored Automations still work.

#### Rollout Verification

Deploy PR 1 independently and observe job/session creation before merging PR 2.

### PR 2 - Make PM And Autopilot Absent

**Purpose:** remove the product and dedicated feature code after it is inert.

#### Frontend

- Delete Autopilot and decision routes.
- Delete Autopilot settings.
- Delete Autopilot components and proposal card.
- Remove PM status banner and `Run now` from Sessions.
- Remove navigation, command palette, page titles, robots entry, query keys,
  types, API calls, mocks, and tests.
- Remove repository PM scheduling and PM-specific settings presentation while
  preserving context and priority controls required by retained non-PM
  features.
- Remove Project `Run now` and its PM planning UI.
- Update onboarding, setup, homepage, and landing copy.

#### API And Dedicated Store

- Remove `/api/v1/autopilot/queue`.
- Delete Autopilot handler, queue store, models, tests, and router wiring.
- Remove PM current/status/plan/decision endpoints with no retained consumer.
- Remove disabled PM mutations once old clients no longer require a
  compatibility response.
- Remove `project_cycle` and the PM-only internal project proposal endpoint.

#### Documentation And Seeds

- Remove public Autopilot documentation and indexes.
- Update README and architecture map.
- Update demo seed surfaces and assertions.
- Preserve historical implemented design docs.

#### Compatibility

- Keep historical PM session rendering.
- Keep PM tables, enum values, shared session context, eval document pins, and
  priority data.
- Do not perform destructive schema cleanup.

#### Required Verification

- Focused frontend tests for navigation, Sessions, settings, docs, and demo
  states.
- Backend tests for removed route registration.
- Full frontend typecheck/lint/build because routes, docs, shared API types,
  and navigation change broadly.
- Focused backend tests plus full tenancy lints.

### PR 3 - Remove Obsolete Machinery

**Purpose:** capture backend simplification after the shutdown is proven.

#### PM Service Cleanup

- Remove PM analysis, bootstrap, refresh, plan execution, and unused prompts.
- Remove PM worker handlers and job constants after old jobs are terminal.
- Remove PM stores, handlers, models, and audit emissions with no historical
  read requirement.
- Remove PM decision-outcome hooks after no PM-linked sessions remain active.

#### Prioritization Cleanup

- Retain standalone priority scoring and complexity estimation.
- Retain priority APIs, manual reprioritization, issue priority sorting, the
  `prioritize` worker, stores, models, prompts, and tables.
- Delete `CheckAutoTrigger`, automatic eligibility and dispatch semantics, and
  session/job dependencies used only by that dead path.
- Replace the duplicate `prioritization.OrgSettings` with the shared settings
  model or a smaller scoring-only configuration.

#### Projects Cleanup

- Remove `project_cycle`, the Project `Run now` action, and PM project planning.
- Remove PM proposal APIs, `proposed_by_pm`, proposal reasoning, source issue
  proposal metadata, proposal summaries, deduplication, and rate limits.
- Preserve human-authored Project CRUD, tasks, execution tracking, progress,
  branches, and associated sessions.

#### Schema And Naming

- Migrate or archive PM plans and decisions.
- Remove PM foreign keys only after application references are gone.
- Neutralize `session_pm_context` and PM-named execution brief fields.
- Rename PM documents and eval pins to a neutral reference-context model while
  preserving eval reproducibility.
- Stop defaulting removed JSON settings and optionally clean legacy keys.
- Preserve historical `pm_agent` values while preventing new PM writes.

Migration `000260` is an expand step because production applies migrations
before rolling API containers and rolls workers afterward. Neutral session,
reference-document, archive, and eval-pin names are exposed through updatable
views or synchronized columns while legacy tables and columns remain available
to draining binaries. Destructive contraction is intentionally deferred until
the whole fleet has run neutral code through a complete deployment.

### PR 4 - Contract Rollout Compatibility

**Purpose:** remove the expand-phase compatibility layer after the fleet has
completed a deployment on neutral application contracts.

- Promote `session_execution_context`, `reference_documents`, and
  reference-context pin projections to physical tables by renaming the
  historical base tables.
- Remove legacy eval pin columns and their synchronization triggers while
  preserving neutral pin foreign keys and snapshots.
- Preserve PM plans, decisions, and Project cycles as renamed archive tables.
- Remove `pm_plan_id` from session execution context and PM-only Project
  proposal provenance, including the obsolete source-issue join table.
- Remove the temporary database trigger that rejected retired PM job types.
- Preserve historical Sessions, Projects, issues, archive rows, reference
  documents, and eval reproducibility.

#### Required Verification

- Focused store/service tests for every migrated contract.
- Migration up/down verification.
- Multi-tenancy lints.
- Full Go tests and vet because service construction, worker registration,
  models, schema, and shared contracts change broadly.
- Full frontend verification if shared session or eval types are renamed.

## Why Not One PR

A single PR would combine:

- production scheduler and worker safety;
- internal API authorization changes;
- thousands of lines of frontend deletion;
- public documentation changes;
- shared Project and eval decisions;
- destructive schema migrations.

That would make it difficult to establish that automatic work has stopped,
harder to review unrelated regressions, and expensive to roll back if the
shutdown affects normal session or Automation workflows.

## Why Four PRs Instead Of Two

Two PRs are possible:

1. shutdown;
2. UI removal and all cleanup.

The second PR would still mix mechanical product deletion with destructive
data-model and shared-contract changes. Eval document pins, Project planning,
priority sorting, runtime concurrency, and shared session context each require
separate judgment.

Four PRs provide the clearest invariants:

| PR | Invariant after deploy |
| --- | --- |
| 1 | PM and Autopilot cannot create work or consume background agent compute. |
| 2 | Users and API clients can no longer access the PM/Autopilot product. |
| 3 | Obsolete backend and schema machinery is removed or renamed safely. |
| 4 | Rolling compatibility objects are contracted after neutral code is fully deployed. |

## Acceptance Criteria

The removal is complete when:

1. No scheduled, integration-triggered, or manual general PM analysis can run.
2. No PM path can create a coding session or enqueue `run_agent`.
3. PM analysis cannot create internal issues or use internal issue creation to
   start work; non-PM internal issue behavior remains unchanged.
4. Automatic PM project proposals cannot be created.
5. Autopilot has no UI route, navigation entry, command, settings page, public
   guide, onboarding dependency, or homepage positioning.
6. `/api/v1/autopilot/queue` and its dedicated store/model stack are gone.
7. Manual Sessions, explicit issue starts, user-authored Projects, and
   user-authored Automations continue to work.
8. Runtime concurrency limits remain enforced.
9. Historical PM-created sessions, issues, projects, PRs, usage, and audits
   remain readable.
10. Eval reproducibility remains intact.
11. Priority scoring, reference documents, and session execution context have
    explicit non-Autopilot ownership.
12. Architecture and public documentation describe the post-removal product.
13. Project `Run now`, `project_cycle`, and PM project proposals are removed
    while human-authored Project management remains functional.
