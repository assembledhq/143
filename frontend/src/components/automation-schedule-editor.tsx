"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronDown, Loader2, Plus, Trash2 } from "lucide-react";
import { TimezonePicker } from "@/app/(dashboard)/automations/timezone-picker";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ApiError, api } from "@/lib/api";
import {
  currentWeekday,
  defaultRunAt,
  defaultScheduleDraft,
  formatNextRun,
  formatWeekdays,
  intervalHasRunAt,
  scheduleDraftToAPI,
  validateScheduleDraft,
  weekdays,
  type IntervalUnit,
  type ScheduleDraft,
  type Weekday,
} from "@/lib/automation-schedule";

// 288 five-minute ticks, localized once at module scope rather than on every
// render of the open dropdown.
const timeOptions = Array.from({ length: 24 * 12 }, (_, index) => {
  const hour = Math.floor(index / 12);
  const minute = (index % 12) * 5;
  const value = `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
  return {
    value,
    label: new Date(2000, 0, 1, hour, minute).toLocaleTimeString(undefined, {
      hour: "numeric",
      minute: "2-digit",
    }),
  };
});

type Props = {
  value: ScheduleDraft | null;
  onChange: (value: ScheduleDraft | null) => void;
  detectedTimezone: string;
  disabled?: boolean;
  /**
   * `valid` answers "can this be committed now?". `serverRejected` separates
   * the two very different reasons it can be false: the preview has not
   * settled yet (keep waiting) versus the API has refused this exact draft
   * (never send it). A caller that autosaves on unmount needs that
   * distinction — treating "not settled" as "refused" drops the edit, and
   * treating "refused" as "not settled" guarantees a failed write.
   */
  onValidityChange?: (
    valid: boolean,
    detail: { serverRejected: boolean },
  ) => void;
};

export function AutomationScheduleEditor({
  value,
  onChange,
  detectedTimezone,
  disabled = false,
  onValidityChange,
}: Props) {
  const [debouncedValue, setDebouncedValue] = useState(value);
  useEffect(() => {
    const timeout = window.setTimeout(() => setDebouncedValue(value), 300);
    return () => window.clearTimeout(timeout);
  }, [value]);

  const clientError = value ? validateScheduleDraft(value) : null;
  const payload = useMemo(
    () => (debouncedValue ? scheduleDraftToAPI(debouncedValue) : null),
    [debouncedValue],
  );
  const preview = useQuery({
    queryKey: ["automation-schedule-preview", payload],
    queryFn: () => api.automations.previewSchedule(payload!),
    enabled: Boolean(payload && !validateScheduleDraft(debouncedValue!)),
    retry: false,
    staleTime: 0,
  });
  const isCurrent = value === debouncedValue;
  const previewValidationError =
    preview.error instanceof ApiError && preview.error.status === 400;
  const previewError =
    isCurrent && preview.error
      ? previewValidationError
        ? preview.error.message
        : "Could not preview this schedule. Try again."
      : null;
  // A rejected preview means the schedule itself is unusable, so it blocks
  // saving. A transport failure does not: the API validates the schedule again
  // on write, and an unreachable preview must not wedge the whole form (on the
  // detail page it would also block saving unrelated fields).
  const previewTransportError = Boolean(
    isCurrent && preview.error && !previewValidationError,
  );
  const valid =
    value === null ||
    (!clientError &&
      (previewTransportError || (isCurrent && preview.isSuccess)));

  // Scoped to `isCurrent` so a verdict about a superseded draft can't be read
  // as one about the draft on screen.
  const serverRejected = Boolean(isCurrent && previewValidationError);

  useEffect(
    () => onValidityChange?.(valid, { serverRejected }),
    [onValidityChange, serverRejected, valid],
  );

  if (!value) {
    return (
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={disabled}
        onClick={() =>
          onChange(defaultScheduleDraft(detectedTimezone || "UTC"))
        }
      >
        <Plus className="h-4 w-4" />
        Add schedule
      </Button>
    );
  }

  const update = (next: ScheduleDraft) => onChange(next);
  const timezone = value.timezone;
  const time = "time" in value ? value.time : undefined;
  // Interval rows saved without interval_run_at are genuinely unanchored, so
  // the control shows nothing rather than a default it does not have. Day and
  // week cadences still offer a one-click way to add one, so an existing
  // schedule's time is reachable without first changing the cadence.
  const unanchoredInterval =
    value.frequency === "interval" &&
    !value.time &&
    intervalHasRunAt(value.value, value.unit)
      ? value
      : null;

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm text-muted-foreground">Every</span>
        <Select
          value={value.frequency}
          disabled={disabled}
          onValueChange={(frequency) =>
            update(changeFrequency(value, frequency, detectedTimezone))
          }
        >
          <SelectTrigger className="h-9 w-auto min-w-28" aria-label="Schedule frequency">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="daily">day</SelectItem>
            <SelectItem value="weekdays">weekday</SelectItem>
            <SelectItem value="weekly">week</SelectItem>
            <SelectItem value="monthly">month</SelectItem>
            <SelectItem value="interval">interval</SelectItem>
            <SelectItem value="advanced">advanced schedule</SelectItem>
          </SelectContent>
        </Select>

        {value.frequency === "weekly" && (
          <>
            <span className="text-sm text-muted-foreground">on</span>
            <WeekdayPicker
              value={value.weekdays}
              disabled={disabled}
              onChange={(selected) => update({ ...value, weekdays: selected })}
            />
          </>
        )}

        {value.frequency === "monthly" && (
          <>
            <span className="text-sm text-muted-foreground">on the</span>
            <Select
              value={String(value.dayOfMonth)}
              disabled={disabled}
              onValueChange={(day) =>
                update({ ...value, dayOfMonth: Number(day) })
              }
            >
              <SelectTrigger className="h-9 w-20" aria-label="Day of month">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {Array.from({ length: 31 }, (_, index) => index + 1).map((day) => (
                  <SelectItem key={day} value={String(day)}>
                    {day}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </>
        )}

        {value.frequency === "interval" && (
          <>
            <IntervalValueInput
              value={value.value}
              disabled={disabled}
              onChange={(next) =>
                update(normalizeIntervalDraft({ ...value, value: next }))
              }
            />
            <Select
              value={value.unit}
              disabled={disabled}
              onValueChange={(unit: IntervalUnit) =>
                update(normalizeIntervalDraft({ ...value, unit }))
              }
            >
              <SelectTrigger className="h-9 w-24" aria-label="Interval unit">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="hours">hours</SelectItem>
                <SelectItem value="days">days</SelectItem>
                <SelectItem value="weeks">weeks</SelectItem>
              </SelectContent>
            </Select>
          </>
        )}

        {value.frequency === "advanced" ? (
          <Input
            value={value.cronExpression}
            disabled={disabled}
            aria-label="Cron expression"
            className="h-9 min-w-56 flex-1 font-mono"
            onChange={(event) =>
              update({ ...value, cronExpression: event.target.value })
            }
          />
        ) : time ? (
          <>
            <span className="text-sm text-muted-foreground">at</span>
            <Select
              value={time}
              disabled={disabled}
              onValueChange={(nextTime) =>
                update({ ...value, time: nextTime } as ScheduleDraft)
              }
            >
              <SelectTrigger className="h-9 w-28" aria-label="Run time">
                <SelectValue />
              </SelectTrigger>
              <SelectContent className="max-h-72">
                {timeOptions.map(({ value: option, label }) => (
                  <SelectItem key={option} value={option}>
                    {label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </>
        ) : unanchoredInterval ? (
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-9"
            disabled={disabled}
            onClick={() =>
              update({ ...unanchoredInterval, time: defaultRunAt })
            }
          >
            Set a run time
          </Button>
        ) : null}

        <TimezonePicker
          value={timezone}
          onChange={(nextTimezone) => update({ ...value, timezone: nextTimezone })}
          detected={detectedTimezone}
          className="max-w-60"
          ariaLabel="Time zone"
          disabled={disabled}
        />
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          disabled={disabled}
          aria-label="Remove schedule"
          title="Remove schedule"
          onClick={() => onChange(null)}
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      </div>

      {value.frequency === "monthly" && value.dayOfMonth >= 29 && (
        <p className="text-xs text-muted-foreground">
          Months without this date will be skipped.
        </p>
      )}
      {/* Announced via aria-live rather than aria-describedby: the wrapper is
          a plain div, so describedby on it would have had no effect, and a
          hardcoded id would collide if two editors ever shared a page. */}
      <div className="min-h-5 text-xs" aria-live="polite">
        {clientError ? (
          <span className="text-destructive">{clientError}</span>
        ) : !isCurrent || preview.isFetching ? (
          <span className="inline-flex items-center gap-1 text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
            Calculating next run…
          </span>
        ) : previewError ? (
          <span className="inline-flex flex-wrap items-center gap-2 text-destructive">
            {previewError}
            {!previewValidationError && (
              <Button
                type="button"
                variant="link"
                size="sm"
                className="h-auto p-0"
                onClick={() => preview.refetch()}
              >
                Retry
              </Button>
            )}
          </span>
        ) : preview.data?.data.next_run_at ? (
          <span className="text-muted-foreground">
            Next run: {formatNextRun(preview.data.data.next_run_at, timezone)}
          </span>
        ) : null}
      </div>
    </div>
  );
}

type IntervalDraft = Extract<ScheduleDraft, { frequency: "interval" }>;

// Sub-day hourly intervals are elapsed durations, so changing the cadence into
// that range drops any run-at time rather than sending one the control no
// longer shows. Cadences that do support a time keep whatever the draft had —
// an unanchored interval stays unanchored until the user picks a time.
function normalizeIntervalDraft(draft: IntervalDraft): IntervalDraft {
  if (intervalHasRunAt(draft.value, draft.unit)) return draft;
  return {
    frequency: "interval",
    value: draft.value,
    unit: draft.unit,
    timezone: draft.timezone,
  };
}

// The raw text is held locally so clearing the field shows an empty box rather
// than snapping to 0, and so partially typed values survive a keystroke. The
// draft still receives the numeric value on every edit, which keeps
// validateScheduleDraft (and therefore the save button) in sync.
function IntervalValueInput({
  value,
  disabled,
  onChange,
}: {
  value: number;
  disabled: boolean;
  onChange: (value: number) => void;
}) {
  const [text, setText] = useState(() => String(value));
  // Resync during render (not in an effect) when the draft's value changes
  // from the outside, e.g. applying a template. An edit the user just typed
  // already agrees numerically, so their raw text — "" or "007" — is kept.
  const [lastValue, setLastValue] = useState(value);
  if (value !== lastValue) {
    setLastValue(value);
    if (Number(text) !== value) setText(String(value));
  }

  return (
    <Input
      type="number"
      min={1}
      max={365}
      value={text}
      disabled={disabled}
      aria-label="Interval value"
      className="h-9 w-20"
      onChange={(event) => {
        setText(event.target.value);
        onChange(Number(event.target.value));
      }}
      onBlur={() => {
        const parsed = Math.trunc(Number(text));
        const normalized =
          text.trim() === "" || !Number.isFinite(parsed)
            ? 1
            : Math.min(365, Math.max(1, parsed));
        if (String(normalized) !== text) setText(String(normalized));
        // Only notify on a real change. An unconditional onChange would rebuild
        // the draft on every blur, restart the debounced preview and disable
        // Save just as the user reaches for it — clicking Save straight after
        // typing would blur, disable the button mid-click and do nothing.
        if (normalized !== value) onChange(normalized);
      }}
    />
  );
}

function WeekdayPicker({
  value,
  onChange,
  disabled,
}: {
  value: Weekday[];
  onChange: (value: Weekday[]) => void;
  disabled: boolean;
}) {
  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          disabled={disabled}
          aria-label={`Days of week: ${formatWeekdays(value)}`}
          className="h-9 max-w-64 justify-between font-normal"
        >
          <span className={value.length >= 3 ? "hidden sm:inline" : "truncate"}>
            {formatWeekdays(value)}
          </span>
          {value.length >= 3 && (
            <span className="sm:hidden">{value.length} days</span>
          )}
          <ChevronDown className="ml-2 h-3.5 w-3.5 shrink-0 opacity-60" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-56 p-2">
        <div className="space-y-1">
          {weekdays.map((day) => {
            const checked = value.includes(day);
            return (
              <Label
                key={day}
                className="flex min-h-8 cursor-pointer items-center gap-2 rounded-md px-2 hover:bg-muted/50"
              >
                <Checkbox
                  checked={checked}
                  onCheckedChange={(next) =>
                    onChange(
                      next === true
                        ? [...new Set([...value, day])]
                        : value.filter((item) => item !== day),
                    )
                  }
                />
                <span className="capitalize">{day}</span>
              </Label>
            );
          })}
        </div>
      </PopoverContent>
    </Popover>
  );
}

function changeFrequency(
  current: ScheduleDraft,
  frequency: string,
  fallbackTimezone: string,
): ScheduleDraft {
  const timezone = current.timezone || fallbackTimezone || "UTC";
  const time = "time" in current && current.time ? current.time : defaultRunAt;
  switch (frequency) {
    case "daily":
      return { frequency, time, timezone };
    case "weekdays":
      return { frequency, time, timezone };
    case "weekly":
      return {
        frequency,
        weekdays: [currentWeekday(timezone)],
        time,
        timezone,
      };
    case "monthly":
      return { frequency, dayOfMonth: 1, time, timezone };
    case "interval":
      return { frequency, value: 1, unit: "days", time, timezone };
    case "advanced": {
      // Seed Advanced from the schedule the user is leaving so switching does
      // not silently discard it.
      const payload = scheduleDraftToAPI(current);
      return {
        frequency,
        cronExpression:
          payload.schedule_type === "cron"
            ? payload.cron_expression
            : "0 9 * * *",
        timezone,
      };
    }
    default:
      return current;
  }
}
