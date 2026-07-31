# Design: Agent-Initiated PR Publication with Automatic Review

> **Status:** Not Started | **Last reviewed:** 2026-07-31
>
> **Depends on:** [overall.md](../overall.md),
> [review agent loops](../implemented/78-review-agent-loops.md),
> [PR creation](../implemented/40-pr-creation-revamp.md),
> [PR readiness](../implemented/107-pr-readiness-checks.md),
> [automatic PR follow-through](../implemented/113-automated-pr-repair-and-readiness.md),
> and [durable publication](../implemented/118-durable-session-publication.md)

## Decision

143 adopts an agent-initiated, platform-managed PR workflow:

```text
agent implements and verifies
  -> agent runs `143-tools pr create`
  -> 143 records durable publication intent
  -> optional two-pass review/fix loop
  -> clean review queues durable PR publication
  -> linked session remains available for CI, conflicts, and feedback
```

The model chooses **when** work is ready. The platform controls **whether and
how** the external side effect occurs.

Two organization settings govern the workflow:

1. **Create a PR when the coding agent is ready**
2. **Run a two-pass review/fix cycle before creating the PR**

Both default on for existing and new organizations. Users may override either
with `inherit`, `on`, or `off`.

This design reuses the existing `create_pr` tool, review-loop service,
changesets, job queue, and `session_publications` ledger. Agents never receive
direct GitHub PR-write credentials. A completed model round alone is not
readiness: it may represent analysis, incomplete work, or a request for input.

## Product Contract

### Agent publication intent

PR handoff applies only to a turn whose requested outcome is a new code change
on an unpublished changeset. It is ineligible when:

- the user is asking a question or requesting explanation, analysis, planning,
  diagnosis, or code/PR review without changes
- the session is reviewing or repairing an existing PR
- the session is read-only, review-only, or otherwise cannot publish
- the current changeset has no non-empty diff from its base
- work is incomplete, cancelled, unsafe, awaiting a material user decision, or
  explicitly should not be published

An existing-PR session updates its linked PR through the existing push,
feedback, repair, and review flows; it never creates a second PR.

Only eligible coding-agent prompts include the instruction to call:

```bash
143-tools pr create
```

The agent calls it only after making the requested change and completing
appropriate verification. The tool call is the readiness signal; there is no
second model-produced readiness field.

If automatic creation is off, the agent leaves verified changes ready for
manual publication. An explicit user instruction such as "create the PR"
overrides that automatic preference, but never overrides authorization,
repository policy, review requirements, or readiness gates.

The tool returns a typed asynchronous state (`review_started`,
`review_in_progress`, `pr_queued`, `already_published`,
`manual_publication_required`, or `blocked`). The agent must not claim a PR
exists when review or publication is only queued.

### Review before publication

The default order is:

```text
implement -> local verification -> review/fix -> create PR -> CI/follow-through
```

This keeps intermediate fixes and stale review comments out of the initial PR.
Repository CI remains authoritative after creation.

Repositories whose required verification only runs for a pull request may use
`draft_first` mode:

```text
implement -> create draft PR -> PR-dependent checks/review/fix
  -> push final changes -> mark ready
```

Draft-first is an explicit repository setting, not a model judgment. It covers
PR-only CI, previews, security scans, and policy bots. 143 never opens a normal
non-draft PR before self-review.

### Two-pass review gate

When effective pre-PR review is on, publication intent starts or joins one
review loop tied to the current changeset, workspace revision, and head SHA:

1. Pass 1 reviews and fixes actionable findings.
2. Pass 2 reviews the updated diff.
3. Any pass may stop early with `REVIEW_CLEAN`.
4. A fresh clean result atomically passes the publication gate and queues the
   original `open_pr` request.

If pass 2 changes code, those mutations are unreviewed: end
`needs_human_decision` and block publication. The primary resolution is an
authorized, audited **Create draft PR** bypass; users may instead inspect,
continue, or start another loop. There is no automatic third pass.

### Independent reviewer

Automatic review prefers a native-review-capable coding agent different from
the implementation agent:

