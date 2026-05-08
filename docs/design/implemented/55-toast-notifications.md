# Design: Toast Notifications

> **Status:** Implemented | **Last reviewed:** 2026-05-06

This document defines how toast notifications should look and behave across the platform.

## Problem

The current toast UI feels janky because it mixes three different design systems in one surface:

- Sonner still owns part of the composition and interaction model.
- The app partially skins Sonner with custom utility classes.
- Error toasts borrow color and typography from the newer recoverable-error card language, while success/info/warning toasts use a lighter ad hoc treatment.

The most obvious symptom is the close button. It is globally enabled at the toaster level, but the toast body is otherwise custom-styled. The result is a dismiss control that feels bolted on rather than designed as part of the object.

## Current implementation

The current toaster lives in `frontend/src/components/ui/app-toaster.tsx`.

- It uses `sonner` with `unstyled: true`.
- It enables `closeButton` globally for every toast.
- It styles `error` toasts with the same container/title/description tokens used by `ErrorNotice`.
- It styles success/info/warning/loading separately with lighter tinted backgrounds.

This likely was not an intentional "close button on the left" product decision. The more plausible explanation is:

1. The team wanted a fast global toast system.
2. The team later introduced a stronger recoverable-error visual language.
3. The toaster was partially aligned to that newer language.
4. The default Sonner close-button layout was left in place even though the surrounding composition was no longer default Sonner.

So the current state reads more like an incremental integration artifact than a finished design decision.

## Why it looks bad

### 1. The control hierarchy is wrong

The dismiss affordance competes with the message instead of quietly supporting it.

- The close button sits too early in the reading path.
- It visually interrupts the icon/message grouping.
- It feels like a foreign object rather than part of a composed header.

For ephemeral toasts, dismissal is secondary. The message should dominate, not the chrome.

### 2. The toast has no owned anatomy

The app has not fully claimed the toast as a first-class product component.

- Error toasts resemble `ErrorNotice`, but not fully.
- Success toasts are simple tinted rectangles with no real structure.
- Action and cancel buttons are locally styled, not part of a toast-specific primitive.

The eye sees inconsistency before it sees polish.

### 3. Status semantics are inconsistent

Errors are visually assertive. Success/info/warning are comparatively flat. The system does not establish a consistent pattern for:

- icon treatment
- title/body hierarchy
- action placement
- dismissal behavior

This makes each variant feel hand-tuned instead of systematized.

### 4. The layout feels plugin-shaped, not product-shaped

`expand` plus full-width custom classes produces a "stack of skinned banners" feeling. That can be useful for long actionable messages, but it is too large and too blunt for routine confirmations like:

- `Created Acme`
- `Auth removed`
- `PR merged`

Those should feel crisp, compact, and calm.

## Design direction

The platform should treat toasts as **brief, precise status objects**.

The visual model should be:

- quiet by default
- structured enough to feel intentional
- assertive only when the severity demands it
- consistent with app cards, buttons, and error surfaces

Think less "browser alert" and more "small, beautifully composed instrument panel event."

## Proposed system

### Toast anatomy

Every toast should use the same structure:

1. Status icon on the left
2. Content block with title and optional description
3. Optional action area
4. Optional dismiss control in the top-right corner

This keeps the reading order stable and makes dismissal clearly secondary.

### Dismiss behavior

Dismiss should not be universal.

- `success`: no visible close button by default; auto-dismiss quickly
- `info`: no visible close button by default unless the message is persistent or actionable
- `warning`: show dismiss only when the toast persists long enough to merit manual control
- `error`: show dismiss when the toast is actionable, long-lived, or multi-line

If shown, the dismiss control should live in the top-right of the toast header area, not in the left-side content flow.

### Variant language

All variants should share one base component and differ only in semantic accents.

- `success`: neutral card with a restrained emerald accent and affirmative icon
- `info`: neutral card with a restrained sky accent
- `warning`: neutral card with a restrained amber accent
- `error`: neutral card with stronger destructive accent, matching `ErrorNotice`

The component should avoid full-surface saturation. Color should guide, not flood.

### Size and density

Use two effective sizes without exposing a formal public API yet:

- **compact** for one-line confirmations
- **standard** for title + description and optional action

Most platform toasts should be compact.

### Motion

Motion should reinforce calmness and precision.

- quick entrance, no dramatic springiness
- short fade/slide
- similarly restrained exit

Toasts should feel like they arrive exactly when needed and leave without fanfare.

## Platform rules

### 1. Success toasts are confirmations, not mini-alerts

For routine success:

- keep copy short
- avoid descriptions unless genuinely useful
- avoid persistent close buttons
- prefer auto-dismiss

Examples:

- `Organization created`
- `PR opened`
- `Auth removed`

### 2. Error toasts should not carry the whole debugging payload

Error toasts should communicate the failure and, when possible, one next step. Long-lived explanation belongs inline near the relevant feature, consistent with the existing recoverable-error pattern.

This matches the existing product direction for PR creation failures: a short toast for the event, durable inline detail for recovery.

### 3. Actions belong inside the toast only when they are immediate

Good toast actions:

- `Retry`
- `Open PR`
- `Undo`

Bad toast actions:

- multi-step workflows
- configuration journeys
- anything that requires reading a large amount of context

### 4. Copy should be normalized

Prefer:

- noun + past-tense outcome for success
- concise direct failure statement for error
- optional short description only when it changes the next action

Avoid a mix of clipped labels, full sentences, and internal phrasing.

## Implementation direction

### Short term

Improve the existing Sonner setup without changing all call sites:

1. Disable the global close button.
2. Introduce a single custom toast content primitive owned by the app.
3. Render dismiss explicitly inside that primitive when a toast opts into manual dismissal.
4. Align success/info/warning/error anatomy so they all use the same layout.
5. Reduce routine toast width and visual weight.

This is the fastest path to removing the current "janky" feeling.

### Medium term

Add a tiny wrapper API around `sonner` so callers express intent rather than styling details.

Examples:

- `notify.success("Organization created")`
- `notify.error({ title: "PR creation failed", description: "...", action: ... })`
- `notify.info({ title: "...", persistent: true })`

That gives the design system a place to encode duration, iconography, dismiss policy, and action styling centrally.

## Decision

Adopt a platform-owned toast composition with:

- top-right dismiss, only when needed
- consistent icon/title/body/action structure
- compact success toasts
- error toasts visually aligned with `ErrorNotice`
- neutral-first card styling with restrained semantic accents

The current left-biased close button should be treated as an integration artifact, not as a design pattern to preserve.
