"use client";

import { useMemo, useState } from "react";
import { format } from "date-fns";
import { CalendarRange, Check, ChevronDown } from "lucide-react";
import type { DateRange } from "react-day-picker";

import { Button } from "@/components/ui/button";
import { Calendar } from "@/components/ui/calendar";
import { ControlTrigger } from "@/components/ui/control-trigger";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Separator } from "@/components/ui/separator";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { useMediaQuery } from "@/hooks/use-media-query";
import {
  customTimeRange,
  customTimeRangeDates,
  timeRangeDisplayDates,
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

// Deliberately two queries instead of one negated query: both use the hook's
// false server snapshot, so SSR and the first hydration render fall back to
// the desktop popover rather than a full-screen sheet.
const MOBILE_QUERY = "(max-width: 767px)";
const TWO_MONTH_QUERY = "(min-width: 768px)";

function defaultDraft(value: TimeRangeFilter): DateRange {
  return timeRangeDisplayDates(value, new Date()) ?? { from: undefined, to: undefined };
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
  const isMobile = useMediaQuery(MOBILE_QUERY);
  const showTwoMonths = useMediaQuery(TWO_MONTH_QUERY);
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
    setDraft(defaultDraft(preset));
    onValueChange(preset);
    setOpen(false);
  };

  const rangeIncomplete = !draft.from || !draft.to;

  const applyCustomRange = () => {
    if (!draft.from || !draft.to) return;
    onValueChange(customTimeRange(draft.from, draft.to));
    setOpen(false);
  };

  const trigger = (
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
  );

  const rangeActions = (
    <div className="flex flex-col gap-2 p-2 sm:flex-row sm:items-center sm:justify-between">
      <p className="text-xs text-muted-foreground">
        {draft.from && draft.to
          ? `${format(draft.from, "MMM d, yyyy")} – ${format(draft.to, "MMM d, yyyy")}`
          : draft.from
            ? `${format(draft.from, "MMM d, yyyy")} – Select an end date`
            : "Select a start date"}
      </p>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={() => handleOpenChange(false)}>
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
  );

  const pickerBody = (
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
        {/* 40px cells on phones — the full-screen sheet has the room, and it
            matches Button's mobile height. 44px (the control minimum in
            AGENTS.md) would make the grid 324px and overflow a 320px viewport.
            The compact rhythm starts at `sm` like every other control. */}
        <Calendar
          mode="range"
          selected={draft}
          onSelect={(nextRange) => setDraft(nextRange ?? { from: undefined, to: undefined })}
          defaultMonth={draft.from}
          numberOfMonths={showTwoMonths ? 2 : 1}
          showOutsideDays={false}
          disabled={{ after: today }}
          resetOnSelect
          className="mx-auto p-2 [--cell-size:--spacing(10)] sm:[--cell-size:--spacing(7)] [&_.rdp-month]:gap-2 [&_.rdp-months]:gap-3 [&_.rdp-week]:mt-1"
        />
        {/* On mobile the actions are pinned below the scroll area instead. */}
        {isMobile ? null : (
          <>
            <Separator />
            {rangeActions}
          </>
        )}
      </div>
    </div>
  );

  return (
    <div className="flex flex-col gap-2">
      <Label className="text-xs text-muted-foreground">{label}</Label>
      {isMobile ? (
        <Sheet open={open} onOpenChange={handleOpenChange}>
          <SheetTrigger asChild>{trigger}</SheetTrigger>
          <SheetContent
            side="bottom"
            className="inset-0 flex h-dvh max-h-dvh max-w-none flex-col gap-0 overflow-hidden rounded-none border-0 p-0"
          >
            <SheetHeader className="shrink-0 border-b border-border px-4 py-3 pr-12">
              <SheetTitle>Choose time range</SheetTitle>
              <SheetDescription className="sr-only">
                Choose a preset or select a custom date range.
              </SheetDescription>
            </SheetHeader>
            <div
              data-slot="time-range-picker-body"
              className="min-h-0 flex-1 overflow-y-auto overscroll-contain"
            >
              {pickerBody}
            </div>
            {/* The safe-area half of this padding only engages if the app ever
                sets `viewportFit: "cover"`; today it resolves to the 0.5rem. */}
            <div className="shrink-0 border-t border-border bg-background pb-[max(0.5rem,env(safe-area-inset-bottom))]">
              {rangeActions}
            </div>
          </SheetContent>
        </Sheet>
      ) : (
        <Popover open={open} onOpenChange={handleOpenChange}>
          <PopoverTrigger asChild>{trigger}</PopoverTrigger>
          <PopoverContent
            align="end"
            className="max-h-[calc(100vh-2rem)] w-[min(40rem,calc(100vw-2rem))] overflow-y-auto p-0"
            role="dialog"
            aria-label="Choose time range"
          >
            {pickerBody}
          </PopoverContent>
        </Popover>
      )}
    </div>
  );
}
