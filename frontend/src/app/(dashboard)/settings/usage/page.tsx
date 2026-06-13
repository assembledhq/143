"use client";

import { useState, useCallback, useMemo } from "react";
import dynamic from "next/dynamic";
import { useQueryState, parseAsString } from "nuqs";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { UsageSummaryCards } from "./usage-summary-cards";
import { UsageDatePicker, type DatePreset } from "./usage-date-picker";
import { UsageBreakdownTable, type UsageBreakdownDimension } from "./usage-breakdown-table";
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
  const [metric, setMetric] = useState<MetricKey>("total_tokens");
  const [dimension, setDimension] = useState<UsageBreakdownDimension>("user");
  const [chartMode, setChartMode] = useState<"totals" | "stacked">("totals");
  const [selectedAgent, setSelectedAgent] = useQueryState("agent", parseAsString);
  const [selectedModel, setSelectedModel] = useQueryState("model", parseAsString);
  const [selectedReasoning, setSelectedReasoning] = useQueryState("reasoning", parseAsString);
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

  const handleRowClick = useCallback(({ dimension: rowDimension, key }: { dimension: UsageBreakdownDimension; key: string }) => {
    if (rowDimension === "agent") {
      setSelectedAgent((prev) => (prev === key ? null : key));
    } else if (rowDimension === "model") {
      setSelectedModel((prev) => (prev === key ? null : key));
    } else if (rowDimension === "reasoning") {
      setSelectedReasoning((prev) => (prev === key ? null : key));
    }
  }, [setSelectedAgent, setSelectedModel, setSelectedReasoning]);

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
      setSelectedAgent(null);
      setSelectedModel(null);
      setSelectedReasoning(null);
    },
    [setSelectedAgent, setSelectedDay, setSelectedModel, setSelectedReasoning]
  );

  const filters = {
    agent: selectedAgent,
    model: selectedModel,
    reasoning: selectedReasoning,
  };
  const exportDimension = dimension === "capacity" ? "user" : dimension;

  return (
    <PageContainer size="wide">
      <div className="space-y-6">
        <PageHeader
          title="Usage & Billing"
          description="Understand container, token, and cost trends across coding runtimes."
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
            {(selectedAgent || selectedModel || selectedReasoning) && (
              <Button
                variant="ghost"
                size="sm"
                className="h-8 text-xs text-muted-foreground"
                onClick={() => {
                  setSelectedAgent(null);
                  setSelectedModel(null);
                  setSelectedReasoning(null);
                }}
              >
                Clear filters
              </Button>
            )}
            <UsageExportButton start={start} end={end} dimension={exportDimension} filters={filters} />
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Select
            value={dimension}
            onValueChange={(v) => {
              const nextDimension = v as UsageBreakdownDimension;
              setDimension(nextDimension);
              if (nextDimension === "user") {
                setChartMode("totals");
              }
            }}
          >
            <SelectTrigger className="h-8 w-40 text-xs" aria-label="Break down by">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="user" className="text-xs">By User</SelectItem>
              <SelectItem value="agent" className="text-xs">By Agent</SelectItem>
              <SelectItem value="model" className="text-xs">By Model</SelectItem>
              <SelectItem value="reasoning" className="text-xs">By Reasoning</SelectItem>
            </SelectContent>
          </Select>
          <Select value={selectedAgent ?? "any"} onValueChange={(v) => setSelectedAgent(v === "any" ? null : v)}>
            <SelectTrigger className="h-8 w-32 text-xs" aria-label="Agent filter">
              <SelectValue placeholder="Agent" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="any" className="text-xs">Any agent</SelectItem>
              <SelectItem value="codex" className="text-xs">Codex</SelectItem>
              <SelectItem value="claude_code" className="text-xs">Claude Code</SelectItem>
              <SelectItem value="gemini_cli" className="text-xs">Gemini CLI</SelectItem>
              <SelectItem value="amp" className="text-xs">Amp</SelectItem>
              <SelectItem value="pi" className="text-xs">Pi</SelectItem>
              <SelectItem value="opencode" className="text-xs">OpenCode</SelectItem>
            </SelectContent>
          </Select>
          <Select value={selectedReasoning ?? "any"} onValueChange={(v) => setSelectedReasoning(v === "any" ? null : v)}>
            <SelectTrigger className="h-8 w-36 text-xs" aria-label="Reasoning filter">
              <SelectValue placeholder="Reasoning" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="any" className="text-xs">Any reasoning</SelectItem>
              <SelectItem value="default" className="text-xs">Default</SelectItem>
              <SelectItem value="low" className="text-xs">Low</SelectItem>
              <SelectItem value="medium" className="text-xs">Medium</SelectItem>
              <SelectItem value="high" className="text-xs">High</SelectItem>
              <SelectItem value="xhigh" className="text-xs">XHigh</SelectItem>
              <SelectItem value="max" className="text-xs">Max</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <UsageTimeseriesChart
          start={start}
          end={end}
          metric={metric}
          onMetricChange={setMetric}
          dimension={dimension}
          chartMode={chartMode}
          onChartModeChange={setChartMode}
          filters={filters}
          onDayClick={handleDayClick}
        />

        <UsageBreakdownTable
          start={breakdownStart}
          end={breakdownEnd}
          dimension={dimension}
          filters={filters}
          onRowClick={handleRowClick}
          selectedKey={dimension === "agent" ? selectedAgent : dimension === "model" ? selectedModel : dimension === "reasoning" ? selectedReasoning : undefined}
        />

        <p className="text-xs text-muted-foreground text-center pb-4">
          Data updates each reaper tick (typically every few minutes). The current hour is rolled up with partial data and finalized at the hour boundary.
        </p>
      </div>
    </PageContainer>
  );
}
