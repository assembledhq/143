# Design: Visual System and Product Polish

> **Status:** Implemented | **Last reviewed:** 2026-07-14

The visual-system migration is implemented across the authenticated product, public site, authentication, and shared component library. The global navigation remains in its existing location and retains its route hierarchy. Instrument Sans, the warm mineral/charcoal token system, semantic surface roles, dense/readable type roles, shared status and resource primitives, solid primary treatments, and the 143 flight-program public identity are now the frontend defaults. Legacy gradient/glow consumers and the obsolete unused radar canvas were removed during the final audit.

Homepage exception: retain the existing flying P-80 canvas, homepage copy, and section composition. The application visual system may influence shared controls and tokens on the public site, but future polish work must not replace the plane-led hero or restructure homepage storytelling without a separate product decision.

143 has a capable, consistent frontend, but much of its visual identity still comes from familiar shadcn dashboard defaults: a cool neutral palette, Lucide icons, compact Geist typography, rounded selected navigation rows, bordered cards, and blue-purple gradient actions. The result is functional and accessible, but it does not yet communicate the product's own character as shared coding-agent infrastructure for engineering teams.

This design defines an implementation path for a more polished, calm, and recognizable product. The intended character is **calm mission control for software teams**: dense enough for operational work, quiet enough for long sessions, and grounded in the 143 story of small autonomous teams, engineering craft, speed, and flight.

