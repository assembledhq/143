# Design: Agent-Initiated PR Publication with Automatic Review

> **Status:** Implemented | **Last reviewed:** 2026-08-02
>
> **Depends on:** [overall.md](../overall.md),
> [review agent loops](../implemented/78-review-agent-loops.md),
> [PR creation](../implemented/40-pr-creation-revamp.md),
> [automatic PR repair](../implemented/113-automated-pr-repair.md),
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
repository policy, review requirements, or publication gates.

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
PR-only CI, previews, security scans, and policy bots. Automatic handoff never
opens a normal non-draft PR before self-review; an authenticated user may make
the explicit publication decision with **Create PR**.

### Two-pass review gate

When effective pre-PR review is on, an automatic agent-ready publication
intent starts or joins one review loop tied to the current changeset,
workspace revision, and head SHA. An authenticated user **Create PR** action
queues publication without this automatic review gate:

1. Pass 1 reviews and fixes actionable findings.
2. Pass 2 reviews the updated diff.
3. Any pass may stop early with `REVIEW_CLEAN`.
4. A fresh clean result atomically passes the publication gate and queues the
   original `open_pr` request.

If pass 2 changes code, those mutations are unreviewed: end
`needs_human_decision` and block publication. The primary resolution is an
authorized, audited **Create draft PR** bypass; users may instead inspect,
continue, or start another loop. There is no automatic third pass.

#### Interaction with the one-running-loop-per-session invariant

`session_review_loops` enforces `idx_session_review_loops_one_running_per_session`
on `(org_id, session_id) WHERE status = 'running'`, and
`reviewloop.Service.Start` returns `ErrReviewLoopAlreadyRunning` when a loop is
already running. That invariant is per **session**, not per changeset, and this
design does not relax it: concurrent review loops within a session remain
disallowed.

"Start or join" therefore resolves as follows when a loop is already running:

- The running loop already carries `source = 'publication'` and matching
  `changeset_id + workspace_revision + desired_head_sha`: join it. Its clean
  result satisfies this publication's gate.
- The running loop carries `source = 'publication'` but stale or different
  evidence: do not join and do not start. Record the intent, leave the gate
  pending, and re-evaluate when that loop terminates.
- The running loop has `source = 'manual'` or `'automation'`: do **not** adopt
  it and do not backfill its evidence columns. A loop started before the
  publication request did not review a known revision, so it cannot produce
  valid evidence. Record the intent with a pending gate and start the
  publication loop when the existing loop terminates.

In the last two cases the tool returns `review_in_progress`, not `blocked` —
the intent is durable and resumes on its own. Without this rule a user-started
manual loop would permanently strand every subsequent `create_pr`, because the
manual loop's `source` exempts it from the evidence CHECK and its evidence
columns stay NULL while the gate requires an exact tuple match.

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

The linked session remains canonical for CI, conflicts, review, feedback,
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

Both fields must be `*bool`, not the plain `bool` used by the existing
default-off flags in `AutomaticFollowThroughOrgSettings`. A plain
`bool` with `omitempty` drops an explicitly stored `false` at marshal time,
which reads back as absent and therefore resolves to `true` — making "off"
unpersistable. Follow the existing default-on precedent in the same package,
`SandboxLifecycleSettings.PreviewHoldsSandbox *bool` plus
`EffectivePreviewHoldsSandbox()`:

```go
CreatePRWhenAgentReady *bool `json:"create_pr_when_agent_ready,omitempty"`
ReviewBeforePR         *bool `json:"review_before_pr,omitempty"`

func (s AutomaticFollowThroughOrgSettings) EffectiveCreatePRWhenAgentReady() bool {
    if s.CreatePRWhenAgentReady == nil {
        return true
    }
    return *s.CreatePRWhenAgentReady
}
```

Resolve absent values in `ParseOrgSettings` (not only in
`DefaultNewOrganizationSettings`) because the default must also apply
retroactively to existing organizations, per the note on that function.

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

`ParseUserSettings` uses `DisallowUnknownFields`, so a binary that does not
know these keys fails to parse the *entire* `users.settings` document, not just
the new fields. Deploy is therefore one-way: the parsing change in PR 1 must be
fully rolled out before any binary writes these keys, and rollback past PR 1 is
unsafe once personal overrides exist. Organization settings do not have this
constraint (`ParseOrgSettings` tolerates unknown fields).

Resolve against the session initiator captured at creation. Personal settings
cannot grant permissions, bypass review requirements, override automation
`publish_policy = none`, or publish from read-only/review-only sessions.

