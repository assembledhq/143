import { describe, it, expect } from "vitest";
import { fillGaps } from "./automation-stats-card";
import type { AutomationRunStatsBucket } from "@/lib/types";

function bucket(
  iso: string,
  overrides: Partial<AutomationRunStatsBucket> = {},
): AutomationRunStatsBucket {
  return {
    bucket: iso,
    total: 0,
    completed: 0,
    completed_noop: 0,
    failed: 0,
    skipped: 0,
    running: 0,
    pending: 0,
    avg_duration_seconds: 0,
    ...overrides,
  };
}

describe("fillGaps", () => {
  it("aligns UTC-midnight bucket ISO strings with filled days", () => {
    const since = new Date("2026-04-15T00:00:00.000Z");
    const until = new Date("2026-04-18T00:00:00.000Z");
    const buckets = [
      bucket("2026-04-15T00:00:00Z", { completed: 3, total: 3 }),
      bucket("2026-04-17T00:00:00Z", { failed: 2, total: 2 }),
    ];

    const out = fillGaps(buckets, since, until);

    expect(out).toHaveLength(3);
    expect(out[0]).toMatchObject({ day: "2026-04-15", completed: 3, failed: 0 });
    expect(out[1]).toMatchObject({ day: "2026-04-16", completed: 0, failed: 0 });
    expect(out[2]).toMatchObject({ day: "2026-04-17", completed: 0, failed: 2 });
  });

  it("matches buckets even when the backend emits a fractional-second ISO", () => {
    const since = new Date("2026-04-15T00:00:00.000Z");
    const until = new Date("2026-04-16T00:00:00.000Z");
    const buckets = [
      bucket("2026-04-15T00:00:00.000000+00:00", { completed: 1, total: 1 }),
    ];

    const out = fillGaps(buckets, since, until);

    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({ day: "2026-04-15", completed: 1 });
  });

  it("treats `until` as an exclusive upper bound", () => {
    const since = new Date("2026-04-15T00:00:00.000Z");
    const until = new Date("2026-04-16T00:00:00.000Z");

    const out = fillGaps([], since, until);

    expect(out).toHaveLength(1);
    expect(out[0].day).toBe("2026-04-15");
  });
});