1. future personal review-agent preference
2. organization-configured supported alternative
3. another available supported alternative
4. implementation agent fallback

Persist the selection; that agent performs review and fixes in one thread and
retries cannot switch agents.

### PR lifecycle

The linked session remains canonical for CI, conflicts, readiness, feedback,
and follow-up; ending or archiving it does not close the PR. There are no
external notifications: all states use normal 143 product surfaces.

## Settings

### Organization defaults

Extend `session_automation.automatic_follow_through`:

```json
{
  "create_pr_when_agent_ready": true,
  "review_before_pr": true
}
```

Absent values resolve to `true` for existing and new organizations. Independent
kill switches may stop execution without changing stored settings.

### Personal overrides

Extend `automatic_pr_follow_through`:

```json
{
  "create_pr_when_agent_ready": "inherit",
  "review_before_pr": "inherit"
}
```

Missing values mean `inherit`. Effective policy is:

```text
personal on/off -> organization value -> product default on
```

Resolve against the session initiator captured at creation. Personal settings
cannot grant permissions, bypass readiness, override automation
`publish_policy = none`, or publish from read-only/review-only sessions.

Organization **Session automation** shows two independent toggles; Account
Settings shows `Use organization default (On) / On / Off`. Review remains
editable when automatic creation is off because it also governs explicit
publication.

### Repository exception

Repository settings add:

```json
{"pr_handoff_mode":"pre_publish"}
```

Values are `pre_publish` (default) and `draft_first`. The repository setting is
authoritative because it represents CI capabilities. `draft_first` UI copy
must explain that 143 marks the draft ready only after review passes.

## Workflow Semantics

### Agent request

```text
create_pr
  -> resolve authoritative session and initiator
  -> reject ineligible session intent or an existing linked PR target
  -> resolve effective policy
  -> resolve repository handoff mode
  -> require a stable primary changeset with a non-empty diff
  -> persist publication intent and caller options
  -> pre_publish + review off: queue open_pr
  -> pre_publish + review on:
       reuse fresh clean review, or start/join two-pass loop
       clean -> atomically queue open_pr
       other terminal result -> block for in-product attention
  -> draft_first:
       queue draft open_pr
       run PR-dependent verification and review against its head
       clean -> push final head and mark ready
       other terminal result -> leave draft and show attention
```

Repeated requests join existing state; later edits invalidate clean evidence.

### Missing tool call

If an eligible implementation turn ends with a diff but no publication intent:

- do not create a PR
- keep the session idle and resumable
- show unpublished changes and the existing `Create PR` action
- emit `agent_pr_intent_missing`

V1 does not spend another model turn asking whether the agent forgot. UI,
Slack, and API `Create PR` actions are explicit intent: they ignore the
automatic-creation preference but still respect review or an authorized bypass.

### Automations and projects

- Automation `publish_policy` and `pre_pr_review_loops` remain authoritative.
- `publish_policy = none` never publishes; `pull_request` requests publication
  for a successful non-empty result.
- Project/stack publication remains parent-controlled.

For ordinary manual sessions, retire the orchestrator behavior that queues a
PR solely from successful process completion plus a diff. Keep it temporarily
for legacy session types that cannot receive the PR tool.

## Prompt Contract

All LLM instructions live in `internal/prompts/templates/`.

Prompt assembly first applies a deterministic capability gate:

```text
new implementation/change intent
AND writable unpublished changeset
AND no linked PR
AND repository publication capability
```

Question, analysis, planning, diagnosis, review-only, and existing-PR sessions
omit both the PR-handoff fragment and the suggestion to call `create_pr`. If a
later user turn changes the task from analysis/review to implementation, prompt
assembly re-evaluates eligibility for that turn.

Eligible prompts include:

```text
For implementation work, continue until the requested change is complete and
appropriately verified. Request a pull request with `143-tools pr create` only
if you actually changed the current unpublished changeset and those changes
should be handed off for review.

Do not request a new pull request when you only answered a question, analyzed
or reviewed code, planned or diagnosed work without implementing it, made no
changes, have incomplete or unsafe changes, are awaiting a material user
decision, were asked not to publish, or are already working on an existing pull
request. Existing pull request work must update that PR through its normal
session workflow instead.

Report the tool's actual result. A queued PR or started review is not a created
PR.
```

