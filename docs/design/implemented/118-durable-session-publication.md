# Durable Session Publication

> **Status:** Implemented | **Last reviewed:** 2026-08-04

## Problem

Branch publication and pull-request creation cross several independently
fallible boundaries: a sandbox push, GitHub's PR API, the local
`pull_requests` insert, session lifecycle updates, and webhook delivery. The
legacy `session_publish_state.pr_creation_state` was designed for button
feedback, not as an operation ledger. It could say `failed` even when GitHub
already contained the desired branch and PR.

The production incident that motivated this design had two coupled causes:

1. An automation agent called `143-tools pr create` without a session ID. The
   CLI required caller-supplied identity even though the signed internal token
   already contained the authoritative session.
2. After a direct GitHub PR create, the backend replay restored a branch whose
   `HEAD` already equaled its upstream. The push script classified that
   idempotent state as `ErrNoChanges` before looking up the existing PR, so no
   local PR row was recorded and the webhook treated the PR as unknown.

The durable design makes publication one server-owned, idempotent operation
used by people, automations, and sandbox tools.

## Contracts

### Authoritative identity

`POST /api/v1/internal/session/pr` derives the session, organization, and
repository exclusively from the signed internal token. The legacy
`/sessions/{session_id}/pr` route remains compatible, but any explicit ID must
match the token. `143-tools pr create` uses the token-scoped route unless the
caller explicitly supplies `session_id`. Sandboxes still receive
`CODING_SESSION_ID` for other session-aware tools, but that contextual
environment variable does not select PR authorization.

Caller-supplied session IDs are therefore hints for compatibility, never the
authorization source.

### One publication owner

The backend owns PR creation. Sandbox agents request it through `143-tools pr
create`; they do not receive a token capable of calling GitHub's PR-write API.
The sandbox auth socket issues repository-bound GitHub App tokens per action:

| Action | Requested GitHub App permissions |
| --- | --- |
| `push` | `contents: write`, `workflows: write` |
| `api` | `contents: read`, `pull_requests: read` |

Neither token has `pull_requests: write`. User-to-server GitHub credentials are
kept out of the sandbox. When a human triggered the session, their user record
is still attached to the resolution so commit author/co-author attribution is
preserved. Token issuance fails closed if repository identity or the scoped
issuer is unavailable.

Existing PR title/body updates follow the same ownership boundary. Agents use
`143-tools pr update`, which calls a token-scoped internal API and updates the
current session's primary PR with a server-held installation token. The
sandbox never receives `pull_requests: write`; the handler verifies tenant,
repository, session, and writable-thread identity, preserves the durable
publication marker and 143 preview footer in replacement descriptions, refreshes the local GitHub
snapshot, and emits an agent audit event. Substantial descriptions can be read
from a sandbox file with `--body-file`, avoiding shell-quoting limits without
making arbitrary host files visible to the API server.

### Existing PR metadata update contract

This capability requires no schema or migration change. It reuses the
tenant-scoped `sessions`, `session_threads`, `repositories`, `pull_requests`,
and `audit_logs` records. The successful GitHub response refreshes the existing
`pull_requests` title, body, URL, head, and base snapshot fields; it does not
create a second PR or publication ledger row.

The internal API exposes both token-scoped and compatibility routes:

- `PATCH /api/v1/internal/session/pr`
- `PATCH /api/v1/internal/sessions/{sessionID}/pr`

Both require the short-lived internal bearer token issued to a session. The
path or optional request `session_id`, when present, must equal the signed
claim. Read-only filesystem threads, review execution threads, repository
mismatches, repo-only tokens, and automation-goal-improvement sessions fail
closed. The JSON request accepts optional `title` and `body` string fields and
requires at least one; `title` cannot be blank and is capped by the existing PR
title limit. `body_file` is a CLI-only flag: `143-tools` reads it inside the
sandbox and sends the resulting `body` string.

A successful synchronous update returns HTTP 200:

```json
{
  "status": "updated",
  "session_id": "<uuid>",
  "pull_request_id": "<uuid>",
  "pull_request_number": 2068,
  "pull_request_url": "https://github.com/owner/repo/pull/2068",
  "title": "Updated title"
}
```

