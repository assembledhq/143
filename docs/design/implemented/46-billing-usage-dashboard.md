# Design: Billing & Usage Dashboard

> **Status:** Implemented | **Last reviewed:** 2026-04-10

A new settings page (`/settings/usage`) that displays container usage and LLM token consumption, allows day-by-day exploration, per-user breakdowns, filtering, and CSV export. Backed by new API endpoints optimized for time-series and dimensional queries over existing tables plus a new hourly rollup table.

## Motivation

PR #244 added `container_usage_events` persistence and a summary endpoint (`GET /api/v1/usage`), but the current API only returns a single aggregate blob for a time range. To power an interactive dashboard we need:

- **Time-series data** — hourly buckets that the frontend aggregates into days using the viewer's timezone
- **Dimensional breakdowns** — by user, capacity tier, exit reason
- **LLM token tracking** — already captured per-session and per-message, but not aggregated for dashboard display
- **Fast queries** — repeated raw-event aggregation won't scale past ~50K events; a materialized rollup avoids rescanning event intervals on every page load
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
    capacity_tier       TEXT,           -- e.g. "2cpu_4096mb_10240diskmb", NULL = all tiers

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
- The original `GetUsageSummary` peak-concurrent query was O(n²) via self-join; the legacy path now uses an interval sweep, but rollups are still the right shape for dashboard-scale per-day and per-breakdown reads
- A rollup makes all dashboard queries O(hours) regardless of event count
- The rollup job is idempotent: `INSERT ... ON CONFLICT DO UPDATE`, safe to re-run

**Capacity tier derivation:** Concatenate `cpu_limit`, `memory_limit_mb`, and `disk_limit_mb` into a human-readable string (`"2cpu_4096mb_10240diskmb"`). This avoids storing separate dimension columns in the rollup and keeps cardinality bounded.

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

**Approach:** Display cost as "Estimated API cost" with a tooltip: *"Estimated cost at standard provider rates. Your actual billing may differ based on your plan."* This is accurate for pay-as-you-go users and still useful for subscription users as a consumption signal. The column header in the breakdown table reads "Est. API cost" so it is explicit that the value is not an invoice amount.

If a row has LLM tokens but no computed USD cost, the UI must show the cost as unavailable rather than `$0.00`. A zero in the rollup can mean "no normalized USD was reported or safely derived" for subscription/native-credit runs, not that the run was free. Very small positive USD costs should render as `<$0.01` instead of being rounded down to zero.

When actual billing integration exists (Stripe, etc.), we can add a "Billed cost" column that reflects the real charge. Until then, estimated API cost is the best signal we have and is strictly more useful than showing nothing.

### LLM token data source

LLM token usage is **already tracked** in the existing schema — no new instrumentation needed:

- **`sessions.token_usage`** (JSONB): normalized usage metadata written by the orchestrator after each agent run. The durable compatibility fields remain `{input_tokens, output_tokens, total_cost_usd}`, and the payload now also carries richer cost metadata when available: `cost` (normalized direct-or-derived USD cost), `native_cost` (provider-native non-USD units such as Codex credits), and `native_usage` (provider/model/billing-mode plus detailed token counters and a `reported` flag so downstream code can distinguish "provider emitted zeroes" from "provider emitted no usage payload at all").
- **`session_messages.token_usage`** (JSONB): Same structure, per-message granularity for multi-turn sessions.
- **`pm_plans.token_usage`** (JSONB): Token usage from PM agent runs.

The agent adapters are intentionally split into two layers:

- **Parsing layer:** Each adapter only parses the usage fields the provider actually emits.
- **Normalization layer:** `FinalizeTokenUsage(...)` produces the persisted contract and records whether cost was **direct** (provider-emitted) or **derived** (computed from official provider pricing when the provider exposes tokens but not a session cost).

This distinction matters because providers are not aligned:

- Claude Code can emit a direct `total_cost_usd` on result events.
- Codex commonly exposes token usage but subscription-backed runs are naturally denominated in **credits**, not USD.
- Gemini commonly exposes token usage but not a built-in session USD cost.
- Amp exposes token usage but not enough model identity to derive a safe cost.
- Pi can often derive cost because its configured model is explicit (`provider/model`) and API-key-backed.

