# Offline historical-review custodian contract

This utility provides a result-free inventory followed by a raw selected-record
bundle. It does not normalize safeguards, policies, reviews, labels, or outcomes;
compute replay scores; contact a provider; query a database; or release a cache.
There are no HTTP, database-schema, authentication, or policy changes.

**Real-data execution is not authorized by this implementation.** Both stages
mechanically read mixed archive pages, including bytes outside selected records.
An operator must separately establish the authorized custodian, input freeze,
historical graph provenance, and precise machine-only read boundary. A CLI
acknowledgment is not that authorization. Replay's development/calibration reader
must receive only the selected bundle, without access to mixed archive pages.
Both holdouts remain outside that reader. Use invented fixtures until the separate
boundary is approved; no example here authorizes production or holdout access.

The utility uses Python 3.11+ and the standard library, on POSIX filesystems with
`O_NOFOLLOW`, directory-relative opens, and hard links. Files published by the CLI
have mode 0600. Use an existing private output directory outside the public repo.
Diagnostics contain static messages only, without values, IDs, paths, or parser
exceptions. Failures never silently skip or truncate records.

## Byte identity and provenance

SHA-256 strings are 64 lowercase hexadecimal digits. File digests always hash the
**exact supplied bytes**, including trailing newlines. The following are different
identities and must never be interchanged:

- `original_catalog_sha256`: original archive catalog, verified opaquely without
  parsing it or assuming its historical format.
- `catalog_sha256`: the custodian descriptor below. A derivative descriptor must
  not be represented as the original archive catalog.
- `manifest_sha256` / `acquisition_manifest_sha256`: original acquisition manifest.
- `page_sha256`: original complete wire page.
- `record_sha256`: exact UTF-8 JSON value span of one original `data[]` element,
  excluding surrounding separator whitespace. Nothing is reserialized.
- `source_sha256`: exact UTF-8 JSON value span of that element's `raw.source`.

Generated inventory and receipt JSON uses sorted keys, compact comma/colon
separators, ASCII escaping (`ensure_ascii=True`), no nonfinite numbers, and one
trailing LF. The newline participates in the file digest. Input metadata files
may use different whitespace, but their exact pinned bytes must match. Original
record and source fingerprints are **not** canonicalized or normalized.

The locator is exactly:

```json
{"acquisition_id":"00000000-0000-0000-0000-000000000010","acquisition_manifest_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","page_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","record_index":0,"source_identity":"assessment:00000000-0000-0000-0000-000000003001"}
```

UUIDs and digests in examples are invented. `record_index` is zero-based within
the original page, not an acquisition cursor or global ordinal. Source identity is
`assessment:<UUID>` or `legacy:<UUID>`. Distinct acquisition observations remain
separate even when their source identities and fingerprints agree.

## Stage A: strict inventory

Supply a descriptor with this exact shape. Paths are relative to `--archive-root`;
components cannot be absolute, normalized away, or contain `..`. Each original
acquisition manifest remains unchanged. The default format is the historical
export API's `{version:1, acquisition, pages:[{file,sha256,request_cursor}]}`.
The matching checksum inventory must contain exactly the manifest and its pages.

```json
{
  "schema_version": 1,
  "original_catalog": {"file":"original-catalog.json","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
  "acquisitions": [{
    "manifest":"acquisition-0/archive-manifest.json",
    "manifest_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "checksums":"acquisition-0/SHA256SUMS",
    "checksums_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
  }]
}
```

An acquisition entry may additionally declare `manifest_format`. Omitting it
means `cursor_manifest_v1`, preserving the strict existing contract. The only
other supported value is the explicit opt-in `operator_acquisition_v0`; formats
are never autodetected or used as fallback after validation fails. Both may occur
in one descriptor without combining their acquisition snapshots.

| Format | Manifest evidence | Cursor validation provenance |
| --- | --- | --- |
| `cursor_manifest_v1` | Version 1 envelope; each page retains `request_cursor`. | Captured request cursors must equal the previous response's validated continuation. |
| `operator_acquisition_v0` | Unversioned operator envelope; each page retains `file`, `sha256`, and integer `bytes`. | Reconstructed response-chain continuity only: ordered response positions, acquisition IDs, continuations, counts and EOF are checked. Original request cursors are unavailable; no request receipt is invented. |

