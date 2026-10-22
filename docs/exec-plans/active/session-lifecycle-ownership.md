# Give shared session lifecycle decisions one owner

Status: Milestone 0 design and characterization accepted by independent
adversarial review on 2026-10-18, with no critical or major findings remaining.
Approval covers the plan, contract, inventories and synthetic probes only; real
caller, full-schema, concurrency, storage, provider and image proof remains in 0a.
No compatibility-floor enforcement or canonical lifecycle mutation behavior has
been enabled.
Milestone 0a has started with passive transaction and grant-persistence
foundations, independently approved on 2026-10-18 after follow-up review.
A subsequent passive slice adds `nodes.lifecycle_writer_version` to existing
registration and heartbeats, advertising version 0 only. Shared/reused node IDs
are last-writer-wins; legacy writes may retain stale metadata. This is not
process-instance provenance or proof of a compatible floor. The next passive
slice adds the same version-0 observation to executor allocation, mark-running
and heartbeat writes. Allocation is dispatcher-owned, not executor process/build
attestation; legacy executor writes can also retain stale metadata. Neither field
changes placement or authorization. A following passive slice registers each
standalone session-executor invocation in `lifecycle_writer_instances` using a
fresh process UUID, actual binary build and version 0. Its asynchronous observer
uses the ordinary pool, with a 30-second interval, two-minute observational TTL
and cooperative five-second attempt budget. It remains alive through runtime
cleanup and joins before pool closure; fatal exits leave rows to expire. Failed
observations are logged and retried, never awaited for startup or admission.
They may still contend for ordinary-pool capacity. Existing daily maintenance
jobs, scheduled for organizations with active integrations, each attempt at most
ten 1,000-row batches within one shared cooperative five-second budget. Cleanup
stops on a short batch, the batch cap or budget exhaustion; normal budget exits
log partial progress, while other database errors and parent cancellation remain
errors. The SQL still removes only observations expired for more than seven
days and protects concurrent renewals. Daily capacity is at most 10,000 times
the number of executed scheduled organization jobs, and can be lower under the
time budget or failures. No active organizations means no scheduled cleanup.
Seven days is eligibility, not a deletion deadline or a bounded history size;
inserts above actual cleanup throughput can grow the backlog indefinitely.
No row establishes continuous liveness, drained transactions or a deployment
compatibility floor. The next passive slice extends the same observer to normal
`api`, `worker` and `all` server modes. Each invocation has one fresh UUID and
its configured role; combined `all` mode has one row, not separate API and
worker rows. Role describes configuration, not successful capability startup.
Unsupported modes log and skip observation without changing startup admission.
Normal server observation begins after the existing credentials migration gate
on the ordinary pool, with the same interval, TTL, version and attempt budget.
The normal HTTP serving path now joins its existing coordinated shutdown routine
before returning: listener closure alone precedes HTTP/gateway/socket finalization.
Observation continues through that routine, then is canceled and joined before
ordinary-pool closure. Genuine serving failures keep their abrupt fatal exit.
Existing shutdown policies and timeouts are unchanged. Workers and credential
socket handlers are not all joined by that routine, and some cleanup has no hard
global bound; neither routine completion nor row expiry proves every writer has
drained. Connection census, business callers, schema guards and activation remain
unchanged; this expanded but partial inventory does not complete 0a.
A following passive slice labels normal API, worker and combined-server
connections, their existing worker control-plane reserve, and standalone executor
connections with `application_name`. One formatter retains the configured role
and exact 40-hex binary build (or `dev`); empty build metadata is `unknown`, and
other values use an explicitly marked 128-bit SHA-256 fingerprint rather than
raw metadata or implicit server truncation. Labels are printable ASCII and fit
PostgreSQL's 63-byte limit. Supported runtime roles intentionally override a
DSN-supplied name; unsupported normal-server modes and callers without attribution
retain their previous naming. Existing pool budgets, timeout/cancellation policy,
observer version and transaction semantics do not change. These labels do not
identify process instances, prove drained transactions or establish a writer
floor, and transaction-pooling proxies need not preserve backend attribution.
Connection census, command/operator pool coverage and every activation gate
remain separate work; no production inventory or rollout is established here.
A subsequent passive slice adds `cmd/lifecycle-census`, a standalone read-only
command requiring a single explicit `DATABASE_URL` URI host and database name.
It rejects empty/comma-separated hosts and ambiguous database aliases or target
keys, then normalizes the selected target before pgx parsing. The port is explicit
or pinned to PostgreSQL's default 5432, never inherited from `PGPORT` or a service.
It opens one direct connection and one READ COMMITTED, READ ONLY transaction,
then separately samples unexpired writer leases and current-database client
activity, excluding its own backend. Masked backend types are retained as unknown;
effective statistics privileges, null fields and disabled tracking remain explicit.
Each source transfers at most 1,001 rows, retains 1,000 and uses the extra row
only to detect overflow before aggregation. Truncated counts describe the
retained sample, never source totals.
The database statement clocks are separate; this is not an atomic snapshot or a
process-instance/backend join. Allowlisted role/build/version aggregates omit raw
identifiers, query text, addresses and arbitrary metadata. Labels remain spoofable.
Connection, setup and reads share a cooperative five-second context budget;
rollback and connection closure each have a separate cooperative 250ms budget.
Closure also awaits pgx's asynchronous cleanup within that same close budget.
Fixed server-side `statement_timeout` and `lock_timeout` values of five seconds
bound each statement/wait independently of client survival. They are not a
five-second total server wall-time guarantee across setup and both reads. If
client cancellation/cleanup cannot finish before process exit, the server limits
still bound the abandoned statement. URI/environment `options` and differently
cased aliases cannot override the fixed session name and timeouts.
Limits bound returned observations, not all internal database work. Versioned JSON
on stdout is an observation, not a floor/drain verdict. Failures return nonzero
with sanitized diagnostics; an output failure can leave a partial JSON prefix.
Operational output belongs outside this public checkout. Runtime/deploy wiring,
command/operator coverage, independent inventory/drain receipts, canonical callers,
guards and activation remain separate gates. This command does not complete 0a.

Passive attribution also covers the existing one-connection pools in
`deploy-guardrail` and `worker-deployctl`, using `143:operator:<safe-build>`.
The fixed connection-only operator formatter shares the runtime build-label
policy. Both packaged operator binaries embed the same `BUILD_SHA` argument as
the server; builds without an injected SHA retain the explicit `dev` label.
The commands intentionally override a DSN-supplied application name while
retaining their existing 15-second and 20-second contexts, pool limits and
operational decisions. They start no writer observer and set no writer-version
marker. The four lease-writer roles and registry schema remain unchanged.
The census recognizes operator connection labels but continues to classify a
supplied operator writer-lease role as unknown. This extends the connection-role
enum within the existing schema-1 JSON shape; external consumers with a strict
four-role enum must accept the added value before relying on these reports.
Labels remain self-reported and do not distinguish these two tools, invocation
identity or privilege. This is partial command coverage: `backfill-usage`,
`migrate`, `wait-postgres`, `demo-seed` and manual SQL paths remain unattributed
by this slice. No command uses a label as deployment, drain or activation proof.
The subsequent command slices below supersede that partial coverage list.

A following migration-only slice labels connections opened by `cmd/migrate`
with the same `143:operator:<safe-build>` policy. It retains golang-migrate's
PostgreSQL/libpq driver, migration source, database selection and defaults,
custom `x-*` options, migration locking and narrow dirty-repair allowlists.
Only case-insensitive application-name parameters are replaced in recognized
PostgreSQL URIs; other input retains the existing driver's handling. Its query
parsing behavior is preserved. Docker and `make build` embed
the existing binary build SHA in the migration command, while uninjected
single-file `go run` builds retain `dev`. There is no observer, lease or
writer-version marker. This does not label `demo-seed`'s separate embedded
migrator, `backfill-usage`, `wait-postgres` or manual SQL. Schema behavior,
writer authority and every deployment/activation gate remain unchanged.
The subsequent demo attribution slice below covers its embedded migrator.

Demo-seed check mode now removes native `dbname`/`database` query overrides when
selecting its generated temporary database, for both pgx and lib/pq. Malformed
query parameters fail before it opens a connection. Unsupported case variants
retain PostgreSQL's rejection behavior. A full-schema check against a disposable
source database verifies that its original rows and catalog remain unchanged and
that the temporary sibling is removed. This target-isolation repair adds no
writer attribution, observer or lifecycle authority.

The auxiliary attribution slice labels `backfill-usage` and `wait-postgres` with
the same operator/build policy. Backfill retains its pool defaults and rollup
behavior. The waiter parses and labels each existing retry attempt, preserving
native URI/keyword/environment handling, Ping/Close and timeout behavior. The
shared pool builder now removes case-insensitive application-name aliases only
when a caller explicitly supplies attribution; empty options retain all previous
naming inputs. Unrelated startup options remain untouched. Neither command starts
an observer or sets a writer-version marker; uninjected builds retain `dev`.

Demo-seed's admin, fallback and temporary pools and its embedded migrator now
share that passive operator/build policy. Pool budgets, native parsing, simple
protocol and existing Check deadlines/cleanup remain unchanged. The migration
URI transformation has one owner in `internal/db`; `cmd/migrate` delegates to
it without changing driver behavior. Existing demo entrypoints remain uninjected
development builds unless their caller explicitly supplies the shared SHA linker
flag. Labels do not distinguish command instances, prove privilege or grant a
writer floor. Manual SQL and any unlabelled external writer remain inventory gates.

