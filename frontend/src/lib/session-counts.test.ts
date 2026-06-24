import { describe, expect, it } from "vitest";

import { getCountForTab, renderCount } from "./session-counts";
import type { SessionCounts } from "./types";

const counts: SessionCounts = {
  all: 99,
  active: 12,
  archived: 4,
  cap: 100,
};

describe("renderCount", () => {
  it("returns undefined when the value or counts are missing", () => {
    expect(renderCount(undefined, counts)).toBeUndefined();
    expect(renderCount(5, undefined)).toBeUndefined();
  });

  it("renders exact counts below the server cap", () => {
    expect(renderCount(12, counts)).toBe("12");
    expect(renderCount(0, counts)).toBe("0");
  });

  it("renders capped counts with a plus suffix", () => {
    expect(renderCount(100, counts)).toBe("99+");
    expect(renderCount(125, counts)).toBe("99+");
  });
});

describe("getCountForTab", () => {
  it("returns undefined when counts are unavailable", () => {
    expect(getCountForTab("all", undefined)).toBeUndefined();
  });

  it("maps known tab values to their count buckets", () => {
    expect(getCountForTab("all", counts)).toBe(99);
    expect(getCountForTab("active", counts)).toBe(12);
    expect(getCountForTab("archived", counts)).toBe(4);
  });

  it("returns undefined for uncounted tabs", () => {
    expect(getCountForTab("mine", counts)).toBeUndefined();
  });
});
