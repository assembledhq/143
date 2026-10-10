"use client";

import { memo, useMemo, useState } from "react";
import { ChartNoAxesColumnIncreasing, ChevronDown } from "lucide-react";
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { EmptyState } from "@/components/empty-state";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { CODE_REVIEW_ANALYTICS_METRIC_LABELS, type CodeReviewAnalyticsMetric } from "@/lib/code-review-analytics-metrics";
import type { CodeReviewAnalyticsTrend as CodeReviewAnalyticsTrendData, CodeReviewAnalyticsTrendBucket, CodeReviewAnalyticsTrendPoint } from "@/lib/types";

type AvailableTrend = Extract<CodeReviewAnalyticsTrendData, { status: "available" }>;

interface TrendChartRow {
  elapsedDays: number;
  current: number | null;
  previous: number | null;
  point: CodeReviewAnalyticsTrendPoint;
}

function metricValue(bucket: CodeReviewAnalyticsTrendBucket | null, metric: CodeReviewAnalyticsMetric): number | null {
  if (!bucket) return null;
  if (metric === "approval_rate") {
    return bucket.prs_reviewed > 0 ? bucket.approved_by_143 / bucket.prs_reviewed * 100 : null;
  }
  return bucket[metric];
}

function formatValue(value: number | null, metric: CodeReviewAnalyticsMetric): string {
  if (value === null || !Number.isFinite(value)) return "—";
  if (metric === "approval_rate") return `${Math.round(value)}%`;
  if (metric === "median_rounds_to_approval" || metric === "average_rounds_to_approval") {
    return value.toLocaleString(undefined, { minimumFractionDigits: 1, maximumFractionDigits: 1 });
  }
  return value.toLocaleString();
}

function dateTime(instant: string): string {
  return new Date(instant).toLocaleString(undefined, {
    year: "numeric", month: "short", day: "numeric", hour: "numeric", minute: "2-digit", second: "2-digit", timeZoneName: "short",
  });
}

function interval(start: string, end: string): string {
  return `${dateTime(start)} – ${dateTime(end)} (end exclusive)`;
}

function compactDateRange(start: string, end: string, includeYear = false): string {
  const first = new Date(start);
  const last = new Date(Math.max(Date.parse(start), Date.parse(end) - 1));
  const showYear = includeYear || first.getFullYear() !== last.getFullYear();
  const dateOptions: Intl.DateTimeFormatOptions = { month: "short", day: "numeric", ...(showYear ? { year: "numeric" } : {}) };
  if (first.toDateString() === last.toDateString()) return first.toLocaleDateString(undefined, dateOptions);
  if (first.getMonth() === last.getMonth() && first.getFullYear() === last.getFullYear()) {
    return `${first.toLocaleDateString(undefined, { month: "short" })} ${first.getDate()}–${last.getDate()}${showYear ? `, ${first.getFullYear()}` : ""}`;
  }
  return `${first.toLocaleDateString(undefined, dateOptions)}–${last.toLocaleDateString(undefined, dateOptions)}`;
}

function compactBucketInterval(bucket: CodeReviewAnalyticsTrendBucket): string {
  const start = new Date(bucket.start);
  const end = new Date(bucket.end);
  const midnight = (value: Date) => value.getHours() === 0 && value.getMinutes() === 0 && value.getSeconds() === 0 && value.getMilliseconds() === 0;
  if (midnight(start) && midnight(end) && (end.getTime() - start.getTime()) % 86_400_000 === 0) {
    return compactDateRange(bucket.start, bucket.end);
  }
  const showTimezone = start.getTimezoneOffset() !== end.getTimezoneOffset();
  const timeOptions: Intl.DateTimeFormatOptions = { hour: "numeric", minute: "2-digit", ...(showTimezone ? { timeZoneName: "short" } : {}) };
  const dateOptions: Intl.DateTimeFormatOptions = { month: "short", day: "numeric", ...(start.getFullYear() !== end.getFullYear() ? { year: "numeric" } : {}) };
  const first = `${start.toLocaleDateString(undefined, dateOptions)}, ${start.toLocaleTimeString(undefined, timeOptions)}`;
  const last = start.toDateString() === end.toDateString()
    ? end.toLocaleTimeString(undefined, timeOptions)
    : `${end.toLocaleDateString(undefined, dateOptions)}, ${end.toLocaleTimeString(undefined, timeOptions)}`;
  return `${first}–${last}`;
}

