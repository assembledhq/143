# 38 - Autopilot Visual Simplification

> **Status:** Proposed | **Last reviewed:** 2026-03-23

## Problem

The current Autopilot surface is visually dense because too many interface elements compete at the same hierarchy level:

- status, setup, controls, and empty states all appear as peers
- the page asks users to parse configuration before they understand system state
- borders, cards, inputs, and section labels are repeated so frequently that nothing feels primary
- large empty-state boxes create visual mass without adding meaning

The result is a page that feels like a settings console instead of a calm product surface.

## Design Goal

Make Autopilot feel like a clear instrument panel:

- first understand what the system is doing
- then understand what needs attention
- only then adjust direction

The page should present one dominant idea per screenful, with secondary controls revealed progressively.

## Core Principles

### 1. State before settings

The first question on entry is not "what can I configure?" but "what is Autopilot doing right now?"

The page should lead with:

- current system state
- latest recommendation or next action
- one primary CTA

Settings should sit below the fold or behind progressive disclosure.

### 2. One hero, not many peers

There should be exactly one dominant element near the top of the page. Today the run button, status pill, empty analysis state, direction label, autonomy selector, PM settings card, documents card, and weights card all fight for attention.

Autopilot needs a single hero panel that answers:

- what Autopilot sees
- what it recommends
- what you should do next

### 3. Progressive disclosure for complexity

Not every operator needs every control every visit.

Default view:

- recommendation
- recent status
- concise direction summary

Secondary view:

- edit direction
- upload documents
- customize weights
- advanced PM settings like model and schedule

### 4. Distinguish human input from machine output

The interface should visually separate:

- machine output: recommendation, analysis, recent decisions, system status
- human steering: philosophy, direction, focus areas, autonomy

That boundary is essential for trust. Users need to instantly know what the system inferred versus what they told it.

### 5. Use silence as a design material

White space is not empty. It is how the product thinks clearly.

Reduce:

- stacked outlined boxes
- long placeholder copy inside large empty containers
- repeated micro-labels
- parallel controls exposed at once

## Proposed Information Architecture

Autopilot should be organized into four vertical zones.

### Zone 1: Control Strip

A compact strip at the top with:

- current mode: `Suggest`, `Act on low-risk`, or `Operate broadly`
- last analysis timestamp
- health summary or attention state
- one primary action: `Run analysis`

This replaces the current feeling of a disconnected status bar plus hero empty state.

### Zone 2: Recommendation Hero

This is the focal point of the page.

It should show:

- a one-sentence system read of the situation
- the single most important next action or cluster
- optionally 2-4 supporting items below it

Examples:

- `3 payment failures appear to share one auth root cause.`
- `Autopilot recommends addressing this cluster before broadening scope.`

This zone should feel editorial, not form-driven.

### Zone 3: Evidence Row

A calm horizontal band of supporting evidence:

- recent decisions
- success rate
- issues reviewed
- last run outcome

This lets the user validate trust without scanning a giant dashboard.

### Zone 4: Your Direction

All steering controls live here, explicitly labeled as human-authored direction.

The default presentation should be summary-first:

- philosophy: short sentence
- current direction: short sentence
- focus areas: tags
- avoid areas: tags

Each block should have an `Edit` affordance instead of exposing full textareas at all times.

Under that, progressively reveal:

- documents
- weights
- advanced agent settings

## Visual Hierarchy Recommendations

### Make one thing visually loud

Only the recommendation hero and its primary CTA should carry strong contrast. Everything else should be quieter.

### Flatten the chrome

The page currently uses too many bordered containers. Reduce card count and let spacing create structure.

Preferred pattern:

- one major hero surface
- one subtle divider before `Your Direction`
- lightweight rows instead of separate boxed empty states

### Replace form density with summaries

Instead of always-open controls:

- show autonomy as a segmented control with one supporting sentence
- show weights as a compact summary line like `Impact 35 · Severity 25 · Recency 20 · Revenue 20`
- open sliders only when the user selects `Customize`
- show PM schedule and model in an `Advanced` disclosure, not in the primary read path

### Reduce empty-state mass

Large empty rectangles are visually expensive and make the page feel unfinished.

Replace them with concise inline empty states:

- `No documents yet. Add roadmap or product docs.`
- `No decisions yet. They will appear after the first analysis.`

### Reserve color for meaning

Use color sparingly and semantically:

