import { describe, it, expect, vi, afterEach } from "vitest";
import { formatDuration } from "./session-detail-content";

const start = "2026-01-01T00:00:00.000Z";
const plus = (ms: number) => new Date(new Date(start).getTime() + ms).toISOString();

describe("formatDuration", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("returns '-' when startedAt is missing", () => {
    expect(formatDuration(undefined)).toBe("-");
    expect(formatDuration("")).toBe("-");
  });

  it("formats sub-minute durations as seconds", () => {
    expect(formatDuration(start, plus(0))).toBe("0s");
    expect(formatDuration(start, plus(45_000))).toBe("45s");
    expect(formatDuration(start, plus(59_999))).toBe("59s");
  });

  it("formats sub-hour durations as minutes and seconds", () => {
    expect(formatDuration(start, plus(60_000))).toBe("1m 0s");
    expect(formatDuration(start, plus(5 * 60_000 + 17_000))).toBe("5m 17s");
    expect(formatDuration(start, plus(59 * 60_000 + 59_000))).toBe("59m 59s");
  });

  it("formats sub-day durations as hours and minutes", () => {
    expect(formatDuration(start, plus(60 * 60_000))).toBe("1h 0m");
    expect(formatDuration(start, plus(17 * 60 * 60_000 + 57 * 60_000 + 17_000))).toBe("17h 57m");
    expect(formatDuration(start, plus(23 * 60 * 60_000 + 59 * 60_000))).toBe("23h 59m");
  });

  it("formats multi-day durations as days and hours", () => {
    expect(formatDuration(start, plus(24 * 60 * 60_000))).toBe("1d 0h");
    expect(formatDuration(start, plus(3 * 24 * 60 * 60_000 + 5 * 60 * 60_000))).toBe("3d 5h");
    expect(formatDuration(start, plus(45 * 24 * 60 * 60_000 + 23 * 60 * 60_000))).toBe("45d 23h");
  });

  it("uses current time when completedAt is omitted", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(plus(2 * 60 * 60_000 + 30 * 60_000)));
    expect(formatDuration(start)).toBe("2h 30m");
  });
});