Organization **Session automation** shows two independent toggles; Account
Settings shows `Use organization default (On) / On / Off`. Review remains
independently editable so an organization default and a personal automatic
handoff override can be configured separately.

### Repository exception

Repository settings add:

```json
{"pr_handoff_mode":"pre_publish"}
```

Values are `pre_publish` (default) and `draft_first`. The repository setting is
authoritative because it represents CI capabilities. `draft_first` UI copy
must explain that 143 marks the draft ready only after review passes.

`PATCH /api/v1/repositories/{id}` currently **replaces** the whole settings
blob (`repo.Settings = *req.Settings`) and has no typed settings model — it
unmarshals to `map[string]json.RawMessage` only to reject the legacy `pm` key.
Sending `{"settings":{"pr_handoff_mode":"draft_first"}}` would therefore
destroy every other repository setting. This work must either read-modify-write
the stored blob or introduce a typed `RepositorySettings` model with RFC 7386
merge semantics, matching how personal settings already behave. Whichever is
chosen, add a regression test that patching `pr_handoff_mode` preserves
unrelated repository settings.

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

An authenticated user-channel `explicit_action` skips the automatic review
gate and queues `open_pr` directly. It still uses the durable publication row,
authorship and builder authorization, repository handoff mode, deduplication,
and recovery paths above. Agent-ready requests continue to use the configured
review gate.

Repeated requests join existing state; later edits invalidate clean evidence.

### Missing tool call

If an eligible implementation turn ends with a diff but no publication intent:

- do not create a PR
- keep the session idle and resumable
- show unpublished changes and the existing `Create PR` action
- emit `agent_pr_intent_missing`

V1 does not spend another model turn asking whether the agent forgot. UI,
Slack, and API `Create PR` actions attributed to an authenticated user are
explicit publication decisions: they ignore automatic-creation and automatic-
review preferences while retaining authorization and repository policy.

### Automations and projects

- Automation `publish_policy` and `pre_pr_review_loops` remain authoritative.
- `publish_policy = none` never publishes; `pull_request` requests publication
  for a successful non-empty result.
- Project/stack publication remains parent-controlled.

Two passes is the fixed count for **agent-ready** publication. Explicit
user-channel publication queues directly. Automation-initiated publication
keeps its configured `pre_pr_review_loops` (validated 0-5, passed straight
through as `MaxPasses` by
the existing worker path) and records that value in `review_max_passes`. The
schema therefore bounds `review_max_passes` to 1-5 rather than pinning it to 2;
pinning it would reject every automation not configured for exactly two loops.
`pre_pr_review_loops = 0` means review is not required for that automation.

For ordinary manual sessions, retire the orchestrator behavior that queues a
PR solely from successful process completion plus a diff. Keep it temporarily
for legacy session types that cannot receive the PR tool.

## Prompt Contract

All LLM instructions live in `internal/prompts/templates/` as `.template`
files, with a corresponding exported render function added to
`internal/prompts/prompts.go`, per the repository prompt convention. No prompt
text is inlined as a Go constant or string literal in service code. Decide
explicitly whether the handoff fragment is overridable via the existing
`prompt_overrides` table (keyed by `template_id`); v1 default is not
overridable.

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
existing `repositories.settings` JSONB, so it requires no schema column.

Both target tables are populated in production, so the constraint additions need
care with locking.

The migrator wraps each migration file in a single transaction, and Postgres
holds locks until commit. That has a consequence worth stating plainly:
`ADD CONSTRAINT ... NOT VALID` followed by `VALIDATE CONSTRAINT` **in the same
file** does not reduce lock duration at all — the `ACCESS EXCLUSIVE` lock taken
by the `ADD` is still held while `VALIDATE` scans the table. The existing uses
of that pattern (`000035`, `000228`, `000241`) are convention, and in `000228`
a way to order a backfill `UPDATE` between the two steps; none of them claim a
locking benefit, and this design needs no such backfill.

The two steps are therefore split across two migrations:

- **Migration N** adds the columns and every CHECK/FK as `NOT VALID`. Each
  `ADD` holds `ACCESS EXCLUSIVE` only long enough to update the catalog, with
  no table scan.
- **Migration N+1** issues the `VALIDATE CONSTRAINT` statements. In its own
  transaction, `VALIDATE` takes `SHARE UPDATE EXCLUSIVE`, so the scans run
  without blocking reads or writes.

Between the two, the constraints already enforce on inserts and updates; only
pre-existing rows are unverified. Deferring N+1 is therefore safe for new
writes. N+1's down migration is a no-op — a validated constraint cannot be
returned to `NOT VALID`, and does not need to be.

