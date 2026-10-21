# Design: Result-free publication clock reconciliation v1

> **Status:** Partially Implemented | **Last reviewed:** 2026-10-21

Producer/consumer agreement is complete. The offline 143 producer and synthetic
semantic tests are implemented; the independent consumer and source extractor are
not implemented here. No provider query or real reconciliation has run.
Inventory/materializer v1 stays unchanged. Contract agreement neither freezes a
replay protocol nor authorizes execution.

## Purpose and boundaries

Reconstruct an unavailable source-publication clock only from a verified original
submission event. **Publication edits cannot supply recovered clocks in v1.**
Retain edits, incomplete evidence and contradictions as explicit uncertainty.
Unknown never means proven unpublished. Export evidence without calculating
cohort membership, ordering, partitions, denominators, scores or verdicts.

Every original observation, locator, fingerprint and acquisition cutoff survives.
Independent acquisitions remain independent snapshots. The consumer must verify
original selected-record `record_metadata` equality against the original inventory
before applying a separate effective-clock view. Unresolved/conflicting entries
remain available for whole-PR/stack quarantine; no silent per-record drops.

No database schema, migration, API route, HTTP, authentication/RBAC or policy change
is proposed. The producer is offline: explicit input files only, no network
fallback, live source expansion or result access. Artifact references are opaque
fingerprints, never fetch URLs. Both holdout results stay sealed and unscored.

## Trust decisions and fixed profile

1. **Integrity:** strict grammar, exact supplied file bytes, trusted expected
   hashes and cross-file bindings agree. Hashes include the trailing LF.
2. **Source acceptance:** an independent operator accepts a reviewed extractor
   revision/file digest, fixed source profile, repository map, exact witness and
   source coverage. The expected acceptance-file hash arrives through a trusted
   invocation context; an extractor cannot accept its own output. A profile name
   or caller-supplied `matched` flag grants no authority.
3. **Execution authorization:** permission to query, read archives/results or use
   holdouts comes from the human/workflow. Acceptance grants no such permission.

No signing service is introduced. A hash proves integrity, not historical truth.
An inspectable metadata witness lets both readers recompute the same bindings;
the operator still attests faithful projection of authenticated original sources
and coverage. A consumer unable to inspect the sealed original cannot independently
recheck its authentication, body extraction or completeness. This limitation is
explicit. No proof-complete source or edit adapter is asserted to exist.

The only supported ruleset is `source-publication-clock/v1`; the only profile is
`publication-event-metadata/v1`. This profile is **GitHub-only**. Provider numeric
IDs are positive signed-int64 GitHub IDs, and review identity is immutable. Other
providers or arbitrary profiles need a separate agreed contract.

## Artifacts and grammar

[`publication-reconciliation-v1.schema.json`](future/publication-reconciliation-v1.schema.json)
defines exact object keys, primitive types, enum vocabularies and bounds. Unknown
fields are errors. Profile/ruleset/dataset selectors are grammatically strings;
the fixed accepted values are enforced at authority stage 4 below. This separates
well-formed unsupported authority from malformed input. Semantic checks below are
mandatory in addition to JSON Schema; `format` annotation alone is insufficient.

| Artifact | Contract |
| --- | --- |
| `inventory.json` | Original strict schema v1; independently pinned exact bytes. |
| `metadata-witness.json` | Result-free events, object observations, source provenance and a binding for every original locator. |
| `source-acceptance.json` | Independently pinned operator acceptance of the exact witness, extractor, repository map, source artifacts and coverage. |
| `reconciliation.json` | Complete deterministic output; consumer recomputes it from the preceding inputs and compares exact canonical bytes. |

Every new artifact has `schema_version:1`, `purpose`, `dataset_kind`, `ruleset`,
`inventory_sha256`, `catalog_sha256`, and `original_catalog_sha256`. Purposes are
respectively `source_publication_metadata_witness`,
`accept_publication_metadata_source`, and `publication_clock_reconciliation`.
The original inventory has no dataset marker; its existing v1 schema is unchanged.

Implemented invocation (Python 3.11+ standard library only):

```text
python3 -B scripts/code_review_export_reconciliation.py --mode historical --inventory INVENTORY --inventory-sha256 TRUSTED_SHA
          --witness WITNESS --source-acceptance ACCEPTANCE
          --source-acceptance-sha256 OPERATOR_ACCEPTED_SHA --output NEW_FILE
```

