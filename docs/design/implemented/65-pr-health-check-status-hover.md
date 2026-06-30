# PR Health Check Status Hover

> **Status:** Implemented | **Last reviewed:** 2026-04-28

## Summary

The session detail PR health banner already surfaced that a pull request had failing tests, but it stopped at the aggregate count. That forced the user to jump to GitHub to answer a basic operational question: which CI jobs are failing, and are the rest done yet?

The implemented behavior adds a lightweight hover drilldown on the failing-tests badge in the PR health banner. Hovering the badge now lists the known check runs and their normalized status: `passed`, `failed`, or `pending`.

## Product behavior

- The hover is attached to the existing failing-tests badge in `PRHealthBanner`.
- The drilldown is intentionally summary-only:
  - show job/check names
  - show coarse status
  - do not inline log output or long annotations
- The list includes passing and pending checks in addition to failing ones so the user can tell whether the PR is broadly blocked, still running, or mostly green.

## Data shape

- PR health check summaries now carry a normalized status enum in both backend and frontend models.
- GitHub check runs are normalized into:
  - `passed` for successful / neutral / skipped completed checks
  - `failed` for failed completed checks
  - `pending` for queued / waiting / in-progress checks

## Backend notes

- `PullRequestCheckSummary` now includes `status`.
- PR health sync no longer drops passing checks from the summary payload.
- Mergeability now treats only non-`passed` checks as blocking.
- Aggregate `ci_status` is derived from the full check set:
  - `pending` if any check is still running
  - otherwise `failure` if any check failed
  - otherwise `success`

## Why this shape

This keeps the PR health banner fast and compact while answering the operator’s first diagnostic question locally in the app. It avoids turning session detail into a full CI log viewer, but removes the need to context-switch to GitHub for the common "what exactly is failing?" case.
