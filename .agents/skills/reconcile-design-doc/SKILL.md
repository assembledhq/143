---
name: reconcile-design-doc
description: Reconcile an existing implementation with a software design document, technical specification, RFC, or implementation plan. Use when Codex must audit design-to-code drift, explain mismatches, implement missing or incorrect behavior, and repeat until the selected design scope and code agree.
---

# Reconcile a Design Document

Act as the senior engineer accountable for bringing an existing implementation into agreement with its intended design. Do not stop at identifying drift: explain it, close it, verify it, and compare the result with the design again.

## Establish the contract and scope

1. Read the complete selected design scope and required linked material.
2. Read repository instructions and the relevant architecture, code, tests, migrations, configuration, and operational wiring.
3. Audit the whole document unless the user names a narrower phase, milestone, component, or requirement. Include dependencies needed to make that unit correct, but do not absorb unrelated later work.
4. Determine whether the document is authoritative, historical, partially implemented, or superseded. Look for explicit status, newer decisions, released behavior, migrations, and durable public contracts rather than assuming every design file describes the current target.
5. Turn the selected scope into a working checklist of observable requirements. Include explicitly deferred or out-of-scope items so they cannot be mistaken for completed work.

Ask the user only when resolving a conflict would materially change product behavior, architecture, security, destructive actions, or scope. Resolve discoverable facts from repository evidence. State conservative assumptions for non-material ambiguity and continue.

Treat an authoritative design as the target. If the document is stale or contradicts a stronger contract, do not blindly rewrite working code to match it; record the conflict and resolve which contract should change.

## Trace the implementation

For each requirement, trace the actual runtime path rather than inferring behavior from filenames or isolated snippets. Inspect the relevant callers and integration seams:

- data models, migrations, persistence, constraints, and rollback behavior;
- services, APIs, validation, errors, permissions, tenancy, and compatibility;
- asynchronous work, concurrency, idempotency, retry, cancellation, and recovery;
- UI loading, empty, error, success, accessibility, and responsive states;
- configuration, rollout, observability, operational support, and cleanup;
- tests and generated outputs required to ship the behavior.

Use tests as evidence, not as proof that untested paths are correct. Check negative cases and partial failures as well as the happy path.

## Build and explain the gap register

Classify findings as:

- `satisfied`: implemented and supported by concrete code or verification evidence;
- `implementation gap`: required behavior is missing, incorrect, disconnected, or inadequately tested;
- `design gap`: a decision needed for a coherent implementation is absent;
- `conflict`: the document contradicts a stronger or newer contract;
- `blocked` or `out of scope`: not closable within the selected unit.

Do not count harmless wording differences as gaps when behavior and durable contracts agree. Split partially implemented requirements into specific, actionable gaps.

For each non-satisfied item, record:

- the requirement and its design source;
- the implementation evidence and affected runtime path;
- the user, security, or operational impact;
- the proposed resolution;
- the verification that will demonstrate closure.

Before editing code, give the user a concise initial gap report grouped by severity or subsystem. Clearly distinguish confirmed implementation gaps from design gaps and contract conflicts. Explain the intended fix order.

The report is a progress update, not an approval gate. Continue immediately on clear, in-scope gaps. Pause only when a material decision or additional authority is required. During later passes, report newly discovered material gaps without repeating unchanged inventory.

## Implement and repeat

Close the highest-risk or dependency-defining gaps first, then work through the remaining register in coherent end-to-end slices:

1. Follow repository architecture and established abstractions. Preserve unrelated user changes.
2. Implement the complete behavior needed for the slice, including error paths, migrations, compatibility, configuration, diagnostics, and cleanup.
3. Add focused regression tests that would have failed before the fix and cover important boundaries.
4. Run focused tests and static checks, then broaden verification according to blast radius.
5. Update the checklist with implementation and verification evidence.

When a `design gap` has one safe, conventional resolution consistent with the repository, state the assumption and implement it. When credible resolutions have materially different contracts, stop and ask the user.

Never edit the design merely to make an implementation gap disappear. Change it only when evidence shows the contract is stale or incomplete and repository policy permits the update.

After each slice, repeat the audit:

1. Re-read the selected design text from its source.
2. Compare every requirement with the resulting code paths, not merely the diff.
3. Reinspect callers, persistence, APIs, background work, UI states, permissions, configuration, and operational behavior.
4. Identify remaining gaps, regressions, dead paths, unjustified deviations, and missing tests.
5. Fix every newly exposed or still-open in-scope gap.
6. Run the relevant verification again.

Continue until one complete audit pass finds no unresolved in-scope implementation gaps. The loop must make progress; do not merely rerun the same checks. Passing tests alone does not close the audit. If blocked after exhausting safe alternatives, report the exact blocker, evidence, and impact.

## Challenge the clean pass

When the register appears clean, review the complete affected system as a skeptical senior reviewer. Look for:

- correctness bugs, races, non-atomic changes, duplicate work, stale state, and data loss;
- authorization bypass, tenant leakage, unsafe defaults, injection, and validation gaps;
- incompatible API or schema transitions, unsafe rollout or rollback order, and poor failure recovery;
- missing diagnostics, inaccessible or inconsistent UX, and overlooked edge cases;
- brittle or overly mocked tests that cannot catch likely regressions;
- needless complexity, duplication, dead code, and leaky abstractions.

Fix every material in-scope finding, add regression coverage, and run the design audit again. Finish only when both the design comparison and this adversarial pass are clean.

## Finish

Report:

- the selected design scope and major behavior reconciled;
- gaps closed, grouped by behavior rather than file;
- important design gaps, conflicts, and assumptions and how they were resolved;
- verification performed and its results;
- explicitly blocked, deferred, or out-of-scope items;
- residual risks only when they are real and actionable.

Never claim full alignment when the selected scope has known material gaps, a required decision remains unresolved, or required verification did not run.
