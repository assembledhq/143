# Design: Billing & Usage Dashboard

> **Status:** Not Started | **Last reviewed:** 2026-04-09

A new settings page (`/settings/usage`) that displays container usage and LLM token consumption, allows day-by-day exploration, per-user breakdowns, filtering, and CSV export. Backed by new API endpoints optimized for time-series and dimensional queries over existing tables plus a new hourly rollup table.

## Motivation

PR #244 added `container_usage_events` persistence and a summary endpoint (`GET /api/v1/usage`), but the current API only returns a single aggregate blob for a time range. To power an interactive dashboard we need:

- **Time-series data** — hourly buckets that the frontend aggregates into days using the viewer's timezone
- **Dimensional breakdowns** — by user, capacity tier, exit reason
- **LLM token tracking** — already captured per-session and per-message, but not aggregated for dashboard display
- **Fast queries** — the raw events table with self-joins won't scale past ~50K events; a materialized rollup avoids O(n²) peak-concurrent scans on every page load
- **Export** — CSV download for external billing systems and finance workflows

## Data Layer

### New table: `usage_hourly`

A pre-aggregated rollup table at **hourly** granularity (UTC hours), populated by a periodic background job. Raw events remain the source of truth; the rollup is a read-optimized cache.

**Why hourly instead of daily:** A daily rollup forces a timezone choice at write time. If we roll up by UTC day, a user in US Pacific sees their "day" split across two rollup rows. By storing hourly buckets, the frontend can group hours into local-timezone days at render time — `hour_utc` 2026-04-01T07:00Z through 2026-04-02T06:59Z becomes "Apr 1" for a UTC-7 user. This costs ~24× more rows than daily but the row size is small and 90 days × 24 hours = 2,160 rows per org/dimension combo, well within fast-scan range.

```sql
CREATE TABLE usage_hourly (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID NOT NULL REFERENCES organizations(id),
    hour_utc            TIMESTAMPTZ NOT NULL,  -- truncated to hour, always UTC

    -- Dimensional keys (nullable = "all" for that dimension)
    user_id             UUID REFERENCES users(id),
    capacity_tier       TEXT,           -- e.g. "2cpu_4096mb", NULL = all tiers

    -- Container aggregates
    total_container_minutes   DOUBLE PRECISION NOT NULL DEFAULT 0,
    total_sessions            INT              NOT NULL DEFAULT 0,
    total_container_starts    INT              NOT NULL DEFAULT 0,
    peak_concurrent           INT              NOT NULL DEFAULT 0,
    avg_duration_sec          DOUBLE PRECISION NOT NULL DEFAULT 0,
    p95_duration_sec          DOUBLE PRECISION NOT NULL DEFAULT 0,

    -- LLM token aggregates
    total_input_tokens        BIGINT NOT NULL DEFAULT 0,
    total_output_tokens       BIGINT NOT NULL DEFAULT 0,
    total_llm_cost_usd        DOUBLE PRECISION NOT NULL DEFAULT 0,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (org_id, hour_utc, user_id, capacity_tier)
);

CREATE INDEX idx_usage_hourly_org_hour
    ON usage_hourly (org_id, hour_utc DESC);

CREATE INDEX idx_usage_hourly_org_user_hour
    ON usage_hourly (org_id, user_id, hour_utc DESC);
```

**Why a rollup table instead of live aggregation:**
- The existing `GetUsageSummary` peak-concurrent query is O(n²) via self-join — unusable for dashboards that need per-day data for 30-90 days
- A rollup makes all dashboard queries O(hours) regardless of event count
- The rollup job is idempotent: `INSERT ... ON CONFLICT DO UPDATE`, safe to re-run

**Capacity tier derivation:** Concatenate `cpu_limit` and `memory_limit_mb` into a human-readable string (`"2cpu_4096mb"`). This avoids storing two float columns in the rollup and keeps cardinality bounded (we have 3-5 tiers).

### Dimensional rollup levels and scaling

The nullable `user_id` and `capacity_tier` columns create a dimensional matrix. A naive approach that writes one row per `(org, hour, user, tier)` combination creates a cartesian product that blows up with many users:

