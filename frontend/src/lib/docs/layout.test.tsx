import { describe, expect, it } from "vitest";
import { docsLayoutOptions } from "./layout";

describe("docsLayoutOptions", () => {
  it("keeps section shortcuts out of the sidebar menu", () => {
    const links = docsLayoutOptions().links ?? [];
    const sectionUrls = new Set([
      "/docs/getting-started",
      "/docs/guides",
      "/docs/self-hosting",
      "/docs/reference",
    ]);

    const sectionLinks = links.filter(
      (link) => "url" in link && link.url && sectionUrls.has(link.url)
    );

    expect(
      sectionLinks.every((link) => "on" in link && link.on === "nav")
    ).toBe(true);
  });
});
