"use client";

import { useState, useCallback, useMemo } from "react";
import dynamic from "next/dynamic";
import { useQueryState, parseAsString } from "nuqs";
import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { UsageSummaryCards } from "./usage-summary-cards";
import { UsageDatePicker, type DatePreset } from "./usage-date-picker";
import { UsageBreakdownTable } from "./usage-breakdown-table";
import { UsageExportButton } from "./usage-export-button";
import { getDateRangePreset, formatDateForApi, nextDayIso, type MetricKey } from "./usage-helpers";

// Move recharts into its own chunk — it's the heaviest dep on this page and
// the rest of the layout (summary cards, breakdown table) can paint before
// the chart bundle finishes loading.
const UsageTimeseriesChart = dynamic(
  () => import("./usage-timeseries-chart").then((m) => ({ default: m.UsageTimeseriesChart })),
  {
    ssr: false,
    loading: () => <div className="h-72 bg-muted/20 animate-pulse rounded-lg" />,
  },
);

export default function UsagePage() {
  const [preset, setPreset] = useState<DatePreset>("30d");
  const [metric, setMetric] = useState<MetricKey>("total_container_minutes");
  const [dimension, setDimension] = useState<"user" | "capacity">("user");
  const [selectedUserId, setSelectedUserId] = useQueryState("user", parseAsString);
  const [selectedDay, setSelectedDay] = useQueryState("day", parseAsString);

  // Memoize date range so query keys stay stable across renders. Truncate
  // to the minute so the ISO string doesn't change every millisecond.
  const { start, end } = useMemo(() => {
    const { start: s, end: e } = getDateRangePreset(preset);
    s.setSeconds(0, 0);
    e.setSeconds(0, 0);
    return { start: formatDateForApi(s), end: formatDateForApi(e) };
  }, [preset]);

  // If a specific day is clicked, narrow the breakdown to that day.
  const breakdownStart = selectedDay
    ? new Date(selectedDay + "T00:00:00").toISOString()
    : start;
  const breakdownEnd = selectedDay
    ? nextDayIso(selectedDay)
    : end;

  const handleRowClick = useCallback(
    (key: string) => {
      if (dimension === "user") {
        setSelectedUserId((prev) => (prev === key ? null : key));
      }
    },
    [dimension, setSelectedUserId]
  );

  const handleDayClick = useCallback(
    (day: string) => {
      setSelectedDay((prev) => (prev === day ? null : day));
    },
    [setSelectedDay]
  );

  const handlePresetChange = useCallback(
    (p: DatePreset) => {
      setPreset(p);
      setSelectedDay(null);
      setSelectedUserId(null);
    },
    [setSelectedDay, setSelectedUserId]
  );

  return (
    <PageContainer size="wide">
      <div className="space-y-6">
        <PageHeader
          title="Usage & Billing"
          description="Monitor container usage and LLM token consumption across your organization."
        />

        <UsageSummaryCards start={start} end={end} />

        <div className="flex items-center justify-between">
          <UsageDatePicker
            activePreset={preset}
            onPresetChange={handlePresetChange}
          />
          <div className="flex items-center gap-2">
            {selectedDay && (
              <Button
                variant="ghost"
                size="sm"
                className="h-8 text-xs text-muted-foreground"
                onClick={() => setSelectedDay(null)}
              >
                Clear day filter
              </Button>
            )}
            {selectedUserId && (
              <Button
                variant="ghost"
                size="sm"
                className="h-8 text-xs text-muted-foreground"
                onClick={() => setSelectedUserId(null)}
              >
                Clear user filter
              </Button>
            )}
            <UsageExportButton start={start} end={end} />
          </div>
        </div>

        <UsageTimeseriesChart
          start={start}
          end={end}
          metric={metric}
          onMetricChange={setMetric}
          userId={selectedUserId}
          onDayClick={handleDayClick}
        />

        <UsageBreakdownTable
          start={breakdownStart}
          end={breakdownEnd}
          dimension={dimension}
          onDimensionChange={setDimension}
          onRowClick={handleRowClick}
          selectedKey={dimension === "user" ? selectedUserId : undefined}
        />

        <p className="text-xs text-muted-foreground text-center pb-4">
          Data updates each reaper tick (typically every few minutes). The current hour is rolled up with partial data and finalized at the hour boundary.
        </p>
      </div>
    </PageContainer>
  );
}