The passive epoch schema slice adds `admission_epoch bigint NOT NULL DEFAULT 0`,
nullable `end_epoch bigint`, and `lifecycle_version smallint NOT NULL DEFAULT 0`
to sessions and threads. Migration 322 changes only sessions; migration 323
changes only threads, each in a separate transaction with a five-second lock
timeout. This avoids holding both hot-table DDL locks in one transaction.
Each table still requires its own exclusive DDL lock; lock timeout remains a
migration failure, not evidence that the schema was applied.
Existing rows and legacy inserts receive version-zero defaults without an
application backfill. A separate DB-only `LifecycleEpochState`
and tenant-scoped read store expose the values without changing public session or
thread representations. Thread reads also require the exact parent session.
Reads retain existing deleted-session/archived-thread visibility and acquire no
row locks. SQL types and nullability are storage representation only; no epoch
relationship policy or admission/start/end authority is installed. No existing
runtime caller reads, increments or enforces these fields. Dispatch/claim records,
canonical fences, cutover state, writer-version changes and activation remain
separate 0a work. Roll back readers before removing the passive columns.

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
| Recheck `Dispatch`, `Claim`, `Complete`, `Fail`, `FailTerminalJob`, `Cancel`, `ReconcileDrainedTerminalTurn` | Existing review transaction and PR/assessment/receipt lock protocol | Same-package DB primitive calls replace shared session/thread mutations; receipt, result, and lifecycle projection stay atomic. Audit actual nested consumers such as `cancelForcedEvidenceAssessment`'s PR-locked terminal reconciliation and use explicit in-transaction variants, not nested wrappers. The scheduler sweep is a pool-backed caller, not nested Dispatch. |
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
the canonical `AppendApprovedGrantInTx` authority operation. The initial extraction
did not rewire human-input callers; the later version-0 answer atomicity step
below does. Canonical consent, exact request/readiness, epochs and domain
domain prelocking remain unimplemented gates. Existing snapshot representation
is sufficient for this slice; no speculative schema is added. The syntax guard
explicitly inventories the extracted root and its direct callers so extraction
cannot silently remove its review coverage.

The initial extraction retained a joined-read lost-update hazard: its metadata
snapshot preceded the wait on `FOR UPDATE OF s`, and acquiring the lock did not
refresh that snapshot. A subsequent freshness slice requires READ COMMITTED
before any parent access, locks the parent in a separate statement, then reads
fresh metadata. Both wrapper and caller-owned paths preserve concurrent distinct
grants and recognize a concurrent duplicate on the full schema. An unsupported
caller or connection-default isolation fails explicitly without being downgraded;
the caller retains settlement of its transaction. No writer marker is installed.
Historical binaries using the joined read/lock SQL can still overwrite grants:
the test-only historical probe retains that evidence, not a desired production
contract. This repair does not establish a drained floor, canonical lock order,
exact consent/readiness or epoch authority. The session-before-automation order
and independent human-input grant transaction still require conversion.

The result/phase persistence slice exposes concrete `pgx.Tx` variants of the
three existing combined result, status and turn-completion boundaries. Existing
wrappers remain transaction owners and call the same persistence implementation.
No InTx path begins, settles, retries or publishes, including its row/head/diff
helpers. Opaque `SessionPersistenceEffects` returns workspace notification data;
the owner discards it after error, rollback or uncertain commit and publishes
only after confirmed commit. It is not a commit receipt, durable outbox or
lifecycle authority. Status and phase publication remain service responsibilities.
SQL predicates, legacy returned-row shape and session/phase association behavior
are unchanged: canonical admission must still establish exact phase ownership,
current allocation and shared epochs before composing these resource-only writes.

Thread status, no-result turn completion, result and result-bearing turn writes
also have concrete `pgx.Tx` persistence variants. Each shares unchanged private
row SQL with its legacy wrapper; only the wrapper retains immediate best-effort
publication. InTx paths return opaque org/thread reload identity, never publish
or settle, and reject missing transaction or result before writing. Legacy nil
result behavior is unchanged. The explicit post-commit adapter reloads current
visible state, possibly a later write; it does not promise the earlier write's
snapshot, durable delivery or commit attestation. Archived-row mutation versus
hidden notification, nil versus empty diff, exact turn and provider-ID behavior
remain intentional legacy semantics, not new authority checks.

The inventory guard registers the explicit composable seam names as both
boundaries and contract-required direct calls. A cross-package `p.store` receiver
cannot bypass review merely because its name lacks session/thread vocabulary.
The two generic InTx name collisions require an explicit reviewed non-lifecycle
classification; ordinary generic status/result discovery remains unchanged.
Empty-SQL lexical fixtures prove discovery and contract enforcement for all eight
named result/phase, thread and seed seams. This is syntax coverage, not runtime
transaction ownership, semantic call resolution or authority enforcement.

Standalone session turn persistence now owns the row/head/snapshot sequence used
by its legacy transactional branch, combined phase boundary and actual evidence
recheck completion. Recheck uses explicit session and thread InTx methods in its
existing receipt transaction, including head-only and authoritative empty-diff
results; it no longer enters a nested savepoint for those cases. Its read-only
nonempty-diff rejection, root usage, domain receipt checks and legacy lock order
are unchanged. Temporary stores still have no publishers and returned passive
effects are deliberately discarded, so this conversion adds no notification.
It does not add canonical shared allocation or admission/end epoch fencing.

Seed materialization, inbox claim/ack/application, phase insertion and runtime
watermarks now have one caller-owned `StartSeedPhaseInTx` persistence sequence.
The legacy wrapper still validates before Begin and retains its original commit
and error behavior. The explicit seam does not settle or publish, including
through phase-start helpers; successful phase/batch records remain staged until
the outer owner commits. Existing runtime lease, exact message/range, historical
never-started batch and primary-tab rules are unchanged. They are not immutable
allocation, shared admission/end epoch or new provider-launch authority.

Standalone result and status projections now also expose explicit caller-owned
transaction seams. Combined result/status plus phase persistence delegates to
them. The plain status wrapper and result wrapper's no-head/no-snapshot direct
path retain their original DBTX behavior; only the existing transactional result
branch delegates. Returned session records keep the original UPDATE RETURNING
shape, before later snapshot revisions or AFTER-trigger mirrors. Status, terminal
stream expiry and workspace notifications retain their original order after
pool-backed commit. Legacy wrappers called through tx-bound stores can still
publish after savepoint release before the true outer commit; converting those
callers and carrying effects to the outer owner remains required. No new
allocation, current-turn or epoch authority is implied.

Force-fresh review cancellation now passes its existing PR-locked transaction to
`ReconcileDrainedTerminalTurnInTx`, rather than entering a nested savepoint via a
tx-bound wrapper. The same terminal receipt, retained PR owner, physical drain
and exact thread-turn predicates are shared with the standalone wrapper. The PR
owner still updates scheduling state, commits and then emits its existing event.
This is caller-transaction composition, not shared epoch or allocation fencing.

The structured-answer path now constructs its approved capability through one
shared validation method and persists it through the existing answer transaction.
A later claim, message, job enqueue or commit failure therefore rolls back both
the answer and capability snapshots, including the linked automation mirror.
The handler adapter only constructs the item; it cannot independently commit a
grant. The standalone grant service retains its configuration error and duplicate
behavior and currently has no production callers; it remains for compatibility
and tests. Answer's pure construction does not require that standalone appender;
production wiring still configures it. This version-0 atomicity prerequisite
preserves existing pending-request,
approval parsing, checkpoint and resume policy, including the request-before-parent
lock order. It is not the canonical `AdmitAnswerInTx`/`AppendApprovedGrantInTx`
authority operation, and cannot activate readiness enforcement or a writer floor.
Acquisition order is unchanged, but the session `FOR UPDATE`, metadata and
automation-row locks now remain held through the outer claim/message/job commit.
The stronger parent lock can block implicit FK `KEY SHARE` child inserts for
that window; this does not establish compatibility with competing run-first
automation writers or the chosen future lock protocol.

Atomic steering now calls a private concrete-transaction acknowledgement core,
instead of entering the standalone acknowledgement owner's nested savepoint.
One SQL sequence retains old-phase closure, exact delivered-input acknowledgement,
batch creation and watermark CAS before the existing replacement-phase operation.
The standalone path keeps input validation and initial replay before Begin, plus
its distinct ordinary and concurrent-replay commit errors. The public service
still publishes only after the store reports success; no new provider authority,
epoch check, lock order or event policy is introduced.

Full-assessment completion and crash reconciliation now settle their assessment,
joined requests and PR scheduling state through the same transaction already
owned by the worker. Shared private PR locking and terminal-policy helpers retain
the existing repository/tenant checks, active-owner and pending-intent rules.
The standalone scheduler still reads assessment identity before Begin, commits
and then publishes its optional event. The two worker stores still have no
publisher, so removing their nested savepoints adds no event. This is persistence
composition, not a new current-allocation, review-authority or epoch fence.

