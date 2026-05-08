# Design: Mobile Session Top Bar Consolidation

> **Status:** Implemented | **Last reviewed:** 2026-05-06

> **Implementation notes:** Mobile session detail now uses a dedicated top bar with back navigation, the existing details-panel icon as the persistent details affordance, and a right-side action sheet for thread switching and other low-frequency session actions. The persistent mobile tab strip has been removed, mobile rename now routes through a secondary dialog instead of destabilizing the header, and the desktop session header/tab layout remains unchanged.

## Summary

Mobile session detail currently behaves like a compressed desktop page. That is the wrong model.

On phones, the primary job is to read the conversation, inspect progress, and continue the active session. The UI should treat the session as a focused drill-in surface, not as a place to keep list-level navigation chrome alive.

The product direction is:

- remove the stacked mobile header model,
- collapse the current mobile header layers into one compact top bar,
- keep only a back affordance for leaving the session,
- remove in-session mobile access to search and new-session creation,
- preserve a first-class `Open session details` affordance in the top bar,
- move thread switching and other secondary session controls into the right-side menu.

This is a density win, but more importantly it is a hierarchy win. It makes the transcript the dominant surface again.

## Decision

Adopt **one mobile pattern only**:

**Single top bar with persistent session-details access.**

There should not be multiple competing mobile variants for this surface. The implementation should converge on one strong opinion and polish it.

## Problem

The current mobile session detail stack spends too much vertical space on chrome that is supportive rather than primary:

1. the dashboard or layout header,
2. the session header,
3. the agent tab strip.

That stack is especially costly because each layer claims both height and attention. The transcript starts too low, the page feels busy before the user has even read a message, and the navigation model mixes two scopes:

- list-level actions like search and create session,
- in-session actions like opening details, switching threads, and checking changes.

Those scopes should not compete on a phone.

## Goals

- Maximize visible conversation area on phones.
- Preserve orientation without preserving redundant chrome.
- Keep the details surface immediately reachable.
- Keep multi-thread sessions usable without paying persistent chrome cost.
- Push non-primary controls into progressive disclosure surfaces.
- Deliver a mobile UI that feels intentional and polished, not merely reduced.

## Non-Goals

- Reworking desktop session detail.
- Removing thread support on mobile.
- Preserving every existing action as top-level visible chrome.
- Introducing a second permanent navigation row under the new top bar.

## UX Principle

On mobile, an open session is not a dashboard. It is a focused workspace.

That principle should drive the surface:

- The back button exists because users need a reliable escape hatch.
- The title exists because users need orientation.
- The details affordance exists because overview, changes, and preview are the main secondary navigation model for a session.
- Everything else should justify its claim on persistent height.

## Proposed Surface

### At-rest layout

```text
┌──────────────────────────────────────────────┐
│ ←  Session title     Open details       ⋯    │
└──────────────────────────────────────────────┘
│                                              │
│ conversation content                         │
│                                              │
│                                              │
├──────────────────────────────────────────────┤
│ composer                                     │
└──────────────────────────────────────────────┘
```

### Top bar contract

- **Left:** back button to the sessions list.
- **Center:** truncated session title.
- **Right-primary:** the existing details-panel icon button used elsewhere in the session UI for opening details.
- **Far right:** session-specific overflow menu.

This is one visual bar, not a bar plus a sub-row. If status or metadata needs to exist, it should appear inside the details surface or the overflow menu, not by rebuilding stacked header chrome.

## Primary Mobile Affordance: Details Panel Button

The top bar should keep the existing details-panel open affordance as the primary persistent control after back navigation.

It should:

- be obvious and easy to hit,
- reuse the same iconography already associated with opening the session details panel,
- map directly to the existing mobile details surface,
- open the sheet that contains `Overview`, `Changes`, `Validation`, and `Preview`,
- feel like the canonical way to navigate within a session beyond the transcript itself.

Recommended visual form:

- icon button using the same panel-open icon already present in the product,
- same visual language as the current details affordance so the interaction is immediately recognizable,
- accessible label such as `Open session details`,
- stable meaning; do not change the icon or semantics based on the currently selected details tab.

This control matters more than thread switching because it unlocks the session’s actual secondary information architecture:

- `Overview` for status and metadata,
- `Changes` for diff/file navigation,
- `Validation` for checks,
- `Preview` when available.

If we keep only one persistent non-back action in the mobile top bar, this is the correct one.

## Overflow Menu

The right-side menu becomes the home for controls that are useful but not important enough to permanently claim top-bar space.

This menu can contain:

- switch thread,
- add thread,
- thread-specific actions,
- rename session,
- session-level actions that are currently iconized in the header,
- any low-frequency actions that do not belong in the details sheet.

Thread navigation is still supported. It is simply not treated as the primary persistent mobile affordance.