Expected hashes come from an accepted handoff, not untrusted files hashed just to
satisfy flags. Historical mode requires `historical` in every marked artifact;
synthetic mode requires `synthetic` in every marked artifact. Reject either
cross-mode direction. The synthetic fixture pin confers no historical authority.

### Acceptance, repositories and coverage

Acceptance adds `witness_sha256`, `source_profile`, `extractor_revision`,
`extractor_code_sha256`, `repository_bindings`, `artifacts`, and
`operator_attestation`. Pin a full reviewed Git SHA and exact extractor file hash.
The attestation has `attestor_id`, `attested_at`, `verification_receipt_sha256`,
`permitted_use:"publication_clock_reconstruction_only"`,
`confers_execution_authorization:false`, and
`limitation:"operator_attested_projection_original_not_reverified_by_consumer"`.
Receipt hashes are opaque acceptance evidence, not permission tokens.

The repository map is a total **bijection onto its declared provider IDs**: its
UUID set equals the inventory acquisition-filter repository UUID set, and no UUID
or provider ID repeats. No missing, extra, contradictory or many-to-one maps.
Display names/current heads are not identity substitutes. Every event, object and
coverage identity must belong to a complete original repo/PR/review group. These
source/map failures are `RECONCILIATION_SOURCE_UNACCEPTED` (stage 4). Distinct known
PR numbers for one `(repository_id, provider_review_id)` already inside the
original inventory are instead bad original input: `RECONCILIATION_INPUT_INVALID`
at stage 3. Contradictory newly supplied source identities remain stage-4 authority
failures. A missing PR number does not itself contradict a known number.

Each accepted artifact has `artifact_id`, `sha256`, `basis`,
`verification_receipt_sha256`, and nullable `coverage`. IDs/hashes must equal the
witness artifact set. Basis/origin combinations are exactly:

| Basis | Witness artifact origin |
| --- | --- |
| `authenticated_provider_submission` | `provider_submission_event` |
| `verified_historical_publication_edit` | `publisher_edit_event` |
| `provider_object_metadata_only` | `provider_review_object` |

A different combination is an authority-stage failure for the whole operation.
Object-only artifacts must have null coverage; they cannot assert event coverage.
For event artifacts, null coverage is allowed but proves no completeness.

Non-null coverage has `identities` (repo/PR/review triples), `from` (timestamp or
null), `until` (timestamp), and `projection` (`complete` or `partial`). It declares
a **closed** source coverage interval. Null `from` means the entire lifetime of
each named provider review identity, including events before this source row was
created. A finite `from` does not prove lifetime completeness. Coverage is a
completeness claim, not a filter: all supplied observations remain visible even
outside that interval. Require finite `from <= until <= artifact.observed_at`.

The operator attests that `complete` projects every original submission or edit
in that identity/interval for the artifact's basis, including same-key re-renders,
redeliveries and contrary keys/heads/clocks. Missing, unprojectable or unverified
history requires `partial`/null coverage; it must not be silently omitted from a
complete projection. A positive event alone never proves full coverage.

A missing original clock can match only when **both event classes** have accepted
complete lifetime coverage through `witness.observed_at`: at least one submission
artifact and one edit artifact must cover that exact identity with null `from`
and `until >= witness.observed_at`. Each artifact observation must be at or before
witness observation, so a covering artifact necessarily attests that same cutoff.
This intentionally strict coherent-cutoff requirement may be unavailable in real
sources; do not widen it using later observation/receipt times. Otherwise retain
`source_coverage_incomplete`. The reader checks declared coverage; faithfulness to
the original remains an explicitly accepted operator claim, not independent proof
from the absence of rows in the witness. No events still means unknown.

### Witness facts and exact authority tuples

Witness adds `source_profile`, `extractor_revision`, `extractor_code_sha256`,
`observed_at`, `artifacts`, `events`, `object_observations`, and `bindings`.
Each artifact has `artifact_id`, `sha256`, `origin_kind`, `observed_at`.
No original body, review state, decision, label, finding, policy, safeguard count,
free-text note, URL, credential or exception is allowed in these artifacts.

