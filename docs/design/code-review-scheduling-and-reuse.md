# Design: Code Review Scheduling, Reuse, and Usage Controls

> **Status:** Partially Implemented | **Last reviewed:** 2026-09-10

Stage 1 is implemented locally behind a disabled rollout switch. No production configuration or rollout has changed. The product direction was agreed in discussion; numeric limits below are proposed starting values, not measured capacity recommendations.

## Purpose

Reduce account exhaustion caused by repeated code reviews while continuing to review **each pull request when it is ready**. A push, restack, or repeated request should first cause a cheap decision about whether review is needed. Start agents only when existing evidence does not cover the current code and relevant context, the PR has settled, and capacity is available.

Administrators manage scheduling and usage limits in **Code reviews → Policy → Review scheduling and usage**. Developers see why work is waiting, when it can run, and which previous assessment is being reused. Every PR retains its own review and audit history.

Success means fewer agent executions and fewer tokens per completed useful review, with bounded waiting and no reuse of stale or incomplete evidence. A held request is pending work, never an approval.

## Progress

Current delivery unit: Stage 1. Later reuse, resource accounting, and post-approval activation remain deferred.

- [x] Presence-aware policy persistence, compatible PATCH/restore, and autosave.
- [x] Durable PR scheduling state, requests, wake/reconciliation, and lease/race handling.
- [x] Common admission for webhooks, manual requests, retries, and worker dispatch.
- [x] Draft/closed/pause transitions, Review now, timing controls, and visible pending work.
- [x] Focused behavior tests, real PostgreSQL concurrency/lifecycle proof, and automated UI verification.
- [x] Adversarial gap audit: cancellation after commit, stranded active metadata, missed draft/base events, duplicate request lineage, and provider outage recovery.
- [ ] Native Chrome UI proof, remote CI, and pilot rollout.

- [x] Trace current automatic reassessment, explicit requests, cancellation, policy settings, and UI controls.
- [x] Confirm the desired workflow: review each ready PR; suppress repeated work during pushes and restacks.
- [x] Include UI configuration and per-PR explanations in implementation scope.
- [x] Draft delivery stages, proposed contracts, rollout gates, and acceptance criteria.
- [x] Reconcile Claude's review with the implementation and tighten scheduling, reader migration, settings compatibility, and capacity contracts.
- [ ] Establish a production baseline and verify execution-to-credential usage attribution.
- [x] Deliver durable scheduling, timing controls, and request consolidation locally; rollout remains pending.
- [ ] Deliver conservative content reuse and revision-bound publication.
- [ ] Deliver review capacity and usage budgets through the same admission path.
- [ ] Evaluate and deliver evidence-only reassessment where coverage can be preserved.
- [ ] Complete pilot validation, public documentation, and rollout.

## Decisions and Scope

1. Review every ready PR independently. Do not restrict reviews to the bottom of a stack, automatically replace constituent reviews with a combined review, or infer readiness from a branch-name pattern.
2. Keep the existing requirement that review is requested before automatic monitoring starts. Do not turn all repository webhook traffic into automatic review.
3. Propose a **five-minute quiet window** after the latest meaningful update and a **15-minute minimum interval between automatic review starts per PR**. An explicit first review or **Review now** bypasses timing delays, subject to eligibility and capacity.
4. Consolidate ordinary equivalent requests. A separate **Force fresh review** action intentionally bypasses content reuse, records a reason, and still respects authorization and resource limits.
5. Content equivalence and request consolidation are built-in behavior. Do not expose hash algorithms or a collection of cache toggles in the product UI.
6. Preserve approval criteria, reviewer quorum, evidence requirements, and author/fork safeguards. Changing scheduling must not silently loosen them.
7. Reuse analysis separately from publishing an approval for a revision. Historical approval is not evidence that later code is covered.
8. Keep all settings organization-wide initially, matching the existing review policy. Per-repository overrides and stack topology analysis are outside the first release.
9. Defer broad incremental code review, model-count reduction, and line-count exemptions. Their quality tradeoffs require separate calibration.

## Stage 1 Implementation Record

Local implementation dated 2026-09-10:

- `internal/services/codereview/scheduling.go` owns authoritative GitHub observations, one durable pending intent per PR, timing, pause/draft/close admission, request identity, and transactional session/job creation. Workers refresh eligibility before reviewer fan-out and publication. Stage 1 remains conservative about changed SHAs and bases.
- Migration `000289_code_review_scheduling` adds scheduling policy JSON and the PR-state/request tables. `internal/db/code_review_scheduling.go` serializes writers with an org/PR advisory lock. Wake completion/rescheduling uses that same lock and worker lease. A bounded cluster sweep repairs lost/dead-lettered wakes and unfinished cancellation after process failure, including closed PRs without pending input.
- Disputes retain dedicated jobs and separate provenance while acquiring the same PR lock. Failed-attempt retries retain their existing validation and immutable replacement links; request redelivery returns its recorded replacement. Pending work cannot dispatch while another starter or reviewer thread is active; abandoned metadata alone does not block forever.
- PATCH policy saves use a version fence and merge only changed fields. Legacy PUT/internal writes and historical restore preserve omitted scheduling settings. New policy versions materialize effective scheduling values so an explicit reset remains distinguishable from pre-migration history. Existing organizations retain true/60/0 defaults. Recommended 300/900 values require the admin action in this slice; automatic adoption for first-ever policies is deferred to pilot/GA rollout.
- The 143 UI exposes **Policy → Safeguards → Review scheduling**, **Review now** on executed review rows, and a separate **Scheduled reviews** queue with pause/resume and wait reasons. Viewers can read the queue; admins/members can request or pause. Request identity survives an uncertain HTTP response while the control remains mounted.

Stage 1 API details intentionally precede the broader assessment API proposed below:

| Route | Implemented Stage 1 response/query |
| --- | --- |
| `GET /api/v1/code-review-policies` | Existing resolved config plus `capabilities.scheduling`; omitted scheduling fields resolve true/60/0. |
| `PATCH /api/v1/code-review-policies` | `{config, expected_version, source}`; returns `{data: <saved policy record>}`. |
| `GET /api/v1/code-review-targets` | Pending queue only; `repository_id`, UUID `cursor`, `limit` 1–100, stable scheduling-row UUID order. Returns `{data: [{schedule, title, github_pr_url, github_pr_number, github_repo}], meta: {next_cursor}}`. Existing attempt filters remain unchanged and apply only to the executed review list. The full PR-centric current view and its filter-bound cursor migrate together in Stage 2. |
| `GET/PATCH /api/v1/pull-requests/{id}/code-review` | `{data: <CodeReviewPRState>}`. PATCH accepts `{automatic_paused}`. Pending raw intent and tenant identifiers are never exposed. Completed/failed session status is projected when reading a dispatched scheduling row. |
| `POST /api/v1/pull-requests/{id}/code-review/requests` | `{request_id, mode}` with `ensure_current` or `review_now`; returns `{data: {request_id, session_id, disposition, schedule}}`. No assessment IDs exist yet. `cancelled` identifies redelivery of a cancelled request, alongside queued/joined/reused outcomes. |

The pending queue is a deliberate compatibility choice: this slice does not synthesize session-less rows in legacy attempt responses or silently change existing analytics/filter meanings. Assessment/current-coverage readers and the Evidence-panel action move with Stage 2. GitHub request source and requester context remain audit information and do not change credential ownership.

### Local validation and rollout

