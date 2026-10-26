# Session lifecycle 0a: compatibility implementation and activation

Status: draft delivery re-baseline. Remaining packages below are unimplemented
unless their progress item explicitly says otherwise. This document does not
authorize implementation, publication, deployment or activation.

Source baseline: `0323c7e8e7b71149cd85f23add4fc136edd79467`.
Recheck source and the executable caller inventory before each implementation
increment. Source inspection establishes neither current CI nor deployment health.

## Purpose and document ownership

Finish the compatibility prerequisites for consolidating session lifecycle policy
without leaving a second admission engine or allowing an older writer to bypass
the new fences. The work is a compatibility program with several implementation
packages and a separate operational acceptance gate.

[The ownership plan](session-lifecycle-ownership.md) owns the overall outcome and
M1–M6. [The M0 contract](session-lifecycle-m0-contract.md) owns identities, policy,
lock order, typed outcomes and safety requirements. This document owns 0a delivery
order, package boundaries and completion evidence. Its summaries do not replace
the contract. Any discovered contract conflict requires an explicit design
decision before implementation; package completion cannot waive a safety gate.

M0 characterization is complete. Completed passive 0a foundations are retained
and are not new work. A–G implement runtime compatibility; H implements the
activation controller and owns release assurance and authorized activation.
Packages are outcomes, not one-PR promises
or calendar estimates. Each lists bounded increments so implementation can stop
at a reviewable boundary without falsely claiming the package or floor complete.

## Source-grounded baseline

At this baseline, `cmd/lint-lifecycle/inventory.json` has 664 entries: 608 marked
`unfenced-needs-conversion`, comprising 255 reviewed roots with canonical targets
and 353 syntax-only candidate rows requiring tracing. The other classifications
are 29 fenced specialists, 17 non-lifecycle candidates and 10 lifecycle-owned
entries. `job-status-inventory.json` has 123 sites. These are source-inventory
counts, not 608 independent writers or a PR estimate; lexical classifications
do not prove that a caller is already compatible.

| Foundation | Source anchors | What remains |
| --- | --- | --- |
| Bounded READ COMMITTED owner and supplied-transaction configuration | `internal/db/session_lifecycle_transaction.go`: `RunLifecycleTransaction`, `ConfigureLifecycleTransactionInTx` | No production caller uses either entry point outside the runner itself. Real-caller conversion and both rolling-version proofs remain. |
| Explicit composable persistence | Existing result/status/phase, seed, grant, review, automation and publication `InTx` seams recorded in the parent plan | Persistence composition does not establish canonical admission, exact current authority or activation. |
| Passive role/build/version observations and connection census | `internal/cluster/node.go`, `internal/db/lifecycle_writer_instance_store.go`, `internal/db/session_executor_store.go`, `cmd/lifecycle-census` | Advertised writer version is 0. Observations do not prove compatible consumers, retired processes or drained transactions. |
| Session/thread/request and execution metadata | `internal/db/session_lifecycle_epochs.go`, `internal/db/session_execution_epochs.go`; migrations 322–328 | Raw nullable metadata remains observation, not eligibility. Production writers do not acquire authority by reading it. |
| Scoped reference-key prerequisites | Migrations 329–331 | Composite reference keys exist; their existence is not tenant/attempt/consumer authorization. |
| Empty dispatch identity and scoped references | Migrations 332–335, 341–344 | No production dispatch producer, durable consumer binding, waiting reconciler or claim mutation exists. Admission/control fields in contract §2 remain proposed. |
| Empty cutover record, interlocks and actual-conflict retry | `internal/db/session_lifecycle_cutover.go`; migrations 336–337; runner's private actual-busy exception | Shared/exclusive try-lock and fresh observation exist, with no production caller or activation owner. Absence of a row is not proof that an active session may revert to version 0. |
| Raw pending-workspace compatibility | `internal/db/session_input_workspace_compatibility.go`: `GetWorkspaceCompatibility`; migrations 338–340 | No final readiness publication, certificate-transition enforcement or recovery action exists. |
| Private exact-scope locking and claim-receipt comparison | `internal/db/session_lifecycle_fences.go`, `internal/db/session_lifecycle_lane_claim.go` | No production callers or exported authority. Exact receipt equality is not admission, drain, start, release or current-turn eligibility. End/reset needs separately proved stronger exclusion. |
| Partial composition and unresolved legacy overlap | `internal/db/agent_capabilities.go`, `internal/services/humaninput/db_repository.go`, existing result/status wrappers and specialist stores | Answer/grant composition holds session FOR UPDATE through outer commit but retains request/parent and session/run inversions. Some tx-bound wrappers still publish before outer commit. Missing splits belong to B.1 (EndInterruptedAttempt and recheck settlement/cancel), A.5 (QueueContinuation and recheck Dispatch) and B.2 (recheck Claim), each retaining the existing domain transaction owner. |

The canonical mutation family, exact-target `VerifyResumable`, durable accepted-
undelivered control, `RecoverPendingInput`, minimum-writer guards and activation
controller are absent from this baseline. Existing entry points still own their
legacy policy, including latest-pending answer selection. Existing synthetic M0
probes are specifications and characterization, not proof of these missing paths.

## Completion levels and authority

1. **Local package complete:** its canonical seams and named real callers work
   together on the fully migrated schema, focused regressions and race tests pass,
   required provider/storage evidence is present, and independent review approves
   that package. An unresolved conformance gap is code-only progress, not package
   completion. No production guarantee follows.
2. **Publication/CI complete, per increment:** separately authorized, scrubbed publication has
   exact-head required CI success. Local tests or an approved plan do not satisfy
   this level. No publication is authorized by this re-baseline task.
   Dark increments may be published independently once their own characterization,
   adjacent-build proof, review and CI gates pass; they need not wait for all A–G
   or H. No dark landing establishes a fleet floor or enables version-1 behavior.
3. **Compatibility release established:** H's local/release proof passes and an
   explicitly authorized release has reached every relevant writer and consumer,
   with positive natural retirement/drain evidence. A lease expiry, version label,
   image build or empty census alone does not establish the floor.
4. **Cohort active:** separately authorized activation gives every request in the
   enumerated session/provider cohort its atomic enforced disposition, with usable
   recovery and compatible consumers. Version-0 sessions outside the cohort remain
   visibly deferred. This is partial coverage, not repository-wide completion.

