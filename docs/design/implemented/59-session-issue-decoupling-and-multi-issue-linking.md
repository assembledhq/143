# 59 - Session/Issue Decoupling and Multi-Issue Linking

> **Status:** Phase 1 and Phase 2 implemented
>
> **Last reviewed:** 2026-04-24
>
> **Depends on:** [../overall.md](../overall.md), [29-projects.md](29-projects.md), [../backlog/34-repo-ribbons-nav.md](../backlog/34-repo-ribbons-nav.md), [../implemented/53-session-composer-mentions.md](../implemented/53-session-composer-mentions.md)

## Summary

Sessions and issues should become distinct first-class concepts:

- An **issue** is a work signal: something ingested or curated from Sentry, Linear, support, PM, or a human backlog.
- A **session** is an execution workspace: the conversation, sandbox, diff, review state, and PR flow for an agent run.

Today the product mostly behaves that way already, but the data model still leaks the old assumption that every session needs exactly one issue. Manual sessions work by creating a synthetic `manual` issue first, then attaching the session to it. That is now the wrong abstraction for the product.

The target model is:

- A session may be linked to **zero, one, or many issues**.
- A session's execution behavior must not depend on a synthetic issue row.
- Issue lifecycle automation must remain conservative and explicit even when multiple issues are linked.

This document proposes the long-term model and a staged migration plan that preserves correctness and avoids another schema dead end.

## Problem

The current model has three structural problems.

### 1. Manual sessions are encoded as fake issues

`POST /api/v1/sessions/manual` creates a synthetic `issues` row with:

- `source = manual`
- the user prompt stored in `issues.description`
- attachments/references stored in `issues.raw_data`

and only then creates the session.

That creates the wrong ownership boundary. The prompt, attachments, references, interactivity, and sandbox state are session concerns, not issue concerns.

### 2. Session input is duplicated across two concepts

For a manual session, the system stores user intent in both:

- the synthetic manual issue
- the turn-0 `session_messages` row

That duplication is tolerable as a temporary bridge, but it is not a correct long-term source of truth. Any future change risks drift between:

- issue description
- session title
- session message content
- session input references

### 3. The current one-session/one-issue assumption blocks future workflows

The product already has legitimate issue-less sessions:

- project-dispatched sessions may run without an issue
- exploratory/manual sessions are often about a repo area, not a backlog item

The next obvious workflow is also many-to-many:

- one manual session linked to multiple related Linear issues
- one PM/project session addressing a cluster of customer reports
- one root-cause fix session linked to several issue records that share the same defect

The current `sessions.issue_id` model cannot represent that cleanly.

## Design Principles

1. **Sessions are the execution primitive.** They own prompt history, attachments, references, sandbox state, diffs, review, and PR flow.
2. **Issues are optional context.** Sessions may have no issue at all.
3. **Links are explicit.** If a session is related to issues, represent that relationship directly instead of inferring it from synthetic data.
4. **Repository ownership belongs on the session.** Execution should not need to fetch an issue to learn which repo to clone.
5. **Do not fan out issue status changes blindly.** Multi-issue linkage must not auto-close or auto-progress unrelated issues.
6. **Prefer additive migration over flag-day rewrites.** Backfill and dual-read where needed; avoid risky data rewrites of historical manual sessions.

## Goals

- Make manual sessions first-class without requiring a synthetic issue row.
- Support sessions with zero, one, or many linked issues.
- Keep issue-first flows simple: "Fix This" on an issue should still create a session naturally.
- Preserve current behavior for validation, PR creation, and review while removing issue-only assumptions.
- Give the product a safe path to multi-issue linking for manual sessions and PM/project sessions.

## Non-Goals

- Redesigning issue prioritization or PM planning itself.
- Solving issue deduplication/merge semantics across all providers.
- Automatically resolving all linked issues from a single session in v1 of multi-issue support.
- Deleting or rewriting legacy synthetic manual issues in place.

## Proposed Long-Term Model