Stable errors include `UNAUTHORIZED`, `SESSION_MISMATCH`, `REPO_MISMATCH`,
`TOOL_NOT_AVAILABLE`, `INVALID_BODY`, `INVALID_TITLE`,
`PULL_REQUEST_NOT_FOUND`, `GITHUB_APP_NOT_CONFIGURED`,
`GITHUB_PERMISSION_MISSING`, and `PULL_REQUEST_UPDATE_FAILED`. A GitHub 403
with `Resource not accessible by integration` maps to
`GITHUB_PERMISSION_MISSING` and tells operators to grant **Pull requests: Read
& Write** and have an organization owner approve the installation permission
change.

### Idempotent branch semantics

Publication distinguishes three successful branch outcomes:

- `created_remote_branch`
- `updated_remote_branch`
- `already_at_desired_head`

`HEAD == upstream` is success, not “no changes.” The special no-change exit is
reserved for an empty replay against the current base. After a successful push
or an already-current branch, the service always runs PR discovery before it
can conclude that no PR exists.

## Durable state

`session_publications` is the operation ledger. It is tenant scoped and unique
on `(org_id, changeset_id)`, so retries converge on one publication operation
per PR target.

```text
requested
  -> review_pending
  -> ready_to_publish
  -> branch_published
  -> pr_resolved
  -> recorded
  -> completed

retryable_failed -> requested
requested/ready_to_publish -> completed_noop
any nonterminal state -> terminal_failed
```

The row stores source, review-gate state, original `open_pr` queue and JSON
payload, a monotonic request-generation timestamp, base/head branches, desired
and published SHAs, GitHub PR number/URL, attempts, error code/message, and
checkpoint timestamps. The first nonempty
request payload wins (except that a real caller supersedes synthesized
backfill/reconciler intent) so review-loop and reconciler retries cannot erase
caller choices such as draft mode, author mode, changeset identity, or
merge-when-ready. Terminal states are `completed`, `completed_noop`, and
`terminal_failed`. Replayed jobs call `StartAttempt`; a terminal row returns
“not started” so the worker performs no GitHub side effects. A `completed`
replay still reloads the recorded PR and finishes the worker-owned local
post-publication phase (action-state convergence, merge-when-ready, and
deduplicated notifications). A strictly newer explicit request reopens
`completed_noop` and `terminal_failed` as a fresh `requested` generation; its
timestamp comes from the durable job's original enqueue time, so retries of the
same job cannot reopen terminal work and an older delayed job cannot supersede
a newer request. The prior checkpoints and caller payload are reset even when
the mutable changeset retains the same stored head SHA, which lets continued
snapshot-backed sessions and corrected terminal failures publish successfully.
`completed` is never reopened because its PR is already the durable result.

The source enum (`user`, `automation`, `agent_tool`, `backend`, `webhook`,
`reconciler`, `backfill`) makes entry-point drift visible without changing the
state machine. Automation publication also persists its review gate. The
worker must move the gate to `passed` before normal automation publication;
an out-of-band PR recovered from a webhook remains visibly `pending` and emits
a warning because it bypassed the intended gate.

`session_publish_state` remains the compatibility/UI action lifecycle. It is
not the publication source of truth.

## Checkpoint and idempotency strategy

The service checkpoints immediately after each external or durable side
effect:

1. Ensure the publication request.
2. Publish or verify the remote branch; immediately checkpoint the changeset's
   expected remote SHA, then record publication `branch_published` and SHA.
3. Add a hidden identity marker to the PR body:
   `<!-- 143-publication session=<uuid> changeset=<uuid> -->`.
4. Create or find the open PR by exact head branch; record `pr_resolved`.
5. Insert or associate the local `pull_requests` row; record `recorded`.
6. Finish session/changeset side effects; record `completed`.

The GitHub `(org_id, github_repo, github_pr_number)` uniqueness race is
handled by adopting the existing row when a webhook wins the insert. A second
unique invariant on `(org_id, changeset_id)` prevents one changeset from being
attached to multiple PRs. PR body markers are not trusted alone: webhook
recovery re-resolves the org-scoped repository, changeset, session, and exact
same-repository working branch before association. Missing head-repository
identity fails closed. Recovered PR authorship comes from GitHub's PR author
object, so webhook and periodic recovery classify human and App-authored PRs
consistently for author-based review policy. For older PRs without a marker, an
exact owned `143/` working branch can be used.

## Recovery

Recovery has two independent paths:

- **Webhook convergence:** an opened, reopened, ready-for-review, or synchronize
  event for an unknown or unowned local PR attempts marker/branch association,
  records all publication checkpoints, and updates the session to `pr_created`.
  Adoption refreshes the immutable GitHub authorship classification as well as
  ownership, so an earlier code-review mirror cannot leave an App-authored PR
  classified as user-authored.
- **Periodic reconciliation:** the PR-state reconciler scans stale
  nonterminal publication states whose review gate permits progress, plus
  blocked rows that already have a local PR to finish recording. This keeps
  permanently blocked review rows from starving the bounded oldest-first
  batch. It adopts a local PR before evaluating the review gate, so a
  webhook/worker crash after local association still converges. When no local
  or GitHub PR exists, it re-enqueues the exact stored request payload on its
  original queue. The normal `open_pr` worker then re-runs snapshot quiescence,
  builder-review, draft/authorship, and automation-review guards; the
  reconciler never calls the low-level PR creator directly. A failed candidate
  is checkpointed as `retryable_failed`, advancing its `updated_at` so a bounded
  oldest-first batch cannot be monopolized by permanently broken rows.

Migration `000252` seeds `retryable_failed` rows for the historical false
“No changes” signature: a primary changeset with a persisted diff and working
branch whose legacy PR action failed with a no-changes message. Reconciliation
still validates GitHub state before associating anything.

## Product and operations visibility

Session detail responses include their publication rows. The session header
shows review, branch, retry, failure, no-op, and completion states and polls
while a publication is nonterminal. This avoids a stuck “Create PR” surface
when recovery is active behind the legacy action state.

Structured logs carry `session_id`, `changeset_id`/`publication_id`, state,
head SHA, PR number, and branch outcome. OpenTelemetry emits bounded-cardinality
metrics:

- `session_publication.transitions` by `state` and `source`
- `session_publication.reconciliations` by `outcome`

Useful production checks:

```sql
SELECT state, source, count(*)
FROM session_publications
WHERE updated_at > now() - interval '24 hours'
GROUP BY state, source
ORDER BY state, source;
```

```sql
SELECT id, session_id, changeset_id, state, attempt_count,
       last_error_code, updated_at
FROM session_publications
WHERE state IN (
    'requested', 'review_pending', 'ready_to_publish', 'branch_published',
    'pr_resolved', 'recorded', 'retryable_failed'
)
  AND updated_at < now() - interval '10 minutes'
ORDER BY updated_at;
```

Alert on a sustained reconciliation error rate, growth in stale nonterminal
rows, or `terminal_failed` spikes by source. IDs belong in logs and traces, not
metric labels.

## Pull-request breakout

The implementation should land as four pull requests. Each PR is independently
deployable, keeps the legacy publication path working until the durable path is
ready, and leaves schema changes additive until the final rollout.

### PR 1 — Make publication identity and branch outcomes authoritative

**Purpose:** remove the two ambiguities that caused the incident without yet
changing the publication owner or persistence model.

- Add the token-scoped `POST /api/v1/internal/session/pr` route and make the
  explicit-session route reject an ID that does not match the signed token.
- Change `143-tools pr create` to use the token-scoped route by default while
  retaining explicit `session_id` compatibility.
- Split sandbox installation-token permissions by action: write access for
  branch pushes and read-only access for GitHub API/PR discovery. Remove
  `pull_requests: write` from sandbox-issued tokens and preserve the initiating
  user's attribution separately.
- Return typed branch outcomes (`created_remote_branch`,
  `updated_remote_branch`, and `already_at_desired_head`) and treat
  `HEAD == upstream` as success. Keep the true empty-replay path as the only
  no-op result.
- Add focused handler, CLI, sandbox-auth, identity, and branch-publish tests.

This PR changes no publication tables and can be rolled back without data
migration. Its branch outcome contract is the prerequisite for PR 2.

### PR 2 — Add the durable ledger and make the worker use it

**Purpose:** establish one idempotent, server-owned publication operation for
the normal PR-creation path.

- Add the tenant-scoped `session_publications` table, typed model enums, store,
  transition validation, uniqueness on `(org_id, changeset_id)`, request
  generation, payload-first-write behavior, and checkpoint timestamps.
