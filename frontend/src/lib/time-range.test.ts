import { describe, expect, it } from "vitest";
import {
  customTimeRange,
  customTimeRangeDates,
  isRollingTimeRange,
  parseTimeRange,
  timeRangeDisplayDates,
  timeRangeRefreshDelayMs,
  timeRangeBounds,
} from "./time-range";

describe("time ranges", () => {
  it.each([
    { value: "7d", expected: "7d" },
    { value: "this_week", expected: "this_week" },
    { value: "last_week", expected: "last_week" },
    { value: "last_2_weeks", expected: "last_2_weeks" },
    { value: "this_month", expected: "this_month" },
    { value: "last_month", expected: "last_month" },
    { value: "all", expected: "all" },
    { value: "custom:2026-07-01:2026-07-31", expected: "custom:2026-07-01:2026-07-31" },
    { value: "custom:2026-02-30:2026-03-01", expected: null },
    { value: "custom:2026-07-31:2026-07-01", expected: null },
    { value: "14d", expected: null },
  ])("parses $value", ({ value, expected }) => {
    expect(parseTimeRange(value)).toBe(expected);
  });

  it("round-trips a custom local-day range", () => {
    const range = customTimeRange(new Date(2026, 6, 1), new Date(2026, 6, 31));

    expect(range).toBe("custom:2026-07-01:2026-07-31");
    expect(customTimeRangeDates(range)).toEqual({
      from: new Date(2026, 6, 1),
      to: new Date(2026, 6, 31),
    });
  });

  it("creates an inclusive API window for custom dates", () => {
    expect(timeRangeBounds("custom:2026-07-01:2026-07-31", new Date())).toEqual({
      created_after: new Date(2026, 6, 1, 0, 0, 0, 0).toISOString(),
      created_before: new Date(2026, 6, 31, 23, 59, 59, 999).toISOString(),
    });
  });

  it("keeps preset windows rolling from their anchor", () => {
    const anchor = new Date("2026-08-01T12:00:00.000Z");

    expect(timeRangeBounds("7d", anchor)).toEqual({
      created_after: "2026-07-25T12:00:00.000Z",
    });
    expect(isRollingTimeRange("7d")).toBe(true);
    expect(isRollingTimeRange("last_week")).toBe(false);
    expect(isRollingTimeRange("all")).toBe(false);
    expect(isRollingTimeRange("custom:2026-07-01:2026-07-31")).toBe(false);
  });

  it.each([
    {
      range: "7d" as const,
      expected: {
        from: new Date(2026, 6, 25, 12, 0, 0, 0),
        to: new Date(2026, 7, 1, 12, 0, 0, 0),
      },
    },
    {
      range: "last_month" as const,
      expected: {
        from: new Date(2026, 6, 1, 0, 0, 0, 0),
        to: new Date(2026, 6, 31, 23, 59, 59, 999),
      },
    },
    {
      range: "custom:2026-07-01:2026-07-31" as const,
      expected: {
        from: new Date(2026, 6, 1),
        to: new Date(2026, 6, 31),
      },
    },
    { range: "all" as const, expected: null },
  ])("returns calendar display dates for $range", ({ range, expected }) => {
    expect(timeRangeDisplayDates(range, new Date(2026, 7, 1, 12, 0, 0, 0))).toEqual(expected);
  });

  it.each([
    {
      range: "this_week" as const,
      expectedFrom: new Date(2026, 6, 26, 0, 0, 0, 0),
      expectedTo: new Date(2026, 7, 1, 23, 59, 59, 999),
    },
    {
      range: "last_week" as const,
      expectedFrom: new Date(2026, 6, 19, 0, 0, 0, 0),
      expectedTo: new Date(2026, 6, 25, 23, 59, 59, 999),
    },
    {
      range: "last_2_weeks" as const,
      expectedFrom: new Date(2026, 6, 12, 0, 0, 0, 0),
      expectedTo: new Date(2026, 6, 25, 23, 59, 59, 999),
    },
    {
      range: "this_month" as const,
      expectedFrom: new Date(2026, 7, 1, 0, 0, 0, 0),
      expectedTo: new Date(2026, 7, 1, 23, 59, 59, 999),
    },
    {
      range: "last_month" as const,
      expectedFrom: new Date(2026, 6, 1, 0, 0, 0, 0),
      expectedTo: new Date(2026, 6, 31, 23, 59, 59, 999),
    },
  ])("creates inclusive calendar bounds for $range", ({ range, expectedFrom, expectedTo }) => {
    const anchor = new Date(2026, 7, 1, 12, 30, 0, 0);

    expect(timeRangeBounds(range, anchor)).toEqual({
      created_after: expectedFrom.toISOString(),
      created_before: expectedTo.toISOString(),
    });
    expect(isRollingTimeRange(range)).toBe(false);
  });

  it.each([
    { range: "7d" as const, nextRefresh: new Date(2026, 7, 1, 12, 31) },
    { range: "this_week" as const, nextRefresh: new Date(2026, 7, 2, 0, 0) },
    { range: "this_month" as const, nextRefresh: new Date(2026, 7, 2, 0, 0) },
    { range: "last_week" as const, nextRefresh: new Date(2026, 7, 2, 0, 0) },
    { range: "last_2_weeks" as const, nextRefresh: new Date(2026, 7, 2, 0, 0) },
    { range: "last_month" as const, nextRefresh: new Date(2026, 8, 1, 0, 0) },
  ])("schedules the next $range refresh at its relevant boundary", ({ range, nextRefresh }) => {
    const anchor = new Date(2026, 7, 1, 12, 30);

    expect(timeRangeRefreshDelayMs(range, anchor, 60_000)).toBe(nextRefresh.getTime() - anchor.getTime());
  });

  it.each(["all" as const, "custom:2026-07-01:2026-07-31" as const])(
    "does not schedule refreshes for %s",
    (range) => {
      expect(timeRangeRefreshDelayMs(range, new Date(2026, 7, 1, 12, 30), 60_000)).toBeNull();
    },
  );
});