### Core entities

#### Issue

An issue remains the canonical record for a backlog or production signal:

- provider metadata
- severity / impact
- prioritization score
- issue lifecycle status
- source-specific identifiers

An issue may exist with no session yet. Many issues will never be executed directly.

#### Session

A session is the canonical execution record:

- conversation history
- initial prompt and follow-up messages
- uploaded attachments and structured references
- repository and branch targeting
- sandbox/snapshot state
- diff, validation, review, and PR state

A session may exist with no linked issue.

#### SessionIssueLink

Introduce an explicit join model between sessions and issues.

Recommended schema:

```sql
CREATE TABLE session_issue_links (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid NOT NULL REFERENCES organizations(id),
    session_id        uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    issue_id          uuid NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    role              text NOT NULL CHECK (role IN ('primary', 'related')),
    position          integer NOT NULL DEFAULT 0,
    added_by_user_id  uuid REFERENCES users(id),
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, issue_id)
);

CREATE UNIQUE INDEX idx_session_issue_links_primary
ON session_issue_links (session_id)
WHERE role = 'primary';
```

Render-order rule:

- primary issue always renders first
- related issues render by `position ASC, created_at ASC, issue_id ASC`

`position` exists to keep prompt construction deterministic across retries, resumes, and future reorder UI.
The API should expose `position` on linked issues and accept explicit reorder writes through the link-management surface instead of leaving ordering implicit.

For the first rollout, keep roles intentionally small:

- `primary`
- `related`

That is enough to support:

- today's single-issue flows
- future "manual session linked to multiple Linear issues" flows
- clustered PM/project work

Do not add more relationship types until there is a real product need.

### Execution invariants

The following rules are hard invariants for a correct long-term system:

1. A session with zero linked issues is a first-class execution path.
2. If a session has any linked issues at execution time, it must have exactly one `primary` link.
3. All linked issues for an execution session must belong to the session repository.
4. Downstream jobs and turns consume the snapshotted issue context captured at execution time, never the live join table.

These invariants keep the "no issue" path simple and keep multi-issue runs correct without forcing issue-link complexity into every session.

### Session origin should be explicit

Manual behavior should not be inferred from `issue.source == manual` or from `triggered_by_user_id != nil`.

Add a first-class session origin/kind field:

```sql
ALTER TABLE sessions ADD COLUMN origin text NOT NULL DEFAULT 'issue_trigger';
```

Recommended values:

- `issue_trigger`
- `manual`
- `project`
- `automation`
- `revision`

`origin` is provenance only. It answers "how was this session created?" It must not become the source of truth for runtime policy.

### Execution policy should be explicit

Do not overload `sessions.origin` with execution behavior.

Long term, session behavior should be driven by explicit policy fields, not provenance. At minimum, the model should separate:

- `interaction_mode`
  - `interactive`
  - `single_run`
- `validation_policy`
  - `on_turn_complete`
  - `on_session_end`
  - `skip`

These fields may begin life as write-time derived values rather than user-facing controls, but they should still be explicit in the data model or session creation command path.

This separation prevents future semantic drift such as:

- manual sessions that are not interactive
- automation sessions that still require explicit end-of-session review
- project sessions that behave like manual sessions for multiple turns

`origin` should remain useful for:

- analytics
- provenance-aware UI
- auditability

### Repository should be authoritative on the session

`sessions.repository_id` should be the primary execution repo field.

Repository resolution order during migration:

1. `sessions.repository_id`
2. primary linked issue repository
3. no repository attached

Long term, execution logic should not depend on issue lookup for repo resolution.

### Repository invariant for linked issues

Multi-issue linkage must not create ambiguous cross-repo execution.

This is a required execution invariant for v1, not just a rollout convenience.

For v1:

- a session may link only issues whose `repository_id` matches `sessions.repository_id`
- if `sessions.repository_id` is null, issue linking is rejected
- if an issue has `repository_id` null, linking it to a session is rejected