Behavior tests cover zero/false/null settings, policy version conflicts and historical restore, quiet/cadence deadlines, ten pushes yielding one final-head assessment, duplicate snapshots, process restart, manual joining/reuse, GitHub outage waits, draft/close invalidation, tenant isolation, concurrent request identity, earlier/later wakes, lost job leases, repair, and draining threads. The lifecycle test creates a disposable database and applies the complete migration chain on PostgreSQL 17; the store tests also exercise migration down/up in an isolated schema.

Run the optional database suites against disposable local infrastructure:

```sh
TEST_DATABASE_URL=<disposable-postgres-url> go test ./internal/db -run '^TestCodeReviewSchedulingPostgres$' -v
CODE_REVIEW_TEST_DATABASE_URL=<disposable-postgres-15-or-newer-url> go test ./internal/services/codereview -run '^TestCodeReviewSchedulingLifecyclePostgres$' -v
```

The lifecycle URL's user must be allowed to create databases; the test drops its own database afterward. Frontend tests exercise timing autosave/version fencing, pending rows, capability/permission gating, disabled controls, and retry request identity. The touched Go package suites, Go vet, tenancy/schema lints, 105 focused frontend tests, full frontend typecheck/lint, and the production frontend build passed locally. Native Chrome proof, remote CI, and production savings measurements remain outstanding.

Deploy the migration first, then compatible API and worker binaries. `CODE_REVIEW_SCHEDULING_ENABLED` defaults false and must be enabled consistently on API and workers only after rollout readiness. It is an operational capability switch; timing values remain versioned settings managed in 143. There is no automatic historical PR backfill or expanded post-approval spending. Existing queued reassessment jobs transfer ordinary intent into the scheduler; dispute jobs retain their provenance and lock admission. Already-created assessments drain through worker freshness checks.

For rollback, disable the capability consistently and keep the additive schema/data. Pending requests and audit rows remain available for later recovery. Do not use the down migration on a live populated installation: it drops the scheduling/request data and is only exercised as a disposable migration compatibility check. Old workers do not understand the new wake job type; deploy compatible binaries everywhere before enabling the capability. This slice makes no content-reuse, capacity-reserve, or measured savings claim.

## Investigation Baseline (Before Stage 1)

These observations describe the pre-change implementation verified during planning. The Stage 1 implementation record below supersedes the scheduling and policy API entries. Deployed configuration and production savings have not been verified; the default production SSH key was unavailable during the investigation.

| Area | Current behavior and source |
| --- | --- |
| Automatic triggers | `internal/api/handlers/code_review_reassessment.go` accepts only `pull_request.synchronize` for reassessment. Description, checks, and ordinary discussion updates do not automatically start a new code review. |
| Revision identity | `MaterialChangeKey` in `internal/services/codereview/service.go` hashes only `head_sha`. It does not compare trees or file contents. |
| Scheduling | `QueueReviewChanged` uses a one-minute default debounce and a job key containing PR and change key. It makes the replacement durable, then marks older active work stale and cancels its threads. |
| Worker refresh | `internal/worker/code_review_reassessment_handler.go` refreshes the authoritative GitHub snapshot and redirects an outdated queued head into the latest debounced head. Preserve this freshness behavior. |
| Explicit requests | `handleExplicitReviewRequest` and `startReview` use delivery identity to distinguish intentional requests from redeliveries. Separate deliveries can request new work on the same head. |
| Approval gate | `HasApprovedByPullRequest` in `internal/db/code_reviews.go` tests for any completed published approval on the PR. Automatic monitoring stops even if the current head is different. |
| Execution | `internal/worker/code_review_handler.go` runs the configured reviewer roster followed by orchestrator synthesis, with live refreshes at phase transitions and before deciding. |
| Existing early stop | `StopAfterDeterministicFailure` can avoid agent work on stable policy blockers. It defaults off; an explicit same-head request after an early stop can request substantive review. Preserve that intent. |
| Existing cooldown | `semantic_dedupe_cooldown_seconds` defaults to 900 seconds. Its admission use is in `internal/services/codereview/dispute_service.go`; it does not throttle ordinary push reassessment. |
| Policy UI | `frontend/src/app/(dashboard)/code-reviews/page.tsx` exposes reviewer count/models/reasoning, timeout, quorum, stable-policy early stop, and the dispute cooldown. Policy editing is admin-only. |
| General capacity UI | `frontend/src/app/(dashboard)/settings/runtime/page.tsx` exposes organization-wide concurrent agent runs. It is not a review-specific usage budget. |
| Capacity enforcement | `internal/api/handlers/sessions.go` checks the organization's session limit during interactive creation; `Orchestrator.checkConcurrency` in `internal/services/agent/orchestrator.go` checks running sessions against its configured ceiling. These are session-count checks, not atomic per-execution reservations or an interactive reserve. |
| Policy persistence | `code_review_policies` is insert-only versioned configuration. Public `PUT /api/v1/code-review-policies` currently accepts a whole config; `SavePolicyExpectingVersion` already supports optimistic concurrency in the store. |
| Runtime credentials | `PickRunnableMulti` supports personal-before-organization selection when a user is supplied. Review sessions created by `startReview` have no user ID and use organization credentials. `internal/services/agent/env.go` tracks credential bindings; credential shedding and fallback already exist. |
| Usage | `internal/db/usage_rollup_store.go` uses persisted token usage and `usage_hourly_execution`. `internal/services/agent/usage_tracker.go` tracks containers, so container hours alone are not evidence of review-token savings. |

Use `docs/design/overall.md` for system context. Related contracts are documented in `implemented/112-code-reviewer-bot-auto-approval.md`, `implemented/117-code-review-policy-simplification.md`, `implemented/122-pr-centric-code-review-analytics.md`, `implemented/124-code-review-visual-evidence.md`, and `121-code-review-decision-feedback-and-policy-tuning.md`.

## Product Behavior

### Admission decision

Admission means deciding whether to reuse, defer, or execute a review before creating expensive agent work. All automatic pushes, reviewer requests, UI actions, dispute reruns, and retry replacements pass through this service after their existing authorization checks.

| Event or condition | Required result |
| --- | --- |
| PR has never been sent to 143 for review | Do not start automatic monitoring merely because it receives a push. |
| PR is closed or merged | Remove pending work, prevent new dispatch/publication, and stop active work through existing cancellation mechanisms. Preserve history. |
| PR is draft | Hold new review work. Becoming ready wakes a previously requested review. |
| Automatic monitoring is paused | Hold automatic requests. An explicit review of an open, ready PR is allowed without resuming later automatic monitoring. |
| Push burst | Replace the pending target with the current revision and recompute the quiet deadline. Do not accumulate a queue of obsolete heads. |
| Equivalent event/request | Return the existing pending/running request or applicable assessment. Redelivery never consumes a fresh-review allowance. |
| History-only rewrite with proven equivalent context | Reuse completed analysis or let equivalent active analysis finish. Publish only after checking the current target. |
| Substantive code change or uncertain equivalence | Review the latest stable revision. Invalidate applicability immediately; cancel incompatible active work after a replacement is durable. |
| Base or parent changes | Refresh the PR's actual comparison and review context. Equal patch text alone does not establish reuse. |
| Description or visual evidence changes | Refresh applicability. Stage 2 falls back to a full review if review inputs differ; Stage 4 may rerun only affected evidence/synthesis. |
| CI/check/status-only update | Refresh applicable deterministic gates without starting a reviewer panel. Do not introduce an unconditional passing-CI prerequisite; current review policy can deliberately evaluate code independently of CI. |
| Review now | Bypass quiet/cadence delays and ensure coverage of the current revision. Reuse equivalent work. |
| Force fresh review | Record a new authorized request and reason; bypass completed-result reuse. Serialize behind active work and obey capacity/budget limits. |
| Rate limit, capacity shortage, or exhausted review budget | Keep one latest pending target with an explanation and retry time when known. Recheck eligibility and coverage on wake. |

