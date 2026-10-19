import { afterEach, describe, expect, it, vi } from "vitest";
import { formatRelativeTime } from "./relative-time";

describe("formatRelativeTime", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  const tests = [
    { name: "less than one minute", elapsedMs: 59_999, expected: "just now" },
    { name: "one minute", elapsedMs: 60_000, expected: "1m ago" },
    { name: "less than one hour", elapsedMs: 59 * 60_000, expected: "59m ago" },
    { name: "one hour", elapsedMs: 60 * 60_000, expected: "1h ago" },
    { name: "less than one day", elapsedMs: 23 * 60 * 60_000, expected: "23h ago" },
    { name: "one day", elapsedMs: 24 * 60 * 60_000, expected: "1d ago" },
  ];

  for (const test of tests) {
    it(`formats ${test.name}`, () => {
      const now = new Date("2026-10-19T16:00:00Z");
      vi.useFakeTimers();
      vi.setSystemTime(now);

      expect(formatRelativeTime(new Date(now.getTime() - test.elapsedMs).toISOString())).toBe(
        test.expected
      );
    });
  }
});
