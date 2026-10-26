# Lifecycle A.0/C.0 contract freeze

This appendix fixes shared representations and their installation owners for
[0a](session-lifecycle-0a.md). It does not enable a writer, establish a fleet floor,
verify a provider, or authorize activation. The
[M0 contract](session-lifecycle-m0-contract.md) remains normative for behavior.

## Identity and authority

`internal/db/session_lifecycle_contract.go` holds explicit admission fences,
exact ingress identity, immutable job input sets and attempt-allocation inputs.
Constructors validate shape, copy caller-owned slices/pointers, and return zero
on error. They do not read the database and never grant authority. Every later
mutation re-reads its exact tenant/session/lane/receipt under the canonical locks.
The organization remains an explicit argument to every mutation and part of each
authority receipt; a shape-valid receipt from another tenant cannot be adopted.

Threaded admission installs the lane claim only. Session claims are reserved for
session-wide execution that conflicts with every lane. Parent running status is
derived from active siblings, not a shared admission-claim pointer. A claim names
the dispatch, token and exact recorded turn; it is not a provider execution lease.

A job's version-1 binding is an immutable nonempty exact dispatch set. Only a new
ordinary-message job may batch selected unbound inputs. An answer or denial job
has exactly one request intent and its associated message/inbox, never unrelated
ordinary input. Dedupe collision, content equality and a nil enqueue ID cannot
resolve a consumer or expand an existing payload. Persist digests only as
64-character lowercase hexadecimal SHA-256 text; reject alternate encodings.

Consumer states have one canonical encoding per phase. `none` has neither
consumer pointer or allocation. `job` has only its exact job, no runtime or
allocation. `runtime` requires both exact job and runtime plus immutable
allocation evidence for that same tenant/lane and containing this dispatch ID.
The runtime stores its own `allocation_expected_turn` alongside the allocation
digest and input set. Current claim turns and mutable parent turns cannot
substitute for that immutable recorded attempt turn.
The two runtime-phase pointers identify one logical consumer, not two. Threadless
execution needs a separate explicitly defined session-executor variant; it cannot
masquerade as a lane runtime. The transition from job-only to its allocated runtime
never changes the input set or makes the prior allocation disposable.
Replacement is a later explicit reclaim/replay operation, never an input-set
setter; it preserves every prior allocation and possible-acceptance receipt.

An own-consumer exception verifies the actual request's tenant, session and lane,
not just its stored request ID. Current job/runtime lease checks use fresh database
statement time; application clocks cannot establish current lease authority.

Current execution and historical own-result settlement have different sealed
input types. Historical allocation does not imply a current lease or current
epochs. Admission compensation instead names the exact committed ingress,
dispatch, epochs, claim token and turn; it cannot fabricate a provider result.
The only stale-shared-state disposition belongs to successful own-receipt
settlement. Admission, start, claim and acknowledgement remain strict.
Turn inputs must fit PostgreSQL's nonnegative 32-bit integer range before any
narrowing. Already-applied and stale-shared-state results contain no generic
current-state notifications. A stale own-settlement's allowed attempt-drain
reconciliation is a durable exact-attempt job in its transaction, not a generic
notification or new-current-work permission.

## Shared workspace contracts

`internal/db/session_lifecycle_workspace_contract.go` owns the shared descriptor,
pending-upload registration receipt, immutable request anchor/certificate
transition, recovery reservation, distinct retention/resource-cleanup inputs,
resume-identity variants, selected-target observation and resumability receipt.
Each binds exact scope, generation/revision/publication and availability versions;
no greater-than-or-equal revision shortcut or generic `verified` flag is evidence.

C.1 owns descriptor and certificate mutations; D.1r owns upload registration and
its one pre-registered exact-key cleanup obligation; E.1 owns actual selected-
target provider verification. Recovery admission owns the acknowledged request/
intent sets, while the shared reservation retains their exact digest and token.
A type or synthetic receipt is not provider conformance, physical drain, storage
publication, deletion, or permission to call a provider.

