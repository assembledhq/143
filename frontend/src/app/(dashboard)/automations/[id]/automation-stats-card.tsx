"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  type TooltipContentProps,
} from "recharts";
import { api } from "@/lib/api";
import { Card, CardContent } from "@/components/ui/card";
import type {
  AutomationRunStats,
  AutomationRunStatsBucket,
} from "@/lib/types";

interface AutomationStatsCardProps {
  automationId: string;
}

// Window length for the runs chart. 30 days matches the backend's default
// window; the backend caps any caller-supplied window at 90. We haven't
// surfaced a picker yet, so hardcoding avoids advertising flexibility the UI
// doesn't actually offer.
const STATS_WINDOW_DAYS = 30;

interface ChartDatum {
  day: string;
  label: string;
  tooltipLabel: string;
  completed: number;
  failed: number;
  completed_noop: number;
  skipped: number;
}

// fillGaps inserts zero-valued buckets for every day in [since, until) that the
// API omitted. Without this, a sparsely-run automation would render with
// uneven bar spacing because recharts spaces categorical ticks evenly — two
// adjacent bars in the data would look like two adjacent days regardless of
// actual gap length.
export function fillGaps(
  buckets: AutomationRunStatsBucket[],
  since: Date,
  until: Date,
): ChartDatum[] {
  const byDay = new Map<string, AutomationRunStatsBucket>();
  for (const b of buckets) {
    // Key is the YYYY-MM-DD prefix of the bucket's ISO timestamp. This matches
    // the backend's date_trunc('day', triggered_at AT TIME ZONE 'UTC') — if
    // that SQL ever switches timezones, this slice must switch with it or
    // buckets will silently miss their slot in the gap-filled output.
    byDay.set(b.bucket.slice(0, 10), b);
  }
  const out: ChartDatum[] = [];
  const cur = new Date(Date.UTC(
    since.getUTCFullYear(),
    since.getUTCMonth(),
    since.getUTCDate(),
  ));
  const end = new Date(Date.UTC(
    until.getUTCFullYear(),
    until.getUTCMonth(),
    until.getUTCDate(),
  ));
  // `until` is an exclusive upper bound (matches the backend's half-open
  // window), so stop strictly before `end`.
  while (cur < end) {
    const key = cur.toISOString().slice(0, 10);
    const b = byDay.get(key);
    // Buckets are UTC day boundaries — the backend aggregates by UTC so
    // labels must match or a user viewing late evening local time would
    // see "today" under yesterday's bar. The tooltip spells out UTC so the
    // bucket boundary convention isn't ambiguous.
    const mm = cur.getUTCMonth() + 1;
    const dd = cur.getUTCDate();
    out.push({
      day: key,
      label: `${mm}/${dd}`,
      tooltipLabel: `${mm}/${dd} (UTC)`,
      completed: b?.completed ?? 0,
      failed: b?.failed ?? 0,
      completed_noop: b?.completed_noop ?? 0,
      skipped: b?.skipped ?? 0,
    });
    cur.setUTCDate(cur.getUTCDate() + 1);
  }
  return out;
}

function formatDuration(seconds: number): string {
  if (!seconds || seconds < 0) return "—";
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const mins = Math.floor(seconds / 60);
  const secs = Math.round(seconds % 60);
  if (mins < 60) return secs === 0 ? `${mins}m` : `${mins}m ${secs}s`;
  const hours = Math.floor(mins / 60);
  const remMins = mins % 60;
  return remMins === 0 ? `${hours}h` : `${hours}h ${remMins}m`;
}

function ChartTooltip({ active, payload, label }: TooltipContentProps) {
  if (!active || !payload?.length) return null;
  const total = payload.reduce(
    (acc, p) => acc + (typeof p.value === "number" ? p.value : 0),
    0,
  );
  // Prefer the row's tooltipLabel (e.g. "4/19 (UTC)") so the user sees the
  // bucket timezone explicitly; fall back to the XAxis label if unset. The
  // `payload` field on each recharts entry is typed `any` because it carries
  // the full row of chart data — narrow it to ChartDatum at this boundary.
  const datum = payload[0]?.payload as ChartDatum | undefined;
  const headerLabel = datum?.tooltipLabel ?? label;
  if (total === 0) {
    // A gap-filled zero day: show a minimal tooltip so the user gets feedback
    // on hover. Returning null here would make the cursor highlight appear
    // without any explanation and read like a broken tooltip.
    return (
      <div className="rounded-lg border bg-surface-raised px-3 py-2 shadow-sm text-xs">
        <p className="font-medium text-foreground mb-1">{headerLabel}</p>
        <p className="text-muted-foreground">No runs</p>
      </div>
    );
  }
  return (
    <div className="rounded-lg border bg-surface-raised px-3 py-2 shadow-sm text-xs">
      <p className="font-medium text-foreground mb-1">{headerLabel}</p>
      {payload.map((p) => (
        <div key={String(p.dataKey ?? "")} className="flex items-center gap-2">
          <span
            className="inline-block h-2 w-2 rounded-sm"
            style={{ backgroundColor: p.color }}
          />
          <span className="text-muted-foreground">{String(p.name ?? "")}</span>
          <span className="ml-auto font-mono text-foreground">
            {typeof p.value === "number" ? p.value : 0}
          </span>
        </div>
      ))}
    </div>
  );
}

