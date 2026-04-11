import type { UsageTimeseriesBucket } from "@/lib/types";

export interface DailyBucket {
  day: string;
  total_container_minutes: number;
  total_sessions: number;
  total_container_starts: number;
  peak_concurrent: number;
  total_input_tokens: number;
  total_output_tokens: number;
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
    total_sessions: sum(hours, "total_sessions"),
    total_container_starts: sum(hours, "total_container_starts"),
    peak_concurrent: max(hours, "peak_concurrent"),
    total_input_tokens: sum(hours, "total_input_tokens"),
    total_output_tokens: sum(hours, "total_output_tokens"),
    total_llm_cost_usd: sum(hours, "total_llm_cost_usd"),
  }));
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
  if (usd < 0.01) return "$0.00";
  return `$${usd.toFixed(2)}`;
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
  | "total_input_tokens"
  | "total_output_tokens"
  | "total_llm_cost_usd";

export const metricOptions: { value: MetricKey; label: string }[] = [
  { value: "total_container_minutes", label: "Container Minutes" },
  { value: "total_sessions", label: "Sessions" },
  { value: "total_container_starts", label: "Container Starts" },
  { value: "peak_concurrent", label: "Peak Concurrent" },
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

export function formatDayLabel(day: string): string {
  const d = new Date(day + "T12:00:00");
  return d.toLocaleDateString("en-US", { month: "short", day: "numeric" });
}