The pool-backed automation attempt owner now captures status and workspace
effects from its existing result/status/turn callback methods. They use explicit
supplied-transaction persistence, and the fresh bound store's inherited methods
have no publisher. Only a successful outer commit dispatches effects through the
original publisher, preserving each call's returned row and status/expiry/workspace
order. Callback, late duration and commit errors discard them without retry.
The actual failed-result and awaiting-input status paths no longer publish before
commit. Actual successful automation callbacks remain diff-free and gain no new
event; arbitrary diff-bearing callbacks are separately covered as a latent case.
This owner-local adapter is not a supported way to nest an already tx-bound
`TurnStore` or a replacement for canonical turn authority.

Scheduled review creation now uses a private session-store adapter whose only
override calls the existing `CreateInTx` with the PR owner's transaction.
Standalone creation, the service interface and inherited read/status/failure
methods remain unchanged. The scoped store is still fresh and publisherless.
The real scheduler therefore persists its session, primary thread/changeset,
review metadata, request, controller job and PR state without a creation savepoint.
This removes nested settlement; it does not add canonical review admission.

Automation checkpoint publication now persists the unchanged checkpoint and
generation-provenance statement through a concrete supplied-transaction seam.
The real attempt-locked owner dispatches the captured checkpoint event only after
its outer commit, through the original publisher. Existing diff effects retain
their `diff_snapshot` reason; checkpoint effects retain `checkpoint` and the
original returned revision/time. The standalone DBTX wrapper keeps its existing
single-statement behavior and immediate notification policy.

The legacy CTE can update the session before finding no matching provenance row,
then return false. The seam returns no effect and requires whole-owner rollback
on false as well as error; the automation owner already does that. Standalone
autocommit's historical false-with-mutation behavior is explicitly characterized,
not silently repaired here. No active-generation predicate, final-pause/readiness
proof, upload obligation, new authority or compatibility floor is added.

The recheck publication callback now also uses `SettleAssessmentInTx` with the
existing publication owner's concrete transaction. Assessment completion,
request/scheduler settlement and optional status-comment job insertion keep
their original order and commit together without a scheduling savepoint.
Earlier publication preparation and the bounded external receipt protocol remain
separate; this does not make GitHub publication atomic with the database or add
new scheduling notifications, review policy or canonical execution authority.

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
- [x] Validate passive runtime connection attribution on PostgreSQL 17, including
  fresh, reused and reset-pool connections and PID-scoped activity checks. This
  does not implement transaction census, process-instance correlation,
  command/operator pool coverage or a writer floor.
- [x] Add a standalone passive census consumer for bounded, separate lease and
  connection observations, with explicit privilege/unknown/truncation semantics.
  No runtime/deploy gate consumes this report and no compatible floor is proved.
- [x] Validate the passive census on the full PostgreSQL 17 schema, including
  real restricted/inherited statistics roles, disabled tracking, expiry clocks,
  overflow boundaries and lock-synchronized cancellation and budget expiry.
  These local checks do not establish deployment, CI or a compatible writer floor.
- [x] Add connection-only operator attribution to the two packaged lifecycle-aware
  deployment commands and their census labels, without expanding lease roles or
  changing pool budgets and operational decisions.
- [x] Validate operator labels on fresh, reused and reset PostgreSQL connections,
  full-schema census aggregation, command wiring and packaged build metadata.
- [x] Validate migration-only operator attribution through the actual command
  and PostgreSQL driver, including target/options, up/down and packaging behavior.
- [x] Isolate demo-seed check-mode writes from database-name query overrides,
  with both native-driver target checks and a full-schema source-preservation test.
- [x] Validate backfill/waiter attribution through real PostgreSQL factories,
  binary startup frames and native environment/keyword targets, including the
  shared opt-in pool's case-alias handling without writer authority.
- [x] Attribute demo-seed's existing pools and embedded migration driver with
  shared operator metadata while preserving target isolation and native behavior.
- [x] Repair grant snapshot freshness for conforming READ COMMITTED wrapper and
  caller-owned paths, proving retained concurrent grants, duplicate detection and
  pre-lock isolation rejection on the full schema. Historical-writer drain and
  canonical consent/lock-order conversion remain open.
- [x] Expose transaction-composable result/status/turn plus activity-phase
  persistence with explicit post-commit workspace effects. Full-schema tests
  prove exact outer commit/rollback and enabled-publisher silence; canonical
  allocation/epoch/phase association checks remain unimplemented.
- [x] Expose four thread persistence seams with explicit current-state
  post-commit effects, preserving legacy wrappers and verifying full-schema
  outer settlement, tenant rejection and notification behavior.
- [x] Strengthen grant proof for concurrent duplicate first grants and absent
  metadata visibility, and make attribution test observation/cleanup synchronous
  so server-side connection exit timing does not create a false failure.
- [x] Register composable persistence boundaries and direct generic callers in
  the syntax guard, with concrete-contract regressions. Review-driven vocabulary
  coverage does not promote these persistence seams to lifecycle authority.
- [x] Compose actual recheck completion with standalone session/thread turn
  persistence, proving head-only and empty-diff results plus late receipt failure
  roll back atomically without nested settlement or new notifications.
- [x] Expose caller-owned seed/ack/activity persistence with one shared SQL
  sequence, preserving legacy wrapper behavior and proving exact outer settlement,
  late failure rollback, scope rejection and committed-seed replay protection.
- [x] Share standalone result/status transaction persistence with existing
  combined boundaries and the legacy transactional result path, preserving
  direct-write fast paths, original returned records and notification order.
- [x] Remove the nested terminal-reconciliation savepoint in the actual
  force-fresh PR callback, preserving standalone outcomes and proving whole-owner
  rollback plus scheduling publication only after visible outer commit.
- [x] Keep version-0 structured-answer grant construction pure and persist its
  request-keyed item through the same answer/claim/message/job transaction, with
  full-schema late-failure rollback of session and linked automation snapshots.
- [x] Remove atomic steering's nested acknowledgement savepoint with one shared
  private SQL core; preserve standalone replay/error behavior and inventory the
  current public acknowledgement chain through its real service callers.
- [x] Compose full-assessment completion and reconciliation with caller-owned
  assessment/request/scheduler settlement, retaining standalone PR event policy
  and proving whole-owner late-failure rollback without a nested savepoint.
- [x] Defer automation attempt result/status/turn effects until its actual
  pool-backed owner commits, preserving diff-free success and proving late
  marker/callback/duration/commit rejection discards every staged notification.
- [x] Compose actual scheduled review creation with its existing PR owner via
  `CreateInTx`, retaining standalone/inherited behavior and proving whole-admission
  rollback after a genuine late scheduling-write failure.
- [x] Defer automation checkpoint events until the attempt-locked owner commits,
  sharing unchanged provenance SQL with the standalone wrapper and documenting
  its historical false-with-mutation edge and required caller rollback.
- [x] Compose the actual recheck publication callback's scheduler settlement
  with its existing publication owner, preserving exact receipts and optional
  status synchronization while proving final enqueue rejection rolls back all
  earlier callback writes.
- [x] Add passive session/thread epoch and lifecycle-version columns with DB-only,
  tenant/parent-scoped reads; prove fresh/upgrade/rollback/reapply, nullable bigint
  round trips and unchanged legacy create/read/status/API behavior on PostgreSQL.
- [x] Add independently nullable request admission epochs with no default or
  backfill, plus a tenant/session/request-scoped passive read. Prove legacy
  threaded and threadless requests retain unknown provenance even with nonzero
  parent epochs, and preserve existing request models and API fields.
- [x] Add nullable request classification/readiness representation without
  defaults or inference. Keep its DB-only reader observational, preserve unknown
  metadata and existing API behavior, and validate known classification labels
  separately from admission authority.
- [x] Add passive runtime/executor/inbox-batch admission epochs and raw runtime
  drain metadata in separate single-table migrations. Prove exact nullable-lane
  reads and unchanged legacy allocation, heartbeat, terminal, reclaim and batch
  behavior; the existing lease reclaimer leaves drain proof absent.
- [x] Establish exact tenant/session/lane referenced-key prerequisites with three
  standalone concurrent unique indexes. Prove index shape, row preservation,
  synthetic composite-FK scope and visible dirty-state refusal without installing
  production foreign keys, changing retention or creating lifecycle authority.
- [x] Add the empty dispatch identity/ingress foundation with nullable epochs,
  scoped parent references, structural checks and exact ingress/message uniqueness.
  Preserve unknown provenance and legacy behavior without adding a producer,
  consumer, claim, admission state or runnable scanner.
- [x] Add a private transaction-local parent/explicit-lane locking kernel with
  stable batched acquisition and read-only policy verification. Prove exact scope,
  caller-owned settlement and confirmed-abort retry composition without exporting
  authorization snapshots or wiring production callers.
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
inversion remains until canonical admission and mixed-writer conversions are
implemented and proved. The independent human-input grant transaction recorded
at this initial extraction is removed by the later version-0 atomicity step;
that does not implement the canonical authority or readiness contract.

A subsequent grant-freshness slice changes the current persistence seam: it
checks READ COMMITTED, locks the parent in a separate statement and reads joined
metadata afterward. Its combined focused race selection passed on PostgreSQL
17.10: six root tests and 58 subtests, zero failures or skips. The complete
unchanged schema covers both wrapper and caller-owned concurrency, exact
duplicates, initially absent metadata and rejection of repeatable-read,
serializable and read-uncommitted isolation before parent access. The historical
joined-read test remains explicitly test-only evidence that older binaries must
still drain. Reproduce with an owned PostgreSQL 15+ server and
`go test -race -p=1 ./internal/db -run '^TestAgentCapabilityApprovedGrant' -count=1
-parallel=1 -timeout=3m`. This is freshness proof, not consent or floor proof.

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

