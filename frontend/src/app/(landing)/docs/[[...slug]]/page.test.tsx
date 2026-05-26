import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const currentDir = path.dirname(fileURLToPath(import.meta.url));

describe("PublicDocsPage", () => {
  it("disables the generated previous-next footer", () => {
    const source = fs.readFileSync(path.join(currentDir, "page.tsx"), "utf8");

    expect(source).toContain("footer={{ enabled: false }}");
  });
});
