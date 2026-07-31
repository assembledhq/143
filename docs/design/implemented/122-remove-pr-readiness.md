# 122 - Remove PR Readiness

> **Status:** Implemented | **Last reviewed:** 2026-07-31
>
> **Applies to:** PR readiness runs, checks, bypasses, contexts, policies, and
> custom checks; the `run_pr_readiness` job; readiness org/user follow-through
> settings; the `pr.readiness_attention` Slack event; readiness repo config; and
> the readiness surfaces in session detail and settings.

## Decision Summary

Remove PR readiness from 143.

Readiness was preflight evidence layered in front of publication: a policy-driven
run tied to a workspace revision/snapshot that produced typed checks plus
prompt-only custom checks, could block builders by role, and supported audited
bypasses. In practice it duplicated what review-agent loops and repository-native
CI already establish, while adding a second blocking gate, a second LLM cost
center, an org/repo policy surface, and a six-table data model.

The pre-PR gate for builders is not removed — it collapses back to the
review-loop gate that readiness itself fell back to whenever no readiness store
was configured: a builder must have a clean review loop for the current snapshot
before `CreatePR` or `PushChangesToPR` is accepted
(409 `REVIEW_REQUIRED_BEFORE_PR`).

## Builder Gate Scope

Readiness was changeset-scoped: it pinned `evaluated_head_sha` to the target
changeset's head. Review loops are session-scoped — a clean loop describes the
session workspace at a snapshot key and says nothing about a separate worktree.

`requireBuilderReviewForTarget` therefore refuses builder publication of a
non-primary materialized changeset instead of admitting it on the session's
unrelated clean review loop. This preserves the old fallback behavior, which
also refused builders on a materialized changeset when no readiness store was
configured. Engineers and admins are unaffected, and a primary changeset that
kept a worktree path after an accepted split still takes the normal
session-scoped path.

Publishing non-primary changesets is Phase 4 of
[../future/111-session-changesets-and-stacks.md](../future/111-session-changesets-and-stacks.md);
if builders need it, that phase should introduce changeset-scoped review
evidence rather than widening this gate.

## Goals

1. Remove readiness product surface, background compute, and LLM spend.
2. Keep the builder pre-PR gate intact through review loops.
3. Retire readiness state safely across a rolling deploy, with no window in
   which an old process can enqueue work whose handler no longer exists.
4. Leave adjacent subsystems (review loops, publication, PR health, automatic PR
   repair, code review) behaviorally unchanged.

## Non-Goals

- Removing review-agent loops, the `Review` action, or the builder pre-PR gate.
- Removing PR health synchronization, repair actions, or code review.
- Preserving readiness history. Readiness runs, checks, and bypasses are
  destroyed by migration `000266` and cannot be reconstructed from within 143.

## Rollout Order

Migration `000266_remove_pr_readiness` is the rolling-deploy barrier, following
the pattern established by `000259_cancel_pending_pm_jobs`:

1. `LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE` before the drain check, so a
   worker cannot claim a pending readiness job between the check and the
   trigger install.
2. Refuse to migrate while any `run_pr_readiness` job is `running`; the operator
   drains first.
3. Install `trg_reject_removed_pr_readiness_jobs` so old API/worker processes
   still serving during the rollout cannot enqueue the removed job type.
4. Cancel pending readiness jobs. Rows are cancelled rather than deleted so job
   history stays readable and the jobs lock is not held for a sequential scan —
   `job_type` has no standalone index. `delete_expired_completed_jobs` does not
   cover `cancelled`, so these rows persist, as the PM shutdown's cancelled rows
   do; the queued readiness backlog is small and bounded.
5. Clear `readiness` changeset leases, then narrow the lease `holder_type`
   check constraint.
6. Strip retired org/user settings keys and the `pr.readiness_attention` Slack
   subscription, then drop the six readiness tables.

Slack subscriptions that listed only `pr.readiness_attention` are pinned to the
`custom` preset before the event is stripped. Without that, an emptied `events`
array would fall through to the preset defaults and a `verbose` row would start
matching every event.

The trigger is temporary infrastructure, as
`000261_contract_pm_compatibility` did for the PM shutdown: it should be dropped
by a follow-up migration once no pre-removal process can still be running.

## Residual Risks

- **User settings strict decoding.** `ParseUserSettings` uses
  `DisallowUnknownFields`, so a retired key is a hard decode failure. The
  migration strips `automatic_pr_follow_through.readiness_after_review_loop`,
  but an old API process serving a cached frontend bundle during the rollout
  window could write it back, after which that one user's settings row fails to
  decode until it is cleared. Org settings decode leniently and are not exposed
  to this. The window closes when the last pre-removal API process retires.
- **Removed endpoints.** Cached frontend bundles calling the removed
  `/api/v1/sessions/{id}/pr-readiness-*` and `/api/v1/pr-readiness-*` routes
  receive 404 until they reload.
- **Audit history.** Historical `pr_readiness_*` audit actions and resource
  types remain in `audit_logs` and stay readable; enum validation runs on write
  only.
