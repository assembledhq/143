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
} from "recharts";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { Card, CardContent } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  groupByLocalDay,
  fillMissingDays,
  formatDayLabel,
  formatMinutes,
  formatTokenCount,
  formatCost,
  metricOptions,
  type MetricKey,
  type DailyBucket,
} from "./usage-helpers";

interface UsageTimeseriesChartProps {
  start: string;
  end: string;
  metric: MetricKey;
  onMetricChange: (metric: MetricKey) => void;
  userId?: string | null;
  onDayClick?: (day: string) => void;
}

function formatMetricValue(value: number, metric: MetricKey): string {
  switch (metric) {
    case "total_container_minutes":
      return formatMinutes(value);
    case "total_input_tokens":
    case "total_output_tokens":
      return formatTokenCount(value);
    case "total_llm_cost_usd":
      return formatCost(value);
    default:
      return String(Math.round(value));
  }
}

function getBarColor(metric: MetricKey): string {
  switch (metric) {
    case "total_container_minutes":
      return "hsl(var(--primary))";
    case "total_sessions":
    case "total_container_starts":
      return "hsl(220, 70%, 55%)";
    case "peak_concurrent":
      return "hsl(350, 70%, 55%)";
    case "total_input_tokens":
    case "total_output_tokens":
      return "hsl(160, 60%, 45%)";
    case "total_llm_cost_usd":
      return "hsl(40, 80%, 50%)";
    default:
      return "hsl(var(--primary))";
  }
}

interface ChartTooltipProps {
  active?: boolean;
  payload?: Array<{ value: number; dataKey: string }>;
  label?: string;
  metric: MetricKey;
}

function ChartTooltip({ active, payload, label, metric }: ChartTooltipProps) {
  if (!active || !payload?.length) return null;

  return (
    <div className="rounded-lg border bg-background px-3 py-2 shadow-sm">
      <p className="text-xs font-medium text-foreground">{label}</p>
      <p className="text-sm font-semibold mt-0.5">
        {formatMetricValue(payload[0].value, metric)}
      </p>
    </div>
  );
}

export function UsageTimeseriesChart({
  start,
  end,
  metric,
  onMetricChange,
  userId,
  onDayClick,
}: UsageTimeseriesChartProps) {
  const { data, isLoading, isError } = useQuery({
    queryKey: queryKeys.usage.timeseries({
      start,
      end,
      ...(userId ? { user_id: userId } : {}),
    }),
    queryFn: () =>
      api.usage.getTimeseries({
        start,
        end,
        ...(userId ? { user_id: userId } : {}),
      }),
  });

  const dailyData = useMemo(() => {
    const grouped = data?.data?.buckets ? groupByLocalDay(data.data.buckets) : [];
    if (grouped.length === 0) return [];
    return fillMissingDays(grouped, start, end).map((d) => ({
      ...d,
      label: formatDayLabel(d.day),
    }));
  }, [data, start, end]);

  const metricLabel = metricOptions.find((o) => o.value === metric)?.label ?? metric;

  return (
    <Card>
      <CardContent className="p-4">
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-sm font-medium">Daily Usage</h3>
          <Select value={metric} onValueChange={(v) => onMetricChange(v as MetricKey)}>
            <SelectTrigger className="h-8 w-48 text-xs">
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
        </div>

        {isLoading ? (
          <div className="h-64 bg-muted/30 animate-pulse rounded" />
        ) : isError ? (
          <div className="h-64 flex items-center justify-center text-sm text-destructive">
            Failed to load usage data. Please try again later.
          </div>
        ) : dailyData.length === 0 ? (
          <div className="h-64 flex items-center justify-center text-sm text-muted-foreground">
            No usage data for this period
          </div>
        ) : (
          <ResponsiveContainer width="100%" height={280}>
            <BarChart
              data={dailyData}
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
              <XAxis
                dataKey="label"
                tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }}
                tickLine={false}
                axisLine={false}
                interval="preserveStartEnd"
              />
              <YAxis
                tick={{ fontSize: 11, fill: "hsl(var(--muted-foreground))" }}
                tickLine={false}
                axisLine={false}
                width={48}
                tickFormatter={(v) => formatMetricValue(v, metric)}
              />
              <Tooltip
                content={({ active, payload, label }) => (
                  <ChartTooltip
                    active={active}
                    payload={payload as unknown as ChartTooltipProps["payload"]}
                    label={label as string}
                    metric={metric}
                  />
                )}
                cursor={false}
              />
              <Bar
                dataKey={metric}
                fill={getBarColor(metric)}
                radius={[3, 3, 0, 0]}
                maxBarSize={40}
                name={metricLabel}
                activeBar={{ fill: getBarColor(metric), fillOpacity: 0.75 }}
                style={{ cursor: onDayClick ? "pointer" : "default" }}
              />
            </BarChart>
          </ResponsiveContainer>
        )}
      </CardContent>
    </Card>
  );
}
