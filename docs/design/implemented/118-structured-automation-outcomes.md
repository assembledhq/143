# Structured automation outcomes

**Status:** Implemented

## Problem

`automation_runs.status` describes whether an agent execution is pending, running, completed, skipped, or failed. It does not describe the business decision made about a triggering pull request. Treating `completed` as `passed` made the automation page ambiguous, while internal pre-PR review threads could emit `REVIEW_CLEAN` even when that result had no bearing on the target pull request.

## Contract

Execution lifecycle and business outcome are separate durable records:

- `automation_runs.status` remains the execution lifecycle.
- `automation_run_outcomes.decision` is one of `passed`, `changes_requested`, `advisory`, or `not_applicable`.
- `automation_run_external_actions` records a GitHub review or comment that the Main automation thread reports actually creating, including a direct URL and an explicit verification state.
- A completed run without an outcome is displayed as **Outcome not reported**. No caller may infer `passed` from execution completion.

Each GitHub PR automation prompt includes the reporting contract. The Main thread receives the intrinsic `143-tools automation-run report` command and a session-scoped `automation-run:report-outcome` token scope. Review-loop threads do not receive that scope, so `REVIEW_CLEAN` cannot overwrite the automation decision.

`changes_requested` requires a matching `github_review_changes_requested` action URL for the target repository and PR. Other decisions may omit an external action. Report retries are idempotent only when their values are identical; conflicting second reports return a conflict instead of rewriting audit history.

## Read API

- `GET /api/v1/automations/{id}/decisions` returns the latest execution per repository, PR number, and head SHA. Headless historical runs are grouped into one unknown-revision bucket per PR.
- `GET /api/v1/automations/{id}/decision-stats` aggregates unique PRs, unique revisions, raw attempts, typed outcomes, evaluations in progress, unreported outcomes, and execution failures.
- The decisions list accepts `outcome`, `pr`, `cursor`, and `limit` filters. Grouping happens in Postgres before pagination.
- `GET /api/v1/automations/{id}/runs` remains the raw execution history.

## Historical data

Migration 247 performs a conservative one-time backfill from Main-thread summaries whose entire leading token matches the legacy `#<pr>: pass|reject|advise|skipped — ...` format. It maps those tokens to typed outcomes and marks them `legacy_inferred`. Historical snapshots that contain a repository and PR number but omit the URL use the canonical `https://github.com/{repository}/pull/{number}` target. The backfill does not inspect session-level `REVIEW_CLEAN`, invent external actions, or classify any other free-form summary.

## Tenancy and durability

Both new tables carry `org_id` foreign keys. Every store query filters by `org_id`, and outcome insertion plus its optional external action is transactional. The outcome row also retains the automation, run, and session identities so authorization and audit provenance do not depend on parsing a later transcript.
