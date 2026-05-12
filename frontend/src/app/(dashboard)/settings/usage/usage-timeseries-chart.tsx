"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type { UsageTimeseriesBucket } from "@/lib/types";
import { Card, CardContent } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { fillMissingDays, formatCost, formatDayLabel, formatMinutes, formatTokenCount, groupByLocalDay, metricOptions, type MetricKey } from "./usage-helpers";
import type { UsageBreakdownDimension } from "./usage-breakdown-table";

type ChartMode = "totals" | "stacked";

interface UsageChartData {
  rows: Array<Record<string, number | string>>;
  series: Array<{ key: string; label: string }>;
}

interface UsageTimeseriesChartProps {
  start: string;
  end: string;
  metric: MetricKey;
  onMetricChange: (metric: MetricKey) => void;
  dimension: UsageBreakdownDimension;
  chartMode: ChartMode;
  onChartModeChange: (mode: ChartMode) => void;
  filters?: {
    agent?: string | null;
    model?: string | null;
    reasoning?: string | null;
  };
  onDayClick?: (day: string) => void;
}

function formatMetricValue(value: number, metric: MetricKey): string {
  switch (metric) {
    case "total_container_minutes":
      return formatMinutes(value);
    case "total_input_tokens":
    case "total_output_tokens":
    case "total_tokens":
      return formatTokenCount(value);
    case "total_llm_cost_usd":
      return formatCost(value);
    default:
      return String(Math.round(value));
  }
}

function seriesColor(index: number): string {
  const palette = [
    "hsl(var(--primary))",
    "hsl(20 80% 52%)",
    "hsl(160 60% 42%)",
    "hsl(220 65% 55%)",
    "hsl(40 85% 54%)",
  ];
  return palette[index % palette.length];
}

function daysInRange(start: string, end: string): string[] {
  const days: string[] = [];
  const cursor = new Date(start);
  const endDate = new Date(end);
  cursor.setUTCHours(0, 0, 0, 0);
  endDate.setUTCHours(0, 0, 0, 0);
  while (cursor < endDate) {
    days.push(cursor.toISOString().slice(0, 10));
    cursor.setUTCDate(cursor.getUTCDate() + 1);
  }
  return days;
}

export function buildUsageChartData(
  buckets: UsageTimeseriesBucket[],
  start: string,
  end: string,
  metric: MetricKey,
  chartMode: ChartMode
): UsageChartData {
  if (chartMode === "stacked") {
    if (buckets.length === 0) {
      return { rows: [], series: [] };
    }
    const dayMap = new Map<string, Record<string, number | string>>();
    const seriesMap = new Map<string, string>();
    for (const bucket of buckets) {
      const hourUTC = bucket.hour_utc;
      if (typeof hourUTC !== "string") {
        continue
      }
      const day = new Date(hourUTC).toLocaleDateString("en-CA");
      const row = dayMap.get(day) ?? { day, label: formatDayLabel(day), total: 0 };
      const key = typeof bucket.series_key === "string" && bucket.series_key !== "" ? bucket.series_key : "unknown";
      const metricValue = Number(bucket[metric] ?? 0);
      row[key] = Number(row[key] ?? 0) + metricValue;
      row.total = Number(row.total) + metricValue;
      dayMap.set(day, row);
      const label = typeof bucket.series_label === "string" && bucket.series_label !== "" ? bucket.series_label : key;
      seriesMap.set(key, label);
    }
    const series = [...seriesMap.entries()].map(([key, label]) => ({ key, label }));
    const rows = daysInRange(start, end).map((day) => {
      const existing = dayMap.get(day) ?? { day, label: formatDayLabel(day), total: 0 };
      const row: Record<string, number | string> = { ...existing };
      for (const item of series) {
        row[item.key] = Number(row[item.key] ?? 0);
      }
      row.total = Number(row.total ?? 0);
      return row;
    });
    return { rows, series };
  }

  if (buckets.length === 0) {
    return { rows: [], series: [] };
  }
  const grouped = groupByLocalDay(buckets);
  return {
    rows: fillMissingDays(grouped, start, end).map((d) => ({ ...d, label: formatDayLabel(d.day) })),
    series: [],
  };
}