function duration(bucket: Pick<CodeReviewAnalyticsTrendBucket, "start" | "end">): string {
  const hours = (Date.parse(bucket.end) - Date.parse(bucket.start)) / 3_600_000;
  return `${hours.toLocaleString(undefined, { maximumFractionDigits: 3 })} hours`;
}

function sample(bucket: CodeReviewAnalyticsTrendBucket, metric: CodeReviewAnalyticsMetric): string {
  if (metric.endsWith("rounds_to_approval")) return `${bucket.approved_by_143.toLocaleString()} approved ${bucket.approved_by_143 === 1 ? "PR" : "PRs"}`;
  if (metric === "prs_reviewed") return `${bucket.prs_reviewed.toLocaleString()} ${bucket.prs_reviewed === 1 ? "PR" : "PRs"}`;
  return `${bucket.approved_by_143.toLocaleString()} of ${bucket.prs_reviewed.toLocaleString()} ${bucket.prs_reviewed === 1 ? "PR" : "PRs"} approved`;
}

function hasDifferentDurations(point: CodeReviewAnalyticsTrendPoint): boolean {
  return point.previous !== null &&
    Date.parse(point.current.end) - Date.parse(point.current.start) !==
      Date.parse(point.previous.end) - Date.parse(point.previous.start);
}

function bucketNotes(point: CodeReviewAnalyticsTrendPoint, period: "current" | "previous"): string[] {
  const bucket = point[period];
  if (!bucket) return ["The previous period is shorter; no comparison is available for this portion."];
  return [
    ...(bucket.partial ? ["Partial bucket interval."] : []),
    ...(hasDifferentDurations(point) ? ["These intervals have slightly different durations."] : []),
    ...(bucket.overflow_prs > 0 ? ["This point includes the latest PRs captured after the chart's range anchor."] : []),
    ...(point.current.overflow_prs > 0 && point.previous ? ["This pair shows nominal-period context; it does not have matched exposure."] : []),
  ];
}

function PointDetails({ point, metric, hasComparison }: { point: CodeReviewAnalyticsTrendPoint; metric: CodeReviewAnalyticsMetric; hasComparison: boolean }) {
  return (
    <div className="max-w-72 space-y-2 rounded-lg border border-border bg-card p-3 text-xs whitespace-normal shadow-sm">
      {(["current", "previous"] as const).map((period) => {
        const bucket = point[period];
        if (!bucket) return null;
        return (
          <div key={period} className="grid grid-cols-[1fr_auto] gap-x-4 gap-y-0.5">
            <p className="font-medium">{period === "current" ? "Current" : "Previous"} · <span className="font-normal text-muted-foreground">{compactBucketInterval(bucket)}</span></p>
            <p className="font-semibold tabular-nums">{formatValue(metricValue(bucket, metric), metric)}</p>
            <p className="col-span-2 text-muted-foreground">{sample(bucket, metric)}</p>
          </div>
        );
      })}
      {hasComparison && !point.previous ? <p className="text-muted-foreground">Previous · No comparison</p> : null}
    </div>
  );
}

