# Session lifecycle: Milestone 0 migration contract

Status: source-derived design and characterization companion, not a deployed
contract. Baseline: `ffe919dcf63807795eb85e1aa24cebe26ef36521`. This document
implements the design-output portion of Milestone 0 in
[the ownership plan](session-lifecycle-ownership.md). Proposed tables, methods,
API outcomes, bounds, and deployment gates below do not exist merely because
they are specified here. No production inspection or operational evidence is
included. Milestone 0 changes no runtime behavior or schema.

The executable writer inventory is produced by `cmd/lint-lifecycle`; its
function-level manifest records reviewed syntax candidates, not a whole-program
proof. The grouped source-audited maps below supplement it with transaction
composition and migration decisions. Review the scanner's limitations as well as
its output after rebasing, before each slice, and when a caller is added.

## 1. Characterized behavior and deliberate changes

Evidence references use paths below `internal/` unless prefixed with `models`,
which also means `internal/models`, or `migrations`. They name `file:function`
plus baseline line; the executable manifest carries exact full paths.
“Preserve” means existing successful semantics, not an assertion that existing
failure handling is safe. Tests remain against existing entry points.

| Entry/state | Existing contract and evidence | Required migration result |
| --- | --- | --- |
| Session send, running | `api/handlers/sessions.go:SendMessage:4167`: insert at session current turn + 1, no claim/job/question answer; comments add a transaction. Images or references without text are valid. | Preserve session attribution, references, plan-mode prefix, comments and 201 message response. Record input identity without making it a thread send. |
| Session send, idle/resumable | Same function `:4231`: session claim -> message -> comments -> latest compatible input/question answer -> session-key job in one transaction; job target is `SessionWorkerTarget`, priority 5. Resumable statuses: completed/pr_created/failed/cancelled/awaiting_input/needs_human_guidance (`models/session_enums.go:80`). | Preserve successful attribution/targeting. Add exact answer readiness/binding; dedupe no-op alone cannot answer. Destroyed workspace remains 410. |
| Thread send, normal | `services/thread/service.go:SendMessage:755`: ownership preflight, client dedupe, limit check, thread claim then parent claim; message/inbox transaction precedes pool enqueue. Thread turn = current + 1; nil human actor is valid for system inputs. | Atomic claim/message/input/comment/dispatch; remove compensating status resets. Preserve source, changeset holder, post-success action and author mode. |
| Thread send, running | Same function `:792`: queue-only, turn current + 2, inbox and pending count; optional live-delivery notification failure does not reject committed input (`:915`). | Preserve accepted queue and fail-soft wake; generation-bound ownership arbitrates live delivery against drain. |
| Thread send, capacity | `queueMessageWaitingForSlot:1009`; active pending/running/awaiting_input counts against three slots, four visible tabs maximum (`models/session_enums.go:523`). | Preserve durable queue, do not report execution failure or exceed capacity. Reconciler finds it independently of a sibling wake. |
| Thread limits/dedupe | `service.go:54`, `validateInboxAdmission:1161`: 256 KiB message, 200 pending/thread, 1000 pending/session; client identity scoped to thread, not message text. | Preserve limits and idempotent return; enforce counters under admission locks. |
| Structured answer/denial | `humaninput/service.go:Answer:114`, `Cancel:225`: request validation, compatibility question, resume claim/message/job, notify after transaction. `validateCheckpointReady:313` currently checks parent status/key/pending key only. | One answer/binding operation for answer, denial and immediate/queued free text. Pending wait never grants or answers. Capability approval must use caller transaction. |
| Legacy question answer | `api/handlers/sessions.go:AnswerQuestion:3749`. | Remains answer-only; no provider/request ID fabricated and no implicit continuation. |
| Review comment send | `api/handlers/session_review_comments.go:SendToAgent:339`: busy fallback returns `sent=false`. | Preserve busy fallback; do not turn it into queued execution. Comment resolution/claim/message/job atomic when sent. |
| Inbox replay | `thread/actions.go:RetryInboxEntry:205`, `db/thread_inbox_store.go:RetryRecoverable:579`. | Preserve explicit `allowUnknownDelivery` authorization/audit; no automatic replay of uncertain delivery. |
| Checkpoint retry/start-over | `api/handlers/sessions.go:RetrySession:2167`: distinct modes, default checkpoint; `retrySessionStartOver:2214` uses ResetForRetry/run_agent; checkpoint chooses `GetRetryTarget` and continuation note. | Preserve failed-only retry API, notes/job types and unavailable/pending checkpoint errors. New paused-input recovery is separate and request-scoped. |
| Session end | `sessions.go:EndSession:4539`: idle-only, completed, no PR publication; read then unconditional update currently races. | Same idle-only/no-publication response; add start/end linearization and retained-owner rejection. |
| Thread end | `thread/service.go:EndThread:1610`: pending/idle/running/awaiting_input -> completed, review-owner preflight only. | Preserve eligibility/response; add durable end epoch, retained owner checks, predecessor drain and delivery boundary. New sends can queue for successor, not reach predecessor. |
| Cancel | `thread/actions.go:CancelThreadTurn:69`, session handler `CancelSession:4582`: intent, then local/remote/queued delivery; response is not proof of process exit. | Preserve exact-turn identity and worker targeting. End is not cancel; denial is not process interrupt. |
| Runtime projection | `agent/thread_runtime_delivery.go:keepSessionRunningIfSiblingRuntimesActive:420` protects parent using sibling state; reaper/worker/PR code also writes status. | Preserve sibling capacity and own result receipts. Reject stale shared projection; do not reset successor to idle/failed. |
| Slack continuation | `worker/handlers.go:enqueueSlackSessionContinuationMessage:6283`: primary-thread lookup, pool message insert then pool continuation enqueue with thread key; no shared claim/readiness transaction. | Preserve actor/reference/thread attribution, fix insert/enqueue gap through canonical admission. This is not already equivalent to thread admission. |
| Slack input answer | `worker/handlers.go:handleSlackHumanInputAnswer:6649`: authorize/validate, pool AnswerPending, external chat.update, then pool job enqueue; no checkpoint check, capability grant or session/thread claim. A chat.update failure leaves answered input with no continuation. | Deliberately add shared readiness, real grant/claim and exact dispatch binding; commit before external update. Retrying update alone cannot repeat admission. |
| Linear prompted | `worker/handlers_linear_agent_prompted.go:appendPromptedMessageAndEnqueueContinue:256`: reserve bridge/comment + message + session-key enqueue in tx; running append uses locked append state. Terminal revisions policy and activity dedupe remain adapter-owned. | Preserve revision-disabled outcome and exact comment identity; enqueue no-op must not falsely prove a consumer. |
| Worker queued answer | `worker/handlers.go:answerQueuedHumanInputForContinue:9651`: worker selects latest pending free-text request at start; caller `:10126` carries answered ID in memory. | Retire this owner; same durable request ID/binding survives retries. Preserve legacy semantics until complete floor, never silently pick a new request after cutover. |
| Capability request | `api/handlers/agent_capabilities.go:InternalAgentCapabilitiesHandler.Request:57` (status write :71) and `services/agent/orchestrator.go:handleHumanInputPause:9152` occur at different readiness stages. | Request-created/awaiting_input is not proof. Final pause/diff/checkpoint provenance precedes readiness; missing approver fails construction. |

Intentional fixes are failure atomicity, dedupe-as-ownership, request readiness,
end/start/seed/live-delivery fencing, asynchronous visible recovery, and stale
projection rejection. They are not characterized baseline guarantees.

## 2. Concrete identities and additive representation (proposed for 0a)

Reuse `jobs`, messages, inbox entries/batches, activity phases, runtime/executor
leases, domain receipts and integration ingress. Existing
`db/continuation_recovery.go:RecordContinuationAttemptWithLease:149` records
continuation runtime/message/task IDs; `InspectContinuationRecovery:123` checks
exact provider outcomes. `db/session_activity_seed.go:StartSeedPhase:18` already
materializes *selected* legacy seeds and tracks applied inbox receipts. Preserve
those proofs; neither is an admission/end epoch or request-specific checkpoint.

Choose these additive records, not a second general execution/event ledger:

