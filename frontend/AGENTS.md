# Frontend AGENTS Guide

This file applies to the entire `frontend/` tree.

## UI Patterns

### Page width and ultra-wide layouts

- Use `PageContainer` (`src/components/page-container.tsx`) for dashboard pages instead of ad-hoc max-width classes.
- Standardize constrained app pages to `max-w-[1200px]` for consistency across routes.
- `PageContainer` sizes `narrow`, `default`, and `wide` all map to the same `max-w-[1200px]` baseline; use `full` only when true full-bleed interaction is required.
- Keep app chrome full-width, but constrain the content region with `PageContainer`.
- On form-heavy pages, cap individual field groups to readable widths (e.g. `max-w-[560px]`) so controls never span the entire monitor.
- For settings-style routes, use `SettingsPageFrame` (`src/components/settings-page-frame.tsx`) so `/settings`-style and `/team`-style pages share the same narrow container and header rhythm.

### Modal action layout

- Keep modal actions on a single horizontal row in the footer.
- In left-to-right layouts, place `Cancel` on the left and the primary action on the right.
- For retry-style flows, use `Cancel` (secondary/outline) on the left and `Try Again` (primary) on the right.
- Do not split related modal actions across different vertical sections/heights.
- If you're doing a destructive action like a delete, especially for important things that are unrecoverable, please add a confirmation model to ensure that someone actually wants to make a deletion.

### Dialog consistency

- Follow the same action ordering across custom modals and shadcn `AlertDialog` flows.
- Prefer using a shared footer pattern (`flex items-center justify-end gap-2`) for custom modals.
- Keep button sizing/variants consistent with nearby app dialogs unless product requirements explicitly differ.

### Design rationale

- This aligns with common modern AI app UX patterns: dismissive actions first (left), confirm/proceed actions second (right) in LTR interfaces.
- Consistent placement reduces misclicks and improves scanability across the app.
