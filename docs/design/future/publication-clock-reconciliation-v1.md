# Design: Result-free publication clock reconciliation v1

> **Status:** Not Started | **Last reviewed:** 2026-10-21

Draft for agreement between independent producer and replay readers. This change
contains a contract and invented fixtures only. No producer, verifier, provider
query, reconciliation over real inputs, or change to inventory/materializer v1 is
implemented. Approval of this document does not freeze a replay protocol.

## Purpose and boundaries

Fill an unavailable source-publication clock only from an accepted, inspectable,
result-free witness of an authoritative submission or publication-edit event.
Preserve every original observation, its fingerprints, and its acquisition cutoff.
Do not compute cohort membership, order, partitions, denominators, scores, or
opportunity verdicts. Missing event evidence means unknown, never unpublished.

The original inventory and selected-record `record_metadata` comparison stay
unchanged. A parent first verifies original payload metadata against that original
inventory; only then may it apply this separately pinned sidecar under an agreed
policy. An unresolved/conflicting observation must remain visible to whole-PR/
stack quarantine. It cannot disappear through a per-record filter.

No database schema, migration, API route, authentication/RBAC, policy, or HTTP
behavior changes are proposed. The future producer is offline. All input paths
are operator-supplied; artifact references are opaque hashes, never fetch URLs.
No network, live state lookup, source expansion, or result access is implicit.

## Three separate trust decisions

1. **Integrity:** exact supplied file bytes, including trailing LF, match hashes;
   strict JSON, schema, cross-file bindings and original fingerprints agree.
2. **Evidence acceptance:** an operator separately accepts a fixed, reviewed
   source profile and pinned extractor/source artifacts. The consumer receives
   the acceptance-file SHA through its trusted invocation context. Neither a
   caller-generated `matched` flag nor an arbitrary profile name establishes
   authority. An extractor must not generate or approve its own acceptance file.
3. **Execution authorization:** permission to read archives, queries, results or
   holdouts comes from the human/workflow, not these files. Source acceptance
   confers no execution authorization. This draft authorizes no real execution.

No signing service or new signature mechanism is introduced. A file hash cannot
prove historical truth. The consumer relies explicitly on the accepted operator
attestation that a reviewed extractor faithfully projected an authenticated
historical source. A consumer that cannot inspect the sealed original cannot
independently recheck the original provider authentication/body extraction; this
limitation is retained, not hidden behind a hash.

## Exact artifacts

The normative grammar is
[`publication-reconciliation-v1.schema.json`](publication-reconciliation-v1.schema.json).
Its `$defs` define the exact key sets below; unknown fields are errors. The
semantic checks in this document are additional requirements, not JSON Schema
features. Inventory input remains the existing strict schema v1.

| Artifact | Purpose and required binding |
| --- | --- |
| `inventory.json` | Original strict v1 inventory; separately pinned exact bytes. |
| `metadata-witness.json` | Result-free source events, object observations, artifact provenance and a binding for every original locator. Pins inventory/catalog/original-catalog hashes. |
| `source-acceptance.json` | Operator acceptance of this exact witness, extractor revision/digest, source profile, repository mapping and artifact bases. Its exact SHA is supplied independently by the trusted caller. |
| `reconciliation.json` | Producer output, independently recomputed by parent from the three inputs. Contains no caller-authoritative conclusion. |

Illustrative future invocation, **not implemented**:

```text
reconcile --mode historical --inventory INVENTORY --inventory-sha256 SHA256
          --witness WITNESS --source-acceptance ACCEPTANCE
          --source-acceptance-sha256 OPERATOR_ACCEPTED_SHA256 --output NEW_FILE
```

The trusted caller must obtain expected hashes from the accepted handoff, rather
than calculate them from untrusted files just to satisfy the flags. `historical`
mode rejects any `synthetic` artifact. Synthetic mode is only for invented tests.

### Source acceptance

