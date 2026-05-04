# Design: Mobile Diff Review Viewport

> **Status:** Implemented | **Last reviewed:** 2026-05-03

> **Implementation notes:** Mobile changed-file review now uses a dedicated full-screen diff reader. The session-level header, agent tabs, shared composer, and footer are removed while that reader is active on phone-sized viewports so code gets the screen. The mobile `Changes` sheet now acts as the file index instead of a second review surface, the review toolbar is compact and icon-first, and new review comments open in a temporary bottom sheet rather than permanently consuming diff-row space.

## Summary

When a user opens changed files on a phone, the current stacked layout leaves too little room for actual code. The mobile review experience should prioritize **code visibility first**, with navigation and comment entry treated as secondary surfaces that appear only when needed.

We should take a two-step path:

1. Ship a short-term **chrome compaction pass** in the current mobile review surface.
2. Follow with a **dedicated full-screen mobile diff reader** that becomes the default way to review changed files on phones.

This is the right sequence because it improves the current experience quickly without locking us into the wrong architecture. The compaction pass buys relief now; the dedicated reader fixes the underlying usability problem.

## Problem

On phones, the changed-files flow currently spends too much vertical space on surrounding UI:

- top-level page/header chrome,
- secondary review headers/tabs,
- persistent comment/composer UI at the bottom.

That leaves a narrow strip for the code itself, which is the primary task. The result is high scroll friction, poor line-by-line comprehension, and a review experience that feels cramped even before long lines or inline comments are considered.

This is not a spacing bug. It is a surface-allocation bug. The UI is spending too much of a scarce resource, vertical viewport height, on controls that are supportive rather than primary.

## Goals

- Maximize visible diff/code area on phones.
- Preserve quick access to file navigation, status, and comments.
- Avoid making review actions feel hidden or hard to discover.
- Reuse the existing desktop mental model where possible.
- Keep the mobile interaction model calm and legible under real review conditions: long files, long lines, inline comments, and repeated back-and-forth between files.

## Non-Goals

- Rebuilding the desktop diff experience on mobile one-for-one.
- Supporting split diff on phones.
- Keeping every piece of chrome permanently visible.

## Product Direction

The long-term design should be a **dedicated full-screen mobile diff reader**.

The near-term release should be a **compaction pass** on the current mobile layout.

These are not competing ideas. They are two phases of the same plan:

- Phase 1 reduces immediate pain without destabilizing the review workflow.
- Phase 2 moves the product to the correct mobile interaction model.

## Why This Direction

On mobile, the job is simple: read code, understand the change, move to the next relevant section, and comment when needed.

The current layout dilutes that by trying to keep multiple desktop concerns simultaneously visible:

- session-level chrome,
- review metadata,
- file navigation,
- comment entry.

That tradeoff is acceptable on desktop because horizontal and vertical space are abundant. On phones it is not. A strong mobile design should aggressively protect the reading surface and make everything else appear on demand.

## Phase 1: Compact The Existing Mobile Layout

Keep the current surface but reduce the footprint of the surrounding chrome.

### Objectives

- Recover meaningful viewport height quickly.
- Remove permanently-mounted UI that is not needed for passive reading.
- Prepare the interaction model for the dedicated reader by making file navigation and commenting more intentional.

### Specific changes

- Collapse the top stack into a **single compact mobile header**.
  The header should contain only back, title/file context, and one overflow/action entrypoint. Session metadata, review stats, and secondary actions should move out of the always-visible stack.
- Convert any secondary tab/status/header rows into a **single condensed control row** or overflow menu.
  Mobile should not inherit multiple stacked sticky bars from desktop.
- Remove the permanently visible bottom comment textarea from passive review mode.
  The comment composer should appear only after explicit intent: tap a comment affordance, reply to an existing thread, or invoke `Add comment`.
- Tighten vertical spacing throughout the diff surface.
  Mobile review should use compact paddings, compact file headers, and compact hunk separators. The default rhythm should favor code density over decorative breathing room.
- Audit every sticky element.
  Sticky UI on mobile is expensive. Each sticky region should justify its permanent claim on viewport height. In practice this likely means one global top bar and, at most, one lightweight local file header.

### UX rules

- Reading is the default state. Commenting is a temporary state.
- Navigation should not consume persistent height unless the user is actively navigating.
- The code should remain visually dominant at rest.

### Expected outcome

This phase should make the current experience materially less cramped, but it should be treated as a bridge. It will improve usability, not complete the job.

## Phase 2: Dedicated Full-Screen Mobile Diff Reader

Once a user on mobile chooses to review changed files, the product should take them into a surface that is purpose-built for reading diffs on a phone.

### Core model

- Tapping `Files changed` should open the dedicated reader directly.
- The reader should take over the viewport.
- The file list should become a **sheet-based index**, not a permanently visible panel.
- The comment composer should be **invoked on demand**, not mounted by default.
- The reader should optimize for **one-file, one-reading-position** focus.

### Specific interaction design

#### Top bar

Use a thin, stable top bar with:

- back,
- current file name,
- file position or progress,
- overflow actions.

This bar should be visually quiet and height-constrained. It exists to preserve orientation, not to carry the full session header.

#### File navigation

The file list should open as a bottom sheet from the reader.

Requirements:

- show changed files in diff order,
- preserve per-file change stats,
- support quick jump to a file,
- close cleanly back into the current reading position.

The file list is important, but it does not need to be always visible to be useful.

#### Commenting

Commenting should be initiated from the line or hunk context, then resolved in a temporary surface:

- an anchored compact composer if the viewport and line position allow it,
- otherwise a compact bottom sheet composer.

The key rule is that the comment flow should feel spatially tied to the selected code without permanently occupying reading space.

#### File headers and hunk structure

Within the reader, file headers should be compact and legible. They should preserve orientation, but they should not feel like mini page headers. Hunk separators and expansion controls should be visually lightweight so the eye stays on code.

#### Motion and transitions

Transitions should be fast and quiet:

- opening the file index should feel like summoning a tool, not navigating away,
- opening a comment composer should feel local to the selected code,
- returning from the reader should preserve the user's session context.

### UX rules

- Mobile review defaults to a single-column unified diff.
- The shared session composer should not permanently occupy space inside the reader.
- Session-level controls belong outside the core reading path unless the user explicitly asks for them.
- At-rest state should show as much code as possible without harming orientation.

### Expected outcome

This is the first version of the mobile experience that is genuinely designed for a phone rather than compressed from desktop. It should materially improve readability, reduce review fatigue, and make changed-file review feel intentional instead of compromised.

## Rollout Plan

### Phase 1: Compaction pass on current mobile review

- Reduce header stack height.
- Hide persistent comment textarea until invoked.
- Tighten spacing and sticky elements.
- Move non-essential controls into overflow or secondary surfaces.

### Phase 2: Introduce dedicated mobile diff reader as the default mobile path

- Add direct entry from `Files changed`.
- Move file index into a sheet.
- Keep only minimal persistent reader chrome.
- Treat the current `Changes` panel as the file index/navigation surface, not the main reading surface.

### Phase 3: Optional polish

- Add restrained auto-hide behavior only if testing shows clear benefit.
- Add swipe or edge-step file navigation only if it improves navigation without accidental input.

## Decision

The product should pursue a two-step plan:

1. **Compact the current mobile review surface now** to remove avoidable chrome and reclaim viewport height.
2. **Move mobile changed-file review into a dedicated full-screen diff reader** as the default long-term experience.

This is the most usable path. It improves the present quickly, but it does not confuse a temporary spacing fix for a complete mobile design.