Each event has `event_id`, `artifact_id`, `provider_repository_id`, `pr_number`,
`provider_review_id`, `commit_sha`, `publication_key_sha256`, `event_kind`,
`clock_kind`, `event_time`, and `publication_link_basis`. The exact tuples below
are exhaustive. A well-formed event using known vocabulary but absent from this
table contributes per-entry `source_not_authoritative`; it is never a clock match.
Unknown vocabulary is malformed input (stage 1). Basis/origin mismatch remains
a whole-operation stage-4 failure, as defined above.

| Basis | Origin | Event kind | Clock kind | Link basis | Clock eligibility |
| --- | --- | --- | --- | --- | --- |
| `authenticated_provider_submission` | `provider_submission_event` | `review_submitted` | `provider_submitted_at` | `provider_event_output_marker` | Submission candidate |
| `authenticated_provider_submission` | `provider_submission_event` | `review_submitted` | `provider_submitted_at` | `publisher_event_exact_publication_key` | Submission candidate |
| `verified_historical_publication_edit` | `publisher_edit_event` | `review_body_edited` | `provider_edit_event_at` | `provider_event_output_marker` | Conflict evidence only |
| `verified_historical_publication_edit` | `publisher_edit_event` | `review_body_edited` | `provider_edit_event_at` | `publisher_event_exact_publication_key` | Conflict evidence only |

For submissions, `commit_sha` is the commit retained by that original provider
submission. For edits it is the original provider review commit, **not** an
assessment head copied into an edit receipt. An edit cannot establish a new
submission commit. Every bound `review_body_edited` event contributes
`edit_basis_not_supported_v1`, even a same-key re-render or same-instant event.
Thus omitting the original submission cannot turn an edit into a v1 match; lack
of complete lifetime coverage also blocks a submission-only partial witness.

Hash the exact source publication key's UTF-8 bytes, without trimming/case folding.
The accepted extractor must prove the key-to-event link from the original event
marker or an authoritative event-linked publisher record. It cannot copy a wanted
inventory key onto an object's old `submitted_at`. Existing markers hash trimmed
keys: a different untrimmed source key remains unmatched. Distinct exact digests
trigger conservative reuse ambiguity, including whitespace variants; the reason
is an ambiguity indicator, not a claim that a new publication actually occurred.

Each object observation has `observation_id`, `artifact_id`, immutable repo/PR/
review IDs, `commit_sha`, and nullable `object_submitted_at`. It has no source key.
An object clock never supplies or contradicts a source-publication clock.

Bindings contain the original five-field `locator`, `record_sha256`,
`source_sha256`, `event_ids` and `object_observation_ids`. Require exactly one per
original observation; never deduplicate by session, PR, head or review ID.

## Deterministic validation and error precedence

Both readers execute the following numbered stages. The **first failing stage**
wins. A later-stage failure cannot change that code. No values, paths or exception
text enter errors; use only the three static codes. Independent failures in one
stage have the same code except stage 2, whose exact pin order is specified.

| Stage | Checks, in order | Failure |
| --- | --- | --- |
| 1 | Bounded reads; UTF-8/JSON framing; depth; duplicate keys; exact key/type/grammar/enum sets; all numeric tokens lexical integers (no `1.0`, exponents, bool-as-int, NaN/Infinity); date/calendar/precision syntax; duplicate event/object/artifact IDs within their namespaces; per-file/array bounds; existing strict inventory-v1 validation. | `RECONCILIATION_INPUT_INVALID` |
| 2a | Compare exact acceptance-file bytes against the trusted expected acceptance SHA. | `RECONCILIATION_SOURCE_UNACCEPTED` |
| 2b | Compare exact original inventory bytes against the trusted expected inventory SHA. | `RECONCILIATION_INPUT_INVALID` |
| 3 | Cross-file inventory/catalog/original-catalog pins; acceptance witness SHA; output witness/acceptance pins when verifying output; extractor revision/digest consistency; equal artifact sets/digests; dangling event/object artifact or binding references; artifact observation <= witness observation <= attestation; valid coverage intervals; contradictory known PR numbers within the original inventory for one repo UUID/review ID; combined/reference expansion bounds. | `RECONCILIATION_INPUT_INVALID` |
| 4 | Fixed supported profile/ruleset/dataset and requested mode in all marked inputs; total/injective trusted repository map; in-scope event/object/coverage identities; consistent review-to-PR identity; accepted basis/origin pairing; object-only coverage is null. | `RECONCILIATION_SOURCE_UNACCEPTED` |
| 5 | Exact original-locator coverage (no extra, omitted or duplicate locators), original source/record fingerprints; complete candidate reference sets; exact canonical recomputed output equality, including frozen strings. | `RECONCILIATION_BINDING_MISMATCH` |