Call 0a complete only for the stated cohort and achieved level. Repository-wide
0a acceptance requires the full inventoried scope and no unbounded active version-0
tail. The implementation ledger must never combine these levels into one checked
item. M1's answer-enabled completion requires established 0a and backlog activation
for the same cohort; a passive extraction before then remains preparation.

## Implementation order and integration joins

Common rules for every increment:

- Version-1 behavior is unreachable in production until the real activation owner
  authorizes the exact session. Local tests use one shared activated-session fixture
  whose invariants the eventual activation transaction must produce. A hand-edited
  cutover row is never an operational activation procedure.
- Preserve characterized version-0 policy. Any change to a live writer's lock order
  or transaction overlap needs an adjacent-build coexistence proof at that increment,
  in addition to H's assembled pre-floor/floor and floor/new proofs. Keeping legacy
  lock order until activation is an unproved alternative, not the selected contract.
  Each family's increment converts its version-0 path to the selected compatible
  order without combining its old transaction units into larger overlaps. Keep
  those old compatible boundaries until the established floor. Larger transaction
  activation and any reliance on new fences require completed 0a for that scope.
- The existing runner already sets writer version 1. Adopt it only with the caller's
  complete canonical active-version branch and locked mode dispatch. A version marker
  is a compatibility backstop, not authority; a marked legacy writer must never mutate
  an active scope. Binary-wide version advertisement waits for full coverage.
- Each transaction of a legacy multi-transaction sequence re-observes cutover state
  under the interlock. A later enqueue cannot cross activation as unbound legacy
  work; it must resolve its exact receipt or return the defined visible disposition.
  No current epoch, primary lane or new consumer may be guessed during repair.
- Keep exactly one responsible package for each inventory root. Trace indirect
  syntax rows to real callees; inventory classification alone is not semantic proof.
  Every increment updates its owned inventory/call-through evidence, not only G.
- Preserve strict transaction composition and post-outer-commit effects. Unknown
  commit is resolved through exact receipts, never replay. Existing-entry tests stay
  alongside the new path's full-schema tests with publishers enabled.
- Cutover/version evidence is monotonic once active. Missing, deleted or malformed
  active provenance fails closed; rollback cannot drop cutover rows, guard stamps or
  receipts and thereby route an active session to legacy semantics.

| Package | Inputs and dependency | Local output |
| --- | --- | --- |
| A. Admission and durable consumer ownership | A.0 freezes identity/authority with B; G.1 protocol core precedes routing. Answer branches consume C.2 and E's controls; whole-root exit also needs F's reachable recovery | Real ordinary, answer and specialist admissions using one canonical DB mutation family |
| B. Execution boundaries, settlement and end | Core consumes A's immutable bindings/authority shapes; whole-root closure waits for C.1w, D.2, E.1/E.2 and F.1/F.2 recovery composition | Start/seed/live/ack, own-attempt settlement, successor-safe projection, end/ownership/topology fences |
| C. Request readiness and workspace evidence | C.0 freezes joint interfaces; C.1p consumes B's settlement core and D.1r's receipt, then C.1w/v compose it. C.2 needs E.1's actual verification receipt; effective outcomes also need E/F actions | Real proof publication, current certificates and typed readiness policy |
| D. Snapshot publication and exact-key cleanup | D.1r consumes C.0/A.0/A.2; D.1c consumes C.1p/w. D.2 consumes C.1's transitions; whole cleanup exit verifies E/F preparation, parked and physical-operation references | Every Save has fresh-key responsibility; publication/expiry/reference/cleanup races are fenced |
| E. Exact-target launch and durable start control | E.1 consumes B.2 preparation and C/D descriptor/availability primitives; E.2 consumes A.2 status policy and B.3c supersession. Actual caller exits join B and E, not a second start owner | Tested provider/build resumability, final launch authorization and durable retry/abandon control |
| F. Reachable recovery and legacy resolution | F.1 owns the shared multi-mode recovery primitive, composed by B.2 continuation and F.2 failed-only adapters; pending action consumes C.2/E controls and F.2 physical reservation/proof | Pending-request recovery, distinct start controls, live retry-mode composition and normalization tested on disposable data |
| G. Compatibility guards and residual wiring | G.1 protocol core precedes the first router; later guards/claimant eligibility and G.2 constructors/residuals consume A–F contracts. G.0 audits their coverage rather than reconverting it | Compatible consumer routing and all authority-surface guards; no unassigned root |
| H. Activation controller, release assurance and authorized activation | Controller development consumes A–G interfaces; assembled assurance requires their local exits | Tested controller, both rolling transitions, performance/rollback proof; separate established-floor and cohort-active gates |

A.0 freezes A/B's identity, consumer-state, immutable-allocation, logical-claim and
settlement interfaces before A.1 or B.1. It has a reviewed schema/retention/fixture
exit, not an active production writer. Increments may then land dark independently
after separate publication authority and per-increment proof. A local stack is an
implementation convenience for a bounded integration join, never a requirement to
hold the entire program unpublished. No producer-only binding cutover.

The [A.0/C.0 freeze appendix](session-lifecycle-0a-contract-freeze.md) records the
shared representation, schema/retention installation ownership and retained
activation-provenance contract used by the first implementation sequence. It does
not replace any package's behavioral, review or release gate below.

C.0 owns the shared descriptor, pending-upload receipt, certificate-transition,
recovery-reservation/reference shapes, RetentionAuthority and exact-target
observation/ResumabilityReceipt interfaces.
D.1r implements only upload registration/responsibility and its receipt before
C.1p; it does not close Save/publication callers. C.1p consumes that receipt;
C.1w composes its descriptor transitions. Only then does D.1c convert Set/Promote/
Clear/SetBase and PR/branch/push callers through those C-owned operations. D.2
consumes C.1's certificate transitions for guarded expiry. E.1 supplies actual
verification receipts before C.2 revalidation; freezing a type is not provider
conformance. Each joined runtime effect remains inactive until H; there is no
permission to perform storage effects without its guards.
E and F close the answer/readiness/start cycle before any answer-enabled activation.
G's coverage and H's mixed-writer proof begin with the first changed caller; they
are not deferred cleanup after a behavioral switch.

Use increment outputs, not whole-package completion, as implementation inputs:

1. A.0 and C.0 freeze shared contracts, including recovery modes, reservation and
   receipt shapes. G.1's protocol core implements/tests missing-active-provenance
   handling against A.0's fixture before A.1 installs its router. Trace each
   affected caller before conversion. These exits need no later runtime effects.
