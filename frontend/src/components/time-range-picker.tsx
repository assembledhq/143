"use client";

import { useMemo, useState } from "react";
import { format, subDays } from "date-fns";
import { CalendarRange, Check, ChevronDown } from "lucide-react";
import type { DateRange } from "react-day-picker";

import { Button } from "@/components/ui/button";
import { Calendar } from "@/components/ui/calendar";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Separator } from "@/components/ui/separator";
import {
  customTimeRange,
  customTimeRangeDates,
  type PresetTimeRange,
  type TimeRangeFilter,
} from "@/lib/time-range";
import { cn } from "@/lib/utils";

const PRESETS: Array<{
  value: PresetTimeRange;
  label: string;
  description: string;
}> = [
  { value: "7d", label: "Last 7 days", description: "Rolling week" },
  { value: "30d", label: "Last 30 days", description: "Rolling month" },
  { value: "90d", label: "Last 90 days", description: "Rolling quarter" },
  { value: "all", label: "All time", description: "No date limit" },
];

function defaultDraft(value: TimeRangeFilter): DateRange {
  const custom = customTimeRangeDates(value);
  if (custom) return custom;
  const today = new Date();
  return { from: subDays(today, 30), to: today };
}

export function timeRangeLabel(value: TimeRangeFilter): string {
  const preset = PRESETS.find((candidate) => candidate.value === value);
  if (preset) return preset.label;

  const custom = customTimeRangeDates(value);
  if (!custom) return "Custom range";
  return `${format(custom.from, "MMM d, yyyy")} – ${format(custom.to, "MMM d, yyyy")}`;
}

export function TimeRangePicker({
  label,
  value,
  onValueChange,
}: {
  label: string;
  value: TimeRangeFilter;
  onValueChange: (value: TimeRangeFilter) => void;
}) {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<DateRange>(() => defaultDraft(value));
  const today = useMemo(() => {
    const date = new Date();
    date.setHours(23, 59, 59, 999);
    return date;
  }, []);

  const handleOpenChange = (nextOpen: boolean) => {
    if (nextOpen) setDraft(defaultDraft(value));
    setOpen(nextOpen);
  };

  const applyPreset = (preset: PresetTimeRange) => {
    onValueChange(preset);
    setOpen(false);
  };

  const rangeIncomplete = !draft.from || !draft.to;

  const applyCustomRange = () => {
    if (!draft.from || !draft.to) return;
    onValueChange(customTimeRange(draft.from, draft.to));
    setOpen(false);
  };

  return (
    <div className="flex flex-col gap-2">
      <Label className="text-xs text-muted-foreground">{label}</Label>
      <Popover open={open} onOpenChange={handleOpenChange}>
        <PopoverTrigger asChild>
          <Button
            type="button"
            variant="outline"
            className="w-full justify-start gap-2 px-2.5 font-normal"
            aria-label={label}
          >
            <CalendarRange className="size-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0 flex-1 truncate text-left">{timeRangeLabel(value)}</span>
            <ChevronDown className={cn("size-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-180")} />
          </Button>
        </PopoverTrigger>
        <PopoverContent
          align="end"
          className="max-h-[calc(100vh-2rem)] w-[min(46rem,calc(100vw-2rem))] overflow-y-auto p-0"
          role="dialog"
          aria-label="Choose time range"
        >
          <div className="flex flex-col md:flex-row">
            <div className="space-y-1 p-3 md:w-44 md:shrink-0">
              <p className="px-2 pb-1 text-xs font-medium text-muted-foreground">Quick ranges</p>
              {PRESETS.map((preset) => {
                const selected = value === preset.value;
                return (
                  <Button
                    key={preset.value}
                    type="button"
                    variant={selected ? "secondary" : "ghost"}
                    className="h-auto w-full justify-start px-2 py-2 text-left"
                    onClick={() => applyPreset(preset.value)}
                  >
                    <span className="min-w-0 flex-1">
                      <span className="block font-medium">{preset.label}</span>
                      <span className="block text-xs font-normal text-muted-foreground">{preset.description}</span>
                    </span>
                    {selected ? <Check className="size-4 text-primary" aria-hidden="true" /> : null}
                  </Button>
                );
              })}
            </div>
            <Separator className="md:hidden" />
            <Separator orientation="vertical" className="hidden self-stretch md:block" />
            <div className="min-w-0 flex-1">
              <div className="px-4 pt-4">
                <p className="text-sm font-medium">Custom range</p>
                <p className="text-xs text-muted-foreground">Select a start and end date.</p>
              </div>
              <Calendar
                mode="range"
                selected={draft}
                onSelect={(nextRange) => setDraft(nextRange ?? { from: undefined, to: undefined })}
                defaultMonth={draft.from}
                numberOfMonths={2}
                showOutsideDays={false}
                disabled={{ after: today }}
                resetOnSelect
                className="mx-auto"
              />
              <Separator />
              <div className="flex flex-col gap-3 p-3 sm:flex-row sm:items-center sm:justify-between">
                <p className="text-sm text-muted-foreground">
                  {draft.from && draft.to
                    ? `${format(draft.from, "MMM d, yyyy")} – ${format(draft.to, "MMM d, yyyy")}`
                    : draft.from
                      ? `${format(draft.from, "MMM d, yyyy")} – Select an end date`
                      : "Select a start date"}
                </p>
                <div className="flex justify-end gap-2">
                  <Button type="button" variant="ghost" size="sm" onClick={() => setOpen(false)}>
                    Cancel
                  </Button>
                  <DisabledTooltip
                    disabled={rangeIncomplete}
                    content={draft.from ? "Select an end date to apply this range." : "Select a start and end date to apply this range."}
                  >
                    <Button type="button" size="sm" disabled={rangeIncomplete} onClick={applyCustomRange}>
                      Apply range
                    </Button>
                  </DisabledTooltip>
                </div>
              </div>
            </div>
          </div>
        </PopoverContent>
      </Popover>
    </div>
  );
}