Wireframe:

```text
┌──────────────────────────────────────────────┐
│ Session actions                        ✕     │
├──────────────────────────────────────────────┤
│ Switch thread                                │
│ New thread                                   │
│ Rename session                               │
└──────────────────────────────────────────────┘
```

If thread switching itself needs a richer surface, the overflow can push into a second sheet for thread selection rather than forcing thread chrome into the top bar.

## What Leaves Mobile Session Detail

The following should not remain visible or directly accessible as primary mobile chrome while a session is open:

- search,
- create new session,
- session-list filtering,
- broader sidebar navigation patterns.

Those are list-level concerns. The route back to them is the back button.

This simplification matters. Mobile UX quality often comes from removing nearby-but-wrong actions, not just compressing them.

## Session Actions

The trailing action affordance in the top bar should remain session-scoped.

Acceptable actions:

- open session-specific overflow actions.

Not acceptable:

- recreating the sessions list toolbar,
- exposing broad app navigation,
- turning the top bar into an icon graveyard.

The bar should feel calm. One strong primary action plus one overflow entry is better than several mediocre icons.

## Interaction Details

### Scrolling behavior

- The top bar should stay sticky.
- It should not auto-hide in v1.
- Motion should be quiet and deterministic.

The goal is stability, not cleverness. Auto-hiding chrome is only worth the complexity if the persistent bar is still too expensive after compaction, which this design should avoid.

### Title behavior

- Prefer single-line truncation.
- The title should not push the details action or overflow off-screen.
- Editing the title on mobile should remain possible, but the edit state should not blow up the bar height.

If inline title editing makes the bar too unstable, move editing into a secondary sheet/action flow on mobile rather than preserving the desktop interaction verbatim.

### Status expression

Do not recreate a full badge row.

If thread or session status must be visible at rest, express it with:

- a subtle icon or small status cue near the details action,
- a subdued label inside the details sheet,
- or richer expression inside overview/details content.

The top bar should preserve orientation, not narrate the entire session state model.

## Implementation Guidance

### Component strategy

Do not continue compressing the existing mobile path through conditionals inside the desktop header and `AgentTabStrip` alone. That will keep the mobile surface coupled to desktop assumptions.

Prefer this structure:

- a dedicated mobile session top bar component,
- reuse of the existing mobile details sheet as the primary drill-in surface,
- a dedicated mobile session actions menu or sheet,
- desktop tab strip unchanged except where shared logic can be cleanly reused.

Shared data logic is good. Shared visual structure is not required.

### Existing code boundaries

The current session detail implementation already has strong primitives to reuse:

- `MobileBackButton` for the return path,
- the existing mobile details sheet pattern,
- mobile media-query branching inside `session-detail-content.tsx`.
- `AgentTabStrip` data model and thread state semantics for any menu-driven thread navigation.

What should change is presentation ownership:

- mobile top-bar rendering should move into a focused component,
- mobile should stop rendering the persistent `AgentTabStrip`,
- the details button should become the persistent top-bar action,
- thread switching should move behind the overflow/menu path.

### Quality bar

- No duplicated top-level business logic between mobile and desktop paths.
- No unstable bar heights caused by conditional wrapping.
- No cramped hit targets.
- No ad hoc icon clusters.
- No raw HTML if a shadcn primitive already fits.

The finished surface should feel deliberately designed, not like a responsive exception.

## Rollout Plan

### Phase 1: Mobile chrome consolidation

- Remove the stacked mobile header composition.
- Introduce the new single-row top bar.
- Remove persistent mobile rendering of the full tab strip.
- Hide list-level actions while inside a session.
- Keep `Open session details` as the main persistent secondary action.

### Phase 2: Overflow/menu restructuring

- Move thread switching and related actions into the overflow path.
- Carry status, active state, and attention indicators into that menu or its follow-on sheet.
- Add `New thread` there if product still wants mobile thread creation at this level.

### Phase 3: Mobile interaction refinement

- Tune truncation behavior.
- Refine spacing and token usage.
- Ensure composer, review mode, and details sheet all coexist cleanly with the new top bar.

## Acceptance Criteria

- On mobile session detail, there is exactly one persistent top bar above the transcript.
- Search and new-session creation are not available from the in-session mobile header.
- `Open session details` is available from the top bar at all times.
- Users can switch threads without relying on a full-width persistent tab strip.
- The transcript starts materially higher on the screen than it does today.
- The surface remains visually stable across running, idle, failed, and awaiting-input states.

## Design Standard

This surface should feel like a senior frontend team chose what not to show.

The quality target is not merely “more compact.” The target is a mobile conversation view with:

- strong hierarchy,
- obvious navigation,
- disciplined progressive disclosure,
- and enough restraint that the conversation once again feels like the product.