### Timing and progress guarantees

For automatic code changes, calculate `eligible_at = max(last_material_change_at + quiet_period, last_agent_start_at + minimum_interval)`. Measure cadence from the first actual reviewer dispatch, not webhook receipt, worker polling, a reused result, or a failed pre-dispatch attempt. An explicit execution updates `last_agent_start_at` too, so it is not immediately followed by a scheduled automatic duplicate.

Stage 1 uses distinct authoritative head/base observations as the conservative definition of a change. Stage 2 advances `last_material_change_at` only when content or relevant inputs differ or cannot be compared. Repeated delivery of the same snapshot must not extend the deadline. Out-of-order webhook payloads cannot overwrite newer state; refresh GitHub before updating the pending target.

Add cheap admission/reconciliation inputs for `ready_for_review`, `converted_to_draft`, `closed`, `reopened`, and relevant `edited` events alongside `synchronize`. In Stage 2, base-branch pushes or periodic reconciliation refresh only already-monitored PRs that target that base. Check/status events refresh gates; relevant human description/evidence updates refresh input applicability. None of these events directly launches an agent. Ignore self-authored status churn and do not monitor unrelated PRs.

Keep at most one active assessment and one pending replacement per PR across workers. While classification is incomplete, the old assessment loses authority to publish for the new revision but may continue briefly. Once inequality is established, cancel it; once equivalence is proven, preserve it. A draft/close event or explicit cancellation takes precedence over reuse.

Use a generation as the version of the PR's desired assessment. A new target or accepted force-fresh intent advances it under the PR lock; equivalent duplicate intent does not. Pending requests do not need an assessment/session yet. When equivalent active work finishes after a history rewrite, persist its immutable source output, release its old active-assessment ownership, and create a reused assessment for the current generation. Do not mutate the old assessment's target or allow both generations to publish. Multiple ordinary/force requests received while work is running share one pending current-target slot; preserve each request and any force-fresh reason, but do not promise a separate execution for every click.

A validated retry of a failed attempt also advances the generation, even when the revision is unchanged; preserve `retry_of_session_id` and, from Stage 2, `retry_of_assessment_id`. Redelivery of that retry joins the same replacement. Under the PR lock, pending intent can only strengthen from `ensure_current` to `review_now` to `force_fresh`. The first upgrade to force-fresh advances the generation; later joins do not. Link every joined request to the pending generation and eventual result, retaining each force reason. Later ordinary requests and automatic pushes cannot silently downgrade an accepted force request.

Continuous meaningful changes may keep automatic work waiting; show elapsed waiting time and allow Review now. There is no automatic maximum-wait override in this release. Once the PR settles and capacity is available, the latest request must eventually run. Use age-aware fairness across PRs and repositories so one busy stack cannot monopolize the organization. Preserve the original waiting age when replacing a target. Use durable timer jobs and a bounded reconciliation sweep for missed wakeups rather than an open agent or an in-memory timer.

The existing `JobStore` enqueue path uses `ON CONFLICT DO NOTHING`: enqueueing the same dedupe key does not move its deadline. Add a scheduling-specific transactional wake operation under the PR lock: create the PR wake job if absent, or update a pending job's `run_at` when its deadline changes, including earlier Review now eligibility. A running wake job rereads durable PR state and requeues itself with its lease token without consuming a failure attempt when it must wait; `RetryWithoutConsumingAttemptWithLease` is the existing primitive to adapt. Do not enqueue a successor behind the running job's own dedupe key and then mark that job complete. Completion and pending-state inspection must serialize with request writers so a concurrent update either gets consumed or leaves a durable wake. Reconciliation repairs crashes and missed notifications; the job payload never overrides the current target/deadline.

### UI

Add **Review scheduling and usage** under the existing organization policy. Keep review quality controls in their existing sections. Use shadcn components, `DurationInput`, numeric-field hooks, TanStack Query, `useAutosave`, and one `AutosaveIndicator` per save scope. Configuration autosaves; Review now and Force fresh review are explicit operations. Viewers see effective settings without mutation controls.

| UI control | Proposed stored field | Initial behavior |
| --- | --- | --- |
| Automatically re-review changed PRs | `scheduling_policy.automatic_re_review` boolean | Enabled for previously requested PRs; preserves manual-only operation when disabled. |
| Wait after changes | `scheduling_policy.quiet_period_seconds` integer | Recommended 300; valid 0–3600. Existing organizations retain 60 until changed. |
| Minimum interval between automatic reviews | `scheduling_policy.minimum_interval_seconds` integer | Recommended 900; valid 0–86400. Existing organizations retain 0 until changed. |
| Concurrent review agents | `resource_policy.max_concurrent_review_agents` nullable integer | Null means no additional review-specific ceiling; existing runtime checks remain in force. Set a pilot value after measuring capacity; count executing reviewers and synthesis, not PRs. |
| Review execution starts per hour | `resource_policy.max_execution_starts_per_hour` nullable integer | Null means no additional hourly ceiling. A configured value counts every actual review execution attempt, including fallback attempts. |
| Capacity reserved for interactive work | `resource_policy.interactive_reserved_slots` integer | Initially 0; enabling a reserve requires an enforceable shared capacity model and a value below the applicable total capacity. |
| Review tokens per rolling 24 hours | `resource_policy.max_tokens_per_24h` nullable integer | Advanced Stage 3 control; expose only after usage coverage and accounting are verified. Null means no additional threshold. |

Nullable limits accept positive integers or explicit null, never zero as an undocumented alias. Implement bounded integer validation and document the chosen operational ceilings before enabling Stage 3 controls. Do not display unenforced controls as active settings. Include a simple action to apply the recommended timing values, with their effect visible before the action.

Rename **Reassessment cooldown** to **Dispute reassessment cooldown** without changing its saved value or semantics. Preserve models, quorum, timeouts, and the stable-policy early-stop control.

Each current PR row and evidence panel shows a static waiting/held state, its explanation, and `eligible_at` or `retry_at` when known: **Waiting for changes to settle**, **Waiting for review interval**, **Paused**, **Waiting for review capacity**, **Waiting for account availability**, or **Review budget reached**. A reused assessment says **Previous review still applies** and links to the source assessment and newly checked revision. Do not show waiting requests as failed sessions or fabricate a session when no agent ran.

Show Review now for authorized members/admins. Disable it with an explanation when the PR is not eligible. Force fresh review lives in the action menu and collects a short reason; it does not imply permission to bypass budgets. Per-PR pause/resume affects automatic monitoring only. Use the shared operational-state presentation and keep waiting states unanimated. Surface schedule changes and reuse through existing live updates.

## Content and Review Coverage

### Three separate identities

- **Request identity:** delivery ID or client-generated request UUID. This provides idempotency across HTTP retries, GitHub redeliveries, and worker retries. Intentional force-fresh requests have distinct identities.
- **Revision identity:** repository, PR, base ref/SHA, merge-base SHA, and head SHA. Retain it for audit and target publication even if commit history is equivalent.
- **Analysis identity:** a versioned digest of complete source content, relevant review instructions/approval contract, reviewer roster and prompt versions, and immutable review inputs. It provides reuse eligibility and does not contain timing or resource limits.

