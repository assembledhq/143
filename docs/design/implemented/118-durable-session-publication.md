# Durable Session Publication

> **Status:** Implemented | **Last reviewed:** 2026-07-16

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
`ASSEMBLED_SESSION_ID` and the legacy `143_SESSION_ID` for other session-aware
tools, but those environment variables do not select PR authorization.

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
  builder-readiness, draft/authorship, and automation-review guards; the
  reconciler never calls the low-level PR creator directly. A failed candidate
  is checkpointed as `retryable_failed`, advancing its `updated_at` so a bounded
  oldest-first batch cannot be monopolized by permanently broken rows.

Migration `000249` seeds `retryable_failed` rows for the historical false
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