The SQL below is presented as one schema change for readability, but PR 1 and
PR 2 ship disjoint halves of it (PR 1: `trigger_kind`, `handoff_mode`,
initiator, policy sources; PR 2: the review-loop and review-evidence columns).
Each half is its own N / N+1 pair — four migrations, and eight files counting
the `.up.sql`/`.down.sql` pairs the migrator requires.

`NOT VALID` does **not** apply to the index builds, which are the residual
locking risk in migration N: the two `UNIQUE (id, org_id)` additions each take
`ACCESS EXCLUSIVE`, and `idx_session_publications_review_loop` takes `SHARE`,
which blocks writes to `session_publications` for the duration of the build.
All three sit in the same transaction, so the holds compound, and
`CREATE INDEX CONCURRENTLY` cannot run inside the migrator's transaction. Cap
the worst case with `SET LOCAL lock_timeout` in every migration here, per
`000135` and the eleven other migrations that use it — noting that
`lock_timeout` bounds lock *acquisition*, not how long a statement holds a lock
once acquired, so it is a fail-fast guard rather than a duration guarantee.

Migration N adds typed publication metadata and revision-bound review evidence:

```sql
SET LOCAL lock_timeout = '5s';

-- Composite unique keys backing the tenant-scoped foreign keys below.
-- Redundant with the primary keys, but required for (id, org_id) references.
-- Naming follows repositories_id_org_id_key (000135) and
-- sessions_id_org_id_key (000238).
ALTER TABLE users
    ADD CONSTRAINT users_id_org_id_key UNIQUE (id, org_id);

ALTER TABLE session_review_loops
    ADD CONSTRAINT session_review_loops_id_org_id_key UNIQUE (id, org_id);

-- Widen the source enum before adding evidence rules that reference it.
ALTER TABLE session_review_loops
    DROP CONSTRAINT chk_session_review_loops_source;
ALTER TABLE session_review_loops
    ADD CONSTRAINT chk_session_review_loops_source
        CHECK (source IN ('manual', 'automation', 'publication')) NOT VALID;

ALTER TABLE session_review_loops
    ADD COLUMN changeset_id uuid,
    ADD COLUMN workspace_revision bigint,
    ADD COLUMN desired_head_sha text;

-- changeset_id is nullable; MATCH SIMPLE skips the FK when it is NULL, so
-- pre-existing manual/automation loops are unaffected.
ALTER TABLE session_review_loops
    ADD CONSTRAINT session_review_loops_changeset_scope_fkey
        FOREIGN KEY (changeset_id, org_id, session_id)
        REFERENCES session_changesets(id, org_id, session_id)
        ON DELETE CASCADE
        NOT VALID;

ALTER TABLE session_review_loops
    ADD CONSTRAINT session_review_loops_publication_evidence_check
        CHECK (
            source <> 'publication'
            OR (
                changeset_id IS NOT NULL
                AND workspace_revision IS NOT NULL
                AND desired_head_sha IS NOT NULL
            )
        ) NOT VALID;

ALTER TABLE session_publications
    ADD COLUMN trigger_kind text NOT NULL DEFAULT 'policy',
    ADD COLUMN handoff_mode text NOT NULL DEFAULT 'pre_publish',
    ADD COLUMN initiated_by_user_id uuid,
    ADD COLUMN automatic_pr_policy_source text NOT NULL DEFAULT 'product_default',
    ADD COLUMN review_policy_source text NOT NULL DEFAULT 'product_default',
    ADD COLUMN review_max_passes integer,
    ADD COLUMN review_loop_id uuid,
    ADD COLUMN review_workspace_revision bigint,
    ADD COLUMN review_desired_head_sha text;

ALTER TABLE session_publications
    ADD CONSTRAINT session_publications_initiator_scope_fkey
        FOREIGN KEY (initiated_by_user_id, org_id)
        REFERENCES users(id, org_id) NOT VALID,
    ADD CONSTRAINT session_publications_review_loop_scope_fkey
        FOREIGN KEY (review_loop_id, org_id)
        REFERENCES session_review_loops(id, org_id) NOT VALID,
    ADD CONSTRAINT session_publications_trigger_kind_check
        CHECK (trigger_kind IN ('agent_ready', 'explicit_action', 'policy')) NOT VALID,
    ADD CONSTRAINT session_publications_handoff_mode_check
        CHECK (handoff_mode IN ('pre_publish', 'draft_first')) NOT VALID,
    ADD CONSTRAINT session_publications_automatic_policy_source_check
        CHECK (automatic_pr_policy_source IN (
            'product_default', 'organization', 'personal', 'automation',
            'explicit_action'
        )) NOT VALID,
    ADD CONSTRAINT session_publications_review_policy_source_check
        CHECK (review_policy_source IN (
            'product_default', 'organization', 'personal', 'automation',
            'explicit_bypass'
        )) NOT VALID,
    -- Only agent tool calls can express agent-judged readiness.
    ADD CONSTRAINT session_publications_agent_ready_source_check
        CHECK (trigger_kind <> 'agent_ready' OR source = 'agent_tool') NOT VALID,
    -- Bounded like session_review_loops.max_passes so an automation's
    -- configured pre_pr_review_loops (1-5) can be recorded verbatim.
    ADD CONSTRAINT session_publications_review_passes_check
        CHECK (review_max_passes IS NULL OR review_max_passes BETWEEN 1 AND 5) NOT VALID,
    ADD CONSTRAINT session_publications_review_evidence_check
        CHECK (
            review_loop_id IS NULL
            OR (
                review_max_passes IS NOT NULL
                AND review_workspace_revision IS NOT NULL
                AND review_desired_head_sha IS NOT NULL
            )
        ) NOT VALID;

CREATE INDEX idx_session_publications_review_loop
    ON session_publications (org_id, review_loop_id)
    WHERE review_loop_id IS NOT NULL;
```

