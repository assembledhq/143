# Give shared session lifecycle decisions one owner

Status: Milestone 0 design and characterization accepted by independent
adversarial review on 2026-10-18, with no critical or major findings remaining.
Approval covers the plan, contract, inventories and synthetic probes only; real
caller, full-schema, concurrency, storage, provider and image proof remains in 0a.
No compatibility-floor or new lifecycle runtime behavior has been enabled.
Milestone 0a has started with passive transaction and grant-persistence
foundations, independently approved on 2026-10-18 after follow-up review.
Business callers, writer registration, schema guards and activation remain
unchanged; this does not complete 0a.

Source baseline: `ffe919dcf63807795eb85e1aa24cebe26ef36521`. Source anchors
identify the inspected implementation; the executable inventory rechecks the
current tree rather than treating line numbers as permanent identities.
Publication preparation uses base `c80f042ab17f9414f465212ac0212351a9a2bd00`;
inventory checks and source-drift review must run against that base before
publication. The earlier M0 review does not establish exact-head approval of
publication edits or later source changes.
Recheck the caller inventory before each migration slice.

Milestone 0's concrete contract and evidence are maintained in
[session-lifecycle-m0-contract.md](session-lifecycle-m0-contract.md). That document
specifies the compatibility release; it does not claim that release exists.

## Goal and scope

Make each shared conversation transition have one implementation and one
transaction owner. A change to resume eligibility, turn attribution, question
handling, queue admission, or cancellation intent should not require synchronized
edits in HTTP handlers, thread services, integration workers, and runtime drains.

The first delivery consolidates ordinary send/resume/queued admission, answers,
input cancellation, inbox-delivery retry, execution retry, cancellation intent,
and explicit end, including the runtime queue-drain producer. A second bounded
delivery consolidates ordinary completion/failure/pause projection and adapts
specialist callers to transaction-bound shared operations.
Execution-start/restart fencing is a prerequisite of both deliveries, including
initial `run_agent` jobs. Consolidating initial-run producer policy is outside
scope, but its runtime-start validation and minimum receipt wiring are not.
Exact request/native-conversation identity, pre-launch resumability checks and
tombstone-first cleanup are minimum 0a prerequisites. Private provider-state
isolation and native-context export bundles are optional separate hardening.
Both deliveries are required for the full scope of this plan; extracting only
the HTTP send handler is an intermediate milestone.

Keep specialist ownership of executor leases, provider processes, snapshots,
automation generations, review assessments, publication receipts, and physical
cleanup. Those are different state machines with real ownership/fencing needs.
This plan does not replace them with a universal session manager, split the
orchestrator wholesale or rewrite the frontend.

## Evidence and current owners

Use `docs/design/overall.md` as the system map. Relevant current implementations:

| Current owner | Behavior / maintenance problem |
| --- | --- |
| `internal/api/handlers/sessions.go:SendMessage:4033` | Session-level sends perform eligibility, claim, message/question/comment persistence, and continuation enqueue in the handler. Non-running admission is transactional. |
| `internal/services/thread/service.go:755` | Thread sends implement their own claims, question handling, inbox/backpressure, compensation, and enqueue. Comments at `:63`, `:86`, and `:1412` explicitly say behavior mirrors the session handler. |
| `internal/services/humaninput/service.go:114` and `db_repository.go:32` | Structured answers/cancellation already have a useful transactional owner, checkpoint checks, provider semantics, and post-commit job notification. Reuse these rules. |
| `internal/services/thread/actions.go:205` | Inbox retry resets delivery state, chooses live delivery versus continuation, and builds another continuation. Migrate this producer too. |
| `internal/worker/handlers.go:6279` and `:6645` | Slack continuation independently inserts/enqueues. Slack human-input independently answers, updates Slack, then enqueues instead of calling the answer service. |
| `internal/worker/handlers_linear_agent_prompted.go` | Linear continuation independently claims/resumes, appends, enqueues, and handles integration receipts. |
| `internal/api/handlers/session_review_comments.go:388` | Send-to-agent independently claims idle, writes a message, enqueues, and compensates on failure. |
| `internal/services/agent/orchestrator.go:6353` | Runtime drain evaluates resumability and creates continuation work. At `:6449` it explicitly leaves queued human-input answering to the worker. |
| `internal/worker/handlers.go:9610` and `:10043` | `answerQueuedHumanInputForContinue` answers the latest pending request at job start, outside admission, and carries the request ID only in memory. This is another answer owner to retire. |
| `internal/db/session_store.go:1196` and `internal/worker/handlers.go:9990` | Ordinary runtime start is not fenced against explicit end: `BeginRuntime` can set running without a generation/state predicate; the thread gate checks cancellation, not end. |
| `internal/services/agent/reaper.go:334`, `internal/services/github/pr.go:937`, and worker failure paths | Status writers outside admission can overwrite a successor. Inventory all status writes, not only send callers. |
| `internal/services/github/pr_health_service.go:2064` | PR repair composes revision context, repair reservation, message, and continuation within a domain transaction. Preserve that atomic composition. |
| `internal/services/agent/thread_runtime_delivery.go:344` | Runtime completion accounts for sibling execution when projecting parent status. This must survive extraction. |
| `internal/api/handlers/agent_capabilities.go:71` and `internal/services/agent/orchestrator.go:9141` | Capability-request and provider-input pauses write lifecycle state at different checkpoint-readiness stages. Classify both explicitly. |

Also inventory callers already using thread services: internal session-tab tools,
review loops, code-review workers, and push reconciliation. Their trusted origin,
changeset targeting, exact-turn identity, and post-success behavior must not be
lost behind a generic message API.

## Target ownership and dependencies

Introduce proposed package `internal/services/sessionlifecycle`. Its scope is
conversation transition/admission, not execution infrastructure. Structure it as
small use-case files and interfaces rather than a service locator containing all
worker dependencies.

| Responsibility | Authoritative owner after migration |
| --- | --- |
| Human/tool send, resume eligibility, queued admission, message/question/comment persistence, required dispatch intent | `sessionlifecycle` command implementation and transaction-bound kernel |
| Provider-specific answer validation/checkpoint interpretation | Human-input policy adapter, composed within the lifecycle command's transaction |
| Input denial/cancellation and explicit inbox retry | Lifecycle input/delivery commands, preserving deny-and-resume and explicit unknown-delivery replay semantics |
| Retry mode, cancellation intent, explicit end | Separate lifecycle commands with operation-specific eligibility |
| Authorization to start/restart a session runtime and consume its seed | Fenced lifecycle operation, composed by worker/runtime owners with their job/attempt identity |
| Authoritative durable generation/ownership fence and conditional mutation | Shared transaction-bound primitives in `internal/db`, used by lifecycle services and specialist DB transactions |
| Ordinary session/thread completion, failure, pause, and sibling-status projection | Fenced lifecycle transition operations invoked by runtime owners |
| Thread topology, archive/fork/revert | Thread service; use shared admission/transition operations where required, without taking over send policy |
| Job/executor attempts, leases, retry classification, dead letters | Existing worker/executor runtime |
| Process cancellation delivery, sandbox/provider effects, snapshot creation | Existing runtime/agent adapters |
| Automation reservation/result/generation/retirement/wakeup | Automation coordinator, retaining its transaction and attempt fences |
| Review assessment/PR ownership/publication | Code-review and publication coordinators, retaining their domain locks and receipts |
| Physical workspace destruction | Existing host-local cleanup after durable ownership checks |

The lifecycle package imports only models, database infrastructure, and its own
narrow ports. It must not import `agent`, `worker`, `thread`, `humaninput`,
`automations`, `codereview`, Slack, or GitHub service implementations. Those
packages implement ports or depend on narrow lifecycle interfaces. Composition
happens in the API router and server/worker constructors.

### Durable fence owner and DB specialist composition

The concurrency fence lives in the DB layer. The service owns ordinary command
policy, validation, and orchestration; `internal/db` owns the single durable
concurrency-fence implementation and its conditional mutation. Introduce proposed
`internal/db/session_lifecycle_fences.go` with operation-specific `...InTx`
primitives for admission/start/end/seed/delivery/projection. Names and exact
signatures are M0 outputs, but their layer and ownership are decided here.
They take an explicit `pgx.Tx` and org/resource/expected admission/attempt identity;
they cannot accept a pool as a substitute transaction. Check and mutation are one
fenced operation, not `Validate` followed by an unfenced `UpdateStatus`.

Dependencies are acyclic: ordinary commands in `sessionlifecycle` call these DB
primitives; specialist services and specialist methods already in `internal/db`
call the same primitives. DB never imports `sessionlifecycle` or another service.
Domain predicates and PR/automation locks stay with the specialist; shared durable
lifecycle predicates do not get copied into each domain SQL statement. Typed
specialist identity/proof variants replace any generic ownership-bypass boolean.

| Caller group | Transaction/lock owner | Shared-fence composition |
| --- | --- | --- |
| Lifecycle commands and worker/runtime adapters | Public command or existing runtime/domain unit of work | Service operations call the DB `...InTx` primitives in that transaction. |
| Recheck `Dispatch`, `Claim`, `Complete`, `Fail`, `FailTerminalJob`, `Cancel`, `ReconcileDrainedTerminalTurn` | Existing review transaction and PR/assessment/receipt lock protocol | Same-package DB primitive calls replace shared session/thread mutations; receipt, result, and lifecycle projection stay atomic. Audit nested callers such as `code_review_scheduling.go:353` and use explicit in-transaction variants, not nested wrappers. |
| Automation `EndInterruptedAttempt`, `CompleteExecutingPreflight`, `CompleteThreadTurnForAttempt`/`FailThreadTurnForAttempt`, `FailRetriesExhausted` | Existing domain owner, with explicit transaction wrapper where an atomic CTE currently owns the write | Split lock acquisition/domain mutation/shared projection inside one transaction. The old run/job lease locks remain held until projection commits. Never split the original atomic CTE into independent pool writes. |
| `CompleteFromMarker` | Existing caller transaction; `automations.TurnCompleter.Complete` remains the outer owner | Call DB primitives using the supplied transaction; generation/result/hold release and retirement/wake intent stay within the existing commit boundary. |
| PR feedback `PullRequestFeedbackStore.QueueContinuation` | Existing PR/batch transaction; add an explicit in-transaction variant | Compose `AdmitFeedbackTurnInTx` with typed `FeedbackAuthority` for org/PR/batch/current-head/job, preserving the feedback lane, message/inbox and `continue_pr_feedback:<batch>` job binding and post-commit notification atomically. Do not route this through ordinary unowned admission. |

These primitives never Begin, create savepoints, Commit, publish, notify, or invoke
providers. Transaction-owning wrappers are separate from `...InTx` variants.
Distinguish execution authority, immutable attempt-settlement authority, and
shared-projection eligibility. Invalid tenant/resource/attempt proof aborts the
outer operation. A verified obsolete attempt may append only its own immutable
transcript/result/cleanup receipt using its allocation-bound identity, even after
its mutable lease is reclaimed. Its stale epoch forbids shared status/result/diff
projection and any new execution, successor input acknowledgement, owner release
or publication. Admission/start/seed/ack result types cannot express a stale
projection success. Only terminal-settlement operations expose that disposition;
their exact-own drain receipt may emit an idempotent drain-reconciliation wake,
not an execution/publication effect. M0 defines the separate result types and
permitted primitive/caller matrix. Return
typed outcomes, changed records, and effect data; only the actual outer
transaction owner publishes after commit.
Late output is attributed to its allocation's original session/lane/turn and
marked historical-attempt evidence. Successor seed queries, summaries, live delivery,
`continuation_task_message_ids` and start-over context exclude it. An authorized
fresh explicit copy may quote evidence as non-consent input, never reuse an old
approval or silently attach late output to the successor's turn.
Retained owner-marker release remains a named specialist operation, not permission
to bypass shared projection fences. The 0a release introduces and wires this DB
authority; M4/5 cannot defer centralization of its predicates to later extraction.
`EndInterruptedAttempt` gets an explicit `...InTx` variant and a separate
transaction-owning wrapper wired with `TxStarter`; callers already in a transaction
use the variant. Do not type-assert a pool or silently fall back to nontransactional
CTE fragments when a transaction dependency is missing.

