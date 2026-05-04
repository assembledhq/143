# Design: Mobile-Friendly Settings Pages

> **Status:** Implemented | **Last reviewed:** 2026-05-04

## Summary

The settings area now treats phone layouts as a first-class surface instead of a compressed desktop table view.

## Implemented direction

- Shared settings page headers now give actions a full-width mobile lane, then collapse back to right-aligned desktop controls at `sm+`.
- Dense settings rows that previously relied on desktop tables now render as stacked mobile cards with inline metadata labels while preserving the existing desktop table layout.
- Integration cards now stack title, description, and action controls vertically on narrow screens so connect/disconnect actions remain tapable without horizontal squeeze.
- Team settings rows expose `Email`, `Role`, and `Actions` labels directly inside each member row on phones, and pending invitations stack cleanly instead of relying on a single horizontal row.
- Account and coding-agent settings keep the existing information density on desktop, but shift the personal/org auth stacks to card-style mobile rows so users can read status, auth type, priority, and actions without sideways scrolling.

## Scope

This pass focuses on layout and tap-target behavior for existing settings surfaces. It does not change autosave semantics, permissions, or backend behavior.
