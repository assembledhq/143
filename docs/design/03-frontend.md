# Design: Frontend Architecture

This document defines the frontend architecture for 143.dev.

## Design Principles

Every page answers: **"What should I do next?"** If a page is purely informational with no next action, it belongs inside a detail page or doesn't exist as a standalone page. The dashboard is the action queue. Detail pages show context to support a decision. Settings are set-and-forget.

### Cross-Cutting UX Patterns

These patterns apply across every page. They are not optional — they define the baseline interaction quality.

- **Cmd+K command palette** — Global fuzzy search across all entities (issues, runs, settings, actions). Single entry point for keyboard-driven navigation. Inspired by Linear's command palette — covers navigation, actions, and search without requiring menu interaction.
- **Keyboard-first navigation** — J/K keys for list navigation on all table/list pages. Space bar for peek preview (opens a side panel with summary details without full navigation). Single-key shortcuts for common actions. The mouse is secondary for power users.
- **Consistent `StatusDot` component** — A single status indicator component used on every surface: sidebar badges, run list rows, dashboard action queue items, browser tab favicon. Six states mapping to the run lifecycle (running / completed / pr_open / in_review / merged / failed) with unambiguous colors. Never invent different status representations for different pages.
- **Confidence as English labels** — Never show raw confidence scores (0.73) to users. Map scores to clear labels: "High confidence — will auto-proceed" / "Medium — needs your review" / "Low — blocked for approval". The score can be available in a tooltip for power users, but the label is the primary display.
- **Progressive disclosure via drawers** — Deep-dive information (traces, breadcrumbs, detailed metadata) opens in slide-out drawer panels from the right edge, preserving the parent page context. Prefer drawers over inline expand/collapse for complex content. Reserve inline expansion for simple one-level toggles.
- **Stable layouts** — Page structure (tabs, columns, sections) should be consistent regardless of data state. Unavailable sections are grayed out or show placeholder text, never hidden. Users build muscle memory from predictable layouts.

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

**Why 6, not 8:** Pull Requests and Sessions were removed as standalone pages. PRs are a stage in a run's lifecycle (not a separate concept), so they appear as a tab on the run detail page. Sessions appear in the Runs list with a "Session" badge and link to their own detail view (`/sessions/[id]`), but don't need top-level nav — they're infrequent compared to batch runs.

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
│   │   ├── sessions/
│   │   │   └── [id]/
│   │   │       └── page.tsx          # live session view (accessed from Runs list, no standalone list page)
│   │   ├── analytics/
│   │   │   └── page.tsx              # failure analytics dashboard
│   │   ├── costs/
│   │   │   └── page.tsx              # cost summary, budget, forecast, ROI
│   │   ├── settings/
│   │   │   ├── page.tsx              # general settings + integrations
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
│   │   │   ├── run-failure-card.tsx  # failure classification display (inline in overview)
│   │   │   ├── run-similarity-card.tsx # similar runs comparison block
│   │   │   ├── run-trace-drawer.tsx  # slide-out drawer for structured trace (opens from Logs tab)
│   │   │   ├── run-validation.tsx    # PR validation checks, severity-tiered (blocking/review/passing)
│   │   │   └── run-pr-section.tsx    # PR & Validation tab: review status, deploy experiment, GitHub link
│   │   ├── dashboard/
│   │   │   ├── health-indicator.tsx  # single success-rate number + sparkline
│   │   │   ├── action-queue.tsx      # items blocked on human input, composite priority sorted
│   │   │   ├── queue-item-panel.tsx  # side panel preview for action queue items (click-to-preview)
│   │   │   └── onboarding-empty.tsx  # empty state: guides first integration setup + test run
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
│   │   ├── sessions/
│   │   │   ├── session-chat.tsx          # bidirectional message feed (right panel)
│   │   │   ├── session-question.tsx      # agent question display with answer input
│   │   │   ├── session-status-bar.tsx    # mode, duration, connection status, branch info (top bar in right panel)
│   │   │   └── session-actions.tsx       # end, generate fix, sync buttons
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
│   │   ├── use-websocket.ts          # WebSocket hook for interactive sessions
│   │   ├── use-prompts.ts            # prompt templates/versions hooks
│   │   ├── use-evals.ts              # eval datasets/runs hooks
│   │   ├── use-session.ts            # TanStack Query hooks for session data
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

