# Design: Frontend Architecture

> **Status:** Partially Implemented | **Last reviewed:** 2026-05-08

This document defines the frontend architecture for 143.dev.

## Design Principles

Every page answers: **"What should I do next?"** If a page is purely informational with no next action, it belongs inside a detail page or doesn't exist as a standalone page. The dashboard is the action queue. Detail pages show context to support a decision. Settings are set-and-forget.

### Cross-Cutting UX Patterns

These patterns apply across every page. They are not optional — they define the baseline interaction quality.

- **Cmd+K command palette** — Global fuzzy search across all entities (issues, runs, settings, actions). Single entry point for keyboard-driven navigation. Inspired by Linear's command palette — covers navigation, actions, and search without requiring menu interaction. Detailed design: [45-global-command-palette.md](implemented/45-global-command-palette.md).
- **Keyboard-first navigation** — J/K keys for list navigation on all table/list pages. Space bar for peek preview (opens a side panel with summary details without full navigation). Single-key shortcuts for common actions. The mouse is secondary for power users.
- **Consistent `StatusDot` component** — A single status indicator component used on every surface: sidebar badges, run list rows, Fix Queue items, browser tab favicon. Nine states mapping to the full run lifecycle (running / awaiting_input / needs_guidance / resumed_locally / completed / pr_open / in_review / merged / failed) with unambiguous colors. `awaiting_input` and `needs_guidance` use a pulsing amber dot to signal "needs you." `resumed_locally` uses a blue dot to signal "human is driving." Never invent different status representations for different pages.
- **Progressive disclosure via drawers** — Deep-dive information (traces, breadcrumbs, detailed metadata) opens in slide-out drawer panels from the right edge, preserving the parent page context. Prefer drawers over inline expand/collapse for complex content. Reserve inline expansion for simple one-level toggles.
- **Stable layouts** — Page structure (tabs, columns, sections) should be consistent regardless of data state. Unavailable sections are grayed out or show placeholder text, never hidden. Users build muscle memory from predictable layouts.

### Visual Density & Tone

- **Dense default rhythm** — Use compact primitives (`h-8` inputs/buttons by default), tighter section spacing (`space-y-6` as a page baseline), and avoid oversized hero padding on app pages.
- **Neutral-first palette** — Keep surfaces mostly neutral (`background`/`muted`/`border`) and reserve saturated color for primary actions and meaningful state.
- **Separator-led hierarchy** — Prefer subtle borders/dividers over heavy shadows for panel structure. Shadows are optional and minimal.
- **App-shell first layout** — Dashboard pages should feel like a working surface: persistent sidebar, full-width content region, and reduced `max-width` constraints unless readability requires one.
- **Operator-controlled pane widths** — Desktop shell navigation and two-pane entity list layouts should use draggable resize handles with conservative min/max bounds, default to the current product widths, and restore the user's last chosen widths from `localStorage` on return.
- **Typing stays local** — Input-heavy surfaces such as session composers, search boxes, and settings forms must keep keystroke updates scoped to the input subtree. Adjacent chrome like pickers, tables, timelines, or other data-driven controls should sit behind memoized boundaries so polling or unrelated state does not degrade typing latency over time.
- **Performance regressions get tests** — Any interactive screen with a realistic risk of render churn should carry a focused frontend performance test that types into the hot-path input and asserts unrelated expensive children do not rerender.

## Framework Decision: Next.js (React)

### Why Next.js

| Criterion | Next.js (React) | Nuxt (Vue) | SvelteKit |
|-----------|----------------|------------|-----------|
| shadcn/ui support | Native (shadcn/ui is built for React) | shadcn-vue (community port, less mature) | shadcn-svelte (community port) |
| AI ecosystem | Best — Vercel AI SDK, streaming primitives, RSC | Limited AI-specific tooling | Minimal AI tooling |
| SSR/SSG | App Router with RSC, streaming | Nitro engine, solid SSR | Good SSR |
| Community & hiring | Largest React ecosystem | Strong but smaller | Growing, smallest |
| Real-time / streaming | Built-in support via RSC + Suspense | Possible but more manual | Possible but more manual |
| TypeScript | First-class | First-class | First-class |

**Decision: Next.js with App Router.** The primary reasons:

1. **shadcn/ui is native React** — no adaptation layer, direct access to all components.
2. **AI-first ecosystem** — streaming responses, Vercel AI SDK for agent log streaming, React Server Components for data-heavy dashboards.
3. **Largest talent pool** — easier for open-source contributors.