This is intentionally strict. It prevents invalid states such as:

- one coding session linked to issues from multiple repositories
- a repo-scoped execution workspace claiming to address repo-agnostic issues with no concrete execution target

Cross-repo issue clusters, repo-agnostic issues, or one session spanning several repos should be treated as a separate future design, not smuggled in through a permissive link table.

### Zero-issue sessions are the fast path

The system must preserve a low-complexity execution path for sessions with no linked issues.

Operationally, that means:

- agent execution must not require an issue lookup
- prompt construction must work with no issue context
- validation and PR flows must behave correctly with no issue lifecycle side effects
- multi-issue machinery must be additive, not mandatory

If this is not kept explicit, the optional-link model will slowly leak complexity into every run.

## How Multi-Issue Sessions Should Work

### Recommendation

Yes, multi-issue support should be part of the target architecture now.

It should **not** block phase 1 of decoupling manual sessions from synthetic issues, but the data model introduced during that refactor should already support multiple links. Otherwise we will finish one migration and immediately need another schema break.

### What multiple issues mean

Linking multiple issues to one session means:

- the session is informed by those issues
- the session may attempt a shared fix that addresses them together

It does **not** automatically mean:

- all linked issues should move to `in_progress`
- all linked issues are fixed by the resulting PR
- all linked issues should inherit the same validation outcome

Those are higher-confidence claims and need explicit product semantics.

### Safe semantics for v1

For the first multi-issue rollout:

- exactly one linked issue must be marked `primary` by the time execution starts
- zero or more additional issues may be marked `related`
- only the **primary** issue participates in automatic lifecycle updates
- related issues are contextual only

That means:

- `primary` may move `triaged -> in_progress -> fixed`
- `related` issues remain unchanged unless a human explicitly updates them

This is intentionally conservative. It avoids false positives where one session references five similar Linear tickets but only partially fixes the underlying problem.

Before execution starts, the UI/API may temporarily allow link editing states where no primary is selected yet. Those are draft editing states, not valid execution states.

### Related-issue follow-up must be explicit

The conservative v1 rule creates a product obligation: related issues cannot simply remain silently open forever.

After a session reaches a meaningful outcome such as PR created, merged, or failed:

- the UI should surface the related issues explicitly
- the operator should be able to mark each related issue as:
  - still open
  - covered by this session/PR
  - detach as unrelated

This follow-up is human-mediated in v1. The system should not auto-transition related issues, but it also should not hide them after encouraging users to link them.

The outcome of that follow-up must be stored durably, not left in transient UI state.

The persisted outcome should support at least:

- `still_open`
- `covered_by_session`
- `detached`

Use a separate append-only `session_issue_review_decisions` table keyed by `(session_id, issue_id, decision_sequence)`.

Recommended schema:

```sql
CREATE TABLE session_issue_review_decisions (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid NOT NULL REFERENCES organizations(id),
    session_id        uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    issue_id          uuid NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    decision_sequence integer NOT NULL,
    decision          text NOT NULL CHECK (
        decision IN ('still_open', 'covered_by_session', 'detached')
    ),
    decided_by_user_id uuid REFERENCES users(id),
    note              text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, issue_id, decision_sequence)
);
```

Why:

- review outcomes may change over time
- auditability matters
- issue detail and session detail should be able to show a history of operator decisions
- append-only review decisions avoid overloading `session_issue_links` with mutable review state

### Long-term extension point

If later we want one session to resolve multiple issues automatically, that should be a separate feature with explicit proof conditions, for example:

- reviewer marks linked issues as covered
- PR body or merge workflow includes explicit "resolves issue X, Y, Z"
- post-merge verification confirms the broader fix

That should not be bundled into the foundational refactor.

## Canonical Ownership of Session Input

After this refactor, the canonical source of manual session intent should be:

- turn-0 `session_messages`
- `session_messages.attachments`
- `session_messages.references`
- session-level metadata such as `title`, `origin`, `repository_id`, and `target_branch`