Migration N+1 validates the pre-existing rows. It touches no schema and can be
run, or re-run, at any time after N:

```sql
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_review_loops
    VALIDATE CONSTRAINT chk_session_review_loops_source;
ALTER TABLE session_review_loops
    VALIDATE CONSTRAINT session_review_loops_changeset_scope_fkey;
ALTER TABLE session_review_loops
    VALIDATE CONSTRAINT session_review_loops_publication_evidence_check;

ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_initiator_scope_fkey;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_review_loop_scope_fkey;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_trigger_kind_check;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_handoff_mode_check;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_automatic_policy_source_check;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_review_policy_source_check;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_agent_ready_source_check;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_review_passes_check;
ALTER TABLE session_publications
    VALIDATE CONSTRAINT session_publications_review_evidence_check;
```

These still share one transaction, so the `SHARE UPDATE EXCLUSIVE` locks are
held on both tables until N+1 commits. That is acceptable where the combined
`ACCESS EXCLUSIVE` hold in a single-file version would not be:
`SHARE UPDATE EXCLUSIVE` permits concurrent `SELECT`, `INSERT`, `UPDATE`, and
`DELETE`, and conflicts only with DDL, `VACUUM`, and other holders of the same
mode. The `lock_timeout` still matters here — a weak lock is not a quickly
acquired one, and `VALIDATE` will queue behind any `ACCESS EXCLUSIVE` holder
such as a concurrent `VACUUM FULL` or another migration. N+1's down migration
is empty; the file must still exist, as it does for the many comment-only down
migrations already in `migrations/`.

Migration N's down migration drops the added constraints, index, and columns in
reverse order and restores the original source enum. It must not delete review
history:
`session_review_loop_passes.loop_id` cascades on delete, so removing
`source = 'publication'` loops would destroy user-visible review output and
could orphan a queued worker job that still references a running loop. Remap
instead:

```sql
SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS idx_session_publications_review_loop;

ALTER TABLE session_publications
    DROP CONSTRAINT IF EXISTS session_publications_review_evidence_check,
    DROP CONSTRAINT IF EXISTS session_publications_review_passes_check,
    DROP CONSTRAINT IF EXISTS session_publications_agent_ready_source_check,
    DROP CONSTRAINT IF EXISTS session_publications_review_policy_source_check,
    DROP CONSTRAINT IF EXISTS session_publications_automatic_policy_source_check,
    DROP CONSTRAINT IF EXISTS session_publications_handoff_mode_check,
    DROP CONSTRAINT IF EXISTS session_publications_trigger_kind_check,
    DROP CONSTRAINT IF EXISTS session_publications_review_loop_scope_fkey,
    DROP CONSTRAINT IF EXISTS session_publications_initiator_scope_fkey,
    DROP COLUMN IF EXISTS review_desired_head_sha,
    DROP COLUMN IF EXISTS review_workspace_revision,
    DROP COLUMN IF EXISTS review_loop_id,
    DROP COLUMN IF EXISTS review_max_passes,
    DROP COLUMN IF EXISTS review_policy_source,
    DROP COLUMN IF EXISTS automatic_pr_policy_source,
    DROP COLUMN IF EXISTS initiated_by_user_id,
    DROP COLUMN IF EXISTS handoff_mode,
    DROP COLUMN IF EXISTS trigger_kind;

-- Preserve the loops and their passes; only the source label must narrow.
UPDATE session_review_loops SET source = 'manual' WHERE source = 'publication';

ALTER TABLE session_review_loops
    DROP CONSTRAINT IF EXISTS session_review_loops_publication_evidence_check,
    DROP CONSTRAINT IF EXISTS session_review_loops_changeset_scope_fkey,
    DROP COLUMN IF EXISTS desired_head_sha,
    DROP COLUMN IF EXISTS workspace_revision,
    DROP COLUMN IF EXISTS changeset_id,
    DROP CONSTRAINT IF EXISTS session_review_loops_id_org_id_key;

ALTER TABLE session_review_loops
    DROP CONSTRAINT IF EXISTS chk_session_review_loops_source;
ALTER TABLE session_review_loops
    ADD CONSTRAINT chk_session_review_loops_source
        CHECK (source IN ('manual', 'automation'));

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_id_org_id_key;
```