export function AutomationStatsCard({ automationId }: AutomationStatsCardProps) {
  // Use a stable bucket-start-of-day window so the query key doesn't change
  // every render (which would thrash the cache). Recomputing on mount is
  // fine — the window is forward-looking from "today in UTC".
  const { since, until } = useMemo(() => {
    const nowUtc = new Date();
    const untilDay = new Date(Date.UTC(
      nowUtc.getUTCFullYear(),
      nowUtc.getUTCMonth(),
      nowUtc.getUTCDate() + 1, // exclusive upper bound, one past today
    ));
    const sinceDay = new Date(untilDay);
    sinceDay.setUTCDate(sinceDay.getUTCDate() - STATS_WINDOW_DAYS);
    return { since: sinceDay.toISOString(), until: untilDay.toISOString() };
  }, []);

  const { data, isLoading, isError } = useQuery({
    queryKey: ["automation-stats", automationId, since, until],
    queryFn: () => api.automations.stats(automationId, { since, until }),
    refetchInterval: 60_000,
  });

  const stats: AutomationRunStats | undefined = data?.data;

  const chartData = useMemo(() => {
    if (!stats) return [];
    return fillGaps(stats.buckets, new Date(stats.since), new Date(stats.until));
  }, [stats]);

  const hasAnyRun = (stats?.totals.total ?? 0) > 0;
  const successPct = stats
    ? Math.round(stats.totals.success_rate * 100)
    : 0;

  return (
    <Card>
      <CardContent className="p-4">
        <div className="flex items-center justify-between mb-3">
          <div>
            <h3 className="text-sm font-medium">Runs · last {STATS_WINDOW_DAYS} days</h3>
            {stats && (
              <p className="text-xs text-muted-foreground mt-0.5">
                {stats.totals.total} total · {successPct}% success · avg {formatDuration(stats.totals.avg_duration_seconds)}
              </p>
            )}
          </div>
          <span
            className="text-xs uppercase tracking-wide text-muted-foreground"
            title="Day buckets use UTC boundaries so aggregates match the backend."
          >
            UTC
          </span>
        </div>

        {isLoading ? (
          <div className="h-40 bg-surface-pane animate-pulse rounded" />
        ) : isError ? (
          <div className="h-40 flex items-center justify-center text-sm text-destructive">
            Failed to load run stats.
          </div>
        ) : !hasAnyRun ? (
          <div className="h-40 flex items-center justify-center text-sm text-muted-foreground">
            No runs in the last {STATS_WINDOW_DAYS} days.
          </div>
        ) : (
          <ResponsiveContainer width="100%" height={180}>
            <BarChart
              data={chartData}
              margin={{ top: 4, right: 4, bottom: 0, left: 4 }}
            >
              <CartesianGrid strokeDasharray="3 3" className="stroke-border/50" vertical={false} />
              <XAxis
                dataKey="label"
                tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }}
                tickLine={false}
                axisLine={false}
                interval="preserveStartEnd"
                minTickGap={16}
              />
              <YAxis
                tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }}
                tickLine={false}
                axisLine={false}
                width={32}
                allowDecimals={false}
              />
              <Tooltip
                content={ChartTooltip}
                cursor={{ fill: "hsl(var(--muted))", opacity: 0.3 }}
              />
              {/*
                Stacked bars: completed + completed_noop count toward "success"
                visually (both green shades); failed is red; skipped is muted.
                Order matters — recharts stacks in render order, so the
                most-meaningful slice ("completed") sits at the bottom.
              */}
              <Bar dataKey="completed" stackId="runs" fill="hsl(142, 71%, 45%)" name="Completed" radius={[0, 0, 0, 0]} maxBarSize={20} />
              <Bar dataKey="completed_noop" stackId="runs" fill="hsl(142, 40%, 65%)" name="No-op" radius={[0, 0, 0, 0]} maxBarSize={20} />
              <Bar dataKey="failed" stackId="runs" fill="hsl(0, 72%, 55%)" name="Failed" radius={[3, 3, 0, 0]} maxBarSize={20} />
              <Bar dataKey="skipped" stackId="runs" fill="hsl(220, 10%, 65%)" name="Skipped" radius={[0, 0, 0, 0]} maxBarSize={20} />
            </BarChart>
          </ResponsiveContainer>
        )}
      </CardContent>
    </Card>
  );
}