The canonical source of execution-time linked-issue context should be a separate immutable per-turn snapshot record, not a mutable field on the parent `sessions` row.

Recommended schema:

```sql
CREATE TABLE session_turn_issue_snapshots (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id            uuid NOT NULL REFERENCES organizations(id),
    session_id        uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    turn_number       integer NOT NULL,
    linked_issues     jsonb NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, turn_number)
);
```

Manual-session references should no longer be rehydrated from `issues.raw_data`.

`issues.raw_data` should remain provider-owned issue payload, not a general-purpose session blob.

## Execution Context Resolution

Issue linking is optional, but when links exist the system must resolve them into prompt and execution context in a deterministic way.

### Prompt construction for linked issues

When issues are linked, they should be injected into prompts as structured context rather than flattened prose.

Rules:

- if zero issues are linked, omit the issue-context block entirely
- if issues are linked, render them in a dedicated XML context block
- the primary issue appears first
- related issues appear after it in deterministic order
- issue context is context only, not instruction authority
- append the rendered issue block to the structured prompt context, not to the raw user message body
- do not leave linked-issue rendering to adapter-specific ad hoc logic; prompt templates should define the canonical shape

Recommended shape:

```xml
<linked_issues>
  <issue role="primary" source="linear">
    <title>Fix OAuth callback redirect loop</title>
    <external_id>ENG-1234</external_id>
    <description>Users are redirected back to login after OAuth callback...</description>
  </issue>
  <issue role="related" source="linear">
    <title>Google login fails on mobile Safari</title>
    <external_id>ENG-1251</external_id>
    <description>Likely same redirect handling defect...</description>
  </issue>
</linked_issues>
```

This is especially important for linked Linear issues: the agent should see them as clearly scoped issue-tracker context with explicit titles and identifiers.

For Linear-linked issues specifically, each `<issue>` block should include:

- `source="linear"`
- `<title>`
- `<external_id>`
- optional bounded `<description>`

This makes it unambiguous to the agent that these are tracker issues being provided as execution context, not free-form user instructions.

### Prompt budget rules

Multi-issue linking must not degrade agent quality by crowding out the actual task prompt.

Rules:

- primary issue may include bounded title + description
- related issues should default to title + identifier + short summary
- total linked-issue prompt context must have a strict size cap
- truncate related issues before truncating the primary issue
- never truncate the actual user message to make room for linked-issue context
- all prompt builders must consume the same canonical ordered linked-issue list

### Transactional resolution requirement

When a session begins execution, issue context must be resolved atomically.

The following should happen within one transaction boundary where feasible:

1. read current `session_issue_links`
2. validate execution invariants
3. resolve the primary and related issue set
4. persist the turn-scoped issue snapshot into `session_turn_issue_snapshots`
5. enqueue the downstream execution job

If the queueing mechanism cannot participate in the same transaction, the system must still persist the resolved snapshot durably to `session_turn_issue_snapshots` before enqueue and require workers to consume that stored snapshot instead of re-reading the live join table.

This prevents races where one issue set is validated and another is actually executed.

Canonical rule:

- `session_turn_issue_snapshots` is the only durable source of truth for the resolved issue context used by a turn
- downstream jobs should carry the `session_turn_issue_snapshots.id` they were created against
- job payloads may carry copies for convenience, but workers must treat the persisted turn snapshot as authoritative
- workers must never rebuild execution issue context from the live join table once a turn/job has been created

## Linked Issue Lifecycle Edge Cases

The join model needs explicit semantics for issue lifecycle changes after linkage.

### Soft-deleted issues

If a linked issue is soft-deleted:

- historical turn/job snapshots remain unchanged
- the live session should still render that a once-linked issue existed, but mark it unavailable/deleted
- no new lifecycle updates should be attempted against that issue

Do not silently erase the relationship from historical views.

### Duplicated / merged issues

If a linked issue is marked duplicate of another issue:

- existing session links remain attached to the originally linked issue for historical correctness
- the UI may surface the duplicate target as additional context
- automatic relinking must not occur silently

If product later wants "repoint link to canonical issue," that should be an explicit operator action with audit logging.

### Repository drift after linkage

If repo metadata on a linked issue later changes or is cleared:

- historical snapshots remain unchanged
- new execution should re-run repository invariant validation before starting
- invalid live links should be surfaced clearly and require repair before execution resumes

### Issue-triggered session with no repository

If a user starts an issue-triggered session from an issue whose `repository_id` is null:

- reject session creation or execution with a clear `repository required` error

Do not silently convert this into an ambiguous repo-less execution path, and do not create a hidden draft state unless a dedicated repository-selection UX is introduced in a separate design.

## Link Mutability and Historical Correctness

Issue links are part of the session's execution context, so mutability rules matter.

### Mutation rules

- links may be added, removed, or reprioritized only while the session is not `running`
- link mutation requests during `running` are rejected
- every mutation must emit an audit log event
- once a session has started its first execution turn, the `primary` link is frozen for the lifetime of that session

Allowed after first execution:

- add or remove `related` links while the session is idle
- reorder `related` links while the session is idle

Not allowed after first execution:

- changing which issue is `primary`
- converting a `related` issue into the new `primary`

Rationale:

- issue lifecycle ownership must remain stable across turns
- PR attribution and merge-side effects should not shift between issues mid-session
- prior turn snapshots remain historically correct without having to explain a moving primary target

### Historical snapshot rules

The live join table represents the session's **current** issue context. It does not, by itself, preserve what issue set a given turn or job actually used.

To preserve reproducibility:

- at the start of each agent turn, snapshot the resolved linked issue set into `session_turn_issue_snapshots`
- when enqueuing validation, PR creation, or other downstream jobs, include the corresponding snapshot ID in the job payload or immutable job metadata

That ensures later edits do not rewrite history. A user may link a new related issue after turn 1, but turn 1's prompt, validation context, and audit trail still refer to the issue set that actually existed at the time.

### Derived primary issue

The concept of a primary issue still exists logically, but it should be derived from `session_issue_links(role = 'primary')`, not stored as a second canonical column on `sessions`.

## API Shape

### Session APIs

Target response shape:

```json
{
  "id": "...",
  "origin": "manual",
  "primary_issue_id": "...",
  "linked_issues": [
    { "issue_id": "...", "role": "primary", "position": 0 },
    { "issue_id": "...", "role": "related", "position": 10 }
  ]
}
```

Migration note:

- `primary_issue_id` is a derived response field from the join table, not a persisted session column
- keep returning `issue_id` temporarily as an alias for `primary_issue_id`
- stop using the zero UUID sentinel in API responses
- use `null` for "no primary issue"

### Manual session create

`POST /api/v1/sessions/manual` should:

- create the session directly
- create the turn-0 message
- optionally create zero or more `session_issue_links`
- never create a synthetic manual issue

### Issue-triggered session create

Issue-driven flows remain straightforward:

- "Fix This" creates a session
- creates one `session_issue_links` row with `role = primary`
- sets `origin = issue_trigger`

### Link management

Add explicit session issue link APIs:

- `POST /api/v1/sessions/:id/issues`
- `DELETE /api/v1/sessions/:id/issues/:issueId`
- `PATCH /api/v1/sessions/:id/issues/:issueId` to promote/demote primary vs related or reorder via `position`

This is the correct surface for future "link more Linear issues to this manual session" UX.

The API must enforce:

- at most one `primary` link
- exactly one `primary` link when execution begins and whenever a downstream execution job is enqueued
- repository invariant checks
- no mutation while session is `running`
- no `primary` reassignment after the first execution turn has begun

Issue-triggered session creation must also enforce:

- if the source issue has no repository, reject the request with a clear `repository required` error

## Migration Plan