2. Build A.2's consumer/status core before A.1's non-answer admission branch;
   then B.1p's shared settlement core and B.2's first-stage/defer/seed core.
   Build B.3's end/supersession, owner and topology primitives from the frozen
   authority shapes. G.1 supplies the positive job-kind registry core before
   D.1r wires exclusive cleanup-kind routing and exact upload registration using
   that registry and A.2's status contract. These are component exits, not whole-root
   completion for B's runtime, reaper, retry or owner adapters.
3. C.1p consumes B.1p and D.1r. C.1w/v compose its descriptor/certificate policy;
   D.1c then converts publication callers through C.1p/w, and D.2 adds guarded
   expiry. F.2's physical reservation/settlement primitives use those descriptors
   and B.3 authority; C/D consumers enforce the reservation shape frozen in step 1.
   D.3 implements reference-safe cleanup, with its full E/F-reference proof at the
   later integration join, not assumed from these components alone.
4. E.1 uses B.2 preparation plus C/D descriptors/availability for actual target
   verification, the same start primitive's final stage and every-invocation CAS.
   E.2 follows A.2's complete 123-site pre-park status-policy proof and composes
   B.3c supersession and that parking policy; E.3 adds control surfaces
   and provider/build conformance. None requires C.2's selected-request revalidation
   to produce a verification receipt for a prepared target.
5. C.2 now consumes C.1 policy and E.1's actual receipt. A.3 implements exact
   answer/denial/grant with these typed outcomes and E's durable start controls.
   A.4 session/review-comment/integration adapters compose A.1/A.3; A.5 specialist
   admissions also compose B.3d reservation and C.1w workspace primitives; A.6
   drain/replay/reconciliation composes A.2/A.3 and B.2's bound handoff. Each may
   finish a bounded non-answer branch earlier, but answer branches cannot close
   before this step. Effective recovery-required outcomes still await step 6.
6. F.1 implements the single typed multi-mode recovery operation and its pending-
   only public action, using C.2, E controls and F.2's physical proof/reservation.
   B.2 converts live continuation retry adapters and the shared continuation
   fragment consumed by B.1q reclaimers through the applicable mode; F.2 converts
   the distinct failed-only retry/start-over adapters. F.3
   implements/tests offline normalization on disposable data, not live retries.
7. Close every real caller branch across A–F against those shared components:
   B.1o/q/a/r settlement, B.2 starts/recheck Claim/retries, B.3 end/owner/topology,
   A's full admission roots and C/D's physical/reference competitors. Verify E/F
   reachable actions and all cleanup references. G completes guards/eligibility,
   constructors, residual wiring and coverage audits; H.0 implements/tests the
   activation controller. No package exit passes on component-only evidence.
8. H assembles all local exits for release assurance. Separately authorized
   operational normalization follows established floor/drain, then separately
   authorized cohort activation. Neither operation is an implementation input.

The [ownership/tracing appendix](session-lifecycle-0a-inventory.md) assigns each
reviewed root and each syntax candidate. A bounded branch increment may land dark
with the enclosing root explicitly **partial**: for example A.1's non-answer send
branch does not close the whole SendMessage root until A.3's answer call-through.
Likewise B.1o's reaper cannot close until D.2/C.1w cover expiry/availability.
Record scoped increment completion separately from whole-root closure; no package
or H exit passes with its roots partially converted or candidates still untraced.

### A. Admission and durable consumer ownership

Deliver a real transaction that owns eligibility, logical claim, exact message/
inbox/comment/dispatch persistence and the bound job. DB owns canonical mutations;
ordinary commands and retained specialist owners compose them through the supplied
transaction. Never copy a predicate or call a top-level owner from another owner.

Reviewable increments:

0. Freeze the A/B contract and schema/retention decision described above; use one
   activated-session fixture and later verify real activation produces its invariants.
   Specify missing-cutover handling: the existing observer's ErrNoRows cannot be
   a blanket version-0 fallback. Distinguish never-activated state from missing
   active provenance using the retained record/version contract and fail closed
   for the latter. A.0 specifies the rule; G.1 owns its early protocol-core
   implementation and deleted/malformed-provenance tests before any router.
1. After A.0/C.0, G.1's fail-closed protocol core and A.2's consumer/status core,
   implement one ordinary non-answer admission vertical and its strict scoped
   writer through `AdmitMessageInTx`. A.1 consumes the A.2-installed shared
   dispatch/consumer schema and consumer query; it owns no second schema or policy.
   Install production routing in `thread.Service.SendMessage` through locked
   cutover observation, the only version switch, covering idle/running/capacity.
   Version 0 uses characterized policy with the selected compatible order and old
   transaction boundaries; the complete canonical version-1 branch stays dark.
   Preserve its existing running, queue/capacity, source, actor and target semantics.
   Ordinary non-answer sibling admission checks scope, capacity and exclusive
   reservations, including session-wide/legacy consumers, any workspace recovery
   slot and unresolved whole-session physical work. It binds no physical target
   and requires no availability certificate at admission. B.2/E.1 must check the
   current descriptor and fresh exact-target evidence before every launch; missing
   availability can defer the bound job. A.3/C.2 retains exact answer readiness.
   Prove three independently bound sibling jobs, fourth-lane waiting, and no veto
   from a sibling's pending job or pending/unprovable request certificate.
2. A.2 has two review exits, not a second admission engine:

   - **Core, before A.1:** under A.0's frozen contract, solely install the shared
     dispatch/consumer schema, scoped consumer queries and competing-consumer
     predicate needed by the vertical. Include all passive dispatch/job/runtime
     consumer references and indexes those queries require; no hidden later B/E
     schema installer may block this exit. B/E retain allocation, settlement,
     preparation and launch semantics/writers. Own the 123-site classification and
     install/test the coupled awaiting_start_control schema/checks, replacement
     dedupe index, active lookup and enqueue-collision interpretation together.
     Distinguish runnable jobs from all nonterminal consumers, including parked
     controls and delivery-uncertain inbox entries. A.1 and E consume this core;
     neither installs parallel consumer fields, indexes or eligibility predicates.
   - **Completion, through real caller joins:** finish cross-key exclusion,
     durable waiting receipts/discovery and all 123 sites' call-through evidence
     as their owning families convert. The full 123-site status interpretation/
     exclusion proof, using synthetic parked controls rather than depending on E's
     writer, is a pre-park barrier before E.2 may create its first parked row.
     Remaining full consumer joins follow their family conversions, not that
     first-row dependency. Prove independent sibling-lane claims;
     reserve session-wide claims for execution conflicting with every lane.
     Parent running status derives from siblings, never a second logical claim.
     A.1's vertical may pass using the core, but A.2's whole exit requires this
     full evidence. E requires both the coupled core and the pre-park barrier;
     D's resource-job exclusion and G's kind registry consume its status policy.
