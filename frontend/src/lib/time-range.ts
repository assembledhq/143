export const PRESET_TIME_RANGE_VALUES = ["7d", "30d", "90d", "all"] as const;

export type PresetTimeRange = (typeof PRESET_TIME_RANGE_VALUES)[number];
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

export function isRollingTimeRange(range: TimeRangeFilter): range is Exclude<PresetTimeRange, "all"> {
  return range !== "all" && !range.startsWith("custom:");
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

  const days = Number.parseInt(range, 10);
  return {
    created_after: new Date(anchor.getTime() - days * 24 * 60 * 60 * 1000).toISOString(),
  };
}
