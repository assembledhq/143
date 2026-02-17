# Design: Cost Intelligence Dashboard

This document describes how 143.dev tracks token usage and costs across agent runs, correlates spend with fix outcomes, forecasts budgets, and applies smart throttling when budgets are tight.

## Overview

Token budgets and per-run cost tracking already exist in the system (`agent_runs.token_usage` in [01-database-schema.md](01-database-schema.md), Datadog `agent_run.cost_usd` in [10-infrastructure.md](10-infrastructure.md)), but there is no comprehensive cost intelligence layer. This design adds:

1. **Per-fix cost rollup** — aggregate token usage from agent runs and validation into a single cost summary
2. **Cost vs. impact correlation** — join cost data with experiment outcomes to measure efficiency
3. **Spend forecasting** — project end-of-period token usage from trailing trends
4. **Smart throttling** — automatically restrict low-priority runs when approaching budget limits

**Key design decision: tokens are the universal primary metric.** Dollar costs are an optional overlay for API-key users only. This is because agents support both API-key billing (pay per token) and subscription plans (Claude Max, ChatGPT Pro, Gemini free tier) where there is no marginal per-token cost — just rate limits and a flat monthly fee.

## Billing Model Abstraction

Agents operate under two billing modes, configured per agent in org settings:

| Mode | Token tracking | Dollar cost | Budget mechanism |
|------|---------------|-------------|-----------------|
| **API-key billing** (usage-based) | Always | Computed via pricing table | Token budget + optional dollar budget |
| **Subscription billing** (flat-rate) | Always (for efficiency comparison) | Fixed monthly fee only | Token budget (for efficiency/throttling) |

Admins configure billing mode per agent in `organizations.settings`. The dashboard always shows token usage; dollar columns appear only when API-key billing is configured for at least one agent.

## Token Tracking Across Agents

All three supported agents (see [06-agent-orchestrator.md](06-agent-orchestrator.md)) provide structured JSON token output. Each agent adapter normalizes output into a unified `TokenUsage` struct:

```go
type TokenUsage struct {
    InputTokens  int64 `json:"input_tokens"`
    OutputTokens int64 `json:"output_tokens"`
    CachedTokens int64 `json:"cached_tokens"`
    TotalTokens  int64 `json:"total_tokens"`
}
```

### Claude Code

`--output-format json` returns `total_cost_usd` plus a token breakdown natively. The adapter parses the SDK result:

```go
func (a *ClaudeCodeAdapter) ParseTokenUsage(result json.RawMessage) (*TokenUsage, error) {
    // SDK result includes input_tokens, output_tokens, cache_read_input_tokens
    // total_cost_usd is available directly for API-key users
}
```

### Codex CLI

`--json` emits JSONL. The adapter filters `turn.completed` events for `{input_tokens, cached_input_tokens, output_tokens}`. Dollar cost is computed via the pricing table.

### Gemini CLI

`--output-format json` returns `stats.models[].tokens` with prompt/input/cached/output breakdown. Dollar cost is computed via the pricing table.

### Dollar Cost Computation

For API-key users, dollar cost is computed at the adapter level using the pricing table:

```go
func ComputeCostUSD(usage *TokenUsage, pricing *ModelPricing) *float64 {
    if pricing == nil {
        return nil // subscription billing — no marginal cost
    }
    cost := float64(usage.InputTokens) * pricing.InputPer1M / 1_000_000 +
            float64(usage.OutputTokens) * pricing.OutputPer1M / 1_000_000 +
            float64(usage.CachedTokens) * pricing.CachedPer1M / 1_000_000
    return &cost
}
```

## Pricing Table

Shipped with sensible defaults for current models, stored in org settings as `model_pricing` JSONB. Admins can override rates. Rates change infrequently (a few times per year).

```json
{
  "claude-opus-4": {"input_per_1m": 15.00, "output_per_1m": 75.00, "cached_per_1m": 1.50},
  "gpt-5.3-codex": {"input_per_1m": 1.25, "output_per_1m": 10.00, "cached_per_1m": 0.125},
  "gemini-2.5-pro": {"input_per_1m": 1.25, "output_per_1m": 10.00, "cached_per_1m": 0.125}
}
```

Go struct:

```go
type ModelPricing struct {
    InputPer1M  float64 `json:"input_per_1m"`
    OutputPer1M float64 `json:"output_per_1m"`
    CachedPer1M float64 `json:"cached_per_1m"`
}
```

## Cost Model

Three cost components per fix:

| Component | Source | Always available? | Notes |
|-----------|--------|-------------------|-------|
| **LLM tokens** | `agent_runs.token_usage` + validation token usage | Yes (as tokens); dollars only for API-key billing | Primary metric |
| **Compute** | Sandbox runtime (`completed_at - started_at`) × `cost_per_cpu_hour` | Optional | Only meaningful if self-hosting on paid infra |
| **Human review** | `review_outcomes.time_to_review` × `cost_per_review_hour` | Optional | From [11-review-feedback-loop.md](11-review-feedback-loop.md) |