This migration can be executed in two phases because the product is still early and the user count is low. The goal is to avoid months of dual-read/dual-write complexity while still preserving historical correctness.

### Phase 1: Cut Over to the New Model

Do one decisive migration that:

- adds `sessions.origin`
- adds explicit execution policy fields such as `interaction_mode` and `validation_policy`
- adds `session_issue_links`
- backfills every existing non-null `sessions.issue_id` into `session_issue_links` with `role = primary`
- stops creating synthetic manual issues for all new manual sessions
- switches all new write paths to the join table immediately
- switches session behavior to explicit session policy fields instead of `issue.source`
- switches repository resolution to session-first
- switches issue lifecycle automation to derive the primary issue from the join table
- snapshots linked issues per turn and per downstream job enqueue
- switches API/frontend nullability away from the zero UUID sentinel
- updates prompt templates/renderers to inject linked issues as bounded XML context when present
- adds `session_turn_issue_snapshots` as the canonical durable execution snapshot source for linked-issue context

Phase 1 implementation status as of 2026-04-23:

- Implemented: `sessions.origin`, `interaction_mode`, and `validation_policy`
- Implemented: `session_issue_links` with backfill from legacy `sessions.issue_id`
- Implemented: `session_turn_issue_snapshots` as the durable per-turn linked-issue context source
- Implemented: manual session creation no longer creates synthetic `source = manual` issues
- Implemented: session behavior now keys off explicit session policy fields, with compatibility fallback only for historical/manual rows
- Implemented: repository resolution is session-first, with issue fallback limited to legacy compatibility paths
- Implemented: validation and PR flows derive the primary issue from the join/snapshot model instead of treating `sessions.issue_id` as canonical
- Implemented: API/frontend session responses now use nullable primary-issue semantics instead of the zero UUID sentinel
- Implemented: prompt rendering injects bounded XML linked-issue context when present
- Implemented: manual session references are canonical on session messages rather than provider-owned issue payloads

Phase 1 intentionally leaves these follow-ups for later work:

- remove `sessions.issue_id` and the remaining compatibility reads
- remove compatibility handling for historical `source = manual` issue-backed sessions
- add dedicated session issue-link mutation APIs/UI for add/remove/reorder/promote flows
- de-emphasize or hide legacy synthetic manual issues in issue-centric surfaces

Implementation expectations for this phase:

- `sessions.issue_id` may remain present in the database only as a temporary compatibility column for old rows or emergency rollback, but application reads and writes should stop depending on it in normal operation
- new link writes must enforce the repository invariant
- link mutation must reject writes while a session is `running`
- execution entrypoints must reject sessions that have related issues but no primary issue
- every execution path must read linked-issue context from `session_turn_issue_snapshots`, never by rebuilding from the live join table

Likely callers to fix in this phase:

- manual session creation
- prompt/adapters
- manual reference extraction
- "interactive session" checks
- validation gating
- resume context logic
- sandbox setup / fresh clone
- repo summary counts and session filtering
- validation / PR / deploy outcome flows
- backend and frontend session models
- prompt rendering / prompt templates

Success conditions:

- new manual sessions run end-to-end without creating manual issues
- issue-triggered sessions still work
- project sessions still work
- issue-less sessions complete cleanly
- primary issue lifecycle automation still works
- related issues are contextual only
- no frontend code relies on the zero UUID sentinel
- zero-issue sessions remain a simple first-class path
- linked issues appear in prompts in structured bounded context blocks
- the primary issue cannot change after first execution begins

Status: complete. The shipped cutover satisfies the phase-1 product/runtime contract above. Remaining work belongs to phase 2 cleanup and later multi-issue editing UX, not to the foundational decoupling itself.

### Phase 2: Deprecate and Remove Legacy Compatibility

After phase 1 has been running cleanly and historical rows are validated:

- drop `sessions.issue_id`
- remove any old compatibility reads that fall back to `sessions.issue_id`
- remove dead code paths that interpret `issue.source == manual`
- remove any temporary API aliases such as legacy `issue_id` fields if they are no longer needed
- optionally hide or de-emphasize legacy `source = manual` issues in issue-centric UI