## Schema and retention installation

A.2 alone installs the dispatch consumer/admission fields, job/runtime references,
matching unique parent keys and reverse/due indexes. Its status-core migration
installs the parked status/check, broader `(queue,dedupe_key)` unique index, active
lookups and enqueue collision interpretation together. No B/E writer introduces
a second consumer representation. Runtime allocation, settlement, preparation
and launch writers remain owned by their named packages.

Use immediate non-cascading real scoped FKs: dispatch job references use
`(org_id,job_id)`; runtime references also retain the exact session/lane route.
An FK proves structural identity, not payload binding, epoch or lease authority.
The existing token-bound lane-claim FK requires dispatch-before-pointer creation
and pointer-clear-before-receipt-token replacement in the same transaction.

Terminal job retention (migration 309's `delete_expired_completed_jobs`) must
exclude referenced jobs in the candidate query **before its LIMIT** when A.2 adds
these references. It must also retain jobs with unresolved object/control
obligations. Filtering only the final DELETE can starve unrelated cleanup.
Neither `ON DELETE CASCADE` nor `SET NULL` may erase a claim, binding, allocation,
uncertain delivery, preparation/control or outstanding object operation to make
retention succeed. Current session soft deletion retains its child evidence.

Generic age is never proof of resolved ownership or physical settlement. Until a
later explicit retention owner proves all obligations closed, retain references
and their jobs/runtimes. That owner deletes resolved dependent receipts first,
then dispatch/control references, then otherwise eligible jobs/runtimes. This
freeze does not add an unbounded deletion worker or silently relax an FK.

A.2's preparation representation has one current
`session_runtime_start_preparations` row per `(org_id,job_id)`. It retains the
fixed input set and recorded epochs, current preparation/claim tokens, exact
target and resume-identity snapshots, and a separate physical-operation slot.
Execution state is `preparing`, `authorized` or `closed`. Physical state is
`none`, `reserved`, `in_progress`, `unknown`, `settled` or `fenced_drained`.
Closing execution or revoking its token preserves unresolved operation identity
and its restore-source association, including after the job becomes terminal.
The scope/due/source indexes include unresolved physical work after closure.
Whole-session physical scope conflicts with every lane; B/E must validate that a
`target_only` operation cannot mutate shared workspace resources before using
that narrower scope. A missing source projection cannot prove an object unused.

The current whole-workspace recovery slot lives on `sessions` as
`workspace_recovery_id`, `workspace_recovery_token`, `workspace_recovery` and
`workspace_recovery_next_attempt_at`. Its versioned envelope retains the recorded
session epoch, complete reservation spec, finite execution/physical disposition
and physical observation. It cannot carry a lane fence. Any present slot blocks
workspace execution until its owner releases it; deadlines and revoked execution
do not release physical exclusion. The passive retention guard has no clear or
replacement path. F must install the exact immutable accepted/outcome/isolation
receipt check before adding conditional release. This current slot does not
replace F's durable recovery history.

These SQL checks establish shape and retention. B/E/F must decode full typed
targets and observations and verify authoritative settlement at their locked
writer seams. A parsed JSON object or a stored settled label supplies no host
proof or provider-start permission.

The retained dispatch control uses an explicit `schema_version=1` snapshot.
It repeats the scalar receipt, version, state, due time and optional refund
reference; retains the complete dedicated answer/denial binding and logical
claim token/turn; and captures the exact resume identity, selected target and
historical resumability outcome. Its full job-payload digest hashes the actual
PostgreSQL `payload::text` representation, not a decoded and re-encoded map.
That digest is distinct from the input dispatch digest. Finite no-launch and
delivery-uncertain variants retain receipt/revocation/allocation references;
decoding those references establishes no physical or no-launch proof.

Control validity does not expire with an execution lease, preparation row or
target observation. Row matching compares the complete actual dispatch/job
association and requires an `awaiting_control` job to be parked with all lease
and completion slots clear. The mirrored due time is captured at UTC microsecond
precision before persisting snapshot and scalar; stored version-1 JSON with
sub-microsecond due precision is refused. Checkpoint publication times are not
normalized by this rule. The later E writers still own current actor and
generation eligibility, revocation, the shared claim-keyed refund CAS and the
ALL-allocation possible-acceptance check. A matching parked logical consumer
does not grant execution or remove its competing-control exclusion.

## Retained activation provenance and the common fixture

G.1 owns the protocol core before the first production router. Reuse the parent
`sessions.lifecycle_version` as the retained protocol stamp, distinct from
`session_lifecycle_cutovers.version` (the cutover CAS revision). Once positive,
the protocol stamp cannot decrease or disappear through hard deletion, scope
movement or cutover deletion/reset. G.1 adds the monotonicity/provenance guards;
full OLD/NEW child-table guard coverage remains the later G integration exit.

The resolver acquires the shared cutover interlock, then reads the exact parent
and optional cutover in a fresh READ COMMITTED statement. A never-activated
version-0 parent with no cutover or inert valid shadow metadata remains legacy.
A valid normalizing record returns normalization-pending, never an implicit
legacy success; H may create it only after its floor/drain gate. An active parent
requires a consistent active cutover and supported complete provenance. Missing,
deleted, contradictory, malformed or unsupported active provenance returns
`LIFECYCLE_GUARD_PROVENANCE_REQUIRED`; it never falls back to version 0.
Mode resolution alone is not admission, readiness or activation authority.

A shadow row may retain a valid incomplete normalization envelope with its exact
cutoff and floor receipt, but still resolves as legacy. Normalizing requires that
envelope with `complete=false`; completion is published atomically with active
cutover and protocol stamps, never as a completed normalizing intermediate state.
Receipt UUIDs compare by decoded identity; canonical hexadecimal encoding applies
to digest text, not alternate equivalent UUID spellings.

The protocol core in `internal/db/session_lifecycle_protocol.go` reads the parent,
optional cutover and visible lane stamps/providers only after acquiring the shared
interlock. It validates the supported JSON envelopes and actual provider coverage;
it does not select a provider build or issue a resumability receipt. An empty
visible lane set does not infer a primary lane or authorize threadless execution.
Missing scope is not-found; every protocol/scan/context failure returns no mode.

Migrations 346–348 install the retention functions and separate parent/cutover
triggers without stamping or activating rows. Either a positive retained parent
or an active cutover independently prevents evidence deletion/reset. These are
provenance-retention guards, not the later complete minimum-writer/child-table
guard set. They intentionally leave ordinary unversioned writes to inactive
version-0 parents compatible. SQL advisory-lock refusal uses the same exact
tenant/session key as Go; only an actual failed advisory try-lock is reported as
`LIFECYCLE_CUTOVER_INTERLOCK_BUSY`.
Once active, normalization is write-once: an exact no-op is allowed, replacement
is refused. Equality uses canonical JSONB text, retaining numeric encoding so an
integer cannot become a fractional encoding rejected by the strict reader. Key
order is immaterial. Normalization stays frozen even on an invalid active row
where it is absent; this component supplies no provenance-repair authority.
Raw DELETE on a positive parent is refused, including the default soft-delete
redirect. Ordinary application soft deletion uses UPDATE of `deleted_at`, which
retains the stamp and remains permitted.
Capability update authority remains a later guard obligation; this
core only prevents clearing it. Malformed replacement fails strict resolution,
and a shape-valid replacement still proves no provider conformance or target authority.

Bulk maintenance also retains provenance. TRUNCATE takes nonwaiting SHARE locks
on both evidence tables before its fresh READ COMMITTED assertion, including the
table other than its target. A relation-lock conflict remains a real PostgreSQL
lock error, without the advisory-busy label. Each down migration locks both
evidence tables with a bounded acquisition before checking for positive/active
evidence and removing guards in that same transaction. REPEATABLE READ and
SERIALIZABLE are rejected because an old transaction snapshot cannot establish
that activation evidence is absent.

An inactive rollback still requires withdrawing readers and inhibiting activation.
The future H controller must acquire schema-protecting relation locks and then
freshly validate its required installed schema/guard floor before normalization
or stamping. An activator waiting behind a successful down migration must fail
that post-lock validation. The retention core supplies no activation controller
and does not prove this later H integration gate. Once positive/active evidence
exists, these down migrations refuse removal rather than reopening legacy mode.

H must bound the distinct scope locks held by each bulk statement and measure
shared lock-table pressure and forward progress. Row-level advisory acquisition
does not provide an unbounded bulk-operation guarantee. H must also measure the
rollback availability window: the retained-evidence check can scan the session
table while the rollback holds exclusive relation locks. Neither capacity nor a
table-size-independent rollback duration is established by this component.

The decision matrix also fixes these error boundaries: missing exact parent is
not-found, while a soft-deleted parent still retains provenance and is not hidden
from protocol resolution (admission visibility is a later locked check). A
version-0 parent with an active cutover, a positive visible lane stamp or malformed
shadow/normalizing metadata, or a positive parent with missing/non-active metadata
is contradictory and fails closed. Active visible lanes must carry the supported
stamp and a provider tuple
covering their actual agent type; a visible version-0 lane under an active parent
is not a legacy escape. A cutover CAS revision need not equal the protocol stamp.

The shared test-only activated-session fixture uses one atomic configured READ
COMMITTED transaction, exclusive interlock, parent/lane protocol stamps, exact
epochs and an active cutover. It uses positive cutoff 1 (consistent with the
existing schema), writer minimum 1, and these versioned JSON shapes:

- `normalization`: `schema_version`, `receipt_id`, `floor_receipt_id`, `cutoff_id`
  matching the stored cutoff, and `complete`.
- `resumability_capability`: `schema_version`, `release_receipt_id`, and nonempty
  `tuples`; each tuple contains `provider`, `cli_build`, `image_build`,
  `resume_identity`, `verification_protocol`, `conformance_receipt_id`.

Version 1 is the supported envelope version. Receipt identities are nonzero UUIDs;
tuple fields are exact nonempty identities, not wildcards, and provider values
are valid agent types covering the fixture's actual parent and lane agents. The
fixture uses cutover CAS revision 3, distinct from protocol stamp 1, with synthetic
builds and receipts. It is not an operational activation procedure or a release
receipt. H must eventually prove that its real activation produces the same
invariants after validating durable release/normalization/provider evidence.

## Delivery and evidence

- A.0/C.0: compile and test shape validation, zero rejection, defensive copying,
  authority separation and the shared full-schema fixture.
- G.1: prove the missing/deleted/malformed-provenance matrix, monotonic stamps,
  fresh post-interlock visibility and mixed-version coexistence on PostgreSQL.
- A.2 core: prove scoped FKs, retention exclusion, cross-key consumer discovery,
  coupled parked status/index/lookups and no runnable/terminal reinterpretation.
- A.1: prove the actual ordinary non-answer `SendMessage` call-through for both
  versions, compatible legacy ordering/boundaries, atomic new admission,
  replay/conflict/rollback, sibling independence and capacity/wait outcomes.

Whole-root conversion, answer admission, provider starts, full status-site joins,
activation and production acceptance are not established by these component exits.

Before their first consumers, C.1p owns certificate transition policy (fresh
operation, no-op/idempotent handling and generation-change dispositions), E.1/C.2
own the bounded short-lived observation policy and explicit disposition handling,
and B.1 owns its combined current/historical settlement, lost-attempt and job-lease
proof shapes. A.4 freezes typed ingress namespaces before adding integration
producers. None of those later behavioral gates is supplied by a shape constructor.
