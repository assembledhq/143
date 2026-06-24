import { describe, expect, it } from "vitest";

import { looksLikeLinearRef } from "./linear-refs";

describe("looksLikeLinearRef", () => {
  it("accepts bare Linear issue identifiers", () => {
    expect(looksLikeLinearRef("ABC-123")).toBe(true);
    expect(looksLikeLinearRef(" ABC_123-456 ")).toBe(true);
  });

  it("accepts Linear issue URLs with optional suffixes", () => {
    expect(looksLikeLinearRef("https://linear.app/acme/issue/ABC-123")).toBe(true);
    expect(looksLikeLinearRef("http://linear.app/acme/issue/ABC-123/comment/xyz")).toBe(true);
    expect(looksLikeLinearRef("https://linear.app/acme/issue/ABC-123?focused=true")).toBe(true);
    expect(looksLikeLinearRef("https://linear.app/acme/issue/ABC-123#activity")).toBe(true);
  });

  it("rejects empty, lowercase, malformed, and non-Linear references", () => {
    expect(looksLikeLinearRef("")).toBe(false);
    expect(looksLikeLinearRef("abc-123")).toBe(false);
    expect(looksLikeLinearRef("ABC")).toBe(false);
    expect(looksLikeLinearRef("https://example.com/acme/issue/ABC-123")).toBe(false);
    expect(looksLikeLinearRef("https://linear.app/acme/project/ABC-123")).toBe(false);
  });
});