Recommendation:

- do not introduce a new persisted `primary_issue_id` column on `sessions`
- if a convenience `primary_issue_id` is needed in APIs or queries, compute it from the join table or materialize it in a dedicated read model

The long-term write model should have one canonical truth: `session_issue_links`.

Phase 2 implementation status as of 2026-04-24:

- Implemented: `sessions.issue_id` column dropped in migration 000097; API responses expose `primary_issue_id` derived from `session_issue_links` via a subquery alias, with no persisted primary column on `sessions`.
- Implemented: all backend reads and writes go through `session_issue_links` (session creation, worker handlers, PR flow, validation, orchestrator resume) — no code path depends on `sessions.issue_id`.
- Implemented: new sessions use `Origin` as the canonical signal for manual-mode policy; `hydrateSessionPolicyForExecution` no longer derives mode purely from `issue.source == 'manual'` for fresh rows.
- Compatibility shim retained: `isLegacySyntheticManualSession` in `orchestrator.go` still recognises pre-Phase 2 sessions whose persisted origin is `issue_trigger` but which point at a `source = 'manual'` issue, and re-classifies them as interactive manual sessions at execution time. This keeps historical interactive sessions working without rewriting their persisted origin. Safe to remove once historical rows have been audited.
- Compatibility shim retained: `manualSessionReferences` is still consulted as a fallback when canonical message references are absent, so legacy synthetic manual sessions can still recover attachments from the manual issue's `raw_data`. New manual sessions persist references on `session_messages.references` and never hit this fallback.
- Implemented: API no longer returns the legacy `issue_id` alias on session responses; frontend `Session` type drops `issue_id`. `primary_issue_id` is the only identifier surfaced for the primary linked issue.
- Implemented: `sessionRepoSlug` resolves repository strictly from `sessions.repository_id` — no fallback to the primary issue's repository for resume workdir derivation. `setupFreshSandbox` still fetches the primary issue for prompt context but repository selection is session-first.

Status: complete. The long-term write model is now `session_issue_links` as the single canonical truth.

#### Phase 2 down-migration caveat

`migrations/000097_drop_sessions_issue_id.down.sql` re-adds the column and backfills `issue_id` from the current `role = 'primary'` row in `session_issue_links`. This means:

- Sessions whose primary link was **added or changed between the Phase 2 deploy and a rollback** will have the rolled-back `issue_id` reflect the *new* primary, not the original Phase 1 value.
- Sessions whose primary link was **deleted** post-Phase 2 will rollback to `issue_id = NULL`.

For an emergency rollback, this is the best the down-migration can do (the original `issue_id` value is no longer recoverable once the column is dropped). Operators rolling back should be aware that the restored column reflects the *current* primary link, not the historical one.

## Recommended Rollout Order

Within the two-phase plan, the recommended order is:

1. Ship the schema additions and backfill.
2. Update backend write paths to use `session_issue_links`.
3. Update backend readers and workers to stop depending on `sessions.issue_id`.
4. Switch manual sessions to issue-less creation.
5. Switch API/frontend nullability and derived primary-issue responses.
6. Verify historical rows and operational behavior.
7. Drop legacy compatibility fields and code.

This is intentionally a fast cutover, not a prolonged coexistence plan.

## Historical Data Strategy

Do not bulk-delete or rewrite old synthetic manual issues during the main migration.

Recommended policy:

- leave historical rows intact
- stop creating new synthetic manual issues
- ensure new code no longer depends on them
- optionally hide or de-emphasize legacy `source = manual` issues in issue-centric UI later

This avoids a risky cleanup project mixed into the behavioral migration.

## Risks

### Risk: phase-1 cutover leaves hidden dependencies on `sessions.issue_id`

Mitigation:

