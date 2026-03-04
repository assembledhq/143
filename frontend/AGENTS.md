# Frontend AGENTS Guide

This file applies to the entire `frontend/` tree.

## UI Patterns

### Modal action layout

- Keep modal actions on a single horizontal row in the footer.
- In left-to-right layouts, place `Cancel` on the left and the primary action on the right.
- For retry-style flows, use `Cancel` (secondary/outline) on the left and `Try Again` (primary) on the right.
- Do not split related modal actions across different vertical sections/heights.

### Dialog consistency

- Follow the same action ordering across custom modals and shadcn `AlertDialog` flows.
- Prefer using a shared footer pattern (`flex items-center justify-end gap-2`) for custom modals.
- Keep button sizing/variants consistent with nearby app dialogs unless product requirements explicitly differ.

### Design rationale

- This aligns with common modern AI app UX patterns: dismissive actions first (left), confirm/proceed actions second (right) in LTR interfaces.
- Consistent placement reduces misclicks and improves scanability across the app.
