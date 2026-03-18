import { describe, it, expect } from "vitest";
import { formatTimeAgo } from "./utils";

describe("formatTimeAgo", () => {
  it("returns 'just now' for dates less than a minute ago", () => {
    const now = new Date().toISOString();
    expect(formatTimeAgo(now)).toBe("just now");
  });

  it("returns minutes ago", () => {
    const fiveMinAgo = new Date(Date.now() - 5 * 60_000).toISOString();
    expect(formatTimeAgo(fiveMinAgo)).toBe("5m ago");
  });

  it("returns '1m ago' at the minute boundary", () => {
    const oneMinAgo = new Date(Date.now() - 60_000).toISOString();
    expect(formatTimeAgo(oneMinAgo)).toBe("1m ago");
  });

  it("returns hours ago", () => {
    const twoHoursAgo = new Date(Date.now() - 2 * 3_600_000).toISOString();
    expect(formatTimeAgo(twoHoursAgo)).toBe("2h ago");
  });

  it("returns days ago for dates within 30 days", () => {
    const threeDaysAgo = new Date(Date.now() - 3 * 86_400_000).toISOString();
    expect(formatTimeAgo(threeDaysAgo)).toBe("3d ago");
  });

  it("returns a formatted date for dates older than 30 days", () => {
    const fiftyDaysAgo = new Date(Date.now() - 50 * 86_400_000).toISOString();
    const result = formatTimeAgo(fiftyDaysAgo);
    // Should not be "Xd ago" format — should be a locale date string
    expect(result).not.toMatch(/\d+d ago/);
    expect(result.length).toBeGreaterThan(0);
  });

  it("returns '59m ago' just under an hour", () => {
    const fiftyNineMinAgo = new Date(Date.now() - 59 * 60_000).toISOString();
    expect(formatTimeAgo(fiftyNineMinAgo)).toBe("59m ago");
  });

  it("returns '23h ago' just under a day", () => {
    const twentyThreeHoursAgo = new Date(Date.now() - 23 * 3_600_000).toISOString();
    expect(formatTimeAgo(twentyThreeHoursAgo)).toBe("23h ago");
  });
});
