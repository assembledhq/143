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
});
