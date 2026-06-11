# Design: Failure Communication

> **Status:** Implemented | **Last reviewed:** 2026-04-21
>
> **Implementation notes:** Failure explanation, category, next steps, retry guidance, persistence, background analysis, and session UI rendering are implemented. The broader learning loop described below remains future work.

This document describes the failure-communication behavior that ships today, then separates the still-unbuilt feedback-loop ideas into explicit future sections.

## Implemented Behavior

When a session fails, the system stores a structured failure summary and surfaces it directly in the session UI instead of showing only a raw process error.

### Stored Fields

The implemented session-level fields are:

- `failure_explanation`
- `failure_category`
- `failure_next_steps`
- `failure_retry_advised`

These are persisted on the session record and returned through the frontend API types.

### Failure Analysis Pipeline

Failure analysis runs as background work after a session ends in a failed state.

- the worker registers an `analyze_failure` job
- `FailureService` classifies the failure
- the session record is updated with the user-facing summary

Primary implementation:

- [internal/services/agent/failure.go](../../../internal/services/agent/failure.go)
- [internal/worker/handlers.go](../../../internal/worker/handlers.go)
- [internal/db/session_store.go](../../../internal/db/session_store.go)

### Implemented Categories

The current implementation already distinguishes multiple failure classes, including:

- context failures such as missing context
- tooling failures such as timeout, sandbox crash, API failure, and build failure
- validation failures such as test regression and security violation
- complexity-style failures based on oversized changes
- auth-expiry failure states for supported agent providers

The current analyzer is rule-based, not LLM-generated. That is intentional as part of the current shipped baseline.

### Implemented UI

Failure summaries are rendered in the session detail and session list/sidebar surfaces. Users can see:

- the explanation
- the category
- next steps
- whether retry is advised

Primary implementation:

- [frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx](../../../frontend/src/app/(dashboard)/sessions/[id]/session-detail-content.tsx)
- [frontend/src/app/(dashboard)/sessions/sessions-page-content.tsx](../../../frontend/src/app/(dashboard)/sessions/sessions-page-content.tsx)
- [frontend/src/app/(dashboard)/sessions/session-sidebar.tsx](../../../frontend/src/app/(dashboard)/sessions/session-sidebar.tsx)

## Future Work

The items below were part of the broader original design but are not part of the currently implemented feature boundary.

### Future: Failure-Driven Routing Improvements

The original design proposed using failure subtype patterns to automatically improve routing decisions. Today the system stores and surfaces failures, but it does not yet close the loop into routing policy automatically.

### Future: Per-Repo Failure Aggregation and Context Gap Detection

The doc envisioned repo-level aggregation such as “this repo fails often in this subsystem.” That reporting and suggestion layer is not built yet.

### Future: Failure-Derived Eval Expansion

The design proposed feeding high-value failures into eval datasets and adversarial test suites. The eval references exist conceptually, but that pipeline is not yet wired.

### Future: Fix-Rate Transparency Surfaces

The broader “fix rate header” and transparent success-rate product surfaces described here are not fully implemented. The current shipped feature is run-level failure explanation, not the full trust dashboard.

### Future: Notification Integration

The design also expected failure explanations to flow into Slack/email notifications. That depends on the broader notification system and remains future work.

### Future: LLM-Based Failure Analysis

The original document described an LLM post-processor. The current classifier is deterministic and rule-based. If we later move to an LLM-assisted classifier, that should be treated as a separate iteration with careful evaluation.

## Relationship to Other Docs

- validation-triggered failures also relate to [07-validation.md](07-validation.md)
- longer-term learning from failures connects to [16-ai-agent-evals.md](../future/16-ai-agent-evals.md)
- any future outbound failure notifications depend on [22-notifications.md](../future/22-notifications.md)
