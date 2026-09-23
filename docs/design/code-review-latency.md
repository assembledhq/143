# Design: Reduce code review latency

> **Status:** Partially Implemented | **Last reviewed:** 2026-09-23

Implementation plan based on the read-only production investigation on 2026-09-23. Implementation is in progress; the progress checklist below records completed milestones. No ticket is assigned.

## Purpose and scope

Deliver useful code review results sooner by removing unnecessary environment preparation, avoiding workspace transfers between review stages, and reducing failed dispatch attempts. Preserve independent reviewers, captured policy, reviewer quorum, evidence requirements, backend-owned approval decisions, exact revision checks, cancellation, and tenant isolation.

Start with infrastructure changes that preserve agent inputs and review quality. Evaluate synthesis changes separately on frozen inputs. Treat content reuse as a later dependency on [Code Review Scheduling, Reuse, and Usage Controls](code-review-scheduling-and-reuse.md), which already owns that contract.

This plan covers the built-in code-review flow. It does not change general coding-session bootstrap, previews, per-target automation behavior, production credentials, host capacity limits, reviewer count, or automatic-review timing defaults. Implementation is authorized; deployment and production policy changes remain separate decisions.

## Measured baseline

The investigation examined local source at `0d747b81cf818bc5dce2be02c2597e7c74ba65b8` and verified relevant orchestrator, continuation-handler, and reviewer-prompt behavior in deployed build `7862ce3e683ff25b0075d23b4bde9e24a0e575c5`. Refresh source and deployment identity before implementing.

For Assembled, approximately seven days ending September 23 produced 1,866 attempts: 1,412 completed, 437 stale, 14 failed, and 3 running. Completed reviews took a median 595.6 seconds, mean 629.7 seconds, and p95 1,054.2 seconds from review metadata creation to completion. This excludes quiet-period waiting before creation. The active policy observed during the audit used a 300-second quiet period and a 300-second minimum interval, two primary reviewers, and high-effort synthesis.

| Observation | Median | p95 | Cohort / interpretation |
| --- | ---: | ---: | --- |
| Docker sandbox create/start | Same second | 1 s | 366 paired events; timestamps have one-second resolution |
| Repository clone | 23 s | 33 s | 199 paired events |
| Clone completion through bootstrap completion | 58 s | 85 s | 196 pairs; includes PR checkout and auth, not only npm |
| Snapshot save | 17 s | 31 s | 352 pairs |
| Compressed snapshot size | 467.8 MiB | 490.4 MiB | 352 saves |
| Container start through restored-workspace setup | 13 s | 18 s | 127 pairs; includes setup/auth beyond transfer |
| Last reviewer response through synthesis phase start | 62.3 s | 105.1 s | 1,380 completed reviews with two usable reviewers and one turn/phase per lane |
| Synthesis execution phase | 104.2 s | 304.5 s | Same 1,380-review cohort; includes CLI, model, and tools |
| Synthesis phase end through review completion | 47.7 s | 73.2 s | Same cohort; includes cleanup, harvesting, and publication |

Lifecycle logs covered 258 matched review sessions in the last approximately 24 hours, not the whole fleet. Among 126 matched synthesis executions, 125 restored a snapshot and one started fresh. The reviewer performing bootstrap finished last in 98 of 128 matched completed reviews. On the latest observed build, 772 executor attempts across 174 reviews included 140 capacity rejections, 93 wrong-node rejections, and 62 sibling-container races. These counts can overlap within a review.

Of the 437 stale attempts, 365 had an activity phase begin and none published a review. This is evidence of started work later superseded, not an exact compute or dollar savings estimate. Separately, 250 recent synthesis threads made a median five tool calls, and only five of 1,537 synthesis results in a later seven-day query required output repair.

These are observed overheads, not measured savings from the proposed changes. Do not add stage medians: cohorts differ, reviewer lanes overlap, and removing work from one lane may not shorten the critical path. Raw local evidence was saved under `/tmp/143-review-perf-20260923/`; that directory is temporary and is not a prerequisite for executing this plan. The tables and measurement instructions here are the durable record. Keep raw transcripts, credentials, and tenant exports out of this public repository.

## Current flow and code map

`internal/worker/code_review_handler.go:newRunCodeReviewHandler` refreshes PR state, loads the captured policy and immutable evidence, dispatches reviewer threads, harvests their outputs, dispatches synthesis, and applies backend policy before publication. Reviewers already execute concurrently. The in-flight phase checkpoint already avoids expensive GitHub preflight on every polling pass; preserve it.