### 0a passive-census validation

The census selection passed locally on PostgreSQL 17.10 with the complete
unmodified migration chain: 36 root tests and 210 subtests, no failures or skips.
Reproduce with an owned disposable PostgreSQL 15+ server in `TEST_DATABASE_URL`
and `go test -race -p=1 ./internal/db ./internal/services/lifecyclecensus
./cmd/lifecycle-census ./cmd/migrate -run
'^(TestLifecycleCensus.*|TestParseLabel|TestAssemble.*|TestCensusEnumValidation|TestCollect.*|TestRun.*)$'
-count=1 -parallel=3 -timeout=3m`. The eight census full-schema roots include eleven
table subtests. The activity-source overflow boundary is mock-tested; the writer
boundary is also exercised against PostgreSQL. No test opens 1,001 connections.

Actual CLI subprocess probes hold an independent table lock across natural
budget expiry, SIGTERM and SIGKILL, then confirm backend cleanup before unlocking.
SIGKILL leaves no surviving client cancellation goroutine: server-side timeouts
must bound that abandoned read. Unit tests also hold pgx cleanup pending and
verify that closure and its completion wait share one 250ms budget. Explicit
target tests cover dropped empty hosts, slash-only paths, ambiguous aliases and
hostile libpq environment/URI overrides.

Focused race/coverage runs report 92.5% statement coverage for the census service
and 88.9% for the CLI. Vet, the repository-pinned linter, tenancy/lifecycle guards
and the security scanner pass for the checked scope. This entry records local
mechanical evidence; each publication candidate still requires independent review.

Run `go run ./cmd/lifecycle-census` with an explicitly configured PostgreSQL URI
in `DATABASE_URL`; it accepts no arguments and has no target fallback. Treat the
JSON and diagnostics as private operational evidence. The command is not wired
into startup, scheduling, deployment or activation, and a zero exit code means
only that observation collection and report delivery succeeded.

### 0a operator-attribution validation

The operator slice passed local race checks: 86 root tests and 1,045 subtests
across the focused runtime/model/store/census selection and the complete
`cmd/migrate`, `cmd/deploy-guardrail` and `cmd/worker-deployctl` packages, with no
failures or skips. Real-PostgreSQL checks run on 17.10 over direct TCP; the census
cases apply the complete unchanged schema. They verify fresh, reused and reset backend labels,
PID-scoped activity, absent writer-version settings, exact operator transaction
aggregates and an empty writer registry. Negative tests reject an operator lease
before SQL and keep synthetic operator writer observations unknown.

Set `TEST_DATABASE_URL` to an owned disposable PostgreSQL 15+ server. Reproduce
the focused selection with `go test -race -p=1 ./internal/db ./internal/models
./internal/services/lifecyclecensus ./cmd/lifecycle-census ./cmd/server -run
'^(TestLifecycle(Connection.*|Operator.*|WriterRole.*|WriterInstanceStore_Observe.*|Census.*)|TestNewPoolConfigApplicationName|TestParseLabel|TestAssemble.*|TestCensusEnumValidation|TestCollect.*|TestRun.*|TestWorkerControlPlanePool.*|TestNewWorkerControlPlanePoolCanceledStartup)$'
-count=1 -parallel=3 -timeout=3m`. The full migration package uses
`go test -race -p=1 ./cmd/migrate -count=1 -parallel=3 -timeout=3m`; the two
operator packages use `go test -race ./cmd/deploy-guardrail
./cmd/worker-deployctl -count=1`.

Vet and the repository-pinned linter pass for all eight affected packages;
tenancy and lifecycle syntax inventories also pass. Both operator commands
compile with the SHA linker flag for the local toolchain and Linux/amd64.
Source tests verify the actual command pool calls and Docker build invocations.
These checks do not construct or deploy the Docker image, prove transaction-mode
proxy attribution, cover every operator path or establish a compatible floor.
Independent exact-candidate review remains a separate gate.

### 0a migration-attribution validation

The complete `cmd/migrate` race suite passed locally on PostgreSQL 17.10:
41 root tests and 782 subtests, with no failures or skips. Reproduce with an
owned disposable PostgreSQL 15+ server in `TEST_DATABASE_URL` and
`go test -race -p=1 ./cmd/migrate -count=1 -parallel=3 -timeout=3m`.
Fixtures apply the complete unchanged migration chain before exercising the
actual stamped migration executable and the retained golang-migrate/libpq driver.

The command tests observe its exact backend label at a database-lock barrier
before allowing synthetic up/down migrations to proceed. They verify path and
query database targets, custom migration-table options, URI/environment option
precedence, unrelated startup settings, absent writer-version markers and an
empty writer registry. Separate actual-driver cases verify `dev` and marked
fingerprint labels. Unit tests compare the pinned driver's parsed options,
preserve malformed/non-URI handling and check the real single-file entrypoint.
Output checks retain the existing timing log and success framing, normalizing
only the variable duration.

Vet, repository-pinned lint, tenancy/lifecycle guards and the security scanner
pass for the checked scope. Docker/Make wiring tests and local/Linux-amd64 builds
verify SHA injection without constructing an image. These are local checks,
not deployment, proxy attribution, floor/drain proof or completion of 0a.
Independent exact-candidate review remains a separate gate.

### 0a auxiliary-command attribution validation

The complete `cmd/backfill-usage` and `cmd/wait-postgres` race suites passed
locally on PostgreSQL 17.10: 11 root tests and 22 subtests, with no failures or
skips. Reproduce with an owned disposable PostgreSQL 15+ server in
`TEST_DATABASE_URL` and `go test -race -p=1 ./cmd/backfill-usage
./cmd/wait-postgres -count=1 -parallel=3 -timeout=3m`.

Full-schema factory probes run in separate processes to exercise native
URI/keyword/environment precedence without mutating shared test environment.
They verify the exact target, safe name, unrelated startup settings, absent
writer-version marker and empty writer registry. Separate startup-only protocol
peers reject authentication before SQL; these prove actual stamped/dev command
metadata and the documented single-file backfill entrypoint, not database
behavior. The real waiter also proves successful Ping and connection closure.
Parse-error, cancellation, retry-wait and actual-loop wiring checks retain the
existing retry policy. Shared pool tests exercise opted-in runtime/operator
aliases and unrelated options on fresh, reused and reset backends. These remain
local attribution checks, not deployment, floor/drain proof or completion of 0a.

### 0a demo attribution validation

The complete demo and migration race suites passed locally on PostgreSQL 17.10:
55 root tests and 822 subtests, with no failures or test skips. `cmd/demo-seed`
has no test files and reports a package-level skip. Reproduce with an owned
disposable PostgreSQL 15+ server in `TEST_DATABASE_URL` and
`go test -race -p=1 ./internal/demoseed ./cmd/demo-seed ./cmd/migrate -count=1
-parallel=3 -timeout=3m`.

The full-schema Check still migrates and seeds only its generated sibling.
Separate pool cases verify native configuration, simple protocol, safe labels,
unrelated options and absent writer marker after opening, reuse and reset.
The embedded lib/pq probe observes the exact connection blocked on a synthetic
custom ledger, then verifies no-change success and the unchanged ledger/table
set. It is a minimal-ledger driver probe, not another full-schema migration.
An additional linked-SHA test run covers those real pool/driver paths; it does
not claim an actual demo command invocation. The unchanged complete migration
command suite separately exercises the promoted shared URI helper. These checks
add no observer or lifecycle authority and do not establish a deployed floor.

### 0a result/phase persistence validation

Focused unit and full-schema race tests passed locally on PostgreSQL 17.10:
four root tests and 63 subtests, no failures or skips. Reproduce with an owned
disposable PostgreSQL 15+ server in `TEST_DATABASE_URL` and
`go test -race ./internal/db -run '^TestSessionPersistence' -count=1 -parallel=3
-timeout=3m`. Existing result/status/turn regressions and focused vet also pass.

An occupied one-connection pool and transaction wrapper reject nested settlement;
an independent reader sees the exact original row set until outer commit.
Whole-row comparisons cover session, phase, publish state, primary changeset and
diff snapshots. A missing phase demonstrably leaves earlier status/snapshot
writes staged until outer rollback; a foreign tenant stages none. Configured
Redis publishers remain silent before commit and after rejection or rollback;
explicit org-checked workspace publication retains the existing event fields.
Legacy wrapper begin, phase and uncertain-commit failures produce no wakeup.
No test treats an opaque in-memory effect value as a committed receipt or exact
once delivery. This proves persistence composition only, not allocation, epoch,
phase association, deployment or compatibility-floor acceptance.

### 0a thread persistence validation

The focused race selection passed locally on PostgreSQL 17.10: seven root tests,
86 subtests, no failures or skips. Reproduce with an owned disposable PostgreSQL
15+ server in `TEST_DATABASE_URL` and `go test -race -p=1 ./internal/db
-run '^TestThreadPersistence' -count=1 -parallel=1 -timeout=3m`. Legacy/new unit
regressions and focused vet also pass.