Compute equality deterministically without starting an LLM or a review sandbox. Stage 2 initially requires equal full head-tree, base-tree, and merge-base-tree contents under the same repository/PR scope, plus equal analysis contract and input manifests. Record the Git object format and fingerprint version. Preserve paths, renames, modes, deletions, submodule pointers, binary objects, and file bytes; do not normalize whitespace or discard test/generated files. A partial or unavailable comparison is not a cache hit.

These conservative rules catch commit-message amendments, empty commits, and restacks that only rewrite history. A restack that incorporates changed parent code may miss the cache even if the child's own patch is unchanged. Reusing those cases requires dependency-impact analysis and is deferred; do not promise that Stage 2 eliminates all restack reviews.

Use the PR's actual comparison base rather than always comparing with the default branch. A base-ref change invalidates applicability until re-evaluated. The snapshot service must add authoritative draft and base-ref information; its current `CodeReviewPullRequestSnapshot` has state and SHAs but no draft/base-ref fields.

Separate the code/reviewer contract from the mutable approval gates. Changes only to quiet time, budgets, or policy audit version must not invalidate code analysis. Changes to review instructions, roster, prompts, analysis-relevant approval policy, PR intent, or visual evidence require appropriate reassessment. Stage 2 can conservatively invalidate all analysis-relevant policy changes; finer reuse belongs in Stage 4.

Do not hash all discussion indiscriminately: 143's own rolling comment must not invalidate its review. Use the existing trust-filtered request context and visual-evidence snapshot contracts, with their versioned fingerprints. Refresh live checks, author/team eligibility, description applicability, and other approval gates before publication. A new statement of intended behavior can invalidate code conclusions even if no source file changed.