`internal/worker/handlers.go:newContinueSessionHandler` hands continuation jobs to session executors. `internal/worker/session_executor_dispatch.go` creates the durable executor and launches its container. `internal/services/agent/orchestrator.go:ContinueSession` then checks local sandbox capacity, worker ownership, workspace reuse, clone/restore, auth, and repository preparation. This ordering allows executors to start only to discover that they must be redirected or retried.

Fresh setup uses `setupFreshSandboxForThread` and `PrepareSandboxRepository`. The latter applies configured dependencies and runs `.143/config.json` bootstrap commands. Assembled currently declares `bash .143/bootstrap-npm.sh`, which can invoke `npm ci` through `.143/preview-install.sh`. The built-in reviewer template explicitly prohibits tests, builds, and linters, so this installation is unnecessary for its read-only task. Synthesis's primary thread is Main and is not currently marked read-only in the same way as fallback reviewer threads; a mode check limited to reviewer thread flags would miss it.

Reviewers share a sandbox, with an exact branch/head readiness barrier in `waitForSandboxWorkspaceReady`. A successful continuation saves a snapshot before its handler persists thread completion. The last runtime can release and destroy the sandbox before synthesis is dispatched; synthesis then restores it. `codeReviewWaitingForReviewers` and `codeReviewWaitingForOrchestrator` normally retry after ten seconds. `internal/worker/worker.go` has a five-second polling fallback and an existing wake channel.

Other implementation anchors:

- `internal/services/agent/providers/docker.go`: `Create`, `CloneRepo`, `Snapshot`, `Restore`, and `Destroy`.
- `internal/services/agent/hydrate.go`: restore/retry behavior.
- `internal/db/session_sandbox_holder_store.go`, `internal/models/thread_runtime.go`, and migration `000152_shared_sandbox_thread_runtimes`: durable holder leases.
- `internal/services/agent/sandbox_capacity.go`, `sandbox_gc.go`, and `reconciler.go`: admission and cleanup.
- `internal/db/jobs.go`: durable jobs, lease-fenced retries, post-commit `Notify`/`Wake`, and worker capacity selection.
- `internal/services/agent/activity_phases.go`: execution-phase boundaries.
- `internal/prompts/templates/code_review_orchestrator.template`, `internal/models/code_review.go`, and `internal/services/thread/service.go`: synthesis input, model/effort policy, and the 256 KiB message limit.

## Progress

- [x] Trace the flow and collect production timing evidence.
- [x] Verify relevant deployed source and document measurement limits.
- [x] Write the prioritized implementation and validation plan.
- [x] M0: Add missing stage instrumentation and establish repeatable comparison cohorts. Production post-change comparison remains pending deployment and sufficient matched reviews.
- [x] M1: Skip unnecessary dependency/bootstrap work for built-in reviews. Implementation, Claude milestone review, and a bounded missing-artifact replay are complete; production measurement remains a rollout gate.
- [x] M2: Preserve the workspace across reviewer-to-synthesis handoff. Local implementation and Claude milestone review are complete; production handoff-latency and occupancy measurement remain rollout gates.
- [ ] M3: Prepare one workspace and resolve dispatch placement before executor launch.
- [ ] M4: Wake the review controller when durable results become ready.
- [ ] M5: Optimize cold checkout if it remains material after M1–M4.
- [ ] M6: Evaluate cheaper synthesis on an isolated replay corpus.
- [ ] M7: Connect supersession reduction to the existing scheduling/reuse plan.
- [ ] Record matched production results, quality checks, recovery behavior, and rollout decisions for each enabled change.

## Plan of work

### M0 — Make stage timing attributable

Add structured start/end timing around dispatch, executor boot, capacity admission, workspace preparation, clone, auth, bootstrap, restore, snapshot, result persistence, controller harvesting, and GitHub publication. Reuse zerolog and existing observability/metrics helpers. Correlate logs by org, review session, thread, executor/job attempt, role, model, reasoning effort, build SHA, and workspace generation/container identity where available. Keep high-cardinality IDs in logs rather than metric labels. Distinguish the executor container from the sandbox container; ingestion metadata must not overwrite the application identity.

Distinguish cold, reused, and restored workspaces; attempted work from successful work; and final agent output from a validated, durable result. Activity phases include model and tool activity and must not be labeled pure model latency. Fix session-executor service labeling: sampled events currently appear as `service=unknown`, so worker-only queries miss them. Do not log prompts, environment variables, or credentials.