Re-applying the up migration after such a rollback leaves those loops labelled
`manual`, which the running-loop rules already handle: they are not adopted as
publication evidence, and a fresh publication loop starts once they terminate.

Existing publications backfill to `trigger_kind = policy`,
`handoff_mode = pre_publish`, policy sources `product_default`, and null review
evidence.

### Column overlap with existing publication state

Two existing `session_publications` columns cover adjacent ground; neither is
duplicated:

- **`source` vs `trigger_kind`.** `source` (`'user'`, `'automation'`,
  `'agent_tool'`, `'backend'`, `'webhook'`, `'reconciler'`, `'backfill'`)
  records the entry point that created the row. `trigger_kind` records *why*.
  They are not derivable from one another: an explicit "create the PR" user
  instruction relayed through the agent tool is
  `source = 'agent_tool'`, `trigger_kind = 'explicit_action'`, whereas the
  agent's own readiness judgment through the same tool is
  `trigger_kind = 'agent_ready'`.
  `session_publications_agent_ready_source_check` pins the one combination that
  must never diverge.
  An authorized user may take over execution of an intent parked by a kill
  switch without rewriting that provenance. The durable request payload then
  records `publication_execution_source = user`; workers use that value for
  runtime authorization and execution gates, and session detail exposes it as
  `execution_source` so the UI does not continue to show the original agent
  channel as paused while the manual path is running.
- **`review_gate_state` is the single source of truth for whether review is
  required.** This design deliberately does *not* add a `review_required`
  boolean to `session_publications`, because
  `review_gate_state = 'not_required'` already encodes it and a second flag
  could contradict it. For rows this design creates, `review_max_passes` is
  non-NULL whenever review applies. That is a write-path invariant, not a
  constraint: migration `000252` backfilled some rows to
  `review_gate_state = 'passed'` with no review columns set, so the passes and
  evidence constraints key off `review_loop_id`/`review_max_passes` rather than
  the gate state. A CHECK requiring the two to agree could be added and left
  permanently `NOT VALID`, which would enforce on new and updated rows while
  never scanning the historical ones. That is declined here: a constraint the
  codebase can never validate is easy to mistake for a verified invariant, and
  the write path is a single coordinator, so the store-level test is the
  clearer guard.

`desired_head_sha` and `review_desired_head_sha` are genuinely different facts:
the SHA the publication intends to publish, and the SHA that was actually
reviewed. They differ whenever the workspace moves after a review, which is
precisely the staleness this design detects.

`review_workspace_revision` and `review_desired_head_sha` are, however, a
deliberate denormalization of the joined `session_review_loops` row reachable
via `review_loop_id`. The copy exists so the atomic
`loop clean + gate passed + open_pr enqueue` transaction can compare evidence
without joining, and so evidence survives if the loop row is later relabelled
(see the down migration). The pair is written when the loop is linked. If an
earlier pass fixes code, the worker pushes that checkpoint and atomically
rotates both the loop evidence and publication copy before starting the next
pass; the pair then remains fixed for that pass. Workspace movement outside
that bounded fix transition invalidates the evidence and starts a new review
loop. Any code path that updates one evidence field must update the other in
the same statement.

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

**Every** non-error outcome returns `202` with an unwrapped body, extending the
existing `{"status","session_id"}` response in place:

```json
{
  "status": "review_started",
  "session_id": "...",
  "publication_id": "...",
  "review_loop_id": "...",
  "pull_request_url": null,
  "reason": null
}
```