3. Add exact `AdmitAnswerInTx`/denial/grant. Atomically bind only that request's
   intent and associated input; the approver is required and fails closed.
4. Convert session-send/review-comment admission and then Slack/Linear admission in
   separate increments. Preserve answer-only `AnswerQuestion`. Integration receipts
   retry external updates independently of successful admission.
5. Add typed review/automation/repair/feedback composition, preserving each domain
   reservation, current-head/generation check and outer-commit effects. Add explicit
   wrapper/InTx splits for recheck Dispatch and feedback continuation owners;
   B.2 alone owns the recheck Claim split and its start-side composition.
6. Convert runtime drain, exact queued answers, explicit inbox replay and the
   pending-admission reconciler. Prove discovery without a sibling/Redis wake.

Every conversion increment installs its actual production locked routing and
version-0 compatible-order conversion in the same increment as its version-1 seam;
none leaves a test-only adapter or an extra enable flag. Characterize and prove
that increment against the adjacent build before a dark landing.

Exit evidence: full-schema real-caller tests for exact replay, conflicting reuse,
cross-org/session/lane rejection, same-input/different-key consumer conflicts,
capacity wait, independent sibling progress, answer/grant rollback at every late
failure, dedupe collision, unknown commit resolution and once-only effects. Waiting
requests remain unanswered. No latest-request re-selection after active cutover.

Exclude provider execution, global rollout and broad service extraction. A's
local exit does not activate an admission producer ahead of B–H.

### B. Execution boundaries, settlement and end

Implement the canonical primitives and convert the actual executor, runtime and
failure paths that must honor them. Their policy may remain in existing adapters
until M1–M5; their durable fence cannot wait for those later extractions.

1. Bind every allocation to exact dispatch/job/attempt/epoch identity. Add own
   immutable terminal observations and `ProjectTurnInTx`,
   `ProjectAdmissionFailureInTx`, `SettleLostAttemptInTx`. Convert reaper, dead-letter,
   failed-start, compensation and specialist settlement without a stale shared
   result/status/diff/phase/holder overwrite. Parent projection stays sibling-aware.
   Review ordinary runtime/worker/executor/reaper settlement separately from the
   review/automation/publication/workspace-failure tranche. Each has its own real
   caller exit; neither defers successor protection to M5. Include a durable
   bound-terminal sweep for crash after terminal commit before callback. Late own-
   attempt output cannot enter successor seeds, task IDs, summaries or start-over.
   B.1q owns each full job/executor reclaim root and its contract §5 lock-order
   conversion: bounded unlocked candidate discovery, never reversed runtime-first
   batch locks, then revalidate each in a fresh ordered unit. Its whole-root exit composes B.2's
   shared continuation fragment and F.1's applicable recovery mode after step 6;
   settlement-only conversion cannot close a reclaim statement that still embeds
   the old payload rewrite. B.2 does not take ownership of these full reclaim roots.
2. Own the single `AuthorizeRuntimeStartInTx` entry point and sealed `StartResult`,
   its first-call preparation stage, strict `StartDeferred` and
   `DeferRuntimeStartInTx` nonconsuming durable retry. E extends this SAME operation's
   final verification/permit stage, never a parallel start predicate. The first
   stage permits no provider entry, running notification or final-ready result.
   Fence initial/continue/retry/handoff/standalone start, seed selection/attachment,
   live delivery claims and acknowledgements.
   Retain exact inbox delivery ambiguity; a lease expiry never proves physical drain.
   Own recheck Claim's wrapper/InTx split, live continuation retry adapters and
   only the shared continuation fragment used by B.1q's reclaim statements,
   including `continuationRecoveryQuery`, `InspectContinuationRecovery`
   and `ListAppliedRecoveryMessageIDs`. Preserve their characterized version-0
   prior-execution receipts; active call-through uses F.1's typed recovery mode
   only when recovery admission is required. Never use the legacy payload rewrite
   for a never-executed `StartDeferred`. Automatic interrupted-worker RecoverSession
   adapters belong here, not to failed-only F.2 or offline F.3. These live roots
   require the same locked routing and adjacent-build proof as other B.2 callers.
3. Add end, cancellation intent, owner/reservation change, topology and logical
   release primitives. Preserve idle-only session end, ordinary retained-owner
   rejection and stricter thread predecessor drain. End/reset uses stronger parent/
   lane exclusion against new FK children. Supersede only exact known-unlaunched
   controls; uncertain possible acceptance keeps its reconciliation responsibility.
   Give end/cancel, domain owner changes and topology/archive separate review exits
   with their contract §4 roots, not one package-wide unchecked caller bucket.

Exit: independent-connection barriers prove delayed start/end, stale reaper and
successor, seed/ack/end, owner/end and sibling completion races through real callers.
Own historical outcome may settle while current shared projection is rejected.
Pre-allocation failure cannot fabricate a provider result. Publication-enabled
outer transactions publish only after true commit, never savepoint release.

E supplies the external verification and every-invocation launch control; B alone
does not authorize provider entry. Physical runtime, provider and domain ownership
remain with their specialists.

### C. Request readiness and workspace evidence

Connect request creation to immutable exact producer/request/provider/turn/epoch
identity, then publish proof only after final pause/diff/checkpoint provenance.
Capability-created awaiting-input status is distinct from a durably resumable
provider pause. Implement contract §7's canonical policy, not a second simplified
query or a caller-supplied ready/drained boolean.

0. C.0 freezes descriptor/pending-upload/certificate/recovery-reservation/reference,
   RetentionAuthority and exact-target observation/ResumabilityReceipt interfaces
   without requiring proof publication, provider conformance or active storage effects.
1. C.1 implements proof publication and current descriptor/certificate transitions
   after B's final-pause settlement core and D.1r's upload receipt exist.
   Convert checkpoint, revision, changeset/materialization, preview, holder and
   publication-lease owners here, retaining their physical ownership contracts and
   recovery-reservation checks. Their routing/order proof belongs to this increment.
   Define physical-target generation separately from ordinary workspace revision
   progress before comparing readiness or start targets: the legacy
   `SessionStore.updateTurnCompleteRow` increments `workspace_generation` on every
   turn completion. That counter alone cannot imply target replacement or make
   every sibling completion invalidate a pending request's native context.
   C.1p owns the sole `AdvanceInputWorkspaceCompatibilityInTx` bounded set-based
   fanout and readiness-classification primitive; C.1w owns workspace/holder/
   materialization composition and C.1v owns preview composition. These are finite
   review tranches in the assignment appendix, each with its own real-caller matrix.