Acceptance: a single review can be reconstructed from request eligibility through publication, including retries and overlapping lanes. Missing events are marked unknown. Record stage distributions and total latency independently. M0 can ship alongside M1 and does not require a new analytics product or telemetry table.

### M1 — Use minimal setup for built-in code review

Introduce an explicit internal repository-preparation mode for platform-owned review work. Resolve it from trusted review-session metadata and the authorized reviewer/synthesis role; never from prompt text, labels, or a caller-supplied flag. Cover the Main synthesis thread, fallback reviewers, fallback synthesis, and repair turns. Fail back to existing preparation when the role cannot be established.

In this mode, retain repository access, exact PR-head/base validation, Git credential bootstrap, agent auth, prompt/evidence delivery, and required platform tools. Skip repository dependency installation and arbitrary repo bootstrap commands. Route both fresh and restored code-review paths through the same policy. Preserve full preparation for ordinary sessions, previews, and unrelated automation turns. Do not change Assembled's shared bootstrap script to solve a 143-specific review problem.

Add focused tests near `internal/services/agent/orchestrator_internal_test.go`: eligible review roles never invoke dependency/bootstrap commands; Main synthesis is included; ordinary sessions still prepare; stale heads and unavailable required inputs still fail; cold, restored, and reused paths agree. Missing generated artifacts or dependencies needed to establish a finding must result in explicit incomplete evidence, not an unsupported clean result.

Acceptance: replayed built-in review setup runs zero npm-install/bootstrap commands, preserves the same authoritative checkout and evidence, and leaves ordinary-session behavior unchanged. Measure the reduction in preparation time and snapshot bytes. The observed 58-second span is the optimization target, not a promised per-review saving.

### M2 — Retain the review workspace between stages

Add a bounded, durable code-review holder for the existing sandbox. Acquire it before the final reviewer can release the last runtime hold. Reviewer completion must leave the workspace available for synthesis on the owning node. Reuse the normal snapshot/rehydration path when the holder, container, owner, or authoritative workspace is no longer valid.

Use a proposed `code_review` holder kind in `session_sandbox_holders`, keyed by the review metadata ID. Acquisition and renewal must validate the active review, matching org/session/container, and a fenced owner token. The existing `CreateActive` upsert can replace a lease token and is not by itself a safe ownership-transfer primitive; add a conditional acquisition path for this kind. A poll/controller attempt can renew a still-owned lease, but a delayed attempt must not revive a superseded review or overwrite a successor's ownership.

Proposed initial bounds: a 60-second lease, renewal at most 20 seconds apart while healthy, and at most 120 seconds of idle inter-stage retention. These are design starting values, not measured production settings. A long provider/GitHub wait should release idle capacity and fall back to recovery. Active thread holders remain independently authoritative. Expiry alone does not prove that an old process stopped: fence state writes and check active runtime ownership before cleanup or reuse.

Integrate the new kind with all holder readers, turn release, `FinalizeContainerDestroy`, GC, restart reconciliation, failure, timeout, cancellation, supersession, dead-letter, and normal completion. Release is idempotent and cannot destroy a container held by a preview or sibling runtime. The holder must survive the executor process that created it; it cannot be an in-memory pin.

First ship reuse while retaining existing checkpoints. Then measure the smaller snapshots produced by M1 and decide whether any remaining checkpoint can move off the result path. Preserve recoverability and immutable reviewer outputs; do not remove checkpoints solely because the workspace is expected to be read-only. Any asynchronous snapshot must hold its own lease and must not race a mutating runtime or container destruction.

Acceptance: healthy reviewer-to-synthesis transitions retain the same container and perform no restore. Crash, drain, expired lease, missing container, owner loss, and head supersession use safe recovery. Every terminal path eventually releases its review holder, with no increase in leaked containers or stuck reviews. Measure handoff latency and live-container occupancy together.

### M3 — Prepare one workspace and dispatch to its owner

Land this in two independently reviewable slices. First, reread durable session ownership before `maybeDispatchSessionExecutor`. Redirect to a healthy owning node before launching an executor; retain the existing runtime-side ownership and capacity checks to handle races. If no workspace exists, use the existing capacity selector before dispatch rather than waiting for a new executor to discover a full host. A capacity observation is advisory, not a reservation, and must never permit oversubscription.

Advisory pre-dispatch placement runs for code reviews on upgraded workers. The executor's runtime ownership and local capacity checks remain authoritative if the placement observation becomes stale.