| Users | Tiers | Hours (90d) | Rows | Timeseries scan (30d) |
|-------|-------|-------------|------|-----------------------|
| 10    | 3     | 2,160       | 65K  | 22K — fast |
| 100   | 3     | 2,160       | 648K | 216K — marginal |
| 500   | 3     | 2,160       | 3.2M | 1.08M — too slow |

**Solution: write pre-aggregated rows at multiple rollup levels.** The rollup job writes separate rows for each combination of dimensions:

| Level | `user_id` | `capacity_tier` | Purpose | Rows (500 users, 90d) |
|-------|-----------|------------------|---------|-----------------------|
| Org total | `NULL` | `NULL` | Main chart, summary cards | 2,160 |
| Per-user | set | `NULL` | User breakdown table | 1.08M |
| Per-tier | `NULL` | set | Capacity breakdown | 6,480 |
| Per-user-tier | set | set | Drill-down (user + tier) | 3.2M |

Each API query filters to the appropriate level:

```sql
-- Timeseries chart (org-level): always fast, scans ~720 rows for 30d
SELECT * FROM usage_hourly
WHERE org_id = @org_id AND hour_utc >= @start AND hour_utc < @end
  AND user_id IS NULL AND capacity_tier IS NULL;

-- User breakdown: scans ~500 rows per hour × narrower range
SELECT * FROM usage_hourly
WHERE org_id = @org_id AND hour_utc >= @start AND hour_utc < @end
  AND user_id IS NOT NULL AND capacity_tier IS NULL;
```

The per-user-tier level (3.2M rows) is **only queried when a user drills into a specific user + capacity filter**, which covers a narrow date range. This level is optional at launch — we can skip writing it initially and add it later if the drill-down use case materializes.

**Rollup job efficiency with many users:** The rollup query groups by `(user_id, capacity_tier)` in a single pass over `container_usage_events` for each hour, then the Go code fans out the org/user/tier level rows. Re-rolling one hour for 500 users × 3 tiers = ~1,500 upserts, which takes <1s in a single transaction.

**Index coverage:** The existing `(org_id, hour_utc DESC)` index covers org-level and tier-level queries. Add a partial index for per-user queries:

```sql
CREATE INDEX idx_usage_hourly_org_user_hour_nonnull
    ON usage_hourly (org_id, user_id, hour_utc DESC)
    WHERE user_id IS NOT NULL;
```

### Linking events to users

The current `container_usage_events` table has no `user_id` column. We need to join through sessions:

```sql
-- Add to rollup query (sessions already have user_id via created_by)
SELECT s.created_by AS user_id, ...
FROM container_usage_events e
JOIN sessions s ON s.id = e.session_id
```

No schema change to `container_usage_events` needed — the session → user link already exists.

### LLM cost display: estimated vs. billed

The `total_cost_usd` from agent output reflects the **raw API provider cost** (what Anthropic/OpenAI charges per token). This is useful as a relative measure ("Alice uses 3x more than Bob") but doesn't represent what a subscription-plan user is actually billed.

**Approach:** Display cost as "Estimated API cost" with a tooltip: *"Estimated cost at standard provider rates. Your actual billing may differ based on your plan."* This is accurate for pay-as-you-go users and still useful for subscription users as a consumption signal. The column header in the breakdown table reads "Est. cost" to keep it compact.

When actual billing integration exists (Stripe, etc.), we can add a "Billed cost" column that reflects the real charge. Until then, estimated API cost is the best signal we have and is strictly more useful than showing nothing.

### LLM token data source

LLM token usage is **already tracked** in the existing schema — no new instrumentation needed:

- **`sessions.token_usage`** (JSONB): `{input_tokens, output_tokens, total_cost_usd}` — set by the orchestrator after each agent run. Every coding agent (Codex, Claude Code, Gemini) emits token counts in its streaming output; the adapter parses them and the orchestrator persists them.
- **`session_messages.token_usage`** (JSONB): Same structure, per-message granularity for multi-turn sessions.
- **`pm_plans.token_usage`** (JSONB): Token usage from PM agent runs.