2. C.2 follows C.1 and E.1's actual exact-target verification; it implements
   selected revalidation through `RevalidateInputContextInTx`,
   including source-specific sibling/domain changes, availability loss and expiry.
   Wire typed ready/wait/context-pending/unavailable/unprovable outcomes to actual
   answer paths without making shadow classification effective early.

Exit: migrated PostgreSQL and real producer tests cover first observation versus
lost published observation, exact producer/drain provenance, final revision drift,
nullable legacy input, all-request fanout, sibling progress, descriptor changes,
answer/end races and independent discovery. Preserve fixed legacy/capacity deadlines
and the distinct post-activation live checkpoint-pending policy from contract §7.
Stored classification never substitutes for admission-time current revalidation.
Use the contract's provisional 128-request fanout cap and its explicit capacity
outcome; measure bounded set-based work rather than silently looping per request.

Recovery-required cannot become effective until F's action is reachable. E's
actual provider/build tests remain required even after the DB policy passes.

### D. Snapshot publication and exact-key cleanup

Keep existing Save/Load/Delete interfaces and storage owners. Add a fresh key and
publication/upload receipt for every Save attempt, including PR/branch/push async
uploads. Before Save, commit the exact typed `snapshot_object_cleanup` responsibility
and writer identity. Generic claimers must exclude that kind before its first job
is registered; SnapshotReaper alone owns it. No storage I/O occurs under DB locks.

1. Split D.1 into two separately reviewable increments:

   - **D.1r registration core:** consume C.0/A.0 and A.2's status contract to
     implement `RegisterSnapshotUploadInTx`, the exact receipt, cleanup-kind
     registration and exclusive routing through G.1's positive kind registry before
     any Save. It has no dependency on `PublishCheckpointInTx` and does not close
     existing publication roots.
   - **D.1c publication callers:** after C.1p/w exist, convert Save/SetPending/
     Promote/Clear/SetBase and PR/branch/push uploads, including stale async
     callbacks, through C.1p's single `PublishCheckpointInTx` and C.1w's single
     `MutateWorkspaceRevisionInTx`. All eleven reviewed D.1 roots and eight syntax
     candidates belong to this later caller tranche; D.1r alone closes none.
2. D.2 follows C.1 with current-reference expiry/archive/merged-PR cleanup and
   tombstone barriers for every reference creator, using the certificate transition.
3. D.3 implements bounded fair discovery and exact-key superseded/orphan deletion,
   writer settlement and retry/attention. The exclusive job-kind routing is part
   of D.1r before registration; its status interpretation uses A.2's policy.
   It also owns existing `cleanupReviewBundle` as the separately inventoried exact
   own-artifact workflow, composing B.1 settlement/D.1 fresh-key requirements.
   Preserve that workflow's reference/writer/unknown-I/O obligations without giving
   SnapshotReaper blanket authority over nonregistered domain artifacts.

Unknown Save/Delete outcomes keep durable same-key responsibility; terminal job
retention cannot erase an outstanding object operation.

Exit: file and S3-equivalent storage-contract tests plus actual publication and
cleanup call-throughs prove Save/Promote/new-publication, late Clear, expiry/
reference/tombstone and Delete races. All current/preparation/parked/physical-
operation source references and positive writer settlement participate. Time or
lost lease alone cannot authorize deletion. Exact deletion settles only after
success or observed idempotent not-found. Fresh keys cannot grow an undiscoverable
or unbounded unowned orphan stream.

Exclude a new object inventory/refcount/event system and optional provider-private
export bundles. C/D must pass their joined certificate/reference tests before
their local package exits.

### E. Exact-target launch and durable accepted-undelivered control

Complete B.2's two-stage preparation and final start authorization through the
same `AuthorizeRuntimeStartInTx` and sealed `StartResult`. The external
`VerifyResumable` check runs outside DB locks against the exact selected native
identity, workspace descriptor, provider and pinned build. A preparation alone
never permits running/started effects. Every actual Execute, including fallback
and credential retry invocations, needs a fresh fenced invocation CAS; preserve
write-once FIRST and possible-acceptance evidence across prior allocations.

E.1 implements exact-target verification and the existing operation's final stage,
then the every-invocation launch CAS. E.2 implements durable PARKED control on the
fixed dispatch/job and crash reconciliation. Control outlives execution
TTL and token/lease expiry. Add exact retry/abandon and once-only logical claim
debit refund. Compose B.3c's sole stale-control supersession operation and verify
A.2's queue/reaper/executor/retention exclusion policy through the actual caller
owners; E.2 implements neither a second supersession predicate nor status policy.
Retry keeps request/grant/audit/binding/native identity intact; it
cannot reanswer, regrant or reconstruct history as a new conversation.
Consume A.2's completed status/check/index/active-lookup/collision contract before
creating the first parked row; E owns no separate job-status classification.
E.3 implements inspect, retry and abandon API/tool/UI/notice surfaces and exact
provider/build/image conformance/routing receipts; DB primitives alone do not make
the accepted-undelivered action reachable after execution leases expire.

Exit: real adapter conformance for every enabled provider/pinned-build/ResumeIdentity
variant; unknown or unsupported remains blocking for that variant. Exercise actual
prepare/check/authorize/Execute and crash/expiry/control races, all prior possible-
acceptance evidence, failed control write atomicity and sibling progress. Missing
launch evidence is uncertainty unless the contract's complete no-launch proof holds.
Provider testing is a recorded gate, not an assumed result of adding an interface.

Test the same exact tuple before candidate image routing and active-session/lane/
provider switches, keeping a compatible serving image when rollout is deferred.
Unexpected runtime capability loss parks only the affected binding, never downgrades
an active session or proves a known-unsupported candidate eligible.

### F. Reachable recovery and legacy resolution

F.1 owns the single canonical `AdmitInputRecoveryInTx` implementation and sealed,
validated `RecoveryAdmission` mode dispatch. Its distinct branches retain all
mode-specific eligibility, identities, acknowledgements and consent: pending
request recovery, failed-only checkpoint/start-over and live continuation retry.
B.2 owns continuation input/attempt proof and adapter composition; F.2 owns failed-
only adapters and physical reservation/settlement. They call this one primitive
where recovery admission is required, never copy its predicate or borrow another
mode's authority. A live continuation rewrite is not historical normalization.