Exact root fields are `schema_version`, `purpose`, `dataset_kind`, `ruleset`,
`inventory_sha256`, `catalog_sha256`, `original_catalog_sha256`, `witness_sha256`,
`source_profile`, `extractor_revision`, `extractor_code_sha256`,
`repository_bindings`, `artifacts`, and `operator_attestation`.

The only v1 profile is `publication-event-metadata/v1`. Its semantics are fixed
below; another string is not a plug-in. Pin a reviewed full Git revision and
extractor file digest. Each repository binding maps one original repository UUID
to one immutable provider repository ID; duplicate/contradictory maps fail.
Repository display names and current PR heads are not identity substitutes.

Each accepted artifact supplies its ID/hash, an accepted basis and an opaque
verification-receipt hash. These must match the witness artifact set exactly.
The attestation records an operator ID, attestation time, a verification receipt,
permitted purpose, `confers_execution_authorization:false`, and the explicit
limitation `operator_attested_projection_original_not_reverified_by_consumer`.
The receipt is an evidence-acceptance reference, not a permission token.

| Accepted basis | Required artifact origin | Permitted witness facts |
| --- | --- | --- |
| `authenticated_provider_submission` | `provider_submission_event` | `review_submitted` + `provider_submitted_at`; exact publication key derived from that authenticated event's output marker or an authoritative event-linked publisher identity record. |
| `verified_historical_publication_edit` | `publisher_edit_event` | `review_body_edited` + `provider_edit_event_at`; requires an authoritative historical edit event linked to the exact publication. An object's old submission clock, HTTP receipt time or generic `updated_at` is insufficient. No currently available adapter for this basis is asserted. |
| `provider_object_metadata_only` | `provider_review_object` | Candidate object identities/timestamps only. Cannot authorize a matched source clock. |

Acceptance of an unsupported/unreviewed extractor or unverified event source is
not allowed merely because the file has a valid digest. The operator must review
that source/profile mapping before supplying its trusted expected hash.

### Metadata witness

Exact root fields are `schema_version`, `purpose`, `dataset_kind`, `ruleset`,
`inventory_sha256`, `catalog_sha256`, `original_catalog_sha256`, `source_profile`,
`extractor_revision`, `extractor_code_sha256`, `observed_at`, `artifacts`, `events`,
`object_observations`, and `bindings`.

An artifact has `artifact_id`, `sha256`, `origin_kind`, and `observed_at`. Only
opaque source fingerprints and result-free provenance are included. No original
body, review state, decision, label, finding, policy, coverage/safeguard count,
free-text note, URL, credential or exception is allowed anywhere in the grammar.

An event has `event_id`, `artifact_id`, `provider_repository_id`, `pr_number`,
`provider_review_id`, `commit_sha`, `publication_key_sha256`, `event_kind`,
`clock_kind`, `event_time`, and `publication_link_basis`. The link basis is
`provider_event_output_marker` or `publisher_event_exact_publication_key`.
The exact source publication key is SHA-256 hashed as its exact UTF-8 bytes:
no trimming, case folding or normalization. A trimmed marker preimage cannot
prove a different untrimmed key; the keys/digests must agree exactly.

The extractor must prove the key-to-event link from the accepted historical
source. It cannot copy a desired key from inventory onto a bare review-object
clock. A signed/accepted assertion of `matched:true` is not an event or link.

An object observation has only its ID, artifact ID, immutable provider repository/
PR/review identity, commit SHA and nullable object submission time. It deliberately
has no publication key. It may guide later lookup; it never supplies a matched
source-publication clock, including when its timestamp happens to be plausible.

Every binding contains the original five-field locator, `record_sha256`,
`source_sha256`, `event_ids` and `object_observation_ids`. It covers one original
observation, not a session, head or deduplicated PR. Independent acquisitions may
share source identities and event references; their original locators, exact
fingerprints and cutoffs remain separate. No missing/extra binding is allowed.