Public commands own Begin/Commit and return typed, committed outcomes.
Transaction-bound operations accept an explicit unit of work/`pgx.Tx`, never
Begin/Commit, and return effects for their caller to dispatch after commit. Do
not hide transactions in context or call a public command from an existing
domain transaction. Keep one-way dependencies; `automations/TurnStore.EndAttempt`
already depends on an agent store interface and cannot gain an opposing import.

Proposed commands include `SubmitSessionMessage`, `SubmitThreadMessage`,
`AnswerInput`, `CancelInputRequest`, `RetryInboxDelivery`, `RecordLegacyAnswer`,
`RetryFromCheckpoint`, `StartOver`, `RecoverPendingInput`,
`RequestSessionCancel`, `RequestThreadCancel`, and explicit session/thread end.
Names are proposals; the ownership and behavioral distinctions are the contract.
Do not replace them with a bag of flags and `SetStatus`.

Use separate consumer interfaces and constructor dependencies:

- Admission commands: message/queue policies plus an admission repository.
- Input commands: input policy and transaction-bound grant/input persistence.
- Control commands: retry/end/cancel policies plus control-intent persistence.
- Runtime-start operations: admission/generation and job/attempt validation, not scheduler or provider ownership.
- Projection operations: explicitly supplied transaction and runtime/attempt fence.

Each repository owns its unit-of-work adapter, exposing only the operations that
use case needs. A single concrete transaction implementation may back multiple
ports, but no all-stores constructor, optional setter collection, or universal
mutable `Service` is required. Share the small eligibility/attribution kernel;
compatibility facades call commands, rather than delegating to each other.

Inputs carry explicit org/resource IDs, actor/source provenance, existing client
message/ingress identity, and relevant execution fence. Actors can be humans,
external API clients, or internal tools; a nil human user ID is valid for some
callers. Ordinary callers cannot select a generic `system=true` ownership bypass.
Specialist transactions use separate typed operations with their durable
automation/review/repair/attempt identity verified under the existing owner locks.

## Behavior that must remain distinct

- Session-level running sends append a session-turn message without starting a
  continuation or automatically answering a question. Attachments/references-only
  requests remain supported. Session turn and dedupe attribution stay session-level.
- Thread-level sends retain thread turns, client-message deduplication, inbox
  state, limits, source attribution, and sibling concurrency. Mid-turn messages
  target the following turn, not the running one. Capacity-queued sends remain
  queued rather than being rejected or creating another execution owner.
- Every resume-producing answer or denial requires request-specific checkpoint
  readiness and an admitted consumer durably bound to the exact
  `human_input_request_id`. This includes immediate session/thread send answers,
  structured `AnswerInput`, `CancelInputRequest`, and queued free text. Status plus
  an older snapshot is insufficient. Preserve kind/choice/payload validation;
  waiting input remains unanswered under the readiness/recovery policy below.
- `CancelInputRequest` denies/cancels an input and admits the provider's denial
  continuation; it does not interrupt the process. `RetryInboxDelivery` preserves
  the caller's explicit permission/audit for replaying `unknown_delivery`; automatic
  recovery cannot manufacture that permission.
- Legacy `AnswerQuestion` only records an answer; it does not silently become a
  resume command. Review-comment send-to-agent retains its busy-session
  `sent=false` fallback instead of automatically queueing.
- Checkpoint retry and start-over remain different commands. Preserve checkpoint
  pending/expired behavior, retry-thread choice, assistant notes, and job type.
- Session end stays idle-only and does not publish a PR. Thread end has its own
  broader eligibility. Do not equate either with cancellation. For running thread
  end, choose drain-before-successor execution: retain the end response, permit
  new input to queue, but do not admit successor execution until the predecessor
  runtime/holder is proven drained. A durable end/admission-generation boundary
  must also exclude that successor input from the predecessor's live inbox poller;
  preventing only another execution from starting is insufficient. Every delivery
  claim checks this boundary, including compatibility-floor workers. Completion
  of the ended runtime may finalize its own records but cannot reopen the
  explicitly ended thread. This queueing/delivery boundary is an intentional
  safety improvement, not existing guaranteed behavior.
- Explicit session/thread end also fences accepted jobs that have not started,
  including delayed retries. Session end's idle eligibility and runtime-start
  authorization must serialize: if start wins, idle-only end fails; if end wins,
  the old job cannot reopen it. A running-thread end may let an already-authorized
  runtime drain, but cannot authorize its stale retry.
- Ordinary session/thread end rejects retained automation/review ownership or a
  current repair reservation by default, including a session-wide exclusive owner
  when ending one of its threads. Unknown ownership fails closed. A completed
  review or retired automation may still retain ownership; historical associations
  without a retained owner/reservation do not alone block end. Specialist end or
  retirement requires its typed domain operation and exact identity, never an
  implicit cancellation hidden inside ordinary end. This is a deliberate safety
  contract; the current session end has no owner guard and thread end checks only
  code-review ownership.
- Cancellation acceptance means durable intent exists, not that a process has
  exited. Preserve local/direct-worker/queued delivery and missing-worker recovery.
  Exact-turn requests cannot cancel a successor turn.
- External API, browser, internal-tool, Slack, and Linear authorization stays in
  their adapters. Org/resource identity, lifecycle ownership, and current-state
  checks are also enforced at durable command admission; adapters must not
  reintroduce transaction logic in handlers.

## Transaction, failure, and recovery contract

For new admitted work, the required session/thread claim, turn/message, pending
question/input change, comment resolution, inbox accounting, and required job or
durable waiting intent commit together. A queued item need not have an immediate
continuation job, but it must be discoverable by the bounded reconciler specified
below. Preserve dedupe scope, priorities, worker affinity, changeset lease
identity, source, and existing delivery receipts in the committed intent.

Enqueue must report a typed disposition: a new job, a verified existing consumer,
or pending durable reconciliation. `uuid.Nil, nil` from `enqueueOn` is a dedupe
no-op, not proof that the current input will be consumed. A predecessor can still
own the dedupe key after its final drain scan. Do not clear a pending marker or
leave a fresh running claim without a bound execution intent because enqueue
returned no error. Preserve the existing same-thread message-scoped drain key in
`orchestrator.go:6470` and `:6584`; do not collapse it into the predecessor's key.

"Verified existing consumer" means a durable binding to the exact input, org,
session/thread, admission generation, and dispatch/job identity; answers also bind
the exact request ID. Dedupe-key presence, matching thread, or a worker that may
read trailing messages is insufficient. Inventory session/thread/drain, push,
review-loop, repair, feedback-batch, and recheck key shapes. In the first release,
do not rebind any existing job payload to a new input. `BoundExistingConsumer`
means replay of the exact same already-recorded input/request/dispatch identity,
not permission to attach another message. A new input receives a dispatch-unique
job only when no nonterminal conflicting consumer exists for its scope: session-
level, same lane, or a whole-workspace holder. Recheck job/runtime/executor/holder
and drain receipts under the canonical locks. Otherwise retain durable pending
admission without a job; answers and grants remain uncommitted. A predecessor's
last drain scan or idle status does not prove it released its consumer. Independent
sibling lanes retain supported concurrency when their positive scope/holder proof
establishes compatibility: distinct lane and current physical generation, a
positively usable workspace, and no exclusive wholeworkspace holder/recovery
reservation. Ordinary shared sibling activity is not an exclusive holder. The
same-session trust model and pre-launch verification are specified in the M0
contract; this refactor does not require provider-private storage.
Never displace another message/request; preserve the one-request-ID-per-resume
limit. `attempts = 0` is not a never-claimed marker: the three
`RetryWithoutConsumingAttempt*` methods decrement it and clear ownership after an
actual runtime attempt. A later payload-rebinding optimization would require
separately reviewed monotonic claim-history proof on every claim/handoff path,
including a trustworthy birth/version distinction for legacy jobs. That
optimization is not part of this release.

Live delivery, queued drain, reconciliation, and specialized producers must
atomically acquire compatible per-input consumption ownership. A job bundling
trailing messages must claim each included input under the same generation fence.
Update competing-consumer probes such as `code_review_recheck_dispatch.go:283`,
which currently checks only the ordinary thread dedupe key, before enabling the
reconciler. Preserve specialist receipts and locks; expose a narrow ownership
predicate instead of letting the ordinary reconciler claim review work.

One transaction-bound answer-admission operation owns the mutation/binding rule
for every resume-producing answer or denial. Immediate session/thread sends,
`AnswerInput`, `CancelInputRequest`, and queued admission compose that operation;
no path may commit the answer/grant/resume claim on a dedupe no-op alone. The
answer, exact request/message identity, and verified dispatch binding commit in
one transaction. A newly created bound job, exact replay of this same already-bound
input/request on its existing job, or exact durable dispatch record consumed
under its fence is valid. No existing payload gains a new input. Otherwise roll back
answer/grant/claim and leave the request pending. An async adapter may separately
accept a durable waiting-answer receipt, but must report waiting, not answered or
delivered. `RecordLegacyAnswer` remains answer-only and does not imply execution.

Queued admission calls this same operation; retire the worker's independent
"answer latest pending" mutation in the same cutover. Workers validate/use the
persisted binding on every retry, never reselect a request. Define normalization of legacy jobs
with `queued_message_id` but no request binding before deployment. Ambiguous
legacy state remains durably pending for reconciliation, not a silent fallback
to whichever request is latest. An answered request ID cannot exist only in
in-memory `ContinueSessionOptions`.

Legacy normalization is bounded and lazy per session. An unresolved
`historical_uncertainty` descriptor gates ordinary automatic execution in that
session, not only replay of one old message. Fresh input may be durably held,
but cannot bypass the descriptor or seed unproved historical output. The existing
proposed `ReviewHistoricalInput` / `ReplaceHistoricalIntent` actions provide a
non-destructive way forward: exact evidence resolves the descriptor, or the user
explicitly acknowledges its digest/range and submits fresh replacement intent.
ReviewHistoricalInput also permits acknowledging an already-drained tail as
historical/non-runnable without sending a replacement message. This closes no
unknown grant/delivery or live-attempt question: those still need exact evidence.
Workspace and transcript are preserved. Replacement still needs ordinary current
authorization, consumer/drain and grant/delivery certainty; it never fabricates
old consent. `NORMALIZATION_PENDING` and `HISTORICAL_INPUT_REVIEW_REQUIRED` have
explicit HTTP/tool/integration outcomes and versioned notices in the companion.

### Readiness proof and legacy pending requests

