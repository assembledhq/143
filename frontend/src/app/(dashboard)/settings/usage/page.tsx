"use client";

import { useState, useCallback } from "react";
import { useQueryState, parseAsString } from "nuqs";
import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { UsageSummaryCards } from "./usage-summary-cards";
import { UsageDatePicker, type DatePreset } from "./usage-date-picker";
import { UsageTimeseriesChart } from "./usage-timeseries-chart";
import { UsageBreakdownTable } from "./usage-breakdown-table";
import { UsageCapacityBars } from "./usage-capacity-bars";
import { UsageExportButton } from "./usage-export-button";
import { getDateRangePreset, formatDateForApi, nextDayIso, type MetricKey } from "./usage-helpers";

export default function UsagePage() {
  const [preset, setPreset] = useState<DatePreset>("30d");
  const [metric, setMetric] = useState<MetricKey>("total_container_minutes");
  const [dimension, setDimension] = useState<"user" | "capacity">("user");
  const [selectedUserId, setSelectedUserId] = useQueryState("user", parseAsString);
  const [selectedDay, setSelectedDay] = useQueryState("day", parseAsString);

  const { start: startDate, end: endDate } = getDateRangePreset(preset);
  const start = formatDateForApi(startDate);
  const end = formatDateForApi(endDate);

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

        <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
          <div className="lg:col-span-2">
            <UsageBreakdownTable
              start={breakdownStart}
              end={breakdownEnd}
              dimension={dimension}
              onDimensionChange={setDimension}
              onRowClick={handleRowClick}
              selectedKey={selectedUserId}
            />
          </div>
          <div>
            <UsageCapacityBars start={start} end={end} />
          </div>
        </div>

        <p className="text-xs text-muted-foreground text-center pb-4">
          Data updates every ~5 minutes. Usage shown is from the rollup table; real-time active containers may differ slightly.
        </p>
      </div>
    </PageContainer>
  );
}