### Framework Version

- Next.js 16+ (App Router)
- React 19+
- TypeScript (strict mode)

## Navigation & Information Architecture

The sidebar has **6 top-level items**. Each page is action-oriented — users should always know what to do next.

```
Dashboard       — "Do I need to act?"
Issues          — "What problems exist?"
Runs            — "What's the agent doing?" (full lifecycle: run → PR → merge)
Analytics       — "Where is the system failing systematically?"
Costs           — "Are we within budget and spending efficiently?"
Settings        — "How is the system configured?"
```

**Why 6, not 8:** Pull Requests were removed as a standalone page. PRs are a stage in a run's lifecycle (not a separate concept), so they appear as a tab on the run detail page.

**User menu navigation:** Team management and organization-level settings are available from the bottom-left user dropdown. Team is a separate destination from organization settings to keep membership workflows distinct from configuration workflows.

Deploy-impact experiments are shown inline on the run detail page's PR & Validation tab (where they contextually belong). Agent config experiments live under Settings > Experiments (they're a configuration concern, not a standalone workflow).

### Keyboard Navigation

All list pages support J/K keyboard navigation and Space bar peek preview. The Cmd+K command palette is available globally from every page via `command-palette.tsx` in the root layout. See [Cross-Cutting UX Patterns](#cross-cutting-ux-patterns) for details.

## Project Structure

```
frontend/
├── src/
│   ├── app/                          # Next.js App Router pages
│   │   ├── layout.tsx                # root layout (sidebar, nav)
│   │   ├── page.tsx                  # dashboard
│   │   ├── issues/
│   │   │   ├── page.tsx              # issue list
│   │   │   └── [id]/
│   │   │       └── page.tsx          # issue detail
│   │   ├── runs/
│   │   │   ├── page.tsx              # run list (full lifecycle: running → PR → merged)
│   │   │   └── [id]/
│   │   │       └── page.tsx          # run detail (stable tabs, unavailable tabs grayed out)
│   │   ├── analytics/
│   │   │   └── page.tsx              # failure analytics dashboard
│   │   ├── costs/
│   │   │   └── page.tsx              # cost summary, budget, forecast, ROI
│   │   ├── team/
│   │   │   └── page.tsx              # team members, roles, invitations
│   │   ├── settings/
│   │   │   ├── page.tsx              # organization settings + integrations
│   │   │   ├── agents/
│   │   │   │   └── page.tsx          # agent config, execution strategy
│   │   │   ├── experiments/
│   │   │   │   └── page.tsx          # agent tuning experiments (separate from agent config)
│   │   │   └── prompts/
│   │   │       └── page.tsx          # prompt lifecycle: templates, evals, rollouts
│   │   └── api/                      # API route handlers (proxy/BFF if needed)
│   ├── components/
│   │   ├── ui/                       # shadcn/ui components (generated)
│   │   ├── layout/
│   │   │   ├── sidebar.tsx
│   │   │   ├── header.tsx
│   │   │   ├── breadcrumbs.tsx
│   │   │   ├── command-palette.tsx    # Cmd+K global fuzzy search (issues, runs, settings, actions)
│   │   │   ├── status-dot.tsx         # consistent 6-state lifecycle indicator used everywhere
│   │   │   └── peek-panel.tsx         # Space bar peek preview side panel for list pages
│   │   ├── issues/
│   │   │   ├── issue-table.tsx
│   │   │   ├── issue-card.tsx
│   │   │   └── issue-filters.tsx
│   │   ├── runs/
│   │   │   ├── run-log-viewer.tsx    # real-time log streaming
│   │   │   ├── run-status-badge.tsx  # full lifecycle badge, wraps shared status-dot.tsx
│   │   │   ├── run-diff-viewer.tsx
│   │   │   ├── run-question-card.tsx # agent question display with answer input (inline in overview)
│   │   │   ├── run-guidance-panel.tsx # guidance options for paused runs (approve, guide, resume locally, dismiss)
│   │   │   ├── run-failure-card.tsx  # failure classification display (inline in overview)
│   │   │   ├── run-similarity-card.tsx # similar runs comparison block
│   │   │   ├── run-trace-drawer.tsx  # slide-out drawer for structured trace (opens from Logs tab)
│   │   │   ├── run-validation.tsx    # PR validation checks, severity-tiered (blocking/review/passing)
│   │   │   └── run-pr-section.tsx    # PR & Validation tab: review status, deploy experiment, GitHub link
│   │   ├── fix-queue/
│   │   │   ├── active-section.tsx    # runs in progress with phase-based progress indicators
│   │   │   ├── needs-you-section.tsx # items blocked on human input, composite priority sorted
│   │   │   ├── failed-section.tsx    # recent failures with inline explanations
│   │   │   ├── shipped-section.tsx   # recently deployed fixes with impact badges
│   │   │   ├── queue-item-panel.tsx  # side panel preview for queue items (click-to-preview)
│   │   │   ├── fix-rate-header.tsx   # fix success rate + sparkline in page header
│   │   │   └── onboarding-empty.tsx  # empty state: guides first integration setup
│   │   ├── experiments/
│   │   │   ├── metrics-comparison.tsx
│   │   │   ├── outcome-badge.tsx
│   │   │   ├── experiment-table.tsx       # agent tuning experiment list table
│   │   │   ├── experiment-results.tsx     # per-variant metrics comparison
│   │   │   └── create-experiment-form.tsx # create agent tuning experiment form
│   │   ├── costs/
│   │   │   ├── cost-overview-cards.tsx    # period totals, budget remaining, forecast summary
│   │   │   ├── cost-per-fix-table.tsx     # per-fix token/cost/impact table
│   │   │   ├── token-impact-chart.tsx     # tokens/cost vs impact visualization
│   │   │   ├── budget-gauge.tsx           # budget utilization and threshold marker
│   │   │   └── usage-forecast-chart.tsx   # historical usage + forecast line
│   │   ├── settings/
│   │   │   ├── prompt-editor.tsx         # prompt override editor with diff
│   │   │   ├── prompt-version-timeline.tsx # version history + rollback actions
│   │   │   ├── eval-dataset-table.tsx    # dataset list and metadata
│   │   │   ├── eval-run-table.tsx        # eval run history and status
│   │   │   └── release-gate-form.tsx     # threshold and canary stage configuration
│   │   └── analytics/
│   │       ├── failure-category-chart.tsx
│   │       ├── failure-trends-chart.tsx
│   │       └── pattern-list.tsx
│   ├── lib/
│   │   ├── api.ts                    # API client (fetch wrapper)
│   │   ├── types.ts                  # shared TypeScript types
│   │   └── utils.ts                  # shadcn/ui cn() utility
│   ├── hooks/
│   │   ├── use-issues.ts             # TanStack Query hooks
│   │   ├── use-agent-runs.ts         # full lifecycle: run status, PR status, deploy experiments
│   │   ├── use-sse.ts                # SSE hook for log streaming
│   │   ├── use-experiments.ts
│   │   ├── use-failure-analytics.ts  # TanStack Query hook for failure analytics
│   │   ├── use-prompts.ts            # prompt templates/versions hooks
│   │   ├── use-evals.ts              # eval datasets/runs hooks
│   │   ├── use-release-gates.ts      # rollout threshold hooks
│   │   ├── use-costs.ts              # cost summary/budget/forecast hooks
│   │   └── use-command-palette.ts    # Cmd+K registration, fuzzy search, action dispatch
│   └── styles/
│       └── globals.css               # Tailwind + shadcn/ui theme
├── public/
├── next.config.ts
├── tailwind.config.ts
├── tsconfig.json
├── package.json
└── components.json                   # shadcn/ui config
```

Command-style popover pickers that support search and single- or multi-select should share a common checked-row primitive in `components/ui/command.tsx` rather than reimplementing their own checkmark colors or spacing per screen. This keeps checked-state contrast consistent across people, branch, timezone, and similar pickers.

## Key Libraries

| Library | Purpose |
|---------|---------|
| `shadcn/ui` | Component library — copy-paste components built on Radix UI + Tailwind |
| `@tanstack/react-query` | Server state management — caching, refetching, optimistic updates |
| `tailwindcss` | Utility-first CSS (required by shadcn/ui) |
| `recharts` | Charts for dashboard and analytics |
| `react-diff-viewer` | Code diff display for agent runs |
| `lucide-react` | Icon library (shadcn/ui default) |
| `nuqs` | URL search params state management for filters |
| `zod` | Schema validation for forms and API responses |
| `cmdk` | Command palette (Cmd+K) — composable, accessible command menu used by Linear, Vercel, Raycast |

### Testing Libraries

| Library | Purpose |
|---------|---------|
| `vitest` | Test runner — fast, native ESM, Vite-compatible, built-in coverage via `@vitest/coverage-v8` |
| `@testing-library/react` | Component testing — test components by how users interact with them, not implementation details |
| `@testing-library/user-event` | User interaction simulation — realistic click, type, and keyboard events |
| `@testing-library/jest-dom` | DOM assertions — `toBeInTheDocument()`, `toHaveTextContent()`, etc. |
| `msw` (Mock Service Worker) | API mocking — intercept network requests at the service worker level for realistic API testing |
| `@playwright/test` | E2E testing — multi-browser end-to-end tests, reliable selectors, trace viewer for debugging |

## Pages & Features

### Fix Queue — Dashboard (`/`)

The dashboard is the **Fix Queue** — the single screen where 80% of users spend 80% of their time. It answers: **"What is the system doing, and what does it need from me?"**

This is not a reporting page. It's a real-time operations view.

#### Layout: Four Sections

The page is divided into four sections, each answering a different question. Sections collapse to a single-line summary when empty.

**1. Active — "What is the system working on right now?"**

Shows runs currently in progress, sorted by start time. Each row:
- `StatusDot` (running), issue title (linked), repo name, elapsed time, live progress indicator (phase: analyzing / coding / testing)
- Phase-based progress replaces raw log streaming for the dashboard view. Users see "Testing fix..." not scrolling terminal output.
- Clicking a row opens the full run detail page with live logs.

This section builds trust. Users see the system is doing work, not a black box.

**2. Needs You — "What's blocked on me?"**

Items requiring human input, sorted by **composite priority** (severity x wait time x item type). A security-critical PR waiting 2 hours ranks above a low-severity run waiting 2 days. Items:
- **Agent questions** — the agent asked a clarifying question during execution. Shows the question text inline with an answer input. Answering resumes the run immediately.
- **Runs needing guidance** — waiting for approval, direction, or operator intervention. Actions: "Approve", "Retry with guidance", "Resume Locally", "Dismiss".
- **PRs awaiting review** — with diff stats summary.
- **Issues manually escalated** for triage.

Each row: `StatusDot`, title (linked), priority reason label (e.g., "Agent question: Which database migration strategy?" or "Needs approval"), wait time, and a primary action button ("Answer", "Approve", "Review", "Dismiss").

**Click-to-preview side panel** — Clicking a queue item opens a side panel (`queue-item-panel.tsx`) on the right with summary context (run status, diff summary, PR status) without navigating away. The side panel provides enough context for simple decisions (approve, dismiss). For deeper review, a "View full detail" link navigates to the full run detail page.

**Bulk actions** — Checkboxes allow batch operations: bulk-approve, bulk-assign, bulk-dismiss. Users with 10+ items shouldn't process them one at a time.

**3. Failed — "What went wrong?"**

Recent failures (last 7 days), sorted by recency. Each row:
- `StatusDot` (failed), issue title, failure category badge (`context` / `complexity` / `tooling` / `validation`)
- One-sentence failure explanation inline (not hidden behind a click)
- "Retry" button if `retry_advised` is true, "View Details" otherwise

This section is critical for trust-building. See [17-failure-communication.md](implemented/17-failure-communication.md) for how failure explanations are generated.

**4. Shipped — "What impact has the system had?"**

Recently deployed fixes (last 7 days) with impact data when available. Each row:
- `StatusDot` (merged), issue title, PR link, deploy date
- Impact badge when observation window is complete: "Reduced errors by 73%" or "No measurable change" or "Measuring..." (observation in progress)

This section closes the loop. Users see that fixes they approved actually worked. It also builds the case for increasing the system's autonomy.

**Fix rate in the header** — The fix success rate (trailing 30 days, e.g., "42%") is displayed as a single line in the page header with a green/yellow/red `StatusDot` and breakdown by issue type available on hover. This sets expectations honestly — see [17-failure-communication.md](implemented/17-failure-communication.md).

#### Empty States

- **New user (no integrations)**: Guide to connect Sentry and GitHub. Show a "Connect Sentry to get started" CTA with estimated time ("takes ~2 minutes").
- **New user (integrations connected, no runs)**: Show "Scanning for fixable issues..." with the quick-win scan progress.
- **Active user (queue empty)**: "Nothing needs your attention. The system is working." with a link to the Runs page for passive monitoring.

#### What the Fix Queue intentionally omits

- **Activity feed** — The Runs page shows full history. Duplicating it here adds noise.
- **Charts and trends** — Available on the Analytics page. The Fix Queue is for action, not reflection.
- **Vanity metrics** — Total issue counts, PRs-opened-this-week. If it doesn't drive action, it doesn't belong here.

### Issues (`/issues`)

- **Data table** (shadcn/ui DataTable) with default columns: **title, severity, status, last seen** (4 columns)
  - Source (Sentry/Linear/Support) shown as a small icon badge on the title, not a separate column
  - Affected customers encoded as a count badge next to severity (e.g., "critical · 42 customers")
  - Priority score and complexity tier available on hover tooltip or in the expandable row detail — not default columns
  - Users can add/remove columns via a column picker if they want the full 8-column view
- **Filters**: source (Sentry/Linear/Support), severity, status, date range
- **Bulk actions**: triage, trigger agent run
- **Peek preview**: Space bar on a selected row opens the peek panel with issue summary (severity, source, affected customers, linked runs) without navigating to the detail page. J/K navigates between rows while the peek panel stays open.
- **Detail page**: full issue context, event history, linked agent runs/PRs
- **One primary action button** on the detail page: **"Fix This"**. Optionally, "Fix with guidance" is available as a checkbox in a modal after clicking Fix ("I want to provide guidance before the agent starts"). Keep it simple — one action, one button.

### Runs (`/runs`) — Full Lifecycle

Runs and Pull Requests are combined into a single page. A PR is a stage in a run's lifecycle, not a separate concept. The full status flow is:

```
running → completed → pr_open → in_review → merged              (happy path)
       → awaiting_input → running → ...                          (agent asked a question)
       → needs_human_guidance → running (approved/guided) → ...  (manual guidance)
                              → resumed_locally → completed → ... (user took over)
                   ↘ failed                                       (at any point)
```

#### Run list

One table with a status column covering the entire lifecycle:

| Column | Description |
|--------|-------------|
| Title | Issue title (linked), e.g., "Fix login timeout (#142)" |
| Status | Full lifecycle badge: running / awaiting_input / needs_guidance / resumed_locally / completed / pr_open / in_review / merged / failed |
| PR | PR number (linked to GitHub), blank if no PR yet |
| Age | How long since the run started |

**Filters**: status (running / awaiting_input / needs_guidance / resumed_locally / completed / pr_open / in_review / merged / failed), date range, agent type. A **"Needs Review"** quick-filter shows all runs requiring human input (awaiting_input, needs_guidance, in_review).

**Bulk actions** — Checkboxes on run rows allow batch operations: bulk-approve (for PRs in review), bulk-retry (for failed runs), bulk-assign. When 5 PRs need review, users should be able to select and batch-approve rather than clicking into each one.

**Peek preview** — Space bar on a selected row opens the `peek-panel` with run summary: status, diff stats, PR link. Navigate between rows with J/K while the peek panel stays open (Linear-style).

#### Run detail page — stable tabs

The detail page always shows **all four tabs**, regardless of run state. Tabs without content are **grayed out with a subtle hint** (e.g., "PR & Validation" shows as disabled with "(available after PR)"). This ensures a predictable layout — users returning to a run at a different lifecycle stage find the same page structure, not a rearranged one. The default (selected) tab changes based on state:

```
While running:         [Overview]  [Logs ←default]  [Diff ·disabled]  [PR & Validation ·disabled]
Awaiting input:        [Overview ←default]  [Logs]  [Diff ·disabled]  [PR & Validation ·disabled]
Needs guidance:        [Overview ←default]  [Logs]  [Diff]  [PR & Validation ·disabled]
Resumed locally:       [Overview ←default]  [Logs]  [Diff ·disabled]  [PR & Validation ·disabled]
Completed (no PR):     [Overview]  [Logs]  [Diff ←default]  [PR & Validation ·disabled]
Failed:                [Overview ←default]  [Logs]  [Diff]  [PR & Validation ·disabled]
PR exists:             [Overview]  [Logs]  [Diff ←default]  [PR & Validation]
PR with experiment:    [Overview]  [Logs]  [Diff]  [PR & Validation ←default]
```

- **Overview**: status, timestamps, result summary, and actions (cancel, retry, approve). The overview adapts to run state:
  - **For `awaiting_input` runs** — the agent's question is displayed prominently as a card with the question text, context of what the agent was doing, and an answer input (free text or multiple-choice buttons if the agent provided options). Answering resumes the run immediately.
  - **For `needs_human_guidance` runs** — a guidance panel explains why operator input is needed and offers four actions: "Approve" (proceed as-is), "Approve with note" (attach guidance for reviewers), "Retry with guidance" (re-run with guidance injected into the prompt — text input expands), and "Dismiss." A **"Resume Locally"** button provides a copyable CLI command (e.g., `143 resume abc123`) for users who want to take over in their terminal.
  - **For `resumed_locally` runs** — shows "Resumed locally by {user}" with a timestamp. The log stream is replaced by a message: "This run is being driven locally. Logs will appear when the session ends."
  - **For failed runs** — failure info is shown inline: failure category and code, LLM reasoning, actionable recommendations, similar runs comparison with side-by-side diff.
- **Logs**: streams logs via SSE, auto-scrolls, supports log level filtering. Includes a **"Show detailed trace" button** that opens a **slide-out drawer** (`run-trace-drawer.tsx`) from the right edge — structured timeline of agent decision events grouped by phase (context_gathering, analysis, implementation, testing, review), expandable details, context map, token usage breakdown per phase. The drawer preserves the log stream on the left so users can cross-reference. Default tab while running.
- **Diff**: shows the generated code changes. Default tab for completed runs (it's what people actually want to see).
- **PR & Validation**: **severity-tiered validation results** — checks are grouped by severity, not shown as a flat list:
  - **Blocking** (red) — security scan failures, test failures, CI failures. These must be resolved.
  - **Needs review** (orange) — direction/correctness/quality concerns. Human judgment required.
  - **Passing** (green) — all checks that passed.
  Users scan red items first, then orange. Green is there for completeness but doesn't demand attention. Also includes: review status, link to GitHub. If a deploy experiment exists, it's shown inline: before/after metrics comparison, outcome classification with explanation, timeline of baseline and observation windows.

### Analytics (`/analytics`)

A single page for understanding systematic failures. This is the place for charts and trends — the dashboard intentionally keeps them out. Content is prioritized into above/below fold to avoid chart overload.

**Above the fold (always visible):**
- **Failure rate by category** — pie chart (context / reasoning / tooling / validation). Answers "what kind of failures?"
- **Failure trends over time** — line chart with category breakdown. Answers "is it getting better or worse?"

**Below the fold (expandable sections):**
- **Top failure codes** — ranked table with counts and links to example runs. Actionable: clicking a failure code filters the Runs list.
- **Detected patterns** — list of patterns with acknowledge/apply/dismiss actions. This is the most actionable section — patterns surface systemic issues that can be fixed by adjusting agent config or prompts.

#### What analytics intentionally omits

- **Failure matrix** (issue type × failure category heatmap) — power-user tool that doesn't drive action for most users. Available as a CSV export for teams that want to do their own analysis.
- **Impact over time** (customer impact resolved over time) — a reporting metric, not an action driver. If a section needs a caveat about why it's there, it probably shouldn't be there. Teams that want this can build it from the API.

### Costs (`/costs`)

- **Overview cards** — total tokens, optional total dollars, budget remaining, forecast
- **Budget gauge** — utilization against threshold and throttle state
- **Per-fix table** — sortable list of fixes with tokens, optional costs, and impact outcome
- **Efficiency charts** — tokens/cost vs outcome and trend lines over time

### Settings (`/settings`)

Settings are grouped into **4 sections** to reduce nav clutter and clarify the mental model. Agent configuration and experiments are separated because they serve different workflows — configuration is "set and tune," experiments are "test and compare."

#### General & Integrations (`/settings`)

The default settings page. Two sections on one page:
- **General**: org name, product direction text
- **Integrations**: add/remove/test Sentry, Linear, GitHub, support tool connections

#### Agent Configuration (`/settings/agents`)

Everything about how the agent behaves day-to-day:
- **Agent & model selection** — choose the coding agent (Claude Code, Codex, OpenCode, custom) and the model it should use
- **Aggressiveness slider** — 4-position labeled slider (Conservative / Moderate / Aggressive / Maximum) that controls which complexity tiers the system will attempt. Each position shows a description, estimated cost impact, and expected issue coverage percentage.
- **Per-issue-type overrides** (advanced, expandable) — override max tier and auto-proceed threshold per issue type (bug_fix, performance, security, etc.)

#### Agent Experiments (`/settings/experiments`)

A/B testing of agent configurations. Separated from agent config because experiments are a distinct workflow (create → run → analyze → decide) that can overwhelm users who just want to tweak a threshold:
- **Experiment list**: name, status, variants, progress bar (runs completed / min required)
- **Experiment detail**: per-variant metrics comparison (fix_rate, validation_pass_rate, pr_approval_rate, avg_cost, avg_tokens), statistical significance indicators, traffic split visualization
- **Create experiment form**: name, description, variant definitions (name, weight, config overrides as JSON editor), metrics to track, min sample size

#### Prompt Lifecycle (`/settings/prompts`)

Prompts, evals, and rollouts are tightly coupled (you edit a prompt, eval it, then roll it out). They belong together:
- **Prompts**:
  - Per-org prompt override editor with scope controls (global/repository/issue type/phase)
  - Side-by-side diff against upstream defaults
  - Version states (draft/candidate/active/archived) and rollback action
  - Promotion blocked until eval release gates pass
- **Evals**:
  - Dataset management (golden/shadow/adversarial, private/public fixture)
  - Private example ingestion flow (metadata visible, raw payload never rendered by default)
  - Eval run history with pass@1, pass@k, and per-slice metrics
  - Failure code drilldowns and trace links
- **Rollouts**:
  - Canary stage configuration (10 -> 30 -> 100 default)
  - Release gate threshold editing (min pass rates, max policy violations, regression delta)
  - Rollback history and trigger reason display

These three sub-sections are rendered as a tabbed interface within the single `/settings/prompts` page. Each tab uses progressive disclosure: show the list first (prompts, datasets, stages), click into an item to see the editor/detail view. Don't show the diff editor, version timeline, eval datasets, and release gates all at once.

## Data Fetching

Use **TanStack Query** for all server state:

```tsx
// Example: issues list with filters
const { data, isLoading } = useQuery({
  queryKey: ['issues', filters],
  queryFn: () => api.issues.list(filters),
});
```

### Real-time Streaming

Agent run logs use **Server-Sent Events (SSE)**:

```tsx
function useAgentLogs(runId: string) {
  const [logs, setLogs] = useState<LogEntry[]>([]);

  useEffect(() => {
    const source = new EventSource(`/api/v1/agent-runs/${runId}/logs`);
    source.onmessage = (e) => {
      setLogs(prev => [...prev, JSON.parse(e.data)]);
    };
    return () => source.close();
  }, [runId]);

  return logs;
}
```

## API Client

A thin fetch wrapper that handles auth, errors, and base URL:

```tsx
const api = {
  issues: {
    list: (params) => get('/api/v1/issues', params),
    get: (id) => get(`/api/v1/issues/${id}`),
    update: (id, data) => patch(`/api/v1/issues/${id}`, data),
    runAgent: (id) => post(`/api/v1/issues/${id}/run-agent`),
  },
  // ... other resources
};
```

## Theming

shadcn/ui uses CSS variables for theming. Support light and dark mode out of the box. The theme is configured in `globals.css` and `tailwind.config.ts`.

## Frontend-Backend Communication

In development, the Next.js dev server proxies API requests to the Go backend. In production, both are served from the same container:

- Go serves the API at `/api/v1/*`
- Go serves the Next.js static export at `/*` (or Next.js runs as a separate process behind a reverse proxy)

**Recommended production approach**: Build the Next.js app as a static export (`next build && next export`) and have the Go server serve the static files. This keeps deployment simple (single binary + static assets) and aligns with the self-hosting goal.

## Testing

**Test-first development is mandatory.** Write tests before implementing any new component, hook, page, or API client function. Tests use Vitest as the test runner with React Testing Library for component testing and Playwright for E2E tests.

### Test Structure

```
frontend/
├── src/
│   ├── components/
│   │   ├── issues/
│   │   │   ├── issue-table.tsx
│   │   │   ├── issue-table.test.tsx        # component unit tests
│   │   │   ├── issue-card.tsx
│   │   │   └── issue-card.test.tsx
│   │   ├── runs/
│   │   │   ├── run-log-viewer.tsx
│   │   │   └── run-log-viewer.test.tsx
│   ├── hooks/
│   │   ├── use-issues.ts
│   │   ├── use-issues.test.ts              # hook unit tests
│   │   ├── use-sse.ts
│   │   └── use-sse.test.ts
│   ├── lib/
│   │   ├── api.ts
│   │   └── api.test.ts                     # API client tests
├── e2e/                                     # Playwright E2E tests
│   ├── issues.spec.ts
│   ├── agent-runs.spec.ts
│   ├── dashboard.spec.ts
│   └── fixtures/                            # shared E2E test data
├── test/
│   ├── setup.ts                             # Vitest global setup (testing-library, MSW)
│   ├── mocks/
│   │   ├── handlers.ts                      # MSW request handlers
│   │   └── server.ts                        # MSW server setup
│   └── test-utils.tsx                       # render wrapper with providers (QueryClient, etc.)
├── vitest.config.ts
├── playwright.config.ts
```

### Running Tests

```bash
# Run all unit/component tests
npx vitest

# Run tests in watch mode (default for dev)
npx vitest --watch

# Run tests with coverage
npx vitest --coverage

# Run a specific test file
npx vitest src/components/issues/issue-table.test.tsx

# Run E2E tests (requires running frontend + backend)
npx playwright test

# Run E2E tests with UI mode (for debugging)
npx playwright test --ui
```

### Test Patterns

#### Component Tests (React Testing Library)

Test components by simulating user interactions:

```tsx
import { render, screen } from '@/test/test-utils'
import userEvent from '@testing-library/user-event'
import { IssueTable } from './issue-table'

describe('IssueTable', () => {
  it('renders issues and supports filtering by source', async () => {
    const user = userEvent.setup()

    render(<IssueTable />)

    // MSW intercepts the API call and returns mock data
    expect(await screen.findByText('Login page broken')).toBeInTheDocument()

    // Filter by source
    await user.click(screen.getByRole('combobox', { name: /source/i }))
    await user.click(screen.getByRole('option', { name: /sentry/i }))

    expect(screen.getByText('Login page broken')).toBeInTheDocument()
    expect(screen.queryByText('Add dark mode')).not.toBeInTheDocument()
  })
})
```

#### Hook Tests

Test custom hooks in isolation:

```tsx
import { renderHook, waitFor } from '@testing-library/react'
import { QueryClientProvider, QueryClient } from '@tanstack/react-query'
import { useIssues } from './use-issues'

function wrapper({ children }: { children: React.ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

it('fetches and returns issues', async () => {
  const { result } = renderHook(() => useIssues({ status: 'open' }), { wrapper })

  await waitFor(() => expect(result.current.isSuccess).toBe(true))
  expect(result.current.data).toHaveLength(3)
})
```

#### API Mocking (MSW)

Mock API responses at the network level:

```tsx
// test/mocks/handlers.ts
import { http, HttpResponse } from 'msw'

export const handlers = [
  http.get('/api/v1/issues', ({ request }) => {
    const url = new URL(request.url)
    const status = url.searchParams.get('status')
    return HttpResponse.json({
      data: mockIssues.filter(i => !status || i.status === status),
      meta: { page: 1, per_page: 50, total: mockIssues.length },
    })
  }),

  http.post('/api/v1/issues/:id/run-agent', () => {
    return HttpResponse.json({ data: { id: 'run-1', status: 'running' } })
  }),
]
```

#### E2E Tests (Playwright)

Test full user flows:

```tsx
// e2e/issues.spec.ts
import { test, expect } from '@playwright/test'

test('user can view issue details and trigger an agent run', async ({ page }) => {
  await page.goto('/issues')

  // Click on an issue
  await page.getByRole('link', { name: /Login page broken/ }).click()
  await expect(page).toHaveURL(/\/issues\//)

  // Verify issue details are visible
  await expect(page.getByText('sentry')).toBeVisible()
  await expect(page.getByText('critical')).toBeVisible()

  // Trigger agent run
  await page.getByRole('button', { name: /fix this/i }).click()
  await expect(page.getByText(/agent run started/i)).toBeVisible()
})
```

### Test Coverage Requirements

- **Minimum 80% line coverage** for all source code
- **Components**: every component must have tests for rendering, user interactions, loading states, and error states
- **Hooks**: every custom hook must have tests for success, loading, and error scenarios
- **API client**: every endpoint function must have tests verifying request format and response parsing
- **E2E**: critical user flows (issue list → detail → trigger run → view logs) must have Playwright tests
- Coverage is tracked in CI and reported on PRs

## Accessibility

- All shadcn/ui components are built on Radix UI which provides WCAG 2.1 AA compliance
- Use semantic HTML and ARIA labels throughout
- Keyboard navigation support for all interactive elements
