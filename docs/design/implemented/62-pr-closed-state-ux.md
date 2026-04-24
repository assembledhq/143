# 62 - PR Closed State UX

> **Status:** Implemented
>
> **Last reviewed:** 2026-04-24
>
> **Depends on:** [../overall.md](../overall.md), [61-pr-state-sync-and-repair-actions.md](61-pr-state-sync-and-repair-actions.md)

## Summary

When a linked GitHub pull request is closed without merging, 143 now presents that state as a first-class terminal outcome in the main session surfaces.

The implemented behavior distinguishes:

- `Open` — active PR, still in review/repair flow
- `Merged` — PR shipped successfully
- `Closed` — PR ended without merge; the session still exists, but that PR is no longer active

## Problem

The sessions sidebar badge previously handled `merged` explicitly and otherwise fell back to CI-derived labels. That allowed a closed PR with previously passing CI to still look green and effectively active.

The detail view also lacked a clear terminal-state treatment for closed PRs, which made it unclear whether repair actions still applied.

## Implemented behavior

### Sessions list / sidebar

- The PR badge now renders an explicit `Closed` state when `pr_summary.status = closed`
- Closed overrides CI-derived labels so `CI passed` no longer masks closure
- Closed uses a neutral muted treatment instead of green success styling

### Session detail header / top metadata

- Sessions with a closed linked PR now show a `PR closed` badge in the detail header area
- `View PR` remains available

### Session detail Overview / PR health area

- Open PRs continue to use the existing live `PR health` banner and repair actions
- Closed PRs now render a compact terminal-state card instead
- Open-only repair actions such as `Resolve conflicts` and `Fix tests` are not shown for closed PRs

## Notes

- This change improves clarity without adding a new session status
- Recovery actions after closure remain a future follow-up; the current implementation focuses on accurate state communication