## Proof checks, in order

1. Bound file reads, validate strict JSON and exact key/type/enum sets, and reject
   duplicate keys, bool-as-int values, duplicate IDs, dangling references and
   nonfinite numbers before publishing output. Verify trusted acceptance/inventory
   pins and all cross-file hashes; acceptance must name the exact witness.
2. Validate the original v1 inventory. Match context pins, dataset mode, fixed
   profile, extractor revision/digest, artifact sets/bases and trusted repository
   mappings. Unknown profiles, foreign repository scopes or contradictory maps
   fail the entire operation. No source or network fallback is attempted.
3. Require exactly one witness binding and exactly one output entry for every
   inventory locator, with identical original locator/source/record hashes.
   Missing, extra, duplicate or falsely fingerprinted bindings fail the operation.
4. Independently recompute each entry. Do not trust the producer's disposition,
   clock, reason list or selected event. Independently derive the full candidate
   event/object set for each original immutable repository + PR number + review
   ID from the witness. Binding reference lists must equal those complete sets;
   a caller cannot omit an unfavorable event. Missing original provider/PR
   identity requires empty candidate sets and remains unresolved. Events/objects
   outside all original identity groups fail as out-of-scope input. Reasons form
   a sorted, unique union over every candidate; no favorable event can conceal
   contradictory/invalid candidates.
5. For a source clock to match, require an accepted authoritative event basis,
   allowed event/clock pair, exact immutable repository + scoped PR number +
   review ID, exact publication-key digest, and exact assessment-time head SHA.
   If retained `submitted_commit_sha` exists, it must equal that head as well.
   Missing identity/head/key/creation time stays unresolved. No completion or
   creation time can stand in for the provider's event clock.
6. Require `source.created_at <= event_time <= original_acquisition.acquired_at`
   for **each observation**. An event retrieved later may be used only as
   explicitly later-observed reconstruction within those original bounds; the
   acquisition cutoff is never widened. Provider capture/receipt/attestation
   time is not the event clock. Require event time at or before its artifact's
   observation time; otherwise retain `event_after_source_observation`. Artifact
   observation must be at or before witness observation, which must be at or
   before the operator attestation. Never round a higher-precision timestamp into a
   match; accept at most microseconds (extra fractional digits must be zero).
7. A provider review identity is reused when it maps to distinct publication-key
   digests in inventory or the supplied witness. **Conservative v1 leaves every
   missing-clock entry for that identity unresolved**, even if distinct events
   could plausibly disambiguate it. A future relaxation requires a new agreed
   ruleset; `submitted_at` from an old object is never reused for a later output.
   Conflicting original observations of one source identity also stay unresolved;
   compare every original observation field except `locator`, including opaque
   fingerprints. Different acquisition cutoffs alone are not a source conflict.
8. Candidates that pass every per-event identity, authority and chronology check
   must agree on one exact UTC instant, even before the global reuse/source-conflict
   checks. Different event times stay unresolved: no earliest/latest choice.
   Equal redeliveries may corroborate the same clock, while every original event
   reference and every source observation remains present.
9. An existing frozen clock is copied byte-for-byte into `original_publication_time`
   and is never overwritten. No authoritative event and no conflicting original
   observation: `preserve_frozen`. Valid
   corroborating events at the same instant: also `preserve_frozen`. Conflicting
   or invalid bound authoritative evidence, or conflicting original observation
   fingerprints: `frozen_conflict`, with no recovered
   clock. Bare object metadata cannot override or contradict a source clock.
10. Emit canonical output only after full validation. Both readers recompute
    the same complete output and require exact equality; an arbitrary supplied
    sidecar or `matched` flag cannot establish evidence.

The chronology checks do not filter to a replay date window. This artifact
exports evidence and explicit uncertainty; cohort admission remains parent-owned.

## Output and failure behavior

