import { describe, expect, it } from "vitest";
import {
  customTimeRange,
  customTimeRangeDates,
  isRollingTimeRange,
  parseTimeRange,
  timeRangeBounds,
} from "./time-range";

describe("time ranges", () => {
  it.each([
    { value: "7d", expected: "7d" },
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
    expect(isRollingTimeRange("all")).toBe(false);
    expect(isRollingTimeRange("custom:2026-07-01:2026-07-31")).toBe(false);
  });
});
