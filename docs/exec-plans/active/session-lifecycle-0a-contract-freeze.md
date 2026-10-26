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
resolve a consumer or expand an existing payload.

Consumer states are `none`, `job` and `runtime`. `none` has neither consumer
pointer. `job` requires its exact job; `runtime` requires its exact runtime.
A runtime pointer must have immutable allocation evidence. A job and its runtime
may both be retained only when that allocation proves they are the same logical
binding. They are not competing consumers merely because both columns are set.
Replacement is a later explicit reclaim/replay operation, never an input-set
setter; it preserves every prior allocation and possible-acceptance receipt.

Current execution and historical own-result settlement have different sealed
input types. Historical allocation does not imply a current lease or current
epochs. Admission compensation instead names the exact committed ingress,
dispatch, epochs, claim token and turn; it cannot fabricate a provider result.
The only stale-shared-state disposition belongs to successful own-receipt
settlement. Admission, start, claim and acknowledgement remain strict.

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
tuple fields are exact nonempty identities, not wildcards. The fixture uses only
synthetic values. It is not an operational activation procedure or a release
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
