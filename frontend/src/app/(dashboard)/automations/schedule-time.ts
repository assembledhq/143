import type { Automation } from "@/lib/types";

// Shared schedule-time helpers for the automation new/edit pages. The backend
// stores interval_run_at as a "HH:MM" string aligned to 5 minutes (see
// migration 88 chk_automations_interval_run_at_format). Keeping the option
// generation here instead of inlining per-page keeps the two dropdowns in sync
// — the UI only shows values the server will accept.

export const RUN_AT_MINUTE_STEP = 5;

// Always emit hours 00..23 and minutes 00, 05, ..., 55 so the selection values
// mirror the stored format exactly.
export const hourOptions: string[] = Array.from({ length: 24 }, (_, h) =>
  h.toString().padStart(2, "0"),
);

export const minuteOptions: string[] = Array.from(
  { length: 60 / RUN_AT_MINUTE_STEP },
  (_, i) => (i * RUN_AT_MINUTE_STEP).toString().padStart(2, "0"),
);

// browserTimezone returns the viewer's IANA zone, or "UTC" if the browser
// can't resolve one (older Safari/headless). Resolving lazily keeps SSR safe
// — pages should call this inside a useState initializer.
export function browserTimezone(): string {
  try {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    return tz || "UTC";
  } catch {
    return "UTC";
  }
}

// Split a "HH:MM" run-at string into its hour and minute parts. Minutes that
// don't land on a 5-minute tick (shouldn't happen for server-sourced values,
// but harden against hand-edited rows) snap down to the previous tick.
// Invalid inputs warn once so data corruption is visible during debugging
// instead of silently resetting the form.
export function splitRunAt(value: string): { hour: string; minute: string } {
  const match = /^(\d{2}):(\d{2})$/.exec(value);
  if (!match) {
    if (typeof console !== "undefined") {
      console.warn(
        `[automations] interval_run_at %o is not in HH:MM format; defaulting to 09:00`,
        value,
      );
    }
    return { hour: "09", minute: "00" };
  }
  const hour = match[1];
  const minuteNum = parseInt(match[2], 10);
  const snapped = Number.isNaN(minuteNum)
    ? 0
    : minuteNum - (minuteNum % RUN_AT_MINUTE_STEP);
  return { hour, minute: snapped.toString().padStart(2, "0") };
}

// Format an "HH:MM" run-at for display alongside a timezone, e.g.
// "9:00 AM (America/New_York)". Uses the browser's locale to honor 12/24hr
// preferences.
export function formatRunAtWithTimezone(runAt: string, timezone: string): string {
  const { hour, minute } = splitRunAt(runAt);
  const formatted = new Date(
    2000,
    0,
    1,
    parseInt(hour, 10),
    parseInt(minute, 10),
  ).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  return `${formatted} (${timezone})`;
}

type AutomationScheduleFields = Pick<
  Automation,
  | "schedule_type"
  | "interval_value"
  | "interval_unit"
  | "interval_run_at"
  | "cron_expression"
  | "timezone"
>;

function intervalLabel(value: number, unit: NonNullable<Automation["interval_unit"]>): string {
  if (unit === "hours" && value === 24) return "Daily";
  if (unit === "days" && value === 1) return "Daily";
  if (value === 1) return `Every ${unit.replace(/s$/, "")}`;
  return `Every ${value} ${unit}`;
}

function shouldShowRunAt(value: number, unit: NonNullable<Automation["interval_unit"]>): boolean {
  return unit !== "hours" || value >= 24;
}

export function formatAutomationSchedule(schedule: AutomationScheduleFields): string {
  if (schedule.schedule_type === "none") {
    return "No schedule";
  }

  const timezone = schedule.timezone || "UTC";
  if (schedule.schedule_type === "cron" && schedule.cron_expression) {
    return `cron: ${schedule.cron_expression} (${timezone})`;
  }

  const value = schedule.interval_value ?? 1;
  const unit = schedule.interval_unit ?? "days";
  const label = intervalLabel(value, unit);
  if (!schedule.interval_run_at || !shouldShowRunAt(value, unit)) {
    return label;
  }

  return `${label} at ${formatRunAtWithTimezone(schedule.interval_run_at, timezone)}`;
}