export function UsageTimeseriesChart({
  start,
  end,
  metric,
  onMetricChange,
  dimension,
  chartMode,
  onChartModeChange,
  filters,
  onDayClick,
}: UsageTimeseriesChartProps) {
  const stackBy = chartMode === "stacked" ? dimension : undefined;
  const { data, isLoading, isError } = useQuery({
    queryKey: queryKeys.usage.timeseries({
      start,
      end,
      ...(stackBy ? { stack_by: stackBy } : {}),
      ...(filters?.agent ? { agent: filters.agent } : {}),
      ...(filters?.model ? { model: filters.model } : {}),
      ...(filters?.reasoning ? { reasoning: filters.reasoning } : {}),
    }),
    queryFn: () =>
      api.usage.getTimeseries({
        start,
        end,
        ...(stackBy ? { stack_by: stackBy } : {}),
        ...(filters?.agent ? { agent: filters.agent } : {}),
        ...(filters?.model ? { model: filters.model } : {}),
        ...(filters?.reasoning ? { reasoning: filters.reasoning } : {}),
      }),
  });

  const chartData = useMemo(() => {
    return buildUsageChartData(data?.data?.buckets ?? [], start, end, metric, chartMode);
  }, [chartMode, data, end, metric, start]);

  return (
    <Card>
      <CardContent className="p-4">
        <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-sm font-medium">Daily Usage</h3>
          <div className="flex items-center gap-2">
            <Select value={metric} onValueChange={(v) => onMetricChange(v as MetricKey)}>
              <SelectTrigger className="h-8 w-40 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {metricOptions.map((opt) => (
                  <SelectItem key={opt.value} value={opt.value} className="text-xs">
                    {opt.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={chartMode} onValueChange={(v) => onChartModeChange(v as ChartMode)}>
              <SelectTrigger className="h-8 w-32 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="totals" className="text-xs">Totals</SelectItem>
                <SelectItem value="stacked" className="text-xs">Stacked bars</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        {isLoading ? (
          <div className="h-64 animate-pulse rounded bg-muted/30" />
        ) : isError ? (
          <div className="flex h-64 items-center justify-center text-sm text-destructive">
            Failed to load usage data. Please try again later.
          </div>
        ) : chartData.rows.length === 0 ? (
          <div className="flex h-64 items-center justify-center text-sm text-muted-foreground">
            No usage data for this period
          </div>
        ) : (
          <ResponsiveContainer width="100%" height={280}>
            <BarChart
              data={chartData.rows}
              margin={{ top: 4, right: 4, bottom: 0, left: 4 }}
              onClick={(e: Record<string, unknown>) => {
                if (!e?.activePayload || !Array.isArray(e.activePayload)) return;
                const first = e.activePayload[0] as { payload?: Record<string, unknown> } | undefined;
                const day = first?.payload?.day;
                if (typeof day === "string") {
                  onDayClick?.(day);
                }
              }}
            >
              <CartesianGrid strokeDasharray="3 3" className="stroke-border/50" vertical={false} />
              <XAxis dataKey="label" tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }} tickLine={false} axisLine={false} interval="preserveStartEnd" />
              <YAxis tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }} tickLine={false} axisLine={false} width={48} tickFormatter={(v) => formatMetricValue(Number(v), metric)} />
              <Tooltip formatter={(value, name) => [formatMetricValue(Number(value ?? 0), metric), String(name)]} />
              {chartMode === "stacked"
                ? chartData.series.map((series, index) => (
                    <Bar key={series.key} dataKey={series.key} stackId="usage" fill={seriesColor(index)} radius={index === chartData.series.length - 1 ? [3, 3, 0, 0] : [0, 0, 0, 0]} name={series.label} />
                  ))
                : (
                    <Bar dataKey={metric} fill={seriesColor(0)} radius={[3, 3, 0, 0]} maxBarSize={40} style={{ cursor: onDayClick ? "pointer" : "default" }} />
                  )}
            </BarChart>
          </ResponsiveContainer>
        )}
      </CardContent>
    </Card>
  );
}