- audit all worker handlers and services that fetch issue by `session.issue_id`
- run end-to-end tests for issue-less sessions before cutover
- keep `sessions.issue_id` only as a short-lived escape hatch until phase 2 removes it

### Risk: repository lookup regressions

Mitigation:

- explicitly test issue-less manual sessions
- explicitly test legacy sessions that still rely on issue repo fallback

### Risk: multi-issue prompt context hurts agent quality

Mitigation:

- render linked issues as structured XML context
- keep primary issue detailed and related issues summarized
- enforce a strict prompt budget for issue context
- ensure zero-issue sessions omit the block entirely
- enforce canonical ordering for related issues

### Risk: incorrect status updates for related issues

Mitigation:

- only primary issue gets automatic lifecycle transitions in v1
- require explicit future design before broader auto-resolution

### Risk: stale related issues create misleading backlog state

Mitigation:

- add explicit post-session related-issue review UI
- make "covered by this session" a human confirmation step, not an implicit assumption

### Risk: permissive linking creates invalid cross-repo state

Mitigation:

- enforce the repository invariant on all link writes
- add tests for rejected cross-repo and repo-null link attempts

### Risk: mutable links rewrite history

Mitigation:

- reject link mutation while running
- snapshot linked issues per turn and per downstream job enqueue
- audit every link mutation event

### Risk: linked issues with no primary create ambiguous execution semantics

Mitigation:

- allow that state only during pre-run editing
- reject execution and downstream job enqueue unless exactly one primary exists when any issues are linked

### Risk: mutable primary ownership splits lifecycle attribution across turns

Mitigation:

- freeze primary-link assignment after first execution starts
- allow only related-link edits thereafter
- keep lifecycle automation bound to the frozen primary

### Risk: optional issue-link support infects all runs with extra branching

Mitigation:

- treat zero-issue sessions as a required fast path
- add explicit tests for runs with no issue context at all
- keep prompt/execution/review flows correct without requiring link lookups

### Risk: issue-triggered sessions with no repository create ambiguous execution targets

Mitigation:

- reject with a clear `repository required` error
- never silently infer, invent, or draft a repository target

## Testing Requirements

At minimum, add end-to-end coverage for these cases:

1. Manual session with no linked issues and a repository.
2. Manual session with one linked issue.
3. Manual session with one primary issue and multiple related issues.
4. Issue-triggered session created from an existing issue.
5. Project-dispatched session with no linked issue.
6. Validation and PR flows for a session with no issue.
7. PR merge flow where only the primary issue is auto-updated.
8. Legacy session created before the migration with `sessions.issue_id` populated.
9. Link mutation while `running` is rejected.
10. Cross-repo issue linking is rejected.
11. A turn/job uses the snapshotted issue set even if links are edited later.
12. Phase-2 removal of `sessions.issue_id` does not change session behavior for migrated rows.
13. A session with linked issues but no primary cannot start execution.
14. Soft-deleted or duplicated linked issues remain historically visible and do not silently relink.
15. Linked Linear issues appear in prompts as structured XML context with bounded size.
16. Transactional resolution stores the exact issue snapshot used for execution before worker pickup.
17. Issue-triggered sessions with no repository are rejected with a clear `repository required` error.
18. Related issues render in deterministic order across retries and turns.
19. Workers consume `session_turn_issue_snapshots` as the canonical durable execution snapshot source.
20. The primary issue cannot be reassigned after first execution begins.

## Recommendation

Break sessions and issues apart.

Do it in a way that also introduces the structural support for multiple linked issues now, but keep the first behavioral version conservative:

- zero-or-many issue links are allowed
- if any issues are linked at execution time, exactly one primary issue is required
- only the primary issue participates in automatic lifecycle changes
- the join table is the only long-term write-model truth for session/issue relationships
- runtime behavior comes from explicit policy fields, not `origin`
- issue linking is repo-scoped and history-preserving
- zero-issue sessions remain a low-complexity first-class path

That gives the system the right long-term shape without overcommitting to unsafe multi-issue automation.