For dashboard rollups, `total_llm_cost_usd` continues to aggregate `total_cost_usd` only. That keeps the existing UI stable while allowing us to preserve richer native billing metadata for later surfaces. The consequence is deliberate: subscription-backed Codex runs can retain a native credit estimate in `native_cost` without being misrepresented as a fake USD total.

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
  capacity  string (optional filter, e.g. "2cpu_4096mb_10240diskmb")
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

**Breakdown share labeling:** The share column is only meaningful when the basis is explicit. The current breakdown share is defined as **share of total container hours** in the selected range, so the UI labels it `Share of Hours` with a tooltip instead of a generic `%`. This keeps the table interpretable when adjacent columns also show sessions, tokens, `Tokens/session`, and estimated cost. If we later support alternate share bases, the label must continue to name the basis directly (for example `Share of Tokens`) rather than reverting to a bare percentage sign.

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
2026-04-01,2026-04-01T00:00:00Z,john@acme.com,2cpu_4096mb_10240diskmb,6.2,3,4,2,45200,12800,0.34
2026-04-01,2026-04-01T01:00:00Z,john@acme.com,2cpu_4096mb_10240diskmb,3.1,1,1,1,22100,8400,0.18
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

The legacy `GetUsageSummary` path now avoids the original O(n²) peak-concurrent self-join by fetching only event intervals that overlap the requested range and computing the peak with a Go sweep-line pass. Dashboard queries still read rollups so repeated per-day and per-breakdown views do not rescan raw events on every page load.

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

## Implementation Phases

### Phase 1: Data foundation

Schema, rollup job, and backfill — no user-facing changes yet, but everything downstream depends on this.

- [ ] **Migration 000061**: Create `usage_hourly` table with all columns, unique constraint, and indexes (including partial index for `user_id IS NOT NULL`)
- [ ] **`UsageRollupStore`**: New store with `RollupHour(ctx, orgID, hour)` method that queries `container_usage_events` joined with `sessions`, computes aggregates at all dimensional levels (org-total, per-user, per-tier), and upserts into `usage_hourly`
- [ ] **Token aggregation in rollup**: Join `sessions.token_usage` JSONB to populate `total_input_tokens`, `total_output_tokens`, `total_llm_cost_usd` columns
- [ ] **Backfill command**: One-time CLI command (`go run cmd/backfill-usage/main.go`) that rolls up all historical `container_usage_events` + `sessions.token_usage` into `usage_hourly`
- [ ] **Periodic rollup integration**: Add phase 4 to the `SessionReaper` loop that re-rolls the current hour every 5 minutes and catches up any missed hours
- [ ] **Retention cleanup**: Add a step that drops `usage_hourly` rows older than 90 days (raw events remain for audit)
- [ ] **Tests**: Unit tests for rollup store with pgxmock — verify multi-level aggregation, idempotent upserts, token parsing from JSONB, and NULL dimension handling

### Phase 2: API endpoints

Backend HTTP handlers that serve the dashboard. Can be tested with curl/Postman before the frontend exists.

- [ ] **`GET /api/v1/usage/timeseries`**: Handler that reads `usage_hourly` for org-total level rows, returns hourly buckets with container + token metrics. Support `group_by`, `user_id`, and `capacity` query params
- [ ] **`GET /api/v1/usage/breakdown`**: Handler that reads per-user or per-tier level rows, returns dimensional breakdown with percentage shares. Support `dimension`, `sort`, `limit` params
- [ ] **`GET /api/v1/usage/export`**: Streaming CSV handler — sets `Content-Type: text/csv` and `Content-Disposition` headers, writes rows directly from DB cursor via `csv.NewWriter(w)`. Support `granularity`, `dimension`, and `tz` params for timezone-aware daily grouping
- [ ] **Route registration**: Add all three routes to `router.go` within the authenticated org scope
- [ ] **Tests**: Handler tests with mock store for each endpoint — verify param validation, response shape, CSV format, and error cases (invalid date range, >90 day range)

### Phase 3: Core dashboard UI

The minimum viable usage page — summary cards and the main timeseries chart.