The rollup job joins `container_usage_events` with `sessions` to pull `token_usage` alongside container metrics. For the hourly bucket, we attribute tokens to the hour of `session.created_at` (or `session_messages.created_at` for per-message granularity).

```sql
-- Token aggregation in rollup query
SELECT
    date_trunc('hour', s.created_at) AS hour_utc,
    s.created_by AS user_id,
    SUM((s.token_usage->>'input_tokens')::bigint) AS total_input_tokens,
    SUM((s.token_usage->>'output_tokens')::bigint) AS total_output_tokens,
    SUM((s.token_usage->>'total_cost_usd')::double precision) AS total_llm_cost_usd
FROM sessions s
WHERE s.org_id = @org_id
  AND s.created_at >= @start AND s.created_at < @end
  AND s.token_usage IS NOT NULL
GROUP BY 1, 2
```

### Rollup job

Add a `RollupHourlyUsage` method to `ContainerUsageStore` that:

1. Queries `container_usage_events` (joined with `sessions`) for all completed hours not yet rolled up (or re-rolls the current hour)
2. Computes container aggregates grouped by `(org_id, hour_utc, user_id, capacity_tier)`
3. Computes token aggregates grouped by `(org_id, hour_utc, user_id)` from `sessions.token_usage`
4. Upserts both into `usage_hourly`
5. For peak concurrent: uses a simpler per-hour approach since the time window is bounded to 60 min

Run from the existing `SessionReaper` periodic loop (already runs every 5 minutes, phase 4 addition) or a dedicated cron. Re-rolling the current hour on each run keeps the dashboard near-real-time.

## API Endpoints

### `GET /api/v1/usage/timeseries`

Returns hourly-bucketed usage data. The frontend groups these into local-timezone days for chart display.

```
Query params:
  start     RFC3339 (default: 30 days ago)
  end       RFC3339 (default: now)
  group_by  "hour" (default) | "user" | "capacity"
  user_id   UUID (optional filter)
  capacity  string (optional filter, e.g. "2cpu_4096mb")
```

Response:
```json
{
  "data": {
    "buckets": [
      {
        "hour_utc": "2026-04-01T14:00:00Z",
        "total_container_minutes": 6.2,
        "total_sessions": 3,
        "total_container_starts": 4,
        "peak_concurrent": 2,
        "avg_duration_sec": 124.0,
        "p95_duration_sec": 310.0,
        "total_input_tokens": 45200,
        "total_output_tokens": 12800,
        "total_llm_cost_usd": 0.34
      }
    ],
    "period_start": "2026-03-10T00:00:00Z",
    "period_end": "2026-04-09T00:00:00Z"
  }
}
```

When `group_by=user`, each bucket includes `user_id` and `user_name`. When `group_by=capacity`, each bucket includes `capacity_tier`.

**Frontend daily aggregation:** The frontend receives UTC hourly buckets and groups them by the viewer's local timezone day using `Intl.DateTimeFormat` or a small helper:

```typescript
function groupByLocalDay(buckets: HourlyBucket[]): DailyBucket[] {
  const byDay = new Map<string, HourlyBucket[]>();
  for (const b of buckets) {
    // toLocaleDateString groups by the viewer's timezone automatically
    const dayKey = new Date(b.hour_utc).toLocaleDateString("en-CA"); // YYYY-MM-DD
    const group = byDay.get(dayKey) ?? [];
    group.push(b);
    byDay.set(dayKey, group);
  }
  return [...byDay.entries()].map(([day, hours]) => ({
    day,
    total_container_minutes: sum(hours, "total_container_minutes"),
    total_sessions: max(hours, "total_sessions"), // unique sessions need dedup — see note
    total_container_starts: sum(hours, "total_container_starts"),
    peak_concurrent: max(hours, "peak_concurrent"),
    total_input_tokens: sum(hours, "total_input_tokens"),
    total_output_tokens: sum(hours, "total_output_tokens"),
    total_llm_cost_usd: sum(hours, "total_llm_cost_usd"),
  }));
}
```