### Dashboard (`/`)

The dashboard answers one question: **"Do I need to do anything?"** It is an action queue, not a reporting page.

#### Layout

**Health indicator in the header** — The fix success rate (trailing 7 days, e.g., "87%") is displayed as a single line in the page header with a green/yellow/red `StatusDot`. It does not get its own section — if the number is good it's noise, and if it's bad, the action queue below already shows what to do about it.

**Action queue (the page)** — This is the entire dashboard. It fills the viewport. Shows items **blocked on human input**, sorted by **composite priority** (severity × wait time × item type), not by age alone. A security-critical PR waiting 2 hours ranks above a low-severity escalation waiting 2 days. Items:
- Runs in review (PRs awaiting review)
- Runs paused at low confidence (waiting for approval)
- Sessions with unanswered agent questions
- Issues manually escalated for triage

Each item is a single row: `StatusDot` (type + lifecycle state), title (linked), priority reason label (e.g., "Low confidence (blocked)" or "Security changes detected"), how long it's been waiting, and a primary action button ("Review", "Approve", "Answer"). The priority reason is visible on the row — users should understand *why* something needs attention, not just *that* it needs attention.

**Click-to-preview side panel** — Clicking a queue item opens a side panel (`queue-item-panel.tsx`) on the right with summary context (run status, diff summary, confidence label, PR status) without navigating away from the dashboard. This lets users process multiple queue items without back-button ping-pong. The side panel provides enough context for simple decisions (approve, answer). For deeper review, a "View full detail" link navigates to the full run/issue detail page.

**Bulk actions** — Checkboxes on queue items allow batch operations: bulk-approve, bulk-assign, bulk-dismiss. Users with 10+ items in their queue shouldn't have to process them one at a time.

**Empty state** — When the queue is empty, don't just show a checkmark. This is an **onboarding moment**:
- For new users (no integrations configured): guide them to connect Sentry/Linear/GitHub and trigger a test run
- For active users (everything clear): show a clear "nothing needs your attention" state with a link to the Runs page for passive monitoring

#### What the dashboard intentionally omits

- **Recent activity feed** — The Runs page already shows this. A collapsed "comfort section" on the dashboard adds cognitive weight just by existing. If users want to see recent completions, they navigate to Runs.
- **Impact-over-time charts** — useful for weekly reviews, not daily dashboards. Available on the Analytics page.
- **Total issue counts, PRs-opened-this-week** — vanity metrics that don't drive action.
- **Health indicator as a standalone section** — demoted to a single line in the header. One number doesn't need its own section.

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
- **Two action buttons** on the detail page: **"Fix"** (primary, batch mode) and **"Investigate"** (secondary). "Fix with guidance" is available as a checkbox in a modal after clicking Fix ("I want to provide guidance before the agent starts"). "Pair on this" lives in a `...` overflow menu — it's a fundamentally different workflow, not a variant of Fix. Don't make users parse a dropdown for the most common action.

### Runs (`/runs`) — Full Lifecycle

Runs and Pull Requests are combined into a single page. A PR is a stage in a run's lifecycle, not a separate concept. The full status flow is:

```
running → completed → pr_open → in_review → merged    (happy path)
                   ↘ failed                             (at any point)
```

#### Run list

One table with a status column covering the entire lifecycle:

| Column | Description |
|--------|-------------|
| Title | Issue title (linked), e.g., "Fix login timeout (#142)" |
| Status | Full lifecycle badge: running / completed / pr_open / in_review / merged / failed |
| PR | PR number (linked to GitHub), blank if no PR yet |
| Age | How long since the run started |

**Filters**: status (running / completed / pr_open / in_review / merged / failed), date range, agent type. A **"Needs Review"** quick-filter replaces the old standalone PR list page. Interactive sessions appear in the list with a "Session" badge and link to `/sessions/[id]` instead of the standard run detail page.

