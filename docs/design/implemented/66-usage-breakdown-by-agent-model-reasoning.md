# Design: Usage Breakdown By Agent, Model, And Reasoning

> **Status:** Implemented | **Last reviewed:** 2026-05-08
>
> **Related docs:** [../overall.md](../overall.md), [46-billing-usage-dashboard.md](46-billing-usage-dashboard.md)

The usage page has evolved from a lightweight billing report into a compact execution analytics surface for engineering leaders. It answers questions like:

- Which coding agents are driving usage?
- Which models are creating most of the token cost?
- Are higher reasoning levels materially changing spend or runtime?

This design made two core decisions:

1. Keep `usage_hourly` for fast org-total charts and summary cards.
2. Add a second hourly execution rollup at the lowest useful non-user grain so the page can answer execution breakdown and filter questions without precomputing arbitrary materialized combinations.

The shipped result adds useful flexibility without turning `/settings/usage` into a BI tool, while supporting charts like `total tokens stacked by model over time`.

It also carries one presentation rule forward from the current usage-page cleanup: the UI should prefer explicit metric labels and short explanatory context over ambiguous `%`-only columns. If we show ratios, they should be named precisely, for example `Share of sessions` or `Share of token cost`.

One implementation correction landed after the initial rollout: execution-backed analytics now distinguish between capacity-specific rows and synthetic all-capacities rows. Capacity views still read the capacity-specific rows, but agent/model/reasoning filters, stacked charts, and filtered CSV exports read the all-capacities rows so a session that changes capacity inside an hour is not counted twice in session or concurrency metrics.

## Product framing

### Primary audience

The main audience is engineering leadership:

- VPs / Heads of Engineering who want a fast sense of usage mix and cost drivers
- Eng managers who want to compare team behavior across agents and models
- Platform owners who need to understand whether runtime knobs like reasoning level are being used intentionally

### Primary questions

The page should make these questions cheap to answer:

- `Which agent is our org actually using?`
- `Within Codex, which models are responsible for most token cost?`
- `Are xhigh / max reasoning levels rare exceptions or common defaults?`
- `Did usage spike because more sessions ran, because a heavier model was chosen, or because reasoning effort increased?`

### Non-goals

- Arbitrary pivot-table exploration
- Full warehouse-style cross-product slicing across every dimension
- Finance-grade invoicing
- Real-time per-second observability

This remains a settings-page analytics surface, not a warehouse UI.

## Implemented state

Implemented:

- `/settings/usage` supports breakdowns by `user`, `agent`, `model`, and `reasoning`
- `usage_hourly` is the main rollup store and is optimized for org totals plus a small fixed set of dimensions
- the chart supports metric switching and day drill-down
- the table supports one active dimension at a time
- capacity remains available in backend rollups and exports, but is no longer a primary breakdown surfaced on the page

Relevant session fields now used by the rollup include:

- `sessions.agent_type`
- `sessions.reasoning_effort`
- `sessions.token_usage`
- `sessions.model_override`

Model breakdown persists normalized model buckets in `usage_hourly_execution.model_used`. The rollup derives that bucket from provider-reported `sessions.token_usage.native_usage.model` when available, falling back to `sessions.model_override` and then `unknown`.

## Design principles

### 1. Keep the org-level chart fast and stable

Org-total timeseries queries should continue to come from the existing `usage_hourly` shape. The page must stay fast for 30-90 day ranges and should not depend on raw-session scans for the common path.

### 2. Support one primary comparison axis cleanly

Leaders usually want one main answer at a time: by agent, by model, by reasoning, or by capacity. The UI should optimize for that pattern rather than exposing a dense matrix.

### 3. Allow narrowing without demanding cube-style precomputation

The page should support focused follow-up questions such as:

- `Break down by model, filtered to Codex`
- `Break down by reasoning, filtered to Claude Code`

That is enough flexibility for most usage investigations without needing to materialize every dimension combination separately.

### 4. Preserve semantic accuracy

If the system cannot identify the true runtime model, it must not pretend it can. Accuracy matters more than surface completeness for cost-oriented views.

### 5. Treat user as a separate scaling concern

`Agent`, `Model`, `Reasoning`, and `Capacity` are low-cardinality dimensions and are safe to power with an hourly execution rollup. `User` is qualitatively different because cardinality grows with org size. We should not force user into the same rollup design just to keep the UI symmetrical.

## Data architecture

### Overview

Keep:

- `usage_hourly` as the authoritative org-total/hourly rollup for summary cards and the default chart

Added:

- `usage_hourly_execution` as an hourly rollup at the lowest useful non-user execution grain

This gives the page a stable foundation for:

- table breakdowns by `agent`, `model`, `reasoning`, and `capacity`
- execution filters such as `agent=codex`
- stacked token charts such as `tokens by model over time`

without building a materialized cube of every projection or filtered view.

### New table: `usage_hourly_execution`

Implemented in migration `000121_usage_hourly_execution`.

```sql
CREATE TABLE usage_hourly_execution (
    org_id UUID NOT NULL REFERENCES organizations(id),
    hour_utc TIMESTAMPTZ NOT NULL,

    agent_type TEXT NOT NULL,
    model_used TEXT NOT NULL,
    reasoning_effort TEXT NOT NULL,
    capacity_key TEXT NOT NULL,

    total_container_minutes DOUBLE PRECISION NOT NULL DEFAULT 0,
    total_sessions INT NOT NULL DEFAULT 0,
    total_container_starts INT NOT NULL DEFAULT 0,
    peak_concurrent INT NOT NULL DEFAULT 0,
    total_input_tokens BIGINT NOT NULL DEFAULT 0,
    total_output_tokens BIGINT NOT NULL DEFAULT 0,
    total_tokens BIGINT NOT NULL DEFAULT 0,
    total_llm_cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (
        org_id,
        hour_utc,
        agent_type,
        model_used,
        reasoning_effort,
        capacity_key
    )
);

CREATE INDEX idx_usage_hourly_execution_org_hour
    ON usage_hourly_execution (org_id, hour_utc DESC);

CREATE INDEX idx_usage_hourly_execution_org_agent_hour
    ON usage_hourly_execution (org_id, agent_type, hour_utc DESC);

CREATE INDEX idx_usage_hourly_execution_org_model_hour
    ON usage_hourly_execution (org_id, model_used, hour_utc DESC);

CREATE INDEX idx_usage_hourly_execution_org_reasoning_hour
    ON usage_hourly_execution (org_id, reasoning_effort, hour_utc DESC);
```

### Why this is the right fit

#### Advantages

- Row growth is driven by observed execution combinations, not by every projection we choose to materialize.
- We can answer `by model`, `by agent`, and `by reasoning` with ordinary `GROUP BY` queries.
- We can support filtered execution views like `model filtered to Codex` without storing separate “filtered rows.”
- The current org-total chart path stays untouched.

#### Tradeoff

This shifts a bit more work to read time than a pure one-dimension-only rollup, but it avoids combinatorial write amplification and remains much smaller than raw sessions.

### All-capacities execution rows

The execution rollup now stores two row shapes for each observed execution combination:

- one row per real `capacity_key`
- one synthetic row with `capacity_key = "__all__"`

The synthetic row is written from the same hourly event/token pass as the capacity rows and preserves the exact per-hour union across capacities for:

- `total_sessions`
- `total_container_starts`
- `peak_concurrent`
- token and cost totals

Read-path rule:

- `capacity` analytics query real capacity rows only
- `agent`, `model`, `reasoning`, and filtered org-total execution analytics query `__all__` rows

This keeps the common analytics path fast while avoiding session/concurrency inflation when a single execution moves across capacity tiers.

### Dimension semantics

#### `agent`

- Source: `sessions.agent_type`
- Label: product label such as `Codex`, `Claude Code`, `Gemini CLI`, `Amp`, `Pi`
- Purpose: execution-mix analysis

#### `reasoning`

- Source: `sessions.reasoning_effort`
- Purpose: runtime-intent analysis

Reasoning needs explicit bucket semantics:

- `low`, `medium`, `high`, `xhigh`, `max` for explicit overrides
- `default` for sessions where no explicit reasoning override was set
- do **not** create a separate `unsupported` bucket in usage UI; unsupported runtimes still fall into `default`, because the product question is how the session was configured, not every adapter capability nuance

#### `model`

- Source: normalized model bucket stored on `usage_hourly_execution.model_used`
- Purpose: cost-driver analysis

The rollup source is provider-reported token metadata when present, then the session's configured `model_override`, then `unknown`.

#### `capacity`

- Source: normalized runtime capacity tier
- Purpose: infrastructure footprint

### Model fidelity requirement

Model breakdown should use a persisted rollup bucket rather than deriving rows from request-time session scans.

