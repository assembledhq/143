import { describe, expect, it } from "vitest";
import {
  automationToScheduleDraft,
  formatScheduleSentence,
  intervalHasRunAt,
  isScheduleDraft,
  parseFriendlyCron,
  sameScheduleDraft,
  scheduleDraftToAPI,
  validateScheduleDraft,
  type ScheduleDraft,
} from "./automation-schedule";

describe("automation schedule conversion", () => {
  it.each([
    {
      name: "daily",
      draft: { frequency: "daily", time: "09:00", timezone: "UTC" },
      expected: {
        schedule_type: "cron",
        cron_expression: "0 9 * * *",
        timezone: "UTC",
      },
    },
    {
      name: "weekdays",
      draft: { frequency: "weekdays", time: "09:05", timezone: "UTC" },
      expected: {
        schedule_type: "cron",
        cron_expression: "5 9 * * 1-5",
        timezone: "UTC",
      },
    },
    {
      name: "multiple weekdays in canonical cron order",
      draft: {
        frequency: "weekly",
        weekdays: ["thursday", "monday"],
        time: "09:00",
        timezone: "America/Los_Angeles",
      },
      expected: {
        schedule_type: "cron",
        cron_expression: "0 9 * * 1,4",
        timezone: "America/Los_Angeles",
      },
    },
    {
      name: "monthly",
      draft: {
        frequency: "monthly",
        dayOfMonth: 15,
        time: "09:00",
        timezone: "UTC",
      },
      expected: {
        schedule_type: "cron",
        cron_expression: "0 9 15 * *",
        timezone: "UTC",
      },
    },
    {
      name: "elapsed interval",
      draft: {
        frequency: "interval",
        value: 6,
        unit: "hours",
        timezone: "UTC",
      },
      expected: {
        schedule_type: "interval",
        interval_value: 6,
        interval_unit: "hours",
        interval_run_at: "",
        timezone: "UTC",
      },
    },
  ] as Array<{ name: string; draft: ScheduleDraft; expected: object }>)(
    "converts $name",
    ({ draft, expected }) => {
      expect(scheduleDraftToAPI(draft)).toEqual(expected);
    },
  );

  it.each([
    ["0 9 * * *", "daily"],
    ["5 9 * * 1-5", "weekdays"],
    ["0 9 * * 4,1", "weekly"],
    ["0 9 * * 7", "weekly"],
    ["0 9 31 * *", "monthly"],
  ])("parses %s as %s", (expression, frequency) => {
    expect(parseFriendlyCron(expression, "UTC")?.frequency).toBe(frequency);
  });

  it.each(["@daily", "0 9 1,15 * *", "0 9 * * MON", "0 9 * */2 *"])(
    "conservatively rejects unsupported expression %s",
    (expression) => {
      expect(parseFriendlyCron(expression, "UTC")).toBeNull();
    },
  );

  it("preserves unsupported cron as advanced", () => {
    expect(
      automationToScheduleDraft({
        schedule_type: "cron",
        cron_expression: "0 9 1,15 * *",
        timezone: "UTC",
      }),
    ).toEqual({
      frequency: "advanced",
      cronExpression: "0 9 1,15 * *",
      timezone: "UTC",
    });
  });

  it("always sends interval_run_at so a stored one can be cleared", () => {
    // PATCH treats an absent interval_run_at as "unchanged", so an unanchored
    // draft must send "" rather than omitting the key — otherwise a run-at
    // stored earlier keeps steering the schedule behind the editor's back.
    expect(
      scheduleDraftToAPI({
        frequency: "interval",
        value: 6,
        unit: "hours",
        timezone: "UTC",
      }),
    ).toEqual({
      schedule_type: "interval",
      interval_value: 6,
      interval_unit: "hours",
      interval_run_at: "",
      timezone: "UTC",
    });
  });

  it("does not invent a run time for an unanchored interval", () => {
    expect(
      automationToScheduleDraft({
        schedule_type: "interval",
        interval_value: 3,
        interval_unit: "days",
        timezone: "UTC",
      }),
    ).toEqual({
      frequency: "interval",
      value: 3,
      unit: "days",
      timezone: "UTC",
    });
  });

  it("keeps the automation timezone when the schedule is removed", () => {
    expect(scheduleDraftToAPI(null, "America/Los_Angeles")).toEqual({
      schedule_type: "none",
      timezone: "America/Los_Angeles",
    });
  });

  it("reports sub-day hourly intervals as having no run-at control", () => {
    expect(intervalHasRunAt(6, "hours")).toBe(false);
    expect(intervalHasRunAt(24, "hours")).toBe(true);
    expect(intervalHasRunAt(1, "days")).toBe(true);
    expect(intervalHasRunAt(2, "weeks")).toBe(true);
  });
});