F.1's public `RecoverPendingInput` command is pending-only, through the existing
authorized API, tools, UI and notice action from paused state. That public scope
does not restrict the shared primitive to one mode. An already-answered request or
committed denial uses E.3's distinct InspectStartControl/Retry/Abandon actions,
never pending-only recovery or a second answer/grant. Preserve grants and audit.
Use the whole-session acknowledged descriptor/request/waiting-intent/parked-control
set. Changed acknowledgement aborts the whole unit, never resets a hidden sibling.

F.2 implements durable recovery reservation and every workspace-operation exclusion,
then positive physical drain/isolation and exact retry/observation controls. Close
the end-to-end notice/action journey with fresh request and fresh consent. Checkpoint
re-ask is unavailable until the exact provider mode is proven; explicit start-over
excludes superseded answer/approval text from executable seeds and live input.
An unreachable host, expired lease or closed logical control is not physical drain.
F.2 also owns the existing failed-only RetrySession checkpoint/start-over adapters,
with their characterized eligibility and F.1's applicable typed admission branch;
they are not pending-input actions or automatic interrupted-worker recovery.
F1 whole-workspace recovery may supersede only explicitly acknowledged parked
binding/control versions with the complete ALL-binding no-acceptance proof, in
the recovery transaction; possible acceptance retains its uncertainty disposition.

F.3 implements only bounded offline historical normalization and historical-input actions
before release, testing them on disposable migrated data with exact era-specific
identity, cutoff and old-job dispositions. H executes normalization only after
the established floor and old in-flight drain. Thus local F completion does not
depend on an operational floor. Unknown grant/delivery/physical outcomes retain
durable visible uncertainty; replacement/no-replay choices require their defined
authorization.
Never fill missing identity from today's primary lane or epochs.

Exit: paused notice → action → blocked/safe recovery → fresh request → fresh consent,
plus integration tests of E's distinct accepted-undelivered actions and unknown
physical-result reconciliation. F does not reimplement E's action owner.
Real ownership/topology/preview/materialization/publication competitors honor the
reservation. Old request/job normalization never manufactures runnable work.
Recovery and its user-facing documentation must exist before effective notices.
Existing failed-session retry/start-over adapters retain their distinct failed-only
eligibility and semantics. F installs only required workspace/recovery/start fences
there; ordinary retry policy extraction remains M4. They do not become pending-only
RecoverPendingInput routes by sharing a typed reservation primitive.

### G. Compatibility guards, constructors and residual wiring

G.0 owns the bounded tracing/coverage audit of syntax-only inventory candidates;
it resolves each to a real owner in A–F or an evidenced non-lifecycle case and
does not author a second mutation. A–F increments convert their own named roots
and record canonical operation, transaction owner, order, constructor ports,
receipt/effects and regression. G.1 first implements/tests the cutover observer's
missing-active-provenance protocol against A.0's contract before any A.1 router.
That protocol-core exit does not require later table guards or consumer wiring;
G.1 retains sole ownership when those complete against A–F. Its positive job-kind
registry core also precedes D.1r's first cleanup-job registration. G.1 owns database
guards/consumer eligibility; G.2 owns constructor completion and residual
maintenance/operator writers only.
No optional nil dependency, pool-write fallback, reverse service import or
specialist-wide exemption remains. Retained specialists keep domain ownership
while calling the same DB primitives. The ownership/tracing appendix bounds
membership; G cannot absorb an unassigned ordinary conversion at package exit.

Build versioned target eligibility into every claimant, not payload convention
alone. Consume A.2's single jobs-status policy for runnable versus nonterminal
ownership, control/retention and unrelated-domain purposes. G.1 owns the positive
non-lifecycle job-kind guard registry, a distinct classification from status sets.
Positively classify non-lifecycle kinds and prove absent lifecycle association; unknown/malformed
bindings fail closed. Lifecycle notices, artifact jobs and parked controls are
not unrelated just because they do not execute providers.

Every protected write uses explicit READ COMMITTED and transaction-local writer
version. Implement the full contract §9 table/scope map, including authority/
reservation rows, OLD and NEW on update, OLD on delete, missing retained provenance
and child-only writes. Reject NULL and empty unset markers, stale version and
unsupported isolation. Validate fresh VOLATILE post-interlock lookup on PostgreSQL,
including late-discovered scopes and transaction-mode pooling. Connection startup
settings and observed version rows cannot replace this protocol.

Exit: inventory/boundary guards cover all changed families and new bypass fixtures;
real constructor and outer-owner call-through tests pass. Prove old-writer rejection,
version-0 coexistence, scope moves/deletes, late statements crossing activation,
cascades, generic bulk forward progress and unrelated-job exclusion. Emit version 1
only after that binary actually meets its complete compatibility obligation.

### H. Release assurance and authorized activation

H has separate local proof, release establishment and activation checklist items.
It starts with each converted caller; its final gate joins A–G for the selected
scope. Public documents keep reusable procedures and synthetic examples. Exact
inventories, capacity, operational receipts and raw evidence belong outside this
public repository in the private operations destination.

H.0 implements and locally tests the activation controller, exact-version operator
retry, bounded normalization invocation, cutover/guard stamping and atomic per-request
classify/enforce/notice transaction before release. Its postconditions must match
the shared activated-session fixture; controller tests run on disposable data.
Installing the controller never authorizes calling its activation action. H.0's
local completion is distinct from H-release's real fleet/drain evidence and
H-cohort's separately authorized operational invocation.

Each independently authorized dark publication needs that increment's full-schema
adjacent-build proof and exact-head CI, without waiting for H completion. Before
declaring the assembled release ready, H additionally uses independent-connection
barriers for pre-floor/floor and floor/new, including request/parent, sibling
archive/claim, automation/grant, recheck dispatch/completion, implicit FK/end,
reclaim/seed and child-only activation. Prove confirmed whole rollback before
bounded retry, no effects on abort and no replay on ambiguous commit. Preserve the
selected contract §5 bounds and actual-cutover-conflict exception.
Record existing pre-floor lock inversions and their abort responses separately
from introduced regressions. A characterized pre-floor abort is not proof of
floor progress; known baseline cycles never excuse starvation or an unresolved
floor/new lock graph.