The prompt also states effective policy:

```text
Automatic PR handoff: on
Pre-PR review/fix: on, up to 2 passes
```

If automatic handoff is off, eligible implementation turns leave verified
changes for manual publication unless the user explicitly requested a PR.

The backend independently rechecks a non-empty diff, unpublished target, and
session capability. Prompt compliance is not a security or correctness
boundary.

## Backend Contract

Introduce a coordinator used by the internal tool handler, UI, Slack, API,
automations, and project publication:

```go
type PublicationIntentCoordinator interface {
    RequestPullRequest(
        ctx context.Context,
        orgID uuid.UUID,
        sessionID uuid.UUID,
        req RequestPullRequest,
    ) (*PublicationIntentResult, error)
}
```

It resolves identity/policy/revision, creates or reuses durable publication and
review state, atomically advances the review gate, enqueues `open_pr`, and
returns typed status. The CLI contains no policy logic.

`session_publications` remains unique on `(org_id, changeset_id)`; draft,
authorship, queue, and replay options remain in `request_payload`.

For `draft_first`, publication stops at `recorded` with the review gate pending.
Clean completion pushes the reviewed head, marks the PR ready, and completes
publication. Replay adopts the recorded draft and never creates another PR.

Clean review evidence must match:

```text
org_id + session_id + changeset_id + workspace_revision + desired_head_sha
```

Loop-clean, gate-passed, and `open_pr` enqueue are one transaction. Other
terminal states persist a blocked gate without enqueueing.

## Database Schema

Organization and personal policy remain in existing
`organizations.settings` and `users.settings` JSONB. `pr_handoff_mode` uses
existing `repositories.settings` JSONB, so it requires no schema column. Add
typed publication metadata and revision-bound review evidence:

```sql
ALTER TABLE users
    ADD CONSTRAINT users_id_org_unique UNIQUE (id, org_id);

ALTER TABLE session_review_loops
    ADD COLUMN changeset_id uuid,
    ADD COLUMN workspace_revision bigint,
    ADD COLUMN desired_head_sha text,
    ADD CONSTRAINT session_review_loops_changeset_scope_fkey
        FOREIGN KEY (changeset_id, org_id, session_id)
        REFERENCES session_changesets(id, org_id, session_id)
        ON DELETE CASCADE,
    ADD CONSTRAINT session_review_loops_publication_evidence_check
        CHECK (
            source <> 'publication'
            OR (
                changeset_id IS NOT NULL
                AND workspace_revision IS NOT NULL
                AND desired_head_sha IS NOT NULL
            )
        ),
    ADD CONSTRAINT session_review_loops_id_org_unique UNIQUE (id, org_id);

ALTER TABLE session_review_loops
    DROP CONSTRAINT chk_session_review_loops_source,
    ADD CONSTRAINT chk_session_review_loops_source
        CHECK (source IN ('manual', 'automation', 'publication'));

ALTER TABLE session_publications
    ADD COLUMN trigger_kind text NOT NULL DEFAULT 'policy',
    ADD COLUMN handoff_mode text NOT NULL DEFAULT 'pre_publish',
    ADD COLUMN initiated_by_user_id uuid,
    ADD COLUMN automatic_pr_policy_source text NOT NULL DEFAULT 'product_default',
    ADD COLUMN review_policy_source text NOT NULL DEFAULT 'product_default',
    ADD COLUMN review_required boolean NOT NULL DEFAULT false,
    ADD COLUMN review_max_passes integer,
    ADD COLUMN review_loop_id uuid,
    ADD COLUMN review_workspace_revision bigint,
    ADD COLUMN review_desired_head_sha text,
    ADD CONSTRAINT session_publications_initiator_scope_fkey
        FOREIGN KEY (initiated_by_user_id, org_id)
        REFERENCES users(id, org_id),
    ADD CONSTRAINT session_publications_review_loop_scope_fkey
        FOREIGN KEY (review_loop_id, org_id)
        REFERENCES session_review_loops(id, org_id),
    ADD CONSTRAINT session_publications_trigger_kind_check
        CHECK (trigger_kind IN ('agent_ready', 'explicit_action', 'policy')),
    ADD CONSTRAINT session_publications_handoff_mode_check
        CHECK (handoff_mode IN ('pre_publish', 'draft_first')),
    ADD CONSTRAINT session_publications_automatic_policy_source_check
        CHECK (automatic_pr_policy_source IN (
            'product_default', 'organization', 'personal', 'automation',
            'explicit_action'
        )),
    ADD CONSTRAINT session_publications_review_policy_source_check
        CHECK (review_policy_source IN (
            'product_default', 'organization', 'personal', 'automation',
            'explicit_bypass'
        )),
    ADD CONSTRAINT session_publications_review_passes_check
        CHECK (
            (review_required AND review_max_passes = 2)
            OR (NOT review_required AND review_max_passes IS NULL)
        ),
    ADD CONSTRAINT session_publications_review_evidence_check
        CHECK (
            review_loop_id IS NULL
            OR (
                review_required
                AND review_workspace_revision IS NOT NULL
                AND review_desired_head_sha IS NOT NULL
            )
        );

CREATE INDEX idx_session_publications_review_loop
    ON session_publications (org_id, review_loop_id)
    WHERE review_loop_id IS NOT NULL;
```