**Note on session dedup:** `total_sessions` at the hourly level counts distinct sessions that had activity in that hour. Summing across hours would double-count sessions spanning multiple hours. Two options: (a) use `total_container_starts` as the daily session proxy (good enough for most dashboards), or (b) add a `/usage/daily-summary` endpoint that queries the raw table with `COUNT(DISTINCT session_id)` grouped by `date_trunc('day', started_at AT TIME ZONE $tz)` for exact counts when the user clicks a specific day.

### `GET /api/v1/usage/breakdown`

Returns a dimensional breakdown for a specific day or range, for the detail table view.

```
Query params:
  start       RFC3339
  end         RFC3339
  dimension   "user" | "capacity" | "exit_reason"
  sort        "minutes_desc" (default) | "sessions_desc" | "tokens_desc"
  limit       int (default 50)
```

Response:
```json
{
  "data": [
    {
      "key": "user_id_or_tier_name",
      "label": "John Wang",
      "total_container_minutes": 87.3,
      "total_sessions": 14,
      "total_container_starts": 18,
      "peak_concurrent": 3,
      "total_input_tokens": 284000,
      "total_output_tokens": 91000,
      "total_llm_cost_usd": 4.12,
      "percentage": 61.2
    }
  ]
}
```

### `GET /api/v1/usage/export`

Returns a CSV download of usage data for the selected time range and filters.

```
Query params:
  start       RFC3339
  end         RFC3339
  granularity "hourly" | "daily" (default "daily")
  dimension   "user" | "capacity" | "none" (default "none")
  tz          IANA timezone (default "UTC", used for daily grouping)
```

Response: `Content-Type: text/csv` with `Content-Disposition: attachment; filename="usage-2026-04-01-to-2026-04-09.csv"`

```csv
date,hour_utc,user_email,capacity_tier,container_minutes,sessions,container_starts,peak_concurrent,input_tokens,output_tokens,llm_cost_usd
2026-04-01,2026-04-01T00:00:00Z,john@acme.com,2cpu_4096mb,6.2,3,4,2,45200,12800,0.34
2026-04-01,2026-04-01T01:00:00Z,john@acme.com,2cpu_4096mb,3.1,1,1,1,22100,8400,0.18
```

When `granularity=daily`, the `hour_utc` column is omitted and rows are grouped by the `tz`-adjusted day. When `dimension=none`, user and capacity columns are omitted (org-level totals only).

**Implementation:** The handler streams rows directly from the DB query using `csv.NewWriter(w)` — no buffering the full result set in memory. Sets `Content-Type` and `Content-Disposition` headers before writing.

### Existing endpoint enhancement

Keep `GET /api/v1/usage` as-is for the summary card at the top. It already returns `total_container_minutes`, `total_sessions`, `peak_concurrent`, and `by_capacity`.

## Frontend

### Navigation

Add a "Usage" entry to the sidebar settings section under the **ORGANIZATION** group:

```typescript
// sidebar-settings-section.tsx — ORGANIZATION group
{ label: "Usage", icon: BarChart3, href: "/settings/usage", adminOnly: true },
```

### Charting library

**Recommendation: Recharts.** Rationale:
- React-native (composable JSX components, no imperative API)
- Lightweight (~45KB gzipped), no D3 dependency bloat
- Built-in responsive container, tooltips, legends
- Used widely with shadcn/ui and Tailwind projects
- Sufficient for line/bar/area charts we need

```bash
cd frontend && npm install recharts
```

### Page layout

Route: `/settings/usage` → `frontend/src/app/(dashboard)/settings/usage/page.tsx`