Matched baseline/floor workloads must show no new lock-cycle pairs, bounded
completion without starvation, p95 ≤ `max(baseline × 1.20, baseline + 10ms)`, the
two-second DB-unit budget and due-work discovery within three five-second ticks
without capacity blockers. Record p99, contention/exhaustion and throughput,
including SET LOCAL, trigger, pooling and certificate fanout cost. These are
unproven provisional gates, not measured guarantees; retuning needs evidence and
review. Real provider/storage gaps block the applicable scope.

Release establishment requires separately authorized deployment and independent
build/role/transaction/runtime census plus positive natural retirement/drain of
all pre-floor writers, starters, consumers and in-flight calls. Do not accelerate
drain, infer retirement from expiry or silently hold legacy jobs before activation.
The rollback floor retains compatible consumers/reconciliation and exact receipts;
it never restores answer-latest, pre-floor lock order or uncertain-input replay.

Only then normalize and activate the explicitly enumerated session/provider
cohort. Atomically classify, enforce and persist notice intent per pending request;
new requests read the locked session activation state. No classified-but-unenforced
interval or notice before the effective boundary. Recheck notices before delivery.
Require tested actual target tuples before routing. Use bounded activation try-lock
and backoff; `ACTIVATION_CONTENDED` retains compatible semantics and stops automatic
retry until an authorized exact-version operator action. No forced stop or implied
intake barrier. Demonstrate rollback on both sides of the activation boundary.

## Safety-obligation crosswalk

Each source row below is mandatory in full; summaries are navigation, not
permission to implement only the named example. Review each source row against
its owner package before checking that package complete. New inventory entries
inherit their family package and must receive explicit evidence.

| Existing obligation | Package owner | Joined acceptance gate |
| --- | --- | --- |
| Contract §§1–3 ordinary behavior, dispatch identity, exact ingress/consumer exclusion and claim accounting | A | A+B; no producer-only activation |
| Contract §3 typed domain authority, grants, immediate/queued answer/denial and integration effects | A, G | A+C+E+F+G before answer-enabled cutover |
| Contract §3 canonical start/seed/live/ack, own terminal settlement and stale shared projection | B, E | A+B+E+G, including all failure/reclaim hooks |
| Contract §§3–4 end/cancel, owner/repair/generation changes, topology and parked supersession | B, F | B+E+F+G, positive predecessor drain |
| Contract §4 every transaction/caller composition row and required ports | A–F assigned increment; G audits and completes constructor/residual scope | Exact per-row real-caller regression and no copied fence/nested owner |
| Contract §5 explicit/implicit/FK lock graph, bounded abort/retry, both rolling transitions and hot-path budget | G, H | Incremental tests from first caller; complete H before release |
| Contract §6 initial/retry/start/end/input boundaries and physical uncertainty | B, E | All starters, pollers, delivery and cleanup paths converted |
| Contract §7 immutable proof, current certificates, all-request fanout, deadlines and typed outcomes | C, A | C+D+E+F before atomic enforcement |
| Contract §7 exact native identity, two-stage start, FIRST/every Execute, retained PARKED controls | E, B | Actual adapter/build conformance and real control races |
| Contract §7 unique Save keys, pre-Save ownership, every reference creator, guarded expiry and exact-key orphan cleanup | D, C | Exclusive SnapshotReaper routing, storage races and durable unknown-I/O responsibility |
| Contract §7 F1 whole-session acknowledgement, physical reservation/isolation and reachable recovery | F, B–E | Both pending and accepted-undelivered journeys before notices |
| Contract §§3, 7–8 legacy provenance, cutoff, historical uncertainty, integration retry/notice/expiry | F, A, C | H floor/drain precedes effective normalization/classification |
| Contract §9 complete protected table map, OLD/NEW routes, late cutover lookup and positive job exclusions | G | PostgreSQL snapshot/guard/pooling and real caller proof |
| Contract §9 exact provider/image routing, deployment/DB gates, cohort activation and supported rollback | E, G, H | Separate release-established and cohort-active receipts |
| Contract §10 remaining real-caller/schema/provider/storage evidence | A–H | No synthetic probe or completed passive slice substitutes |

### Parent-plan requirements retained from the source baseline

These requirements were explicit in the earlier parent 0a section or passive
schema description. They remain stated here even where the companion also
contains a related rule; shortening the parent must not erase their timing.

| Retained obligation | Owning increment | Evidence and stop gate |
| --- | --- | --- |
| Before activating larger transactions, convert competing old entry points and store primitives to the chosen compatible lock order: answers/grants, sends/drains, sibling claim/archive CTEs, explicit multi-row order, implicit FK and retained specialist transactions. | A.1/A.3–A.6 admissions, B.1 settlements/reclaimers, B.2 starts, B.3 owners/end/topology, C.1 workspace, D.1–D.2 storage; G.2 residual operators | Each actual production version-0 path and locked mode router converts in its owning increment. Adjacent-build full-schema barriers and the full H graph must pass before the larger transaction activates. |
| If old writers remain, keep the old compatible transaction boundaries active until the floor is established. | Every A–G caller increment; H establishes floor | Do not combine legacy units or extend lock overlap merely because the new branch exists or has a marker. Test multi-transaction activation straddles with exact receipt resolution. |
| 0a is a prerequisite for any slice that changes lock overlap or relies on the new semantics, including storage work moved forward from later milestones. | Every activation join in A–G; H release gate | Only proved compatibility-order preparation retaining the old boundaries may run pre-floor. Any new overlap/reliance stays dark until the established 0a floor; adjacent-build proof alone cannot waive this prerequisite. |
| Record baseline inversions/abort outcomes separately from regressions; pre-floor aborts do not prove safety, and known cycles cannot excuse starvation or unresolved floor/new graph. | Each increment's characterization; H assembled proof | Preserve baseline outcomes alongside no-new-pair, bounded-progress and throughput results. Missing progress evidence blocks release. |
| Roll back readers before removing passive schema columns. | Each additive schema increment's down/rollback procedure; H rollback review | Reader-before-column removal is required while inactive. Once active, the supported rollback floor additionally forbids dropping epochs, cutover/guard provenance, bindings or unresolved receipts/controls. |

### Production routing and caller membership

Locked cutover observation is the only mode selector in actual production entry
points. A.1, A.3–A.6, B.1–B.3, C.1–C.2, D.1–D.3, E.1–E.3 and F.1–F.3 each own
the mode routing and compatible-order conversion of their assigned caller roots.
G owns the guard/claim/constructor/residual roots assigned to it, not a second
conversion of A–F. Before converting a root, enumerate its competing baseline
edges, preserve old transaction boundaries and prove adjacent coexistence.
Shared callers listed under another package compose the root owner's canonical
operation; they cannot copy its predicate or invent an additional enable switch.