Second, make cold review initialization a single durable job per active review session, before reviewer fan-out. Introduce proposed job type `prepare_code_review_workspace` in the existing queue, with a dedupe key containing the review/session identity and recovery generation. The payload contains org, review metadata, session, and expected generation; the handler reloads authoritative revision and policy. The job prepares the repository once, attaches the M2 holder, and publishes readiness only after the required setup and exact-head checks succeed. Both reviewer jobs then target the recorded owner and reuse the workspace.

The preparation handler is registered on every upgraded worker, while `CODE_REVIEW_WORKSPACE_PREPARATION_ENABLED` defaults to false. Enable placement first, then enable preparation only after the entire agent-worker fleet can claim the new job type. The controller requires both switches before enqueuing preparation jobs; this permits a compatible-worker rollout without scattering prepared reviewer work across nodes.

The controller's preparation wait is bounded from the first durable preparation enqueue for that review generation, independent of any earlier GitHub retry window. A prior enqueue also checkpoints preflight: pending preparation polls use durable review/session state and repeat the GitHub sync once after readiness, before reviewer fan-out. Sessions with an existing snapshot or a preparation that exceeds the wait limit use the ordinary reviewer recovery path. A timed-out controller cancels the preparation job's lease before falling back. Host GC identifies each unpublished preparation container by its attempt's lock-token label, preserving it only while that exact lease is live and reclaiming leaked siblings.

Use the job lease and session container/generation compare-and-swap for publication. Do not hold a database transaction across Docker, Git, or network operations. A stale initializer cannot publish after lease loss, and can destroy only its own unpublished losing container. A succeeded initialization job is not proof that its old container still exists; recovery revalidates the workspace and creates a new fenced generation when necessary. Keep the readiness barrier as defense in depth, with readiness defined for the chosen preparation mode.

Do not claim that an in-process reservation coordinates separate executor processes. A shared hard capacity-reservation authority is deferred to the existing scheduling plan's capacity stage; this slice improves placement and removes duplicate initialization without weakening admission. Include queued/retrying initialization in cancellation and reconciliation so a stopped review cannot later dispatch reviewers.

Acceptance: two simultaneous reviewer lanes create one sandbox and one checkout on the healthy path. Known wrong-node/full-node placement is redirected before executor launch. Real PostgreSQL concurrency tests prove dedupe, lease-loss fencing, crash recovery, and cancellation before readiness. Report capacity rejections, sibling races, wrong-node redirects, executor launches, and time to first reviewer execution per completed review.

The current completion log reports time to first reviewer **thread start**, a proxy for execution start; it does not measure first agent token. Derive production percentiles from review/session-scoped logs after rollout and compare them with M0 before marking the latency acceptance complete.

### M4 — Advance on durable result completion

Persist the validated thread result and terminal turn state, then make the existing `run_code_review` job runnable and notify workers through the existing job notifier. Preserve the polling path as recovery for lost notifications. The controller continues to enforce reviewer quorum, fallbacks, synthesis validation, live revision checks, and publication ownership.

Do not enqueue another job behind an active dedupe key and assume it will execute. Use a transactionally coordinated, lease-aware wake of the existing pending job; when it is running, ensure the completion event cannot be lost between its state read and retry scheduling. Specify that race with a real database test before choosing the final wake implementation. Any new pending-wake marker required by that implementation must be documented in the schema section before coding it.

Do not publish directly from an agent stream or treat the last token as completion. The process must finish, output must be validated, and required result/read-only checks must be durable. M2 permits retaining the sandbox while the next stage proceeds. Keep checkpointing and cleanup correctly fenced if they are decoupled from result readiness.

Acceptance: duplicate notifications dispatch neither an extra synthesis nor a duplicate GitHub review. Lost notifications still progress through reconciliation/polling. Test completion racing retry, cancellation, head change, and worker restart. Proposed unloaded-test target: p95 durable-result-to-controller wake under two seconds; report GitHub/preflight time separately rather than counting it as queue latency.

### M5 — Optimize the remaining cold checkout

Start with the narrow change: add a code-review checkout path that fetches the required graph/refs and checks out the authoritative review head once, avoiding materializing the default branch first. Keep ordinary `CloneRepo` behavior unchanged. Preserve exact base/head checks, merge-base history, fork handling, token scrubbing, and useful history access. Do not replace the current partial clone with an insufficient shallow clone.

