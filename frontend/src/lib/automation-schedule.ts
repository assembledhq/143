import type { Automation } from "@/lib/types";

export const weekdays = [
  "monday",
  "tuesday",
  "wednesday",
  "thursday",
  "friday",
  "saturday",
  "sunday",
] as const;

export type Weekday = (typeof weekdays)[number];

export const intervalUnits = ["hours", "days", "weeks"] as const;

export type IntervalUnit = (typeof intervalUnits)[number];

type CalendarDraft = { time: string; timezone: string };

export type ScheduleDraft =
  | (CalendarDraft & { frequency: "daily" })
  | (CalendarDraft & { frequency: "weekdays" })
  | (CalendarDraft & { frequency: "weekly"; weekdays: Weekday[] })
  | (CalendarDraft & { frequency: "monthly"; dayOfMonth: number })
  | {
      frequency: "interval";
      value: number;
      unit: IntervalUnit;
      time?: string;
      timezone: string;
    }
  | {
      frequency: "advanced";
      cronExpression: string;
      timezone: string;
    };

export type AutomationSchedulePayload =
  | { schedule_type: "none"; timezone: string }
  | {
      schedule_type: "cron";
      cron_expression: string;
      timezone: string;
    }
  | {
      schedule_type: "interval";
      interval_value: number;
      interval_unit: IntervalUnit;
      // Always sent — "" clears a stored run-at. See scheduleDraftToAPI.
      interval_run_at: string;
      timezone: string;
    };

export const defaultRunAt = "09:00";

// Sub-day hourly intervals are elapsed durations, so they carry no wall clock.
// Everything else (hours >= 24, days, weeks) anchors to a run-at time.
export function intervalHasRunAt(value: number, unit: IntervalUnit): boolean {
  return unit !== "hours" || value >= 24;
}

const weekdayCron: Record<Weekday, number> = {
  monday: 1,
  tuesday: 2,
  wednesday: 3,
  thursday: 4,
  friday: 5,
  saturday: 6,
  sunday: 0,
};

const cronWeekday: Record<string, Weekday> = {
  "0": "sunday",
  "1": "monday",
  "2": "tuesday",
  "3": "wednesday",
  "4": "thursday",
  "5": "friday",
  "6": "saturday",
  "7": "sunday",
};

// New weekly schedules default to the weekday it currently is in the selected
// zone, so "Every week on ..." starts on a day the user recognises.
export function currentWeekday(timezone: string, now = new Date()): Weekday {
  try {
    const name = new Intl.DateTimeFormat("en-US", {
      timeZone: timezone || "UTC",
      weekday: "long",
    })
      .format(now)
      .toLowerCase();
    if (weekdays.includes(name as Weekday)) return name as Weekday;
  } catch {
    // The API validates timezone identifiers. UTC fallback keeps a new draft usable.
  }
  return weekdays[(now.getUTCDay() + 6) % 7];
}

export function defaultScheduleDraft(
  timezone: string,
  now = new Date(),
): ScheduleDraft {
  return {
    frequency: "weekly",
    weekdays: [currentWeekday(timezone, now)],
    time: defaultRunAt,
    timezone,
  };
}

// `fallbackTimezone` is only consulted when there is no draft: removing a
// schedule must not silently rewrite the automation's stored zone to UTC, so
// callers pass the zone the automation (or the form) is already using.
export function scheduleDraftToAPI(
  draft: ScheduleDraft | null,
  fallbackTimezone = "UTC",
): AutomationSchedulePayload {
  if (!draft) {
    return { schedule_type: "none", timezone: fallbackTimezone || "UTC" };
  }
  switch (draft.frequency) {
    case "daily":
      return cronPayload(draft.time, "*", "*", draft.timezone);
    case "weekdays":
      return cronPayload(draft.time, "*", "1-5", draft.timezone);
    case "weekly": {
      const days = draft.weekdays
        .map((day) => weekdayCron[day])
        .sort((a, b) => a - b)
        .join(",");
      return cronPayload(draft.time, "*", days, draft.timezone);
    }
    case "monthly":
      return cronPayload(draft.time, String(draft.dayOfMonth), "*", draft.timezone);
    case "interval":
      return {
        schedule_type: "interval",
        interval_value: draft.value,
        interval_unit: draft.unit,
        // Always explicit. PATCH treats an absent interval_run_at as
        // "unchanged", so omitting it when the draft is unanchored would leave
        // a previously stored run-at in place — the row would keep firing
        // against a wall clock the editor no longer shows, and against the
        // preview the user just approved. "" is the documented clear value.
        interval_run_at: draft.time ?? "",
        timezone: draft.timezone,
      };
    case "advanced":
      return {
        schedule_type: "cron",
        cron_expression: draft.cronExpression,
        timezone: draft.timezone,
      };
  }
}