- Introduce the publication service that ensures a request, starts an attempt,
  publishes or verifies the branch, discovers or creates the GitHub PR,
  records the local PR, and completes the session/changeset side effects.
- Add the hidden session/changeset marker and exact-head PR lookup. Adopt a
  local row when the webhook wins the GitHub-number uniqueness race, while
  enforcing one PR per changeset.
- Route user, automation, review-loop, and agent-tool `open_pr` jobs through
  the service. Preserve the original queue and full request payload, and keep
  `session_publish_state` as the UI compatibility lifecycle.
- Cover state transitions, generation ordering, tenant isolation, checkpoint
  ordering, replay after every checkpoint, PR uniqueness races, guarded worker
  payloads, and stack/queue affinity.

The worker should write the ledger for all new requests after deploy, but the
legacy UI remains usable. Recovery scanning and historical backfill wait for
PR 3 so this change has a bounded operational surface.

### PR 3 — Converge webhooks, retries, and historical failures

**Purpose:** make partially completed and out-of-band publication repair
itself.

- Teach opened, reopened, ready-for-review, and synchronize webhooks to recover
  unknown or unowned PRs using a validated marker or an exact owned `143/`
  branch. Fail closed on repository, org, session, changeset, or head-repository
  mismatches.
- Add the bounded oldest-first publication reconciler. It adopts a local PR
  before checking the review gate, re-enqueues the stored request on its
  original queue, and advances failed candidates so one bad row cannot starve
  the batch.
- Implement `retryable_failed`, terminal replay short-circuiting, completion
  resumption, and strictly newer explicit-request reopening for
  `completed_noop` and `terminal_failed`.
- Add the historical false-“No changes” backfill migration and require
  reconciliation to verify current GitHub state before associating a PR.
- Test malformed and cross-tenant markers, authorship recovery, review-gate
  bypass visibility, stale-row fairness, remote-already-at-HEAD retries,
  completed post-publication resumption, and backfill selection.

Deploy the migration before enabling the reconciler. Start with a conservative
batch size and confirm that stale nonterminal counts converge without a rise in
GitHub API or terminal-failure errors.

### PR 4 — Expose status and close the operational loop

**Purpose:** make durable publication understandable to users and operators,
then retire reliance on legacy failure text.

- Include publication rows in session detail responses and add typed frontend
  response models.
- Render review-pending, publishing, retrying, failed, no-op, and completed
  states in the session header; poll only while a publication is nonterminal.
- Emit structured checkpoint/reconciliation logs and the bounded-cardinality
  transition and reconciliation metrics described above.
- Add the production queries and alerting for stale nonterminal rows,
  reconciliation errors, and terminal failures. Update public agent-tool and
  review/ship documentation for the server-owned flow.
- Add API response, frontend rendering/polling, and metric-label tests, plus a
  deployment smoke test that retries one already-published branch and observes
  the existing PR become locally recorded.

After this PR has been observed in production, `session_publish_state` can
remain as an action-state compatibility projection, but it must no longer be
used for recovery decisions or incident diagnosis.

## Verification

The regression suite covers:

- token-scoped current-session routing and explicit-ID compatibility;
- repository/action permission bodies for sandbox installation tokens;
- fail-closed sandbox identity and preserved human commit attribution;
- `HEAD == upstream` as a successful branch publication;
- branch-push retry recovery when the remote already equals local `HEAD`;
- publication marker round trips and malformed-marker rejection;
- exact webhook branch ownership and one-PR-per-changeset enforcement;
- original guarded-worker payload and queue replay;
- per-changeset stack replay intent and agent-queue affinity;
- completed post-publication resumption, explicit no-op/terminal generation
  reopening, and
  tenant-scoped publication reads;
- deterministic recovered authorship, required-local-checkpoint ordering, and
  fair failed-candidate rotation;
- webhook/store association and PR uniqueness recovery paths;
- model enum validation and migration tenancy checks.

## Related

- [40-pr-creation-revamp.md](40-pr-creation-revamp.md)
- [61-pr-state-sync-and-repair-actions.md](61-pr-state-sync-and-repair-actions.md)
- [78-review-agent-loops.md](78-review-agent-loops.md)
- [85-pr-lifecycle-action-states.md](85-pr-lifecycle-action-states.md)
- [109-sandbox-auth-socket-ownership.md](109-sandbox-auth-socket-ownership.md)