Test branch and fork PRs, unavailable SHAs, changed base refs, missing history, submodules, and cancellation. Compare the same repositories and revisions under equivalent network/cache conditions. If clone remains material, separately design a host-local Git object cache with org/repository scoping, immutable inputs, locking/GC, credential hygiene, and cache-ABI invalidation. No cross-tenant shared working tree or global pool of authenticated warm containers is part of the first rollout.

Acceptance: the prepared head and merge base match the existing path, agents retain required history, and cold checkout improves on matched workloads. Keep the existing path when the experiment offers no meaningful gain.

### M6 — Evaluate synthesis separately from infrastructure

Create or use an isolated frozen-input replay harness that cannot publish, mutate production review state, or supersede live heads. Do not run historical cases through the live `run_code_review` handler. Freeze PR/body/base/head context, reviewer outputs, policy, visual evidence, prompt version, and every model/effort selection, including fallbacks.

Compare the current high-effort synthesis with a lower-effort synthesis-only variant and, independently, a compact evidence package with relevant diff excerpts. Preserve complete applicable policy/evidence and the full rendered message's 256 KiB limit. On overflow, use the existing incomplete-evidence behavior or a separately specified retrieval strategy; never silently truncate evidence to fit. Reviewers retain their current roster and explicit per-reviewer efforts.

The existing `agent_roster.reasoning_effort` supplies primary synthesis effort and can also be inherited by reviewers. A synthesis-only trial must preserve/materialize every reviewer's effective effort; verify fallback effort resolution too. Use existing versioned policy semantics if a live trial is later selected. No policy setting changes occur merely by implementing the replay harness.

Use a human-adjudicated corpus that includes P0/P1 findings, policy blockers, missing/ambiguous visual evidence, disputes, injected instructions, and incomplete reviewers. Compare blocker recall, unsupported approvals, schema validity, evidence citations, tokens, and latency. Approval-rate equality alone is not quality proof. Proposed promotion gate: no missed adjudicated blockers or new unsupported approvals in the corpus and at least a 20% median synthesis-phase reduction, followed by a bounded shadow trial. Report corpus size and uncertainty. Reject the candidate when quality cannot be established.

### M7 — Reduce superseded work through the existing reuse contract

Use the measured 365 started-and-superseded attempts to prioritize Stage 2 of the scheduling/reuse plan. Keep the current quiet period as the comparison baseline; longer waiting is not the default speed optimization. Preserve explicit Review now behavior.

Follow that document's conservative source-tree, base-tree, merge-base-tree, policy/prompt/roster, request-context, and visual-evidence equivalence rules. Same SHA alone is insufficient, and equal patch text alone is insufficient when surrounding code changes. Missing or incomplete inputs are cache misses. Reuse complete findings, including blocking findings, without treating historical approval as authorization for a new revision. Revalidate live publication gates and ownership against the current head.

This milestone depends on the existing assessment/reuse schema and API design and must not invent a parallel cache in this plan. Measure avoided starts and added request delay separately. Report missing token/cost usage as unknown; do not extrapolate an exact savings percentage from stale-status counts.

## Database and API contracts

M0, M1, M3's placement checks, and M5 require no database schema or public API changes. M0 uses existing logs/metrics and durable review, activity-phase, and executor records. M1 adds an internal typed `code_review` session-message source in the existing text column, written only by the platform review dispatcher and repair path; older untagged in-flight messages retain full preparation. M3's preparation job uses existing `jobs` payload, dedupe, target node, lock token, status, and lease columns; its job type is a new internal contract requiring compatible workers before ingress is enabled. Job payloads never override authoritative tenant/review state.

M2 proposes one additive migration, numbered at implementation time: extend `session_sandbox_holders`' holder-kind CHECK with `code_review` and add its typed Go constant/validation plus `internal/models/enum_db_sync_test.go` coverage. Reuse the existing columns: UUID `id`, `org_id`, `session_id`, `holder_id`, `lease_token`; text `container_id`, `holder_kind`, `owner_node_id`, `status`; and timestamptz `heartbeat_at`, `expires_at`, `created_at`, `released_at`, `updated_at`. Preserve the organization/session FKs, nonempty-container constraint, statuses, and active/draining partial unique index on `(org_id, session_id, holder_kind, holder_id)`. No new trigger is proposed. The application validates that `holder_id` is the code-review metadata ID for the same org/session; all new tenant store operations take and filter `orgID`.

The new review-holder acquisition/renewal/release operations must be conditional on the current lease and review ownership. Holder cleanup must honor every live holder kind, not only `thread_runtime`. A rollback must drain/release new holders before removing CHECK support; do not run a destructive down migration against active holders.