[Ando](https://www.ando.so/) is a useful quality reference for restraint, negative space, typography, selective depth, and composed product storytelling. It is not a template to copy. 143 should interpret those qualities through its own workshop, flight-program, and mission-control identity.

## Decision Summary

1. Keep the current global navigation placement, information architecture, organization switcher, repository context switcher, and responsive navigation behavior.
2. Replace the cool generic neutral ramp with a warmer mineral-neutral system in both light and dark themes.
3. Replace gradient primary actions and gradient chat bubbles with solid, restrained brand color.
4. Introduce a two-family typography system: a distinctive display face for brand and page-level hierarchy, with Geist retained for dense UI, code, metadata, and transcripts.
5. Reduce visible container chrome. Borders, cards, badges, and shadows become opt-in signals rather than the default treatment for every group.
6. Make Sessions the first product surface migrated and use it to establish the refreshed hierarchy for dense workspaces.
7. Migrate Previews, Code reviews, Automations, Autopilot, Settings, and Integrations onto shared resource-row, section-group, and status primitives.
8. Refresh the public site after the application system is stable so marketing depicts the real product rather than a parallel visual language.

## Problem

The existing visual system has four broad weaknesses.

### The component library is carrying the brand

The app is correctly built on semantic tokens and shadcn/Radix primitives, but the default shapes remain recognizable. `Card`, `Button`, `Badge`, `Tabs`, sidebar selected states, and Lucide icon-label rows account for most of the product's personality. The black square `1` mark and primary gradient are not yet enough to make the interface recognizably 143.

### Hierarchy is too compressed

The global body size is 13px, descriptions and metadata are frequently 12px, and headings are compact. This works for dense panes but makes titles, explanatory copy, navigation, metadata, and actions feel closer in importance than they are. The interface is information-dense without always being visually decisive.

### Too many surfaces announce themselves

Cards, tables, inputs, panels, selected rows, and nested groups often add their own border. When every group is outlined, users see the component boundaries before they see the page's content hierarchy. Hover treatment on the base `Card` also implies interactivity on cards that are purely structural.

### Color communicates category more than character

The cool gray canvas and purple-blue gradient fit the category of AI software but do not create a distinct 143 atmosphere. Saturated fills on primary actions, user messages, and status badges compete with the work itself.

## Goals

1. Give 143 a recognizable visual identity without changing its product model or navigation architecture.
2. Improve information hierarchy and readability across dense operational screens.
3. Reduce visual noise from borders, nested cards, and overused pills.
4. Make state and action priority legible without relying on saturated color.
5. Establish reusable primitives so page migrations converge on one system.
6. Preserve or improve accessibility, keyboard behavior, performance, dark mode, and responsive behavior.
7. Make the public site and authenticated product feel like parts of the same brand.

## Non-Goals

1. Moving global navigation to a masthead, rail, command-only surface, or other location.
2. Changing global navigation labels, route hierarchy, organization switching, repository switching, or Settings placement.
3. Redesigning backend models, API contracts, authorization, polling, SSE, or mutation behavior.
4. Changing product terminology or workflow semantics except where microcopy is needed for hierarchy and clarity.
5. Replacing shadcn/Radix primitives or abandoning semantic design tokens.
6. Rebuilding every feature page in one release.
7. Reproducing Ando's brand, Japanese architectural references, layouts, or visual assets.

## Product Principles

### Calm before spectacle

Primary work should remain readable during long sessions. Animation, gradients, glow, blur, and saturated fills should never be the default indicators of intelligence or activity.

### Hierarchy before containers

Use type, spacing, alignment, and grouping before adding a card or border. A visible container must communicate one of four things: interactivity, selection, elevation, or a durable conceptual boundary.

### State is not decoration

Status color is reserved for status. Brand color is reserved for primary actions, selection, and a small number of important links. Product areas do not each receive arbitrary accent colors.

### Dense surfaces and narrative surfaces are different

Sessions, diffs, tables, and logs need compact rhythm. Page introductions, empty states, onboarding, settings explanations, and marketing need more generous rhythm. They should share tokens without sharing identical spacing.

### 143 should own the final composition

shadcn components remain implementation primitives, not final page composition. Screens should not read as sequences of default cards, tabs, inputs, and badges.

## Visual Foundation

### Color

The first implementation should prototype the following light-theme values. The final values may move during visual QA, but their roles and relationships are part of this design.

| Token role | Starting value | Intended use |
|---|---:|---|
| Canvas | `#F6F5F0` | App background and primary reading canvas. |
| Raised surface | `#FEFDFB` | Cards, composers, menus, dialogs, and selected structural surfaces. |
| Recessed surface | `#EFEEE8` | Sidebars, code gutters, inactive grouped controls, and subtle wells. |
| Strong text | `#1B1B19` | Titles, primary labels, and body copy. |
| Secondary text | `#6B6B65` | Descriptions, timestamps, and secondary metadata. |
| Hairline | `#E1DED5` | Necessary separation between durable regions. |
| Strong border | `#CAC6BB` | Inputs, focus-adjacent controls, and selected structural boundaries. |
| Flight blue | `#315CE8` | Primary actions, focus, selection, and important links. |
| Soft blue | `#E7ECFF` | Selected or informative low-emphasis backgrounds. |

The dark theme should use warm charcoal rather than blue-black:

| Token role | Starting value |
|---|---:|
| Canvas | `#151513` |
| Raised surface | `#1D1D1A` |
| Recessed surface | `#11110F` |
| Strong text | `#F4F3EE` |
| Secondary text | `#AAA89F` |
| Hairline | `rgb(255 255 255 / 9%)` |
| Strong border | `rgb(255 255 255 / 16%)` |
| Flight blue | `#7992FF` |
| Soft blue | `rgb(80 108 255 / 18%)` |

Implementation requirements:

- Express final values as semantic CSS variables in `frontend/src/app/globals.css`; OKLCH values are preferred after visual validation.
- Preserve existing shadcn token names where they already express the correct role.
- Add role aliases such as `--surface-raised`, `--surface-recessed`, `--text-secondary`, and `--border-strong` only when an existing token cannot express the distinction.
- Keep status tokens (`success`, `warning`, `attention`, `info`, and `destructive`) separate from brand tokens.
- Remove `--gradient-primary`, `--gradient-primary-hover`, and primary glow usage after all consumers have migrated.
- Do not introduce raw Tailwind palette colors in feature components.

### Typography

Use a two-family system:

- **Display:** self-hosted Instrument Sans variable font, subject to a final rendering and licensing check, for the wordmark, marketing headlines, page titles, major empty-state titles, and selected narrative headings.
- **Interface:** Geist Sans for controls, navigation, tables, transcripts, forms, and body copy.
- **Monospace:** Geist Mono for code, SHAs, logs, command output, keyboard shortcuts, and identifiers.

Target scale:

| Role | Desktop size / line height | Notes |
|---|---|---|
| Marketing display | `clamp(48px, 7vw, 88px)` / `0.98` | Public site only. |
| Page title | `28px / 34px` | `24px / 30px` on mobile. |
| Section title | `18px / 24px` | Major page sections. |
| Feature/card title | `14px / 20px` | Use weight and spacing before increasing size. |
| Body | `14px / 21px` | New default for readable product copy. |
| Dense UI | `13px / 18px` | Tables, session lists, and dense controls. |
| Metadata | `12px / 16px` | Timestamps, SHAs, counts, and badges only. |

The global body should move from 13px to 14px. Dense surfaces must opt into the 13px role rather than inheriting it across the application. Page descriptions should no longer default to 12px.

### Spacing and layout rhythm

Continue using Tailwind's 4px-based spacing scale, but standardize compositions around these tiers:

- `4–8px`: icon-label gaps, status metadata, and compact control internals.
- `12–16px`: row padding, dense card padding, and related control groups.
- `24–32px`: page section separation and normal page gutters.
- `48–72px`: narrative sections, onboarding, empty states, and public pages.

Page headers should establish consistent top and bottom rhythm through `PageHeader` and page layout primitives instead of one-off margins.

### Corners

Use radius to describe object type:

| Object | Radius |
|---|---:|
| Inputs, buttons, tabs, and compact controls | `6px` |
| Cards, composers, menus, dialogs, and resource groups | `10–12px` |
| Marketing compositions and large illustrative frames | `20–28px` |
| Avatars and status dots | Fully round |

Avoid fully rounded pills for ordinary actions. Pills remain appropriate for compact status, filters, keyboard hints, and identity chips.

### Borders, elevation, and depth

Use three levels only:

1. **Canvas:** no border or shadow.
2. **Durable surface:** hairline border or tonal background, normally not both.
3. **Floating surface:** raised background, hairline border, and a soft ambient shadow for menus, popovers, dialogs, and deliberately floating marketing fragments.

The base `Card` must not have hover styling. Introduce an explicit interactive-card variant or component for clickable cards. Avoid shadows on tables, ordinary settings sections, and static list rows.

### Iconography and brand motifs

Lucide remains the default functional icon library. Distinction should come from composition and a small set of first-party assets, not from replacing hundreds of icons.

Create first-party assets for:

- a refined 143 wordmark and compact mark that remain legible at sidebar size;
- session/run state symbols where generic media icons are ambiguous;
- subtle engineering/flight annotations for marketing and empty states;
- an archival image treatment based on the XP-80 story and appropriately licensed imagery.

Do not use decorative sparkles, magic wands, glowing orbs, robot heads, or generic neural-network motifs as the primary visual shorthand for agents.

### Motion

Use motion to clarify state and spatial change:

- control feedback: `100–140ms`;
- selection and row transitions: `160–200ms`;
- panel, dialog, and composed marketing transitions: `200–280ms`;
- working-state loops: slow, low-contrast, and limited to the smallest meaningful indicator.

All new animation must respect `prefers-reduced-motion`. Avoid perpetual animation outside live working states.

## Shared Component Changes

### Existing primitives

Update the following shared components before broad page migration:

- `Button`: solid primary fill, quieter outline treatment, no gradient or glow, and clearly different secondary/ghost priority.
- `Card`: remove default hover treatment; provide static, interactive, selected, and elevated variants.
- `Badge`: distinguish identity, count, filter, and status usage. Status variants use soft tints or dot-plus-label patterns instead of saturated fills by default.
- `Input`, `Textarea`, `Select`, and `Tabs`: move to the new radius, border, typography, and focus treatment.
- `PageHeader`: adopt the page-title scale, 14px descriptions, stable action alignment, and consistent section spacing.
- `EmptyState`: provide compact, standard, and narrative variants without relying on a bordered card.
- `Table` and `ResponsiveResourceList`: align row rhythm, metadata hierarchy, selected state, mobile behavior, and action placement.

### New application primitives

Add shared application-level components where repeated patterns already exist:

#### `StatusLabel`

Owns status dot/icon, semantic color, label, optional explanation, and optional activity animation. Feature code supplies a typed state and copy; it does not compose arbitrary badge colors.

#### `ResourceRow`

Owns the common operational-list hierarchy:

```text
[icon/state] Primary label                       Primary or overflow action
             Secondary context · metadata        Optional status explanation
```

It must support desktop columns and the current mobile responsive-list behavior without duplicating feature markup.

#### `SectionGroup`

Groups related content through heading, description, spacing, and optional dividers. A bordered container is an explicit variant, not the default.

#### `ContextHeader`

Owns the hierarchy within an existing workspace pane: title, editable title affordance, status, metadata, tabs, and actions. It does not move or replace global navigation.

#### `InteractiveCard`

Provides the focus, hover, selected, and pressed behavior that must be removed from the static base `Card`.

## Product Surface Specifications

### Sessions

Sessions are the flagship migration because they contain the broadest mix of navigation, dense lists, conversation, activity, status, code review, previews, and actions.

#### Session list

- Keep the existing session-sidebar placement and resizing behavior.
- Increase hierarchy between title, lifecycle state, time, PR state, and diff summary.
- Use one selection signal: a soft blue background and narrow leading indicator. Do not combine ring, shadow, border, and background.
- Reduce status pills. Use `StatusLabel` or compact inline metadata for ordinary lifecycle state.
- Keep search, ownership filters, tabs, and creation behavior unchanged.
- Preserve virtualization, caching, optimistic sessions, keyboard navigation, and mobile behavior.

#### Session header

- Consolidate title, editable-title affordance, run state, branch/PR state, diff summary, and primary action into `ContextHeader`.
- Keep tabs and existing page placement.
- Show one dominant action at a time; secondary actions belong in ghost buttons or overflow menus.
- Do not use saturated state fills across the full header.

#### Transcript

- Replace the gradient user bubble with a quiet soft-blue or raised-surface treatment.
- Render agent prose as the primary reading surface, with less gray-card framing.
- Group tool calls and logs into compact activity clusters with a single summary row and progressive disclosure.
- Give human-input requests, errors, and blocked states clear structural emphasis without making routine agent activity equally prominent.
- Keep markdown, syntax highlighting, attachment, mention, and windowing behavior unchanged.

#### Composer

- Treat the composer as the primary elevated object in the transcript pane.
- Use a calm raised surface, strong focus boundary, and clearer separation between prompt content, attachments, model selection, and send state.
- Preserve shortcuts, attachment delivery, slash commands, mentions, drafts, file drop, and disabled-state rules.

#### Overview, review, preview, and diff panes

- Use consistent context headers and section groups.
- Remove unnecessary nested cards inside already bounded side panes.
- Standardize file rows, review status, preview status, and metadata with the new shared primitives.
- Preserve pane resizing, full-screen diff mode, file ordering, keyboard review, and responsive behavior.

### Previews, Code Reviews, Automations, and Autopilot

These pages should converge on an operational-resource visual language while retaining their individual workflows.

#### Shared rules

- Prefer one continuous resource surface per section over multiple bordered cards or tables.
- Each row has one primary label, one secondary metadata line, one status treatment, and at most one visually dominant action.
- Section headings and whitespace establish grouping; use a border only when the group must read as a durable object.
- Empty, loading, error, and permission states occupy the same geometry as loaded content to avoid layout jumps.
- Filters and search remain URL-backed where they are already implemented with `nuqs`.

#### Previews

- Preserve the current Running, Needs attention, Ready to resume, and Recent semantics unless a separate product design changes them.
- Replace saturated Ready and Out-of-date pills with `StatusLabel` treatments that retain accessible contrast.
- Reduce each preview to name/source, repository and SHA context, state explanation, and contextual action.
- Make pool/capacity metadata visible but subordinate to the section title.

#### Code reviews

- Emphasize repository/PR identity, review state, risk decision, and the next reviewer action.
- Treat findings counts and file counts as metadata, not competing colored badges.
- Carry the same file-row and status language into the review detail and diff surfaces.

#### Automations

- Keep existing automation goals, schedules, filters, enable/pause semantics, and template behavior.
- Render existing automations as operational resource rows rather than generic cards.
- Treat the template library as an editorial gallery with stronger type, outcome-oriented summaries, and less nested card chrome.
- Limit template tags to information that helps selection; do not display tags merely to fill space.

#### Autopilot

- Preserve the current readiness, steering, queue, evidence, and autonomy controls.
- Use whitespace and section rhythm to separate configuration, state, and evidence.
- Reserve color for readiness problems and active work rather than decorating every subsystem.

### Settings and Integrations

The current Settings location and sidebar structure remain unchanged.

- Keep category navigation visible only according to the existing Settings behavior.
- Use plain section groups for explanatory settings and bordered/elevated surfaces only for credentials, destructive actions, or independently managed objects.
- Increase setting descriptions to the body role where they affect security, cost, or runtime behavior.
- Align labels, saved state, autosave indicators, validation errors, and last-activity metadata across all settings pages.
- Present integration identity, purpose, connection state, and action as one strong row hierarchy.
- Preserve provider setup flows, permissions, mutation behavior, and audit logging.

### Public Site and Product Storytelling

The public site should migrate after the authenticated product has established the final visual system.

#### Direction

- Preserve the flying P-80 hero canvas and existing homepage story while refining shared controls and supporting visual details around it.
- Use the 143/XP-80 origin as a source of atmosphere: archival aerospace photography, engineering annotations, restrained technical diagrams, and the idea of a small autonomous workshop.
- Compose real product fragments rather than dropping full unedited dashboard screenshots into generic browser frames.
- Use negative space, large display typography, warm canvas colors, and selective floating depth.
- Keep copy concise and concrete: sessions, shared context, cloud execution, previews, review loops, automations, and PRs.
- Maintain a small number of intentional transitions rather than continuous ambient animation.

#### Brand coherence

- The public site and application must share color, type, radii, button hierarchy, icon treatment, and product terminology.
- Marketing may use more generous radius, spacing, photography, and display type, but it must not introduce a separate primary color or control language.
- Product compositions must be generated from maintained demo states so they do not drift from the shipped interface.

## Responsive Behavior

This refresh must preserve the current navigation and workflow behavior at all breakpoints.

- Mobile controls retain at least 44px touch targets even when desktop controls remain compact.
- Page titles and actions stack without pushing primary content below unnecessary empty space.
- Resource rows collapse into the established mobile label/value hierarchy.
- Session panes retain current mobile routing and back-button behavior.
- No page may introduce horizontal viewport scrolling outside intentional code/diff regions.
- Large display typography is limited to narrative surfaces and scales with `clamp()`.

## Accessibility

- Text and meaningful icons must meet WCAG AA contrast against every supported surface.
- Status must never be communicated by color alone.
- Focus rings remain visible on warm light surfaces and dark charcoal surfaces.
- Static cards must not receive interactive semantics or hover-only affordances.
- Interactive rows must have a single understandable focus target or documented nested-action behavior.
- Motion respects `prefers-reduced-motion` and does not block task completion.
- Typography changes must be checked at 200% zoom and with long titles, repository names, and translated browser UI settings.

## Implementation Strategy

Avoid a single global token flip before representative pages are ready. The migration should establish the new system in layers while avoiding a long-lived mixture of two unrelated visual languages.

### Phase 0: Direction validation

Create high-fidelity designs for three representative surfaces:

1. Session detail with session list, transcript, activity group, composer, and overview pane.
2. Previews index with running, attention, resumable, and recent states.
3. Public landing hero with 143-specific imagery and a composed product fragment.

Validate light and dark themes at 1440px, 1024px, and 390px. Lock typography, palette relationships, resource-row hierarchy, status treatment, and component radii before broad implementation.

### Phase 1: Foundations

1. Add the display font and semantic token additions.
2. Update shared typography utilities and `PageHeader`.
3. Update Button, Card, Badge, form controls, Tabs, Table, and EmptyState.
4. Add StatusLabel, ResourceRow, SectionGroup, ContextHeader, and InteractiveCard.
5. Add focused component tests for variant semantics, accessible state labels, and interactive/static behavior.

Keep the legacy token values mapped until the representative vertical slice is visually complete. Remove gradient/glow variables only after their consumers have migrated.

### Phase 2: Sessions vertical slice

1. Migrate session list selection and metadata hierarchy.
2. Migrate the session context header.
3. Migrate transcript prose, user messages, activity groups, and special states.
4. Migrate the composer.
5. Align overview, review, preview, and diff pane chrome.
6. Verify responsive layout, keyboard navigation, transcript performance, and dark theme.

Once Sessions is accepted, make the new base palette and typography the application default.

### Phase 3: Operational pages

Migrate in this order:

1. Previews, because it validates sections, status, resource rows, and contextual actions.
2. Code reviews, because it reuses status and file-row language.
3. Automations, including the template gallery.
4. Autopilot, including configuration, readiness, and evidence sections.

Each page migration includes loading, empty, error, permission, mobile, and dark-theme states.

### Phase 4: Settings and integrations

1. Migrate shared settings section composition.
2. Migrate connection and credential rows.
3. Standardize autosave, validation, destructive-action, and activity treatments.
4. Spot-check every settings route for nested-card and metadata regressions.

### Phase 5: Public brand

1. Finalize the wordmark and first-party visual assets.
2. Preserve and regression-test the existing flying P-80 hero canvas.
3. Build maintained product compositions from the refreshed application.
4. Align marketing sections, docs entry points, footer, auth, and onboarding with the system.
5. Refresh public screenshots only after the underlying demo states are stable.

### Phase 6: Cleanup

1. Remove unused gradient/glow tokens and legacy component variants.
2. Search for raw palette colors, one-off shadows, duplicate status badges, and feature-local card patterns.
3. Consolidate remaining resource rows and section groups.
4. Update `docs/design/03-frontend.md` with the implemented visual-system rules when the migration is complete.

## Expected Frontend Touchpoints

The implementation will primarily affect:

```text
frontend/src/app/globals.css
frontend/src/app/(dashboard)/**
frontend/src/app/(landing)/**
frontend/src/components/authenticated-layout.tsx   # styling only; placement remains unchanged
frontend/src/components/page-header.tsx
frontend/src/components/page-container.tsx
frontend/src/components/empty-state.tsx
frontend/src/components/responsive-resource-list.tsx
frontend/src/components/chat-timeline.tsx
frontend/src/components/ui/**
frontend/src/components/landing/**
frontend/public/product/**
```

The navigation implementation may receive token, typography, wordmark, spacing, and selected-state styling changes. It must not receive structural placement, route, hierarchy, or behavior changes under this design.

## Testing and Verification

### Focused component tests

- Button and card variant behavior.
- Static cards do not expose interactive affordances.
- StatusLabel announces state text independently of color.
- ResourceRow preserves actions and metadata in desktop and mobile compositions.
- PageHeader title, description, and actions remain accessible at responsive widths.

### Feature tests

Preserve existing behavioral tests for Sessions, Previews, Code reviews, Automations, Autopilot, Settings, and landing pages. Update assertions only when visible hierarchy or copy intentionally changes; do not weaken workflow assertions to accommodate styling work.

### Visual verification

Capture representative screenshots at:

- 1440x900 desktop;
- 1024x768 compact desktop/tablet;
- 390x844 mobile;
- light and dark themes;
- loading, empty, populated, error, and active-working states where relevant.

After each UI phase, use the session preview workflow to update the running application and inspect screenshots, interactions, console errors, keyboard focus, and overflow before reporting the phase complete.

### Performance verification

- The display font must be self-hosted, subset where practical, and must not introduce a runtime third-party request.
- Decorative marketing imagery must use responsive formats and explicit dimensions.
- Transcript and resource-list changes must not introduce broad rerenders or remove existing windowing/virtualization behavior.
- Continuous working-state animation should affect only composited properties where practical.

## Acceptance Criteria

The design is implemented when:

1. Global navigation remains in its current location and retains its route hierarchy and behavior.
2. Light and dark themes use the warm mineral/charcoal surface system across authenticated and public pages.
3. Primary product buttons and user chat messages no longer use blue-purple gradients or glow.
4. The global readable body role is 14px, with 13px applied explicitly to dense UI and 12px reserved for metadata.
5. Static cards have no hover affordance; interactive cards opt into one.
6. Sessions use the refreshed hierarchy for list selection, context header, transcript, activity groups, composer, and supporting panes.
7. Previews, Code reviews, Automations, and Autopilot use shared status, resource-row, and section-group primitives.
8. Settings and integration rows share a consistent identity/state/action hierarchy without changing their location or setup behavior.
9. The public site uses the same core visual system and expresses a distinct 143 engineering/flight identity.
10. Representative screens pass responsive, keyboard, contrast, reduced-motion, console-error, and visual review checks.
11. Legacy gradient/glow tokens and obsolete one-off variants are removed.

## Risks and Mitigations

### A global token change causes unrelated regressions

Add roles first, validate a Sessions vertical slice, then switch the application defaults. Capture representative screenshots before and after the token flip.

### Reduced chrome harms scannability

Remove borders only when typography, spacing, alignment, or tonal grouping replaces their structural role. Dense data and durable pane boundaries may retain hairlines.

### Larger type reduces useful density

Use 14px for readable copy and retain an explicit 13px dense role for session lists, tables, logs, and compact controls. Validate at 1024px before applying globally.

### Brand motifs become decorative clutter

Confine archival imagery and engineering annotations primarily to public, onboarding, and empty-state surfaces. Operational screens should express the brand through restraint, type, color, and rhythm.

### Page-by-page migration leaves a mixed system

Keep phases short, migrate shared primitives before feature pages, and remove legacy variants during the final cleanup phase. Do not allow new feature work to introduce additional one-off visual patterns during the migration.

## Open Questions

1. Does Instrument Sans provide enough distinction in the final product compositions, or should a different self-hosted display family be evaluated before Phase 1?
2. Should the compact 143 mark remain a refinement of the existing black square or become a new first-party symbol?
3. Which archival XP-80 imagery is licensed for commercial public-site use?
4. Should the refreshed public hero retain a minimal interactive canvas as a secondary layer, or remove canvas rendering entirely?
5. Which demo organization and product states should become the maintained source for marketing and public-doc screenshots?

## Related Documents

- [Frontend Architecture](../03-frontend.md)
- [Homepage Positioning Refresh](81-homepage-positioning-refresh.md)
- [Homepage Product Screenshots](88-homepage-product-screenshots.md)
- [Autopilot Visual Simplification](38-autopilot-visual-simplification.md)
- [Code Review Display](36-code-review-display.md)
- [Current-Oriented Preview Index](102-preview-index-current-targets.md)