Existing publications backfill to `trigger_kind = policy`,
`handoff_mode = pre_publish`, policy sources `product_default`,
`review_required = false`, and null review evidence.

Every store method accepts `orgID` and filters by `org_id`. Fresh-review lookup
uses the exact tenant/session/changeset/revision/SHA tuple and `status = clean`.
No database trigger is required.

## API Contract

### Agent publication

```text
POST /api/v1/internal/session/pr
POST /api/v1/internal/sessions/{sessionID}/pr
```

Signed internal bearer claims provide organization, session, and repository;
explicit IDs must match. Request:

```json
{"session_id":"optional-matching-uuid","draft":false,"author_mode":"auto"}
```

New/joined work returns `202`; an existing PR may return `200`:

```json
{
  "data": {
    "status": "review_started",
    "session_id": "...",
    "publication_id": "...",
    "review_loop_id": "...",
    "pull_request_url": null,
    "reason": null
  }
}
```

Repeated nonterminal requests return current state rather than `409`.

Errors use the standard envelope:

| HTTP | Code | Meaning |
| ---: | --- | --- |
| 400 | `INVALID_BODY` | Malformed JSON |
| 400 | `INVALID_AUTHOR_MODE` | Unsupported author mode |
| 400 | `SESSION_MISMATCH` | Explicit ID differs from token |
| 401 | `UNAUTHORIZED` | Missing/invalid token |
| 403 | `TOOL_NOT_AVAILABLE` | Session origin cannot publish |
| 403 | `REPO_MISMATCH` | Token/session repository mismatch |
| 403 | `PUBLICATION_NOT_ALLOWED` | Authorization or repository policy |
| 404 | `NOT_FOUND` | Session not found |
| 409 | `SESSION_NOT_PUBLICATION_ELIGIBLE` | Question/review/read-only/existing-PR session |
| 409 | `WORKSPACE_NOT_READY` | No stable current snapshot |
| 409 | `NO_CHANGES` | Current changeset has no publishable diff |
| 422 | `REVIEW_UNSUPPORTED` | Required review has no supported agent |
| 500 | `PUBLICATION_INTENT_FAILED` | Durable intent failure |

Policy-disabled handoff returns `manual_publication_required`, not an error.

### Settings

```text
GET   /api/v1/settings
PATCH /api/v1/settings
GET   /api/v1/auth/me
PATCH /api/v1/auth/me/settings
PATCH /api/v1/repositories/{id}
```