The operator envelope has exactly `status`, `acquisition`,
`live_sha_before_acquisition`, `pages`, `unique_source_records`,
`publication_denominator_established`, `split_assignment_performed`,
`holdout_opened`, and `historical_scoring_performed`. Status must be
`acquired_unpartitioned_unscored`, the recorded source revision must be a full
SHA, and all four bookkeeping flags must be Boolean false. These are retained
archive claims, not authorization or proof about a subsequent operation.
`unique_source_records` must be an integer equal to this acquisition's declared
record count, never a cross-acquisition review denominator. Page byte lengths
must fit existing bounds and exactly match the bytes read during inventory and
selected materialization. Unknown fields, formats, types, flags, contradictory
sizes, or cursor discontinuities fail closed.

For an operator manifest, add this field to its descriptor entry:

```json
"manifest_format": "operator_acquisition_v0"
```

Do not rewrite or wrap an original manifest to make it look like version 1.
`manifest_sha256` and every locator's `acquisition_manifest_sha256` continue to
hash the exact original manifest. The pinned descriptor's format tag provides
metadata-only cursor-provenance classification; retain the descriptor with the
inventory and receipt. Inventory, locator, selection and bundle schemas remain
unchanged. No original source, record or page fingerprint is reserialized.

The descriptor's acquisition inventory must be approved against the frozen archive
by its custodian. Hashing an opaque original catalog cannot independently prove
that a descriptor lists every acquisition in an arbitrary catalog format.
Acquisition/page counts and cursor chains are validated within every listed
acquisition. This does not assert publication-cohort completeness or a replay
denominator. Missing acquisitions cannot be discovered by inspecting outcomes.

Inventory root keys are exactly:

```text
schema_version = 1
catalog_sha256
original_catalog_sha256
acquisitions[]
observations[]
```

Each acquisition header contains exactly `id`, `manifest_sha256`, `schema_version`,
`filters`, `acquired_at`, `expires_at`, and `consistency`. Filters retain repository
ID, inclusive `from`, exclusive `until`, `time_basis`, and explicit unresolved
publication inclusion. No coverage or other outcome-derived count is copied.
Creation/completion windows can overlap by source identity; every original
acquisition retains its independent boundary and provenance.

Each observation contains exactly the following fields. All nullable values stay
null when not retained; no mutable current state is substituted.

| Fields | Meaning |
| --- | --- |
| `locator`, `record_sha256`, `source_sha256` | Exact original observation location and byte fingerprints. |
| `source_kind`, `source_id` | Assessment or legacy identity; must agree with locator and raw source ID. |
| `repository_id`, `pull_request_id`, `session_id`, `metadata_id`, `assessment_id` | Retained UUID references. Repository ID must match the acquisition. |
| `source_assessment_id`, `previous_assessment_id`, `previous_published_assessment_id`, `superseded_by_assessment_id`, `superseded_by_session_id` | Retained lineage references; no automatic record expansion. |
| `generation` | Retained positive integer or null; legacy does not invent a generation. |
| `head_sha`, `base_sha`, `submitted_commit_sha`, `base_ref` | Source capture only. Missing is null. SHAs are lowercase full 40/64-character hashes. |
| `created_at`, `completed_at`, `superseded_at` | Source lifecycle clocks; never publication-time substitutes. |
| `repository_full_name`, `repository_name_availability`, `pr_number` | Captured name when present, otherwise acquisition-time mirrored name marked reconstructed; missing mapping remains unavailable. PR number comes from the mirrored PR matching the source UUID. |
| `provider_review_id`, `publication_key` | Retained positive provider ID and assessment publication key / legacy review output key. Nonpositive provider IDs/PR numbers remain null (unknown), with original bytes fingerprinted unchanged. Neither provider ID nor head uniquely identifies a publication. |
| `publication_time`, `publication_time_availability`, `publication_time_provenance` | Already-bound frozen export clock, marked reconstructed with fixed `frozen_export_binding` provenance; otherwise null/unavailable/`not_bound_in_frozen_export`. No provider-body inspection or clock rebinding. Original detailed provenance remains in the fingerprinted raw record. |
| `legacy_alias_ids` | Sorted, unique legacy UUIDs only; nested alias results are never projected. |
| `stack_availability` | Always `unavailable`: the archive export does not retain historical stack membership. |

