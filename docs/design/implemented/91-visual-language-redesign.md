# Visual Language Redesign

> **Status:** Implemented | **Last reviewed:** 2026-05-28

143's authenticated app should use a deliberate surface hierarchy, not
screen-by-screen color choices. The current UI mixes `bg-background`,
`bg-card`, `bg-muted/30`, `bg-sidebar`, and one-off state colors in ways that
make adjacent panes feel accidentally different. The most visible example is
the Sessions page: the global navigation, session list, and empty session
canvas all use nearby light neutrals, but the session list is the plane with the
most visual distinction. That makes secondary navigation compete with the
primary navigation and lowers overall contrast.

This redesign should create a durable visual language for the whole product:
high contrast where the user navigates or acts, quiet surfaces where the user
reads or works, and consistent state colors everywhere.

## Goals

- Make visual hierarchy obvious within two seconds: global navigation, local
  navigation, selected object, primary action, and passive content should each
  read differently.
- Move structural contrast into the app shell and navigation instead of giving
  arbitrary feature panes their own background color.
- Replace ad hoc neutral usage with named product surface tokens that map to
  shadcn/Tailwind semantic tokens.
- Keep 143 dense and operational. This is a workbench for sessions, previews,
  settings, and review workflows, not a marketing surface.
- Preserve themeability and accessibility. Light mode should have materially
  stronger contrast than today; dark mode should use the same hierarchy, not a
  separate design system.

## Non-Goals

- Do not redesign product information architecture as part of this work.
- Do not introduce page-specific brand illustrations, decorative gradients,
  background orbs, or marketing-style cards in app surfaces.
- Do not replace shadcn/ui primitives.
- Do not change the meaning of status colors while fixing hierarchy.

## Design Direction

Adopt a three-plane shell with explicit raised and selected states:

| Plane | Purpose | Contrast | Examples |
|---|---|---|---|
| `nav` | Global product navigation and workspace/account chrome | Highest structural contrast | Left app sidebar, compact app rail, mobile nav drawer |
| `pane` | Local navigation and object lists | Moderate structure, same family as canvas | Session list, settings subnav, project task lists |
| `canvas` | Primary work/read area | Quietest background | Session transcript, empty states, settings content, detail pages |
| `raised` | Interactive controls and grouped repeated items | Clear boundary on top of pane/canvas | Inputs, command menu, popovers, row cards where needed |
| `selected` | Current route/object focus | Strong state, not a new plane | Active nav item, selected session row, active tab |

The shell should feel closer to focused developer tools such as Codex: a
confident navigation anchor, a clean work canvas, and restrained use of accent
color. The product should not become dark by default, but the light theme should
stop depending on near-identical gray planes for hierarchy.

## Token Model

Add product-level semantic tokens in `frontend/src/app/globals.css` and map them
into Tailwind theme values. Exact OKLCH values should be tuned during
implementation, but the relationships are the contract:

| Token | Light relationship | Dark relationship |
|---|---|---|
| `--surface-nav` | Noticeably darker than canvas | Darkest plane |
| `--surface-nav-foreground` | Strong foreground on nav | Strong foreground on nav |
| `--surface-pane` | Slightly separated from canvas, lower contrast than nav | Slightly lighter than nav |
| `--surface-canvas` | App working background | Main dark working background |
| `--surface-raised` | White or near-white above pane/canvas | Raised dark surface above pane/canvas |
| `--surface-selected` | Subtle primary-tinted selected surface | Higher-contrast primary-tinted selected surface |
| `--surface-hover` | Neutral hover with visible contrast | Neutral hover with visible contrast |
| `--border-strong` | Stronger separator for app shell boundaries | Stronger separator for app shell boundaries |
| `--focus-visible` | Accessible focus ring | Accessible focus ring |

Existing shadcn tokens stay in place. The new product tokens should either feed
the current shadcn variables or sit beside them as app-shell aliases. Feature
code should prefer semantic intent over raw utilities:

- Use `bg-surface-nav` for global navigation.
- Use `bg-surface-pane` for local side panes.
- Use `bg-surface-canvas` for main work areas.
- Use `bg-surface-raised` for inputs, popovers, dialogs, and repeated cards.
- Use `bg-surface-selected` plus a primary rail or stronger text for selected
  rows.

Avoid new direct uses of `bg-muted/30`, `bg-primary/5`, and hard-coded
gray-like values in app surfaces unless the component has a documented reason.

## Color Semantics

Keep saturated color sparse and meaningful:

- Primary: active route, selected object accent rail, primary button, active
  tab underline, live/working status where already established.
- Green: success, merged, positive diff counts.
- Red/destructive: failure, blocking errors, negative diff counts.
- Amber/orange: needs human attention or warning.
- Purple/violet: only if it represents a distinct product concept; do not use
  it as generic decoration.

Status dots and badges should use shared components wherever possible. If a
surface needs a new status style, add it centrally instead of tuning color in
the page.

