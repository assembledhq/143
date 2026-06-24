import { describe, expect, it } from "vitest";

import { sidebarLabelForPageTreeFile } from "./sidebar-labels";

describe("sidebarLabelForPageTreeFile", () => {
  it("keeps the current label without a file path", () => {
    expect(sidebarLabelForPageTreeFile(undefined, "Docs")).toBe("Docs");
  });

  it("renames section index pages to Overview", () => {
    expect(sidebarLabelForPageTreeFile("guides/index.mdx", "Guides")).toBe(
      "Overview",
    );
  });

  it("normalizes Windows path separators before matching", () => {
    expect(sidebarLabelForPageTreeFile("reference\\index.mdx", "Reference")).toBe(
      "Overview",
    );
  });

  it("keeps normal document labels", () => {
    expect(sidebarLabelForPageTreeFile("guides/autopilot.mdx", "Autopilot")).toBe(
      "Autopilot",
    );
  });
});
