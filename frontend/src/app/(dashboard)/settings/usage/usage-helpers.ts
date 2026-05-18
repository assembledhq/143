import type { UsageTimeseriesBucket } from "@/lib/types";

export interface DailyBucket {
  day: string;
  total_container_minutes: number;
  total_sessions: number;
  total_container_starts: number;
  peak_concurrent: number;
  total_input_tokens: number;
  total_output_tokens: number;
  total_tokens: number;
  total_llm_cost_usd: number;
}

export function groupByLocalDay(buckets: UsageTimeseriesBucket[]): DailyBucket[] {
  const byDay = new Map<string, UsageTimeseriesBucket[]>();

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
    // Sessions use max-of-hourly (not sum) because hourly distinct counts
    // can't be naively summed — a session spanning multiple hours would be
    // double-counted. Max gives the peak hour's count, which underestimates
    // the true daily total. This is acceptable for the chart; the breakdown
    // table shows accurate counts via server-side COUNT(DISTINCT).
    total_sessions: max(hours, "total_sessions"),
    total_container_starts: sum(hours, "total_container_starts"),
    peak_concurrent: max(hours, "peak_concurrent"),
    total_input_tokens: sum(hours, "total_input_tokens"),
    total_output_tokens: sum(hours, "total_output_tokens"),
    total_tokens: sum(hours, "total_tokens"),
    total_llm_cost_usd: sum(hours, "total_llm_cost_usd"),
  }));
}

// Fill any missing local-days between start and end (inclusive) with a
// zero-value bucket so the chart renders a continuous x-axis even when
// no activity was recorded on some days.
export function fillMissingDays(
  buckets: DailyBucket[],
  start: string,
  end: string,
): DailyBucket[] {
  const byDay = new Map(buckets.map((b) => [b.day, b]));
  const startLocal = startOfLocalDay(new Date(start));
  const endLocal = startOfLocalDay(new Date(end));

  const result: DailyBucket[] = [];
  const cursor = new Date(startLocal);
  while (cursor <= endLocal) {
    const dayKey = cursor.toLocaleDateString("en-CA");
    result.push(
      byDay.get(dayKey) ?? {
        day: dayKey,
        total_container_minutes: 0,
        total_sessions: 0,
        total_container_starts: 0,
        peak_concurrent: 0,
        total_input_tokens: 0,
        total_output_tokens: 0,
        total_tokens: 0,
        total_llm_cost_usd: 0,
      },
    );
    cursor.setDate(cursor.getDate() + 1);
  }
  return result;
}

function startOfLocalDay(d: Date): Date {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate());
}

function sum(items: UsageTimeseriesBucket[], key: keyof UsageTimeseriesBucket): number {
  return items.reduce((acc, item) => acc + (Number(item[key]) || 0), 0);
}

function max(items: UsageTimeseriesBucket[], key: keyof UsageTimeseriesBucket): number {
  return items.reduce((acc, item) => Math.max(acc, Number(item[key]) || 0), 0);
}

export function formatTokenCount(count: number): string {
  if (count >= 1_000_000) {
    return `${(count / 1_000_000).toFixed(1)}M`;
  }
  if (count >= 1_000) {
    return `${(count / 1_000).toFixed(1)}K`;
  }
  return String(count);
}

export function formatCost(usd: number): string {
  if (Math.abs(usd) < 0.01) return "$0.00";
  if (usd < 0) return `-$${Math.abs(usd).toFixed(2)}`;
  return `$${usd.toFixed(2)}`;
}

export function formatEstimatedCost(usd: number, totalTokens: number): string {
  if (usd === 0 && totalTokens > 0) return "Unavailable";
  if (usd > 0 && usd < 0.01) return "<$0.01";
  return formatCost(usd);
}

export function formatMinutes(minutes: number): string {
  if (minutes >= 60) {
    const hours = minutes / 60;
    return `${hours.toFixed(1)}h`;
  }
  return `${minutes.toFixed(1)}m`;
}

export function formatNumber(n: number): string {
  return new Intl.NumberFormat("en-US").format(n);
}

export type MetricKey =
  | "total_container_minutes"
  | "total_sessions"
  | "total_container_starts"
  | "peak_concurrent"
  | "total_tokens"
  | "total_input_tokens"
  | "total_output_tokens"
  | "total_llm_cost_usd";

export const metricOptions: { value: MetricKey; label: string }[] = [
  { value: "total_container_minutes", label: "Container Hours" },
  { value: "total_sessions", label: "Peak Hourly Sessions" },
  { value: "total_container_starts", label: "Container Starts" },
  { value: "peak_concurrent", label: "Peak Concurrent" },
  { value: "total_tokens", label: "Total Tokens" },
  { value: "total_input_tokens", label: "Input Tokens" },
  { value: "total_output_tokens", label: "Output Tokens" },
  { value: "total_llm_cost_usd", label: "LLM Cost" },
];

export function getDateRangePreset(preset: string): { start: Date; end: Date } {
  const now = new Date();
  const end = now;

  switch (preset) {
    case "7d": {
      const start = new Date(now);
      start.setDate(start.getDate() - 7);
      return { start, end };
    }
    case "30d": {
      const start = new Date(now);
      start.setDate(start.getDate() - 30);
      return { start, end };
    }
    case "this_month": {
      const start = new Date(now.getFullYear(), now.getMonth(), 1);
      return { start, end };
    }
    default: {
      const start = new Date(now);
      start.setDate(start.getDate() - 30);
      return { start, end };
    }
  }
}

export function formatDateForApi(date: Date): string {
  return date.toISOString();
}

/**
 * Return the ISO string for midnight of the day after a "YYYY-MM-DD" string.
 * DST-safe because setDate adjusts for local-time day boundaries.
 *
 * Note: parsing with "T00:00:00" (no Z) creates a local-time Date, so the
 * resulting ISO string represents local midnight in UTC. This means the
 * breakdown window covers one local-time day, which matches the chart's
 * local-day grouping.
 */
export function nextDayIso(day: string): string {
  const d = new Date(day + "T00:00:00");
  d.setDate(d.getDate() + 1);
  return d.toISOString();
}

export function formatDayLabel(day: string): string {
  const d = new Date(day + "T12:00:00");
  return d.toLocaleDateString("en-US", { month: "short", day: "numeric" });
}