**Bulk actions** — Checkboxes on run rows allow batch operations: bulk-approve (for PRs in review), bulk-retry (for failed runs), bulk-assign. When 5 PRs need review, users should be able to select and batch-approve rather than clicking into each one.

**Peek preview** — Space bar on a selected row opens the `peek-panel` with run summary: status, confidence label, diff stats, PR link. Navigate between rows with J/K while the peek panel stays open (Linear-style).

#### Run detail page — stable tabs

The detail page always shows **all four tabs**, regardless of run state. Tabs without content are **grayed out with a subtle hint** (e.g., "PR & Validation" shows as disabled with "(available after PR)"). This ensures a predictable layout — users returning to a run at a different lifecycle stage find the same page structure, not a rearranged one. The default (selected) tab changes based on state:

```
While running:         [Overview]  [Logs ←default]  [Diff ·disabled]  [PR & Validation ·disabled]
Completed (no PR):     [Overview]  [Logs]  [Diff ←default]  [PR & Validation ·disabled]
Failed:                [Overview ←default]  [Logs]  [Diff]  [PR & Validation ·disabled]
PR exists:             [Overview]  [Logs]  [Diff ←default]  [PR & Validation]
PR with experiment:    [Overview]  [Logs]  [Diff]  [PR & Validation ←default]
```

- **Overview**: status and metadata (complexity tier, **confidence label** — "High / Medium / Low" with English description, raw score in tooltip), **risk factors** (tags/chips), actions (cancel, retry, approve). **For failed runs**, failure info is shown inline: failure category and code, LLM reasoning, actionable recommendations, similar runs comparison with side-by-side diff. The overview adapts to run state.
- **Logs**: streams logs via SSE, auto-scrolls, supports log level filtering. Includes a **"Show detailed trace" button** that opens a **slide-out drawer** (`run-trace-drawer.tsx`) from the right edge — structured timeline of agent decision events grouped by phase (context_gathering, analysis, implementation, testing, review), expandable details, context map, token usage breakdown per phase. The drawer preserves the log stream on the left so users can cross-reference. Default tab while running.
- **Diff**: shows the generated code changes. Default tab for completed runs (it's what people actually want to see).
- **PR & Validation**: **severity-tiered validation results** — checks are grouped by severity, not shown as a flat list:
  - **Blocking** (red) — security scan failures, test failures, CI failures. These must be resolved.
  - **Needs review** (orange) — direction/correctness/quality concerns. Human judgment required.
  - **Passing** (green) — all checks that passed.
  Users scan red items first, then orange. Green is there for completeness but doesn't demand attention. Also includes: review status, link to GitHub. If a deploy experiment exists, it's shown inline: before/after metrics comparison, outcome classification with explanation, timeline of baseline and observation windows.

#### Interactive sessions (`/sessions/[id]`)

Sessions appear in the Runs list with a "Session" badge but have their own detail view because the UI is fundamentally different (live chat vs. tabs). No standalone session list page — the Runs list with a "Sessions" filter serves this purpose.

**Live session view** — 2 panels (not 3 — a third panel is cramped on laptop screens):
- **Agent Activity** (left): streaming logs reusing the `run-log-viewer` component
- **Chat / Directives** (right): bidirectional message feed with question/answer display
  - Top status bar: mode, duration, connection status, branch info (pair mode: current branch, last push by agent/human)
  - Agent questions show as cards with answer options (buttons for multiple choice, text input for free text)
  - Human directives are typed into the chat input
  - Status messages show session lifecycle events
- **Session actions**: "End Session", "Generate Fix" (investigate mode), "Sync Branch" (pair mode)

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
- **Agent & model selection** — choose the coding agent (Claude Code, Codex, Gemini CLI, custom) and the model it should use
- **Aggressiveness slider** — 4-position labeled slider (Conservative / Moderate / Aggressive / Maximum) that controls which complexity tiers the system will attempt. Each position shows a description, estimated cost impact, and expected issue coverage percentage.
- **Confidence thresholds** — configurable score thresholds for auto-proceed (default 0.8) and human-review-required (default 0.5). Displayed with English labels matching the confidence display elsewhere: "High confidence — auto-proceed" / "Medium — needs review" / "Low — blocked"
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