describe("isScheduleDraft", () => {
  it.each([
    { frequency: "daily", time: "09:00", timezone: "UTC" },
    { frequency: "weekdays", time: "09:00", timezone: "UTC" },
    { frequency: "weekly", weekdays: ["monday"], time: "09:00", timezone: "UTC" },
    { frequency: "monthly", dayOfMonth: 15, time: "09:00", timezone: "UTC" },
    { frequency: "interval", value: 6, unit: "hours", timezone: "UTC" },
    { frequency: "interval", value: 3, unit: "days", time: "09:00", timezone: "UTC" },
    { frequency: "advanced", cronExpression: "0 9 * * *", timezone: "UTC" },
  ])("accepts a complete %o", (draft) => {
    expect(isScheduleDraft(draft)).toBe(true);
  });

  it.each([
    // Each of these used to pass the discriminant-only guard and then throw
    // downstream, taking the create page down while rehydrating a draft.
    { frequency: "weekly", timezone: "UTC" },
    { frequency: "advanced", timezone: "UTC" },
    { frequency: "daily", timezone: "UTC" },
    { frequency: "monthly", time: "09:00", timezone: "UTC" },
    { frequency: "interval", unit: "days", timezone: "UTC" },
    { frequency: "interval", value: 1, unit: "fortnights", timezone: "UTC" },
    { frequency: "weekly", weekdays: ["someday"], time: "09:00", timezone: "UTC" },
    { frequency: "weekly", weekdays: "monday", time: "09:00", timezone: "UTC" },
    { frequency: "nonsense", timezone: "UTC" },
    { frequency: "daily", time: "09:00" },
    null,
    "daily",
  ])("rejects the incomplete %o", (draft) => {
    expect(isScheduleDraft(draft)).toBe(false);
  });

  it("only admits drafts the rest of the module can handle", () => {
    const drafts = [
      { frequency: "weekly", timezone: "UTC" },
      { frequency: "advanced", timezone: "UTC" },
    ];
    for (const draft of drafts) {
      expect(isScheduleDraft(draft)).toBe(false);
      // Proves the guard is load-bearing: these throw if they get through.
      expect(() => validateScheduleDraft(draft as never)).toThrow();
    }
  });
});

describe("sameScheduleDraft", () => {
  it("ignores key order and weekday selection order", () => {
    expect(
      sameScheduleDraft(
        { frequency: "weekly", weekdays: ["thursday", "monday"], time: "09:00", timezone: "UTC" },
        { timezone: "UTC", time: "09:00", weekdays: ["monday", "thursday"], frequency: "weekly" } as ScheduleDraft,
      ),
    ).toBe(true);
  });

  it("detects real edits", () => {
    const base: ScheduleDraft = {
      frequency: "weekly",
      weekdays: ["monday"],
      time: "09:00",
      timezone: "UTC",
    };
    expect(sameScheduleDraft(base, { ...base, time: "10:00" })).toBe(false);
    expect(sameScheduleDraft(base, { ...base, timezone: "UTC+1" })).toBe(false);
    expect(sameScheduleDraft(base, { ...base, weekdays: ["monday", "friday"] })).toBe(false);
    expect(sameScheduleDraft(base, null)).toBe(false);
    expect(sameScheduleDraft(null, null)).toBe(true);
  });

  it("treats a missing and a present run time as different", () => {
    const anchored: ScheduleDraft = {
      frequency: "interval",
      value: 3,
      unit: "days",
      time: "09:00",
      timezone: "UTC",
    };
    expect(
      sameScheduleDraft(anchored, {
        frequency: "interval",
        value: 3,
        unit: "days",
        timezone: "UTC",
      }),
    ).toBe(false);
  });
});

describe("formatScheduleSentence", () => {
  it.each([
    [
      {
        frequency: "weekly",
        weekdays: ["thursday", "monday"],
        time: "09:00",
        timezone: "UTC",
      },
      "Every week on Monday and Thursday at 9:00 AM",
    ],
    [
      {
        frequency: "monthly",
        dayOfMonth: 31,
        time: "09:00",
        timezone: "UTC",
      },
      "Every month on the 31st at 9:00 AM",
    ],
    [
      {
        frequency: "interval",
        value: 1,
        unit: "hours",
        timezone: "UTC",
      },
      "Every hour",
    ],
    [
      {
        frequency: "advanced",
        cronExpression: "0 9 1,15 * *",
        timezone: "UTC",
      },
      "Custom schedule: 0 9 1,15 * *",
    ],
  ] as Array<[ScheduleDraft, string]>)("formats a schedule", (draft, expected) => {
    expect(formatScheduleSentence(draft)).toBe(expected);
  });
});

describe("validateScheduleDraft", () => {
  it.each([
    [
      {
        frequency: "weekly",
        weekdays: [],
        time: "09:00",
        timezone: "UTC",
      },
      "Select at least one day.",
    ],
    [
      {
        frequency: "monthly",
        dayOfMonth: 32,
        time: "09:00",
        timezone: "UTC",
      },
      "Day of month must be between 1 and 31.",
    ],
    [
      {
        frequency: "interval",
        value: 0,
        unit: "days",
        timezone: "UTC",
      },
      "Interval must be between 1 and 365.",
    ],
  ] as Array<[ScheduleDraft, string]>)("rejects invalid draft", (draft, expected) => {
    expect(validateScheduleDraft(draft)).toBe(expected);
  });
});