function AboutChart({ trend, metric }: { trend: AvailableTrend; metric: CodeReviewAnalyticsMetric }) {
  return (
    <Collapsible className="contents">
      <CollapsibleTrigger asChild>
        <Button variant="ghost" size="sm" className="group gap-2">
          About this chart
          <ChevronDown
            className="size-4 transition-transform group-data-[state=open]:rotate-180"
            aria-hidden="true"
          />
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent className="w-full space-y-3 text-xs text-muted-foreground">
        {trend.current_window ? <div className="space-y-1">
          <p><span className="font-medium text-foreground">Current requested range:</span> {interval(trend.current_window.start, trend.current_window.end)}</p>
          <p><span className="font-medium text-foreground">Current observed range:</span> {interval(trend.current_window.start, trend.current_window.observed_end)}</p>
        </div> : null}
        {trend.previous_window ? <div className="space-y-1">
          <p><span className="font-medium text-foreground">Previous requested range:</span> {interval(trend.previous_window.start, trend.previous_window.end)}</p>
          <p><span className="font-medium text-foreground">Previous compared range:</span> {interval(trend.previous_window.start, trend.previous_window.compared_end)}</p>
        </div> : null}
        <p>Intervals align by elapsed time from each period&apos;s start. Dates and times use your local timezone. Interval ends are exclusive.</p>
        {metric !== "prs_reviewed" ? <p>Outcomes include later completed reviews. Recent PRs may still receive approvals.{trend.previous_window ? " Previous-period PRs have had longer to do so." : ""}</p> : null}
        {trend.observed_end_advanced_by_data || trend.overflow_prs > 0 ? <p>The observed range includes PRs captured after the range anchor ({dateTime(trend.generated_at)}).{trend.overflow_prs > 0 ? ` ${trend.overflow_prs.toLocaleString()} ${trend.overflow_prs === 1 ? "PR is" : "PRs are"} attributed to the final nominal interval; that comparison does not represent matched exposure.` : ""}</p> : null}
        <p>The data table shows each interval&apos;s full dates, actual duration, sample counts, and any comparison limits.</p>
      </CollapsibleContent>
    </Collapsible>
  );
}

function TrendDataTable({ trend, metric }: { trend: AvailableTrend; metric: CodeReviewAnalyticsMetric }) {
  const [open, setOpen] = useState(false);
  return (
    <Collapsible className="contents" open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger asChild>
        <Button variant="ghost" size="sm" className="gap-2">
          {open ? "Hide trend data" : "Show trend data"}
          <ChevronDown className={open ? "size-4 rotate-180" : "size-4"} aria-hidden="true" />
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent className="w-full">
        <Table aria-label={`${CODE_REVIEW_ANALYTICS_METRIC_LABELS[metric]} trend data`}>
          <TableHeader>
            <TableRow>
              <TableHead>Period</TableHead>
              <TableHead>Actual interval (local time)</TableHead>
              <TableHead className="text-right">Duration</TableHead>
              <TableHead className="text-right">{CODE_REVIEW_ANALYTICS_METRIC_LABELS[metric]}</TableHead>
              <TableHead className="text-right">PRs</TableHead>
              <TableHead className="text-right">Approved PRs</TableHead>
              <TableHead>Notes</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {trend.points.flatMap((point) => (["current", "previous"] as const)
              .filter((period) => period === "current" || trend.previous_window !== null)
              .map((period) => {
                const bucket = point[period];
                return (
                  <TableRow key={`${point.index}-${period}`}>
                    <TableCell>{period === "current" ? "Current" : "Previous"}</TableCell>
                    <TableCell>{bucket ? interval(bucket.start, bucket.end) : "No comparison"}</TableCell>
                    <TableCell className="text-right tabular-nums">{bucket ? duration(bucket) : "—"}</TableCell>
                    <TableCell className="text-right tabular-nums">{formatValue(metricValue(bucket, metric), metric)}</TableCell>
                    <TableCell className="text-right tabular-nums">{bucket?.prs_reviewed.toLocaleString() ?? "—"}</TableCell>
                    <TableCell className="text-right tabular-nums">{bucket?.approved_by_143.toLocaleString() ?? "—"}</TableCell>
                    <TableCell className="max-w-80 whitespace-normal text-xs text-muted-foreground">{bucketNotes(point, period).join(" ")}</TableCell>
                  </TableRow>
                );
              }))}
          </TableBody>
        </Table>
      </CollapsibleContent>
    </Collapsible>
  );
}