| Record | Columns and invariants |
| --- | --- |
| `sessions` | `admission_epoch bigint NOT NULL DEFAULT 0`, `end_epoch bigint NULL`, `lifecycle_version smallint NOT NULL DEFAULT 0`; nullable `admission_claim_dispatch_id`/`admission_claim_token` identify an owned logical admission claim, distinct from derived sibling-running projection; nullable recovery ID/token identify exclusive workspace recovery. Finite pending-upload receipt binds publication/upload ID/key/epochs/generation/token for exact Set/Save/Promote/Clear. Current descriptor includes generation/revision, exact publication key/producer/receipt and snapshot/physical availability versions, not A's provider identity. Session end increments admission epoch and records ended epoch; accepted successor increments again. |
| `session_threads` | Same admission/end/version and exact logical admission-claim dispatch/token columns, plus `draining_runtime_id uuid NULL`. Claim token is recorded in the admission receipt/dispatch and changes on a replacement claim; it is not provider authority. Thread end increments only lane epoch; session end fences every lane through parent epoch. New lane admission never changes a running sibling's epoch. |
| `session_input_dispatches` | `id uuid PK`, `org_id`, `session_id`, nullable `thread_id`, `session_epoch`, nullable `thread_epoch`, nullable `message_id bigint`, nullable `request_id uuid`, `kind` (message/answer/denial/initial/retry/recovery), `ingress_kind`, `ingress_key`, `payload_digest`, `validated_payload jsonb`, `admission_state` (waiting/bound/terminal/historical), `consumer_kind` (none/job/runtime), nullable `job_id`, nullable `runtime_id`, nullable `job_lock_token`, nullable `runtime_lease_token`, nullable `admission_claim_token uuid`, nullable `admission_claim_turn int`, `normalization_version`, `next_attempt_at`, `deadline_at`, `reason`, `created_at`, `updated_at`. Finite current parked-control fields hold receipt/version/state, exact fixed binding/native identity/epochs/generation, revoked execution-token snapshot, no-launch proof/reason and once-only refund reference; they outlive disposable execution preparation. This is ONLY epoch/consumer/required-admission/control metadata; it never stores delivery/applied/uncertain status. Inbox owns those states. One row per exact input, never per dedupe family. Initial/retry/control-only dispatches need no message/inbox. |
| `session_human_input_requests` | `admission_session_epoch`, `admission_thread_epoch`, `producer_runtime_id`, `producer_job_id`, `producer_attempt_token`, immutable `legacy_request` (created before this session's activation), `proof_policy_version`, `enforcement_version`, `classification_version`, `readiness_state` (unclassified/ready/waiting/recovery_required), `readiness_deadline_at`, immutable `readiness_proof jsonb` final-pause/provider anchor + anchor/context-scope digests, `recovery_receipt_id NULL`. Anchor includes own lane/request/turn/provider type/build/native conversation or explicit continuation-mode identity/workdir scope/drain and finalization metadata, never current sibling producer. Context-scope digest identifies scope, not immutable provider bytes. Nullable native_context_observation_id is installed only on actual observation publication; absence before first publication is not loss. Nullable legacy columns until proved; no “use current epoch” default. |
| `session_runtime_start_preparations` | One CURRENT execution preparation per fixed job/input binding, not attempt history: org/session/lane/request/dispatch/job IDs and epochs; preparation ID/token/state/deadline, exact target/descriptor/generation/provider/build/native identity; finite current check. Park atomically copies the durable control snapshot/version to the bound dispatch and sets job status. Closed preparation can be deleted only after that capture; control never depends on its row, token deadline or old lease. Retry consumes dispatch control then creates fresh execution token; binding/native identity/generation remain fixed. |
| `session_input_workspace_compatibility` | One row per pending request, real org/request/session FKs: `org_id`, `request_id` unique, `anchor_digest`, `context_scope_digest`, state (`certified`/`pending`/`unavailable`/`unprovable`), `workspace_generation`, `workspace_revision`, `snapshot_key`, exact latest `workspace_state jsonb`, `last_authority_kind`, `last_authority_receipt_id`, reason and due revalidation time. EVERY authorized workspace change advances EVERY pending request's watermark/state in the same transaction; pending may span many changes. Missing/stale/corrupt certificate becomes that request's unprovable state, never a veto of another authorized operation. Authorized same-session sibling/domain metadata preserves by default under the explicit trust model; named context-affecting operations require their own disposition. Immutable lane/provider/phase/drain anchor never moves. |
| `session_input_recoveries` | Exact org/session/initiating thread/request, immutable acknowledged request/version set, waiting-intent IDs and known-unlaunched parked dispatch/control-version set with digest, original epochs/classification versions, idempotency key/digest, mode, checkpoint key/provenance, `workspace_generation`, `workspace_revision`, sibling-set digest, new epochs, state/version, current dispatch ID, attempt number, previous failed receipt/version, isolation/destruction receipt, failure code and timestamps. New attempt is a new version/dispatch, not a second supersede of original request set. |
| `jobs` payload | Add versioned `lifecycle_binding_version=1`, immutable admission epochs and exact `input_dispatch_ids`. First release: answer/denial job binds ONLY that request intent and its associated answer message/inbox; no unrelated ordinary input in that job. Ordinary-only jobs may atomically batch selected unbound messages. Preserve queued-message/task/recovery/domain/holder/target/action/purpose identities; one input cannot bind two consumers and an existing payload never gains new input. |
| Snapshot cleanup jobs / current upload receipt | Before any Save, create one typed `snapshot_object_cleanup` job per fresh, never-reused key and link its ID from the publication/upload receipt. Immutable cleanup payload binds org/session/publication/upload/key/token/epochs/generation. Add nullable typed `snapshot_object_control jsonb` on jobs ONLY for this job kind: object state/version, upload writer identity/settlement receipt, upload deadline, superseded_at, delete_after, tombstone/version, blocker/retry/attention. This finite per-object resource receipt is outside input-job payloads, not a new object/refcount/event table. Retain the job until exact deletion settles; unknown writer/delete cannot disappear through generic budgets or terminal retention. |
| Runtimes/executors/batches | Add immutable admission epochs and dispatch IDs at allocation/claim. Runtime allocation also records immutable `job_id`, `job_attempt_token`, `allocation_digest` alongside existing runtime ID/lease; together these are the immutable allocation receipt. Add write-once `provider_terminal_result jsonb`, digest and settled_at for exact own provider outcome without changing shared current result. Lease-loss/aborted-allocation observations have separate typed immutable receipt slots and cannot consume or overwrite provider_final. Add bounded launch fields on the existing allocation/continuation-attempt receipt: write-once first_provider_launch_marker, monotonic invocation ordinal and current invocation nonce/purpose; first marker is never cleared. Every Execute consumes a fresh fenced invocation CAS. Existing renewable leases govern current mutation, not historical settlement provenance. Add nullable immutable `thread_runtimes.drain_proof jsonb`: receipt ID, exact runtime/lease, provider process identity, exit/stop acknowledgement and observation authority. Authorized observer writes it only after actual process drain; expiry cannot create it. |
| `session_lifecycle_cutovers` | One row per org/session: `org_id`, `session_id` unique, `version`, `state` (shadow/normalizing/active), `legacy_input_cutoff_id`, `normalization jsonb`, activated_at/minimum_writer_version and tested resumability capability tuple (provider,pinned CLI build,ResumeIdentity variant,verification protocol/release receipt). Normalize bounded cursor/ranges and at most one historical-uncertainty descriptor only after floor/drain. No historical range becomes runnable. Missing tested tuple defers version0 activation; no downgrade of active sessions. |
| Writer compatibility metadata | Add `lifecycle_writer_version` to existing node/executor registration and their heartbeats; a dedicated `lifecycle_writer_instances` lease table covers API/ingress/standalone writers absent from node registry (`instance_id`, role, build, version, last_heartbeat, expires_at). This is compatibility metadata, not an execution ledger. No tenant/customer operational inventory is stored in repository documentation. |
| Existing integration receipt rows | Add nullable `lifecycle_dispatch_id`, `lifecycle_outcome`, `lifecycle_outcome_version`, `notice_job_id`; waiting answers retain exact validated payload/actor/request digest in dispatch payload. Reuse existing jobs for notice retries. Recovery-required/expiry notices have unique job key `lifecycle_notice:<dispatch-or-request>:<version>`. |

All new tenant tables have real organization/session/thread/request/job FKs where
applicable and org-scoped methods. Message ID uses `(org_id,message_id)` uniqueness
and transaction-side tenant validation; add composite tenant FKs only with matching
unique referenced indexes. Never add an unscoped ID-only lookup to simplify this.
`ingress_key` is caller-supplied client ID, Slack webhook delivery+action identity,
Linear bridge+comment ID, exact domain reservation or server-generated command ID;
content digest detects conflicting reuse, it is not a dedupe key by itself.

Indexes: unique `(org_id,ingress_kind,ingress_key)`; unique `(org_id,message_id)`
where message nonnull; unique `(org_id,request_id)` where kind answer/denial and
admission_state bound (waiting attempts use ingress identity, with winner selected
under request lock); pending scan `(next_attempt_at,id)` where admission_state
waiting; per-org/session lane scan `(org_id,session_id,thread_id,admission_state)`; job/runtime
reverse indexes `(org_id,job_id)`, `(org_id,runtime_id)`; request readiness index
`(readiness_deadline_at,id)` where status pending and readiness waiting;
recovery unique `(org_id,request_id,idempotency_key)` and one active recovery per
request. Schema CHECKs require exact epochs and dispatch consumer identity for
version 1 bound rows; a request-associated binding has exactly its one answer/
denial intent (its message/inbox is the same intent), never an unrelated ordinary
dispatch. Store exact logical claim dispatch/token in admission receipt/dispatch
metadata for conditional release. Migrations are additive and retain version 0 reads
before activation; no migration or index has been executed in M0.
Compatibility table has unique `(org_id,request_id)` and session lookup
`(org_id,session_id,request_id)`; terminal request cleanup removes its compatibility
row only after retention/audit policy, not its immutable anchor.
Start preparation is unique by `(org_id,job_id)` with exact fixed dispatch
association and CAS of current claim/preparation/control version. State is
preparing/authorized/closed, separate from typed
current check disposition verified/retryable/unavailable/unsupported. Index
`(next_attempt_at,id)` where preparing and `(org_id,session_id,thread_id)` for
current reservations; due parked-control scan uses its control_state/version,
not expired execution deadline. Proposed typed job status awaiting_start_control
is non-runnable with no lease; generic claim/reclaim/dead-letter/failure scans
exclude it. Dispatch stays bound with awaiting_control metadata/control version.
Models/status CHECKs and all queue consumers must recognize this additive state
before activation. It remains NONTERMINAL for per-input/lane dedupe, ownership
and competing-consumer checks, despite having no runnable lease. Ordinary input
may be durably accepted waiting, but cannot acquire a competing consumer until
the parked binding is retried, abandoned or authoritatively closed.

Use shared typed job-status policy at the DB boundary, with operation-specific
scope/authority joins; do not copy one broadened status list into every query:

| Policy | Required interpretation |
| --- | --- |
| Runnable | pending plus existing due time/target/version eligibility only; never parked. |
| Lease-owned | running plus exact current lease/token/owner required by that mutation. Park has no such lease; expiry does not prove physical drain. |
| Nonterminal input consumer | Exact input/scope ownership includes pending/running/awaiting_start_control and retained bound control, even with no lease. Used by admission/dedupe/domain competition, not runnable claim. |
| Lifecycle cleanup blocker | Operation-specific nonterminal binding/control PLUS real runtime/provider/physical reservation uncertainty; do not discard parked intent or replace real drain with age. |
| Terminal-only retention/result | Validated terminal status and its exact result/receipt; awaiting_start_control never qualifies. Snapshot cleanup has its own retained resource obligation until Delete settles. |
| Observation / unrelated domain | Report parked waiting/control separately from running resource usage. Keep genuinely unrelated wave/domain state machines separate; an unrelated-domain status is not evidence about jobs. |

The separate executable jobs-status inventory must cover each actual jobs-bound
SQL/Go predicate, including aliased/derived status fields, singleton equality,
negated sets, cleanup and visibility branches. Before activation migrate every
runtime site by its policy, not just `jobs.go`: automation completion/dispatch/
run-result/turn/action, code-review capacity/scheduling/stale-container/terminal/
recheck/workspace recovery, continuation recovery, executor/node/review-loop/
session cleanup, deploy guardrails and worker/domain consumers. Historical SQL
records source evidence and index/constraint migrations, not blanket rewriting
of old migrations. A mixed-role query gets per-predicate actions. Wave-only
status predicates are excluded by actual jobs table binding, not guessed names.
This syntax inventory is a floor checklist, not whole-program SQL/type proof.
Its reviewed sibling ledger is `cmd/lint-lifecycle/job-status-inventory.json`;
`job_status.go` fingerprints complete lexical queries and mixed-role predicate
facts. Discovery `go run ./cmd/lint-lifecycle -discover-job-status` does not approve
policy; the normal checker validates ledger coverage/drift. Dynamic SQL, renamed/
imported helpers and raw Go query-variable forms retain the documented manual
source-audit gate before activation. In particular, automation age/no-lease
completion/abandonment, review-loop no-active restart, all review workspace
recovery branches and executor pre-handoff need their exact parked policy;
physical capacity/node/deploy usage and explicitly nonparkable controller jobs
must not be broadened blindly.

The existing `idx_jobs_dedupe` (`migrations/000001_init.up.sql:122`) covers only
pending/running; `JobStore.HasActiveByDedupeKey:279` and
`GetActiveByDedupeKey:307` use that same incomplete set. In 0a install a replacement
unique partial index with the SAME `(queue,dedupe_key)` scope and predicate
`dedupe_key IS NOT NULL AND status IN ('pending','running','awaiting_start_control')`.
Build/validate the broader index before dropping the old one or permitting the
first parked row; stop activation if duplicate keys prevent its creation. Both
indexes may coexist during floor deployment. Update active lookup/conflict
predicates and enqueue collision interpretation together; `enqueueOn:789`'s
nil-ID collision is still not authority. This key index supplements, never
replaces, key-independent canonical dispatch/control arbitration across competing
key families. Keep runnable claim and lease-reclaim predicates narrow, not the
broader nonterminal exclusion set. No index/status migration has run in M0.

Compatibility
due revalidation uses `(next_revalidation_at,request_id)` where state is pending.
Snapshot availability tombstones and exact-key delete receipts remain auditable
after physical removal; no new native-bundle/refcount/storage-version schema.
Logical claim dispatch pointers use org-scoped FKs/unique referenced keys. In
admission, pre-create the unbound dispatch under already-held parent/lane/request
locks before installing its claim pointer/token; message/job references may be
filled later in the SAME transaction. Do not invent a pointer-first FK exception
or separately committed claim. Failed admission rolls back all these writes.

### Consumer arbitration and discovery

`PendingAdmissionReconciler` is a lifecycle service invoked at worker startup and
every five seconds, independent of Redis/ingress membership. Page 100 due rows,
maximum 500 per pass; keyset `(next_attempt_at,id)` candidate reads without locks,
per-org round-robin and next-attempt backoff capped at 60 seconds. It calls the
same canonical admission operation, never resets another owner to idle. Successful
notification is optional because the durable scan recovers lost wakes.

The following are **proposed post-migration queries**, with parameters, not claims
about today's schema. Execute them in disposable PostgreSQL before 0a activation:

```sql
SELECT id, org_id, session_id, thread_id
FROM session_input_dispatches
WHERE admission_state='waiting' AND next_attempt_at <= now()
  AND (next_attempt_at,id) > ($1,$2)
ORDER BY next_attempt_at,id LIMIT 100;

SELECT d.id FROM session_input_dispatches d
WHERE d.org_id=$1 AND d.id=ANY($2::uuid[])
  AND d.session_epoch=$3 AND d.thread_epoch IS NOT DISTINCT FROM $4
  AND d.admission_state='waiting' AND d.consumer_kind='none'
ORDER BY d.id FOR UPDATE NOWAIT;
```

Inbox `delivery_state` and `applied_at` are the sole delivery truth. Live poller
and drain lock the same inbox plus dispatch rows and atomically bind compatible
consumer identity; a dispatch is not a second delivery state machine. Provider
write is after commit. Ack verifies exact dispatch/runtime/lease/epoch and batch,
not only a sequence watermark. Preserve existing applied-at/batch receipts.
The discovery scan is not admission authority: process each candidate in a fresh
canonical-order transaction, acquiring its domain/job/session/lane locks before
locking and rechecking dispatch state/due time. A lost claim is a no-op. Never
hold a scan's dispatch lock while requesting higher-ranked parent locks. Multiple
reconcilers may read the same candidate; the conditional mutation chooses one.

An enqueue collision yields `NewJob`, `BoundExistingConsumer` or
`PendingReconciliation`, never success inferred from nil UUID. **First release
never rebinds or adds input to any existing job payload.** BoundExistingConsumer
means replay of an already durable receipt for the EXACT SAME input/request,
epochs, digest, job and consumer identity. Otherwise new input gets a new
dispatch-unique job **only after** the same transaction proves no nonterminal
conflicting consumer in its scope; otherwise PendingReconciliation, no new job
and no answer/grant mutation. Scope includes session-wide legacy execution,
same lane's pending/running/retrying/handoff/awaiting_start_control job/dispatch,
its durable parked control even without a lease, provider and uncertain
predecessor, plus actual exclusive wholeworkspace holder/reservation. Real drain
proof, not lease expiry or a different dedupe key, removes a predecessor blocker.
Independently admitted sibling lanes retain the supported three-slot concurrency.
Compatible sibling scope means a different lane, no exclusive wholeworkspace
holder/recovery reservation, and positive current-generation workspace availability
under the same-session trusted workspace/HOME model, not merely absence of a running job. Neither a
pending nor unprovable request certificate creates an exclusive holder or vetoes
another lane's authorized workspace mutation. Dedupe collisions cannot answer/grant.
`JobStore.RetryWithoutConsumingAttempt*` can reset attempts and clear ownership
after actual execution, so attempts=0 is not “never claimed”. Monotonic claim
marker/payload rebinding is a separate future reviewed optimization, not this
cutover. A new ordinary-only job may atomically batch selected unbound pending
messages. Answer/denial instead gets a dedicated request-intent binding, including
its own message/inbox only. Unrelated ordinary input remains its own durable
dispatch waiting/admitted separately; it cannot be dropped or rebound by answer
abandonment. No existing job payload is rewritten for either path.

At runtime-start recheck a previously valid binding may meet a temporary drain/
capacity/recovery reservation blocker. Return strict `StartDeferred{dispatch_id,
reason,next_check_at}`: no permit/allocation/provider/prewarm/running projection,
retain exact bound dispatch, queue retry without consuming execution attempts and
persist due reconciliation independently of job retry/dead-letter budgets. The
dedicated start reconciliation scan also joins bound dispatches and their jobs,
not only waiting/no-job input; stale authorization instead returns conflict and
cannot be deferred into a successor. Deferred is not a failed session or phantom
successful runtime start.
Use proposed explicit `DeferRuntimeStartInTx(ctx,tx,orgID,jobID,claimToken,
dispatchIDs,reason,nextCheckAt)`: lock exact current job lease and bound dispatch,
require no runtime/provider allocation, persist same-input deferral receipt,
set that job pending/run_at and release only its claim. Restore exactly the one
claim-time attempt debit (never zero previous real attempts), leave payload,
dispatch/request and execution purpose unchanged. Idempotent exact receipt replay
does not debit twice. Do not reuse RetryWithoutConsumingAttempt*'s
`continuationRecoveryQuery` path: it may rewrite payload to checkpoint_recovery
after actual execution. StartDeferred has never executed provider and is never
checkpoint recovery. An end/expired/terminal authority change yields a durable
typed control/notice result for already-answered input, not silent job death or
automatic admission to another generation.

| Writer | Authoritative inbox transition × dispatch mutation |
| --- | --- |
| Admission/materialization | Exact new message -> pending inbox; dispatch waiting/no consumer or bound/new job. Session-level legacy seed must materialize its exact inbox row before provider claim; threadless maps only to proved primary lane. |
| Live claim / seed claim | pending -> delivering under same epoch; dispatch binds exact runtime allocation or its already-bound job. Any delivering/delivered/unknown_delivery/acked/applied entry excludes new admission. |
| Provider write | delivering -> delivered only for exact live lease; dispatch unchanged. Lost write response preserves delivered/unknown, never fresh pending. |
| Ack | delivered/delivering -> acked plus applied_at under exact batch confirmation; dispatch identity unchanged; no delivery status copied there. |
| Unsent failure/reclaim | Only proved never-sent delivering -> pending; clear inbox's old runtime under exact reclaim proof and release consumer only if no provider admission. Runtime/dispatch history retained. |
| Delivered owner loss | delivered -> unknown_delivery; dispatch keeps exact owner/provenance, no new consumer. |
| Explicit unknown replay | User-authorized RetryInboxDelivery records replay receipt/version, unknown_delivery -> pending; same dispatch identity receives a new consumer only after exact old drain/uncertainty resolution authorized by replay contract. Never rewrite old job payload. |
| End/recovery supersede | Unconsumed pending -> dead_letter with typed superseded/end reason in same tx as admission terminal marker; delivered/unknown/applied cannot be called unconsumed or silently cleared. |
| Pre-allocation terminal failure | Bound dispatch + authoritative failed/cancelled/dead-letter/skipped job + no allocation -> ProjectAdmissionFailureInTx. Pending inbox becomes dead_letter with exact failure receipt/reason and visible outcome; dispatch terminal, never acked/applied. Superseded epochs return conflict/rollback with no current inbox/status mutation; no F2 stale-success result. |
| Known-not-launched resumability/preparation control | Atomically park exact job awaiting_start_control/no lease, dispatch bound+awaiting_control/version, closed execution prep plus durable control snapshot; pending inbox/request/grant stay committed, no ack/apply. Claim/reclaim/dead-letter/failure sweeps exclude it; no fresh-conversation replay. |
| Explicit abandon of known-unlaunched intent | Terminalize only explicitly user-abandoned, proven-unsent dedicated request intent and its own job (cancelled/control reason, not provider failure); same tx release only its exact logical claim/counter through AdmissionAuthority, sibling-aware/nonfailure. Historical/non-runnable, never delivered/acked; request/grant/audit and unrelated input remain. |

Crash recovery for the pre-allocation terminal-failure row is durable: startup/periodic admission reconciler
pages bound dispatches joined to terminal jobs, excludes any immutable runtime
allocation for that dispatch/job attempt, locks/re-reads exact job outcome/current
epochs and calls ProjectAdmissionFailureInTx. It does not depend on in-memory
dead-letter callbacks or the waiting-only index. Add index
`(org_id,job_id,id) WHERE admission_state='bound'` for the join and cursor over
terminal job completion/id. Succeeded-without-allocation and missing terminal
provenance become visible admission_attention for exact reconciliation, not an
invented failure/provider receipt. Job budget exhaustion cannot silently strand
bound input; retries of the sweep replay the same failure receipt.
The sweep excludes awaiting_start_control jobs and exact durable parked controls
from generic terminal-failure projection; it instead wakes that binding's
durable start-control reconciler. A failed native check before Execute cannot
be converted into unaccepted/dead-lettered input by worker budget exhaustion.
Crash before recording the check remains discoverable by exact bound answer/
preparation/attempt. Reconciler FIRST invalidates old execution lease/permit/
preparation under canonical locks, then inspects write-once FIRST launch markers
on ALL immutable allocation/continuation receipts for that exact bound input.
Marker absence plus no allocation or explicitly revoked unattempted allocation
can park it only if no prior receipt retains unresolved possible acceptance.
Marker presence is possible Execute/unknown even if no outcome;
final permit alone is not attempted-provider evidence. Preserve separate uncertain
physical preparation blocker. None of this follows merely from lease expiry.

Competing key families: `continue_session:<session-or-thread>` (legacy/session,
Slack, Linear, repair and thread); `continue_session_drain:<session>:<message>`
(`orchestrator.go:6634`); `continue_session_review_loop:<loop>:<pass>:<phase>`
(`reviewloop/service.go:536`); `continue_session_push_reconcile:<session>:<revision>`
(`worker/handlers.go:13281`); `code_review_recheck_turn:<assessment>`
(`db/code_review_recheck_dispatch.go:314`); `continue_pr_feedback:<batch>`
(`db/pull_request_feedback.go:QueueContinuation:375`); `run_agent:<session>`; automation
target/run reservation keys. All exclude by exact dispatch ownership, not key
string equality. Replace recheck's generic-key-only conflict probe at `:283`
with canonical `HasCompetingInputConsumerInTx` before reconciler activation.

### Legacy cutoff and normalization

Normalize lazily per session before that session's first active-version command;
background pages use the same bounded procedure. No global rewrite of every
historical user message. Capture cutoff max committed message ID plus activation
time only after old writers/in-flight transactions drain; a sequence ID reserved
before cutoff can otherwise commit later. Store cursor/boundary/digest and receipt
provenance in `session_lifecycle_cutovers.normalization jsonb`, maximum 100 inputs
per transaction/500 per pass. Incomplete normalization keeps that session's new
answer/end semantics inactive and returns visible `NORMALIZATION_PENDING`, not
false acceptance. New compatible ingress remains durably held at the cutover.

Receipt precedence by era:

1. Applied inbox + exact runtime/confirmed batch, then exact finalized activity
   phase containing that seed: historical consumed. Existing
   `continuation_runtime_id`, `continuation_message_id`,
   `continuation_task_message_ids` preserve exact attempt context.
2. Pre-phase/inbox era: succeeded job with exact `queued_message_id` and matching
   org/session/lane, plus an unambiguous corresponding final outcome, proves only
   that exact message. Job success alone or “latest user text” proves no tail.
3. Existing pending inbox with no competing/delivered/unknown consumer is pending;
   a job with exact same-input receipt is bound; naturally live legacy attempts
   drain without rewriting payload. First release never adds input to those jobs.
4. Older transcript without exact receipts is `historical_unproven`, nonrunnable,
   represented by bounded cutoff/ranges in normalization metadata, not a pending
   dispatch per message. The current accepted tail beyond last exact scoped
   outcome boundary gets exact dispatch only when input identity is established.
   If no reliable boundary locates it, create ONE session-level
   `historical_uncertainty` descriptor with cutoff/range, known candidates and
   reason, preserve full transcript, block automatic replay. Do not mark every
   historical message unfinished or silently call the ambiguity consumed.

Historical uncertainty **blocks all ordinary execution for that session**, not
only inferred replay; fresh ordinary sends may be accepted waiting with a visible
receipt but cannot run around it. It is user-visible with `ReviewHistoricalInput` (read-only
transcript/receipt view) and `ReplaceHistoricalIntent` (explicit fresh message,
idempotency key and acknowledgement of descriptor digest/candidate range). The
replacement is new intent under new admission, not inferred replay or fabricated
consent; only acknowledged unconsumed waiting intents are superseded. If old
delivery/grant is uncertain, replacement execution stays blocked for exact
delivery/grant reconciliation. User may leave uncertainty unresolved without data
loss, but no automatic new execution occurs. These actions/notice are 0a cutover
requirements, not an operator instruction to accept hidden loss.
Exact new authority/drain/uncertain-grant checks still apply to replacement;
ReviewHistoricalInput may resolve the descriptor through exact evidence instead.
Neither action resets workspace/transcript or reuses an old approval by default.
ReviewHistoricalInput also permits an explicit authorized no-replay resolution:
acknowledge exact descriptor digest/range and keep that tail as historical-only.
This does not assert it was consumed, delete it, replay it or import old consent;
uncertain delivery/grants still need their own resolution before execution. Reuse
this action/receipt rather than adding a broad historical-acknowledgement API.

Baseline discovery (read-only; no production query run):

```sql
SELECT m.id,m.thread_id,m.turn_number,e.delivery_state,e.runtime_id,e.applied_at
FROM session_messages m LEFT JOIN thread_inbox_entries e
 ON e.org_id=m.org_id AND e.message_id=m.id
WHERE m.org_id=$1 AND m.session_id=$2 AND m.role='user'
 AND m.id>$3 AND m.id<=$4
ORDER BY m.id LIMIT 100;

SELECT id,status,attempts,owner_kind,lock_token,lease_expires_at,payload
FROM jobs WHERE org_id=$1 AND job_type IN ('run_agent','continue_session')
 AND payload->>'session_id'=$2 AND status IN ('pending','running')
 AND id>$3 ORDER BY id LIMIT 100;
```

For queued-message-only jobs, verify org/session/explicit lane, exact message,
runtime/task/apply receipts and any bound request under owner locks. A request
may be bound only from exact request ID/provenance, never “latest pending”.
No existing job payload is rewritten, even pending/attempts0. Exact pre-provider
legacy job is normalized through a sidecar dispatch/allocation receipt for its
already-proved same input; compatible starter resolves that receipt by exact job
ID before claim/start. This does not add a new input to the job. In-flight known
attempt -> naturally drain then classify; ambiguous/uncertain
attempt -> durable hold/reconciliation *after* activation. Before the floor,
legacy consumers keep characterized behavior; do not install a global hold that
strands still-valid old work. Initial jobs get `kind=initial` admission from their
actual pending session/creation receipt before first start, not from today's
generation after an end. Producers across API/Slack/Linear/PagerDuty/issue/eval/
goal-improvement need minimum receipt wiring even though their policy is not
being consolidated.

## 3. Canonical DB seams and typed outcomes (proposed)

`internal/db/session_lifecycle_fences.go` owns durable checks **and** mutation.
All signatures begin `(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, ...)`.
No `DBTX`/pool substitute, hidden context transaction, Begin/savepoint/Commit,
publisher, provider call or optional fallback. Types live in db/models and carry
IDs/expected values, not service imports or `system=true`.

```go
type AdmissionFence struct {
    SessionID uuid.UUID; ThreadID *uuid.UUID
    SessionEpoch int64; ThreadEpoch *int64
}
type AttemptFence struct {
    AdmissionFence
    DispatchIDs []uuid.UUID
    JobID, JobToken, RuntimeID, RuntimeToken uuid.UUID
    ExpectedTurn int
}
// Separate concrete inputs: OrdinaryAuthority, ReviewAuthority (PR,
// assessment, dispatch), AutomationAuthority (target,generation,run,attempt),
// RepairAuthority (PR,current head,reservation,job), FeedbackAuthority
// (PR,batch,head,job). No caller-controlled bypass.
type AdmissionResult[T any] struct { Value T; Effects []LifecycleEffect }
type StartResult interface { lifecycleStartResult() } // sealed in db
type preparedStartResult struct { preparation StartPreparation; effects []LifecycleEffect }
type deferredStartResult struct { deferral StartDeferred; effects []LifecycleEffect }
type readyStartResult struct { permit StartPermit; effects []LifecycleEffect }
// Unexported implementations, validating constructors and exhaustive MatchStartResult.
// nil/typed-nil/zero payload is InvalidLifecycleResult, never a start permit.
type InputClaimResult struct { Inputs []ClaimedInput; Effects []LifecycleEffect }
type AckResult struct { Receipt InputReceipt; Effects []LifecycleEffect }
type EndResult struct { EndReceipt EndReceipt; Effects []LifecycleEffect }
type ProjectionDisposition string // Applied, AlreadyApplied, StaleSharedState
type SettlementResult[T any] struct {
    OwnReceipt T; Projection ProjectionDisposition; Effects []LifecycleEffect
}
// InvalidAuthority is an error: no mutation/effect can be committed.
```

Only DB-owned newPreparedStartResult/newDeferredStartResult/newReadyStartResult
construct the sealed StartResult after validating required IDs/fences/payload.
Unexported concrete variants and exhaustive MatchStartResult expose one valid
prepared/deferred/ready value, not three nullable pointers or caller flags.
Matcher/validator rejects nil, typed-nil or zero/invalid variant payload without
effects; a zero value never means ready. External packages cannot implement or
instantiate variants. This typing is proposed, not compiled runtime code in M0.

Input types are immutable command records: `MessageAdmission` includes exact
actor/source/ingress, message/references/commands/images, changeset and lease
holder, comment IDs, expected fence and dispatch policy; `AnswerAdmission` adds
request/classification version, validated answer or typed denial and grant
intent plus exact current request/workspace eligibility;
`StartAdmission` requires persisted dispatch IDs plus current job/attempt,
exact bound provider/native conversation and generation, and (on final call)
preparation token plus selected-target resumability receipt;
`OrdinaryEnd` requires expected fence and
actor/ingress, not a target status; `TurnProjection` includes exact own result
message/phase receipt, expected turn and requested terminal/pause result;
`InputAcknowledgement` carries exact batch, sequences, apply time and provider
confirmation; `LostAttemptProof` carries exact runtime/executor/job/lease and
observed terminal or expired outcome; `RequestClassification` carries request,
classification CAS version, proof/deadline and disposition; `RecoveryAdmission`
carries every identity/mode/acknowledgement described in section 7. Enum types
have named constants and `Validate`; no arbitrary status/flags bag. The typed
authority interface is sealed in db with separate concrete review, automation,
repair and ordinary/runtime variants; its values are inputs to mandatory durable
checks, never trusted permission objects supplied by an adapter.

| Primitive and remaining arguments/return | Atomic contract |
| --- | --- |
| `AdmitMessageInTx(..., MessageAdmission) (AdmissionResult[Admission],error)` | Owner/epoch/status/capacity/dedupe check plus message/inbox/comment/dispatch/job; typed queued/running/new-job/same-input-existing-consumer/wait result. Ordinary authority only. |
| `AdmitAnswerInTx(..., AnswerAdmission) (AdmissionResult[AnswerAdmissionResult],error)` | Exact request/version/readiness plus validated answer or denial, compatibility question, request-keyed grant, message, claim and exact dispatch. All-or-nothing; otherwise separate waiting receipt with request unanswered. |
| `AdmitReviewTurnInTx(..., ReviewAuthority, MessageAdmission)` / `AdmitAutomationTurnInTx(..., AutomationAuthority, MessageAdmission)` / `AdmitRepairTurnInTx(..., RepairAuthority, MessageAdmission)` / `AdmitFeedbackTurnInTx(..., FeedbackAuthority, MessageAdmission)` -> `(AdmissionResult[Admission],error)` | Same shared fence/mutation implementation, typed domain authority verified under domain locks; keep reservation/result atomic. |
| `AuthorizeRuntimeStartInTx(..., StartAdmission) (StartResult,error)` | First call checks end/owner/drain/lease/binding before any target preparation/prewarm/restore, persists narrow StartPreparation/token and no provider permit. Final call consumes exact selected-target VerifyResumable receipt, current refs/availability/descriptor/reservation/job lease and ALL-receipt same-binding possible-acceptance predicate before immutable runtime/executor admission/StartPermit. Unresolved earlier provider entry is uncertainty, not another initial launch. Temporary blocker returns strict StartDeferred. Initial/direct/retry/handoff all compose the same seam; no trusted-object bool bypass. |
| `DeferRuntimeStartInTx(..., JobLeaseAuthority, StartDeferral) (AdmissionResult[StartDeferred],error)` | Exact claim/dispatch, no allocation; payload-preserving nonconsuming retry and durable due receipt. Undo only this claim's attempt debit; no generic checkpoint_recovery rewrite or silent terminal drop. |
| `MarkProviderLaunchInTx(..., AttemptFence, ProviderInvocation) (AdmissionResult[ProviderLaunchReceipt],error)` | Immediately before EVERY Execute, current permit/lease/epochs/generation/identity plus ALL-receipt same-binding possible-acceptance/retry-authority check, then CAS from expected ordinal to fresh invocation nonce/purpose; set write-once FIRST marker if absent. Known commit + fresh CAS receipt alone permits that one call. AlreadyMarked/unknown commit never permits repeating it. Revocation/no-launch reconciliation serializes on these rows; FIRST marker never clears. |
| `EndSessionInTx(..., OrdinaryEnd)` / `EndThreadInTx(..., OrdinaryEnd)` -> `(EndResult,error)` | Owner/eligibility checks, exact known-unlaunched parked-intent supersession and pending-job fencing plus end epoch advance in one commit; thread end retains real predecessor drain blocker. Session idle test occurs here and is not broadened. Strict end, never process cancellation/publication or stale-success return. |
| `ClaimSeedInTx(..., AttemptFence, []uuid.UUID)` / `ClaimLiveInputInTx(..., AttemptFence, int)` -> `(InputClaimResult,error)` | Exact inputs/epochs/current consumer lease claim, compatible inbox batches/phase write; stale input attach is an error with entire tx rollback. |
| `AcknowledgeInputInTx(..., AttemptFence, InputAcknowledgement) (AckResult,error)` | Current receipt/lease + exact epochs/batch sequences; stale epoch/lease rejects entire tx, never a successful stale projection. Unknown provider result preserves uncertainty. |
| `ProjectTurnInTx(..., TypedAttemptAuthority, TurnProjection) (SettlementResult[TurnReceipt],error)` | Verify allocation/own outcome authority first; own immutable message/result/phase receipt settlement, conditional shared session/thread result/status/turn/diff; sibling-aware parent projection. |
| `ProjectAdmissionFailureInTx(..., AdmissionAuthority, AdmissionFailure) (AdmissionResult[AdmissionFailureReceipt],error)` | Pre-allocation failure only: exact durable dispatch/job, authoritative terminal job outcome, current epochs/consumer and absence of runtime allocation. Strict conditional failed-admission projection, no fabricated provider transcript/result and no stale-settlement disposition. |
| `SettleLostAttemptInTx(..., LostAttemptProof) (SettlementResult[LostAttemptReceipt],error)` | Exact job/executor/runtime provenance and proven expiry/terminality; settle only own artifacts, preserve delivered uncertainty. New execution/generation changes cannot follow stale result. |
| `ActivateRequestReadinessInTx(..., RequestClassification)` / `AdmitInputRecoveryInTx(..., RecoveryAdmission)` | Locked exact classification+enforcement+notice; or request/control receipt/supersede/new dispatch. Own immutable request/version, not latest selection. |
| `AppendApprovedGrantInTx(..., RequestGrant)` | Request-keyed idempotency and validated capability item, metadata snapshot and linked automation run snapshot; no nested transaction. |
| `ChangeReviewOwnerInTx(..., ReviewAuthority, OwnerChange)` / `ChangeAutomationOwnerInTx(..., AutomationAuthority, OwnerChange)` / `ChangeRepairReservationInTx(..., RepairAuthority, ReservationChange)` | Strict current-domain claim/release CAS and lifecycle marker/reservation mutation. Completed/retired is not release; no stale-success return. |
| `RecordCancelIntentInTx(..., CancelAuthority, CancelIntent)` | Actor/domain authority, exact epochs/turn, durable process-delivery intent; bulk locks stable session/thread IDs, cannot cancel successor. |
| `PublishCheckpointInTx(..., WorkspaceAuthority, CheckpointPublication)` / `MutateWorkspaceRevisionInTx(..., WorkspaceAuthority, WorkspaceMutation)` | Sealed current RuntimeAuthority, RecoveryAuthority or explicit specialist domain/publication variant, each with exact receipt/epochs and reservation check. Runtime path requires final pause/diff/checkpoint provenance. Restore/postpublication paths cannot fabricate runtime lease; historical candidate cannot install current snapshot/revision. |
| `AdvanceInputWorkspaceCompatibilityInTx(..., WorkspaceAuthority, WorkspaceCompatibilityAdvance)` | Independent typed workspace authority + exact prior session descriptor CAS authorizes workspace install. One bounded set-based update advances ALL pending-request watermarks/states. Authorized sibling/domain metadata preserves exact-base certification by default in the trusted same-session model; named context-affecting effects become pending/unavailable, prior pending stays pending, corrupt prior becomes own unprovable. No per-request query loop or A veto of B; key-only transitions are valid descriptor changes. |
| `RevalidateInputContextInTx(..., ResumabilityReceipt, RequestContextCAS)` | Selected pending request only: consume exact provider/native identity/target/workspace availability receipt, current descriptor/epochs and deadline. CAS pending -> certified or visible unavailable/unprovable control; no answer/grant/new native context. Start still uses the preparation/final authorization handshake. |
| `RecordStartPreparationOutcomeInTx(..., StartParkingAuthority, StartPreparationOutcome) (AdmissionResult[StartControl],error)` | Under exact job/binding/epoch/generation locks revoke execution lease/preparation/permit, require the same-binding ALL-receipt no-launch predicate (no allocation or explicitly revoked unattempted allocations), and atomically PARK: job awaiting_start_control/no lease, dispatch bound+awaiting_control/version and durable no-expiry control snapshot/notice; close execution preparation. Refund only original claim debit once. Any unresolved prior FIRST/possible acceptance is uncertainty, never parked no-launch. Preserve request/grant/inbox; no dead-letter/reanswer. |
| `RetryUnlaunchedStartInTx(..., StartControlAuthority, SameBindingStartRetry)` / `AbandonUnlaunchedIntentInTx(..., StartControlAuthority, StartIntentAbandonment)` -> `(AdmissionResult[StartControl],error)` | Exact answered OR committed-denial dedicated dispatch and durable PARKED control version; no live worker lease/unexpired preparation required. Retry consumes that control, requeues same job/payload/native identity/generation, then fresh claim/preparation. Abandon cancels only own parked job and proven-unsent input, releases stamped own claim/counter via AdmissionAuthority sibling-aware/nonfailure. Preserve request/grant/audit/unrelated work and physical unknown blockers; no ack/fake result/successor release. |
| `SupersedeUnlaunchedIntentInTx(..., SupersessionAuthority, IntentSupersession)` -> `(AdmissionResult[StartControl],error)` | Current authorized end/successor/F1 recovery/archive/delete transition, exact affected old dedicated binding/control version and ALL-binding no-acceptance proof. Same transaction terminalizes only that known-unlaunched job/intent with typed superseded reason, closes control and conditionally releases own logical claim/counter, sibling-aware/nonfailure. No stale-token revival, grant reversal, fake physical drain or successor mutation. Possible acceptance instead records exact superseded_delivery_uncertain reconciliation disposition; never unsent closure. |
| `ExpireWorkspaceArtifactsInTx(..., RetentionAuthority, ArtifactExpiry)` | Exact current association/publication key, generation/revision/availability version and expiry/archive/merged-PR cleanup-policy CAS. Commit availability tombstone, affected certificate/control outcome and notice, and schedule the key's pre-registered cleanup job BEFORE physical deletion; never create a second delete job. No indefinitely pinned input; deletion cannot target a reused successor key. |
| `RegisterSnapshotUploadInTx(..., WorkspaceAuthority, SnapshotUploadReservation)` | Exact current writer/publication/restore authority and never-used candidate key; create upload receipt and typed cleanup job/control together before Save. No storage I/O under locks and no input-job payload rewrite. |
| `TombstoneSnapshotObjectInTx(..., RegisteredSnapshotCleanupAuthority, SnapshotObjectCAS)` | Exact cleanup job/lease/control version, positive writer settlement, bounded eligibility and ALL usable-reference checks under canonical owner/session locks. Tombstone is the barrier against every later reference creator; it does not require an obsolete object to be the current session publication. Current-reference expiry first uses RetentionAuthority. |
| `SettleSnapshotDeletionInTx(..., RegisteredSnapshotCleanupAuthority, SnapshotDeleteObservation)` | Own exact key/tombstone/version plus observed successful or idempotent-not-found Delete; settle cleanup only after that proof. Failed/unknown Delete preserves the durable same-key responsibility and retry/attention. Never clear another publication or infer writer drain from time. |
| `ReserveWorkspaceRecoveryInTx(..., RecoveryAuthority, WorkspaceRecoveryReservation)` / `SettleWorkspaceRecoveryInTx(..., RecoveryAuthority, WorkspaceRecoveryOutcome)` | Exact recovery receipt/token, descriptor acknowledgement, quiescence and host operation/isolation receipt; only current reservation can bump workspace generation/install outcome/release. |
| `ArchiveThreadInTx(..., TopologyAuthority, ArchiveThread)` / `CreateThreadInTx(..., TopologyAuthority, ThreadCreation)` -> `(TopologyResult,error)` | Strict actor + expected session/lane epoch, parent-first then stable visible siblings; preserve visible-count/active/pending, source/provenance and creation limit checks; honor wholeworkspace reservation. Topology service remains owner, no message/status helper misuse. |

Every row-returning primitive returns no effect when unchanged. Public commands
own Begin/Commit and map results; specialist owners collect returned effects
in their own unit of work. Commit unknown -> resolve exact ingress/dispatch/receipt
on a new read, never blindly repeat write. Guaranteed integration/notice effects
are jobs in the transaction; Redis/SSE/local notify/audit/link enrichment occur
after known commit and cannot turn accepted work into failure.

`PublicationAuthority` identifies existing publication/changeset receipt,
generation/head and its recorded session admission epoch; it permits own published
receipt settlement/current projection only, never runtime admission. Pending
snapshot authority additionally binds exact upload/publication ID, fresh key,
upload token, expected descriptor/epochs/generation and pending receipt version
across Set/Save/Promote/Clear; background context or PR success alone is not proof.
`AdmissionAuthority` identifies exact committed ingress/dispatch and epochs for
admission compensation; stale compensation returns no current mutation, never a
receipt settlement for another attempt. `RuntimeAuthority` is allocation plus
current lease/epochs; `HistoricalAttemptAuthority` is immutable allocation for
own result settlement only. `RecoveryAuthority` includes recovery/version/token
and exact physical operation/isolation proof. `CancelAuthority` includes ordinary
actor plus exact epochs/turn or explicit typed specialist identity. All are sealed
typed inputs with DB validation, not caller-selected ownership bypasses.
`JobLeaseAuthority` names exact current org/job/claim token and already-bound
dispatch/epochs; it authorizes only payload-preserving pre-allocation deferral or
the current-worker branch of exact preparation-outcome handling,
not a provider outcome or another input binding.
`StartParkingAuthority` has DB-validated current-worker or reconciliation
variants. Both lock the exact job/binding/attempt, invalidate old execution lease/
permit/preparation and prove the ALL-receipt same-binding no-launch predicate before parking; an
expired lease or adapter error alone is insufficient.
`StartControlAuthority` adds exact authorized actor or lifecycle reconciliation
receipt, answered/denial request, fixed dispatch/job and durable PARKED control
version/snapshot, current owner/epochs/generation and revocation/no-launch proof.
It does NOT require the old worker lease or unexpired execution preparation.
Retry/abandon consumes this exact control, not revoked launch authority. It is
sealed DB-validated authority, never an adapter-error boolean.
An exact committed denial uses the same control variant with its cancelled
request status and bound denial receipt (`humaninput/service.go:Cancel:225`);
arbitrary cancelled/expired requests do not.
`SupersessionAuthority` is a separate sealed current-transition authority: end
receipt/new end epoch, successor admission receipt/current epoch, accepted F1
recovery plus acknowledged intent set, or archive/delete actor/policy and current
visibility-transition receipt. It may close only the exact older intent selected
under canonical locks. It is not stale StartControlAuthority made valid again.
`RetentionAuthority` is the durable retention/archive/merged-PR cleanup policy and
version plus exact publication key/availability/current associations, not a stale
reaper list or provider lease. It permits availability expiry/deletion intent only, not
end, reset, answer, recovery or successor status projection.
`RegisteredSnapshotCleanupAuthority` is a separate sealed resource-job variant:
exact org/session/cleanup-job/key/upload/publication identity, current job lease
and control/tombstone version plus observed writer settlement and reference
eligibility. It authorizes only its registered superseded/orphan object, not a
current publication, runtime or input. A historical key never impersonates the
current-reference RetentionAuthority. Storage owns this resource workflow; DB
primitives own its durable checks/mutation in the caller's explicit transaction.

Capacity/stale-container dead-letter hooks may run before any runtime allocation.
They use strict `ProjectAdmissionFailureInTx` with the authoritative terminal job
outcome and exact current dispatch, not an invented HistoricalAttemptAuthority.
An allocated attempt instead uses its immutable allocation and F2 settlement.
Captured callback status/error cannot replace the final durable job outcome;
missing current proof leaves visible reconciliation, not an unfenced status write.

F2 resolution has two different authorities. Current admission/consumption needs
live lease + current epochs + domain ownership; no stale-success variant exists
in AdmissionResult, StartResult, InputClaimResult or AckResult. Historical own
settlement instead proves immutable allocation `(org,session,lane,epochs,job ID,
job attempt token,runtime ID,runtime lease,allocation digest)`. A reclaimed but
still alive predecessor may append its own immutable transcript/terminal result
against that allocation even after mutable job lease/parent epochs move. This
does not authorize current status, shared diff/checkpoint or input consumption.
Repeated exact same-kind terminal digest is idempotent; conflicting same-kind
result digest rejects. A lease-lost observation does not preclude a later old
provider's own immutable final response; it does not replace already-closed phase
boundaries either. Append outcome/transcript evidence, never reopen/overwrite a
terminal phase or use historical settlement to mark new input applied.
Late historical messages retain allocation's own lane/turn/phase and explicit
historical-attempt attribution; they are excluded from successor seed selection,
continuation_task_message_ids, automatic context compaction and start-over task
context, including automatic successor summaries and compaction-derived context.
Only an explicit newly authorized copy with fresh input identity can
include their text, never as active answer/grant/consent. Appending old output
must not steer a successor merely because it has a later message ID.
Missing/foreign/mismatched allocation is `InvalidAuthority` and rolls back ALL
domain/receipt mutations. Mutable status alone cannot validate or invalidate the
historical receipt. Only SettlementResult can return `StaleSharedState`.

| Primitive family | Valid current authority | Stale projection with exact allocation | Authorization rejection |
| --- | --- | --- | --- |
| AdmitMessage/Answer/Review/Automation/Repair/Feedback; end/recovery/classification/grant/control | Coupled current command rows/effects, same-input replay only | Not supported; return typed conflict and rollback, no answer/grant/owner/new execution | Whole tx rollback |
| AuthorizeRuntimeStart; ClaimSeed/Live; AcknowledgeInput | Exact current permit/input/ack, known committed effects | Not supported; rollback. Historical provider evidence may go to own settlement receipt, not inbox ack/watermark | Whole tx rollback |
| ProjectAdmissionFailure (no runtime allocated) | Exact dispatch/current admission and authoritative terminal job failure; conditional failure projection and notice only | Not supported; rollback. No runtime allocation means no invented provider outcome or transcript to settle | Whole tx rollback |
| ProjectTurn | Own immutable result plus current shared projection if matching; postcommit status/diff notifications only for applied fields | Own allocation's transcript/final outcome/phase only; skip parent/thread status/result/turn/diff/checkpoint, owner release, input ack and publication/execution | Whole tx rollback including domain marker |
| SettleLostAttempt | Exact own executor/runtime/holder/unsent/uncertain receipts; current projection only if still authorized | Own terminal/expiry/drain observation and immutable result; no successor inbox reset or current holder release | Whole tx rollback |
| Owner claim/release/repair and workspace checkpoint/revision mutation | Exact current typed domain/recovery authority under domain+parent locks | Not supported; a stale attempt can record its own immutable checkpoint candidate but cannot install it as current | Whole tx rollback |

Allowed stale effects are deliberately narrow: durable `ReconcileAttemptDrain`
notice keyed by exact allocation can wake a reconciler after a predecessor really
drains; it cannot start work, publish or clear a current owner. Reconciler rechecks
current epochs/owner before any successor admission, preventing lost liveness
after an ended runtime's final close. For automation, existing
`automations.TurnCompleter.Sweep:373` -> `RecoverAbandonedRun:238`,
`releaseStrandedOwnership:532` -> `AutomationTargetStore.ReleaseStrandedOwnership`
and `reconcileWake:554` owns hold/retained-marker release and target wake under its
current target/generation/run locks. Stale settlement only records/schedules that
exact drain check; Sweep decides release/wake with fresh authority. It never
replays a completed result marker to mint execution/publication permission.

## 4. Function-level transaction/caller composition

`S` = public lifecycle command owns transaction; `D` = existing domain owner;
`R` = runtime adapter unit of work. Every specialist row retains its result,
lease/publication receipt and domain locks. “Variant” means an explicit InTx
function, not a wrapper invoked on `NewXStore(tx)` that might Begin again.

| Existing callers (baseline source) | Outer owner / canonical call / effects |
| --- | --- |
| `handlers/sessions.go:SendMessage:4033`; `thread/service.go:SendMessage:755`, `createMessageInTx`, queued paths; `agent/orchestrator.go:drainQueuedMessagesAfterProcessedID:6399` | S -> AdmitMessage/AdmitAnswer; queue drain R supplies attempt then calls same InTx admission. Postcommit HTTP audit/Linear linking, inbox and jobs wake. |
| `humaninput/service.go:Answer:114`, `Cancel:225`; `thread/actions.go:RetryInboxEntry:205`; `handlers/session_review_comments.go:SendToAgent:339`; legacy `AnswerQuestion` | S -> answer/denial/replay/message or answer-only seam; no facade-to-facade/nested transaction. |
| `worker/handlers.go:enqueueSlackSessionContinuationMessage:6283`, `handleSlackHumanInputAnswer:6649`; `handlers_linear_agent_prompted.go:appendPromptedMessageAndEnqueueContinue:256`, `appendPromptedMessageToRunningSession:356` | S invoked from adapter; integration receipt joins tx; only committed outcome triggers Slack card/Linear activity. Retire independent Slack AnswerPending and worker answer-latest helper. |
| Session/tab tools, review-loop `sendPlain`/`sendReview`, code-review agent message, push reconcile (`worker/handlers.go:13281`) | Thin thread facade -> S; preserve trusted origin, exact thread, custom dedupe, changeset/action. No ordinary owner bypass for system actor. |
| Session retry/start-over/end/cancel; thread `EndThread:1610` and cancel exact-turn/bulk | S -> retry/recovery/end/control primitives; process stop delivery outside locks. |
| `worker/handlers.go` run_agent/continue handler, `session_executor_runtime.go` handoff/restart, `agent/orchestrator.go:beginRuntimeControl:2261`, `ContinueSession:4354` | R -> AuthorizeRuntimeStart before eval/Slack-running/preview/provider effects and again before allocation/handoff; ClaimSeed/Ack exact dispatch; no fallback UpdateStatus. |
| `db/thread_runtime_store.go:CreateStarting:58`, `ReclaimExpiredLeases:439`; `db/session_executor_store.go:ReclaimLost:349`, `ReclaimLostForSession:503`; `db/jobs.go` reclaim/failure hooks | Explicit TxStarter wrappers -> InTx SettleLostAttempt + start/seed/delivery fence; original CTE atomicity retained inside one tx, reset only own unsent inbox, delivered -> unknown. |
| `agent/reaper.go` list-then-status paths; `github/pr.go` PR-created status; `pr_health_service.go` projections; worker failure/retry/cancel/dead-letter; orchestrator drain/retry reverts; `db/session_store.go:ResetAfterLostReuse:3596` | R/domain wrapper -> conditional ProjectTurn or SettleLostAttempt with re-read exact owner/epoch. Plain UpdateStatus/current status/old list is insufficient. No stale compensating reset. |
| `agent/thread_runtime_delivery.go:threadRuntimeControl.Close:350`, `keepSessionRunningIfSiblingRuntimesActive:420`; `agent/orchestrator.go` capability/provider pauses; `handlers/agent_capabilities.go` early request pause | R -> ProjectTurn plus request proof writer. Early request-created is not checkpoint-ready; retain distinct pending-checkpoint transition. Parent projection uses locked sibling activity. |
| `github/pr_health_service.go:resumeRepairSession:2064` | D transaction -> lock PR/current-head/reservation -> AdmitRepairTurn; revision context/message/reservation/job remain atomic. Reservation checks use active/current head/job, not unfiltered historical GetAutoRepairRunByThread. |
| `db/code_review_recheck_dispatch.go:Dispatch:195`, `Claim:338` | D wrappers + explicit DispatchInTx/ClaimInTx -> AdmitReviewTurn/AuthorizeRuntimeStart. PR advisory, state/assessment/dispatch/job locks retained. Existing receipt collision returns exact prior result; new binding required. |
| Recheck `Complete:463`, `Fail:597`, `FailTerminalJob:643`, `Cancel:707`, `ReconcileDrainedTerminalTurn:750` | D wrappers + corresponding InTx variants -> ProjectTurn/SettleLostAttempt, F2 outcomes; exact job/attempt or terminal-job proof. Keep immutable receipt, root historical usage, own phase and drain rules; no stale idle projection. |
| `db/code_review_scheduling.go:RepairMissingWakes:229` terminal-recheck loop at `:353`; `codereview/recheck_admission.go` cancellation callback `:766` | Standalone sweep uses wrapper; WithLockedPR callback calls ReconcileDrainedTerminalTurnInTx with supplied tx, never wrapper/NewStore(tx) Begin. Domain wake only actual outer postcommit owner. |
| `db/code_review_terminal_recovery.go:ReconcileTerminalReviews:26`, private `reconcileTerminalReviews:38`, also admission callback | WithLockedPR owns tx; private helper already accepts tx -> ProjectTurn for terminal threads. Preserve no-publication/terminal metadata/executor/job/no-live-runtime proof; no new tx. |
| `db/session_thread_workspace_failure.go:FailWorkspaceWaitForDeadLetter:16` | Explicit TxStarter wrapper -> InTx exact dead-letter proof -> ProjectTurn thread-only; PR lock before parent/thread and runtime-FK exclusion. No optional type-assert fallback in new contract. |
| `db/session_review_workspace_recovery.go:RecoverIncompleteCodeReviewWorkspace:29` | Existing bounded D wrapper -> InTx SettleLostAttempt; retain exact preparation/waiter/revision/workspace-generation and peer/preview/holder exclusions. Physical destroy remains host-local after commit. |
| `db/automation_dispatch.go:ClaimForAutomationTurn:556`, `ClaimPrimaryForAutomationTurn:588` | Caller D transaction -> AdmitAutomationTurn; canonical parent/thread mutation instead of copied shared SQL. Target/generation/run/job authority and reservations unchanged. |
| `db/automation_turns.go:EndInterruptedAttempt:79` | Add TxStarter wrapper and EndInterruptedAttemptInTx; existing run/job CTE becomes explicit locks+SettleLostAttempt/ProjectTurn in same transaction. No pool fragments or silent missing transaction. |
| Automation `CompleteExecutingPreflight:215`, `CompleteThreadTurnForAttempt:311`, `FailThreadTurnForAttempt:318`, private `setThreadTurnForAttempt:322` | Supplied tx D -> ProjectTurn with exact AutomationAuthority; result, pending ownership release and target wake stay same commit. F2 cannot release a successor generation. |
| `db/automation_completion.go:CompleteFromMarker:88`, `FailRetriesExhausted:250` | `automations.TurnCompleter.Complete` / coordinator owns tx -> canonical ProjectTurn/SettleLostAttempt; marker/result/turn count/retirement/hold-release/wake stay atomic, emit only after outer commit. |
| Automation `ReleaseStrandedOwnership:368`, lifecycle retire/release helper, review `ClaimFullAssessmentOwner:78`, `RetireOwnerForCompletedReplacement:148` | Named domain authority changes retained marker under domain then session locks. Ordinary end/recovery checks same locks; completed/retired status alone is not release proof. |
| `db/pull_request_feedback.go:QueueContinuation:375` | D owns batch transaction; add explicit QueueContinuationInTx -> AdmitFeedbackTurnInTx(FeedbackAuthority PR/batch/head/job). Preserve batch status, PR/session ownership, canonical feedback lane, message/inbox structured prompt, `continue_pr_feedback:<batch>` and postcommit notify atomically. PR advisory/current-head locks precede batch/session; no ordinary reconciler borrowing its key. |

The following hand-audited writer rows supplement syntax discovery. Each names
the authority, exact own settlement allowance and stale/current effect boundary;
none is a broad “specialist exemption”. File:function is authoritative if a line
shifts; executable inventory records source hash/line and the individual site.

| Exact caller(s), below internal/ | Canonical composition and authority | Own settlement / stale result / effects |
| --- | --- | --- |
| `worker/code_review_handler.go:reconcileCodeReviewSessionCompletion`, `reconcileCodeReviewSessionFailureWithDetails`, `ensureCodeReviewOrchestratorThread` (status sites :845/:882/:3219) | PR advisory -> assessment/metadata/controller attempt -> session/lanes; ProjectTurnInTx(ReviewAuthority), admission normalization through AdmitReviewTurnInTx | Confirmed own review/attempt metadata/result may settle. Current parent completion/failure/idle normalization requires current assessment/epochs and terminal sibling proof. Stale shared write skipped; no synthesis dispatch/publication from stale success. |
| `services/codereview/service.go:reconcileStrandedSession`, `reconcileUnclaimedReviewSession` (:1307/:1322) | Existing PR scheduling owner supplies exact assessment/metadata creation receipt and expected session epoch -> ProjectTurnInTx(ReviewAuthority) | Failure/cancel only if exact unclaimed/stranded receipt still owns parent. No allocation/receipt means reconciliation-needed, not guessed authority from status. Old rejection must not cancel a replacement session. |
| `services/github/pr.go:completePublicationLocalState`, `reconcileWebhookPullRequest`; `pr_health_service.go:completeReconciledSessionPublication` (PR-created :938/:2836/:1624) | Existing publication coordinator/PR synchronization -> publication/changeset receipt + generation/head + recorded session epoch -> ProjectTurnInTx(PublicationAuthority) | Own already-established publication/PR receipt can settle. Current PR-created projection only if epoch matches; stale skips shared status and may notify immutable publication, never newly publish/execute. No exact association means hold/notice, not successor projection. |
| `services/agent/reaper.go:reap` (:334/:368/:446/:523) | Nonlocking list -> fresh transaction -> exact current allocation/lease/domain/end/sibling revalidation -> SettleLostAttemptInTx/ProjectTurnInTx | Expired own attempt can receive bounded failure/drain receipt; old list/current status alone never authorizes. Missing legacy provenance becomes visible reconciliation, not fabricated attempt. No stale status/result/holder overwrite. |
| `worker/handlers.go:registerStaleSandboxDeadLetter`, `registerSandboxCapacityDeadLetter`, `registerSystemInterruptDeadLetter`, `newRunAgentHandler`, `newContinueSessionHandler`; `session_executor_runtime.go:finishAttempt`, `retryJob`, `markJobDeadLetter` | Exact bound dispatch/job attempt, terminal job receipt or immutable allocation; SettleLostAttemptInTx, ProjectTurnInTx; pre-allocation terminal job failure uses strict ProjectAdmissionFailureInTx/AdmissionAuthority | Own job/dead-letter/runtime failure receipt settles; stale shared projection skips and preserves successor. No allocation means no fabricated provider receipt. Retry cannot adopt current epoch or select new input. Drain-only wake allowed; no status reset merely to make retry eligible. |
| `worker/handlers.go:finalizeCancelledThreadBeforeContinuation`, `finalizeThreadCancellationIfRequested` | Exact cancel intent/epochs/turn + bound dispatch; RecordCancelIntentInTx then fenced cleanup and sibling projection | Dead-letter/clear pending only exact cancelled-generation unconsumed entries. Stale intent cannot clear successor counters/input or idle active parent. |
| `services/agent/orchestrator.go:beginRuntimeControl`, `RunAgent`, `ContinueSession`, `admitNextQueuedThread`, `cleanupContinueSessionStartupFailure`, `abandonReuseForRetry`, `finishWorkerOwnershipFailure` | Start/admission use strict AuthorizeRuntimeStart/AdmitMessage; failed startup/claim compensation uses AdmissionAuthority exact receipt; begun attempts use allocation authority + SettleLostAttempt/ProjectTurn | Pre-provider exact own rejection may settle failed admission; no stale fallback UpdateStatus. Existing failure/revert sites are converted in 0a, not left until M5. New dispatch/job never follows stale projection result. |
| `orchestrator.go:failRunWithResultAndPhaseTerminal`, `failCodeReviewThreadTurn`, `failNonPrimaryThreadWithResult`, `handleCancelledSession`, `handleSystemInterruptedSession`, `handleHumanInputPause`, `failHumanInputPause`, `updatePrimaryThreadTerminal`; `activity_phase_execution.go:persistSessionResultAndComplete`, `persistSessionStatusAndComplete`; `automation_publication_intent.go:persistRunResult` | ProjectTurnInTx with exact own allocation or typed automation/review owner, phase/result in same outer tx | Current status/diff/pause/checkpoint requires current authority; stale can append own immutable phase/result only. Fenced final pause proof is sealed after final pause/diff revision, not early capability status; an earlier unchanged image may use the typed metadata seal in section 7. Invalid authority aborts domain marker too. |
| `api/handlers/sessions.go:revertCheckpointRetry`, `createManual`; `session_review_comments.go:SendToAgent`; `services/thread/service.go:releaseThread`, `revertAfterSendFailure` | Exact admission/creation receipt; canonical admission transaction replaces compensation; any retained cleanup uses AdmissionAuthority | No unconditional failed/idle revert over successor. Failed command cannot erase already-committed accepted message. No runtime allocation means no invented own terminal output. |
| `worker/handlers_linear_agent_prompted.go:handleLinearAgentPrompted`; `services/linear/create_path.go:MarkLinearPrepareFailedWithError:475` | Linear bridge/comment/preparation receipt, exact generation -> admission/conditional preparation-state seam | Preserve sticky ready and prior accepted command; stale preparation failure cannot overwrite ready or successor. External error/activity retries are postcommit own receipt only. |

Owner/input/workspace roots absent from a status-only search are explicitly in
the same first-release floor:

| Root/function | Canonical operation / required current authority |
| --- | --- |
| `SessionStore.Archive:4244`, `ArchiveSystem:4264`, `SoftDelete:4296`; `EndSessionInTx`, `EndThreadInTx`, successor admission and accepted F1 recovery owners | Retain each command's existing current authorization/eligibility, compose SupersedeUnlaunchedIntentInTx with exact selected old dedicated controls in the SAME transition transaction. Known-unlaunched closes own job/intent and conditional logical claim; uncertainty routes existing exact reconciliation, never stale token revival or successor release. Idle-only session end is not expanded. |
| `CodeReviewRecheckStore.ClaimFullAssessmentOwner`, `RetireOwnerForCompletedReplacement` | ChangeReviewOwnerInTx with PR/assessment/replacement identity and existing PR advisory/FK order; retained marker release and full-review drain predicates preserved. |
| `AutomationTargetStore.InsertGeneration`, `RetireGeneration`, `ReleaseStrandedOwnership`; `applyPendingOwnershipRelease` | ChangeAutomationOwnerInTx with target/generation/run attempt. Target -> generation/run/job -> session; pending release/turn holds are not inferred from retired status. |
| `PullRequestStore.CreateRepairRun`, `DeactivateRepairRun` | ChangeRepairReservationInTx with PR/current head/exact reservation/job; former is currently DBTX insert and latter org/id only, so compatible wrapper must require tx/current-head lock. |
| `SessionHumanInputRequestStore.AnswerPending`, `CancelPending`, `AnswerLatestPendingFreeTextBySession`, `AnswerLatestPendingFreeTextByThread`; humaninput `dbTx.AnswerRequest`, `CancelRequest`; worker answerQueued helper | AdmitAnswerInTx: exact request/readiness/admission/consumer; latest selectors retired at activation. Legacy AnswerQuestion alone stays answer-only. |
| `SessionStore.RequestCancel`, `ConsumeCancelRequest`; thread `MarkCancelRequested`, `MarkCancelRequestedForTurn`, `MarkCancelRequestedBySessions`; `thread.Service.CancelActiveThreads` | RecordCancelIntentInTx with exact epochs/turns and stable ordered bulk locks, then process delivery after commit. Consuming one intent cannot consume successor cancellation. |
| `SessionStore.PublishCheckpoint`, `PublishCheckpointWithProvenance`, `UpdateSnapshotInfo`, `UpdateWorkspaceSnapshot`, `ClearSnapshotKey`, `SetPendingSnapshotKey`, `PromotePendingSnapshot`, `ClearPendingSnapshot`, `ReapStrandedPendingSnapshots`; `SessionThreadStore.SetBaseSnapshot` | PublishCheckpointInTx with current RuntimeAuthority, recovery reservation or typed PublicationAuthority pending-upload receipt. Preserve checkpoint lineage/final revision; Set/Promote/Clear/reap require exact receipt ID/key/epochs/generation/version, never org/session-only overwrite/clear. |
| `services/github/pr.go:PRService.CreatePR:1041` (key `:1404`, Set `:1405`) | Replace reused post-pr key with fresh per-publication/upload-attempt UUID key under PublicationAuthority; install exact pending-upload receipt before async Save. Current PR/head/session association, recovery fence and publication receipt remain required. Unknown/retry gets a new key, never rewrite installed key. |
| `services/github/pr.go:PRService.CreateBranch:1544` (key `:1626`, Set `:1627`) | Same exact pending-upload receipt/key/generation/epoch protocol; branch-only publication remains branch-only. Fresh key each attempt/retry, no post-branch alias overwrite or implicit resume authority. |
| `services/github/pr.go:PRService.PushChangesToPR:1681` (key `:1842`, Set `:1843`) | Exact existing PR/publication/head and current session descriptor -> new upload receipt/key, not the shared post-pr alias. Preserve push receipts; stale pending upload cannot replace newer current/pending snapshot. |
| `services/github/pr.go:PRService.dispatchPostPRSnapshotUpload:1874` (Save `:1894`, Clear `:1886`/`:1896`, Promote `:1902`) | Carry same PublicationAuthority pending-upload receipt across background lifetime. Save only its never-installed unique attempt key outside DB. Fenced Promote/Clear compare receipt/key/epochs/gen/expected descriptor; stale uploader may leave own orphan but cannot clear/promote successor state. Retry Save always new receipt/key. Exact timeout/reap clears only its own receipt. |
| `worker/handlers.go:newRunAgentHandler:8300` (eval running `:8470`, Slack running `:8471`, prewarm `:8472`, Run/Recover `:8482`–`:8485`); `markSessionBackedEvalRunning:8861` | Move running/started domain/external effects to FINAL-permit committed effects, never phase1 preparation. Prewarm requires exact preparation+workspace lease or waits until final. Worker recognizes parked StartControl without generic success/failure/dead-letter completion; all source Execute/retry helpers use MarkProviderLaunchInTx. |
| `agent/reaper.go:reap` phase 2 (`:533`, Delete `:572`, UpdateSandboxState `:581`); `SessionStore.ListExpiredSnapshots`, `UpdateSandboxState`, `ClearSnapshotKey`; `storage.CleanupSessionSnapshot:47` | ExpireWorkspaceArtifactsInTx/RetentionAuthority first tombstones exact current key/availability, affected certificate/start-control and notice/delete intent; durable exact-key job deletes later. Current delete-before-metadata update is not safe against publication/successor drift. Preserve preview/holder checks; failed preview stop is not drain proof. |
| `api/handlers/sessions.go:SessionHandler.ArchiveSession` cleanup call `:5352` | Archive owner composes exact current session/publication association + archive-policy receipt through ExpireWorkspaceArtifactsInTx, then durable physical cleanup. Preserve archive response/audit and fail-soft notice semantics; stale pre-archive snapshot read cannot delete successor key. No runtime cancellation authority inferred. |
| `services/github/pr.go:PRService.runMergedPullRequestFollowUps` cleanup call `:3357` | Existing PR follow-up owner retains stackComplete/merged association and issue/deploy effects; re-read current session/changeset/publication key/generation under RetentionAuthority before tombstone/delete intent. Merged PR is not blanket authority over a later session snapshot or active successor. |
| `services/github/pr.go:PRService.maybeAutoArchiveSessionOnPRClose` cleanup call `:3507` | Keep configured auto-archive policy/ArchiveSystem outcome; cleanup requires exact CURRENT PR/session/publication association and expected availability even when PR already merged or archive already occurred. Same tombstone-first seam; no stale key deletion, cancellation or successor projection from webhook replay. |
| `SessionStore.BumpWorkspaceRevision`, `writeDiffSnapshot`, `UpdateRevisionContext`; `SessionChangesetStore.RecordMaterializedDiff`, `CompleteMaterialization` | MutateWorkspaceRevisionInTx with current runtime/domain/recovery authority; honor exclusive recovery reservation and exact physical workspace generation; no stale diff/revision install. |
| `SessionStore.AcquireTurnHold`, `AcquireExistingTurnHold`, `ReleaseTurnHold`, `PublishHydratedContainerID`, `ClearContainerID`, `FinalizeContainerDestroy`, `FinalizeIdleCodeReviewContainer`; holder `CreateActive`, `ReleaseWithLease`, code-review release helpers | Current exact container/holder/lease plus reservation check; own stale holder release only after proof it cannot affect current reference. Physical cleanup remains host-local with positive guarded destroy proof. |
| `Orchestrator.MaterializeChangeset`; worker `newMaterializeChangesetHandler`; `SessionChangesetStore.AcquireLease`, `BeginMaterialization`; thread `Create`, `CreateWithProvenance`, `Archive`; preview `Manager.ReservePreview`, `ReservePreviewForWorkerInTx`, `LaunchPreview`, `SoftRestartPreviewWithRevision`, `ResumeStoppedWarmPreview`; PR `CreatePR`, `CreateBranch`, `PushChangesToPR`, publication lease acquisition | Each checks/serializes session recovery reservation before workspace-affecting lease/reservation and again before physical effect. Existing exact lease/generation stays authoritative; reserve/recover/settle uses RecoveryAuthority and cannot silently coexist. These callers need no universal manager, but cannot bypass the workspace fence. |

Persistence seam inventory: `SessionStore.WithTx:63` copies publishers;
`UpdateStatus:1668`, `UpdateResult:1760`, `UpdateTurnComplete:2395` can publish or
own diff transactions; `UpdateResultAndCompleteActivityPhase:1805` and
`UpdateTurnCompleteAndCompleteActivityPhase:2433` combine boundary/result work;
thread `UpdateStatus:307`, `CompleteTurn:419`, `UpdateResult:456`,
`UpdateTurnComplete:973` publish; activity `StartPhase:74`,
`persistBoundaryAndComplete:278`, `StartSeedPhase:18`,
`AcknowledgeInboxBatchAndStartPhase:576`, `acknowledgeInboxBatch:680` own
transactions. Provide narrowly typed `WriteSessionResultInTx`,
`WriteThreadResultInTx`, `WriteDiffSnapshotInTx`, `CompleteActivityPhaseInTx`,
`StartSeedPhaseInTx`, `AcknowledgeInboxBatchInTx`, called only by canonical fenced
operations. Combined result/phase writes remain one tx. Tests configure original
publishers to prove zero precommit events, not rely on nil streams.

Production wiring evidence: API router `:638` wires human-input store into
thread facade; `worker/handlers.go:newWorkerThreadService:709` wires inbox/tx but
not full question/grant/ownership ports. `humaninput.Service` currently silently
skips absent approver (`service.go:182`). New constructors require grant policy,
transaction, ownership, runtime and comment ports; startup fails closed. API
router, standalone executor and all worker facade constructors use the same
required wiring before any call-through cutover.

## 5. Lock graph and chosen coexistence protocol

Baseline cycles are real design debt, not regressions introduced by extraction:

| Existing edge | Competing edge / evidence |
| --- | --- |
| Session send session -> input (`sessions.go:4259`, `:4299`) | Structured answer request FOR UPDATE -> session claim (`humaninput/db_repository.go:88`, `:108`); original capability approver also opens nested session transaction. |
| Thread send sibling/thread claim -> parent (`thread/service.go:782`, `:805`) | Structured resume parent -> thread; archive and claims lock all visible siblings without explicit ORDER BY (`db/session_thread_store.go:272`, `:743`). |
| Grant session -> automation run (`db/agent_capabilities.go:188`) | Automation lifecycle target -> run -> job -> session (`automation_completion.go:493`, `automation_turns.go:215`). Source comment saying “job then run” is not authority; SQL takes run before job. |
| Recheck Complete attempt rows -> thread -> session (`recheck_dispatch.go:476`, `:502`, `:513`) | Dispatch parent -> thread (`:265`); LockAttempt's multi-relation FOR UPDATE is not a documented physical relation order. |
| StartSeed thread -> runtime -> batches/entries (`session_activity_seed.go:28`) | Other claim/reclaim scans first lock executor/job/runtime; original runtime CreateStarting mutates expired runtime/holder/inbox before inserting new runtime (`thread_runtime_store.go:58`). |

FK graph is part of acquisition: input -> sessions/threads/user; runtime ->
session/thread; inbox -> session/thread/runtime; executor -> session/job/node;
phase/batch -> thread/runtime/message relationships; repair/review/automation
reservations -> session, job and domain rows (migrations 000129, 000137, 000152,
000264/265). Inserts/updates can take implicit KEY SHARE on parents. They must
occur after required parent locks; NOWAIT on one SELECT does not bound FK waits.
Non-key status changes should use FOR NO KEY UPDATE where sufficient, but end/
reset/no-runtime proof uses FOR UPDATE to exclude new FK children. Implicit row,
unique index, FK and advisory acquisition all receive lock_timeout.

Chosen 0a final order (all rows within a class stable UUID/id order):

0. Cutover interlock: pre-acquire shared try-locks for known scopes when practical;
   a newly discovered scope (generic ClaimNextRunnable or bulk sweeps) may take
   the shared **try-lock** after row selection because it never waits. Successful
   late acquisition continues with fresh version lookup; only a real exclusive
   activation conflict aborts/retries the whole transaction. Do not repeatedly
   abort merely because a generic claimant did not know its next session in advance.
1. Locate domain IDs without authority; take review PR advisory lock
   `code_review_pr:<org>:<pr>` and/or automation target lock, sorted by namespace
   then ID if both observed (ordinary command then rejects dual ownership).
   Repair uses that PR advisory key as a new compatible claim/release guard.
2. Domain state/generation -> assessment/run -> dispatch/reservation -> existing
   job rows -> existing executor reservations. Automation keeps target -> run -> job; review explicitly locks
   state, assessment, dispatch then job, replacing ambiguous joined lock orders.
   Runtime start/same-input replay prelocks its existing job here; new job inserts
   occur later. Discovery of another required higher-rank row aborts/restarts;
   the nonblocking cutover interlock is the explicit exception, not a row-lock exemption.
3. Parent session -> all affected visible threads ordered ID -> new executors ->
   runtimes -> workspace holders/preview references -> input requests ordered ID
   -> start-preparation/reference/availability/certificate rows ordered request/key
   -> activity phases/batches -> inbox/dispatch/messages -> newly created jobs
   and outbox. Acquire rows once; request grant uses prelocked automation run.
4. Re-read marker/reservation/locator identity after parent lock. Changed locator
   aborts and restarts; missing/unknown ownership fails closed. Every claim/release
   uses same parent lock, preventing new owner after rejection check.

This is a selected design, **not a proven deadlock-free implementation**. For
pre-floor/floor coexistence, floor writers take newly conflicting explicit locks
NOWAIT (including advisory try-lock); `SET LOCAL lock_timeout='250ms'`,
`statement_timeout='1500ms'`, whole DB admission/lock-unit deadline 2 seconds
(including retries, not outside-transaction selected artifact I/O). On `55P03` or
`40P01` only, confirm transaction abort with Rollback before replay, at most three
attempts with jittered 25–100ms delay and full revalidation. `57014`, network,
arbitrary application errors or unknown commit never auto-retry. A rollback
failure is not a confirmed abort; abandon connection and resolve command receipt.
No provider/Slack/Redis effect inside any attempted transaction. Foreground lock
retry exhaustion returns typed Contended without a claim/answer; already accepted
async receipt stays pending for independent reconciliation, not ingress-job retry
budget. The same bounded guard remains during floor/new coexistence. Existing
pre-floor writers can still abort on their characterized cycle; that is not proof
of floor progress. Prove with barriers and independent PostgreSQL connections:
request-first answer vs parent-first; sibling archive/claim; run-first vs grant;
recheck completion vs dispatch; FK child insert vs end; late reclaimer vs seed.

Reclaimers do **not** retain their current runtime-first acquisition order.
`ThreadRuntimeStore.ReclaimExpiredLeases`, executor `ReclaimLost`/targeted variant,
and job recovery first read bounded candidates without locks; for each candidate,
fresh transaction takes domain/job/existing executor/session/thread before runtime,
then rechecks lease/owner/epoch and mutates runtime/holder/inbox atomically. A
candidate changing meanwhile is a no-op. Preserve original CTE atomicity, not its
old lock order. This exact old-reclaimer/floor-reclaimer and floor-seed interplay
is a mixed-version test gate; runtime-first batch-lock scans cannot be retained
as an exception to the new graph.

Bounds above and scan defaults are provisional engineering limits, not measured
performance guarantees. Before 0a release, run matched baseline/floor fixtures
and representative queued/live workloads; require no new lock-cycle pairs,
bounded completion/no starvation, p95 command latency no worse than
`max(baseline*1.20, baseline+10ms)`, no DB admission/lock unit exceeding the 2s
budget except cancellation/connection teardown, and due pending work found
within three 5s reconciliation ticks in the no-capacity-block case. Record p99,
contention/exhaustion rate and scan throughput too; investigate any regression
before enabling. Retune defaults only with recorded evidence/review; this document
does not claim the chosen bounds are already viable under production load.

## 6. End, runtime start and input boundaries

End and start lock same session/lane epoch rows. Start wins -> idle-only session
end rejects; end wins -> pending/delayed job cannot allocate executor/runtime,
prewarm, publish running, seed or launch provider. Recheck at handoff because
scheduler claim alone is not start authority. `BeginRuntime:1196`, fallback
`beginRuntimeControl`, `CreateStarting` and initial/direct paths must call the
canonical operation. Lease plus current_turn cannot identify epoch.

Running thread end records exact predecessor runtime/dispatch drain blocker and
ended epoch. Its already authorized process may drain but its poller claims only
its old epochs. A new send gets successor lane epoch and pending dispatch;
execution waits until no matching runtime/executor/job/holder remains active or
uncertain, including missing-expiry leases. Completion may settle predecessor
receipt but shared status remains ended/new-admission projection. New input cannot
be seeded/acked by an old runtime. Expired lease is not proof of process drain;
reclaim marks exact ownership lost and preserves uncertain provider delivery.
Thread-local end does not bump session epoch or cancel sibling execution.
An active start-preparation target operation is also a drain/reservation blocker
even before runtime allocation. End invalidates its provider-start token, not a
claim that an in-flight restore/prewarm stopped; successor effects wait for its
exact target-operation observation/fencing under existing workspace leases.

Ordinary end and recovery reject retained review marker, automation generation
(including release-pending/retired), session-wide owner when targeting a tab, and
current-head active repair reservation whose job/runtime still owns work. Released
historical association is not a blocker; stale but live holder remains a drain
blocker independently of current reservation. Owner acquisition/release/end have
one transactional winner; ordinary operation never implicitly cancels a domain.
End and every later superseding ingress also compose the exact parked-intent
closure below. Rejecting a stale retry is not cleanup of that intent.
This does NOT expand idle-only EndSession eligibility: a current parked logical
claim keeping the parent non-idle still rejects session end, with no epoch/control
mutation, and exposes the existing reachable StartControl/explicit abandon.
Safe abandon may release the claim, after which ordinary idle end can succeed.
A legally idle end still closes exact stale known-unlaunched controls. EndThread
keeps its existing broader eligibility; park -> EndThread -> fresh send must
progress once physical blockers settle. No-provider evidence alone is not general
permission to end a non-idle session or bypass a retained specialist owner.

## 7. Request readiness and reachable recovery

### Same-session trust model and immutable request identity

This refactor keeps the existing same-session trusted sibling workspace/HOME
model. Authorized lanes, tools and specialist operations share mutable files;
they are not mutually hostile tenants. Org/session/resource authorization and
exact request/consumer identity remain mandatory. This is NOT an inter-tenant
boundary, byte-immutability guarantee, or protection against an authorized sibling
intentionally changing another lane's native state. A scope digest identifies
provider/workdir scope, not a hash proving immutable files. Residual risk is
explicit: trusted tools can change native context between observations; a
resumability check establishes availability/identity at its target, not isolation.

Keep immutable A request/provider ID/kind, org/session/lane/epochs/turn, provider
type and pinned build, exact native conversation or explicit continuation-mode
identity, workdir scope, producing job/runtime attempt, closed input phase,
finalization and actual drain anchor. Current B checkpoint producer/revision
cannot replace A's identity. Parent running/idle from B is not A's readiness
test; answer admission checks A plus current compatible capacity/holders, not
fabricated parent awaiting_input. Compatible sibling scope is a different lane,
no exclusive wholeworkspace holder/recovery reservation, and positive usable
current-generation workspace evidence. It preserves the supported three lanes.
Request identity has explicit ProviderRequest(exact native provider_request_id)
and CapabilityRequest(internal request UUID, capability item and exact producing
phase/runtime) variants. The latter has no fabricated provider request ID;
ResumeIdentity still separately binds the actual conversation/continuation mode.
Missing tuple evidence is not permission to choose whichever request is latest.

Source `agent/adapter.go:AgentAdapter:51` exposes Name/PreparePrompt/Execute/
ResumeMode, not the proposed resumability check. `adapters/registry.go:DefaultMap:19`
ships Claude Code, Codex, OpenCode, Amp and Pi. Their deterministic native resume
commands (`claude_code.go:120`, `codex.go:156`, `opencode.go:89`, `amp.go:89`,
`pi.go:97`) do not themselves prove that native state exists on a restored/live
target. Docker Snapshot (`providers/docker.go:1503`) includes shared workspace
and provider HOME state; Restore (`:1592`) extracts it. No private provider bundle,
export manifest, new immutable-store API or OS isolation program is required by
this baseline. Those are optional later hardening with separate cost/product
review, not prerequisites silently imposed on every existing provider.

### Bounded current-workspace metadata

Ordinary `PublishCheckpoint:1511`, automation `PublishCheckpointWithProvenance:1564`,
thread base_snapshot_key and session_diff_snapshots retain useful keys/heads/
turn/phase evidence, not complete ordinary request ancestry. The success path
snapshots/publishes (`orchestrator.go:6286`, `:6305`) before UpdateTurnComplete
(`:6358`) -> writeDiffSnapshot (`session_store.go:2652`) advances logical
revision. An exact shared current revision/producer comparison against A would
reject normal metadata and sibling progress.

Choose bounded current certificate metadata, not a per-revision journal.
Workspace authorization is independent of request certification: exact typed
workspace authority and old SESSION descriptor CAS authorize the operation.
Parent + stable pending-request locks protect one bounded set-based update of
ALL request watermarks/states in the SAME transaction. No DB query per request
loop. Each receives the exact latest descriptor and last typed operation receipt.
Missing/stale/corrupt A metadata becomes only A's unprovable state, never a veto
of B's authorized write or another request. Key-only and availability-only changes
are valid descriptor transitions without a +1 revision eligibility rule.

States are certified/pending/unavailable/unprovable. Exact-base certified A stays
certified by default across authorized same-session sibling/domain metadata.
Named context-affecting operations below instead choose pending/unavailable.
Prior pending stays pending across N further changes until selected revalidation;
unavailable/unprovable cannot be silently promoted by another B checkpoint.
Raw drift without current operation authority fails closed. No caller-controlled
compatible flag or numeric >= test substitutes for real receipt joins.

| Authorized operation | Per-request disposition |
| --- | --- |
| Sibling checkpoint/diff, metadata revision/key update, ordinary publication promotion, preview metadata | Preserve exact-base A certification by default in this trusted same-session model; advance every watermark. Keep A native conversation/workdir/phase. Prior pending remains pending through both B checkpoint AND diff. No per-B filesystem hash/export fanout. |
| Final pause metadata seal | Raw snapshot capture can precede final diff metadata; typed pause_metadata_seal binds final revision/time and unchanged selected image. Final proof follows finalization, without unnecessary image retake for metadata alone. This is identity/order evidence, not immutable HOME evidence. |
| AcceptSplit / primary pointer update | Preserve by default only when A retains its recorded workdir/native scope. A scope-retarget operation marks A pending for exact selected verification; do not silently retarget its provider conversation or block B's authorized split. |
| Physical snapshot restore/context-replacing installation or generation change | Unlike ordinary B checkpoint publication, this changes the target native scope: mark affected A pending, verify actual target/native identity; absence is unavailable. Raw unreceipted generation drift is unprovable. An accepted answer stays bound to its recorded generation, not implicitly rebound. End/reset/recovery epoch changes still supersede/fence old A. |
| Expiry/delete / destroyed workspace | Tombstone exact key/availability before physical deletion. Mark affected restore-dependent requests unavailable; a positively usable live target may be selected explicitly, never inferred from missing key. Shared snapshot loss is not automatically native context loss on an independently verified live target. |
| Same-lane successor/end or unauthorized mutation | Old request/answer cannot authorize successor work. Unauthorized workspace write rejects; unknown A evidence is unprovable and visible, not reset permission. |

Fanout cap 128 pending-request metadata rows/session is a provisional
activation/performance bound, not a product invariant or artifact-hash count.
Lock stable IDs and update one bounded batch. Active-version request creation
returns visible CHECKPOINT_PROOF_CAPACITY before losing the request if exceeded.
Legacy over-cap sessions remain visibly activation-deferred with current compatible
behavior; bounded classification/control cleanup permits explicit retry. No
unbounded journal or all-lane execution mutex is introduced.

### VerifyResumable and the start-preparation handshake

Add proposed adapter method `VerifyResumable(ctx, ResumabilityTarget,
ResumeIdentity) (ResumabilityCheck,error)`. ResumeIdentity binds exact org/session/
lane/request, provider type/build/native conversation/workdir and generation;
ResumabilityTarget binds the actual live sandbox/container or prepared current-
snapshot restore target, source publication key/availability version and
preparation token. Return typed verified/retryable/unavailable/unsupported, with
short-lived exact-target observation receipt. It is a read-only check: no provider
launch, answer submission or history/fresh-context fallback. Existing
ResumeBySessionID requires a provider-specific exact-ID check in 0a; do not infer
support from ID syntax. Other continuation modes need an explicit tested typed
variant, not a fake native ID. Missing implementation is unsupported/actionable,
never generic verified. A TESTED VerifyResumable capability for each exact
provider/pinned-build/ResumeIdentity variant is an activation precondition; a
version0 session without it stays deferred. This is not a private-isolation gate.
Unexpected unsupported on an already-active supported tuple parks the selected
start; it cannot downgrade the session to version0 or silently fresh-start.
Choose a sealed ResumeIdentity union: NativeConversation(exact native ID),
WorkspaceContinuation(exact provider_request_id, producer_phase_id, workdir,
checkpoint publication and adapter answer_protocol_version), or InitialTurn.
WorkspaceContinuation is eligible only with that adapter's tested same-request
workspace-answer protocol; absent protocol returns unsupported. InitialTurn is
never valid for a bound answer/denial. Resolver checks canonical target-scoped
native paths/build and cannot search another session/HOME for a convenient ID.

The two calls to AuthorizeRuntimeStartInTx form one fenced workflow:

1. Before prewarm, restore or target preparation, validate exact binding, current
   job/claim lease, end/ownership/epoch/drain and compatible holder rules. Persist
   narrow StartPreparation ID/token/deadline and allowed actual target/current
   restore source. No runtime allocation or provider StartPermit yet. Target
   effects consume this reservation plus existing container/workspace lease;
   restoring a shared workspace requires its normal quiescence/exclusive-operation
   proof and cannot overwrite B merely to recover A's old context.
2. Outside DB locks, prepare that authorized target and VerifyResumable against A's
   EXACT bound identity on it. Observe source/target availability and return a
   receipt, not an assertion about immutable bytes.
3. Final AuthorizeRuntimeStartInTx consumes the same preparation token and verified
   receipt; rechecks current lease, dispatch/request epochs, provider identity,
   bound generation, exact target/current descriptor/refs/availability and
   reservation. Only then allocate runtime/executor admission and StartPermit.
   Drift/end/lost lease rejects; no provider starts from stale preparation.
   Temporary blocker returns strict StartDeferred on the same binding.
4. After setup and immediately BEFORE EACH adapter.Execute, MarkProviderLaunchInTx
   rechecks current permit/lease/epochs/generation and CASes expected invocation
   ordinal to a fresh nonce/purpose on the existing allocation/continuation-
   attempt record. It sets write-once FIRST marker if absent and never clears it.
   Only known commit of that fresh invocation CAS permits that one call.
   AlreadyMarked/replayed receipt/unknown commit is NOT permission to invoke it
   again. No provider/setup I/O occurs inside DB locks or between marker commit
   and the intended immediate Execute; setup finishes before this marker.

Every launch path consumes this fence: initial/continue Execute
(`orchestrator.go:3943`, `:6038`), ordinary permitted native fallback (`:6052`),
credential retry helpers (`:9954`, `:10012`) and alternate adapters/executors.
Bound-answer reconstruction remains prohibited. Unbound retries retain their
characterized policy but require fresh invocation ordinal/nonce/current permit.
Once FIRST exists, a missing later current marker never restores no-launch proof
for that allocation or input binding. The no-launch predicate joins ALL immutable
allocations/continuation-attempt receipts for the same dispatch/request binding,
not just the current preparation, job or runtime. Possible acceptance means FIRST
marker OR unresolved execution_state provider_launched/delivery_unknown OR
equivalent provider-entry/acceptance evidence, including legacy/normalized
receipts whose marker is missing. Marker absence alone is never no-launch proof.
Final authorization, invocation CAS, reconciliation, retry and abandon all inspect
this current/prior same-binding receipt set under canonical locks; any unresolved
possible acceptance blocks automatic no-launch retry or abandon-as-unsent;
new job/allocation/preparation cannot wash that history. Existing explicit
unknown-delivery replay or exact positive no-input-accepted resolution is separate
authority, not inferred from a replacement record. Bound-answer post-Execute retry
needs existing explicit no-input-accepted/replay authority, not a renewed resumability check. The marker-
commit-before-call crash gap deliberately remains unknown/possibly delivered;
the protocol does not claim an atomic DB-and-provider call.
Retain those existing unresolved allocation/continuation receipts while the
binding/control can be considered for execution; retention cannot erase a FIRST
marker and thereby manufacture no-launch evidence. This is an eligibility and
retention predicate on existing receipts, not another attempt-history store.

Slack/eval/Linear running or started effects are emitted only after FINAL permit
commit, never preparation-only authority. Source worker/handlers.go:8470 marks
eval running and :8471 queues Slack running before orchestrator dispatch at
:8482–:8485; migrate them to final-permit receipt effects. :8472 preview prewarm
may run earlier only with exact StartPreparation and existing workspace lease
scope; otherwise defer it too. Delayed running jobs recheck permit/control version
so parked state renders current waiting/control, not stale running. No precommit
external effect and no provider/setup work under DB locks.

Known pre-launch unavailable/unsupported is a durable PARKED control, not a
running job whose 30-second preparation merely expires. RecordStartPreparationOutcomeInTx
locks exact job/dispatch/current preparation/attempt and, in one transaction:

1. Revalidate fixed answer/denial binding, owner/epochs/generation and native
   identity. Invalidate the old execution preparation, lease and permit; inspect
   ALL immutable allocation/continuation receipts for this fixed input binding
   under these locks. Require positive no-entry/no-acceptance proof across that
   set (not just missing FIRST), plus no allocation or explicitly revoked unattempted
   allocations. Never infer this from lease expiry or an adapter error.
2. Set ONLY the dedicated job to proposed typed awaiting_start_control, clear its
   execution lease, leave dispatch bound with awaiting_control metadata and
   increment control version. Close/revoke execution preparation; persist the
   durable last outcome/control receipt snapshot on the dispatch (exact job/
   binding/epochs/gen, native identity, revoked tokens/permit/allocation and
   no-launch proof).
3. Refund only that original claim's attempt debit exactly once, keyed by claim/
   parking receipt; never zero prior attempts. Persist visible accepted-undelivered
   notice/control. Answer/denial/grant/inbox remain committed and unacknowledged.
   DeferRuntimeStart and parking use the SAME claim-debit refund guard, not two
   independently refundable counters; replay cannot refund that claim twice.

Parking/reconciliation has ONE atomic outcome, not a best-effort control upsert
after execution authority was already changed. Under those locks validate the
existing control version/binding/status first. Dependent job/dispatch/preparation/
check/refund writes require the exact successful returned control mutation;
validate their expected affected rows before commit. A conflicting/no-op control
upsert with no exact prior replay receipt aborts the WHOLE transaction, including
token revocation, check consumption and refund. Exact already-applied receipt
replay is unchanged throughout. No zero-row control write may leave a partially
revoked/parked job or newly usable authority.

The finite current parked-control fields live on the bound dispatch, with the
dedicated job status updated atomically, not a new event journal. Closed execution
preparation can expire or be deleted after this snapshot is captured; its row is
not the control's continued authority. Execution deadline/lease can pass;
control receipt/version remains valid until explicit retry/abandon or a new
fenced stale disposition. Generic runnable claim, lease reclaim, retry/dead-letter
and admission-failure sweeps must EXCLUDE awaiting_start_control. It is neither
runnable nor terminal-failed; do not depend only on a worker returning early.
It remains nonterminal in dedupe/competing-consumer and ownership discovery; a
lease-free parked answer blocks another same-scope execution, not durable waiting
input or otherwise compatible independent siblings. The parked job's logical
claim is released only by safe abandon as below.

The compatible-writer floor must change these source boundaries together; the
status is not safe merely because a new handler understands it:

| Existing boundary | Required parked-control behavior before activation |
| --- | --- |
| `models/job_enums.go:JobStatus.Validate:16`; `migrations/000035_check_constraints.up.sql:79` | Add typed awaiting_start_control and status constraint support; distinguish nonterminal from runnable/lease-owned sets. |
| `db/jobs.go:ClaimNextRunnable:840`; `HasActiveByDedupeKey:279`, `GetActiveByDedupeKey:307`; dedupe index above | Claim only pending; active exclusion includes parked job AND bound control across key families. No new consumer from a parked dedupe collision. |
| `JobStore.MarkSucceededWithLease:1227`, `MarkFailedWithLease:1251`, `RetryWithLease:1275`, `RetryWithLeaseAndTarget:1302`, `RetryWithoutConsumingAttemptWithLease:1330`, reset-window `:1358`, target `:1386`, `DeadLetterWithLease:1414`; `ReclaimLostRunningJobs:1441`, `ReclaimLostRunningSessionJobsForSession:1545` | Keep exact running/token predicates; parked has no lease and cannot enter generic success/retry/reclaim/failure/dead-letter or payload-rewrite paths. Only typed retry consumes its control. |
| `db/session_executor_store.go:ReclaimLost:349`, `ReclaimLostForSession:503` | Old pre-handoff branches `:371`/`:519` do not require running job status. Cleaning an old executor must preserve the parked binding/control/claim, never requeue or fail its input. Physical target operation still needs its own drain proof. |
| `worker/worker.go:319`; `worker/session_executor_runtime.go:417` | Handle durable park separately from success/FatalError/retry-budget terminalization; generic return handling cannot succeed/dead-letter a parked job or fail its logical claim. |
| `SessionStore.ListStalePendingSessions:4110`, `ListStaleRunningSessions:4152`; `SessionThreadStore.ListStuckRunningThreads:540`; `agent/reaper.go:reap:327` | Exclude exact current parked claim/control from generic age-based failure, even when its session/lane remains pending/running. Reconcile actual physical blockers independently; elapsed logical age is not failed execution. |
| `migrations/000309_resume_bounded_retention.up.sql:42`, `delete_expired_completed_jobs` | Keep terminal-only cleanup; awaiting_start_control is not abandoned history eligible for job deletion. Retain its fixed dispatch/control association. |

0a must test these real caller paths, not only the synthetic parking statement,
including park beyond both lease and preparation expiry, old executor cleanup,
age sweeps, a second same-lane input and compatible sibling progress.

Retryable I/O can use the same durable control/due reconciliation without spending
execution attempts. PendingAdmissionReconciler scans parked controls independently
of job lease/dead-letter budgets. For a request STILL pending, selected target
revalidation may CAS its pending certificate. An answered/committed-denial request
instead uses its parked control and start handshake, never a pending-only CAS or
status flip. Snapshot/control deadlines cannot reclassify first native observation
that a still-live producer has not published as loss; known published loss wins.

Crash-before-park recovery first locks and revokes the exact old execution lease/
preparation/permit, then checks the same-binding immutable receipt-set predicate
above; any prior unresolved launch forbids no-launch classification. The launch-
marker CAS and this revocation serialize. Only positive no-launch proof may
create the parked snapshot. If final authorization allocated an unattempted
runtime, explicitly revoke it and preserve that receipt; final permit alone is
not evidence provider was attempted. FIRST marker or unresolved provider-entry/
acceptance evidence means existing unknown-delivery rules, even with no marker
or if a process crashed before the call. Reconciliation preserves unknown and
does not refund that claim's attempt debit; it cannot manufacture parked no-launch.
Unknown physical prewarm/restore remains its separate target-operation drain
blocker; parking never pretends that operation stopped.

RetryUnlaunchedStart/AbandonUnlaunchedIntent consume exact PARKED control version,
not an old live worker lease, execution preparation token or unexpired check.
Retry verifies current actor/owner/epochs/generation/native identity and repaired
availability, consumes control, sets the SAME job pending with unchanged payload/
dispatch and no execution-attempt budget loss. A fresh claim and NEW preparation
token/check are mandatory. Original execution tokens never regain authority.
End/successor/owner already won yields visible stale control, not requeue under a
new generation. Job/process cancellation invalidating start authority is distinct
from committed CancelInputRequest denial-and-resume. Any current/prior FIRST
launch marker, unresolved provider_launched/delivery_unknown or equivalent
provider-entry evidence cannot use no-launch retry/abandon; only existing explicit
no-input-accepted/replay authority can resolve an attempted bound answer.

Start-control detail/actions are reachable from an ANSWERED request and its exact
accepted-undelivered dispatch/PARKED control receipt, not only pending input UI or
RecoverPendingInput. Proposed narrow InspectStartControl, RetryUnlaunchedStart
and AbandonUnlaunchedIntent actions reuse the planned lifecycle control surfaces.
Require existing role/org/resource authorization and exact control version.
Retry requires the same native identity/current recorded generation, repaired
availability and positive no-launch proof; re-run the preparation/final handshake,
never flip the request pending or grant again. Definitive unavailable/unsupported
has an explicit authorized abandon/close-this-undelivered-intent action: under
locks verify answered request/exact dispatch, no delivery/ack/apply/uncertain
launch or grant mutation, then mark only its pending inbox/dispatch terminal with
user-abandoned-unlaunched reason and notice. Preserve request status answered,
grant and audit. Any destructive workspace recovery still requires F1 separately.
The binding is dedicated to that request intent; unrelated ordinary dispatches
are neither in its payload nor abandoned. In the SAME transaction exact parked
job/dispatch/control-version CAS makes only that dedicated job cancelled with
typed user-abandoned-unlaunched control reason and consumes the durable control;
old execution tokens stay revoked, no live lease/preparation TTL is required.
Payload is unchanged; no provider-failure result,
transcript, generic process cancellation or other job mutation. Every claimant
also rejects terminal dispatch/closed preparation. This removes the old job as a
phantom competing consumer; physical uncertain preparation blockers still remain.
Then compose the
AdmissionAuthority release: match current session/lane claim dispatch ID/token,
epochs and claimed turn against the admission receipt. Apply its owned pending-
count delta exactly once under the lane lock, never reset other queued counts.
Only a still-owned claim with no other active/bound job/runtime/dispatch consumer can
clear its marker and return the lane to idle/nonfailure; sibling-aware parent
projection stays running for active siblings and preserves current domain or
publication state. Foreign/successor claim tokens never match and cannot be
released. Idempotent repeat does not decrement again. Fresh ordinary queued input
must then be discoverable/admissible without a phantom running/capacity slot;
no fake failed result or provider transcript is written to release the claim.
The no-other-consumer test excludes this exact just-closed job. Unbound queued
ordinary input remains counted/discoverable but does not retain the abandoned
execution claim or prevent its idle/capacity release.
The same eligibility covers an exact committed denial with cancelled status and
its bound denial dispatch; it never reopens arbitrary cancelled requests.
Abandon does not release any unproved physical preparation drain blocker, so
fresh intent still waits for normal current workspace/drain authorization.

Abandoned answer/denial payload is historical/control evidence, excluded from
automatic seed, task IDs, summaries/compaction, reconstructed ordinary history and
start-over context. Only a new explicitly authorized non-consent copy may include
its text. This narrow filter prevents a later fresh unbound continuation from
inheriting old yes/no; ordinary fallback policy otherwise stays characterized.
User can then submit fresh intent/recovery under ordinary current authorization
and any new provider request needs fresh consent. Abandon never starts a new
conversation with the old answer. End/successor/owner already won produces visible
stale/closed old control without touching current status, requeuing a new
generation or fabricating delivery; uncertain delivery instead requires its own
existing reconciliation, not the abandon-no-launch path.

### Superseding a parked intent

The lifecycle owner of EndSessionInTx/EndThreadInTx, successor admission,
accepted F1 recovery, Archive/ArchiveSystem and SoftDelete MUST compose
SupersedeUnlaunchedIntentInTx in that transition's transaction. Source visibility
writers are `SessionStore.Archive:4244`, `ArchiveSystem:4264`, `SoftDelete:4296`;
their current metadata-only/pool writes are not this contract. Preserve archive/
delete eligibility, preview cleanup and audit semantics, but serialize their
visibility write and exact parked closure through the canonical shared fence.
Unarchive does not revive a closed intent, job, grant or execution token.
EndSession remains idle-only. A non-idle parked session-end request rejects
without epoch/control mutation and leaves explicit abandon reachable; that
action can release its own logical claim before a later eligible session end.
EndThread retains its broader eligibility. Supersession never grants permission
to bypass any transition's owner, domain, quiescence or visibility rules.

Discover affected binding/job IDs without locks, then acquire canonical domain/
job/session/lane/request/dispatch/control locks and recheck the complete affected
set. A newly discovered higher-rank scope follows section 5's try-lock/restart
protocol; never commit an epoch/visibility change with a partial closure set.
The CURRENT transition receipt/epochs authorize closing selected historical
intent, not the old control token. For exact known-unlaunched dedicated answer/
denial binding, inspect ALL current/prior possible-acceptance receipts, revoke
execution authority and atomically cancel only that job, terminalize its unsent
inbox/dispatch and close parked control with typed ended/superseded/recovered/
archived/deleted reason. Keep answered/committed-denial request, grant, payload
and audit; no ack, provider result or re-answer. Proven-unsent superseded answer/
denial follows the same automatic seed/history/summary/compaction exclusion as
explicitly abandoned consent, unless newly authorized as non-consent copy.
Apply the existing conditional
AdmissionAuthority own-claim/counter release in the SAME transaction, preserving
foreign/current successor claims and sibling-aware projection. If the transition
already installed a new claim, the old token cannot match or clear it. A fresh
ordinary queued intent can progress after real blockers settle, not wait behind
the closed job. F1 closes only its explicitly acknowledged unconsumed set; its
existing quiescence/consent checks remain mandatory.

Unknown provider entry/delivery is not known-unsent closure. Record exact
superseded_delivery_uncertain control/reason under current transition authority,
retain immutable input/attempt evidence and the real delivery/drain blocker, and
route it to PendingAdmissionReconciler's ReconcileSupersededIntent disposition.
It consumes existing InspectContinuationRecovery/provider-result/batch/ack and
physical drain observations; known terminal/delivery resolution settles only
that old receipt. Existing explicit RetryInboxDelivery uncertainty authorization
is a separate action, never automatic replay caused by end/archive/delete.
Expose receipt detail/resolution to an authorized tenant/resource owner even when
the session is archived/soft-deleted; that surface cannot send, unarchive or
revive it. A source lookup hiding a deleted session must not silently discard
the retained control. Missing evidence stays visibly reconciliation_required,
not a phantom busy job that no reconciler owns. An uncertain physical preparation
may coexist with safe logical closure, but remains its separately discoverable
target-operation blocker until positive existing drain/fencing proof; closing a
control is never that proof.
Discovery also includes exact retained logical claims/physical reservations of
already-abandoned or superseded known-unlaunched intent, not only controls still
awaiting user action. A later end/epoch/visibility change cannot strand those
receipts behind stale StartControlAuthority. The current transition/reconciler
composes the same conditional AdmissionAuthority release against the old immutable
claim identity; actual physical settlement still requires its own positive proof.
Never project an old idle result over a successor. 0a must cover abandon with an
uncertain preparation, then end, then observed drain and fresh input.

If the answer/grant/request/dispatch already committed and provider is known NOT
launched, failure is accepted-undelivered: preserve every committed row and exact
binding. Do not acknowledge/apply input, revoke/regrant capabilities, reanswer,
dead-letter it as unaccepted, rewrite payload, rebind to a successor or replay a
fresh conversation. Safe retry is only that same binding/native conversation/
current recorded generation with positive not-launched proof and renewed target
verification. Unavailable/unsupported yields exact recovery/control action; only
the named current authorized end/successor/recovery/archive/delete transition
may supersede that exact intent as specified above. Once provider launch or
delivery is uncertain, existing unknown-delivery rules apply, not this no-launch
retry. No claim that an ordinary CLI error proves input was not delivered.

Current hydrate fallbackToReconstruction (`orchestrator.go:5260`), missingResumeID
construction/branch (`:5717`, `:5756`), fallback prompt (`:5799`) and failed native
resume second Execute (`:6042`–`:6069`) must be disabled for a bound answer/denial.
Native lookup/check failure cannot silently
switch that accepted answer into a new conversation. Preserve characterized
ordinary unbound continuation policy separately; do not widen this change into
an unrelated prompt-history rewrite.

Provisional selected check timeout is five seconds, receipt lifetime 30 seconds,
start-preparation deadline 30 seconds. On timeout preserve pending/control and no
provider launch; expired execution token cannot be reused. Durable parked control
does not expire with that deadline or the old worker lease. Section 5's 2s DB admission/lock
unit is separate, no I/O under locks. Measure per-provider check completeness,
latency, restore/start drift races and independent sibling throughput in 0a;
these are engineering bounds, not current adapter support or performance claims.

### Snapshot availability and tombstone-first cleanup

Keep SnapshotStore Save/Load/Delete and existing snapshot ownership. Ordinary
snapshotSession (`orchestrator.go:9365`) currently reuses the workspace key;
File Save (`storage/file.go:23`) truncates and S3 Save (`storage/s3.go:57`)
overwrites. For protected ordinary checkpoints adopt the existing automation
per-publication naming precedent (`automation_turn.go:1142`): fresh unique key
per unpublished single-producer upload, fresh key on every retry, never rewrite
an installed publication key, and fenced DB publication only after successful
complete Save. An unknown upload never gets published by guessing; reconcile or
abandon that unpublished key. Existing interfaces suffice; this is trusted writer
discipline, not a new object-version/PutImmutable framework. Active references
never point at a reused mutable alias; normalize old installed keys under known
writer quiescence or retain an explicit unavailable/deferred disposition.

This includes PRService CreatePR/CreateBranch/PushChangesToPR and their shared
dispatchPostPRSnapshotUpload, not only orchestrator snapshots. Existing reused
post-pr/post-branch keys are not exceptions. Before Save install a typed current
pending-upload receipt: org/session, publication and upload-attempt IDs, new UUID
key/token, admission epochs/generation, expected pre-publication descriptor and
receipt version/deadline. Same PublicationAuthority receipt accompanies Set,
background Save and conditional Promote/Clear. No DB locks during upload.
Only complete Save of that never-installed key may be promoted under exact
current receipt/key/epochs/generation/descriptor CAS. Open/Save failure or timeout
clears ONLY matching receipt; an old goroutine cannot clear a newer pending key.
Retry/unknown upload uses fresh key/receipt, never Save again into a possibly
installed key. Stale completed upload is an orphan candidate under its own exact
receipt, not current publication authority. Stranded-pending cleanup also matches
the exact expired upload receipt; no session-only clear. Existing Save/Load/Delete
and snapshot/publication owners remain, not a new storage API or global ledger.

Register object cleanup BEFORE Save, not after success: the upload reservation
transaction also creates its exact snapshot_object_cleanup job and current
SnapshotObjectControl metadata, then commits before any storage I/O. Its immutable
payload is the object identity; nullable `jobs.snapshot_object_control jsonb`
holds the typed finite control record, never a rewrite of an input job payload.
Object states are reserved/installed/superseded/upload_uncertain/delete_blocked/
tombstoned/deleted; writer_state is in_progress/unknown/settled/fenced_drained,
with settled/fenced_drained requiring the exact writer/operation receipt. Add a due index `(run_at,id)`
where job_type=snapshot_object_cleanup and status=pending, plus org/session/key/
upload-ID lookup for exact association checks; enforce unique
`(org_id,(payload->>'key'))` for this typed job kind. Job kind, metadata enum/shape and
tenant/session/key provenance are schema/store validated. The current upload
receipt points at this job; no new object table, ListObjects, refcount, storage
version API or bucket lifecycle requirement.

Successor publication atomically marks the former installed object's cleanup
receipt superseded and schedules its existing cleanup job before replacing the
current reference. A stale upload records its OWN settlement and cleanup handoff,
not another pending receipt. If Save completes but process crashes before recording
that result, the pre-Save job still discovers that exact key/writer; uncertainty
cannot become an untracked object. No Save may precede registration, use an
already-installed candidate, or resume writing a key after its tombstone. A retry
uses a new key and new pre-Save receipt/job.

Proposed SnapshotReaper belongs to the existing storage/snapshot owner, separate
from UploadReaper (`storage/upload_reaper.go:14`), which handles uploaded files
and disables itself in S3 mode. At startup and every five seconds, SnapshotReaper
keyset-pages 100 due receipt/jobs, at most 500 per pass, with org fairness; it
uses exact registered keys, never lists a bucket/filesystem. Route
`snapshot_object_cleanup` jobs only to SnapshotReaper's exact-kind claim path;
generic `ClaimNextRunnable` and other generic job/executor consumers explicitly
exclude this kind before the first registration. Their ordinary pending-job
eligibility is not permission to run snapshot cleanup. Chosen engineering
grace is 24 hours after superseded_at, or upload_deadline + 24 hours for an orphan
upload. These are proposed policies, not measured production defaults. Current
snapshots still use SESSION_MAX_SNAPSHOT_AGE/archive policy below, not the orphan
grace. Incomplete/unknown uploader remains delete_blocked with visible reason;
deadline/lease expiry is NOT settled upload proof. Cleanup requires exact Save
completion or positive writer fencing plus observed host/operation drain that
proves no late write can complete. ReconcileUploadObservation uses that exact
existing upload/host operation receipt; unavailable host/ambiguous Save stays
visible snapshot_cleanup_attention with backoff, not presumed quiescence.

Before deletion recheck ALL protected usable references under canonical owner/
session and exact receipt locks: current/pending session publication, active
automation checkpoint, thread base snapshot, active review continuation snapshot,
accepted recovery source, active/reconciling start preparation and parked-control
source, plus any source retained by an unresolved physical preparation after
logical control closure. Its existing operation/holder receipt must retain that
exact source association until positive drain; deleting a closed execution
preparation cannot erase it. Historical audit key text alone is not usable restore authority. Current
policy expiry may typed-invalidate those restore references and issue their
unavailable/control notices; they do not extend retention indefinitely. A live
physical load/restore/upload still needs positive drain before physical deletion.
This preserves the existing accepted-undelivered/uncertain-delivery rules rather
than dropping input to free storage.

Exact-key tombstone is a ref-creation barrier, not merely a deletion note. Under
the SAME canonical locks, a valid new reference wins -> GC defers; GC tombstone
wins -> publication, preparation and parked-source binding cannot install/revive
that key. New references require the exact positively usable object receipt;
missing/deleted receipt fails unavailable, never implicitly live. Unique keys are
never reused. Commit tombstone/version and durable exact-key delete handoff before
Delete outside DB locks; retry only the same key after unknown/failed Delete.
Source receipts remain until deletion is known settled, and every later reference
creator must honor the tombstone/absence barrier. No read pin/refcount framework.

SnapshotReaper owns durable retry/backoff and visible attention independently of
generic job attempt/dead-letter budgets. A cleanup job remains pending/due or
currently lease-owned while object/writer/delete is uncertain; generic fatal or
terminal retention cannot delete it or silently end its cleanup responsibility.
Its typed worker outcome is durable cleanup handoff, not nil-success/FatalError.
Under exact cleanup-job lease/control-version CAS, reschedule pending/run_at and
refund only that poll claim's attempt debit once; never use continuation payload-
rewrite retry helpers. Crash reclaim preserves object control and tombstone,
then repeats only safe exact-key observation/Delete. This job-kind floor applies
before its first registration, including generic worker/executor terminal paths.
Only known successful/idempotent exact-key Delete plus current tombstone CAS marks
it succeeded/deleted and eligible for normal terminal retention. Bounded discovery
and grace do not promise an absolute storage bound under an unreachable writer:
timely deletion is conditional on positive settlement/drain. Version1 registration
must cover every new candidate and current normalized key; this is not a claim to
have inventoried all unreferenced pre-cutover storage.

Bound retention by current SESSION_MAX_SNAPSHOT_AGE policy
(`config/config.go:300`, default 720h), plus existing explicit archive/merged-PR
cleanup policies. Pending input does not extend it indefinitely; no new read-use
pin/native-bundle retention scheme is required. Current reaper phase 2 deletes
before UpdateSandboxState (`reaper.go:572`, `:581`); CleanupSessionSnapshot
(`storage/snapshot.go:47`) also deletes before pointer clear. Replace those
callers with ExpireWorkspaceArtifactsInTx/RetentionAuthority: lock/re-read exact
org/session/current publication association, key, generation/revision/full
descriptor/availability version and cleanup-policy eligibility. First commit
tombstone, affected certificate/start-control state and visible notice plus an
exact-key delete intent on that key's existing pre-registered cleanup job. Expiry,
archive and merged-PR cleanup schedule the same unique job/control, never a second
deletion job. Only then physically delete through SnapshotReaper.

Delete worker rechecks that exact tombstone/association and no current successor
uses the key. Unique publication keys are never reused, so old GC cannot delete
a new active upload by alias. Repeated delete is idempotent; crash after tombstone
stays visibly unavailable and retries only its own job. Physical workspace loss
is a separate observed availability transition, not inferred from missing blob or
unreachable host. Failed preview stop is not positive drain. Archive/PR-merge
cleanup does not acquire permission to cancel runtime or destroy successor work.

Expiry and answer/start serialize on the exact descriptor/reference version.
Expiry first prevents new answer/start against that lost target. Answer first
does not promise indefinite retention: if expiry wins before provider launch,
keep the answer/grant/dispatch accepted-undelivered, revalidate or expose exact
control outcome under the no-launch rules above. If a current live target is
explicitly usable, same-binding retry may use it only with positive identity/
generation/availability checks, never implicit fallback. Already launched or
uncertain delivery cannot be called no-launch or automatically resent.

### Canonical executable policy and adapter outcomes

The one SQL policy source is
`db/session_lifecycle_readiness_characterization_postgres_test.go`:
sessionLifecycleReadinessPolicyProbe, with workspace transition,
selected-revalidation, availability/expiry and prelaunch preparation examples in
that same file. Do not maintain a weaker parallel docs query. Synthetic
probe_native_context_availability represents actual provider/target availability
observations, not immutable files or a production caller-supplied bool.
probe_start_preparations and probe_resumability_checks model the exact selected
target/identity/token/current-descriptor start handshake, not actual adapter I/O.
The selected-start symbols are sessionLifecycleAuthorizePreparedStartProbe,
sessionLifecycleAbandonAcceptedInputProbe, sessionLifecycleReleaseAbandonedClaimProbe
and sessionLifecycleOrdinaryAdmissionAfterAbandonProbe in
TestSessionLifecyclePreparedStartPolicyProbePostgres. Durable parking/retry and
crash reconciliation use sessionLifecycleRetryParkedStartProbe and
sessionLifecycleReconcileExpiredStartProbe in
TestSessionLifecycleParkedStartControlProbePostgres. The bounded first-marker/
invocation CAS is sessionLifecycleMarkProviderInvocationProbe in
TestSessionLifecycleProviderLaunchMarkerProbePostgres; these model possible entry,
not observed provider acceptance. Shared sessionLifecyclePossibleAcceptanceProbe
supplies current/prior FIRST-or-entry evidence to authorize/mark/retry/abandon/
reconcile examples, including missing-marker uncertainty in
TestSessionLifecyclePossibleAcceptanceProbePostgres.
sessionLifecycleSupersedeUnlaunchedIntentProbe in
TestSessionLifecycleParkedSupersessionProbePostgres models same-transaction
known-unlaunched closure and logical release under current transition authority,
including exact F1 acknowledged-control versions, not stale retry authority.
TestSessionLifecycleIdleSessionEndProbePostgres distinguishes the broader thread
end from idle-only session end and explicit abandon-then-end.
TestSessionLifecycleStartControlAtomicityProbePostgres compares complete state
when either ordinary parking or expired-start control writes conflict: no
dispatch/job/lease/check authority may advance if the exact control write fails.
TestSessionLifecycleSealedStartResultProbe
models sealed outcome validation and final-ready effect selection.
sessionLifecycleActivateResumeCapabilityProbe in
TestSessionLifecycleResumeCapabilityGateProbePostgres models exact passed
provider/build/identity conformance receipt matching, not actual adapter support.
sessionLifecyclePinnedBuildRolloutProbe in
TestSessionLifecyclePinnedBuildRolloutProbePostgres separately models candidate
image/provider/build/identity eligibility before routing and preservation of the
compatible serving image; no real deployment runs.
Synthetic
probe_bound_dispatches/probe_pending_input_inbox/probe_start_attempts/
probe_start_targets/probe_accepted_input_controls project existing dispatch/inbox/
job-attempt/target/control authority; they are not new production ledgers.
probe_start_controls is the isolated projection of retained dispatch-owned
control fields, not a new required production table; deleting an execution
preparation does not delete that authority. probe_resume_capabilities and
probe_resume_conformance_receipts project the proposed cutover/build conformance
gate, not a caller-selected supported boolean.
sessionLifecyclePublishSnapshotProbe, sessionLifecycleClearPendingSnapshotProbe
and sessionLifecycleRetainSnapshotProbe in
TestSessionLifecycleSnapshotPublicationCASProbePostgres use
newSessionLifecycleSnapshotPublicationProbe with synthetic probe_snapshot_objects,
probe_snapshot_upload_receipts and probe_snapshot_retention_authorities. They
illustrate exact fresh-attempt key/receipt promotion. Current pending receipt/
key/token/version matching fences an old completed upload even before the newer
pending upload publishes; stale pending Clear cannot clear the newer receipt.
Pending-upload Clear is distinct from retention's installed-reference Clear,
which requires the exact tombstone association CAS. These do not exercise actual storage Save/Delete,
asynchronous uploader effects or real publication/GC concurrency.
sessionLifecycleTombstoneSnapshotObjectProbe,
sessionLifecycleBindSnapshotSourceProbe and
sessionLifecycleCompleteSnapshotDeleteProbe in
TestSessionLifecycleSnapshotGarbageCollectionProbePostgres project pre-Save
cleanup-job discovery, grace, positive writer settlement, current/preparation/
parked-source references, tombstone/reference exclusion and durable exact-key
delete handoff. Synthetic probe_snapshot_cleanup_jobs control fields represent
the typed existing-job resource metadata; probe_snapshot_source_references stand
for the real joined source associations, not a new production pin table. A SQL
object row deletion is only a stand-in for Save/Delete observation, never evidence
of actual file/S3 I/O, late-write fencing or concurrent publication/GC correctness.
Separate prototype statements do not establish the required compound abandon/
job-close/claim-release transaction atomicity or real concurrent no-launch proof.
The parent ledger identifies exact symbols/cases run; no production checks,
provider effects, locks across I/O or storage deletion are proven by these probes.

legacy_request=true means created before THIS session's activation, including
a compatible producer during rollout. Unproved old producers are not blessed:
only exact compatible producer identity permits the bounded legacy wait.
proof_policy_version/enforcement_version both1 bind active schema/interpreter.
Fixture jobs.session_id/phase.kind stand in exact real payload/lane/allocation and
activity-boundary joins; pause_* fields normalize immutable finalization/phase/
provider/drain anchor. A's scope digest is identity, never native-artifact bytes.
Known conversation ID before first proof does NOT mean a native availability
observation was published. An exact compatible live producer with no first
observation still returns checkpoint_pending (legacy: fixed waiting), not loss.
Only an explicitly expected/published observation reference that is missing, or
positive current native/workspace loss, returns unavailable and dominates live
producer status. Raw absence before publication cannot manufacture either ready
or unavailable. Bind observation identity/version to the actual producer source.
Fixture readiness_published_at represents that exact final proof/observation
publication receipt boundary, not permission inferred from a timestamp. Production
joins the recorded request/producer/phase publication receipt; a caller cannot
set an arbitrary time to assert observation existence or expected loss.
Legacy threadless requests/jobs need exact producer-phase/dispatch evidence of
their normalized primary lane before using this policy; never choose whichever
tab is primary now. Missing lane evidence stays explicit recovery/control.
Source-specific workspace receipts are required for sibling/domain transitions;
real provider/native-state and actual target checks remain 0a, not invented data.

| Disposition | Eligibility / mutation |
| --- | --- |
| Ready | Exact A tuple/drain, certified current watermark and positive usable workspace/target evidence. Attach classification, not answer/grant. Bound execution still requires VerifyResumable and final authorization. |
| Context-pending | Named context-affecting operation or prior pending at latest descriptor. Selected revalidation/action, no answer/grant or forced reset merely because B progressed. |
| Checkpoint-pending (new) | Exact compatible live producer can still publish first proof even if conversation ID already known. No five-minute deadline; absent never-published observation is not loss. Expected/published observation missing or genuine known loss instead yields unavailable. |
| Waiting (legacy) | Exact compatible live producer can still supply proof; fixed first-classification+5min deadline, never extended by polling. No answer/grant/job while waiting. |
| Unavailable/unprovable control | Known target loss/expiry, invalid scope/receipt, old unprovable paused producer or expired legacy wait. Preserve intent and exact reason/action; no automatic recovery/reset authority or veto of B. |
| Accepted-undelivered start control | Answer/grant/binding committed, selected native check failed before known provider launch. Keep rows and exact consumer identity; retry/control notice only, never reanswer/regrant/fresh-history fallback. |

Stored readiness_state is classification metadata, not final authority. Even if
ready remains stored while certificate/availability changes, pending answer
admission uses the canonical current tuple/certificate/workspace policy. After
answer/denial commits, that request is no longer in pending-certificate fanout:
bound start uses immutable request identity + committed dispatch and CURRENT
preparation/target/descriptor, not an obsolete pending-only watermark or a second
answer. Every launch uses final authorization; no ready-only shortcut. 0a typed mapping distinguishes context_pending,
context_unavailable, unsupported, accepted-undelivered and delivery_unknown,
without implying consent, input acknowledgement or reset permission.

Capacity-wait accepted async input has a 24-hour fixed deadline from durable
acceptance; only **legacy** checkpoint wait uses the five-minute deadline above.
Post-activation valid-live checkpoint-pending does not inherit the capacity/legacy
timeout: activity/liveness monitoring reports progress without superseding consent.
Contention uses
same receipt deadline. Deadline becomes visible `recovery_required` for paused
input or `terminal_expired` for other unexecuted ingress, never silent dead letter.
No automatic answer/cancel/expiry beyond the classified recovery control state.

Proposed `POST /api/v1/sessions/{id}/human-input-requests/{request_id}/recover`,
paired `143-tools sessions human-input recover` and existing request detail action
return typed mode choices. Require existing write role, org/resource/actor
authorization, explicit thread, expected epochs/classification version, idempotency
key, mode and checkpoint reference; reattempt adds failed recovery receipt/version.
Detail renders recovery action while parent awaiting_input (no failed-only retry).

Recovery state: recovery_required -> recovery_pending (accepted receipt/drain
wait) -> recovering -> awaiting_fresh_request -> resolved; blocked/failed/uncertain
are visible alternatives. Duplicate identical key returns same receipt, different
digest conflicts. Under locks acceptance requires an acknowledged session-scoped
set of pending unconsumed requests, exact classifications and owner, no uncertain
grant/delivery, valid provider mode; then supersedes only acknowledged requests
and their unconsumed waiting intents plus explicitly listed known-unlaunched
parked dispatch/control versions, increments affected epochs,
and binds recovery dispatch/wait. Preserve old transcript/checkpoint/audit and
separately committed grants. Recovery dispatch is authorized from this control
receipt, not failed parent or old readiness. Ordinary sends/answer/cancel to this
request return recovery_required or recovery_pending, not answered.

Checkpoint mode initially available only for provider adapters that positively
implement `CanReaskCheckpoint(requestKind, checkpointKind)` and a tested re-ask
contract. Default all adapters to unsupported until their test proves fresh
question/approval after restore; absence of support returns
CheckpointReaskUnavailable plus explicit start-over option. Do not assume native
provider resume skips an unanswered approval. Start-over begins a new provider
conversation from non-consent task context, excluding superseded pending approval
and waiting answer/denial text from seed/live input. Fresh provider request needs
new explicit approval. Historical text is evidence, never active consent.

### F1: whole-workspace scope, availability and acknowledgement

No lane-local restore/reset exists. `SessionStore.ResetForRetry:2162` resets shared
result/status fields; checkpoint is session-wide (`session_store.go:1511`), and
tabs share one workspace. Both recovery modes therefore declare
`workspace_scope=whole_session`. Descriptor includes a **session-scoped recovery
set**: exact IDs/versions/epochs of every recovery-required paused request, plus
ended/archived sibling stale pending requests and their unconsumed waiting intents.
Also list known-unlaunched bound parked answer/denial dispatches and exact control
versions that recovery would supersede. Their requests remain answered or exact
committed-denial, not relabeled pending; their grants/audit remain intact.
The user can acknowledge this whole set atomically, avoiding two recovery-required
tabs permanently blocking each other. EndThread alone does not clear a pending
request; its stale request is explicitly listed and superseded only by this
acknowledgement. Live or unclassified pending sibling input, unacknowledged queued
work or nonselected nonterminal consumer, active runtime/executor/holder, unknown delivery/grant, uncertain
publication, retained domain owner, active preview or pending checkpoint/changeset
lease still blocks. No implicit sibling cancellation/reset is allowed. The affected ordinary producer may
receive its normal fenced graceful stop, never forced kill to meet deadline.

Even when quiescent, a sibling's filesystem progress can be discarded by restore.
Require a typed acknowledgement object, not a generic boolean:
`{scope:"whole_session", session_id, workspace_generation, workspace_revision,
affected_thread_ids:[all tabs including archived], sibling_state_digest,
recovery_requests:[{request_id,classification_version,session_epoch,thread_epoch}],
superseded_waiting_intent_ids:[exact IDs], recovery_set_digest,
superseded_parked_intents:[{dispatch_id,control_receipt_id,control_version,binding_digest}],
mode, checkpoint_id, acknowledge:"discard_workspace_changes_outside_selected_checkpoint"}`
(start-over uses `"discard_current_workspace_for_fresh_start"`). Server supplies
the current descriptor; UI explicitly says all tabs share this reset/restore.
Admission compares every token and returns ScopeChanged with fresh descriptor
on drift. Under domain/parent/stable lane/request/intent locks, verify every listed
request is still recovery-required or ended/archived stale pending and every
listed intent is unconsumed. Atomically supersede exactly that set, preserve all
other requests/intents; any new/uncertain item aborts without partial supersede.
For every listed parked intent require the accepted recovery receipt/digest to
bind its exact dispatch/control version and ALL-binding no-acceptance proof;
stale/missing/unlisted control aborts the WHOLE recovery. A generic acknowledge
boolean cannot supply this authority. Compose the canonical supersession seam;
logical closure never substitutes for F1 physical quiescence.
Acknowledgement cannot override an availability blocker or authorize another
person's otherwise-protected resource. Preserve every sibling's transcript
and status; do not present their historical result/diff as current filesystem
state after workspace generation changes. Add stale-workspace result labeling
as part of 0a recovery UI. Nonmutating recovery preparation may be offered without
ack, actual dispatch cannot. Revalidate descriptor/quiescence before restore;
new sibling work after acceptance blocks execution visibly, it is not erased.

The check-to-restore interval needs durable exclusion, not a last-second read.
Acceptance records the exact recovery ID/token on the locked session. Under that
same parent lock, runtime starts, preview/materialization/publication, topology
mutations, snapshot writers and other workspace-mutating operations must honor
this reservation. New input may remain queued, but cannot execute or reach a
provider while recovery owns the workspace. Execution rechecks the acknowledged
descriptor and all drain receipts, then enters `recovering` and acquires the
host-local exclusive workspace-operation lock before any restore/reset. A changed
descriptor during preparation gives a visible scope-changed/blocked receipt and
requires an explicitly acknowledged reattempt; it never expands the old consent.
Topology/file mutations stay excluded throughout physical restore. Commit the
new workspace generation and restore outcome before conditionally releasing only
that recovery's reservation. Missing/expired owner lease or an unknown physical
outcome holds the reservation for reconciliation; it is not permission for a
successor to restore or start. A new recovery attempt normally requires positive
drain of previous physical operation, not expiry. A bounded reconciler presents
`InspectRecoveryOperation` (exact allocation/host/operation receipt status),
`RetryRecoveryObservation` (read-only probe), and, when proof exists,
`ReplaceIsolatedRecoveryWorkspace` (explicit start-over-only action). No indefinite
invisible hold and no automatic destruction: blocked/uncertain shows reason,
last observation, next retry and available resolver action.

For unknown physical outcome, authoritative host/runtime adapter must prove either
exact old operation exited and workspace was destroyed, or old workspace is
isolated with revoked storage/mount/container access and no ability to address
any new workspace/publication identity. Host unavailable alone is NOT proof.
Persist immutable isolation/destruction receipt scoped old recovery/token/workspace,
then require new whole-session acknowledgement with mode=start_over,
isolation_receipt_id and descriptor digest. Under reservation locks bump physical
workspace generation, use fresh sandbox/storage/container/operation identity and
newly fenced publication capability; old operation cannot access new storage or
publish on new generation. Never reuse uncertain snapshot/restore as checkpoint.
If isolation cannot be proved, action is unavailable with visible
RECOVERY_HOST_PROOF_REQUIRED; authorized operator repair through existing host
controls must produce proof, not bypass the reservation. This is a named safe
resolver, not a claim that a missing host means drained. Add all reservation
callers below to floor/races; current holder registry alone is not this lock.

Activation blast radius is explicit: unprovable legacy paused inputs will require
recovery; all checkpoint re-ask adapters default unsupported until proven, which
can leave start-over as the only available mode. Activate per session only after
showing that session's affected request set, wholeworkspace impact, blockers and
mode choices. User can leave it blocked/preserve workspace; notice is not consent
to reset. No silent global conversion followed by destructive fallback.
Provider-specific exact resumability checks/continuation-mode handling remain 0a
proof; unsupported selected checks produce explicit control, never false verified.
This is not a mandatory provider-private export/isolation rollout gate.

Failed recovery reattempt uses exact failed receipt/version and new key, validates
current generation/end/domain/quiescence and creates new attempt/dispatch. Unknown
execution first reconciles; executing receipt cannot be reset. End/successor/owner
race rejects stale reattempt. Missing checkpoint never auto-falls back destructive.

## 8. Adapter outcomes and integration receipts

All outcomes carry typed code, resource/command receipt where authorized,
retry-after/action when relevant; never raw DB/provider text. Existing successful
envelopes stay; new waiting/recovery outcomes are deliberate 0a additions.

| Durable result | HTTP | Internal tool | Slack / Linear asynchronous adapter |
| --- | --- | --- | --- |
| Accepted/applied/idempotent | Existing 201 message / 200 answer-control envelope | Success + exact committed receipt | Mark ingress accepted against dispatch; retry external update only. |
| Accepted waiting checkpoint/capacity/drain | 202 with receipt, state, applicable deadline/action (NULL deadline for valid post-activation live checkpoint); request unanswered | `accepted_waiting` receipt, not delivered | Persist waiting outcome; visible waiting card/activity, reconciler outlives handler job. |
| Accepted-undelivered resumability control | Existing answer success stays committed; receipt exposes accepted_undelivered with RESUME_CHECK_PENDING / RESUME_UNAVAILABLE / RESUME_UNSUPPORTED and exact action | accepted_undelivered / action_required, never a second answer | Keep same accepted dispatch/request/grant; versioned visible start-control notice and same-binding retry only with proven no launch. No automatic reconstructed/fresh conversation. |
| Normalization incomplete | 409 NORMALIZATION_PENDING + Retry-After: 1 if unaccepted; 202 waiting_normalization if exact ingress receipt accepted | retryable normalization_pending / accepted_waiting | Persist/reconcile receipt independently of handler budget; visible preparing/waiting update, never delivered/answered. |
| Historical uncertainty | 409 HISTORICAL_INPUT_REVIEW_REQUIRED with ReviewHistoricalInput/ReplaceHistoricalIntent; new accepted input 202 held receipt | action_required / accepted_waiting | One visible authorized review/action notice; no automatic ordinary execution or inferred replay around it. |
| Compatible workspace proof pending / artifact unavailable | 409 INPUT_CONTEXT_PENDING with RevalidateInputContext, or INPUT_CONTEXT_UNAVAILABLE with explicit recovery choices; accepted wait 202 | action_required / accepted_waiting | Preserve A request/provider proof and wait visibly; B progress alone never announces destructive recovery. |
| Checkpoint pending, unaccepted sync | 409 CHECKPOINT_PENDING + retry-after | retryable checkpoint_pending | Durable waiting acceptance before acknowledging ingress, else retryable job/backoff. |
| Lock contention, unaccepted | 409 LIFECYCLE_CONTENDED, Retry-After: 1 | retryable contended | Preserve existing receipt if accepted; else ingress retry. Never “answered”. |
| Recovery-required/pending | 409 INPUT_RECOVERY_REQUIRED or 202 INPUT_RECOVERY_PENDING + request action/receipt | action_required / accepted_waiting | One versioned private notice with actionable app link; no approval/grant until fresh request. |
| Invalid body/kind/choice/mode/idempotency digest | 400 INVALID_ANSWER/INVALID_MODE; conflicting key 409 IDEMPOTENCY_CONFLICT | invalid_argument/conflict | Terminal rejected receipt + visible safe error; no automatic retry of payload. |
| Unauthorized / absent resource | Existing 403/404 without cross-tenant disclosure | forbidden/not_found | Terminal ingress decision; safe authorized-surface notice, no resource leak. |
| Ended/stale epoch/request no longer pending/owner requirement | 409 STALE_ADMISSION / NOT_PENDING / SPECIALIST_CONTROL_REQUIRED | conflict/action_required | Terminal stale receipt and visible outcome, never resume successor; link authorized current action. |
| Destroyed checkpoint / unsupported re-ask / scope blocker | 410 SNAPSHOT_EXPIRED; 409 CHECKPOINT_UNAVAILABLE/RECOVERY_SCOPE_BLOCKED | unavailable/action_required | Visible explicit mode choice/blocker; never implicit start-over. |
| Commit unknown / provider delivery uncertain | 503 COMMAND_OUTCOME_UNKNOWN with same receipt key; 409 DELIVERY_UNCERTAIN for unsafe replay | unknown_outcome/reconciliation_required | Do not mark failed-as-unaccepted or repeat admission; reconcile exact key, card says pending confirmation. |
| Waiting-intake deadline expired | Fetch receipt returns terminal_expired or recovery_required | terminal/action_required | Versioned once-only error/recovery notice, independent notification retry job. Does not expire a parked accepted-undelivered control with its execution preparation/lease TTL. |

Receipt map: Slack webhook delivery ledger -> `slack_inbound_events` (delivery FK,
status/job) -> dispatch result; human-input card update job references dispatch +
request/version and uses existing `slack_outbound_messages` target. Existing
`handleSlackHumanInputAnswer:6649` answers before chat.update then enqueue; new
order is commit answer/grant/binding -> update job. A failed chat.update cannot
repeat approval or continuation. `slack_inbound_events.MarkProcessed` means ingress
handled, not provider delivery; lifecycle outcome records waiting/terminal.
Linear `linear_agent_prompted_messages.Reserve` (migration 000136, append function
`:256`) retains bridge/comment uniqueness; add dispatch/outcome to that row in same
command tx. `linear_agent_activity_log` remains external activity idempotency,
not proof of message/job admission. Existing `emitOnce` retries only activity after
accepted receipt. Review-loop/pass, repair reservation, recheck dispatch and
automation result marker keep their own existing IDs as ingress/domain proof;
ordinary reconciliation cannot claim them without typed authority.

## 9. Compatible floor, atomic activation, rollback

Selected sequence, with activation disabled by default:

1. Add nullable/versioned representation and indexes, proof writers and recovery
   API/tools/UI/notice workers. Deploy canonical InTx seams and bounded ordering
   to every inventory caller, including DB-only/nested callers, grants, initial
   runtime start, executor/reaper/seed/live delivery and status cleanup. New code
   handles version 0 with characterized semantics while not relying on epochs.
2. Establish compatible build floor across API, ingress replayers, scheduler,
   generic workers, standalone executors and all long-lived provider pollers.
   Each publishes lifecycle consumer version/build, active job/attempt version
   and writer instance lease. After old registration expires, independently
   prove no pre-floor job executor or in-flight transaction remains. Connection
   application_name includes build/role; PG transaction census plus instance/
   runtime inventory and drain receipts form gate. Missing inventory blocks.
   Do not force-stop work. All dispatch targets require consumer version 1;
   absent version/old target cannot claim new payloads. Implement claim eligibility
   for generic and handoff/executor claimers, not payload convention alone.
3. Shadow legacy scans are inert: no effective classification, answer eligibility
   change, supersede/expire or notice. Once floor proved, normalize exact legacy
   jobs/input and naturally drained attempts; uncertain work has durable receipt
   and visible action. Capture session cutoff only after old writers drained.
4. For each pending request, acquire canonical domain/session/lane/request locks;
   re-read status, owner, epochs, checkpoint and producer proof. Answer/cancel
   winner means no pending classification. Commit classification version,
   enforcement version, ready/wait/recovery receipt and unique notice intent in
   **one** transaction. Request creation sets legacy_request based on locked
   per-session activation state; after activation false plus proof/enforcement
   versions commit atomically, before activation true even on compatible build.
   Unknown old producer is never blessed by this flag. No
   classified-but-unenforced interval. Every pre-proof pending request must have
   active ready/wait/recovery-required disposition before answer-enabled M1.
5. Notice worker rechecks request/version/generation and renders current state;
   delayed recovery notice after accepted answer/recovery becomes obsolete, not
   contradictory. Recovery API/tool/action is tested reachable from paused state
   before any recovery-required classification goes live.
6. Activate new admission/start/end/reconciler slice only after all its consumers
   and pending backlog dispositions are proved. Keep one engine per caller, no
   dual effects. Record supported rollback build/version. Rollback may disable
   new intake and retain compatible consumers/reconciler, never roll below floor,
   drop epochs/receipts, reinterpret uncertain delivery, or restore answer-latest.
   Database guard rejects an older writer version for active records; deployment
   gate rejects incompatible build. Require both gates, not process census alone.
   Concretely, every protected unit of work uses an explicit READ COMMITTED
   transaction and `SET LOCAL lifecycle143.writer_version=1` before any protected
   write; no protected autocommit path remains. API, generic/standalone workers,
   reclaimers, integrations, scheduled jobs and operational scripts use this
   wrapper. Session-level startup connection settings may optimize non-pooled
   deployments, but are not safety authority with transaction-pooling proxies.
   BEFORE triggers on protected session/thread/request/dispatch/runtime/inbox/
   batch/executor writes and lifecycle jobs inspect cutover/record version and
   reject missing/older settings with `55000 LIFECYCLE_WRITER_TOO_OLD`. New job
   claim SQL also checks registered consumer version >= payload binding version.
   These are compatibility interlocks, not user authorization; middleware and
   durable tenant/owner checks remain mandatory. Activation's own tx sets version
   1, installs guard state and active record versions atomically. Version-0 records
   remain compatible before activation; triggers do not strand old jobs early.

Activation/M1 completion is scoped to an explicitly enumerated session/provider
cohort. All requests/consumers in that cohort must meet these rules. The compatible
version-0 tail remains visible as activation-deferred with characterized old
semantics; partial cohort completion is not repository-wide lifecycle ownership.
Full-plan acceptance cannot leave an unbounded active version-0 tail. Every bound
answer path needs a TESTED VerifyResumable implementation for each exact provider,
pinned CLI build and ResumeIdentity variant BEFORE activation, across every
runnable lane in the cohort. Version0 sessions lacking a tested tuple remain
visibly deferred; active sessions never downgrade. Later provider/build/variant
or new-lane changes must pass the same capability gate or reject/defer that
change, not use generic verified. Unexpected unavailable/unsupported at runtime
parks the selected start; it does not make a known-unsupported cohort eligible.
Absent private-isolation capability is still not a gate. Persist the tested tuple/
verification protocol/release receipt in cutover/build metadata and check actual
target build against it; source-specific evidence, not caller-supplied booleans.
Pinned CLI/image rollout is gated BEFORE enabling any new or mixed-build target
for an active cohort. The activation controller checks the exact provider/build/
image/ResumeIdentity conformance tuple before the scheduler's eligible target set
changes; node/build registration advertises that tested tuple, not generic writer
version alone. An untested/unsupported image may be staged but cannot receive
active-cohort starts. Keep existing compatible images serving and expose
provider_rollout_deferred with exact tuple/reason/retry; do not deploy first and
park all answers on the new default image. During mixed rollout, claim/prepare/
final target selection must choose a positively compatible registered image and
recheck the actual target tuple. Existing accepted binding stays pinned to its
recorded build/native identity; a passed new-build test cannot silently rebind
that answer. New lane/default/provider switches have the same gate. Unexpected
runtime capability loss still parks only the affected binding under the existing
control contract, never active-version downgrade or proof that rollout was safe.
The lifecycle activation controller owns each deferred attempt and receipt. An
explicit authorized operator retry of exact org/session and expected cutover
version restarts that same attempt only; queue/ordinary input cannot silently
restart it or bypass its guard. Expose current reason, affected provider/request
cohort and retry action through that existing planned activation control surface,
not a new automatic recovery or operator backdoor.

Lifecycle-job guard exclusion is positive: registered typed job kind classified
non-lifecycle AND validated payload has no session/thread/dispatch/allocation/
artifact association or binding version. For example source
newSyncGitHubOrgRosterHandler (`worker/handlers.go:14243`) operates on installation/
roster and is eligible for explicit non-lifecycle classification. A reviewed kind
registry covers every generic claimant's type before 0a rollout; unknown kind,
malformed binding or any retained lifecycle association is not an exemption.
Artifact-delete/availability/notice/revalidation jobs are lifecycle jobs even
without run_agent/continue_session type and must carry exact protected scope.
Measure SET LOCAL protocol/extra-round-trip cost, trigger lookups and batched
certificate metadata updates on hot claim/heartbeat/lease paths, including
transaction-mode pooling, against section 5's provisional latency/throughput
gates. Correct semantics alone do not establish acceptable hot-path overhead.

Guard coverage includes authority/reservation rows that need not write `sessions`.
Protect INSERT/UPDATE/DELETE on the following exact tables, not just status
columns; minimum-version guard does not substitute for canonical owner/epoch/
lease authorization. Each mapping is org-scoped. On UPDATE resolve **both OLD
and NEW scopes**, on DELETE OLD, on INSERT NEW; clearing/moving an association or
deleting a blocker cannot escape the older scope's active cutover. Guard version
is inherited/stamped from active cutover and cannot be downgraded. Existing active
associated rows receive `lifecycle_guard_version=1` in their session activation;
this provenance still rejects old mutation if a referenced parent later vanishes.

| Protected authority/reservation tables | Session/cutover identity route |
| --- | --- |
| `automation_target_sessions`, `automation_runs`, `automation_run_results` | Generation's session_id; run's session_id or target_id + target_generation -> exact generation; result's run_id -> run -> generation/session. Guard `automation_targets` active_generation/current ownership/wake changes against every old/new associated generation session, including retained/release-pending generations. |
| `code_review_pr_state`, `code_review_revision_assessments`, `code_review_session_metadata`, `code_review_recheck_dispatches` | Direct session_id or old/new active_session_id; assessment/dispatch -> exact session and PR association. A cleared active pointer still checks OLD session. PR state changes also check retained session marker associations, not only active pointer. |
| `pull_request_repair_runs`, `pull_request_feedback_batches` | Direct org/session_id and exact thread/PR association; old/new reservation/head/job fields remain guarded even when active=false or batch terminal. |
| `session_changesets`, `session_changeset_leases`, `session_changeset_split_plans`, `session_changeset_split_paths`, `session_changeset_split_omissions`, `session_diff_snapshots` | Direct org/session_id; lease and split child identities cross-check their changeset/plan's same session. Guard materializing/worktree/head/revision/primary/lease acquire-renew-release and topology edits, not only status. |
| `preview_instances`, `preview_runtimes`, `session_sandbox_holders` | Instance's org/session_id; runtime's preview_instance_id -> instance -> session; holder's direct session_id. Guard preview reservation/restart/ready/lease and holder create/renew/release/expiry that change F1 drain predicates. Positively standalone branch previews with NULL session and no shared workspace/holder association are outside this session cutover; missing/ambiguous provenance is not that exemption. |
| `session_publications`, `session_execution_metadata`, `session_automation_links` | Direct org/session_id and exact changeset/publication or automation association. Publication attempt/receipt changes and grant capability snapshots cannot bypass active session by omitting a status update. |
| `session_input_workspace_compatibility` | Direct org/session_id cross-checked through request_id -> exact pending request/session and immutable anchor; guard OLD/NEW request/scope as well as certificate/version mutation. It is not an unguarded evidence-writing escape. |
| `session_runtime_start_preparations`, dispatch parked-control fields/version, job awaiting_start_control/refund fields; exact-key cleanup/start-control jobs | Org/session/lane plus fixed request/dispatch/job association; guard OLD/NEW execution preparation AND retained dispatch-owned parked-control scope/version, OLD on delete. Closed preparation deletion cannot delete/invalidate its captured dispatch control. No live-lease/TTL bypass or immutable identity/generation replacement. Cleanup job routes exact key/policy receipt to session; parked status is not an unrelated-job exemption. No bundle/export/refcount tables. |
| Existing allocation/continuation-attempt launch-marker fields and session pending-upload receipt fields | Allocation -> exact org/session/lane/dispatch/job/attempt; FIRST marker cannot clear, ordinal/nonce/purpose mutate only under current permit/lease CAS. Pending upload -> exact session/publication/upload/key/epochs/gen. Guard all OLD/NEW routes, metadata-only writes and deletes, not status alone; minimum writer version supplements canonical authority, never proves it. |
| `jobs.snapshot_object_control` and snapshot_object_cleanup jobs; publication/upload -> cleanup-job association | Immutable payload's org/session/publication/upload/key/token/epochs/generation plus current typed object/control/tombstone version route OLD/NEW and delete to the exact cutover. Guard writer settlement, supersession, reference installation, due/backoff/attention, Delete outcome and cleanup retention, not just job status. A snapshot resource job is lifecycle-associated, never the unrelated-job exemption; missing provenance fails closed. No receipt/control may be deleted before its outstanding object/write/delete obligation resolves. |

Original protected tables (sessions/threads/requests/dispatches/runtimes/inbox/
batches/executors/lifecycle jobs) keep their existing direct or payload/foreign-key
routes; newly introduced compatibility/recovery/cutover rows are protected from their first
active version. Missing/malformed route for a version-1 record, or a record known
to be associated with an active session through retained marker, job/allocation,
generation/reservation or workspace reference, fails closed as
`LIFECYCLE_GUARD_PROVENANCE_REQUIRED`; it must not be treated as unrelated/version0.
Rows positively unrelated to any active cutover retain version0 coexistence.
Compatible creation orders identity rows before pointers that depend on them;
for example canonical automation generation creation holds target lock, inserts
the exact generation/session row, then advances active_generation in that same
transaction. It cannot temporarily point at an unresolvable new generation and
ask the guard to trust a generic bypass. Current pre-floor pointer-first writes
remain version0-compatible only before their associated session activates.

Activation and child-only guard checks use the nonblocking per-session cutover
interlock above; late-discovered scopes may continue after successful shared
try-lock. Only exclusive conflict aborts the whole transaction; successful late
acquisition is not a livelock/restart condition. All protected transactions are
READ COMMITTED (reject REPEATABLE READ/SERIALIZABLE for this protocol), so after
shared acquisition an explicitly VOLATILE trigger's fresh lookup must observe
the then-current active version, never trust the caller statement's earlier
snapshot. Chosen implementation is an explicitly VOLATILE trigger function with
a fresh post-acquisition lookup; 0a must validate its PostgreSQL snapshot behavior
before relying on it. Activation holds exclusive interlock through locked
reclassification/guard stamping/active commit. Acquisition conflict aborts whole
transaction under bounded protocol; implicit waits retain lock_timeout. Activation
uses at most three try-lock attempts within its 2s transaction budget, then stays
inactive for that session and reschedules with jittered 10–30s backoff. After
five minutes of repeated contention surface `ACTIVATION_CONTENDED`, retain
compatible semantics and stop automatic activation for that session until an
explicit authorized activation-controller retry with expected cutover version;
other sessions proceed. Never starve intake by spinning exclusive
locks or silently force-stop holders. An intake barrier would need separate user
authorization. This is
an unproven 0a gate, not evidence that the check-before-activation race is closed.
Barrier probes must start late INSERT/UPDATE/DELETE statements before activation,
delay their guard lookup until after activation commits, and prove active version
is observed/rejected rather than preactivation snapshot accepted. Also prove
multi-scope OLD/NEW successful late try-lock versus exclusive-conflict abort,
ClaimNextRunnable/bulk-sweep forward progress, FK/cascade behavior, unversioned reservation-writer
rejection and version0-before-activation; process census alone is insufficient.

M0 selects this protocol; 0a must implement the build registration/claim/schema
guard and prove floor/rollback in mixed-version PostgreSQL and integration tests.
An empty scan or green unit tests alone do not establish a deployed floor.

## 10. Evidence ledger and remaining proof

This companion originated in source inspection; the parent plan records the
M0 validation commands and results. No runtime/schema change, production query
or migration is included. Keep source facts, isolated query probes and proposed
implementation gates distinct:

| Evidence | State here | Required next proof |
| --- | --- | --- |
| Function/writer/producer inventory | Executable manifest owned by M0 inventory change; references above are source-derived | Run inventory/tests and review every expanded caller before its migration slice. |
| Existing-entry behavior tests | Existing test names in parent plan; M0 adds characterization only | Record exact focused commands, exits, revisions and skips in parent. |
| F1/F2 contract | Selected above, no unresolved lane-local reset or blanket rejection rule | 0a typed outcome/wholeworkspace acknowledgement tests, plus all terminal caller call-throughs. |
| SQL/schema and readiness/discovery | Proposed schema; canonical minimal-schema readiness/workspace-state/start-control examples in test source (case counts/results in the parent validation section) | Disposable migrated PG: production indexes/explain, atomic dispositions, cutoff/normalization, uniqueness, nonterminal parked exclusion versus runnable sets, exact consumer exclusion and set-based all-request metadata updates. |
| Lock progress/activation/rollback | Minimal-schema lock-pattern probes (counts/results in the parent validation section); actual rolling callers not tested | Independent connection barriers for all baseline pairs, pre-floor/floor and floor/new; no effects on abort; unknown commit no automatic replay. |
| Native resume / cleanup | Source confirms ID-based resume, reconstruction fallbacks, mutable ordinary key and delete-before-metadata cleanup; proposed trusted same-session checks/handshake and unique-publication-key discipline only | Tested exact provider/build/ResumeIdentity activation tuples; actual target/preparation/final authorization and sealed outcomes, bound-answer no-fallback; durable park/control beyond execution TTL, all-source FIRST/invocation fences and post-permit running effects; all named PR-upload/cleanup call-throughs, fresh-key receipt/GC and tombstone-before-delete proof. |
| Recovery user journey | Proposed request/control/API/tool contract | Paused notice -> action -> blocked/safe restore -> fresh request -> new consent; checkpoint re-ask unsupported until proved, not a private-context program requirement. |

Before declaring 0a safe: prove first-start/end and delayed retry races; seed
selection/end/attach/ack; old poller vs successor; exact answer/crash/retry and
dedupe collision; current owner end/recovery races; publisher-enabled transaction
tests; Slack/Linear accepted update retry and visible waiting/expiry; shadow vs
atomic activation, old writer rejection and late notice; exact-own terminal
settlement with stale shared projection. These are implementation gates, not
reasons to claim M0 repaired existing runtime behavior.

The readiness probe's `writer_version`/`can_publish_checkpoint` and synthetic
`drain_receipt_id` stand in for registered compatible-writer capability and an
authorized immutable physical-drain receipt. They are not authoritative booleans
that a production caller may supply. Its jobs.session_id and phase.kind columns
simplify real payload/phase joins in the isolated fixture. Production 0a uses
the actual job payload identity and activity-phase kind/boundary, verifies receipt
provenance, and persists the applicable legacy first deadline with classification
under canonical locks. Readiness and workspace/certificate prototypes use only
isolated synthetic fixture state; they do not prove production source authority,
bounded real set-based fanout, actual provider/native-target availability or launch,
start-preparation/final-authorization/park/launch races, real queue/reaper/executor
behavior or upload/availability/GC behavior, activation persistence
or real concurrent writer behavior. The parent validation section records the
probe groups and case counts; normative choices
here are not a substitute for those results or the remaining 0a physical proof.