The complete unchanged schema covers one occupied connection, stable transaction
identity, exact whole-row outer commit/rollback, foreign/nil rejection before
writes and enabled-publisher silence. Tests retain lower exact turns, empty
provider-ID differences, nil/empty diff, failure and timestamp fields, archived
row behavior and publication of a later committed state. Reload/publish failures
remain best-effort warnings. These are composable persistence prerequisites,
not canonical allocation/epoch fences, deployment or writer-floor evidence.

Review follow-ups add wrapper and InTx concurrent duplicate-first-grant cases,
independent pre-commit absence checks and an anchored metadata-read mock query.
Grant freshness remains scoped to conforming grants, not unrelated snapshot
replacement writers or historical binaries. Attribution observations now poll
server-side backend disappearance and complete their query before connection
cleanup, including timeout paths. These are test and documentation refinements;
they do not broaden writer authority or establish a deployment floor.

### 0a recheck persistence validation

Focused race and full-schema tests passed locally on PostgreSQL 17.10: five new
root tests and 23 subtests, no failures or skips. The selection also retains the
existing combined result/phase regressions. Reproduce with an owned PostgreSQL
15+ server in `TEST_DATABASE_URL` and `go test -race -p=1 ./internal/db
-run '^(TestSessionTurnPersistence|TestSessionTurnAndRecheckPersistenceFullSchemaPostgres|TestRecheckCompleteExplicitPersistenceWiring|TestSessionPersistence)' -count=1
-parallel=1 -timeout=3m`. Focused vet and whitespace checks also pass.

The real `Complete` method runs against the complete unchanged schema, with
synthetic exact review/receipt/lease rows rather than a claim of Dispatch or
admission proof. A one-connection outer owner rejects nested Begin and captures
the exact staged graph. A fixture-only final-receipt trigger first verifies that
assistant, phase, session/thread, head and optional snapshot writes occurred,
then suppresses the receipt update. The application-level rejection leaves the
transaction inspectable until its owner rolls back every touched row. Successful
completion preserves root historical usage, exact receipt provenance and the
existing lack of publication. This is atomic persistence proof, not a new fence,
deployed compatibility floor or production acceptance.

### 0a seed/ack persistence validation

The new focused race/full-schema selection passed locally on PostgreSQL 17.10:
five roots and 87 subtests, no failures or skips. Reproduce with an owned
PostgreSQL 15+ server in `TEST_DATABASE_URL` and `go test -race -p=1 ./internal/db
-run '^TestSeedActivityTransaction' -count=1 -parallel=1 -timeout=3m`. Five existing
legacy PostgreSQL seed-wrapper roots and 27 subtests also pass, including
concurrent starts, historical ranges and backfill. Focused vet and gofmt pass.

Tests cover every retained SQL/collect/count failure stage, exact wrapper errors,
and a complete unchanged schema with one occupied connection. Whole-row
message/inbox/batch/runtime/phase snapshots prove independent invisibility and
exact outer commit/rollback. A real late phase uniqueness failure verifies that
materialize, claim, ack and apply writes occurred before rollback; a later caller
SQL failure removes a successful complete seed unit too. Foreign relationships,
wrong/expired lease and invalid input stage no writes in the tested fully-foreign
cases. Mixed valid/foreign seed sets can stage earlier writes and require whole
owner rollback. Rolled-back seeds remain
replayable; already committed/applied seeds remain rejected after their original
phase completes. Returned batch timestamp representation retains the legacy
input shape while its durable instant is checked independently. These are
persistence proofs, not canonical admission/start fencing or floor acceptance.

### 0a scalar result/status persistence validation

Four new focused race/full-schema roots and 46 subtests passed locally on
PostgreSQL 17.10, with no failures or skips. Reproduce using an owned PostgreSQL
15+ server in `TEST_DATABASE_URL` and `go test -race -p=1 ./internal/db
-run '^TestSessionScalarPersistence' -count=1 -parallel=1 -timeout=3m`. Retained
combined result/phase and standalone turn/recheck regression selections also pass.
Focused vet and whitespace checks pass.

One occupied connection and guarded transaction settlement prove the explicit
seams use only the outer transaction. Whole-row snapshots establish independent
invisibility, exact commit/rollback and unchanged phase/publication groups.
Foreign-tenant and deleted sessions stage no writes. A fixture-only late snapshot
rejection verifies genuine earlier row/head writes before whole-owner rollback.
Enabled Redis tests compare complete serialized original returned rows and exact
status, terminal expiry, then workspace command order. Nil results, head-only and
empty authoritative diffs retain legacy behavior; failed or uncertain commits
produce no events. This remains persistence composition, not current allocation
authority, canonical fencing, a deployed writer floor or production acceptance.

### Composable persistence proof follow-ups

Independent review follow-ups strengthen tests without changing production
behavior. Foreign thread notification effects must produce the exact explicit
rejection log before any reload, and zero effects produce no log or database
access. Seed retry after rollback validates returned phase/batch values against
the staged rows. Recheck completion now explicitly checks the root's authoritative
empty diff. Legacy turn wrapper tests configure real Redis streams and cover
successful workspace publication, no new status event, failure/uncertain-commit
silence and optional notification failure.

The focused race/full-schema selection passes locally on PostgreSQL 17.10:
four roots and 43 subtests, no failures or skips. Reproduce with an owned
PostgreSQL 15+ `TEST_DATABASE_URL` and `go test -race -p=1 ./internal/db
-run '^(TestSessionTurnPersistenceLegacyTransactionEffects|TestThreadPersistenceCommittedEffects|TestSeedActivityTransactionFullSchemaPostgres|TestSessionTurnAndRecheckPersistenceFullSchemaPostgres)$'
-count=1 -parallel=1 -timeout=3m`. Existing result/turn wrapper and DB/worker
recheck regressions also pass at the preceding scalar-persistence snapshot:
27 roots and 42 subtests, no failures or skips. Neither result proves all legacy
tx-bound wrappers defer effects until their real outer transaction commits.

### 0a force-fresh reconciliation composition validation

The new concrete-transaction reconciliation preserves the original SQL sequence
and raw errors. The standalone wrapper retains early false/no-commit outcomes
and the original successful boolean when its commit returns an error; callers
must still inspect that error. Nil and typed-nil transactions are rejected.
Future generic callers require a concrete inventory contract, and repository
root assertions pin all newly extracted persistence boundaries.

Focused race tests passed locally on PostgreSQL 17.10: seven roots and 97
subtests, no failures or skips, including existing force-fresh service modes.
Reproduce using an owned PostgreSQL 15+ `TEST_DATABASE_URL` with two selections:
`go test -race -p=1 ./internal/db
-run '^Test(CodeReviewRecheckStoreReconcileDrainedTerminalTurn|RecheckReconciliation)'
-count=1 -parallel=1 -timeout=3m`, then `go test -race -p=1
./internal/services/codereview
-run '^(TestForcedEvidenceReconciliationUsesSuppliedTransaction|TestCodeReviewSchedulingLifecyclePostgres)$/force_fresh'
-count=1 -parallel=1 -timeout=3m`. Focused vet and pinned lint pass.

Full-schema tests run both direct caller-owned reconciliation and the real
`WithLockedPR` owner with a one-connection pool and nested-Begin rejection.
Exact whole-graph snapshots prove tenant/successor isolation and outer settlement.
A fixture-only final scheduling-write trigger first observes genuine staged
parent and thread reconciliation, then raises a controlled failure; outer rollback
restores those rows and scheduling state together and emits no event. Successful
publication uses an independent reader to observe the already committed graph.
The actual force-fresh callback's supplied-tx call is separately pinned in source;
existing service modes cover its retained behavior. This does not implement a
canonical current-allocation/epoch fence or prove a deployed compatibility floor.

### 0a structured-answer capability grant atomicity validation

Eight focused race roots and 40 subtests pass locally on PostgreSQL 17.10, with
no failures or skips. Reproduce using an owned PostgreSQL 15+
`TEST_DATABASE_URL` and `go test -race -p=1
./internal/services/agentcapabilities ./internal/services/humaninput ./internal/api/handlers
-run '^(TestServiceBuildApprovedGrant|TestServiceApplyApprovedGrant.*|TestHumanInputApprovedGrant.*|TestHumanInputDBGrantRejectsNilTransaction|TestHumanInputCapabilityApprovalRecovery|TestSessionCapabilityApproverBuildsWithoutAppender)$'
-count=1 -parallel=1 -timeout=3m`. Full touched-package race verification also
passes: 1,490 roots and 1,890 subtests, with one existing external image-tool
fixture skip. Focused vet and whitespace checks pass.

Pure construction shares the prior catalog/access checks and exact item fields
with standalone approval; only the retained standalone method checks appender
configuration first. Real `Answer` runs on the full unchanged schema
with a one-connection owner and nested-Begin rejection. Whole-row snapshots prove
independent invisibility and atomic settlement of the request, capability metadata,
automation mirror, claims, message and continuation job. Tenant mismatch and a
same-tenant request from another session cannot bind a grant. Denial, nil service,
invalid catalog data, existing grant retries and duplicate answers retain their
previous semantics. Fixture-only late claim/message errors observe genuine prior
answer/grant staging; enqueue and deferred-commit triggers first validate earlier
writes, then raise genuine PostgreSQL errors. All rejected units return no accepted
result and restore the exact prior row set. This intermediate version-0 repair is
not consent/readiness/epoch fencing or evidence of a deployed compatibility floor.