Reasons:

- many sessions inherit model defaults rather than setting `model_override`
- auth stack ordering can affect the final provider/model selection
- Amp and Pi express model selection differently than Codex/Claude/Gemini
- finance-oriented comparisons must reflect actual runtime choice

The shipped UI reads persisted `usage_hourly_execution.model_used`. The hourly rollup may use `model_override` only as a fallback when provider token metadata does not report the runtime model.

### User drilldowns

User-level analytics stay on the original `usage_hourly` path rather than the lower-cardinality execution rollup.

Why:

- `user` is the one dimension that can grow into the thousands for large engineering organizations
- forcing user into the same hourly execution rollup would dominate row and index growth
- the current product need is straightforward per-user attribution, not cross-filtered warehouse-style user exploration

Current behavior:

- `/settings/usage` exposes a `By User` table breakout again
- user rows come from `usage_hourly` per-user rollups
- execution filters (`agent`, `model`, `reasoning`) are intentionally not supported for the user dimension

Future improvement:

- add a dedicated `usage_daily_user` or similarly coarse user rollup if leadership needs scalable filtered user drilldowns later

## UX direction

### Page structure

The page should stay compact:

- summary cards at top from `usage_hourly`
- one primary chart
- one compact controls row
- one detail table below

This is still a settings-page report, not an analytics workbench.

### Controls

Use one primary comparison selector:

- `Break down by: Capacity | Agent | Model | Reasoning`

Then add compact narrowing filters that only appear when they make the result more useful:

- `Agent`
- `Model`
- `Reasoning`

Examples:

- `Break down by Model`, filtered to `Codex`
- `Break down by Reasoning`, filtered to `Claude Code`

Do not expose a second independent `group by` control.

### Visual shape

The page should still feel like the current usage view, just with one stronger chart mode and a slightly richer control bar. The intended reading order is:

1. summary cards
2. date range + metric + `Break down by`
3. optional execution filters
4. main chart
5. breakdown table

The new chart mode should not turn the page into a dashboard grid. There should still be one dominant visualization at a time.

We are explicitly choosing to extend the **existing main chart area** on the usage page rather than:

- adding a separate `Execution breakdown` tab
- rendering a second persistent chart below the current one

The stacked bar chart is therefore another mode of the current chart surface, not a sibling surface. This keeps `/settings/usage` as one coherent page and avoids splitting the user between multiple charts that compete for attention.

### Table semantics

The table should match the selected breakdown and use direct labels:

- leading column = selected dimension label
- metrics columns = sessions, container minutes, total tokens, estimated cost
- optional ratio columns only when named explicitly, such as `Share of sessions`

Avoid generic `%` headers because they force the user to infer the denominator.

### Empty and unavailable states

- If a selected filter produces no rows, the chart/table shell remains visible with an explanatory empty state instead of clearing the whole page.

## Chart behavior

The page should support two chart modes:

- default org-total timeseries from `usage_hourly`
- stacked execution bars when the selected metric and breakdown make comparison clearer

The main motivating chart is:

- `total tokens stacked by model over time`

This should be supported directly from `usage_hourly_execution` by grouping on:

- time bucket
- selected breakdown dimension

Recommended guardrails:

- use stacked bars for low-cardinality execution dimensions only
- cap visible series to a reasonable top N and collapse the tail into `Other`
- keep `User` out of stacked chart plans unless a future dedicated user rollup exists

This lets the page answer “what made total token usage go up?” without making the visualization unreadable.

### Stacked bar UX

The stacked bar chart should be a first-class presentation mode for execution analytics, not a hidden debug toggle.

Recommended behavior:

- when the metric is `Total tokens` and `Break down by` is `Model`, default the chart to stacked bars
- when the metric is `Total tokens` and `Break down by` is `Agent` or `Reasoning`, allow stacked bars as a selectable chart mode
- when the selected breakdown would produce too many series, collapse the tail into `Other`
- keep the org-total trend legible by showing the full bar height as the total for each time bucket

This chart should answer two questions at once:

- how much total usage changed over time
- which models or runtimes were responsible for that change

### Placement decision

The chart placement decision is:

- keep one primary chart on the page
- extend that existing chart with richer controls and chart modes
- switch the chart into stacked-bar mode when the active metric and `Break down by` selection make stacking the clearest presentation

This means the page behavior should be:

- default view: the current org-total usage chart remains the first thing the user sees
- execution exploration: changing `Break down by` and filters updates that same chart region
- stacked model view: `Metric = Total tokens` plus `Break down by = Model` turns the main chart into stacked bars

We are **not** choosing:

- a second chart permanently mounted beneath the current one
- a tabbed split between `Overview` and `Execution`

Reasoning:

- one dominant chart is easier to scan
- the chart and table stay tightly coordinated
- the existing usage page IA remains intact
- the controls explain the transformation directly, instead of forcing the user to discover a separate surface

### Wireframe

```text
+----------------------------------------------------------------------------------+
| Usage                                                                   Last 30d |
| Understand container, token, and cost trends across coding runtimes.            |
+----------------------------------------------------------------------------------+
| [Metric: Total tokens v] [Break down by: Model v] [Chart: Stacked bars v]       |
| [Agent: Codex v] [Reasoning: Any v] [Clear filters]                              |
+----------------------------------------------------------------------------------+
| Main chart: Total tokens by model                                                |
|                                                                                  |
|  2.8M ┤                    ███                                                    |
|  2.1M ┤              ████ ███                                                    |
|  1.4M ┤        ████  ████ ███     ███                                            |
|  0.7M ┤  ████  ████  ████ ███  ███ ███                                           |
|    0  ┼────────────────────────────────────────────                               |
|         Apr 10 Apr 17 Apr 24 May 01 May 08                                       |
|                                                                                  |
|  Legend: [GPT-5] [GPT-5 Mini] [Claude Sonnet 4] [Other]                          |
+----------------------------------------------------------------------------------+
| Breakdown table                                                                  |
| Model             Tokens        Sessions      Est. cost      Share of tokens      |
| GPT-5             24.6M         94            $22.14         61.8%                |
| GPT-5 Mini         9.1M         61             $4.92         22.8%                |
| Claude Sonnet 4    4.0M         18             $3.77         10.0%                |
| Other              2.2M         11             $1.18          5.4%                |
+----------------------------------------------------------------------------------+
```

### Interaction notes

- Hovering a bar segment should show the time bucket, segment label, segment tokens, and full-bar total tokens.
- Clicking a legend item should temporarily isolate or mute that series without rewriting the underlying filters.
- Clicking a row in the table should still apply the semantic filter for that row, for example selecting `model=gpt-5`.
- If the user switches to a metric or breakdown that does not benefit from stacking, the same main chart should fall back to the simpler timeseries mode automatically rather than leaving a second execution chart behind.

## Rollout

1. Added `usage_hourly_execution` and backfilled `agent`, `reasoning`, `model`, and `capacity` rollups through the existing hourly reroll path.
2. Shipped `Agent` and `Reasoning` breakdowns plus execution filters.
3. Persisted normalized model buckets as `usage_hourly_execution.model_used`.
4. Enabled `Model` breakdown, `Model` filter, and stacked token-by-model charts.

Still explicitly not part of this implementation:

- any `usage_daily_user` or other scalable per-user rollup

## Rollup production

### Rollup levels

For each hour and org, the rollup writer should emit:

- one `usage_hourly` org-total row
- one `usage_hourly_execution` row per observed execution combination

Examples for a given hour:

- `agent_type='codex', model_used='gpt-5', reasoning_effort='high', capacity_key='2cpu_4096mb'`
- `agent_type='codex', model_used='gpt-5-mini', reasoning_effort='medium', capacity_key='2cpu_4096mb'`
- `agent_type='claude_code', model_used='claude-sonnet-4', reasoning_effort='default', capacity_key='4cpu_8192mb'`

No extra rows are written for:

- “by model” only
- “by agent” only
- “filtered to Codex”

Those are query-time aggregations over the same hourly execution table.

### Metric attribution

The execution table should use the same metric semantics already established by the existing usage page:

- container minutes from overlapping container runtime in that hour
- session counts based on distinct sessions
- token totals and estimated cost from persisted token usage

The grouping key comes from the owning session/runtime combination for that hour.

### Current-hour freshness

The existing periodic reroll strategy should continue:

- reroll the current hour every reaper tick
- finalize once the hour closes

The execution table should be updated in the same pass as `usage_hourly` so org totals and breakdown views stay aligned.

### Backfill

Historical backfill should populate the execution table alongside the existing rollup. This is especially important for `agent` because leaders will expect the view to be useful immediately, not only prospectively.

## API design

### Existing endpoints

Preserve:

- `GET /api/v1/usage`
- `GET /api/v1/usage/timeseries`

The current org-total chart path remains the default.

### Breakdown endpoint

Extend `GET /api/v1/usage/breakdown` to support:

```text
dimension = capacity | agent | model | reasoning
sort      = minutes_desc | sessions_desc | tokens_desc | cost_desc
```

Optional filters:

```text
agent       = <agent key>
model       = <model key>
reasoning   = <reasoning key>
```

Filter rules:

- filters narrow execution-backed dimensions
- invalid filter combinations return `400`
- filtering by `model` is unavailable until `model` support launches

The response shape can remain close to the existing one, but prefer explicit labels over a generic `percentage` field:

```json
{
  "data": [
    {
      "key": "gpt-5",
      "label": "GPT-5",
      "total_container_minutes": 512.4,
      "total_sessions": 94,
      "total_input_tokens": 1823000,
      "total_output_tokens": 641000,
      "total_tokens": 2464000,
      "total_llm_cost_usd": 22.14,
      "share_of_sessions": 61.8
    }
  ]
}
```

### Timeseries filtering

Extend `GET /api/v1/usage/timeseries` with the same execution filters:

```text
agent       = <agent key>
model       = <model key>
reasoning   = <reasoning key>
```

For stacked execution charts, the endpoint should also support:

```text
stack_by    = agent | model | reasoning | capacity
```

This allows:

- org-total timeseries by default from `usage_hourly`
- narrowed timeseries from `usage_hourly_execution`
- stacked token bars such as `stack_by=model&agent=codex`

### Export

Extend CSV export so the user can choose:

- `Org totals`
- `By Capacity`
- `By Agent`
- `By Model`
- `By Reasoning`

Exports should honor active execution filters. This is important because engineering leaders will often want to export a constrained slice like `By Model, filtered to Codex`.

## Product sequencing

### Phase 1

Implemented.

Ship:

- `usage_hourly_execution`
- `Agent` breakdown
- `Reasoning` breakdown
- execution filter bar with `Agent` and `Reasoning`

Why first:

- data already exists with acceptable fidelity
- these views answer valuable operational questions quickly
- they validate whether leaders actually use execution analytics on this page

### Phase 2

Implemented.

Ship:

- persisted `usage_hourly_execution.model_used`
- `Model` breakdown
- `Model` filter
- stacked `total tokens by model` chart
- export support for model slices

Why second:

- avoids launching an inaccurate model view
- lets the team validate table/filter interaction before adding the most important stacked execution chart

### Future

Consider separately:

- scalable user drilldowns via `usage_daily_user` or equivalent

This remains out of scope for the current implementation phases.

## Performance expectations

The target should remain:

- org-total chart: fast and predictable for 30-90 days
- breakdown queries: fast for top-N table views
- filtered chart queries: bounded and indexable via `(org_id, hour_utc)` plus the low-cardinality execution dimensions
- stacked execution charts: fast enough for day/hour buckets because they read pre-aggregated hourly rows, not raw sessions

The split between `usage_hourly` and `usage_hourly_execution` keeps these expectations realistic without moving the page onto raw-session scans for the normal path.

## Risks and mitigations

### Risk: model confusion

Users may assume the model view is authoritative before it is.

Mitigation:

- do not expose `Model` until `usage_hourly_execution.model_used` is populated by the hourly rollup

### Risk: filter complexity overwhelms casual users

Mitigation:

- keep filters compact and collapsed into a simple inline row
- preserve a clean default state with no filters set

### Risk: user analytics pressure returns later

Mitigation:

- keep user explicitly out of the hourly execution rollup
- add a separate user-specific rollup later if that use case proves important

### Risk: stacked charts become noisy

Mitigation:

- only use stacked charts for bounded-cardinality execution dimensions
- cap visible series and aggregate long tails into `Other`

## Final recommendation

Implement the usage breakdown redesign as:

- **data:** `usage_hourly` plus a new `usage_hourly_execution` hourly rollup table
- **UI:** one `Break down by` selector for `Capacity` / `Agent` / `Model` / `Reasoning`, compact execution filters, and support for stacked execution charts where they materially improve readability

This is the right balance of product clarity and scalability:

- it gives engineering leaders meaningful flexibility
- it keeps the page fast
- it supports the desired stacked token-by-model chart
- it avoids cube-style write amplification
- it keeps high-cardinality user analytics as a separate future concern instead of contaminating the main execution rollup
