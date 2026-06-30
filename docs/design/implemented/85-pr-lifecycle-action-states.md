# PR Lifecycle Action States

> **Status:** Implemented | **Last reviewed:** 2026-06-30

Session detail PR actions use a stable lifecycle row. Actions stay visible when they belong to the current artifact state, and disabled states explain the temporary blocker with a tooltip/title. Actions are hidden only when they no longer make sense for the artifact.

## Visibility policy

- Visible enabled: the action can run now.
- Visible disabled: the action belongs here, but is blocked by a temporary dependency such as an active agent turn, missing snapshot, required Review, pending CI, active repair, GitHub auth, or another PR action already in flight.
- Hidden: the action is structurally irrelevant, such as Create PR after a PR exists, Push changes when there are no unpushed changes, Merge after merge/close, or Review for an agent without native review support.

## Action matrix

Before a PR exists:

| State | Review | Create PR |
| --- | --- | --- |
| No changes | Hidden | Hidden |
| Running with changes | Visible disabled | Visible disabled |
| Completed, no snapshot | Visible disabled | Visible disabled |
| Completed, snapshot ready | Visible enabled | Visible enabled unless Review is required |
| Builder requires Review, no clean Review | Visible enabled | Visible disabled |
| Review running | Visible disabled/spinning | Visible disabled |
| Clean Review exists | Visible enabled | Visible enabled |

After an open PR exists:

| State | Review | Push changes | Resolve conflicts | Fix tests | Merge |
| --- | --- | --- | --- | --- | --- |
| Healthy, no local changes | Visible enabled | Hidden | Hidden | Hidden | Visible enabled |
| Agent running | Visible disabled | Visible disabled if unpushed changes exist | Visible disabled if relevant | Visible disabled if relevant | Visible disabled |
| Unpushed changes | Visible enabled | Visible enabled unless temporarily blocked | Based on health | Based on health | Visible disabled until health allows it |
| Conflicts | Visible enabled | Based on changes | Visible enabled | Hidden or suppressed | Visible disabled |
| Tests failing | Visible enabled | Based on changes | Hidden unless conflicts exist | Visible enabled | Visible disabled |
| CI pending or unconfirmed | Visible enabled | Based on changes | Hidden | Hidden | Visible disabled |
| Repair running | Visible enabled or temporarily disabled | Visible disabled if relevant | Badge or suppressed | Badge or suppressed | Visible disabled |
| Merged or closed | Hidden | Hidden | Hidden | Hidden | Hidden |

## State diagram

```text
No changes
  -> Agent running
  -> Completed with changes

Completed with changes
  -> Snapshot unavailable
      Review disabled
      Create PR disabled
  -> Snapshot ready
      Review enabled
      Create PR enabled unless Review is required

Review required
  -> No clean review
      Review enabled
      Create PR disabled
  -> Review running
      Review disabled/spinning
      Create PR disabled
  -> Clean review
      Review enabled
      Create PR enabled

Create PR
  -> Queueing PR
      Create PR disabled/spinning
  -> Creating PR
      Create PR disabled/spinning
  -> Open PR

Open PR
  -> Healthy
      Review enabled
      Merge enabled
  -> CI pending or unconfirmed
      Review enabled
      Merge disabled
  -> Tests failing
      Review enabled
      Fix tests enabled
      Merge disabled
  -> Conflicts
      Review enabled
      Resolve conflicts enabled
      Merge disabled
  -> Unpushed changes
      Review enabled
      Push changes enabled unless temporarily blocked
      Merge disabled until health allows it
  -> Repair running
      Open repair session visible when external
      conflicting actions disabled or suppressed
      Merge disabled
  -> Merged or closed
      PR lifecycle actions hidden
      terminal PR status visible
```

The executable mapping lives in `frontend/src/lib/session-pr-action-state.ts`; its tests are the source of truth for the state diagram's core transitions.