- Organization GET: authenticated `viewer`, `member`, or `admin`.
- Organization PATCH: `admin`.
- Personal routes: authenticated user with active membership; self only.
- Repository PATCH: authenticated `admin`; accepts
  `settings.pr_handoff_mode = pre_publish|draft_first`.
- Organization booleans reject invalid values with `400 INVALID_SETTINGS`.
- Personal merge-patch values reject invalid values with
  `400 INVALID_USER_SETTINGS`.
- Invalid repository handoff mode returns `400 INVALID_REPOSITORY_SETTINGS`.
- Existing settings concurrency and RFC 7386 personal merge behavior remain.

Session detail adds resolved policy:

```json
{
  "publication_policy": {
    "create_pr_when_agent_ready": true,
    "create_pr_source": "organization",
    "review_before_pr": false,
    "review_source": "personal",
    "review_max_passes": 2,
    "pr_handoff_mode": "pre_publish"
  }
}
```

Existing live updates trigger refetches; no new SSE event is required.

## UX States

Use one session Overview card and hide duplicate `Create PR` actions during
automatic progress:

| State | Display/action |
| --- | --- |
| Reviewing | Pass N of 2 and current review/fix activity |
| Needs attention | Draft bypass, open review, and continue actions |
| Publishing | Review passed; publication queued |
| Published | PR number, title, and review link |

## Failure and Idempotency

| Condition | Result |
| --- | --- |
| Agent request with no diff | Return `NO_CHANGES`; create no publication |
| Policy/automation request with no diff | Durable completed no-op |
| Repeated request | Existing review/publication state |
| Fresh clean review | Queue without another review |
| Pass 2 changes code | Block; offer audited draft bypass |
| Draft-first review passes | Push reviewed head, then mark PR ready |
| Draft-first review blocks/fails | Leave draft open with in-product attention |
| Review failure/cancel | Preserve intent; show in-product retry |
| Revision/head changes | Mark evidence stale; require new review |
| Session ends mid-review | Durable jobs continue |
| Existing GitHub PR | Adopt through publication reconciliation |
| Publication fails after clean review | Retry without reviewing unchanged head |
| Setting changes mid-operation | Current intent keeps snapshot; new intent re-resolves |
| Kill switch off | Fail safe to manual publication |

## Audit and Metrics

Audit agent intent, policy source, review start/reuse/pass/block, publication,
failure, and bypass with tenant/session/changeset/user identifiers.

Metrics:

- `agent_pr_intents_total{source,outcome}`
- `agent_pr_intent_missing_total{agent_type,session_origin}`
- `pre_pr_review_loops_total{outcome,passes}`
- `pre_pr_review_fix_rate`
- `pre_pr_review_stale_total`
- `pr_publication_after_review_seconds`
- `automatic_pr_manual_override_total{direction}`

Watch review-induced changes, time-to-PR, personal disable rates, immediate PR
closure/revert, publication success, cost, and latency.

## Rollout

1. Ship parsing, effective-policy responses, UI, audit, and kill switches with
   execution disabled.
2. Enable prompt/tool states internally.
3. Enable two-pass review internally and inspect churn/block rates.
4. Enable for selected organizations with visible default-on settings.
5. Enable generally.
6. Remove the generic manual-session completion trigger after agent-tool
   coverage is demonstrated.

Kill switches affect execution only and never mutate customer settings.

## Verification

Backend tests cover defaults, inheritance, stable initiator, automation
precedence, explicit intent, review on/off, independent-agent selection,
atomic clean completion, stale evidence, pass-2 mutation blocking, failures,
idempotency, question/review/existing-PR rejection, agent and automation
no-diff behavior, authorization, and every `org_id` filter.

Frontend tests cover default-on controls, inherited/overridden copy, progress
without duplicate actions, draft bypass, attention/retry, and queued-versus-
created wording.

Prompt/adapter tests prove that questions, analysis, review-only work, existing
PR sessions, and no-change completion never suggest or call `create_pr`; they
also cover eligible changes, disabled policy, explicit requests, and typed
outcomes.

## Non-Goals