## New Database Tables

### `cost_summaries`

Materialized per-fix rollup. Populated by a background job after each PR merge or experiment completion.

```sql
CREATE TABLE cost_summaries (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid NOT NULL REFERENCES organizations(id),
    agent_run_id       uuid NOT NULL REFERENCES agent_runs(id),
    issue_id           uuid NOT NULL REFERENCES issues(id),
    total_tokens       bigint NOT NULL,
    input_tokens       bigint NOT NULL,
    output_tokens      bigint NOT NULL,
    cached_tokens      bigint NOT NULL DEFAULT 0,
    llm_cost_usd       numeric,          -- null for subscription billing
    compute_seconds    int NOT NULL DEFAULT 0,
    compute_cost_usd   numeric,          -- null if compute tracking disabled
    review_seconds     int DEFAULT 0,
    review_cost_usd    numeric,          -- null if review tracking disabled
    total_cost_usd     numeric,          -- null if any component is subscription-based
    experiment_outcome text,              -- denormalized: success/no_change/regression/inconclusive
    impact_score       float,             -- denormalized from priority_scores
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_cost_summaries_org ON cost_summaries (org_id, created_at);
CREATE INDEX idx_cost_summaries_issue ON cost_summaries (issue_id);
CREATE INDEX idx_cost_summaries_run ON cost_summaries (agent_run_id);
```

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| agent_run_id | uuid | FK -> agent_runs |
| issue_id | uuid | FK -> issues |
| total_tokens | bigint | total tokens (input + output) across run + validation |
| input_tokens | bigint | |
| output_tokens | bigint | |
| cached_tokens | bigint | |
| llm_cost_usd | numeric | nullable — null for subscription billing |
| compute_seconds | int | sandbox wall-clock runtime |
| compute_cost_usd | numeric | nullable |
| review_seconds | int | human review time in seconds |
| review_cost_usd | numeric | nullable |
| total_cost_usd | numeric | nullable — sum of all cost components, null if any component is subscription-based |
| experiment_outcome | text | denormalized: success/no_change/regression/inconclusive |
| impact_score | float | denormalized from `priority_scores` ([05-prioritization.md](05-prioritization.md)) |
| created_at | timestamptz | |

### `budget_periods`

Monthly budget tracking in tokens (primary) with optional dollar tracking.

```sql
CREATE TABLE budget_periods (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                uuid NOT NULL REFERENCES organizations(id),
    period_start          date NOT NULL,
    period_end            date NOT NULL,
    token_budget          bigint NOT NULL,
    tokens_used           bigint NOT NULL DEFAULT 0,
    tokens_forecasted     bigint NOT NULL DEFAULT 0,
    dollar_budget_usd     numeric,         -- null for subscription billing
    dollars_spent_usd     numeric,
    dollars_forecasted_usd numeric,
    throttle_active       boolean NOT NULL DEFAULT false,
    updated_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, period_start)
);

CREATE INDEX idx_budget_periods_org ON budget_periods (org_id, period_start);
```

| Column | Type | Notes |
|--------|------|-------|
| id | uuid | PK |
| org_id | uuid | FK -> organizations |
| period_start | date | |
| period_end | date | |
| token_budget | bigint | monthly token cap (primary budget mechanism) |
| tokens_used | bigint | running total |
| tokens_forecasted | bigint | projected end-of-period usage |
| dollar_budget_usd | numeric | nullable — only for API-key billing |
| dollars_spent_usd | numeric | nullable |
| dollars_forecasted_usd | numeric | nullable |
| throttle_active | boolean | default false |
| updated_at | timestamptz | |

Updated incrementally as runs complete; forecast recalculated periodically (hourly).

## Org Settings Extensions

New fields in `organizations.settings` JSONB (see [01-database-schema.md](01-database-schema.md)):

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `billing_mode` | object | `{}` | Per-agent map: `{"claude_code": "api_key", "codex": "subscription"}` |
| `model_pricing` | object | (built-in defaults) | Per-model rate overrides (see pricing table) |
| `cost_per_cpu_hour` | numeric | 0.05 | Compute cost rate in USD |
| `cost_per_review_hour` | numeric | 75.00 | Human review cost rate in USD |
| `monthly_token_budget` | bigint | null | Evolves existing token budget concept |
| `monthly_dollar_budget` | numeric | null | Only for API-key users |
| `throttle_threshold_pct` | int | 80 | % of budget at which smart throttling activates |

## Cost vs. Impact Correlation

Join `cost_summaries` with experiment outcomes from [09-observability.md](09-observability.md). Key metrics:

- **Tokens per successful fix** vs. **tokens per failed fix** — are failed runs more expensive?
- **For API-key users**: dollars per successful fix, cost per unit of impact reduction
- **Efficiency trends over time** — are fixes getting cheaper as the review feedback loop ([11-review-feedback-loop.md](11-review-feedback-loop.md)) improves?

```go
type CostImpactMetrics struct {
    AvgTokensPerSuccess   int64   `json:"avg_tokens_per_success"`
    AvgTokensPerFailure   int64   `json:"avg_tokens_per_failure"`
    AvgCostPerSuccess     *float64 `json:"avg_cost_per_success,omitempty"`     // null for subscription
    AvgCostPerFailure     *float64 `json:"avg_cost_per_failure,omitempty"`     // null for subscription
    TokenEfficiencyTrend  float64 `json:"token_efficiency_trend"`             // % change over 30 days
    SuccessRate           float64 `json:"success_rate"`
}
```

Example query for the correlation:

```sql
SELECT
    experiment_outcome,
    count(*) AS fix_count,
    avg(total_tokens) AS avg_tokens,
    avg(llm_cost_usd) AS avg_cost_usd,
    avg(impact_score) AS avg_impact
FROM cost_summaries
WHERE org_id = $1
  AND created_at >= $2
GROUP BY experiment_outcome;
```

## Forecasting

Linear extrapolation from trailing 30-day token usage, adjusted for issue volume trends. Works universally since it's token-based. Dollar forecasting only shown for API-key billing.

```go
type ForecastResult struct {
    PeriodEnd            time.Time `json:"period_end"`
    TokensUsed           int64     `json:"tokens_used"`
    TokenBudget          int64     `json:"token_budget"`
    TokensForecastedEOP  int64     `json:"tokens_forecasted_eop"`   // end-of-period
    BudgetUtilizationPct float64   `json:"budget_utilization_pct"`
    DollarsUsed          *float64  `json:"dollars_used,omitempty"`
    DollarBudget         *float64  `json:"dollar_budget,omitempty"`
    DollarsForecastedEOP *float64  `json:"dollars_forecasted_eop,omitempty"`
    WillExceedBudget     bool      `json:"will_exceed_budget"`
    DaysUntilExhaustion  *int      `json:"days_until_exhaustion,omitempty"` // null if not projected to exceed
}
```

The forecast job runs hourly and updates `budget_periods.tokens_forecasted` (and `dollars_forecasted_usd` for API-key billing). Algorithm:

1. Compute daily average token usage over the trailing 30 days
2. Adjust for issue volume trend (if issue ingest rate is increasing, scale up proportionally)
3. Extrapolate to end of period
4. Update `budget_periods` and check throttle threshold

## Smart Throttling

When `tokens_used / token_budget >= throttle_threshold_pct / 100`, the system restricts auto-triggered runs:

1. Only auto-trigger runs for issues above a minimum priority score threshold
2. Prefer `low` token mode over `high`
3. Skip complexity tiers above "moderate" unless manually triggered (see [12-smart-routing.md](12-smart-routing.md))
4. Manual triggers always allowed (with a budget warning in the UI)

**Integration point**: the orchestrator's run lifecycle (from [06-agent-orchestrator.md](06-agent-orchestrator.md)) adds a budget gate before starting a run:

```go
func (o *Orchestrator) shouldAttemptWithBudget(ctx context.Context, issue *models.Issue, trigger string) (bool, string) {
    budget, err := o.costService.GetCurrentBudget(ctx, issue.OrgID)
    if err != nil || budget == nil {
        return true, "" // no budget configured — allow
    }

    utilizationPct := float64(budget.TokensUsed) / float64(budget.TokenBudget) * 100

    if trigger == "manual" {
        if utilizationPct >= 100 {
            return true, "warning: token budget exceeded" // manual always allowed, with warning
        }
        return true, ""
    }

    // Auto-triggered: apply throttle rules
    if utilizationPct < float64(budget.ThrottleThresholdPct) {
        return true, "" // under threshold — allow
    }

    // Throttle active — only allow high-priority, low-complexity issues
    if issue.PriorityScore < o.cfg.ThrottleMinPriority {
        return false, "throttled: priority below threshold"
    }
    if issue.ComplexityTier > models.ComplexityModerate {
        return false, "throttled: complexity above moderate"
    }

    return true, "throttle active: restricted to high-priority, low-complexity"
}
```

When throttling activates, the system also forces `low` token mode for all auto-triggered runs.

## API Endpoints