This response is *not* wrapped in the `{"data": ...}` `SingleResponse` envelope
used by the public API, and terminal outcomes such as `already_published` do
not return `200`. Both constraints come from the shipped in-sandbox client,
`integration.InternalPullRequestCreator.CreatePullRequest`, which treats any
status other than `202` as a hard failure and unmarshals the body directly into
a flat `CreatePullRequestResult`. A `data`-wrapped body would decode to a
zero-valued struct with an empty `status` and **no error**, silently destroying
the typed signal the whole product contract depends on. Keeping the shape flat
and the status `202` means old and new clients agree.

Repeated nonterminal requests return current state rather than `409`. This is
the end state reached in PR 3; PR 1 adds the fields above while still returning
today's `409`s.

#### Client compatibility

`143-tools` is compiled into the sandbox image (`sandbox/Dockerfile`), so
running sandboxes carry whatever client shipped with their image. The rollout
must therefore assume version skew in both directions:

- New server, old client, **additive fields only**: safe. The extra fields are
  ignored by the old struct and the status code is unchanged.
- New server, old client, **after the `409`s are retired**: not merely
  additive. A changeset that previously produced a `PR_ALREADY_CREATED` tool
  error now returns `202`, and the old client reports it to the model as a
  success whose `status` string it does not recognize. That is the intended end
  state, but it is a semantic change for old clients and is exactly why the
  retirement waits for images to drain.
- Old server, new client: the new client must tolerate a response carrying only
  `status` and `session_id`, and must read `status` directly — the old server
  already returns `"queued"`. Do not infer state from a missing
  `publication_id`; a future response may legitimately omit it.

`InternalPullRequestCreator` still needs two changes in PR 1: surface the
richer typed statuses to the agent — `blocked` and `manual_publication_required`
arrive on the success path as `202` statuses, not as errors — and stop
collapsing every non-`202` into an opaque truncated body string, so structured
failures such as `403 PUBLICATION_NOT_ALLOWED` and `409 NO_CHANGES` reach the
model as codes it can act on. Its `http.Client` timeout is 10s, which the coordinator's
synchronous work (policy resolution, changeset diff check, gate transaction,
review-loop start) must fit inside; if it cannot, respond before starting the
loop rather than raising the timeout.

Errors use the standard `{"error": {...}}` envelope. Codes marked *(existing)*
are already emitted by this endpoint and are retained:

| HTTP | Code | Meaning |
| ---: | --- | --- |
| 400 | `INVALID_BODY` | Malformed JSON *(existing)* |
| 400 | `INVALID_ID` | Unparseable path session ID *(existing)* |
| 400 | `INVALID_AUTHOR_MODE` | Unsupported author mode *(existing)* |
| 400 | `SESSION_MISMATCH` | Body `session_id` differs from token *(existing)* |
| 401 | `UNAUTHORIZED` | Missing/invalid token *(existing)* |
| 403 | `SESSION_MISMATCH` | Token not scoped to, or not authorized for, this session *(existing)* |
| 403 | `TOOL_NOT_AVAILABLE` | Session origin cannot publish *(existing)* |
| 403 | `REPO_MISMATCH` | Token/session repository mismatch *(existing)* |
| 403 | `PUBLICATION_NOT_ALLOWED` | Authorization or repository policy |
| 404 | `NOT_FOUND` | Session not found *(existing)* |
| 409 | `SESSION_NOT_PUBLICATION_ELIGIBLE` | Question/review/read-only/existing-PR session |
| 409 | `WORKSPACE_NOT_READY` | No stable current snapshot |
| 409 | `NO_CHANGES` | Current changeset has no publishable diff |
| 422 | `REVIEW_UNSUPPORTED` | Required review has no supported agent |
| 500 | `PUBLICATION_INTENT_FAILED` | Durable intent failure |

`SESSION_MISMATCH` is deliberately listed twice: the existing handler returns
`400` for a body/token mismatch but `403` when the token is not authorized for
the session at all. Both must be preserved — collapsing the `403` into a `400`
would downgrade an authorization rejection.

The following codes are **retired** by the move to idempotent intent, and this
is a breaking change to a live agent-facing contract:

| Retired code | HTTP | Replaced by |
| --- | ---: | --- |
| `PR_IN_FLIGHT` | 409 | `202` with the current `status` |
| `PR_ALREADY_CREATED` | 409 | `202` with `status = already_published` |
| `PR_EXISTS` | 409 | `202` with `status = already_published` |
| `PRIMARY_CHANGESET_FAILED` | 500 | `PUBLICATION_INTENT_FAILED` |
| `PR_STATE_FAILED` | 500 | `PUBLICATION_INTENT_FAILED` |
| `ENQUEUE_FAILED` | 500 | `PUBLICATION_INTENT_FAILED` |
| `INTERNAL_ERROR` | 500 | `PUBLICATION_INTENT_FAILED` |

