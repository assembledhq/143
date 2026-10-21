# Invented publication reconciliation v1 fixture

All data and provenance are invented. This is a draft agreement fixture, not a
producer or verifier implementation. It contains no raw review pages, results,
private keys, credentials, labels or real identifiers. Source/record fingerprints refer
to invented data made with the repository synthetic factory.

- `inventory.json`: unchanged strict inventory schema v1.
- `metadata-witness.json`: inspectable metadata events, bare object observation,
  provenance and complete original-locator bindings.
- `source-acceptance.json`: **synthetic-only** operator acceptance. Its pin in
  `fixture-manifest.json` is a test anchor, never a historical source authority.
- `expected-reconciliation.json`: hand-declared complete output.
- `cases.json`: readable scenario-to-locator mapping and expected dispositions.
- `negative-cases.json`: invalid-input obligations for the future readers.
- `fixture-manifest.json`: exact byte digests and the independently supplied test
  acceptance pin. It is not an execution permission.

Structural JSON Schema and existing inventory validation pass. No reconciliation
algorithm is implemented or claimed tested here. Both readers must independently
compute the hand-declared output once the contract is agreed. The invented edit
event demonstrates required proof shape; it does not assert that such a complete
historical edit source or provider adapter is currently available.