M4 is intended to reuse existing job state and notifier APIs. The running-job/lost-wakeup race is an explicit design gate: if the existing transaction/lease operations cannot encode it safely, specify an additive pending-wake field and its transitions here before implementing that slice. Polling remains correct until then.

M6 reuses the existing typed roster and versioned policy contract. An experiment must not silently change reviewer inheritance. M7's schema, assessment identity, routes, auth/RBAC, pagination, and SSE contracts remain owned by the linked scheduling/reuse design. No new public routes, request/response fields, error shapes, or SSE payloads are proposed by M0–M5. Routine implementation should not update public docs; any later user-visible scheduling/policy/API expansion follows repository documentation rules.

## Validation and measurement

Run focused verification for each delivery slice from the repository root. Select relevant cases while iterating, then run the affected package suites. Commands below are planned validation, not results already obtained:

```sh
go test ./internal/services/agent ./internal/services/agent/providers
go vet ./internal/services/agent ./internal/services/agent/providers
go test ./internal/worker ./internal/db ./internal/models
go vet ./internal/worker ./internal/db ./internal/models
make lint-tenancy
```

Only run the worker/DB/model suites and tenancy lint when those areas change. Use table-driven parallel tests with isolated fixtures, `require` assertions, and descriptive messages. Follow existing PostgreSQL integration-test conventions in a disposable local database for holder and job races; mocks alone cannot establish concurrency correctness. Do not use production mutations to test recovery or performance. General session, preview, cancellation, fallback, stale-head, and publication-idempotency regressions are mandatory for lifecycle changes.

Refresh the production baseline with `make db-query` and bounded `make logs-query` windows. Filter tenant tables by the selected organization and join on both org/session identity. Use `code_review_session_metadata.created_at/completed_at` for attempt latency; correlate agent-result thread IDs with `session_activity_phases` for execution, and use immutable executor-attempt/log boundaries for retries. Restrict phase comparisons to single-turn/single-phase executions or explicitly reconstruct multi-turn timelines. Do not use mutable thread `started_at` or executor row initialization timestamps as a substitute for actual execution/boot events.

Report both request-to-publication and creation-to-completion; keep quiet-period delay visible. Include all terminal outcomes and unfinished/censored attempts alongside completed-review latency so faster failure does not look like improvement. Match treatment/control by repository, PR size, model/effort, policy, trigger, host load, and cold/reused/restored state. Keep cache state, snapshot bytes, execution starts, and resource use distinct. Prefer concurrent canaries or frozen paired replays over an unmatched before/after median. Attribute effects to one change at a time.

Proposed initial measurement gate: at least 100 comparable completed reviews per infrastructure variant plus all failures/supersessions in that window; label tail estimates provisional if the cohort remains small. Require the targeted stage to improve, review outcomes/recovery checks to pass, and no unexplained deterioration in end-to-end p95, failure rate, or capacity pressure before widening. This is a rollout criterion, not a claim of statistical power. Set an explicit comparison threshold before each experiment and record its result below.

## Rollout, idempotence, and recovery

Deliver M0/M1 first; they provide the clearest bounded change. M2 can follow independently, then M3 builds on its durable holder. M4 can start once result durability and holder ownership are established. Re-measure before investing in M5. M6 and M7 are separate quality/data-model efforts and do not block the initial speedups.

Use the repository's existing rollout mechanism where suitable; document any new internal gate before adding it rather than inventing production environment settings in this plan. Ship compatible schema/readers/cleanup before writing a new holder kind or admitting the new preparation job. Mixed old/new workers must not claim a job they cannot handle. Keep each change independently reversible: restore full preparation, disable new warm holds after draining existing ones, fall back to existing continuation initialization, preserve polling, or restore the prior synthesis policy version.

Every retried action must reconcile durable state before creating another sandbox, holder, agent turn, or GitHub review. Never treat notification delivery, a cached readiness flag, lease expiry, or an existing approval as sufficient ownership/coverage proof. Production secret changes, deployment, merging, and model-policy adoption remain separate actions from this planning request.

## Surprises & Discoveries