- [ ] **Install recharts**: Add `recharts` dependency to `frontend/package.json`
- [ ] **Sidebar nav entry**: Add `{ label: "Usage", icon: BarChart3, href: "/settings/usage", adminOnly: true }` to the ORGANIZATION group in `sidebar-settings-section.tsx`
- [ ] **`/settings/usage/page.tsx`**: Page shell with `PageContainer` and `PageHeader`, composes child components
- [ ] **`usage-summary-cards.tsx`**: Four KPI cards (Container Minutes, Total Sessions, Peak Concurrent, LLM Tokens) using existing `GET /api/v1/usage` for totals
- [ ] **`usage-date-picker.tsx`**: Date range selector with preset buttons (Last 7d, Last 30d, This month) and custom range via shadcn Calendar. State persisted in URL params via `nuqs`
- [ ] **`usage-helpers.ts`**: `groupByLocalDay()` function that aggregates UTC hourly buckets into local-timezone days, plus `formatTokenCount()` and `formatCost()` utilities
- [ ] **`usage-timeseries-chart.tsx`**: Recharts `BarChart` showing daily container minutes (default metric). Metric selector dropdown for switching between Container Minutes, Sessions, LLM Tokens, LLM Cost. Calls `GET /api/v1/usage/timeseries`
- [ ] **Query key registration**: Add `queryKeys.usage.timeseries`, `queryKeys.usage.breakdown`, `queryKeys.usage.summary` to `lib/query-keys.ts`
- [ ] **Empty/loading states**: Skeleton placeholders for cards and chart; "No usage data yet" empty state
- [ ] **Tests**: Component tests for summary cards, date picker preset logic, and `groupByLocalDay` helper

### Phase 4: Breakdown table and interactivity

The detailed breakdown view and cross-filtering between chart and table.

- [ ] **`usage-breakdown-table.tsx`**: TanStack React Table showing per-user breakdown with columns: User, Minutes, Sessions, Tokens, Est. Cost, Tokens/session, Share of Hours. Dimension toggle for "By User" / "By Capacity" / "By Exit Reason". Calls `GET /api/v1/usage/breakdown`
- [ ] **`usage-capacity-bars.tsx`**: Horizontal stacked bar chart showing capacity tier distribution
- [ ] **User filter on chart**: Dropdown populated from breakdown data — selecting a user filters the timeseries chart to that user's data via `user_id` query param
- [ ] **Click-to-filter (table → chart)**: Clicking a row in the breakdown table filters the chart to that user/tier
- [ ] **Click-to-filter (chart → table)**: Clicking a bar in the chart narrows the breakdown table to that specific day
- [ ] **URL state sync**: All filter state (`start`, `end`, `group`, `user`, `metric`) persisted in URL search params via `nuqs` for shareability
- [ ] **Tests**: Table rendering tests, filter interaction tests

### Phase 5: CSV export and polish

Export functionality and UX refinements.

- [ ] **`usage-export-button.tsx`**: Button next to date range selector. Opens a popover with granularity (Hourly/Daily) and breakdown (None/By User/By Capacity) options. Triggers download via `GET /api/v1/usage/export` with current filters and browser timezone
- [ ] **"Last updated" indicator**: Small timestamp below the chart showing when the rollup last ran, with "Data updates every ~5 minutes" tooltip
- [ ] **Month-over-month comparison**: Summary cards show % change vs. equivalent prior period (e.g., "▲ 12% vs last 30d")
- [ ] **Responsive layout**: Ensure cards stack on narrow viewports, chart has a minimum height, table scrolls horizontally
- [ ] **Cost tooltip**: "Estimated API cost" tooltip on the Est. Cost column header explaining it reflects provider rates, not billing plan charges

### Phase 6: Advanced features

Post-launch improvements that add depth to the dashboard.

- [ ] **Per-model token breakdown**: Add `model` field to the `TokenUsage` struct in `adapter.go` and persist in `sessions.token_usage` JSONB. Add `model` dimension to `usage_hourly` and the breakdown endpoint. Display model-level cost breakdown (e.g., Opus vs Sonnet)
- [ ] **Alerts and budgets**: Configurable monthly container-minute and token budgets stored in `org_settings`. Background job checks usage against budget and sends email alerts at 80%/100% thresholds
- [ ] **Compute cost projection**: Add a configurable per-minute container rate to org settings. Multiply container-minutes by rate to show estimated compute cost alongside LLM cost
- [ ] **Intra-day charts**: When date range is ≤ 3 days, show hour-by-hour chart instead of daily (data already available at hourly granularity)
- [ ] **JSON export**: Add `Accept: application/json` support to the export endpoint for programmatic integrations
- [ ] **Per-user-tier drill-down**: Enable the 4th rollup level (per-user × per-tier) for orgs that need to see which users consume which capacity tiers