- Automatic merge.
- Treating process exit as readiness.
- Replacing human review.
- Normalizing review findings.
- Configurable pass counts in v1.
- Direct agent GitHub PR-write access.
- Replacing automation or project publication policy.

## Implementation PRs

Implement in three sequential PRs. The first two ship with execution kill
switches off and no user-visible controls, so each is safe to merge
independently.

### PR 1: Publication intent, policy, and eligibility

Build the durable foundation without changing default product behavior.

Scope:

- Add organization and personal setting types, validation, inheritance, and
  effective-policy resolution; absent organization values resolve on.
- Add the non-review `session_publications` metadata (`trigger_kind`,
  `handoff_mode`, initiator, and policy sources), constraints, models, and
  tenant-scoped stores. Defer review-loop links/evidence to PR 2.
- Introduce `PublicationIntentCoordinator` and route agent/UI/API publication
  requests through it.
- Return typed asynchronous tool/API outcomes and make repeated requests
  idempotent instead of conflicting.
- Enforce new-change eligibility: reject questions, analysis/review-only
  sessions, existing-PR sessions, read-only sessions, unstable workspaces, and
  empty diffs.
- Add the conditional prompt template and adapter wiring, guarded by the
  automatic-publication kill switch.
- Preserve existing automation/project policy and the legacy manual completion
  trigger while the kill switch is off.
- Add audit events, metrics, and focused model/store/handler/prompt tests.

Acceptance:

- With the kill switch off, production behavior is unchanged.
- With it on in tests, an eligible implementation can request one durable PR
  publication; ineligible and no-change sessions cannot.
- No review loop or `draft_first` behavior is enabled yet.

### PR 2: Review gate and draft-first lifecycle

Connect durable publication intent to the existing review-loop and publication
state machines.

Scope:

- Add review-loop changeset/revision/SHA evidence, `publication` source, and
  tenant-scoped constraints.
- Add `session_publications` review-required, pass-count, loop-link,
  revision/SHA evidence columns, constraints, and index.
- Implement fresh clean-review reuse and atomic
  `loop clean + gate passed + open_pr enqueue`.
- Run the bounded two-pass review/fix cycle, prefer and persist an independent
  reviewer, and block when pass 2 changes code.
- Implement the audited draft bypass.
- Add `pre_publish` and `draft_first` repository policy parsing and
  persistence.
- Implement draft creation, PR-dependent verification/review, final-head push,
  mark-ready, replay, and failure recovery without duplicate PRs.
- Add in-product activity/state events, but no external notifications.
- Add concurrency, staleness, recovery, and end-to-end service/worker tests.

Acceptance:

- `pre_publish` creates no PR until a revision-bound review is clean.
- `draft_first` creates exactly one draft and marks it ready only after the
  reviewed head is published.
- Review failure, pass-two mutation, cancellation, and head races fail safe
  without publishing a normal unreviewed PR.

### PR 3: Product surfaces and rollout

Expose the completed workflow and remove the conflicting legacy behavior.

Scope:

- Add organization and Account Settings controls with effective-value copy.
- Add repository `pr_handoff_mode` control and draft-first explanation.
- Add the unified session Overview states and actions for review, attention,
  draft bypass, publication, and published PR.
- Ensure UI, Slack, API, automation, and project entry points use the same
  coordinator while emitting no external workflow notifications.
- Enable prompt/publication/review kill switches through staged rollout
  configuration.
- Measure missing intent, review churn, override rate, time-to-PR, publication
  failures, immediate closure/revert, latency, and cost.
- After tool-call coverage is demonstrated, remove automatic PR creation based
  only on manual-session process completion plus a diff; retain explicitly
  configured automation/project publication.
- Complete frontend integration tests and broad backend regression
  verification.

Acceptance:

- Both organization defaults are visibly on, with working personal overrides.
- Questions, review-only sessions, existing-PR sessions, and no-change turns
  never suggest or create a new PR.
- Eligible implementation sessions follow the configured pre-publish or
  draft-first workflow through one coherent in-product experience.