- Docker create/start is already fast in the sampled logs. Dependency/bootstrap work and repeated workspace transfer are stronger initial targets than a generic warm-container pool.
- Existing reviewer parallelism and in-flight GitHub preflight suppression are already implemented; preserve them.
- The 58-second setup span includes checkout/auth. It must not be reported as a pure npm benchmark.
- Primary synthesis uses Main, so review-role detection cannot rely only on read-only thread flags.
- Snapshot archives retain Git and dependency/build state for recovery. A review-specific optimization must not globally change snapshot exclusions.
- Executor logs use `service=unknown` in the sample. Some readiness-related error text describes cancellation; count it as cancellation, not a workspace timeout.
- Output-schema repair was uncommon in the audit. It is not a first-priority latency project.
- This checkout has no `.agent/PLANS.md`, `docs/AGENTS.md`, or `docs/exec-plans/` convention. The plan follows `docs/design/AGENTS.md` and retains living progress/decision/outcome sections.

## Decision Log

- 2026-09-23: Prioritize preparation, workspace reuse, and dispatch correctness before changing reviewer models. Production measurements identify substantial overhead outside execution, and these changes can preserve review policy.
- 2026-09-23: Keep quantitative observations separate from proposed targets. Stage cohorts overlap, and no optimization has been benchmarked yet.
- 2026-09-23: Retain checkpoints when introducing warm handoff; reassess snapshot policy after removing unnecessary dependencies and measuring the new archive size.
- 2026-09-23: Reuse existing holder/job machinery with explicit fencing. Do not present process-local capacity counters as cross-process reservations.
- 2026-09-23: Preserve the existing scheduling/reuse design as the authority for content equivalence and resource budgets. The new baseline supersedes historical measurements, not that design's safety contract.
- 2026-09-23: Keep synthesis experiments isolated from live publication and hold reviewer efforts constant.

## Outcomes & Retrospective

Planning outcome: seven optimization areas are captured with source anchors, measured baselines, delivery order, schema/API boundaries, and validation gates.

M0 implementation: paired stage boundaries now cover the review controller, executor launch and boot, workspace preparation, agent turn, result durability, and GitHub publication. Stage end events carry bounded outcomes; best-effort setup is marked `attempted`, genuine capacity pressure `waiting`, and cancelled workspace readiness `cancelled`. Configuration and live-counter failures remain `failed`; a dedicated capacity-pressure error preserves the existing broad capacity match. Controller dispatch/harvest stages are named `*_check` because a polling pass can do no work. Workspace stages carry the initially selected cold/reused/restored source; a failed restore followed by cold creation emits an explicit final-source correction. Correlation fields include org/session, review/head, thread, job/lock token, running and dispatched build SHAs, and distinct executor/sandbox container IDs where available. The service label for session-executor logs is explicit. Reviewer role is recorded on durable-result events and is added to agent-stage logs when M1's trusted lookup succeeds. Legacy `container_id` logging is preserved alongside `sandbox_container_id`.

The existing seven-day Assembled baseline above is the pre-change cohort. A repeatable post-change comparison will group completed reviews by repository, PR size, model/effort, policy, trigger, host load, and workspace source, while reporting all failures and supersessions separately. Stage spans and end-to-end latency will be reported independently; controller `*_check` events will not count as actual dispatches. M0's instrumentation itself did not target a latency reduction. Record each later deployed build and matched post-change cohort before claiming an end-to-end delta.

M1 implementation: a tenant-scoped lookup joins active review metadata to the persisted agent result's thread identity and validated reviewer/synthesis role. Only that verified role, paired with a valid review revision context and platform-tagged code-review input for the turn, selects minimal preparation; mixed or human follow-up input keeps full preparation. Missing, ambiguous, stale, or unavailable ownership also retains full preparation. Synthesis currently persists its agent result shortly after dispatch, so the orchestrator rechecks a missing role immediately before repository preparation; if still missing, it safely uses full setup. M2/M3 must retain this fallback or make result-before-dispatch durable. A role found at the later check is carried into repository-preparation timing logs. The minimal path skips repository-declared dependencies and bootstrap commands on fresh/restored review setup. Git credential bootstrap, agent auth, exact-head checkout, and ordinary-session preparation remain on their existing paths. Initial `RunAgent` turns always use full setup because built-in reviews start idle and enter through `ContinueSession`, where platform message provenance is available.

Reviewer, synthesis, and synthesis-repair prompts now explicitly preserve limitations from missing generated artifacts or dependencies. The synthesis contract's `unresolved_uncertainty` field produces a backend risk reason that withholds approval when set. Focused tests cover trusted role resolution, per-turn message provenance, repository setup selection, declared dependency/bootstrap suppression, prompt composition, production wiring, and legacy executor log fields. The touched Go package suites, `go vet` on those packages, and `make lint-tenancy` passed; focused tests for the final review fixes passed. A full `go test ./...` run reported a repeatable deploy dashboard telemetry-query assertion outside this change and a preview cache assertion that passed when rerun alone. Claude's read-only M1 review used the `opus` alias, which reported `claude-opus-5`, and found no P0/P1; its final two P2 observations were addressed with full initial-run setup and retained executor `container_id` logging.

