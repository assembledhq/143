import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const currentDir = path.dirname(fileURLToPath(import.meta.url));

describe("PublicDocsLayout", () => {
  it("disables generated top tabs so they cannot cover the docs body", () => {
    const source = fs.readFileSync(path.join(currentDir, "layout.tsx"), "utf8");

    expect(source).toContain("tabs={false}");
    expect(source).not.toContain('tabMode="top"');
  });

  it("keeps the desktop docs sidebar compact", () => {
    const source = fs.readFileSync(path.join(currentDir, "layout.tsx"), "utf8");
    const css = fs.readFileSync(path.join(currentDir, "../../globals.css"), "utf8");

    expect(source).toContain("docs-fumadocs-layout");
    expect(css).toContain("#nd-docs-layout.docs-fumadocs-layout");
    expect(css).toContain("--fd-sidebar-width: 244px");
  });

  it("keeps focused sidebar links away from the scroll viewport clip edge", () => {
    const source = fs.readFileSync(path.join(currentDir, "../../globals.css"), "utf8");

    expect(source).toContain("#nd-sidebar [data-radix-scroll-area-viewport]");
    expect(source).toContain("padding-inline: 1.125rem");
  });
});
