# 38 - Autopilot Page Redesign

> **Status:** Proposed | **Last reviewed:** 2026-04-08
>
> **Supersedes:** Previous version of this document (4-zone architecture from 2026-03-23).
> The earlier design reduced density but still showed too many sections simultaneously
> and left empty states (zero stats, "Not set yet" rows) visible by default.
> This revision takes a more aggressive stance: the page should feel like a briefing,
> not a dashboard.

## Problem

The Autopilot page is the first thing a user sees when they open 143.
In its current form it overwhelms new users and under-serves returning ones:

1. **Empty-state overload.** Setup mode shows a control strip, hero card, evidence row
   (all zeros), setup checklist, and a direction summary (eight rows of "Not set yet").
   Six competing sections before the user has done anything.
2. **Redundant messaging.** The page header description, control strip secondary text,
   and hero card body all explain what Autopilot does.
3. **No progressive disclosure.** Every section is always visible regardless of whether
   it has meaningful content. Zeros and placeholder text look broken, not empty.
4. **Flat hierarchy.** Every section carries equal visual weight. There is no clear
   answer to "what should I look at first?"

The page should feel like an Apple or Stripe product surface: calm, confident, and
focused on exactly one thing at a time.

## Design Goal

The page should answer one question per visit:

| Visit type | Question | Answer |
|---|---|---|
| First visit | "What is this and what do I do?" | A single setup card |
| Post-setup | "What happens when I press the button?" | A brief promise + CTA |
| Returning (90% of visits) | "What should I pay attention to?" | The analysis headline |
| Error | "What went wrong?" | Clear error + retry |

Everything else is secondary. Configuration should be available but quiet.

## Core Principles

### 1. State-driven rendering

The page should only show sections relevant to the current state.
No zeros, no "Not set yet", no empty grids. If a section has nothing
meaningful to display, it does not render.

### 2. One hero per state

Each state has exactly one dominant element. In setup, it is the setup card.
In active mode, it is the analysis headline. Nothing else competes.

### 3. The reading order is the priority order

A user who glances for 2 seconds should get the headline.
A user who reads for 10 seconds should get headline + brief + metrics.
Only users who scroll past the separator reach configuration.

### 4. Silence is design

White space, hidden sections, and absent elements are intentional.
The page communicates confidence by showing less, not more.

## Information Architecture

```
┌─────────────────────────────────────────────┐
│  Page header     Title + CTA button         │  ← always visible
│  Status line     Autonomy · Freshness · Next│
├─────────────────────────────────────────────┤
│  Headline        Bold one-line "so what"    │  ← the thing you see
│  Brief           2-3 sentences of context   │
│  Evidence        3 metric cards             │  ← hidden when empty
├─────────────────────────────────────────────┤
│  Proposals       (conditional, only if > 0) │
├──────────────── separator ──────────────────┤
│  Config footer   Direction · Focus · Docs   │  ← quiet, scannable
│                  Weights & more              │
└─────────────────────────────────────────────┘
```

The reading order matches priority. The separator creates an explicit boundary
between "what Autopilot thinks" (machine output) and "how you steer it"
(human input).

## Page States

### State 1: Setup

Shown when the coding agent or GitHub is not connected.

**What renders:**
- Page title ("Autopilot") — no description subtitle
- A single centered card with:
  - Heading: "Set up Autopilot"
  - Subheading: "Connect a coding agent and your repos. Autopilot handles triage from there."
  - Step 1: Coding agent selector + connect/configure button
  - Step 2: GitHub connect button
  - Optional integrations line: Sentry · Linear · Slack
- Nothing else. No evidence row, no direction summary, no control strip.

**What is hidden:**
- Evidence row (would show all zeros)
- Direction summary (would show all "Not set yet")
- Control strip (no analysis to control yet)
- Proposals card (no analysis has run)

```
┌──────────────────────────────────────────────────┐
│                                                  │
│  Autopilot                                       │
│                                                  │
│  ┌────────────────────────────────────────────┐  │
│  │                                            │  │
│  │  Set up Autopilot                          │  │
│  │  Connect a coding agent and your repos.    │  │
│  │  Autopilot handles triage from there.      │  │
│  │                                            │  │
│  │  ① Coding agent                 [Set up]   │  │
│  │  ② GitHub repos                [Connect]   │  │
│  │                                            │  │
│  │  Optional: Sentry · Linear · Slack         │  │
│  │                                            │  │
│  └────────────────────────────────────────────┘  │
│                                                  │
│            (nothing else on the page)            │
│                                                  │
└──────────────────────────────────────────────────┘
```

### State 2: First analysis

Shown when setup is complete but no analysis has been run yet.

**What renders:**
- Page header with CTA: "Autopilot" + [Run first analysis] button
- Status line: `Suggest · No analysis yet`
- A single card explaining what the analysis will do
- One optional nudge to set product direction (single text line, not a full section)