```
┌─────────────────────────────────────────────────────────┐
│  Usage & Billing                                        │
│  Monitor container usage across your organization.      │
│                                                         │
│  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ │
│  │ Container   │ │ Total       │ │ Peak        │ │ LLM Tokens  │ │
│  │ Minutes     │ │ Sessions    │ │ Concurrent  │ │ (in/out)    │ │
│  │ 1,247.3     │ │ 312         │ │ 8           │ │ 2.1M / 680K │ │
│  │ ▲ 12% mtd   │ │ ▲ 5% mtd   │ │ ▼ 2 mtd     │ │ ~$47 est.   │ │
│  └─────────────┘ └─────────────┘ └─────────────┘ └─────────────┘ │
│                                                         │
│  ┌─ Date range ──────────────────────────────────────────────┐ │
│  │ [Last 7d] [Last 30d] [This month] [Custom ▾]  [Export CSV]│ │
│  └────────────────────────────────────────────────────────────┘ │
│                                                         │
│  ┌─ Daily Usage Chart ─────────────────────────────┐   │
│  │                                                  │   │
│  │   ▓▓                                            │   │
│  │   ▓▓  ▓▓      ▓▓  ▓▓                           │   │
│  │   ▓▓  ▓▓  ▓▓  ▓▓  ▓▓  ▓▓      ▓▓              │   │
│  │   ▓▓  ▓▓  ▓▓  ▓▓  ▓▓  ▓▓  ▓▓  ▓▓  ▓▓         │   │
│  │   Apr 1  2   3   4   5   6   7   8   9          │   │
│  │                                                  │   │
│  │  [Container Minutes ▾]  [Show: All users ▾]      │   │
│  │  Also: LLM Tokens, LLM Cost, Sessions           │   │
│  └──────────────────────────────────────────────────┘   │
│                                                         │
│  ┌─ Breakdown Table ───────────────────────────────┐   │
│  │  [By User ▾]  Showing: Apr 1 – Apr 9            │   │
│  │                                                  │   │
│  │  User          Minutes  Sessions  Tokens  Est.cost   %  │   │
│  │  ─────────────────────────────────────────────────────── │   │
│  │  john@acme.com   487.3      112    820K    $18.40  39%  │   │
│  │  alice@acme.com  341.2       98    614K    $13.70  27%  │   │
│  │  bob@acme.com    218.8       62    441K     $9.80  18%  │   │
│  │  ci-bot          200.0       40    322K     $5.30  16%  │   │
│  │                                                  │   │
│  │  Clicking a row filters the chart to that user.  │   │
│  └──────────────────────────────────────────────────┘   │
│                                                         │
│  ┌─ Capacity Breakdown ────────────────────────────┐   │
│  │                                                  │   │
│  │  ██████████████████████  2 CPU / 4 GB   72.4%   │   │
│  │  ████████               4 CPU / 8 GB   21.1%   │   │
│  │  ███                    8 CPU / 16 GB   6.5%   │   │
│  │                                                  │   │
│  └──────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
```

### Component breakdown

```
settings/usage/
├── page.tsx                    # Main page, composes sections
├── usage-summary-cards.tsx     # Top KPI cards (container + token metrics)
├── usage-date-picker.tsx       # Date range selector (preset buttons + custom)
├── usage-timeseries-chart.tsx  # Recharts BarChart/AreaChart, groups hourly→daily by local tz
├── usage-breakdown-table.tsx   # Tabular breakdown by user/capacity/exit_reason
├── usage-capacity-bars.tsx     # Horizontal stacked bar for capacity tiers
├── usage-export-button.tsx     # CSV export trigger with format options
└── usage-helpers.ts            # groupByLocalDay(), formatTokenCount(), etc.
```

### Interaction model

1. **Date range selector** drives all queries. Preset buttons for "Last 7d", "Last 30d", "This month". Custom opens a date range picker (shadcn Calendar component).

2. **Chart metric selector** dropdown: Container Minutes (default), Sessions, Container Starts, Peak Concurrent, LLM Tokens (in+out), LLM Cost.

3. **User filter** dropdown on the chart: "All users" (default) or select a specific user. Populated from the breakdown data.

4. **Breakdown dimension toggle**: "By User" (default), "By Capacity", "By Exit Reason". Changes the table grouping.

5. **Click-to-filter**: Clicking a row in the breakdown table filters the timeseries chart to that dimension value (e.g., clicking a user shows only their usage in the chart).

6. **Day drill-down**: Clicking a bar in the chart filters the breakdown table to that specific day.

7. **CSV export**: "Export CSV" button next to the date range selector. Opens a small popover with options:
   - Granularity: Hourly / Daily (default)
   - Breakdown: None (org totals) / By User / By Capacity
   - Triggers a download via `GET /api/v1/usage/export` with the current date range and filters. The browser's timezone is sent as the `tz` param for daily grouping.