New route group under `/api/v1/costs` (see [02-api-server.md](02-api-server.md) for routing conventions):

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/costs/summary` | Token + cost breakdown over time range. Filterable by `agent_type`, `complexity_tier`, `date_from`, `date_to` |
| GET | `/api/v1/costs/per-fix` | List of fixes with token usage + optional cost + impact data. Supports pagination and sorting |
| GET | `/api/v1/costs/budget` | Current budget period status |
| PATCH | `/api/v1/costs/budget` | Update budget settings (token budget, dollar budget, throttle threshold) |
| GET | `/api/v1/costs/forecast` | Token + optional dollar forecast for current period |
| GET | `/api/v1/costs/roi` | Token efficiency and cost vs. impact correlation data |

### Example Responses

**GET `/api/v1/costs/summary`**

```json
{
  "period": {"start": "2025-06-01", "end": "2025-06-30"},
  "total_tokens": 45200000,
  "input_tokens": 32100000,
  "output_tokens": 11800000,
  "cached_tokens": 1300000,
  "total_cost_usd": 287.50,
  "fix_count": 142,
  "success_count": 98,
  "avg_tokens_per_fix": 318310,
  "by_agent_type": {
    "claude_code": {"total_tokens": 30000000, "fix_count": 95},
    "codex": {"total_tokens": 15200000, "fix_count": 47}
  }
}
```

**GET `/api/v1/costs/budget`**

```json
{
  "period_start": "2025-06-01",
  "period_end": "2025-06-30",
  "token_budget": 100000000,
  "tokens_used": 45200000,
  "tokens_forecasted": 78000000,
  "budget_utilization_pct": 45.2,
  "dollar_budget_usd": 500.00,
  "dollars_spent_usd": 287.50,
  "throttle_active": false,
  "throttle_threshold_pct": 80,
  "days_remaining": 12
}
```

## Frontend

New page `app/costs/page.tsx` (see [03-frontend.md](03-frontend.md) for page conventions).

### Components

| Component | File | Description |
|-----------|------|-------------|
| `cost-overview-cards.tsx` | `components/costs/` | Total tokens this period, budget remaining, forecast. Dollar equivalents shown if API-key billing |
| `cost-per-fix-table.tsx` | `components/costs/` | Sortable data table: token columns (input/output/cached/total), optional dollar columns, impact outcome badge |
| `token-impact-chart.tsx` | `components/costs/` | Scatter plot (Recharts): tokens spent vs. impact score, colored by outcome |
| `budget-gauge.tsx` | `components/costs/` | Visual gauge of token usage vs. budget with throttle threshold marker |
| `usage-forecast-chart.tsx` | `components/costs/` | Line chart: historical daily token usage + projected trend line to end of period |

### Settings Integration

Budget configuration added to the existing Settings > Agents page, evolving the token budget concept into a full budget section:

- Billing mode toggle per agent (API-key vs. subscription)
- Monthly token budget input
- Monthly dollar budget input (shown only for API-key billing)
- Throttle threshold percentage slider
- Model pricing overrides table
- Compute and review cost rate inputs

## Background Jobs

| Job | Schedule | Description |
|-----|----------|-------------|
| `compute_cost_summary` | After PR merge or experiment completion | Aggregates token usage from `agent_runs` and validation, computes costs, inserts into `cost_summaries` |
| `update_budget_period` | After each run completion | Increments `tokens_used` (and `dollars_spent_usd`) on current `budget_periods` row |
| `forecast_budget` | Hourly | Recalculates `tokens_forecasted` and checks throttle threshold |
| `create_budget_period` | Daily | Creates next month's `budget_periods` row if it doesn't exist |

## Datadog Metrics

New metrics emitted to Datadog (see [10-infrastructure.md](10-infrastructure.md)):

| Metric | Type | Tags | Notes |
|--------|------|------|-------|
| `cost.tokens_total` | counter | `org_id`, `agent_type`, `token_type` (input/output/cached) | Always emitted |
| `cost.usd_total` | counter | `org_id`, `agent_type`, `cost_type` (llm/compute/review) | Only for API-key billing |
| `cost.budget_utilization_pct` | gauge | `org_id` | Current period utilization |
| `cost.throttle_active` | gauge | `org_id` | 1 if throttling is active, 0 otherwise |

## Dependencies

This design depends on:

- [01-database-schema.md](01-database-schema.md) — `agent_runs.token_usage`, `organizations.settings`, `issues`, `priority_scores`
- [02-api-server.md](02-api-server.md) — route group conventions, middleware
- [03-frontend.md](03-frontend.md) — page layout, component conventions, Recharts
- [06-agent-orchestrator.md](06-agent-orchestrator.md) — `AgentResult.TokenUsage`, orchestrator run lifecycle
- [09-observability.md](09-observability.md) — experiment outcomes for cost vs. impact correlation
- [10-infrastructure.md](10-infrastructure.md) — Datadog metrics infrastructure
- [11-review-feedback-loop.md](11-review-feedback-loop.md) — `review_outcomes.time_to_review` for review cost
- [12-smart-routing.md](12-smart-routing.md) — complexity tiers for throttling rules