No `input_sha256`, normalized-input byte length, raw/evidence/body/policy/safeguard
object, decision, acceptability, review status, coverage, failure reason, findings,
provider observation, receipt, merge outcome, or outcome aggregate appears in the
inventory. A JSON framer validates syntax, duplicate keys, escapes and depth while
skipping opaque result values; only allowlisted scalar fields are decoded.

## Historical graph and replay-owned authorization files

Graph shape is strict. Every PR belongs to at most one component, scoped by
immutable repository and PR UUIDs. Each component must explicitly assert complete
membership, including verified singletons. A missing member never means singleton.

```text
{schema_version:1, groups:[{
  repository_id:UUID, stack_id:string, pull_request_ids:[UUID...], complete:true,
  provenance:{availability:"recorded"|"reconstructed",
    kind:"historical_graphite_export"|"historical_git_graph"|"historical_operator_attestation",
    effective_at:RFC3339, observed_at:RFC3339, source_sha256:SHA256}
}]}
```

`effective_at <= observed_at`. An authoritative historical graph/attestation must
be supplied independently; syntax and hashes cannot prove historical truth.
Current base refs and mutable changeset links are insufficient. Components
containing PRs absent from the bound inventory are quarantined. A provenance
source digest is a reference to retained historical evidence, not permission to
fetch or inspect that evidence.

The validator supports only `chronological-pr-stack-outcome-censor/v1`, protocol
TOML version 3 / replay decision version 2, the explicit July 15 through October
19 exclusive 2026 UTC window (96 days), conservative unknowns, disabled holdouts,
and the exact approved split rules. The top-level protocol `status` must be
explicitly `draft` or `frozen`; this validation does not freeze the protocol or
authorize any data access. Missing or unsupported status fails before page reads.
Unsupported rules fail closed.

It independently validates the replay-owned assignment of every observation:
whole PR/stack grouping, identity/alias connectivity, conflicting observation and
shared-provider quarantine, unknown publication/stack quarantine, chronological
first-publication ordering, immutable repository UUID then numeric PR tie order,
latest whole groups reserved for the 20% model holdout, and 60/20/20 of remaining
PRs without splitting groups. Original partition starts are fixed before overlap
quarantine, with no refill. A whole group is quarantined when its last publication
reaches or crosses the next original partition start. **Reviews are retained
when only their 14-day outcome window crosses that boundary.** Outcome censoring
and proof of full observation are replay-owned future normalization, not Stage A
or B work. The validator does not interpret outcome labels.

Partition file:

```text
{schema_version:1,ruleset,catalog_sha256,inventory_sha256,stack_graph_sha256,
 protocol_sha256,assignments:[{locator,partition}]}
```

Assignments must cover every inventory observation once, including quarantine and
both holdouts, and match independent recomputation. Their labels are metadata,
not review results. A caller's `development` label alone cannot authorize access.
The original catalog digest is bound transitively by the inventory file digest.

Selected-set file:

```text
{schema_version:1,ruleset,catalog_sha256,inventory_sha256,stack_graph_sha256,
 protocol_sha256,partition_sha256,
 partitions:["development","calibration"],selected:[{locator,partition}]}
```

`partitions` may instead contain only development or only calibration, in that
order. Every observation assigned to a requested partition must be explicitly
selected, exactly once. Subsets, omitted PR histories, missing repeated acquisition
observations, holdout/embargo/quarantine assignments, and extra entries fail.
The operator independently pins the exact selection-file digest. These digests
provide integrity; supplying a self-chosen digest is not a separate authorization
or proof of original cohort completeness.

## Stage B: atomic raw selected bundle

Before reading any page, Stage B validates all metadata inputs, catalog/manifests,
independently recomputed assignments, and the exact complete selected set. It then
opens only pages containing selected observations. Those mixed pages are still
mechanically read in full for framing/checksums. It reprojects each selected
record to verify exact inventory agreement before copying its unchanged span.
It does not reread pages containing no selected records or claim those unselected
pages were reverified at materialization time.