After stage-5 binding checks pass, compute the complete entries below and then
perform any supplied-output comparison as the last check of stage 5. Entry
reasons are evidence outcomes, not operation failures.

Malformed hashes or malformed selectors fail stage 1. A well-formed stale trusted
acceptance pin fails 2a, even if the inventory pin is also stale. A stale witness
hash fails 3 before an unsupported profile at 4. Mutation fixtures refresh synthetic
pins when isolating later stages; doing so is test preparation, never historical
source acceptance. An unavailable output path, existing output file or write/
size failure returns `RECONCILIATION_INPUT_INVALID` with no partial publication.

New metadata timestamps use the portable lexical subset of the existing
[`timestamp`](../../scripts/code_review_export_metadata.py) helper described
here. The reference runtime is **Python 3.11 or later**. Other implementations must
enforce the same limits even if their native parser accepts more:
`YYYY-MM-DDTHH:MM:SS[.fraction](Z|+HH:MM|-HH:MM)`, uppercase T/Z, years **0001-9999**,
valid calendar dates, time hour 00-23, minute/second 00-59, no leap seconds,
offset hour **00-23** and offset minute **00-59**. Both signed-zero offsets are UTC.
Fraction length is 1-9; digits after the sixth must all be zero. Normalize to UTC
only for comparisons, and require the normalized value also to remain within
years 0001-9999. Reject overflow, malformed values or nonzero higher precision at
stage 1; never truncate or round. New clocks render exactly six digits and Z.

The JSON Schema enforces these lexical bounds. Both readers must additionally
validate calendar and UTC-range semantics. With Python, apply the new lexical
guard **before** calling the existing helper; the helper alone can normalize
out-of-range offset minutes. Optional format annotations do not replace this.
The original inventory still uses its unchanged v1 validator/helper, and copied
`original_publication_time` is an exception to the new lexical subset: accept its
existing validated v1 spelling and validate it with the v1 helper at stage 1.
Its instant comparisons use the v1 helper; byte-for-byte equality with the original
field is checked at stage 5. For portability, v1 offsets normalize as
`sign × (hours × 60 + minutes)` minutes; their magnitude must be below 24 hours,
and the resulting UTC instant must remain in years 0001-9999. Never rewrite/reject
an otherwise valid original merely because the new-fact grammar is stricter.

## Per-observation computation

1. Derive all candidate events/objects sharing the original mapped provider repo,
   PR number and review ID. A missing PR number, pull-request UUID or review ID
   requires empty candidates. A binding must equal these full sets, never a
   favorable subset. All original observations and every candidate reference stay.
2. Compare observations of one `source_identity` using **only** the exact fields
   below, by original JSON value equality (no normalization). Any difference gives
   `conflicting_original_observations`. These are source-derived evidence; a
   different opaque source fingerprint conservatively signals changed source data.

```text
source_kind, source_id, source_sha256, repository_id, pull_request_id, session_id,
metadata_id, assessment_id, source_assessment_id, previous_assessment_id,
previous_published_assessment_id, superseded_by_assessment_id,
superseded_by_session_id, generation, provider_review_id, publication_key,
head_sha, base_sha, base_ref, submitted_commit_sha, created_at, completed_at,
superseded_at
```

Exclude `locator`, `record_sha256`, `publication_time`, its availability/provenance,
`repository_full_name`, `repository_name_availability`, `pr_number`, `legacy_alias_ids`
and `stack_availability`. Record hashes, retained provider observations, frozen
clocks and mirror-derived annotations may legitimately differ across acquisitions.
They still retain their own exact fingerprints; exclusion never rewrites them.
This reconciler comparison is a separate overlay. It cannot clear the custody
partitioner's stricter pre-existing conflicting-observation quarantine, admit a
record/group, change original metadata equality, or alter the replay protocol.
If two **non-null** original clocks for one source identity differ by UTC instant,
add `conflicting_original_clocks`. Null versus known is not itself a conflict;
a later known clock is never copied to an earlier missing observation.

