import { describe, expect, it } from "vitest";

import {
  MAX_CONCURRENT_RUNS,
  MIN_CONCURRENT_RUNS,
  clampNumber,
} from "./settings-constants";

describe("clampNumber", () => {
  it("keeps values inside the allowed range", () => {
    expect(clampNumber(5, MIN_CONCURRENT_RUNS, MAX_CONCURRENT_RUNS)).toBe(5);
  });

  it("raises values below the minimum", () => {
    expect(clampNumber(0, MIN_CONCURRENT_RUNS, MAX_CONCURRENT_RUNS)).toBe(1);
  });

  it("lowers values above the maximum", () => {
    expect(clampNumber(30, MIN_CONCURRENT_RUNS, MAX_CONCURRENT_RUNS)).toBe(25);
  });
});