## Next implementation slice and M1–M6 handoff

The next work starts with prerequisites, not the A.1 production router:

1. **A.0/C.0 contract freeze:** resolve schema/retention, immutable authority,
   consumer, reservation and receipt shapes plus the activated-session fixture.
   This is the immediate next design exit; no new admission writer is installed.
2. **G.1 protocol core:** implement and test retained/missing-active-provenance
   handling against that contract, including deleted/malformed cases. No router
   may interpret missing active provenance as a version-0 fallback.
3. **A.2 consumer/status core:** solely install/test the shared schema, competing-
   consumer query and coupled status/check/index/lookup/collision contract before
   an ordinary writer consumes them. This does not claim all caller joins complete;
   the full 123-site status-policy proof additionally gates E's first parked row.
4. **A.1 ordinary non-answer thread-lane vertical:** only after those exits, add
   canonical `AdmitMessageInTx` through
   `internal/services/thread/service.go:Service.SendMessage`. A.1 consumes A.2's
   schema, not a second consumer model. Real-caller full-schema tests prove atomic
   claim/message/inbox/comment/dispatch/new-job creation, replay/conflicting reuse,
   rollback and conflicting-consumer wait. Name the construction seam and exact
   behavior matrix when this implementation is authorized.

These are separately reviewable exits, not a requirement for one combined PR.
A.1 installs its real locked router and compatible version-0 path with the complete
version-1 branch dark, never a test-only seam or second feature flag. Binary-wide
version-1 advertisement and activation remain disabled until their joined gates
pass. An empty schema/reader does not satisfy the A.1 behavioral vertical, and
that vertical alone does not close A.2's remaining real-caller/status evidence.

M1 then removes the ordinary session handler's policy/transaction choreography;
M2 consolidates thread send/drain/reconciliation; M3 removes answer/integration
adapter choreography; M4 consolidates retry/cancel/end commands; M5 consolidates
projection policy and retained specialist composition; M6 removes bypasses and
verifies maintainability. Their prerequisite safety conversions happen in 0a.
Later milestones reuse these primitives rather than introducing a second fence.
Track decisions/transaction owners removed, not only additions and line count.

## Required inputs and stop gates

These are bounded future decisions, not permission to invent a policy during
implementation or to declare a blocked increment complete:

| Gate / owner | Required decision or evidence | Stops |
| --- | --- | --- |
| A.0 pre-producer schema review | Resolve dispatch/control references to jobs/runtimes against terminal retention in migration 309 and hard-delete policy. Preserve real tenant-scoped FKs and required receipt/control evidence; specify cleanup/deletion ordering rather than dropping constraints to make deletion pass. Freeze binding/consumer states and the active-session fixture with B. | A.2 schema installation, then A.1's first dispatch producer |
| A.4 session-send characterization | Prove the legacy threadless/primary-lane identity and sibling concurrency, then specify its canonical claim installation. Do not infer the current primary lane or broaden an ordinary send into a whole-session claim. | Session-level canonical admission, not A.1 |
| E/H first cohort | Select explicit provider, pinned build, ResumeIdentity variants and session kinds for the first operational cohort; test every runnable tuple. Keep unsupported version-0 variants visibly deferred with an eventual migration/retirement disposition. | Cohort conformance claim and activation, not local generic primitives |
| G/H operator policy | Require compatible explicit transactions for protected manual/operator writes, or forbid those operations on active scopes. Specify the permitted surface and fail-closed behavior; a raw SQL exception is not implicit. | Established floor and activation |
| H performance / commit uncertainty | Measure the existing bounds, hot-path overhead and late-commit uncertainty. Retain the selected retry/timeout policy unless evidence and design review justify a revision; no arbitrary commit-reserve cutoff. | Release readiness |

The contract's current lock/coexistence protocol remains selected. A version-0-
lock-order-until-activation alternative, caller-family-limited floor, global
snapshot-policy change or intake barrier would require a separate approved design
change and its own proof; none is chosen by this re-baseline. Recovery copy/API
details are reviewed in F before publication, while whole-session acknowledgement,
fresh consent and default-unsupported checkpoint re-ask remain fixed requirements.

## Progress

- [x] Inventory the implemented passive foundations separately from authority.
- [ ] Finish and independently review this delivery re-baseline.
- [ ] A: contract freeze plus admission/consumer ownership, increments A.0–A.6.
- [ ] Each separately authorized dark increment: review, adjacent-build proof,
  scrubbed publication and exact-head CI. Repeat per increment, not only after H.
- [ ] B: execution/settlement/end, all three increments.
- [ ] C: readiness and current workspace evidence, joined with D.
- [ ] D: unique-key publication, expiry and bounded cleanup, joined with C.
- [ ] E: actual exact-target launch and durable accepted-undelivered control.
- [ ] F: reachable recovery and bounded legacy resolution.
- [ ] G: complete caller/guard coverage and version-1 capability proof.
- [ ] H-local: mixed-writer, performance, provider/storage and rollback proof.
- [ ] H-release: separately authorized compatible fleet floor and natural drain.
- [ ] H-cohort: separately authorized atomic backlog/new-request activation.
- [ ] Repository-wide 0a acceptance; no unbounded active version-0 tail.

## Surprises & Discoveries

The shipped seams remove nested transaction and effect-ordering hazards, but
none is a production caller of the lifecycle runner. Passive receipt equality
also deliberately leaves eligibility and release unresolved. Counting these
foundations as canonical authority would hide most remaining behavioral work.

Admission/settlement and readiness/storage/start/recovery form integration joins.
Splitting them into reviewable increments is safe only while activation remains
closed until the joined callers and their failure paths are proven.

## Decision Log

- Preserve the M0 contract and completed foundations; re-baseline delivery rather
  than renaming unimplemented safety work as complete.
- Attach remaining schema to concrete consuming behavior and named real-caller
  proof. Keep one canonical DB authority and specialist outer transaction owners.
- Separate local approval, exact-head CI, established release and cohort activation.
  This planning task authorizes none of the latter operational actions.
- Retain provider-private isolation/export as optional hardening. Exact-target
  verification, trusted-session safeguards and reference-safe cleanup remain gates.

## Outcomes & Retrospective

The current deliverable is a draft plan, not an implemented compatibility release.
Implementation size and provider conformance are still uncertain; no percentage,
calendar estimate or fixed PR count is asserted. Update package progress only with
the corresponding source and verification evidence, then record operational
acceptance separately without copying private receipts into this document.