## Component Rules

- App shell owns the strongest structural contrast. Local panes should never be
  visually louder than the global nav.
- Local list panes should use the same surface family across Sessions,
  Automations, Projects, Settings, and future entity lists.
- Selected rows need three cues: stronger text, selected surface, and a small
  primary marker. Do not rely on a faint tinted background alone.
- Hover states should be visible but lower priority than selected states.
- Use borders for pane boundaries and rows; reserve shadows for floating
  surfaces such as popovers, command palette, sheets, and modals.
- Cards should be used for repeated items or real grouped content only. Do not
  turn full page sections into floating cards.
- Text contrast should be raised on primary content. `text-muted-foreground`
  is for metadata, helper text, timestamps, and inactive nav labels, not object
  titles that are meant to be scanned.

## First Screens To Migrate

1. **Authenticated shell**
   - Tune global sidebar and compact rail to be the primary contrast anchor.
   - Normalize active nav item styling across desktop, compact, and mobile.
   - Introduce `surface-nav`, `surface-pane`, `surface-canvas`, and
     `surface-raised` tokens.

2. **Sessions list and detail**
   - Change the sessions pane to use the local pane token instead of a one-off
     muted background.
   - Make selected session rows visibly selected through text weight,
     selected surface, and primary rail.
   - Keep the session canvas quiet and aligned with the main work background.
   - Ensure the composer remains a raised surface with clear contrast.

3. **Settings**
   - Apply the same pane/canvas/raised rules to settings lists, side sections,
     sheets, and table-like inventories.
   - Remove one-off card backgrounds where a page section should be unframed.

4. **Automations and Projects**
   - Bring list rows, status badges, and execution/task surfaces onto the same
     token language.
   - Validate that active work and human-needed states have stronger contrast
     than passive completed history.

5. **Public/docs boundary**
   - Do not blindly apply app shell tokens to the public homepage. Public pages
     may keep their separate editorial visual system, but shared shadcn tokens
     must not regress the app shell.

## Implementation Plan

### Phase 1: Audit and Token Definition

- Inventory current uses of neutral background utilities across app shell,
  sessions, settings, automations, projects, command palette, dialogs, and
  shared components.
- Add product surface tokens to `globals.css`, including dark-mode mappings.
- Add Tailwind theme aliases for the new tokens.
- Update shared documentation in `docs/design/03-frontend.md` once the token
  names are final.

### Phase 2: Shell and Sessions

- Migrate `AuthenticatedLayout`, compact rail, mobile drawer, `SidebarLayout`,
  and `SessionSidebar` to the new tokens.
- Replace session row selected/hover styles with shared selected-row classes or
  a small shared primitive.
- Verify desktop, compact desktop, tablet, and mobile session layouts.

### Phase 3: Shared Components

- Add or update shared primitives for:
  - selected rows
  - local pane containers
  - empty states inside panes
  - status/badge contrast
  - command-style checked rows if needed
- Move page-specific status color choices into shared helpers where possible.

### Phase 4: Secondary Surfaces

- Migrate Settings, Automations, Projects, Usage, Audit Log, Preview settings,
  and Evals surfaces.
- Remove duplicated neutral/selected styling from individual pages.
- Keep changes incremental by page, but do not leave a page half-migrated if it
  mixes old and new surface semantics in adjacent panes.

### Phase 5: Validation and Regression Guardrails

- Add focused frontend tests for any extracted shared primitives.
- Run the required frontend checks after code changes:
  - `npm run typecheck`
  - `npm run lint`
  - `npm run build`
- Capture Playwright screenshots for representative app states:
  - Sessions empty/new-session state
  - Sessions selected active run
  - Settings integration inventory
  - Automations detail/history
  - Mobile session list/detail
- Check color contrast for primary text, muted metadata, active nav, selected
  rows, and focus rings in both light and dark mode.

## Acceptance Criteria

- Global navigation is the highest-contrast structural plane in the
  authenticated app.
- Local panes no longer use arbitrary gray backgrounds that make them appear
  more important than global navigation.
- A selected session, selected nav route, active tab, primary action, and muted
  metadata can be distinguished without reading labels.
- App surfaces use named semantic tokens rather than page-specific neutral
  utility combinations.
- Light and dark themes share the same hierarchy.
- No major app screen contains adjacent panes that appear accidentally different
  because of unrelated color choices.

## Open Questions

- Should the global nav become materially darker in light mode, or should it
  remain light but with stronger border/active contrast? The current product
  likely benefits from a darker nav anchor, but this should be confirmed with
  screenshots.
- Should `primary` stay in the current blue-violet family, or shift slightly
  toward a less purple coding-tool blue? The answer affects active states,
  focus rings, and live session dots.
- Should selected row styling become a reusable component API, or just a shared
  class helper? Start with the smallest abstraction that avoids drift.