Git documents that `patch-id --stable` ignores whitespace, so it is not a sufficient safety key: [Git patch-id](https://git-scm.com/docs/git-patch-id). GitHub may dismiss approvals after diff or merge-base changes under branch protection: [protected branches](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches). Reuse must respect the current repository rules and must not change branch protection settings.

### Applicability and publication

Stage 2 replaces historical approval as a claim of current coverage with a revision-bound coverage reader. Enabling reuse does not itself enable automatic post-approval execution. Keep that execution expansion behind a separate rollout capability, initially disabled; choose its admin-facing enablement/default before activation. With the capability enabled and automatic monitoring allowed, a substantive post-approval change becomes pending review. With it disabled, show uncovered current content and allow an explicit review, while preserving the legacy stop on automatic spending after approval. Never label that deliberate hold as covered or promise an automatic wake. Preserve the historical approval record; do not automatically dismiss a human review. This feature does not merge PRs or replace GitHub merge protection.

Use only complete, usable reviewer results that satisfy the current contract. A failed, cancelled, timed-out, partial, or deterministic-early-stop attempt is not a completed clean code review. An early-stop result may still satisfy an equivalent policy-only check, but cannot supply missing code coverage. Reuse valid blocking findings as well as clean results; reuse is not synonymous with approval.

For a reused result, create an auditable assessment for the new revision referencing the immutable source session. Do not relabel or mutate the original session's SHAs or pretend to run agents. A fresh force request bypasses reuse of an already completed result but does not erase history.

Before publication, refresh the authoritative PR, its eligibility, applicable analysis contract, and relevant evidence. Verify the worker still owns the current PR generation. Publish against the explicitly checked head. Preserve existing rolling-comment and formal-review idempotency; duplicate jobs must not publish twice. A push during the final refresh/publish interval cannot be approved under the wrong commit ID. After publication, reconcile whether newer work is pending without retargeting the old approval to it.

## Capacity and Usage Admission

Keep scheduling admission in `internal/services/codereview`; enforce actual execution admission at the runtime dispatch boundary as well. A panel contains several agents and fallback executions, so limiting only the number of parent review sessions is insufficient.

Stage 0 must verify how each execution attempt, including disputes/triage, reviewer fallback, synthesis fallback, and repair of invalid output, maps to its session, thread, selected credential, model, and token usage. Instrument missing attribution before enabling hard accounting. Count these costs in review usage even when their execution fails or the parent review is superseded.

Implement organization-wide review concurrency and hourly execution-start admission atomically across workers. A concurrency reservation occupies a slot from dispatch reservation until confirmed execution end/non-start; a start-budget reservation becomes consumed usage when execution starts and is not refunded on completion. Retries of one delivery do not acquire two reservations. A fallback that actually executes is a new charged attempt. Reconcile abandoned leases against runtime state; a worker losing a lease must not continue dispatching. Reservation TTL alone is not proof that the underlying execution stopped.

Use the existing credential eligibility and shedding logic. Do not select another user's personal credential or make a rate-limited account healthy to satisfy a review budget. Agent/provider aliases that share one credential must share its usage and availability state. A budget wait is not an authentication failure and must not trigger credential rotation. Model capacity errors and account exhaustion remain distinct.

The requester's identity is audit/authorization context, not a change to session credential ownership. Keep code-review execution on the existing organization credential path; recording a local requester must not begin selecting their personal credentials.

The existing credential row/binding identity is the starting point, not proof that two separate configured rows represent different upstream accounts. Stage 0 must settle same-account aliasing and quota-window/reset attribution before claiming account-level reserves. Until then, enforce verified organization-wide limits and show account availability based on known provider signals; do not invent remaining quota percentages. Do not expose another tenant's credential identity or usage to solve a shared-account problem.

Build and validate a shared atomic execution-admission authority before enabling interactive reserves. Integrate interactive and review dispatch, including thread/fallback paths; keep existing session ceilings as separate compatibility constraints until their migration is explicitly validated. A review-only ledger cannot reserve interactive capacity. Stage 3 must specify the shared execution ceiling and its configuration mapping, without treating the current organization session count as an interchangeable execution count.

Already running work can drain; lowering a ceiling stops new admissions rather than killing valid work. Give required synthesis and reviewer continuations priority within enforced limits, and reserve the known minimum remaining execution starts before admitting a panel under an hourly budget. Keep future-start reservations separate from occupied concurrency slots; release unused start reservations on terminal cancellation/failure and obtain additional budget for fallback attempts. Reviewers finish before synthesis, so do not require concurrent slots equal to reviewer count plus one. Small limits may serialize reviewer lanes without reducing quorum. Unknown fallback demand or later policy reductions can still hold a panel; show that wait rather than promising unconditional completion.

For the rolling token threshold, use verified persisted usage and in-flight reservations. State the accounting convention using existing provider token fields and count cached tokens exactly once. Unknown usage must not silently count as zero. Provider accounting may arrive after execution; document and display that this threshold controls **new starts** and may be exceeded by already running work. It is not an exact guarantee of the upstream account's remaining quota. Hard per-token interruption is outside this plan.

When limits are reached, retain the latest target and wake at the earliest meaningful budget/credential reset or capacity-release event, with bounded retry/backoff when unknown. Manual priority may bypass timing but not capacity, authorization, or budget accounting. Both initial and repeat reviews consume budgets; otherwise repeated explicit requests become a bypass.

## Proposed Database Contracts

Names and fields below are proposed, not existing schema. Allocate migration numbers from the actual checkout at implementation time. Use direct pgx stores and transactions. Every new tenant table has `org_id uuid NOT NULL REFERENCES organizations(id)` and every query filters by org. Add normal FKs and parent-ownership validation; use composite tenant keys/FKs where needed to prevent mismatched PR/session/policy references.

Introduce these contracts incrementally. Stage 1 needs policy additions, PR scheduling state, and request identity; it continues to use existing review metadata for executed sessions. Add assessment tables/links and their FKs in Stage 2, and reservation tables in Stage 3. Do not add dangling FKs to future tables or require the new publisher before scheduling can ship. All active-session checks in Stage 1 still execute under the PR scheduling lock.

### Versioned policy additions

In Stage 1 add `scheduling_policy jsonb NOT NULL DEFAULT '{}'` to `code_review_policies`; add `resource_policy jsonb NOT NULL DEFAULT '{}'` in Stage 3. Define typed Go structs and validation for the UI fields above. Use presence-aware stored/input fields (`models.Optional[T]` or pointers as appropriate) and one explicit resolver: absent automatic monitoring means true, absent quiet period means 60, absent interval means 0, while explicit false and zero retain their meaning. Patch null removes the override and resolves the documented default. Do not use Go zero values or `value != 0` to detect absence. Extend policy scanning, cloning, resolution, history comparisons, restore, internal APIs, frontend types, and optimistic updates.

Empty historical scheduling config resolves to the legacy effective timing (automatic monitoring enabled, 60-second quiet period, zero minimum interval). Empty resource config adds no ceilings and reserves zero interactive slots. Only an organization's first-ever policy created after general availability explicitly stores recommended timing. Later versions, including unrelated policy edits, carry forward existing values. Do not mutate historical rows to fabricate prior settings; configuration changes deactivate and insert a new version transactionally.

### PR scheduling state

Add `code_review_pr_state` as a mutable operational table:

- `id uuid PRIMARY KEY`, org FK, `repository_id uuid` and `pull_request_id uuid` FKs; unique `(org_id, pull_request_id)`.
- `monitoring_requested_at timestamptz NULL`, `automatic_paused boolean NOT NULL DEFAULT false`.
- `generation bigint NOT NULL DEFAULT 0`, latest head/base SHAs and base ref as text, `snapshot_observed_at timestamptz`, and `is_draft boolean`.
- `last_material_change_at`, `first_pending_at`, `last_agent_start_at`, `eligible_at`, and `retry_at` as nullable timestamps.
- Stage 1: `active_session_id uuid NULL` and `pending_request_id uuid NULL` FKs, with nullable links to avoid insertion cycles. Stage 2 adds `active_assessment_id uuid NULL` only after the assessment table exists, and moves active ownership to that link transactionally.
- `state text NOT NULL` with checked values `idle`, `waiting`, `running`, `covered`, `paused`, `closed`; nullable typed `wait_reason` (`quiet_period`, `minimum_interval`, `draft`, `manual_pause`, `capacity`, `account_unavailable`, `budget`, `context_unavailable`). Stage 2 adds `post_approval_manual_only` for uncovered content intentionally held without an automatic wake. Stage 1 must not infer `covered` for a new head from an older approval.
- `created_at` and `updated_at` timestamps. This is lifecycle state, so insert-only settings semantics do not apply.

Index due pending rows by `(org_id, eligible_at)` and retryable rows by `(org_id, retry_at)` with appropriate state predicates. A system scheduler's cross-org scan must have the documented lint exemption; tenant store methods remain scoped.

This scheduling row owns the authoritative draft/base-ref observations required by admission; add them to the snapshot DTO and persist them here in Stage 1. Do not assume the existing `PullRequest` mirror contains these fields or independently trust webhook payload order. Serialize authoritative refresh/write under the PR lock, or fence responses so an older in-flight refresh cannot overwrite a newer observation. Admission and publication read the same observation contract.

### Requests and assessments

Add `code_review_requests` for durable intent and idempotency: UUID PK, org/repository/PR FKs, source kind and source identity, request mode (`ensure_current`, `review_now`, `force_fresh`), requester UUID when available, nullable `reason text` bounded to 2000 characters, normalized input hash, and created timestamp. Force-fresh requires a nonblank reason. Unique `(org_id, source_kind, source_identity)` prevents redelivery. Bind source identity to the PR and body; UUID reuse for different input returns a conflict. Trusted GitHub actors without a local user ID retain their verified source identity. Request content stays immutable; links/lifecycle status may be filled as processing advances.

Stage 1 omits the assessment FK and instead includes `target_generation bigint NOT NULL`, nullable `session_id` and `retry_of_session_id` FKs, plus status (`pending`, `joined`, `satisfied`, `superseded`, `cancelled`, `failed`). Stage 2 adds nullable `assessment_id` and `retry_of_assessment_id` FKs. `pending_request_id` identifies the representative request, not the full membership: all requests linked to the generation are resolved together. Carry still-pending membership forward when the target changes, preserving immutable input and force reasons. Retain request identity for the supported redelivery/retry horizon; any later retention job must preserve referenced audit rows and idempotency tombstones.

Add `code_review_revision_assessments` for the result applicable to a target revision, including reuse without a synthetic agent session:

- UUID PK; org/repository/PR/request/policy FKs; PR generation; immutable base/head/ref snapshot.
- `status` (`running`, `publishing`, `completed`, `superseded`, `failed`, `cancelled`) and nullable `result_origin` (`executed`, `reused`, `evidence_only`). Requests own pending work; create an assessment when execution or reuse is claimed.
- Nullable source session FK, fingerprint version, full content manifest, analysis-contract digest, evidence/input digests, and `coverage_complete boolean NOT NULL DEFAULT false`.
- Final decision and applicability reasons, publication identity, GitHub review ID/URL, result body, and created/completed timestamps. Decision/body/context become immutable after completion; publication recovery fields are operational state.
- Unique `(org_id, pull_request_id, generation)` and a partial unique active-assessment index per `(org_id, pull_request_id)` for `status IN ('running', 'publishing')`. Pending target intent is owned by `code_review_pr_state.pending_request_id`; it does not require a second active assessment. The PR row lock serializes generation assignment and claim/publish transitions. Equivalent requests link to the same assessment instead of generating duplicate active rows.

Keep `code_review_session_metadata` as execution evidence. Add a nullable assessment FK for new executions. New result publication is owned by the assessment path; adapt the existing handler/submitter rather than running two publishers. Historical rows remain readable without a fabricated fingerprint or assessment. Do not treat unbackfilled historical sessions as reusable content coverage. Analytics separates executed attempts from reused assessments and retains existing PR-round semantics.

Stage 2 requires one current-coverage reader returning the target revision/generation, applicability, decision, publication identity, and source execution provenance. Before enabling reuse, migrate every current-result consumer: automatic admission and historical-approval checks; same-head/retry validation in `service.go`; latest/current queries in `internal/db/code_reviews.go`; rolling comments in `internal/worker/code_review_status_comment.go`; current list/detail/evidence responses; and stats/analytics. Audit callers of `HasApprovedByPullRequest`, `GetLatestByPullRequest`, `GetLatestCompletedByPullRequest`, and `GetLatestSubmittedByPullRequest`; retain explicitly historical queries for history only. Cover a reused clean code result whose refreshed gates produce its first approval, not only reuse of an already published approval. Reuse must neither disappear from current decisions nor count as a new executed round or add source tokens again. Preserve legacy executed-round metrics and expose assessment/publication counts separately; historical metadata remains a compatibility source, never proof that an unverified current revision is covered.

### Resource reservations

For Stage 3, add `code_review_execution_reservations`: UUID PK, required org FK, nullable assessment/request/dispute/session/thread FKs, stable logical credential identity when known, `execution_attempt_key text NOT NULL` unique within the org, state (`reserved`, `started`, `settled`, `released`), bigint reserved/actual token counters, start/settle timestamps, and lease owner/fence/expiry. Require a source assessment, request, or dispute; inline triage without a session must have its own stable source/attempt identity instead of a fabricated session. Use the real runtime attempt identity established in Stage 0; do not use a parent session ID as the attempt key. Preserve ledger rows for failed/superseded work. Add indexes for active leases and rolling-window lookups.

Before Stage 3 implementation, add the concrete schema/API contract for the shared execution-admission authority: ownership, ceiling configuration, interactive execution leases, and conversion of review reservations into shared claims. This is an explicit Stage 3 design gate; the existing session counters do not satisfy it. Use one authority for the new execution ceiling rather than independent review/interactive counters. Include separate panel future-start budget holds keyed by org and panel with reserved/consumed/released counts; these do not occupy execution slots. Lock shared capacity and budget records in a deterministic order when acquiring reservations. All request insertion, pending-target updates, and wake changes that must succeed together use one transaction. All writers, including cancellation, retry, reconciliation, and publisher completion, acquire the same PR lock; fence worker mutations by lease and generation.

No new database triggers are required. Do not attach broad recount triggers to these operational tables.

## Proposed API and Live-Update Contracts

All routes below are organization-scoped, use existing authentication/CSRF protections and repository access checks, and never admit a PR merely because its UUID is known. Read access follows the existing code-review visibility rules. Mutation access is admin/member unless specifically marked admin-only; viewers/builders cannot spend review compute through these new routes.

| Route | Contract |
| --- | --- |
| Existing `GET /api/v1/code-review-policies` | Add effective scheduling/resource settings and capabilities identifying which controls are enforced. Preserve `{data: ...}` and policy version information. |
| New `PATCH /api/v1/code-review-policies` (admin) | Accept `{config: <RFC 7386 merge patch>, expected_version: <integer>, source: "manual"}`. Merge under the policy lock, validate, and insert a new active version. Omitted fields retain their values; null resets a field to its documented default/no-limit semantics. Return the saved policy. |
| Existing policy PUT/internal update/restore | Preserve old clients using raw-field presence or presence-aware DTOs: omitted new fields retain current values, explicit false/zero are applied, and explicit null resets to the documented default. All writers use the same lock and versioning path. Restoring a version predating these fields preserves current new-section values; the UI explains this before restore. Migrate UI editing to PATCH. |
| New `GET /api/v1/code-review-targets` | PR-centric source for **Current reviews**, including waiting requests without sessions. Accept `repository_id`, `state`, `author`, `search`, `decision`, `outcome`, `activity_status`, `status`, `risk`, `reason`, `created_after`, `created_before`, `sort_by`, `sort_order`, `cursor`, and `limit` (1–100). Preserve existing filter/sort choices while defining them against the current assessment and scheduling row. Return `{data: [<PR scheduling/assessment summaries>], meta: {next_cursor}}`, one row per monitored PR, identified by PR ID; session/assessment IDs may be null. Existing attempt-based routes remain the source for history. |
| New `GET /api/v1/pull-requests/{id}/code-review` | Return requested/paused monitoring state, authoritative target revision, scheduling state/reason, deadlines, active/pending IDs, and current assessment with source-session link and coverage applicability. This works before any review session exists. |
| New `PATCH /api/v1/pull-requests/{id}/code-review` | Accept `{automatic_paused: true|false}`. Pause/resume automatic monitoring, audit the action, and schedule reconciliation. Resuming does not bypass quiet time or budgets. |
| New `POST /api/v1/pull-requests/{id}/code-review/requests` | Accept `{request_id: <UUID>, mode: "ensure_current"|"review_now"|"force_fresh", reason?: <string>}`. Force-fresh requires a nonempty bounded reason. Return `{data: {request_id, assessment_id, source_session_id, disposition, state, eligible_at, retry_at}}`. Disposition is `queued`, `joined`, or `reused`; nullable IDs/times reflect actual state. |
| Existing `POST /api/v1/code-reviews/{id}/retry` | Preserve retry-specific validation and immutable replacement history; route replacement admission through the common service. Do not reinterpret retry as forced re-review of a completed result. |
| Existing list/detail/evidence and stream routes | Add optional current assessment/scheduling data and reuse provenance without breaking historical session responses. Do not insert session-less synthetic rows into the legacy attempt response type; the new target endpoint owns the PR-centric current view. |

Capabilities gate both UI and API admission: Stage 1 accepts `ensure_current` and `review_now`; unsupported `force_fresh` returns 409 `CODE_REVIEW_CAPABILITY_UNAVAILABLE`. Assessment fields remain null until Stage 2. Resource-policy writes likewise reject unsupported controls instead of accepting ineffective limits.

Use 202 for durably queued/joined nonterminal requests and 200 for an already available reusable result. Waiting on capacity/budget is an accepted request with visible state, not HTTP 429 or a failed review. Use 400 with existing `CODE_REVIEW_POLICY_INVALID` or new `CODE_REVIEW_REQUEST_INVALID` for invalid input, 403 for insufficient mutation rights, and 404 for inaccessible PRs. Use 409 with `CODE_REVIEW_POLICY_VERSION_CONFLICT`, `CODE_REVIEW_REQUEST_ID_CONFLICT`, or `CODE_REVIEW_PR_INELIGIBLE` for a stale policy version, request-ID reuse with different input, or an ineligible closed/draft/policy-disabled PR. Preserve `{error: {code, message, details}}`. Include current version on a policy conflict; refetch and preserve unsaved fields rather than silently retrying a full overwrite.

Replace whole-config optimistic replacement and latest-payload-only coalescing in `frontend/src/lib/code-review-autosave.ts`. Accumulate dirty field paths and compose queued edits into an RFC 7386 patch against the last acknowledged config; null must remain an intentional reset, including parent-reset followed by child-edit cases. Serialize saves per policy, take `expected_version` from the last successful server response at dispatch time, and reapply still-unsaved edits over that response. On conflict, refetch and surface conflicting fields; never silently overwrite another tab. Use the same documented default resolver for optimistic display and server responses.

For current-list filters, unassessed rows have null decision/risk/result fields and do not match result-specific filters; they remain visible with unfiltered or scheduling-state queries. Status/activity map to displayed current state, without manufacturing an attempt. Define current-view date filters and recency sorting against the current request/assessment's creation time, falling back to monitoring creation when neither exists; label that date in the UI. Use explicit null ordering, an allowlisted sort, and a PR-ID tie-breaker. Bind opaque cursors to normalized filters/sort and reject mismatches with 400. Verify parity against `CodeReviewListFilters` and handler validation before replacing the current view.

Extend `CodeReviewUpdatedEvent` additively with optional `pull_request_id`, `assessment_id`, `generation`, and `scheduling_state`; session ID remains optional. Publish after commit from scheduling transitions as well as assessment/publication transitions, including states with no session. Clients invalidate current PR state, current review lists, and applicable evidence queries. Redis/SSE remains a notification channel; durable state plus refetch/reconciliation is authoritative. Scope subscriptions and replay/fallback reads by org.

## Delivery Plan

### Stage 0 — Attribution and baseline

Inspect a bounded representative period once approved production read-only access works. Group costs by PR, trigger source, head, policy/contract, result, and execution role. Identify superseded execution, repeated same-content candidates, manual rerequests, disputes, model/credential fallback, and evidence-only repair requests. Verify token attribution and account aliasing. Do not equate same SHA, repeated attempts, or a stale status with proven waste.

Add reason-coded admission metrics and execution lineage needed for later enforcement. Extend existing analytics rather than starting a separate reporting product. This work can accompany Stage 1; unavailable production access does not block deterministic scheduling tests, but it blocks measured savings claims and numeric budget activation.

Exit: reproducible queries/fixtures and a documented accounting contract for execution starts and tokens, including missing/late usage. Record proposed pilot limits from evidence. Do not read credentials or production secrets outside the established Make-target workflow.

### Stage 1 — Durable scheduling and UI controls

Implement policy fields/PATCH concurrency, PR request/state persistence, one pending target, timing decisions with an injected clock, draft/ready/closed/pause transitions, and durable wake/reconciliation. Wire all relevant initial/automatic/manual entry points through common admission, preserving their authorization rules. Add authoritative draft/base-ref snapshot fields. Ship timing controls, renamed dispute cooldown, per-PR wait states, and Review now together.

Keep this slice independent: presence-aware scheduling policy and compatible PATCH/autosave, PR state with an active session link, request idempotency, and deadline-aware wake handling. Do not add assessment/resource FKs, fingerprints, reuse publication, or shared capacity enforcement. Resource controls and Force fresh become available only in their respective later stages.

Touch the lifecycle service, webhook reassessment handler, reassessment worker, job/store/model contracts, API/router, and code-review frontend. Add small dedicated modules/components rather than expanding the already large page/worker file indiscriminately. Existing source filenames above are anchors; new module names should follow nearby patterns.

Stage 1 may continue conservative SHA-based cancellation and the existing historical-approval rule until Stage 2 is ready. Explicitly label those remaining limitations in the rollout record. Do not advertise content reuse or current post-approval coverage at this stage.

Exit: a burst of ten head updates yields one execution of the latest stable target; duplicate delivery does not postpone it; process restart loses neither the request nor its due time. A pending deadline can move earlier or later, and a push racing running-job completion leaves a durable wake. UI values persist and visibly drive scheduling; unrelated edits and restore do not reset existing timing.

### Stage 2 — Conservative reuse and revision-bound results

Add complete content/input manifests and analysis-contract hashing. Classify before cancellation/dispatch; join equivalent active work and reuse complete outcomes. Implement revision assessments, reuse provenance, generation-fenced publication, and migrate all current-result readers. Add base-retarget/context refresh and bounded reconciliation for tracked approved PRs without reviewing every repository push. Replace automatic SHA-only behavior behind the reuse capability only after safety tests pass. Keep automatic post-approval execution separately gated; current applicability must still be reported honestly when that gate is off.

Wire Force fresh review with explicit intent. Preserve existing dispute authorization/semantic dedupe; a distinct objection remains eligible for fresh reasoning even if source code is identical. A second explicit request after deterministic early stop must still offer the full substantive review. A budget or cooldown must not erase the recorded objection.

Exit: equivalent history rewrites produce no additional reviewer executions, while changes to parent context, meaningful whitespace, instructions, or required evidence invalidate reuse. Reused results appear consistently in current decisions, retries, comments, and analytics without double-counting executions. Substantive post-approval updates are visibly uncovered; they schedule automatically only in the separately enabled cohort. No stale worker publishes against the latest head.

### Stage 3 — Capacity and usage budgets

Build on verified attribution. Complete the shared execution-admission schema/configuration gate, then implement atomic review concurrency, execution-start ceilings, and interactive reserves; follow with token thresholds and verified account-pool availability. Include every review-related execution path and fallback. Add resource UI controls only as their enforcement becomes available. Verify session-limit compatibility and distinguish future-start holds from occupied execution slots. Give required panel continuations priority within limits and show resumption times honestly.

Exit: concurrent workers cannot oversubscribe the configured review allowance; provider aliases cannot multiply a single credential's allowance; failure/retry/worker-crash paths settle reservations correctly; manual requests cannot bypass budgets; an eligible waiting PR resumes after capacity/reset without another user request.

### Stage 4 — Evidence-only follow-ups and evaluation

Use Stage 0–3 data to prioritize full reruns caused solely by description/evidence repair. Reuse code reviewers only when source and code-analysis inputs still match; rerun the necessary description/visual assessment and final synthesis with current policy. Changed intent, uncertain coupling, or insufficient coverage falls back to the full review path.

This stage does not introduce general patch-only code review or weaker quorum. Produce calibration cases comparing reused-code outcomes to independent full-review outcomes before activation. Keep prompts in `internal/prompts/templates/` with exported render functions when a new prompt role is needed.

Exit: correcting an evidence-only requirement can produce a current decision without a new code-review panel, and changed behavioral intent reliably takes the full path.

## Validation and Acceptance

Tests listed here are future implementation requirements; none were run for this planning-only change.

| Area | Required observable proof |
| --- | --- |
| Timing | Fake-clock tests for quiet deadline, cadence from actual start, same-snapshot redelivery, newest-target replacement, and explicit timing bypass. |
| Durable concurrency | PostgreSQL tests race webhook deliveries, manual requests, worker claims, and crashes; prove one active assessment, one pending target, one execution reservation per attempt, and no orphaned enqueue. Mock tests alone do not prove these invariants. |
| Request intent | Same request UUID/redelivery joins; UUID with different body conflicts; ordinary equivalent requests reuse; force requests preserve their reasons while sharing at most one pending fresh pass; dispute provenance remains enforced. |
| Readiness | Draft, ready, closed, merged, pause/resume, policy-disabled, and missed-webhook reconciliation paths; no unrequested PR becomes monitored. |
| Source equality | Amend/empty/history-only restack reuse; parent code changes, base retargets, binary/mode/submodule changes, meaningful whitespace, incomplete files, and unavailable GitHub context do not falsely reuse. |
| Review contract | Timing/budget edits preserve analysis; relevant instructions/roster/prompt/intent/evidence changes invalidate it; bot status comments do not invalidate themselves. |
| Publication | Push/policy/evidence change during classification, synthesis, and final publish; stale worker lease; duplicate publisher delivery; explicit commit ID; new-head applicability never inferred from old approval. |
| Limits | Multi-worker reservations, rolling-window boundary, retries/fallbacks, aliases, cancellation and late usage; known account reset; unknown usage; interactive reserve; panel completion with a small concurrency ceiling. |
| Authorization/tenancy | Cross-org PR/request/source-session/policy/credential access fails; viewers/builders cannot enqueue or change policy; members cannot change org limits; personal credential scope is preserved. |
| Settings UI | Admin/view-only states, autosave/blur/unmount, rapid edits, concurrent tabs/version conflicts, null/reset semantics, errors, and round-trip effective values. |
| PR UI | No fake session for waiting/reused work; source links and revision labels; disabled-action reasons; current-list visibility and SSE refresh; keyboard and mobile behavior in native Chrome. |
| Compatibility | Historical policy/session reads, old PUT clients, policy restore, retry paths, dispute cooldown, PR-centric analytics, and rolling deployment with old jobs. |
| Wake and retry races | Earlier/later pending deadlines, running wake versus push/Review now, lease loss, failed same-head retry with a new generation, and joined force intent surviving later ordinary events. |
| Policy presence | Historical `{}` resolves to true/60/0; explicit false/0 survives round-trip; only first-ever policies get recommended defaults; unrelated version inserts and old-version restore preserve timing; patch coalescing preserves independent edits and null resets. |
| Reuse readers | A reused source whose current gates yield its first approval is visible to every current-result consumer; retries never resurrect superseded coverage; reused publication adds no executed round or duplicated tokens; post-approval capability off/on is tested separately. |
| Admission accounting | Reviewer count greater than concurrent slots still reaches synthesis by serialization; future-start holds occupy no execution slots; start usage survives completion; interactive and review races cannot exceed the shared execution ceiling. |
| Outcome | Report actual tokens/starts per useful completed PR assessment, superseded spend, reuse rate, queue latency, stale-publication incidents, and sampled outcome differences. Do not substitute attempted-review counts for savings. |

Run focused checks per implementation slice, expanding when a shared contract changes:

```sh
go test ./internal/services/codereview ./internal/models ./internal/db
go test ./internal/worker -run 'CodeReview'
go test ./internal/api/handlers -run 'CodeReview'
go vet ./internal/services/codereview ./internal/models ./internal/db ./internal/worker ./internal/api/handlers
make lint-tenancy
```

For Stage 3, include affected `internal/services/agent` tests and vet. Run relevant real-Postgres tests with `TEST_DATABASE_URL` configured to an isolated local test database; a skipped PostgreSQL suite is not concurrency proof. Validate additive migration up/down/up in a disposable database and preserve data on rollback. Follow the repo's table-driven, parallel, exact `require` assertion patterns.

From `frontend/`, run targeted Vitest and ESLint for changed code-review components/page and API/autosave helpers. For example:

```sh
npx vitest run 'src/app/(dashboard)/code-reviews/page.test.tsx'
npx eslint 'src/app/(dashboard)/code-reviews/page.tsx' src/lib/code-review-autosave.ts
```

Shared API/type changes and the eventual public-doc update also require the repository-prescribed full frontend typecheck/lint/build. Use native Chrome for manual UI proof and preserve the running dev server. Record local checks, remote CI, UI proof, and production rollout separately.

## Rollout, Migration, and Recovery

1. Ship additive readers/schema and accounting first. Old records remain readable; unknown fingerprints are misses. Gate new dispatch ownership until all workers serving the organization understand it.
2. Preserve existing organization timing and limits until an admin changes them. Make effective values visible in 143. Recommended defaults apply only to an organization's first-ever policy created after rollout, not every inserted version.
3. Pilot Stage 1 with UI-configured five-/15-minute timing. Stage 2 first records would-reuse decisions without reusing outputs; inspect known-equivalent and invalidating cases before enabling reuse.
4. Ship truthful current-coverage reporting and complete reader/publication safeguards with reuse. Keep automatic post-approval execution disabled until a separate pilot has an explicit admin enablement/default decision and measured demand. Do not silently expand spending as a consequence of enabling reuse. Compare useful coverage and total demand in the opt-in cohort.
5. Enable Stage 3 limits from measured capacity, with UI previews of their effect and visible queue states. Do not claim exact provider quota protection until account identity and reset behavior are verified.
6. Publish the changed behavior in `docs/public/guides/code-review-policy.mdx` as each stage is available. Update implemented design references and `overall.md` only after the corresponding behavior ships. Follow the public-docs instructions when editing that file.

Operational rollout controls may disable new dispatch/reuse, but ordinary user limits live in 143, not environment variables. A rollback stops new ownership transitions, lets compatible in-flight work drain or cancels it through the normal path, and preserves pending targets and audit rows. It never changes a waiting request into approval or discards requests to make the queue look healthy. Restore the prior policy through versioned settings rather than editing history. Do not automatically drain a held backlog at unlimited speed when disabling a budget.

Migration rollout must define how existing queued jobs acquire or defer to the new PR lock/generation. Preserve historical explicit deliveries and approvals. Do not run a blind backfill of all historical PRs or automatically review them. Seed monitoring state from previously requested, currently open PRs in the chosen rollout cohort and refresh them before action.

## Surprises & Discoveries

- The visible 15-minute cooldown already exists, but belongs to disputes. Its broad label makes it easy to mistake for push throttling.
- Stable deterministic early stop already avoids some fan-out when enabled; this plan must build on it rather than duplicate it or equate an early stop with completed code coverage.
- Automatic reassessment currently ends after any published approval. Correct current-content coverage can add necessary post-approval work while removing redundant pre-approval work; measure both.
- Policy saves are whole-config PUTs even though newer settings guidance calls for merge patches. Store-level version conflict support exists and can support safer UI writes.
- The snapshot used by deferred review entry points lacks draft/base-ref fields required by the proposed readiness/context decisions.
- The exec-plan-writer skill references `.agent/PLANS.md` and `docs/AGENTS.md`; neither exists here. This plan follows `docs/design/AGENTS.md`, using `future/` and `Not Started`, and retains the skill's living-document sections.

## Decision Log

- **2026-09-10 — Preserve per-PR review.** The user chose review-each-ready-PR with suppression of repeated push/restack work over bottom-of-stack or combined-stack review.
- **2026-09-10 — Make settings a UI feature.** The user asked for these controls in 143. Include scheduling, resource controls, and explanations in each delivery stage rather than leaving them as backend-only settings.
- **2026-09-10 — Prioritize timing, exact reuse, then enforced budgets.** Timing is a bounded first slice; exact reuse needs explicit coverage/publication contracts; budgets require trustworthy execution attribution. Baseline instrumentation accompanies the early slices.
- **2026-09-10 — Prefer conservative equality.** Full content/context equality precedes dependency-aware restack reuse and incremental code review. No line-count-only shortcut or smaller reviewer quorum is part of this work.
- **2026-09-10 — Keep limits provisional.** Five-minute quiet time and 15-minute cadence are proposed starting settings. Account-pool identity, token completeness, and numeric usage ceilings require measurement before enforcement claims.
- **2026-09-10 — Incorporate validated Claude findings.** Specify wake/lease races, retry generations, joined intent, reader migration, presence-aware defaults, compatible restore/autosave, and current-list parity. Preserve Stage 1 as an independently deliverable scheduling/UI change. Existing runtime checks count sessions; a shared atomic execution authority is a Stage 3 prerequisite, not an existing guarantee. Reject a reviewer-count-plus-one concurrency floor because synthesis follows reviewers.
- **2026-09-10 — Separate post-approval spending from reuse.** Coverage reporting must be accurate, but expanding automatic execution after approval remains disabled pending its own rollout and admin enablement decision.

## Open Measurements and Stage Gates

- Which fraction of current review usage comes from superseded execution, equivalent content, explicit rerequests, evidence repair, and fallback? This determines prioritization within the agreed stages.
- Can existing credential bindings identify the same upstream account across configured aliases/rows, and what reset/quota signals are reliable? Settle before enabling account-level reserve claims.
- Can every actual execution attempt and token report be reconciled exactly once, including failed and fallback work? If not, instrument it before enabling the corresponding hard accounting.
- Which capacity/start/token values preserve interactive headroom and acceptable review latency for the pilot? Choose and record them from the baseline; no universal daily token number is assumed.
- What shared execution ceiling, configuration mapping, and lease schema will integrate interactive and review dispatch without silently changing session-limit semantics? Complete the Stage 3 schema/API addendum before implementing interactive reserves.
- What admin control/default and measured demand justify enabling automatic post-approval execution? Resolve before that separate capability is activated; reuse can ship with it disabled.
- How much outcome divergence does Stage 4 introduce on evidence-only cases? Keep that stage gated until calibration supports preserving the original code analysis.

These are bounded implementation/rollout questions, not reasons to postpone the deterministic Stage 1 work or omit its UI.

## Outcomes & Retrospective

Stage 1 now has a local implementation and automated database/UI evidence. Content reuse, shared execution admission, interactive reserves, and post-approval activation remain later delivery units with explicit design gates. Native Chrome proof, remote CI, deployment, production measurements, and savings validation remain outstanding.

When implementation proceeds, append measured outcomes and decisions here, update Progress, and move this document according to `docs/design/AGENTS.md`. Do not mark the plan implemented on the strength of local tests alone.