M1's missing-artifact gate used a disposable synthetic repository pinned at base `d6cebbb7890f1c0de6b91e11325b7fa1d4200687` and head `9f3e977327739bcf279022b81f0bf27ca1f20199`. Its one-line authorization change depended on `generated/role-capabilities.mjs`, which was absent because minimal setup did not install the private `@private/roles` dependency or run generation. Read-only Claude `opus` reviewer run `claude-code-read-only-frozen-f150e8b4` identified that exact evidence gap and returned an incomplete review, not a clean finding. A separate frozen synthesis run `claude-code-read-only-frozen-8ce2fb31` preserved it as `approval_recommended=false` and `unresolved_uncertainty=true`. Both runs reported `claude-opus-5`; neither used the live review handler or published anything. A focused backend regression test verifies that a complete structured synthesis with this flag produces the `unresolved_uncertainty` risk reason and withholds automated approval. This is a bounded synthetic prompt-behavior replay, not proof of every model, repository, fallback, or production execution path. No matched end-to-end latency or snapshot-size delta has been measured yet.

On 2026-09-23, the first production M1 check observed build `7522e7c4` skip repository preparation in five cold reviewer turns (median 0.004 ms) and three restored synthesis turns (median 0.005 ms). In the same two-hour log window, the prior build `08cd5578` had 11 successful cold review setups at median 53.637 seconds (44.592–71.415 seconds) and seven restored setups at median 0.166 seconds. The observed cold setup saving is about 54 seconds per reviewer turn; parallel reviewer lanes, capacity retries, and small post-deploy samples prevent an end-to-end speedup claim. Five observed M1 sandbox-create stages succeeded in 0.274–0.384 seconds.

M2 implementation: the new `code_review` holder is acquired under the active review and session row locks before a successful built-in reviewer turn releases its sandbox. Concurrent reviewer lanes extend the same holder without replacing its lease token; each completed turn starts a new idle-retention epoch, including when an expired holder has not yet been swept. Controller attempts renew a live, same-owner holder at most once per 20 seconds, capped at 120 seconds from that latest turn. A successful synthesis turn releases the exact matching holder before its turn hold ends, allowing immediate container destruction; the synthesis role is verified against the persisted agent result and cannot rearm reviewer retention. Terminal reviews and graceful owner-node drain also release holders; GC expires holders when their lease, owner node, revision, container, or review state becomes invalid. An idle review container is reclaimed only after all turn, preview, runtime, holder, and queued/running executor-job checks pass. GC now obtains all referenced containers and the review-only subset in one database scan. Reuse rechecks the durable container identity during readiness waiting and takes a strict container-ID compare-and-swap so a delayed turn cannot republish a container that GC cleared. A reclaimed-workspace retry conditionally resets an unheld session only when its container remains null, preserving any successor lane's running status. Normal snapshot creation and cold recovery remain in place. Focused tests cover holder maintenance before turn release and the controller's terminal release/active renewal; a disposable PostgreSQL 17 test exercises concurrent and staggered acquisition, synthesis release, renewal, stale-head expiry, queued-job gating, terminal release, delayed-reuse fencing, and one-time finalization. The PostgreSQL 17 test passed ten consecutive runs after the final cleanup fixes. Claude's first M2 read-only review used the `opus` alias, which reported `claude-opus-5`, and found no P0/P1; its three P2 findings prompted the direct coverage, synchronous synthesis cleanup, and named GC grace bound. This is local safety evidence, not a production handoff-latency or occupancy result.

A subsequent read-only Claude review found a generic reuse-path race: a strict container-ID CAS loss had treated a live replacement as a cleared ID, potentially retrying a duplicate turn. The recovery path now re-reads the durable ID, diagnoses a live replacement before deciding whether to retry, and uses the stale-container retry sentinel only for a cleared ID. Focused tests cover a cleared ID, a live replacement, and a successor appearing during reset. The redundant old-ID predicate was removed from `ResetAfterLostReuse`. These changes passed touched Go package tests, `go vet ./...`, `go build ./...`, and ten disposable PostgreSQL integration runs; M2 is not yet deployed or measured in production.
