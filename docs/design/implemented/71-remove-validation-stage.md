# Remove Validation Stage

> **Status:** Implemented | **Last reviewed:** 2026-06-30

## Summary

The session pipeline no longer runs a product-owned `validation` stage between agent execution and PR creation.

When a session is explicitly ended, the backend now enqueues `open_pr` directly. The frontend session detail UI no longer exposes a `Validation` tab or calls a validation-specific API.

## Rationale

- Re-running correctness, CI, and policy checks inside 143 duplicated what repository CI/CD systems already do better.
- The extra stage added product complexity across worker jobs, API surface, session UI, PR body generation, and tests.
- Removing it keeps 143 focused on orchestrating coding work and publishing changes, while the repository remains the source of truth for test and deploy gates.

## Resulting Flow

1. Agent run completes.
2. User ends the session.
3. Backend marks the session completed and enqueues `open_pr`.
4. Repository-native CI/CD validates the branch after the PR is opened or updated.

## Scope Removed

- Worker job registration and execution for `validate`
- Backend validation service and validation store code
- Session validation API route
- Session detail `Validation` tab and frontend validation types/query helpers
- PR body enrichment that depended on stored validation records

## Deliberate Non-Goals

- Historical migrations and tables are not rewritten in-place.
- Session policy/schema compatibility fields may remain where they are inert and low-risk, but they no longer control runtime behavior.
