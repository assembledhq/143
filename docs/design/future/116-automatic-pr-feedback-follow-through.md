# Design: Automatic PR Feedback Follow-Through

> **Status:** Not Started | **Last reviewed:** 2026-07-10

> **Depends on:** [overall.md](../overall.md), [PR command continuity](../implemented/76-pr-repair-session-continuity.md), [durable sessions](../implemented/82-durable-session-executors.md), [shared session threads](../implemented/88-shared-sandbox-thread-runtimes.md), [durable webhook ingress](../implemented/100-slack-webhook-ingress-durability.md), [automatic PR repair](../implemented/113-automated-pr-repair-and-readiness.md)

## Decision

143 automatically monitors feedback on open, 143-generated PRs, continues the PR's canonical session, applies requested changes to the existing branch, and responds in every originating GitHub conversation.

Defaults:

- trusted human feedback: on
- private-repository bots: all on
- public-repository bots: GitHub first-party, repository-installed GitHub Apps, and explicitly allowed bots only
- 143's own bot: always ignored
- one active feedback batch per PR
- at most three bot-driven repair cycles by default; organizations may configure a finite limit or unlimited

This is first-class session follow-through, not a default GitHub-triggered Automation. Generic automations create separate sessions and do not preserve complete multi-comment review or reply-thread state.

## Product Contract

1. Signed GitHub feedback is durably recorded before acknowledgement.
2. Policy, author provenance, noise filters, and triage determine eligibility.
3. Comments from one submitted review are batched into one agent turn.
4. Work continues in a dedicated PR feedback thread inside the original session.
5. The workspace must match the current PR head before editing.
6. The automatic agent can edit/test locally but cannot push, merge, create PRs, or post comments.
7. The control plane pushes one branch update when code changed.
8. 143 posts one verified response per source feedback item.
9. Questions may be answered without code changes; acknowledgements do not wake an agent.
10. 143 never merges, approves, dismisses a review, or resolves a review thread automatically.

## Competitive Landscape and 143 Baseline

Competitor behavior validates the user need and exposes useful product tradeoffs; it does not define 143's implementation. Devin is one market reference: it documents automatic PR-comment handling while a session is unarchived, default-on human/CI follow-through, per-PR monitoring, mention-only mode, and bot none/allowlist/all controls. Bot comments default off because repeated rescans can loop. See [GitHub integration](https://docs.devin.ai/integrations/gh), [2024 release notes](https://docs.devin.ai/release-notes/2024), [bot settings](https://docs.devin.ai/product-guides/bot-comment-settings), and [Auto-Fix](https://docs.devin.ai/use-cases/gallery/devin-review-autofix).

