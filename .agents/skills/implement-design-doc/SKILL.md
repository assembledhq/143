---
name: implement-design-doc
description: Implement a software design document, technical specification, RFC, implementation plan, or a defined portion such as one phase or PR. Use when an agent must build a spec thoroughly, audit the code against it in repeated gap-closing passes, and finish with an adversarial quality, product, reliability, and security review.
---

# Implement a Design Document

Act as the senior staff engineer accountable for shipping the specification, not merely producing a plausible patch. Build high-quality, maintainable code with the simplest clean abstractions that fit the codebase.

## Establish scope

1. Read the complete design document, including linked material needed to understand requirements.
2. Read repository instructions and the architecture, conventions, tests, and nearby implementations relevant to the change.
3. Determine the requested delivery unit:
   - Implement the entire document when the user requests the whole design.
   - Implement only the named phase, PR, milestone, component, or requirement when the user narrows the scope.
   - Treat dependencies required to make that unit correct and usable as in scope. Do not silently implement unrelated later phases.
4. Convert the selected scope into a working checklist that maps requirements to code, migrations, tests, operational work, and user-facing behavior. Record explicitly deferred items.
5. Ask the user only about ambiguities that materially change product behavior, architecture, security, destructive actions, or scope. Resolve discoverable facts from the repository and make conservative, clearly stated assumptions for non-material gaps.

## Design the complete behavior

Treat the document as the product contract, while recognizing that it may omit necessary details. Think through adjacent behavior such as:

- empty, loading, error, retry, cancellation, and partial-failure states;
- permissions, tenancy, validation, privacy, abuse cases, and secure defaults;
- compatibility, migrations, rollout, rollback, observability, and supportability;
- concurrency, idempotency, performance, resource limits, and failure recovery;
- accessibility, responsive behavior, terminology, and consistency with existing workflows;
- API boundaries, data lifecycle, defaults, configuration, and upgrade behavior.

Follow established repository patterns unless the specification intentionally changes them. Prefer extending a sound existing abstraction over introducing a parallel one. Do not add speculative frameworks or generality unsupported by current requirements.

When the document conflicts with the codebase or cannot be implemented safely as written, surface the conflict and choose the smallest coherent resolution. Stop for user direction when the choice would materially alter the contract.

## Implement

1. Make an end-to-end vertical slice work, then complete the remaining checklist.
2. Keep business logic out of presentation and transport layers. Preserve module boundaries and invariants.
3. Handle errors explicitly. Add logging, metrics, or diagnostics where operators will need them.
4. Include migrations, generated artifacts, configuration, cleanup, and compatibility work required for a deployable result.
5. Add or update focused tests for every behavior change, including important negative cases and boundaries.
6. Update public or internal documentation only when required by the specification or repository policy.
7. Preserve unrelated user changes. Keep the diff focused on the selected scope.

Run focused checks early and broaden verification in proportion to the blast radius. Inspect failures; do not hide, weaken, or delete meaningful checks merely to obtain a passing result.

## Close implementation gaps

After the first implementation, repeat this loop:

1. Re-read the selected design scope from the source document.
2. Compare each requirement and implied product invariant with the actual diff and resulting code paths.
3. Inspect every integration seam: callers, persistence, APIs, background work, UI states, permissions, configuration, and operational behavior.
4. Identify concrete gaps, incomplete paths, unjustified deviations, dead code, missing tests, and behavior that works only in the happy path.
5. Fix every in-scope gap.
6. Run the relevant tests, static analysis, builds, and repository-specific validation.
7. Update the requirement-to-implementation checklist with evidence.

Continue until a full pass finds no unresolved in-scope implementation gaps. Do not declare completion based only on tests passing. If an item is blocked, exhaust safe in-scope alternatives, then report the exact blocker and its impact rather than representing the work as complete.

## Perform an adversarial final review

Once the gap loop is clean, review the result as a skeptical senior reviewer who wants to prevent a production incident and a poor product experience. Review the complete affected code, not only individual edited lines.

Search for:

- correctness bugs, race conditions, stale state, data loss, and non-atomic operations;
- authorization bypasses, tenant leakage, injection, insecure defaults, secret exposure, and validation gaps;
- broken failure recovery, retry storms, duplicate work, resource leaks, and poor observability;
- backward-incompatible API or schema changes and unsafe deploy or rollback ordering;
- confusing UX, inaccessible interactions, inconsistent states, missing feedback, and overlooked edge cases;
- needless complexity, leaky abstractions, duplication, naming problems, and code that is difficult to test or operate;
- tests that are missing, brittle, overly mocked, or incapable of detecting the likely regressions.

Rank findings by severity and fix every material in-scope finding. Add regression tests where appropriate, rerun verification, then repeat both the design-gap audit and adversarial review until neither finds a material unresolved issue.

## Finish

Report:

- the delivery unit completed and the major behavior implemented;
- important product or technical decisions made for omissions in the document;
- verification performed and its result;
- any explicitly deferred, blocked, or out-of-scope items;
- residual risks only when they are real and actionable.

Never claim that the design is fully implemented if the selected scope still has known material gaps or required verification did not run.
