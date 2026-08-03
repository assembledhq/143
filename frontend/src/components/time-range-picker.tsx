"use client";

import { useMemo, useState } from "react";
import { format, subDays } from "date-fns";
import { CalendarRange, Check, ChevronDown } from "lucide-react";
import type { DateRange } from "react-day-picker";

import { Button } from "@/components/ui/button";
import { Calendar } from "@/components/ui/calendar";
import { ControlTrigger } from "@/components/ui/control-trigger";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { useMediaQuery } from "@/hooks/use-media-query";
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

const PRESET_GROUPS: Array<{
  label: string;
  presets: Array<{ value: PresetTimeRange; label: string }>;
}> = [
  {
    label: "Calendar ranges",
    presets: [
      { value: "this_week", label: "This week" },
      { value: "last_week", label: "Last week" },
      { value: "last_2_weeks", label: "Last 2 weeks" },
      { value: "this_month", label: "This month" },
      { value: "last_month", label: "Last month" },
    ],
  },
  {
    label: "Rolling ranges",
    presets: [
      { value: "7d", label: "Last 7 days" },
      { value: "30d", label: "Last 30 days" },
      { value: "90d", label: "Last 90 days" },
      { value: "all", label: "All time" },
    ],
  },
];

const PRESETS = PRESET_GROUPS.flatMap((group) => group.presets);

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
  const showTwoMonths = useMediaQuery("(min-width: 768px)");
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
          <ControlTrigger
            type="button"
            variant="outline"
            density="compact"
            className="w-full justify-start gap-2 px-2.5 font-normal"
            aria-label={label}
          >
            <CalendarRange className="size-4 shrink-0 text-muted-foreground" />
            <span className="min-w-0 flex-1 truncate text-left">{timeRangeLabel(value)}</span>
            <ChevronDown className={cn("size-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-180")} />
          </ControlTrigger>
        </PopoverTrigger>
        <PopoverContent
          align="end"
          className="max-h-[calc(100vh-2rem)] w-[min(40rem,calc(100vw-2rem))] overflow-y-auto p-0"
          role="dialog"
          aria-label="Choose time range"
        >
          <div className="flex flex-col md:flex-row">
            <div className="space-y-3 p-2 md:w-36 md:shrink-0">
              {PRESET_GROUPS.map((group) => (
                <div key={group.label} className="space-y-0.5">
                  <p className="px-2 pb-0.5 text-xs font-medium text-muted-foreground">{group.label}</p>
                  {group.presets.map((preset) => {
                    const selected = value === preset.value;
                    return (
                      <Button
                        key={preset.value}
                        type="button"
                        size="sm"
                        variant={selected ? "secondary" : "ghost"}
                        className="w-full justify-start px-2"
                        onClick={() => applyPreset(preset.value)}
                      >
                        <span className="min-w-0 flex-1 truncate text-left">{preset.label}</span>
                        {selected ? <Check className="size-3.5 text-primary" aria-hidden="true" /> : null}
                      </Button>
                    );
                  })}
                </div>
              ))}
            </div>
            <Separator className="md:hidden" />
            <Separator orientation="vertical" className="hidden self-stretch md:block" />
            <div className="min-w-0 flex-1">
              <div className="px-3 pt-3">
                <p className="text-sm font-medium">Custom range</p>
              </div>
              <Calendar
                mode="range"
                selected={draft}
                onSelect={(nextRange) => setDraft(nextRange ?? { from: undefined, to: undefined })}
                defaultMonth={draft.from}
                numberOfMonths={showTwoMonths ? 2 : 1}
                showOutsideDays={false}
                disabled={{ after: today }}
                resetOnSelect
                className="mx-auto p-2 [--cell-size:--spacing(8)] sm:[--cell-size:--spacing(7)] [&_.rdp-month]:gap-2 [&_.rdp-months]:gap-3 [&_.rdp-week]:mt-1"
              />
              <Separator />
              <div className="flex flex-col gap-2 p-2 sm:flex-row sm:items-center sm:justify-between">
                <p className="text-xs text-muted-foreground">
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