**What is hidden:**
- Evidence row (no data yet)
- Full direction summary (premature — user hasn't seen value yet)

```
┌──────────────────────────────────────────────────┐
│                                                  │
│  Autopilot                  [Run first analysis] │
│  Suggest · No analysis yet                       │
│                                                  │
│  ┌────────────────────────────────────────────┐  │
│  │                                            │  │
│  │  Ready for your first analysis             │  │
│  │                                            │  │
│  │  Autopilot will review your open issues,   │  │
│  │  group related ones together, and tell     │  │
│  │  you what's highest leverage to work on.   │  │
│  │                                            │  │
│  └────────────────────────────────────────────┘  │
│                                                  │
│  Set your product direction for better      [→]  │
│  results.                                        │
│                                                  │
└──────────────────────────────────────────────────┘
```

### State 3: Active (post-analysis)

This is the primary state. 90% of page visits land here.

**What renders:**
- Page header: "Autopilot" + [Run analysis] button
- Status line: `Act on low-risk · Analyzed 2h ago · Next in 2h`
- **Headline:** Bold, one-line summary — the "so what" (e.g. "Auth token rotation is highest leverage")
- **Brief:** 2-3 sentence analysis body explaining the why and what-to-do
- **Evidence:** Three metric cards in a row — Success rate, Issues reviewed, Tasks delegated
- **Proposals:** Conditional — only if proposed project count > 0
- Separator
- **Config footer:** Key-value rows for Direction, Focus, Documents, Weights & more

```
┌──────────────────────────────────────────────────┐
│                                                  │
│  Autopilot                       [Run analysis]  │
│  Act on low-risk · Analyzed 2h ago · Next in 2h  │
│                                                  │
│  Auth token rotation is highest leverage         │
│                                                  │
│  Three related issues affecting 12 customers     │
│  share a root cause in the session middleware.   │
│  Agents should prioritize this cluster first.    │
│                                                  │
│  ┌──────────────┐ ┌──────────────┐ ┌──────────┐ │
│  │     94%      │ │     47       │ │    12    │ │
│  │   success    │ │   reviewed   │ │ delegated│ │
│  └──────────────┘ └──────────────┘ └──────────┘ │
│                                                  │
│  3 proposed projects               [Review →]    │
│                                                  │
│  ──────────────────────────────────────────────  │
│                                                  │
│  Direction        Ship fast, fix fast    [Edit]  │
│  Focus            Auth · API · Billing           │
│  Documents        3 attached           [Manage]  │
│  Weights & more                     [Settings]   │
│                                                  │
└──────────────────────────────────────────────────┘
```

### State 4: Attention needed (error)

Shown when the last analysis failed or there is a blocking issue.

**What renders:**
- Same header and status line as active state
- Warning headline: "Attention needed"
- Error message body with suggested action
- Evidence row with last-known stats (if any exist)
- Config footer (unchanged)

```
┌──────────────────────────────────────────────────┐
│                                                  │
│  Autopilot                       [Run analysis]  │
│  Act on low-risk · Last analyzed Apr 7           │
│                                                  │
│  ⚠ Attention needed                              │
│                                                  │
│  The last analysis failed: rate limit exceeded   │
│  on GitHub API. This usually resolves itself —   │
│  try running again.                              │
│                                                  │
│  ──────────────────────────────────────────────  │
│                                                  │
│  Direction        Ship fast, fix fast    [Edit]  │
│  Focus            Auth · API · Billing           │
│  Documents        3 attached           [Manage]  │
│  Weights & more                     [Settings]   │
│                                                  │
└──────────────────────────────────────────────────┘
```

## Component Changes

### What gets removed

| Current component | Action | Reason |
|---|---|---|
| `PageHeader` description prop | Remove description text | Title is sufficient; analysis demonstrates value |
| `AutopilotControlStrip` (as a separate card) | Remove | CTA moves to page header; autonomy + timestamp become a status subtitle |
| `AutopilotHero` (generic card) | Replace | Becomes a styled headline + body, not a boxed card |
| `AutopilotEvidenceRow` (4-column grid) | Replace with 3-column | Drop "Next run" (moves to status line); hide when all values are zero |
| Direction summary (8 rows with separators) | Replace with compact footer | 4 key-value rows replace 8 separated rows |

### What gets added

| New element | Purpose |
|---|---|
| Status subtitle | Single line: `{autonomy} · {freshness} · {next run}` under the page title |
| Analysis headline | Bold `text-lg font-semibold` line — extracted from or summarizing the analysis |
| Setup card | Self-contained card for setup state — replaces hero + checklist + evidence combo |
| Direction nudge | Single-line prompt in first-analysis state: "Set your direction for better results →" |

### What stays (but moves)

| Element | Change |
|---|---|
| CTA button ("Run analysis") | Moves into `PageHeader` action slot |
| Proposals card | Stays conditional, moves below evidence row |
| Side sheets (steering, weights, documents) | No change — triggered from config footer |

## Evidence: Three Metrics, Not Four

The current evidence row shows four stats: Success rate, Issues reviewed,
Delegated, and Next run. This revision reduces to three:

| Metric | Keep? | Reason |
|---|---|---|
| **Success rate** | Yes | Trust signal — "Is Autopilot working?" |
| **Issues reviewed** | Yes | Scope signal — "How much did it cover?" |
| **Tasks delegated** | Yes | Output signal — "What did it actually do?" |
| **Next run** | Move to status line | This is scheduling metadata, not a performance metric |

Three cards are also visually cleaner — they divide evenly across common
viewport widths.

### Visibility rule

The evidence row is only rendered when at least one metric has a non-zero
value. In setup and first-analysis states, it is hidden entirely.

## Config Footer

The config footer replaces the current `AutopilotDirectionSummary` component.
It is a flat list of key-value rows below a separator, each with an optional
action button.

| Row | Value | Action |
|---|---|---|
| Direction | Philosophy or direction text (whichever is set) | [Edit] → opens steering sheet |
| Focus | Focus area tags, or "None set" | (included in Edit sheet) |
| Documents | "{n} attached" | [Manage] → opens documents sheet |
| Weights & more | Weight summary or "Using defaults" | [Settings] → opens weights sheet or navigates to settings page |

### What the footer does NOT show

- Autonomy level (already in the status line)
- Avoid areas as a separate row (included in the Edit sheet)
- Philosophy and Direction as separate rows (combine into one "Direction" row
  showing whichever has content, with full detail in the sheet)
- Advanced/model/cadence (lives in Settings page, linked from "Weights & more")

This reduces the footer from 8 rows to 4.

## Status Line

The status line replaces the `AutopilotControlStrip` card. It renders as a
single muted text line directly below the page title:

```
{autonomy_label} · {freshness} · {next_run}
```

Examples:
- `Suggest · No analysis yet`
- `Act on low-risk · Analyzed 2h ago · Next in 2h`
- `Operate broadly · Analyzed Apr 8, 2:34 PM · Next in 30m`
- `Act on low-risk · Last analyzed Apr 7` (no next run scheduled)

The status line is always visible in states 2–4. In setup state, it is hidden
(replaced by the setup card's own messaging).

## Analysis Headline

The headline is the single most important element on the active page.
It should be a bold, one-line summary answering "what should I pay attention to?"

**Source:** The headline is derived from the latest PM plan analysis. Options:

1. **First sentence of the analysis** — simplest, often works well
2. **Dedicated field** — the PM agent produces a `title` or `headline` field
   in its output (requires backend change)
3. **Truncated analysis** — first ~80 characters of the analysis, ellipsized

Option 1 is recommended for the initial implementation. If the PM analysis
format evolves to include structured output, option 2 is better long-term.

**Styling:** `text-lg font-semibold text-foreground` — noticeably bolder than
the brief body text below it.

## Setup Card Design

The setup card consolidates the current `AutopilotHero` + `AutopilotSetupChecklist`
into a single self-contained component. It should feel like an onboarding moment,
not a broken dashboard.

### Content

- **Heading:** "Set up Autopilot"
- **Subheading:** "Connect a coding agent and your repos. Autopilot handles triage from there."
- **Step 1:** Coding agent — agent type selector + connect/configure action
- **Step 2:** GitHub repos — connect button
- **Optional line:** Links to connect Sentry, Linear, Slack (de-emphasized)

### Behavior

- Steps show a checkmark when completed
- When both required steps are done, the card transitions to the first-analysis
  state on the next render (no manual "continue" button needed)
- The card does not show evidence, direction, or any other page section

## Relationship to Existing Documents

- Aligns with the operator workspace model in
  [37-pm-agent-top-level-review.md](./37-pm-agent-top-level-review.md)
- The human/machine boundary (config footer vs. analysis) follows the
  steering vs. output distinction from doc 37
- Settings placement (contextual steering on-page, admin settings in Settings)
  carries forward unchanged from the prior version of this document

## Implementation Sequence

1. **Restructure the page shell.** Remove `PageHeader` description, remove
   `AutopilotControlStrip` as a card, add status subtitle and CTA to header.
2. **Build the setup card.** Consolidate hero + checklist into one component.
   Hide evidence row and direction summary in setup state.
3. **Build the analysis headline + brief.** Replace `AutopilotHero` with
   headline (bold) + body (normal) rendering. No card wrapper needed.
4. **Reduce evidence to 3 metrics.** Remove "Next run" from evidence, add it
   to status line. Add visibility rule: hide when all zeros.
5. **Build the config footer.** Replace `AutopilotDirectionSummary` (8 rows)
   with 4 compact key-value rows below a separator.
6. **Wire up first-analysis state.** Show the direction nudge line instead of
   the full direction summary.
7. **Polish.** Spacing, typography, empty-state transitions.

## What To Remove From The Default View

- Page header description text
- Control strip as a separate bordered card
- Evidence row when all values are zero
- "Next run" as a stat card (moves to status line)
- Direction summary during setup state
- Separate rows for Philosophy, Direction, Focus, Avoid, Autonomy, Documents,
  Weights, and Advanced (8 rows → 4 rows)
- All instances of "Not set yet" / "None set" visible by default — if nothing
  is set, the row either shows a brief prompt or is hidden
