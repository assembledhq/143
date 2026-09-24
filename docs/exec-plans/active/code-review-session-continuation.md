# Conditional code review session continuation

Status: First-release implementation complete locally; draft PR preparation underway. Activation remains gated on provider, browser, CI, and pilot validation. Last reviewed: 2026-09-24.

Source: [missing-screenshot discussion](https://assembled-hq.slack.com/archives/C0AELD22NBG/p1790191880952539) and the planning conversation that followed. Repository baseline: `77c158c4`. No production state, latency, or cost claims have been verified for this plan.

This is the active delivery plan for the evidence-only reassessment portion of [Code Review Scheduling, Reuse, and Usage Controls](../../design/code-review-scheduling-and-reuse.md). It builds on [Automation Target Session Continuity](../../design/125-automation-target-session-continuity.md). It brings the assessment identity and evidence-only slice forward without requiring history-rewrite equivalence, review budgets, or incremental review across changed code to ship first.

The repository has no `.agent/PLANS.md` or `docs/AGENTS.md` at this revision. This document follows the ExecPlan writer's self-contained planning requirements and living sections. The linked architecture documents retain the repository's design-document status conventions.

## Purpose and observable outcome

After 143 completes a code review and withholds approval solely because visual evidence is missing, an author can add the evidence and select **Re-check PR**, or post a new authorized reviewer mention. If the reviewed code and review contract still match, 143 continues the existing orchestrator conversation to inspect the new evidence, reevaluates current approval gates, and records a new assessment. It does not repeat the reviewer panel for that qualified case.

Each assessment retains its own inputs, decision, execution usage, and GitHub publication history. The conversation can survive several assessments. Restoring a conversation does not establish that its earlier code conclusions apply: applicability is decided independently before dispatch and again before publication.

When native context is unavailable, the same bounded task reconstructs its context from persisted reviewer evidence and completed assessment summaries. A reconstruction must not silently broaden the task or imply that native context survived. When applicability cannot be established, the request takes the existing full-review route.

The first release targets the button and existing explicit reviewer mentions. Automatic rechecks after evidence edits are a subsequent delivery slice. This is the working default from planning; no different trigger preference was supplied. The runtime and assessment design supports both entry points.

## Progress

- [x] Read the Slack discussion and trace review scheduling, full-review execution, synthesis repair, visual evidence capture, and GitHub publication.
- [x] Trace the automation dispatcher, owned-session lifecycle, checkpoint provenance, reconstruction, and shared `ContinueSession` path.
- [x] Locate the existing proposed assessment model and align this plan with it.
- [x] Draft product behavior, schema/API contracts, delivery slices, failure handling, and acceptance cases.
- [x] Apply the stated first-release default: button plus existing authorized reviewer mentions, with automatic evidence triggers after the pilot.
- [x] Complete the source and contract consistency audit; record the remaining implementation gates below.
- [x] Incorporate source-validated findings from Claude Fable review `claude-code-review-conditional-c-5918c3ed`; distinguish accepted corrections from unproven simplifications.
- [x] Start implementation on `codex/code-review-session-continuation` with GPT-6 Sol agents owning assessment persistence, input comparison, and durable continuation; captain owns integration and verification.
- [x] Implement and locally verify the assessment foundation and compatible readers, preserving historical full-review evidence and session analytics.
- [x] Implement transactional dispatch/completion and the focused recheck handler on the existing continuation path; verify duplicate dispatch, exact-turn completion, reclaim, stale-lease rejection, and job retention with disposable PostgreSQL.
- [x] Implement conservative evidence-only routing and assessment publication; verify malformed responses, current-input fences, uncertain receipts, and durable full fallback.
- [x] Implement Re-check PR, Force fresh, and explicit-mention routing locally behind disabled capability and organization controls.
- [ ] Complete authenticated browser evidence and required CI before shipping the first release.
- [ ] Run isolated comparison, native-provider validation, and an authorized production pilot.
- [ ] Evaluate and deliver automatic evidence-change triggers.

## Surprises & Discoveries

- The automation work is substantive reuse infrastructure. `TargetDispatcher.decide` already distinguishes fresh, continued, reconstructed, waiting, and pending-checkpoint states. Its DB ownership and result-marker contracts are automation-specific; shared runtime code can be reused, while review admission and publication need their own records.
- Code review already sends additional orchestrator turns through `threads.SendMessage`, while its supervising job polls durable thread state to repair invalid structured synthesis. The new capability extends that pattern across completed assessments; full reviews can keep their existing controller.
- `SendMessage` persists the message/inbox before separately enqueueing `continue_session`. Recording an expected turn and harvesting that exact turn does not by itself close the crash window between those operations or prevent a repeated send. The recheck path needs durable dispatch identity and recovery, with the storage choice gated on a transaction/lease audit.
- `VisualEvidenceService.Capture` restores its immutable snapshot using `visualEvidenceRecordKey(sessionID, headSHA)`. A second assessment in the same session and at the same head would otherwise keep seeing the first evidence set. New records must use assessment identity.
- `code_review_session_metadata` has a session index, not a unique session constraint, but store APIs and callers address one review through `GetBySessionID`, `MarkRunning`, `CompleteReview`, and session-scoped result queries. Inserting another metadata row for the same session would not solve the identity problem.
- `harvestCodeReviewOrchestratorResult` reads the latest assistant output and cumulative thread cost. Continued assessments require an exact execution/turn result and per-attempt usage.
- `ContinueSessionOptions.OnTurnComplete` is explicitly best effort. It cannot be the sole durable completion receipt for a review assessment. Automation turn completion provides a stronger fenced transactional pattern to consult where the current review path lacks a required guarantee; copying its entire execution model is not a prerequisite.
- Checkpoints are shared at session level, and snapshotting can be skipped while sibling threads are active. Native continuation must prove that the installed checkpoint contains the intended orchestrator context after reviewer threads have drained.
- `DescriptionInputHash` is recorded without a comparison in the current evaluator. A test explicitly permits best-effort approval after a description change. The new evidence-only route therefore needs a fresh comparison at publication; the recorded hash is not an existing freshness guarantee.
- Scheduling is available when its services are wired; there is no scheduling rollout flag in this checkout. New continuation controls below are proposed additions.
- Integration inspection found that the legacy primary orchestrator was not guaranteed read-only. Assessment-enabled full runs now use a dedicated review thread in read-only mode; reusable coverage checks the recorded execution mode, validated synthesis, and complete reviewer roster. Flag-off execution retains its prior path.
- Full/recheck integration exposed reader and recovery obligations beyond the initial happy path: out-of-line reviewer output must be hydrated, old session usage must remain unchanged, terminal assessments must settle the scheduling request, and publication must distinguish an unsent reservation from a possibly accepted GitHub write. These are implemented and covered by focused or PostgreSQL tests below.

## Decision Log

1. **2026-09-23: Reuse existing continuation infrastructure.** Use the review supervisor's thread-continuation pattern and the shared provider/snapshot runtime. Reuse automation checkpoint-readiness, workspace reconstruction, and fencing patterns where required. Extract only demonstrated common primitives; retain domain-specific stores and policy decisions.
2. **2026-09-23: Separate assessment identity from conversation identity.** An assessment is the immutable unit of review evidence and decision. A conversation is the session plus its saved agent context. A request is durable user/event intent and can join an assessment.
3. **2026-09-23: Start with unchanged-code visual-evidence repair.** Require a completed full baseline whose only approval blocker is `description_failed`, and verify that every missing requirement is visual. Changes to code, base, review policy, or intended behavior use a full review in this version.
4. **2026-09-23: Continue the orchestrator only for an evidence-only assessment.** Preserve the baseline reviewer results, quorum, and findings as referenced evidence. The narrow response may update visual-requirement assessments or request escalation; it cannot clear code findings or other risk reasons.
5. **2026-09-23: Treat checkpoint loss independently from coverage loss.** Missing native state can reconstruct a valid evidence-only task. Missing baseline evidence requires a full review. Successful reconstruction is observable and is not called native resume.
6. **2026-09-23: Keep publication in the backend.** Agents produce structured evidence. Full reviews and rechecks use shared assessment-aware evaluation/publication helpers with one PR publication fence.
7. **2026-09-23: Preserve previous decisions.** New assessments never overwrite earlier input manifests, findings, prompt records, decisions, or publication receipts. Mutable dispatch/publication state remains operational data.
8. **2026-09-23: Establish new full baselines before reuse.** Historical session records remain readable. They are not automatically assigned invented fingerprints or checkpoint provenance; a legacy review lacking the new complete baseline takes a full review on its first recheck.
9. **2026-09-23, after Fable review: Keep the full-review controller.** Adapt `run_code_review` to capture/finalize full assessments, and add a small supervising recheck handler. Do not rehost full review on a new execution controller. Keep the current single-run interaction mode; a mode change is not required merely to send another thread turn.
10. **2026-09-23, after Fable review: Make extra execution tables conditional.** Prefer extending existing agent-result/message/job identity. Do not claim that exact-turn polling proves dispatch idempotency, lost-lease safety, checkpoint coherence, or complete attempt usage. Resolve those proofs before choosing the minimum additional durable records. The reviewer suggested dropping all new bookkeeping; that simplification remains unproven.
11. **2026-09-23, implementation audit: Use a narrow recheck dispatch receipt.** The existing message/inbox and enqueue operations are separate; generic thread completion is not job-lease-fenced. A review-specific dispatch/outbox row will bind assessment, message, job, exact thread turn, bounded attempt usage, and checkpoint provenance. It supplies atomic dispatch and fenced completion without a replacement full-review controller or general execution tables. Database race tests remain required before activation.

12. **2026-09-23, implementation: Keep ownership on the session and provenance on the exact dispatch.** The selected implementation uses a PR owner on the existing session and a narrow transactional `code_review_recheck_dispatches` ledger. It does not create a parallel conversation lifecycle table. Migration constraints and PostgreSQL tests cover tenant/target identity, duplicate dispatch, exact-turn completion, and stale-lease rejection. Native continuation is conservative: the first recheck reconstructs; later continuation requires a preceding coherent receipt.
13. **2026-09-23, integration review: Stage before reserving publication and resume the staged outcome.** A crash after staging cannot cause a new decision/body to overwrite the assessment. Both summary and formal-approval identities are retained; matching markers must confirm the requested commit and approval state. Freshness is checked after acquiring the shared publication lock. Send intent changes durably from reserved to uncertain before a network mutation. Proven-unsent stale results can be superseded and replaced; uncertain results are marker-reconciled, with writes retried only for unchanged inputs. GitHub and PostgreSQL still cannot commit atomically or guarantee exactly-once external effects through markers alone.
14. **2026-09-23, integration review: Fingerprint external prompt context.** Code-review prompt construction and capture share strict integration-context resolution. Dependency failures remain errors; unversioned memory or overflowed visual discovery cannot establish reusable coverage. The existing full review remains available for deterministic reuse-ineligible captures.

## Context and orientation

Paths in this section exist at the repository baseline. Later proposed module names are implementation destinations, not existing APIs.

| Responsibility | Current source and integration point |
| --- | --- |
| PR admission and request identity | `internal/services/codereview/scheduling.go`: `RequestScheduledReview`, `scheduleReview`, `ReconcileSchedule`; `internal/db/code_review_scheduling.go`: `WithLockedPR`, `RecordCodeReviewRequest`, `HasActiveCodeReview` |
| New full review | `internal/services/codereview/service.go`: `startReview` creates a single-run session, metadata, and `run_code_review` job |
| Reviewer panel, synthesis, gates, output | `internal/worker/code_review_handler.go`: reviewer/orchestrator helpers, `evaluateLiveCodeReviewOutcome`, `submitCodeReviewToGitHub` |
| Existing follow-up turn | `requestCodeReviewOrchestratorSynthesisRepair` and `codeReviewOrchestratorObserveRepairCompletion` in the same worker file; `internal/services/thread/service.go`: `SendMessage`, `claimSessionForSend`, `createMessageInTx` |
| Native resume and fallback | `internal/services/agent/orchestrator.go`: `ContinueSession`, `ContinueSessionOptions`; `internal/services/agent/runtime.go`: checkpoint capability |
| Automation ownership and provenance | `internal/services/automations/target_dispatch.go`, `internal/services/agent/automation_turn.go`, `internal/db/automation_turns.go`, `internal/db/automation_targets.go`, `internal/db/automation_completion.go` |
| Worker/executor dispatch | `internal/worker/handlers.go`: `newContinueSessionHandler`; `internal/worker/session_executor_runtime.go` |
| Evidence snapshots | `internal/services/codereview/visual_evidence.go`, `internal/services/github/code_review_visual_evidence.go`, `internal/models/code_review_visual_evidence.go` |
| Existing UI request | `internal/api/handlers/code_review_scheduling.go`, `frontend/src/components/code-review/scheduling.tsx`, `frontend/src/components/code-review/review-now-from-comment.tsx` |
| Explicit mention classification | `internal/services/codereview/service.go`: `handleExplicitReviewRequest`, `QueueReviewChanged`; `internal/services/codereview/dispute_service.go`: triage and `queueReviewRequest`; preserve existing trust rules |
| Current evidence/read models | `internal/db/code_reviews.go`, `internal/api/handlers/code_reviews.go`, `frontend/src/lib/api.ts`, `frontend/src/lib/types.ts`, `frontend/src/app/(dashboard)/code-reviews/page.tsx` |
| Webhook observations | `internal/api/handlers/code_review_reassessment.go` and `internal/api/handlers/webhooks.go` |

The existing scheduling plan proposes `code_review_revision_assessments`. This plan uses that name and its single-current-assessment model. It specializes the initial analysis identity to exact immutable revisions; it does not implement equality between different commits.

## Review routing contract

Before allocating an agent, fetch the authoritative PR snapshot, resolve the current policy, load the latest applicable full baseline and latest assessment, and discover/capture current review inputs. Record the admission decision and reason. A caller cannot force the evidence-only route by choosing a button label or claiming to have added a screenshot.

| Observation | Route |
| --- | --- |
| Matching delivery/request UUID | Return the recorded disposition; do not dispatch again |
| Equivalent assessment is active | Join it; if newer evidence arrived, retain a pending follow-up rather than losing it behind the active request |
| Exact review inputs and current gate state still match a completed assessment | Return that assessment with an unchanged result; refresh before claiming applicability |
| Same head, base SHA/ref, complete baseline, unchanged review contract and intent, only visual requirement(s) were missing, evidence changed | `evidence_only`; continue or reconstruct the orchestrator |
| Head/base/ref changed, full review was incomplete, general description/testing requirement missing, missing provenance, new policy/roster/prompt contract, substantive intent change, or a dispute-routed request | `full`; start a fresh conversation generation and run the normal panel |
| PR closed, policy disabled, or requester unauthorized | Apply existing ineligibility/authorization behavior |
| GitHub snapshot/evidence fetch unavailable | Wait with a reason; do not classify an unavailable input as unchanged |

`recheck` bypasses scheduling delay but does not set the legacy `force` bit merely because it is explicit or carries a new screenshot message. Compare the captured inputs first: unchanged inputs return the completed assessment with `disposition=reused` and HTTP 200; eligible evidence changes select the narrow route; incompatibility or incomplete coverage selects full review explicitly. Preserve `review_now` semantics. Translate a full-route decision into fresh dispatch only after classification so same-head legacy reuse cannot swallow that decision. Request UUID/actor/trigger-envelope changes are audit identity; changed substantive instructions or objections remain review inputs and cannot be ignored to obtain reuse. Preserve the existing never-reviewed-PR restriction for this endpoint.

For release 1, the button and trusted non-dispute mentions through `handleExplicitReviewRequest` enter the same planner. Requests carrying `TriggeringDisputeID` or `ReviewRequestDisputeID` currently bypass `scheduleReview` in `QueueReviewChanged`; keep those on full review and preserve their dispute provenance. Do not claim that all mentions share one entry point. Both routes participate in the PR admission/publication fence and current-assessment projection.

For the initial `evidence_only` route, all of the following must hold:

- Same org, repository, PR, exact head SHA, base SHA, and base ref; changed-file manifest matches the baseline. No patch-ID or whitespace normalization.
- Captured policy ID/version, reviewer roster, actual model/fallback configuration, prompt-contract version, and repository instructions match. Versioned prompt content digests are part of the contract. Any unrecorded external dependency used by the baseline makes it ineligible.
- Baseline reviewer coverage satisfies the captured quorum, output validation, and read-only requirements. Baselines with failed/timed-out/cancelled required reviewers, early stop, invalid synthesis, or unverified coverage cannot supply code coverage.
- The baseline's complete risk reason set contains only `description_failed`; inspect structured requirement assessments to ensure every missing requirement has `evidence_kind=visual`. Advisory findings may remain and must still appear in evidence.
- PR title and non-evidence statement of intent are unchanged. For the first release, permit only insertion/replacement of Markdown image nodes and their evidence captions, or images in human discussion without changes to the PR's prose. Treat changed prose that may redefine intended behavior as full review. Capture/parser failure is a full-review fallback, not permission to ignore text.
- New image bytes, source provenance, and source text are captured under this assessment. A URL alone or the sentence “I added a screenshot” cannot satisfy a requirement. Use existing download limits, trust filtering, source attribution, and evidence validation.

Store separate versioned digests for code context, review contract, PR intent, visual evidence, request context, and live gates. The aggregate input digest covers them all. The router may permit a difference only in named evidence components; equality of head or policy alone is insufficient. Image alt text/captions remain untrusted evidence even when the code/intent digest is unchanged.

The follow-up prompt includes the full baseline summary, hydrated reviewer outputs, source findings, applicable visual requirements, current evidence attachments, and exact code identity. Its strict JSON response binds `baseline_assessment_id` and `input_digest`, requires an explicit `escalate_full_review` boolean, and otherwise returns every selected requirement with `status=satisfied|missing`. Escalation carries a concrete reason bounded to 1900 bytes, leaving room for the supervisor's prefix within the fallback request limit. The reason is explanatory text, rather than a new enum taxonomy; the backend alone chooses the full-review route. An escalation retains its exact execution response and reason, withholds approval, and durably queues a full review. It cannot erase an adverse observation while falling back.

Validate the narrow response before merging any requirement update. Missing/malformed output, unknown or omitted requirement IDs, contradictory outcomes, absent image citations, and citations outside the new assessment snapshot cannot clear `description_failed`. Record an explicit invalid-response failure, preserve the baseline blockers, and expose a retry/full-review recovery action. Do not fabricate a model `still_missing` response for an execution failure. An explicit valid `still_missing` remains a completed non-approving assessment. Run the existing image-basis validation against the new snapshot. The current evaluator defaults `DescriptionPassed` to true when synthesis is unusable and separately adds invalid-synthesis blockers; neither that default nor successful baseline synthesis may substitute for validation of the new response.

The backend merges only the permitted requirement updates with baseline evidence, then invokes the normal deterministic evaluator using fresh checks, branch/PR state, author/team/fork eligibility, and policy. New blockers prevent approval. Rechecking visual evidence does not exempt any other gate. Unchanged-code deterministic-gate-only optimization for previously blocked reviews is deferred; outside the matching-input reuse case above, use the full route until that optimization has its own contract.

## Conversation and execution lifecycle

A conversation generation belongs to one org/repository/PR and one compatible review contract. The first full assessment uses the existing `startReview`/`run_code_review` flow and its single-run session, augmented with review ownership and assessment identity. Reviewer threads run as they do today, followed by the orchestrator. Completed sessions can already be claimed for continuation when resumable; switching interaction mode is unnecessary for this feature. After a successful full assessment, the conversation can continue for qualifying rechecks. A full-review fallback, force-fresh request, incompatible contract, or unavailable session creates another full-review session. The old owner is released after its replacement completes and the old runtime drains. A 25-assessment cap remains a proposed pilot safeguard; this implementation currently enforces prompt/image bounds and exact checkpoint eligibility, rather than a session-wide assessment-count limit.

Only one assessment per PR may own execution or publication at a time, across generations. The existing PR admission lock remains the authority. A pending newer request can coexist with the active assessment. If code or context changes, mark applicability obsolete immediately, persist replacement intent, and cancel/drain old execution before dispatching the new generation. A checkpoint still uploading is waited on using the existing bounded pending-checkpoint/reaper contract; never race its publisher.

Add a focused supervising job, proposed name `run_code_review_recheck`, deduplicated by assessment publication key. Use the existing `continue_session` worker/runtime path, with transactional message/inbox/job dispatch through `CodeReviewRecheckStore`, then poll/harvest the bound thread turn. Under the PR/thread admission locks, bind the assessment and its agent-result identity to the selected thread, expected turn, prompt digest, and durable dispatch key before execution. Resume a recorded dispatch after a supervisor retry; never infer a new send from the absence of a collected result.

Close the existing send/enqueue gap through transactional binding of assessment dispatch, message/inbox insertion, and job enqueue, or a durable outbox/reconciler that repairs that exact dispatch without inserting another message. Audit `createMessageInTx` and `EnqueueWithOpts` rather than treating them as one transaction today. Harvest by bound thread, exact turn, and successful completion/message identity, not merely `CurrentTurn >= expected_turn`. Duplicate dispatch, partial completion, and stale lease paths need database-backed proofs. The existing synthesis-repair path is a reuse pattern, not proof of those guarantees.

Pass assessment/result identity and generation through the existing worker/session-executor path where required for fencing, provenance, and attribution. If a typed review context is needed, keep it mutually exclusive with automation, PR repair, and PR feedback; do not fabricate an automation run. Reuse provider adapters, pending-snapshot handling, and exact-head reconstruction. Extract a shared helper only where the audit identifies duplicate correctness logic. Existing automation execution identity, action-delivery receipts, and ownership behavior must remain intact.

Bind `code_review_recheck_dispatches.thread_id` from the completed orchestrator agent result's `thread_id` (currently `state.ThreadID` in structured output). Native resume uses that thread's `session_threads.agent_session_id` and completed turn, with actual provider/model configuration, bound to the installed checkpoint. Never use `sessions.agent_session_id` as the orchestrator identity: the thread-scoped snapshot path can store the parent agent's ID there. A fallback may have selected a sibling thread/model. Resume that exact compatible thread or reconstruct explicitly; record whether native context survived.

At workspace preparation, verify repository identity and `HEAD`, remove residual review workspace changes using the existing reconstruction/read-only mechanisms, reapply auth/allowed capabilities, and use the dependency fingerprint behavior from automation continuation. A dirty or unverifiable source cannot become trusted review context. Reconstruct using a fresh checkout if necessary.

Persist checkpoint provenance atomically with the installed checkpoint key, including the assessment/agent-result/thread/turn it represents and whether that turn completed. A successful assessment can outlive a failed snapshot upload: the next turn uses the actual older checkpoint baseline and embeds intervening immutable summaries, or reconstructs. It must not resume older native state while claiming it contains the latest decision. Snapshot publication after a sibling reviewer turn cannot masquerade as an orchestrator checkpoint. Publish the final coherent checkpoint after reviewer runtimes have drained.

Require a durable successful-turn receipt bound to the dispatch and exact thread turn under the actual job lease and assessment/conversation fence. Prefer existing agent-result, message, and job records if they can provide this contract. The slice 2 audit must establish an atomic completion boundary or an equally strong recoverable protocol; use a narrow transactional hook where needed. Session status, latest message, a turn counter, and `OnTurnComplete` alone are insufficient. A retry finding committed completion consumes it without repeating the turn. Lost-lease completion is rejected; checkpoint failure and agent failure remain separate outcomes. The selected narrow dispatch ledger provides this contract; general execution/attempt tables are not required.

Extend the existing owned-session guard to cover code-review generations. User sends, thread creation, preview/workspace mutation, retry/start-over, archive, and other session mutation paths must reject active review-owned sessions with a link to the PR's Re-check/Force fresh controls. System review turns enter through the fenced controller. Cancellation remains available. Retirement keeps ownership until active execution and sandbox holds have drained, then releases it; an old worker cannot clear a newer generation's owner. Enforce the same restrictions in internal tools and runtime capability resolution.

## Database contracts

Migrations 000293 through 000296 implement these additive contracts. All new tables have `org_id uuid NOT NULL REFERENCES organizations(id)`, typed Go enums with validation tests, tenant-scoped store methods, normal FKs, and indexes supporting their actual readers. Use composite `(org_id, id)` keys/FKs for tenant-owned references; additionally validate PR/repository/session consistency in the owning transaction. Lifecycle tables use updates; configuration uses versioned insertion. Narrow ownership triggers enforce message, preview, and thread-structure fences alongside application checks.

### Assessments

Add `code_review_revision_assessments`, aligned with the scheduling design:

- `id uuid PRIMARY KEY`; `repository_id`, `pull_request_id`, `policy_id` UUID FKs; `generation bigint NOT NULL`; nullable reserved `conversation_id`, and tenant/PR-bound `source_assessment_id`, `previous_assessment_id`, and `previous_published_assessment_id` UUID FKs. `source_assessment_id` always points directly to the immutable full baseline supplying code evidence; `previous_assessment_id` preserves immediate lineage. `previous_published_assessment_id` identifies the latest prior publication being updated, which can be a later recheck rather than the full baseline.
- Immutable `base_sha`, `base_ref`, `head_sha text NOT NULL`, `input_version integer NOT NULL`, and code/contract/intent/visual/request/gate/aggregate digests as text. `input_manifest jsonb NOT NULL` contains captured source identities/content references and completeness flags, not credentials. Capture inputs before selecting the route; an unavailable capture remains pending work rather than a fabricated manifest.
- `review_scope text NOT NULL` checked to `full|evidence_only`; `result_origin text NULL` checked to `executed|reused|evidence_only`; `route_reason text NOT NULL` with typed reasons matching the routing table; `coverage_complete boolean NOT NULL DEFAULT false`.
- `status text NOT NULL` checked to `reserved|running|publishing|completed|superseded|failed|cancelled`; nullable typed decision/acceptable, `risk_reason_details jsonb`, validated structured outcome, rendered body, and failure detail. `superseded` terminates unfinished obsolete work. A completed assessment keeps its terminal status and immutable inputs/results; later applicability is represented by the PR's current-assessment projection and nullable `superseded_by_assessment_id`/`superseded_at` operational fields, without rewriting its decision.
- `publication_key text NOT NULL`, GitHub review ID/URL, publication state/receipt JSON, and created/completed timestamps. Publication state is `not_started|reserved|uncertain|confirmed|not_required`; receipts distinguish persistent-summary updates from formal approval, with each output marker, submitted commit, input digest, and returned/reconciled GitHub identity.
- Unique `(org_id, pull_request_id, generation)` and `(org_id, publication_key)`; partial unique `(org_id, pull_request_id)` for active `running|publishing`; history index `(org_id, pull_request_id, created_at DESC, id)`.

Same-code new evidence and deliberate force-fresh intent advance the PR assessment generation. Keep that separate from the conversation generation counter. Matching redelivery/equivalent active intent does not advance either counter.

### Conversation ownership and continuation receipts

The first release uses `sessions.code_review_owner_pr_id` instead of a separate `code_review_conversations` table. Each full review creates a fresh session; compatible evidence assessments retain that session and record their full baseline and immediate predecessor. The nullable `conversation_id` assessment field is reserved and is not an authority source in this release.

A completed, fully covered full assessment claims the session under the same transaction as completion, after checking that threads, containers, previews, runtimes, and executors have drained. Generic message, resume, preview, thread creation, and archive paths reject review-owned sessions. Database guards close the race between application preflight and message/structure mutation. A completed replacement full assessment can retire the earlier owner only after the old runtime has drained; reconciliation retries deferred releases.

`code_review_recheck_dispatches` supplies the selected narrow execution ledger. It binds one assessment to the actual session, orchestrator thread, expected turn, immutable prompt digest, user-message ID, inbox identity, job ID, and job lease. Dispatch inserts message, inbox, job, and receipt atomically. Completion records the exact assistant message and thread/session state under the current job lease. A per-lease/provider-launch usage map retains observed failed and successful attempt usage; unavailable usage remains null. The reused root session retains its full-review usage, so legacy session accounting does not overwrite or charge baseline execution again.

The first recheck reconstructs from assessment-linked reviewer outputs, findings, and validated synthesis. Out-of-line outputs are loaded completely before applying the prompt size limit; missing or oversized context routes to full review. A later native continuation requires the preceding completed recheck's exact thread/provider ID and turn, the same installed snapshot, unchanged code/contract, and a drained runtime. Parent-session provider IDs cannot authorize native continuation. Real provider behavior still requires the pilot validation below.

Assessment IDs are added to existing results, findings, and prompt records. Historical full evidence remains immutable and is read through its assessment link. Every visual snapshot uses an assessment-specific key. Fresh publication checks rediscover sources and fetch image bytes without replacing that immutable snapshot. General execution and attempt tables are not introduced. Referenced job identities are retained by cleanup; execution-message identity is validated transactionally because the partitioned message table cannot supply the desired composite FK.

### Scheduling and policy integration

Add nullable `active_assessment_id`/`current_assessment_id` to `code_review_pr_state`, and nullable `assessment_id`/`retry_of_assessment_id` to `code_review_requests`. Preserve legacy session links for response compatibility; they are not assessment identity. Request records stay immutable as intent; membership/lifecycle links can advance under the PR lock.

Migration 000296 extends the `code_review_requests.mode` CHECK from `ensure_current|review_now` to include `recheck` and `force_fresh`, alongside `CodeReviewRequestMode.Validate` and serialized pending-input readers. The request handler supports both new modes when the conditional-recheck capability is enabled; unsupported activation is rejected. Migration rollback after new modes are written requires data-aware recovery, not simply restoring the old CHECK.

Add `continuation_policy jsonb NOT NULL DEFAULT '{}'` to versioned `code_review_policies`. Initial fields are `enabled boolean` and `automatic_evidence_rechecks boolean`, both default false. Preserve field presence through PATCH, historical restore, PUT compatibility, and internal writers. Automatic rechecks require continuation enabled and the corresponding backend capability. Turn cap and snapshot limits are runtime safeguards, not product settings in this version.

The migrations create parent composite keys before dependent FKs and add cyclic nullable links after their parent tables exist. Disposable PostgreSQL tests apply the migration chain and exercise tenant/relationship constraints. Job cleanup retains rows referenced by continuation receipts while still deleting unrelated expired jobs. Production rollout and populated-data rollback remain operational validation gates; do not treat destructive test-database rollback as a production rollback procedure.

## API, UI, and compatibility contracts

All routes retain existing auth, CSRF, org context, repository access, and standard response/error envelopes. Members/admins can request compute; viewers/builders cannot. Reviewer mentions preserve the signed-webhook trust checks, including `author_association` of OWNER/MEMBER/COLLABORATOR and bot/app filtering; those checks are not a live GitHub permission lookup. Preserve organization credential selection. Requester identity is audit context, not an instruction to use personal credentials.

| Surface | Proposed contract |
| --- | --- |
| `GET /api/v1/code-review-policies` | Add effective `continuation_policy` plus `capabilities.conditional_recheck` and `capabilities.automatic_evidence_recheck`. Capabilities report actual API/worker support; org enablement remains policy. |
| Existing policy PATCH/PUT/restore | Add the two presence-aware fields. PATCH remains admin-only and version-fenced. Reject unsupported activation with `409 CODE_REVIEW_CAPABILITY_UNAVAILABLE`. |
| `POST /api/v1/pull-requests/{id}/code-review/requests` | Preserve `ensure_current` and `review_now`; add `recheck` and the already-proposed `force_fresh`. Body: `{request_id: UUID, mode, reason?: string}`. `recheck` fetches inputs and bypasses timing, but does not inherit explicit-request forcing; it returns unchanged reuse or selects evidence-only/full via the planner. `force_fresh` requires a nonblank reason of at most 2000 characters, retires the conversation once drained, and runs a full panel. Both preserve eligibility/capacity/authorization and the endpoint's prior-review requirement. |
| Request response | Preserve `{data: {request_id, session_id, disposition, schedule}}`; add nullable `assessment_id` and `source_assessment_id`. Use 202 for queued/joined and 200 for completed reuse/redelivery; IDs are null until allocation. `disposition` retains existing queued/joined/reused/cancelled values. A full fallback is a successful queued request, not an HTTP error. |
| `GET /api/v1/pull-requests/{id}/code-review` | Add `current_assessment`, `active_assessment`, and `latest_failed_assessment` summaries: ID, status, review scope, route reason, source ID, conversation/session link, decision/reasons, input head, and publication state. Failed attempts remain separate from completed coverage. Keep existing schedule fields. |
| New `GET /api/v1/code-review-assessments/{id}` and `/evidence` | Immutable assessment detail/evidence, including source references and exact executions/turns. Existing code-review session URLs retain historical meaning and never silently redirect to the latest assessment. |
| Existing targets endpoint | Retain pending-queue semantics in this slice. The current review list/detail joins assessment summaries for each PR without changing queue cursors or manufacturing extra legacy session rows. |
| SSE | Add optional `assessment_id` and `generation` to `CodeReviewUpdatedEvent`; retain optional session/PR IDs. Notify after commit. Clients refetch authoritative assessment/current-review state. |

Use existing 400 invalid-request, 403 role denial, 404 inaccessible resource, 409 request-ID conflict/ineligible/capability-unavailable errors. Owned-session mutations add `409 SESSION_CODE_REVIEW_OWNED` with the owning `pull_request_id` and a user-safe link to review actions. GitHub/evidence outages stay accepted pending work when possible, with existing operational status/retry presentation.

Place **Re-check PR** on completed review detail and current PR review rows. For an eligible case, helper text says “Check updated evidence using the previous code review.” After admission, show “Checking updated evidence” or “Running a full review” with a concise reason. Evidence view shows “Code review reused from assessment …” and the actual new evidence/decision. Native/reconstructed details belong in technical evidence, not the main action label. **Force fresh review** sits in the action menu with its reason field.

The GitHub rolling comment can link to `/code-reviews?recheck=<assessment-id>`; opening/unfurling the link performs reads only. An authenticated page shows the PR and requires a button click for the POST. Preserve legacy `review_now=<session-id>` links. A repeated request UUID survives an uncertain HTTP response. Render with shadcn components, TanStack Query, and nuqs.

Before enabling continuation, migrate current-result readers together: admission and historical-approval checks, retry validation, rolling/formal review output, list/detail/evidence, disputes, review-history tools, analytics, and SSE invalidation. Audit `GetBySessionID`, `GetByOutputKey`, `GetLatestByPullRequest`, `GetLatestByPullRequestHead`, `GetLatestCompletedByPullRequest`, `GetLatestSubmittedByPullRequest`, `HasApprovedByPullRequest`, `HasPriorDeterministicEarlyStop`, and session-result aggregation. Include raw scheduling SQL (`RepairMissingWakes`, `StaleActiveSessions`, `HasActiveCodeReview`), the metadata joins in `internal/db/code_review_disputes.go`, latest-review analytics in `internal/db/code_reviews.go`, and `internal/worker/code_review_status_comment_handler.go`. The current active-thread check helps serialize reused sessions, but it does not replace assessment admission while a recheck is reserved or publishing. Use one current-assessment projection, with explicit legacy fallback only for PRs without new assessment records. Retain historical queries for historical questions.

Implemented reader boundaries:

| Consumer | Assessment behavior and preserved history |
| --- | --- |
| Recheck admission and duplicate requests | Reads immutable full baseline, latest completed assessment, and active assessment under PR admission fencing. A request UUID cannot force a second execution. |
| Scheduling and recovery | Active assessments block competing review dispatch. Terminal settlement clears only the matching active assessment, preserves newer pending input, restores prior completed coverage after failure, and repairs missing wake/fallback work. |
| Automatic spending after approval | `HasApprovedByPullRequest` recognizes confirmed completed assessment approvals as well as legacy submitted approvals. |
| Review list/detail and history tool list data | Each session row projects its latest completed assessment decision, body, reasons, publication, filters, and ordering. Assessment summaries are displayed only on their matching session/head. A latest failed/superseded attempt is exposed separately with an immutable-detail link and does not replace completed coverage. No second legacy session row is created. |
| Immutable assessment evidence | New ID-based detail/evidence routes expose baseline findings/results, current visual evidence, citations, and exact continuation receipt. Legacy session evidence remains scoped to its full baseline. |
| GitHub publication and rolling status | Full/recheck publication reads the latest confirmed assessment predecessor. Rolling status uses the latest applicable evidence assessment; prior bodies and receipts remain locally immutable. |
| General PR disputes | Intake selects the latest completed assessment decision and body and snapshots its assessment ID/risk reasons in durable queue signals. Triage uses those reasons even if another assessment finishes later. Inline finding disputes retain the original finding's source review and reasons. |
| SSE | Assessment identity/generation accompanies the existing update event; detail and evidence query keys share the existing `code-reviews` invalidation prefix. |
| Statistics and analytics | `GetReviewStats` and `GetReviewAnalytics` intentionally continue counting historical session rounds. Rechecks retain per-attempt usage separately and never replace the baseline session's token usage. They are not counted as newly executed full panels. |
| Legacy full-review control reads | Session/output-key metadata and session-wide harvesters continue describing the full review. The recheck supervisor uses assessment/dispatch identities instead of inserting new metadata or harvesting the latest session message. |

Keep `run_code_review` as the full-review controller. New full runs capture an assessment/input manifest before dispatch and bind result/evidence rows to it; wrap legacy `CompleteReview` and assessment finalization in one transaction under the assessment fence. `CompleteReview` is a store update today, not an existing multi-record transaction to assume. Legacy in-flight jobs lacking complete input capture remain historical and cannot supply reusable baselines.

Use the small recheck supervisor only for evidence-only assessments. Do not create a second legacy metadata row for the reused session, reset its completed full review, or feed new recheck rows into session-wide legacy harvesters. Share assessment-aware evaluation/publication helpers between the two controllers. Deploy compatible readers before new writes; historical full-review evidence stays frozen and conversation links remain separate from assessment links.

## Publication, concurrency, and recovery

Refresh the authoritative target, current policy and membership, gates, description/evidence identity, and ownership before publishing. New evidence captured during execution cannot be substituted into the existing assessment. Mark its result superseded or retain it as historical, and queue a new assessment of the changed inputs. Perform the new intent/evidence digest comparison explicitly; the existing recorded description hash does not enforce freshness.

Reserve publication under the PR lock with assessment ID, generation, input digest, and exact commit. Use the existing GitHub submission/rolling-comment reconciliation with an assessment-specific output key. Revalidate the reservation before external submission, then persist the receipt under the same fence. An uncertain response is reconciled by its output marker before retrying; cancellation cannot permit a second independent publisher to race an unresolved send.

Set `OutputKey` to this assessment's publication key. Resolve `PreviousOutputKey`, `ExistingGitHubReviewID`/URL, previous decision/body/time from the latest prior published assessment (or verified legacy publication for the first transition). This publication predecessor can differ from both the immediate assessment predecessor and the full baseline supplying code coverage. Repeated rechecks must chain the most recent publication rather than always updating from the full baseline's state.

Adapt `submitCodeReviewToGitHub` so its already-published shortcut reads this assessment's receipt, not the reused session's `metadata.GitHubReviewID`. Preserve `GitHubSubmitter.updateExistingReview` for the persistent summary and inline-comment deduplication. Its `ensureFormalApproval` may deliberately create a separate approval using `<OutputKey>:formal-approval`; a second GitHub object is not automatically a duplicate. Preserve that marker-based reconciliation and record summary and formal-approval outcomes separately. Extend the helper's result/receipt plumbing where necessary so a crash between updating the summary and submitting approval retries only the unresolved operation, never marks an unconfirmed approval complete, and preserves historical assessment bodies locally.

Send intent is committed as `uncertain` before the network mutation, outside the completion transaction that could roll back. If inputs changed while the reservation is still provably unsent, supersede the assessment and queue one full fallback. If a send may have happened, changed or unavailable inputs permit read-only marker reconciliation. A missing marker then keeps publication unresolved and blocks competing publication; absence from a GitHub read is not proof that the earlier mutation failed. The implementation records this condition, but an operator reconciliation procedure remains required before production activation. For unchanged inputs, retry the existing marker-aware protocol so a crash between the summary update and a not-yet-attempted formal approval can recover.

GitHub and Postgres cannot participate in one atomic transaction. Submit against the captured head commit and refresh/reconcile after the network operation. If the target advanced, the receipt remains attached to the old assessment/commit and the current view reports it superseded; newer pending work survives. Never retarget an approval to a new head based on conversation continuity. Preserve existing human-review/branch-protection behavior.

Handle these failures explicitly: duplicate delivery; crash after request insertion; crash after dispatch but before result collection; committed result with failed checkpoint; old checkpoint plus newer completed turn; cancelled turn with an incomplete checkpoint; provider session missing; authentication/capacity failure; lost lease; policy disable; PR close; force fresh while running; archive/reset attempts; and ambiguous publication. Extend the bounded reconciliation sweep for stranded assessments, pending wakes, unresolved publication reservations, and ownership release. Expired lease timestamps alone do not prove a runtime has stopped.

## Plan of work and delivery boundaries

Each slice should be reviewable using native Git/GitHub for this repository. Implementation is authorized and underway with GPT-6 Sol agents. Production activation and the automatic-trigger pilot remain gated on the specified verification. No production changes have been made.

### 1. Assessment identity and compatible reads

Add assessment schema/models/stores and the current-assessment projection, with nullable IDs on existing evidence/results and request/state DTOs. Introduce immutable input capture/versioned digests and assessment-specific visual/prompt keys. Add capability-gated assessment capture/finalization to the existing full-review controller, including atomic completion with its legacy metadata. Deploy compatible readers before enabling new full-assessment writes; keep rechecks disabled. The conditional execution/attempt tables are excluded from this slice. New modules should be small files under `internal/models`, `internal/db`, and `internal/services/codereview`, rather than expanding the main worker/page further.

Exit: the existing full-review flow produces an immutable executed assessment without a replacement controller; legacy in-flight runs remain readable. Existing history/queue/dispute/analytics behavior is preserved, new assessment fixtures display correctly, and no reused source cost is counted again. Migration tests cover full-chain application and tenant/relationship constraints. Reader inventory is checked off before activation.

### 2. Focused recheck supervisor and durable dispatch

First resolve the dispatch/completion proof gate and record the minimum selected storage design. Add the small recheck supervisor using the existing thread-send/continuation path, with transaction/outbox recovery, exact-turn harvesting, review-owned conversation guards, and per-attempt usage attribution. Bind checkpoint provenance to the actual orchestrator thread's provider session ID; support pending snapshots and reconstruction. Reuse or narrowly extract automation primitives where the audit demonstrates a need. Add extra execution records only if the selected proof requires them. Keep the full-review handler and its control flow in place; keep external recheck admission disabled.

Exit: internal fixtures run successive orchestrator turns in one review-owned session with separate assessment/result identity. Crashes before/after message insertion, enqueue, completion, and harvesting recover the same dispatch without duplicate sends or stale receipts. A missing snapshot reconstructs; an older checkpoint never masquerades as newer context. Existing automation continuation/action-delivery regressions pass for any touched shared code. Source-only reasoning is insufficient to waive these database/runtime proofs.

### 3. Evidence-only planner, narrow prompt, and publication

Implement a pure routing function over captured facts, the visual-only response schema and fail-closed validator, baseline-reference assembly, escalation-to-full behavior, fresh gate evaluation, and shared assessment publication helpers. Distinguish the code baseline from the latest publication predecessor; reconcile summary and formal approval independently. Add a `.template` under `internal/prompts/templates/` and an exported render function. Budget the complete rendered message, including any embedded reconstruction history, against the existing thread-message bound; separately enforce the selected provider's context budget. Missing/oversized source context falls back to full without silently dropping reviewers/findings.

Exit: same-code missing-screenshot fixtures dispatch one orchestrator execution and zero reviewer executions; the real screenshot is available to the model and cited by ID. Changed code/policy/intent and incomplete baselines select full review. Native and reconstructed execution produce the same allowed response contract, and publication handles races/idempotency.

### 4. Explicit product flow and controlled pilot

Add versioned org enablement, backend capability/kill switch, Re-check PR, Force fresh, immutable assessment views, and GitHub read-only deep links. Extend the request mode enum, database CHECK, pending-input serialization, and API handlers together. Route trusted non-dispute mentions through the same planner; preserve the full-review path for dispute-routed requests. Verify unchanged `recheck` returns reuse without inheriting `review_now` forcing. New org fields default disabled. The kill switch disables new continuation dispatch without disabling readers, completion reconciliation, or ownership release. Requests fall back to full review using compatible new binaries.

Exit: browser/API evidence shows both fast and full routes, pending/error states, permissions, and older assessment history. No caption or UI promises a latency bound. A user can recover through force fresh while preserving audit history.

### 5. Automatic evidence routing after pilot

Add a separately enabled trigger slice for previously monitored, open PRs whose latest relevant result is blocked only on visual evidence. Observe PR description edits and human comment/review-image creation or edits. Events request authoritative rediscovery; route using the same input comparison and admission lock. Exclude 143's own status/review messages and unauthorized bot content. Preserve request delivery identity, bound rediscovery work, and coalesce repeated equivalent evidence updates.

Do not run an LLM for every ordinary comment. Evidence source hashes determine whether there is new work; ambiguous prose/intent changes use full review if a review is requested. Honor pause and automatic-review settings. Keep the existing stop on automatic spending after approval; this slice does not activate broad post-approval monitoring. Edits during an active assessment leave a durable pending recheck. Define deletion/removal as changed evidence for applicability; this slice makes no automatic revocation promise for already published approvals.

Exit: screenshot addition starts one recheck, repeated delivery starts none, unrelated/bot status comments do not cause a loop, active-turn races preserve the latest update, and disabled/pause/post-approval cases do not expand spending.

## Validation and acceptance

Behavior changes require focused tests near the changed code. Use table-driven Go tests, `t.Parallel`, exact expected values, descriptive `require` messages, tenancy checks, and disposable PostgreSQL for locking/transaction proofs. Mocked tests do not establish native provider restoration or production latency.

| Case | Required observable result |
| --- | --- |
| Completed full review; only visual evidence missing; author adds screenshot | Same PR conversation, new assessment and orchestrator turn, zero new reviewer turns; fetched bytes/citations retained; fresh gates determine approval |
| Same URL serves different bytes | New evidence content digest; old snapshot remains inspectable |
| A second assessment at the same head/session | Captures new evidence using its assessment key; cannot restore the first assessment's manifest by accident |
| Missing or irrelevant screenshot | Remains unapproved with the precise missing requirement; no invented evidence |
| Missing/malformed narrow output, incomplete requirements, or stale/unknown image citation | Explicit invalid-response failure; baseline blockers remain; no approval via default description success or baseline synthesis |
| New evidence reveals a concern | Escalation retained, full review queued, no approval from the narrow result |
| Changed head/base/ref/policy/model/intent; legacy/incomplete baseline | Full route with a recorded reason |
| Unchanged `recheck` with a new request UUID or an evidence-only trigger message | HTTP 200 reused; audit identity alone cannot set legacy force or dispatch a panel |
| Trusted direct mention versus dispute-routed mention | Direct path uses the planner; dispute path stays full and retains provenance; both serialize with active rechecks |
| API request-mode rollout and rollback | Enum, database CHECK, pending serialization, capability rejection, and prior-review eligibility agree |
| Advisory findings in baseline | Still visible; never copied as a new reviewer execution or charged again |
| Provider context exists / is missing / has expired | Native resume is measured when successful; reconstruction is explicit; auth/capacity failures are not disguised as successful restoration |
| Snapshot failed after a successful turn | Previous actual checkpoint used with intervening summaries or reconstruction; latest result is not lost |
| Orchestrator fallback was a sibling thread; parent and thread provider IDs differ | Resume the successful result's thread with its checkpoint-bound provider ID/model, or reconstruct; never use the parent's ID by assumption |
| Duplicate request, worker retry, or two workers | One admitted execution/result/publication identity; no lost pending follow-up |
| Crash after dispatch reservation, message insert, enqueue, or exact-turn completion | Recover the same dispatch/message/job or committed result; no second message and no completion based only on a turn counter |
| New push/evidence/policy during turn or publication | Old result never claims current coverage; exact submitted commit is recorded and current work is queued/reconciled |
| Crash after GitHub accepted submission | Recover existing marker/receipt; no blind second review |
| Second and third rechecks, including approval after a summary update | Use the latest published assessment's linkage, preserve full-baseline evidence and historical bodies, and reconcile separate summary/formal-approval markers |
| Reused session already has `metadata.GitHubReviewID` | New assessment still publishes; only its own confirmed receipt can short-circuit |
| User/session tool attempts a competing mutation | Owned-session error; cancellation and controlled force-fresh path remain functional |
| Cross-org IDs and mismatched PR/session/thread links | Rejected by tenancy/relationship validation and constraints |
| Analytics on two assessments in one conversation | Two assessment events, actual new execution usage once, baseline reviewer cost once, historical code-review round semantics preserved |

Run touched-package tests and vet during each slice. The following existing commands are starting points, expanded only for the shared contracts actually changed:

```sh
go test ./internal/services/codereview ./internal/models ./internal/prompts
go test ./internal/db ./internal/worker ./internal/services/thread
go vet ./internal/services/codereview ./internal/models ./internal/prompts ./internal/db ./internal/worker ./internal/services/thread
go test ./internal/services/agent ./internal/services/automations
go vet ./internal/services/agent ./internal/services/automations
make lint-tenancy
```

For PostgreSQL proof, use the existing disposable test conventions and add focused continuation/assessment suites. Existing coverage to retain:

```sh
TEST_DATABASE_URL=<disposable-postgres-url> go test ./internal/db -run '^TestAutomationTargetsPostgres$' -v
CODE_REVIEW_TEST_DATABASE_URL=<disposable-postgres-url> go test ./internal/services/codereview -run '^TestCodeReviewSchedulingLifecyclePostgres$' -v
```

The lifecycle suite creates/drops its own database and needs an appropriately scoped local user. New migration tests must verify FK ordering, terminal immutability through stores, duplicate identity, lease/generation races, and safe rollback on disposable data. Do not run destructive migration-down tests against production.

Frontend verification: focused Vitest tests for the action, assessment evidence, request-id retry, SSE invalidation, and permissions, plus lint of touched files. Because this changes shared API/types and routing, run the full frontend typecheck/lint/build at that integration boundary. Capture native Chrome UI and network proof against the tested head for both continued and full fallback cases.

Before pilot, run an isolated frozen-input comparison of full review versus evidence-only native/reconstructed follow-ups with GitHub publication and external writes disabled. Do not use the live `run_code_review` handler as a historical replay runner: it refreshes targets and can publish. Include passing evidence, irrelevant/malicious evidence, ambiguous intent, incomplete source output, and each supported fallback path. Review every false approval or dropped blocker before activation. Establish one real provider round-trip for every adapter/model combination enabled in the pilot; exclude unvalidated combinations from native continuation rather than claiming provider parity from mocks.

Measure route, routing reason, native context, restore time, agent duration, reviewer executions avoided, incremental token/cost usage, fallback rate, and time from accepted request to published assessment. Compare matched input cases and include restoration/reconstruction costs. Reused baseline cost is historical cost, not new usage. Numeric savings and latency targets remain unset until measured.

## Rollout, idempotence, and recovery

Deploy additive schema and compatible readers first. Enable assessment capture/finalization for new full runs on the existing controller only after reader parity. Keep rechecks disabled, drain or explicitly exclude legacy active review jobs from reuse, then deploy compatible API/worker/session-executor binaries before enabling recheck admission for a pilot org. Existing full review remains the fallback. No historical automatic rechecks or synthetic provenance backfill occur on deployment.

Activation requires the acceptance matrix, disposable DB race proofs, isolated decision comparison, provider continuation/reconstruction proof, current-head native Chrome evidence, and required CI. An authorized pilot then supplies actual latency/cost data and checks that saved-code evidence remains visible through every current-result surface. The automatic trigger capability stays disabled until its own tests and pilot pass.

Disabling continuation stops new native/evidence-only admissions; compatible workers finish or cancel active assessments and reconcile publication/ownership. Retain all new data and readers. A binary rollback to code unaware of assessment identity requires paused admission/processing and drained execution/publication; it cannot safely resume queued new job types or interpret current results. Do not drop populated tables as an operational rollback.

## Open questions and implementation gates

- First-release scope remains button plus trusted non-dispute mentions; dispute-routed requests stay full. Automatic evidence updates follow the pilot. Fable's suggestion to defer all mention routing and Force fresh was not adopted as a validated correctness finding.
- The dispatch ledger and database race tests resolve the local execution protocol. Production activation still requires running the acceptance matrix against deployed API/worker/session-executor binaries and the enabled provider combination.
- The tested intent normalizer accepts simple Markdown image nodes and rejects ambiguous constructs for reuse. Unsupported Markdown and substantive prose changes route full; this conservative behavior may reduce the fast-path rate. Do not broaden the parser without new applicability tests.
- Verify the reader boundaries above with authenticated UI/API flows, including current approval, a failed recheck, source findings, and immutable older evidence. Historical session analytics intentionally retain their existing meaning.
- Establish an operator procedure for ambiguous external publication with changed inputs and no visible marker. Preserve the unresolved reservation until receipt identity or proven non-delivery is established; never clear it merely to unblock a second publisher.
- Confirm which deployed provider/model combinations preserve the required native context and establish baseline checkpoint/restore costs. Source support does not establish live support or performance.
- Legacy baselines default to full review. Any later legacy-import optimization requires its own proof that all input and execution provenance can be reconstructed from stored data.
- Broader incremental code review, deterministic-gate-only reruns, cross-SHA content reuse, general human conversation with owned sessions, automatic approval revocation, warm-container retention, and changes to screenshot/risk policy are outside this delivery.

## Outcomes & Retrospective

The first-release implementation was built locally with GPT-6 Sol agents. The assessment schema, conservative planner, transactional continuation dispatch, focused supervisor, immutable evidence readers, and explicit Re-check PR/Force fresh UI are behind `CODE_REVIEW_ASSESSMENTS_ENABLED=false` and `CODE_REVIEW_RECHECKS_ENABLED=false`. Automatic evidence triggers remain a separate unimplemented delivery slice. A draft PR is being prepared on 2026-09-24; no production changes have been made.

Integration testing found and corrected failures that unit-level planning did not expose: an empty neutralized summary prevented every recheck approval; policy scan mocks needed the new versioned field; immutable visual restore could not serve as publication freshness; original reviewer output could be stored out of line; session ownership changed generic resume expectations; terminal assessments needed scheduler/request settlement; and current publication/approval-history readers needed to observe the new assessment rather than the original full result. The accepted Claude Fable findings remain incorporated. This implementation has not received another Fable review.

Final local verification passed:

- Full tests for the 12 affected Go packages: `internal/config`, `internal/models`, `internal/db`, `internal/prompts`, `internal/services/codereview`, `internal/services/agent`, `internal/services/thread`, `internal/services/automations`, `internal/api`, `internal/api/handlers`, `internal/worker`, and `cmd/server`. HTTP/Redis fixture tests ran with localhost access. Output: `/private/tmp/code-review-final-tests.log`.
- `go vet` for the same packages, `make lint-tenancy`, and `git diff --check`. Outputs: `/private/tmp/code-review-final-vet.log` and `/private/tmp/code-review-final-tenancy.log`.
- Disposable PostgreSQL 17 tests applied the full migration chain and exercised tenant/relationship constraints, duplicate dispatch, worker reclaim, stale-lease rejection, exact-turn completion, checkpoint identity, job retention, scheduler settlement, and drained owner retirement. These optional database cases ran separately from the ordinary package suite.
- The PostgreSQL supervisor fixture exercised screenshot-to-approval with one continuation and zero new reviewer jobs, malformed response rejection, preserved escalation reasons, deterministic input incompatibility, initial and in-publication-lock input changes, and changed-input uncertain publication with no visible marker. GitHub and provider execution were synthetic boundaries in this fixture.
- Frontend production build, full typecheck, and full lint passed during integration using worktree-local Node 24 dependencies. After the final reader changes, 9 targeted frontend tests, typecheck, and touched-file lint passed again.

Native Chrome reached the local built frontend but authentication/session loading could not complete because no local API/auth backend was running. This is not UI acceptance proof. Actual provider native restoration, isolated model-quality comparison, required CI, authenticated browser/API evidence, and the authorized production pilot remain activation gates. Synthetic receipts and mocks establish control-flow behavior, not provider capability, safety parity, latency, or savings.