Output is one deterministic, uncompressed ZIP with only generated members
`observations/000000.json`, etc. and `receipt.json`. Member timestamps are fixed;
regular-file modes are 0600. Each observation member is exactly the original
record JSON span. The receipt has exactly:

```text
schema_version:1
ruleset
partitions
selection_sha256, catalog_sha256, original_catalog_sha256, inventory_sha256
stack_graph_sha256, protocol_sha256, partition_sha256
acquisitions: original filtered inventory headers
observations:[{member,locator,record_sha256,record_bytes,source_sha256}]
```

These are selected-output byte lengths/checksums, not normalized projections.
Record/source bytes and acquisition provenance stay intact. Each original
observation has its own receipt entry. No latest-wins, cross-acquisition dedup,
lineage expansion, time-filter fallback, partial export, or live substitution.

The completed file is fsynced and atomically hard-linked into its final name
without replacing any existing file/symlink. Failed preparation removes its
private temporary file; a cleanup failure emits a static warning. A bundle-wide
checksum is recorded separately after publication, since a receipt inside a ZIP
cannot hash its own enclosing bytes. Readers must validate the external bundle
checksum, metadata receipt and exact safe member set before opening results;
reject compressed/duplicate/unsafe/oversized/extra members.

Released/expired acquisition payloads cannot be refetched as their original
snapshots. Offline operations use the preserved original archive; a new API
acquisition is a different snapshot and is never substituted here.

## Bounds and failure behavior

Catalogs, original catalogs, manifests, checksum inventories and protocol files:
8 MiB each. Inventories, graph/partition/selection files and receipts: 256 MiB each.
Wire pages: 512 MiB each, at most 100 records/page, JSON depth 64. Acquisition
source count: 20,000; descriptor acquisition count: 256; total observations:
100,000; total page bytes read per operation: 2 GiB. Materialized ZIP including
its receipt: at most 1 GiB. A bound failure fails the entire operation, never
silently drops or truncates a record. SHA and timestamp validation is strict;
nonzero sub-microsecond timestamp precision is rejected rather than rounded.
Archive components must be regular single-link files, without symlinks or path
traversal. Inputs are bounded even when hashes are attacker-controlled.

## Synthetic validation and commands

From a checkout, generate a **new invented fixture** using
`test_code_review_export_custodian.write_synthetic_fixture(new_directory)`, with
`scripts` on Python's import path. This test factory never discovers operator
archives and refuses an existing directory. It returns catalog/selection hashes.
Ten weekly PRs have PR-level assignments 4/2/2/2; the independent extra observation
of PR 1 makes observation-level assignments 5/2/2/2. Development plus calibration
therefore emits seven original observations. All result fields are synthetic
sentinels. The parent replay repository can use this factory for a separate
cross-repository golden check without depending on this checkout in its CI.

The following commands are templates for that fixture. All variables must refer
to its new private directory and exact hashes. They are **not instructions to
run on historical data before the separate custodian authorization**.

```bash
python3 -B scripts/code_review_export_custodian.py inventory \
  --archive-root "$SYNTHETIC_DIR" --catalog "$SYNTHETIC_DIR/catalog.json" \
  --catalog-sha256 "$CATALOG_SHA256" --output "$SYNTHETIC_DIR/checked-index.json" \
  --acknowledge-mixed-page-read

python3 -B scripts/code_review_export_custodian.py materialize \
  --archive-root "$SYNTHETIC_DIR" --catalog "$SYNTHETIC_DIR/catalog.json" \
  --catalog-sha256 "$CATALOG_SHA256" --inventory "$SYNTHETIC_DIR/inventory.json" \
  --graph "$SYNTHETIC_DIR/graph.json" --protocol "$SYNTHETIC_DIR/protocol.toml" \
  --partition "$SYNTHETIC_DIR/partition.json" --selection "$SYNTHETIC_DIR/selection.json" \
  --selection-sha256 "$SELECTION_SHA256" --output "$SYNTHETIC_DIR/selected.zip" \
  --acknowledge-mixed-page-read

python3 -B -m unittest discover -s scripts -p 'test_*code_review_export*.py'
```