- neutral for structure
- one accent color for primary action
- amber or red only for attention states

Avoid multiple competing colored pills in the same first screenful.

## Recommended Layout

```text
Autopilot                                            [Run analysis]
Act on low-risk · Last analyzed 2h ago · Attention needed

┌─────────────────────────────────────────────────────────────────┐
│ Recommendation                                                 │
│                                                                 │
│ 3 payment issues appear linked by auth middleware failure.      │
│ Prioritize this cluster before expanding scope.                 │
│                                                                 │
│ [Review cluster]                                 84% confidence │
└─────────────────────────────────────────────────────────────────┘

Success rate 84%    14 issues reviewed    3 delegated    4 skipped

Your Direction                                            [Edit]
Ship reliability first
Payments hardening this quarter
Focus: auth, incidents, checkout
Avoid: redesigns, onboarding polish

Documents                 Weights                  Advanced
2 attached                Impact 35 / Sev 25       PM model, cadence
```

## What To Remove From The Default View

- always-open textareas for philosophy and direction
- always-visible document upload dropzone when there are zero documents
- always-visible weight sliders
- PM model and schedule controls in the primary page body
- repeated section cards for every small control group
- multiple primary-looking buttons in the same viewport

## States

### Pre-first-analysis

The hero should explain value, not show a blank dashboard.

- one calm message about what analysis will produce
- one primary CTA
- direction summary visible beneath, even if partially unset

### Healthy recurring use

The hero should show the current recommendation, not configuration.

### Needs attention

The hero should switch to a clear issue:

- missing context
- stale direction
- analysis failure
- low-confidence cluster requiring review

But the page structure should remain the same. Only the hero content changes.

## Immediate Product Recommendations

### 1. Collapse the page into summary mode by default

The top viewport should fit:

- control strip
- recommendation hero
- one row of evidence
- the first lines of `Your Direction`

If the first screen contains more than this, it is still too dense.

### 2. Move advanced PM controls behind disclosure

Model selection, schedule, and low-frequency admin controls should not compete with the main task of understanding Autopilot.

### 3. Convert form sections into readable product copy

Autopilot should read like a collaborator, not like a settings schema.

### 4. Make `Your Direction` feel intentionally authored

Treat it like a brief, not a form. Short summaries with edit affordances will feel much more premium and legible.

### 5. Use a single persistent save model

If edits remain inline, save behavior should be quiet and local. Avoid a large sticky save bar unless the user is actively editing multiple fields.

## Settings Placement

The product should use a simple rule:

- **contextual steering settings** stay on the Autopilot page
- **administrative and low-frequency settings** live in the Settings area

This keeps the main workflow coherent while preserving consistency with the existing page-based settings model.

### Keep on Autopilot

These directly affect what the PM/autopilot decides next and are part of the operator's active loop:

- philosophy
- current direction
- focus areas
- avoid areas
- reference documents
- priority weights
- autonomy level

These should be visible as summaries on the page and edited contextually.

### Edit via side sheet from Autopilot

Use a side sheet for edits that benefit from keeping the recommendation visible in the background:

- edit direction and philosophy
- manage focus and avoid areas
- adjust weights
- add or review documents

The side sheet is the right pattern here because the user is making a steering adjustment in response to what they are seeing on the page. They should not lose the surrounding context.

### Move to Settings page

These are lower-frequency admin controls and should not compete with the product surface:

- PM model
- PM schedule / cadence
- coding agent credentials and provider setup
- organization-wide defaults that are not part of the recommendation loop
- team, audit, integration administration

These belong in page-based settings because they are administrative, not interpretive.

### Consistency rule

The user should feel one coherent pattern across the app:

- **Settings pages** are for system configuration
- **Autopilot** is for operating and steering the system
- **side sheets** are for contextual edits inside an operating workflow

This avoids building a second hidden settings center inside Autopilot while still keeping the most important steering inputs close to the work.

## Wireframes

