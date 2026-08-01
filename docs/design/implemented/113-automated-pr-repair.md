# Design: Automated PR Repair

> **Status:** Implemented | **Last reviewed:** 2026-07-31
>
> **Depends on:** [61-pr-state-sync-and-repair-actions.md](61-pr-state-sync-and-repair-actions.md), [78-review-agent-loops.md](78-review-agent-loops.md), [88-shared-sandbox-thread-runtimes.md](88-shared-sandbox-thread-runtimes.md)

## Summary

143 can automatically take the next deterministic repair step when an idle session has a linked open pull request with merge conflicts or failing tests. This is backend-owned follow-through on the existing `Resolve conflicts` and `Fix tests` actions; it does not add an autonomous merge or publication path.

Existing organizations retain their configured behavior. New organizations default automatic conflict repair and automatic test repair on. Users can override each preference with `Use organization default`, `On`, or `Off`.

## Product Boundaries

- Conflicts are handled before tests because conflict resolution can invalidate check results.
- Each automatic repair is bounded by pull-request head, action type, policy, and attempt budget.
- Only one repair is started for a pull-request head at a time.
- Automatic actions remain visible in session activity and use platform attribution.
- Users can stop automatic repair and can always invoke the existing manual repair actions.
- Merge, publication, bypass, scope expansion, and ambiguous product decisions remain explicit user actions.

## Trigger and State Model

The auto-repair coordinator evaluates after successful session continuation and after pull-request health synchronization. It performs cheap policy, linkage, budget, and session-state checks before reading GitHub health.

Before starting work it verifies that:

1. the session is idle and resumable;
2. the linked pull request is open and its health is not blocked;
3. the observed head SHA is still current;
4. the selected blocker still exists;
5. no equivalent repair is active or exhausted for that head.

The coordinator delegates to the same repair service used by manual actions, so prompt construction, session continuation, health enrichment, deduplication, and attempt accounting have one implementation.

## Settings and UX

Organization defaults live under **Session automation**. Personal preferences inherit from those defaults unless explicitly set to `On` or `Off`.

The session Overview reuses the PR health surface. While automation runs, the manual action is replaced with progress and a stop control. When the attempt budget is exhausted, the UI returns to a compact manual repair action with the failure context.

## Reliability and Observability

The durable repair record is created before the UI reports work in progress. Attempts and dedupe identity are persisted against the pull-request head. Outcome notifications cover failures and exhausted budgets; audit and metrics events cover decisions, outcomes, explicit stops, user reverts, and pull-request head changes during repair.

## Verification

Tests cover policy inheritance and overrides, action ordering, head-SHA races, deduplication, attempt exhaustion, session-busy behavior, automatic message attribution, health-triggered evaluation, and the UI’s progress, stop, and manual fallback states.
