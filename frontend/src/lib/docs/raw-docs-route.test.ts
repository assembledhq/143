import { describe, expect, it } from "vitest";

import { getRawDocsStaticParams } from "./raw-docs-route";

describe("getRawDocsStaticParams", () => {
  it("drops root docs pages and maps non-empty slugs", () => {
    expect(
      getRawDocsStaticParams([
        { slugs: [] },
        { slugs: ["guides"] },
        { slugs: ["guides", "autopilot"] },
      ]),
    ).toEqual([
      { slug: ["guides"] },
      { slug: ["guides", "autopilot"] },
    ]);
  });
});