The visibility observer owns a dedicated physical connection opened before
`Answer`, independently of the shared fixture setup/DDL pool. This avoids a
parallel-test pool-acquisition cycle that PostgreSQL cannot detect while another
case's trigger DDL waits on the open answer's table locks. Explicit migration
driver registrations keep the fixture independent of sibling test files.
Repeated race verification with `GOMAXPROCS=4` passes at default parallelism
(`-count=3`: three roots, 36 case executions) and at `-parallel=8 -count=5`
(five roots, 60 case executions), with no failures or skips. Serial proof alone
did not exercise this harness hazard.

### 0a atomic steering acknowledgement composition validation

The expanded database race selection passes locally on PostgreSQL 17.10:
11 roots and 62 subtests, no failures or skips. Reproduce with an owned
PostgreSQL 15+ `TEST_DATABASE_URL` and `go test -race -p=1 ./internal/db
-run '^TestInboxAck|^TestSessionActivityPhaseStoreAcknowledgeInboxBatch|^TestSessionActivityPhaseStorePostgresConcurrentBatchRetriesCreateOnePhase$'
-count=1 -parallel=1 -timeout=3m`. Two retained agent delivery-receipt roots
and nine subtests also pass: `go test -race -p=1 ./internal/services/agent
-run '^TestThreadDeliveryPostgres(LateReceiptDoesNotResurrectPhase|AtomicSteeringRejectsForeignOrStaleReceipt)$'
-count=1 -parallel=1 -timeout=3m`. Focused vet and formatting pass.

The real atomic owner runs with one occupied connection and rejects nested Begin.
Independent whole-row observations remain unchanged until the outer commit;
successful commit exposes exact staged old/new phases, acknowledged/applied inputs,
batch receipts and runtime watermarks. A scoped replacement-phase trigger first
checks genuine earlier mutations, then raises a real PostgreSQL error; whole-owner
rollback restores the entire prior graph. Injected outer commit error, started
replay, abandoned repair, missing/duplicate confirmations, uncertain input and
foreign lease retain original outcomes. Unit tests cover every retained SQL/count
failure, initial replay before Begin, nil/typed-nil inputs and commit error prefixes.

The syntax inventory now covers the previously unregistered acknowledgement chain:
five DB symbols and three actual agent/service callers. New exact names require
concrete root/direct-caller contracts even for generic receivers. These are
conservative syntax candidates, not semantic runtime authority. Standalone legacy
lease checks use acknowledgement time and allow draining, while initial replay
does not revalidate live ownership; the atomic path additionally checks the actual
clock and exact positive receipts. Canonical allocation/epoch fencing remains open.

A separate caller trace qualifies earlier wrapper risk notes: the two current
automation success callbacks clear diff/base/head before `UpdateTurnComplete`, so
they do not produce the hypothesized workspace wakeup. A diff-bearing tx-bound
wrapper remains a latent hazard; reachable failed-result and awaiting-input status
publication are separate owner-local paths, not evidence of that wakeup. The
attempt-owner follow-up below now defers those reachable status effects.

### 0a full-assessment scheduler settlement composition validation

The expanded focused race selection passes locally on PostgreSQL 17.10:
11 roots and 178 subtests, no failures or skips. Reproduce with an owned
PostgreSQL 15+ `TEST_DATABASE_URL` and `go test -race -p=1 ./internal/db
./internal/worker ./internal/services/codereview
-run '^TestCodeReviewSchedule.*Persistence|^TestAssessmentSettlement(FullSchemaPostgres|LegacyPublicationFullSchemaPostgres)$|^TestRecheckReconciliationSchedulingOwnerFullSchemaPostgres$|^TestFullAssessmentSettlementUsesSuppliedTransaction$|^TestFullAssessmentCompletionWithRetainedWorkspacePostgres$|^TestConfirmedFullPublicationCompletionAtomicPostgres$|^TestCodeReviewSchedulingLifecyclePostgres$/^force_fresh'
-count=1 -parallel=1 -timeout=3m`. Focused vet, pinned lint and whitespace
checks pass. Counts include retained caller/owner and force-fresh regressions,
not only the new persistence tests.

The new supplied-transaction seam rejects nil and typed-nil transactions before
I/O. Shared SQL preserves PR advisory locking, tenant/repository ownership,
schedule creation/row locking, callback validation and final scheduler update.
The policy keeps newer active owners, pending intent and wake identity; it settles
only the original assessment's matching requests. A one-connection owner rejects
nested Begin. Exact whole-graph assertions cover genuinely staged earlier
completion writes, outer commit/rollback, replay and a fixture-only final
scheduling-write failure. The standalone enabled publisher observes already
committed rows through an independent reader and emits nothing on failure.
An injected error before physical commit proves rejection/rollback, not a lost
network acknowledgement or unknown-commit recovery protocol.

Both actual worker call sites are pinned to the supplied-tx seam. Earlier
intentionally separate publication preparation remains separate; no whole-function
atomicity, new event, canonical shared authority or deployed floor is claimed.
Registering the shared PR/policy vocabulary also exposes retained indirect
callers; these remain explicitly syntax-only observations rather than newly
approved mutation authority.

### 0a automation attempt post-commit effects validation

The original slice's focused race selection passed on PostgreSQL 17.10: four
roots and 25 subtests, no failures or skips. Original full-package verification
passed 74 roots and 186 subtests. Reproduce with an owned PostgreSQL 15+
`TEST_DATABASE_URL` and
`go test -race -p=1 ./internal/services/automations
-run '^TestTurnAttemptPersistence' -count=1 -timeout=3m`. Repeated
`GOMAXPROCS=4` verification at `-parallel=8 -count=3` passed 12 roots and 75
case executions with no skips or failures. Focused vet and whitespace pass.

The actual pool-backed `EndAttempt` uses one physical transaction and rejects
nested Begin in its head-only, empty/full diff and status paths. Enabled Redis
command hooks observe exactly the entire committed session, marker, duration,
changeset and snapshot graph through a pre-opened independent connection.
Before commit, both the publisher and independent row view remain unchanged.
Real duration SQL and deferred-commit trigger errors first validate genuine
prior writes, then reject the whole unit with no notification. Unit commit-error
coverage checks discard/no retry; it is not lost-network-acknowledgement recovery.

The original publisher remains usable after the fresh transaction-bound copy's
streams are cleared. Each captured status keeps its exact original UPDATE
RETURNING record, including its pre-snapshot revision, and preserves
status/terminal expiry/workspace order. Snapshot/container row-only callbacks
remain inherited. A bounded syntax test pins the real callback method set and
pool-backed constructor; a new publishing or escaped callback method requires
explicit review. This guard does not resolve arbitrary interfaces or prove
canonical authority. The separate checkpoint owner is converted below.

### 0a scheduled review creation composition validation

Focused race verification on PostgreSQL 17.10 passes six roots and 23 subtests,
including the retained scheduled-generation root. The five new roots contain
13 subtests. Reproduce with an owned PostgreSQL 15+ `TEST_DATABASE_URL` and
`go test -race -p=1 ./internal/services/codereview -run '^TestScheduledSession'
-count=1 -timeout=3m`. With `GOMAXPROCS=4`, repeated default parallelism
(`-count=3`) passes 18 roots/69 case executions; `-parallel=8 -count=5`
passes 30 roots/115 cases, with no failures or skips. Twelve retained standalone
service roots and 47 subtests also pass using `-run '^TestService_'`.
Focused vet, pinned lint and whitespace checks pass.

The actual `ReconcileSchedule` callback runs with its owner's only connection
occupied and rejects nested Begin. A dedicated physical observer is opened
before the owner, avoiding setup-pool acquisition inside transaction hooks.
Twelve-table snapshots prove independent invisibility, exact staged-graph commit
and complete rollback. A fixture-only final scheduler trigger first validates
the genuinely inserted session, read-only primary thread, primary changeset,
review metadata, controller job and satisfied request, then raises an error.
The injected pre-commit error is not network-unknown-commit recovery proof.

Unit tests preserve every session default and returned model field, creation
errors, tenant arguments, nil/typed-nil rejection and inherited nil-publisher
behavior. Source tests pin the private adapter's single override and exact
transaction-scoped binding. The syntax scanner now recognizes explicit embedded
`SessionStore` creation receivers without globally selecting unrelated `Create`
methods; the current root is explicitly inventoried, not lifecycle authority.

### 0a automation checkpoint post-commit effects validation

The original slice's repeated race verification passed on PostgreSQL 17.10 using
an owned PostgreSQL 15+ `TEST_DATABASE_URL`. With `GOMAXPROCS=4`, run `go test -race -p=1
./internal/db ./internal/services/automations
-run '^TestCheckpointPersistence|^TestTurnAttemptPersistence' -count=3 -timeout=3m`:
30 root executions and 255 subtest executions passed, with no failures or skips.
The six new checkpoint roots alone, selected with `-run '^TestCheckpointPersistence'
-parallel=8 -count=5`, passed 30 roots/300 cases. Focused vet and whitespace passed.

Unit tests compare exact SQL/arguments, errors, UTC conversion, checkpoint kinds,
nullable provenance and original effect payloads in both standalone and supplied
transaction modes. Nil/typed-nil, invalid key/generation, no row, collection
failure, tenant refusal and optional notification failure remain explicit.
Blank private effect reasons preserve all previous diff-snapshot notifications.