function AvailableChart({ trend, metric }: { trend: AvailableTrend; metric: CodeReviewAnalyticsMetric }) {
  const [inspectedPoint, setInspectedPoint] = useState<number | null>(null);
  const chartData = useMemo<TrendChartRow[]>(() => trend.points.map((point) => ({
    elapsedDays: trend.current_window ? (Date.parse(point.current.start) - Date.parse(trend.current_window.start)) / 86_400_000 : 0,
    current: metricValue(point.current, metric),
    previous: metricValue(point.previous, metric),
    point,
  })), [trend, metric]);
  const currentEmpty = trend.points.every((point) => point.current.prs_reviewed === 0);
  const bothEmpty = currentEmpty && trend.points.every((point) => !point.previous || point.previous.prs_reviewed === 0);
  const noCurrentApprovals = trend.points.every((point) => point.current.approved_by_143 === 0);
  const hasMetricData = chartData.some((row) => row.current !== null || row.previous !== null);
  const roundsMetric = metric.endsWith("rounds_to_approval");
  const selectedPoint = inspectedPoint === null ? undefined : trend.points[inspectedPoint];
  const futureOnly = trend.mode === "finite" && trend.points.length === 0;
  const includeLegendYear = !!trend.current_window && !!trend.previous_window &&
    new Date(trend.current_window.start).getFullYear() !== new Date(trend.previous_window.start).getFullYear();
  const notices = [
    ...(trend.points.some((point) => point.current.partial || point.previous?.partial) ? ["Some intervals are partial."] : []),
    ...(trend.points.some(hasDifferentDurations) ? ["Some comparison intervals have different durations."] : []),
    ...(trend.overflow_prs > 0 ? ["Latest PRs are included; the final comparison uses nominal dates."] : trend.observed_end_advanced_by_data ? ["Latest PRs are included."] : []),
  ];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap gap-x-6 gap-y-2 text-xs text-muted-foreground" aria-label="Compared periods">
        {trend.current_window ? (
          <p className="flex items-center gap-2">
            <span className="w-5 shrink-0 border-t-2 border-primary" aria-hidden="true" />
            <span><span className="font-medium text-foreground">Current</span> · {compactDateRange(trend.current_window.start, Date.parse(trend.current_window.observed_end) > Date.parse(trend.current_window.start) ? trend.current_window.observed_end : trend.current_window.end, includeLegendYear)}</span>
          </p>
        ) : null}
        {trend.previous_window ? (
          <p className="flex items-center gap-2">
            <span className="w-5 shrink-0 border-t-2 border-dashed border-muted-foreground" aria-hidden="true" />
            <span><span className="font-medium text-foreground">Previous</span> · {compactDateRange(trend.previous_window.start, Date.parse(trend.previous_window.compared_end) > Date.parse(trend.previous_window.start) ? trend.previous_window.compared_end : trend.previous_window.end, includeLegendYear)}</span>
          </p>
        ) : null}
      </div>
      {!trend.previous_window ? <p className="text-xs text-muted-foreground">All time has no previous period to compare.</p> : null}
      {notices.length > 0 ? <p className="text-xs text-muted-foreground">{notices.join(" ")}</p> : null}
      {roundsMetric && noCurrentApprovals && !bothEmpty && hasMetricData ? <p className="text-xs text-muted-foreground">No approved PRs in this period.</p> : null}

      {futureOnly || bothEmpty || !hasMetricData ? (
        <EmptyState
          icon={ChartNoAxesColumnIncreasing}
          variant="inline"
          title={futureOnly ? "This period has not started yet." : bothEmpty ? (trend.mode === "all_time" ? "No PRs were first sent in this period." : "No PRs were first sent in either period.") : "No approved PRs in this period."}
          description={futureOnly ? "Select an observed period to view its trend." : "Choose another metric, time window, or repository to view a trend."}
        />
      ) : (
        <>
          <ResponsiveContainer width="100%" height={280}>
            <LineChart
              data={chartData}
              accessibilityLayer
              aria-label={`${CODE_REVIEW_ANALYTICS_METRIC_LABELS[metric]} over elapsed time`}
              margin={{ top: 12, right: 12, left: 0, bottom: 8 }}
              onClick={(state) => {
                const index = Number(state.activeTooltipIndex);
                if (state.activeTooltipIndex !== undefined && Number.isInteger(index) && trend.points[index]) setInspectedPoint(index);
              }}
            >
              <CartesianGrid stroke="var(--border)" vertical={false} />
              <XAxis
                dataKey="elapsedDays"
                type="number"
                domain={[0, Math.max(1, chartData.at(-1)?.elapsedDays ?? 0)]}
                allowDecimals={false}
                tickFormatter={(day: number) => `Day ${day + 1}`}
                tick={{ fill: "var(--muted-foreground)", fontSize: 12 }}
                stroke="var(--border)"
              />
              <YAxis
                domain={metric === "approval_rate" ? [0, 100] : [0, "auto"]}
                allowDecimals={metric === "approval_rate" || roundsMetric}
                tickFormatter={(value: number) => metric === "approval_rate" ? `${value}%` : value.toLocaleString()}
                tick={{ fill: "var(--muted-foreground)", fontSize: 12 }}
                stroke="var(--border)"
              />
              <Tooltip
                filterNull={false}
                isAnimationActive={false}
                content={({ active, payload }) => {
                  const row = payload?.[0]?.payload as TrendChartRow | undefined;
                  return active && row ? <PointDetails point={row.point} metric={metric} hasComparison={trend.previous_window !== null} /> : null;
                }}
              />
              <Line name="Current period" dataKey="current" type="linear" stroke="var(--primary)" strokeWidth={2} dot={{ r: 2 }} activeDot={{ r: 4 }} connectNulls={false} isAnimationActive={false} />
              {trend.previous_window ? <Line name="Previous period" dataKey="previous" type="linear" stroke="var(--muted-foreground)" strokeWidth={2} strokeDasharray="5 4" dot={{ r: 2 }} activeDot={{ r: 4 }} connectNulls={false} isAnimationActive={false} /> : null}
            </LineChart>
          </ResponsiveContainer>
          {selectedPoint ? <div aria-live="polite"><PointDetails point={selectedPoint} metric={metric} hasComparison={trend.previous_window !== null} /></div> : null}
        </>
      )}

      <div className="flex flex-wrap items-start gap-x-2 gap-y-3">
        <AboutChart trend={trend} metric={metric} />
        {trend.points.length > 0 ? <TrendDataTable trend={trend} metric={metric} /> : null}
      </div>
    </div>
  );
}

export const CodeReviewAnalyticsTrend = memo(function CodeReviewAnalyticsTrend({ trend, metric }: {
  trend?: CodeReviewAnalyticsTrendData;
  metric: CodeReviewAnalyticsMetric;
}) {
  const unavailableCopy = !trend
    ? "Trend data is unavailable from this server."
    : trend.status === "unavailable"
      ? trend.unavailable_reason === "range_too_large"
        ? "Choose a range of ten years or less to view trends."
        : "This range cannot be charted. Choose another range."
      : null;
  return (
    <Card className="shadow-sm" aria-label="Code review analytics trend">
      <CardHeader>
        <CardTitle><h2 className="text-sm font-semibold">{CODE_REVIEW_ANALYTICS_METRIC_LABELS[metric]} trend</h2></CardTitle>
      </CardHeader>
      <CardContent className="pt-3">
        {trend?.status === "available" ? <AvailableChart trend={trend} metric={metric} /> : (
          <EmptyState icon={ChartNoAxesColumnIncreasing} variant="inline" title={unavailableCopy ?? "Trend unavailable"} description="The current report remains available above and below this chart." />
        )}
      </CardContent>
    </Card>
  );
});