Exact root fields: `schema_version`, `purpose`, `dataset_kind`, `ruleset`,
`inventory_sha256`, `catalog_sha256`, `original_catalog_sha256`, `witness_sha256`,
`source_acceptance_sha256`, `entries`.

Each entry has `locator`, `record_sha256`, `source_sha256`,
`original_publication_time`, `disposition`, `reconciled_publication_time`,
`event_ids`, `object_observation_ids`, and `reasons`.

| Disposition | Reconciled clock | Meaning |
| --- | --- | --- |
| `matched_event` | Canonical UTC event time | Original clock absent; all event proof checks pass. This is explicitly reconstructed evidence. |
| `unresolved` | null | Missing, unmatched, reused, invalid or conflicting evidence. Absence is not proof of non-publication. |
| `preserve_frozen` | null | Retain the original clock; do not rewrite or normalize its stored spelling. |
| `frozen_conflict` | null | Original clock remains intact but new authoritative evidence cannot safely corroborate it; quarantine the whole affected group. |

Reasons are the exact schema enum; no free-text reasons or result-derived values.
For missing original clocks, `no_authoritative_event` applies when no event is
bound (object observations do not count). Any missing-identity reasons also remain.
For existing frozen clocks, suppress `no_authoritative_event`; if event references
exist, run all their proof checks and refuse contradictions as `frozen_conflict`.
`frozen_clock_conflict` additionally records a valid candidate at a different
instant. Quarantine requirements apply to both `unresolved` and `frozen_conflict`;
the parent cannot silently drop those entries and select the remaining PR rows.

The reason vocabulary has these deterministic triggers. Take the union for each
entry; do not stop at its first failed check. For a frozen clock with no bound
events, only `conflicting_original_observations` applies; missing fields, bare
object observations and later identity reuse do not revise an existing clock.
When events are bound to a frozen clock, run every applicable check below.

| Reason | Trigger |
| --- | --- |
| `missing_provider_review_id` | Original positive provider review ID is absent. |
| `missing_pr_identity` | Original PR number or pull-request UUID is absent. |
| `missing_publication_key` | Original exact publication key is absent. |
| `missing_head_sha` | Original head SHA is absent. |
| `missing_source_created_at` | Original source creation time is absent. |
| `submitted_head_mismatch` | Original non-null submitted commit differs from original non-null head. |
| `no_authoritative_event` | No event candidates are bound to a missing-clock entry. A bare object is insufficient. |
| `source_not_authoritative` | A candidate's accepted artifact basis/origin or event/clock pair does not match the authority table. |
| `provider_identity_mismatch` | Defensive identity check fails; a conforming full-candidate binding cannot produce this. A supplied reference outside the derived set is a whole-operation binding error. |
| `publication_key_mismatch` | Candidate key digest differs from the digest of an available original key. |
| `event_head_mismatch` | Candidate commit differs from an available original head. |
| `before_source_creation` | Candidate event precedes an available original creation time, including when another candidate check also fails. |
| `after_acquisition_boundary` | Candidate event exceeds this observation's original acquisition cutoff. |
| `event_after_source_observation` | Candidate event exceeds its source artifact observation time. |
| `reused_provider_review` | The original immutable provider repo/PR/review identity has more than one distinct non-null key digest across inventory plus witness. |
| `conflicting_event_times` | Candidates passing every per-event check disagree on the UTC instant. Global reuse/source conflicts do not hide this check. |
| `conflicting_original_observations` | Observations of the same original source identity differ in any non-locator field. |
| `frozen_clock_conflict` | A candidate passing every per-event check differs from the existing frozen instant. |

An absent field does not create a comparison-mismatch reason for that field.
All numeric fields must be lexical integers; floats such as `1.0` and booleans
are invalid even when a JSON Schema implementation accepts numeric equality.
Malformed fields fail input validation rather than becoming absent. Missing PR
identity gives empty candidate sets even if a provider review ID alone matches.
For an original frozen clock, a nonempty reason set means `frozen_conflict`;
otherwise preserve its exact original spelling. For a missing clock, a nonempty
reason set means `unresolved`; an empty set requires at least one validated event
and produces `matched_event`.

