# Design: Preview Command Header

> Status: Implemented | Last reviewed: 2026-05-22

The Preview tab should present preview access as the primary action, not as one peer button in a lifecycle-control strip.

## Decision

Ready previews use a compact command header:

- Left side: `Preview` title with quiet status metadata such as `Running` or `Partially ready`.
- Right side: a primary `Open Preview` link when the preview is openable.
- Secondary operations live behind `Preview actions`, including `Restart preview`, `Stop preview`, and lifetime controls.

The old ready-state status pill is intentionally removed. Readiness is useful context, but when the app is already openable it should not compete with the primary action.

Failed previews are the exception: `Restart Preview` can become the primary header action because `Open Preview` is not available.

## Rationale

Preview is a workspace surface that can accumulate many tools: external open, lifecycle controls, lifetime management, console errors, design mode, diagnostics, inspector tools, and future preview artifacts. Giving every control equal visual weight makes the main review action harder to find.

The command-header pattern keeps a stable hierarchy:

- primary: open or start the usable preview
- secondary: manage the preview process
- metadata: communicate state without adding another call to action

This keeps the Preview tab scalable as additional preview tooling is added.