3. Collect exact non-null key digests across inventory plus witness by mapped
   provider repo + review ID. More than one digest means `reused_provider_review`.
   An inventory row missing PR number/UUID still contributes its key to this
   conservative repo/review ambiguity check, but cannot acquire candidates or a
   clock. Do not cross repositories. Objects have no key and contribute none.
4. Check each bound event independently. Union all applicable reasons, even when
   another check fails. An absent original field gives its missing reason, not a
   comparison-mismatch reason. Candidate time must be at or after original source
   creation, at or before **this observation's** acquisition cutoff, and at or
   before its artifact observation. Cutoff comparison is inclusive. Later retrieval
   permits explicitly reconstructed evidence, never a widened acquisition cutoff.
5. An eligible submission has an exact authority tuple, matching key/head,
   retained submitted commit equal to head if present, all required original
   fields, and all per-event chronology checks. Distinct eligible submission times
   give `conflicting_event_times` on **both frozen and missing-clock paths**,
   before global reuse/source/coverage checks. Equal redeliveries corroborate
   without losing any event reference.
6. Frozen path: copy original clock unchanged. Always check original
   `submitted_head_mismatch`, global source/clocks conflicts and reuse, **even with
   no bound events**. If events exist, also union all original missing-field,
   submitted-commit and per-event reasons, `conflicting_event_times` from step 5,
   and any eligible submission at a different instant (`frozen_clock_conflict`).
   A submitted-head contradiction makes a submission ineligible, so that candidate
   alone adds `submitted_head_mismatch`, not `frozen_clock_conflict`.
   Do not add `no_authoritative_event` or coverage reasons
   on this path: absent/partial new evidence does not refute an existing clock.
   Any resulting reason means `frozen_conflict`; otherwise `preserve_frozen`.
   Same-instant corroboration preserves only when **all** checks are empty; other-
   key, edit or invalid evidence still causes conflict. Adding a corroborating event
   cannot erase an existing reuse conflict. Object observations have no effect.
7. Missing-clock path: union original missing-field/submitted-commit reasons,
   global conflicts/reuse, every event reason, and coverage failure (evaluate
   coverage when original repo/PR/review identity and creation time are available).
   If no events are bound, add `no_authoritative_event`. A nonempty reason set is
   `unresolved`; empty reasons require eligible submissions at one instant plus
   complete lifetime coverage of both event classes, producing `matched_event`.

## Output, reasons and bounds

Output adds `witness_sha256`, `source_acceptance_sha256`, and `entries`. Each entry
has `locator`, `record_sha256`, `source_sha256`, `original_publication_time`,
`disposition`, `reconciled_publication_time`, `event_ids`, `object_observation_ids`,
and sorted unique `reasons`.

| Disposition | Reconciled time | Original time | Reasons |
| --- | --- | --- | --- |
| `matched_event` | Canonical eligible submission time | null | empty |
| `unresolved` | null | null | nonempty |
| `preserve_frozen` | null | Exact original string | empty |
| `frozen_conflict` | null | Exact original string | nonempty; whole-group quarantine |

| Reason | Exact trigger |
| --- | --- |
| `missing_provider_review_id` | Original positive review ID absent. |
| `missing_pr_identity` | Original PR number or pull-request UUID absent. |
| `missing_publication_key` | Original exact source key absent. |
| `missing_head_sha` | Original assessment-time head absent. |
| `missing_source_created_at` | Original source creation time absent. |
| `submitted_head_mismatch` | Non-null submitted commit and head differ. |
| `no_authoritative_event` | Missing clock and empty event candidate list; objects do not count. |
| `source_not_authoritative` | Known-vocabulary event tuple is absent from the exhaustive authority table. |
| `publication_key_mismatch` | Candidate digest differs from an available original key's exact UTF-8 digest. |
| `event_head_mismatch` | Candidate provider commit differs from an available original head. |
| `before_source_creation` | Candidate event precedes an available original creation instant. |
| `after_acquisition_boundary` | Candidate event exceeds this observation's original cutoff. |
| `event_after_source_observation` | Candidate event exceeds its artifact observation instant. |
| `edit_basis_not_supported_v1` | Any bound `review_body_edited` event, including same-key edits. |
| `source_coverage_incomplete` | Missing clock, complete original identity/creation, and either event class lacks complete lifetime coverage through witness cutoff. |
| `reused_provider_review` | Multiple exact non-null key digests for mapped provider repo/review ID; conservative ambiguity, not proof of actual reuse. |
| `conflicting_event_times` | Eligible submission candidates disagree on UTC instant, on both frozen and missing-clock paths. |
| `conflicting_original_observations` | Same source identity differs in the exact source-comparison field set. |
| `conflicting_original_clocks` | Same source identity has different non-null original UTC clock instants. |
| `frozen_clock_conflict` | Eligible submission differs from this original frozen instant. |

