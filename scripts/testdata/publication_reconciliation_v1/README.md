# Invented publication reconciliation v1 fixtures

All identities, source metadata, receipts, clocks and fingerprints are invented.
Opaque source/record/page fingerprints refer only to invented bytes; no raw review
page or result is supplied. These fixtures neither establish historical authority
nor grant execution permission. Existing inventory/materializer v1 stays unchanged.

- `inventory.json`: strict original inventory schema v1 with independent snapshots.
- `metadata-witness.json`: complete candidate references and result-free facts.
- `source-acceptance.json`: synthetic-only acceptance, exact repository mapping,
  source/profile pins and explicit lifetime/partial coverage for each event class.
- `expected-reconciliation.json`: hand-declared complete output, not output of a
  reconciliation implementation.
- `cases.json`: scenario-to-locator mapping with exact dispositions/reasons.
- `negative-cases.json`: ordered executable mutation recipes and expected error
  stages/codes for future readers. The expectation is an obligation, not a claim
  that an unimplemented reader already returns that error.
- `fixture-manifest.json`: exact fixture/schema digests and trusted **test-only**
  acceptance pin. It is not a historical source acceptance or execution permit.

The scenarios cover submission corroboration/redelivery, edit refusal, absent or
partial lifetime history, invalid authority tuples, missing identities/head/key/
creation, chronology failures, reuse including a missing-PR observation, frozen
corroboration/submitted-head contradictions/conflicting times/invalid evidence/
reuse/edit/conflicts, changed opaque source evidence,
and legitimate acquisition-derived hash/clock differences. Bare object timestamps
cannot match or refute a frozen clock. Clock reconciliation never clears the
custody partitioner's stricter pre-existing quarantine or admits a cohort.

## Negative recipe execution

Use a fresh in-memory copy of the four baseline artifact objects for **each**
case. These recipes prepare invented test inputs; they implement no reconciliation
or error classifier. An independent reader must later execute the prepared inputs
and compare the exact expected static code/stage. No real input is accepted here.

1. Apply `mutations` in order. `artifact` selects the root name in `artifact_names`.
   Operations are JSON Patch `add`, `replace`, or `remove`, with RFC 6901 pointers
   (`~0`/`~1` unescape). `add` to an array `/-` appends; array `remove` deletes that
   index; all other indexes/parents must already exist. `replace` never adds a key.
2. Refresh dependency hashes using canonical sorted compact ASCII JSON plus LF:
   hash inventory bytes and copy that pin and its catalog/original-catalog pins to
   witness, acceptance and output; hash witness and copy its pin to acceptance and
   output; hash acceptance and copy its pin to output. **Do not** replace mutated
   selectors, maps, source fingerprints, locators or dispositions with originals.
3. Apply `after_refresh_mutations` in order without repairing downstream hashes.
   This explicitly isolates stale cross-file pin/chronology precedence cases.
4. Canonicalize all four objects to bytes; then apply `raw_mutations` in order.
   A `find`/`replace` raw mutation requires exactly one ASCII byte occurrence and
   replaces it once. An `append`/`repeat` mutation appends that ASCII byte string
   the stated number of times (the overflow case may be streamed to a temp file).
5. Derive the **synthetic test caller's** expected inventory/acceptance SHA from
   those final bytes, with mode `synthetic`. Then apply `invocation_overrides`.
   This deliberately pins mutated invented inputs to isolate later validation
   stages. It must never be used to accept an arbitrary historical source.
6. A future output verifier receives all four prepared byte strings plus the
   invocation. If several failures exist, the document's first-failing-stage rule
   and acceptance-before-inventory pin order determine the expected error.

Raw duplicate-key/float/overflow cases are intentionally not valid JSON Schema
inputs. Calendar tests must invoke the normative timestamp helper; optional
format checking does not replace it. Integer-token/precision requirements remain
additional to JSON Schema. New facts use Python 3.11+ reference semantics and the
portable year/offset grammar; copied original clocks retain validated v1 spelling.

## Validation limits

Offline JSON Schema, the normative timestamp helper, the existing strict
inventory validator, byte/hash pins, source/record/locator bindings, complete candidate-set
references, declared coverage shape/chronology, exact frozen strings and scenario
expectation alignment can be checked without a reconciliation reader. Expected
reasons/dispositions remain hand-declared until both independently implemented
readers reproduce them after contract agreement. No real source dataset or edit
adapter is claimed available, and no actual archive is used by these fixtures.
