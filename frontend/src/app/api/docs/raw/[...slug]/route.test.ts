import { describe, expect, it } from "vitest";
import { getRawDocsStaticParams } from "@/lib/docs/raw-docs-route";

describe("raw docs route", () => {
  it("generates only paths that match the required slug catch-all route", () => {
    const params = getRawDocsStaticParams([
      { slugs: [] },
      { slugs: ["guides", "repo-config"] },
    ]);

    expect(
      params.every((param) => param.slug.length > 0)
    ).toBe(true);
  });
});