The original seventeen full-schema cases exercise the real automation owner,
direct seam and standalone wrapper with an occupied one-connection owner and
nested-Begin trap.
A pre-opened physical observer verifies exact session/generation/run/target/thread/
job invisibility and settlement. Genuine late provenance SQL and deferred-commit
errors first validate earlier writes, then reject the complete owner without an
event. Lease, tenant, generation and historical retired-generation cases preserve
existing policy. Both the supplied-tx rollback and standalone autocommit forms of
the missing-provenance false edge are tested; false is not proof of no writes.

The actual owner call sequence is source-pinned through successful commit before
effect publication. A commit error discards effects and is never retried; unit
error injection does not establish whether a lost network acknowledgement left
durable writes. Resolving such uncertainty still needs the later receipt protocol.
Legacy wrappers invoked through arbitrary tx-bound stores remain outside this
owner-specific physical-commit guarantee.

### 0a recheck publication settlement composition validation

The two new worker race roots contain ten subtests. With an owned PostgreSQL
15+ `TEST_DATABASE_URL` and `GOMAXPROCS=4`, run `go test -race -p=1
./internal/worker -run '^TestRecheckPublicationSettlement' -count=3 -timeout=3m`:
six root executions and 30 subtest executions pass. Repeating with
`-parallel=8 -count=5` passes ten roots/50 cases, with no failures or skips.
Focused worker vet, pinned lint and whitespace pass.

The real `RunWithGitHubPublicationLock` owner occupies its pool's only connection
and rejects nested Begin. A pre-opened independent physical observer compares
all columns across the session, thread, review metadata/results, assessment,
request, scheduler and job graph before commit and after rollback/settlement.
A final status-comment enqueue trigger first validates genuinely staged receipt,
assessment completion, request resolution and scheduler pointers, then raises a
real PostgreSQL error. The entire callback rolls back without losing the prior
receipt. Confirmed and locally reconciled receipts, lost supervisor lease,
absence of a status-comment updater and exact committed replay are covered.
Local fakes prove these cases submit no external review or comment.

The original unavailable-transaction error remains, now requiring a concrete
`pgx.Tx` rather than another transaction starter. Source tests pin the exact same
transaction and complete/settle/optional-enqueue order, and reject legacy owners,
replacement transactions and known wrapper-helper fallbacks. An injected error
before physical commit is not unknown network-commit recovery proof. Earlier
separate reservation/send-intent work and external receipt handling are unchanged;
future shared lifecycle projection still needs exact canonical review authority,
allocation/current epochs, compatible locks and a deployed floor.

### 0a final proof follow-ups

The automation proof selection now includes five attempt-persistence roots and
33 subtests, plus three checkpoint roots and 30 subtests. With an owned
PostgreSQL 15+ `TEST_DATABASE_URL` and `GOMAXPROCS=4`, reproduce using
`go test -race -p=1 ./internal/services/automations
-run '^TestTurnAttemptPersistence|^TestCheckpointPersistence' -count=1 -timeout=3m`.
On PostgreSQL 17.10, all eight roots and 63 subtests pass; repeating with
`-parallel=8 -count=3` passes 24 root executions and 189 subtest executions,
with no failures or skips. Focused vet and lifecycle inventory guards pass.

Attempt marker, callback and commit rollback inequalities require a typed,
nonnil staged graph rather than allowing a missing observation to pass.
Checkpoint staged inequalities use the same explicit capture checks. The real
owner's failed and successful callback persistence paths preserve a nonnil
cleared result's model, summary, usage and failure error; cancellation separately
verifies nil-result normalization. A bounded AST test pins the calls and
selectors of both inherited row-only session methods,
with direct, indirect, stream-reference and alias regression fixtures. It is
not whole-program effect analysis or a new mutation-authority classification.

The eighteenth checkpoint full-schema case calls the supplied-transaction seam
with a revoked job token without first invoking the owner's attempt lock. Its
original SQL fence returns false, no effects and an exactly unchanged staged
graph; the caller explicitly rolls back. The standalone checkpoint comment now
states the already-characterized missing-provenance false-with-mutation edge.
These are proof and documentation changes only, not runtime-policy changes.

### 0a passive epoch representation validation

`go test ./cmd/migrate ./internal/db -run
'TestSessionLifecycleEpochMigrationPostgres|TestLifecycleEpochStore' -count=1`
with a disposable PostgreSQL 15+ `TEST_DATABASE_URL` exercises the complete
migration chain. On PostgreSQL 17.10, both fresh-schema and populated-upgrade
fixtures pass the 321 → 322 → 323 upgrade and 323 → 322 → 321 rollback/reapply
boundaries with exact legacy row preservation. The intermediate 322 boundary has
exactly three session columns and no thread columns; rolling back threads leaves
session metadata intact. Each migration changes only one hot table. The actual
session and thread stores create and update rows while omitting the new columns;
the new accessors preserve nullable end epochs and large bigint values and reject
another tenant or parent. Existing model projections and serialized API fields
remain unchanged. Twelve parallel mock cases additionally cover zero versus absent
end epochs, missing rows, database errors and late scan failures. Tenancy and
lifecycle inventory guards pass. The complete `internal/db` and `cmd/migrate`
packages also pass against the same disposable server with `GOMAXPROCS=4` and
`-p=1 -parallel=4`. These proofs establish passive representation,
not canonical authority, compatibility-floor enforcement or production activation.

### 0a passive request epoch representation validation

`go test ./cmd/migrate ./internal/db -run
'TestSessionHumanInputEpochMigrationPostgres|TestLifecycleEpochStore_GetRequest'
-count=1` with a disposable PostgreSQL 15+ `TEST_DATABASE_URL` exercises the
complete migration chain through request metadata. PostgreSQL 17.10 fixtures
cover fresh schema and populated 323 → 324 → 323 → 324 boundaries, exact legacy
row preservation and nullable bigint columns without defaults. Both threaded and
threadless legacy requests retain NULL admission epochs, including newly created
requests whose parent epochs are already nonzero. Each missing epoch remains
unknown independently; neither a current parent value nor zero fills it in.

The passive accessor matches the existing request store's tenant/session/request
scope without status, expiry or parent-visibility filtering. It does not lock or
mutate rows. Real driver tests distinguish NULL, present zero and large bigint
values while retaining complete legacy model and serialized API projections.
Actual legacy answer/cancel transitions preserve populated metadata; expired and
nonpending requests remain observable even with archived/deleted parents, matching
the existing request lookup rather than changing its visibility policy.
Eight parallel mock cases additionally preserve underlying query/no-row errors
and discard partially scanned metadata on failure. This slice adds no producer
proof, classification, readiness enforcement, dispatch binding, epoch mutation,
runtime consumer, writer floor or activation authority.

### 0a passive request readiness representation validation

`go test ./cmd/migrate ./internal/db ./internal/models -run
'TestSessionHumanInputReadinessMigrationPostgres|TestSessionHumanInputRequestStore_GetLifecycleReadiness|TestHumanInputReadinessState_Validate'
-count=1` with a disposable PostgreSQL 15+ `TEST_DATABASE_URL` covers the complete
migration chain through readiness metadata. PostgreSQL 17.10 fixtures exercise
fresh schema and populated 324 → 325 → 324 → 325 boundaries, preserving every
legacy field and both existing admission epochs. The seven new columns are
nullable and have no defaults or backfill. Existing threaded/threadless creation
leaves all seven unknown, even with populated epochs and an expired request.

The DB-only reader returns independently nullable legacy/version/state/deadline
fields and raw JSON without classification or eligibility decisions. SQL NULL
stays distinct from explicit false, zero and JSON null. The typed state helper
validates the four known labels; the observational reader deliberately preserves
unknown stored labels rather than asserting their validity. Real legacy answer
and cancel, nonpending/expired requests and archived/deleted parents retain their
existing model, API and lookup behavior. Seventeen parallel reader cases and eight
parallel enum cases cover exact scope, raw values, errors and late-scan cleanup.

No readiness proof author, immutability or shape enforcement, active classifier,
deadline policy, due index, producer/recovery association, answer/start consumer,
notice, compatibility guard or activation is introduced. Future canonical writers
must validate those contracts atomically; a stored label or opaque JSON is not
readiness proof. This migration deliberately remains a catalog-only additive
prerequisite rather than scanning the hot table to enforce a policy not yet wired.

### 0a passive execution metadata validation

`go test ./cmd/migrate ./internal/db -run
'TestSessionExecutionEpoch|TestExecutionEpochMetadata_Reads|TestThreadRuntimeStore_GetLifecycleMetadata_DrainProof'
-count=1` with a disposable PostgreSQL 15+ `TEST_DATABASE_URL` exercises the real
schema and stores. PostgreSQL 17.10 fixtures cover fresh schema and independent
325 → 326 → 327 → 328 upgrades, reverse rollback and reapply. Each migration
alters only its own hot table with nullable, default-free columns: three runtime
fields, two executor fields and two delivery-batch fields. Full legacy row graphs
and API projections, including existing writer and request metadata, survive.

The three DB-only reads require exact tenant/session/lane/resource scope without
locks, active-status/lease filters or inference. An absent executor lane matches
only SQL NULL, never every lane or the zero UUID. Real driver round trips preserve
independent NULL, zero and signed bigint values; raw runtime JSON distinguishes
SQL NULL, JSON null and opaque future values. Thirty-eight parallel mock cases
also cover exact query arguments, underlying errors and complete scan-failure
cleanup.