GitHub Copilot cloud agent also continues work from PR comments, limits automatic response to users with write access, retains context from prior sessions on the PR, and recommends submitted-review batching so multiple comments trigger one turn. See [Copilot task best practices](https://docs.github.com/en/copilot/using-github-copilot/using-copilot-coding-agent-to-work-on-tasks/best-practices-for-using-copilot-to-work-on-tasks) and [using Copilot cloud agent](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/use-cloud-agent-on-github).

The competitive lessons are continuity, configurable author policy, and explicit loop protection. 143 derives its contract from its own canonical-session architecture and user workflow: eligible bots default on, complete review context stays together, and triage, fingerprints, configurable cross-head budgets, restricted credentials, and kill switches contain risk. Cognition does not publish its backend, so this design assumes none of its internal mechanisms.

143 already has:

- handlers for issue_comment, pull_request_review, and pull_request_review_comment
- pull_requests.session_id as the canonical PR/session link
- review_comments classification and repository-memory learning
- system-authored continuation, PR-head reconstruction, and branch publishing
- shared-workspace session threads
- organization/personal automatic follow-through settings
- automatic conflict/test repair with durable active state and attempt budgets

Missing pieces are durable feedback ingress, conversation identity, bot provenance, batching, canonical-session coordination, push/reply chaining, per-PR controls, and reconciliation.

## Scope and Eligibility

V1 accepts:

- issue_comment created/edited/deleted on a PR
- pull_request_review submitted/dismissed with a body
- pull_request_review_comment created/edited/deleted, including replies
- pull_request_review_thread resolved as a pending-work cancellation signal
- only open pull_requests rows with non-null session_id
- only non-archived canonical sessions

V1 excludes human-created PR takeover, non-PR issues, agent interruption, parallel PR writers, autonomous merge/approval/thread resolution, and arbitrary operational instructions embedded in comments.

Eligibility order:

1. Resolve installation, repository, org, tracked PR, and canonical session.
2. Require open PR and non-archived session.
3. Ignore the 143 App and hidden 143 response markers.
4. Resolve organization, personal, and per-PR monitoring policy.
5. Humans require OWNER, MEMBER, or COLLABORATOR association; other public actors require mention mode.
6. Private repos accept all non-143 bots for triage.
7. Public repos require GitHub first-party, repository-installed App, or explicit-allowlist bot provenance.
8. Reject empty/deleted, resolved-pending, unchanged-body, and same-head duplicate-finding items.
9. Triage into change_request, question, mixed, acknowledgement, or unsafe_or_unsupported.

CI remains sourced from check_run/check_suite and PR health. Bot prose is not a replacement for structured CI state.

## Configuration Contract

Organization settings extend session_automation.automatic_follow_through:

~~~json
{
  "pr_feedback_mode": "all_trusted_humans",
  "pr_feedback_bot_mode": "all",
  "pr_feedback_bot_cycle_limit": 3,
  "pr_feedback_bot_allowlist": []
}
~~~

Human modes:

| Value | Behavior |
| --- | --- |
| all_trusted_humans | Default; process trusted humans |
| mentions | Require the installed 143 App mention |
| off | Capture for audit/learning, but do not acknowledge, run, push, or reply |

Bot modes:

| Value | Behavior |
| --- | --- |
| all | Default; all private-repo bots, provenance-gated public-repo bots |
| allowlist | Only listed canonical bot logins |
| none | Capture but never act |

Absent human and bot modes resolve to all_trusted_humans and all at GA. A pre-GA server flag remains the independent kill switch.

For `pr_feedback_bot_cycle_limit`, an absent field defaults to 3, `null` means unlimited, 0 disables bot-driven cycles, and 1-100 sets a finite limit. Parsing must preserve absent versus explicit `null`; unlimited removes only the cross-head ceiling, not hourly caps, deduplication, pause controls, or the kill switch.

Personal settings add automatic_pr_follow_through.respond_to_pr_feedback with inherit/on/off. Personal on cannot override organization off.

Each pull request has feedback_monitoring with inherit/enabled/disabled. Organization off is authoritative; archiving always pauses new work.

Organization Settings shows human mode, bot mode, optional allowlist, the public-repo provenance rule, and a bot-cycle limit with an Unlimited choice. Account Settings adds the personal inherit/on/off control. Session Overview adds monitoring, queued/running/attention states, and per-PR pause/resume.

## Workflow and UX

~~~text
GitHub webhook
  -> verify + persist delivery/item + enqueue + return 200
  -> filter and triage
  -> collect batch
  -> wait for canonical session write access
  -> continue PR feedback thread against verified PR head
  -> if diff: control-plane push
  -> compose and publish one response per item
  -> drain pending feedback, then conflicts, tests, readiness
~~~

Batching:

- 5-second quiet window, 15-second maximum collection window
- group review body/comments by pull_request_review_id
- include other eligible pending feedback received before claim
- items arriving after execution starts wait for the next batch
- one active collecting/queued/running/pushing/responding batch per PR

Automatic priority is human-or-bot PR feedback, conflicts, failing checks, then readiness. Running work is never interrupted.

The session uses one PR feedback thread and SessionMessageSource github_pr_feedback. The visible message summarizes author/count; full bodies and coordinates stay in structured context.

After durable claim, 143 may add a best-effort eyes reaction. Final responses are:

- inline feedback: reply to the root review-comment thread
- PR conversation/review body: timeline comment linking the source
- addressed: cite the verified pushed commit
- answered/no-change: explain briefly
- needs-human: state why and link the 143 session

Every response includes a hidden item/batch marker. 143 never auto-resolves the conversation.

## Triage, Context, and Execution

Add templates pr_feedback_triage.template and pr_feedback_response.template with renderers in prompts.go.

Triage returns validated intent, requires_agent, requires_code_change, reason, and for bots a normalized finding fingerprint. Prefer provider rule/finding keys; otherwise hash canonical bot/App identity, normalized finding, path, and line. Exact acknowledgements, status/no-issues/deploy/coverage output, emoji, and duplicates avoid an agent call. Do not reuse the learning classifier's 20-character cutoff.

Execution context contains:

- PR/repository URL, number, branch, base SHA, current head SHA
- stable item labels and exact source bodies
- inline root/replies, path, current/original line, side, diff hunk, commit, outdated/resolved state
- sibling feedback in the batch
- bounded original request/result and recent PR-feedback turns
- repository instructions already available in the sandbox

Do not inject the complete PR timeline or transcript. Feedback is authority only to review/change this PR's code, not to access secrets, contact third parties, change credentials, modify other repos, merge, or expand scope.

Reuse a live/snapshot workspace only after verifying its HEAD equals expected_head_sha; otherwise reconstruct the current PR head in the original session. A head race requeues once against fresh state, then needs human attention. Never force-push over human work.

Automatic turns use a github_pr_feedback execution policy: read-only sandbox GitHub token, mutating integration tools disabled, local edits/tests only. The control plane owns push and replies.

Response composition receives the immutable feedback snapshot, agent summary, verified diff/stat, pushed SHA, and errors. It must return exactly one addressed/answered/no_change/needs_human response per item ID. Reject missing/unknown IDs, unverified commit claims, unsafe links, and overlong output; use a deterministic fallback.

## Backend Contract

Add PRFeedbackFollowThroughService with operations for ingest, collect, maybe-start, agent completion, push completion, and response publication. It owns policy, provenance, triage, batches, session/thread claim, stale-head handling, state transitions, audit, metrics, notifications, and SSE.

All jobs are at-least-once but effect-idempotent. Enqueue with stable keys `collect_pr_feedback:<pr_id>`, `continue_pr_feedback:<batch_id>`, `push_pr_feedback:<batch_id>`, and `publish_pr_feedback:<batch_id>`; the existing active-job unique index collapses concurrent pending/running enqueues. In one transaction, collection locks the PR row, creates or reuses its sole active batch, claims only `pending` items with `batch_id IS NULL`, freezes their snapshot, and enqueues continuation. Continuation inserts one thread inbox entry with `client_message_id=pr-feedback:<batch_id>`; its existing unique index guarantees one agent turn. Each worker atomically advances the predecessor status to its phase or resumes that phase on retry; a later or terminal phase is a successful no-op. Batch completion atomically enqueues another collector when unclaimed pending items remain.

Provider identity and publication have separate guards: the item uniqueness constraint absorbs duplicate webhook/reconciliation discovery, and response publication skips a recorded github_response_comment_id or searches for the stable hidden item marker before creating a comment after an ambiguous GitHub result. A retry can resume the incomplete phase but cannot rerun the agent, push, or response for an already-advanced phase.

Extract generic PR-command workspace preparation from StartPullRequestRepair. Do not add review feedback to PullRequestRepairActionType; feedback identity/budgets differ from health repair. The worker command becomes a typed health_repair or review_feedback variant while accepting legacy repair payloads during migration.

Jobs:

- collect_pull_request_feedback
- continue_session with feedback_batch_id and PR command context
- push_pr_changes with optional feedback_batch_id
- publish_pull_request_feedback_responses
- reconcile_pull_request_feedback

Session completion evaluates pending feedback before MaybeStartAutoRepair.

## Database Contract

All stores take orgID and every query filters org_id. Write paths validate that PR, session, thread, batch, item, repository, and integration share the org. These control-plane tables use normal foreign keys. No triggers are required.

~~~sql
ALTER TABLE pull_requests
  ADD COLUMN feedback_monitoring text NOT NULL DEFAULT 'inherit',
  ADD COLUMN feedback_bot_epoch bigint NOT NULL DEFAULT 0,
  ADD COLUMN feedback_bot_cycles_in_epoch integer NOT NULL DEFAULT 0,
  ADD CONSTRAINT chk_pr_feedback_monitoring CHECK (feedback_monitoring IN ('inherit','enabled','disabled')),
  ADD CONSTRAINT chk_pr_feedback_bot_cycles CHECK (feedback_bot_cycles_in_epoch >= 0);

CREATE TABLE pull_request_feedback_batches (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES organizations(id),
  pull_request_id uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
  session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  thread_id uuid REFERENCES session_threads(id) ON DELETE SET NULL,
  status text NOT NULL CHECK (status IN ('collecting','queued','running','pushing',
    'responding','completed','needs_attention','cancelled')),
  source_kind text NOT NULL CHECK (source_kind IN ('human_or_mixed','bot_only')),
  bot_feedback_epoch bigint,
  expected_head_sha text NOT NULL, result_head_sha text,
  workspace_mode text CHECK (workspace_mode IS NULL OR workspace_mode IN ('snapshot_continuation','pr_head_reconstruction')),
  feedback_snapshot jsonb NOT NULL DEFAULT '[]',
  debounce_until timestamptz NOT NULL, max_collect_until timestamptz NOT NULL,
  attempt_count integer NOT NULL DEFAULT 0,
  result_summary text, error_code text, error_detail text,
  started_at timestamptz, completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_pr_feedback_one_active ON pull_request_feedback_batches (pull_request_id)
  WHERE status IN ('collecting','queued','running','pushing','responding');
CREATE INDEX idx_pr_feedback_batches_org_pr ON pull_request_feedback_batches (org_id,pull_request_id,created_at DESC);

CREATE TABLE pull_request_feedback_items (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id uuid NOT NULL REFERENCES organizations(id),
  pull_request_id uuid NOT NULL REFERENCES pull_requests(id) ON DELETE CASCADE,
  batch_id uuid REFERENCES pull_request_feedback_batches(id) ON DELETE SET NULL,
  surface text NOT NULL CHECK (surface IN ('issue_comment','review_body','review_comment')),
  provider_object_id bigint NOT NULL, github_delivery_id text,
  github_review_id bigint, github_thread_root_comment_id bigint,
  in_reply_to_comment_id bigint, github_app_id bigint, github_app_slug text,
  author_login text NOT NULL,
  author_type text NOT NULL CHECK (author_type IN ('User','Bot','Mannequin','Organization','Unknown')),
  author_association text NOT NULL DEFAULT '',
  bot_eligibility_source text NOT NULL DEFAULT '' CHECK (bot_eligibility_source IN
    ('','private_repository_all','github_first_party','repository_installed_app','explicit_allowlist')),
  body text NOT NULL, body_hash text NOT NULL, processed_body_hash text,
  provider_finding_key text, finding_fingerprint text,
  automatic_attempt_count integer NOT NULL DEFAULT 0,
  path text, line integer, side text, diff_hunk text,
  comment_commit_sha text, observed_head_sha text NOT NULL DEFAULT '',
  intent text NOT NULL DEFAULT 'unknown' CHECK (intent IN ('unknown','change_request','question',
    'mixed','acknowledgement','unsafe_or_unsupported')),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ignored','claimed','running',
    'responded','needs_attention','cancelled')),
  ignore_reason text, github_response_comment_id bigint,
  response_body text, response_commit_sha text,
  provider_created_at timestamptz, provider_updated_at timestamptz,
  received_at timestamptz NOT NULL DEFAULT now(),
  processed_at timestamptz, updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (pull_request_id,surface,provider_object_id)
);
CREATE INDEX idx_pr_feedback_items_pending ON pull_request_feedback_items (org_id,pull_request_id,received_at) WHERE status='pending';
CREATE INDEX idx_pr_feedback_items_batch ON pull_request_feedback_items (org_id,batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX idx_pr_feedback_bot_fingerprint ON pull_request_feedback_items (org_id,pull_request_id,finding_fingerprint,observed_head_sha)
  WHERE author_type='Bot' AND finding_fingerprint IS NOT NULL;

ALTER TABLE review_comments ADD COLUMN source_feedback_item_id uuid
  REFERENCES pull_request_feedback_items(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX idx_review_comments_feedback_item ON review_comments (source_feedback_item_id) WHERE source_feedback_item_id IS NOT NULL;
~~~

An eligible human item or explicit retry increments feedback_bot_epoch and resets the bot-cycle counter. Claiming a bot-only batch locks the PR row, atomically checks/increments the counter, and stamps the epoch. Body edits reset item attempts only when body_hash differs from processed_body_hash; historical batches retain immutable snapshots.

Use typed strings with Validate methods for every new enum-like model and table-driven validation tests.

## API and SSE Contract

Existing settings routes carry the new organization/personal fields:

- GET/PATCH /api/v1/settings (PATCH admin-only)
- GET /api/v1/auth/me
- PATCH /api/v1/auth/me/settings

Feedback routes:

| Route | RBAC | Contract |
| --- | --- | --- |
| GET /api/v1/pull-requests/{id}/feedback-follow-through | viewer+ | effective modes/scope, override, pause reason, counts, active batch, 20 recent items |
| PATCH /api/v1/pull-requests/{id}/feedback-follow-through | member/engineer/admin | body monitoring=inherit/enabled/disabled; returns current state |
| POST /api/v1/pull-requests/{id}/feedback-follow-through/{batch}/retry | member/engineer/admin | retry needs_attention batch against current head with human attribution |

The GET response includes effective_mode, effective_bot_mode, effective_bot_cycle_limit (`null` unlimited, 0 disabled), bot_scope, monitoring, paused_reason, pending_count, needs_attention_count, active_batch, and recent_items. bot_scope is server-derived: all_private_repository_bots, installed_or_first_party_public_bots, selected_bots, or none.

Errors use the standard envelope with PR_NOT_LINKED_TO_SESSION, PR_FEEDBACK_DISABLED, PR_FEEDBACK_BATCH_ACTIVE, PR_FEEDBACK_NOT_RETRYABLE, PR_HEAD_CHANGED, and GITHUB_PERMISSION_MISSING.

After durable changes publish:

~~~text
event: pull_request.feedback.updated
data: {"pull_request_id":"uuid","batch_id":"uuid","status":"running","pending_count":2}
~~~

Clients invalidate only matching feedback, PR-health, and session queries.

## GitHub, Durability, and Safety

Subscribe to issue_comment, pull_request_review, pull_request_review_comment, and pull_request_review_thread. Verify Pull Requests write, Contents write for the control plane, and subscriptions during installation health. The sandbox receives only a restricted read token.

Ingress transaction:

1. Verify HMAC-SHA256 and resolve installation/repository/org.
2. Insert webhook_deliveries using X-GitHub-Delivery.
3. Upsert normalized item and enqueue collection in the same transaction.
4. Return 200; all GitHub reads, triage, agent, push, and replies are async.

GitHub does not automatically redeliver failed webhooks. Reconcile recently updated monitored PRs during PR-state sync and with a low-frequency cursor job listing issue comments, reviews, and review comments.

Safety and failure rules:

- one attempt per item/body hash
- same bot fingerprint on the same head is ignored even with a new comment ID
- same finding on a new 143 head may consume another bot cycle
- maximum five batches or 30 items per PR/hour
- maximum effective_bot_cycle_limit bot-only cycles across 143-created heads per feedback epoch; `null` is unlimited and 0 permits none
- only human feedback or explicit human retry resets the bot epoch
- head race retries once; second race needs attention
- close cancels collecting/queued work; running work cannot push/reply as addressed
- archive/disable pauses new work without cancelling a running batch
- push failure cannot produce an addressed-commit claim
- reply retries search hidden markers before create, preventing duplicates
- GitHub secondary limits use bounded backoff in responding state
- reply failure never reruns agent/push
- item text, diff hunks, context, and response sizes are bounded
- public bot provenance and all provider URLs are verified server-side
- audit records policy/provenance, item/batch/session/thread, heads, outcome, and response IDs

## Rollout, Metrics, and Tests

Rollout:

1. Shadow: durable capture, learning projection, policy/triage decisions, reconciliation.
2. Internal: mention-only humans plus all private-repo bots; restricted execution and push/reply.
3. Internal default-on: human all mode and bot all mode; separately verify public provenance.
4. GA: absent modes resolve on/all; notify orgs; retain server kill switch and optional narrowing.

Gate GA on low false-action, revert/regret, duplicate-run, reply-failure, pause, and cycle-exhaustion rates.

Measure webhook-to-ingest, ingest-to-start/reply, ignored reasons, batch size, triage intent, fingerprints deduped, bot cycles, stale heads, agent/push/reply outcomes, cost/item, reconciliation recovery, pauses, human follow-ups, and reverts. Logs carry org, PR, item, batch, session, thread, delivery, request, and trace IDs.

Required tests:

- exact normalized mapping and org scoping for every event/store
- signature, delivery, provider-object, active-batch, stable-job-key, phase-transition, response-marker, and reconciliation idempotency
- all policy combinations, private/public bot eligibility, and self/noise filters
- missing settings resolve human on, bot all, and cycle limit 3; test `null` unlimited and 0 disabled
- concurrent collectors claim each item once; job replays resume/no-op by phase; batching defers late arrivals
- questions can reply without push; change batches use the canonical thread
- workspace/head verification, reconstruction, human-push race, and no force-push
- fingerprint dedupe, atomic default-three/configured/unlimited cross-head budget, and human reset
- one push and response per item; push/reply failures never repeat agent work
- close/archive/disable/hourly-cap behavior
- settings and Overview UI, role gating, transcript source, and targeted SSE invalidation
- end-to-end multi-comment human review, private bot rescan, public untrusted bot, and missed webhook recovery

Backend tests follow table-driven, require, exact-value/message, and t.Parallel conventions. Frontend uses focused Vitest/Testing Library/MSW coverage.

## Engineering Implementation Spec

Implement as four sequential, independently deployable PRs. Keep the server kill switch off until Phase 4; each phase must include focused tests, `go vet` for touched Go packages, and `make lint-tenancy` where stores or migrations change.

### Phase 1 — Contracts and persistence

- Implement the next migration, typed enums/models in `internal/models/pr_feedback.go`, settings changes in `org_settings.go`/`user_settings.go`, and `internal/db/pull_request_feedback.go`. Use a custom nullable setting type so absent, `null`, 0, and positive limits remain distinct; every store method takes `orgID` and every SQL query filters it.
- Test migration up/down, enum and setting validation/defaults, exact store values, foreign keys, active-batch/item uniqueness, compare-and-swap transitions, and tenant isolation.
- Done when settings round-trip without workers reading them and stores can upsert an item, claim one batch transactionally, advance phases, and project one `review_comments` row.

### Phase 2 — Durable ingress, policy, and batching

- Extend `internal/api/handlers/webhooks.go`; add `internal/services/github/pr_feedback.go`, the two prompt templates/renderers, and collect/reconcile registrations in `internal/worker/handlers.go`. Reuse `WebhookDeliveryStore`; acknowledge only after delivery/item/job commit, and run in shadow mode with no agent, push, reaction, or reply.
- Test every event/action mapping, signature and redelivery handling, edited/deleted/resolved items, self/public/private bot provenance, triage/fingerprints, submitted-review batching, stable job keys, concurrent collectors, caps, and cycle reset/limit semantics.
- Done when shadow metrics explain every ignored/eligible item, reconciliation restores a missed event, and concurrent/redelivered inputs still produce one normalized item and one active batch.

### Phase 3 — Canonical-session execution and GitHub write-back

- Extract workspace preparation from `pr_health_service.go`; add the feedback command variant and handlers in `internal/worker/handlers.go`. Deliver one inbox entry keyed `pr-feedback:<batch_id>`, enforce the restricted execution policy, verify/reconstruct head, then let the control plane push and publish marker-protected per-item responses.
- Test one agent turn per batch, question-only replies, multi-comment changes, snapshot/reconstruction paths, human/head races, no force-push, no sandbox mutations, push/no-change/failure outcomes, ambiguous reply recovery, and phase replay after worker failure.
- Done when an internal fixture review updates the existing branch and each source thread exactly once, while any stale head, failed push, or unsafe request produces no false addressed claim.

### Phase 4 — Product surface, recovery, and rollout

- Add routes/RBAC in `internal/api/handlers/pull_requests.go` and `router.go`, Redis/SSE support in `internal/cache/pull_request_streams.go`, frontend types/API hooks, Organization and Account settings controls, and Session Overview monitoring/pause/retry states. Add reconciliation scheduling, audit events, metrics, alerts, kill switch, and rollout cohort controls.
- Test handler envelopes and roles, SSE scoping, React/MSW interactions, default/null/zero settings, pause/resume/retry, close/archive behavior, reconciliation, rate limits, and end-to-end human/private-bot/public-bot flows.
- Done when shadow and internal gates meet the metrics above, operators can pause globally or per PR, the UI explains every state, and GA default-on is a configuration change rather than a deployment.

This design supersedes the child revision-run auto-apply flow in [review feedback loop](../backlog/11-review-feedback-loop.md).
