import { beforeEach, describe, expect, it } from "vitest";
import { liveRefreshInterval } from "./live-refresh-policy";

describe("liveRefreshInterval", () => {
  beforeEach(() => localStorage.clear());

  it("returns stable jitter for the same browser, query, and health state", () => {
    const first = liveRefreshInterval(["sessions"], "list", "healthy", true);
    const second = liveRefreshInterval(["sessions"], "list", "healthy", true);
    expect(second).toBe(first);
    expect(first).toBeGreaterThanOrEqual(120_000);
    expect(first).toBeLessThanOrEqual(300_000);
  });

  it.each([false, true])("suppresses work when visibility is %s and connectivity requires it", (visible) => {
    const result = liveRefreshInterval(["session", "one"], "active-detail", visible ? "offline" : "healthy", visible);
    expect(result).toBe(false);
  });

  it("keeps converging state narrow even while degraded", () => {
    const result = liveRefreshInterval(["automation", "one"], "converging", "degraded", true);
    expect(result).toBeGreaterThanOrEqual(2_000);
    expect(result).toBeLessThanOrEqual(5_000);
  });

  it("widens fallback polling after sustained degradation", () => {
    const detail = liveRefreshInterval(["session", "one"], "active-detail", "degraded-sustained", true);
    const list = liveRefreshInterval(["sessions"], "list", "degraded-sustained", true);
    expect(detail).toBeGreaterThanOrEqual(15_000);
    expect(detail).toBeLessThanOrEqual(30_000);
    expect(list).toBeGreaterThanOrEqual(30_000);
    expect(list).toBeLessThanOrEqual(90_000);
  });
});
