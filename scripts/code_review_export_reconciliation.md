# Offline publication-clock reconciliation producer

This tool reads only three explicit result-free metadata files: original v1
inventory, source witness and operator acceptance. It reads no archive pages,
original source artifacts, provider APIs, outcomes or holdouts. It does not modify
or call the inventory/materializer/partition workflows. POSIX, Python 3.11+ and the
standard library are sufficient.

The [agreed contract](../docs/design/publication-clock-reconciliation-v1.md) and
[JSON Schema](../docs/design/future/publication-reconciliation-v1.schema.json)
define the complete inputs, source authority, five validation stages and output.
All original observations remain; distinct acquisitions retain their own cutoffs.
No absence of evidence establishes an unpublished review.

For the **invented fixture only**, create a private output directory and run:

```bash
private_dir=$(mktemp -d)
python3 -B scripts/code_review_export_reconciliation.py \
  --mode synthetic \
  --inventory scripts/testdata/publication_reconciliation_v1/inventory.json \
  --inventory-sha256 46fc8452702d51627c5885ccbfe422f18ec5c77494de42f28339cdcf687b90c9 \
  --witness scripts/testdata/publication_reconciliation_v1/metadata-witness.json \
  --source-acceptance scripts/testdata/publication_reconciliation_v1/source-acceptance.json \
  --source-acceptance-sha256 86299dec81f777b0463afceb74939701d76a54ffc1be922c03c1a726d4ac0b0e \
  --verify-output scripts/testdata/publication_reconciliation_v1/expected-reconciliation.json \
  --output "$private_dir/reconciliation.json"
```

The two pins above refer only to committed synthetic bytes. They confer no
historical authority. Historical operation uses the same arguments with explicit
`--mode historical` and separately authorized result-free inputs and accepted
handoff pins. Do not manufacture trust by hashing arbitrary files to satisfy the
flags. A source acceptance does not grant execution permission. No historical
source availability or completeness is asserted by this implementation.

`--verify-output FILE` is optional: the producer always recomputes every entry
from the three inputs, then requires the supplied fourth file to have exactly
the same canonical bytes. It never trusts supplied dispositions or clocks.
Verification still publishes the recomputed output to the new `--output` path.
This allows a future independently implemented consumer to demonstrate agreement.

The output directory must exist, be owned by the invoking operator, and grant no
group/other access. Output is mode 0600 and becomes visible only after completion.
Existing files, directories and symlinks are never overwritten. A retry requires
a fresh output path; if an output exists, verify its exact bytes separately. Input
files must be regular, singly linked files with no symlink leaf or parent.
All metadata input/output sizes, reference expansion and array bounds remain as
specified in the contract; an overflow fails the operation without truncation.

Exit 0 prints a static publication confirmation. Exit 1 prints exactly one static
code to stderr, without input values, paths or parser exceptions:

| Code | Contract stage |
| --- | --- |
| `RECONCILIATION_INPUT_INVALID` | Grammar/bounds, inventory integrity, cross-file consistency or output IO/size failure. |
| `RECONCILIATION_SOURCE_UNACCEPTED` | Trusted acceptance pin mismatch, or unsupported source/profile/mode/scope/map/authority. |
| `RECONCILIATION_BINDING_MISMATCH` | Original locator/fingerprint/reference mismatch, or supplied output differs from the complete recomputed canonical bytes. |

The importable `reconcile(inventory_raw, witness_raw, acceptance_raw, *, mode,
inventory_sha256, source_acceptance_sha256, output_raw=None)` returns canonical
sidecar bytes or `ReconciliationError` with `code` and numeric `stage` attributes.
The CLI exposes only the static code. It hashes exact input bytes, including LF;
input order/spacing may change integrity pins but cannot choose different events.
The optional output must itself be canonical with a trailing LF.

Synthetic verification:

```bash
python3 -B -m unittest discover -s scripts -p 'test_*code_review_export*.py'
```

Tests compare every result with the pre-existing hand-declared entries, exercise
all negative mutation recipes with exact stage precedence, and check private
exclusive publication and failure cleanup. The oversized-file recipe uses a
sparse synthetic file and the actual bounded reader without allocating hundreds
of MiB. None of these checks accepts a real source, clears custody quarantine,
joins real clocks, admits a cohort, partitions, scores or freezes a protocol.