Because old clients turn the retired `409`s into tool errors and the new `202`s
into successes, this transition is safe in one direction only: it must ship
after the client tolerates the richer payload, never before. It is therefore
the last contract change in the sequence — PR 1 adds the response fields while
still returning the `409`s, and PR 3 retires them once sandbox images have
rolled forward. Everything else in this design is additive on the wire.

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
- Repository PATCH: `admin` **or `member`** — the existing route already sits
  behind `RequireRole("admin", "member")`. This design does not tighten it;
  restricting `pr_handoff_mode` to admins would require moving the route and
  would break members who edit repository settings today. Accepts
  `settings.pr_handoff_mode = pre_publish|draft_first`.
- Organization booleans reject invalid values with `400 INVALID_SETTINGS`.
- Personal merge-patch values reject invalid values with
  `400 INVALID_USER_SETTINGS`.
- Invalid repository handoff mode returns `400 INVALID_SETTINGS`, reusing the
  code the repository PATCH handler already emits for bad settings.
- Existing settings concurrency and RFC 7386 personal merge behavior remain.
- Repository PATCH must preserve unrelated keys in `repositories.settings`
  (see [Repository exception](#repository-exception)); it currently replaces
  the entire blob.

Session detail adds resolved policy:

```json
{
  "publication_policy": {
    "create_pr_when_agent_ready": true,
    "create_pr_source": "organization",
    "review_before_pr": false,
    "review_execution_enabled": true,
    "agent_publication_execution_enabled": true,
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
| Execution paused | Needs attention with a continue action; never an indefinite reviewing spinner |
| Publishing | Review passed; publication queued |
| Published | PR number, title, and review link |

## Failure and Idempotency

| Condition | Result |
| --- | --- |
| Agent request with no diff | Return `NO_CHANGES`; create no publication |
| Policy/automation request with no diff | Durable completed no-op |
| Repeated request | Existing review/publication state |
| Unrelated review loop already running | Park intent as `review_in_progress`; start the publication loop when it ends |
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
   execution disabled. Personal settings parsing must be fully rolled out
   before anything writes the new keys, because `DisallowUnknownFields` makes
   the change one-way.
2. Ship the updated in-sandbox `143-tools` client (PR 1) and let sandbox images
   roll forward. Response changes up to this point are additive only; the
   `409` retirement waits for this step to drain and lands in PR 3.
3. Enable prompt/tool states internally.
4. Enable two-pass review internally and inspect churn/block rates.
5. Enable for selected organizations with visible default-on settings.
6. Enable generally.
7. Remove the generic manual-session completion trigger after agent-tool
   coverage is demonstrated.

Kill switches affect execution only and never mutate customer settings.

Step 2 is a hard ordering constraint, not a preference: the server and the
agent-facing client ship in separate artifacts with independent rollout, so any
response-contract change that lands before the client drains breaks in-flight
sandboxes.

## Verification

Backend tests cover defaults, inheritance, stable initiator, automation
precedence, explicit intent, review on/off, independent-agent selection,
atomic clean completion, stale evidence, pass-2 mutation blocking, failures,
idempotency, question/review/existing-PR rejection, agent and automation
no-diff behavior, authorization, and every `org_id` filter.

Specific regressions this design's constraints depend on:

- Storing `create_pr_when_agent_ready = false` at organization scope round-trips
  as `false`, not as absent-resolving-to-`true`.
- An automation with `pre_pr_review_loops` of 1, 3, 4, and 5 produces a
  publication row that satisfies `session_publications_review_passes_check`.
- `create_pr` while a manual review loop is running returns
  `review_in_progress` and leaves a durable pending intent, rather than
  erroring or stranding.
- The publication loop starts once the pre-existing loop terminates.
- Patching `settings.pr_handoff_mode` preserves unrelated repository settings.
- The success body decodes correctly in the *current* `CreatePullRequestResult`
  struct (no `data` wrapper, status `202`), asserted directly against
  `integration.InternalPullRequestCreator`.
- After PR 1, every status code the endpoint returned before PR 1 is still
  returned — a golden-file or table test over the full code set, so the
  additive-only guarantee cannot regress silently before images drain.
- A `403 SESSION_MISMATCH` is still returned for an unauthorized token, not a
  `400`.
- Down-migrating with `source = 'publication'` loops present preserves both the
  loop rows and their `session_review_loop_passes` children, and re-applying
  the up migration afterwards leaves them as non-adoptable `manual` loops.

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
- User-configurable pass counts for agent- and user-initiated publication in
  v1; it is fixed at two. Automations keep their existing configurable
  `pre_pr_review_loops`.
- Direct agent GitHub PR-write access.
- Replacing automation or project publication policy.

## Implementation PRs

Implement in three sequential PRs. The first two ship with execution kill
switches off and no user-visible controls, so each is safe to merge
independently.

### PR 1: Publication intent, policy, and eligibility

Build the durable foundation without changing default product behavior.

Scope:

- Add organization and personal setting types (`*bool` at organization scope
  with `Effective*` accessors), validation, inheritance, and effective-policy
  resolution; absent organization values resolve on.
- Add the non-review `session_publications` metadata (`trigger_kind`,
  `handoff_mode`, initiator, and policy sources), constraints, models, and
  tenant-scoped stores. Defer review-loop links/evidence to PR 2.
- Ship this as an N / N+1 migration pair: columns and constraints `NOT VALID`
  in the first, `VALIDATE CONSTRAINT` in the second. Do not collapse them into
  one file — the migrator runs each file in a single transaction, so a combined
  file holds `ACCESS EXCLUSIVE` across the validation scans.
- Update `integration.InternalPullRequestCreator` to accept the extended flat
  `202` payload and to surface typed non-success statuses instead of
  collapsing every non-`202` into an opaque error string.
- Introduce `PublicationIntentCoordinator` and route agent/UI/API publication
  requests through it.
- Add the new response fields (`publication_id`, `review_loop_id`,
  `pull_request_url`, `reason`) to the existing flat `202` body. This is purely
  additive; **keep returning the current `409`s** (`PR_IN_FLIGHT`,
  `PR_ALREADY_CREATED`, `PR_EXISTS`) so the endpoint stays wire-compatible with
  sandbox images that predate the client update. Retiring them is PR 3.
- Enforce new-change eligibility: reject questions, analysis/review-only
  sessions, existing-PR sessions, read-only sessions, unstable workspaces, and
  empty diffs.
- Add the conditional prompt template and adapter wiring, guarded by the
  automatic-publication kill switch.
- Preserve existing automation/project policy and the legacy manual completion
  trigger while the kill switch is off.
- Add audit events, metrics, and focused model/store/handler/prompt tests.

Acceptance:

- With the kill switch off, production behavior is unchanged. The response
  contract in particular is additive-only: every status code this endpoint
  returns today it still returns, so sandbox images predating this PR are
  unaffected.
- With it on in tests, an eligible implementation can request one durable PR
  publication; ineligible and no-change sessions cannot.
- No review loop or `draft_first` behavior is enabled yet.

### PR 2: Review gate and draft-first lifecycle

Connect durable publication intent to the existing review-loop and publication
state machines.

Scope:

- Add review-loop changeset/revision/SHA evidence, `publication` source, and
  tenant-scoped constraints.
- Add `session_publications` pass-count, loop-link, revision/SHA evidence
  columns, constraints, and index. Review requirement stays encoded in the
  existing `review_gate_state`; no `review_required` boolean is added.
- Ship this half as its own N / N+1 migration pair, on the same split rule as
  PR 1.
- Implement fresh clean-review reuse and atomic
  `loop clean + gate passed + open_pr enqueue`.
- Implement the running-loop resolution rules so a pre-existing manual or
  automation loop parks the intent as `review_in_progress` instead of
  stranding it, and resume when that loop terminates. Do not relax
  `idx_session_review_loops_one_running_per_session`.
- Run the bounded two-pass review/fix cycle for agent- and user-initiated
  publication, honor an automation's `pre_pr_review_loops` when that is the
  trigger, prefer and persist an independent reviewer, and block when the final
  pass changes code.
- Implement the audited draft bypass.
- Add `pre_publish` and `draft_first` repository policy parsing and
  persistence, converting repository settings PATCH to preserve unrelated keys
  rather than replacing the whole blob.
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
- Retire the `409` conflict codes (`PR_IN_FLIGHT`, `PR_ALREADY_CREATED`,
  `PR_EXISTS`) and the four legacy `500`s in favor of idempotent `202`
  responses and `PUBLICATION_INTENT_FAILED`. Gate this on evidence that
  sandbox images predating PR 1's client update have drained; it is the only
  non-additive change to the agent-facing contract in this design.
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