Eighteen isolated full-schema cases exercise actual runtime allocation, live
transition, heartbeat, delivery advancement, terminal close and expiry reclaim;
threaded/unthreaded executor allocation, heartbeat, drain, terminal and reclaim;
and real message/inbox seed, acknowledgement, batch start and abandonment paths.
Exact typed checks preserve large epoch values and ensure absent drain proof
remains SQL NULL after terminal or lease-loss observations. Populated metadata
also survives unchanged. Batch comparisons normalize only the preexisting UTC
versus driver-local timestamp representation when comparing logical values;
no response formatting or production timeout policy changes.

These are representation and compatibility proofs, not immutable allocation,
dispatch binding, physical process drain, launch permission, historical settlement,
writer-floor or activation proof. No metadata author, runtime consumer, public
model field or lifecycle authority is added.

### 0a scoped referenced-key prerequisite validation

`go test ./cmd/migrate -run 'TestSessionLifecycleReference' -count=1` with a
disposable PostgreSQL 15+ `TEST_DATABASE_URL` exercises the real migration driver.
PostgreSQL 17.10 fixtures cover fresh schema, populated 328 → 329 → 330 → 331,
reverse rollback and reapply. Each migration contains one standalone
`CREATE UNIQUE INDEX CONCURRENTLY`; each rollback contains one standalone
`DROP INDEX CONCURRENTLY`. No transaction wrapper, conditional name masking or
cascading drop is added. The keys are `(org_id, session_id, id)` for threads and
human-input requests, and `(org_id, session_id, thread_id, id)` for runtimes.

Catalog checks verify exact ordered columns, uniqueness, validity, readiness,
access method and the absence of predicates, expressions or included columns.
Every preexisting parent index, complete legacy row graph, previously installed
passive metadata field and existing API projection survives. Exact JSON bytes
preserve adjacent large bigint values without floating-point normalization.
Real unchanged thread, request and runtime creation remains accepted, with new
passive metadata still unknown.

Twenty-four synthetic PostgreSQL cases cover valid and rejected tenant, session,
lane and resource references, explicit optional-runtime null handling, and the
limits of these indexes. A request's tenant/session/id FK does not enforce its
nullable lane or validate a job's JSON session route. Canonical writers must
still check both under their eventual lock/fence contract. A runtime reference
using `MATCH SIMPLE` needs an explicit nonnull lane when the runtime is present;
otherwise a NULL lane skips its composite FK. No implicit primary lane is proved.
These child tables exist only in owned disposable test databases, not migrations.

For each new index name, a failed concurrent unique build on a separate synthetic
duplicate table leaves a real invalid conflicting index. The actual migration
then fails visibly, retains its dirty marker and invalid index, and is refused
by the existing automatic-repair allowlist. This proves name-conflict handling,
not interruption during a build on the intended parent. Separate direct drop
probes preserve valid indexes and dependent FKs when ordinary or cascading
concurrent drops are refused; they do not claim a clean migration-ledger outcome
after a failed down migration. Policy tests recognize both ordinary and unique
concurrent-index syntax, enforce the six explicit single-statement exceptions,
and reject automatic repair of versions 329–331.

These index-only prerequisites add no production FK, consumer, metadata author,
retention change, writer guard or activation. Concurrent builds can still wait on
older transactions; local small-fixture timings do not prove production timing.
Dependent FKs must be removed before these indexes can be rolled back. Future
children also need lookup indexes and evidence-retention protection before their
first active writer; no cascading evidence deletion is authorized here.

### 0a passive dispatch identity foundation validation

`go test ./cmd/migrate -run 'TestSessionInputDispatch' -count=1` with a disposable
PostgreSQL 15+ `TEST_DATABASE_URL` exercises the real schema. Migrations 332–335
introduce only the identity/ingress subset of `session_input_dispatches`, not its
complete consumer-binding protocol. The first migration creates the empty table,
organization FK, structural checks and lookup/identity indexes. Each subsequent
migration installs one tenant/session-scoped parent FK, for session, optional lane
or optional request. Their separate transactions use the five-second local lock
timeout and never hold several existing hot-parent locks at once. No production
code inserts, reads, normalizes or scans these rows.

The representation stores six input kinds, exact ingress identity/digest, opaque
JSON, independently nullable nonnegative epochs and an explicit normalization
version without a default. A thread epoch requires an explicit lane. Answer and
denial identities require a request. These are structural checks, not validation,
consent, readiness or authority; JSON null and future payload shapes remain raw
stored values. No zero/current-parent epoch or inferred primary lane is supplied.
Allowing an unknown session epoch deliberately relaxes the eventual contract's
required field at this empty identity-only boundary. Before the first production
producer, tighten that requirement with the consumer/binding protocol; unknown
provenance must never become a current-epoch fallback or runnable binding.
Uniqueness is tenant/ingress-kind/key and tenant/nonnull-message identity, not
content digest or request alone. The tenant/session/id key can support future
parent-scoped references, without installing any claim pointer. Lane/request
lookup indexes support the new parent FKs.

PostgreSQL fixtures cover fresh schema, populated 331 → 332 → 333 → 334 → 335,
independent rollback and reapply, exact catalog shape, clean ledgers and an empty
dispatch table at schema boundaries. They preserve complete prior row graphs,
API projections and indexes, including the preceding referenced-key prerequisites.
Separate synthetic rows exercise exact scoped references, structural rejection,
ingress/message uniqueness and nullable/large-value round trips. Parent-delete
and dependent-reference probes retain evidence rather than cascading it away.
Existing session soft deletion remains unchanged; the physical-delete refusal
probe opts into the existing hard-delete setting only inside an owned disposable
test transaction and rolls it back, including all session and dispatch fields.
Direct DDL refusal probes do not claim the ledger stays clean after a failed
migration rollback.

The request FK proves tenant/session/request membership, not null-safe lane
equality. The partitioned message table lacks a matching tenant-scoped unique
parent key, so `message_id` has no FK; its child uniqueness does not prove message
existence, tenant/session/lane ownership or inbox association. Canonical writers
must establish these facts atomically before any row can become bound.

Admission/consumer states, job/runtime bindings, claim tokens, due scans, parked
control, conditional bound-request uniqueness and all producers/consumers remain
deferred together. No new job/runtime retention dependency is installed. Before
the first production writer, complete the binding checks, exact FK/retention and
hard-delete policy, canonical authority and activation gates. Rollback removes
only these new schema objects without CASCADE; future readers/writers and any
dependent references must be retired first. These local proofs do not establish
hosted CI, production lock timing, compatible floor or activation.
Dropping the parent FKs during rollback can take stronger, read-blocking locks
than adding them; the local timeout bounds acquisition, not production hold time.
Inspect actual `pg_constraint.conindid` dependencies before retiring an equivalent
parent key rather than assuming which matching unique index PostgreSQL selected.

### 0a private session/lane lock-kernel validation

`go test ./internal/db -run 'LifecycleScope|LifecycleTransaction' -count=1` with
a disposable PostgreSQL 15+ `TEST_DATABASE_URL` exercises the new private kernel
alongside the existing transaction runner. The kernel locks the exact tenant
session first, then one UUID-ordered batch of explicitly selected lanes using
`FOR NO KEY UPDATE NOWAIT`. It clones, deduplicates and sorts inputs without
mutating their backing array, issues no queries inside loops, rejects an incomplete
or foreign-parent selection, and returns no partial snapshot on any error.
An empty lane list locks only the session; it never discovers a primary lane.

The read-only policy prerequisite shares the existing configuration constants.
It requires effective READ COMMITTED, the existing writer marker and timeouts,
and a live caller deadline with at most the lifecycle unit budget remaining.
It does not configure, downgrade or reset anything. Existing configurator behavior
and emitted SQL stay unchanged. Effective settings cannot establish that the
configurator ran or that settings were transaction-local; remaining time cannot
prove transaction age. The owner must establish the whole budget before acquiring
the transaction. Neither observation establishes a writer floor or activation.

Full-schema PostgreSQL cases preserve exact raw statuses, visibility markers,
nullable end epochs and integer extremes, including hidden and terminal rows.
Policy failures acquire no session/thread relation lock and leave effective
settings and data unchanged. Deterministic barriers delegate the actual production
lane SQL unchanged: independent connection probes see the parent locked while
lanes are still free, then verify exact selected/unselected scope. Real NOWAIT
failures return no snapshot; only the owner rolls back staged writes. A real runner
retry reaches a fresh transaction after confirmed abort, observes no first-attempt
effect and commits only the second attempt. No sleep-based ordering is assumed.
Mock regressions additionally fix the literal SQL contract and cover query, scan,
iteration, membership and synchronous cancellation failures with zero results.

Both private prerequisites and their direct callers are explicit syntax-inventory
boundaries, without promotion to mutation authority. Locked observations are not
permission objects: no domain/reservation/expected-epoch/receipt eligibility is
decided here, and there are no production callers or lifecycle writes. Future
canonical primitives must compose mandatory checks and mutation together using
this implementation. Cutover interlocks, higher-rank discovery/restarts, stronger
end/reset child exclusion and mixed-version lock-graph progress remain open.
These tests do not prove historical deadlock freedom or production timing.

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