function cronPayload(
  time: string,
  dayOfMonth: string,
  dayOfWeek: string,
  timezone: string,
): AutomationSchedulePayload {
  const [hour, minute] = time.split(":");
  return {
    schedule_type: "cron",
    cron_expression: `${Number(minute)} ${Number(hour)} ${dayOfMonth} * ${dayOfWeek}`,
    timezone,
  };
}

export function automationToScheduleDraft(
  automation: Pick<
    Automation,
    | "schedule_type"
    | "interval_value"
    | "interval_unit"
    | "interval_run_at"
    | "cron_expression"
    | "timezone"
  >,
): ScheduleDraft | null {
  const timezone = automation.timezone || "UTC";
  if (automation.schedule_type === "none") return null;
  if (
    automation.schedule_type === "interval" ||
    (automation.schedule_type as string | undefined) === undefined
  ) {
    const value = automation.interval_value ?? 1;
    const unit = automation.interval_unit ?? "days";
    return {
      frequency: "interval",
      value,
      unit,
      // Faithful to the row: a schedule stored without interval_run_at has no
      // anchor time, and summaries must not invent one. The editor supplies a
      // default for the run-at control separately.
      ...(automation.interval_run_at && intervalHasRunAt(value, unit)
        ? { time: automation.interval_run_at }
        : {}),
      timezone,
    };
  }
  const expression = automation.cron_expression ?? "";
  return parseFriendlyCron(expression, timezone) ?? {
    frequency: "advanced",
    cronExpression: expression,
    timezone,
  };
}

export function parseFriendlyCron(
  expression: string,
  timezone: string,
): ScheduleDraft | null {
  const match = /^(\d{1,2})\s+(\d{1,2})\s+(\*|[1-9]|[12]\d|3[01])\s+\*\s+(\*|1-5|[0-7](?:,[0-7])*)$/.exec(
    expression.trim(),
  );
  if (!match) return null;
  const minute = Number(match[1]);
  const hour = Number(match[2]);
  if (hour > 23 || minute > 59 || minute % 5 !== 0) return null;
  const time = `${String(hour).padStart(2, "0")}:${String(minute).padStart(2, "0")}`;
  const dayOfMonth = match[3];
  const dayOfWeek = match[4];
  if (dayOfMonth !== "*" && dayOfWeek === "*") {
    return {
      frequency: "monthly",
      dayOfMonth: Number(dayOfMonth),
      time,
      timezone,
    };
  }
  if (dayOfMonth !== "*") return null;
  if (dayOfWeek === "*") return { frequency: "daily", time, timezone };
  if (dayOfWeek === "1-5") return { frequency: "weekdays", time, timezone };
  const parsedDays = dayOfWeek.split(",").map((day) => cronWeekday[day]);
  if (parsedDays.some((day) => !day)) return null;
  return {
    frequency: "weekly",
    weekdays: sortWeekdays([...new Set(parsedDays)]),
    time,
    timezone,
  };
}

export function validateScheduleDraft(draft: ScheduleDraft): string | null {
  if (!draft.timezone.trim()) return "Select a time zone.";
  if (draft.frequency === "weekly" && draft.weekdays.length === 0) {
    return "Select at least one day.";
  }
  if (
    draft.frequency === "monthly" &&
    (!Number.isInteger(draft.dayOfMonth) ||
      draft.dayOfMonth < 1 ||
      draft.dayOfMonth > 31)
  ) {
    return "Day of month must be between 1 and 31.";
  }
  if (
    draft.frequency === "interval" &&
    (!Number.isInteger(draft.value) || draft.value < 1 || draft.value > 365)
  ) {
    return "Interval must be between 1 and 365.";
  }
  if (
    "time" in draft &&
    draft.time !== undefined &&
    !isValidTime(draft.time)
  ) {
    return "Select a valid time in five-minute increments.";
  }
  if (draft.frequency === "advanced" && !draft.cronExpression.trim()) {
    return "Enter a cron expression.";
  }
  return null;
}

// Field-by-field rather than a JSON.stringify comparison: drafts are rebuilt by
// spreads all over the editor, so key order is not stable enough to compare on.
export function sameScheduleDraft(
  a: ScheduleDraft | null,
  b: ScheduleDraft | null,
): boolean {
  if (a === null || b === null) return a === b;
  if (a.frequency !== b.frequency || a.timezone !== b.timezone) return false;
  const timeA = "time" in a ? a.time : undefined;
  const timeB = "time" in b ? b.time : undefined;
  if (timeA !== timeB) return false;
  if (a.frequency === "weekly" && b.frequency === "weekly") {
    const left = sortWeekdays(a.weekdays);
    const right = sortWeekdays(b.weekdays);
    return (
      left.length === right.length && left.every((day, i) => day === right[i])
    );
  }
  if (a.frequency === "monthly" && b.frequency === "monthly") {
    return a.dayOfMonth === b.dayOfMonth;
  }
  if (a.frequency === "interval" && b.frequency === "interval") {
    return a.value === b.value && a.unit === b.unit;
  }
  if (a.frequency === "advanced" && b.frequency === "advanced") {
    return a.cronExpression === b.cronExpression;
  }
  return true;
}