Unresolved/frozen-conflict entries must remain in whole-PR/stack quarantine. Do not
filter them away or infer publication absence. Chronology checks admit no replay
cohort; parent owns eventual selection and all result normalization.

Limits: 256 MiB per metadata input/output artifact, depth 64, 100,000 original
observations/bindings, 200,000 combined event/object observations, 4,096 source
artifacts, 256 repository bindings, 1,000,000 total coverage identity references
and 1,000,000 total event/object references across bindings. Fail the whole
operation on overflow, including output expansion; no truncation/record dropping.
Index by identity before expansion. No DB queries are added or existing acquisition
row/byte/query limits relaxed. Output is atomic/exclusive, mode 0600 in a private
directory; never overwrite an output or expose a partially written file.

Entries use the existing locator sort: acquisition ID, page SHA, zero-based record
index, source identity. ID/reason arrays sort by ASCII, hashes are lowercase hex.
Generated JSON uses sorted ASCII keys, compact separators, ASCII escaping, no
floats, trailing LF. Compare exact canonical output bytes; hash input bytes as
supplied, never reserialize an input to obtain a matching pin.

## Available source and agreement gate

[Export models](../../internal/models/code_review_export.go) retain source keys,
provider review IDs and head/submitted-commit identity. Assessment/legacy rows and
[`SubmitReviewResult`](../../internal/services/codereview/github_submitter.go)
do not retain a dedicated submission clock. The frozen `raw.publication_time`
already uses retained-event resolution; `evidence.provider_submitted_at` aliases it.
There is no alternate frozen clock to uncover by renaming fields.

An authenticated retained submission event with its original marker is the
smallest positive event source already available in code. The current resolver
already consumes such matches. Re-extraction may recover nothing, and such an
event alone does **not** establish complete source history required by this draft.
Bare provider object IDs/commit/`submitted_at` are lookup candidates only; existing
clients also return bodies/state and must not run in agent context as-is. Later
metadata-only access needs reviewed field allowlists, source coverage and separate
human authorization. No complete submission+edit history dataset/adapter is known
to be available. If coverage is absent, preserve unknowns. Outcome-oriented mutable
human-review `observed_at` may be receipt time and is not a recovery source.

Invented fixtures are in
[`scripts/testdata/publication_reconciliation_v1`](../../scripts/testdata/publication_reconciliation_v1).
They retain hand-declared expected outputs and concrete negative mutation recipes.
The producer now reproduces their complete hand-declared semantic output and
error precedence in synthetic tests. The independent consumer must still reproduce
those expectations separately. Structural grammar/hash checks alone do not prove
semantic agreement between two readers. Source acceptance, protocol freeze, real source
acquisition, cohort admission and holdout access remain separate gates.


## Producer delivery and remaining work

The producer lives in [`code_review_export_reconciliation.py`](../../scripts/code_review_export_reconciliation.py).
It validates all five stages, computes all observations, and optionally verifies
an independently supplied sidecar through `--verify-output`. It publishes only a
complete canonical sidecar, exclusively at a new path in an operator-owned private
directory. See [CLI usage](../../scripts/code_review_export_reconciliation.md).
The existing inventory, materializer and custody selection modules are unchanged.

The independent consumer, reviewed source extractor, accepted historical coverage,
real source acquisition/execution and any replay use remain outside this delivery.
No current graph, protocol or custody decision consumes the new output. A matching
synthetic fixture is neither historical source acceptance nor holdout permission.
