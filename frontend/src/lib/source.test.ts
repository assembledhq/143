import { describe, expect, it } from "vitest";
import { sidebarLabelForPageTreeFile } from "./docs/sidebar-labels";

describe("docs source", () => {
  it("labels section index pages as overview items in the sidebar", () => {
    const tests = [
      { filePath: "getting-started/index.mdx", currentName: "Get started" },
      { filePath: "guides/index.mdx", currentName: "Guides" },
      { filePath: "self-hosting/index.mdx", currentName: "Self-hosting" },
      { filePath: "reference/index.mdx", currentName: "Reference" },
    ];

    for (const tt of tests) {
      expect(sidebarLabelForPageTreeFile(tt.filePath, tt.currentName)).toBe(
        "Overview"
      );
    }
  });

  it("keeps non-section page labels unchanged", () => {
    expect(sidebarLabelForPageTreeFile("guides/repo-config.mdx", "Repo config")).toBe(
      "Repo config"
    );
    expect(sidebarLabelForPageTreeFile("index.mdx", "143.dev docs")).toBe(
      "143.dev docs"
    );
  });
});