### State management

All filter state lives in URL search params for shareability:

```
/settings/usage?start=2026-04-01&end=2026-04-09&group=user&user=<uuid>&metric=minutes
```

React Query keys include all filter params so caching and refetching work correctly:

```typescript
queryKeys.usage = {
  timeseries: (params: UsageTimeseriesParams) =>
    ["usage", "timeseries", params] as const,
  breakdown: (params: UsageBreakdownParams) =>
    ["usage", "breakdown", params] as const,
  summary: (params: { start: string; end: string }) =>
    ["usage", "summary", params] as const,
};
```

### Empty/loading states

- **No data**: Show a centered illustration with "No usage data yet. Container usage will appear here once sessions start running."
- **Loading**: Skeleton placeholders matching the card/chart/table dimensions.
- **Partial data** (today still rolling up): Show a subtle "Data updates hourly" note below the chart.

## Performance Considerations

### Query performance targets

| Query | Target | Strategy |
|-------|--------|----------|
| Summary cards | < 50ms | SUM over rollup rows for date range |
| Timeseries (30d) | < 100ms | ~720 hourly rows from rollup, indexed on `(org_id, hour_utc)` |
| Timeseries (90d) | < 200ms | ~2160 hourly rows from rollup |
| Breakdown by user | < 100ms | Rollup grouped by `user_id`, indexed |
| Day drill-down | < 50ms | 24 hourly rows for one day |
| CSV export (90d) | < 2s | Streaming query, no in-memory buffering |

### Why not query raw events directly?

The peak concurrent calculation in `GetUsageSummary` uses a self-join that is O(n²). For an org with 10K events/month, running this per-day for 30 days would mean 30 × O(10K²/30²) ≈ 30 × O(111K) comparisons. The rollup pre-computes this once.

### Rollup staleness

The current hour's rollup is refreshed on each job run (every 5 min via the reaper loop). Dashboard shows a "Last updated: X minutes ago" timestamp. For real-time active container count, the existing `container.active` OTel gauge or a separate lightweight query (`SELECT COUNT(*) FROM container_usage_events WHERE stopped_at IS NULL`) can supplement.

### Storage footprint

For a large org with 500 users, 3 capacity tiers, 90 days retention (skipping per-user-tier level):

| Level | Rows | Size (~200 bytes/row) |
|-------|------|-----------------------|
| Org total | 2,160 | 0.4 MB |
| Per-user | 1.08M | 216 MB |
| Per-tier | 6,480 | 1.3 MB |
| **Total** | **~1.09M** | **~218 MB** |

Manageable for Postgres. A periodic cleanup job drops rows older than 90 days (raw events remain for audit). For most orgs with <50 users, the total is ~110K rows / ~22 MB.

## Migration plan

1. **Migration 000061**: Create `usage_hourly` table and indexes
2. **Backfill job**: One-time task that rolls up all existing `container_usage_events` + `sessions.token_usage` into `usage_hourly`
3. **Periodic rollup**: Integrate into the reaper loop or a standalone ticker
4. **API endpoints**: Add `/api/v1/usage/timeseries`, `/api/v1/usage/breakdown`, and `/api/v1/usage/export`
5. **Frontend**: Add recharts dependency, create the settings/usage page and components
6. **Sidebar**: Add "Usage" link to the ORGANIZATION settings group

## Future Extensions

- **Cost projection**: Multiply container-minutes by a configurable per-minute rate to show estimated compute costs alongside LLM costs
- **Alerts/budgets**: Set monthly container-minute and token budgets with email alerts at 80%/100%
- **Per-model token breakdown**: The current `sessions.token_usage` doesn't include model name; adding a `model` field to the token JSON would enable per-model cost tracking (e.g., Opus vs Sonnet usage)
- **JSON export**: Add `Accept: application/json` support to the export endpoint for programmatic integrations
- **Intra-day charts**: Since the rollup is already hourly, add an option to view hour-by-hour charts when the date range is ≤ 3 days