The authoritative readiness rules, data fields, typed outcomes and verification
protocol are in [the M0 contract, section 7](session-lifecycle-m0-contract.md#7-request-readiness-and-reachable-recovery).
Its sections 2, 3 and 9 define representation, transaction seams and activation.
Keep those details there; this section records the migration intent, not a second
policy or query.

Tabs in one session share a trusted workspace and provider HOME. This refactor
does not add a hostile-sibling security boundary. Tenant, actor, domain, epoch and
consumer authorization remain mandatory. Provider-private bundles/isolation are
optional later hardening, not a prerequisite for M1 or a property claimed here.

Preserve the immutable request/lane/provider/final-pause anchor and sticky,
all-request workspace continuity certificates. Authorized sibling metadata
progress preserves continuity by default; named context-affecting changes receive
explicit pending/unavailable outcomes. One request's bad certificate cannot veto
another lane's authorized workspace write. Snapshot cleanup revokes applicable
availability before physical deletion, including archive and PR-close callers.

Admission readiness is not a guarantee that native context will still be usable
at launch. The start path verifies the exact bound conversation on its actual
live/restored target before provider launch and rechecks its fenced preparation
under the canonical DB locks. A failed check preserves a committed answer,
grant and dispatch as accepted-but-undelivered; it cannot mark the answer pending,
regrant, acknowledge delivery or fall back to reconstructed/fresh context.
Unknown delivery retains its distinct no-automatic-replay policy.
An already answered request, or exact committed denial with its bound dispatch,
uses the companion's start-control receipt for an undelivered start, not
pending-only RecoverPendingInput. Its detail exposes
same-native retry after repair or explicit abandonment of that undelivered
intent, preserving committed grants/audit and excluding abandoned consent from
automatic successor context. Safe abandonment also releases only its own logical
admission claim, without clearing another lane's work or uncertain physical
preparation. Neither action silently starts a fresh conversation.
Unavailable/unsupported starts park durably. User controls use the parked
receipt/version after execution-preparation and worker leases expire; they do
not require a live worker or a still-valid launch token. Companion section 7
owns the park/retry/abandon and pre-launch crash protocol.
Current end, successor, recovery and archive/delete authority must also close an
exact stale known-unlaunched intent through the same canonical owner. A stale
retry rejection alone cannot leave its old job as a permanent lane blocker.
Logical claim release is distinct from physical drain and uncertain delivery.
This does not broaden idle-only session end: a non-idle session-end request
rejects without advancing epochs, leaving the reachable abandon action intact.
Thread end retains its broader existing eligibility. Each successful transition
composes supersession only after its own authorization and eligibility checks.

0a must prove the contract through real providers, stores and concurrent callers.
Legacy normalization, proof publication, atomic classification/enforcement/notice,
writer-floor and rollback gates remain prerequisites of answer-enabled cutover.
Use the companion's single executable policy and selected-start probe rather than
copying their predicates into adapters.

### Reachable recovery from a paused request

The authoritative pending-request recovery, whole-workspace acknowledgement,
mode eligibility, reservation and uncertain-operation resolver are in
[companion section 7](session-lifecycle-m0-contract.md#7-request-readiness-and-reachable-recovery).
`RecoverPendingInput` is distinct from failed-only `RetrySession` and from the
accepted-undelivered start controls above. Recovery must be reachable while the
ordinary session is paused, with the exact request/receipt and fresh consent;
it cannot infer a thread-local reset from a thread-scoped request. No proposed
recovery route or provider re-ask capability exists merely because it is planned.
Deliver the API/tool action, API client/TanStack Query integration and focused
notice/button tests before enabling recovery-required notices or enforcement.

The proposed `PendingAdmissionReconciler` belongs to the lifecycle owner. Its
bounded startup/periodic scans must outlive incoming requests, Redis wakes and
integration membership. Companion section 2 owns discovery, consumer arbitration,
terminal-job reconciliation and the exact per-writer inbox/dispatch matrix;
section 7 owns the accepted-undelivered preparation/control exceptions. Keep
those predicates there rather than duplicate them here. Waiting ingress, bound
deferred work and successor intent remain discoverable; actual delivery
uncertainty and unproved predecessor drain never become automatic replay.
Existing `ownerloss` recovery of jobs/executors does not cover missing-input
discovery. In-memory dead-letter callbacks are not its crash-recovery owner.

The current thread path commits message/inbox before normal continuation enqueue.
Moving claims and enqueue into one admission transaction is a deliberate failure-
atomicity improvement, not a claim that today's paths are already equivalent.
Characterize successful behavior first, add failure-injection regressions, and
remove unconditional compensating status resets only after the atomic path owns
all coupled writes. Do not restore an old status over a successor or delete
durable accepted messages to simulate rollback after commit.

Acquire owner guards under the same transaction as admission, not only as a
preflight check. Preserve SQL compare-and-set, lease, turn, runtime-token,
generation, and ownership predicates. Share business decisions without weakening
database fencing.

### Execution start, end, and stale cleanup

Every session-runtime start/restart/recovery must validate the accepted admission
identity/generation and current job/attempt authority against the same durable
boundary as end. Include initial `run_agent`, ordinary `continue_session`, direct
orchestrator calls, and executor handoff. Check before dispatch and runtime-related
effects, and revalidate at handoff/runtime allocation. Phase-1 target preparation
does not authorize eval/Slack running updates: dispatch those only after final
StartPermit commit. Existing early calls in `worker/handlers.go:8470`-`:8472`
must move behind that boundary; prewarm may precede it only within an explicitly
fenced preparation/workspace reservation. `BeginRuntime`, `CreateStarting`, and
`beginRuntimeControl`'s fallback must not unconditionally reopen ended work.
The active-runtime uniqueness index prevents overlap, not stale-generation start.
`AuthorizeRuntimeStartInTx` also returns typed `StartDeferred` when the bound
dispatch is valid but a conflicting consumer/drain/capacity reservation remains.
It grants no provider/start permission. Preserve the exact binding and use a
non-consuming retry plus independent durable reconciliation, with no failed/idle
compensation, new job, reselection, dead-letter or ingress-attempt-budget loss.
Use a lease-fenced admission-deferral seam that preserves the original payload,
dispatch and request; existing retry helpers' checkpoint-recovery rewriting is
not the semantics of a provider-free StartDeferred result.
A readiness/holder change after admission cannot strand an answered request's
only consumer. Test predecessor final-scan/new-send and answer/start races.
The companion also distinguishes final permission from attempted provider
execution. Every Execute path consumes the required durable launch marker.
Fenced absence of that marker permits known-unlaunched reconciliation only when
ALL same-binding receipts also lack unresolved provider-entry/delivery evidence.
Legacy or normalized uncertainty without a marker remains uncertain; a committed
marker also preserves uncertainty if a crash happened before the actual call.
Do not duplicate that protocol or its predicates in worker adapters.
The current transition also composes exact parked-intent supersession when end,
successor admission, accepted recovery or archive/delete fences that intent.
Preserve committed request/grant/audit and conditional sibling-aware claim release;
do not treat closing a never-launched job as stopping a process or proving drain.

Apply this identity to seed selection/claim/ack, live delivery, expired-runtime
replacement, lease-reclaim inbox resets, and start/failure/retry/dead-letter cleanup.
An obsolete attempt may settle its own receipt but cannot project running/idle/
failed over end or a successor, or acknowledge successor input. A live lease alone
does not establish permission to consume that input. End versus start must have
one transactional winner; preserve input and delivery uncertainty on rejection.

Milestone 0 selects the representation and migration of legacy jobs lacking a
binding. Missing identity must never mean "use the current generation". Establish
a provable mapping under the same locks or naturally drain/reconcile old work
before relying on the new fence. Unknown ownership is held with an actionable
durable reconciliation state, not dropped or blindly executed. Compatibility
consumers must understand the fence before newer producers rely on it; use a
proven version/job-type targeting contract if old consumers could claim new work.

End's owner check re-reads durable ownership under the same lock protocol as
claim/release/dispatch. Preliminary reads may locate locks but cannot authorize
the write. Include retained `code_review_owner_pr_id`,
`automation_owner_generation_id`/ownership-release-pending, and exact active
repair reservations. A lack of running jobs is not release proof. Rejected
ordinary end changes no generation, domain receipt, cancellation intent, or
wakeup/publication state. A concurrent claim or release and ordinary end have
one transactional winner. Specialist operations retain their own domain locks,
typed identity, retries, and drain checks; they compose the canonical fence
instead of using a generic bypass. Obsolete specialist runtimes still retain
their runtime cleanup/delivery fences after a reservation stops being current.

### Locking and transaction-bound persistence

Before combining existing transactions, record the lock graph, including
implicit FK locks. Thread sends currently claim a thread before its parent;
human-input resume claims the session before the thread; thread admission can
lock visible siblings. The companion's selected order is cutover interlock,
specialist domain and existing job/executor rows, then parent session,
stable-ordered affected threads and dependent runtime/input/inbox/message/new-job
rows. It is a concrete design, not a proven deadlock-free implementation.
Preserve PR advisory lock before session/FK writes and automation attempt locks
before checkpoint/hold mutations. A newly discovered blocking higher-rank row
still requires ordered reacquisition. The cutover shared try-lock may be acquired
late, including inside the any-session job claimer or bulk writer: it never waits
and aborts only on a real exclusive-lock conflict. Rejecting every unknown scope
would livelock `ClaimNextRunnable`. Fresh post-acquisition guard lookup requires
READ COMMITTED; trigger snapshot and multi-scope behavior remain 0a proof gates.
Milestone 0 must resolve any conflicting order before transactional extraction.
The compatibility release below must land and finish rolling out before commands
use a new lock order. The baseline already has inversions: session send locks the
session before pending input (`sessions.go:4254`, `:4295`), whereas structured
answer locks input before session (`humaninput/db_repository.go:88`, `:108`).
Capability grants also lock session then automation run, competing with automation
dispatch's run-first order. Characterize these existing pairs; do not describe
them as introduced by the extraction or assume a deadlock-free baseline.

Explicit transaction-bound persistence is required; rebinding a store to
`pgx.Tx` is insufficient. Existing `SessionStore.WithTx` can retain publishers,
`UpdateStatus` can publish from a write, and result/turn/activity-phase helpers can
Begin/Commit internally. Introduce narrow `...InTx` operations for required
status/result/turn/diff/activity-phase and thread writes: no Begin, Commit,
savepoint creation, or publication. Return changed records/effect data to the
outer transaction owner. Preserve the combined result/activity-phase atomicity.
Tests must enable real publishers on the original stores so absent wiring cannot
hide premature notifications.

After commit, dispatch Redis/SSE wakeups, local signals, integration updates, and
audit/enrichment through explicit effect adapters. A failed optional notification
does not reject already accepted work or cause another execution; jobs/pending
inbox state remain the recovery source. Effects requiring guaranteed retry use
existing durable integration/jobs receipts, not an in-memory callback alone.
The transaction owner collects effects; nested primitives never publish them.

Slack answer updates must follow durable answer/resume admission. Retrying a
failed Slack update must recognize the already accepted command and retry only
the update. Preserve existing thread client-message and provider ingress IDs;
do not deduplicate arbitrary repeated messages by text. Milestone 0 maps each
integration receipt to a committed command result. If an existing receipt cannot
represent this safely, specify the smallest additive receipt/outbox change in
that slice; do not invent a parallel general execution ledger.

DB-backed capability approvals must use the explicit caller transaction. The
current `AppendApprovedSessionGrant` begins another transaction and locks the
same session row; invoking it after a new parent-first lock would self-block.
Extract its persistence, including any automation-run capability snapshot update,
into a transaction-bound grant operation. Keep catalog validation in the policy
adapter and preserve request-keyed idempotency/grant scope. Do not leave the old
transaction-owning port as a production fallback. No external provider/Docker/
GitHub operation runs while DB locks are held.

Preserve `unknown_delivery` for messages whose provider delivery cannot be
established. A stale worker or lost response must not cause a blind resend,
publication replay, or clearing of another generation's ownership. A commit error
with unknown outcome is not proof that rollback occurred; reconcile using the
existing command identity where available, or preserve uncertainty without retrying
the write automatically.

## Delivery milestones

### 0. Characterize commands and establish the migration contract

Produce an executable caller/writer inventory and a behavior matrix covering
status, session/thread turns, ownership, checkpoint state, input kinds, queue
limits, dedupe, worker targeting, errors, and response/audit behavior. Reuse the
existing tests instead of replacing them with tests of the proposed design.
Record which differences are deliberate and which failure windows are being fixed.

Enumerate every session/thread status writer, including raw SQL and indirect
store calls. Classify each as lifecycle-owned, a named fenced specialist, or an
unfenced writer requiring conversion to conditional mutation/revalidation under
the owner lock. Include reaper list-then-write paths (`agent/reaper.go:334`,
`:368`, `:446`, `:523`), PR-created projections (`github/pr.go:937`, `:2835`,
`pr_health_service.go:1624`), code-review writers (`code_review_handler.go:845`,
`:882`, `:3219`, `codereview/service.go:1307`, `:1322`), worker start/failure/
cancellation cleanup, drain-admission revert, session end, and checkpoint-retry
revert. Include phase-2 snapshot deletion and UpdateSandboxState, whose eligibility
effect does not require a status write. Record each predicate, lock, and stale-write result. Plain `UpdateStatus`
is not a fence. Identify the minimum competing-writer fixes required before
new admission/start semantics; do not defer those to Milestone 5.

Document the lock graph and choose the order after checking all competing writers.
Specify command result/effect ownership and integration receipt mapping. Verify
where the production constructors enable transaction/inbox/ownership dependencies;
optional nil dependencies in old tests must not create a production bypass.
List current result/turn/activity-phase store methods that own transactions or
publish, and design the explicit no-effect transaction seam for each migrated use.
Record the pending-admission scan/index, startup/periodic owner, dispatch
dispositions, and live-delivery versus queue-drain entry-state handshake.
Define the exact per-input dispatch binding, all competing dedupe-key families,
specialist conflict probes, and legacy in-flight job normalization. Inventory
`run_agent`/`continue_session` start, restart, executor handoff, failure hooks,
and seed/inbox reset paths, not just producers. Specify request-specific ready
checkpoint/turn/generation and producer-drain proof before Milestone 2/3 answering.
An early capability `awaiting_input` write plus an earlier snapshot is not proof.
Specify and exercise the three-way legacy-readiness policy above, including paused
pre-proof requests, in-rollout old producers, lost checkpoint evidence, waiting
answers, deadlines, and fresh-consent recovery. Specify `RecoverPendingInput`'s
request/control states, authorization, provider modes, receipt identities, ordinary-
send responses, and API/tool/notice action. It must work from `awaiting_input`;
existing failed-only retry eligibility is not the recovery implementation.
Enforcement is gated on complete compatible consumers and atomic per-request
classify/enforce commits, not visible classification followed by later enforcement.
The same exact answer/binding rule applies to immediate session/thread answers,
structured answers, denial continuations, and queued input; M1 cannot defer it.
Include accepted legacy session messages that have no inbox row: specify how
their pending dispatch is durably discoverable without treating every historical
message as unfinished work, with an explicit historical cutoff/legacy classification.
Specify how input accepted after explicit end is
distinguished from the ended generation; status alone is not that distinction.

Map every lifecycle rejection per HTTP/tool/integration adapter. For asynchronous
answers, specify checkpoint/capacity waiting, retry-with-backoff, and terminal
failure plus a user-visible notice. Accepted waiting input needs a durable receipt
and readiness/capacity-triggered reconciliation independent of the original job's
attempt budget; a bounded retry ending in an unnoticed dead letter is not recovery.
Define expiration/escalation for unresolved intent. End/generation loss is not
permission to resume a successor. Required grant dependencies fail construction
closed; missing approvers cannot silently accept capability approvals.

For explicit end, specify completion's runtime/lease/state fence and the durable
drain-before-successor check. A lease plus turn number is not automatically enough:
end and resume currently can preserve the same turn. Old completion must not
overwrite explicit end or a later admission, and new execution cannot start merely
because the old thread status became completed. The still-running predecessor's
poller must not claim successor input either: today's `ClaimDeliverableAfter`
does not enforce an end/admission-generation boundary. If existing durable
runtime/holder identities cannot prove these boundaries, specify the minimum
additive fence and its compatibility migration before implementing the command.
Include pending/retrying and known-unlaunched parked jobs in both session and
thread end; name their exact terminalization/claim-release operation. Classify all
actual job-status predicates, not just runnable claims: ownership, drain, recovery,
cleanup, scheduling and resource accounting have different parked-work semantics.
Make new or changed unclassified status sites fail the M0 inventory check; classify
unrelated table statuses separately rather than widening them. Specify execution-
start versus end linearization, legacy payload handling, and the seed selection/
attach/ack race, including `CreateStarting` replacement and reaper lease reclaim.
Use the reject-by-default specialist-end contract above in the behavior matrix.
Map session-wide versus thread-local ownership and exact claim/release locks;
completed review or retired automation with retained markers remains owned.
Only historical associations after proven release are insufficient to reject.
Ordinary end never implicitly composes a domain cancellation; a specialized end
requires an explicit typed domain call and its existing authorization.
Inventory retained review ownership (`db/code_review_recheck_dispatch.go:32`),
automation release-pending (`db/automation_lifecycle.go:290`), and repair
reservation scope/current-head/job identity (`db/pull_request_health.go:408`,
`:539`, `:578`). An unfiltered historical repair lookup is not a current owner
predicate; an obsolete but still-live runtime retains its separate drain fence.

Design the function-level call graph for the chosen DB-canonical fence. Map every
caller above to an explicit transaction/lock owner and DB `...InTx` mutation;
service commands own ordinary policy, while DB owns durable shared predicates
from their first release. Include DB-only scheduling callers, recheck completion/
terminal-failure siblings, and automation preflight/thread-turn/retry-exhaustion
paths, not just Dispatch/Claim. M0 specifies signatures and locks, not a choice
between upward DB imports and copied SQL. No new universal service is required.
Its start result is a sealed outcome with validating constructors, not three
optional pointers plus an exactly-one comment. M0 pins valid preparation,
permission and deferral variants and rejects nil/zero invalid outcomes; 0a owns
the production type and all caller wiring.

Exit: first-slice contract tests run against existing entry points; writer/start
inventory, universal answer binding, new/legacy checkpoint-proof disposition,
specialist-end matrix, canonical fence call graph, adapter outcomes, lock graph
and baseline inversions, legacy normalization, and mixed-version receipt/fence
designs are concrete. No unresolved transaction-composition question is hidden
behind "extract helper". The chosen representation and queries still need real
database and rollout proof; these textual policies do not constitute that proof.

### 0a. Deploy a lock/fence compatibility release

Implementation is split into reviewable slices; none individually establishes a
deployed compatibility floor. The first slice adds
`internal/db/session_lifecycle_transaction.go`: a pool-owned top-level runner and
an explicit caller-owned transaction configurator. Both use READ COMMITTED and
transaction-local protocol settings. The runner gives acquisition, setup, work,
retries and commit one cooperative two-second context budget; independent
rollback cleanup may add up to 250ms afterward. A callback that ignores its
context cannot be forcibly bounded by this helper. It retries only classified
contention after confirmed rollback; commit uncertainty is never replayed. A
live context is not a minimum commit-time reserve, so a late deadline may still
leave commit uncertain; callers must resolve their command receipt, not resubmit.
Any commit-reserve policy remains a measured pre-activation decision, not an
arbitrary new cutoff in this passive slice. This is mechanical plumbing, not
tenant/domain/epoch authority or evidence that a binary is floor-compatible.
No existing writer uses it yet. Subsequent slices still need passive schema,
composable persistence/grants, canonical durable mutations and every caller
conversion below before any activation.

The next passive slice splits `AgentCapabilityPolicyStore.AppendApprovedSessionGrant`
into its existing transaction-owning wrapper and
`AppendApprovedSessionGrantInTx(ctx, orgID, tx, sessionID, item)`. The helper uses
only the caller's concrete `pgx.Tx`; it never settles or retries the unit. The
wrapper retains its existing errors, duplicate behavior, SQL and
session-before-automation lock order. This lower-level persistence seam is not
the canonical `AppendApprovedGrantInTx` authority operation. No human-input
caller is rewired, and consent, exact request/readiness, epochs and canonical
domain prelocking remain unimplemented gates. Existing snapshot representation
is sufficient for this slice; no speculative schema is added. The syntax guard
explicitly inventories the extracted root and its direct callers so extraction
cannot silently remove its review coverage.

The unchanged joined grant query has a pre-existing concurrent lost-update
hazard: its metadata snapshot is taken before waiting on `FOR UPDATE OF s`.
Waiting for that lock does not refresh the joined metadata snapshot, whether the
other writer only locked or also updated the session row. Two unprelocked grants
can both succeed while the last session/automation snapshot drops an earlier
grant, or a duplicate is missed. The legacy wrapper deliberately preserves that
SQL in this candidate.
The canonical operation must lock the parent in an earlier statement or reread
metadata after locking; wrapping this seam in the runner alone is insufficient.
Full-schema concurrent distinct-grant proof of both retained grants is mandatory
before converting callers, including coexistence with any unconverted legacy
wrapper. Existing lock-order and consent/readiness gates still apply; the earlier
lock is not an independent authorization check.

Before activating larger transactions, update competing old entry points and
store primitives to the chosen compatible lock order. Include structured answers,
grant persistence, ordinary sends/drains, and sibling claim/archive CTEs; explicitly
order multi-row locks. Audit implicit FK locks and retained specialist transactions.
Introduce the authoritative DB `...InTx` fence/mutation primitives and wire all
inventoried floor callers to them in this release. Add narrow lifecycle service
operations for ordinary command policy; DB specialists compose the same DB
primitives directly in their own transactions. Implement the explicit wrapper/
in-transaction split for atomic-CTE and nested scheduling callers. Never accept
pool writes, copied predicates, reverse service imports, or nested commits as a
compatibility shortcut. Boundary/call-through checks cover `internal/db` as well
as handlers/workers from this first release; later extraction does not create the
first authoritative fence.
Implement and wire `RecoverPendingInput`, its compatible workers, API/tools, and
notice action in 0a before any recovery-required classification becomes active.
The action is not deferred to M4. A notice/action contract test starting in paused
state is an activation gate. M4 later reuses this command while migrating the
remaining failed-session retry/start-over and control orchestration.
Implement the companion's exact-conversation VerifyResumable start preparation,
final authorization/launch marker and durable parked-control outcome in this
readiness prerequisite. Retry/abandon remain reachable after transient execution
leases expire. Guarded expiry and fresh per-attempt publication keys cover every
snapshot producer, including PR/branch pending uploads and their async promotion/
failure-clear callbacks, as well as every cleanup caller. The same release owns
bounded discovery and exact-key cleanup of superseded and orphan uploads, with
durable handoff before Save, reference/writer checks and a tombstone barrier before
deletion. It cannot replace one mutable key with an unbounded stream of abandoned
objects. The companion owns that narrow storage contract and its failure rules.
Shared HOME
remains inside the documented trusted-session model; no private-context export
or provider-isolation program is required for this release.

Prove both mixed-version transitions: pre-floor writers alongside compatibility
writers, then compatibility writers alongside the new commands. The compatibility
release itself changes ordering and is not safe merely because its final order
matches the new kernel. Milestone 0 must select an explicit coexistence protocol
before 0a begins. A candidate is nonblocking acquisition of newly ordered
conflicting locks, with confirmed whole-transaction rollback and bounded retry
before any external effect. `NOWAIT` cannot bound implicit FK waits: require
`SET LOCAL lock_timeout` with a documented bound for those waits as well. Retry
the whole transaction with bounded attempts/backoff only after confirmed abort
for explicitly classified `40P01`/`55P03` conflicts; revalidate state each time.
Never retry unknown commit, arbitrary errors, or partially published effects.
When attempts exhaust, unaccepted synchronous commands return a typed contention
outcome without answer/grant/claim changes; adapters map it to a retryable response.
Accepted async intent stays pending in its durable receipt for later reconciliation
until its recorded deadline, then receives the defined visible recovery/terminal
outcome. Exhaustion never becomes false acceptance, silent loss, or status reset.
Account for every competing row/FK lock, not just the first parent. If this cannot
be proved, specify a staged compatible protocol or a separately authorized intake barrier with
natural drain; do not assume a normal rolling deploy is safe.

Test both transitions on separate connections. Reproduce the pre-floor answer
holding its input lock while waiting for a session, and the compatibility writer
holding that session while trying the input; prove the chosen protocol releases
the conflict and commands settle without duplicate effects. Include sibling
claim/archive, automation-run/grant, and implicit-FK competitors. Record existing
pre-floor lock inversions and observed error outcomes separately from regressions.
Require no new deadlock pairs versus that baseline, bounded progress for floor/
new writers, and no effects from aborted attempts. A pre-floor command may return
its characterized abort response; that is not evidence the new protocol is safe.
Known baseline cycles cannot excuse starvation or an unresolved floor/new lock graph.

The floor also installs the execution-start/end, cleanup, seed claim/ack, and
live-delivery boundary, plus the minimum conditional competing-status writes.
Those use the chosen DB-canonical mutations, not additional predicate
implementations. Deploy proof publication and every compatible answer/recovery
consumer first. Shadow inventory may run earlier but is inert. Once no pre-floor
producer, consumer, or in-flight answer call remains, activate classification and
enforcement together per request under its locks, with notice outbox intent in
that commit. New requests adopt the active version at creation. Do not send a
recovery notice before this per-request boundary; notification delivery rechecks
its version/current state. Readiness enforcement is inseparable from effective
classification, not a later global step after visible classification.
Include initial starts and legacy retry payloads; job claim is not authorization
to start. Bind/prove legacy identity or naturally drain/reconcile it without
assigning today's generation. Do not enable new queued-answer bindings while old
workers still answer "latest pending", or new end semantics while an older starter/
poller/cleanup path can bypass them. Prove job version/target routing if needed.
Finish deploying the floor to all database writers, runtime starters, and delivery
claimants, including standalone session executors and long-lived workers. Existing work
drains naturally; do not force-stop it to activate the refactor. If old writers
remain, keep the old compatible transaction boundaries active until the floor is
established. Record the supported rollback floor; do not roll a new kernel back
into incompatible pre-floor lock order or fence interpretation.
The proposed database minimum-writer guard covers every active-record authority
surface, not just session/thread status: owner and repair reservations, workspace/
changeset/materialization, preview and holder mutations can change eligibility
without writing a session row. The companion maps those tables to tenant/session
cutovers, including OLD and NEW identities for update/delete. Missing provenance
for an active association fails closed; version-0 coexistence before activation
remains unchanged. This guard supplements build/transaction census and consumer
routing; it is not a replacement for tenant, attempt or domain authorization.
Every protected write uses an explicit READ COMMITTED transaction that sets
`SET LOCAL lifecycle143.writer_version='1'` before mutation, including queue
claimers, heartbeats, maintenance/operator scripts and old compatibility wrappers.
Protected autocommit pool writes are not a fallback. Connection startup settings
alone are not safe through transaction-mode pooling. On a pooled connection with
no startup custom GUC, `current_setting(..., true)` is NULL before first use and
can be an empty string after a `SET LOCAL` transaction ends. Future guards must
treat both NULL and empty as unset/version zero, not cast empty text to a valid
writer version or infer authority from having observed a marker earlier.
Activation uses bounded
try-lock/backoff and a visible deferred outcome under sustained shared holders;
it cannot busy-spin, force-stop work or claim starvation-free activation without
the 0a mixed-writer/throughput evidence specified in the companion.
After ACTIVATION_CONTENDED, the lifecycle activation controller owns the stopped
attempt and its visible receipt. An explicitly authorized operator retry of the
same org/session and expected cutover version resets only that attempt; normal
queue work does not silently restart activation. Measure SET LOCAL's extra
protocol/round-trip cost on hot claims and heartbeat/lease writers, including
transaction-mode pooling, in the 0a latency/throughput gate. Non-lifecycle jobs
are positively excluded from lifecycle-job triggers by typed job kind and absent
lifecycle association; malformed versioned/session bindings fail closed.
Before activation, unbound jobs from pre-floor producers retain characterized
legacy semantics; do not deploy a consumer that silently holds all legacy work.
The new hold-unknown contract activates only after no pre-floor producer/consumer
remains and old job dispositions are established. Legacy request classifications
become effective only in their atomic classify-and-enforce commit; any preceding
inventory is shadow data. Completed M1 waits for that backlog activation.
Require activation/rollback tests for both sides of that boundary.
Activation and M1 can proceed by an explicitly enumerated session/provider
cohort. Every request in that cohort must meet the active classification rule;
the compatible version-0 tail remains visible as activation-deferred and retains
its old semantics. This is partial cohort coverage, not repository-wide safety
or full-plan completion. Final acceptance requires no unbounded active version-0
tail. Before activation, every provider/pinned-build/ResumeIdentity variant in the
cohort needs an implemented, tested exact resumability check. Until then those
sessions retain characterized version-0 semantics with a visible deferred reason;
an active session cannot downgrade or switch to an unsupported variant. Missing
private-isolation capability is not this gate. Unexpected unsupported/context-loss
results after activation use durable undelivered control, never silent history
reconstruction. Companion sections 7 and 9 define capability and switch checks.
The same tested-tuple gate applies before routing an upgraded sandbox image/CLI
build to an active cohort, not only before first session activation. Unsupported
candidate builds leave the compatible serving build in place with a visible
rollout-deferred reason; they must not park every bound answer after deployment.

Exit: writer/build inventory establishes the floor, pre-floor/floor and floor/new
concurrency tests pass, and required transaction-bound grant/store operations are
available. Legacy in-flight jobs have a proven disposition and every relevant
consumer/failure hook honors the new fence. This is a prerequisite for any slice
that changes lock overlap or relies on these semantics, even if that moves part
of a later milestone's storage work forward.

### 1. Extract legacy session send as the first vertical slice

Completed, answer-enabled M1 follows 0a: proof writers, the complete compatible
floor, and per-request legacy classification/enforcement activation are mandatory
prerequisites. There is no "already safe" or lock-equivalence exception in this
baseline, whose session send lacks request-specific readiness checks.
Only passive extraction retaining existing answer/dedupe behavior may precede
0a; it is a preparatory substep, not M1 completion and not a new safety guarantee.
Do not activate new end semantics early. Existing lock inversions remain
characterized rather than declared fixed.

Add admission commands to the lifecycle service package over the DB primitives
introduced in 0a, with a small transaction adapter. Move
the `SessionHandler.SendMessage` decision/transaction into `SubmitSessionMessage`.
Preserve running fast-path behavior, comments, changesets, actor attribution,
question compatibility, session dedupe, and worker target. The handler retains
HTTP parsing/auth/error mapping and response rendering only.
Its immediate free-text answer and required grant/claim use the same canonical
answer-admission operation as M2/3. Enqueue dedupe without an exact consumer binding
rolls back the answer; a separately accepted waiting receipt leaves it unanswered.
Preserve legacy question identity/turn binding where no provider input exists;
never invent a provider request ID or change legacy answer-only semantics.

Return committed outcomes and run audit/Linear linking once after commit. Keep
tests through the old HTTP surface as well as service tests. Remove its old
claim/enqueue choreography in the same commit; a forwarding wrapper with the old
decision tree still present is not completion.

Exit: one ordinary session command is service-owned end to end, with rollback,
commit uncertainty, and post-commit notification tests. No new route or response
envelope; corrected dedupe/readiness outcomes are explicit in the behavior matrix.
Immediate-answer dedupe collision, crash/retry, and legacy-checkpoint outcomes
are required M1 regressions, not tests postponed to queued-answer migration.

### 2. Consolidate thread admission and runtime queue drain

Implement `SubmitThreadMessage` with the existing explicit thread modes. Move
thread/session claims and coupled persistence/enqueue into the documented unit
of work; keep queued admission durable when no slot is available. Preserve the
normal/running/capacity paths and all existing inbox semantics.

Convert `thread.Service.SendMessage` into a compatibility facade over the new
command; keep thread topology/read operations in the thread service. Internal
session-tab tools, review loops, and existing system callers therefore use the
same command without reimplementing it.

In this same slice, wire every facade constructor, including
`newWorkerThreadService` (`worker/handlers.go:706`), session-tab tools, push
reconciliation, code-review workers, and review loops. Supply required ownership,
input/grant, transaction, runtime, and comment ports; no optional nil dependency
may preserve the old bypass. Fail-closed construction and representative worker
caller contract tests are Milestone 2 exit requirements, not deferred to Milestone 3.

Migrate `Orchestrator` queue draining to transaction-bound queued-admission
operations. The runtime still decides when it owns capacity and may attempt a
drain; the shared operation rechecks eligibility, selects/claims pending work,
and atomically commits an exact input/request-to-dispatch binding. For queued
answers, it is the single answer owner and commits only with a bound consumer
and request-specific readiness/drain proof. Deploy the required checkpoint proof
publication/validation before this cutover; the later projection extraction is
not permission to keep inferring readiness from `awaiting_input` and any snapshot.
Remove `answerQueuedHumanInputForContinue`'s independent answer mutation and its
"latest pending" selection; the worker replays the exact persisted request ID.
Normalize legacy jobs under their actual ownership with unambiguous provenance,
or drain/reconcile them first. Do not reconstruct already-answered legacy work
from answer text or create another consumer while an old one may still execute.
Leave no second resumable-status switch or job-payload builder in the drain.
Install the pending-admission reconciler in this slice, not after claiming queued
work is safe. Preserve the provider live-input handshake: `DeliverThreadInbox`
claims before writing to the provider and acknowledges afterward. Drain/recovery
must not select an entry simultaneously in `delivering`, `delivered`, or
`unknown_delivery` as a fresh continuation seed. Inbox-delivery retry delegates
to the new `RetryInboxDelivery` command with explicit replay authorization.
Update cross-key consumer exclusion and specialist conflict probes before enabling
the reconciler or new producers. Test ordinary thread key versus message-drain key
and review recheck versus drain/reconciliation without weakening specialist fences.

Exit: one policy and persistence implementation serves session sends, thread
sends, and drained queued work. Failure injection cannot leave claimed-without-
intent work, lose a queue signal, or exceed the running cap. Runtime owners retain
execution infrastructure; the proven lifecycle start/seed fence must authorize
their current admission/job identity. Queued answers survive retry with the same
request ID, dedupe collisions leave unbound answers pending, and every facade
caller is fully wired and covered.

### 3. Route answers and integration continuations through that owner

Adapt human-input validation/provider behavior as lower-level policy ports and
transaction-bound persistence, with the lifecycle command as transaction owner.
Keep `humaninput.Service` as a compatibility facade where needed; it must not
start a transaction then call another top-level transactional command. Preserve
legacy answer-only behavior through its separate command.
Migrate `humaninput.Service.Cancel` through `CancelInputRequest`, preserving its
denial message, checkpoint checks, answer compatibility, and resume payload.
Use the actual transaction-bound capability approver in concurrency/rollback tests.
Reuse the request-specific checkpoint/generation and producer-drain proof plus
legacy-readiness disposition installed before answering cutover. All immediate
answers and deny-and-resume cancellation use the canonical answer/binding operation;
an old snapshot or a dedupe no-op cannot make a request ready or answered.

Migrate review-comment send-to-agent, Slack continuation/answers, and Linear
prompted continuations. Keep channel authorization, rendering, event receipt
identity, revision policy, and response mapping in adapters. Remove their direct
claim/message/enqueue/compensation sequences. Extend the already-required worker/
server wiring to these direct adapters, including the real grant policy and
transaction-bound persistence ports; a nil approver is a configuration failure.

Implement the Milestone 0 rejection matrix per adapter. Checkpoint/capacity waits
retain the exact unanswered request/ingress receipt for bounded backoff plus
readiness/capacity-triggered reconciliation. Terminal invalid/stale/unauthorized
input produces a user-visible outcome without automatic successor admission.
Do not consume an answer receipt before durable acceptance or exhaust only the
original job's attempts and silently abandon it. Preserve once-only admission
when a later Slack card update fails; retry the card update, not the grant/answer.
Test Slack approval while checkpoint-pending (including an older snapshot),
capacity rejection, stale end, real capability grant, and retry after acceptance.

Exit: all ordinary external conversation-entry paths use the owner; integration
retries cannot repeat a successful answer, grant, or continuation. A provider
notification failure cannot strand an answered request before enqueue. Every
asynchronous rejection has a tested pending/retry/terminal disposition and visible
expiration/escalation; required dependencies cannot silently skip grants.

### 4. Extract retry, cancellation intent, and explicit end

Implement the separate retry modes and end commands, then migrate session/thread
handlers and integration callers. Centralize cancellation marking and delivery
intent while leaving process/RPC execution in runtime adapters. Retain exact-turn
identity and bulk-by-worker cancellation where currently supported.

Maintain current error/status/response behavior and end-without-publication tests.
Reuse the floor's canonical end/start operations without moving or reimplementing
their predicates. Only remaining command eligibility/orchestration and adapter
wiring migrate here. Explicit session end checks idle and fences
pending jobs atomically; thread end excludes stale retries as well as live pollers.
Both ordinary commands apply the canonical retained-owner rejection under the
domain lock protocol, including completed-but-owned review, retired-but-release-
pending automation, and current repair reservations. Specialist control remains
explicit and typed; a rejected end emits no cancellation, domain wake or publication.
Delete replaced compensation helpers only when no call sites remain. Never make
an HTTP cancellation handler claim that execution or snapshots have stopped.

Exit: ordinary lifecycle handlers contain no status eligibility, claims, or
coupled job writes. A state transition change has one command-level test matrix.

### 5. Consolidate ordinary runtime projections and specialist composition

Create narrow fenced operations for ordinary turn completion/failure and pause, including
parent status derived from sibling activity. Worker/executor/agent runtime
adapters supply the current attempt/lease/turn fence; stale writes are rejected.
Preserve result/checkpoint/activity-phase commit boundaries and pending delivery
ambiguity. Do not turn a parent session into idle while a sibling legitimately
owns execution or queued work is being admitted.
Migrate or retain with explicit predicates every status writer in Milestone 0's
inventory, including reaper, PR-created projection, code-review, and failure/revert
paths. Reaper stale-list results are not write authorization; revalidate current
ownership/state at mutation. The floor's prerequisite stale-write protections
remain active throughout extraction.

Classify capability-input requests separately from provider checkpoint-ready
pauses. The capability endpoint's early `awaiting_input` write and orchestrator's
post-checkpoint pause are not interchangeable. Keep request creation/checkpoint
pending versus durably resumable pause explicit. Route ordinary projection
through transaction-bound operations, including parent/thread and compatibility
question state where appropriate; provider checkpoint creation stays with the
runtime. Answer admission must never infer readiness from status alone. List any
temporarily retained pause writer with its owner/fence/checkpoint contract and
remove ordinary duplicates before milestone exit. Request-specific readiness proof
is already required before Milestone 2/3; this milestone consolidates its writers,
not introduces its first trustworthy check.

Adapt PR repair and other specialized continuation producers to the transaction-
bound operations after their domain reservations/locks. Keep their reservation,
revision context, message, and continuation atomic. Automation and review
coordinators retain their assessment/generation/result/publication contracts;
reuse shared operations only where the operation's preconditions actually fit.
List any retained direct writer with a specific owner, fence, and regression test.

Preserve execution-drained versus workspace-drained distinctions. Full review
completion may keep an idle container; reassessment dispatch/replacement/retirement
must retain stricter physical-drain checks. GC must still protect every current
runtime/job/holder/preview and uncertain publication receipt.

Exit: ordinary completion/failure/pause projection has one owner, and retained specialist writers
are explicit, tested exceptions rather than alternate ordinary lifecycle engines.
Broad executor, snapshot, publication, or GC rewrites remain separate work. Exact
request/native identity, resumability/start controls, unique publication keys and
their bounded superseded/orphan cleanup and guarded expiry are already 0a
prerequisites, not deferred by this exclusion.

### 6. Remove bypasses and verify maintainability gains

Add an AST/package-boundary guard that detects direct admission transitions,
message-plus-continuation choreography, raw session/thread `UpdateStatus` calls,
and direct `UPDATE sessions/session_threads SET status` outside the canonical
operations and explicitly named specialists. Include indirect call sites, reaper,
publication, failure/retry cleanup, and start/seed paths, not only migrated HTTP
adapters. Approved DB persistence primitives must have the declared transaction
and fence contract; their existence does not allow arbitrary adapter calls.
Within `internal/db`, specialist methods must use the canonical fence/mutation
primitive for shared lifecycle writes. Domain-only reservation/receipt SQL remains
local; no broad specialist exemption permits copied shared generation predicates
or an escape through generic `UpdateStatus`/`UpdateTurnComplete`.
Classify specialist exceptions by operation, owner, predicate, and regression
test. A broad file/package allowlist would defeat this guard. Assert dependency
direction too.

Remove obsolete mirrored helpers/comments and production fallback implementations.
Keep compatibility facades only as thin mappings, with contract tests proving they
call the canonical implementation. Production construction fails closed when a
required dependency is absent; tests use explicit fakes rather than implicit
nontransactional fallbacks.

Update the relevant internal architecture documentation after implementation
because transaction ownership materially changes. No public API or schema change
is required merely to extract services. Any narrowly justified receipt/fencing
migration must be separately specified, additive, and reviewed with mixed-version
tests; public docs change only if an intentional user-facing contract changes.
The proposed recovery action is such an intentional API/user-facing change:
specify its request/outcome model, additive control receipts, focused frontend
types/hooks, and public setup/API/recovery documentation in 0a before activation.
This plan does not authorize changing public docs or implementation ahead of that slice.

Exit: every ordinary caller and runtime writer in the inventory has migrated or
has an explicit specialist contract. Report removed decision owners, transaction
sites, fallback paths, and dependencies. Line count is supporting information,
not the acceptance criterion.

## Verification and migration safety

Use existing suites as contracts, especially:

- `internal/api/handlers/sessions_test.go`: sends/comments, checkpoint/start-over,
  local/remote cancellation, and end without publication.
- `internal/services/thread/service_test.go`: queue capacity, mid-turn admission,
  backpressure, client IDs, human input, archive races, and notification failure.
- `internal/services/humaninput/recovery_lifecycle_test.go` and
  `recovery_postgres_test.go`: thread turns, capability recovery, and atomic writes.
- `internal/services/agent/orchestrator_drain_internal_test.go`: drain dedupe,
  enqueue failure, human input, resumability, and worker affinity.
- `internal/db/session_thread_archival_postgres_test.go`, automation lifecycle
  fixtures, and Linear/Slack worker tests: competing admissions and ingress retries.
- `internal/worker/session_executor_runtime_test.go`: late lease owners, terminal
  writes, deploy stop/retry, and detached cleanup.
- `internal/worker/code_review_workspace_completion_postgres_test.go`,
  `code_review_completion_cleanup_lock_postgres_test.go`, and agent sandbox-GC
  PostgreSQL tests: drain predicates, lock order, and live-owner protection.

Add real-PostgreSQL tests with independent connections for concurrent sends,
send-versus-answer/cancel/archive, last-sibling completion versus new admission,
capacity drain versus a new send, concurrent repeated ingress IDs, stale completion
versus successor, and enqueue/commit failures. Use barriers rather than sleep-based
ordering. Test old/new writers together where a migration changes lock behavior.
Database tests that skip are incomplete evidence.

Required adversarial regressions additionally include: predecessor final drain
scan then a new send then predecessor acknowledgement; dedupe no-op without a
future consumer; admission/queue crash and restart without another user message;
simultaneous live delivery and drain; replay of unknown delivery only with explicit
authorization; real capability answer/grant/message/job rollback and no nested
lock wait; old request-first versus compatibility/new writers; running thread end
then new input then a predecessor inbox-poller tick that claims nothing, followed
by drain and exactly one successor delivery; late old completion after end;
capability request versus completion;
and checkpoint pause versus answer admission. Give each a bounded liveness check.

Additional required regressions:

- Accepted initial/continuation job or failed-turn retry, then explicit session/
  thread end, then start/restart/handoff: no reopening, provider start, or successor
  seed consumption. Also test start winning versus idle-only session end.
- Seed selected before end/new admission, then runtime attach/seed claim/ack;
  expired-runtime replacement and lease reclaim cannot retarget that input.
- Answer commits then the worker fails/crashes before lease acquisition/dispatch:
  retry preserves the exact request ID and never answers a newer request.
- A predecessor still owns its session/lane key after its final drain scan:
  a new message/answer remains pending without another competing job. A later
  start race returns StartDeferred and preserves its binding through non-consuming
  retry/reconciliation, with no status revert or stranded answered request.
- A pauses while B publishes a checkpoint and then its diff: A pending through
  both steps never blocks B or becomes recovery-required merely for two updates.
  Revalidate A after N steps; with A pending and C certified, only A remains
  pending. A stale/corrupt certificate cannot veto any authorized workspace write.
  Key-only descriptor changes work without revision arithmetic. Same-lane
  successors and unauthorized drift cannot become ready by numeric ordering.
- Selected resumability verification checks the exact bound provider conversation
  on its actual live/restored target. Context loss after answer/grant commit is
  accepted-but-undelivered, not unanswered or acknowledged. Stale checks cannot
  launch; transient I/O defers; uncertain delivery cannot automatically replay.
  Bound answers cannot use missing-ID or failed-native-resume history fallback.
- Known-unlaunched unavailable/unsupported outcomes commit a parked, non-runnable
  job/control receipt with no worker lease. Minutes later, after preparation and
  worker TTLs expire, exact-version retry and abandon still work. Neither reclaim
  nor dead-letter may consume parked intent; repeated controls cannot restore an
  old token, decrement twice or affect a successor. Safe abandon frees only its
  dedicated job/logical claim; fresh ordinary input progresses after real blockers
  settle. Test both answers and exact committed denials.
- Park then end/supersede/recover/archive: current transition authority closes only
  its exact known-unlaunched old intent, including its dedicated job/control and
  owned logical claim. A fresh ordinary send can progress after actual physical
  blockers settle. Unknown acceptance, foreign/successor claims and sibling work
  are not converted to unsent, cleared or replayed. A failed control-row CAS must
  leave all dispatch/job/claim state unchanged, not a dangling parked version.
- Crash after final permit but before launch-marker commit, versus after marker
  commit: only fenced positive no-launch evidence permits same-binding recovery.
  Old permits cannot race that recovery into Execute. Phase-1 preparation emits
  no Slack/eval running effect. The residual marker-before-call crash window stays
  uncertain; no marker claims to prove actual provider acceptance. Current or
  prior same-binding delivery uncertainty with a missing FIRST marker must still
  prevent refund, no-launch classification, retry/abandon-as-unsent and a new
  allocation's launch; missing legacy marker data is not proof of no delivery.
- A known native conversation awaiting its first availability observation from a
  valid live producer stays checkpoint-pending (legacy deadline unchanged).
  Missing expected published observation or positively known loss still fails
  closed. Provider/build/variant activation and later switches require tested
  resume support, without reinstating private-isolation prerequisites.
- Snapshot/context expiry races answer and start. Availability is revoked and
  notice/delete intent committed before physical deletion; same key/revision/
  generation with destroyed availability is not ready. Stale GC cannot delete a
  successor object. Reaper, explicit archive, merged-PR follow-ups and auto-archive
  all use that boundary. Pending input does not override maximum retention age.
  Include overlapping PR/branch/push async uploads: fresh key per attempt, exact
  pending-upload receipt for publication/clear, stale completion cannot promote
  or clear a newer upload, and old tombstone/delete cannot target successor bytes.
  Registered pre-Save cleanup work discovers superseded and never-installed keys;
  reference creation and exact-key tombstone serialize, and unresolved writers
  remain visible blockers rather than false timeout-based drain. Test grace,
  orphan completion, crash/retry and stale cleanup with the existing storage API.
- Any-session SKIP LOCKED queue claim and multi-session cancellation/sweeps take
  late shared cutover try-locks without blocking; only real exclusive conflict
  aborts. RC late-statement guard checks see activation, activation backs off
  without starvation claims, and all protected explicit transactions set the
  writer version locally under transaction pooling.
- Crash immediately after terminal-job commit and before an in-memory dead-letter
  callback: durable bound-terminal sweep settles exact no-allocation admission
  failure plus input/notice. Unknown allocation cannot be misclassified as never
  started, and late allocated output remains out of successor context.
- Queued-answer dedupe collision cannot mark input answered without a bound
  consumer. Thread-key versus message-drain-key and specialist recheck versus
  reconciler cannot consume the same input or hide a competing owner.
- Legacy `queued_message_id`-only jobs mixed with new consumers/producers have
  proven binding/drain/reconciliation outcomes; ambiguity never selects latest.
- Reaper list, successor admission, stale reaper write; PR-created/failure/revert
  writes racing admission/end: stale mutation is rejected or revalidated safely.
- Characterized pre-floor lock inversions versus pre-floor/floor and floor/new
  concurrency, including implicit FK timeout/known-abort retry with no effects.
- Every Milestone 2 worker/facade constructor supplies required ports and fails
  closed without them. Push, review-loop, code-review and tool paths still work.
- Slack checkpoint/capacity waiting and terminal errors have durable recovery/
  user outcomes; capability approval uses the real grant transaction. An earlier
  snapshot plus a new capability request is not a ready checkpoint.
- Legacy-readiness migration: provably matching paused request receives proof
  without an answer; a live compatible producer receives a deadline-bound wait;
  unprovable paused/orphan request or deadline expiry gets one visible recovery-
  required outcome. Late proof cannot silently reuse old consent. New explicit
  recovery produces a fresh request; old request/input/audit remain attributable.
- For each immediate session/thread answer, `AnswerInput`, and deny-and-resume
  cancellation: dedupe collision without exact binding commits no answer/grant/
  resume claim; a safe bound consumer preserves the exact request across retry.
  Waiting receipts are not reported as answered/delivered. Test the one-request-
  per-resume limit and no displacement of another input, including legacy question
  compatibility and answer-only calls.
- Before M4, compatibility entry points delegate to the canonical start/end/seed/
  delivery/projection fence. Boundary and call-through tests reject copied
  predicates or a nil-dependency fallback; the same fence tests survive cutovers.
- Ordinary end versus owner acquisition/release/dispatch: reject completed review
  with retained owner, retired automation with release pending, and a current
  repair reservation without changing domain work. Historical released associations
  alone do not reject an otherwise eligible end. Typed specialist control requires
  its exact identity and drain proof; stale runtime cleanup still cannot affect a
  successor. Test both session-wide and thread-local ownership scope.
- Proof-writer/legacy-classification/enforcement ordering and legacy-job activation:
  older producers retain characterized behavior before activation; no unclassified
  producer remains afterward. Exhausted lock retries retain accepted waiting work
  or return an explicit contention outcome, never a false answer acceptance.
- Paused ordinary request -> enforced recovery-required notice -> authorized
  `RecoverPendingInput` action -> exact accepted recovery receipt -> quiescent
  recovery execution -> fresh provider request -> fresh answer. The parent never
  needs a fabricated failed state. Include unavailable/provider-unsupported
  checkpoint, explicit start-over, affected-thread preservation, active sibling,
  duplicate/conflicting keys, accepted-recovery failure and reattempt, and
  answer/cancel/end/owner acquisition races. Unknown delivery/grant cannot be
  superseded as unconsumed; old pending approval is not reused or a committed grant
  silently revoked. API/tool/notice UI is reachable before enforcement activates.
- Shadow classification while an old Slack/worker/API answer path is active emits
  no effective classification or notice. At activation, race answer/cancel with
  locked reclassification: exactly one wins; classification and enforcement version
  commit together. Include new-request creation, legacy-job normalization, delayed
  outbox notice after recovery, and rollback to an incompatible consumer being denied.
- Each DB-only specialist call in the composition table uses the same fence/mutation
  primitive while preserving its receipt/result/lease locks and outer commit.
  Recheck scheduling cannot nest transactions. Atomic-CTE conversion cannot expose
  an intermediate session write; `CompleteFromMarker` cannot publish before its
  service-owned outer transaction commits. Boundary tests cover DB escape hatches.

Use table-driven isolated tests, parallel where safe, `require` with exact values
and descriptive messages. Compare semantic outcomes, not implementation call
sequences. Verify no provider/Redis/Slack/GitHub effects before commit, no automatic
retry after unknown commit, and eventual durable-job discovery after a failed wake.
Any lifecycle concurrency test added under `cmd/server` must be explicitly covered
by CI, whose current general backend package inventory otherwise omits most cmd tests.

During each slice run tests/vet for touched packages. Before preparing the complete
refactor for publication, run the affected API/service/worker suites, the tagged
integration suite with a disposable migrated database, relevant race tests,
`make lint-tenancy`, repository-pinned lint, and full Go tests/vet because shared
contracts change. Record command, revision, passes, skips, and any unresolved
failure. Planning review is not implementation validation.

Cut over one command/caller slice at a time. Do not dual-write or execute both
old and new engines for comparison; pure decision comparisons may run against
captured fixtures without effects. Each deployed slice has one active owner and
removes its superseded implementation. Prove coexistence with old runtime callers
during rolling deploys. Retain job payload and receipt compatibility; no queue
reset, worker termination, automatic replay, or production configuration change
is part of this refactor.

Observe bounded command outcomes, lock wait/admission latency, pending inbox age,
claim-without-intent/recovery counts, and duplicate/stale rejection signals using
existing telemetry where possible. Compare representative fixtures/workloads to
avoid adding a hot global/session lock to running-message fast paths. Define the
per-slice regression thresholds before rollout. Include selected resumability
verification cost, launch-marker round trips, bounded certificate metadata fanout,
and per-transaction writer-version round trips on hot heartbeats in that gate.
Rollback uses a known compatible
revision; it must not reinterpret new durable records or trigger replay.

## Acceptance criteria

- One tested implementation owns each ordinary transition, and every entry point
  in the inventory delegates to it. Adapters do not retain mirrored policy.
- Session and thread semantics, actor attribution, wire responses, job targeting,
  dedupe, checkpoint requirements, and publication boundaries remain explicit.
- Coupled admission writes have one transaction owner; composing specialists do
  not open nested transactions or emit effects before commit.
- Queued work remains durable and discoverable; uncertainty is not converted to
  blind retry or compensating writes over a successor.
- Runtime completion/failure/pause projections retain attempt fences and sibling safety.
- Every runtime start/restart/handoff and seed mutation is bound to a valid
  admission/job identity, including initial jobs and legacy payload disposition.
  Explicit end fences not-yet-started work as well as already-running predecessors.
- Every resume-producing answer/denial has one mutation/binding owner and durable
  exact request/message/consumer identity across crashes and dedupe; no worker
  reselects latest pending input. Waiting receipts leave the request unanswered;
  legacy answer-only behavior stays distinct.
- Consumer proof is per-input and generation-bound across all key families;
  reconciliation and specialist exclusions cannot create a second consumer.
- A dedupe no-op is not treated as execution ownership, and committed queued work
  recovers after a process restart without another user send or Redis wake.
- DB-backed grants and state/result/activity writes use the caller transaction
  without hidden transactions, savepoints, or pre-commit publication.
- A verified compatibility release precedes changed transaction overlap, and
  both pre-floor/floor and floor/new coexistence are tested. Explicit end cannot
  let an old runtime overwrite a successor or deliver its newly queued input.
- Automation/review/GC lock order, drain distinctions, and immutable receipts stay
  protected by real concurrency regressions.
- Production constructors enforce required dependencies, and architecture tests
  prevent new admission bypasses without broad file exemptions.
- All status writers are classified and conditionally fenced; reaper/publication/
  failure paths cannot overwrite successors. Guards cover raw/indirect writes.
- All asynchronous lifecycle rejections have tested durable pending/retry or
  terminal user-visible outcomes. Readiness is request-specific before answer
  cutover; missing grant ports fail closed.
- A request's pending/unprovable certificate cannot veto an authorized sibling
  workspace write. Certified readiness requires usable workspace and exact
  request/provider identity under the trusted-session model. Launch requires the
  companion's selected resumability check and final fenced authorization;
  failed checks preserve committed answer/grant and undelivered intent.
- Unavailable starts have durable controls independent of expired execution
  leases. Safe retry/abandon and pre-launch crash recovery follow the companion's
  exact parked/launch protocol; every supported provider variant is tested before
  activation and candidate image routing. Current transitions terminalize stale
  known-unlaunched controls without fabricating physical drain. Running effects
  follow final permission, not preparation.
- The job-status inventory distinguishes runnable/lease-owned work from
  nonterminal ownership and cleanup. Exact-key snapshot cleanup has a durable
  owner, bounded discovery/eligibility and reference/writer guards; optional bucket
  lifecycle configuration is not an unstated correctness dependency.
- Pre-proof pending requests have a proven ready/waiting/recovery-required
  disposition before enforcement. No indefinite retroactive-proof wait, blanket
  snapshot backfill, lost original answer intent, or cross-generation consent.
- The compatibility floor introduces canonical fence operations once; old callers
  delegate without copying predicates. Later milestones reuse that owner.
- Ordinary end rejects retained specialist ownership under the shared lock
  protocol; typed specialist control alone may retire its work. Completion or
  historical associations are not substituted for positive owner-release proof.
- Recovery-required has a reachable request-scoped action from ordinary paused
  state, with explicit mode, idempotent receipt, affected-lane identity, safe
  quiescence, and fresh consent. Ordinary sends expose the action rather than
  falling through failed-only retry or claiming an unavailable readiness proof.
- Effective classification/enforcement and notice intent commit together per
  request after the compatible-writer floor; stale shadow results and late notices
  cannot contradict accepted answers/recovery. Completed M1 is always after 0a.
- DB owns shared durable fence/mutation primitives; service policy and specialist
  transactions compose them without reverse imports, duplicated predicates,
  validate-then-write gaps, nested commits, or pre-commit effects.
- Verification proves behavior and concurrency; reduced file size alone does not
  establish success.

## Progress

- [x] Establish the source baseline and inventory admission, runtime and projection
  owners.
- [x] Add executable inventories covering 606 lifecycle symbols, including 240
  concrete operation contracts, and 123 classified job-status sites.
- [x] Add characterization tests against existing entry points and isolated
  PostgreSQL lock/readiness/control probes.
- [x] Specify transaction composition, exact answer binding, readiness/recovery,
  parked-control supersession, snapshot cleanup, adapter outcomes and the
  compatibility-floor rollout.
- [x] Enforce both inventories in repository-root command tests and add
  checker-path regressions for newly introduced status sites.
- [x] Complete independent adversarial review of M0 and address all blocking
  design and characterization findings.
- [x] Start 0a with the passive transaction runner/configurator and focused unit
  and real PostgreSQL transaction tests. Review-driven regressions prevent a
  runner from taking ownership of an existing connection's outer transaction.
- [x] Validate real-SessionStore composition on PostgreSQL 17.10 with the complete
  unmodified migration chain, including callback-error rollback and outer commit.
- [x] Expose caller-owned approved-grant persistence, preserving the legacy
  wrapper and SQL/order. Unit tests and six synthetic actual-SQL PostgreSQL
  cases cover shared commit/rollback, duplicates, tenant rejection and deleted
  sessions. Explicitly inventory the new root; it does not yet implement
  canonical admission authority.
- [x] Validate the six approved-grant composition cases on PostgreSQL 17.10
  with the complete unmodified migration chain, plus concurrent characterization
  of legacy lost updates and prior-statement prelocking; no prerequisite skips
  remain. This does not fix or authorize conversion of the legacy writer.
- [x] Complete final independent review of this passive foundation candidate
  after the cleanup, concurrency-characterization and proof follow-ups.
- [ ] Add remaining passive versioned schema and composable persistence with
  their concrete consumers, then wire canonical mutations by inventoried family.
- [ ] In 0a, prove new/legacy readiness, all-answer binding, canonical fence
  wiring, retained-owner end policy, activation and compatibility with real callers.
- [ ] In 0a, complete remaining real-caller/full-schema characterization and close
  lock/receipt implementation proof gates; synthetic M0 probes do not satisfy them.
- [ ] Complete the compatibility-floor release before changing transaction overlap.
- [ ] Implement milestones 1–4 for ordinary commands and adapters.
- [ ] Implement milestone 5 for runtime projections and specialist composition.
- [ ] Complete boundary guards, verification, documentation and rollout evidence.

### M0 validation

The M0 exit checks below passed locally on 2026-10-18. They
describe the characterization/tooling candidate, not production acceptance or a
hosted-CI result. To reproduce the PostgreSQL probes, set `TEST_DATABASE_URL` to
an owned disposable PostgreSQL database; these tests create and remove isolated
synthetic schemas.

| Check | Recorded local result |
| --- | --- |
| `go test -cover ./cmd/lint-lifecycle -count=1` | Exit 0; 94.3% statement coverage. |
| `go test -race ./cmd/lint-lifecycle -count=1` | Exit 0. |
| `go run ./cmd/lint-lifecycle` | Exit 0; 606 lifecycle symbols and 123 classified job-status sites match. |
| `TEST_DATABASE_URL=<disposable-db-url> go test -race ./internal/db -run '^TestSessionLifecycle.*Probe(Postgres)?$' -count=1 -json` | Exit 0; 222 cases: 213 PostgreSQL and nine Go-only, with no failures or skips. |
| `go vet ./cmd/lint-lifecycle ./internal/db` | Exit 0. |
| `go test ./internal/db -run 'Tenan' -count=1` | Exit 0. |

The 123-site status ledger contains 102 Go-source jobs-SQL sites, six explicit
Go conditions/switches and 15 historical SQL files. Classes are 42 lease-owned,
30 lifecycle-cleanup, 16 nonterminal-consumer, five runnable, 14 unrelated-domain,
nine observation and seven terminal-only. Forty-two mixed sites have 101
explicit predicate policies. `TestJobStatusInventoryMatchesRepository` checks
the real ledger; parallel checker-path fixtures prove new singleton, aliased-set
and negated-set production sites fail without review. Existing CI runs the
command tests, so this guard needs no separate workflow.

The 222 probe cases cover readiness (72), workspace compatibility (22), prepared
start (36), parked control (13), launch marker (8), possible acceptance (4),
resume capability (6), snapshot publication (6), atomic control (10), parked
supersession (12), idle-session end (2), pinned-build rollout (5), snapshot
garbage collection (13), lock order (3), implicit FK locking (1) and Go-only
sealed results (9). Go source was gofmt-clean and the submitted files had no
whitespace diagnostics. The disposable database was removed after its fixture
schemas were gone.

These probes used PostgreSQL 14.19 with minimal synthetic schemas. They do not
establish behavior on a fully migrated schema or supported PostgreSQL versions,
actual API/domain/queue/reaper/executor callers, real concurrent compound
transitions, provider resumability, image rollout, storage Save/Delete,
activation or deployment. Those are 0a proof gates. The syntax inventories also
do not prove dynamic SQL, arbitrary aliases/imported helpers or the correctness
of the eventual production predicates. Recheck each classified site when
implementing its migration.

### 0a transaction-foundation validation

Initial race verification passed 295 subtests plus standalone checks, including
the existing M0 probes, new transaction unit cases and 12 real PostgreSQL cases.
There were no failures or skips in that selected run. PostgreSQL cases verify
READ COMMITTED despite different pool defaults, transaction-local settings on a
reused connection, application-error rollback, confirmed-abort retries for NOWAIT
and implicit FK contention, and preservation of an existing caller transaction.
Vet, tenancy checks, both source inventories and formatting also pass.

The earlier PostgreSQL 14 prerequisite gap is now closed for these two passive
foundations. Both full-schema test roots pass on disposable PostgreSQL 17.10,
applying the complete unmodified migration chain through migration 000314 in
separate databases. `TestLifecycleTransactionFullSchemaPostgres` executes real
SessionStore create/update/read behavior and verifies that only the outer unit
can commit or roll back the mutation. The test server uses a private Unix socket
with no TCP listener; no kernel settings, existing database services, production
data, consumer registration or activation changed. This is not proof of canonical
caller wiring, mixed-writer coexistence, the deployed floor or production overhead.

The approved-grant persistence slice also passed its focused unit and race
checks. The original five PostgreSQL 14 synthetic cases execute the actual store
SQL and verify standalone and linked commits, caller rollback of earlier work
and both snapshots, unchanged duplicates and foreign-org rejection. PostgreSQL
17 verification now covers six cases on both synthetic and fully migrated
schemas, adding exact soft-delete rejection and rollback of earlier caller work.
Two additional full-schema cases deliberately characterize the legacy concurrent
lost update and verify that a prior-statement parent lock retains both distinct
grants in the session and automation snapshots. They observe the server-side
blocking graph rather than infer ordering from sleep. This is proof of a required
composition prerequisite, not a repair of the unchanged legacy wrapper or a
canonical authorization operation. The reviewed source
inventory now contains 607 lifecycle symbols (one added persistence root) and
the unchanged 123 job-status sites. This increment is passive extraction, not
an additional lifecycle authority owner or completion of 0a. The legacy lock
inversion and independent human-input grant transaction remain until canonical
admission and all mixed-writer conversions are implemented and proved.

After the review follow-ups, the combined race selection on PostgreSQL 17.10
passed 334 subtests and 45 test roots, with zero failures or skips (23.429s).
Both full-schema roots also passed three additional repetitions: 30 root/subtest
pass events, no failures or skips (51.832s). No fixture databases or lock-probe
schemas remained afterward. Reproduce
with an owned disposable PostgreSQL 15+ server in `TEST_DATABASE_URL` and
`go test -race ./internal/db -run '^(TestLifecycleTransactionFullSchemaPostgres|TestAgentCapabilityApprovedGrantFullSchemaPostgres)$' -count=3`.
The complete selection also includes the M0 probes and transaction/grant unit
tests; narrow full-schema success does not replace the remaining 0a gates.

Review regressions cover panic and `runtime.Goexit` cleanup without replacing the
abnormal exit or replaying work. A one-connection PostgreSQL pool verifies that
Goexit releases the connection and rolls back its staged write. A real two-row
deadlock produces SQLSTATE 40P01, proves the aborted write absent at a retry
barrier, then commits only the fresh attempt; that test also passed ten
race-enabled repetitions. Both worker goroutines are cancelled and joined before
fixture cleanup, including assertion failures. The settings matrix verifies
NULL-before-use and empty-after-transaction custom-GUC behavior on the same
connection without a startup marker. Unit checks sample the 25–100ms jitter
bounds and observe the minimum delay after rollback before replacement
acquisition. The runner still makes no minimum commit-time reservation; live
context is not proof that a subsequent commit outcome will be certain.

The final permission-aware candidate repeated the combined race selection with
334 subtests and 45 roots passing, no failures or skips (12.144s). The deadlock
victim probe requires permission to set `deadlock_timeout`: only SQLSTATE 42501
from its initial setting statement skips that probe, before starting workers.
Other setup errors still fail. The owned PostgreSQL 17 run exercises the actual
deadlock; an environment that skips it does not establish that part of the proof.
A focused check with a restricted non-superuser role in its own disposable
database reached that explicit skip; its temporary database and role were removed.

Vet, tenancy, source inventories, formatting and the pinned security scanner
pass for the touched packages. The linter command has 95.1% statement coverage
and passes its race suite. Gosec module version v2.22.4 scanned 140 files in
`internal/db` and `cmd/lint-lifecycle` with zero issues. The only new scanner
exception is a single-site G404 justification for non-security contention-delay
jitter; it does not govern identity, authorization or replay permission.

Independent Opus review at xhigh approved this passive foundation slice on
2026-10-18 with no residual findings under the unchanged acceptance criteria.
The reviewer verified all ten frozen file hashes before and after review and
independently repeated the 45-root/334-subtest PostgreSQL race selection without
failures or skips. The final changes to this plan only record that verdict and
complete the review checkbox. This is not approval of canonical authority,
caller conversion, whole 0a, activation, deployment or production acceptance.

## Surprises & Discoveries

Shared ownership includes integration workers and runtime drains, not just the
two HTTP surfaces. Existing paths differ in intentional behavior and failure
atomicity. Making thread admission transactional changes lock overlap; current
thread/session acquisition order cannot simply be copied into one large
transaction. Baseline writers already invert lock order, so compatible rollout
must distinguish existing cycles from regressions.

Capability grants and integration notification retries need explicit transaction
and recovery treatment. A successful dedupe call can return no job, and existing
owner-loss recovery does not discover all orphan pending input. Explicit end
does not by itself revoke an old runtime's turn identity. Execution start lacks
the assumed ordinary-end fence, and queued answering happens in the worker, not
the drain. These source findings require start/seed fencing and durable answer
binding; they are not evidence that existing behavior has been repaired.

## Decision Log

- Keep specialist runtime/domain ownership. Consolidate ordinary conversation
  decisions without creating a universal lifecycle service.
- Extract legacy session send first, then thread admission and runtime drain.
  Preserve their distinct public behavior rather than routing everything through
  the current thread service.
- Separate transaction-owning commands from explicit transaction-bound operations.
  DB owns the canonical durable fence/mutation primitives; ordinary service policy
  and specialist DB transactions compose them without reverse imports, copied
  predicates or nested commits.
- Deploy and verify the lock/fence compatibility floor before answer-enabled
  cutover. Only passive preparation may precede 0a; completed M1 always follows it.
  Prove both pre-floor/floor and floor/new coexistence.
- Fence execution start/restart, seed attachment and predecessor delivery claims,
  including initial jobs. Running thread end uses drain-before-successor execution.
- Bind each resume-producing answer/denial to its exact request and consumer.
  Waiting admission leaves the request unanswered. A dedupe key alone is not
  ownership proof, and a worker must not reselect the latest pending request.
- Keep shadow classification inert. Effective readiness classification,
  enforcement version and notice intent commit together after compatible writers
  and in-flight legacy calls are accounted for.
- Provide request-scoped `RecoverPendingInput` from recovery-required paused
  state before enabling recovery notices. Ordinary end rejects retained specialist
  ownership; specialist end remains an explicit typed domain action.
- Use the trusted-session workspace/provider-HOME baseline. Exact request
  identity, sticky continuity certificates, pre-launch native verification and
  tombstone-first cleanup remain mandatory. Provider-private bundles/isolation
  are optional separate hardening, not an M1 gate.
- Keep companion section 7 authoritative for readiness and recovery; parent-plan
  summaries must not become a second policy.
- First-release answer/denial jobs bind only that request's intent, not unrelated
  ordinary input. Safe abandonment releases only its exact logical claim and
  preserves committed grants/audit. Physical uncertainty remains independently
  fenced.
- Separate execution-preparation TTL from durable start-control authority.
  Current end/successor/recovery/archive/delete transitions close stale
  known-unlaunched controls without treating expiry as physical drain.
- Require tested provider/build/continuation-mode evidence before activation and
  candidate-image routing. Active sessions never downgrade to version 0.
- Use a fresh key for every upload, including PR/branch/push snapshots, with
  pre-Save durable cleanup work and exact reference/writer/tombstone eligibility.
  Current-key cleanup schedules the key's existing job; `snapshot_object_cleanup`
  belongs only to SnapshotReaper, never generic executor/claim paths.
- Classify every discovered jobs-status predicate by its purpose before
  activation. Runnable claims, nonterminal input ownership, cleanup and unrelated
  domain state machines must not share a blindly broadened status set.

## Adversarial review

Independent adversarial review accepted the M0 technical scope on 2026-10-18 with
all six review criteria passing and no critical or major findings remaining:

1. Stale parked work has current-authority closure and cannot permanently retain
   a logical lane claim.
2. Affected job-status sites have explicit classifications, with repository-test
   enforcement against new or changed uncovered sites.
3. Unique snapshot keys have a durable cleanup owner, bounded discovery and
   reference-safe/writer-safe eligibility.
4. Reconciliation requires the exact control write before other authority can
   advance, and pinned-build rollout requires the tested tuple.
5. Focused mechanical checks pass without skipped database cases.
6. Scope remains M0 plan, inventory and characterization work; uncertainty,
   immutable request binding, consent and compatibility safeguards are preserved.

The review applies to design and synthetic-characterization scope. It does not
approve a compatibility release or establish runtime correctness. One optional
0a ledger follow-up remains: mirror the contract's `snapshot_object_cleanup`
exclusion in the ClaimNextRunnable entry's `action_0a` when implementing that
migration. Exclusive SnapshotReaper routing is already a required contract, not
a waived gate.

## Outcomes & Retrospective

Milestone 0 is complete for characterization, inventory tooling and the migration
contract. It changes no lifecycle runtime behavior, schema or compatibility-floor
activation. The acceptance criteria for the full refactor remain future
implementation gates. Keep existing-entry-point tests, isolated query-pattern
probes, proposed operations and rollout verification distinct when reporting
progress.