### Wireframe A: Default Autopilot view

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│ Autopilot                                               [Run analysis]      │
│ Act on low-risk · Last analyzed 2h ago · Attention needed                   │
│                                                                              │
│ ┌──────────────────────────────────────────────────────────────────────────┐ │
│ │ Recommendation                                                           │ │
│ │                                                                          │ │
│ │ 3 payment failures appear linked by one auth middleware issue.           │ │
│ │ Prioritize this cluster before expanding scope.                          │ │
│ │                                                                          │ │
│ │ [Review cluster]                                        84% confidence   │ │
│ └──────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│ Success rate 84%     14 issues reviewed     3 delegated     4 skipped       │
│                                                                              │
│ ─────────────────────────────── Your Direction ────────────────────────────  │
│                                                                              │
│ Philosophy                                               [Edit]             │
│ Ship reliability first, then broaden automation.                            │
│                                                                              │
│ Current direction                                         [Edit]            │
│ Payments hardening this quarter.                                             │
│                                                                              │
│ Focus                                                    [Edit]             │
│ [auth] [checkout] [incidents]                                              │
│                                                                              │
│ Avoid                                                    [Edit]             │
│ [redesigns] [onboarding polish]                                             │
│                                                                              │
│ Documents                 Weights                  Advanced                  │
│ 2 attached                Impact 35 · Sev 25       Model, cadence           │
│ [Manage]                  [Customize]              [Open settings]          │
└──────────────────────────────────────────────────────────────────────────────┘
```

### Wireframe B: Direction editing side sheet

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│ Autopilot                                               [Run analysis]      │
│                                                                              │
│ Recommendation hero remains visible but dimmed in the background             │
│                                                                              │
│                                              ┌────────────────────────────┐  │
│                                              │ Edit direction             │  │
│                                              │                            │  │
│                                              │ Philosophy                 │  │
│                                              │ [textarea]                 │  │
│                                              │                            │  │
│                                              │ Current direction          │  │
│                                              │ [textarea]                 │  │
│                                              │                            │  │
│                                              │ Focus areas                │  │
│                                              │ [tag editor]               │  │
│                                              │                            │  │
│                                              │ Avoid areas                │  │
│                                              │ [tag editor]               │  │
│                                              │                            │  │
│                                              │          [Cancel] [Save]   │  │
│                                              └────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────┘
```

### Wireframe C: Advanced settings stay page-based

```text
User menu
  General settings
  Integrations
  Agent
  Team
  Audit log
  Autopilot settings

┌──────────────────────────────────────────────────────────────────────────────┐
│ Autopilot settings                                                          │
│ Configure PM model, cadence, and system-wide automation defaults.           │
│                                                                              │
│ PM model                                                                    │
│ [select]                                                                    │
│                                                                              │
│ Analysis cadence                                                            │
│ [every 4 hours]                                                             │
│                                                                              │
│ Organization defaults                                                       │
│ [controls]                                                                  │
│                                                                              │
│                                                          [Save settings]    │
└──────────────────────────────────────────────────────────────────────────────┘
```

### Wireframe D: First-analysis empty state

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│ Autopilot                                          [Run first analysis]     │
│ Suggest · GitHub connected · 23 open issues detected                         │
│                                                                              │
│ ┌──────────────────────────────────────────────────────────────────────────┐ │
│ │ Recommendation                                                           │ │
│ │                                                                          │ │
│ │ Run the first analysis and Autopilot will tell you:                      │ │
│ │ which issues matter most, which issues cluster together, and             │ │
│ │ what your agents should work on first.                                   │ │
│ │                                                                          │ │
│ │ [Run first analysis]                                                     │ │
│ └──────────────────────────────────────────────────────────────────────────┘ │
│                                                                              │
│ ─────────────────────────────── Your Direction ────────────────────────────  │
│ Philosophy                                               [Edit]             │
│ Not set yet                                                               │
│                                                                              │
│ Current direction                                         [Edit]            │
│ Payments hardening this quarter.                                             │
│                                                                              │
│ Documents                 Weights                  Advanced                  │
│ 0 attached                Using defaults            [Open settings]         │
│ [Manage]                  [Customize]                                      │
└──────────────────────────────────────────────────────────────────────────────┘
```

## Implementation Guidance

Suggested sequence:

1. Rework the above-the-fold area into control strip + recommendation hero.
2. Convert open-ended settings into summary cards with edit dialogs or disclosures.
3. Hide weights and PM advanced settings behind secondary actions.
4. Replace empty-state boxes with inline rows and concise copy.
5. Tune spacing, typography, and color restraint last.

## Relationship To Existing Design Work

This proposal aligns with the split between AI output and human steering described in [35-pm-agent-top-level-review.md](./35-pm-agent-top-level-review.md), but sharpens the visual rule:

- AI output should dominate the top half of the page
- human controls should be quieter and more compressed
- complexity should open only when requested
