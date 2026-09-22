# Resumable actions for automations

## Contract

Every automation can opt into durable external actions, whether it runs on a schedule,
is started manually, responds to an event, or continues an existing target session.
Organization admins enable the `automation_actions` capability on an automation (not session defaults) and select individual operations and
their destinations. Supported primitives are GitHub PR labels, team review requests,
PR comments, Notion pages in a configured data source, and Slack channel messages.
There is no design-review verdict, required bundle, fixed ordering, or dependency on
all three providers. Provider credentials remain on the server.

The agent calls `143-tools automation execute-action --file request.json` once per
action. The request contains `operation_key`, `action_key`, `kind`, and the content
for that kind. GitHub operations also require `pr_number` and the current `head_sha`.
Notion properties are typed and constrained to the configured property allowlist.
Slack and GitHub comment text is supplied directly, without design-review formatting.

An operation key identifies a logical workflow, such as `daily-report:2026-09-22`.
An action key identifies a step within it, such as `notify-support`. Multiple steps
may use the same provider operation. Keys are stable across job retries, runs, and
session reconstruction. Per-target sessions add a server-derived target namespace;
other automation runs share an automation-and-repository namespace. Separate occurrences use new
operation keys. The service never guesses workflow completion from an expected
number of steps.

`automation action-status --operation_key KEY` lists recorded steps. Resumption uses
`automation execute-action --resume --operation_key KEY --action_key KEY` and the
stored request. Completed steps return their existing receipts with `reused: true`,
creation/completion timestamps, and originating/last sending run IDs. Only pending steps
and definite provider rejections can be retried. Concurrent claims cannot send the
same step twice. Changed content or destinations under an existing key are rejected.
An uncertain send or expired sending lease becomes `unknown`; it requires provider
reconciliation and is never automatically resent. A late receipt with the original
send token may settle it. The API executes one bounded send per request; no worker
or background retry service is added. Insufficient request time leaves the step
pending and returns `BUDGET_EXHAUSTED` before starting a send.

## Authority

Tokens bind organization, repository, session, thread, automation run, job, and
attempt lock token. Each claim rechecks the running job lease, run/session membership,
current enabled automation policy, and the run's capability snapshot. Ordinary runs
must still be running; human continuations of completed runs cannot send actions. Per-target
runs additionally require the active target generation and current executing turn.
Merged targets permit only their admitted final merge-event turn; earlier turns
remain blocked. Unbound Slack/Notion actions can run there, while PR-conditioned
actions still require a live open PR. Checkpoint recovery refreshes tokens using
the stored executing thread identity.
Historical status can read receipts after a run ends but never authorizes a send.
GitHub writes are restricted to the session repository and configured repository,
and require a live open-PR/head check immediately before claiming. Per-target actions
cannot select another PR. Slack/Notion may omit PR preconditions in any run mode,
so pending steps can resume after a push. Explicit PR/head preconditions remain
frozen and enforced; stale commit-specific content cannot be sent after a push.
Revoking a grant or changing its configuration blocks old attempts; reordering the
selected actions is a semantic no-op. Receipt persistence remains possible after revocation.

The table has tenant filters and durable foreign keys. Identity is
`(org_id, automation_id, scope_key, operation_key, action_key)`; kind is not an
identity key. A transaction serializes reservation and claims on the automation.
Payload, destination, and digest freeze at reservation. The database clock sets send
deadlines. Rollback refuses to discard existing receipts.

## Product integration and acceptance checklist

- [x] Generic configuration validates only selected kinds and relevant destinations.
- [x] Every existing automation run mode can mint a lease-bound action token.
- [x] Continuous sessions refresh tokens and keep the read allowlist plus configured actions.
- [x] Independently execute Slack, Notion, and GitHub actions; repeat kinds with distinct keys.
- [x] Reuse receipts and resume safe work across runs and reconstructed generations.
- [x] Reject stale attempts, revoked grants, other tenants/targets, and changed payloads.
- [x] Unknown sends stay blocked while unrelated actions remain callable.
- [x] UI, CLI, tool schemas, and prompt guidance describe the generic contract.
- [x] Own-app comment webhooks match durable receipts without retriggering their automation.
- [x] Focused regression tests, real PostgreSQL fencing tests, and local static checks pass.

These PRs replace the unshipped design-review-specific API and migration. No deployed
client compatibility layer or production configuration change is required. Existing
automations remain opt-out. Arbitrary HTTP calls and unsupported provider operations
do not gain durable execution through this capability.

## Verification evidence

- `internal/db/automation_actions_postgres_test.go`: concurrent reservations/claims,
  tenant and attempt fencing, later per-run execution, continuous turns, reconstruction,
  uncertainty/late receipts, own-comment matching, and rollback retention; passed on
  local PostgreSQL with the race detector.
- `internal/services/automationactions`: independent actions, repeated kinds, frozen
  content, live-head checks, safe retries, provider request shapes, and uncertain
  outcomes; tests with race detection passed (81.6% package coverage).
- All affected backend packages passed. Go vet, golangci-lint 2.10.1, gosec 2.22.4,
  and tenancy lints passed locally. The configuration component's nine tests, targeted
  ESLint, TypeScript checking, and the production frontend/docs build passed.
- Hosted validation is tracked on the final heads of PRs #2167 and #2168. No live
  GitHub/Notion/Slack write canary or production enablement has been performed.