Input/acceptance/fingerprint failures return static codes
`RECONCILIATION_INPUT_INVALID`, `RECONCILIATION_SOURCE_UNACCEPTED` or
`RECONCILIATION_BINDING_MISMATCH`, without values, paths or exceptions. No partial
output, record truncation or overwrite is allowed. Proposed limits: 256 MiB per
metadata input and output artifact, 100,000 original observations/bindings, 200,000
event/object observations combined, 4,096 artifact references, and 1,000,000 total
candidate references across all bindings. Readers index candidates by identity
and enforce the reference/byte bounds before expanding output. Limit failures are whole-operation
failures. Output is an exclusive atomic file, mode 0600 in a private directory.

Entries sort by the existing locator key: acquisition ID, page SHA, zero-based
record index, source identity. Candidate IDs and reason sets sort by ASCII. Hashes
are lowercase hex. New clocks render exactly `YYYY-MM-DDTHH:MM:SS.ffffffZ`;
original strings remain untouched. Canonical generated JSON uses sorted ASCII
keys, compact separators, ASCII escaping, no floats, and a trailing LF. Hash all
exact bytes including that LF. Consumers hash witnesses as supplied; do not
reserialize inputs just to obtain a matching hash.

## Available source and smallest later access path

The [export models](../../../internal/models/code_review_export.go) retain source
publication keys, provider review IDs and head/submitted-commit identity.
Assessment/legacy rows and
[`SubmitReviewResult`](../../../internal/services/codereview/github_submitter.go)
do not retain a dedicated provider submission clock. `raw.publication_time` already
uses the conservative retained-event resolver; `evidence.provider_submitted_at`
is its alias. There is no alternate frozen clock to uncover by renaming fields.

The smallest existing **complete-evidence source** is an authenticated retained
submitted-review event with its original publication marker and timestamp,
projected by a reviewed machine-only extractor into metadata. The current frozen
resolver already consumes such retained matches, so extracting the same events
cannot be assumed to recover additional unavailable clocks.

A provider review-object metadata export can provide IDs, commit and submission
time as lookup candidates, but cannot prove which publication/body edit that
clock dates. Existing clients also return review body/state and must not be used
in agent context as-is. A future metadata-only provider query needs its own
reviewed field allowlist/access scope. Reused-object/edit cases still need an
actual historical publication-event archive or exact timestamped publisher-event
ledger. No such proof-complete metadata-only dataset or edit adapter is asserted
to exist. If it cannot be supplied, retain unknowns rather than relax this proof.

The outcome-oriented human-review `observed_at` projection is unsuitable: it can
fall back to observation time, is mutable/state-filtered, and lacks exact source
publication-key/head linkage. It is not a publication-clock recovery source.

## Invented fixtures and agreement gate

The fixture directory is
[`scripts/testdata/publication_reconciliation_v1`](../../../scripts/testdata/publication_reconciliation_v1).
All identities, clocks, provenance and source fingerprints are invented. The
fixture acceptance is explicitly synthetic and grants no historical authority.
It includes matched submission/edit events, a bare object clock, missing identity,
wrong key/head, invalid lifecycle bounds, conflicting times, reused review IDs,
a retained frozen clock, and independent acquisition cutoffs. Expected output is
hand-declared data, not generated by a reconciliation implementation.

Before either reader is implemented, agree on: the operator-attestation trust
boundary and fixed source profile; conservative v1 reuse rule; frozen-conflict
quarantine behavior; canonical bytes; and exact fixture/schema vocabulary. The
parent implementation must retain its original `record_metadata` check. Changes
to this contract or later acceptance of a real source are separate from protocol
freeze and holdout access.
