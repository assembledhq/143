import {
  addDays,
  addMonths,
  addWeeks,
  endOfDay,
  endOfMonth,
  endOfWeek,
  startOfMonth,
  startOfWeek,
  subMonths,
  subWeeks,
} from "date-fns";

export const ROLLING_TIME_RANGE_VALUES = ["7d", "30d", "90d"] as const;

export const PRESET_TIME_RANGE_VALUES = [
  "this_week",
  "last_week",
  "last_2_weeks",
  "this_month",
  "last_month",
  ...ROLLING_TIME_RANGE_VALUES,
  "all",
] as const;

export type PresetTimeRange = (typeof PRESET_TIME_RANGE_VALUES)[number];
export type RollingTimeRange = (typeof ROLLING_TIME_RANGE_VALUES)[number];
export type CustomTimeRange = `custom:${string}:${string}`;
export type TimeRangeFilter = PresetTimeRange | CustomTimeRange;

export const DEFAULT_TIME_RANGE = "30d" satisfies TimeRangeFilter;

const CUSTOM_TIME_RANGE_PATTERN = /^custom:(\d{4}-\d{2}-\d{2}):(\d{4}-\d{2}-\d{2})$/;

function parseLocalDay(value: string): Date | null {
  const [year, month, day] = value.split("-").map(Number);
  if (!year || !month || !day) return null;

  const date = new Date(year, month - 1, day);
  if (
    date.getFullYear() !== year
    || date.getMonth() !== month - 1
    || date.getDate() !== day
  ) return null;
  return date;
}

export function parseTimeRange(value: string): TimeRangeFilter | null {
  if ((PRESET_TIME_RANGE_VALUES as readonly string[]).includes(value)) {
    return value as PresetTimeRange;
  }

  const match = CUSTOM_TIME_RANGE_PATTERN.exec(value);
  if (!match) return null;
  const from = match[1];
  const to = match[2];
  if (!from || !to || !parseLocalDay(from) || !parseLocalDay(to) || from > to) return null;
  return value as CustomTimeRange;
}

export function customTimeRange(from: Date, to: Date): CustomTimeRange {
  const formatDay = (date: Date) => [
    date.getFullYear(),
    String(date.getMonth() + 1).padStart(2, "0"),
    String(date.getDate()).padStart(2, "0"),
  ].join("-");
  return `custom:${formatDay(from)}:${formatDay(to)}`;
}

export function customTimeRangeDates(range: TimeRangeFilter): { from: Date; to: Date } | null {
  const match = CUSTOM_TIME_RANGE_PATTERN.exec(range);
  if (!match) return null;
  const from = match[1] ? parseLocalDay(match[1]) : null;
  const to = match[2] ? parseLocalDay(match[2]) : null;
  return from && to ? { from, to } : null;
}

export function isRollingTimeRange(range: TimeRangeFilter): range is RollingTimeRange {
  return (ROLLING_TIME_RANGE_VALUES as readonly string[]).includes(range);
}

export function timeRangeRefreshDelayMs(
  range: TimeRangeFilter,
  anchor: Date,
  rollingRefreshMs: number,
): number | null {
  if (isRollingTimeRange(range)) return rollingRefreshMs;

  let nextRefresh: Date;
  switch (range) {
    case "this_week":
    case "this_month":
      nextRefresh = new Date(addDays(anchor, 1).setHours(0, 0, 0, 0));
      break;
    case "last_week":
    case "last_2_weeks":
      nextRefresh = startOfWeek(addWeeks(anchor, 1));
      break;
    case "last_month":
      nextRefresh = startOfMonth(addMonths(anchor, 1));
      break;
    default:
      return null;
  }

  return Math.max(1, nextRefresh.getTime() - anchor.getTime());
}

export function timeRangeBounds(
  range: TimeRangeFilter,
  anchor: Date,
): { created_after?: string; created_before?: string } {
  if (range === "all") return {};

  const custom = customTimeRangeDates(range);
  if (custom) {
    custom.from.setHours(0, 0, 0, 0);
    custom.to.setHours(23, 59, 59, 999);
    return {
      created_after: custom.from.toISOString(),
      created_before: custom.to.toISOString(),
    };
  }

  const calendarRange = calendarTimeRangeDates(range, anchor);
  if (calendarRange) {
    return {
      created_after: calendarRange.from.toISOString(),
      created_before: calendarRange.to.toISOString(),
    };
  }

  const days = Number.parseInt(range, 10);
  return {
    created_after: new Date(anchor.getTime() - days * 24 * 60 * 60 * 1000).toISOString(),
  };
}

function calendarTimeRangeDates(
  range: TimeRangeFilter,
  anchor: Date,
): { from: Date; to: Date } | null {
  switch (range) {
    case "this_week":
      return { from: startOfWeek(anchor), to: endOfDay(anchor) };
    case "last_week": {
      const previousWeek = subWeeks(anchor, 1);
      return { from: startOfWeek(previousWeek), to: endOfWeek(previousWeek) };
    }
    case "last_2_weeks":
      return { from: startOfWeek(subWeeks(anchor, 2)), to: endOfWeek(subWeeks(anchor, 1)) };
    case "this_month":
      return { from: startOfMonth(anchor), to: endOfDay(anchor) };
    case "last_month": {
      const previousMonth = subMonths(anchor, 1);
      return { from: startOfMonth(previousMonth), to: endOfMonth(previousMonth) };
    }
    default:
      return null;
  }
}
