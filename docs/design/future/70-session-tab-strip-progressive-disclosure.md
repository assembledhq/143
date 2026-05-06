# Design: Session Tab Strip Progressive Disclosure

> **Status:** Not Started | **Last reviewed:** 2026-05-06

## Summary

The session conversation view should not show a persistent tab strip before the
user has actually created more than one tab. Multi-tab is a powerful but
secondary workflow, so the default single-tab conversation surface should read
as one calm, focused agent canvas rather than a workspace manager.

This refinement keeps tab creation discoverable while reducing visual weight in
the highest-frequency session state.

## Goal

When a session has exactly one tab:

- do not render the full tab strip
- keep transcript and shared composer visually primary
- preserve a discoverable but quiet entrypoint for `Add tab`
- make the surface feel intentionally minimal rather than "feature hidden"

When a session has two or more tabs:

- reveal the full tab strip above the transcript
- preserve the current multi-tab affordances and status signals

## Design Principles

### 1. Default to one surface

The single-tab state should feel like one agent conversation, not a diminished
version of a more complex workspace.

### 2. Reveal complexity only after commitment

Users should see tab-management chrome only after they have chosen to create a
second tab. Before that moment, the product should advertise possibility, not
structure.

### 3. Keep creation local to the conversation

The entrypoint for adding a tab should stay inside the conversation area rather
than moving into distant global chrome. Multi-tab is contextual to the current
sandbox.

## UI Options

### Option A: Quiet inline `+` beside the active agent label

In the single-tab state, replace the tab strip with a plain header row such as:

`Codex`                                                `[ + ]`

The `+` stays at the far right of the conversation header. The strip only
appears after a second tab exists.

Pros:

- least visual weight
- preserves locality; creation still happens where tabs will later appear
- strongest continuity with the existing implemented design
- easy to understand after first use

Cons:

- some users may miss the affordance
- the lone `+` can read slightly mechanical if not styled carefully

### Option B: Header kebab menu with `New tab`

In the single-tab state, remove the visible `+` and place `New tab` inside the
existing header overflow menu for the active tab/agent.

Pros:

- cleanest default surface
- avoids introducing a new visible control in the calm state
- scales if more secondary actions move into the same menu

Cons:

- discoverability drops materially
- adds one extra click to a feature that benefits from experimentation
- hides an important capability too well

### Option C: Composer-adjacent add control

Place `New tab` as a secondary action near the shared composer controls,
adjacent to model/settings actions rather than in the header.

Pros:

- associates tab creation with "start another line of work"
- convenient at the moment the user is already composing an instruction

Cons:

- risks overcrowding the composer, which is the most important control in the
  view
- weakens the mental model that tabs are conversation lanes above the
  transcript
- less elegant once the full strip appears elsewhere

### Option D: Contextual ghost pill in the header

Instead of a bare icon, show a subtle text button such as `Add tab` or `+ New
tab` in the single-tab header. Keep it visually light; remove border weight and
avoid badge-like styling.

Pros:

- better discoverability than icon-only
- still quieter than a full strip
- can feel polished if typography and spacing are restrained

Cons:

- more visible than the feature likely deserves
- text control can compete with the session title/agent identity
- easier to tip into product-management UI rather than refined tool UI

## Recommendation

Adopt **Option A** as the default direction, with **Option D** as a fallback if
testing shows the icon-only affordance is too hidden.

Why:

- It best matches the product principle that a one-tab session should feel like
  one focused conversation.
- It keeps the affordance in exactly the place where the full strip will later
  appear, so the interface "explains itself" once the user adds a tab.
- It removes almost all of the unused chrome without making the capability feel
  buried.

## Placement Recommendation

For the single-tab state:

- keep a very light conversation header row
- show only the active agent label/status on the left
- place a quiet `+` icon button on the right
- do not show tab pills, borders, overlap badges, or queued-message badges

For the multi-tab state:

- replace that quiet header with the full existing tab strip in the same
  vertical slot
- preserve the current `+` placement at the trailing edge of the strip

This creates a clean before/after transition:

- before second tab: `agent header + entrypoint`
- after second tab: `full tab management strip`

## Motion and Polish

- The transition from single-tab header to multi-tab strip should feel like one
  surface expanding, not a region being swapped out abruptly.
- Prefer a short fade/size transition that grows the strip from the header row.
- Keep the baseline and left edge aligned so the view does not appear to jump.
- The single-tab `+` should feel tool-like and precise: low-contrast, crisp hit
  target, stronger hover/focus than resting state.

## Guardrails

- Do not move `Add tab` into global session chrome or the side panel.
- Do not show dormant tab-strip structure before a second tab exists.
- Do not overload the composer with tab-management actions.
- Do not make the single-tab affordance so subtle that only existing power users
  can find it.

## Open Questions

- Whether the single-tab header should show agent status text (`running`,
  `idle`) or only the dot.
- Whether first-run education should rely purely on the affordance or include a
  one-time tooltip after a session becomes idle.
- Whether mobile should use the same hidden-until-second-tab rule but move the
  entrypoint into a compact overflow action when horizontal space is tight.