// Guards a draft rehydrated from session storage. It checks every field the
// variant needs, not just the discriminant: a truncated or hand-edited draft
// that only carried `frequency` and `timezone` used to pass and then throw in
// validateScheduleDraft/formatScheduleSentence, taking the page down during
// hydration — which is exactly what this guard exists to prevent.
export function isScheduleDraft(value: unknown): value is ScheduleDraft {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Record<string, unknown>;
  if (typeof candidate.timezone !== "string") return false;
  const hasTime = typeof candidate.time === "string";
  switch (candidate.frequency) {
    case "daily":
    case "weekdays":
      return hasTime;
    case "weekly":
      return (
        hasTime &&
        Array.isArray(candidate.weekdays) &&
        candidate.weekdays.every((day) => weekdays.includes(day as Weekday))
      );
    case "monthly":
      return hasTime && typeof candidate.dayOfMonth === "number";
    case "interval":
      return (
        typeof candidate.value === "number" &&
        intervalUnits.includes(candidate.unit as IntervalUnit) &&
        (candidate.time === undefined || hasTime)
      );
    case "advanced":
      return typeof candidate.cronExpression === "string";
    default:
      return false;
  }
}

export function formatScheduleSentence(draft: ScheduleDraft): string {
  const at = "time" in draft && draft.time ? ` at ${formatTime(draft.time)}` : "";
  switch (draft.frequency) {
    case "daily":
      return `Every day${at}`;
    case "weekdays":
      return `Every weekday${at}`;
    case "weekly":
      return `Every week on ${formatWeekdays(draft.weekdays)}${at}`;
    case "monthly":
      return `Every month on the ${ordinal(draft.dayOfMonth)}${at}`;
    case "interval": {
      const unit = draft.value === 1 ? draft.unit.slice(0, -1) : draft.unit;
      return `Every ${draft.value === 1 ? "" : `${draft.value} `}${unit}${at}`;
    }
    case "advanced":
      return `Custom schedule: ${draft.cronExpression}`;
  }
}

export function formatAutomationSchedule(
  automation: Parameters<typeof automationToScheduleDraft>[0],
): string {
  const draft = automationToScheduleDraft(automation);
  return draft ? formatScheduleSentence(draft) : "No schedule";
}

// The sentence renders a wall clock in the automation's zone using the reader's
// locale, so the IANA zone has to travel alongside it. Returns null when there
// is nothing to qualify (no schedule, or no stored zone).
export function automationScheduleTimezone(
  automation: Parameters<typeof automationToScheduleDraft>[0],
): string | null {
  if (automation.schedule_type === "none") return null;
  return automation.timezone || null;
}

// Intl throws a RangeError on an unparseable IANA identifier. A stale or
// hand-edited row must not take down the page that renders it — but silently
// re-rendering in the reader's own zone would show a confident, wrong local
// time, so the fallback surfaces UTC and names the zone it could not use.
export function formatNextRun(value: string, timezone: string): string {
  const options: Intl.DateTimeFormatOptions = {
    weekday: "long",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    timeZoneName: "short",
  };
  const date = new Date(value);
  try {
    return new Intl.DateTimeFormat(undefined, {
      ...options,
      timeZone: timezone,
    }).format(date);
  } catch {
    const utc = new Intl.DateTimeFormat(undefined, {
      ...options,
      timeZone: "UTC",
    }).format(date);
    return `${utc} (unknown time zone ${timezone})`;
  }
}

export function formatWeekdays(selected: Weekday[]): string {
  const names = sortWeekdays(selected).map(capitalize);
  if (names.length === 0) return "no days";
  if (names.length === 1) return names[0];
  if (names.length === 2) return `${names[0]} and ${names[1]}`;
  return `${names.slice(0, -1).join(", ")}, and ${names.at(-1)}`;
}

export function sortWeekdays(selected: Weekday[]): Weekday[] {
  return [...selected].sort(
    (a, b) => weekdays.indexOf(a) - weekdays.indexOf(b),
  );
}

export function formatTime(value: string): string {
  const [hour, minute] = value.split(":").map(Number);
  return new Date(2000, 0, 1, hour, minute).toLocaleTimeString(undefined, {
    hour: "numeric",
    minute: "2-digit",
  });
}

function isValidTime(value: string): boolean {
  const match = /^([01]\d|2[0-3]):([0-5]\d)$/.exec(value);
  return Boolean(match && Number(match[2]) % 5 === 0);
}

function ordinal(value: number): string {
  const mod100 = value % 100;
  if (mod100 >= 11 && mod100 <= 13) return `${value}th`;
  return `${value}${({ 1: "st", 2: "nd", 3: "rd" } as Record<number, string>)[value % 10] ?? "th"}`;
}

function capitalize(value: string): string {
  return `${value.charAt(0).toUpperCase()}${value.slice(1)}`;
}
